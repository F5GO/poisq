// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Что делает этот код и для чего он нужен:
// Данный файл repo_tracks_dedup.go реализует интеллектуальную дедупликацию таблицы tracks:
// 1. Находит повторяющиеся треки (по исполнителю, альбому, названию и хронометражу).
// 2. Оценивает каждый дубликат по системе скоринга (приоритет оригинального каталога над «копиями»,
//    наличие проверенного/синхронного LRC-текста, наличие качественной обложки).
// 3. Сохраняет лучший экземпляр трека и физически удаляет дубликаты из poisq.db.
package db

import (
	"math"
	"strings"
	"unicode"

	"github.com/F5GO/poisq/internal/logger"
)

// normalizeDedupText приводит строку к нижнему регистру, нормализуя пробелы и удаляя лишнюю пунктуацию.
func normalizeDedupText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r == '\u00a0' || r == '\u202f' || unicode.Is(unicode.Zs, r) {
			b.WriteRune(' ')
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	// Схлопываем множественные пробелы
	fields := strings.Fields(b.String())
	return strings.Join(fields, " ")
}

// scoreTrack возвращает рейтинг качества трека для выбора победителя при дедупликации.
func scoreTrack(t TrackRecord) int {
	score := 0
	lowPath := strings.ToLower(t.FilePath)

	// Штрафуем каталоги-копии
	if !strings.Contains(lowPath, "копия") && !strings.Contains(lowPath, "copy") {
		score += 100
	}

	// Бонусы за наличие и качество текста песни
	switch t.LyricsStatus {
	case "verified":
		score += 50
	case "synced":
		score += 40
	case "plain":
		score += 20
	}

	// Бонус за наличие обложки
	if t.HasCover {
		score += 10
		if t.CoverWidth >= 500 {
			score += 5
		}
	}

	// Предпочитаем треки без поврежденной кодировки (Mojibake)
	if !t.IsMojibake {
		score += 15
	}

	return score
}

// DeduplicateTracks выполняет интеллектуальный аудит таблицы tracks, устраняя любые
// дубликаты песен, гарантируя уникальность каждой композиции в фонотеке poisq.
func (r *TrackRepository) DeduplicateTracks() (int64, error) {
	if r.db == nil {
		return 0, nil
	}

	rows, err := r.db.Query(`
		SELECT id, file_path, folder_path, artist, album, album_artist, title,
		       track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
		       is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		FROM tracks
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var allTracks []TrackRecord
	for rows.Next() {
		t, err := scanTrackRows(rows)
		if err != nil {
			continue
		}
		allTracks = append(allTracks, *t)
	}
	_ = rows.Close()

	if len(allTracks) <= 1 {
		return 0, nil
	}

	// Группируем треки по нормализованному ключу: Исполнитель + Название
	type trackGroupKey struct {
		artist string
		title  string
	}

	groups := make(map[trackGroupKey][]TrackRecord)
	for _, t := range allTracks {
		normArtist := normalizeDedupText(t.Artist)
		normTitle := normalizeDedupText(t.Title)
		if normArtist == "" && normTitle == "" {
			continue
		}
		key := trackGroupKey{artist: normArtist, title: normTitle}
		groups[key] = append(groups[key], t)
	}

	var toDeleteIDs []string

	for _, list := range groups {
		if len(list) <= 1 {
			continue
		}

		// Внутри группы исполнитель+название разбиваем на подгруппы дубликатов (по альбому или близкой длительности)
		used := make([]bool, len(list))
		for i := 0; i < len(list); i++ {
			if used[i] {
				continue
			}

			cluster := []TrackRecord{list[i]}
			used[i] = true
			normAlb1 := normalizeDedupText(list[i].Album)

			for j := i + 1; j < len(list); j++ {
				if used[j] {
					continue
				}

				normAlb2 := normalizeDedupText(list[j].Album)
				sameAlbum := (normAlb1 != "" && normAlb1 == normAlb2)
				diffDur := math.Abs(float64(list[i].Duration - list[j].Duration))
				closeDuration := (list[i].Duration > 0 && list[j].Duration > 0 && diffDur <= 3.0)

				// Если альбомы совпадают, или длительность практически идентична (разница <= 3 сек) — это дубликат!
				if sameAlbum || closeDuration {
					cluster = append(cluster, list[j])
					used[j] = true
				}
			}

			if len(cluster) <= 1 {
				continue
			}

			// Выбираем победителя кластера
			bestIdx := 0
			bestScore := scoreTrack(cluster[0])

			for k := 1; k < len(cluster); k++ {
				score := scoreTrack(cluster[k])
				if score > bestScore {
					bestScore = score
					bestIdx = k
				} else if score == bestScore {
					// При равном счете отдаем приоритет более свежей записи
					if cluster[k].UpdatedAt > cluster[bestIdx].UpdatedAt {
						bestIdx = k
					}
				}
			}

			// Все остальные кандидаты из кластера помечаем на удаление
			for k := 0; k < len(cluster); k++ {
				if k != bestIdx {
					toDeleteIDs = append(toDeleteIDs, cluster[k].ID)
				}
			}
		}
	}

	if len(toDeleteIDs) == 0 {
		return 0, nil
	}

	// Удаляем дубликаты транзакцией
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`DELETE FROM tracks WHERE id = ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, id := range toDeleteIDs {
		_, _ = stmt.Exec(id)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	logger.Info("[DEDUP] Успешно удалено дубликатов треков из poisq.db: %d", len(toDeleteIDs))
	return int64(len(toDeleteIDs)), nil
}
