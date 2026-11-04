// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл library_match.go реализует умное сопоставление запроса с треками библиотеки
// для веб-админки poisq Studio. Раньше поиск был обычным SQL LIKE: он не различал регистр кириллицы
// и не находил «максим» среди «МакSим» (смешанные алфавиты), «maksim» среди «Максим», «symptom» среди «Sимптом».
// Теперь запрос и поля трека приводятся к одному виду (регистр, ё, латинские «двойники» в кириллических словах),
// сравниваются в двух алфавитах (кириллица и латиница-транслит) и допускают одну-две опечатки.
package search

import (
	"sort"
	"strings"
	"unicode"
)

// homoglyphToCyr — латинские буквы, визуально похожие на кириллические (для слов со смешанным алфавитом).
var homoglyphToCyr = map[rune]rune{
	'a': 'а', 'b': 'в', 'c': 'с', 'e': 'е', 'h': 'н', 'k': 'к', 'm': 'м', 'o': 'о',
	'p': 'р', 's': 'с', 't': 'т', 'x': 'х', 'y': 'у',
}

func isCyrRune(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }
func isLatRune(r rune) bool { return unicode.Is(unicode.Latin, r) }

// FoldText приводит строку к сравнимому виду: нижний регистр, ё→е, знаки препинания→пробелы,
// латинские двойники внутри кириллических слов («МакSим») заменяются кириллическими.
func FoldText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range words {
		hasC, hasL := false, false
		for _, r := range w {
			if isCyrRune(r) {
				hasC = true
			} else if isLatRune(r) {
				hasL = true
			}
		}
		if hasC && hasL {
			rs := []rune(w)
			for j, r := range rs {
				if isLatRune(r) {
					if c, ok := homoglyphToCyr[r]; ok {
						rs[j] = c
					}
				}
			}
			words[i] = string(rs)
		}
	}
	return strings.Join(words, " ")
}

// LibraryKey — подготовленный ключ трека для поиска: текст в кириллице и его латинская транслитерация.
type LibraryKey struct {
	cyr   string
	lat   string
	words []string
}

// BuildLibraryKey строит ключ по полям трека (артист, название, альбом, путь...).
func BuildLibraryKey(fields ...string) LibraryKey {
	cyr := FoldText(strings.Join(fields, " "))
	lat := TransliterateRuToEn(cyr)
	k := LibraryKey{cyr: cyr, lat: lat}
	seen := map[string]struct{}{}
	for _, src := range []string{cyr, lat} {
		for _, w := range strings.Fields(src) {
			if _, ok := seen[w]; !ok {
				seen[w] = struct{}{}
				k.words = append(k.words, w)
			}
		}
	}
	return k
}

// LibraryQuery — разобранный запрос со всеми вариантами (раскладка, транслитерация, фонетика).
type LibraryQuery struct {
	forms [][]string
}

// PrepareLibraryQuery строит варианты запроса пользователя. Пустой запрос подходит всему.
func PrepareLibraryQuery(q string) LibraryQuery {
	var lq LibraryQuery
	seen := map[string]struct{}{}
	addForm := func(s string) {
		f := FoldText(s)
		if f == "" {
			return
		}
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		lq.forms = append(lq.forms, strings.Fields(f))
	}
	addForm(q)
	for _, v := range GenerateVariants(q) {
		addForm(v)
	}
	return lq
}

func fuzzyLimit(tokenLen int) int {
	switch {
	case tokenLen <= 3:
		return 0
	case tokenLen <= 5:
		return 1
	default:
		return 2
	}
}

// Score возвращает оценку совпадения (0 — не подходит). Каждый токен варианта запроса должен найтись
// в ключе трека подстрокой либо с небольшой опечаткой в одном из слов.
func (lq LibraryQuery) Score(k LibraryKey) int {
	if len(lq.forms) == 0 {
		return 1
	}
	best := 0
	for fi, tokens := range lq.forms {
		total := 0
		ok := true
		for _, tok := range tokens {
			sc := tokenScore(tok, k)
			if sc == 0 {
				ok = false
				break
			}
			total += sc
		}
		if !ok {
			continue
		}
		// Исходный запрос (форма 0) ценнее производных вариантов
		if fi == 0 {
			total += 5
		}
		if total > best {
			best = total
		}
	}
	return best
}

func tokenScore(tok string, k LibraryKey) int {
	for _, src := range []string{k.cyr, k.lat} {
		if strings.Contains(" "+src+" ", " "+tok+" ") {
			return 16 // совпадение с целым словом
		}
	}
	for _, src := range []string{k.cyr, k.lat} {
		if idx := strings.Index(src, tok); idx >= 0 {
			if idx == 0 || src[idx-1] == ' ' {
				return 12 // совпадение с начала слова
			}
			return 8
		}
	}
	n := len([]rune(tok))
	limit := fuzzyLimit(n)
	if limit == 0 {
		return 0
	}
	for _, w := range k.words {
		wl := len([]rune(w))
		if wl+limit < n || n+limit < wl {
			continue
		}
		if PrefixDamerauLevenshteinDistance(tok, w) <= limit && DamerauLevenshteinDistance(tok, w) <= limit {
			return 4
		}
	}
	return 0
}

// RankedID — идентификатор записи и её оценка.
type RankedID struct {
	ID    string
	Score int
}

// RankByQuery оставляет подходящие записи и сортирует их по убыванию оценки (стабильно).
func RankByQuery(lq LibraryQuery, ids []string, keys []LibraryKey, limit int) []RankedID {
	var out []RankedID
	for i, k := range keys {
		if sc := lq.Score(k); sc > 0 {
			out = append(out, RankedID{ID: ids[i], Score: sc})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
