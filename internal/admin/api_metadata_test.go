package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/musicmeta"
	"github.com/F5GO/poisq/internal/security"
	"github.com/F5GO/poisq/internal/tagger"
)

func TestMetadataControlAPIContracts(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(folder, "song.flac")
	if err := os.WriteFile(filePath, []byte("fLaC-test"), 0o644); err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	guard, _ := security.NewPathGuard(root)
	folder = filepath.Join(guard.Root(), "Artist", "Album")
	filePath = filepath.Join(folder, "song.flac")
	tracks := db.NewTrackRepository(database)
	staging := db.NewStagingRepository(database)
	if err := tracks.UpsertTrack(db.TrackRecord{ID: "t1", FilePath: filePath, FolderPath: folder, Artist: "Artist", Album: "Album", Title: "Song", Duration: 120}); err != nil {
		t.Fatal(err)
	}
	metadataRepo := db.NewMetadataRepository(database)
	doc, _ := metadataRepo.GetTrackDocument(context.Background(), "t1")
	doc.Subtitle = "Mix"
	doc.Genres = []string{"Rock"}
	doc.FieldStates[musicmeta.FieldSubtitle] = musicmeta.FieldState{Source: musicmeta.SourceAudioFile, Confidence: .9}
	if err := metadataRepo.PutTrackDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	mediaInfo := db.NewMediaInfoRepository(database)
	if err := mediaInfo.Put(context.Background(), db.MediaInfoRecord{TrackID: "t1", Container: "flac", AudioCodec: "flac", SampleRate: 48000, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	analysisRepo := db.NewAudioAnalysisRepository(database)
	lufs, lra, peak := -13.0, 4.0, -1.2
	if err := analysisRepo.PutCalculated(context.Background(), db.AudioAnalysisRecord{TrackID: "t1", IntegratedLUFS: &lufs, LoudnessRangeLU: &lra, TruePeakDB: &peak, Fingerprint: db.FileFingerprint{Size: 9, MTimeNS: 1}, AnalyzerVersion: 1}); err != nil {
		t.Fatal(err)
	}
	policy := db.NewMetadataPolicyRepository(database)
	enrichment := db.NewMediaEnrichmentRepository(database)
	seed := "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	peer := "1b022e01-4da6-387b-8658-8678046e4cef"
	if err := enrichment.ReplaceSimilarArtists(context.Background(), seed, []db.SimilarArtistRecord{{ReferenceMBID: seed, SimilarMBID: peer, Name: "Peer", Score: .7, ListenCount: 7}}); err != nil {
		t.Fatal(err)
	}
	cover := metadata.NewCoverFetcher(guard)
	stagingSvc := tagger.NewStagingService(tagger.NewAtomicTagWriter(guard), cover, tracks, staging)
	scanner := metadata.NewLibraryScanner(root, tracks, staging, nil, cover)
	server := NewAdminServer(tracks, staging, db.NewJobRepository(database), db.NewProviderCacheRepository(database), nil, scanner, stagingSvc, nil, nil, guard, nil)
	audioService := metadata.NewAudioAnalysisService(guard, tracks, analysisRepo, staging, nil, 1)
	nfoService := metadata.NewNFOService(guard, tracks, metadataRepo, policy, staging)
	enrichmentService := metadata.NewMediaEnrichmentService(guard, staging, enrichment, nil)
	server.SetMetadataControlServices(MetadataControlServices{
		Metadata: metadataRepo, MediaInfo: mediaInfo, Policy: policy, Analysis: analysisRepo,
		Writebacks: staging.WritebackRepository(), AudioService: audioService, NFOService: nfoService,
		Enrichment: enrichment, EnrichmentSvc: enrichmentService, History: db.NewMetadataHistoryRepository(database),
	})

	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost:4535"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		return recorder
	}

	detail := request(http.MethodGet, "/api/v1/metadata/track?id=t1", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"subtitle":"Mix"`) || !strings.Contains(detail.Body.String(), `"container":"flac"`) || !strings.Contains(detail.Body.String(), `"integratedLufs":-13`) {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	lock := request(http.MethodPost, "/api/v1/metadata/lock", `{"trackId":"t1","field":"subtitle","locked":true}`)
	if lock.Code != http.StatusOK {
		t.Fatalf("lock: %d %s", lock.Code, lock.Body.String())
	}
	locked, _ := metadataRepo.GetTrackDocument(context.Background(), "t1")
	if !locked.FieldStates[musicmeta.FieldSubtitle].Locked {
		t.Fatal("subtitle was not locked")
	}
	badLock := request(http.MethodPost, "/api/v1/metadata/lock", `{"trackId":"t1","field":"unknown","locked":true}`)
	if badLock.Code != http.StatusBadRequest {
		t.Fatalf("unknown field lock status=%d", badLock.Code)
	}

	putPolicy := request(http.MethodPut, "/api/v1/metadata/policy", `{"rules":[{"source":"manual","priority":0,"refreshMode":"priority","enabled":true,"minConfidence":1},{"source":"musicbrainz","priority":10,"refreshMode":"newer","enabled":true,"minConfidence":0.8}]}`)
	if putPolicy.Code != http.StatusOK || !strings.Contains(putPolicy.Body.String(), `"refreshMode":"newer"`) {
		t.Fatalf("policy: %d %s", putPolicy.Code, putPolicy.Body.String())
	}
	badPolicy := request(http.MethodPut, "/api/v1/metadata/policy", `{"rules":[]}`)
	if badPolicy.Code != http.StatusBadRequest {
		t.Fatalf("bad policy status=%d", badPolicy.Code)
	}
	preview := request(http.MethodPost, "/api/v1/metadata/tags/preview", `{"trackIds":["t1","t1"],"patch":{"bpm":128,"rating":80}}`)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"field":"bpm"`) || !strings.Contains(preview.Body.String(), `"field":"rating"`) {
		t.Fatalf("tag preview: %d %s", preview.Code, preview.Body.String())
	}
	bulk := request(http.MethodPost, "/api/v1/metadata/tags/apply", `{"trackIds":["t1"],"patch":{"bpm":128,"rating":80}}`)
	if bulk.Code != http.StatusOK || !strings.Contains(bulk.Body.String(), `"created":1`) || !strings.Contains(bulk.Body.String(), `"stagingId"`) {
		t.Fatalf("tag apply: %d %s", bulk.Code, bulk.Body.String())
	}
	var bulkPayload struct {
		Items []struct {
			StagingID int64 `json:"stagingId"`
		} `json:"items"`
	}
	_ = json.Unmarshal(bulk.Body.Bytes(), &bulkPayload)
	historyRepo := db.NewMetadataHistoryRepository(database)
	if err := historyRepo.Append(context.Background(), "t1", bulkPayload.Items[0].StagingID, "tx-seed", "manual", "studio", "studio", []musicmeta.FieldChange{{Field: "title", Before: json.RawMessage(`"Old Song"`), After: json.RawMessage(`"Song"`)}}); err != nil {
		t.Fatal(err)
	}
	history := request(http.MethodGet, "/api/v1/metadata/history?trackId=t1", "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"field":"title"`) {
		t.Fatalf("history: %d %s", history.Code, history.Body.String())
	}
	var historyPayload struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	_ = json.Unmarshal(history.Body.Bytes(), &historyPayload)
	previewRevert := request(http.MethodPost, "/api/v1/metadata/history/revert/preview", fmt.Sprintf(`{"historyIds":[%d]}`, historyPayload.Items[0].ID))
	if previewRevert.Code != http.StatusOK || !strings.Contains(previewRevert.Body.String(), `Old Song`) || !strings.Contains(previewRevert.Body.String(), `"diverged":false`) {
		t.Fatalf("revert preview: %d %s", previewRevert.Code, previewRevert.Body.String())
	}
	divergedDoc, _ := metadataRepo.GetTrackDocument(context.Background(), "t1")
	divergedDoc.Title = "Later edit"
	if err := metadataRepo.PutTrackDocument(context.Background(), divergedDoc); err != nil {
		t.Fatal(err)
	}
	previewRevert = request(http.MethodPost, "/api/v1/metadata/history/revert/preview", fmt.Sprintf(`{"historyIds":[%d]}`, historyPayload.Items[0].ID))
	if previewRevert.Code != http.StatusOK || !strings.Contains(previewRevert.Body.String(), `"diverged":true`) {
		t.Fatalf("diverged revert preview: %d %s", previewRevert.Code, previewRevert.Body.String())
	}
	var revertPayload struct {
		Fingerprint string `json:"fingerprint"`
	}
	_ = json.Unmarshal(previewRevert.Body.Bytes(), &revertPayload)
	stale := request(http.MethodPost, "/api/v1/metadata/history/revert/apply", fmt.Sprintf(`{"historyIds":[%d],"fingerprint":"stale"}`, historyPayload.Items[0].ID))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revert=%d", stale.Code)
	}
	revert := request(http.MethodPost, "/api/v1/metadata/history/revert/apply", fmt.Sprintf(`{"historyIds":[%d],"fingerprint":%s}`, historyPayload.Items[0].ID, jsonString(revertPayload.Fingerprint)))
	if revert.Code != http.StatusOK || !strings.Contains(revert.Body.String(), `"status":"staged"`) {
		t.Fatalf("revert: %d %s", revert.Code, revert.Body.String())
	}

	nfo := request(http.MethodPost, "/api/v1/metadata/nfo", `{"folderPath":`+jsonString(folder)+`,"action":"export"}`)
	if nfo.Code != http.StatusOK || !strings.Contains(nfo.Body.String(), `"created":2`) {
		t.Fatalf("nfo: %d %s", nfo.Code, nfo.Body.String())
	}
	replayGain := request(http.MethodPost, "/api/v1/metadata/replaygain", `{"trackId":"t1","targetLufs":-14}`)
	if replayGain.Code != http.StatusOK || !strings.Contains(replayGain.Body.String(), `"status":"staged"`) {
		t.Fatalf("replaygain: %d %s", replayGain.Code, replayGain.Body.String())
	}
	artworkBody := `{"scope":"album","scopeId":"album-1","folderPath":` + jsonString(folder) + `,"role":"logo","candidates":[{"url":"https://images.example/logo.png","source":"fanarttv","width":1200,"height":400,"score":80}]}`
	artwork := request(http.MethodPost, "/api/v1/metadata/artwork", artworkBody)
	if artwork.Code != http.StatusOK || !strings.Contains(artwork.Body.String(), `"stagingId"`) {
		t.Fatalf("artwork: %d %s", artwork.Code, artwork.Body.String())
	}
	similar := request(http.MethodGet, "/api/v1/metadata/similar?artistMbid="+seed, "")
	if similar.Code != http.StatusOK || !strings.Contains(similar.Body.String(), peer) {
		t.Fatalf("similar: %d %s", similar.Code, similar.Body.String())
	}
	recovery := request(http.MethodGet, "/api/v1/metadata/writeback", "")
	if recovery.Code != http.StatusOK || !strings.Contains(recovery.Body.String(), `"healthy":true`) || strings.Contains(recovery.Body.String(), "targetPath") {
		t.Fatalf("recovery: %d %s", recovery.Code, recovery.Body.String())
	}
	page := request(http.MethodGet, "/api/v1/staging?status=pending&limit=1", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"nextCursor":"`) {
		t.Fatalf("staging page: %d %s", page.Code, page.Body.String())
	}
	badCursor := request(http.MethodGet, "/api/v1/staging?status=pending&cursor=broken", "")
	if badCursor.Code != http.StatusBadRequest {
		t.Fatalf("bad staging cursor status=%d", badCursor.Code)
	}
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
