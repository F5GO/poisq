package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/musicmeta"
)

// MetadataControlServices groups the normalized metadata dependencies without
// expanding the long-standing AdminServer constructor.
type MetadataControlServices struct {
	Metadata      *db.MetadataRepository
	MediaInfo     *db.MediaInfoRepository
	Policy        *db.MetadataPolicyRepository
	Analysis      *db.AudioAnalysisRepository
	Writebacks    *db.WritebackRepository
	History       *db.MetadataHistoryRepository
	AudioService  *metadata.AudioAnalysisService
	NFOService    *metadata.NFOService
	Enrichment    *db.MediaEnrichmentRepository
	EnrichmentSvc *metadata.MediaEnrichmentService
}

func (s *AdminServer) SetMetadataControlServices(services MetadataControlServices) {
	s.metadataRepo = services.Metadata
	s.mediaInfoRepo = services.MediaInfo
	s.policyRepo = services.Policy
	s.analysisRepo = services.Analysis
	s.writebackRepo = services.Writebacks
	s.historyRepo = services.History
	s.analysisSvc = services.AudioService
	s.nfoSvc = services.NFOService
	s.enrichmentRepo = services.Enrichment
	s.enrichmentSvc = services.EnrichmentSvc
	s.analysisJobs = make(map[string]bool)
	s.analysisSlots = make(chan struct{}, 2)
}

func (s *AdminServer) handleMetadataTrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	trackID := strings.TrimSpace(r.URL.Query().Get("id"))
	if trackID == "" || len(trackID) > musicmeta.MaxIDBytes || s.metadataRepo == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid track id or metadata service unavailable")
		return
	}
	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}
	document, err := s.metadataRepo.GetTrackDocument(r.Context(), trackID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := map[string]any{"track": track, "document": document}
	if s.mediaInfoRepo != nil {
		if value, err := s.mediaInfoRepo.Get(r.Context(), trackID); err == nil {
			result["mediaInfo"] = value
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if s.analysisRepo != nil {
		if value, err := s.analysisRepo.Get(r.Context(), trackID); err == nil {
			result["analysis"] = value
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *AdminServer) handleMetadataLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.metadataRepo == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		TrackID string          `json:"trackId"`
		Field   musicmeta.Field `json:"field"`
		Locked  bool            `json:"locked"`
	}
	if err := decodeJSONBody(r, &req); err != nil || strings.TrimSpace(req.TrackID) == "" || strings.TrimSpace(string(req.Field)) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid lock request")
		return
	}
	if err := s.metadataRepo.SetFieldLock(r.Context(), req.TrackID, req.Field, req.Locked); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "field": req.Field, "locked": req.Locked})
}

func (s *AdminServer) handleMetadataPolicy(w http.ResponseWriter, r *http.Request) {
	if s.policyRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "metadata policy service unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		policy, err := s.policyRepo.Get(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, policy)
	case http.MethodPut:
		var policy musicmeta.MergePolicy
		if err := decodeJSONBody(r, &policy); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid policy request")
			return
		}
		if err := s.policyRepo.Replace(r.Context(), policy); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		stored, _ := s.policyRepo.Get(r.Context())
		writeJSON(w, http.StatusOK, stored)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *AdminServer) handleMetadataRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.scanner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "metadata audit service unavailable")
		return
	}
	var req struct {
		TrackID string `json:"trackId"`
	}
	if err := decodeJSONBody(r, &req); err != nil || strings.TrimSpace(req.TrackID) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid track refresh request")
		return
	}
	if !s.startMetadataAudit(req.TrackID, 1) {
		writeJSONError(w, http.StatusConflict, "metadata audit is already running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *AdminServer) handleMetadataNFO(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.nfoSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		FolderPath string `json:"folderPath"`
		Action     string `json:"action"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid NFO request")
		return
	}
	var count int
	var err error
	switch req.Action {
	case "import":
		count, err = s.nfoSvc.ImportFolder(r.Context(), req.FolderPath)
	case "export":
		count, err = s.nfoSvc.StageExportFolder(r.Context(), req.FolderPath)
	default:
		err = errors.New("invalid NFO action")
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "created": count})
}

func (s *AdminServer) handleMetadataAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.analysisSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		TrackID string `json:"trackId"`
	}
	if err := decodeJSONBody(r, &req); err != nil || strings.TrimSpace(req.TrackID) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid analysis request")
		return
	}
	track, err := s.tracksRepo.GetByID(req.TrackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}
	s.analysisJobMu.Lock()
	if s.analysisJobs[req.TrackID] {
		s.analysisJobMu.Unlock()
		writeJSONError(w, http.StatusConflict, "track analysis is already running")
		return
	}
	select {
	case s.analysisSlots <- struct{}{}:
		s.analysisJobs[req.TrackID] = true
		s.analysisJobMu.Unlock()
	default:
		s.analysisJobMu.Unlock()
		writeJSONError(w, http.StatusTooManyRequests, "audio analysis worker limit reached")
		return
	}
	go func(value db.TrackRecord) {
		defer func() {
			<-s.analysisSlots
			s.analysisJobMu.Lock()
			delete(s.analysisJobs, value.ID)
			s.analysisJobMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, _ = s.analysisSvc.AnalyzeTrack(ctx, value)
	}(*track)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *AdminServer) handleMetadataReplayGain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.analysisSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		TrackID    string  `json:"trackId"`
		FolderPath string  `json:"folderPath"`
		TargetLUFS float64 `json:"targetLufs"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid ReplayGain request")
		return
	}
	if req.TrackID != "" {
		id, err := s.analysisSvc.StageTrackReplayGain(r.Context(), req.TrackID, req.TargetLUFS)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "created": 1, "stagingId": id})
		return
	}
	count, err := s.analysisSvc.StageAlbumReplayGain(r.Context(), req.FolderPath, req.TargetLUFS)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "created": count})
}

func (s *AdminServer) handleMetadataArtwork(w http.ResponseWriter, r *http.Request) {
	if s.enrichmentRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "media enrichment service unavailable")
		return
	}
	if r.Method == http.MethodGet {
		values, err := s.enrichmentRepo.ListArtwork(r.Context(), r.URL.Query().Get("scope"), r.URL.Query().Get("scopeId"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": values})
		return
	}
	if r.Method != http.MethodPost || s.enrichmentSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		Scope      string                      `json:"scope"`
		ScopeID    string                      `json:"scopeId"`
		FolderPath string                      `json:"folderPath"`
		Role       metadata.ArtworkRole        `json:"role"`
		Candidates []metadata.ArtworkCandidate `json:"candidates"`
	}
	if err := decodeJSONBody(r, &req); err != nil || len(req.Candidates) > 100 {
		writeJSONError(w, http.StatusBadRequest, "invalid artwork request")
		return
	}
	id, err := s.enrichmentSvc.StageArtwork(r.Context(), req.Scope, req.ScopeID, req.FolderPath, req.Role, req.Candidates)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "stagingId": id})
}

func (s *AdminServer) handleMetadataSimilar(w http.ResponseWriter, r *http.Request) {
	if s.enrichmentRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "media enrichment service unavailable")
		return
	}
	if r.Method == http.MethodGet {
		values, err := s.enrichmentRepo.ListSimilarArtists(r.Context(), r.URL.Query().Get("artistMbid"), 25)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": values})
		return
	}
	if r.Method != http.MethodPost || s.enrichmentSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	var req struct {
		ArtistMBID string `json:"artistMbid"`
		Limit      int    `json:"limit"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid similar artists request")
		return
	}
	values, err := s.enrichmentSvc.RefreshSimilarArtists(r.Context(), req.ArtistMBID, req.Limit)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": values})
}

func (s *AdminServer) handleMetadataWriteback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.writebackRepo == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	incomplete, err := s.writebackRepo.ListIncomplete(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	unreconciled, err := s.writebackRepo.ListCommittedUnreconciled(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	states := make(map[string]int)
	for _, value := range incomplete {
		states[value.State]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"healthy":    len(incomplete) == 0 && len(unreconciled) == 0,
		"incomplete": len(incomplete), "unreconciled": len(unreconciled), "states": states,
	})
}

func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		return errors.New("multiple JSON values")
	}
	return nil
}
