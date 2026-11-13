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
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
)

const groqChatEndpoint = "https://api.groq.com/openai/v1/chat/completions"

const groqModelsEndpoint = "https://api.groq.com/openai/v1/models"

// Модели Groq выводятся из обращения часто, поэтому клиент не полагается на одно жёстко заданное имя:
// список доступных моделей запрашивается у самого Groq (GET /models) и выбирается лучшая по предпочтениям.
// GROQ_LLM_MODEL (если задан) всегда пробуется первой. Значения ниже — только запасные догадки,
// если список получить не удалось.
var groqModelPreference = []string{
	"gpt-oss-120b", "llama-3.3-70b", "llama-4-maverick", "llama-4-scout", "qwen", "kimi", "gpt-oss-20b", "llama-3.1-70b", "llama-3.1-8b",
}

var groqModelExclude = []string{"whisper", "guard", "tts", "playai", "orpheus", "embed", "distil", "compound", "prompt-guard", "safeguard"}

const (
	guessGroqChatModel         = "llama-3.3-70b-versatile"
	guessGroqChatFallbackModel = "llama-3.1-8b-instant"
	groqModelListTTL           = time.Hour
)

// GroqChatClient — клиент текстовой модели Groq.
type GroqChatClient struct {
	keyPool      *keypool.Manager
	client       *http.Client
	pinnedModel  string // из GROQ_LLM_MODEL
	mu           sync.Mutex
	models       []string // отранжированные доступные модели
	modelsLoaded time.Time
}

// NewGroqChatClient создаёт клиент. Ключи берутся из пула провайдера "groq".
func NewGroqChatClient(keyPool *keypool.Manager) *GroqChatClient {
	return &GroqChatClient{
		keyPool:     keyPool,
		client:      &http.Client{Timeout: 30 * time.Second},
		pinnedModel: strings.TrimSpace(os.Getenv("GROQ_LLM_MODEL")),
	}
}

// rankGroqModels отбирает чат-модели и сортирует их по списку предпочтений.
func rankGroqModels(ids []string) []string {
	var chat []string
	for _, id := range ids {
		low := strings.ToLower(id)
		skip := false
		for _, bad := range groqModelExclude {
			if strings.Contains(low, bad) {
				skip = true
				break
			}
		}
		if !skip {
			chat = append(chat, id)
		}
	}
	var out []string
	used := map[string]bool{}
	for _, pref := range groqModelPreference {
		for _, id := range chat {
			if !used[id] && strings.Contains(strings.ToLower(id), pref) {
				used[id] = true
				out = append(out, id)
			}
		}
	}
	for _, id := range chat {
		if !used[id] {
			out = append(out, id)
		}
	}
	return out
}

// candidateModels возвращает модели в порядке попыток (закреплённая → лучшие из списка Groq → догадки).
func (g *GroqChatClient) candidateModels(ctx context.Context, apiKey string) []string {
	g.mu.Lock()
	cached := g.models
	fresh := time.Since(g.modelsLoaded) < groqModelListTTL
	g.mu.Unlock()

	if len(cached) == 0 || !fresh {
		if ids, err := g.listModels(ctx, apiKey); err != nil {
			logger.Warn("[GROQ-LLM] Не удалось получить список моделей: %v", err)
		} else if ranked := rankGroqModels(ids); len(ranked) > 0 {
			g.mu.Lock()
			g.models = ranked
			g.modelsLoaded = time.Now()
			g.mu.Unlock()
			cached = ranked
			logger.Info("[GROQ-LLM] Доступные чат-модели (по предпочтению): %s", strings.Join(ranked, ", "))
		}
	}

	var out []string
	seen := map[string]bool{}
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	add(g.pinnedModel)
	for _, preference := range modelPreference(ctx) {
		for _, model := range cached {
			if strings.EqualFold(model, preference) || strings.Contains(strings.ToLower(model), strings.ToLower(preference)) {
				add(model)
			}
		}
	}
	for _, m := range cached {
		add(m)
	}
	if len(cached) == 0 {
		add(guessGroqChatModel)
		add(guessGroqChatFallbackModel)
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func (g *GroqChatClient) listModels(ctx context.Context, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, groqModelsEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Groq model list request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Groq model list HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID     string `json:"id"`
			Active *bool  `json:"active"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range parsed.Data {
		if m.ID != "" && (m.Active == nil || *m.Active) {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
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

func isModelUnavailable(status int, err error) bool {
	if status == http.StatusNotFound {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "model_not_found") || strings.Contains(low, "decommissioned") ||
		(status == http.StatusBadRequest && strings.Contains(low, "model"))
}

func shouldTryNextGroqModel(status int, err error, mime string) bool {
	if isModelUnavailable(status, err) {
		return true
	}
	return status == http.StatusBadRequest && strings.Contains(strings.ToLower(mime), "json")
}

func (g *GroqChatClient) callModels(ctx context.Context, apiKey, system, user, mime string) (string, int, error) {
	var lastStatus int
	var lastErr error
	for i, model := range g.candidateModels(ctx, apiKey) {
		out, status, err := g.callOnce(ctx, model, apiKey, system, user, mime)
		if err == nil {
			if i > 0 {
				logger.Info("[GROQ-LLM] Работает модель %s", model)
			}
			return out, status, nil
		}
		lastStatus, lastErr = status, err
		if !shouldTryNextGroqModel(status, err, mime) {
			return "", status, err
		}
		logger.Warn("[GROQ-LLM] Модель %s отклонила запрос (HTTP %d), пробую следующую", model, status)
		// Список мог устареть — при следующем вызове запросим заново
		g.mu.Lock()
		g.modelsLoaded = time.Time{}
		g.mu.Unlock()
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("нет доступных моделей Groq")
	}
	return "", lastStatus, lastErr
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
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("Groq LLM request failed")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("Groq LLM HTTP %d (%s)", resp.StatusCode, model)
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
