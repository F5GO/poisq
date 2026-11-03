// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл gemini_response_parser.go реализует надежный разбор ответов официального
// Google Interactions API (/v1beta/interactions), извлечение полезной текстовой нагрузки
// из цепочки шагов model_output и обработку диагностических ошибок API.
package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseInteractionResponse разбирает JSON-ответ от Google Interactions API,
// извлекает и склеивает весь сгенерированный текст из шагов model_output.
func parseInteractionResponse(respBytes []byte) (string, string, error) {
	var parsed geminiInteractionResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", "", fmt.Errorf("ошибка декодирования JSON ответа Interactions API: %w", err)
	}

	if parsed.Error != nil {
		return "", parsed.Error.Status, fmt.Errorf("Google API ошибка: %s (код %d)", parsed.Error.Message, parsed.Error.Code)
	}

	var textBuilder strings.Builder
	for _, step := range parsed.Steps {
		if step.Type == "model_output" {
			for _, content := range step.Content {
				if content.Type == "text" && content.Text != "" {
					textBuilder.WriteString(content.Text)
				}
			}
		}
	}

	resText := strings.TrimSpace(textBuilder.String())
	if resText == "" {
		return "", "", fmt.Errorf("ответ Interactions API не содержит шагов model_output с текстом")
	}

	return resText, "OK", nil
}
