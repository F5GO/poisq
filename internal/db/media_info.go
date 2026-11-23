package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const MaxMediaInfoErrorBytes = 2 << 10

type FileFingerprint struct {
	Size    int64 `json:"size"`
	MTimeNS int64 `json:"mtimeNs"`
}

type MediaInfoRecord struct {
	TrackID           string          `json:"trackId"`
	Container         string          `json:"container"`
	AudioCodec        string          `json:"audioCodec"`
	Bitrate           int64           `json:"bitrate"`
	SampleRate        int             `json:"sampleRate"`
	BitDepth          int             `json:"bitDepth"`
	Channels          int             `json:"channels"`
	DurationMillis    int64           `json:"durationMillis"`
	HasEmbeddedCover  bool            `json:"hasEmbeddedCover"`
	HasEmbeddedLyrics bool            `json:"hasEmbeddedLyrics"`
	Fingerprint       FileFingerprint `json:"fingerprint"`
	ProbeVersion      int             `json:"probeVersion"`
	Status            string          `json:"status"`
	LastError         string          `json:"lastError,omitempty"`
	ProbedAt          time.Time       `json:"probedAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type MediaInfoRepository struct{ db *sql.DB }

func NewMediaInfoRepository(database *sql.DB) *MediaInfoRepository {
	return &MediaInfoRepository{db: database}
}

func (r *MediaInfoRepository) IsCurrent(ctx context.Context, trackID string, fp FileFingerprint, probeVersion int) (bool, error) {
	var size, mtime int64
	var version int
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT file_size,file_mtime_ns,probe_version,status FROM track_media_info WHERE track_id=?`, trackID).
		Scan(&size, &mtime, &version, &status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read media fingerprint: %w", err)
	}
	return status == "ready" && size == fp.Size && mtime == fp.MTimeNS && version == probeVersion, nil
}

func (r *MediaInfoRepository) Put(ctx context.Context, rec MediaInfoRecord) error {
	if rec.TrackID == "" || len(rec.TrackID) > 512 {
		return fmt.Errorf("invalid media-info track id")
	}
	if rec.Status != "ready" && rec.Status != "failed" && rec.Status != "missing" && rec.Status != "unsupported" {
		return fmt.Errorf("invalid media-info status")
	}
	if rec.Bitrate < 0 || rec.SampleRate < 0 || rec.BitDepth < 0 || rec.Channels < 0 || rec.DurationMillis < 0 || rec.Fingerprint.Size < 0 || rec.ProbeVersion < 0 {
		return fmt.Errorf("negative media-info value")
	}
	rec.LastError = strings.TrimSpace(rec.LastError)
	if len(rec.LastError) > MaxMediaInfoErrorBytes {
		rec.LastError = rec.LastError[:MaxMediaInfoErrorBytes]
	}
	now := time.Now().UTC()
	if rec.ProbedAt.IsZero() {
		rec.ProbedAt = now
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO track_media_info(
			track_id,container,audio_codec,bitrate,sample_rate,bit_depth,channels,duration_ms,
			has_embedded_cover,has_embedded_lyrics,file_size,file_mtime_ns,probe_version,status,
			last_error,probed_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			container=excluded.container,audio_codec=excluded.audio_codec,bitrate=excluded.bitrate,
			sample_rate=excluded.sample_rate,bit_depth=excluded.bit_depth,channels=excluded.channels,
			duration_ms=excluded.duration_ms,has_embedded_cover=excluded.has_embedded_cover,
			has_embedded_lyrics=excluded.has_embedded_lyrics,file_size=excluded.file_size,
			file_mtime_ns=excluded.file_mtime_ns,probe_version=excluded.probe_version,status=excluded.status,
			last_error=excluded.last_error,probed_at=excluded.probed_at,updated_at=excluded.updated_at`,
		rec.TrackID, rec.Container, rec.AudioCodec, rec.Bitrate, rec.SampleRate, rec.BitDepth, rec.Channels,
		rec.DurationMillis, boolInt(rec.HasEmbeddedCover), boolInt(rec.HasEmbeddedLyrics), rec.Fingerprint.Size,
		rec.Fingerprint.MTimeNS, rec.ProbeVersion, rec.Status, rec.LastError, rec.ProbedAt.Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("write media info: %w", err)
	}
	return nil
}

// MarkStatus records a probe failure without discarding the last valid media facts.
func (r *MediaInfoRepository) MarkStatus(ctx context.Context, trackID, status, lastError string) error {
	if trackID == "" || len(trackID) > 512 {
		return fmt.Errorf("invalid media-info track id")
	}
	if status != "failed" && status != "missing" && status != "unsupported" {
		return fmt.Errorf("invalid media-info failure status")
	}
	lastError = strings.TrimSpace(lastError)
	if len(lastError) > MaxMediaInfoErrorBytes {
		lastError = lastError[:MaxMediaInfoErrorBytes]
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO track_media_info(track_id,status,last_error,probed_at,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			status=excluded.status,last_error=excluded.last_error,
			probed_at=excluded.probed_at,updated_at=excluded.updated_at`,
		trackID, status, lastError, now, now)
	if err != nil {
		return fmt.Errorf("mark media-info status: %w", err)
	}
	return nil
}

func (r *MediaInfoRepository) Get(ctx context.Context, trackID string) (MediaInfoRecord, error) {
	var rec MediaInfoRecord
	var cover, lyrics int
	var probedAt, updatedAt int64
	err := r.db.QueryRowContext(ctx, `
		SELECT track_id,container,audio_codec,bitrate,sample_rate,bit_depth,channels,duration_ms,
		       has_embedded_cover,has_embedded_lyrics,file_size,file_mtime_ns,probe_version,status,
		       last_error,probed_at,updated_at
		FROM track_media_info WHERE track_id=?`, trackID).Scan(
		&rec.TrackID, &rec.Container, &rec.AudioCodec, &rec.Bitrate, &rec.SampleRate, &rec.BitDepth,
		&rec.Channels, &rec.DurationMillis, &cover, &lyrics, &rec.Fingerprint.Size, &rec.Fingerprint.MTimeNS,
		&rec.ProbeVersion, &rec.Status, &rec.LastError, &probedAt, &updatedAt)
	if err != nil {
		return MediaInfoRecord{}, err
	}
	rec.HasEmbeddedCover = cover == 1
	rec.HasEmbeddedLyrics = lyrics == 1
	rec.ProbedAt = time.Unix(probedAt, 0).UTC()
	rec.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rec, nil
}
