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
)

const (
	groqWhisperEndpoint = "https://api.groq.com/openai/v1/audio/transcriptions"
	groqWhisperModel    = "whisper-large-v3-turbo"
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

// TranscribeAudio отправляет Ogg Opus буфер в Groq (whisper-large-v3-turbo) и возвращает пословные и посегментные таймкоды.
// Опциональный параметр promptHint передает первые строки текста песни для предотвращения галлюцинаций на музыке.
func (gw *GroqWhisperClient) TranscribeAudio(ctx context.Context, opusData []byte, langHint string, promptHint ...string) (*WhisperTranscriptionResult, error) {
	const maxKeySwitches = 5

	hintText := ""
	if len(promptHint) > 0 {
		hintText = strings.TrimSpace(promptHint[0])
		// Ограничиваем длину подсказки по стандарту Whisper (~224 токена / 500 рун)
		runes := []rune(hintText)
		if len(runes) > 500 {
			hintText = string(runes[:500])
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

		res, status, err := gw.callOnce(ctx, leased.PlainKey, opusData, langHint, hintText)
		if err == nil {
			gw.keyPool.ReportSuccess(leased.ID)
			return res, nil
		}

		lastErr = err
		errLower := strings.ToLower(err.Error())

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

func (gw *GroqWhisperClient) callOnce(ctx context.Context, apiKey string, opusData []byte, langHint string, promptHint string) (*WhisperTranscriptionResult, int, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "track.ogg")
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(opusData); err != nil {
		return nil, 0, err
	}

	_ = writer.WriteField("model", groqWhisperModel)
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
