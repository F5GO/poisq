package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaInfoRoundTripFingerprintAndCascade(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := NewTrackRepository(database).UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMediaInfoRepository(database)
	rec := MediaInfoRecord{TrackID: "t", Container: "flac", AudioCodec: "flac", Bitrate: 900000, SampleRate: 48000, BitDepth: 24, Channels: 2, DurationMillis: 1234, HasEmbeddedCover: true, HasEmbeddedLyrics: true, Fingerprint: FileFingerprint{Size: 44, MTimeNS: 55}, ProbeVersion: 1, Status: "ready"}
	if err := repo.Put(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	current, err := repo.IsCurrent(context.Background(), "t", rec.Fingerprint, 1)
	if err != nil || !current {
		t.Fatalf("current=%v err=%v", current, err)
	}
	got, err := repo.Get(context.Background(), "t")
	if err != nil || got.AudioCodec != "flac" || !got.HasEmbeddedCover || got.DurationMillis != 1234 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if _, err := database.Exec(`DELETE FROM tracks WHERE id='t'`); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = database.QueryRow(`SELECT COUNT(*) FROM track_media_info`).Scan(&count)
	if count != 0 {
		t.Fatalf("orphan media info: %d", count)
	}
}

func TestMediaInfoFailureIsNotCurrentAndErrorIsBounded(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_ = NewTrackRepository(database).UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"})
	repo := NewMediaInfoRepository(database)
	fp := FileFingerprint{Size: 1, MTimeNS: 2}
	if err := repo.Put(context.Background(), MediaInfoRecord{TrackID: "t", Fingerprint: fp, ProbeVersion: 1, Status: "failed", LastError: strings.Repeat("x", MaxMediaInfoErrorBytes+100)}); err != nil {
		t.Fatal(err)
	}
	current, _ := repo.IsCurrent(context.Background(), "t", fp, 1)
	if current {
		t.Fatal("failed probe considered current")
	}
	got, _ := repo.Get(context.Background(), "t")
	if len(got.LastError) != MaxMediaInfoErrorBytes {
		t.Fatalf("error length=%d", len(got.LastError))
	}
}

func TestMediaInfoMarkStatusPreservesLastValidFacts(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_ = NewTrackRepository(database).UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"})
	repo := NewMediaInfoRepository(database)
	if err := repo.Put(context.Background(), MediaInfoRecord{
		TrackID: "t", AudioCodec: "flac", SampleRate: 96000,
		Fingerprint: FileFingerprint{Size: 42, MTimeNS: 99}, ProbeVersion: 1, Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkStatus(context.Background(), "t", "failed", "temporary probe error"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.AudioCodec != "flac" || got.SampleRate != 96000 || got.Fingerprint.Size != 42 {
		t.Fatalf("last valid facts were not preserved: %#v", got)
	}
}
