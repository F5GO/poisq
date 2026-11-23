package metadata

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/musicmeta"
	"github.com/F5GO/poisq/internal/security"
)

type fakeAudioProber struct {
	result     LocalAudioMetadata
	err        error
	calls      atomic.Int32
	active     atomic.Int32
	maxActive  atomic.Int32
	blockUntil <-chan struct{}
}

func (p *fakeAudioProber) Probe(ctx context.Context, _, trackID string) (LocalAudioMetadata, error) {
	p.calls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		current := p.maxActive.Load()
		if active <= current || p.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	if p.blockUntil != nil {
		select {
		case <-p.blockUntil:
		case <-ctx.Done():
			return LocalAudioMetadata{}, ctx.Err()
		}
	}
	result := p.result
	result.Document = result.Document.Clone()
	result.Document.TrackID = trackID
	return result, p.err
}

type localImporterFixture struct {
	database  *sql.DB
	tracks    *db.TrackRepository
	metadata  *db.MetadataRepository
	mediaInfo *db.MediaInfoRepository
	guard     *security.PathGuard
	root      string
}

func newLocalImporterFixture(t *testing.T) localImporterFixture {
	t.Helper()
	root := t.TempDir()
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	guard, err := security.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	return localImporterFixture{
		database: database, tracks: db.NewTrackRepository(database), metadata: db.NewMetadataRepository(database),
		mediaInfo: db.NewMediaInfoRepository(database), guard: guard, root: root,
	}
}

func (f localImporterFixture) addTrack(t *testing.T, id string) db.TrackRecord {
	t.Helper()
	path := filepath.Join(f.root, id+".flac")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := db.TrackRecord{ID: id, FilePath: path, Artist: "Fallback Artist", Album: "Fallback Album", Title: "Fallback Title"}
	if err := f.tracks.UpsertTrack(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestLocalImporterReconcilesLocksFallbackAndFingerprint(t *testing.T) {
	f := newLocalImporterFixture(t)
	track := f.addTrack(t, "track")
	base, err := f.metadata.GetTrackDocument(context.Background(), track.ID)
	if err != nil {
		t.Fatal(err)
	}
	base.Genres = []string{"Fallback Genre"}
	base.FieldStates[musicmeta.FieldGenres] = musicmeta.FieldState{Source: musicmeta.SourceNavidrome, Confidence: 1}
	base.FieldStates[musicmeta.FieldAlbum] = musicmeta.FieldState{Source: musicmeta.SourceManual, Confidence: 1, Locked: true}
	if err := f.metadata.PutTrackDocument(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	localState := musicmeta.FieldState{Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1, UpdatedAt: now}
	prober := &fakeAudioProber{result: LocalAudioMetadata{
		Document: musicmeta.Document{
			Title: "Embedded Title", Album: "Embedded Album",
			FieldStates: map[musicmeta.Field]musicmeta.FieldState{musicmeta.FieldTitle: localState, musicmeta.FieldAlbum: localState},
		},
		AudioCodec: "flac", SampleRate: 48000, DurationMillis: 1234,
		ReplayGain: ReplayGainMetadata{TrackGainDB: float64Pointer(-7.5)},
	}}
	importer := NewLocalImporter(f.guard, f.metadata, f.mediaInfo, prober, 2)
	analysisRepo := db.NewAudioAnalysisRepository(f.database)
	importer.SetAnalysisRepository(analysisRepo)
	result, err := importer.ImportTrack(context.Background(), track)
	if err != nil || !result.Imported {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	got, err := f.metadata.GetTrackDocument(context.Background(), track.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Embedded Title" || got.Album != "Fallback Album" || len(got.Genres) != 1 || got.Genres[0] != "Fallback Genre" {
		t.Fatalf("unexpected merged document: %#v", got)
	}
	if !got.FieldStates[musicmeta.FieldAlbum].Locked || got.FieldStates[musicmeta.FieldTitle].Source != musicmeta.SourceAudioFile {
		t.Fatalf("unexpected provenance: %#v", got.FieldStates)
	}
	analysis, err := analysisRepo.Get(context.Background(), track.ID)
	if err != nil || analysis.ReplayGainTrackGainDB == nil || *analysis.ReplayGainTrackGainDB != -7.5 {
		t.Fatalf("ReplayGain was not imported: %#v err=%v", analysis, err)
	}
	track.Title = "Navidrome Rescan Title"
	track.Album = "Navidrome Rescan Album"
	if err := f.tracks.UpsertTrack(track); err != nil {
		t.Fatal(err)
	}
	result, err = importer.ImportTrack(context.Background(), track)
	if err != nil || !result.Skipped || prober.calls.Load() != 1 {
		t.Fatalf("unchanged import result=%#v calls=%d err=%v", result, prober.calls.Load(), err)
	}
	got, _ = f.metadata.GetTrackDocument(context.Background(), track.ID)
	if got.Title != "Embedded Title" || got.Album != "Fallback Album" {
		t.Fatalf("inventory rescan overwrote authoritative metadata: %#v", got)
	}
	if err := os.WriteFile(track.FilePath, []byte("audio changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = importer.ImportTrack(context.Background(), track)
	if err != nil || !result.Imported || prober.calls.Load() != 2 {
		t.Fatalf("changed import result=%#v calls=%d err=%v", result, prober.calls.Load(), err)
	}
}

func TestLocalImporterFailurePreservesDocumentAndMediaFacts(t *testing.T) {
	f := newLocalImporterFixture(t)
	track := f.addTrack(t, "track")
	good := &fakeAudioProber{result: LocalAudioMetadata{
		Document: musicmeta.Document{Title: "Imported", FieldStates: map[musicmeta.Field]musicmeta.FieldState{
			musicmeta.FieldTitle: {Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1},
		}},
		AudioCodec: "flac", SampleRate: 44100,
	}}
	if _, err := NewLocalImporter(f.guard, f.metadata, f.mediaInfo, good, 1).ImportTrack(context.Background(), track); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(track.FilePath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := &fakeAudioProber{err: errors.New("broken file")}
	if _, err := NewLocalImporter(f.guard, f.metadata, f.mediaInfo, bad, 1).ImportTrack(context.Background(), track); err == nil {
		t.Fatal("expected probe error")
	}
	doc, _ := f.metadata.GetTrackDocument(context.Background(), track.ID)
	info, _ := f.mediaInfo.Get(context.Background(), track.ID)
	if doc.Title != "Imported" || info.AudioCodec != "flac" || info.SampleRate != 44100 || info.Status != "failed" {
		t.Fatalf("failure discarded valid state: doc=%#v info=%#v", doc, info)
	}
}

func TestLocalImporterUsesConfiguredMergePolicy(t *testing.T) {
	f := newLocalImporterFixture(t)
	track := f.addTrack(t, "track")
	state := musicmeta.FieldState{Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1}
	prober := &fakeAudioProber{result: LocalAudioMetadata{Document: musicmeta.Document{
		Title: "Embedded", FieldStates: map[musicmeta.Field]musicmeta.FieldState{musicmeta.FieldTitle: state},
	}}}
	importer := NewLocalImporter(f.guard, f.metadata, f.mediaInfo, prober, 1)
	importer.SetPolicyProvider(func(context.Context) (musicmeta.MergePolicy, error) {
		return musicmeta.MergePolicy{Rules: []musicmeta.ProviderRule{
			{Source: musicmeta.SourceLegacy, Priority: 1, RefreshMode: musicmeta.RefreshPriority, Enabled: true},
			{Source: musicmeta.SourceAudioFile, Priority: 10, RefreshMode: musicmeta.RefreshNever, Enabled: true},
		}}, nil
	})
	if _, err := importer.ImportTrack(context.Background(), track); err != nil {
		t.Fatal(err)
	}
	doc, _ := f.metadata.GetTrackDocument(context.Background(), track.ID)
	if doc.Title != "Fallback Title" {
		t.Fatalf("configured never policy ignored: %#v", doc)
	}
}

func TestLocalImporterBoundsWorkerConcurrency(t *testing.T) {
	f := newLocalImporterFixture(t)
	tracks := make([]db.TrackRecord, 0, 6)
	for n := 0; n < 6; n++ {
		tracks = append(tracks, f.addTrack(t, string(rune('a'+n))))
	}
	release := make(chan struct{})
	prober := &fakeAudioProber{blockUntil: release, result: LocalAudioMetadata{Document: musicmeta.Document{FieldStates: map[musicmeta.Field]musicmeta.FieldState{}}}}
	importer := NewLocalImporter(f.guard, f.metadata, f.mediaInfo, prober, 2)
	done := make(chan ImportStats, 1)
	go func() { done <- importer.ImportTracks(context.Background(), tracks) }()
	deadline := time.After(2 * time.Second)
	for prober.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("workers did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if prober.calls.Load() != 2 || prober.maxActive.Load() != 2 {
		t.Fatalf("calls=%d maxActive=%d", prober.calls.Load(), prober.maxActive.Load())
	}
	close(release)
	stats := <-done
	if stats.Imported != len(tracks) || stats.Failed != 0 || prober.maxActive.Load() > 2 {
		t.Fatalf("stats=%#v maxActive=%d", stats, prober.maxActive.Load())
	}
}

func TestLibraryScannerRunsOptionalLocalImporterWithoutChangingSyncResult(t *testing.T) {
	f := newLocalImporterFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "song.flac"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	navDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nav.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer navDB.Close()
	if _, err := navDB.Exec(`
		CREATE TABLE media_file (
			id TEXT PRIMARY KEY, path TEXT, title TEXT, artist TEXT, album TEXT, album_artist TEXT,
			track_number INTEGER, year INTEGER, duration REAL, has_cover_art INTEGER, embed_art_path TEXT
		);
		INSERT INTO media_file VALUES ('t', 'song.flac', 'Nav Title', 'Nav Artist', 'Nav Album', 'Nav Artist', 1, 2020, 180, 0, '');
	`); err != nil {
		t.Fatal(err)
	}
	state := musicmeta.FieldState{Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1}
	prober := &fakeAudioProber{result: LocalAudioMetadata{Document: musicmeta.Document{
		Title: "Embedded Title", FieldStates: map[musicmeta.Field]musicmeta.FieldState{musicmeta.FieldTitle: state},
	}}}
	scanner := NewLibraryScanner(f.root, f.tracks, db.NewStagingRepository(f.database), nil, nil)
	scanner.SetLocalImporter(NewLocalImporter(f.guard, f.metadata, f.mediaInfo, prober, 2))
	synced, err := scanner.SyncFromNavidromeDB(navDB)
	if err != nil || synced != 1 {
		t.Fatalf("synced=%d err=%v", synced, err)
	}
	doc, err := f.metadata.GetTrackDocument(context.Background(), "t")
	if err != nil || doc.Title != "Embedded Title" || prober.calls.Load() != 1 {
		t.Fatalf("doc=%#v calls=%d err=%v", doc, prober.calls.Load(), err)
	}
}
