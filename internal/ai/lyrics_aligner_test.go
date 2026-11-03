// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл lyrics_aligner_test.go тестирует модуль Forced Alignment (lyrics_aligner.go и needleman_aligner.go):
// 1. Калибровку растянутых таймкодов первого слова после инструментального интро (Word Duration Clamping).
// 2. Глобальное выравнивание последовательностей Нидлмана — Вунша (Needleman-Wunsch).
// 3. Пост-фильтр SnapLRCToAcousticWords, исправляющий галлюцинации таймкодов со стороны LLM.
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
	if !strings.Contains(aligned, "[00:18.42] Я помню чудное мгновенье") {
		t.Errorf("первая строка должна иметь точный таймкод [00:18.42]:\n%s", aligned)
	}
	if !strings.Contains(aligned, "[00:22.10] Передо мной явилась ты") {
		t.Errorf("вторая строка должна иметь точный таймкод [00:22.10]:\n%s", aligned)
	}
}

func TestLyricsAligner_StretchedIntroClampingAndSnap(t *testing.T) {
	// Имитируем реальный баг Whisper на музыке:
	// Интро играет с 0.00 до 15.00, первое слово "Группа" заканчивается на 15.51,
	// но Whisper растянул Start первого слова на 0.00!
	whisperRes := &WhisperTranscriptionResult{
		Language: "ru",
		Duration: 180.0,
		Words: []WhisperWord{
			{Word: "Группа", Start: 0.00, End: 15.51},
			{Word: "крови", Start: 15.55, End: 16.05},
			{Word: "на", Start: 16.10, End: 16.30},
			{Word: "рукаве", Start: 16.35, End: 17.10},
			// Проигрыш 10 секунд, Whisper снова растянул Start слова "Мой" с 17.20 до 27.60
			{Word: "Мой", Start: 17.20, End: 27.60},
			{Word: "порядковый", Start: 27.65, End: 28.40},
			{Word: "номер", Start: 28.45, End: 29.00},
			{Word: "Субтитры сделал DimaTorzok", Start: 170.0, End: 175.0},
		},
	}

	data := SanitizeWhisperTimeline(whisperRes)

	// Проверяем, что галлюцинация DimaTorzok удалена (осталось 7 реальных слов)
	if len(data.Words) != 7 {
		t.Fatalf("ожидалось 7 очищенных слов, получено %d", len(data.Words))
	}

	// Проверяем, что растянутый Start=0.00 у слова "Группа" (End=15.51) прижался к ~15.00!
	if data.Words[0].Start < 14.80 || data.Words[0].Start > 15.30 {
		t.Errorf("ожидался откалиброванный Start первого слова около 15.00, получено: %.2f", data.Words[0].Start)
	}

	// Проверяем, что растянутый Start=17.20 у слова "Мой" (End=27.60) прижался к ~27.32!
	if data.Words[4].Start < 27.00 || data.Words[4].Start > 27.45 {
		t.Errorf("ожидался откалиброванный Start после проигрыша около 27.32, получено: %.2f", data.Words[4].Start)
	}

	// Проверяем, что если Gemini вернул сбитые таймкоды ([00:01.00] и [00:05.00]),
	// SnapLRCToAcousticWords жестко примагнитит их к реальным словам на 15-й и 27-й секундах!
	driftedGeminiLRC := "[00:01.00] Группа крови на рукаве\n[00:05.00] Мой порядковый номер"
	snapped := SnapLRCToAcousticWords(driftedGeminiLRC, data)

	if !strings.Contains(snapped, "[00:15.00] Группа крови на рукаве") {
		t.Errorf("SnapLRCToAcousticWords не примагнитил первую строку к 15.00:\n%s", snapped)
	}
	if !strings.Contains(snapped, "[00:27.32] Мой порядковый номер") {
		t.Errorf("SnapLRCToAcousticWords не примагнитил вторую строку к 27.32:\n%s", snapped)
	}
}
