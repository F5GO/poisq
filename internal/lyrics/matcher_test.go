// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл matcher_test.go проверяет точность сопоставления исполнителей и названий треков,
// очистку изданий, предотвращение конфликтов версий (Live/Acoustic), сопоставление альбомов и оценку качества.
package lyrics

import (
	"strings"
	"testing"
	"time"
)

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

func TestMatcher_CleanTitleAndEditions(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hotel California (2013 Remaster)", "Hotel California"},
		{"Smells Like Teen Spirit [Deluxe Edition]", "Smells Like Teen Spirit"},
		{"Отпускаю (Юбилейное переиздание 2021)", "Отпускаю"},
		{"Кукла колдуна [Remastered]", "Кукла колдуна"},
		{"In the End (Album Version)", "In the End"},
	}

	for _, tc := range tests {
		got := CleanTitle(tc.input)
		if got != tc.expected {
			t.Errorf("CleanTitle(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestMatcher_VersionConflict(t *testing.T) {
	// 1. Студийный запрос против концертной/ремикс версии — конфликт!
	if !HasVersionConflict("Hotel California", "Hotel California (Live)") {
		t.Errorf("Ожидался конфликт между студийной версией и Live")
	}
	if !HasVersionConflict("Отпускаю", "Отпускаю (Remix)") {
		t.Errorf("Ожидался конфликт между оригиналом и Remix")
	}
	if !HasVersionConflict("Кукла колдуна", "Кукла колдуна (Акустика)") {
		t.Errorf("Ожидался конфликт между оригиналом и Акустикой")
	}

	// 2. Концертный запрос против концертного кандидата — совпадение (нет конфликта)!
	if HasVersionConflict("Hotel California (Live)", "Hotel California (Live in Paris)") {
		t.Errorf("Не должно быть конфликта, когда оба Live")
	}

	// 3. Студийный запрос против ремастера — нет конфликта (ремастер не меняет исполнение)!
	if HasVersionConflict("Hotel California", "Hotel California (Remastered)") {
		t.Errorf("Ремастер не должен вызывать конфликт версий")
	}
}

func TestMatcher_AlbumsMatch(t *testing.T) {
	// Совпадение альбомов без учета регистра и стоп-слов
	match, ratio := AlbumsMatch("The Dark Side of the Moon", "Dark Side of the Moon")
	if !match || ratio < 0.75 {
		t.Errorf("Ожидалось совпадение альбомов, got match=%v ratio=%v", match, ratio)
	}

	// Русскоязычные альбомы с пометкой переиздания
	match, _ = AlbumsMatch("Группа крови", "Группа Крови (Переиздание 1996)")
	if !match {
		t.Errorf("Ожидалось совпадение альбомов Кино с переизданием")
	}

	// Несовпадение номеров томов (Greatest Hits 1 vs Greatest Hits 2)
	match, _ = AlbumsMatch("Greatest Hits Vol. 1", "Greatest Hits Vol. 2")
	if match {
		t.Errorf("Не должны совпадать разные части сборника (Vol 1 vs Vol 2)")
	}
}

func TestMatcher_CensorshipDetection(t *testing.T) {
	if !IsCensored("I don't give a f**k about it") {
		t.Errorf("Ожидалось обнаружение цензуры в f**k")
	}
	if !IsCensored("Какого х*я ты тут делаешь") {
		t.Errorf("Ожидалось обнаружение цензуры в х*я")
	}
	if IsCensored("Я не могу дышать без тебя, отпускаю") {
		t.Errorf("В чистом тексте не должно быть обнаружено цензуры")
	}
}

func TestMatcher_ScoreCandidate(t *testing.T) {
	query := SearchQuery{
		Artist:   "МакСим",
		Title:    "Отпускаю",
		Album:    "Трудный возраст",
		Duration: 260 * time.Second,
	}

	// 1. Идеальный кандидат (Яндекс Музыка, синхронизированный LRC, точная длительность)
	score1 := ScoreCandidate(
		query,
		"МакSим",
		"Отпускаю",
		"Трудный возраст",
		261*time.Second,
		KindLRC,
		"[00:10.00]Я не могу дышать...",
		4*time.Second,
	)

	if !score1.Accepted || score1.Total < 90.0 {
		t.Errorf("Ожидался высший балл для идеального кандидата, got score=%+v", score1)
	}

	// 2. Чужой трек (ложный хит Genius из рэпа)
	score2 := ScoreCandidate(
		query,
		"Big Baby Tape",
		"Отпускаю дым",
		"Dragonborn",
		180*time.Second,
		KindPlain,
		"Биг бой как максим отпускаю дым",
		4*time.Second,
	)

	if score2.Accepted {
		t.Errorf("Ложный трек чужого артиста должен быть отклонен, got score=%+v", score2)
	}

	// 3. Конфликт версий (Live вместо студийного)
	score3 := ScoreCandidate(
		query,
		"МакСим",
		"Отпускаю (Live в Олимпийском)",
		"Трудный возраст",
		260*time.Second,
		KindLRC,
		"Текст песни...",
		4*time.Second,
	)

	if score3.Accepted {
		t.Errorf("Кандидат с конфликтом версий (Live) должен быть отклонен")
	}

	// 4. Цензурированный кандидат против нецензурированного
	scoreClean := ScoreCandidate(
		query,
		"МакСим",
		"Отпускаю",
		"Трудный возраст",
		260*time.Second,
		KindPlain,
		"Чистый оригинальный текст",
		4*time.Second,
	)
	scoreCensored := ScoreCandidate(
		query,
		"МакСим",
		"Отпускаю",
		"Трудный возраст",
		260*time.Second,
		KindPlain,
		"Текст со зв***чками f**k",
		4*time.Second,
	)

	if scoreCensored.Total >= scoreClean.Total {
		t.Errorf("Цензурированный кандидат должен иметь более низкий балл, got clean=%v, censored=%v",
			scoreClean.Total, scoreCensored.Total)
	}
}

func TestMatcher_DefectiveLyricsAndVariants(t *testing.T) {
	// 1. Дефектный текст из скриншота пользователя: начинается со 2-го куплета
	defective := `6 Contributors
[Куплет 2: Найк Брозов, Ruslana & Вместе]
О, жизнь, ты прекрасна
О, жизнь, ты прекрасна вполне`

	if !IsDefectiveLyrics(defective) {
		t.Errorf("Ожидалось, что дефектный текст (начинающийся со 2-го куплета) будет отбракован")
	}

	// 2. Нормальный текст песни
	normal := `[Куплет 1]
Я знаю сам, куда иду
[Припев]
О, жизнь, ты прекрасна вполне`
	if IsDefectiveLyrics(normal) {
		t.Errorf("Нормальный текст песни не должен считаться дефектным")
	}

	// 3. Заглушка
	if !IsDefectiveLyrics("Текст песни еще не добавлен") {
		t.Errorf("Заглушка отсутствия текста должна быть отбракована")
	}

	// 4. Генерация вариантов для МакСим
	vars := GenerateArtistVariants("максим")
	foundMakSim := false
	for _, v := range vars {
		if v == "МакSим" {
			foundMakSim = true
			break
		}
	}
	if !foundMakSim {
		t.Errorf("Для 'максим' должен быть сгенерирован вариант 'МакSим', получено: %v", vars)
	}
}

func TestGenius_CleanGeniusText(t *testing.T) {
	raw := `6 Contributors
Lyrics for Song
[Куплет 1]
Я знаю сам, куда иду
You might also like
[Припев]
О, жизнь, ты прекрасна
15Embed`

	cleaned := cleanGeniusText(raw)
	if strings.Contains(cleaned, "6 Contributors") {
		t.Errorf("В очищенном тексте осталась строка Contributors: %s", cleaned)
	}
	if strings.Contains(cleaned, "You might also like") {
		t.Errorf("В очищенном тексте осталась строка You might also like: %s", cleaned)
	}
	if strings.Contains(cleaned, "15Embed") {
		t.Errorf("В очищенном тексте остался Embed: %s", cleaned)
	}
	if !strings.Contains(cleaned, "Я знаю сам, куда иду") {
		t.Errorf("Потеряны полезные строки песни: %s", cleaned)
	}
}

// Транслитерированное имя исполнителя (МакSим / MakSim) допустимо только при точном совпадении
// названия и длительности (±2 сек); без совпадения длительности кандидат отклоняется.
func TestScoreCandidateWeakArtistNeedsStrongDuration(t *testing.T) {
	q := SearchQuery{Artist: "МакSим", Title: "Отпускаю", Duration: 261 * time.Second}

	ok := ScoreCandidate(q, "MakSim", "Отпускаю", "", 262*time.Second, KindLRC, "[00:01.00] a", 6*time.Second)
	if !ok.Accepted {
		t.Fatalf("кандидат с другим написанием артиста и той же длительностью должен приниматься, score=%+v", ok)
	}

	bad := ScoreCandidate(q, "MakSim", "Отпускаю", "", 270*time.Second, KindLRC, "[00:01.00] a", 6*time.Second)
	if bad.Accepted {
		t.Fatalf("кандидат с другим артистом и чужой длительностью должен отклоняться, score=%+v", bad)
	}

	other := ScoreCandidate(q, "Drake", "NOKIA", "", 0, KindLRC, "[00:01.00] a", 6*time.Second)
	if other.Accepted {
		t.Fatalf("чужой трек не должен приниматься, score=%+v", other)
	}
}
