package musicmeta

import (
	"encoding/json"
	"fmt"
	"strings"
)

type FieldValue struct {
	Field string
	Value json.RawMessage
}

// ApplyFieldValues applies canonical history values to only the named fields.
func ApplyFieldValues(input Document, values []FieldValue) (Document, error) {
	doc := input.Clone()
	for _, item := range values {
		if strings.HasPrefix(item.Field, "custom:") {
			key := strings.TrimPrefix(item.Field, "custom:")
			var value *string
			if err := json.Unmarshal(item.Value, &value); err != nil {
				return Document{}, fmt.Errorf("decode %s: %w", item.Field, err)
			}
			updated := false
			for index := range doc.CustomTags {
				if strings.EqualFold(doc.CustomTags[index].Key, key) {
					if value == nil {
						doc.CustomTags = append(doc.CustomTags[:index], doc.CustomTags[index+1:]...)
					} else {
						doc.CustomTags[index].Value = *value
					}
					updated = true
					break
				}
			}
			if !updated && value != nil {
				doc.CustomTags = append(doc.CustomTags, CustomTag{Key: key, Value: *value})
			}
			continue
		}
		field := Field(item.Field)
		if !ValidField(field) || field == FieldCustomTags {
			return Document{}, fmt.Errorf("invalid history field %q", item.Field)
		}
		if err := unmarshalDocumentField(&doc, field, item.Value); err != nil {
			return Document{}, fmt.Errorf("decode %s: %w", item.Field, err)
		}
	}
	return ValidateDocument(doc)
}

func unmarshalDocumentField(doc *Document, field Field, raw json.RawMessage) error {
	switch field {
	case FieldTitle:
		return json.Unmarshal(raw, &doc.Title)
	case FieldSubtitle:
		return json.Unmarshal(raw, &doc.Subtitle)
	case FieldAlbum:
		return json.Unmarshal(raw, &doc.Album)
	case FieldSortTitle:
		return json.Unmarshal(raw, &doc.SortTitle)
	case FieldSortAlbum:
		return json.Unmarshal(raw, &doc.SortAlbum)
	case FieldReleaseDate:
		return json.Unmarshal(raw, &doc.ReleaseDate)
	case FieldOriginalReleaseDate:
		return json.Unmarshal(raw, &doc.OriginalReleaseDate)
	case FieldTrackNumber:
		return json.Unmarshal(raw, &doc.TrackNumber)
	case FieldTrackTotal:
		return json.Unmarshal(raw, &doc.TrackTotal)
	case FieldDiscNumber:
		return json.Unmarshal(raw, &doc.DiscNumber)
	case FieldDiscTotal:
		return json.Unmarshal(raw, &doc.DiscTotal)
	case FieldCompilation:
		return json.Unmarshal(raw, &doc.Compilation)
	case FieldLabel:
		return json.Unmarshal(raw, &doc.Label)
	case FieldCatalogNumber:
		return json.Unmarshal(raw, &doc.CatalogNumber)
	case FieldBarcode:
		return json.Unmarshal(raw, &doc.Barcode)
	case FieldISRC:
		return json.Unmarshal(raw, &doc.ISRC)
	case FieldGrouping:
		return json.Unmarshal(raw, &doc.Grouping)
	case FieldComment:
		return json.Unmarshal(raw, &doc.Comment)
	case FieldCopyright:
		return json.Unmarshal(raw, &doc.Copyright)
	case FieldBPM:
		return json.Unmarshal(raw, &doc.BPM)
	case FieldRating:
		return json.Unmarshal(raw, &doc.Rating)
	case FieldCredits:
		return json.Unmarshal(raw, &doc.Credits)
	case FieldGenres:
		return json.Unmarshal(raw, &doc.Genres)
	case FieldExternalIDs:
		return json.Unmarshal(raw, &doc.ExternalIDs)
	default:
		return fmt.Errorf("unsupported field")
	}
}
