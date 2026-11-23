package metadata

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

func TestParseEBUR128SummaryUsesFinalSummary(t *testing.T) {
	output := " I: -30.0 LUFS\n LRA: 1.0 LU\n Peak: -8.0 dBFS\nSummary:\n  I: -14.2 LUFS\n  LRA: 5.3 LU\n  Peak: -1.1 dBFS\n"
	got, err := parseEBUR128Summary(output)
	if err != nil || got.IntegratedLUFS != -14.2 || got.LoudnessRangeLU != 5.3 || got.TruePeakDB != -1.1 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if _, err := parseEBUR128Summary("not a summary"); err == nil {
		t.Fatal("malformed summary accepted")
	}
}

func TestFFmpegLoudnessAnalyzerBoundedExecution(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ffmpeg")
	body := "#!/bin/sh\nprintf '%s' '  I: -12.0 LUFS\n  LRA: 4.0 LU\n  Peak: -0.5 dBFS\n' >&2\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := NewFFmpegLoudnessAnalyzer(script).Analyze(context.Background(), "/ignored.flac")
	if err != nil || got.IntegratedLUFS != -12 || got.TruePeakDB != -.5 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	slow := filepath.Join(t.TempDir(), "slow")
	_ = os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	analyzer := NewFFmpegLoudnessAnalyzer(slow)
	analyzer.timeout = 20 * time.Millisecond
	if _, err := analyzer.Analyze(context.Background(), "/ignored"); err == nil {
		t.Fatal("analysis timeout not enforced")
	}
}

type fakeLoudnessAnalyzer struct {
	result    LoudnessResult
	calls     atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
	block     <-chan struct{}
}

func (a *fakeLoudnessAnalyzer) Analyze(ctx context.Context, _ string) (LoudnessResult, error) {
	a.calls.Add(1)
	active := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		current := a.maxActive.Load()
		if active <= current || a.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return LoudnessResult{}, ctx.Err()
		}
	}
	return a.result, nil
}

func TestAudioAnalysisServiceFingerprintConcurrencyAlbumAndStaging(t *testing.T) {
	rawRoot := t.TempDir()
	guard, _ := security.NewPathGuard(rawRoot)
	root := guard.Root()
	folder := filepath.Join(root, "album")
	_ = os.MkdirAll(folder, 0o755)
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracksRepo := db.NewTrackRepository(database)
	repo := db.NewAudioAnalysisRepository(database)
	staging := db.NewStagingRepository(database)
	tracks := make([]db.TrackRecord, 0, 4)
	for index := 0; index < 4; index++ {
		path := filepath.Join(folder, string(rune('a'+index))+".flac")
		_ = os.WriteFile(path, []byte("audio"), 0o600)
		track := db.TrackRecord{ID: string(rune('a' + index)), FilePath: path, FolderPath: folder, Artist: "A", Album: "B", Title: "T", Duration: index + 1}
		_ = tracksRepo.UpsertTrack(track)
		tracks = append(tracks, track)
	}
	release := make(chan struct{})
	fake := &fakeLoudnessAnalyzer{result: LoudnessResult{IntegratedLUFS: -10, LoudnessRangeLU: 3, TruePeakDB: -1}, block: release}
	service := NewAudioAnalysisService(guard, tracksRepo, repo, staging, fake, 2)
	done := make(chan AnalysisBatchStats, 1)
	go func() { done <- service.AnalyzeTracks(context.Background(), tracks) }()
	deadline := time.After(2 * time.Second)
	for fake.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("analysis workers did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if fake.maxActive.Load() != 2 {
		t.Fatalf("max concurrency=%d", fake.maxActive.Load())
	}
	close(release)
	stats := <-done
	if stats.Analyzed != 4 || stats.Failed != 0 {
		t.Fatalf("stats=%#v", stats)
	}
	stats = service.AnalyzeTracks(context.Background(), tracks)
	if stats.Skipped != 4 || fake.calls.Load() != 4 {
		t.Fatalf("fingerprint skip failed: stats=%#v calls=%d", stats, fake.calls.Load())
	}
	// Change one stored value to verify the duration-weighted album formula.
	lufs, lra, peak := -20.0, 2.0, -3.0
	stat, _ := os.Stat(tracks[1].FilePath)
	_ = repo.PutCalculated(context.Background(), db.AudioAnalysisRecord{
		TrackID: tracks[1].ID, IntegratedLUFS: &lufs, LoudnessRangeLU: &lra, TruePeakDB: &peak,
		Fingerprint: db.FileFingerprint{Size: stat.Size(), MTimeNS: stat.ModTime().UnixNano()}, AnalyzerVersion: audioAnalyzerVersion,
	})
	album, err := service.AggregateAlbum(context.Background(), folder)
	if err != nil || album.TrackCount != 4 || math.IsNaN(album.IntegratedLUFS) || album.TruePeakDB != -1 {
		t.Fatalf("album=%#v err=%v", album, err)
	}
	proposalID, err := service.StageTrackReplayGain(context.Background(), tracks[0].ID, -18)
	if err != nil || proposalID <= 0 {
		t.Fatalf("proposal=%d err=%v", proposalID, err)
	}
	proposal, _ := staging.GetByID(proposalID)
	if proposal.ChangeType != "audio_gain" {
		t.Fatalf("proposal=%#v", proposal)
	}
	albumProposals, err := service.StageAlbumReplayGain(context.Background(), folder, -18)
	if err != nil || albumProposals != 4 {
		t.Fatalf("album proposals=%d err=%v", albumProposals, err)
	}
}
