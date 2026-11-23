package metadata

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/musicmeta"
	"github.com/F5GO/poisq/internal/security"
)

type ImportResult struct {
	Imported bool
	Skipped  bool
}

type ImportStats struct {
	Total    int
	Imported int
	Skipped  int
	Failed   int
}

// LocalImporter reconciles embedded file tags into normalized metadata.
type LocalImporter struct {
	guard     *security.PathGuard
	metadata  *db.MetadataRepository
	mediaInfo *db.MediaInfoRepository
	prober    AudioProber
	workers   int
	policy    func(context.Context) (musicmeta.MergePolicy, error)
	analysis  *db.AudioAnalysisRepository
}

func (i *LocalImporter) SetAnalysisRepository(repository *db.AudioAnalysisRepository) {
	if i != nil {
		i.analysis = repository
	}
}

func (i *LocalImporter) SetPolicyProvider(provider func(context.Context) (musicmeta.MergePolicy, error)) {
	if i != nil {
		i.policy = provider
	}
}

func NewLocalImporter(guard *security.PathGuard, metadataRepo *db.MetadataRepository, mediaInfoRepo *db.MediaInfoRepository, prober AudioProber, workers int) *LocalImporter {
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}
	return &LocalImporter{guard: guard, metadata: metadataRepo, mediaInfo: mediaInfoRepo, prober: prober, workers: workers}
}

func (i *LocalImporter) ImportTrack(ctx context.Context, track db.TrackRecord) (ImportResult, error) {
	if i == nil || i.guard == nil || i.metadata == nil || i.mediaInfo == nil || i.prober == nil {
		return ImportResult{}, fmt.Errorf("local importer is not configured")
	}
	path, err := i.guard.ValidateTarget(track.FilePath, false)
	if err != nil {
		i.markFailure(ctx, track.ID, "failed", err)
		return ImportResult{}, fmt.Errorf("validate audio path: %w", err)
	}
	if !security.IsAudioFile(path) {
		err = fmt.Errorf("unsupported audio extension")
		i.markFailure(ctx, track.ID, "unsupported", err)
		return ImportResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		status := "failed"
		if os.IsNotExist(err) {
			status = "missing"
		}
		i.markFailure(ctx, track.ID, status, err)
		return ImportResult{}, fmt.Errorf("stat audio file: %w", err)
	}
	if !info.Mode().IsRegular() {
		err = fmt.Errorf("audio path is not a regular file")
		i.markFailure(ctx, track.ID, "unsupported", err)
		return ImportResult{}, err
	}
	fingerprint := db.FileFingerprint{Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}
	current, err := i.mediaInfo.IsCurrent(ctx, track.ID, fingerprint, localProbeVersion)
	if err != nil {
		return ImportResult{}, err
	}
	if current {
		return ImportResult{Skipped: true}, nil
	}

	base, err := i.metadata.GetTrackDocument(ctx, track.ID)
	if err != nil {
		i.markFailure(ctx, track.ID, "failed", err)
		return ImportResult{}, fmt.Errorf("load current metadata: %w", err)
	}
	probed, err := i.prober.Probe(ctx, path, track.ID)
	if err != nil {
		i.markFailure(ctx, track.ID, "failed", err)
		return ImportResult{}, err
	}
	policy := musicmeta.DefaultMergePolicy()
	if i.policy != nil {
		policy, err = i.policy(ctx)
		if err != nil {
			i.markFailure(ctx, track.ID, "failed", err)
			return ImportResult{}, fmt.Errorf("load metadata merge policy: %w", err)
		}
	}
	merged, err := musicmeta.Merge(base, []musicmeta.Document{probed.Document}, policy)
	if err != nil {
		i.markFailure(ctx, track.ID, "failed", err)
		return ImportResult{}, fmt.Errorf("merge imported metadata: %w", err)
	}
	if err := i.metadata.PutTrackDocument(ctx, merged); err != nil {
		i.markFailure(ctx, track.ID, "failed", err)
		return ImportResult{}, fmt.Errorf("persist imported metadata: %w", err)
	}
	if i.analysis != nil {
		gain := probed.ReplayGain
		if err := i.analysis.PutImportedReplayGain(ctx, track.ID, gain.TrackGainDB, gain.TrackPeak, gain.AlbumGainDB, gain.AlbumPeak); err != nil {
			i.markFailure(ctx, track.ID, "failed", err)
			return ImportResult{}, fmt.Errorf("persist imported ReplayGain: %w", err)
		}
	}
	if err := i.mediaInfo.Put(ctx, db.MediaInfoRecord{
		TrackID: track.ID, Container: probed.Container, AudioCodec: probed.AudioCodec,
		Bitrate: probed.Bitrate, SampleRate: probed.SampleRate, BitDepth: probed.BitDepth,
		Channels: probed.Channels, DurationMillis: probed.DurationMillis,
		HasEmbeddedCover: probed.HasEmbeddedCover, HasEmbeddedLyrics: probed.HasEmbeddedLyrics,
		Fingerprint: fingerprint, ProbeVersion: localProbeVersion, Status: "ready",
	}); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Imported: true}, nil
}

func (i *LocalImporter) markFailure(ctx context.Context, trackID, status string, cause error) {
	if i != nil && i.mediaInfo != nil && trackID != "" {
		_ = i.mediaInfo.MarkStatus(ctx, trackID, status, cause.Error())
	}
}

func (i *LocalImporter) ImportTracks(ctx context.Context, tracks []db.TrackRecord) ImportStats {
	stats := ImportStats{Total: len(tracks)}
	if len(tracks) == 0 {
		return stats
	}
	type outcome struct {
		result ImportResult
		err    error
	}
	jobs := make(chan db.TrackRecord)
	results := make(chan outcome)
	var workers sync.WaitGroup
	for n := 0; n < i.workers; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for track := range jobs {
				result, err := i.ImportTrack(ctx, track)
				results <- outcome{result: result, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, track := range tracks {
			select {
			case jobs <- track:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	processed := 0
	for outcome := range results {
		processed++
		switch {
		case outcome.err != nil:
			stats.Failed++
		case outcome.result.Imported:
			stats.Imported++
		case outcome.result.Skipped:
			stats.Skipped++
		}
	}
	stats.Failed += len(tracks) - processed
	return stats
}
