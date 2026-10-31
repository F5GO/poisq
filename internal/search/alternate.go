// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл alternate.go реализует вычисление альтернативного поискового запроса (AlternateQuery)
// для параллельного fan-out опроса Navidrome: кросс-языковую транслитерацию (EN <-> RU),
// смену раскладки клавиатуры и нормализацию для гарантированного попадания в SQLite индексы.
package search

import "strings"

// ComputeAlternateQuery формирует оптимальный альтернативный запрос для Navidrome.
// Если исходный запрос был на латинице (например, "Na moey" или "Lune"), а целевая сущность на кириллице ("На Моей Луне"),
// альтернативным запросом становится прямая транслитерация на кириллицу ("на моей" / "луне").
// Если запрос на кириллице ("Нике"), а сущность на латинице ("Nickelback"),
// альтернативным запросом становится транслитерация на латиницу ("nike").
func ComputeAlternateQuery(rawQuery, rewrittenQuery string) string {
	cleanRaw := strings.TrimSpace(rawQuery)
	cleanRewritten := strings.TrimSpace(rewrittenQuery)

	if cleanRaw == "" {
		return ""
	}

	// 1. Проверяем точную смену раскладки (например "rbyj" <-> "кино")
	swapped := SwapLayout(cleanRaw)
	if strings.EqualFold(swapped, cleanRewritten) {
		return cleanRaw
	}

	rawHasCyr := HasCyrillic(cleanRaw)
	rawHasLat := HasLatin(cleanRaw)
	rewHasCyr := HasCyrillic(cleanRewritten)
	rewHasLat := HasLatin(cleanRewritten)

	// 2. Пользователь ввел латиницу ("Na moey", "Lune"), а сущность на кириллице ("На Моей Луне")
	if rawHasLat && rewHasCyr {
		translitRu := TransliterateEnToRu(cleanRaw)
		if translitRu != "" && !strings.EqualFold(translitRu, cleanRewritten) {
			return translitRu
		}
	}

	// 3. Пользователь ввел кириллицу ("Нике", "Линкин"), а сущность на латинице ("Nickelback", "Linkin Park")
	if rawHasCyr && rewHasLat {
		translitEn := TransliterateRuToEn(cleanRaw)
		if translitEn != "" && !strings.EqualFold(translitEn, cleanRewritten) {
			return translitEn
		}
	}

	// 4. Проверка смены раскладки ТОЛЬКО если раскладка дает кросс-языковое изменение (например "rbyj" -> "кино")
	if (rawHasLat && rewHasCyr) || (rawHasCyr && rewHasLat) {
		if !strings.EqualFold(swapped, cleanRaw) && !strings.EqualFold(swapped, cleanRewritten) {
			return swapped
		}
	}

	// 5. Если исходный запрос отличается от переписанного, отправляем исходный вторым потоком
	if !strings.EqualFold(cleanRaw, cleanRewritten) {
		return cleanRaw
	}

	return ""
}
