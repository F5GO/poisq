package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

const (
	audioAnalyzerVersion = 1
	audioAnalysisTimeout = 10 * time.Minute
	audioAnalysisLogMax  = 1 << 20
)

type LoudnessResult struct {
	IntegratedLUFS  float64
	LoudnessRangeLU float64
	TruePeakDB      float64
}

type LoudnessAnalyzer interface {
	Analyze(context.Context, string) (LoudnessResult, error)
}

type FFmpegLoudnessAnalyzer struct {
	executable string
	timeout    time.Duration
}

func NewFFmpegLoudnessAnalyzer(executable string) *FFmpegLoudnessAnalyzer {
	if strings.TrimSpace(executable) == "" {
		executable = "ffmpeg"
	}
	return &FFmpegLoudnessAnalyzer{executable: executable, timeout: audioAnalysisTimeout}
}

func (a *FFmpegLoudnessAnalyzer) Analyze(ctx context.Context, path string) (LoudnessResult, error) {
	timeout := a.timeout
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = audioAnalysisTimeout
	}
	analysisCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(analysisCtx, a.executable,
		"-hide_banner", "-nostats", "-nostdin", "-i", path,
		"-filter_complex", "ebur128=peak=true", "-f", "null", "-")
	stderr := &limitedBuffer{limit: audioAnalysisLogMax}
	command.Stdout = io.Discard
	command.Stderr = stderr
	err := command.Run()
	if analysisCtx.Err() != nil {
		return LoudnessResult{}, fmt.Errorf("loudness analysis timeout: %w", analysisCtx.Err())
	}
	if stderr.overflow {
		return LoudnessResult{}, fmt.Errorf("loudness analysis output exceeds limit")
	}
	if err != nil {
		return LoudnessResult{}, fmt.Errorf("loudness analysis failed")
	}
	return parseEBUR128Summary(stderr.String())
}

var (
	ebuIntegratedPattern = regexp.MustCompile(`(?m)^\s*I:\s*(-?[0-9]+(?:\.[0-9]+)?)\s+LUFS\s*$`)
	ebuLRAPattern        = regexp.MustCompile(`(?m)^\s*LRA:\s*([0-9]+(?:\.[0-9]+)?)\s+LU\s*$`)
	ebuPeakPattern       = regexp.MustCompile(`(?m)^\s*Peak:\s*(-?[0-9]+(?:\.[0-9]+)?)\s+dBFS\s*$`)
)

func parseEBUR128Summary(output string) (LoudnessResult, error) {
	integrated, okI := lastRegexFloat(ebuIntegratedPattern, output)
	lra, okLRA := lastRegexFloat(ebuLRAPattern, output)
	peak, okPeak := lastRegexFloat(ebuPeakPattern, output)
	if !okI || !okLRA || !okPeak || integrated < -100 || integrated > 10 || lra < 0 || lra > 100 || peak < -100 || peak > 10 {
		return LoudnessResult{}, fmt.Errorf("invalid EBU R128 summary")
	}
	return LoudnessResult{IntegratedLUFS: integrated, LoudnessRangeLU: lra, TruePeakDB: peak}, nil
}

func lastRegexFloat(pattern *regexp.Regexp, value string) (float64, bool) {
	matches := pattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

type AudioAnalysisService struct {
	guard    *security.PathGuard
	tracks   *db.TrackRepository
	repo     *db.AudioAnalysisRepository
	staging  *db.StagingRepository
	analyzer LoudnessAnalyzer
	workers  int
}

type AnalysisBatchStats struct {
	Total    int
	Analyzed int
	Skipped  int
	Failed   int
}

func NewAudioAnalysisService(guard *security.PathGuard, tracks *db.TrackRepository, repo *db.AudioAnalysisRepository, staging *db.StagingRepository, analyzer LoudnessAnalyzer, workers int) *AudioAnalysisService {
	if workers < 1 {
		workers = 1
	}
	if workers > 2 {
		workers = 2
	}
	return &AudioAnalysisService{guard: guard, tracks: tracks, repo: repo, staging: staging, analyzer: analyzer, workers: workers}
}

func (s *AudioAnalysisService) AnalyzeTrack(ctx context.Context, track db.TrackRecord) (bool, error) {
	path, err := s.guard.ValidateTarget(track.FilePath, false)
	if err != nil {
		_ = s.repo.MarkFailure(ctx, track.ID, "failed", err)
		return false, err
	}
	if !security.IsAudioFile(path) {
		err := fmt.Errorf("unsupported audio format")
		_ = s.repo.MarkFailure(ctx, track.ID, "unsupported", err)
		return false, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		status := "failed"
		if os.IsNotExist(err) {
			status = "missing"
		}
		_ = s.repo.MarkFailure(ctx, track.ID, status, err)
		return false, err
	}
	fingerprint := db.FileFingerprint{Size: stat.Size(), MTimeNS: stat.ModTime().UnixNano()}
	current, err := s.repo.IsCurrent(ctx, track.ID, fingerprint, audioAnalyzerVersion)
	if err != nil {
		return false, err
	}
	if current {
		return true, nil
	}
	result, err := s.analyzer.Analyze(ctx, path)
	if err != nil {
		_ = s.repo.MarkFailure(ctx, track.ID, "failed", err)
		return false, err
	}
	if err := s.repo.PutCalculated(ctx, db.AudioAnalysisRecord{
		TrackID: track.ID, IntegratedLUFS: float64Pointer(result.IntegratedLUFS),
		LoudnessRangeLU: float64Pointer(result.LoudnessRangeLU), TruePeakDB: float64Pointer(result.TruePeakDB),
		Fingerprint: fingerprint, AnalyzerVersion: audioAnalyzerVersion,
	}); err != nil {
		return false, err
	}
	return false, nil
}

func (s *AudioAnalysisService) AnalyzeTracks(ctx context.Context, tracks []db.TrackRecord) AnalysisBatchStats {
	stats := AnalysisBatchStats{Total: len(tracks)}
	if len(tracks) == 0 {
		return stats
	}
	type outcome struct {
		skipped bool
		err     error
	}
	jobs := make(chan db.TrackRecord)
	results := make(chan outcome)
	var group sync.WaitGroup
	for index := 0; index < s.workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for track := range jobs {
				skipped, err := s.AnalyzeTrack(ctx, track)
				results <- outcome{skipped: skipped, err: err}
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
	go func() { group.Wait(); close(results) }()
	processed := 0
	for result := range results {
		processed++
		if result.err != nil {
			stats.Failed++
		} else if result.skipped {
			stats.Skipped++
		} else {
			stats.Analyzed++
		}
	}
	stats.Failed += len(tracks) - processed
	return stats
}

func (s *AudioAnalysisService) AggregateAlbum(ctx context.Context, folderPath string) (db.AlbumAudioAnalysis, error) {
	folder, err := s.guard.ValidateTarget(folderPath, false)
	if err != nil {
		return db.AlbumAudioAnalysis{}, err
	}
	tracks, err := s.tracks.ListByFolder(folder)
	if err != nil {
		return db.AlbumAudioAnalysis{}, err
	}
	var weightedEnergy, totalDuration float64
	truePeak := math.Inf(-1)
	count := 0
	for _, track := range tracks {
		record, err := s.repo.Get(ctx, track.ID)
		if err != nil || record.Status != "ready" || record.IntegratedLUFS == nil || record.TruePeakDB == nil {
			continue
		}
		duration := float64(track.Duration)
		if duration <= 0 {
			duration = 1
		}
		weightedEnergy += math.Pow(10, *record.IntegratedLUFS/10) * duration
		totalDuration += duration
		if *record.TruePeakDB > truePeak {
			truePeak = *record.TruePeakDB
		}
		count++
	}
	if count == 0 || totalDuration == 0 {
		return db.AlbumAudioAnalysis{}, fmt.Errorf("album has no ready track analysis")
	}
	value := db.AlbumAudioAnalysis{
		FolderPath: folder, IntegratedLUFS: 10 * math.Log10(weightedEnergy/totalDuration),
		TruePeakDB: truePeak, TrackCount: count, Source: "ffmpeg_ebur128",
	}
	if err := s.repo.PutAlbum(ctx, value); err != nil {
		return db.AlbumAudioAnalysis{}, err
	}
	return value, nil
}

func (s *AudioAnalysisService) StageTrackReplayGain(ctx context.Context, trackID string, targetLUFS float64) (int64, error) {
	if targetLUFS < -30 || targetLUFS > -5 || math.IsNaN(targetLUFS) || math.IsInf(targetLUFS, 0) {
		return 0, fmt.Errorf("invalid ReplayGain target")
	}
	track, err := s.tracks.GetByID(trackID)
	if err != nil {
		return 0, err
	}
	record, err := s.repo.Get(ctx, trackID)
	if err != nil || record.Status != "ready" || record.IntegratedLUFS == nil || record.TruePeakDB == nil {
		return 0, fmt.Errorf("track loudness analysis is not ready")
	}
	extra := map[string]string{
		"REPLAYGAIN_TRACK_GAIN": fmt.Sprintf("%.2f dB", targetLUFS-*record.IntegratedLUFS),
		"REPLAYGAIN_TRACK_PEAK": fmt.Sprintf("%.6f", math.Pow(10, *record.TruePeakDB/20)),
	}
	return s.stageReplayGain(track, record, extra)
}

func (s *AudioAnalysisService) StageAlbumReplayGain(ctx context.Context, folderPath string, targetLUFS float64) (int, error) {
	if targetLUFS < -30 || targetLUFS > -5 || math.IsNaN(targetLUFS) || math.IsInf(targetLUFS, 0) {
		return 0, fmt.Errorf("invalid ReplayGain target")
	}
	album, err := s.AggregateAlbum(ctx, folderPath)
	if err != nil {
		return 0, err
	}
	tracks, err := s.tracks.ListByFolder(album.FolderPath)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, track := range tracks {
		record, err := s.repo.Get(ctx, track.ID)
		if err != nil || record.Status != "ready" || record.IntegratedLUFS == nil || record.TruePeakDB == nil {
			continue
		}
		extra := map[string]string{
			"REPLAYGAIN_TRACK_GAIN": fmt.Sprintf("%.2f dB", targetLUFS-*record.IntegratedLUFS),
			"REPLAYGAIN_TRACK_PEAK": fmt.Sprintf("%.6f", math.Pow(10, *record.TruePeakDB/20)),
			"REPLAYGAIN_ALBUM_GAIN": fmt.Sprintf("%.2f dB", targetLUFS-album.IntegratedLUFS),
			"REPLAYGAIN_ALBUM_PEAK": fmt.Sprintf("%.6f", math.Pow(10, album.TruePeakDB/20)),
		}
		if _, err := s.stageReplayGain(&track, record, extra); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func (s *AudioAnalysisService) stageReplayGain(track *db.TrackRecord, record db.AudioAnalysisRecord, extra map[string]string) (int64, error) {
	currentJSON, _ := json.Marshal(map[string]any{"integratedLufs": *record.IntegratedLUFS, "truePeakDb": *record.TruePeakDB})
	proposedJSON, _ := json.Marshal(map[string]any{
		"artist": track.Artist, "album": track.Album, "albumArtist": track.AlbumArtist,
		"title": track.Title, "year": track.Year, "trackNumber": track.TrackNumber,
		"extraTags": extra, "reason": "Calculated EBU R128 ReplayGain",
	})
	return s.staging.CreateProposal(db.StagingChangeRecord{
		Scope: "track", TargetID: track.ID, FolderPath: track.FolderPath, ChangeType: "audio_gain",
		Confidence: 100, CurrentJSON: string(currentJSON), ProposedJSON: string(proposedJSON),
	})
}

func float64Pointer(value float64) *float64 { return &value }
