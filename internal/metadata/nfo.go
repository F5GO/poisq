package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/musicmeta"
	"github.com/F5GO/poisq/internal/security"
)

const MaxNFOBytes = 1 << 20

type nfoUniqueID struct {
	Type  string `xml:"type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type ArtistNFO struct {
	XMLName             xml.Name      `xml:"artist"`
	Name                string        `xml:"name,omitempty"`
	SortName            string        `xml:"sortname,omitempty"`
	MusicBrainzArtistID string        `xml:"musicbrainzartistid,omitempty"`
	Genres              []string      `xml:"genre,omitempty"`
	Overview            string        `xml:"overview,omitempty"`
	UniqueIDs           []nfoUniqueID `xml:"uniqueid,omitempty"`
}

type AlbumNFO struct {
	XMLName                   xml.Name      `xml:"album"`
	Title                     string        `xml:"title,omitempty"`
	SortTitle                 string        `xml:"sorttitle,omitempty"`
	Artist                    string        `xml:"artist,omitempty"`
	Year                      int           `xml:"year,omitempty"`
	ReleaseDate               string        `xml:"releasedate,omitempty"`
	Label                     string        `xml:"label,omitempty"`
	Genres                    []string      `xml:"genre,omitempty"`
	Overview                  string        `xml:"overview,omitempty"`
	MusicBrainzAlbumID        string        `xml:"musicbrainzalbumid,omitempty"`
	MusicBrainzReleaseGroupID string        `xml:"musicbrainzreleasegroupid,omitempty"`
	UniqueIDs                 []nfoUniqueID `xml:"uniqueid,omitempty"`
}

type NFOService struct {
	guard    *security.PathGuard
	tracks   *db.TrackRepository
	metadata *db.MetadataRepository
	policy   *db.MetadataPolicyRepository
	staging  *db.StagingRepository
}

func NewNFOService(guard *security.PathGuard, tracks *db.TrackRepository, metadataRepo *db.MetadataRepository, policy *db.MetadataPolicyRepository, staging *db.StagingRepository) *NFOService {
	return &NFOService{guard: guard, tracks: tracks, metadata: metadataRepo, policy: policy, staging: staging}
}

func (s *NFOService) ImportFolder(ctx context.Context, folderPath string) (int, error) {
	folder, err := s.validateFolder(folderPath)
	if err != nil {
		return 0, err
	}
	album, albumAt, albumErr := readAlbumNFO(filepath.Join(folder, "album.nfo"))
	artist, artistAt, artistErr := readArtistNFO(filepath.Join(filepath.Dir(folder), "artist.nfo"))
	if albumErr != nil && !os.IsNotExist(albumErr) {
		return 0, albumErr
	}
	if artistErr != nil && !os.IsNotExist(artistErr) {
		return 0, artistErr
	}
	if album == nil && artist == nil {
		return 0, os.ErrNotExist
	}
	tracks, err := s.tracks.ListByFolder(folder)
	if err != nil {
		return 0, err
	}
	policy, err := s.policy.Get(ctx)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, track := range tracks {
		current, err := s.metadata.GetTrackDocument(ctx, track.ID)
		if err != nil {
			return created, err
		}
		candidate, err := nfoCandidate(current, album, artist, latestTime(albumAt, artistAt))
		if err != nil {
			return created, err
		}
		merged, err := musicmeta.Merge(current, []musicmeta.Document{candidate}, policy)
		if err != nil {
			return created, err
		}
		if reflect.DeepEqual(documentValues(current), documentValues(merged)) {
			continue
		}
		currentJSON, _ := json.Marshal(current)
		proposedJSON, _ := json.Marshal(documentProposal(merged, "Import from Jellyfin/Kodi NFO"))
		if _, err := s.staging.CreateProposal(db.StagingChangeRecord{
			Scope: "track", TargetID: track.ID, FolderPath: folder, ChangeType: "nfo_import", Confidence: 100,
			CurrentJSON: string(currentJSON), ProposedJSON: string(proposedJSON),
		}); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func (s *NFOService) StageExportFolder(ctx context.Context, folderPath string) (int, error) {
	folder, err := s.validateFolder(folderPath)
	if err != nil {
		return 0, err
	}
	tracks, err := s.tracks.ListByFolder(folder)
	if err != nil || len(tracks) == 0 {
		if err == nil {
			err = fmt.Errorf("folder has no tracks")
		}
		return 0, err
	}
	doc, err := s.metadata.GetTrackDocument(ctx, tracks[0].ID)
	if err != nil {
		return 0, err
	}
	album := albumNFOFromDocument(doc)
	artist := artistNFOFromDocument(doc)
	albumXML, err := renderNFO(album)
	if err != nil {
		return 0, err
	}
	artistXML, err := renderNFO(artist)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range []struct {
		path    string
		content []byte
	}{
		{filepath.Join(folder, "album.nfo"), albumXML},
		{filepath.Join(filepath.Dir(folder), "artist.nfo"), artistXML},
	} {
		count, err := s.stageNFOExport(item.path, item.content)
		if err != nil {
			return created, err
		}
		created += count
	}
	return created, nil
}

func (s *NFOService) stageNFOExport(path string, content []byte) (int, error) {
	safePath, err := s.guard.ValidateTarget(path, true)
	if err != nil {
		return 0, err
	}
	current, err := readBoundedFile(safePath, MaxNFOBytes)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if exists && bytes.Equal(current, content) {
		return 0, nil
	}
	currentJSON, _ := json.Marshal(map[string]any{"path": safePath, "content": string(current), "exists": exists})
	proposedJSON, _ := json.Marshal(map[string]any{"path": safePath, "content": string(content), "reason": "Export Jellyfin/Kodi NFO"})
	if _, err := s.staging.CreateProposal(db.StagingChangeRecord{
		Scope: "file", TargetID: safePath, FolderPath: filepath.Dir(safePath), ChangeType: "nfo_export",
		Confidence: 100, CurrentJSON: string(currentJSON), ProposedJSON: string(proposedJSON),
	}); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *NFOService) validateFolder(folderPath string) (string, error) {
	if s == nil || s.guard == nil || s.tracks == nil || s.metadata == nil || s.policy == nil || s.staging == nil {
		return "", fmt.Errorf("NFO service is not configured")
	}
	folder, err := s.guard.ValidateTarget(folderPath, false)
	if err != nil {
		return "", err
	}
	stat, err := os.Stat(folder)
	if err != nil || !stat.IsDir() {
		return "", fmt.Errorf("invalid NFO folder")
	}
	return folder, nil
}

func readAlbumNFO(path string) (*AlbumNFO, time.Time, error) {
	var value AlbumNFO
	at, err := readNFO(path, "album", &value)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &value, at, nil
}

func readArtistNFO(path string) (*ArtistNFO, time.Time, error) {
	var value ArtistNFO
	at, err := readNFO(path, "artist", &value)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &value, at, nil
}

func readNFO(path, root string, target any) (time.Time, error) {
	content, err := readBoundedFile(path, MaxNFOBytes)
	if err != nil {
		return time.Time{}, err
	}
	if !utf8.Valid(content) {
		return time.Time{}, fmt.Errorf("NFO is not valid UTF-8")
	}
	lower := bytes.ToLower(content)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return time.Time{}, fmt.Errorf("NFO directives are not allowed")
	}
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	var envelope struct{ XMLName xml.Name }
	if err := xml.Unmarshal(content, &envelope); err != nil || envelope.XMLName.Local != root {
		return time.Time{}, fmt.Errorf("invalid %s NFO root", root)
	}
	if err := decoder.Decode(target); err != nil {
		return time.Time{}, fmt.Errorf("decode %s NFO: %w", root, err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return stat.ModTime().UTC(), nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}

func nfoCandidate(current musicmeta.Document, album *AlbumNFO, artist *ArtistNFO, updatedAt time.Time) (musicmeta.Document, error) {
	doc := musicmeta.Document{TrackID: current.TrackID, FieldStates: make(map[musicmeta.Field]musicmeta.FieldState)}
	state := musicmeta.FieldState{Source: musicmeta.SourceNFO, Provider: "nfo", Confidence: 1, UpdatedAt: updatedAt}
	albumArtist := ""
	if artist != nil {
		albumArtist = strings.TrimSpace(artist.Name)
	}
	if album != nil {
		doc.Album = strings.TrimSpace(album.Title)
		doc.SortAlbum = strings.TrimSpace(album.SortTitle)
		doc.Label = strings.TrimSpace(album.Label)
		doc.Comment = strings.TrimSpace(album.Overview)
		doc.Genres = cleanNFOGenres(album.Genres)
		if strings.TrimSpace(album.Artist) != "" {
			albumArtist = strings.TrimSpace(album.Artist)
		}
		date := strings.TrimSpace(album.ReleaseDate)
		if date == "" && album.Year > 0 {
			date = strconv.Itoa(album.Year)
		}
		if parsed, err := musicmeta.ParsePartialDate(date); err == nil {
			doc.ReleaseDate = parsed
		}
		addNFOID(&doc, musicmeta.IDMusicBrainzRelease, firstNFOID(album.MusicBrainzAlbumID, album.UniqueIDs, "musicbrainzalbum", "musicbrainzrelease"))
		addNFOID(&doc, musicmeta.IDMusicBrainzReleaseGroup, firstNFOID(album.MusicBrainzReleaseGroupID, album.UniqueIDs, "musicbrainzreleasegroup"))
	}
	if artist != nil {
		addNFOID(&doc, musicmeta.IDMusicBrainzArtist, firstNFOID(artist.MusicBrainzArtistID, artist.UniqueIDs, "musicbrainzartist"))
	}
	if albumArtist != "" {
		for _, credit := range current.Credits {
			if credit.Role != musicmeta.RoleAlbumArtist {
				doc.Credits = append(doc.Credits, credit)
			}
		}
		doc.Credits = append(doc.Credits, musicmeta.Credit{Role: musicmeta.RoleAlbumArtist, Name: albumArtist})
	}
	for field, present := range map[musicmeta.Field]bool{
		musicmeta.FieldAlbum: doc.Album != "", musicmeta.FieldSortAlbum: doc.SortAlbum != "",
		musicmeta.FieldLabel: doc.Label != "", musicmeta.FieldComment: doc.Comment != "",
		musicmeta.FieldGenres: len(doc.Genres) > 0, musicmeta.FieldReleaseDate: doc.ReleaseDate.Value != "",
		musicmeta.FieldCredits: len(doc.Credits) > 0, musicmeta.FieldExternalIDs: len(doc.ExternalIDs) > 0,
	} {
		if present {
			doc.FieldStates[field] = state
		}
	}
	clean, err := musicmeta.ValidateDocument(doc)
	if err != nil {
		return musicmeta.Document{}, fmt.Errorf("validate NFO metadata: %w", err)
	}
	return clean, nil
}

func addNFOID(doc *musicmeta.Document, kind musicmeta.ExternalIDType, value string) {
	if value == "" {
		return
	}
	test, err := musicmeta.ValidateDocument(musicmeta.Document{TrackID: doc.TrackID, ExternalIDs: musicmeta.ExternalIDs{{Type: kind, Value: value}}})
	if err == nil {
		doc.ExternalIDs = append(doc.ExternalIDs, test.ExternalIDs[0])
	}
}

func firstNFOID(direct string, values []nfoUniqueID, kinds ...string) string {
	if direct = strings.TrimSpace(direct); direct != "" {
		return direct
	}
	for _, value := range values {
		for _, kind := range kinds {
			if strings.EqualFold(strings.TrimSpace(value.Type), kind) {
				return strings.TrimSpace(value.Value)
			}
		}
	}
	return ""
}

func cleanNFOGenres(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ";") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func documentValues(doc musicmeta.Document) musicmeta.Document {
	out := doc.Clone()
	out.FieldStates = nil
	return out
}

func documentProposal(doc musicmeta.Document, reason string) map[string]any {
	return map[string]any{
		"artist": musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleArtist), "album": doc.Album,
		"albumArtist": musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleAlbumArtist), "title": doc.Title,
		"subtitle": doc.Subtitle, "year": doc.ReleaseDate.Year(), "trackNumber": doc.TrackNumber,
		"trackTotal": doc.TrackTotal, "discNumber": doc.DiscNumber, "discTotal": doc.DiscTotal,
		"compilation": doc.Compilation, "genres": doc.Genres, "label": doc.Label,
		"catalogNumber": doc.CatalogNumber, "barcode": doc.Barcode, "isrc": doc.ISRC,
		"grouping": doc.Grouping, "comment": doc.Comment, "copyright": doc.Copyright,
		"artistMbid":       findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzArtist),
		"releaseMbid":      findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzRelease),
		"releaseGroupMbid": findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzReleaseGroup),
		"recordingMbid":    findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzRecording),
		"releaseTrackMbid": findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzReleaseTrack),
		"reason":           reason,
	}
}

func findExternalID(values musicmeta.ExternalIDs, kind musicmeta.ExternalIDType) string {
	for _, value := range values {
		if value.Type == kind {
			return value.Value
		}
	}
	return ""
}

func albumNFOFromDocument(doc musicmeta.Document) AlbumNFO {
	return AlbumNFO{
		Title: doc.Album, SortTitle: doc.SortAlbum, Artist: musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleAlbumArtist),
		Year: doc.ReleaseDate.Year(), ReleaseDate: doc.ReleaseDate.Value, Label: doc.Label,
		Genres: append([]string(nil), doc.Genres...), Overview: doc.Comment,
		MusicBrainzAlbumID:        findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzRelease),
		MusicBrainzReleaseGroupID: findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzReleaseGroup),
	}
}

func artistNFOFromDocument(doc musicmeta.Document) ArtistNFO {
	name := musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleAlbumArtist)
	if name == "" {
		name = musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleArtist)
	}
	return ArtistNFO{Name: name, Genres: append([]string(nil), doc.Genres...), MusicBrainzArtistID: findExternalID(doc.ExternalIDs, musicmeta.IDMusicBrainzArtist)}
}

func renderNFO(value any) ([]byte, error) {
	body, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	content := append([]byte(xml.Header), body...)
	content = append(content, '\n')
	if len(content) > MaxNFOBytes {
		return nil, fmt.Errorf("rendered NFO exceeds size limit")
	}
	return content, nil
}

func latestTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}
