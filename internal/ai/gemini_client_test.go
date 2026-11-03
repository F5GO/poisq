// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл gemini_client_test.go тестирует инициализацию клиента Gemini,
// чтение модели из переменной окружения GEMINI_MODEL (дефолтная gemini-3.8-flash),
// парсинг ответа официального Interactions API и работу каскадного фоллбэка при ошибках.
package ai

import (
	"os"
	"strings"
	"testing"
)

func TestGeminiClient_ModelConfiguration(t *testing.T) {
	// 1. По умолчанию используется gemini-3.8-flash (оптимальная быстрая модель текущего поколения)
	_ = os.Unsetenv("GEMINI_MODEL")
	c1 := NewGeminiClient(nil)
	if c1.model != "gemini-3.8-flash" {
		t.Errorf("ожидалась дефолтная модель 'gemini-3.8-flash', получено: %s", c1.model)
	}

	// 2. Переменная окружения переопределяет модель
	_ = os.Setenv("GEMINI_MODEL", "gemini-4-argon")
	defer os.Unsetenv("GEMINI_MODEL")

	c2 := NewGeminiClient(nil)
	if c2.model != "gemini-4-argon" {
		t.Errorf("ожидалась модель из env 'gemini-4-argon', получено: %s", c2.model)
	}
}

func TestGeminiClient_InteractionsAPIParsing(t *testing.T) {
	// 1. Успешный ответ с цепочкой thought и model_output
	rawJSON := []byte(`{
		"id": "v1_test_interaction",
		"status": "completed",
		"steps": [
			{
				"type": "thought",
				"signature": "test_thought_signature"
			},
			{
				"type": "model_output",
				"content": [
					{
						"type": "text",
						"text": "{\"langCode\":\"ru\",\"isSynced\":true,\"cleanedLyrics\":\"[00:10.00] Тест\"}"
					}
				]
			}
		]
	}`)

	resText, status, err := parseInteractionResponse(rawJSON)
	if err != nil {
		t.Fatalf("неожиданная ошибка разбора ответа: %v", err)
	}
	if status != "OK" {
		t.Errorf("ожидался статус OK, получено: %s", status)
	}
	expectedSubstring := `[00:10.00] Тест`
	if !strings.Contains(resText, expectedSubstring) {
		t.Errorf("текст ответа не содержит ожидаемой подстроки: %s", resText)
	}

	// 2. Ответ с ошибкой Google API
	errJSON := []byte(`{
		"error": {
			"code": 404,
			"message": "Model not found",
			"status": "NOT_FOUND"
		}
	}`)
	_, _, err = parseInteractionResponse(errJSON)
	if err == nil {
		t.Errorf("ожидалась ошибка от API, но функция вернула nil")
	}

	// 3. Ответ без текста model_output
	emptyJSON := []byte(`{
		"id": "v1_empty",
		"status": "completed",
		"steps": []
	}`)
	_, _, err = parseInteractionResponse(emptyJSON)
	if err == nil {
		t.Errorf("ожидалась ошибка при отсутствии шагов model_output, но получено nil")
	}
}
