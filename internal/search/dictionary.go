// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл dictionary.go реализует потокобезопасный словарь библиотеки
// с поддержкой точного совпадения, префиксного автокомплита и нечеткого исправления опечаток.
package search

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// stopWords содержит служебные предлоги и артикли, которые не должны индексироваться как отдельные ключевые слова.
var stopWords = map[string]bool{
	"the": true, "зе": true, "де": true,
	"of": true, "оф": true,
	"and": true, "энд": true,
	"for": true, "фор": true,
	"in": true, "ин": true,
	"on": true, "он": true,
	"at": true, "эт": true,
	"to": true, "ту": true,
	"with": true,
	"a":    true, "an": true, "э": true,
	"la": true, "le": true, "les": true,
	"der": true, "die": true, "das": true,
	"und": true,
	"или": true, "под": true, "над": true,
}

// DictItem описывает элемент библиотеки (артист, альбом или трек), загружаемый в словарь.
type DictItem struct {
	Text       string
	EntityType string // "artist", "album", "track"
	PlayCount  int
}

// TokenEntry хранит информацию о слове или фразе словаря.
type TokenEntry struct {
	Key          string // Ключ поиска в нижнем регистре (например "офспринг", "offspring", "король")
	Canonical    string // Каноническое слово (например "Offspring", "Король")
	ParentEntity string // Полное название родительской сущности (например "The Offspring", "Король и Шут")
	Weight       int    // Вес ранжирования
	RuneLen      int    // Длина Key в рунах
}

// Dictionary инкапсулирует словарь слов и фраз медиатеки с потокобезопасным обновлением.
type Dictionary struct {
	mu            sync.RWMutex
	trie          *Trie
	exactMap      map[string]TokenEntry
	tokensByLen   map[int][]TokenEntry
	tokensByRune  map[rune][]TokenEntry // Индекс по первой руне для сверхбыстрого нечеткого префиксного поиска
	canonicalSet  map[string]bool
	libraryItems  []DictItem        // Сохраненные элементы медиатеки из SQLite
	activeAliases map[string]string // Актуальные алиасы из aliases.json
}

// NewDictionary создает пустой словарь.
func NewDictionary() *Dictionary {
	d := &Dictionary{
		trie:          NewTrie(),
		exactMap:      make(map[string]TokenEntry),
		tokensByLen:   make(map[int][]TokenEntry),
		tokensByRune:  make(map[rune][]TokenEntry),
		canonicalSet:  make(map[string]bool),
		activeAliases: make(map[string]string),
	}
	// Инициализируем словарь базовыми музыкальными алиасами
	d.Rebuild(nil)
	return d
}

// CalculateWeight вычисляет вес популярности сущности на основе ее типа и количества воспроизведений.
func CalculateWeight(entityType string, playCount int) int {
	base := 100
	switch entityType {
	case "artist":
		base = 1000
	case "album":
		base = 500
	case "track":
		base = 100
	}
	return base + playCount*5
}

// Rebuild полностью перестраивает структуру словаря на основе списка элементов из SQLite и актуальных алиасов.
func (d *Dictionary) Rebuild(items []DictItem) {
	d.mu.Lock()
	if items != nil {
		d.libraryItems = items
	} else {
		items = d.libraryItems
	}

	aliasesToUse := musicAliases
	if len(d.activeAliases) > 0 {
		aliasesToUse = d.activeAliases
	}
	d.mu.Unlock()

	newTrie := NewTrie()
	newExact := make(map[string]TokenEntry)
	newTokensByLen := make(map[int][]TokenEntry)
	newTokensByRune := make(map[rune][]TokenEntry)
	newCanonicalSet := make(map[string]bool)
	seenKeyParent := make(map[string]int)

	addEntry := func(searchKey, canonical, parentEntity string, weight int) {
		k := strings.ToLower(strings.TrimSpace(searchKey))
		if k == "" {
			return
		}

		kp := k + "|" + parentEntity
		if prevWeight, ok := seenKeyParent[kp]; ok && prevWeight >= weight {
			return
		}
		seenKeyParent[kp] = weight

		runeLen := utf8.RuneCountInString(k)
		runes := []rune(k)
		firstRune := runes[0]

		entry := TokenEntry{
			Key:          k,
			Canonical:    canonical,
			ParentEntity: parentEntity,
			Weight:       weight,
			RuneLen:      runeLen,
		}

		// Добавляем в Trie
		newTrie.Insert(k, canonical, parentEntity, weight)

		// Добавляем в exactMap
		if existing, exists := newExact[k]; !exists || weight > existing.Weight {
			newExact[k] = entry
		}

		// Добавляем в индекс по длине
		newTokensByLen[runeLen] = append(newTokensByLen[runeLen], entry)

		// Добавляем в индекс по первой руне
		newTokensByRune[firstRune] = append(newTokensByRune[firstRune], entry)
	}

	addItem := func(phrase, canonical, parentEntity string, weight int) {
		clean := strings.TrimSpace(phrase)
		if clean == "" {
			return
		}

		// Варианты полной фразы
		for _, variant := range GenerateVariants(clean) {
			addEntry(variant, canonical, parentEntity, weight)
		}

		// Разбиваем фразу на отдельные слова, исключая предлоги и артикли
		words := strings.Fields(clean)
		if len(words) > 1 {
			for _, w := range words {
				wClean := strings.Trim(w, ",.-_!?'\"()[]{}")
				wLower := strings.ToLower(wClean)
				if utf8.RuneCountInString(wClean) < 2 || stopWords[wLower] {
					continue
				}

				for _, wVariant := range GenerateVariants(wClean) {
					addEntry(wVariant, wClean, parentEntity, weight)
				}
			}
		}
	}

	// 1. Индексируем актуальные музыкальные алиасы (встроенные + из aliases.json)
	for alias, canonical := range aliasesToUse {
		addItem(alias, canonical, canonical, 800)
		addItem(canonical, canonical, canonical, 800)
	}

	// 2. Индексируем элементы из базы данных Navidrome (приоритет выше встроенных алиасов)
	for _, item := range items {
		weight := CalculateWeight(item.EntityType, item.PlayCount)
		newCanonicalSet[item.Text] = true
		addItem(item.Text, item.Text, item.Text, weight)
	}

	d.mu.Lock()
	d.trie = newTrie
	d.exactMap = newExact
	d.tokensByLen = newTokensByLen
	d.tokensByRune = newTokensByRune
	d.canonicalSet = newCanonicalSet
	d.mu.Unlock()
}

// UpdateAliases обновляет активную таблицу алиасов и перестраивает словарь на лету.
func (d *Dictionary) UpdateAliases(newAliases map[string]string) {
	d.mu.Lock()
	merged := make(map[string]string, len(musicAliases)+len(newAliases))
	for k, v := range musicAliases {
		merged[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for k, v := range newAliases {
		merged[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	d.activeAliases = merged
	d.mu.Unlock()

	d.Rebuild(nil)
}

// FindExact ищет точное совпадение токена в словаре.
func (d *Dictionary) FindExact(word string) (TokenEntry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	entry, ok := d.exactMap[strings.ToLower(strings.TrimSpace(word))]
	return entry, ok
}

// SearchPrefix ищет совпадения по префиксу через префиксное дерево Trie.
func (d *Dictionary) SearchPrefix(prefix string, limit int) []TrieMatch {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.trie.SearchPrefix(prefix, limit)
}

// FindFuzzy ищет ближайшее слово по полному расстоянию Дамерау-Левенштейна.
// Возвращает канонический токен, расстояние и признак успешности поиска.
func (d *Dictionary) FindFuzzy(queryToken string, maxDistance int) (TokenEntry, int, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	lower := strings.ToLower(strings.TrimSpace(queryToken))
	if entry, ok := d.exactMap[lower]; ok {
		return entry, 0, true
	}

	if maxDistance <= 0 {
		return TokenEntry{}, 0, false
	}

	queryLen := utf8.RuneCountInString(lower)
	var bestEntry TokenEntry
	bestDist := maxDistance + 1
	bestWeight := -1

	// Проверяем только слова с длиной [queryLen - maxDistance, queryLen + maxDistance]
	for l := queryLen - maxDistance; l <= queryLen+maxDistance; l++ {
		tokens, ok := d.tokensByLen[l]
		if !ok {
			continue
		}

		for _, candidate := range tokens {
			dist := DamerauLevenshteinDistance(lower, candidate.Key)
			if dist <= maxDistance {
				if dist < bestDist || (dist == bestDist && candidate.Weight > bestWeight) {
					bestDist = dist
					bestEntry = candidate
					bestWeight = candidate.Weight
				}
			}
		}
	}

	if bestWeight != -1 && bestDist <= maxDistance {
		return bestEntry, bestDist, true
	}

	return TokenEntry{}, 0, false
}

// FindFuzzyPrefix ищет префиксное совпадение токена с учетом опечаток.
// Например, для запроса "короы" (5 рун) находит "король" (префикс "корол" отличается на 1 руну: 'ы' -> 'л').
func (d *Dictionary) FindFuzzyPrefix(queryToken string, maxDistance int) (TokenEntry, int, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	lower := strings.ToLower(strings.TrimSpace(queryToken))
	queryRunes := []rune(lower)
	queryLen := len(queryRunes)
	if queryLen == 0 {
		return TokenEntry{}, 0, false
	}

	firstRune := queryRunes[0]
	var bestEntry TokenEntry
	bestDist := maxDistance + 1
	bestWeight := -1

	checkCandidates := func(candidates []TokenEntry) {
		for _, cand := range candidates {
			if cand.RuneLen < queryLen {
				continue
			}

			// Вычисляем расстояние Дамерау-Левенштейна между запросом и префиксом кандидата
			candRunes := []rune(cand.Key)
			candPrefix := string(candRunes[:queryLen])
			dist := DamerauLevenshteinDistance(lower, candPrefix)

			if dist <= maxDistance {
				if dist < bestDist || (dist == bestDist && cand.Weight > bestWeight) {
					bestDist = dist
					bestEntry = cand
					bestWeight = cand.Weight
				}
			}
		}
	}

	// 1. Быстрая проверка кандидатов с совпадающей первой буквой
	if candidates, ok := d.tokensByRune[firstRune]; ok {
		checkCandidates(candidates)
	}

	// Если найдено совпадение с расстоянием 0 или 1, сразу возвращаем его
	if bestWeight != -1 && bestDist <= 1 {
		return bestEntry, bestDist, true
	}

	// 2. Если ничего не найдено, проверяем остальные буквы (на случай опечатки в 1-м символе)
	if bestWeight == -1 && maxDistance >= 1 {
		for r, candidates := range d.tokensByRune {
			if r == firstRune {
				continue
			}
			checkCandidates(candidates)
			if bestWeight != -1 && bestDist == 1 {
				break
			}
		}
	}

	if bestWeight != -1 && bestDist <= maxDistance {
		return bestEntry, bestDist, true
	}

	return TokenEntry{}, 0, false
}

// TotalWords возвращает текущее количество уникальных поисковых ключей в словаре.
func (d *Dictionary) TotalWords() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.exactMap)
}
