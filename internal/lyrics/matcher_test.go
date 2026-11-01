// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл matcher_test.go проверяет точность сопоставления исполнителей и названий треков,
// включая латиницу/кириллицу ("МакSим" vs "МакСим"), пометки версий и фильтрацию ложных треков.
package lyrics

import "testing"

func TestMatcher_RussianNamesAndHomoglyphs(t *testing.T) {
	tests := []struct {
		name        string
		queryArtist string
		candArtist  string
		expected    bool
	}{
		{"Точное совпадение", "МакСим", "МакСим", true},
		{"Латинская S в МакSим", "МакСим", "МакSим", true},
		{"Нижний регистр", "максим", "МАКСИМ", true},
		{"Совместный трек", "МакСим", "МакСим feat. Баста", true},
		{"Совершенно другой артист", "МакСим", "Big Baby Tape", false},
		{"Король и Шут", "Король и Шут", "Король И Шут", true},
		{"Кино", "Кино", "Виктор Цой и Кино", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchArtist(tc.queryArtist, tc.candArtist)
			if got != tc.expected {
				t.Errorf("MatchArtist(%q, %q) = %v; want %v", tc.queryArtist, tc.candArtist, got, tc.expected)
			}
		})
	}
}

func TestMatcher_Titles(t *testing.T) {
	tests := []struct {
		name       string
		queryTitle string
		candTitle  string
		expected   bool
	}{
		{"Точное совпадение", "Отпускаю", "Отпускаю", true},
		{"С пометкой версии в скобках", "Отпускаю", "Отпускаю (Radio Edit)", true},
		{"С пометкой feat", "Отпускаю", "Отпускаю ft. Лигалайз", true},
		{"Другой трек со схожим словом", "Отпускаю", "Я тебя не отпускаю никогда", false},
		{"Несвязанный трек", "Отпускаю", "Дым в потолок", false},
		{"Кукла колдуна", "Кукла колдуна", "Кукла Колдуна (Remastered 2020)", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchTitle(tc.queryTitle, tc.candTitle)
			if got != tc.expected {
				t.Errorf("MatchTitle(%q, %q) = %v; want %v", tc.queryTitle, tc.candTitle, got, tc.expected)
			}
		})
	}
}

func TestMatcher_MatchTrack(t *testing.T) {
	q := SearchQuery{
		Artist: "МакСим",
		Title:  "Отпускаю",
	}

	// Позитивные кейсы
	if !MatchTrack(q, "МакSим", "Отпускаю") {
		t.Errorf("Ожидалось совпадение для 'МакSим' - 'Отпускаю'")
	}
	if !MatchTrack(q, "МакСим", "Отпускаю (Radio Edit)") {
		t.Errorf("Ожидалось совпадение для 'МакСим' - 'Отпускаю (Radio Edit)'")
	}

	// Негативные кейсы (ложные хиты из полнотекстового поиска Genius)
	if MatchTrack(q, "Big Baby Tape", "Отпускаю") {
		t.Errorf("Не должно совпадать для другого артиста 'Big Baby Tape'")
	}
	if MatchTrack(q, "МакСим", "Знаешь ли ты") {
		t.Errorf("Не должно совпадать для другой песни того же артиста")
	}
}
