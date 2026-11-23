package musicmeta

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	mbidPattern    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	isrcPattern    = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)
	barcodePattern = regexp.MustCompile(`^[0-9]{8,14}$`)
)

var validRoles = map[CreditRole]bool{
	RoleArtist: true, RoleAlbumArtist: true, RoleComposer: true, RoleLyricist: true,
	RoleConductor: true, RolePerformer: true, RoleProducer: true, RoleRemixer: true,
}

var validIDTypes = map[ExternalIDType]bool{
	IDMusicBrainzArtist: true, IDMusicBrainzAlbumArtist: true, IDMusicBrainzRelease: true,
	IDMusicBrainzReleaseGroup: true, IDMusicBrainzRecording: true, IDMusicBrainzReleaseTrack: true,
	IDISRC: true, IDBarcode: true, IDAudioDBArtist: true, IDAudioDBAlbum: true, IDListenBrainzArtist: true,
}

var validFields = map[Field]bool{
	FieldTitle: true, FieldSubtitle: true, FieldAlbum: true, FieldSortTitle: true, FieldSortAlbum: true,
	FieldReleaseDate: true, FieldOriginalReleaseDate: true, FieldTrackNumber: true, FieldTrackTotal: true,
	FieldDiscNumber: true, FieldDiscTotal: true, FieldCompilation: true, FieldLabel: true,
	FieldCatalogNumber: true, FieldBarcode: true, FieldISRC: true, FieldGrouping: true,
	FieldComment: true, FieldCopyright: true, FieldCredits: true, FieldGenres: true, FieldExternalIDs: true,
	FieldBPM: true, FieldRating: true, FieldCustomTags: true,
}

var reservedCustomTagKeys = map[string]bool{
	"artist": true, "album": true, "album_artist": true, "title": true, "subtitle": true,
	"date": true, "originaldate": true, "track": true, "disc": true, "genre": true,
	"composer": true, "lyricist": true, "conductor": true, "performer": true, "producer": true, "remixer": true,
	"bpm": true, "rating": true, "label": true, "catalognumber": true, "barcode": true, "isrc": true,
	"grouping": true, "comment": true, "copyright": true, "lyrics": true, "unsyncedlyrics": true, "syncedlyrics": true,
	"replaygain_track_gain": true, "replaygain_track_peak": true, "replaygain_album_gain": true, "replaygain_album_peak": true,
	"musicbrainz_albumid": true, "musicbrainz_releasegroupid": true, "musicbrainz_artistid": true,
	"musicbrainz_trackid": true, "musicbrainz_releasetrackid": true,
}

var validSources = map[Source]bool{
	SourceLegacy: true, SourceAudioFile: true, SourceNavidrome: true, SourceNFO: true,
	SourceMusicBrainz: true, SourceManual: true, SourceDerived: true,
}

// ValidMusicBrainzID reports whether value is a canonical MusicBrainz UUID.
func ValidMusicBrainzID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && mbidPattern.MatchString(value)
}

func ParsePartialDate(value string) (PartialDate, error) {
	trimmed := strings.TrimSpace(value)
	if value != trimmed {
		return PartialDate{}, fmt.Errorf("invalid partial date %q", value)
	}
	value = trimmed
	if value == "" {
		return PartialDate{}, nil
	}
	var layout string
	var precision DatePrecision
	switch len(value) {
	case 4:
		layout, precision = "2006", PrecisionYear
	case 7:
		layout, precision = "2006-01", PrecisionMonth
	case 10:
		layout, precision = "2006-01-02", PrecisionDay
	default:
		return PartialDate{}, fmt.Errorf("invalid partial date %q", value)
	}
	parsed, err := time.Parse(layout, value)
	if err != nil || parsed.Year() < 1 || parsed.Year() > 9999 {
		return PartialDate{}, fmt.Errorf("invalid partial date %q", value)
	}
	return PartialDate{Value: value, Precision: precision}, nil
}

func (d PartialDate) Year() int {
	if len(d.Value) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(d.Value[:4])
	return year
}

func ValidateDocument(input Document) (Document, error) {
	doc := input.Clone()
	var err error
	if doc.TrackID, err = cleanText("track id", doc.TrackID, MaxIDBytes, false); err != nil || doc.TrackID == "" {
		if err == nil {
			err = fmt.Errorf("track id is empty")
		}
		return Document{}, err
	}
	for name, target := range map[string]*string{
		"title": &doc.Title, "subtitle": &doc.Subtitle, "album": &doc.Album,
		"sort title": &doc.SortTitle, "sort album": &doc.SortAlbum, "label": &doc.Label,
		"catalog number": &doc.CatalogNumber, "barcode": &doc.Barcode, "isrc": &doc.ISRC,
		"grouping": &doc.Grouping, "copyright": &doc.Copyright,
	} {
		if *target, err = cleanText(name, *target, MaxTextBytes, false); err != nil {
			return Document{}, err
		}
	}
	if doc.Comment, err = cleanText("comment", doc.Comment, MaxCommentBytes, true); err != nil {
		return Document{}, err
	}
	if doc.ReleaseDate, err = validatePartialDate(doc.ReleaseDate); err != nil {
		return Document{}, fmt.Errorf("release date: %w", err)
	}
	if doc.OriginalReleaseDate, err = validatePartialDate(doc.OriginalReleaseDate); err != nil {
		return Document{}, fmt.Errorf("original release date: %w", err)
	}
	for name, value := range map[string]int{
		"track number": doc.TrackNumber, "track total": doc.TrackTotal,
		"disc number": doc.DiscNumber, "disc total": doc.DiscTotal, "bpm": doc.BPM,
	} {
		maximum := 99999
		if name == "bpm" {
			maximum = 999
		}
		if value < 0 || value > maximum {
			return Document{}, fmt.Errorf("%s out of range", name)
		}
	}
	if doc.Rating != nil && (*doc.Rating < 0 || *doc.Rating > 100) {
		return Document{}, fmt.Errorf("rating out of range")
	}
	if doc.TrackTotal > 0 && doc.TrackNumber > doc.TrackTotal {
		return Document{}, fmt.Errorf("track number exceeds total")
	}
	if doc.DiscTotal > 0 && doc.DiscNumber > doc.DiscTotal {
		return Document{}, fmt.Errorf("disc number exceeds total")
	}
	if len(doc.Credits) > MaxCredits || len(doc.Genres) > MaxGenres || len(doc.ExternalIDs) > MaxExternalIDs || len(doc.CustomTags) > MaxCustomTags {
		return Document{}, fmt.Errorf("metadata collection exceeds limit")
	}
	doc.Credits, err = validateCredits(doc.Credits)
	if err != nil {
		return Document{}, err
	}
	doc.Genres, err = validateGenres(doc.Genres)
	if err != nil {
		return Document{}, err
	}
	doc.ExternalIDs, err = validateExternalIDs(doc.ExternalIDs)
	if err != nil {
		return Document{}, err
	}
	doc.CustomTags, err = validateCustomTags(doc.CustomTags)
	if err != nil {
		return Document{}, err
	}
	if doc.ISRC != "" {
		doc.ISRC = strings.ToUpper(doc.ISRC)
		if !isrcPattern.MatchString(doc.ISRC) {
			return Document{}, fmt.Errorf("invalid ISRC")
		}
	}
	if doc.Barcode != "" && !barcodePattern.MatchString(doc.Barcode) {
		return Document{}, fmt.Errorf("invalid barcode")
	}
	states := make(map[Field]FieldState, len(doc.FieldStates))
	for field, state := range doc.FieldStates {
		if !validFields[field] {
			return Document{}, fmt.Errorf("unknown metadata field %q", field)
		}
		if !validSources[state.Source] {
			return Document{}, fmt.Errorf("unknown metadata source %q", state.Source)
		}
		if math.IsNaN(state.Confidence) || math.IsInf(state.Confidence, 0) || state.Confidence < 0 || state.Confidence > 1 {
			return Document{}, fmt.Errorf("invalid confidence for %s", field)
		}
		state.Provider, err = cleanText("provider", state.Provider, MaxProviderBytes, false)
		if err != nil {
			return Document{}, err
		}
		states[field] = state
	}
	doc.FieldStates = states
	return doc, nil
}

func validateCustomTags(values []CustomTag) ([]CustomTag, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > MaxCustomTags {
		return nil, fmt.Errorf("too many custom tags")
	}
	out := make([]CustomTag, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, err := cleanText("custom tag key", value.Key, 128, false)
		if err != nil || key == "" {
			if err == nil {
				err = fmt.Errorf("custom tag key is empty")
			}
			return nil, err
		}
		for _, r := range key {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' || r == '-' {
				continue
			}
			return nil, fmt.Errorf("custom tag key %q contains unsupported characters", key)
		}
		canonical := strings.ToLower(strings.ReplaceAll(key, " ", "_"))
		if reservedCustomTagKeys[canonical] || strings.HasPrefix(canonical, "musicbrainz_") || strings.HasPrefix(canonical, "replaygain_") {
			return nil, fmt.Errorf("custom tag key %q is reserved", key)
		}
		if _, ok := seen[canonical]; ok {
			return nil, fmt.Errorf("duplicate custom tag key %q", key)
		}
		cleanValue, err := cleanText("custom tag value", value.Value, MaxTextBytes, true)
		if err != nil {
			return nil, err
		}
		seen[canonical] = struct{}{}
		out = append(out, CustomTag{Key: key, Value: cleanValue})
	}
	return out, nil
}

func ValidField(field Field) bool { return validFields[field] }

func validatePartialDate(value PartialDate) (PartialDate, error) {
	parsed, err := ParsePartialDate(value.Value)
	if err != nil {
		return PartialDate{}, err
	}
	if value.Value != "" && value.Precision != "" && value.Precision != parsed.Precision {
		return PartialDate{}, fmt.Errorf("precision does not match value")
	}
	return parsed, nil
}

func validateCredits(values []Credit) ([]Credit, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]Credit, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, credit := range values {
		if !validRoles[credit.Role] {
			return nil, fmt.Errorf("unknown credit role %q", credit.Role)
		}
		var err error
		credit.Name, err = cleanText("credit name", credit.Name, MaxTextBytes, false)
		if err != nil || credit.Name == "" {
			if err == nil {
				err = fmt.Errorf("credit name is empty")
			}
			return nil, err
		}
		credit.SortName, err = cleanText("credit sort name", credit.SortName, MaxTextBytes, false)
		if err != nil {
			return nil, err
		}
		credit.JoinPhrase, err = cleanText("credit join phrase", credit.JoinPhrase, MaxTextBytes, false)
		if err != nil {
			return nil, err
		}
		credit.ExternalIDs, err = validateExternalIDs(credit.ExternalIDs)
		if err != nil {
			return nil, err
		}
		key := string(credit.Role) + "\x00" + strings.ToLower(credit.Name) + "\x00" + credit.JoinPhrase + "\x00" + externalIDKey(credit.ExternalIDs)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, credit)
	}
	return out, nil
}

func validateGenres(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clean, err := cleanText("genre", value, MaxTextBytes, false)
		if err != nil {
			return nil, err
		}
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out, nil
}

func validateExternalIDs(values ExternalIDs) (ExternalIDs, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > MaxExternalIDs {
		return nil, fmt.Errorf("too many external ids")
	}
	out := make(ExternalIDs, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, externalID := range values {
		if !validIDTypes[externalID.Type] {
			return nil, fmt.Errorf("unknown external id type %q", externalID.Type)
		}
		value, err := cleanText("external id", externalID.Value, MaxIDBytes, false)
		if err != nil || value == "" {
			if err == nil {
				err = fmt.Errorf("external id is empty")
			}
			return nil, err
		}
		externalID.Value = canonicalExternalID(externalID.Type, value)
		if err := validateExternalID(externalID); err != nil {
			return nil, err
		}
		key := string(externalID.Type) + "\x00" + externalID.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, externalID)
	}
	return out, nil
}

func canonicalExternalID(idType ExternalIDType, value string) string {
	switch idType {
	case IDMusicBrainzArtist, IDMusicBrainzAlbumArtist, IDMusicBrainzRelease, IDMusicBrainzReleaseGroup, IDMusicBrainzRecording, IDMusicBrainzReleaseTrack, IDListenBrainzArtist:
		return strings.ToLower(value)
	case IDISRC:
		return strings.ToUpper(strings.ReplaceAll(value, "-", ""))
	default:
		return value
	}
}

func validateExternalID(externalID ExternalID) error {
	switch externalID.Type {
	case IDMusicBrainzArtist, IDMusicBrainzAlbumArtist, IDMusicBrainzRelease, IDMusicBrainzReleaseGroup, IDMusicBrainzRecording, IDMusicBrainzReleaseTrack, IDListenBrainzArtist:
		if !mbidPattern.MatchString(externalID.Value) {
			return fmt.Errorf("invalid UUID for %s", externalID.Type)
		}
	case IDISRC:
		if !isrcPattern.MatchString(externalID.Value) {
			return fmt.Errorf("invalid ISRC")
		}
	case IDBarcode:
		if !barcodePattern.MatchString(externalID.Value) {
			return fmt.Errorf("invalid barcode")
		}
	case IDAudioDBArtist, IDAudioDBAlbum:
		for _, r := range externalID.Value {
			if r < '0' || r > '9' {
				return fmt.Errorf("invalid numeric id for %s", externalID.Type)
			}
		}
	}
	return nil
}

func externalIDKey(values ExternalIDs) string {
	var b strings.Builder
	for _, value := range values {
		b.WriteString(string(value.Type))
		b.WriteByte('=')
		b.WriteString(value.Value)
		b.WriteByte(';')
	}
	return b.String()
}

func cleanText(name, value string, limit int, multiline bool) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%s is not valid UTF-8", name)
	}
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return "", fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	for _, r := range value {
		if r == 0 || (unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t'))) {
			return "", fmt.Errorf("%s contains control characters", name)
		}
	}
	return value, nil
}
