package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/musicmeta"
)

func (s *AdminServer) handleMetadataHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || s.historyRepo == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeJSONError(w, http.StatusBadRequest, "invalid history limit")
			return
		}
		limit = value
	}
	var beforeAt, beforeID int64
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 {
			writeJSONError(w, http.StatusBadRequest, "invalid history cursor")
			return
		}
		beforeAt, _ = strconv.ParseInt(parts[0], 10, 64)
		beforeID, _ = strconv.ParseInt(parts[1], 10, 64)
		if beforeAt <= 0 || beforeID <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid history cursor")
			return
		}
	}
	items, err := s.historyRepo.List(r.Context(), strings.TrimSpace(r.URL.Query().Get("trackId")), strings.TrimSpace(r.URL.Query().Get("field")), beforeAt, beforeID, limit)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = fmt.Sprintf("%d:%d", last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

type historyRevertRequest struct {
	HistoryIDs  []int64 `json:"historyIds"`
	Fingerprint string  `json:"fingerprint,omitempty"`
}

func (s *AdminServer) handleMetadataHistoryRevertPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.historyRepo == nil || s.metadataRepo == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	_, records, current, proposed, ok := s.prepareHistoryRevert(w, r)
	if !ok {
		return
	}
	fingerprint, _ := musicmeta.DocumentFingerprint(current)
	changes, _ := musicmeta.DiffDocuments(current, proposed)
	diverged := false
	for _, record := range records {
		withHistoricalNew, err := musicmeta.ApplyFieldValues(current, []musicmeta.FieldValue{{Field: record.Field, Value: record.NewValue}})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		newDiff, _ := musicmeta.DiffDocuments(current, withHistoricalNew)
		if len(newDiff) != 0 {
			diverged = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trackId": current.TrackID, "fingerprint": fingerprint, "changes": changes, "diverged": diverged})
}

func (s *AdminServer) handleMetadataHistoryRevertApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.historyRepo == nil || s.metadataRepo == nil || s.stagingSvc == nil {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed or service unavailable")
		return
	}
	request, _, current, proposed, ok := s.prepareHistoryRevert(w, r)
	if !ok {
		return
	}
	changes, err := musicmeta.DiffDocuments(current, proposed)
	if err != nil || len(changes) == 0 {
		writeJSONError(w, http.StatusBadRequest, "selected fields already match their historical values")
		return
	}
	fingerprint, _ := musicmeta.DocumentFingerprint(current)
	if request.Fingerprint == "" || request.Fingerprint != fingerprint {
		writeJSONError(w, http.StatusConflict, "metadata changed; refresh revert preview")
		return
	}
	id, err := s.stagingSvc.StageDocumentEdit(r.Context(), current, proposed, "Откат выбранных полей истории")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.historyRepo.LinkRevert(r.Context(), request.HistoryIDs, id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "staged", "stagingId": id})
}

func (s *AdminServer) prepareHistoryRevert(w http.ResponseWriter, r *http.Request) (historyRevertRequest, []db.MetadataHistoryRecord, musicmeta.Document, musicmeta.Document, bool) {
	var request historyRevertRequest
	if err := decodeJSONBody(r, &request); err != nil || len(request.HistoryIDs) == 0 || len(request.HistoryIDs) > 200 {
		writeJSONError(w, http.StatusBadRequest, "invalid history revert request")
		return request, nil, musicmeta.Document{}, musicmeta.Document{}, false
	}
	records, err := s.historyRepo.ListByIDs(r.Context(), request.HistoryIDs)
	if err != nil || len(records) != len(request.HistoryIDs) {
		writeJSONError(w, http.StatusBadRequest, "history records not found")
		return request, nil, musicmeta.Document{}, musicmeta.Document{}, false
	}
	trackID := records[0].TrackID
	values := make([]musicmeta.FieldValue, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		if record.TrackID != trackID || seen[record.Field] {
			writeJSONError(w, http.StatusBadRequest, "history records must target unique fields of one track")
			return request, nil, musicmeta.Document{}, musicmeta.Document{}, false
		}
		seen[record.Field] = true
		values = append(values, musicmeta.FieldValue{Field: record.Field, Value: record.OldValue})
	}
	current, err := s.metadataRepo.GetTrackDocument(r.Context(), trackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return request, nil, musicmeta.Document{}, musicmeta.Document{}, false
	}
	proposed, err := musicmeta.ApplyFieldValues(current, values)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return request, nil, musicmeta.Document{}, musicmeta.Document{}, false
	}
	return request, records, current, proposed, true
}
