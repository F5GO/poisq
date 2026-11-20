package artistbio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCatalogOrigins      = 4
	maxCatalogExtractRunes = 1200
	maxCatalogEvidenceByte = 4 << 10
	minSufficientCatalog   = 1200
)

type EvidenceItem struct {
	ID          string   `json:"id"`
	Origin      string   `json:"origin"`
	Provider    Provider `json:"provider"`
	CandidateID int64    `json:"candidateId"`
	SourceURL   string   `json:"sourceUrl"`
	Language    string   `json:"language"`
	Text        string   `json:"text"`
	UpdatedUnix int64    `json:"updatedAt"`
}

type PreparedEvidence struct {
	Items      []EvidenceItem
	Version    string
	TotalRunes int
	Origins    int
	Sufficient bool
}

// PrepareCatalogEvidence compacts accepted source prose deterministically. It
// performs no network or model call and never includes manual/local/generated
// text in a future summary input.
func (s *Store) PrepareCatalogEvidence(ctx context.Context, artistID string) (PreparedEvidence, error) {
	candidates, err := s.Candidates(ctx, artistID, 64)
	if err != nil {
		return PreparedEvidence{}, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if candidateOriginRank(left.Provider) != candidateOriginRank(right.Provider) {
			return candidateOriginRank(left.Provider) > candidateOriginRank(right.Provider)
		}
		if left.MatchConfidence != right.MatchConfidence {
			return left.MatchConfidence > right.MatchConfidence
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID < right.ID
	})

	seenOrigin := make(map[Provider]struct{}, maxCatalogOrigins)
	seenSentence := make(map[string]struct{})
	items := make([]EvidenceItem, 0, maxCatalogOrigins)
	totalBytes := 0
	for _, candidate := range candidates {
		if len(items) >= maxCatalogOrigins || totalBytes >= maxCatalogEvidenceByte {
			break
		}
		if candidate.Status != CandidateAccepted || !eligibleSynthesisProvider(candidate.Provider) {
			continue
		}
		if _, exists := seenOrigin[candidate.Provider]; exists {
			continue
		}
		language := strings.TrimSpace(candidate.DetectedLanguage)
		languageConfidence := candidate.LanguageConfidence
		if language == "" {
			analysis := DetectBiographyLanguage(candidate.OriginalText)
			language = analysis.Language
			languageConfidence = analysis.Confidence
		}
		// Accepted candidates have already passed actual-language validation.
		// Reuse that result instead of reclassifying full source text here.
		if languageConfidence < 0.90 || language == "und" || language == "mixed" {
			continue
		}
		extract := compactBiographyEvidence(candidate.OriginalText, seenSentence, maxCatalogExtractRunes)
		remaining := maxCatalogEvidenceByte - totalBytes
		extract = truncateUTF8Bytes(extract, remaining)
		if utf8.RuneCountInString(extract) < minBiographyLetters {
			continue
		}
		item := EvidenceItem{
			ID: fmt.Sprintf("catalog:%s:%d", candidate.Provider, candidate.ID), Origin: string(candidate.Provider),
			Provider: candidate.Provider, CandidateID: candidate.ID, SourceURL: candidate.SourceURL,
			Language: language, Text: extract, UpdatedUnix: candidate.UpdatedAt.Unix(),
		}
		items = append(items, item)
		seenOrigin[candidate.Provider] = struct{}{}
		totalBytes += len(extract)
	}

	version, err := evidenceVersion(items)
	if err != nil {
		return PreparedEvidence{}, err
	}
	result := PreparedEvidence{Items: items, Version: version, Origins: len(items)}
	for _, item := range items {
		result.TotalRunes += utf8.RuneCountInString(item.Text)
	}
	result.Sufficient = result.Origins >= 2 && result.TotalRunes >= minSufficientCatalog
	return result, nil
}

func eligibleSynthesisProvider(provider Provider) bool {
	return provider.valid() && provider != ProviderManual && provider != ProviderLocalLibrary &&
		provider != ProviderAISummary && provider != ProviderNavidrome && provider != ProviderMusicBrainz
}

func candidateOriginRank(provider Provider) int {
	switch provider {
	case ProviderOfficial:
		return 8
	case ProviderYandexMusic:
		return 7
	case ProviderWikipedia:
		return 6
	case ProviderAppleMusic:
		return 5
	case ProviderDeezer:
		return 4
	case ProviderTheAudioDB:
		return 3
	case ProviderLastFM:
		return 2
	case ProviderDiscogs:
		return 1
	default:
		return 0
	}
}

func compactBiographyEvidence(text string, globalSeen map[string]struct{}, maxRunes int) string {
	sentences := splitBiographySentences(text)
	selected := make([]string, 0, len(sentences))
	used := 0
	for _, sentence := range sentences {
		normalized := normalizeEvidenceSentence(sentence)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, duplicate := globalSeen[key]; duplicate {
			continue
		}
		length := utf8.RuneCountInString(normalized)
		separator := 0
		if len(selected) > 0 {
			separator = 1
		}
		if used+separator+length > maxRunes {
			continue
		}
		globalSeen[key] = struct{}{}
		selected = append(selected, normalized)
		used += separator + length
	}
	return strings.Join(selected, " ")
}

// splitBiographySentences splits prose into sentences while keeping their
// terminal punctuation, so compacted evidence stays readable and facts from
// adjacent sentences are not run together. Decimal numbers, domains, initials,
// and common abbreviations (г., гг., т. е., feat.) do not end a sentence.
func splitBiographySentences(text string) []string {
	runes := []rune(text)
	sentences := make([]string, 0, 16)
	start := 0
	emit := func(end int) {
		if end > start {
			if sentence := strings.TrimSpace(string(runes[start:end])); sentence != "" {
				sentences = append(sentences, sentence)
			}
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' || r == '\r' {
			emit(i)
			start = i + 1
			continue
		}
		if r != '.' && r != '!' && r != '?' && r != '…' {
			continue
		}
		end := i + 1
		for end < len(runes) && strings.ContainsRune(".!?…»\"')]", runes[end]) {
			end++
		}
		if end < len(runes) && !unicode.IsSpace(runes[end]) {
			i = end - 1
			continue
		}
		if r == '.' && endsWithAbbreviation(runes[start:i]) {
			i = end - 1
			continue
		}
		next := end
		for next < len(runes) && (runes[next] == ' ' || runes[next] == '\t') {
			next++
		}
		if next < len(runes) && unicode.IsLower(runes[next]) {
			i = end - 1
			continue
		}
		emit(end)
		i = end - 1
	}
	emit(len(runes))
	return sentences
}

var biographyAbbreviations = map[string]struct{}{
	"г": {}, "гг": {}, "т": {}, "е": {}, "д": {}, "п": {}, "др": {}, "пр": {}, "см": {}, "им": {}, "ул": {},
	"св": {}, "род": {}, "ок": {}, "англ": {}, "рус": {}, "нем": {}, "фр": {}, "ср": {}, "напр": {},
	"тыс": {}, "млн": {}, "млрд": {}, "обл": {}, "пос": {}, "feat": {}, "ft": {}, "vs": {}, "mr": {},
	"mrs": {}, "ms": {}, "dr": {}, "st": {}, "jr": {}, "sr": {}, "vol": {}, "no": {}, "approx": {}, "ca": {},
	"янв": {}, "фев": {}, "февр": {}, "мар": {}, "апр": {}, "авг": {}, "сен": {}, "сент": {}, "окт": {},
	"ноя": {}, "нояб": {}, "дек": {}, "jan": {}, "feb": {}, "apr": {}, "aug": {}, "sep": {}, "sept": {},
	"oct": {}, "nov": {}, "dec": {},
}

func endsWithAbbreviation(segment []rune) bool {
	end := len(segment)
	begin := end
	for begin > 0 && unicode.IsLetter(segment[begin-1]) {
		begin--
	}
	if begin == end {
		return false
	}
	word := strings.ToLower(string(segment[begin:end]))
	if end-begin == 1 && unicode.IsUpper(segment[begin]) {
		return true // initial such as «А. С. Пушкин»
	}
	_, ok := biographyAbbreviations[word]
	return ok
}

func normalizeEvidenceSentence(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 20 {
		return ""
	}
	value = strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '-' || r == '—' || r == ';' || r == ','
	})
	if last, _ := utf8.DecodeLastRuneInString(value); !strings.ContainsRune(".!?…", last) {
		value += "."
	}
	return value
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func evidenceVersion(items []EvidenceItem) (string, error) {
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode catalog evidence version: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
