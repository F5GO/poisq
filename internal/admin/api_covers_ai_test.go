// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_covers_ai_test.go тестирует работу эндпоинта /api/v1/covers/ai-resolve,
// проверяя валидацию входных параметров и корректность ответов сервера при различных сценариях.
package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCoverAIResolve_Validation(t *testing.T) {
	server := &AdminServer{}

	// 1. Метод, отличный от GET/POST
	req := httptest.NewRequest(http.MethodDelete, "http://localhost:4535/api/v1/covers/ai-resolve", nil)
	w := httptest.NewRecorder()
	server.handleCoverAIResolve(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405 Method Not Allowed, got %d", w.Code)
	}

	// 2. Без подключенного арбитра
	req = httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/covers/ai-resolve?path=/music/test", nil)
	w = httptest.NewRecorder()
	server.handleCoverAIResolve(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503 Service Unavailable, got %d", w.Code)
	}

	// 3. Без указания пути или ID
	req = httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/covers/ai-resolve", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	server.handleCoverAIResolve(w, req)
	if w.Code != http.StatusServiceUnavailable { // Арбитр по-прежнему nil
		t.Errorf("Expected 503, got %d", w.Code)
	}
}
