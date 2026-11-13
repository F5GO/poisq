package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/db"
)

const maxArtistBiographyRequestBytes = artistbio.MaxBiographyBytes + (8 << 10)

type artistBiographyView struct {
	Text       string             `json:"text"`
	Source     artistbio.Provider `json:"source"`
	SourceURL  string             `json:"sourceUrl,omitempty"`
	Translated bool               `json:"translated"`
	UpdatedAt  time.Time          `json:"updatedAt"`
}

type artistBiographyDetail struct {
	ArtistID     string               `json:"artistId"`
	ArtistName   string               `json:"artistName"`
	HasBiography bool                 `json:"hasBiography"`
	ManualActive bool                 `json:"manualActive"`
	ManualText   string               `json:"manualText"`
	ManualSource string               `json:"manualSourceUrl,omitempty"`
	Effective    *artistBiographyView `json:"effective,omitempty"`
	Automatic    *artistBiographyView `json:"automatic,omitempty"`
	JobStatus    artistbio.JobStatus  `json:"jobStatus,omitempty"`
	LastError    string               `json:"lastError,omitempty"`
	NextRetryAt  time.Time            `json:"nextRetryAt,omitempty"`
}

// SetBiographyStore attaches the already constructed local store. It performs
// no database read and must be called before serving requests.
func (s *AdminServer) SetBiographyStore(store *artistbio.Store) { s.biographies = store }

func (s *AdminServer) SetBiographyCoordinator(coordinator interface {
	Request(context.Context, string, bool) (<-chan struct{}, artistbio.JobState, error)
}) {
	s.biographyJobs = coordinator
}

func (s *AdminServer) handleArtistBiographies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.openNavDB == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist inventory is unavailable")
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeJSONError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	database, err := s.openNavDB()
	if err != nil || database == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist inventory is unavailable")
		return
	}
	defer database.Close()
	artists, err := db.SearchArtistInventory(r.Context(), database, r.URL.Query().Get("query"), limit)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid artist search")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artists": artists})
}

func (s *AdminServer) handleArtistBiographyDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.biographies == nil || s.openNavDB == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist biographies are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		artist, found, ok := s.requireLibraryArtist(w, r.Context(), r.URL.Query().Get("id"))
		if !ok {
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "artist not found")
			return
		}
		s.writeArtistBiographyDetail(w, r.Context(), artist)
	case http.MethodPut:
		var request struct {
			ArtistID  string `json:"artistId"`
			Text      string `json:"text"`
			SourceURL string `json:"sourceUrl"`
		}
		if err := decodeStrictAdminJSON(w, r, &request); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		artist, found, ok := s.requireLibraryArtist(w, r.Context(), request.ArtistID)
		if !ok {
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "artist not found")
			return
		}
		text := normalizeManualBiography(request.Text)
		analysis := artistbio.DetectBiographyLanguage(text)
		if !analysis.QualityOK || analysis.Language != "ru" || analysis.Confidence < 0.90 || analysis.RussianShare < 0.80 {
			writeJSONError(w, http.StatusBadRequest, "manual biography must be verified Russian text")
			return
		}
		if err := s.ensureBiographyIdentity(r.Context(), artist); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save manual biography")
			return
		}
		override := artistbio.ManualOverride{ArtistID: artist.ID, Text: text, SourceURL: strings.TrimSpace(request.SourceURL), UpdatedAt: time.Now()}
		if err := override.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid manual biography or source URL")
			return
		}
		if err := s.biographies.SetManualOverride(r.Context(), override); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save manual biography")
			return
		}
		s.writeArtistBiographyDetail(w, r.Context(), artist)
	case http.MethodDelete:
		artist, found, ok := s.requireLibraryArtist(w, r.Context(), r.URL.Query().Get("id"))
		if !ok {
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "artist not found")
			return
		}
		if _, identityFound, err := s.biographies.Identity(r.Context(), artist.ID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to reset manual biography")
			return
		} else if identityFound {
			if err := s.biographies.SetManualOverride(r.Context(), artistbio.ManualOverride{ArtistID: artist.ID}); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to reset manual biography")
				return
			}
		}
		s.writeArtistBiographyDetail(w, r.Context(), artist)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *AdminServer) handleArtistBiographyReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.biographies == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist biographies are unavailable")
		return
	}
	var request struct{}
	if err := decodeStrictAdminJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	report, err := s.biographies.ResetAutomaticCache(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to reset artist biography cache")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "cleared": report, "manualOverridesPreserved": true,
	})
}

func (s *AdminServer) handleArtistBiographyRebuild(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.biographies == nil || s.biographyJobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist biography rebuild is unavailable")
		return
	}
	var request struct {
		ArtistID string `json:"artistId"`
	}
	if err := decodeStrictAdminJSON(w, r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	artist, found, ok := s.requireLibraryArtist(w, r.Context(), request.ArtistID)
	if !ok {
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "artist not found")
		return
	}
	if err := s.ensureBiographyIdentity(r.Context(), artist); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to prepare artist biography")
		return
	}
	_, state, err := s.biographyJobs.Request(r.Context(), artist.ID, true)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "failed to queue artist biography rebuild")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"artistId": artist.ID, "status": state.Status})
}

func (s *AdminServer) requireLibraryArtist(w http.ResponseWriter, ctx context.Context, artistID string) (db.LibraryArtist, bool, bool) {
	database, err := s.openNavDB()
	if err != nil || database == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "artist inventory is unavailable")
		return db.LibraryArtist{}, false, false
	}
	defer database.Close()
	artist, found, err := db.FindLibraryArtist(ctx, database, artistID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid artist id")
		return db.LibraryArtist{}, false, false
	}
	return artist, found, true
}

func (s *AdminServer) ensureBiographyIdentity(ctx context.Context, artist db.LibraryArtist) error {
	_, found, err := s.biographies.Identity(ctx, artist.ID)
	if err != nil || found {
		return err
	}
	return s.biographies.PutIdentity(ctx, artistbio.ArtistIdentity{
		ArtistID: artist.ID, ArtistName: artist.Name, Status: artistbio.ResolutionPending,
		EvidenceJSON: `{}`, UpdatedAt: time.Now(),
	})
}

func (s *AdminServer) writeArtistBiographyDetail(w http.ResponseWriter, ctx context.Context, artist db.LibraryArtist) {
	detail := artistBiographyDetail{ArtistID: artist.ID, ArtistName: artist.Name}
	if state, found, err := s.biographies.JobState(ctx, artist.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read artist biography")
		return
	} else if found {
		detail.JobStatus = state.Status
		detail.LastError = state.LastError
		detail.NextRetryAt = state.NextRetryAt
	}
	if effective, found, err := s.biographies.EffectiveBiography(ctx, artist.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read artist biography")
		return
	} else if found {
		detail.HasBiography = true
		detail.ManualActive = effective.Manual
		detail.Effective = effectiveBiographyView(effective)
		if effective.Manual {
			detail.ManualText = effective.Text
			detail.ManualSource = effective.SourceURL
		}
	}
	if automatic, found, err := s.biographies.AutomaticCandidate(ctx, artist.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read artist biography")
		return
	} else if found {
		text := automatic.OriginalText
		translated := false
		if automatic.RussianText != "" {
			text = automatic.RussianText
			translated = true
		}
		detail.Automatic = &artistBiographyView{
			Text: text, Source: automatic.Provider, SourceURL: automatic.SourceURL,
			Translated: translated, UpdatedAt: automatic.UpdatedAt,
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

func effectiveBiographyView(value artistbio.EffectiveBiography) *artistBiographyView {
	return &artistBiographyView{
		Text: value.Text, Source: value.Source, SourceURL: value.SourceURL,
		Translated: value.Translated, UpdatedAt: value.UpdatedAt,
	}
}

func normalizeManualBiography(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	output := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			if len(output) > 0 && !blank {
				output = append(output, "")
				blank = true
			}
			continue
		}
		output = append(output, clean)
		blank = false
	}
	return strings.TrimSpace(strings.Join(output, "\n"))
}

func decodeStrictAdminJSON(w http.ResponseWriter, r *http.Request, target any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxArtistBiographyRequestBytes))
	if err != nil || len(body) == 0 || !utf8.Valid(body) || rejectDuplicateAdminJSONKeys(body) != nil {
		return fmt.Errorf("invalid json")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid json")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid json")
	}
	return nil
}

func rejectDuplicateAdminJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("invalid key")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("invalid delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}
