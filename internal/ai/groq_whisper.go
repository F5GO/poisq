// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл groq_whisper.go реализует клиент к Groq Cloud Audio Transcriptions API
// (модель whisper-large-v3-turbo) с параметрами timestamp_granularities=["word", "segment"]
// и автоматической ротацией ключей из пула keypool.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
)

const (
	groqWhisperEndpoint      = "https://api.groq.com/openai/v1/audio/transcriptions"
	groqWhisperPrimaryModel  = "whisper-large-v3"
	groqWhisperFallbackModel = "whisper-large-v3-turbo"
)

// WhisperWord содержит таймкод отдельного распознанного слова.
type WhisperWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// WhisperSegment содержит таймкод отдельного акустического сегмента (фразы).
type WhisperSegment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// WhisperTranscriptionResult представляет структурированный ответ от Groq Whisper.
type WhisperTranscriptionResult struct {
	Language string           `json:"language"`
	Duration float64          `json:"duration"`
	Text     string           `json:"text"`
	Words    []WhisperWord    `json:"words"`
	Segments []WhisperSegment `json:"segments"`
}

// GroqWhisperClient управляет отправкой сжатого аудио в Groq API.
type GroqWhisperClient struct {
	keyPool *keypool.Manager
	client  *http.Client
}

// NewGroqWhisperClient создает новый экземпляр клиента Groq Whisper.
func NewGroqWhisperClient(keyPool *keypool.Manager) *GroqWhisperClient {
	return &GroqWhisperClient{
		keyPool: keyPool,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

// TranscribeAudio отправляет Ogg Opus буфер в Groq (whisper-large-v3 с фоллбэком на whisper-large-v3-turbo)
// и возвращает пословные и посегментные DTW-таймкоды.
// ВАЖНО: В Whisper поле prompt является контекстом предыдущего аудио-окна (prev_tokens).
// Передача туда строк самой песни заставляет декодер считать, что первый куплет уже прозвучал до 00:00,
// поэтому в prompt передается только нейтральный заголовок метаданных (Артист — Название) без строк песни.
func (gw *GroqWhisperClient) TranscribeAudio(ctx context.Context, opusData []byte, langHint string, promptHint ...string) (*WhisperTranscriptionResult, error) {
	const maxKeySwitches = 5

	hintText := ""
	if len(promptHint) > 0 {
		rawHint := strings.TrimSpace(promptHint[0])
		// Берем только первую строку (Артист — Название), категорически исключая строки текста песни,
		// чтобы Whisper не посчитал первый куплет уже прозвучавшим в предыдущем 30-сек окне!
		if idx := strings.IndexByte(rawHint, '\n'); idx != -1 {
			rawHint = strings.TrimSpace(rawHint[:idx])
		}
		runes := []rune(rawHint)
		if len(runes) > 90 {
			rawHint = string(runes[:90])
		}
		if rawHint != "" {
			hintText = rawHint
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxKeySwitches; attempt++ {
		leased, err := gw.keyPool.Acquire("groq")
		if err != nil {
			if lastErr != nil {
				return nil, fmt.Errorf("%w (последняя ошибка Groq: %v)", err, lastErr)
			}
			return nil, err
		}

		logger.Info("[GROQ] Попытка %d/%d: ключ #%d, аудио %.2f МБ, язык=%q, prompt=%q",
			attempt, maxKeySwitches, leased.ID, float64(len(opusData))/1048576, langHint, hintText)
		started := time.Now()
		res, status, err := gw.callWithModelFallback(ctx, leased.PlainKey, opusData, langHint, hintText)
		if err == nil {
			gw.keyPool.ReportSuccess(leased.ID)
			logger.Info("[GROQ] Успех за %s: язык=%s, длительность=%.1f сек, слов=%d, сегментов=%d",
				time.Since(started).Round(time.Millisecond), res.Language, res.Duration, len(res.Words), len(res.Segments))
			if len(res.Words) == 0 {
				logger.Warn("[GROQ] Whisper не вернул пословные таймкоды (words пуст): будет использован откат на сегменты")
			}
			return res, nil
		}

		lastErr = err
		errLower := strings.ToLower(err.Error())
		logger.Warn("[GROQ] Попытка %d/%d не удалась через %s (HTTP %d): %v",
			attempt, maxKeySwitches, time.Since(started).Round(time.Millisecond), status, err)

		if status == http.StatusTooManyRequests || strings.Contains(errLower, "rate_limit") {
			if strings.Contains(errLower, "tokens per day") || strings.Contains(errLower, "requests per day") || strings.Contains(errLower, "daily") {
				gw.keyPool.ReportDailyQuotaExhausted(leased.ID, err.Error())
			} else {
				gw.keyPool.ReportRateLimit429(leased.ID, err.Error())
			}
			continue
		}

		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			gw.keyPool.ReportInvalid(leased.ID, status, err.Error())
			continue
		}

		return nil, err
	}

	return nil, lastErr
}

func (gw *GroqWhisperClient) callWithModelFallback(ctx context.Context, apiKey string, opusData []byte, langHint string, promptHint string) (*WhisperTranscriptionResult, int, error) {
	// В первую очередь вызываем полную 32-слойную модель whisper-large-v3 (максимальная точность DTW на музыке)
	res, status, err := gw.callOnce(ctx, groqWhisperPrimaryModel, apiKey, opusData, langHint, promptHint)
	if err == nil {
		return res, status, nil
	}
	// При недоступности полной модели или 503 переключаемся на whisper-large-v3-turbo
	if status == http.StatusNotFound || status == http.StatusServiceUnavailable || status == http.StatusBadRequest {
		logger.Warn("[GROQ] Модель %s вернула HTTP %d (%v): переключаюсь на %s", groqWhisperPrimaryModel, status, err, groqWhisperFallbackModel)
		return gw.callOnce(ctx, groqWhisperFallbackModel, apiKey, opusData, langHint, promptHint)
	}
	return nil, status, err
}

func (gw *GroqWhisperClient) callOnce(ctx context.Context, modelName string, apiKey string, opusData []byte, langHint string, promptHint string) (*WhisperTranscriptionResult, int, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "track.ogg")
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(opusData); err != nil {
		return nil, 0, err
	}

	_ = writer.WriteField("model", modelName)
	_ = writer.WriteField("response_format", "verbose_json")
	_ = writer.WriteField("temperature", "0")
	_ = writer.WriteField("timestamp_granularities[]", "word")
	_ = writer.WriteField("timestamp_granularities[]", "segment")
	if len(langHint) == 2 {
		_ = writer.WriteField("language", strings.ToLower(langHint))
	}
	if promptHint != "" {
		_ = writer.WriteField("prompt", promptHint)
	}

	if err := writer.Close(); err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqWhisperEndpoint, &body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := gw.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("Groq HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var parsed WhisperTranscriptionResult
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("ошибка декодирования ответа Groq Whisper: %w", err)
	}

	return &parsed, http.StatusOK, nil
}
