// Package musicmeta defines the normalized music metadata domain shared by
// local import, remote providers, staging, and writeback.
package musicmeta

import "time"

const (
	MaxIDBytes       = 512
	MaxTextBytes     = 2 << 10
	MaxCommentBytes  = 16 << 10
	MaxCredits       = 256
	MaxGenres        = 128
	MaxExternalIDs   = 64
	MaxCustomTags    = 128
	MaxProviderBytes = 512
)

type DatePrecision string

const (
	PrecisionNone  DatePrecision = ""
	PrecisionYear  DatePrecision = "year"
	PrecisionMonth DatePrecision = "month"
	PrecisionDay   DatePrecision = "day"
)

// PartialDate preserves the precision supplied by the metadata source.
type PartialDate struct {
	Value     string        `json:"value,omitempty"`
	Precision DatePrecision `json:"precision,omitempty"`
}

type CreditRole string

const (
	RoleArtist      CreditRole = "artist"
	RoleAlbumArtist CreditRole = "album_artist"
	RoleComposer    CreditRole = "composer"
	RoleLyricist    CreditRole = "lyricist"
	RoleConductor   CreditRole = "conductor"
	RolePerformer   CreditRole = "performer"
	RoleProducer    CreditRole = "producer"
	RoleRemixer     CreditRole = "remixer"
)

type ExternalIDType string

const (
	IDMusicBrainzArtist       ExternalIDType = "musicbrainz_artist"
	IDMusicBrainzAlbumArtist  ExternalIDType = "musicbrainz_album_artist"
	IDMusicBrainzRelease      ExternalIDType = "musicbrainz_release"
	IDMusicBrainzReleaseGroup ExternalIDType = "musicbrainz_release_group"
	IDMusicBrainzRecording    ExternalIDType = "musicbrainz_recording"
	IDMusicBrainzReleaseTrack ExternalIDType = "musicbrainz_release_track"
	IDISRC                    ExternalIDType = "isrc"
	IDBarcode                 ExternalIDType = "barcode"
	IDAudioDBArtist           ExternalIDType = "audiodb_artist"
	IDAudioDBAlbum            ExternalIDType = "audiodb_album"
	IDListenBrainzArtist      ExternalIDType = "listenbrainz_artist"
)

type ExternalID struct {
	Type  ExternalIDType `json:"type"`
	Value string         `json:"value"`
}

type ExternalIDs []ExternalID

type Credit struct {
	Role        CreditRole  `json:"role"`
	Name        string      `json:"name"`
	SortName    string      `json:"sortName,omitempty"`
	JoinPhrase  string      `json:"joinPhrase,omitempty"`
	ExternalIDs ExternalIDs `json:"externalIds,omitempty"`
}

type CustomTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Field string

const (
	FieldTitle               Field = "title"
	FieldSubtitle            Field = "subtitle"
	FieldAlbum               Field = "album"
	FieldSortTitle           Field = "sort_title"
	FieldSortAlbum           Field = "sort_album"
	FieldReleaseDate         Field = "release_date"
	FieldOriginalReleaseDate Field = "original_release_date"
	FieldTrackNumber         Field = "track_number"
	FieldTrackTotal          Field = "track_total"
	FieldDiscNumber          Field = "disc_number"
	FieldDiscTotal           Field = "disc_total"
	FieldCompilation         Field = "compilation"
	FieldLabel               Field = "label"
	FieldCatalogNumber       Field = "catalog_number"
	FieldBarcode             Field = "barcode"
	FieldISRC                Field = "isrc"
	FieldGrouping            Field = "grouping"
	FieldComment             Field = "comment"
	FieldCopyright           Field = "copyright"
	FieldCredits             Field = "credits"
	FieldGenres              Field = "genres"
	FieldExternalIDs         Field = "external_ids"
	FieldBPM                 Field = "bpm"
	FieldRating              Field = "rating"
	FieldCustomTags          Field = "custom_tags"
)

type Source string

const (
	SourceLegacy      Source = "legacy"
	SourceAudioFile   Source = "audio_file"
	SourceNavidrome   Source = "navidrome"
	SourceNFO         Source = "nfo"
	SourceMusicBrainz Source = "musicbrainz"
	SourceManual      Source = "manual"
	SourceDerived     Source = "derived"
)

type FieldState struct {
	Source     Source    `json:"source"`
	Provider   string    `json:"provider,omitempty"`
	Confidence float64   `json:"confidence"`
	Locked     bool      `json:"locked"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Document is the effective normalized metadata of one track.
type Document struct {
	TrackID             string               `json:"trackId"`
	Title               string               `json:"title,omitempty"`
	Subtitle            string               `json:"subtitle,omitempty"`
	Album               string               `json:"album,omitempty"`
	SortTitle           string               `json:"sortTitle,omitempty"`
	SortAlbum           string               `json:"sortAlbum,omitempty"`
	ReleaseDate         PartialDate          `json:"releaseDate,omitempty"`
	OriginalReleaseDate PartialDate          `json:"originalReleaseDate,omitempty"`
	TrackNumber         int                  `json:"trackNumber,omitempty"`
	TrackTotal          int                  `json:"trackTotal,omitempty"`
	DiscNumber          int                  `json:"discNumber,omitempty"`
	DiscTotal           int                  `json:"discTotal,omitempty"`
	Compilation         bool                 `json:"compilation,omitempty"`
	Label               string               `json:"label,omitempty"`
	CatalogNumber       string               `json:"catalogNumber,omitempty"`
	Barcode             string               `json:"barcode,omitempty"`
	ISRC                string               `json:"isrc,omitempty"`
	Grouping            string               `json:"grouping,omitempty"`
	Comment             string               `json:"comment,omitempty"`
	Copyright           string               `json:"copyright,omitempty"`
	BPM                 int                  `json:"bpm,omitempty"`
	Rating              *int                 `json:"rating,omitempty"`
	Credits             []Credit             `json:"credits,omitempty"`
	Genres              []string             `json:"genres,omitempty"`
	ExternalIDs         ExternalIDs          `json:"externalIds,omitempty"`
	CustomTags          []CustomTag          `json:"customTags,omitempty"`
	FieldStates         map[Field]FieldState `json:"fieldStates,omitempty"`
}

// Clone returns a deep copy safe for independent mutation.
func (d Document) Clone() Document {
	out := d
	out.Credits = make([]Credit, len(d.Credits))
	for i, credit := range d.Credits {
		out.Credits[i] = credit
		out.Credits[i].ExternalIDs = append(ExternalIDs(nil), credit.ExternalIDs...)
	}
	out.Genres = append([]string(nil), d.Genres...)
	out.ExternalIDs = append(ExternalIDs(nil), d.ExternalIDs...)
	out.CustomTags = append([]CustomTag(nil), d.CustomTags...)
	if d.Rating != nil {
		value := *d.Rating
		out.Rating = &value
	}
	out.FieldStates = make(map[Field]FieldState, len(d.FieldStates))
	for field, state := range d.FieldStates {
		out.FieldStates[field] = state
	}
	return out
}
