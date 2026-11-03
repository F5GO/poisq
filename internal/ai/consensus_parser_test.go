// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл consensus_parser_test.go тестирует сверхустойчивый парсер ParseConsensusOutput:
// обработку стандартного JSON, исправление закрывающей скобки ']' вместо '}',
// автоматическую конвертацию массивов строк в multiline-текст и аварийное извлечение LRC.
package ai

import (
	"strings"
	"testing"
)

func TestParseConsensusOutput_Standard(t *testing.T) {
	input := `{"langCode": "ru", "isSynced": true, "cleanedLyrics": "[00:12.34] Строка 1\n[00:16.78] Строка 2"}`
	out, err := ParseConsensusOutput(input)
	if err != nil {
		t.Fatalf("ошибка парсинга стандартного JSON: %v", err)
	}
	if out.LangCode != "ru" || !out.IsSynced {
		t.Errorf("неверные метаданные: %+v", out)
	}
	if !strings.Contains(out.CleanedLyrics, "[00:12.34] Строка 1") {
		t.Errorf("не найден текст строки: %s", out.CleanedLyrics)
	}
}

func TestParseConsensusOutput_MismatchedBracket(t *testing.T) {
	// Ошибка пользователя: invalid character ']' after object key:value pair
	input := `{"langCode": "ru", "isSynced": true, "cleanedLyrics": "[00:10.00] Куплет 1\n[00:15.00] Припев"]`
	out, err := ParseConsensusOutput(input)
	if err != nil {
		t.Fatalf("парсер должен был исправить непарную скобку ]: %v", err)
	}
	if !strings.Contains(out.CleanedLyrics, "[00:10.00] Куплет 1") {
		t.Errorf("не найден текст LRC: %s", out.CleanedLyrics)
	}
}

func TestParseConsensusOutput_ArrayOfStrings(t *testing.T) {
	// Модель вернула массив строк вместо единой строки
	input := `{
		"langCode": "en",
		"isSynced": true,
		"cleanedLyrics": [
			"[00:05.10] Hello world",
			"[00:09.50] Second line"
		]
	}`
	out, err := ParseConsensusOutput(input)
	if err != nil {
		t.Fatalf("ошибка разбора массива строк: %v", err)
	}
	if out.LangCode != "en" {
		t.Errorf("ожидался langCode en, получено: %s", out.LangCode)
	}
	expected := "[00:05.10] Hello world\n[00:09.50] Second line"
	if out.CleanedLyrics != expected {
		t.Errorf("ожидалось:\n%s\nполучено:\n%s", expected, out.CleanedLyrics)
	}
}

func TestParseConsensusOutput_MarkdownWrapped(t *testing.T) {
	input := "```json\n{\n  \"langCode\": \"ru\",\n  \"isSynced\": true,\n  \"cleanedLyrics\": \"[00:01.00] Старт\"\n}\n```"
	out, err := ParseConsensusOutput(input)
	if err != nil {
		t.Fatalf("ошибка разбора markdown JSON: %v", err)
	}
	if !strings.Contains(out.CleanedLyrics, "[00:01.00] Старт") {
		t.Errorf("не найден текст: %s", out.CleanedLyrics)
	}
}

func TestParseConsensusOutput_BrokenJSON_RegexFallback(t *testing.T) {
	// Полностью сломанный синтаксис JSON, но есть валидные LRC-строки
	input := `Вот результат синхронизации:
	{
		"langCode": "ru",
		"bad_syntax": [00:14.20] Текст первой строки
		"another_line": [00:18.50] Текст второй строки
	] `
	out, err := ParseConsensusOutput(input)
	if err != nil {
		t.Fatalf("аварийный экстрактор должен был извлечь LRC: %v", err)
	}
	if !strings.Contains(out.CleanedLyrics, "[00:14.20]") || !strings.Contains(out.CleanedLyrics, "[00:18.50]") {
		t.Errorf("не извлечены таймкоды: %s", out.CleanedLyrics)
	}
}
