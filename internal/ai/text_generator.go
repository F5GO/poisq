// Package ai реализует облачные AI-пайплайны сервиса poisq.
//
// Данный файл text_generator.go задаёт общий интерфейс текстовой LLM и каскад «основная → запасная».
// Gemini часто отвечает 503 (High Demand), поэтому по умолчанию первой вызывается Groq (быстрый Llama),
// а Gemini остаётся запасным вариантом — и наоборот, если Groq исчерпал лимиты.
package ai

import (
	"context"
	"fmt"

	"github.com/F5GO/poisq/internal/logger"
)

type modelPreferenceContextKey struct{}

func withModelPreference(ctx context.Context, models ...string) context.Context {
	bounded := make([]string, 0, len(models))
	for _, model := range models {
		if model != "" && len(model) <= 128 {
			bounded = append(bounded, model)
		}
	}
	return context.WithValue(ctx, modelPreferenceContextKey{}, bounded)
}

func modelPreference(ctx context.Context) []string {
	models, _ := ctx.Value(modelPreferenceContextKey{}).([]string)
	return models
}

// TextGenerator — любая текстовая LLM, умеющая вернуть ответ (при mime "application/json" — валидный JSON).
type TextGenerator interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt, responseMimeType string) (string, error)
}

// namedGenerator связывает LLM с именем для логов.
type namedGenerator struct {
	name string
	gen  TextGenerator
}

// FallbackGenerator по очереди пробует несколько LLM и возвращает первый успешный ответ.
type FallbackGenerator struct {
	chain []namedGenerator
}

// NewFallbackGenerator создаёт каскад. Названия используются только в логах; nil-генераторы пропускаются.
func NewFallbackGenerator() *FallbackGenerator {
	return &FallbackGenerator{}
}

// Add добавляет LLM в конец каскада.
func (f *FallbackGenerator) Add(name string, gen TextGenerator) *FallbackGenerator {
	if gen != nil {
		f.chain = append(f.chain, namedGenerator{name: name, gen: gen})
	}
	return f
}

// GenerateText вызывает LLM из каскада по порядку.
func (f *FallbackGenerator) GenerateText(ctx context.Context, systemPrompt, userPrompt, responseMimeType string) (string, error) {
	if len(f.chain) == 0 {
		return "", fmt.Errorf("не настроена ни одна LLM")
	}
	var lastErr error
	for i, g := range f.chain {
		out, err := g.gen.GenerateText(ctx, systemPrompt, userPrompt, responseMimeType)
		if err == nil {
			if i > 0 {
				logger.Info("[LLM] Ответ получен от запасной модели %s", g.name)
			}
			return out, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		logger.Warn("[LLM] %s не ответил (%v), пробую следующую", g.name, err)
	}
	return "", fmt.Errorf("все LLM недоступны: %w", lastErr)
}
