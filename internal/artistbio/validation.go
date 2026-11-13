package artistbio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/ai"
)

const maxValidationCandidates = 64

type biographyAIAPI interface {
	ClassifyLanguage(ctx context.Context, text string, allowedEvidence []string) (ai.BiographyLanguageResult, error)
	Translate(ctx context.Context, source string) (ai.BiographyTranslation, error)
	VerifyGrounding(ctx context.Context, translation ai.BiographyTranslation) (ai.BiographyGroundingResult, error)
}

type ValidationReport struct {
	Scanned    int
	Accepted   int
	Translated int
	Rejected   int
	Retryable  int
	Skipped    int
}

type BiographyValidator struct {
	store  *Store
	ai     biographyAIAPI
	global chan struct{}

	mu       sync.Mutex
	inflight map[string]*validationCall
}

type validationCall struct {
	done   chan struct{}
	report ValidationReport
	err    error
}

func NewBiographyValidator(store *Store, model biographyAIAPI, globalConcurrency int) (*BiographyValidator, error) {
	if store == nil || model == nil {
		return nil, fmt.Errorf("biography validator requires store and ai service")
	}
	if globalConcurrency < 1 {
		globalConcurrency = 4
	}
	if globalConcurrency > 16 {
		globalConcurrency = 16
	}
	return &BiographyValidator{
		store: store, ai: model, global: make(chan struct{}, globalConcurrency),
		inflight: make(map[string]*validationCall),
	}, nil
}

func (v *BiographyValidator) ValidateArtist(ctx context.Context, artistID string) (ValidationReport, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return ValidationReport{}, err
	}
	v.mu.Lock()
	if call := v.inflight[artistID]; call != nil {
		v.mu.Unlock()
		select {
		case <-call.done:
			return call.report, call.err
		case <-ctx.Done():
			return ValidationReport{}, ctx.Err()
		}
	}
	call := &validationCall{done: make(chan struct{})}
	v.inflight[artistID] = call
	v.mu.Unlock()

	call.report, call.err = v.validateOnce(ctx, artistID)
	v.mu.Lock()
	delete(v.inflight, artistID)
	close(call.done)
	v.mu.Unlock()
	return call.report, call.err
}

func (v *BiographyValidator) validateOnce(ctx context.Context, artistID string) (ValidationReport, error) {
	if err := acquireSlot(ctx, v.global); err != nil {
		return ValidationReport{}, err
	}
	defer releaseSlot(v.global)
	candidates, err := v.store.Candidates(ctx, artistID, maxValidationCandidates)
	if err != nil {
		return ValidationReport{}, err
	}
	var report ValidationReport
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		if candidate.Status == CandidateAccepted || candidate.Status == CandidateRejected {
			report.Skipped++
			continue
		}
		report.Scanned++
		outcome, err := v.validateCandidate(ctx, candidate)
		if err != nil {
			// A user-triggered cache reset may delete a fetched candidate while
			// its bounded AI translation is in flight. This is a benign race: the
			// next background pass rebuilds it from the source.
			if errors.Is(err, ErrCandidateNotFound) {
				report.Skipped++
				continue
			}
			return report, err
		}
		switch outcome {
		case CandidateAccepted:
			report.Accepted++
			updated, _, readErr := v.store.Candidate(ctx, candidate.ArtistID, candidate.Provider, candidate.SourceArtistID)
			if readErr == nil && updated.RussianText != "" {
				report.Translated++
			}
		case CandidateRejected:
			report.Rejected++
		case CandidateTranslationPending:
			report.Retryable++
		}
	}
	return report, nil
}

func (v *BiographyValidator) validateCandidate(ctx context.Context, candidate Candidate) (CandidateStatus, error) {
	analysis := DetectBiographyLanguage(candidate.OriginalText)
	if !analysis.QualityOK {
		return CandidateRejected, v.persistValidation(ctx, candidate, analysis, "", CandidateRejected, "validation:quality_rejected")
	}
	language := analysis.Language
	languageConfidence := analysis.Confidence
	if analysis.RequiresAI {
		classified, err := v.ai.ClassifyLanguage(ctx, candidate.OriginalText, analysis.EvidenceCodes)
		if err != nil {
			return CandidateTranslationPending, v.persistValidation(ctx, candidate, analysis, "", CandidateTranslationPending, "validation:ai_retry")
		}
		language = classified.Language
		languageConfidence = classified.Confidence
		if languageConfidence < 0.90 || language == "und" {
			analysis.Language = language
			analysis.Confidence = languageConfidence
			return CandidateRejected, v.persistValidation(ctx, candidate, analysis, "", CandidateRejected, "validation:language_uncertain")
		}
	}
	analysis.Language = language
	analysis.Confidence = languageConfidence
	if language == "ru" && languageConfidence >= 0.90 && analysis.RussianShare >= 0.80 {
		return CandidateAccepted, v.persistValidation(ctx, candidate, analysis, "", CandidateAccepted, "validation:direct_ru")
	}
	if language == "und" {
		return CandidateRejected, v.persistValidation(ctx, candidate, analysis, "", CandidateRejected, "validation:language_uncertain")
	}
	translation, err := v.ai.Translate(ctx, candidate.OriginalText)
	if err != nil {
		return CandidateTranslationPending, v.persistValidation(ctx, candidate, analysis, "", CandidateTranslationPending, "validation:ai_retry")
	}
	translatedAnalysis := DetectBiographyLanguage(translation.Text)
	if !translatedAnalysis.QualityOK || translatedAnalysis.Language != "ru" || translatedAnalysis.Confidence < 0.90 || translatedAnalysis.RussianShare < 0.80 {
		return CandidateRejected, v.persistValidation(ctx, candidate, analysis, "", CandidateRejected, "validation:translation_not_ru")
	}
	grounding, err := v.ai.VerifyGrounding(ctx, translation)
	if err != nil {
		return CandidateTranslationPending, v.persistValidation(ctx, candidate, analysis, "", CandidateTranslationPending, "validation:ai_retry")
	}
	if !grounding.Grounded || grounding.Confidence < 0.95 {
		return CandidateRejected, v.persistValidation(ctx, candidate, analysis, "", CandidateRejected, "validation:translation_not_grounded")
	}
	analysis.RussianShare = translatedAnalysis.RussianShare
	analysis.Confidence = minFloat(languageConfidence, grounding.Confidence)
	return CandidateAccepted, v.persistValidation(ctx, candidate, analysis, translation.Text, CandidateAccepted, "validation:translated_grounded")
}

func (v *BiographyValidator) persistValidation(ctx context.Context, candidate Candidate, analysis LanguageAnalysis, russianText string, status CandidateStatus, code string) error {
	evidence, err := addValidationEvidence(candidate.EvidenceJSON, code)
	if err != nil {
		return err
	}
	return v.store.ApplyValidationAndSelect(ctx, CandidateValidation{
		ArtistID: candidate.ArtistID, CandidateID: candidate.ID, RussianText: russianText,
		DetectedLanguage: analysis.Language, LanguageConfidence: analysis.Confidence,
		RussianShare: analysis.RussianShare, Status: status, EvidenceJSON: evidence, UpdatedAt: time.Now(),
	})
}

func addValidationEvidence(existing, code string) (string, error) {
	if strings.TrimSpace(code) == "" || len(code) > 64 {
		return "", fmt.Errorf("invalid validation evidence")
	}
	fields := make(map[string]json.RawMessage)
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &fields); err != nil {
			return "", fmt.Errorf("invalid stored biography evidence")
		}
	}
	validation, _ := json.Marshal([]string{code})
	fields["validation"] = validation
	encoded, err := json.Marshal(fields)
	if err != nil || len(encoded) > MaxEvidenceBytes {
		return "", fmt.Errorf("invalid validation evidence")
	}
	return string(encoded), nil
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}
