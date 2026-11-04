// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics).
//
// Данный файл variants.go реализует генерацию вариантов поискового запроса для «умного поиска»:
// один и тот же трек часто лежит в базах под другим написанием («МакSим» / «MakSim» / «Максим»),
// с лишними пометками в названии или с несколькими исполнителями в одной строке.
// Варианты строятся детерминированно (без LLM), поэтому работают мгновенно и без ключей.
package lyrics

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Разделители нескольких исполнителей: «A • B», «A feat. B», «A & B», «A, B», «A aka B», «A x B».
	multiArtistSplitRe = regexp.MustCompile(`(?i)\s*(?:•|·|,|;|/|&|\+|\bfeat\.?\b|\bft\.?\b|\bfeaturing\b|\baka\b|\bvs\.?\b|\bx\b|\bfeat\b|\s+и\s+)\s*`)
	// Содержимое скобок в названии: «Название (Remastered 2011)», «Название [feat. X]».
	bracketContentRe = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]\s*`)
	// Хвост после « - » / « – » в названии: «Название - Radio Edit».
	titleDashTailRe = regexp.MustCompile(`\s+[-–—]\s+.*$`)
)

// ruToLat — упрощённая транслитерация (близкая к тому, как артисты пишутся в международных базах).
var ruToLat = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r",
	'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// TransliterateRuToLat переводит кириллицу в латиницу; остальные символы сохраняются.
func TransliterateRuToLat(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if lat, ok := ruToLat[r]; ok {
			sb.WriteString(lat)
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// titleCaseWords делает «maksim» → «Maksim» для читаемого запроса.
func titleCaseWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// PrimaryArtist возвращает первого исполнителя из строки вида «A • B aka C».
func PrimaryArtist(artist string) string {
	parts := multiArtistSplitRe.Split(strings.TrimSpace(artist), 2)
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return strings.TrimSpace(artist)
}

// ArtistSearchVariants возвращает варианты написания исполнителя: исходный, канонические псевдонимы,
// смесь кириллицы/латиницы, первый из нескольких исполнителей и транслитерацию на латиницу.
func ArtistSearchVariants(artist string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}

	for _, v := range GenerateArtistVariants(artist) {
		add(v)
	}

	primary := PrimaryArtist(artist)
	if primary != "" && !strings.EqualFold(primary, artist) {
		for _, v := range GenerateArtistVariants(primary) {
			add(v)
		}
	}

	// Транслитерация нормализованного имени («МакSим» → «максим» → «Maksim»)
	for _, base := range []string{artist, primary} {
		norm := NormalizeTrackString(base)
		if norm != "" && hasCyrillic(norm) {
			add(titleCaseWords(TransliterateRuToLat(norm)))
		}
	}
	return out
}

// TitleSearchVariants возвращает варианты названия: исходное, очищенное от пометок изданий,
// без любых скобок и без хвоста « - Remastered / Radio Edit».
func TitleSearchVariants(title string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		k := strings.ToLower(v)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}

	add(title)
	add(CleanTitle(title))
	noBrackets := strings.TrimSpace(bracketContentRe.ReplaceAllString(title, " "))
	add(noBrackets)
	add(titleDashTailRe.ReplaceAllString(noBrackets, ""))
	return out
}

// GenerateQueryVariants строит дополнительные поисковые запросы (без исходного), не более max штук.
// Сначала меняется только исполнитель (самая частая причина промаха), затем только название,
// затем самые «агрессивные» комбинации.
func GenerateQueryVariants(q SearchQuery, max int) []SearchQuery {
	if max <= 0 {
		max = 4
	}
	artists := ArtistSearchVariants(q.Artist)
	titles := TitleSearchVariants(q.Title)

	seen := map[string]struct{}{strings.ToLower(q.Artist) + "|" + strings.ToLower(q.Title): {}}
	var out []SearchQuery
	add := func(artist, title string) {
		if len(out) >= max {
			return
		}
		k := strings.ToLower(artist) + "|" + strings.ToLower(title)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		v := q
		v.Artist = artist
		v.Title = title
		out = append(out, v)
	}

	for _, a := range artists {
		add(a, q.Title)
	}
	for _, t := range titles {
		add(q.Artist, t)
	}
	if len(artists) > 0 && len(titles) > 0 {
		add(artists[len(artists)-1], titles[len(titles)-1])
		add(artists[0], titles[len(titles)-1])
	}
	return out
}
