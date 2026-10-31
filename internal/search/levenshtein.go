// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл levenshtein.go реализует быстрый алгоритм расстояния Дамерау-Левенштейна
// (учитывающий вставки, удаления, замены и перестановки соседних символов/runes).
package search

// DamerauLevenshteinDistance вычисляет расстояние Дамерау-Левенштейна между двумя строками в рунах.
// Поддерживает корректную работу с UTF-8 (русские и английские символы).
func DamerauLevenshteinDistance(a, b string) int {
	runesA := []rune(a)
	runesB := []rune(b)

	lenA := len(runesA)
	lenB := len(runesB)

	if lenA == 0 {
		return lenB
	}
	if lenB == 0 {
		return lenA
	}

	// Ограничиваем аллокации матрицей (lenA + 2) x (lenB + 2)
	maxDist := lenA + lenB
	h := make([][]int, lenA+2)
	for i := range h {
		h[i] = make([]int, lenB+2)
	}

	h[0][0] = maxDist
	for i := 0; i <= lenA; i++ {
		h[i+1][0] = maxDist
		h[i+1][1] = i
	}
	for j := 0; j <= lenB; j++ {
		h[0][j+1] = maxDist
		h[1][j+1] = j
	}

	da := make(map[rune]int)

	for i := 1; i <= lenA; i++ {
		db := 0
		for j := 1; j <= lenB; j++ {
			i1 := da[runesB[j-1]]
			j1 := db
			cost := 1
			if runesA[i-1] == runesB[j-1] {
				cost = 0
				db = j
			}

			// Операции: удаление, вставка, замена
			subCost := h[i][j] + cost
			insCost := h[i+1][j] + 1
			delCost := h[i][j+1] + 1

			minVal := subCost
			if insCost < minVal {
				minVal = insCost
			}
			if delCost < minVal {
				minVal = delCost
			}

			// Транспозиция
			transCost := h[i1][j1] + (i - i1 - 1) + 1 + (j - j1 - 1)
			if transCost < minVal {
				minVal = transCost
			}

			h[i+1][j+1] = minVal
		}
		da[runesA[i-1]] = i
	}

	return h[lenA+1][lenB+1]
}

// PrefixDamerauLevenshteinDistance вычисляет расстояние Дамерау-Левенштейна между строкой query
// и префиксом целевой строки target той же длины в рунах.
func PrefixDamerauLevenshteinDistance(query, target string) int {
	runesQuery := []rune(query)
	runesTarget := []rune(target)

	if len(runesTarget) < len(runesQuery) {
		return DamerauLevenshteinDistance(query, target)
	}

	targetPrefix := string(runesTarget[:len(runesQuery)])
	return DamerauLevenshteinDistance(query, targetPrefix)
}

// AllowedMaxDistance возвращает допустимое расстояние для нечеткого поиска по длине слова:
// для 1-2 рун: 0 (только префиксный поиск)
// для 3-4 рун: 1 (допускается 1 опечатка)
// для 5+ рун: 2 (допускается до 2 опечаток)
func AllowedMaxDistance(runeLength int) int {
	if runeLength <= 2 {
		return 0
	}
	if runeLength <= 4 {
		return 1
	}
	return 2
}
