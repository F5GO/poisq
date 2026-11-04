package search

import "testing"

func TestLibraryMatchMaksimVariants(t *testing.T) {
	entry := BuildLibraryKey("МакSим", "Отпускаю", "Трудный возраст", "/music/МакСим/07 - Отпускаю.flac")
	other := BuildLibraryKey("Linkin Park", "Good Things Go", "From Zero", "/music/Linkin Park/11 Good Things Go.flac")

	for _, q := range []string{"максим", "Максим", "МАКСИМ", "maksim", "MakSim", "макs", "максим отпускаю"} {
		lq := PrepareLibraryQuery(q)
		if lq.Score(entry) == 0 {
			t.Errorf("запрос %q должен находить «МакSим — Отпускаю»", q)
		}
		if lq.Score(other) != 0 {
			t.Errorf("запрос %q не должен находить Linkin Park", q)
		}
	}
}

func TestLibraryMatchTypoAndMixedScript(t *testing.T) {
	sym := BuildLibraryKey("Grebz • 4atty aka Tilla • Sимптом", "Юрфак", "STYLE IN MC", "")
	for _, q := range []string{"симптом", "symptom", "simptom", "юрфак", "yurfak"} {
		if PrepareLibraryQuery(q).Score(sym) == 0 {
			t.Errorf("запрос %q должен находить трек с «Sимптом»", q)
		}
	}
	if PrepareLibraryQuery("металлика").Score(sym) != 0 {
		t.Errorf("посторонний запрос не должен находить трек")
	}
}

func TestLibraryRankPrefersWordStart(t *testing.T) {
	keys := []LibraryKey{BuildLibraryKey("Мария Максимова"), BuildLibraryKey("Максим")}
	got := RankByQuery(PrepareLibraryQuery("максим"), []string{"a", "b"}, keys, 10)
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("ожидался приоритет точного артиста Максим: %+v", got)
	}
}
