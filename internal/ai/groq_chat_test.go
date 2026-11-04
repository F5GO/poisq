package ai

import (
	"errors"
	"net/http"
	"testing"
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
