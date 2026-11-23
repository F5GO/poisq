package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

type AudioAnalysisRecord struct {
	TrackID               string          `json:"trackId"`
	ReplayGainTrackGainDB *float64        `json:"replaygainTrackGainDb,omitempty"`
	ReplayGainTrackPeak   *float64        `json:"replaygainTrackPeak,omitempty"`
	ReplayGainAlbumGainDB *float64        `json:"replaygainAlbumGainDb,omitempty"`
	ReplayGainAlbumPeak   *float64        `json:"replaygainAlbumPeak,omitempty"`
	IntegratedLUFS        *float64        `json:"integratedLufs,omitempty"`
	LoudnessRangeLU       *float64        `json:"loudnessRangeLu,omitempty"`
	TruePeakDB            *float64        `json:"truePeakDb,omitempty"`
	Fingerprint           FileFingerprint `json:"fingerprint"`
	AnalyzerVersion       int             `json:"analyzerVersion"`
	Source                string          `json:"source"`
	Status                string          `json:"status"`
	LastError             string          `json:"lastError,omitempty"`
	AnalyzedAt            time.Time       `json:"analyzedAt"`
	UpdatedAt             time.Time       `json:"updatedAt"`
}

type AlbumAudioAnalysis struct {
	FolderPath     string    `json:"folderPath"`
	IntegratedLUFS float64   `json:"integratedLufs"`
	TruePeakDB     float64   `json:"truePeakDb"`
	TrackCount     int       `json:"trackCount"`
	Source         string    `json:"source"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type AudioAnalysisRepository struct{ db *sql.DB }

func NewAudioAnalysisRepository(database *sql.DB) *AudioAnalysisRepository {
	return &AudioAnalysisRepository{db: database}
}

func (r *AudioAnalysisRepository) PutImportedReplayGain(ctx context.Context, trackID string, trackGainDB, trackPeak, albumGainDB, albumPeak *float64) error {
	if trackGainDB == nil && trackPeak == nil && albumGainDB == nil && albumPeak == nil {
		return nil
	}
	for _, value := range []*float64{trackGainDB, trackPeak, albumGainDB, albumPeak} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Errorf("invalid ReplayGain value")
		}
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO track_audio_analysis(
			track_id,replaygain_track_gain_db,replaygain_track_peak,replaygain_album_gain_db,replaygain_album_peak,
			source,status,updated_at
		) VALUES(?,?,?,?,?,'audio_file','pending',?)
		ON CONFLICT(track_id) DO UPDATE SET
			replaygain_track_gain_db=COALESCE(excluded.replaygain_track_gain_db,track_audio_analysis.replaygain_track_gain_db),
			replaygain_track_peak=COALESCE(excluded.replaygain_track_peak,track_audio_analysis.replaygain_track_peak),
			replaygain_album_gain_db=COALESCE(excluded.replaygain_album_gain_db,track_audio_analysis.replaygain_album_gain_db),
			replaygain_album_peak=COALESCE(excluded.replaygain_album_peak,track_audio_analysis.replaygain_album_peak),
			source=CASE WHEN track_audio_analysis.source='' THEN 'audio_file' ELSE track_audio_analysis.source END,
			updated_at=excluded.updated_at`,
		trackID, nullableFloat(trackGainDB), nullableFloat(trackPeak), nullableFloat(albumGainDB), nullableFloat(albumPeak), now)
	if err != nil {
		return fmt.Errorf("persist imported ReplayGain: %w", err)
	}
	return nil
}

func (r *AudioAnalysisRepository) IsCurrent(ctx context.Context, trackID string, fingerprint FileFingerprint, analyzerVersion int) (bool, error) {
	var size, mtime int64
	var version int
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT file_size,file_mtime_ns,analyzer_version,status FROM track_audio_analysis WHERE track_id=?`, trackID).
		Scan(&size, &mtime, &version, &status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "ready" && size == fingerprint.Size && mtime == fingerprint.MTimeNS && version == analyzerVersion, nil
}

func (r *AudioAnalysisRepository) PutCalculated(ctx context.Context, record AudioAnalysisRecord) error {
	if record.IntegratedLUFS == nil || record.LoudnessRangeLU == nil || record.TruePeakDB == nil {
		return fmt.Errorf("calculated loudness is incomplete")
	}
	for _, value := range []*float64{record.IntegratedLUFS, record.LoudnessRangeLU, record.TruePeakDB} {
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return fmt.Errorf("calculated loudness is not finite")
		}
	}
	if record.Fingerprint.Size < 0 || record.AnalyzerVersion < 1 {
		return fmt.Errorf("invalid analysis fingerprint or version")
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO track_audio_analysis(
			track_id,integrated_lufs,loudness_range_lu,true_peak_db,file_size,file_mtime_ns,
			analyzer_version,source,status,last_error,analyzed_at,updated_at
		) VALUES(?,?,?,?,?,?,?,'ffmpeg_ebur128','ready','',?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			integrated_lufs=excluded.integrated_lufs,loudness_range_lu=excluded.loudness_range_lu,
			true_peak_db=excluded.true_peak_db,file_size=excluded.file_size,file_mtime_ns=excluded.file_mtime_ns,
			analyzer_version=excluded.analyzer_version,source=excluded.source,status='ready',last_error='',
			analyzed_at=excluded.analyzed_at,updated_at=excluded.updated_at`,
		record.TrackID, *record.IntegratedLUFS, *record.LoudnessRangeLU, *record.TruePeakDB,
		record.Fingerprint.Size, record.Fingerprint.MTimeNS, record.AnalyzerVersion, now.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("persist calculated loudness: %w", err)
	}
	return nil
}

func (r *AudioAnalysisRepository) MarkFailure(ctx context.Context, trackID, status string, cause error) error {
	if status != "failed" && status != "missing" && status != "unsupported" {
		return fmt.Errorf("invalid analysis status")
	}
	message := ""
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
		if len(message) > MaxMediaInfoErrorBytes {
			message = message[:MaxMediaInfoErrorBytes]
		}
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO track_audio_analysis(track_id,status,last_error,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET status=excluded.status,last_error=excluded.last_error,updated_at=excluded.updated_at`,
		trackID, status, message, now)
	return err
}

func (r *AudioAnalysisRepository) Get(ctx context.Context, trackID string) (AudioAnalysisRecord, error) {
	var record AudioAnalysisRecord
	var trackGain, trackPeak, albumGain, albumPeak, lufs, lra, truePeak sql.NullFloat64
	var analyzedAt, updatedAt int64
	err := r.db.QueryRowContext(ctx, `
		SELECT track_id,replaygain_track_gain_db,replaygain_track_peak,replaygain_album_gain_db,replaygain_album_peak,
		       integrated_lufs,loudness_range_lu,true_peak_db,file_size,file_mtime_ns,analyzer_version,
		       source,status,last_error,analyzed_at,updated_at
		FROM track_audio_analysis WHERE track_id=?`, trackID).Scan(
		&record.TrackID, &trackGain, &trackPeak, &albumGain, &albumPeak, &lufs, &lra, &truePeak,
		&record.Fingerprint.Size, &record.Fingerprint.MTimeNS, &record.AnalyzerVersion,
		&record.Source, &record.Status, &record.LastError, &analyzedAt, &updatedAt)
	if err != nil {
		return AudioAnalysisRecord{}, err
	}
	record.ReplayGainTrackGainDB = floatPointer(trackGain)
	record.ReplayGainTrackPeak = floatPointer(trackPeak)
	record.ReplayGainAlbumGainDB = floatPointer(albumGain)
	record.ReplayGainAlbumPeak = floatPointer(albumPeak)
	record.IntegratedLUFS = floatPointer(lufs)
	record.LoudnessRangeLU = floatPointer(lra)
	record.TruePeakDB = floatPointer(truePeak)
	record.AnalyzedAt = time.Unix(analyzedAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return record, nil
}

func (r *AudioAnalysisRepository) PutAlbum(ctx context.Context, value AlbumAudioAnalysis) error {
	if strings.TrimSpace(value.FolderPath) == "" || value.TrackCount < 1 || math.IsNaN(value.IntegratedLUFS) || math.IsInf(value.IntegratedLUFS, 0) || math.IsNaN(value.TruePeakDB) || math.IsInf(value.TruePeakDB, 0) {
		return fmt.Errorf("invalid album analysis")
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO album_audio_analysis(folder_path,integrated_lufs,true_peak_db,track_count,source,updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(folder_path) DO UPDATE SET integrated_lufs=excluded.integrated_lufs,
			true_peak_db=excluded.true_peak_db,track_count=excluded.track_count,source=excluded.source,updated_at=excluded.updated_at`,
		value.FolderPath, value.IntegratedLUFS, value.TruePeakDB, value.TrackCount, value.Source, now)
	return err
}

func (r *AudioAnalysisRepository) GetAlbum(ctx context.Context, folderPath string) (AlbumAudioAnalysis, error) {
	var value AlbumAudioAnalysis
	var updatedAt int64
	err := r.db.QueryRowContext(ctx, `SELECT folder_path,integrated_lufs,true_peak_db,track_count,source,updated_at FROM album_audio_analysis WHERE folder_path=?`, folderPath).
		Scan(&value.FolderPath, &value.IntegratedLUFS, &value.TruePeakDB, &value.TrackCount, &value.Source, &updatedAt)
	value.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return value, err
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func floatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
