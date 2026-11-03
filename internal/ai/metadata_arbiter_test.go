// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini 2.5 Flash, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Данный файл metadata_arbiter_test.go содержит модульные тесты для парсера и валидатора решений арбитража.
package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArbiterDecisionParsing(t *testing.T) {
	rawJSON := `{
		"canonical_artist": "Green Day",
		"canonical_album": "American Idiot",
		"release_type": "studio_album",
		"track_title": "Boulevard of Broken Dreams",
		"cover_search_term": "Green Day American Idiot",
		"confidence": 98,
		"explanation": "Песня Boulevard of Broken Dreams является заглавным синглом студийного альбома American Idiot (2004)."
	}`

	var decision ArbiterDecision
	if err := json.Unmarshal([]byte(rawJSON), &decision); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decision.CanonicalArtist != "Green Day" {
		t.Fatalf("expected Green Day, got %s", decision.CanonicalArtist)
	}
	if decision.CanonicalAlbum != "American Idiot" {
		t.Fatalf("expected American Idiot, got %s", decision.CanonicalAlbum)
	}
	if decision.CoverSearchTerm != "Green Day American Idiot" {
		t.Fatalf("expected Green Day American Idiot, got %s", decision.CoverSearchTerm)
	}
	if !strings.Contains(decision.Explanation, "American Idiot") {
		t.Fatalf("unexpected explanation: %s", decision.Explanation)
	}
}
