package ai

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRankGroqModelsPrefersLargeChatAndSkipsAudio(t *testing.T) {
	ids := []string{"whisper-large-v3", "llama-guard-4-12b", "playai-tts", "llama-3.1-8b-instant", "openai/gpt-oss-120b", "llama-3.3-70b-versatile", "mystery-model"}
	got := rankGroqModels(ids)
	want := []string{"openai/gpt-oss-120b", "llama-3.3-70b-versatile", "llama-3.1-8b-instant", "mystery-model"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestGroqRolePreferenceReordersAvailableModels(t *testing.T) {
	client := &GroqChatClient{
		models:       []string{"openai/gpt-oss-120b", "qwen/qwen3.8-27b", "openai/gpt-oss-20b"},
		modelsLoaded: time.Now(),
	}
	models := client.candidateModels(withModelPreference(context.Background(), "openai/gpt-oss-20b"), "unused")
	if len(models) < 3 || models[0] != "openai/gpt-oss-20b" || models[1] != "openai/gpt-oss-120b" {
		t.Fatalf("unexpected role order: %v", models)
	}
}

func TestIsModelUnavailable(t *testing.T) {
	if !isModelUnavailable(http.StatusNotFound, errors.New("x")) {
		t.Fatal("404 = модель недоступна")
	}
	if !isModelUnavailable(http.StatusBadRequest, errors.New(`{"code":"model_decommissioned"}`)) {
		t.Fatal("decommissioned = недоступна")
	}
	if isModelUnavailable(http.StatusTooManyRequests, errors.New("rate limit")) {
		t.Fatal("429 не про модель")
	}
}

func TestGroqJSONBadRequestTriesNextModel(t *testing.T) {
	if !shouldTryNextGroqModel(http.StatusBadRequest, errors.New("generated JSON rejected"), "application/json") {
		t.Fatal("JSON generation HTTP 400 must try the next model")
	}
	if shouldTryNextGroqModel(http.StatusBadRequest, errors.New("bad request"), "text/plain") {
		t.Fatal("non-JSON HTTP 400 must remain terminal")
	}
}
