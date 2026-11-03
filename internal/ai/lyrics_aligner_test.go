// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл lyrics_aligner_test.go тестирует модуль Forced Alignment (lyrics_aligner.go):
// пословное форматирование временной шкалы [mm:ss.xx]слово и математическое нахождение
// акустических якорей для каждой строки эталонного текста песни.
package ai

import (
	"strings"
	"testing"
)

func TestLyricsAligner_WordStreamAndPreAlign(t *testing.T) {
	whisperRes := &WhisperTranscriptionResult{
		Language: "ru",
		Duration: 120.0,
		Words: []WhisperWord{
			// Первое слово начинается на 18.42 сек (после 18 секунд гитарного интро)
			{Word: "Я", Start: 18.42, End: 18.65},
			{Word: "помню", Start: 18.70, End: 19.15},
			{Word: "чудное", Start: 19.20, End: 19.75},
			{Word: "мгновенье", Start: 19.80, End: 20.50},
			// Пауза 1.6 сек перед второй строкой
			{Word: "Передо", Start: 22.10, End: 22.55},
			{Word: "мной", Start: 22.60, End: 23.00},
			{Word: "явилась", Start: 23.05, End: 23.65},
			{Word: "ты", Start: 23.70, End: 24.10},
		},
	}

	payload, err := BuildAcousticPayload(whisperRes)
	if err != nil {
		t.Fatalf("ошибка сборки акустического payload: %v", err)
	}

	stream, data := FormatWordLevelStream(payload)
	if data == nil {
		t.Fatalf("ожидалась структура AcousticTimelineData")
	}
	if data.FirstWordStart != 18.42 {
		t.Errorf("ожидался FirstWordStart 18.42, получено: %.2f", data.FirstWordStart)
	}
	if !strings.Contains(stream, "[00:18.42]Я") || !strings.Contains(stream, "[00:22.10]Передо") {
		t.Errorf("лента слов не содержит ожидаемых пословных таймкодов:\n%s", stream)
	}

	refLyrics := "Я помню чудное мгновенье\nПередо мной явилась ты"
	aligned := PreAlignReferenceWithWords(refLyrics, data)
	if !strings.Contains(aligned, "[00:18.42]") {
		t.Errorf("первая строка должна иметь якорь [00:18.42]:\n%s", aligned)
	}
	if !strings.Contains(aligned, "[00:22.10]") {
		t.Errorf("вторая строка должна иметь якорь [00:22.10]:\n%s", aligned)
	}
}
