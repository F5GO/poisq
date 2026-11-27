package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

const (
	ffprobeTimeout    = 10 * time.Second
	ffprobeWaitDelay  = 100 * time.Millisecond
	ffprobeOutputMax  = 4 << 20
	ffprobeStderrMax  = 2 << 10
	localProbeVersion = 1
)

type LocalAudioMetadata struct {
	Document          musicmeta.Document
	Container         string
	AudioCodec        string
	Bitrate           int64
	SampleRate        int
	BitDepth          int
	Channels          int
	DurationMillis    int64
	HasEmbeddedCover  bool
	HasEmbeddedLyrics bool
	ReplayGain        ReplayGainMetadata
}

type ReplayGainMetadata struct {
	TrackGainDB *float64
	TrackPeak   *float64
	AlbumGainDB *float64
	AlbumPeak   *float64
}

type AudioProber interface {
	Probe(context.Context, string, string) (LocalAudioMetadata, error)
}

type FFProbeAudioProber struct {
	executable string
	timeout    time.Duration
}

func NewFFProbeAudioProber(executable string) *FFProbeAudioProber {
	if strings.TrimSpace(executable) == "" {
		executable = "ffprobe"
	}
	return &FFProbeAudioProber{executable: executable, timeout: ffprobeTimeout}
}

type probeJSON struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType        string            `json:"codec_type"`
		CodecName        string            `json:"codec_name"`
		SampleRate       string            `json:"sample_rate"`
		Channels         int               `json:"channels"`
		BitsPerSample    int               `json:"bits_per_sample"`
		BitsPerRawSample string            `json:"bits_per_raw_sample"`
		Tags             map[string]string `json:"tags"`
		Disposition      struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

func (p *FFProbeAudioProber) Probe(ctx context.Context, path, trackID string) (LocalAudioMetadata, error) {
	timeout := p.timeout
	if timeout <= 0 || timeout > time.Minute {
		timeout = ffprobeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, p.executable,
		"-v", "error", "-show_format", "-show_streams", "-of", "json", path)
	cmd.WaitDelay = ffprobeWaitDelay
	stdout := &limitedBuffer{limit: ffprobeOutputMax}
	stderr := &limitedBuffer{limit: ffprobeStderrMax}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if probeCtx.Err() != nil {
		return LocalAudioMetadata{}, fmt.Errorf("ffprobe timeout: %w", probeCtx.Err())
	}
	if stdout.overflow {
		return LocalAudioMetadata{}, fmt.Errorf("ffprobe output exceeds %d bytes", ffprobeOutputMax)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return LocalAudioMetadata{}, fmt.Errorf("ffprobe failed: %s", message)
	}
	var decoded probeJSON
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&decoded); err != nil {
		return LocalAudioMetadata{}, fmt.Errorf("decode ffprobe JSON: %w", err)
	}
	return mapAudioProbe(trackID, decoded)
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	if original > remaining {
		b.overflow = true
	}
	return original, nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }

func mapAudioProbe(trackID string, decoded probeJSON) (LocalAudioMetadata, error) {
	result := LocalAudioMetadata{Container: firstCSVValue(decoded.Format.FormatName)}
	tags := make(map[string]string)
	mergeProbeTags(tags, decoded.Format.Tags)
	for _, stream := range decoded.Streams {
		if stream.Disposition.AttachedPic == 1 {
			result.HasEmbeddedCover = true
		}
		if strings.EqualFold(stream.CodecType, "subtitle") && strings.Contains(strings.ToLower(stream.CodecName), "lyric") {
			result.HasEmbeddedLyrics = true
		}
		if stream.CodecType != "audio" || result.AudioCodec != "" {
			continue
		}
		result.AudioCodec = stream.CodecName
		result.SampleRate, _ = strconv.Atoi(stream.SampleRate)
		result.Channels = stream.Channels
		result.BitDepth = stream.BitsPerSample
		if raw, err := strconv.Atoi(stream.BitsPerRawSample); err == nil && raw > 0 {
			result.BitDepth = raw
		}
		mergeProbeTags(tags, stream.Tags)
	}
	result.Bitrate, _ = strconv.ParseInt(decoded.Format.BitRate, 10, 64)
	if duration, err := strconv.ParseFloat(decoded.Format.Duration, 64); err == nil && duration >= 0 && !math.IsInf(duration, 0) && !math.IsNaN(duration) {
		result.DurationMillis = int64(math.Round(duration * 1000))
	}
	for key := range tags {
		if key == "lyrics" || key == "unsyncedlyrics" || key == "syncedlyrics" || key == "unsynced_lyrics" {
			result.HasEmbeddedLyrics = strings.TrimSpace(tags[key]) != ""
		}
	}
	doc, err := mapAudioTags(trackID, tags)
	if err != nil {
		return LocalAudioMetadata{}, err
	}
	result.Document = doc
	result.ReplayGain = mapReplayGain(tags)
	return result, nil
}

func mapReplayGain(tags map[string]string) ReplayGainMetadata {
	return ReplayGainMetadata{
		TrackGainDB: parseReplayGainValue(firstTag(tags, "replaygain_track_gain", "rg_track_gain"), -60, 60, true),
		TrackPeak:   parseReplayGainValue(firstTag(tags, "replaygain_track_peak", "rg_track_peak"), 0, 10, false),
		AlbumGainDB: parseReplayGainValue(firstTag(tags, "replaygain_album_gain", "rg_album_gain"), -60, 60, true),
		AlbumPeak:   parseReplayGainValue(firstTag(tags, "replaygain_album_peak", "rg_album_peak"), 0, 10, false),
	}
}

func parseReplayGainValue(value string, minimum, maximum float64, stripDB bool) *float64 {
	value = strings.TrimSpace(value)
	if stripDB {
		lower := strings.ToLower(value)
		if strings.HasSuffix(lower, "db") {
			value = strings.TrimSpace(value[:len(value)-2])
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		return nil
	}
	return &parsed
}

func mergeProbeTags(target, source map[string]string) {
	for key, value := range source {
		if clean := strings.TrimSpace(value); clean != "" {
			target[canonicalTagKey(key)] = clean
		}
	}
}

func canonicalTagKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(value)
	return value
}

func mapAudioTags(trackID string, tags map[string]string) (musicmeta.Document, error) {
	doc := musicmeta.Document{TrackID: trackID, FieldStates: make(map[musicmeta.Field]musicmeta.FieldState)}
	doc.Title = firstTag(tags, "title")
	doc.Subtitle = firstTag(tags, "subtitle", "version")
	doc.Album = firstTag(tags, "album")
	doc.SortTitle = firstTag(tags, "titlesort", "title_sort", "sort_title")
	doc.SortAlbum = firstTag(tags, "albumsort", "album_sort", "sort_album")
	doc.Label = firstTag(tags, "label", "publisher", "organization")
	doc.CatalogNumber = firstTag(tags, "catalognumber", "catalog_number")
	doc.Grouping = firstTag(tags, "grouping", "contentgroup", "content_group")
	doc.Comment = firstTag(tags, "comment", "description")
	doc.Copyright = firstTag(tags, "copyright")
	doc.BPM = boundedInt(firstTag(tags, "bpm", "tempo"), 0, 999)
	if raw := firstTag(tags, "rating", "popularimeter"); raw != "" {
		value := boundedInt(raw, 0, 100)
		doc.Rating = &value
	}
	compilationTag := firstTag(tags, "compilation", "itunescompilation")
	doc.Compilation = parseBoolTag(compilationTag)

	addCredits := func(role musicmeta.CreditRole, aliases ...string) {
		for _, value := range splitUnambiguous(firstTag(tags, aliases...)) {
			doc.Credits = append(doc.Credits, musicmeta.Credit{Role: role, Name: value})
		}
	}
	addCredits(musicmeta.RoleArtist, "artists", "artist")
	addCredits(musicmeta.RoleAlbumArtist, "albumartists", "album_artists", "albumartist", "album_artist")
	addCredits(musicmeta.RoleComposer, "composer", "composers")
	addCredits(musicmeta.RoleLyricist, "lyricist", "writer")
	addCredits(musicmeta.RoleConductor, "conductor")
	addCredits(musicmeta.RolePerformer, "performer")
	addCredits(musicmeta.RoleProducer, "producer")
	addCredits(musicmeta.RoleRemixer, "remixer", "mixer")
	doc.Genres = splitUnambiguous(firstTag(tags, "genre", "genres"))

	doc.TrackNumber, doc.TrackTotal = parseIndexTotal(firstTag(tags, "track", "tracknumber", "track_number"), firstTag(tags, "tracktotal", "totaltracks", "track_total"))
	doc.DiscNumber, doc.DiscTotal = parseIndexTotal(firstTag(tags, "disc", "discnumber", "disc_number"), firstTag(tags, "disctotal", "totaldiscs", "disc_total"))
	if value := firstTag(tags, "date", "releasedate", "release_date", "year"); value != "" {
		if parsed, err := musicmeta.ParsePartialDate(normalizeProbeDate(value)); err == nil {
			doc.ReleaseDate = parsed
		}
	}
	if value := firstTag(tags, "originaldate", "original_date", "originalyear", "original_year"); value != "" {
		if parsed, err := musicmeta.ParsePartialDate(normalizeProbeDate(value)); err == nil {
			doc.OriginalReleaseDate = parsed
		}
	}

	idAliases := []struct {
		idType  musicmeta.ExternalIDType
		aliases []string
	}{
		{musicmeta.IDMusicBrainzArtist, []string{"musicbrainz_artistid", "musicbrainz_artist_id"}},
		{musicmeta.IDMusicBrainzAlbumArtist, []string{"musicbrainz_albumartistid", "musicbrainz_album_artist_id"}},
		{musicmeta.IDMusicBrainzRelease, []string{"musicbrainz_albumid", "musicbrainz_releaseid", "musicbrainz_album_id"}},
		{musicmeta.IDMusicBrainzReleaseGroup, []string{"musicbrainz_releasegroupid", "musicbrainz_release_group_id"}},
		{musicmeta.IDMusicBrainzRecording, []string{"musicbrainz_trackid", "musicbrainz_recordingid", "musicbrainz_track_id"}},
		{musicmeta.IDMusicBrainzReleaseTrack, []string{"musicbrainz_releasetrackid", "musicbrainz_release_track_id"}},
		{musicmeta.IDISRC, []string{"isrc"}},
		{musicmeta.IDBarcode, []string{"barcode", "upc"}},
		{musicmeta.IDAudioDBArtist, []string{"audiodb_artistid", "audiodb_artist_id"}},
		{musicmeta.IDAudioDBAlbum, []string{"audiodb_albumid", "audiodb_album_id"}},
	}
	for _, mapping := range idAliases {
		for _, value := range splitUnambiguous(firstTag(tags, mapping.aliases...)) {
			temp, err := musicmeta.ValidateDocument(musicmeta.Document{TrackID: trackID, ExternalIDs: musicmeta.ExternalIDs{{Type: mapping.idType, Value: value}}})
			if err == nil {
				doc.ExternalIDs = append(doc.ExternalIDs, temp.ExternalIDs[0])
				if mapping.idType == musicmeta.IDISRC && doc.ISRC == "" {
					doc.ISRC = temp.ExternalIDs[0].Value
				}
				if mapping.idType == musicmeta.IDBarcode && doc.Barcode == "" {
					doc.Barcode = temp.ExternalIDs[0].Value
				}
			}
		}
	}
	customKeys := make([]string, 0, len(tags))
	for key := range tags {
		if !managedProbeTag(key) {
			customKeys = append(customKeys, key)
		}
	}
	sort.Strings(customKeys)
	for _, key := range customKeys {
		if len(doc.CustomTags) >= musicmeta.MaxCustomTags {
			break
		}
		doc.CustomTags = append(doc.CustomTags, musicmeta.CustomTag{Key: key, Value: tags[key]})
	}
	markImportedFields(&doc)
	if compilationTag != "" {
		doc.FieldStates[musicmeta.FieldCompilation] = musicmeta.FieldState{
			Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1, UpdatedAt: time.Now().UTC(),
		}
	}
	clean, err := musicmeta.ValidateDocument(doc)
	if err != nil {
		return musicmeta.Document{}, fmt.Errorf("validate probed tags: %w", err)
	}
	return clean, nil
}

func markImportedFields(doc *musicmeta.Document) {
	now := time.Now().UTC()
	state := musicmeta.FieldState{Source: musicmeta.SourceAudioFile, Provider: "ffprobe", Confidence: 1, UpdatedAt: now}
	values := map[musicmeta.Field]bool{
		musicmeta.FieldTitle: doc.Title != "", musicmeta.FieldSubtitle: doc.Subtitle != "", musicmeta.FieldAlbum: doc.Album != "",
		musicmeta.FieldSortTitle: doc.SortTitle != "", musicmeta.FieldSortAlbum: doc.SortAlbum != "", musicmeta.FieldReleaseDate: doc.ReleaseDate.Value != "",
		musicmeta.FieldOriginalReleaseDate: doc.OriginalReleaseDate.Value != "", musicmeta.FieldTrackNumber: doc.TrackNumber > 0,
		musicmeta.FieldTrackTotal: doc.TrackTotal > 0, musicmeta.FieldDiscNumber: doc.DiscNumber > 0, musicmeta.FieldDiscTotal: doc.DiscTotal > 0,
		musicmeta.FieldCompilation: doc.Compilation, musicmeta.FieldLabel: doc.Label != "", musicmeta.FieldCatalogNumber: doc.CatalogNumber != "",
		musicmeta.FieldBarcode: doc.Barcode != "", musicmeta.FieldISRC: doc.ISRC != "", musicmeta.FieldGrouping: doc.Grouping != "",
		musicmeta.FieldComment: doc.Comment != "", musicmeta.FieldCopyright: doc.Copyright != "", musicmeta.FieldCredits: len(doc.Credits) > 0,
		musicmeta.FieldGenres: len(doc.Genres) > 0, musicmeta.FieldExternalIDs: len(doc.ExternalIDs) > 0,
		musicmeta.FieldBPM: doc.BPM > 0, musicmeta.FieldRating: doc.Rating != nil, musicmeta.FieldCustomTags: len(doc.CustomTags) > 0,
	}
	for field, present := range values {
		if present {
			doc.FieldStates[field] = state
		}
	}
}

func boundedInt(value string, minimum, maximum int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		return 0
	}
	return parsed
}

func managedProbeTag(key string) bool {
	key = canonicalTagKey(key)
	managed := map[string]bool{
		"title": true, "subtitle": true, "version": true, "album": true, "artist": true, "artists": true,
		"albumartist": true, "albumartists": true, "album_artist": true, "album_artists": true,
		"titlesort": true, "title_sort": true, "sort_title": true, "albumsort": true, "album_sort": true, "sort_album": true,
		"label": true, "publisher": true, "organization": true, "catalognumber": true, "catalog_number": true,
		"grouping": true, "contentgroup": true, "content_group": true, "comment": true, "description": true, "copyright": true,
		"compilation": true, "itunescompilation": true, "composer": true, "composers": true, "lyricist": true, "writer": true,
		"conductor": true, "performer": true, "producer": true, "remixer": true, "mixer": true, "genre": true, "genres": true,
		"track": true, "tracknumber": true, "track_number": true, "tracktotal": true, "totaltracks": true, "track_total": true,
		"disc": true, "discnumber": true, "disc_number": true, "disctotal": true, "totaldiscs": true, "disc_total": true,
		"date": true, "releasedate": true, "release_date": true, "year": true, "originaldate": true, "original_date": true,
		"originalyear": true, "original_year": true, "isrc": true, "barcode": true, "upc": true, "bpm": true, "tempo": true,
		"rating": true, "popularimeter": true, "lyrics": true, "unsyncedlyrics": true, "syncedlyrics": true, "unsynced_lyrics": true,
		"encoder": true, "encoded_by": true,
	}
	return managed[key] || strings.HasPrefix(key, "musicbrainz_") || strings.HasPrefix(key, "replaygain_") || strings.HasPrefix(key, "rg_") || strings.HasPrefix(key, "audiodb_")
}

func firstTag(tags map[string]string, aliases ...string) string {
	for _, alias := range aliases {
		if value := strings.TrimSpace(tags[canonicalTagKey(alias)]); value != "" {
			return value
		}
	}
	return ""
}

func splitUnambiguous(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	value = strings.NewReplacer("\x00", ";", "\x1f", ";").Replace(value)
	parts := strings.Split(value, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if clean := strings.TrimSpace(part); clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

func parseIndexTotal(value, explicitTotal string) (int, int) {
	parts := strings.SplitN(strings.TrimSpace(value), "/", 2)
	index := positiveInt(firstSlice(parts))
	total := positiveInt(explicitTotal)
	if len(parts) == 2 && total == 0 {
		total = positiveInt(parts[1])
	}
	return index, total
}

func firstSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func positiveInt(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func normalizeProbeDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 10 {
		return value[:10]
	}
	if len(value) >= 7 {
		return value[:7]
	}
	if len(value) >= 4 {
		return value[:4]
	}
	return value
}

func parseBoolTag(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func firstCSVValue(value string) string {
	if index := strings.IndexByte(value, ','); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}
