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
// Строгий белый список крупных чат-моделей Groq, подходящих для синтеза биографий и JSON-задач.
// Мелкие и специализированные модели (gpt-oss-20b, llama-3.1-8b, allam-2-7b и др.) намеренно не используются:
// при лимите 429 клиент сразу переключается на другую крупную модель из списка или на следующий ключ из пула.
var groqModelPreference = []string{
	"gpt-oss-120b",
	"qwen3.8-27b",
	"qwen3-32b",
	"qwen-qwq-32b",
	"llama-3.3-70b",
	"llama-4-maverick",
	"llama-4-scout",
	"kimi-k2",
}

var groqModelExclude = []string{"whisper", "guard", "tts", "playai", "orpheus", "embed", "distil", "compound", "prompt-guard", "safeguard", "allam", "arabic", "canopylabs", "gpt-oss-20b", "8b"}

const (
	guessGroqChatModel         = "openai/gpt-oss-120b"
	guessGroqChatFallbackModel = "qwen/qwen3.8-27b"
	groqModelListTTL           = time.Hour
)

// GroqChatClient — клиент текстовой модели Groq.
type GroqChatClient struct {
	keyPool       *keypool.Manager
	client        *http.Client
	pinnedModel   string // из GROQ_LLM_MODEL
	mu            sync.Mutex
	models        []string // отранжированные доступные модели
	modelsLoaded  time.Time
	lastKeyID     int64
	modelCooldown map[string]time.Time
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
	return g.orderByModelCooldown(apiKey, out)
}

func (g *GroqChatClient) orderByModelCooldown(apiKey string, models []string) []string {
	if len(models) == 0 || apiKey == "" {
		return models
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.modelCooldown) == 0 {
		return models
	}
	ready := make([]string, 0, len(models))
	for _, m := range models {
		if until, ok := g.modelCooldown[apiKey+"|"+m]; ok && now.Before(until) {
			continue
		}
		ready = append(ready, m)
	}
	return ready
}

func (g *GroqChatClient) recordModelRateLimit(apiKey, model string, err error) {
	if apiKey == "" || model == "" || err == nil {
		return
	}
	low := strings.ToLower(err.Error())
	ttl := 15 * time.Second
	if strings.Contains(low, "per day") || strings.Contains(low, "daily") || strings.Contains(low, "(tpd)") {
		ttl = 30 * time.Minute
	}
	g.mu.Lock()
	if g.modelCooldown == nil {
		g.modelCooldown = make(map[string]time.Time)
	}
	g.modelCooldown[apiKey+"|"+model] = time.Now().Add(ttl)
	g.mu.Unlock()
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
	Model           string            `json:"model"`
	Messages        []groqChatMessage `json:"messages"`
	Temperature     float64           `json:"temperature"`
	ResponseFormat  map[string]string `json:"response_format,omitempty"`
	ReasoningFormat string            `json:"reasoning_format,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
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
		g.mu.Lock()
		excludeID := g.lastKeyID
		g.mu.Unlock()

		leased, err := g.keyPool.AcquireExcluding("groq", excludeID)
		if err != nil {
			if lastErr != nil {
				return "", fmt.Errorf("%w (последняя ошибка Groq: %v)", err, lastErr)
			}
			return "", err
		}

		g.mu.Lock()
		g.lastKeyID = leased.ID
		g.mu.Unlock()

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
		case status == http.StatusBadRequest && strings.Contains(low, "json_validate_failed"):
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
	return status == http.StatusTooManyRequests ||
		(status == http.StatusBadRequest && strings.Contains(strings.ToLower(mime), "json"))
}

func (g *GroqChatClient) callModels(ctx context.Context, apiKey, system, user, mime string) (string, int, error) {
	candidates := g.candidateModels(ctx, apiKey)
	if len(candidates) == 0 {
		return "", http.StatusTooManyRequests, fmt.Errorf("rate_limit_exceeded: подходящие модели Groq на данном ключе находятся в лимите")
	}
	var lastStatus int
	var lastErr error
	var rateLimitErr error
	for i, model := range candidates {
		out, status, err := g.callOnce(ctx, model, apiKey, system, user, mime)
		if err == nil {
			if i > 0 {
				logger.Info("[GROQ-LLM] Работает модель %s", model)
			}
			return out, status, nil
		}
		lastStatus, lastErr = status, err
		if status == http.StatusTooManyRequests {
			g.recordModelRateLimit(apiKey, model, err)
			if rateLimitErr == nil {
				rateLimitErr = err
			}
		}
		if !shouldTryNextGroqModel(status, err, mime) {
			return "", status, err
		}
		logger.Warn("[GROQ-LLM] Модель %s отклонила запрос (HTTP %d): %v — пробую следующую", model, status, err)
		if isModelUnavailable(status, err) {
			// Только отсутствие модели означает, что кеш списка мог устареть.
			g.mu.Lock()
			g.modelsLoaded = time.Time{}
			g.mu.Unlock()
		}
	}
	if rateLimitErr != nil && lastStatus == http.StatusBadRequest {
		return "", http.StatusTooManyRequests, rateLimitErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("нет доступных моделей Groq")
	}
	return "", lastStatus, lastErr
}

func buildGroqChatRequest(model, system, user, mime string) groqChatRequest {
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
		lowModel := strings.ToLower(model)
		if strings.Contains(lowModel, "qwen") {
			// Qwen3 reasoning models emit <think>...</think> into content unless parsed reasoning is requested.
			reqBody.ReasoningFormat = "parsed"
		} else if strings.Contains(lowModel, "gpt-oss") {
			reqBody.ReasoningEffort = "low"
		}
	}
	return reqBody
}

func extractGroqAPIError(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if code := strings.TrimSpace(parsed.Error.Code); code != "" {
		parts = append(parts, code)
	}
	if typ := strings.TrimSpace(parsed.Error.Type); typ != "" && typ != parsed.Error.Code {
		parts = append(parts, typ)
	}
	if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
		if len(msg) > 200 {
			msg = msg[:200]
		}
		parts = append(parts, msg)
	}
	return strings.Join(parts, ": ")
}

func (g *GroqChatClient) callOnce(ctx context.Context, model, apiKey, system, user, mime string) (string, int, error) {
	out, status, err := g.doCallOnce(ctx, model, apiKey, system, user, mime)
	if err != nil && status == http.StatusBadRequest && strings.Contains(strings.ToLower(mime), "json") && strings.Contains(strings.ToLower(err.Error()), "json_validate_failed") {
		return g.doCallOnce(ctx, model, apiKey, system+" Return strictly one valid JSON object without markdown fences or extra text.", user, "text/plain")
	}
	return out, status, err
}

func (g *GroqChatClient) doCallOnce(ctx context.Context, model, apiKey, system, user, mime string) (string, int, error) {
	reqBody := buildGroqChatRequest(model, system, user, mime)
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
		if detail := extractGroqAPIError(body); detail != "" {
			return "", resp.StatusCode, fmt.Errorf("Groq LLM HTTP %d (%s): %s", resp.StatusCode, model, detail)
		}
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
