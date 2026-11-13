package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
)

const defaultIdentityCacheTTL = 30 * 24 * time.Hour

type IdentityCandidateSource interface {
	FindIdentityCandidates(ctx context.Context, input IdentityContext) ([]IdentityCandidate, error)
}

type IdentityArbiter interface {
	ArbitrateIdentity(ctx context.Context, input ai.IdentityArbitrationInput) (ai.IdentityArbitrationDecision, error)
}

type LocalIdentityContextSource interface {
	ArtistIdentityContext(ctx context.Context, artistName string, albumLimit, trackLimit int) (db.ArtistIdentityContext, error)
}

type IdentityResolver struct {
	store    *Store
	local    LocalIdentityContextSource
	source   IdentityCandidateSource
	arbiter  IdentityArbiter
	cacheTTL time.Duration

	mu       sync.Mutex
	inflight map[string]*identityResolveCall
}

type identityResolveCall struct {
	done   chan struct{}
	result IdentityResolution
	err    error
}

func NewIdentityResolver(store *Store, local LocalIdentityContextSource, source IdentityCandidateSource, arbiter IdentityArbiter) *IdentityResolver {
	return &IdentityResolver{
		store: store, local: local, source: source, arbiter: arbiter,
		cacheTTL: defaultIdentityCacheTTL, inflight: make(map[string]*identityResolveCall),
	}
}

func (r *IdentityResolver) Resolve(ctx context.Context, input IdentityContext) (IdentityResolution, error) {
	bounded, err := BoundIdentityContext(input)
	if err != nil {
		return IdentityResolution{}, err
	}
	if cached, ok, err := r.cached(ctx, bounded.ArtistID); err != nil {
		return IdentityResolution{}, err
	} else if ok {
		return cached, nil
	}

	r.mu.Lock()
	if call := r.inflight[bounded.ArtistID]; call != nil {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return IdentityResolution{}, ctx.Err()
		}
	}
	call := &identityResolveCall{done: make(chan struct{})}
	r.inflight[bounded.ArtistID] = call
	r.mu.Unlock()

	call.result, call.err = r.resolveOnce(ctx, bounded)
	r.mu.Lock()
	delete(r.inflight, bounded.ArtistID)
	close(call.done)
	r.mu.Unlock()
	return call.result, call.err
}

func (r *IdentityResolver) resolveOnce(ctx context.Context, input IdentityContext) (IdentityResolution, error) {
	input = r.mergeLocalContext(ctx, input)
	if ctx.Err() != nil {
		return IdentityResolution{}, ctx.Err()
	}
	if r.source == nil {
		result := IdentityResolution{Status: ResolutionUncertain}
		return result, r.persist(ctx, input, result)
	}
	candidates, err := r.source.FindIdentityCandidates(ctx, input)
	if ctx.Err() != nil {
		return IdentityResolution{}, ctx.Err()
	}
	if err != nil {
		result := IdentityResolution{Status: ResolutionUncertain}
		return result, r.persist(ctx, input, result)
	}
	if len(candidates) > MaxIdentityCandidates {
		candidates = candidates[:MaxIdentityCandidates]
	}
	result := ResolveIdentityDeterministically(input, candidates)
	if result.Status != ResolutionResolved && result.RequiresAI && r.arbiter != nil {
		if decided, ok := r.arbitrate(ctx, input, candidates); ok {
			result = decided
		}
	}
	if err := r.persist(ctx, input, result); err != nil {
		return IdentityResolution{}, err
	}
	return result, nil
}

func (r *IdentityResolver) mergeLocalContext(ctx context.Context, input IdentityContext) IdentityContext {
	if r.local == nil || (len(input.Albums) >= MaxIdentityAlbums && len(input.Tracks) >= MaxIdentityTracks) {
		return input
	}
	local, err := r.local.ArtistIdentityContext(ctx, input.Name, MaxIdentityAlbums, MaxIdentityTracks)
	if err != nil {
		return input
	}
	for _, album := range local.Albums {
		input.Albums = append(input.Albums, IdentityRelease{Title: album.Title, Year: album.Year})
	}
	input.Tracks = append(input.Tracks, local.Tracks...)
	bounded, err := BoundIdentityContext(input)
	if err != nil {
		return input
	}
	return bounded
}

func (r *IdentityResolver) arbitrate(ctx context.Context, input IdentityContext, candidates []IdentityCandidate) (IdentityResolution, bool) {
	scores := make(map[string]IdentityCandidateScore, len(candidates))
	request := ai.IdentityArbitrationInput{Artist: ai.IdentityArbitrationArtist{
		Name: input.Name, Aliases: input.Aliases, Tracks: input.Tracks, Country: input.Country,
	}}
	for _, album := range input.Albums {
		request.Artist.Albums = append(request.Artist.Albums, album.Title)
	}
	for _, candidate := range candidates {
		score := ScoreIdentityCandidate(input, candidate)
		scores[candidate.ID] = score
		eligible := score.Decisive && score.Confidence >= AutoResolveConfidence || identityScoreEligibleForAI(score)
		option := ai.IdentityArbitrationCandidate{
			ID: candidate.ID, Name: candidate.Name, Aliases: candidate.Aliases, Country: candidate.Country,
			DeterministicConfidence: score.Confidence,
			Eligible:                eligible,
			EvidenceCodes:           append([]string(nil), score.EvidenceCodes...),
			ContradictionCodes:      append([]string(nil), score.ContradictionCodes...),
		}
		for _, album := range candidate.Albums {
			option.Albums = append(option.Albums, album.Title)
		}
		request.Candidates = append(request.Candidates, option)
	}
	decision, err := r.arbiter.ArbitrateIdentity(ctx, request)
	if err != nil || decision.Decision != "same_artist" || decision.Confidence < AutoResolveConfidence {
		return IdentityResolution{}, false
	}
	score, exists := scores[decision.CandidateID]
	if !exists || !(score.Decisive && score.Confidence >= AutoResolveConfidence || identityScoreEligibleForAI(score)) ||
		!allStringsAllowed(decision.EvidenceCodes, score.EvidenceCodes) ||
		(!score.Decisive && identityScoreEligibleForAI(score) && (!containsString(decision.EvidenceCodes, "name:equivalent") ||
			!containsString(decision.EvidenceCodes, "album:overlap"))) {
		return IdentityResolution{}, false
	}
	result := resolutionFromScore(score, ResolutionResolved)
	if decision.Confidence < result.Confidence {
		result.Confidence = decision.Confidence
	}
	result.EvidenceCodes = append(result.EvidenceCodes, "ai:confirmed")
	result.RequiresAI = false
	return result, true
}

func (r *IdentityResolver) persist(ctx context.Context, input IdentityContext, result IdentityResolution) error {
	if r.store == nil {
		return fmt.Errorf("identity resolver requires store")
	}
	evidence, err := json.Marshal(struct {
		Evidence       []string `json:"evidence"`
		Contradictions []string `json:"contradictions,omitempty"`
	}{result.EvidenceCodes, result.ContradictionCodes})
	if err != nil {
		return fmt.Errorf("encode identity evidence: %w", err)
	}
	resolvedArtistID := result.ProviderArtistID
	if resolvedArtistID == "" && result.Status == ResolutionResolved {
		resolvedArtistID = result.CandidateID
	}
	return r.store.PutIdentity(ctx, ArtistIdentity{
		ArtistID: input.ArtistID, ArtistName: input.Name, MusicBrainzID: result.MusicBrainzID,
		Status: result.Status, ResolvedSource: result.Provider, ResolvedArtistID: resolvedArtistID,
		Confidence: result.Confidence, EvidenceJSON: string(evidence), UpdatedAt: time.Now(),
	})
}

func (r *IdentityResolver) cached(ctx context.Context, artistID string) (IdentityResolution, bool, error) {
	if r.store == nil {
		return IdentityResolution{}, false, fmt.Errorf("identity resolver requires store")
	}
	identity, found, err := r.store.Identity(ctx, artistID)
	if err != nil || !found || identity.Status != ResolutionResolved || time.Since(identity.UpdatedAt) > r.cacheTTL {
		return IdentityResolution{}, false, err
	}
	var evidence struct {
		Evidence       []string `json:"evidence"`
		Contradictions []string `json:"contradictions"`
	}
	_ = json.Unmarshal([]byte(identity.EvidenceJSON), &evidence)
	return IdentityResolution{
		Status: identity.Status, CandidateID: string(identity.ResolvedSource) + ":" + identity.ResolvedArtistID,
		Provider: identity.ResolvedSource, ProviderArtistID: identity.ResolvedArtistID,
		MusicBrainzID: identity.MusicBrainzID, Confidence: identity.Confidence,
		EvidenceCodes: evidence.Evidence, ContradictionCodes: evidence.Contradictions,
	}, true, nil
}

func allStringsAllowed(values, allowed []string) bool {
	for _, value := range values {
		if !containsString(allowed, value) {
			return false
		}
	}
	return true
}

type MusicBrainzIdentityClient interface {
	SearchArtists(ctx context.Context, artistName string, limit int) ([]metadata.MBArtistInfo, error)
	LookupRelease(ctx context.Context, artist, album string) (*metadata.MBReleaseInfo, error)
}

type MusicBrainzIdentitySource struct {
	client MusicBrainzIdentityClient
}

func NewMusicBrainzIdentitySource(client MusicBrainzIdentityClient) *MusicBrainzIdentitySource {
	return &MusicBrainzIdentitySource{client: client}
}

func (s *MusicBrainzIdentitySource) FindIdentityCandidates(ctx context.Context, input IdentityContext) ([]IdentityCandidate, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("MusicBrainz identity source is not configured")
	}
	artists, err := s.client.SearchArtists(ctx, input.Name, MaxIdentityCandidates)
	if err != nil {
		return nil, err
	}
	candidates := make([]IdentityCandidate, 0, len(artists))
	byMBID := make(map[string]int, len(artists))
	for _, artist := range artists {
		candidate := IdentityCandidate{
			ID: "musicbrainz:" + artist.ArtistMBID, Provider: ProviderMusicBrainz,
			ProviderArtistID: artist.ArtistMBID, MusicBrainzID: artist.ArtistMBID,
			Name: artist.CanonicalName, Aliases: artist.Aliases, Country: artist.Country,
			ProviderScore: float64(artist.Score) / 100,
		}
		byMBID[artist.ArtistMBID] = len(candidates)
		candidates = append(candidates, candidate)
	}
	if input.MusicBrainzID != "" {
		return candidates, nil
	}
	for i, album := range input.Albums {
		if i == 2 {
			break
		}
		release, err := s.client.LookupRelease(ctx, input.Name, album.Title)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || release == nil {
			continue
		}
		if index, ok := byMBID[release.ArtistMBID]; ok {
			candidates[index].Albums = append(candidates[index].Albums, IdentityRelease{Title: release.AlbumTitle, Year: release.Year})
		}
	}
	return candidates, nil
}

var _ IdentityArbiter = (*ai.LLMArtistJudge)(nil)
var _ LocalIdentityContextSource = (*db.TrackRepository)(nil)
