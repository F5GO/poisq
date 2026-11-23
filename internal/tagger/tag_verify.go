package tagger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	tagVerifyOutputMax = 1 << 20
	tagVerifyErrorMax  = 2 << 10
)

func (w *AtomicTagWriter) VerifyTags(ctx context.Context, audioFilePath string, expected CanonicalTagSet) error {
	safeTarget, err := w.guard.ValidateTarget(audioFilePath, false)
	if err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(verifyCtx, w.ffprobeExecutable,
		"-v", "error", "-show_entries", "format_tags", "-of", "json", safeTarget)
	stdout := &tagLimitedBuffer{limit: tagVerifyOutputMax}
	stderr := &tagLimitedBuffer{limit: tagVerifyErrorMax}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if verifyCtx.Err() != nil {
			return fmt.Errorf("tag verification timeout: %w", verifyCtx.Err())
		}
		return fmt.Errorf("tag verification failed: %s", strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return fmt.Errorf("tag verification output exceeds limit")
	}
	var payload struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return fmt.Errorf("decode tag verification: %w", err)
	}
	tags := make(map[string]string, len(payload.Format.Tags))
	for key, value := range payload.Format.Tags {
		tags[verifyTagKey(key)] = strings.TrimSpace(value)
	}
	checks := []struct {
		aliases []string
		value   string
	}{
		{[]string{"artist"}, expected.Artist},
		{[]string{"album"}, expected.Album},
		{[]string{"albumartist", "album_artist"}, expected.AlbumArtist},
		{[]string{"title"}, expected.Title},
		{[]string{"subtitle", "version"}, expected.Subtitle},
		{[]string{"musicbrainzalbumid"}, expected.ReleaseMBID},
		{[]string{"musicbrainzreleasegroupid"}, expected.ReleaseGroupMBID},
		{[]string{"musicbrainzartistid"}, expected.ArtistMBID},
		{[]string{"musicbrainztrackid"}, expected.RecordingMBID},
		{[]string{"musicbrainzreleasetrackid"}, expected.ReleaseTrackMBID},
		{[]string{"composer"}, strings.Join(expected.Composer, "; ")},
		{[]string{"lyricist", "writer"}, strings.Join(expected.Lyricist, "; ")},
		{[]string{"conductor"}, strings.Join(expected.Conductor, "; ")},
		{[]string{"performer"}, strings.Join(expected.Performer, "; ")},
		{[]string{"producer"}, strings.Join(expected.Producer, "; ")},
		{[]string{"remixer"}, strings.Join(expected.Remixer, "; ")},
	}
	if expected.BPM > 0 {
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"bpm", "tempo"}, strconv.Itoa(expected.BPM)})
	}
	if expected.Rating != nil {
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"rating"}, strconv.Itoa(*expected.Rating)})
	}
	expectedDate := strings.TrimSpace(expected.ReleaseDate)
	if expectedDate == "" && expected.Year > 1900 {
		expectedDate = strconv.Itoa(expected.Year)
	}
	if expectedDate != "" {
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"date", "year"}, expectedDate})
	}
	if expected.OriginalDate != "" {
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"originaldate", "original_date"}, expected.OriginalDate})
	}
	if expected.TrackNumber > 0 {
		trackValue := strconv.Itoa(expected.TrackNumber)
		if expected.TrackTotal > 0 {
			trackValue += "/" + strconv.Itoa(expected.TrackTotal)
		}
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"track", "tracknumber"}, trackValue})
	}
	if expected.DiscNumber > 0 {
		discValue := strconv.Itoa(expected.DiscNumber)
		if expected.DiscTotal > 0 {
			discValue += "/" + strconv.Itoa(expected.DiscTotal)
		}
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"disc", "discnumber"}, discValue})
	}
	checks = append(checks,
		struct {
			aliases []string
			value   string
		}{[]string{"genre"}, strings.Join(expected.Genres, "; ")},
		struct {
			aliases []string
			value   string
		}{[]string{"label", "publisher"}, expected.Label},
		struct {
			aliases []string
			value   string
		}{[]string{"catalognumber", "catalog_number"}, expected.CatalogNumber},
		struct {
			aliases []string
			value   string
		}{[]string{"barcode", "upc"}, expected.Barcode},
		struct {
			aliases []string
			value   string
		}{[]string{"isrc"}, expected.ISRC},
		struct {
			aliases []string
			value   string
		}{[]string{"grouping"}, expected.Grouping},
		struct {
			aliases []string
			value   string
		}{[]string{"comment"}, expected.Comment},
		struct {
			aliases []string
			value   string
		}{[]string{"copyright"}, expected.Copyright},
	)
	if expected.Compilation {
		checks = append(checks, struct {
			aliases []string
			value   string
		}{[]string{"compilation"}, "1"})
	}
	for key, value := range expected.ExtraTags {
		if safeExtraTagKey(strings.TrimSpace(key)) && strings.TrimSpace(value) != "" {
			checks = append(checks, struct {
				aliases []string
				value   string
			}{[]string{key}, value})
		}
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			continue
		}
		actual := ""
		for _, alias := range check.aliases {
			if value := tags[verifyTagKey(alias)]; value != "" {
				actual = value
				break
			}
		}
		if check.aliases[0] == "track" && !strings.Contains(check.value, "/") && strings.Contains(actual, "/") {
			actual = strings.SplitN(actual, "/", 2)[0]
		}
		if check.aliases[0] == "date" && len(actual) >= 4 {
			actual = actual[:4]
		}
		if actual != strings.TrimSpace(check.value) {
			return fmt.Errorf("tag verification mismatch for %s", check.aliases[0])
		}
	}
	return nil
}

func verifyTagKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(value)
}

type tagLimitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *tagLimitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = b.buffer.Write(value[:remaining])
		} else {
			_, _ = b.buffer.Write(value)
		}
	}
	if original > remaining {
		b.overflow = true
	}
	return original, nil
}

func (b *tagLimitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *tagLimitedBuffer) String() string { return b.buffer.String() }
