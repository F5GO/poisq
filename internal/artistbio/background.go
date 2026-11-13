package artistbio

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/logger"
)

const (
	defaultBiographyBatchSize = 25
	defaultBiographyFreshTTL  = 30 * 24 * time.Hour
	defaultValidationRetryTTL = time.Hour
	defaultIdentityRetryTTL   = 5 * time.Minute
)

type identityResolverAPI interface {
	Resolve(ctx context.Context, input IdentityContext) (IdentityResolution, error)
}

type ingestionAPI interface {
	Ingest(ctx context.Context, artist ResolvedArtist) (IngestionReport, error)
}

type validationAPI interface {
	ValidateArtist(ctx context.Context, artistID string) (ValidationReport, error)
}

type BackgroundProcessor struct {
	store            *Store
	local            LocalIdentityContextSource
	resolver         identityResolverAPI
	ingestion        ingestionAPI
	validator        validationAPI
	batchSize        int
	freshTTL         time.Duration
	retryTTL         time.Duration
	identityRetryTTL time.Duration
}

type BackgroundReport struct {
	Scanned   int
	Skipped   int
	Resolved  int
	Ingested  int
	Validated int
	Uncertain int
	Empty     int
	Failed    int
}

func NewBackgroundProcessor(store *Store, local LocalIdentityContextSource, resolver identityResolverAPI, ingestion ingestionAPI, batchSize int) (*BackgroundProcessor, error) {
	if store == nil || resolver == nil || ingestion == nil {
		return nil, fmt.Errorf("biography background processor requires store, resolver, and ingestion")
	}
	if batchSize < 1 {
		batchSize = defaultBiographyBatchSize
	}
	if batchSize > 100 {
		batchSize = 100
	}
	return &BackgroundProcessor{
		store: store, local: local, resolver: resolver, ingestion: ingestion,
		batchSize: batchSize, freshTTL: defaultBiographyFreshTTL, retryTTL: defaultValidationRetryTTL,
		identityRetryTTL: defaultIdentityRetryTTL,
	}, nil
}

// SetValidator attaches validation to the existing background lifecycle.
// Construction performs no model or provider request.
func (p *BackgroundProcessor) SetValidator(validator validationAPI) {
	p.validator = validator
}

// ProcessBatch scans inventory locally and performs external work for at most batchSize stale artists.
func (p *BackgroundProcessor) ProcessBatch(ctx context.Context, navDB *sql.DB) (BackgroundReport, error) {
	var report BackgroundReport
	cursor := ""
	processed := 0
	for processed < p.batchSize {
		artists, err := db.LoadArtistInventoryPage(ctx, navDB, cursor, db.MaxArtistInventoryPage)
		if err != nil {
			return report, err
		}
		if len(artists) == 0 {
			break
		}
		for _, artist := range artists {
			if processed >= p.batchSize {
				break
			}
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			cursor = artist.ID
			report.Scanned++
			if p.validator != nil {
				ready, err := p.validationReady(ctx, artist.ID)
				if err != nil {
					report.Failed++
					continue
				}
				if ready {
					processed++
					validationReport, err := p.validator.ValidateArtist(ctx, artist.ID)
					if err != nil {
						if ctx.Err() != nil {
							return report, ctx.Err()
						}
						report.Failed++
						logger.Debug("[BIO] artist_id=%q validation_error=%v", artist.ID, err)
					} else {
						report.Validated++
						logger.Debug("[BIO] artist_id=%q validation accepted=%d translated=%d rejected=%d retry=%d", artist.ID,
							validationReport.Accepted, validationReport.Translated, validationReport.Rejected, validationReport.Retryable)
					}
					if processed >= p.batchSize {
						break
					}
					continue
				}
			}
			fresh, err := p.hasFreshState(ctx, artist.ID)
			if err != nil {
				report.Failed++
				continue
			}
			if fresh {
				report.Skipped++
				continue
			}
			processed++
			identityContext := p.localContext(ctx, artist)
			resolution, err := p.resolver.Resolve(ctx, identityContext)
			if err != nil {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				report.Failed++
				logger.Debug("[BIO] artist_id=%q identity_error=%v", artist.ID, err)
				continue
			}
			if err := p.ensureLocalLibraryFallback(ctx, identityContext, resolution); err != nil {
				report.Failed++
				logger.Debug("[BIO] artist_id=%q local_fallback_error=%v", artist.ID, err)
			}
			if resolution.Status != ResolutionResolved {
				report.Uncertain++
				logger.Debug("[BIO] artist_id=%q artist=%q identity=uncertain confidence=%.2f ai_required=%t evidence=%v contradictions=%v",
					artist.ID, artist.Name, resolution.Confidence, resolution.RequiresAI, resolution.EvidenceCodes, resolution.ContradictionCodes)
				continue
			}
			report.Resolved++
			identityContext.MusicBrainzID = resolution.MusicBrainzID
			ingestionReport, err := p.ingestion.Ingest(ctx, ResolvedArtist{
				Context:  identityContext,
				Identity: resolution,
			})
			if err != nil {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				report.Failed++
				continue
			}
			report.Ingested += ingestionReport.Stored
			report.Empty += ingestionReport.Empty
			report.Failed += ingestionReport.Failed
			logger.Debug("[BIO] artist_id=%q artist=%q identity=resolved confidence=%.2f candidates=%d empty_sources=%d source_errors=%d", artist.ID,
				artist.Name, resolution.Confidence, ingestionReport.Stored, ingestionReport.Empty, ingestionReport.Failed)
			if ingestionReport.Stored == 0 {
				continue
			}
			if p.validator != nil {
				validationReport, err := p.validator.ValidateArtist(ctx, artist.ID)
				if err != nil {
					if ctx.Err() != nil {
						return report, ctx.Err()
					}
					report.Failed++
					logger.Debug("[BIO] artist_id=%q validation_error=%v", artist.ID, err)
				} else {
					report.Validated++
					logger.Debug("[BIO] artist_id=%q validation accepted=%d translated=%d rejected=%d retry=%d", artist.ID,
						validationReport.Accepted, validationReport.Translated, validationReport.Rejected, validationReport.Retryable)
				}
			}
			if processed >= p.batchSize {
				break
			}
		}
		if len(artists) < db.MaxArtistInventoryPage {
			break
		}
	}
	return report, nil
}

// ensureLocalLibraryFallback guarantees a truthful Russian description even
// for artists absent from every external catalog. The text states only what is
// present in local metadata and has the lowest provider priority, so any real
// accepted biography replaces it automatically.
func (p *BackgroundProcessor) ensureLocalLibraryFallback(ctx context.Context, artist IdentityContext, resolution IdentityResolution) error {
	if _, found, err := p.store.Identity(ctx, artist.ArtistID); err != nil {
		return err
	} else if !found {
		identityEvidence, err := json.Marshal(struct {
			Evidence       []string `json:"evidence"`
			Contradictions []string `json:"contradictions,omitempty"`
		}{resolution.EvidenceCodes, resolution.ContradictionCodes})
		if err != nil {
			return fmt.Errorf("encode local fallback identity: %w", err)
		}
		if err := p.store.PutIdentity(ctx, ArtistIdentity{
			ArtistID: artist.ArtistID, ArtistName: artist.Name, MusicBrainzID: resolution.MusicBrainzID,
			Status: resolution.Status, ResolvedSource: resolution.Provider, ResolvedArtistID: resolution.ProviderArtistID,
			Confidence: resolution.Confidence, EvidenceJSON: string(identityEvidence), UpdatedAt: time.Now(),
		}); err != nil {
			return err
		}
	}
	text := localLibraryBiography(artist)
	analysis := DetectBiographyLanguage(text)
	if !analysis.QualityOK || analysis.Language != "ru" || analysis.Confidence < 0.90 || analysis.RussianShare < 0.80 {
		return fmt.Errorf("generated local library description failed Russian validation")
	}
	evidence, err := json.Marshal(struct {
		Evidence []string `json:"evidence"`
	}{Evidence: []string{"library:artist_id", "library:metadata"}})
	if err != nil {
		return fmt.Errorf("encode local library evidence: %w", err)
	}
	now := time.Now()
	candidateID, err := p.store.PutCandidate(ctx, Candidate{
		ArtistID: artist.ArtistID, Provider: ProviderLocalLibrary, SourceArtistID: artist.ArtistID,
		OriginalText: text, OriginalLanguage: "ru", MatchConfidence: 1,
		Status: CandidateFetched, EvidenceJSON: string(evidence), FetchedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	validatedEvidence, err := addValidationEvidence(string(evidence), "validation:deterministic_ru")
	if err != nil {
		return err
	}
	return p.store.ApplyValidationAndSelect(ctx, CandidateValidation{
		ArtistID: artist.ArtistID, CandidateID: candidateID, DetectedLanguage: "ru",
		LanguageConfidence: analysis.Confidence, RussianShare: analysis.RussianShare,
		Status: CandidateAccepted, EvidenceJSON: validatedEvidence, UpdatedAt: now,
	})
}

func localLibraryBiography(artist IdentityContext) string {
	name := strings.TrimSpace(artist.Name)
	var text strings.Builder
	text.WriteString("По метаданным медиатеки, имя «")
	text.WriteString(name)
	text.WriteString("» указано как исполнитель сохранённых аудиозаписей. Эта справка не содержит внешних биографических сведений и перечисляет только данные музыкальной коллекции.")
	if len(artist.Albums) > 0 {
		text.WriteString(" Сохранённые релизы: ")
		limit := min(4, len(artist.Albums))
		for index := 0; index < limit; index++ {
			if index > 0 {
				text.WriteString(", ")
			}
			text.WriteString("«")
			text.WriteString(strings.TrimSpace(artist.Albums[index].Title))
			text.WriteString("»")
		}
		text.WriteString(".")
	}
	if len(artist.Tracks) > 0 {
		text.WriteString(" Среди сохранённых композиций: ")
		limit := min(5, len(artist.Tracks))
		for index := 0; index < limit; index++ {
			if index > 0 {
				text.WriteString(", ")
			}
			text.WriteString("«")
			text.WriteString(strings.TrimSpace(artist.Tracks[index]))
			text.WriteString("»")
		}
		text.WriteString(".")
	}
	return text.String()
}

func (p *BackgroundProcessor) localContext(ctx context.Context, artist db.LibraryArtist) IdentityContext {
	result := IdentityContext{ArtistID: artist.ID, Name: artist.Name}
	if p.local == nil {
		return result
	}
	local, err := p.local.ArtistIdentityContext(ctx, artist.Name, MaxIdentityAlbums, MaxIdentityTracks)
	if err != nil {
		return result
	}
	for _, album := range local.Albums {
		result.Albums = append(result.Albums, IdentityRelease{Title: album.Title, Year: album.Year})
	}
	result.Tracks = append(result.Tracks, local.Tracks...)
	bounded, err := BoundIdentityContext(result)
	if err != nil {
		return IdentityContext{ArtistID: artist.ID, Name: artist.Name}
	}
	return bounded
}

func (p *BackgroundProcessor) hasFreshState(ctx context.Context, artistID string) (bool, error) {
	cutoff := time.Now().Add(-p.freshTTL).Unix()
	if p.validator == nil {
		identityRetryCutoff := time.Now().Add(-p.identityRetryTTL).Unix()
		var exists int
		err := p.store.db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM artist_bio_candidates WHERE artist_id = ? AND provider != 'local_library' AND updated_at >= ?
			UNION ALL
			SELECT 1 FROM artist_bio_identity WHERE artist_id = ? AND status IN ('uncertain', 'resolved') AND updated_at >= ?
		)`, artistID, cutoff, artistID, identityRetryCutoff).Scan(&exists)
		if err != nil {
			return false, fmt.Errorf("read biography refresh state: %w", err)
		}
		return exists == 1, nil
	}
	retryCutoff := time.Now().Add(-p.retryTTL).Unix()
	identityRetryCutoff := time.Now().Add(-p.identityRetryTTL).Unix()
	var exists int
	err := p.store.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artist_bio_candidates
			WHERE artist_id = ? AND provider != 'local_library' AND status = 'accepted' AND updated_at >= ?
		UNION ALL
		SELECT 1 FROM artist_bio_candidates
			WHERE artist_id = ? AND status = 'translation_pending' AND updated_at >= ?
		UNION ALL
		SELECT 1 FROM artist_bio_identity WHERE artist_id = ? AND status IN ('uncertain', 'resolved') AND updated_at >= ?
	)`, artistID, cutoff, artistID, retryCutoff, artistID, identityRetryCutoff).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read biography refresh state: %w", err)
	}
	return exists == 1, nil
}

func (p *BackgroundProcessor) validationReady(ctx context.Context, artistID string) (bool, error) {
	retryCutoff := time.Now().Add(-p.retryTTL).Unix()
	var exists int
	err := p.store.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artist_bio_candidates
		WHERE artist_id = ? AND (
			status = 'fetched' OR (status = 'translation_pending' AND updated_at <= ?)
		)
	)`, artistID, retryCutoff).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read biography validation state: %w", err)
	}
	return exists == 1, nil
}
