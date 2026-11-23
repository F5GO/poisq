package metadata

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/musicmeta"
	"github.com/F5GO/poisq/internal/security"
)

type nfoFixture struct {
	root     string
	folder   string
	database interface{ Close() error }
	tracks   *db.TrackRepository
	metadata *db.MetadataRepository
	policy   *db.MetadataPolicyRepository
	staging  *db.StagingRepository
	guard    *security.PathGuard
	service  *NFOService
}

func newNFOFixture(t *testing.T) nfoFixture {
	t.Helper()
	rawRoot := t.TempDir()
	guard, _ := security.NewPathGuard(rawRoot)
	root := guard.Root()
	folder := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tracks := db.NewTrackRepository(database)
	metadataRepo := db.NewMetadataRepository(database)
	policy := db.NewMetadataPolicyRepository(database)
	staging := db.NewStagingRepository(database)
	return nfoFixture{
		root: root, folder: folder, database: database, tracks: tracks, metadata: metadataRepo,
		policy: policy, staging: staging, guard: guard,
		service: NewNFOService(guard, tracks, metadataRepo, policy, staging),
	}
}

func (f nfoFixture) addTrack(t *testing.T) {
	t.Helper()
	if err := f.tracks.UpsertTrack(db.TrackRecord{
		ID: "t", FilePath: filepath.Join(f.folder, "01 Song.flac"), FolderPath: f.folder,
		Artist: "Artist", AlbumArtist: "Artist", Album: "Locked Album", Title: "Song", TrackNumber: 1, Year: 2000,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNFORenderAndParseRoundTrip(t *testing.T) {
	value := AlbumNFO{
		Title: "Album & More", Artist: "Artist", Year: 2020, ReleaseDate: "2020-02-03",
		Genres: []string{"Rock", "Alternative"}, MusicBrainzAlbumID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	}
	content, err := renderNFO(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "album.nfo")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, _, err := readAlbumNFO(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Title != value.Title || parsed.ReleaseDate != value.ReleaseDate || len(parsed.Genres) != 2 || parsed.MusicBrainzAlbumID != value.MusicBrainzAlbumID {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestNFOImportUsesMergePolicyAndRespectsLockedFields(t *testing.T) {
	f := newNFOFixture(t)
	f.addTrack(t)
	doc, _ := f.metadata.GetTrackDocument(context.Background(), "t")
	doc.FieldStates[musicmeta.FieldAlbum] = musicmeta.FieldState{Source: musicmeta.SourceManual, Confidence: 1, Locked: true}
	if err := f.metadata.PutTrackDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	albumXML := `<?xml version="1.0" encoding="UTF-8"?>
<album><title>NFO Album</title><artist>NFO Artist</artist><year>2021</year><genre>Rock</genre><label>Label</label><musicbrainzalbumid>aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa</musicbrainzalbumid></album>`
	artistXML := `<artist><name>NFO Artist</name><musicbrainzartistid>bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb</musicbrainzartistid></artist>`
	if err := os.WriteFile(filepath.Join(f.folder, "album.nfo"), []byte(albumXML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.folder), "artist.nfo"), []byte(artistXML), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := f.service.ImportFolder(context.Background(), f.folder)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	items, _ := f.staging.List("pending", 10)
	if len(items) != 1 || items[0].ChangeType != "nfo_import" {
		t.Fatalf("items=%#v", items)
	}
	var proposed map[string]any
	_ = json.Unmarshal([]byte(items[0].ProposedJSON), &proposed)
	if proposed["album"] != "Locked Album" || proposed["label"] != "Label" || proposed["releaseMbid"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("proposal=%#v", proposed)
	}
}

func TestNFOExportStagesDeterministicFiles(t *testing.T) {
	f := newNFOFixture(t)
	f.addTrack(t)
	doc, _ := f.metadata.GetTrackDocument(context.Background(), "t")
	doc.Genres = []string{"Rock"}
	doc.Label = "Label"
	doc.ExternalIDs = musicmeta.ExternalIDs{{Type: musicmeta.IDMusicBrainzRelease, Value: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}
	if err := f.metadata.PutTrackDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	created, err := f.service.StageExportFolder(context.Background(), f.folder)
	if err != nil || created != 2 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	items, _ := f.staging.List("pending", 10)
	var albumProposal db.StagingChangeRecord
	for _, item := range items {
		if item.TargetID == filepath.Join(f.folder, "album.nfo") {
			albumProposal = item
		}
	}
	if albumProposal.ID == 0 {
		t.Fatalf("album export not staged: %#v", items)
	}
	var proposed map[string]any
	if err := json.Unmarshal([]byte(albumProposal.ProposedJSON), &proposed); err != nil {
		t.Fatal(err)
	}
	content, _ := proposed["content"].(string)
	if !strings.Contains(content, "<musicbrainzalbumid>aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa</musicbrainzalbumid>") {
		t.Fatalf("content=%s", content)
	}
}

func TestNFORejectsDirectivesOversizeAndPathEscape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "album.nfo")
	if err := os.WriteFile(path, []byte(`<!DOCTYPE album [<!ENTITY x "bad">]><album><title>&x;</title></album>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAlbumNFO(path); err == nil {
		t.Fatal("entity-bearing NFO accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxNFOBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAlbumNFO(path); err == nil {
		t.Fatal("oversized NFO accepted")
	}
	f := newNFOFixture(t)
	if _, err := f.service.StageExportFolder(context.Background(), filepath.Join(f.root, "..")); err == nil {
		t.Fatal("path escape accepted")
	}
}
