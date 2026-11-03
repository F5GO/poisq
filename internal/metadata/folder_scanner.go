// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл folder_scanner.go реализует сканер медиатеки в Staging-режиме (без слепой перезаписи файлов)
// и обеспечивает выполнение правила безопасности «Folder-as-Album Lock», запрещающего дробить
// номерные альбомы внутри одной папки на случайные сборники.
package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/lyrics"
)

// FolderLockConsensus содержит вычисленный эталон альбома для папки по правилу «Folder-as-Album Lock».
type FolderLockConsensus struct {
	FolderPath      string `json:"folderPath"`
	DominantArtist  string `json:"dominantArtist"`
	DominantAlbum   string `json:"dominantAlbum"`
	DominantYear    int    `json:"dominantYear"`
	IsNumberedAlbum bool   `json:"isNumberedAlbum"`
	TrackCount      int    `json:"trackCount"`
}

// ComputeFolderAlbumLock анализирует все треки внутри одной физической папки и вычисляет
// единый альбомный консенсус (Folder-as-Album Lock), чтобы не допустить распада номерного альбома.
func ComputeFolderAlbumLock(tracks []db.TrackRecord) FolderLockConsensus {
	if len(tracks) == 0 {
		return FolderLockConsensus{}
	}

	albumCounts := make(map[string]int)
	artistCounts := make(map[string]int)
	yearCounts := make(map[int]int)
	numberedTracks := 0

	for _, t := range tracks {
		alb := strings.TrimSpace(t.Album)
		if alb != "" {
			albumCounts[alb]++
		}
		art := strings.TrimSpace(t.AlbumArtist)
		if art == "" {
			art = strings.TrimSpace(t.Artist)
		}
		if art != "" {
			artistCounts[art]++
		}
		if t.Year > 1900 {
			yearCounts[t.Year]++
		}
		if t.TrackNumber > 0 {
			numberedTracks++
		}
	}

	domAlbum := maxKeyString(albumCounts)
	domArtist := maxKeyString(artistCounts)
	domYear := maxKeyInt(yearCounts)

	// Если в папке >= 2 треков и большинство имеют номера дорожек или единый альбом — блокируем как номерной альбом
	isNumbered := len(tracks) >= 2 && (numberedTracks >= len(tracks)/2 || albumCounts[domAlbum] >= len(tracks)/2)

	return FolderLockConsensus{
		FolderPath:      tracks[0].FolderPath,
		DominantArtist:  domArtist,
		DominantAlbum:   domAlbum,
		DominantYear:    domYear,
		IsNumberedAlbum: isNumbered,
		TrackCount:      len(tracks),
	}
}

// LibraryScanner координирует безопасное сканирование медиатеки и формирование Staging-предложений.
type LibraryScanner struct {
	musicRoot   string
	tracksRepo  *db.TrackRepository
	stagingRepo *db.StagingRepository
	mbClient    *MusicBrainzClient
	acoustID    *AcoustIDClient
	coverFetch  *CoverFetcher
	arbiter     *ai.MetadataArbiter
}

// NewLibraryScanner создает новый экземпляр сканера медиатеки.
func NewLibraryScanner(
	musicRoot string,
	tracksRepo *db.TrackRepository,
	stagingRepo *db.StagingRepository,
	mbClient *MusicBrainzClient,
	acoustID *AcoustIDClient,
	coverFetch *CoverFetcher,
) *LibraryScanner {
	return &LibraryScanner{
		musicRoot:   musicRoot,
		tracksRepo:  tracksRepo,
		stagingRepo: stagingRepo,
		mbClient:    mbClient,
		acoustID:    acoustID,
		coverFetch:  coverFetch,
	}
}

// SyncFromNavidromeDB синхронизирует каталог треков из таблицы media_file базы Navidrome в poisq.db,
// проверяет наличие .lrc файлов, размеры обложек и детектирует битую кодировку CP1251 (Mojibake).
func (s *LibraryScanner) SyncFromNavidromeDB(navDB *sql.DB) (int, error) {
	if navDB == nil {
		return 0, nil
	}

	rows, err := navDB.Query(`
		SELECT id, path, title, artist, album, album_artist, track_number, year, duration
		FROM media_file
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	folderCoverCache := make(map[string]struct {
		has bool
		w   int
		h   int
	})

	batch := make([]db.TrackRecord, 0, 500)
	count := 0
	for rows.Next() {
		var id, relOrAbsPath, title, artist, album, albumArtist string
		var trackNum, year int
		var durationFloat float64

		if err := rows.Scan(&id, &relOrAbsPath, &title, &artist, &album, &albumArtist, &trackNum, &year, &durationFloat); err != nil {
			continue
		}

		fullPath := s.resolveMusicPath(relOrAbsPath)
		folderPath := filepath.Dir(fullPath)

		cov, ok := folderCoverCache[folderPath]
		if !ok {
			has, w, h, _ := s.coverFetch.InspectFolderCover(folderPath)
			cov = struct {
				has bool
				w   int
				h   int
			}{has: has, w: w, h: h}
			folderCoverCache[folderPath] = cov
		}

		fixedArtist, mojArtist := DetectAndFixMojibake(artist)
		fixedTitle, mojTitle := DetectAndFixMojibake(title)
		fixedAlbum, mojAlbum := DetectAndFixMojibake(album)
		isMojibake := mojArtist || mojTitle || mojAlbum

		// Проверяем, лежит ли уже рядом готовый .lrc файл
		ext := filepath.Ext(fullPath)
		lrcPath := strings.TrimSuffix(fullPath, ext) + ".lrc"
		lyrStatus := "none"
		lyrSource := ""
		lyrContent := ""
		lrcSynced := false

		if lrcBytes, err := os.ReadFile(lrcPath); err == nil && len(lrcBytes) > 0 {
			lyrContent = strings.TrimSpace(string(lrcBytes))
			if len(lyrics.ParseLRC(lyrContent)) > 0 {
				lyrStatus = "synced"
				lrcSynced = true
			} else if lyrContent != "" {
				lyrStatus = "plain"
			}
			lyrSource = "local_lrc"
		}

		rec := db.TrackRecord{
			ID:            id,
			FilePath:      fullPath,
			FolderPath:    folderPath,
			Artist:        NormalizeNFC(artist),
			Album:         NormalizeNFC(album),
			AlbumArtist:   NormalizeNFC(albumArtist),
			Title:         NormalizeNFC(title),
			TrackNumber:   trackNum,
			Year:          year,
			Duration:      int(durationFloat),
			HasCover:      cov.has,
			CoverWidth:    cov.w,
			CoverHeight:   cov.h,
			IsMojibake:    isMojibake,
			LyricsStatus:  lyrStatus,
			ActiveSource:  lyrSource,
			ActiveLyrics:  lyrContent,
			LrcFileSynced: lrcSynced,
		}

		batch = append(batch, rec)
		count++

		if len(batch) >= 500 {
			_ = s.tracksRepo.UpsertTracksBatch(batch)
			batch = batch[:0]
		}

		// Если обнаружена битая кодировка CP1251 — сразу формируем предложение в Staging!
		if isMojibake {
			curBytes, _ := json.Marshal(map[string]any{
				"artist": artist,
				"album":  album,
				"title":  title,
				"year":   year,
			})
			propBytes, _ := json.Marshal(map[string]any{
				"artist":      fixedArtist,
				"album":       fixedAlbum,
				"albumArtist": fixedArtist,
				"title":       fixedTitle,
				"year":        year,
				"reason":      "Исправление битой кириллицы (CP1251 -> UTF-8 NFC)",
			})
			_, _ = s.stagingRepo.CreateProposal(db.StagingChangeRecord{
				Scope:        "track",
				TargetID:     id,
				FolderPath:   folderPath,
				ChangeType:   "mojibake",
				Confidence:   98,
				CurrentJSON:  string(curBytes),
				ProposedJSON: string(propBytes),
			})
		}
	}

	if len(batch) > 0 {
		_ = s.tracksRepo.UpsertTracksBatch(batch)
	}

	return count, rows.Err()
}

// RunDeepAudit запускает глубокую проверку папок через MusicBrainz, AcoustID и iTunes/CoverArt,
// складывая все найденные улучшения тегов и обложек в таблицу staging_changes (Staging-режим).
func (s *LibraryScanner) RunDeepAudit(ctx context.Context, limitFolders int) (int, error) {
	tracks, err := s.tracksRepo.ListTracks("all", "", 500)
	if err != nil {
		return 0, err
	}

	byFolder := make(map[string][]db.TrackRecord)
	for _, t := range tracks {
		if t.FolderPath != "" {
			byFolder[t.FolderPath] = append(byFolder[t.FolderPath], t)
		}
	}

	proposalsCreated := 0
	foldersProcessed := 0

	for folderPath, folderTracks := range byFolder {
		if ctx.Err() != nil {
			break
		}
		if limitFolders > 0 && foldersProcessed >= limitFolders {
			break
		}
		foldersProcessed++

		consensus := ComputeFolderAlbumLock(folderTracks)
		if consensus.DominantArtist == "" {
			continue
		}

		// 1. Проверка каноничного имени артиста и релиза в MusicBrainz (с защитой Folder-as-Album Lock)
		var releaseMBID string
		if s.mbClient != nil {
			if relInfo, err := s.mbClient.LookupRelease(ctx, consensus.DominantArtist, consensus.DominantAlbum); err == nil && relInfo != nil {
				releaseMBID = relInfo.ReleaseMBID
				// Если имя артиста или год уточнены в MusicBrainz (например, "Максим" -> "МакSим")
				if relInfo.CanonicalArtist != consensus.DominantArtist || (consensus.DominantYear == 0 && relInfo.Year > 0) {
					for _, tr := range folderTracks {
						targetAlbum := tr.Album
						if consensus.IsNumberedAlbum && relInfo.AlbumTitle != "" {
							// Folder-as-Album Lock: удерживаем единый номерной альбом для всех треков папки
							targetAlbum = relInfo.AlbumTitle
						}
						targetYear := tr.Year
						if targetYear == 0 && relInfo.Year > 0 {
							targetYear = relInfo.Year
						}
						curJSON, _ := json.Marshal(map[string]any{
							"artist":      tr.Artist,
							"album":       tr.Album,
							"albumArtist": tr.AlbumArtist,
							"title":       tr.Title,
							"year":        tr.Year,
						})
						propJSON, _ := json.Marshal(map[string]any{
							"artist":      relInfo.CanonicalArtist,
							"album":       targetAlbum,
							"albumArtist": relInfo.CanonicalArtist,
							"title":       tr.Title,
							"year":        targetYear,
							"releaseMbid": relInfo.ReleaseMBID,
							"artistMbid":  relInfo.ArtistMBID,
							"reason":      "Канонизация MusicBrainz + Folder-as-Album Lock",
						})
						if _, err := s.stagingRepo.CreateProposal(db.StagingChangeRecord{
							Scope:        "track",
							TargetID:     tr.ID,
							FolderPath:   folderPath,
							ChangeType:   "musicbrainz",
							Confidence:   relInfo.Score,
							CurrentJSON:  string(curJSON),
							ProposedJSON: string(propJSON),
						}); err == nil {
							proposalsCreated++
						}
					}
				}
			} else if artInfo, err := s.mbClient.LookupArtist(ctx, consensus.DominantArtist); err == nil && artInfo != nil {
				if artInfo.CanonicalName != "" && artInfo.CanonicalName != consensus.DominantArtist {
					for _, tr := range folderTracks {
						curJSON, _ := json.Marshal(map[string]any{
							"artist": tr.Artist,
							"album":  tr.Album,
							"title":  tr.Title,
							"year":   tr.Year,
						})
						propJSON, _ := json.Marshal(map[string]any{
							"artist":      artInfo.CanonicalName,
							"album":       consensus.DominantAlbum,
							"albumArtist": artInfo.CanonicalName,
							"title":       tr.Title,
							"year":        tr.Year,
							"artistMbid":  artInfo.ArtistMBID,
							"aliases":     artInfo.Aliases,
							"reason":      "Каноничное имя артиста MusicBrainz (" + consensus.DominantArtist + " -> " + artInfo.CanonicalName + ")",
						})
						if _, err := s.stagingRepo.CreateProposal(db.StagingChangeRecord{
							Scope:        "track",
							TargetID:     tr.ID,
							FolderPath:   folderPath,
							ChangeType:   "artist_canonical",
							Confidence:   artInfo.Score,
							CurrentJSON:  string(curJSON),
							ProposedJSON: string(propJSON),
						}); err == nil {
							proposalsCreated++
						}
					}
				}
			}
		}

		// 2. Проверка обложки папки (если нет качественной cover.jpg >= 500x500 — ищем в Cover Art Archive / iTunes 3000x3000)
		hasCov, w, h, existingFile := s.coverFetch.InspectFolderCover(folderPath)
		if !hasCov || w < 500 || h < 500 {
			var cand *CoverCandidate
			var aiExplanation, canonicalAlbum, releaseType string

			isDisputed := IsSuspectedSingleOrDisputed(folderPath, folderTracks)
			if isDisputed && s.arbiter != nil {
				// Если это сингл или имя папки содержит дефис/тире — сразу привлекаем контекстный AI-арбитраж
				if aiCand, decision, err := s.ResolveFolderCoverWithAI(ctx, folderPath, folderTracks, releaseMBID); err == nil && aiCand != nil {
					cand = aiCand
					if decision != nil {
						aiExplanation = decision.Explanation
						canonicalAlbum = decision.CanonicalAlbum
						releaseType = decision.ReleaseType
					}
				}
			}

			// Если AI не привлекался или не нашел кандидата — стандартный поиск
			if cand == nil {
				cand, _ = s.coverFetch.FindHighResCover(ctx, releaseMBID, consensus.DominantArtist, consensus.DominantAlbum)
			}

			// Если стандартный поиск не дал результатов, пробуем AI как фоллбэк
			if cand == nil && !isDisputed && s.arbiter != nil {
				if aiCand, decision, err := s.ResolveFolderCoverWithAI(ctx, folderPath, folderTracks, releaseMBID); err == nil && aiCand != nil {
					cand = aiCand
					if decision != nil {
						aiExplanation = decision.Explanation
						canonicalAlbum = decision.CanonicalAlbum
						releaseType = decision.ReleaseType
					}
				}
			}

			if cand != nil {
				// Защита: если существующая обложка уже больше или равна по разрешению, не меняем на худшую
				if hasCov && cand.Width <= w && cand.Height <= h {
					continue
				}
				curCovJSON, _ := json.Marshal(map[string]any{
					"hasCover":     hasCov,
					"width":        w,
					"height":       h,
					"existingFile": existingFile,
					"artist":       consensus.DominantArtist,
					"album":        consensus.DominantAlbum,
				})

				reason := "Обложка высокого разрешения (" + cand.Source + ")"
				if aiExplanation != "" {
					reason = "Подобрано через AI: " + canonicalAlbum + " (" + aiExplanation + ")"
				}

				propCovJSON, _ := json.Marshal(map[string]any{
					"coverUrl":       cand.URL,
					"source":         cand.Source,
					"width":          cand.Width,
					"height":         cand.Height,
					"artist":         consensus.DominantArtist,
					"album":          consensus.DominantAlbum,
					"canonicalAlbum": canonicalAlbum,
					"releaseType":    releaseType,
					"aiExplanation":  aiExplanation,
					"reason":         reason,
					"alternatives":   cand.Alternatives,
				})
				conf := cand.Score
				if conf <= 0 {
					conf = 95
				}
				if _, err := s.stagingRepo.CreateProposal(db.StagingChangeRecord{
					Scope:        "folder",
					TargetID:     folderPath,
					FolderPath:   folderPath,
					ChangeType:   "cover",
					Confidence:   conf,
					CurrentJSON:  string(curCovJSON),
					ProposedJSON: string(propCovJSON),
				}); err == nil {
					proposalsCreated++
				}
			}
		}
	}

	log.Printf("[INFO] [SCANNER] Глубокий аудит завершен: проверено папок %d, создано предложений в Staging: %d", foldersProcessed, proposalsCreated)
	return proposalsCreated, nil
}

func (s *LibraryScanner) resolveMusicPath(p string) string {
	clean := filepath.Clean(p)
	if filepath.IsAbs(clean) {
		return clean
	}
	return filepath.Join(s.musicRoot, clean)
}

func maxKeyString(m map[string]int) string {
	bestK := ""
	bestV := 0
	for k, v := range m {
		if v > bestV {
			bestV = v
			bestK = k
		}
	}
	return bestK
}

func maxKeyInt(m map[int]int) int {
	bestK := 0
	bestV := 0
	for k, v := range m {
		if v > bestV {
			bestV = v
			bestK = k
		}
	}
	return bestK
}
