package musicmeta

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type FieldChange struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

func DiffDocuments(before, after Document) ([]FieldChange, error) {
	left, err := ValidateDocument(before)
	if err != nil {
		return nil, err
	}
	right, err := ValidateDocument(after)
	if err != nil {
		return nil, err
	}
	if left.TrackID != right.TrackID {
		return nil, fmt.Errorf("track ids do not match")
	}
	fields := append([]Field(nil), allMetadataFields...)
	changes := make([]FieldChange, 0)
	for _, field := range fields {
		l, r := documentFieldValue(left, field), documentFieldValue(right, field)
		if reflect.DeepEqual(l, r) {
			continue
		}
		lb, _ := json.Marshal(l)
		rb, _ := json.Marshal(r)
		changes = append(changes, FieldChange{Field: string(field), Before: lb, After: rb})
	}
	leftCustom, rightCustom := customTagMap(left.CustomTags), customTagMap(right.CustomTags)
	keys := make(map[string]bool, len(leftCustom)+len(rightCustom))
	for key := range leftCustom {
		keys[key] = true
	}
	for key := range rightCustom {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		leftValue, leftOK := leftCustom[key]
		rightValue, rightOK := rightCustom[key]
		if leftOK == rightOK && (!leftOK || leftValue == rightValue) {
			continue
		}
		var leftCanonical, rightCanonical *string
		if leftOK {
			value := leftValue
			leftCanonical = &value
		}
		if rightOK {
			value := rightValue
			rightCanonical = &value
		}
		lb, _ := json.Marshal(leftCanonical)
		rb, _ := json.Marshal(rightCanonical)
		changes = append(changes, FieldChange{Field: "custom:" + key, Before: lb, After: rb})
	}
	return changes, nil
}

func DocumentFingerprint(doc Document) (string, error) {
	clean, err := ValidateDocument(doc)
	if err != nil {
		return "", err
	}
	clean.FieldStates = nil
	encoded, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func documentFieldValue(doc Document, field Field) any {
	switch field {
	case FieldTitle:
		return doc.Title
	case FieldSubtitle:
		return doc.Subtitle
	case FieldAlbum:
		return doc.Album
	case FieldSortTitle:
		return doc.SortTitle
	case FieldSortAlbum:
		return doc.SortAlbum
	case FieldReleaseDate:
		return doc.ReleaseDate
	case FieldOriginalReleaseDate:
		return doc.OriginalReleaseDate
	case FieldTrackNumber:
		return doc.TrackNumber
	case FieldTrackTotal:
		return doc.TrackTotal
	case FieldDiscNumber:
		return doc.DiscNumber
	case FieldDiscTotal:
		return doc.DiscTotal
	case FieldCompilation:
		return doc.Compilation
	case FieldLabel:
		return doc.Label
	case FieldCatalogNumber:
		return doc.CatalogNumber
	case FieldBarcode:
		return doc.Barcode
	case FieldISRC:
		return doc.ISRC
	case FieldGrouping:
		return doc.Grouping
	case FieldComment:
		return doc.Comment
	case FieldCopyright:
		return doc.Copyright
	case FieldBPM:
		return doc.BPM
	case FieldRating:
		return doc.Rating
	case FieldCredits:
		return doc.Credits
	case FieldGenres:
		return doc.Genres
	case FieldExternalIDs:
		return doc.ExternalIDs
	case FieldCustomTags:
		return nil // custom keys are diffed independently
	default:
		return nil
	}
}

func customTagMap(values []CustomTag) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[strings.ToLower(strings.TrimSpace(value.Key))] = value.Value
	}
	return out
}
