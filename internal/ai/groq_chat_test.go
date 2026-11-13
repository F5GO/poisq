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
	if !shouldTryNextGroqModel(http.StatusTooManyRequests, errors.New("model rate limit"), "application/json") {
		t.Fatal("model-specific HTTP 429 must try the next model before cooling the key")
	}
}

func TestBuildGroqChatRequestSetsParsedReasoningForQwenJSON(t *testing.T) {
	qwenReq := buildGroqChatRequest("qwen/qwen3.8-27b", "sys", "usr", "application/json")
	if qwenReq.ResponseFormat["type"] != "json_object" || qwenReq.ReasoningFormat != "parsed" {
		t.Fatalf("unexpected qwen request: %#v", qwenReq)
	}

	gptReq := buildGroqChatRequest("openai/gpt-oss-20b", "sys", "usr", "application/json")
	if gptReq.ResponseFormat["type"] != "json_object" || gptReq.ReasoningFormat != "" {
		t.Fatalf("unexpected gpt-oss request: %#v", gptReq)
	}
}

func TestExtractGroqAPIError(t *testing.T) {
	detail := extractGroqAPIError([]byte(`{"error":{"message":"Failed to validate JSON","type":"invalid_request_error","code":"json_validate_failed"}}`))
	if detail != "json_validate_failed: invalid_request_error: Failed to validate JSON" {
		t.Fatalf("unexpected detail: %q", detail)
	}
	if extractGroqAPIError([]byte("not json")) != "" {
		t.Fatal("expected empty string for non-JSON body")
	}
}
