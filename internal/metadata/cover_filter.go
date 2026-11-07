// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_filter.go реализует строгую фильтрацию и оценку релевантности обложек из iTunes
// и Cover Art Archive. Полностью отсекает караоке, инструментальные версии, трибьюты и сторонние кавер-группы.
package metadata

import (
	"strings"
)

// DisallowedCoverKeywords содержит стоп-слова и маркеры нерелевантных релизов
// (караоке-лейблы, трибьюты, рингтоны, минусовки).
var DisallowedCoverKeywords = []string{
	"karaoke", "караоке",
	"tribute", "трибьют",
	"instrumental", "инструментал", "инструментальная",
	"backing track", "backing tracks",
	"минусовка", "минус",
	"originally performed", "originally by",
	"in the style of", "made famous by",
	"soundalike", "sound-a-like",
	"ringtone", "рингтон",
	"play along", "play-along",
	"performance track", "karaoke version",
	"cover version", "acoustic tribute",
	"piano tribute", "lullaby", "tribute band",
	"party tyme", "ameritz", "sing2piano", "karafun", "hit crew",
	"the karaoke", "prosound karaoke", "sing karaoke",
}

var genericArtistNames = map[string]bool{
	"various artists":     true,
	"разные исполнители":  true,
	"soundtrack":          true,
	"саундтрек":           true,
	"va":                  true,
	"v.a.":                true,
	"original soundtrack": true,
	"ost":                 true,
}

// CleanTargetAlbum очищает название альбома от повторения имени артиста
// (например, "Green Day – Boulevard of Broken Dreams" -> "Boulevard of Broken Dreams").
func CleanTargetAlbum(artist, album string) string {
	a := strings.TrimSpace(album)
	art := strings.TrimSpace(artist)
	if art == "" || a == "" {
		return a
	}

	normArt := strings.ToLower(art)
	normAlb := strings.ToLower(a)

	separators := []string{" - ", " – ", " — ", ": "}
	for _, sep := range separators {
		prefix := normArt + sep
		if strings.HasPrefix(normAlb, prefix) {
			return strings.TrimSpace(a[len(prefix):])
		}
	}
	return a
}

// IsDisallowedCoverRelease проверяет, содержит ли кандидат стоп-слова караоке/трибьютов,
// если они отсутствуют в оригинальном названии альбома или артиста.
func IsDisallowedCoverRelease(candArtist, candAlbum, targetArtist, targetAlbum string) bool {
	normCandArtist := strings.ToLower(strings.TrimSpace(candArtist))
	normCandAlbum := strings.ToLower(strings.TrimSpace(candAlbum))
	normTgtArtist := strings.ToLower(strings.TrimSpace(targetArtist))
	normTgtAlbum := strings.ToLower(strings.TrimSpace(targetAlbum))

	for _, kw := range DisallowedCoverKeywords {
		if strings.Contains(normCandAlbum, kw) && !strings.Contains(normTgtAlbum, kw) {
			return true
		}
		if strings.Contains(normCandArtist, kw) && !strings.Contains(normTgtArtist, kw) {
			return true
		}
	}
	return false
}

// ScoreCoverCandidate рассчитывает оценку соответствия кандидата целевому альбому.
// Если артист кандидата принципиально не совпадает с целевым артистом — возвращает valid=false.
func ScoreCoverCandidate(candArtist, candAlbum, targetArtist, targetAlbum string) (valid bool, score int) {
	normCandArtist := strings.ToLower(strings.TrimSpace(candArtist))
	normCandAlbum := strings.ToLower(strings.TrimSpace(candAlbum))
	normTgtArtist := strings.ToLower(strings.TrimSpace(targetArtist))

	cleanAlbum := CleanTargetAlbum(targetArtist, targetAlbum)
	normCleanAlbum := strings.ToLower(cleanAlbum)

	if IsDisallowedCoverRelease(candArtist, candAlbum, targetArtist, targetAlbum) {
		return false, 0
	}

	isGeneric := genericArtistNames[normTgtArtist]

	// 1. Проверка артиста (если артист известен и не является сборником, совпадение обязательно!)
	artistMatchScore := 0
	if !isGeneric && normTgtArtist != "" {
		if normCandArtist == normTgtArtist {
			artistMatchScore = 50
		} else if strings.Contains(normCandArtist, normTgtArtist) || strings.Contains(normTgtArtist, normCandArtist) {
			artistMatchScore = 35
		} else {
			// Артист вообще не совпадает — это чужой релиз (караоке-лейбл или другой исполнитель)
			return false, 0
		}
	} else if isGeneric {
		artistMatchScore = 20
	}

	// 2. Проверка альбома
	albumMatchScore := 0
	if normCleanAlbum != "" {
		if normCandAlbum == normCleanAlbum {
			albumMatchScore = 50
		} else if strings.Contains(normCandAlbum, normCleanAlbum) || strings.Contains(normCleanAlbum, normCandAlbum) {
			albumMatchScore = 40
		} else {
			// Название релиза отличается (например, сингл "Boulevard of Broken Dreams" входит в альбом "American Idiot")
			// Для того же официального артиста это валидный кандидат
			albumMatchScore = 20
		}
	} else {
		albumMatchScore = 25
	}

	totalScore := artistMatchScore + albumMatchScore
	if totalScore < 40 {
		return false, totalScore
	}

	return true, totalScore
}
