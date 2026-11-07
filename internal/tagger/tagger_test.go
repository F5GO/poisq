// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл tagger_test.go проверяет корректность извлечения MD5-сигнатуры аудиопотока
// из заголовка FLAC STREAMINFO и генерацию флагов метаданных по стандарту MusicBrainz Picard.
package tagger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractFLACAudioMD5(t *testing.T) {
	// Формируем минимальный валидный 42-байтный заголовок FLAC с известной 16-байтной сигнатурой MD5
	header := make([]byte, 42)
	copy(header[0:4], []byte("fLaC"))
	header[4] = 0x00 // STREAMINFO block type = 0
	header[7] = 34   // length = 34 bytes

	expectedMD5 := []byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
	}
	copy(header[26:42], expectedMD5)

	tmpFile := filepath.Join(t.TempDir(), "test.flac")
	if err := os.WriteFile(tmpFile, header, 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	md5Hex, err := ExtractFLACAudioMD5(tmpFile)
	if err != nil {
		t.Fatalf("ExtractFLACAudioMD5 error: %v", err)
	}
	if md5Hex != "0123456789abcdeffedcba9876543210" {
		t.Fatalf("got %q, want 0123456789abcdeffedcba9876543210", md5Hex)
	}
}

func TestBuildFFmpegMetadataArgsPicardStandard(t *testing.T) {
	tags := CanonicalTagSet{
		Artist:      "МакSим",
		Album:       "Трудный возраст",
		AlbumArtist: "МакSим",
		Title:       "Знаешь ли ты",
		Year:        2006,
		TrackNumber: 2,
		ReleaseMBID: "1234-release-mbid",
		ArtistMBID:  "5678-artist-mbid",
	}

	args := BuildFFmpegMetadataArgs("/music/MakSim/02.flac", tags)
	joined := strings.Join(args, " ")

	for _, expected := range []string{
		"artist=МакSим",
		"album=Трудный возраст",
		"ORIGINALDATE=2006",
		"MUSICBRAINZ_ALBUMID=1234-release-mbid",
		"MUSICBRAINZ_ARTISTID=5678-artist-mbid",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing expected Picard metadata flag %q in %q", expected, joined)
		}
	}
}

// Откат убирает то, что добавила правка: album_artist, которого не было, и MBID из правки.
func TestUndoClearsAddedTags(t *testing.T) {
	orig := CanonicalTagSet{Artist: "Кино", Title: "Группа крови"}
	proposed := CanonicalTagSet{Artist: "Кино", ReleaseMBID: "rel-1"}
	orig.Clear = undoClears(orig, proposed)
	joined := strings.Join(BuildFFmpegMetadataArgs("/music/Kino/01.flac", orig), " ")
	for _, want := range []string{"album_artist=", "MUSICBRAINZ_ALBUMID=", "artist=Кино"} {
		if !strings.Contains(joined, want) {
			t.Errorf("нет %q в %q", want, joined)
		}
	}
	if strings.Contains(joined, "MUSICBRAINZ_TRACKID") {
		t.Errorf("MBID, которого не было в правке, трогать нельзя: %q", joined)
	}
	if got := undoClears(CanonicalTagSet{AlbumArtist: "Кино"}, CanonicalTagSet{}); len(got) != 0 {
		t.Errorf("исходный album_artist сохраняется, а не удаляется: %v", got)
	}
}
