package metadata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

func TestMapAudioTagsPreservesTypedIDsAndCredits(t *testing.T) {
	tags := map[string]string{
		"TITLE": "Song", "album": "Album", "ARTISTS": "A; B", "album_artist": "A & B", "composer": "C",
		"genre": "Rock; Alternative", "date": "2001-02", "originaldate": "1999-12-31", "track": "3/12", "disc": "2/3",
		"musicbrainz_trackid":        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"musicbrainz_releasetrackid": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"musicbrainz_releasegroupid": "cccccccc-cccc-cccc-cccc-cccccccccccc",
		"isrc":                       "US-RC1-76-07839", "barcode": "12345678",
	}
	canonical := make(map[string]string)
	mergeProbeTags(canonical, tags)
	doc, err := mapAudioTags("t", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleArtist) != "A; B" || musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleAlbumArtist) != "A & B" || musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleComposer) != "C" {
		t.Fatalf("credits=%#v", doc.Credits)
	}
	if doc.TrackNumber != 3 || doc.TrackTotal != 12 || doc.DiscNumber != 2 || doc.DiscTotal != 3 || doc.ReleaseDate.Precision != musicmeta.PrecisionMonth || doc.OriginalReleaseDate.Precision != musicmeta.PrecisionDay {
		t.Fatalf("numbers/dates=%#v", doc)
	}
	seen := map[musicmeta.ExternalIDType]string{}
	for _, id := range doc.ExternalIDs {
		seen[id.Type] = id.Value
	}
	if seen[musicmeta.IDMusicBrainzRecording] == "" || seen[musicmeta.IDMusicBrainzReleaseTrack] == "" || seen[musicmeta.IDMusicBrainzRecording] == seen[musicmeta.IDMusicBrainzReleaseTrack] || seen[musicmeta.IDMusicBrainzReleaseGroup] == "" {
		t.Fatalf("ids=%#v", doc.ExternalIDs)
	}
	if doc.ISRC != "USRC17607839" || doc.Barcode != "12345678" {
		t.Fatalf("isrc/barcode=%q/%q", doc.ISRC, doc.Barcode)
	}
}

func TestMapReplayGainAcceptsStandardFiniteRanges(t *testing.T) {
	values := map[string]string{
		"replaygain_track_gain": "-7.25 dB", "replaygain_track_peak": "0.987654",
		"replaygain_album_gain": "+1.50 DB", "replaygain_album_peak": "1.01",
	}
	gain := mapReplayGain(values)
	if gain.TrackGainDB == nil || *gain.TrackGainDB != -7.25 || gain.TrackPeak == nil || *gain.TrackPeak != .987654 || gain.AlbumGainDB == nil || *gain.AlbumGainDB != 1.5 {
		t.Fatalf("gain=%#v", gain)
	}
	invalid := mapReplayGain(map[string]string{"replaygain_track_gain": "NaN dB", "replaygain_track_peak": "1000"})
	if invalid.TrackGainDB != nil || invalid.TrackPeak != nil {
		t.Fatalf("invalid ReplayGain accepted: %#v", invalid)
	}
}

func TestFFProbeExecutesBoundedJSONProbe(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ffprobe")
	body := `#!/bin/sh
printf '%s' '{"format":{"format_name":"flac","duration":"1.25","bit_rate":"900000","tags":{"title":"Song","artist":"A","lyrics":"words"}},"streams":[{"codec_type":"audio","codec_name":"flac","sample_rate":"48000","channels":2,"bits_per_raw_sample":"24"},{"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}]}'
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := NewFFProbeAudioProber(script).Probe(context.Background(), "/ignored.flac", "t")
	if err != nil {
		t.Fatal(err)
	}
	if got.Document.Title != "Song" || got.AudioCodec != "flac" || got.SampleRate != 48000 || got.BitDepth != 24 || got.DurationMillis != 1250 || !got.HasEmbeddedCover || !got.HasEmbeddedLyrics {
		t.Fatalf("probe=%#v", got)
	}
}

func TestFFProbeRejectsMalformedMissingAndOversizedOutput(t *testing.T) {
	if _, err := NewFFProbeAudioProber(filepath.Join(t.TempDir(), "missing")).Probe(context.Background(), "/x", "t"); err == nil {
		t.Fatal("missing executable accepted")
	}
	for name, output := range map[string]string{"malformed": "{", "oversized": strings.Repeat("x", ffprobeOutputMax+1)} {
		script := filepath.Join(t.TempDir(), name)
		body := "#!/bin/sh\nprintf '%s' '" + output + "'\n"
		if err := os.WriteFile(script, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFFProbeAudioProber(script).Probe(context.Background(), "/x", "t"); err == nil {
			t.Fatalf("%s output accepted", name)
		}
	}
}

func TestFFProbeHonorsTimeoutAndCancellation(t *testing.T) {
	script := filepath.Join(t.TempDir(), "slow-ffprobe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prober := NewFFProbeAudioProber(script)
	prober.timeout = 20 * time.Millisecond
	started := time.Now()
	if _, err := prober.Probe(context.Background(), "/x", "t"); err == nil || time.Since(started) > time.Second {
		t.Fatalf("timeout was not enforced promptly: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFFProbeAudioProber(script).Probe(cancelled, "/x", "t"); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
}
