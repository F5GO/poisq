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
	"strconv"
	"strings"
)

// CanonicalTagSet содержит нормализованный набор метаданных трека для записи и отката (Undo).
type CanonicalTagSet struct {
	Artist        string            `json:"artist"`
	Album         string            `json:"album"`
	AlbumArtist   string            `json:"albumArtist"`
	Title         string            `json:"title"`
	Year          int               `json:"year,omitempty"`
	TrackNumber   int               `json:"trackNumber,omitempty"`
	ReleaseMBID   string            `json:"releaseMbid,omitempty"`
	ArtistMBID    string            `json:"artistMbid,omitempty"`
	RecordingMBID string            `json:"recordingMbid,omitempty"`
	ExtraTags     map[string]string `json:"extraTags,omitempty"`
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
		keys = append(keys, "MUSICBRAINZ_TRACKID", "MusicBrainz Release Track Id")
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

	addMeta("artist", tags.Artist)
	addMeta("album", tags.Album)
	addMeta("album_artist", tags.AlbumArtist)
	addMeta("title", tags.Title)

	if tags.Year > 1900 {
		yearStr := strconv.Itoa(tags.Year)
		addMeta("date", yearStr)
		if ext == ".flac" || ext == ".ogg" || ext == ".opus" {
			addMeta("ORIGINALDATE", yearStr)
		}
	}

	if tags.TrackNumber > 0 {
		addMeta("track", strconv.Itoa(tags.TrackNumber))
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
		addMeta("MusicBrainz Release Track Id", tags.RecordingMBID)
	}

	for _, k := range tags.Clear {
		args = append(args, "-metadata", k+"=")
	}
	return args
}
