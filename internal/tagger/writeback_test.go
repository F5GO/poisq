package tagger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

func TestAtomicWriterBackupRestoreIsExactAndDetectsTampering(t *testing.T) {
	root := t.TempDir()
	guard, err := security.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewAtomicTagWriter(guard)
	target := filepath.Join(root, "song.mp3")
	original := []byte("original audio bytes and unknown tags")
	if err := os.WriteFile(target, original, 0o640); err != nil {
		t.Fatal(err)
	}
	txID := strings.Repeat("a", 32)
	backup, err := writer.PrepareBackup(target, txID)
	if err != nil {
		t.Fatal(err)
	}
	if !backup.OriginalExists || backup.OriginalSize != int64(len(original)) || len(backup.OriginalSHA256) != 64 {
		t.Fatalf("backup=%#v", backup)
	}
	if err := os.WriteFile(target, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.RestoreBackup(backup, txID); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(original) {
		t.Fatalf("restored bytes=%q", got)
	}
	if err := os.WriteFile(backup.BackupPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.RestoreBackup(backup, txID); err == nil {
		t.Fatal("tampered backup restored")
	}
}

func TestAtomicWriterRestoreRemovesNewlyCreatedTarget(t *testing.T) {
	root := t.TempDir()
	guard, _ := security.NewPathGuard(root)
	writer := NewAtomicTagWriter(guard)
	target := filepath.Join(root, "cover.jpg")
	txID := strings.Repeat("b", 32)
	backup, err := writer.PrepareBackup(target, txID)
	if err != nil || backup.OriginalExists {
		t.Fatalf("backup=%#v err=%v", backup, err)
	}
	if err := os.WriteFile(target, []byte("new cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.RestoreBackup(backup, txID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("new target was not removed: %v", err)
	}
}

func TestVerifyTagsChecksDistinctMusicBrainzIDs(t *testing.T) {
	root := t.TempDir()
	guard, _ := security.NewPathGuard(root)
	target := filepath.Join(root, "song.mp3")
	_ = os.WriteFile(target, []byte("audio"), 0o600)
	script := filepath.Join(t.TempDir(), "ffprobe")
	body := `#!/bin/sh
printf '%s' '{"format":{"tags":{"artist":"A","album":"B","album_artist":"A","title":"T","date":"2020","track":"2/10","MUSICBRAINZ_ALBUMID":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","MUSICBRAINZ_RELEASEGROUPID":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","MUSICBRAINZ_ARTISTID":"cccccccc-cccc-cccc-cccc-cccccccccccc","MUSICBRAINZ_TRACKID":"dddddddd-dddd-dddd-dddd-dddddddddddd","MUSICBRAINZ_RELEASETRACKID":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"}}}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writer := NewAtomicTagWriter(guard)
	writer.ffprobeExecutable = script
	tags := CanonicalTagSet{
		Artist: "A", Album: "B", AlbumArtist: "A", Title: "T", Year: 2020, TrackNumber: 2,
		ReleaseMBID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", ReleaseGroupMBID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		ArtistMBID: "cccccccc-cccc-cccc-cccc-cccccccccccc", RecordingMBID: "dddddddd-dddd-dddd-dddd-dddddddddddd",
		ReleaseTrackMBID: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
	}
	if err := writer.VerifyTags(context.Background(), target, tags); err != nil {
		t.Fatal(err)
	}
	tags.ReleaseTrackMBID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if err := writer.VerifyTags(context.Background(), target, tags); err == nil {
		t.Fatal("wrong release-track id passed verification")
	}
}

func TestStagingWritebackApplyAndUndoRestoresExactBytes(t *testing.T) {
	root := t.TempDir()
	guard, _ := security.NewPathGuard(root)
	audio := filepath.Join(root, "song.mp3")
	original := []byte(strings.Repeat("original-audio-and-tags", 100))
	if err := os.WriteFile(audio, original, 0o640); err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := db.NewTrackRepository(database)
	staging := db.NewStagingRepository(database)
	if err := tracks.UpsertTrack(db.TrackRecord{ID: "t", FilePath: audio, FolderPath: root, Artist: "Old", AlbumArtist: "Old", Album: "Album", Title: "Old title", TrackNumber: 1, Year: 2000}); err != nil {
		t.Fatal(err)
	}
	proposed := CanonicalTagSet{Artist: "New", AlbumArtist: "New", Album: "Album", Title: "New title", TrackNumber: 2, Year: 2020}
	proposedJSON, _ := json.Marshal(proposed)
	proposalID, err := staging.CreateProposal(db.StagingChangeRecord{
		TargetID: "t", FolderPath: root, ChangeType: "manual", Confidence: 100,
		CurrentJSON: `{"artist":"Old","title":"Old title"}`, ProposedJSON: string(proposedJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	ffmpegBody := `#!/bin/sh
input=''
last=''
need_input=0
for arg in "$@"; do
  if [ "$need_input" = 1 ]; then input="$arg"; need_input=0
  elif [ "$arg" = '-i' ]; then need_input=1
  fi
  last="$arg"
done
cp "$input" "$last"
printf '%s' 'changed-by-writer' >> "$last"
`
	if err := os.WriteFile(ffmpeg, []byte(ffmpegBody), 0o755); err != nil {
		t.Fatal(err)
	}
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	ffprobeBody := `#!/bin/sh
printf '%s' '{"format":{"tags":{"artist":"New","album":"Album","album_artist":"New","title":"New title","date":"2020","track":"2"}}}'
`
	if err := os.WriteFile(ffprobe, []byte(ffprobeBody), 0o755); err != nil {
		t.Fatal(err)
	}
	writer := NewAtomicTagWriter(guard)
	writer.ffmpegExecutable = ffmpeg
	writer.ffprobeExecutable = ffprobe
	service := NewStagingService(writer, nil, tracks, staging)
	if err := service.ApplyProposalWithCustomCover(context.Background(), proposalID, ""); err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(audio)
	if string(changed) == string(original) {
		t.Fatal("writer did not replace target")
	}
	tx, err := staging.WritebackRepository().GetByProposal(context.Background(), proposalID)
	if err != nil || tx.State != "committed" || !tx.Reconciled {
		t.Fatalf("tx=%#v err=%v", tx, err)
	}
	track, _ := tracks.GetByID("t")
	if track.Title != "New title" || track.TrackNumber != 2 {
		t.Fatalf("projection not reconciled: %#v", track)
	}
	history, err := db.NewMetadataHistoryRepository(database).List(context.Background(), "t", "", 0, 0, 50)
	if err != nil || len(history) == 0 {
		t.Fatalf("field history missing: %#v err=%v", history, err)
	}
	seenTitle := false
	for _, item := range history {
		if item.Field == "title" && string(item.OldValue) == `"Old title"` && string(item.NewValue) == `"New title"` {
			seenTitle = true
		}
	}
	if !seenTitle {
		t.Fatalf("title history missing: %#v", history)
	}
	if err := service.UndoProposal(context.Background(), proposalID); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(audio)
	if string(restored) != string(original) {
		t.Fatal("undo did not restore exact original bytes")
	}
	proposal, _ := staging.GetByID(proposalID)
	tx, _ = staging.WritebackRepository().GetByProposal(context.Background(), proposalID)
	if proposal.Status != "reverted" || tx.State != "rolled_back" {
		t.Fatalf("proposal=%#v tx=%#v", proposal, tx)
	}
}

func TestStagingWritebackRecoversIncompleteWrite(t *testing.T) {
	root := t.TempDir()
	guard, _ := security.NewPathGuard(root)
	audio := filepath.Join(root, "song.mp3")
	original := []byte(strings.Repeat("original", 100))
	_ = os.WriteFile(audio, original, 0o600)
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := db.NewTrackRepository(database)
	staging := db.NewStagingRepository(database)
	_ = tracks.UpsertTrack(db.TrackRecord{ID: "t", FilePath: audio, FolderPath: root, Artist: "Old", Title: "Old"})
	proposalID, _ := staging.CreateProposal(db.StagingChangeRecord{TargetID: "t", FolderPath: root, ChangeType: "manual", CurrentJSON: `{}`, ProposedJSON: `{"artist":"New","title":"New"}`})
	writer := NewAtomicTagWriter(guard)
	txID := strings.Repeat("c", 32)
	backup, err := writer.PrepareBackup(audio, txID)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, _ := json.Marshal(CanonicalTagSet{Artist: "Old", Title: "Old"})
	repo := staging.WritebackRepository()
	if err := repo.CreatePrepared(context.Background(), db.WritebackTransaction{
		TransactionID: txID, ProposalID: proposalID, Kind: "tags", TargetPath: backup.TargetPath,
		BackupPath: backup.BackupPath, OriginalExists: true, OriginalSize: backup.OriginalSize,
		OriginalSHA256: backup.OriginalSHA256, OriginalJSON: string(originalJSON),
	}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(audio, []byte("partial replacement"), 0o600)
	_ = repo.UpdateState(context.Background(), txID, "written", nil)
	service := NewStagingService(writer, nil, tracks, staging)
	if err := service.RecoverIncomplete(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(audio)
	if string(restored) != string(original) {
		t.Fatal("startup recovery did not restore original")
	}
	tx, _ := repo.GetByProposal(context.Background(), proposalID)
	proposal, _ := staging.GetByID(proposalID)
	if tx.State != "rolled_back" || proposal.Status != "pending" {
		t.Fatalf("tx=%#v proposal=%#v", tx, proposal)
	}
}

func TestNFOExportApplyAndUndoRestoresFileState(t *testing.T) {
	root := t.TempDir()
	guard, _ := security.NewPathGuard(root)
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := db.NewTrackRepository(database)
	staging := db.NewStagingRepository(database)
	target := filepath.Join(root, "album.nfo")
	proposed, _ := json.Marshal(map[string]any{"path": target, "content": "<?xml version=\"1.0\"?>\n<album><title>Album</title></album>\n"})
	current, _ := json.Marshal(map[string]any{"path": target, "exists": false, "content": ""})
	proposalID, err := staging.CreateProposal(db.StagingChangeRecord{
		Scope: "file", TargetID: target, FolderPath: root, ChangeType: "nfo_export", Confidence: 100,
		CurrentJSON: string(current), ProposedJSON: string(proposed),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := NewStagingService(NewAtomicTagWriter(guard), nil, tracks, staging)
	if err := service.ApplyProposalWithCustomCover(context.Background(), proposalID, ""); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(target); err != nil || !strings.Contains(string(content), "<title>Album</title>") {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if err := service.UndoProposal(context.Background(), proposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("new NFO was not removed: %v", err)
	}
}

func TestArtworkProjectionApplyAndRestore(t *testing.T) {
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := db.NewMediaEnrichmentRepository(database)
	previous := db.ArtworkRecord{Scope: "artist", ScopeID: "artist-1", Role: "logo", FilePath: "/music/old/logo.png", Source: "old", Width: 900, Height: 300, Score: 60}
	if err := repo.PutArtwork(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	current, _ := json.Marshal(map[string]any{"previous": previous})
	proposed, _ := json.Marshal(map[string]any{
		"scope": "artist", "scopeId": "artist-1", "role": "logo", "source": "fanarttv",
		"width": 1200, "height": 400, "score": 95,
	})
	record := &db.StagingChangeRecord{CurrentJSON: string(current), ProposedJSON: string(proposed)}
	service := &StagingService{enrichment: repo}
	if err := service.reconcileArtworkApplied(context.Background(), record, "/music/new/logo.png"); err != nil {
		t.Fatal(err)
	}
	values, _ := repo.ListArtwork(context.Background(), "artist", "artist-1")
	if len(values) != 1 || values[0].FilePath != "/music/new/logo.png" || values[0].Source != "fanarttv" {
		t.Fatalf("applied=%#v", values)
	}
	if err := service.reconcileArtworkRestored(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	values, _ = repo.ListArtwork(context.Background(), "artist", "artist-1")
	if len(values) != 1 || values[0].FilePath != previous.FilePath || values[0].Source != previous.Source {
		t.Fatalf("restored=%#v", values)
	}

	current, _ = json.Marshal(map[string]any{"previous": nil})
	record.CurrentJSON = string(current)
	if err := service.reconcileArtworkRestored(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	values, _ = repo.ListArtwork(context.Background(), "artist", "artist-1")
	if len(values) != 0 {
		t.Fatalf("new artwork projection not deleted: %#v", values)
	}
}
