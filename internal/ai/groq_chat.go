// Package ai реализует облачные AI-пайплайны сервиса poisq.
//
// Данный файл groq_chat.go реализует клиент текстовой LLM Groq (OpenAI-совместимый /chat/completions)
// с ротацией ключей из того же пула "groq", что и Whisper. Используется как быстрая замена Gemini
// для коротких задач: варианты написания артистов, арбитраж альбомов, очистка текстов.
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

const groqChatEndpoint = "https://api.groq.com/openai/v1/chat/completions"

// Модели Groq по умолчанию: основная и запасная. Переопределяются GROQ_LLM_MODEL / GROQ_LLM_FALLBACK_MODEL.
const (
	defaultGroqChatModel         = "llama-3.3-70b-versatile"
	defaultGroqChatFallbackModel = "llama-3.1-8b-instant"
)

// GroqChatClient — клиент текстовой модели Groq.
type GroqChatClient struct {
	keyPool       *keypool.Manager
	client        *http.Client
	model         string
	fallbackModel string
}

// NewGroqChatClient создаёт клиент. Ключи берутся из пула провайдера "groq".
func NewGroqChatClient(keyPool *keypool.Manager) *GroqChatClient {
	model := strings.TrimSpace(os.Getenv("GROQ_LLM_MODEL"))
	if model == "" {
		model = defaultGroqChatModel
	}
	fb := strings.TrimSpace(os.Getenv("GROQ_LLM_FALLBACK_MODEL"))
	if fb == "" {
		fb = defaultGroqChatFallbackModel
	}
	return &GroqChatClient{
		keyPool:       keyPool,
		client:        &http.Client{Timeout: 30 * time.Second},
		model:         model,
		fallbackModel: fb,
	}
}

type groqChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqChatRequest struct {
	Model          string            `json:"model"`
	Messages       []groqChatMessage `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
}

type groqChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// GenerateText реализует TextGenerator.
func (g *GroqChatClient) GenerateText(ctx context.Context, systemPrompt, userPrompt, responseMimeType string) (string, error) {
	const maxKeySwitches = 4
	var lastErr error

	for attempt := 1; attempt <= maxKeySwitches; attempt++ {
		leased, err := g.keyPool.Acquire("groq")
		if err != nil {
			if lastErr != nil {
				return "", fmt.Errorf("%w (последняя ошибка Groq: %v)", err, lastErr)
			}
			return "", err
		}

		started := time.Now()
		out, status, err := g.callModels(ctx, leased.PlainKey, systemPrompt, userPrompt, responseMimeType)
		if err == nil {
			g.keyPool.ReportSuccess(leased.ID)
			logger.Debug("[GROQ-LLM] Ответ за %s (ключ #%d)", time.Since(started).Round(time.Millisecond), leased.ID)
			return out, nil
		}
		lastErr = err
		low := strings.ToLower(err.Error())
		logger.Warn("[GROQ-LLM] Попытка %d/%d не удалась (ключ #%d, HTTP %d, %s): %v", attempt, maxKeySwitches, leased.ID, status, time.Since(started).Round(time.Millisecond), err)

		switch {
		case status == http.StatusTooManyRequests || strings.Contains(low, "rate_limit"):
			if strings.Contains(low, "per day") || strings.Contains(low, "daily") {
				g.keyPool.ReportDailyQuotaExhausted(leased.ID, err.Error())
			} else {
				g.keyPool.ReportRateLimit429(leased.ID, err.Error())
			}
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			g.keyPool.ReportInvalid(leased.ID, status, err.Error())
			continue
		case status >= 500:
			g.keyPool.ReportUnavailable503(leased.ID, err.Error())
			continue
		}
		return "", err
	}
	return "", lastErr
}

func (g *GroqChatClient) callModels(ctx context.Context, apiKey, system, user, mime string) (string, int, error) {
	out, status, err := g.callOnce(ctx, g.model, apiKey, system, user, mime)
	if err == nil {
		return out, status, nil
	}
	// Модель выведена из обращения / не найдена / отклонила запрос — пробуем запасную
	if g.fallbackModel != "" && g.fallbackModel != g.model &&
		(status == http.StatusNotFound || status == http.StatusBadRequest || status == http.StatusServiceUnavailable) {
		logger.Warn("[GROQ-LLM] Модель %s вернула HTTP %d: пробую %s", g.model, status, g.fallbackModel)
		return g.callOnce(ctx, g.fallbackModel, apiKey, system, user, mime)
	}
	return "", status, err
}

func (g *GroqChatClient) callOnce(ctx context.Context, model, apiKey, system, user, mime string) (string, int, error) {
	reqBody := groqChatRequest{
		Model: model,
		Messages: []groqChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0,
	}
	if strings.Contains(strings.ToLower(mime), "json") {
		reqBody.ResponseFormat = map[string]string{"type": "json_object"}
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqChatEndpoint, bytes.NewReader(b))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("Groq LLM HTTP %d (%s): %s", resp.StatusCode, model, strings.TrimSpace(string(body)))
	}

	var parsed groqChatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", resp.StatusCode, fmt.Errorf("ошибка разбора ответа Groq LLM: %w", err)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", resp.StatusCode, fmt.Errorf("Groq LLM вернул пустой ответ")
	}
	return parsed.Choices[0].Message.Content, http.StatusOK, nil
}
