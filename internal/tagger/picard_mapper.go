// Package tagger реализует бережное чтение, хирургическую перезапись и откат (Undo)
// аудио-тегов по стандарту MusicBrainz Picard без перекодирования аудиопотока (Bit-Perfect).
//
// Данный файл picard_mapper.go определяет структуру каноничных тегов и таблицу их маппинга
// в аргументы контейнеров (FLAC Vorbis Comments, MP3 ID3v2.4, M4A iTunes atoms)
// в соответствии со спецификацией MusicBrainz Picard.
package tagger

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CanonicalTagSet содержит нормализованный набор метаданных трека для записи и отката (Undo).
type CanonicalTagSet struct {
	ReplaceAll       bool              `json:"replaceAll,omitempty"`
	Artist           string            `json:"artist"`
	Album            string            `json:"album"`
	AlbumArtist      string            `json:"albumArtist"`
	Title            string            `json:"title"`
	Subtitle         string            `json:"subtitle,omitempty"`
	Year             int               `json:"year,omitempty"`
	ReleaseDate      string            `json:"releaseDate,omitempty"`
	OriginalDate     string            `json:"originalDate,omitempty"`
	TrackNumber      int               `json:"trackNumber,omitempty"`
	TrackTotal       int               `json:"trackTotal,omitempty"`
	DiscNumber       int               `json:"discNumber,omitempty"`
	DiscTotal        int               `json:"discTotal,omitempty"`
	Compilation      bool              `json:"compilation,omitempty"`
	Genres           []string          `json:"genres,omitempty"`
	Label            string            `json:"label,omitempty"`
	CatalogNumber    string            `json:"catalogNumber,omitempty"`
	Barcode          string            `json:"barcode,omitempty"`
	ISRC             string            `json:"isrc,omitempty"`
	Grouping         string            `json:"grouping,omitempty"`
	Comment          string            `json:"comment,omitempty"`
	Copyright        string            `json:"copyright,omitempty"`
	BPM              int               `json:"bpm,omitempty"`
	Rating           *int              `json:"rating,omitempty"`
	Composer         []string          `json:"composer,omitempty"`
	Lyricist         []string          `json:"lyricist,omitempty"`
	Conductor        []string          `json:"conductor,omitempty"`
	Performer        []string          `json:"performer,omitempty"`
	Producer         []string          `json:"producer,omitempty"`
	Remixer          []string          `json:"remixer,omitempty"`
	ReleaseMBID      string            `json:"releaseMbid,omitempty"`
	ReleaseGroupMBID string            `json:"releaseGroupMbid,omitempty"`
	ArtistMBID       string            `json:"artistMbid,omitempty"`
	RecordingMBID    string            `json:"recordingMbid,omitempty"`
	ReleaseTrackMBID string            `json:"releaseTrackMbid,omitempty"`
	ExtraTags        map[string]string `json:"extraTags,omitempty"`
	// Clear — теги, которые нужно удалить из файла (откат тегов, добавленных правкой). Пустое значение ffmpeg понимает как удаление.
	Clear []string `json:"-"`
}

// undoClears — теги, которые правка добавила, а откат должен убрать: исходный слепок их не содержит, и пустые значения при записи пропускаются.
// shortcut: слепок не хранит MBID файла, поэтому откат убирает MBID из правки, даже если такой же был в файле до неё; хранить MBID в слепке, если это станет важно.
func undoClears(orig, proposed CanonicalTagSet) []string {
	var keys []string
	if strings.TrimSpace(orig.AlbumArtist) == "" { // правка всегда ставит album_artist (по умолчанию — исполнитель)
		keys = append(keys, "album_artist")
	}
	if proposed.ReleaseMBID != "" {
		keys = append(keys, "MUSICBRAINZ_ALBUMID", "MusicBrainz Album Id")
	}
	if proposed.ArtistMBID != "" {
		keys = append(keys, "MUSICBRAINZ_ARTISTID", "MusicBrainz Artist Id")
	}
	if proposed.RecordingMBID != "" {
		keys = append(keys, "MUSICBRAINZ_TRACKID", "MusicBrainz Track Id")
	}
	if proposed.ReleaseGroupMBID != "" {
		keys = append(keys, "MUSICBRAINZ_RELEASEGROUPID", "MusicBrainz Release Group Id")
	}
	if proposed.ReleaseTrackMBID != "" {
		keys = append(keys, "MUSICBRAINZ_RELEASETRACKID", "MusicBrainz Release Track Id")
	}
	return keys
}

// BuildFFmpegMetadataArgs формирует список флагов -metadata key=value по таблице MusicBrainz Picard,
// сохраняя все существующие пользовательские/технические теги (-map_metadata 0).
func BuildFFmpegMetadataArgs(filePath string, tags CanonicalTagSet) []string {
	ext := strings.ToLower(filepath.Ext(filePath))
	var args []string

	addMeta := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			args = append(args, "-metadata", fmt.Sprintf("%s=%s", k, strings.TrimSpace(v)))
		}
	}
	if tags.ReplaceAll {
		for _, key := range []string{"artist", "album", "album_artist", "title", "subtitle", "date", "ORIGINALDATE", "track", "disc", "compilation", "genre", "label", "catalognumber", "barcode", "isrc", "grouping", "comment", "copyright", "bpm", "rating", "composer", "lyricist", "conductor", "performer", "producer", "remixer", "MUSICBRAINZ_ALBUMID", "MUSICBRAINZ_RELEASEGROUPID", "MUSICBRAINZ_ARTISTID", "MUSICBRAINZ_TRACKID", "MUSICBRAINZ_RELEASETRACKID"} {
			args = append(args, "-metadata", key+"=")
		}
	}
	for _, k := range tags.Clear {
		args = append(args, "-metadata", k+"=")
	}

	addMeta("artist", tags.Artist)
	addMeta("album", tags.Album)
	addMeta("album_artist", tags.AlbumArtist)
	addMeta("title", tags.Title)
	addMeta("subtitle", tags.Subtitle)

	releaseDate := strings.TrimSpace(tags.ReleaseDate)
	if releaseDate == "" && tags.Year > 1900 {
		releaseDate = strconv.Itoa(tags.Year)
	}
	addMeta("date", releaseDate)
	if ext == ".flac" || ext == ".ogg" || ext == ".opus" {
		originalDate := strings.TrimSpace(tags.OriginalDate)
		// Legacy callers only supplied Year and historically wrote it to both fields.
		if originalDate == "" && tags.ReleaseDate == "" && tags.Year > 1900 {
			originalDate = strconv.Itoa(tags.Year)
		}
		addMeta("ORIGINALDATE", originalDate)
	}

	if tags.TrackNumber > 0 {
		track := strconv.Itoa(tags.TrackNumber)
		if tags.TrackTotal > 0 {
			track += "/" + strconv.Itoa(tags.TrackTotal)
		}
		addMeta("track", track)
	}
	if tags.DiscNumber > 0 {
		disc := strconv.Itoa(tags.DiscNumber)
		if tags.DiscTotal > 0 {
			disc += "/" + strconv.Itoa(tags.DiscTotal)
		}
		addMeta("disc", disc)
	}
	if tags.Compilation {
		addMeta("compilation", "1")
	}
	if len(tags.Genres) > 0 {
		addMeta("genre", strings.Join(tags.Genres, "; "))
	}
	addMeta("label", tags.Label)
	addMeta("catalognumber", tags.CatalogNumber)
	addMeta("barcode", tags.Barcode)
	addMeta("isrc", tags.ISRC)
	addMeta("grouping", tags.Grouping)
	addMeta("comment", tags.Comment)
	addMeta("copyright", tags.Copyright)
	if tags.BPM > 0 {
		addMeta("bpm", strconv.Itoa(tags.BPM))
	}
	if tags.Rating != nil {
		addMeta("rating", strconv.Itoa(*tags.Rating))
	}
	for _, value := range []struct {
		key    string
		values []string
	}{
		{"composer", tags.Composer}, {"lyricist", tags.Lyricist}, {"conductor", tags.Conductor},
		{"performer", tags.Performer}, {"producer", tags.Producer}, {"remixer", tags.Remixer},
	} {
		if len(value.values) > 0 {
			addMeta(value.key, strings.Join(value.values, "; "))
		}
	}

	// MusicBrainz Picard стандартные идентификаторы для Navidrome
	if tags.ReleaseMBID != "" {
		addMeta("MUSICBRAINZ_ALBUMID", tags.ReleaseMBID)
		addMeta("MusicBrainz Album Id", tags.ReleaseMBID)
	}
	if tags.ArtistMBID != "" {
		addMeta("MUSICBRAINZ_ARTISTID", tags.ArtistMBID)
		addMeta("MusicBrainz Artist Id", tags.ArtistMBID)
	}
	if tags.RecordingMBID != "" {
		addMeta("MUSICBRAINZ_TRACKID", tags.RecordingMBID)
		addMeta("MusicBrainz Track Id", tags.RecordingMBID)
	}
	if tags.ReleaseGroupMBID != "" {
		addMeta("MUSICBRAINZ_RELEASEGROUPID", tags.ReleaseGroupMBID)
		addMeta("MusicBrainz Release Group Id", tags.ReleaseGroupMBID)
	}
	if tags.ReleaseTrackMBID != "" {
		addMeta("MUSICBRAINZ_RELEASETRACKID", tags.ReleaseTrackMBID)
		addMeta("MusicBrainz Release Track Id", tags.ReleaseTrackMBID)
	}
	extraKeys := make([]string, 0, len(tags.ExtraTags))
	for key := range tags.ExtraTags {
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		value := tags.ExtraTags[key]
		key = strings.TrimSpace(key)
		if safeExtraTagKey(key) {
			addMeta(key, value)
		}
	}

	return args
}

func safeExtraTagKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ' ' {
			continue
		}
		return false
	}
	return true
}
