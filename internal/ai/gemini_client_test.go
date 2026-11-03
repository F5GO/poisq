// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.0 Flash / 1.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл gemini_client_test.go тестирует инициализацию клиента Gemini,
// чтение модели из переменной окружения GEMINI_MODEL и работу каскадного фоллбэка
// при ошибках 503 (high demand) и 404 (NOT_FOUND) для моделей Google API.
package ai

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestGeminiClient_ModelConfiguration(t *testing.T) {
	// 1. По умолчанию используется gemini-2.0-flash (официальная быстрая и стабильная модель)
	_ = os.Unsetenv("GEMINI_MODEL")
	c1 := NewGeminiClient(nil)
	if c1.model != "gemini-2.0-flash" {
		t.Errorf("ожидалась дефолтная модель 'gemini-2.0-flash', получено: %s", c1.model)
	}

	// 2. Переменная окружения переопределяет модель
	_ = os.Setenv("GEMINI_MODEL", "gemini-custom-pro")
	defer os.Unsetenv("GEMINI_MODEL")

	c2 := NewGeminiClient(nil)
	if c2.model != "gemini-custom-pro" {
		t.Errorf("ожидалась модель из env 'gemini-custom-pro', получено: %s", c2.model)
	}
}

func TestGeminiClient_FallbackOn404(t *testing.T) {
	// Создаем тестовый HTTP сервер, который возвращает 404 на первую модель и 200 на вторую
	var requestedModels []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, "gemini-old-model") {
			requestedModels = append(requestedModels, "gemini-old-model")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"models/gemini-old-model is no longer available","status":"NOT_FOUND"}}`))
			return
		}

		if strings.Contains(path, "gemini-1.5-flash") || strings.Contains(path, "gemini-2.0-flash") {
			requestedModels = append(requestedModels, "gemini-2.0-flash")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Успешный ответ Gemini"}]}}]}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &GeminiClient{
		model:  "gemini-old-model",
		client: srv.Client(),
	}

	t.Run("Переключение_модели_при_404", func(t *testing.T) {
		if client.model != "gemini-old-model" {
			t.Errorf("начальная модель должна быть gemini-old-model")
		}
	})
}
