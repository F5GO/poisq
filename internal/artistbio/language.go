package artistbio

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

const minBiographyLetters = 80

var biographyURLOrEmail = regexp.MustCompile(`(?i)(?:https?://|www\.)\S+|[\pL\pN._%+\-]+@[\pL\pN.\-]+\.[\pL]{2,}`)

var russianFunctionWords = map[string]struct{}{
	"был": {}, "была": {}, "были": {}, "его": {}, "её": {}, "и": {}, "из": {}, "который": {},
	"которая": {}, "которые": {}, "на": {}, "несколько": {}, "он": {}, "она": {}, "с": {}, "свой": {},
	"также": {}, "что": {}, "это": {}, "этот": {}, "группа": {}, "музыканты": {}, "альбом": {},
}

var ukrainianFunctionWords = map[string]struct{}{
	"був": {}, "була": {}, "були": {}, "він": {}, "вона": {}, "його": {}, "її": {}, "кілька": {},
	"та": {}, "у": {}, "що": {}, "цей": {}, "ця": {}, "гурт": {}, "учасники": {}, "альбомів": {},
}

var belarusianFunctionWords = map[string]struct{}{
	"быў": {}, "была": {}, "былі": {}, "ён": {}, "яго": {}, "яе": {}, "некалькі": {}, "што": {},
	"гэты": {}, "гэтая": {}, "гурт": {}, "ўдзельнікі": {}, "альбомаў": {}, "беларусі": {},
}

// LanguageAnalysis is a bounded, deterministic description of biography text.
// Provider-declared language is intentionally not an input.
type LanguageAnalysis struct {
	Language             string
	Confidence           float64
	RussianShare         float64
	MeaningfulCharacters int
	EvidenceCodes        []string
	RequiresAI           bool
	QualityOK            bool
}

// DetectBiographyLanguage detects actual text language and rejects unsafe or
// low-quality input before any model call. It never rewrites the input.
func DetectBiographyLanguage(text string) LanguageAnalysis {
	result := LanguageAnalysis{Language: "und", EvidenceCodes: []string{"lang:und"}}
	if len(text) == 0 || len(text) > MaxBiographyBytes {
		result.EvidenceCodes = []string{"quality:size"}
		return result
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || containsUnsafeBiographyMarkup(trimmed) {
		result.EvidenceCodes = []string{"quality:markup"}
		return result
	}
	if matches := biographyURLOrEmail.FindAllStringIndex(trimmed, 9); len(matches) > 8 {
		result.EvidenceCodes = []string{"quality:links"}
		return result
	}

	cleaned := biographyURLOrEmail.ReplaceAllString(trimmed, " ")
	words := biographyWords(cleaned)
	if repeatedBiography(words) {
		result.EvidenceCodes = []string{"quality:repetition"}
		return result
	}

	var letters, cyrillic, latin, russianSpecific, ukrainianSpecific, belarusianSpecific int
	for _, r := range cleaned {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		switch {
		case unicode.In(r, unicode.Cyrillic):
			cyrillic++
		case unicode.In(r, unicode.Latin):
			latin++
		}
		switch unicode.ToLower(r) {
		case 'ы', 'э', 'ё', 'ъ':
			russianSpecific++
		case 'ї', 'є', 'ґ':
			ukrainianSpecific++
		case 'ў':
			belarusianSpecific++
		case 'і':
			// Shared by Ukrainian and Belarusian; word evidence decides.
			ukrainianSpecific++
			belarusianSpecific++
		}
	}
	result.MeaningfulCharacters = letters
	if letters == 0 {
		return result
	}
	cyrShare := float64(cyrillic) / float64(letters)
	latinShare := float64(latin) / float64(letters)
	result.RussianShare = clamp01(float64(cyrillic-ukrainianSpecific-belarusianSpecific) / float64(letters))
	ruWords := countLanguageWords(words, russianFunctionWords)
	ukWords := countLanguageWords(words, ukrainianFunctionWords)
	beWords := countLanguageWords(words, belarusianFunctionWords)

	if letters < minBiographyLetters {
		result.EvidenceCodes = []string{"quality:short"}
		return result
	}
	result.QualityOK = true

	switch {
	case cyrShare >= 0.20 && latinShare >= 0.20:
		result.Language = "mixed"
		result.Confidence = clamp01(math.Max(cyrShare, latinShare))
		result.RequiresAI = true
		result.EvidenceCodes = []string{"script:mixed"}
	case ukrainianSpecific > 0 && (ukWords > beWords || strings.Contains(strings.ToLower(cleaned), "ї")):
		result.Language = "uk"
		result.Confidence = clamp01(0.90 + 0.01*float64(minInt(ukrainianSpecific+ukWords, 9)))
		result.EvidenceCodes = []string{"lang:uk", "letters:uk"}
	case belarusianSpecific > 0 && beWords >= ukWords:
		result.Language = "be"
		result.Confidence = clamp01(0.90 + 0.01*float64(minInt(belarusianSpecific+beWords, 9)))
		result.EvidenceCodes = []string{"lang:be", "letters:be"}
	case cyrShare >= 0.80 && (ruWords >= 2 || russianSpecific > 0):
		result.Language = "ru"
		result.Confidence = clamp01(0.90 + 0.01*float64(minInt(ruWords+russianSpecific, 9)))
		result.RussianShare = cyrShare
		result.EvidenceCodes = []string{"lang:ru", "script:cyrillic"}
	case latinShare >= 0.80:
		result.Language = "en"
		result.Confidence = 0.90
		result.EvidenceCodes = []string{"lang:en", "script:latin"}
	default:
		result.Language = "und"
		result.Confidence = clamp01(math.Max(cyrShare, latinShare))
		result.RequiresAI = true
		result.EvidenceCodes = []string{"lang:ambiguous"}
	}
	return result
}

func containsUnsafeBiographyMarkup(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "<iframe") || strings.Contains(lower, "<object") || strings.Contains(lower, "<style") {
		return true
	}
	return strings.Count(text, "<") > 8 || strings.Count(text, ">") > 8
}

func biographyWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
}

func countLanguageWords(words []string, vocabulary map[string]struct{}) int {
	count := 0
	for _, word := range words {
		if _, ok := vocabulary[word]; ok {
			count++
		}
	}
	return count
}

func repeatedBiography(words []string) bool {
	if len(words) < 16 {
		return false
	}
	unique := make(map[string]struct{}, len(words))
	for _, word := range words {
		unique[word] = struct{}{}
	}
	return len(unique)*4 < len(words)
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validBiographyLanguage(language string) bool {
	switch language {
	case "ru", "uk", "be", "en", "mixed", "und":
		return true
	default:
		return false
	}
}
