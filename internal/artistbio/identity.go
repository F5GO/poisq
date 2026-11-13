package artistbio

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/search"
)

const (
	MaxIdentityNameBytes  = 2 << 10
	MaxIdentityAliases    = 20
	MaxIdentityAlbums     = 30
	MaxIdentityTracks     = 50
	MaxIdentityISRCs      = 100
	MaxIdentityCandidates = 8
	AutoResolveConfidence = 0.95
)

type IdentityRelease struct {
	Title string `json:"title"`
	Year  int    `json:"year,omitempty"`
}

type IdentityContext struct {
	ArtistID      string            `json:"artist_id"`
	Name          string            `json:"name"`
	MusicBrainzID string            `json:"musicbrainz_id,omitempty"`
	Aliases       []string          `json:"aliases,omitempty"`
	Albums        []IdentityRelease `json:"albums,omitempty"`
	Tracks        []string          `json:"tracks,omitempty"`
	ISRCs         []string          `json:"isrcs,omitempty"`
	Country       string            `json:"country,omitempty"`
}

type IdentityCandidate struct {
	ID               string            `json:"id"`
	Provider         Provider          `json:"provider"`
	Name             string            `json:"name"`
	MusicBrainzID    string            `json:"musicbrainz_id,omitempty"`
	ProviderArtistID string            `json:"provider_artist_id,omitempty"`
	Aliases          []string          `json:"aliases,omitempty"`
	Albums           []IdentityRelease `json:"albums,omitempty"`
	ISRCs            []string          `json:"isrcs,omitempty"`
	Country          string            `json:"country,omitempty"`
	ProviderScore    float64           `json:"provider_score,omitempty"`
}

type IdentityCandidateScore struct {
	Candidate          IdentityCandidate
	Confidence         float64
	Decisive           bool
	EvidenceCodes      []string
	ContradictionCodes []string
}

type IdentityResolution struct {
	Status             ResolutionStatus
	CandidateID        string
	Provider           Provider
	ProviderArtistID   string
	MusicBrainzID      string
	Confidence         float64
	EvidenceCodes      []string
	ContradictionCodes []string
	RequiresAI         bool
}

func BoundIdentityContext(input IdentityContext) (IdentityContext, error) {
	if err := validateRequired("artist id", input.ArtistID, MaxProviderIDBytes); err != nil {
		return IdentityContext{}, err
	}
	if err := validateRequired("artist name", input.Name, MaxIdentityNameBytes); err != nil {
		return IdentityContext{}, err
	}
	if err := validateOptional("musicbrainz id", input.MusicBrainzID, MaxProviderIDBytes); err != nil {
		return IdentityContext{}, err
	}
	output := input
	output.ArtistID = strings.TrimSpace(input.ArtistID)
	output.Name = strings.TrimSpace(metadata.NormalizeNFC(input.Name))
	output.MusicBrainzID = strings.TrimSpace(input.MusicBrainzID)
	output.Country = strings.ToUpper(strings.TrimSpace(input.Country))
	var err error
	if output.Aliases, err = boundStrings(input.Aliases, MaxIdentityAliases, MaxIdentityNameBytes, normalizeIdentityText); err != nil {
		return IdentityContext{}, err
	}
	if output.Tracks, err = boundStrings(input.Tracks, MaxIdentityTracks, MaxIdentityNameBytes, normalizeIdentityText); err != nil {
		return IdentityContext{}, err
	}
	output.ISRCs, err = boundStrings(input.ISRCs, MaxIdentityISRCs, 32, normalizeISRC)
	if err != nil {
		return IdentityContext{}, err
	}
	output.Albums, err = boundReleases(input.Albums, MaxIdentityAlbums)
	if err != nil {
		return IdentityContext{}, err
	}
	return output, nil
}

func ScoreIdentityCandidate(rawContext IdentityContext, rawCandidate IdentityCandidate) IdentityCandidateScore {
	context, err := BoundIdentityContext(rawContext)
	if err != nil {
		return IdentityCandidateScore{Candidate: rawCandidate, ContradictionCodes: []string{"input:invalid"}}
	}
	candidate, err := boundIdentityCandidate(rawCandidate)
	if err != nil {
		return IdentityCandidateScore{Candidate: rawCandidate, ContradictionCodes: []string{"candidate:invalid"}}
	}
	result := IdentityCandidateScore{Candidate: candidate}

	if context.MusicBrainzID != "" && candidate.MusicBrainzID != "" {
		if strings.EqualFold(context.MusicBrainzID, candidate.MusicBrainzID) {
			result.Confidence = 1
			result.Decisive = true
			result.EvidenceCodes = append(result.EvidenceCodes, "id:musicbrainz")
			return result
		}
		result.ContradictionCodes = append(result.ContradictionCodes, "id:musicbrainz")
	}

	nameMatch := anyNameEquivalent(append([]string{context.Name}, context.Aliases...), append([]string{candidate.Name}, candidate.Aliases...))
	if nameMatch {
		result.Confidence += 0.45
		result.EvidenceCodes = append(result.EvidenceCodes, "name:equivalent")
	}

	isrcMatches := overlapCount(context.ISRCs, candidate.ISRCs)
	if isrcMatches > 0 {
		result.Confidence = math.Max(result.Confidence, 0.99)
		result.Decisive = true
		result.EvidenceCodes = append(result.EvidenceCodes, "isrc:overlap")
	} else if len(context.ISRCs) > 0 && len(candidate.ISRCs) > 0 {
		result.ContradictionCodes = append(result.ContradictionCodes, "isrc:none")
	}

	albumMatches, yearVariants := releaseOverlap(context.Albums, candidate.Albums)
	if albumMatches > 0 {
		result.EvidenceCodes = append(result.EvidenceCodes, "album:overlap")
		result.Confidence += math.Min(0.35, float64(albumMatches)*0.20)
	}
	if yearVariants > 0 {
		// A MusicBrainz release-group stores the original release date while a
		// local tag commonly stores the date of a reissue. An exact title match
		// remains positive evidence; the year variant is useful provenance but
		// must not turn a real reissue into a different artist.
		result.EvidenceCodes = append(result.EvidenceCodes, "album:year_variant")
	}
	if albumMatches >= 2 {
		result.Confidence = math.Max(result.Confidence, 0.96)
		result.Decisive = true
		result.EvidenceCodes = append(result.EvidenceCodes, "album:multiple")
	}

	if context.Country != "" && candidate.Country != "" {
		if strings.EqualFold(context.Country, candidate.Country) {
			result.Confidence += 0.05
			result.EvidenceCodes = append(result.EvidenceCodes, "country:match")
		} else {
			result.ContradictionCodes = append(result.ContradictionCodes, "country:mismatch")
			result.Confidence -= 0.1
		}
	}
	if isFiniteUnit(candidate.ProviderScore) {
		result.Confidence += candidate.ProviderScore * 0.05
	}
	if containsString(result.ContradictionCodes, "id:musicbrainz") {
		result.Confidence = math.Min(result.Confidence, 0.3)
		result.Decisive = false
	}
	if containsString(result.ContradictionCodes, "isrc:none") {
		result.Confidence = math.Min(result.Confidence, 0.5)
		result.Decisive = false
	}
	result.Confidence = math.Max(0, math.Min(1, result.Confidence))
	if !result.Decisive || result.Confidence < AutoResolveConfidence {
		result.Decisive = false
	}
	return result
}

func ResolveIdentityDeterministically(context IdentityContext, candidates []IdentityCandidate) IdentityResolution {
	if len(candidates) == 0 {
		return IdentityResolution{Status: ResolutionUncertain}
	}
	if len(candidates) > MaxIdentityCandidates {
		candidates = candidates[:MaxIdentityCandidates]
	}
	scores := make([]IdentityCandidateScore, 0, len(candidates))
	for _, candidate := range candidates {
		scores = append(scores, ScoreIdentityCandidate(context, candidate))
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Confidence == scores[j].Confidence {
			return scores[i].Candidate.ID < scores[j].Candidate.ID
		}
		return scores[i].Confidence > scores[j].Confidence
	})
	top := scores[0]
	if top.Decisive && top.Confidence >= AutoResolveConfidence {
		if len(scores) > 1 && scores[1].Decisive && top.Confidence-scores[1].Confidence < 0.02 &&
			!sameStableIdentity(top.Candidate, scores[1].Candidate) {
			return IdentityResolution{Status: ResolutionUncertain, Confidence: top.Confidence, RequiresAI: true}
		}
		return resolutionFromScore(top, ResolutionResolved)
	}
	meaningful := 0
	for _, score := range scores {
		if identityScoreEligibleForAI(score) {
			meaningful++
		}
	}
	return IdentityResolution{
		Status: ResolutionUncertain, Confidence: top.Confidence,
		EvidenceCodes:      append([]string(nil), top.EvidenceCodes...),
		ContradictionCodes: append([]string(nil), top.ContradictionCodes...),
		RequiresAI:         meaningful >= 1,
	}
}

// identityScoreEligibleForAI permits the model to confirm a real catalog
// candidate only when deterministic evidence already ties both the artist name
// and at least one local release. Name-only homonyms never reach the model.
func identityScoreEligibleForAI(score IdentityCandidateScore) bool {
	return score.Confidence >= 0.60 && len(score.ContradictionCodes) == 0 &&
		containsString(score.EvidenceCodes, "name:equivalent") &&
		containsString(score.EvidenceCodes, "album:overlap")
}

func resolutionFromScore(score IdentityCandidateScore, status ResolutionStatus) IdentityResolution {
	return IdentityResolution{
		Status: status, CandidateID: score.Candidate.ID, Provider: score.Candidate.Provider,
		ProviderArtistID: score.Candidate.ProviderArtistID, MusicBrainzID: score.Candidate.MusicBrainzID,
		Confidence: score.Confidence, EvidenceCodes: append([]string(nil), score.EvidenceCodes...),
		ContradictionCodes: append([]string(nil), score.ContradictionCodes...),
	}
}

func boundIdentityCandidate(input IdentityCandidate) (IdentityCandidate, error) {
	if err := validateRequired("candidate id", input.ID, MaxProviderIDBytes); err != nil {
		return IdentityCandidate{}, err
	}
	if !input.Provider.valid() || input.Provider == ProviderManual {
		return IdentityCandidate{}, errInvalidProvider
	}
	if err := validateRequired("candidate name", input.Name, MaxIdentityNameBytes); err != nil {
		return IdentityCandidate{}, err
	}
	if !isFiniteUnit(input.ProviderScore) {
		return IdentityCandidate{}, errInvalidConfidence
	}
	output := input
	output.Aliases, _ = boundStrings(input.Aliases, MaxIdentityAliases, MaxIdentityNameBytes, normalizeIdentityText)
	output.ISRCs, _ = boundStrings(input.ISRCs, MaxIdentityISRCs, 32, normalizeISRC)
	output.Albums, _ = boundReleases(input.Albums, MaxIdentityAlbums)
	output.Country = strings.ToUpper(strings.TrimSpace(input.Country))
	return output, nil
}

var (
	errInvalidProvider   = errors.New("invalid identity provider")
	errInvalidConfidence = errors.New("invalid identity confidence")
)

type identityInputError struct{ message string }

func (e *identityInputError) Error() string { return e.message }

func boundStrings(values []string, limit, maxBytes int, normalizer func(string) string) ([]string, error) {
	output := make([]string, 0, min(len(values), limit))
	seen := make(map[string]struct{}, min(len(values), limit))
	for _, value := range values {
		if len(value) > maxBytes {
			return nil, &identityInputError{"identity value exceeds byte limit"}
		}
		normalized := normalizer(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		output = append(output, strings.TrimSpace(metadata.NormalizeNFC(value)))
		if len(output) == limit {
			break
		}
	}
	return output, nil
}

func boundReleases(values []IdentityRelease, limit int) ([]IdentityRelease, error) {
	output := make([]IdentityRelease, 0, min(len(values), limit))
	seen := make(map[string]struct{}, min(len(values), limit))
	for _, value := range values {
		if len(value.Title) > MaxIdentityNameBytes {
			return nil, &identityInputError{"release title exceeds byte limit"}
		}
		key := normalizeIdentityText(value.Title)
		if key == "" {
			continue
		}
		keyWithYear := key + "#" + strconv.Itoa(value.Year)
		if _, exists := seen[keyWithYear]; exists {
			continue
		}
		seen[keyWithYear] = struct{}{}
		output = append(output, IdentityRelease{Title: strings.TrimSpace(metadata.NormalizeNFC(value.Title)), Year: value.Year})
		if len(output) == limit {
			break
		}
	}
	return output, nil
}

func normalizeIdentityText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(metadata.NormalizeNFC(value)), " "))
}

func comparableIdentityText(value string) string {
	var output strings.Builder
	for _, r := range normalizeIdentityText(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
		}
	}
	return output.String()
}

func identityNamesEquivalent(left, right string) bool {
	l := comparableIdentityText(left)
	r := comparableIdentityText(right)
	if l == "" || r == "" {
		return false
	}
	if l == r {
		return true
	}
	return comparableIdentityText(search.TransliterateRuToEn(l)) == comparableIdentityText(search.TransliterateRuToEn(r))
}

func anyNameEquivalent(left, right []string) bool {
	for _, l := range left {
		for _, r := range right {
			if identityNamesEquivalent(l, r) {
				return true
			}
		}
	}
	return false
}

func normalizeISRC(value string) string {
	var output strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
		}
	}
	if output.Len() != 12 {
		return ""
	}
	return output.String()
}

func releaseOverlap(left, right []IdentityRelease) (matches, yearVariants int) {
	for _, l := range left {
		for _, r := range right {
			if normalizeIdentityText(l.Title) != normalizeIdentityText(r.Title) {
				continue
			}
			if l.Year > 0 && r.Year > 0 && abs(l.Year-r.Year) > 1 {
				yearVariants++
			}
			matches++
			break
		}
	}
	return matches, yearVariants
}

func overlapCount(left, right []string) int {
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		if normalized := normalizeISRC(value); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	count := 0
	for _, value := range right {
		if _, exists := set[normalizeISRC(value)]; exists {
			count++
		}
	}
	return count
}

func sameStableIdentity(left, right IdentityCandidate) bool {
	return left.MusicBrainzID != "" && strings.EqualFold(left.MusicBrainzID, right.MusicBrainzID)
}

func isFiniteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
