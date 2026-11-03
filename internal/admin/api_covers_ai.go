// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_covers_ai.go реализует эндпоинт интеллектуального подбора обложек через Gemini
// (/api/v1/covers/ai-resolve). Использует нейросеть для разрешения споров о синглах/альбомах,
// находит оригинальные обложки в Apple Music / iTunes и мгновенно обновляет предложение в Staging.
package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
)

// SetMetadataArbiter подключает интеллектуальный арбитр к серверу панели управления.
func (s *AdminServer) SetMetadataArbiter(arbiter *ai.MetadataArbiter) {
	s.arbiter = arbiter
}

// handleCoverAIResolve разрешает спорные ситуации с обложками и альбомами с помощью нейросети Gemini 2.5 Flash.
func (s *AdminServer) handleCoverAIResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.arbiter == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "AI арбитр метаданных не подключен (проверьте API-ключи Gemini)")
		return
	}

	folderPath := strings.TrimSpace(r.URL.Query().Get("path"))
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))

	// Если передан JSON в теле POST-запроса, читаем параметры из него
	if r.Method == http.MethodPost && r.Body != nil {
		var reqBody struct {
			Path string `json:"path"`
			ID   int64  `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err == nil {
			if reqBody.Path != "" {
				folderPath = strings.TrimSpace(reqBody.Path)
			}
			if reqBody.ID > 0 {
				idStr = strconv.FormatInt(reqBody.ID, 10)
			}
		}
	}

	stagingID, _ := strconv.ParseInt(idStr, 10, 64)

	// Если передан stagingID, но не передан folderPath — берем путь из Staging
	var stagingItem *db.StagingChangeRecord
	if stagingID > 0 && s.stagingRepo != nil {
		if it, err := s.stagingRepo.GetByID(stagingID); err == nil && it != nil {
			stagingItem = it
			if folderPath == "" {
				folderPath = it.FolderPath
				if folderPath == "" {
					folderPath = it.TargetID
				}
			}
		}
	}

	if folderPath == "" {
		writeJSONError(w, http.StatusBadRequest, "необходимо указать path (путь к папке) или id предложения Staging")
		return
	}

	cleanFolder := filepath.Clean(folderPath)
	folderName := filepath.Base(cleanFolder)

	// Сбор контекста треков из базы данных
	var trackFiles []string
	var existingArtists []string
	var existingAlbums []string
	var existingTitles []string

	artistSet := make(map[string]bool)
	albumSet := make(map[string]bool)
	titleSet := make(map[string]bool)

	if s.tracksRepo != nil {
		if allTracks, err := s.tracksRepo.ListTracks("all", "", 1000); err == nil {
			for _, t := range allTracks {
				if filepath.Clean(t.FolderPath) == cleanFolder || strings.HasPrefix(filepath.Clean(t.FilePath), cleanFolder) {
					if t.Artist != "" && !artistSet[t.Artist] {
						artistSet[t.Artist] = true
						existingArtists = append(existingArtists, t.Artist)
					}
					if t.Album != "" && !albumSet[t.Album] {
						albumSet[t.Album] = true
						existingAlbums = append(existingAlbums, t.Album)
					}
					if t.Title != "" && !titleSet[t.Title] {
						titleSet[t.Title] = true
						existingTitles = append(existingTitles, t.Title)
					}
					if t.FilePath != "" {
						trackFiles = append(trackFiles, filepath.Base(t.FilePath))
					}
				}
			}
		}
	}

	// Если треков в базе не нашлось, считываем реальные файлы из файловой системы
	if len(trackFiles) == 0 {
		if s.guard != nil {
			if safePath, err := s.guard.ValidateTarget(cleanFolder, false); err == nil {
				if entries, err := os.ReadDir(safePath); err == nil {
					for _, e := range entries {
						if !e.IsDir() {
							trackFiles = append(trackFiles, e.Name())
						}
					}
				}
			}
		}
	}

	folderCtx := ai.FolderContext{
		FolderPath:      cleanFolder,
		FolderName:      folderName,
		TrackFiles:      trackFiles,
		ExistingArtists: existingArtists,
		ExistingAlbums:  existingAlbums,
		ExistingTitles:  existingTitles,
	}

	decision, err := s.arbiter.ResolveDisputedMetadata(r.Context(), folderCtx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Ошибка AI арбитража: "+err.Error())
		return
	}

	// Поиск обложек высокого разрешения для канонического альбома
	fetcher := metadata.NewCoverFetcher(s.guard)
	candidates := fetcher.FindCoverCandidates(r.Context(), "", decision.CanonicalArtist, decision.CanonicalAlbum, 12)

	if len(candidates) == 0 && decision.CoverSearchTerm != "" {
		parts := strings.Fields(decision.CoverSearchTerm)
		if len(parts) >= 2 {
			candidates = fetcher.FindCoverCandidates(r.Context(), "", parts[0], strings.Join(parts[1:], " "), 12)
		}
	}

	// Проверяем текущую локальную обложку на диске
	hasCover, curW, curH, existingFile := fetcher.InspectFolderCover(cleanFolder)

	// Если есть активное Staging-предложение, обновляем его новыми AI-данными
	var updatedProposal *db.StagingChangeRecord
	if stagingItem != nil && len(candidates) > 0 {
		best := candidates[0]
		var alts []*metadata.CoverCandidate
		if len(candidates) > 1 {
			alts = candidates[1:]
		}

		curJSON, _ := json.Marshal(map[string]any{
			"hasCover":     hasCover,
			"width":        curW,
			"height":       curH,
			"existingFile": existingFile,
			"artist":       decision.CanonicalArtist,
			"album":        decision.CanonicalAlbum,
		})

		propJSON, _ := json.Marshal(map[string]any{
			"coverUrl":       best.URL,
			"source":         best.Source,
			"width":          best.Width,
			"height":         best.Height,
			"artist":         decision.CanonicalArtist,
			"album":          decision.CanonicalAlbum,
			"canonicalAlbum": decision.CanonicalAlbum,
			"releaseType":    decision.ReleaseType,
			"aiExplanation":  decision.Explanation,
			"reason":         "Подобрано через AI: " + decision.CanonicalAlbum + " (" + decision.Explanation + ")",
			"alternatives":   alts,
		})

		stagingItem.Confidence = decision.Confidence
		stagingItem.CurrentJSON = string(curJSON)
		stagingItem.ProposedJSON = string(propJSON)

		// Обновляем предложение в staging_changes
		if err := s.stagingRepo.UpdateProposedJSON(stagingItem.ID, stagingItem.ProposedJSON); err == nil {
			updatedProposal = stagingItem
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"decision":        decision,
		"candidates":      candidates,
		"hasLocalCover":   hasCover,
		"localWidth":      curW,
		"localHeight":     curH,
		"existingFile":    existingFile,
		"updatedProposal": updatedProposal,
	})
}
