package musicmeta

import (
	"strconv"
	"strings"
)

// LegacyTrack is the metadata subset understood by the original tracks table.
type LegacyTrack struct {
	TrackID     string
	Artist      string
	Album       string
	AlbumArtist string
	Title       string
	Year        int
	TrackNumber int
}

func DocumentFromLegacy(value LegacyTrack) Document {
	doc := Document{
		TrackID:     value.TrackID,
		Title:       strings.TrimSpace(value.Title),
		Album:       strings.TrimSpace(value.Album),
		TrackNumber: value.TrackNumber,
		Credits:     append(displayCredits(RoleArtist, value.Artist), displayCredits(RoleAlbumArtist, value.AlbumArtist)...),
		FieldStates: make(map[Field]FieldState),
	}
	if value.Year > 0 {
		doc.ReleaseDate = PartialDate{Value: strconv.Itoa(value.Year), Precision: PrecisionYear}
	}
	for field, present := range map[Field]bool{
		FieldTitle: doc.Title != "", FieldAlbum: doc.Album != "", FieldTrackNumber: doc.TrackNumber > 0,
		FieldReleaseDate: doc.ReleaseDate.Value != "", FieldCredits: len(doc.Credits) > 0,
	} {
		if present {
			doc.FieldStates[field] = FieldState{Source: SourceLegacy, Confidence: 1}
		}
	}
	return doc
}

// ApplyLegacy overlays only fields representable by LegacyTrack.
func ApplyLegacy(base Document, value LegacyTrack) Document {
	out := base.Clone()
	out.TrackID = value.TrackID
	out.Title = strings.TrimSpace(value.Title)
	out.Album = strings.TrimSpace(value.Album)
	out.TrackNumber = value.TrackNumber
	if value.Year > 0 {
		out.ReleaseDate = PartialDate{Value: strconv.Itoa(value.Year), Precision: PrecisionYear}
	} else {
		out.ReleaseDate = PartialDate{}
	}
	preserved := make([]Credit, 0, len(out.Credits))
	for _, credit := range out.Credits {
		if credit.Role != RoleArtist && credit.Role != RoleAlbumArtist {
			preserved = append(preserved, credit)
		}
	}
	out.Credits = append(displayCredits(RoleArtist, value.Artist), displayCredits(RoleAlbumArtist, value.AlbumArtist)...)
	out.Credits = append(out.Credits, preserved...)
	return out
}

func ToLegacy(doc Document) LegacyTrack {
	return LegacyTrack{
		TrackID: doc.TrackID, Artist: DisplayCredits(doc.Credits, RoleArtist),
		Album: doc.Album, AlbumArtist: DisplayCredits(doc.Credits, RoleAlbumArtist),
		Title: doc.Title, Year: doc.ReleaseDate.Year(), TrackNumber: doc.TrackNumber,
	}
}

func DisplayCredits(credits []Credit, role CreditRole) string {
	values := make([]string, 0, len(credits))
	for _, credit := range credits {
		if credit.Role == role && strings.TrimSpace(credit.Name) != "" {
			values = append(values, strings.TrimSpace(credit.Name))
		}
	}
	return strings.Join(values, "; ")
}

func displayCredits(role CreditRole, value string) []Credit {
	parts := strings.Split(value, ";")
	out := make([]Credit, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, Credit{Role: role, Name: name})
		}
	}
	return out
}
