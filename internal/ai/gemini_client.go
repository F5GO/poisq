// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл gemini_client.go реализует высокопроизводительный клиент к Google Gemini API
// на базе официального Interactions API (/v1beta/interactions) с оптимальной моделью по умолчанию
// gemini-3.8-flash (и каскадным фоллбэком на gemini-3.6-flash / gemini-4-argon), ротацией ключей из пула keypool,
// автоматической обработкой пиковых нагрузок (HTTP 503 high demand) и квот.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
)

const (
	// defaultGeminiModel — оптимальная быстрая и экономичная модель Google Gemini для караоке и консенсуса текстов.
	defaultGeminiModel    = "gemini-3.8-flash"
	geminiInteractionsURL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	geminiAPIBase         = "https://generativelanguage.googleapis.com/v1beta/models"
	geminiAPIVersionDate  = "2026-05-20"
)

// geminiInteractionRequest представляет запрос к официальному Interactions API Google.
type geminiInteractionRequest struct {
	Model             string                `json:"model"`
	Input             string                `json:"input"`
	SystemInstruction string                `json:"system_instruction,omitempty"`
	Store             bool                  `json:"store"`
	ResponseFormat    *geminiResponseFormat `json:"response_format,omitempty"`
}

type geminiResponseFormat struct {
	Type     string `json:"type,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type geminiInteractionResponse struct {
	ID     string                  `json:"id"`
	Status string                  `json:"status"`
	Steps  []geminiInteractionStep `json:"steps"`
	Error  *geminiAPIError         `json:"error,omitempty"`
}

type geminiInteractionStep struct {
	Type    string                         `json:"type"`
	Content []geminiInteractionContentPart `json:"content,omitempty"`
}

type geminiInteractionContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type geminiAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// Устаревший формат generateContent (для совместимости со старыми моделями)
type geminiGenerateRequest struct {
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature      float64 `json:"temperature"`
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
}

type geminiGenerateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *geminiAPIError `json:"error,omitempty"`
}

// GeminiClient выполняет запросы к Gemini с автоматической ротацией ключей.
type GeminiClient struct {
	keyPool *keypool.Manager
	client  *http.Client
	model   string
}

// NewGeminiClient создает новый экземпляр клиента Gemini с оптимальной моделью gemini-3.8-flash.
func NewGeminiClient(keyPool *keypool.Manager) *GeminiClient {
	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if model == "" || model == "gemini-2.0-flash" || model == "gemini-2.5-flash" {
		model = defaultGeminiModel
	}
	return &GeminiClient{
		keyPool: keyPool,
		model:   model,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

// GenerateText выполняет запрос к Gemini с системной инструкцией и пользовательским промптом.
// При ошибке 503 (High Demand) выполняет серию аккуратных повторов с экспоненциальной паузой прямо на месте,
// не сбрасывая выполнение текущей задачи воркера.
func (g *GeminiClient) GenerateText(ctx context.Context, systemPrompt, userPrompt, responseMimeType string) (string, error) {
	const maxAttempts = 8

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		leased, err := g.keyPool.Acquire("gemini")
		if err != nil {
			// Если все ключи в кратковременном 503-кулдауне (до 20 сек) — ждем освобождения прямо внутри этапа Gemini, не прерывая задачу!
			nextTs := g.keyPool.NextRetryTimestamp("gemini")
			waitSec := nextTs - time.Now().Unix()
			if waitSec > 0 && waitSec <= 25 && attempt < maxAttempts {
				sleepDur := time.Duration(waitSec+1) * time.Second
				select {
				case <-time.After(sleepDur):
					leased, err = g.keyPool.Acquire("gemini")
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			if err != nil {
				if lastErr != nil {
					return "", fmt.Errorf("%w (последняя ошибка API: %v)", err, lastErr)
				}
				return "", err
			}
		}

		started := time.Now()
		resText, status, apiStatus, err := g.callOnce(ctx, leased.PlainKey, systemPrompt, userPrompt, responseMimeType)
		if err == nil {
			g.keyPool.ReportSuccess(leased.ID)
			logger.Info("[GEMINI] Успех: попытка %d/%d, ключ #%d, модель %s, %s, промпт %d симв., ответ %d симв.",
				attempt, maxAttempts, leased.ID, g.model, time.Since(started).Round(time.Millisecond), len(userPrompt), len(resText))
			return resText, nil
		}

		lastErr = err
		errLower := strings.ToLower(err.Error() + " " + apiStatus)
		logger.Warn("[GEMINI] Попытка %d/%d не удалась (ключ #%d, HTTP %d, %s, %s): %v",
			attempt, maxAttempts, leased.ID, status, apiStatus, time.Since(started).Round(time.Millisecond), err)

		// Классификация ошибки для пула ключей
		if status == http.StatusTooManyRequests || strings.Contains(errLower, "resource_exhausted") || strings.Contains(errLower, "quota") {
			if strings.Contains(errLower, "perday") || strings.Contains(errLower, "daily") || strings.Contains(errLower, "quota exceeded for quota metric") {
				g.keyPool.ReportDailyQuotaExhausted(leased.ID, err.Error())
			} else {
				g.keyPool.ReportRateLimit429(leased.ID, err.Error())
			}
			continue // Мгновенно переключаемся на следующий активный ключ!
		}

		// Временная перегрузка или недоступность Google (HTTP 503 UNAVAILABLE / 502 / 504) или сетевой таймаут
		if status == http.StatusServiceUnavailable || status == http.StatusBadGateway || status == http.StatusGatewayTimeout ||
			strings.Contains(errLower, "high demand") || strings.Contains(errLower, "unavailable") ||
			strings.Contains(errLower, "timeout") || strings.Contains(errLower, "deadline exceeded") {
			g.keyPool.ReportUnavailable503(leased.ID, err.Error())
			// Аккуратная прогрессивная пауза (2с, 4с, 6с...) прямо внутри этапа без перезапуска задачи с нуля
			backoff := time.Duration(attempt*2) * time.Second
			if backoff > 12*time.Second {
				backoff = 12 * time.Second
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			continue
		}

		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			if strings.Contains(errLower, "api_key_invalid") || strings.Contains(errLower, "key not valid") || status != http.StatusBadRequest {
				g.keyPool.ReportInvalid(leased.ID, status, err.Error())
				continue
			}
		}

		// Любая другая непредвиденная ошибка API также фиксируется в статистике ошибок ключа
		g.keyPool.ReportUnavailable503(leased.ID, err.Error())
		return "", err
	}

	return "", lastErr
}

func (g *GeminiClient) callOnce(ctx context.Context, apiKey, systemPrompt, userPrompt, responseMimeType string) (string, int, string, error) {
	// Фоллбэк только между оптимальными быстрыми моделями: gemini-3.8-flash -> gemini-3.6-flash
	initialModel := g.model
	if initialModel == "" || initialModel == "gemini-2.0-flash" || initialModel == "gemini-2.5-flash" {
		initialModel = defaultGeminiModel
	}
	modelsToTry := []string{initialModel}
	candidates := []string{"gemini-3.8-flash", "gemini-3.6-flash"}
	for _, c := range candidates {
		if c != initialModel {
			modelsToTry = append(modelsToTry, c)
		}
	}

	var lastStatus int
	var lastApiStatus string
	var lastErr error

	for _, m := range modelsToTry {
		// До 3 попыток на каждой модели при кратковременной перегрузке 503 (High Demand) с паузами 2с и 4с
		for retry := 0; retry < 3; retry++ {
			if ctx.Err() != nil {
				return "", 0, "", ctx.Err()
			}
			resText, status, apiStatus, err := g.callSingleModel(ctx, m, apiKey, systemPrompt, userPrompt, responseMimeType)
			if err == nil {
				if m != g.model {
					g.model = m // Автоматически фиксируем успешно ответившую модель
				}
				return resText, status, apiStatus, nil
			}

			lastStatus = status
			lastApiStatus = apiStatus
			lastErr = err
			logger.Debug("[GEMINI] Модель %s, повтор %d: HTTP %d (%s): %v", m, retry+1, status, apiStatus, err)

			errLower := strings.ToLower(err.Error() + " " + apiStatus)
			isTransient := status == http.StatusServiceUnavailable ||
				strings.Contains(errLower, "high demand") ||
				strings.Contains(errLower, "unavailable") ||
				strings.Contains(errLower, "timeout") ||
				strings.Contains(errLower, "deadline exceeded")

			// Если это 503/таймаут — делаем паузу (2с, 4с) и повторяем запрос к этой же модели
			if isTransient && retry < 2 {
				sleepDur := time.Duration((retry+1)*2) * time.Second
				select {
				case <-time.After(sleepDur):
				case <-ctx.Done():
					return "", lastStatus, lastApiStatus, ctx.Err()
				}
				continue
			}

			// Если это не 503 и не 404 (например 429 квота или 401/403) — сразу выходим для ротации ключа
			if !isTransient && status != http.StatusNotFound {
				return "", lastStatus, lastApiStatus, lastErr
			}

			// Если повторы на текущей модели не удались или статус 404 — переходим к следующей модели (3.6-flash)
			logger.Warn("[GEMINI] Модель %s недоступна (HTTP %d): переход к следующей модели из списка", m, status)
			break
		}
	}

	return "", lastStatus, lastApiStatus, lastErr
}

// callSingleModel выполняет запрос к модели: в первую очередь через официальный Interactions API,
// а при необходимости прозрачно поддерживает фоллбэк на legacy generateContent.
func (g *GeminiClient) callSingleModel(ctx context.Context, modelName, apiKey, systemPrompt, userPrompt, responseMimeType string) (string, int, string, error) {
	// 1. Попытка вызова через официальный Interactions API Google
	resText, status, apiStatus, err := g.callInteractionsAPI(ctx, modelName, apiKey, systemPrompt, userPrompt, responseMimeType)
	if err == nil {
		return resText, status, apiStatus, nil
	}

	// Если Interactions API вернул 404 или endpoint недоступен — пробуем legacy generateContent
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		return g.callGenerateContent(ctx, modelName, apiKey, systemPrompt, userPrompt, responseMimeType)
	}

	return "", status, apiStatus, err
}

// callInteractionsAPI отправляет запрос к официальному Interactions API Google (/v1beta/interactions).
func (g *GeminiClient) callInteractionsAPI(ctx context.Context, modelName, apiKey, systemPrompt, userPrompt, responseMimeType string) (string, int, string, error) {
	reqPayload := geminiInteractionRequest{
		Model:             modelName,
		Input:             userPrompt,
		SystemInstruction: systemPrompt,
		Store:             false,
	}
	if responseMimeType != "" {
		reqPayload.ResponseFormat = &geminiResponseFormat{
			Type:     "text",
			MimeType: responseMimeType,
		}
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiInteractionsURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Api-Revision", geminiAPIVersionDate)

	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, "", ctx.Err()
		}
		return "", 0, "", fmt.Errorf("Gemini Interactions request failed")
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", resp.StatusCode, "", err
	}

	if resp.StatusCode != http.StatusOK {
		var parsedErr geminiInteractionResponse
		_ = json.Unmarshal(respBytes, &parsedErr)
		apiStatus := ""
		if parsedErr.Error != nil {
			apiStatus = parsedErr.Error.Status
		}
		return "", resp.StatusCode, apiStatus, fmt.Errorf("Gemini Interactions HTTP %d (%s)", resp.StatusCode, apiStatus)
	}

	resText, apiStatus, err := parseInteractionResponse(respBytes)
	if err != nil {
		return "", resp.StatusCode, apiStatus, err
	}

	return resText, http.StatusOK, "OK", nil
}

// callGenerateContent отправляет запрос к устаревшему generateContent эндпоинту Google.
func (g *GeminiClient) callGenerateContent(ctx context.Context, modelName, apiKey, systemPrompt, userPrompt, responseMimeType string) (string, int, string, error) {
	reqPayload := geminiGenerateRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: userPrompt}}},
		},
		GenerationConfig: &geminiGenConfig{
			Temperature:      0.1,
			ResponseMimeType: responseMimeType,
		},
	}
	if strings.TrimSpace(systemPrompt) != "" {
		reqPayload.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		}
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", 0, "", err
	}

	endpoint := fmt.Sprintf("%s/%s:generateContent?key=%s", geminiAPIBase, modelName, apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, "", ctx.Err()
		}
		return "", 0, "", fmt.Errorf("Gemini generateContent request failed")
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", resp.StatusCode, "", err
	}

	var parsed geminiGenerateResponse
	_ = json.Unmarshal(respBytes, &parsed)

	if resp.StatusCode != http.StatusOK || parsed.Error != nil {
		apiStatus := ""
		if parsed.Error != nil {
			apiStatus = parsed.Error.Status
		}
		return "", resp.StatusCode, apiStatus, fmt.Errorf("Gemini generateContent HTTP %d (%s)", resp.StatusCode, apiStatus)
	}

	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", resp.StatusCode, "", fmt.Errorf("Gemini вернул пустой ответ без текста")
	}

	return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text), http.StatusOK, "OK", nil
}
