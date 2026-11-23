package musicmeta

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const MaxProviderRules = 64

type RefreshMode string

const (
	RefreshMissingOnly RefreshMode = "missing_only"
	RefreshPriority    RefreshMode = "priority"
	RefreshNewer       RefreshMode = "newer"
	RefreshNever       RefreshMode = "never"
)

type ProviderRule struct {
	Source        Source      `json:"source"`
	Provider      string      `json:"provider,omitempty"`
	Priority      int         `json:"priority"`
	RefreshMode   RefreshMode `json:"refreshMode"`
	Enabled       bool        `json:"enabled"`
	MinConfidence float64     `json:"minConfidence"`
}

type MergePolicy struct {
	Rules []ProviderRule `json:"rules"`
}

func DefaultMergePolicy() MergePolicy {
	return MergePolicy{Rules: []ProviderRule{
		{Source: SourceManual, Priority: 0, RefreshMode: RefreshPriority, Enabled: true},
		{Source: SourceAudioFile, Priority: 10, RefreshMode: RefreshPriority, Enabled: true},
		{Source: SourceNFO, Priority: 20, RefreshMode: RefreshPriority, Enabled: true},
		{Source: SourceMusicBrainz, Priority: 30, RefreshMode: RefreshPriority, Enabled: true, MinConfidence: 0.5},
		{Source: SourceDerived, Priority: 40, RefreshMode: RefreshMissingOnly, Enabled: true, MinConfidence: 0.5},
		{Source: SourceNavidrome, Priority: 50, RefreshMode: RefreshMissingOnly, Enabled: true},
		{Source: SourceLegacy, Priority: 60, RefreshMode: RefreshMissingOnly, Enabled: true},
	}}
}

func ValidateMergePolicy(input MergePolicy) (MergePolicy, error) {
	if len(input.Rules) == 0 {
		return MergePolicy{}, fmt.Errorf("merge policy has no rules")
	}
	if len(input.Rules) > MaxProviderRules {
		return MergePolicy{}, fmt.Errorf("merge policy exceeds %d rules", MaxProviderRules)
	}
	out := MergePolicy{Rules: make([]ProviderRule, 0, len(input.Rules))}
	seen := make(map[string]struct{}, len(input.Rules))
	for _, rule := range input.Rules {
		if !validSources[rule.Source] {
			return MergePolicy{}, fmt.Errorf("unknown metadata source %q", rule.Source)
		}
		provider, err := cleanText("provider", rule.Provider, MaxProviderBytes, false)
		if err != nil {
			return MergePolicy{}, err
		}
		rule.Provider = provider
		if rule.Priority < 0 || rule.Priority > 100000 {
			return MergePolicy{}, fmt.Errorf("invalid priority for %s", rule.Source)
		}
		switch rule.RefreshMode {
		case RefreshMissingOnly, RefreshPriority, RefreshNewer, RefreshNever:
		default:
			return MergePolicy{}, fmt.Errorf("invalid refresh mode %q", rule.RefreshMode)
		}
		if math.IsNaN(rule.MinConfidence) || math.IsInf(rule.MinConfidence, 0) || rule.MinConfidence < 0 || rule.MinConfidence > 1 {
			return MergePolicy{}, fmt.Errorf("invalid minimum confidence for %s", rule.Source)
		}
		key := string(rule.Source) + "\x00" + strings.ToLower(rule.Provider)
		if _, ok := seen[key]; ok {
			return MergePolicy{}, fmt.Errorf("duplicate merge rule for %s/%s", rule.Source, rule.Provider)
		}
		seen[key] = struct{}{}
		out.Rules = append(out.Rules, rule)
	}
	sort.Slice(out.Rules, func(i, j int) bool {
		if out.Rules[i].Priority != out.Rules[j].Priority {
			return out.Rules[i].Priority < out.Rules[j].Priority
		}
		if out.Rules[i].Source != out.Rules[j].Source {
			return out.Rules[i].Source < out.Rules[j].Source
		}
		return out.Rules[i].Provider < out.Rules[j].Provider
	})
	return out, nil
}

func Merge(current Document, candidates []Document, inputPolicy MergePolicy) (Document, error) {
	base, err := ValidateDocument(current)
	if err != nil {
		return Document{}, fmt.Errorf("validate current metadata: %w", err)
	}
	policy, err := ValidateMergePolicy(inputPolicy)
	if err != nil {
		return Document{}, err
	}
	validated := make([]Document, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.TrackID != base.TrackID {
			return Document{}, fmt.Errorf("candidate track id %q does not match %q", candidate.TrackID, base.TrackID)
		}
		clean, err := ValidateDocument(candidate)
		if err != nil {
			return Document{}, fmt.Errorf("validate metadata candidate: %w", err)
		}
		validated = append(validated, clean)
	}
	out := base.Clone()
	for _, field := range allMetadataFields {
		currentState, currentPresent := base.FieldStates[field]
		if currentPresent && currentState.Locked {
			continue
		}
		winner := fieldChoice{document: base, state: currentState, present: currentPresent, current: true}
		if currentPresent {
			winner.rule, winner.hasRule = findRule(policy, currentState)
		}
		for _, candidate := range validated {
			state, present := candidate.FieldStates[field]
			if !present {
				continue
			}
			rule, ok := findRule(policy, state)
			if !ok || !rule.Enabled || rule.RefreshMode == RefreshNever || state.Confidence < rule.MinConfidence {
				continue
			}
			switch rule.RefreshMode {
			case RefreshMissingOnly:
				if currentPresent {
					continue
				}
			case RefreshNewer:
				if currentPresent && !state.UpdatedAt.After(currentState.UpdatedAt) {
					continue
				}
			}
			choice := fieldChoice{document: candidate, state: state, rule: rule, hasRule: true, present: true}
			if betterFieldChoice(field, choice, winner) {
				winner = choice
			}
		}
		if winner.present && !winner.current {
			copyDocumentField(&out, winner.document, field)
			out.FieldStates[field] = winner.state
		}
	}
	return ValidateDocument(out)
}

type fieldChoice struct {
	document Document
	state    FieldState
	rule     ProviderRule
	hasRule  bool
	present  bool
	current  bool
}

func findRule(policy MergePolicy, state FieldState) (ProviderRule, bool) {
	for _, rule := range policy.Rules {
		if rule.Source == state.Source && rule.Provider != "" && strings.EqualFold(rule.Provider, state.Provider) {
			return rule, true
		}
	}
	for _, rule := range policy.Rules {
		if rule.Source == state.Source && rule.Provider == "" {
			return rule, true
		}
	}
	return ProviderRule{}, false
}

func betterFieldChoice(field Field, candidate, winner fieldChoice) bool {
	if !winner.present {
		return true
	}
	candidatePriority := candidate.rule.Priority
	winnerPriority := 100001
	if winner.hasRule && winner.rule.Enabled {
		winnerPriority = winner.rule.Priority
	}
	if candidatePriority != winnerPriority {
		return candidatePriority < winnerPriority
	}
	if candidate.state.Confidence != winner.state.Confidence {
		return candidate.state.Confidence > winner.state.Confidence
	}
	if !candidate.state.UpdatedAt.Equal(winner.state.UpdatedAt) {
		return candidate.state.UpdatedAt.After(winner.state.UpdatedAt)
	}
	candidateKey := string(candidate.state.Source) + "\x00" + strings.ToLower(candidate.state.Provider) + "\x00" + fieldValueKey(candidate.document, field)
	winnerKey := string(winner.state.Source) + "\x00" + strings.ToLower(winner.state.Provider) + "\x00" + fieldValueKey(winner.document, field)
	return candidateKey < winnerKey
}

var allMetadataFields = []Field{
	FieldTitle, FieldSubtitle, FieldAlbum, FieldSortTitle, FieldSortAlbum, FieldReleaseDate,
	FieldOriginalReleaseDate, FieldTrackNumber, FieldTrackTotal, FieldDiscNumber, FieldDiscTotal,
	FieldCompilation, FieldLabel, FieldCatalogNumber, FieldBarcode, FieldISRC, FieldGrouping,
	FieldComment, FieldCopyright, FieldCredits, FieldGenres, FieldExternalIDs,
	FieldBPM, FieldRating, FieldCustomTags,
}

func copyDocumentField(target *Document, source Document, field Field) {
	switch field {
	case FieldTitle:
		target.Title = source.Title
	case FieldSubtitle:
		target.Subtitle = source.Subtitle
	case FieldAlbum:
		target.Album = source.Album
	case FieldSortTitle:
		target.SortTitle = source.SortTitle
	case FieldSortAlbum:
		target.SortAlbum = source.SortAlbum
	case FieldReleaseDate:
		target.ReleaseDate = source.ReleaseDate
	case FieldOriginalReleaseDate:
		target.OriginalReleaseDate = source.OriginalReleaseDate
	case FieldTrackNumber:
		target.TrackNumber = source.TrackNumber
	case FieldTrackTotal:
		target.TrackTotal = source.TrackTotal
	case FieldDiscNumber:
		target.DiscNumber = source.DiscNumber
	case FieldDiscTotal:
		target.DiscTotal = source.DiscTotal
	case FieldCompilation:
		target.Compilation = source.Compilation
	case FieldLabel:
		target.Label = source.Label
	case FieldCatalogNumber:
		target.CatalogNumber = source.CatalogNumber
	case FieldBarcode:
		target.Barcode = source.Barcode
	case FieldISRC:
		target.ISRC = source.ISRC
	case FieldGrouping:
		target.Grouping = source.Grouping
	case FieldComment:
		target.Comment = source.Comment
	case FieldCopyright:
		target.Copyright = source.Copyright
	case FieldCredits:
		target.Credits = source.Clone().Credits
	case FieldGenres:
		target.Genres = append([]string(nil), source.Genres...)
	case FieldExternalIDs:
		target.ExternalIDs = append(ExternalIDs(nil), source.ExternalIDs...)
	case FieldBPM:
		target.BPM = source.BPM
	case FieldRating:
		if source.Rating == nil {
			target.Rating = nil
		} else {
			value := *source.Rating
			target.Rating = &value
		}
	case FieldCustomTags:
		target.CustomTags = append([]CustomTag(nil), source.CustomTags...)
	}
}

func fieldValueKey(document Document, field Field) string {
	var value any
	switch field {
	case FieldTitle:
		value = document.Title
	case FieldSubtitle:
		value = document.Subtitle
	case FieldAlbum:
		value = document.Album
	case FieldSortTitle:
		value = document.SortTitle
	case FieldSortAlbum:
		value = document.SortAlbum
	case FieldReleaseDate:
		value = document.ReleaseDate
	case FieldOriginalReleaseDate:
		value = document.OriginalReleaseDate
	case FieldTrackNumber:
		value = document.TrackNumber
	case FieldTrackTotal:
		value = document.TrackTotal
	case FieldDiscNumber:
		value = document.DiscNumber
	case FieldDiscTotal:
		value = document.DiscTotal
	case FieldCompilation:
		value = document.Compilation
	case FieldLabel:
		value = document.Label
	case FieldCatalogNumber:
		value = document.CatalogNumber
	case FieldBarcode:
		value = document.Barcode
	case FieldISRC:
		value = document.ISRC
	case FieldGrouping:
		value = document.Grouping
	case FieldComment:
		value = document.Comment
	case FieldCopyright:
		value = document.Copyright
	case FieldCredits:
		value = document.Credits
	case FieldGenres:
		value = document.Genres
	case FieldExternalIDs:
		value = document.ExternalIDs
	case FieldBPM:
		value = document.BPM
	case FieldRating:
		value = document.Rating
	case FieldCustomTags:
		value = document.CustomTags
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func NewFieldState(source Source, provider string, confidence float64, at time.Time) FieldState {
	return FieldState{Source: source, Provider: provider, Confidence: confidence, UpdatedAt: at.UTC()}
}
