package musicmeta

import (
	"fmt"
	"strings"
)

// MetadataPatch distinguishes omitted fields (preserve) from explicit zero/empty values (clear).
type MetadataPatch struct {
	Title               *string      `json:"title,omitempty"`
	Subtitle            *string      `json:"subtitle,omitempty"`
	Album               *string      `json:"album,omitempty"`
	SortTitle           *string      `json:"sortTitle,omitempty"`
	SortAlbum           *string      `json:"sortAlbum,omitempty"`
	ReleaseDate         *string      `json:"releaseDate,omitempty"`
	OriginalReleaseDate *string      `json:"originalReleaseDate,omitempty"`
	TrackNumber         *int         `json:"trackNumber,omitempty"`
	TrackTotal          *int         `json:"trackTotal,omitempty"`
	DiscNumber          *int         `json:"discNumber,omitempty"`
	DiscTotal           *int         `json:"discTotal,omitempty"`
	Compilation         *bool        `json:"compilation,omitempty"`
	Label               *string      `json:"label,omitempty"`
	CatalogNumber       *string      `json:"catalogNumber,omitempty"`
	Barcode             *string      `json:"barcode,omitempty"`
	ISRC                *string      `json:"isrc,omitempty"`
	Grouping            *string      `json:"grouping,omitempty"`
	Comment             *string      `json:"comment,omitempty"`
	Copyright           *string      `json:"copyright,omitempty"`
	BPM                 *int         `json:"bpm,omitempty"`
	Rating              *int         `json:"rating,omitempty"`
	Credits             *[]Credit    `json:"credits,omitempty"`
	Genres              *[]string    `json:"genres,omitempty"`
	ExternalIDs         *ExternalIDs `json:"externalIds,omitempty"`
	CustomTags          *[]CustomTag `json:"customTags,omitempty"`
	Clear               []Field      `json:"clear,omitempty"`
}

func ApplyPatch(input Document, patch MetadataPatch) (Document, error) {
	doc := input.Clone()
	set := patchSetFields(patch)
	seenClear := make(map[Field]bool, len(patch.Clear))
	for _, field := range patch.Clear {
		if !ValidField(field) {
			return Document{}, fmt.Errorf("unknown metadata field %q", field)
		}
		if seenClear[field] {
			return Document{}, fmt.Errorf("duplicate clear field %q", field)
		}
		if set[field] {
			return Document{}, fmt.Errorf("field %q cannot be set and cleared", field)
		}
		seenClear[field] = true
		clearDocumentField(&doc, field)
	}
	if patch.Title != nil {
		doc.Title = *patch.Title
	}
	if patch.Subtitle != nil {
		doc.Subtitle = *patch.Subtitle
	}
	if patch.Album != nil {
		doc.Album = *patch.Album
	}
	if patch.SortTitle != nil {
		doc.SortTitle = *patch.SortTitle
	}
	if patch.SortAlbum != nil {
		doc.SortAlbum = *patch.SortAlbum
	}
	if patch.ReleaseDate != nil {
		value, err := ParsePartialDate(strings.TrimSpace(*patch.ReleaseDate))
		if err != nil {
			return Document{}, err
		}
		doc.ReleaseDate = value
	}
	if patch.OriginalReleaseDate != nil {
		value, err := ParsePartialDate(strings.TrimSpace(*patch.OriginalReleaseDate))
		if err != nil {
			return Document{}, err
		}
		doc.OriginalReleaseDate = value
	}
	if patch.TrackNumber != nil {
		doc.TrackNumber = *patch.TrackNumber
	}
	if patch.TrackTotal != nil {
		doc.TrackTotal = *patch.TrackTotal
	}
	if patch.DiscNumber != nil {
		doc.DiscNumber = *patch.DiscNumber
	}
	if patch.DiscTotal != nil {
		doc.DiscTotal = *patch.DiscTotal
	}
	if patch.Compilation != nil {
		doc.Compilation = *patch.Compilation
	}
	if patch.Label != nil {
		doc.Label = *patch.Label
	}
	if patch.CatalogNumber != nil {
		doc.CatalogNumber = *patch.CatalogNumber
	}
	if patch.Barcode != nil {
		doc.Barcode = *patch.Barcode
	}
	if patch.ISRC != nil {
		doc.ISRC = *patch.ISRC
	}
	if patch.Grouping != nil {
		doc.Grouping = *patch.Grouping
	}
	if patch.Comment != nil {
		doc.Comment = *patch.Comment
	}
	if patch.Copyright != nil {
		doc.Copyright = *patch.Copyright
	}
	if patch.BPM != nil {
		doc.BPM = *patch.BPM
	}
	if patch.Rating != nil {
		value := *patch.Rating
		doc.Rating = &value
	}
	if patch.Credits != nil {
		doc.Credits = append([]Credit(nil), (*patch.Credits)...)
	}
	if patch.Genres != nil {
		doc.Genres = append([]string(nil), (*patch.Genres)...)
	}
	if patch.ExternalIDs != nil {
		doc.ExternalIDs = append(ExternalIDs(nil), (*patch.ExternalIDs)...)
	}
	if patch.CustomTags != nil {
		doc.CustomTags = append([]CustomTag(nil), (*patch.CustomTags)...)
	}
	return ValidateDocument(doc)
}

func patchSetFields(p MetadataPatch) map[Field]bool {
	return map[Field]bool{
		FieldTitle: p.Title != nil, FieldSubtitle: p.Subtitle != nil, FieldAlbum: p.Album != nil,
		FieldSortTitle: p.SortTitle != nil, FieldSortAlbum: p.SortAlbum != nil, FieldReleaseDate: p.ReleaseDate != nil,
		FieldOriginalReleaseDate: p.OriginalReleaseDate != nil, FieldTrackNumber: p.TrackNumber != nil,
		FieldTrackTotal: p.TrackTotal != nil, FieldDiscNumber: p.DiscNumber != nil, FieldDiscTotal: p.DiscTotal != nil,
		FieldCompilation: p.Compilation != nil, FieldLabel: p.Label != nil, FieldCatalogNumber: p.CatalogNumber != nil,
		FieldBarcode: p.Barcode != nil, FieldISRC: p.ISRC != nil, FieldGrouping: p.Grouping != nil,
		FieldComment: p.Comment != nil, FieldCopyright: p.Copyright != nil, FieldBPM: p.BPM != nil,
		FieldRating: p.Rating != nil, FieldCredits: p.Credits != nil, FieldGenres: p.Genres != nil,
		FieldExternalIDs: p.ExternalIDs != nil, FieldCustomTags: p.CustomTags != nil,
	}
}

func clearDocumentField(doc *Document, field Field) {
	switch field {
	case FieldTitle:
		doc.Title = ""
	case FieldSubtitle:
		doc.Subtitle = ""
	case FieldAlbum:
		doc.Album = ""
	case FieldSortTitle:
		doc.SortTitle = ""
	case FieldSortAlbum:
		doc.SortAlbum = ""
	case FieldReleaseDate:
		doc.ReleaseDate = PartialDate{}
	case FieldOriginalReleaseDate:
		doc.OriginalReleaseDate = PartialDate{}
	case FieldTrackNumber:
		doc.TrackNumber = 0
	case FieldTrackTotal:
		doc.TrackTotal = 0
	case FieldDiscNumber:
		doc.DiscNumber = 0
	case FieldDiscTotal:
		doc.DiscTotal = 0
	case FieldCompilation:
		doc.Compilation = false
	case FieldLabel:
		doc.Label = ""
	case FieldCatalogNumber:
		doc.CatalogNumber = ""
	case FieldBarcode:
		doc.Barcode = ""
	case FieldISRC:
		doc.ISRC = ""
	case FieldGrouping:
		doc.Grouping = ""
	case FieldComment:
		doc.Comment = ""
	case FieldCopyright:
		doc.Copyright = ""
	case FieldBPM:
		doc.BPM = 0
	case FieldRating:
		doc.Rating = nil
	case FieldCredits:
		doc.Credits = nil
	case FieldGenres:
		doc.Genres = nil
	case FieldExternalIDs:
		doc.ExternalIDs = nil
	case FieldCustomTags:
		doc.CustomTags = nil
	}
}
