package admin

import (
	"net/http"
	"strings"

	"github.com/F5GO/poisq/internal/musicmeta"
)

const maxBulkMetadataTracks = 500

type metadataPatchRequest struct {
	TrackIDs []string                `json:"trackIds"`
	Patch    musicmeta.MetadataPatch `json:"patch"`
}

type metadataPatchResult struct {
	TrackID   string                  `json:"trackId"`
	Changes   []musicmeta.FieldChange `json:"changes,omitempty"`
	StagingID int64                   `json:"stagingId,omitempty"`
	Unchanged bool                    `json:"unchanged,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

func (s *AdminServer) handleMetadataTagsPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.metadataRepo == nil || s.stagingSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	request, ok := decodeMetadataPatchRequest(w, r)
	if !ok {
		return
	}
	results := make([]metadataPatchResult, 0, len(request.TrackIDs))
	for _, trackID := range request.TrackIDs {
		current, err := s.metadataRepo.GetTrackDocument(r.Context(), trackID)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: "track not found"})
			continue
		}
		proposed, err := musicmeta.ApplyPatch(current, request.Patch)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: err.Error()})
			continue
		}
		changes, err := musicmeta.DiffDocuments(current, proposed)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: err.Error()})
			continue
		}
		results = append(results, metadataPatchResult{TrackID: trackID, Changes: changes, Unchanged: len(changes) == 0})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (s *AdminServer) handleMetadataTagsApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.metadataRepo == nil || s.stagingSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	request, ok := decodeMetadataPatchRequest(w, r)
	if !ok {
		return
	}
	results := make([]metadataPatchResult, 0, len(request.TrackIDs))
	created := 0
	for _, trackID := range request.TrackIDs {
		current, err := s.metadataRepo.GetTrackDocument(r.Context(), trackID)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: "track not found"})
			continue
		}
		proposed, err := musicmeta.ApplyPatch(current, request.Patch)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: err.Error()})
			continue
		}
		changes, err := musicmeta.DiffDocuments(current, proposed)
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: err.Error()})
			continue
		}
		if len(changes) == 0 {
			results = append(results, metadataPatchResult{TrackID: trackID, Unchanged: true})
			continue
		}
		id, err := s.stagingSvc.StageDocumentEdit(r.Context(), current, proposed, "Пакетная ручная правка в Studio")
		if err != nil {
			results = append(results, metadataPatchResult{TrackID: trackID, Error: err.Error()})
			continue
		}
		created++
		results = append(results, metadataPatchResult{TrackID: trackID, Changes: changes, StagingID: id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "items": results})
}

func decodeMetadataPatchRequest(w http.ResponseWriter, r *http.Request) (metadataPatchRequest, bool) {
	var request metadataPatchRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid metadata patch request")
		return request, false
	}
	if len(request.TrackIDs) == 0 || len(request.TrackIDs) > maxBulkMetadataTracks {
		writeJSONError(w, http.StatusBadRequest, "trackIds must contain 1..500 items")
		return request, false
	}
	seen := make(map[string]bool, len(request.TrackIDs))
	out := request.TrackIDs[:0]
	for _, id := range request.TrackIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > musicmeta.MaxIDBytes {
			writeJSONError(w, http.StatusBadRequest, "invalid track id")
			return request, false
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	request.TrackIDs = out
	return request, true
}
