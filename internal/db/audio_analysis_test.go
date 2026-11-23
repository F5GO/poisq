package db

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestAudioAnalysisRepositoryPreservesImportedAndCalculatedValues(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_ = NewTrackRepository(database).UpsertTrack(TrackRecord{ID: "t", FilePath: "/music/t.flac"})
	repo := NewAudioAnalysisRepository(database)
	gain, peak := -7.2, 0.91
	if err := repo.PutImportedReplayGain(context.Background(), "t", &gain, &peak, nil, nil); err != nil {
		t.Fatal(err)
	}
	lufs, lra, truePeak := -14.2, 5.1, -1.0
	fingerprint := FileFingerprint{Size: 10, MTimeNS: 20}
	if err := repo.PutCalculated(context.Background(), AudioAnalysisRecord{
		TrackID: "t", IntegratedLUFS: &lufs, LoudnessRangeLU: &lra, TruePeakDB: &truePeak,
		Fingerprint: fingerprint, AnalyzerVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background(), "t")
	if err != nil || got.ReplayGainTrackGainDB == nil || *got.ReplayGainTrackGainDB != gain || got.IntegratedLUFS == nil || *got.IntegratedLUFS != lufs || got.Status != "ready" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	current, err := repo.IsCurrent(context.Background(), "t", fingerprint, 1)
	if err != nil || !current {
		t.Fatalf("current=%v err=%v", current, err)
	}
	album := AlbumAudioAnalysis{FolderPath: "/music/album", IntegratedLUFS: -13.5, TruePeakDB: -.5, TrackCount: 2, Source: "test"}
	if err := repo.PutAlbum(context.Background(), album); err != nil {
		t.Fatal(err)
	}
	gotAlbum, err := repo.GetAlbum(context.Background(), album.FolderPath)
	if err != nil || math.Abs(gotAlbum.IntegratedLUFS-album.IntegratedLUFS) > 0.0001 || gotAlbum.TrackCount != 2 {
		t.Fatalf("album=%#v err=%v", gotAlbum, err)
	}
}
