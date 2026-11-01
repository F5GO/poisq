// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл matcher.go реализует интеллектуальный алгоритм нормализации, сопоставления и ранжирования
// кандидатов текстов песен на основе принципов nd-lyrics с расширением для русскоязычной музыки:
// - Очистка служебных пометок изданий (Deluxe, Remastered, Юбилейное издание и т.д.);
// - Строгий контроль несовпадения маркеров версий (Live, Acoustic, Remix, Концертная версия);
// - Сопоставление альбомов с учетом стоп-слов, числовых токенов и расстояния Левенштейна;
// - Сверка хронометража с настраиваемым допуском (duration tolerance);
// - Обнаружение цензуры (звездочки f**k, б**дь) с приоритетом оригинальных нецензурированных текстов;
// - Комплексная оценка качества кандидата (CandidateScore от 0 до 100) для выбора наилучшего текста.
package lyrics

import (
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

var (
	// Регулярные выражения для базовой очистки и токенизации
	punctRegex      = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	spacesRegex     = regexp.MustCompile(`\s+`)
	dashRegex       = regexp.MustCompile(`[\-‐‒–—―]`)
	openBracketSet  = "([{\uFF08\u3010" // (, [, {, （, 【
	closeBracketSet = ")]}\uFF09\u3011" // ), ], }, ）, 】
)

// Безопасные термины изданий на английском и русском языках (их можно безболезненно отсекать в скобках)
var safeEditionTerms = map[string]struct{}{
	// Английские термины (из nd-lyrics)
	"deluxe": {}, "edition": {}, "remaster": {}, "remastered": {}, "anniversary": {},
	"expanded": {}, "bonus": {}, "special": {}, "explicit": {}, "clean": {},
	"mono": {}, "stereo": {}, "digital": {}, "vinyl": {}, "reissue": {},
	"version": {}, "ver": {}, "collectors": {}, "disc": {}, "cd": {},
	"part": {}, "volume": {}, "vol": {}, "track": {}, "tracks": {},
	"super": {}, "box": {}, "set": {}, "complete": {}, "ultimate": {},
	"definitive": {}, "legacy": {}, "platinum": {}, "gold": {}, "single": {},
	"ep": {}, "album": {}, "lp": {},
	// Русскоязычные термины
	"делюкс": {}, "издание": {}, "ремастер": {}, "ремастеринг": {}, "юбилейное": {},
	"расширенное": {}, "бонус": {}, "специальное": {}, "версия": {}, "переиздание": {},
	"моно": {}, "стерео": {}, "цифровое": {}, "винил": {}, "коллекционное": {},
	"диск": {}, "часть": {}, "том": {}, "трек": {}, "треки": {}, "полное": {},
	"сингл": {}, "альбом": {}, "сборник": {},
}

// Маркеры версий (если трек концертный, акустический или ремикс — это критично учитывать!)
var versionMarkers = map[string]struct{}{
	// Английские
	"live": {}, "remix": {}, "acoustic": {}, "instrumental": {}, "demo": {},
	"karaoke": {}, "cover": {}, "unplugged": {}, "session": {}, "alternate": {},
	"bootleg": {}, "extended": {}, "orchestral": {}, "reimagined": {},
	"slowed": {}, "reverb": {}, "speedup": {},
	// Русские
	"лайв": {}, "концерт": {}, "концертная": {}, "ремикс": {}, "акустика": {},
	"акустическая": {}, "инструментал": {}, "инструментальная": {}, "минусовка": {},
	"демо": {}, "караоке": {}, "кавер": {}, "оркестровая": {},
}

// Стоп-слова для сопоставления альбомов
var albumStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "of": {}, "&": {},
	"и": {}, "в": {}, "на": {}, "с": {}, "со": {}, "о": {}, "об": {}, "от": {}, "для": {},
}

// CandidateScore содержит структурированную оценку совпадения кандидата.
type CandidateScore struct {
	Total         float64 // Итоговый балл (0.0 — 100.0)
	TitleScore    float64 // Балл совпадения названия (0.0 — 1.0)
	ArtistScore   float64 // Балл совпадения исполнителя (0.0 — 1.0)
	DurationScore float64 // Балл совпадения хронометража (0.0 — 1.0)
	AlbumScore    float64 // Балл совпадения альбома (0.0 — 1.0)
	SyncBonus     float64 // Бонус синхронизации (LRC = 1.0, Plain = 0.4)
	IsCensored    bool    // Флаг обнаружения цензуры
	Accepted      bool    // Принят ли кандидат (прошел базовые фильтры)
}

// CleanTitle очищает название композиции по аналогии с nd-lyrics:
// отсекает скобки, содержащие исключительно пометки изданий (Deluxe, Remastered 2020),
// но сохраняет маркеры версий (Live, Acoustic, Remix) и очищает завершающие разделители.
func CleanTitle(title string) string {
	runes := []rune(title)
	n := len(runes)
	var out []rune
	i := 0

	for i < n {
		r := runes[i]
		if strings.ContainsRune(openBracketSet, r) {
			closeRune := matchingCloseBracket(r)
			end := -1
			depth := 1
			for j := i + 1; j < n; j++ {
				if runes[j] == r {
					depth++
				} else if runes[j] == closeRune {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}

			if end != -1 {
				inner := string(runes[i+1 : end])
				if isSafeToDiscardBracketContent(inner) {
					// Пропускаем содержимое скобок (это чисто пометка издания/ремастера)
					i = end + 1
					continue
				}
				// Если внутри есть полезная информация (версия, фит) — разворачиваем без скобок
				out = append(out, ' ')
				out = append(out, runes[i+1:end]...)
				out = append(out, ' ')
				i = end + 1
				continue
			}
		}

		out = append(out, r)
		i++
	}

	result := string(out)
	// Отсекаем завершающие разделители вида " - " если за ними ничего не осталось
	parts := dashRegex.Split(result, -1)
	if len(parts) > 0 {
		result = parts[0]
	}

	return strings.TrimSpace(spacesRegex.ReplaceAllString(result, " "))
}

func matchingCloseBracket(open rune) rune {
	switch open {
	case '(':
		return ')'
	case '[':
		return ']'
	case '{':
		return '}'
	case '\uFF08': // （
		return '\uFF09' // ）
	case '\u3010': // 【
		return '\u3011' // 】
	default:
		return ')'
	}
}

// isSafeToDiscardBracketContent проверяет, состоит ли содержимое скобок только из безопасных терминов издания и цифр.
func isSafeToDiscardBracketContent(s string) bool {
	words := strings.Fields(punctRegex.ReplaceAllString(strings.ToLower(s), " "))
	if len(words) == 0 {
		return false
	}

	for _, w := range words {
		// Если слово является маркером версии (live, acoustic, remix) — удалять нельзя!
		if _, isVer := versionMarkers[w]; isVer {
			return false
		}
		// Если слово — число (год издания 2011, 2024) — безопасно
		if isNumericToken(w) {
			continue
		}
		// Если слово в списке терминов издания — безопасно
		if _, isSafe := safeEditionTerms[w]; isSafe {
			continue
		}
		return false
	}
	return true
}

func isNumericToken(w string) bool {
	for _, r := range w {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// isMixedScriptWord проверяет, содержит ли слово одновременно кириллические и латинские символы.
func isMixedScriptWord(s string) bool {
	hasCyrillic := false
	hasLatin := false
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			hasCyrillic = true
		} else if unicode.Is(unicode.Latin, r) {
			hasLatin = true
		}
		if hasCyrillic && hasLatin {
			return true
		}
	}
	return false
}

// ExtractVersionMarkers возвращает список маркеров версий (live, remix, acoustic и т.д.), найденных в строке.
func ExtractVersionMarkers(s string) map[string]struct{} {
	norm := NormalizeTrackString(s)
	words := strings.Fields(norm)
	found := make(map[string]struct{})

	for _, w := range words {
		if _, ok := versionMarkers[w]; ok {
			found[w] = struct{}{}
		}
	}
	return found
}

// HasVersionConflict проверяет конфликт версий между запросом и кандидатом (ключевая логика nd-lyrics).
// Если в запросе нет маркера "live", а в кандидате он есть — это конфликт!
// Пользователь, включивший студийную запись, не должен получать текст концертного выступления.
func HasVersionConflict(queryTitle, candTitle string) bool {
	qMarkers := ExtractVersionMarkers(queryTitle)
	cMarkers := ExtractVersionMarkers(candTitle)

	// 1. Проверяем маркеры кандидата, которых нет в запросе
	for m := range cMarkers {
		if _, ok := qMarkers[m]; !ok {
			return true // Кандидат имеет лишний маркер (например, Live или Remix)
		}
	}

	// 2. Проверяем маркеры запроса, которых нет в кандидате
	for m := range qMarkers {
		if _, ok := cMarkers[m]; !ok {
			return true // Запрос требовал Live/Remix, а кандидат студийный
		}
	}

	return false
}

// NormalizeTrackString нормализует строку для нечеткого сравнения:
// - приводит к нижнему регистру;
// - нормализует смешанную кириллицу/латиницу (например, "МакSим" -> "максим");
// - удаляет знаки препинания;
// - сжимает пробелы.
func NormalizeTrackString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	s = strings.ToLower(s)
	s = punctRegex.ReplaceAllString(s, " ")
	words := strings.Fields(s)

	var normalizedWords []string
	for _, w := range words {
		// Нормализуем латинские омоглифы только если слово является смешанным (кириллица + латиница, например "макsим")
		if isMixedScriptWord(w) {
			var sb strings.Builder
			for _, r := range w {
				switch r {
				case 's':
					sb.WriteRune('с')
				case 'a':
					sb.WriteRune('а')
				case 'e':
					sb.WriteRune('е')
				case 'o':
					sb.WriteRune('о')
				case 'p':
					sb.WriteRune('р')
				case 'x':
					sb.WriteRune('х')
				case 'c':
					sb.WriteRune('с')
				case 'y':
					sb.WriteRune('у')
				default:
					sb.WriteRune(r)
				}
			}
			normalizedWords = append(normalizedWords, sb.String())
		} else {
			normalizedWords = append(normalizedWords, w)
		}
	}

	return strings.Join(normalizedWords, " ")
}

// LevenshteinDistance вычисляет редакционное расстояние между двумя строками (с поддержкой Unicode).
func LevenshteinDistance(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	n, m := len(ar), len(br)

	if n == 0 {
		return m
	}
	if m == 0 {
		return n
	}

	prev := make([]int, m+1)
	curr := make([]int, m+1)

	for j := 0; j <= m; j++ {
		prev[j] = j
	}

	for i := 1; i <= n; i++ {
		curr[0] = i
		for j := 1; j <= m; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		copy(prev, curr)
	}

	return prev[m]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// WordSimilar проверяет схожесть отдельных слов (алгоритм nd-lyrics: dist * 5 <= max_len).
func WordSimilar(a, b string) bool {
	if a == b {
		return true
	}
	ar := []rune(a)
	br := []rune(b)
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen < 4 {
		return false
	}
	return LevenshteinDistance(a, b)*5 <= maxLen
}

// MatchArtist оценивает степень совпадения исполнителей с учетом коллабораций (feat/&).
func MatchArtist(queryArtist, candArtist string) bool {
	q := NormalizeTrackString(queryArtist)
	c := NormalizeTrackString(candArtist)

	if q == "" || c == "" {
		return false
	}
	if q == c {
		return true
	}

	// Проверяем начало/конец строки (например, "МакСим & Баста", "Виктор Цой и Кино")
	if strings.HasPrefix(c, q+" ") || strings.HasPrefix(q, c+" ") ||
		strings.HasSuffix(c, " "+q) || strings.HasSuffix(q, " "+c) {
		return true
	}

	if strings.Contains(c, " "+q+" ") || strings.Contains(q, " "+c+" ") {
		return true
	}

	// Проверка первого исполнителя
	qFirst := strings.Split(q, " ")[0]
	cFirst := strings.Split(c, " ")[0]
	if len(qFirst) >= 4 && qFirst == cFirst {
		return true
	}

	return false
}

// MatchTitle проверяет совпадение названия трека с учетом нормализации и вариаций.
func MatchTitle(queryTitle, candTitle string) bool {
	q := NormalizeTrackString(CleanTitle(queryTitle))
	c := NormalizeTrackString(CleanTitle(candTitle))

	if q == "" || c == "" {
		return false
	}
	if q == c {
		return true
	}

	if strings.HasPrefix(c, q+" ") || strings.HasPrefix(q, c+" ") {
		return true
	}

	qWords := strings.Fields(q)
	cWords := strings.Fields(c)
	if len(qWords) > 1 && len(cWords) > 1 {
		matchedWords := 0
		for _, qw := range qWords {
			if len(qw) <= 2 {
				continue
			}
			for _, cw := range cWords {
				if WordSimilar(qw, cw) {
					matchedWords++
					break
				}
			}
		}
		if matchedWords >= len(qWords)/2+1 && math.Abs(float64(len(qWords)-len(cWords))) <= 2 {
			return true
		}
	}

	return false
}

// AlbumsMatch проверяет совпадение названий альбомов по алгоритму nd-lyrics:
// очистка пометок изданий (CleanTitle), исключение стоп-слов, строгое совпадение числовых токенов (Vol 1 vs Vol 2) и порог схожести 75%.
func AlbumsMatch(queryAlbum, candAlbum string) (bool, float64) {
	qClean := CleanTitle(queryAlbum)
	cClean := CleanTitle(candAlbum)

	q := NormalizeTrackString(qClean)
	c := NormalizeTrackString(cClean)

	if q == "" || c == "" {
		return true, 0.5 // Альбом не указан — нейтральный результат
	}
	if q == c {
		return true, 1.0
	}

	qWords := filterStopwords(strings.Fields(q))
	cWords := filterStopwords(strings.Fields(c))

	// Числовые токены должны строго совпадать
	qNums := extractNumericTokens(qWords)
	cNums := extractNumericTokens(cWords)
	if !equalNumberSets(qNums, cNums) {
		return false, 0.0
	}

	// Очищаем слова от служебных терминов издания
	qPure := filterSafeEditionWords(qWords)
	cPure := filterSafeEditionWords(cWords)

	smaller, larger := qPure, cPure
	if len(qPure) > len(cPure) {
		smaller, larger = cPure, qPure
	}

	if len(smaller) == 0 {
		return len(larger) == 0, 1.0
	}

	if len(smaller) < 2 && len(qNums) == 0 {
		if len(larger) == 1 && WordSimilar(smaller[0], larger[0]) {
			return true, 1.0
		}
		return false, 0.0
	}

	matched := 0
	for _, sw := range smaller {
		for _, lw := range larger {
			if WordSimilar(sw, lw) {
				matched++
				break
			}
		}
	}

	ratio := float64(matched) / float64(len(smaller))
	return ratio >= 0.75, ratio
}

func filterStopwords(words []string) []string {
	var res []string
	for _, w := range words {
		if _, isStop := albumStopwords[w]; !isStop {
			res = append(res, w)
		}
	}
	return res
}

func filterSafeEditionWords(words []string) []string {
	var res []string
	for _, w := range words {
		if isNumericToken(w) {
			continue
		}
		if _, isSafe := safeEditionTerms[w]; isSafe {
			continue
		}
		res = append(res, w)
	}
	return res
}

func extractNumericTokens(words []string) []string {
	var res []string
	for _, w := range words {
		if isNumericToken(w) {
			res = append(res, w)
		}
	}
	return res
}

func equalNumberSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int)
	for _, n := range a {
		counts[n]++
	}
	for _, n := range b {
		counts[n]--
		if counts[n] < 0 {
			return false
		}
	}
	return true
}

// IsCensored проверяет, содержит ли текст песни признаки цензуры (звездочки f**k, б**дь).
func IsCensored(lyricsText string) bool {
	if !strings.Contains(lyricsText, "*") {
		return false
	}
	runes := []rune(lyricsText)
	for i, r := range runes {
		if r == '*' {
			if (i > 0 && unicode.IsLetter(runes[i-1])) || (i < len(runes)-1 && unicode.IsLetter(runes[i+1])) {
				return true
			}
		}
	}
	return false
}

// ScoreCandidate выполняет комплексную оценку кандидата текста песни.
// Возвращает оценку CandidateScore и признак допустимости кандидата.
func ScoreCandidate(
	query SearchQuery,
	candArtist, candTitle, candAlbum string,
	candDuration time.Duration,
	kind LyricsKind,
	lyricsText string,
	tolerance time.Duration,
) CandidateScore {
	var score CandidateScore

	// 1. Контроль несовпадения маркеров версий (Live/Remix/Acoustic)
	if HasVersionConflict(query.Title, candTitle) {
		score.Accepted = false
		return score
	}

	// 2. Оценка исполнителя
	if candArtist != "" {
		if MatchArtist(query.Artist, candArtist) {
			score.ArtistScore = 1.0
		} else {
			score.Accepted = false
			return score
		}
	} else {
		score.ArtistScore = 0.7 // Если провайдер не вернул имя артиста отдельно
	}

	// 3. Оценка названия
	if MatchTitle(query.Title, candTitle) {
		score.TitleScore = 1.0
	} else {
		score.Accepted = false
		return score
	}

	// 4. Оценка длительности трека
	if query.Duration > 0 && candDuration > 0 {
		diff := math.Abs(query.Duration.Seconds() - candDuration.Seconds())
		tolSec := tolerance.Seconds()
		if tolSec <= 0 {
			tolSec = 4.0
		}

		if diff <= 1.0 {
			score.DurationScore = 1.0
		} else if diff <= tolSec {
			score.DurationScore = 1.0 - (diff / (tolSec * 2.0))
		} else {
			// Выход за пределы погрешности длительности
			score.Accepted = false
			return score
		}
	} else {
		score.DurationScore = 0.8 // Длительность не задана
	}

	// 5. Оценка альбома
	if query.Album != "" && candAlbum != "" {
		match, ratio := AlbumsMatch(query.Album, candAlbum)
		if !match {
			score.AlbumScore = 0.2
		} else {
			score.AlbumScore = ratio
		}
	} else {
		score.AlbumScore = 0.5
	}

	// 6. Формат текста (LRC / Instrumental / Plain)
	if kind == KindLRC || kind == KindInstrumental {
		score.SyncBonus = 1.0
	} else {
		score.SyncBonus = 0.45
	}

	// 7. Обнаружение цензуры
	score.IsCensored = IsCensored(lyricsText)

	// Итоговый расчет весов (макс. 100 баллов)
	// Title (30%) + Artist (25%) + Sync (25%) + Duration (15%) + Album (5%)
	total := (score.TitleScore * 30.0) +
		(score.ArtistScore * 25.0) +
		(score.SyncBonus * 25.0) +
		(score.DurationScore * 15.0) +
		(score.AlbumScore * 5.0)

	if score.IsCensored {
		total -= 10.0 // Штраф за цензуру в пользу оригинальной версии
	}

	score.Total = math.Max(0.0, total)
	score.Accepted = score.Total >= 60.0

	return score
}

// MatchTrack производит базовую экспресс-валидацию кандидата: совпадение исполнителя и названия.
func MatchTrack(query SearchQuery, candArtist, candTitle string) bool {
	if HasVersionConflict(query.Title, candTitle) {
		return false
	}
	if candArtist != "" && !MatchArtist(query.Artist, candArtist) {
		return false
	}
	if !MatchTitle(query.Title, candTitle) {
		return false
	}
	return true
}

// IsRussianText проверяет, содержит ли текст символы кириллицы.
func IsRussianText(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
