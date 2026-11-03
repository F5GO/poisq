// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.0 Flash / 1.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл gemini_client.go реализует отказоустойчивый клиент к Google Gemini API
// (модель gemini-2.0-flash с автоматическим фоллбэком на gemini-1.5-flash) с ротацией ключей из пула keypool,
// обработкой временной перегрузки Google (HTTP 503 high demand), мгновенным переключением при 429 и отработкой квот.
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
)

const (
	defaultGeminiModel = "gemini-2.0-flash"
	geminiAPIBase      = "https://generativelanguage.googleapis.com/v1beta/models"
)

type geminiGenerateRequest struct {
	SystemInstruction *geminiContent       `json:"systemInstruction,omitempty"`
	Contents          []geminiContent      `json:"contents"`
	GenerationConfig  *geminiGenConfig     `json:"generationConfig,omitempty"`
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
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// GeminiClient выполняет запросы к Gemini 2.5 Flash с автоматической ротацией ключей.
type GeminiClient struct {
	keyPool *keypool.Manager
	client  *http.Client
	model   string
}

// NewGeminiClient создает новый экземпляр клиента Gemini.
func NewGeminiClient(keyPool *keypool.Manager) *GeminiClient {
	model := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if model == "" || model == "gemini-3.8-flash" {
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
// При ошибке 429 или исчерпании дневной квоты автоматически помечает текущий ключ и переключается на следующий.
func (g *GeminiClient) GenerateText(ctx context.Context, systemPrompt, userPrompt, responseMimeType string) (string, error) {
	const maxKeySwitches = 5

	var lastErr error
	for attempt := 1; attempt <= maxKeySwitches; attempt++ {
		leased, err := g.keyPool.Acquire("gemini")
		if err != nil {
			if lastErr != nil {
				return "", fmt.Errorf("%w (последняя ошибка API: %v)", err, lastErr)
			}
			return "", err
		}

		resText, status, apiStatus, err := g.callOnce(ctx, leased.PlainKey, systemPrompt, userPrompt, responseMimeType)
		if err == nil {
			g.keyPool.ReportSuccess(leased.ID)
			return resText, nil
		}

		lastErr = err
		errLower := strings.ToLower(err.Error() + " " + apiStatus)

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
			time.Sleep(1 * time.Second)
			continue // Мгновенно переключаемся на следующий ключ в пуле!
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
	// Список реальных production моделей Google Gemini API для последовательного фоллбэка
	initialModel := g.model
	if initialModel == "" || initialModel == "gemini-2.5-flash" || initialModel == "gemini-3.8-flash" {
		initialModel = defaultGeminiModel
	}
	modelsToTry := []string{initialModel}
	candidates := []string{"gemini-2.0-flash", "gemini-1.5-flash", "gemini-2.0-flash-lite", "gemini-1.5-pro"}
	for _, c := range candidates {
		if c != initialModel {
			modelsToTry = append(modelsToTry, c)
		}
	}

	var lastStatus int
	var lastApiStatus string
	var lastErr error

	for _, m := range modelsToTry {
		// Попытка вызова модели с возможностью 1 быстрого повтора при 503 (кратковременный всплеск нагрузки в Google)
		for retry := 0; retry < 2; retry++ {
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

			errLower := strings.ToLower(err.Error() + " " + apiStatus)
			isTransient := status == http.StatusServiceUnavailable ||
				strings.Contains(errLower, "high demand") ||
				strings.Contains(errLower, "unavailable") ||
				strings.Contains(errLower, "timeout") ||
				strings.Contains(errLower, "deadline exceeded")

			// Если это 503/таймаут и это первая попытка — делаем короткую паузу 1 сек и повторяем на этой же модели
			if isTransient && retry == 0 {
				time.Sleep(1 * time.Second)
				continue
			}

			// Если это не 503 и не 404 (например 429 квота или 401/403) — сразу выходим для ротации ключа
			if !isTransient && status != http.StatusNotFound {
				return "", lastStatus, lastApiStatus, lastErr
			}

			// Если повтор на текущей модели не удался или статус 404 — переходим к следующей модели (например с 2.0 на 1.5)
			break
		}
	}

	return "", lastStatus, lastApiStatus, lastErr
}

func (g *GeminiClient) callSingleModel(ctx context.Context, modelName, apiKey, systemPrompt, userPrompt, responseMimeType string) (string, int, string, error) {
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
		return "", 0, "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", resp.StatusCode, "", err
	}

	var parsed geminiGenerateResponse
	_ = json.Unmarshal(respBytes, &parsed)

	if resp.StatusCode != http.StatusOK || parsed.Error != nil {
		apiMsg := string(respBytes)
		apiStatus := ""
		if parsed.Error != nil {
			apiMsg = parsed.Error.Message
			apiStatus = parsed.Error.Status
		}
		return "", resp.StatusCode, apiStatus, fmt.Errorf("Gemini HTTP %d (%s): %s", resp.StatusCode, apiStatus, apiMsg)
	}

	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", resp.StatusCode, "", fmt.Errorf("Gemini вернул пустой ответ без текста")
	}

	return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text), http.StatusOK, "OK", nil
}
