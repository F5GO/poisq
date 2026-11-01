// Package lyrics_test тестирует корректность разбора и генерации формата LRC.
//
// Данный файл lrc_parser_test.go проверяет точность извлечения миллисекунд из меток [mm:ss.xx],
// расчет длительностей строк и удаление таймингов для plain текста.
package lyrics_test

import (
	"testing"

	"github.com/F5GO/poisq/internal/lyrics"
)

func TestParseLRC(t *testing.T) {
	raw := `
	[00:01.50] Первая строка
	[00:04.200] Вторая строка
	[01:10.05] Третья строка
	`

	lines := lyrics.ParseLRC(raw)
	if len(lines) != 3 {
		t.Fatalf("Ожидалось 3 строки, получено %d", len(lines))
	}

	if lines[0].StartMs != 1500 || lines[0].Text != "Первая строка" {
		t.Errorf("Некорректная первая строка: %+v", lines[0])
	}
	if lines[1].StartMs != 4200 || lines[1].Text != "Вторая строка" {
		t.Errorf("Некорректная вторая строка: %+v", lines[1])
	}
	if lines[2].StartMs != 70050 || lines[2].Text != "Третья строка" {
		t.Errorf("Некорректная третья строка: %+v", lines[2])
	}

	if lines[0].DurationMs != 2700 {
		t.Errorf("Ожидалась длительность 2700мс, получено %d", lines[0].DurationMs)
	}
}

func TestBuildLRC(t *testing.T) {
	lines := []lyrics.Line{
		{Text: "Привет", StartMs: 1250},
		{Text: "Мир", StartMs: 5000},
	}

	lrc := lyrics.BuildLRC(lines)
	expected := "[00:01.25] Привет\n[00:05.00] Мир"
	if lrc != expected {
		t.Errorf("Ожидалось:\n%s\nПолучено:\n%s", expected, lrc)
	}
}

func TestStripLRCTimestamps(t *testing.T) {
	raw := "[00:01.25] Привет\n[00:05.00] Мир"
	plain := lyrics.StripLRCTimestamps(raw)
	expected := "Привет\nМир"
	if plain != expected {
		t.Errorf("Ожидалось %q, получено %q", expected, plain)
	}
}
