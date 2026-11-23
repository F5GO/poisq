package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/httpclient"
	"github.com/F5GO/poisq/internal/security"
)

const listenBrainzBaseURL = "https://api.listenbrainz.org"

type ArtworkRole string

const (
	ArtworkAlbumPrimary  ArtworkRole = "album_primary"
	ArtworkArtistPrimary ArtworkRole = "artist_primary"
	ArtworkDisc          ArtworkRole = "disc"
	ArtworkLogo          ArtworkRole = "logo"
	ArtworkBanner        ArtworkRole = "banner"
	ArtworkBackdrop      ArtworkRole = "backdrop"
)

type ArtworkCandidate struct {
	URL    string `json:"url"`
	Source string `json:"source"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Score  int    `json:"score"`
}

type RankedArtwork struct {
	ArtworkCandidate
	QualityScore float64 `json:"qualityScore"`
}

func RankArtworkCandidates(role ArtworkRole, input []ArtworkCandidate) []RankedArtwork {
	targetAspect, minWidth, ok := artworkGeometry(role)
	if !ok {
		return nil
	}
	values := make([]RankedArtwork, 0, len(input))
	for _, candidate := range input {
		parsed, err := url.Parse(strings.TrimSpace(candidate.URL))
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || candidate.Width <= 0 || candidate.Height <= 0 || candidate.Width > 20000 || candidate.Height > 20000 || strings.TrimSpace(candidate.Source) == "" {
			continue
		}
		aspect := float64(candidate.Width) / float64(candidate.Height)
		aspectPenalty := math.Min(40, math.Abs(math.Log(aspect/targetAspect))*30)
		resolution := math.Min(25, float64(candidate.Width)/float64(minWidth)*15)
		if candidate.Width < minWidth {
			resolution -= 20
		}
		sourceBonus := map[string]float64{"coverartarchive": 12, "fanarttv": 10, "musicbrainz": 8, "itunes": 5}[strings.ToLower(candidate.Source)]
		quality := float64(candidate.Score) + resolution + sourceBonus - aspectPenalty
		values = append(values, RankedArtwork{ArtworkCandidate: candidate, QualityScore: quality})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].QualityScore != values[j].QualityScore {
			return values[i].QualityScore > values[j].QualityScore
		}
		if values[i].Width*values[i].Height != values[j].Width*values[j].Height {
			return values[i].Width*values[i].Height > values[j].Width*values[j].Height
		}
		if values[i].Source != values[j].Source {
			return values[i].Source < values[j].Source
		}
		return values[i].URL < values[j].URL
	})
	return values
}

func artworkGeometry(role ArtworkRole) (float64, int, bool) {
	switch role {
	case ArtworkAlbumPrimary, ArtworkArtistPrimary, ArtworkDisc:
		return 1, 1000, true
	case ArtworkLogo:
		return 3, 400, true
	case ArtworkBanner:
		return 5.4, 1000, true
	case ArtworkBackdrop:
		return 16.0 / 9.0, 1280, true
	default:
		return 0, 0, false
	}
}

func artworkFileName(role ArtworkRole) string {
	switch role {
	case ArtworkAlbumPrimary:
		return "cover.jpg"
	case ArtworkArtistPrimary:
		return "artist.jpg"
	case ArtworkDisc:
		return "disc.png"
	case ArtworkLogo:
		return "logo.png"
	case ArtworkBanner:
		return "banner.jpg"
	case ArtworkBackdrop:
		return "fanart.jpg"
	default:
		return ""
	}
}

type MediaEnrichmentService struct {
	guard   *security.PathGuard
	staging *db.StagingRepository
	repo    *db.MediaEnrichmentRepository
	lb      *ListenBrainzClient
}

func NewMediaEnrichmentService(guard *security.PathGuard, staging *db.StagingRepository, repo *db.MediaEnrichmentRepository, lb *ListenBrainzClient) *MediaEnrichmentService {
	return &MediaEnrichmentService{guard: guard, staging: staging, repo: repo, lb: lb}
}

func (s *MediaEnrichmentService) StageArtwork(ctx context.Context, scope, scopeID, folderPath string, role ArtworkRole, candidates []ArtworkCandidate) (int64, error) {
	if (scope != "album" && scope != "artist") || strings.TrimSpace(scopeID) == "" {
		return 0, fmt.Errorf("invalid artwork scope")
	}
	if (role == ArtworkArtistPrimary && scope != "artist") || ((role == ArtworkAlbumPrimary || role == ArtworkDisc) && scope != "album") {
		return 0, fmt.Errorf("artwork role does not match scope")
	}
	folder, err := s.guard.ValidateTarget(folderPath, false)
	if err != nil {
		return 0, err
	}
	ranked := RankArtworkCandidates(role, candidates)
	if len(ranked) == 0 {
		return 0, fmt.Errorf("no valid artwork candidates")
	}
	fileName := artworkFileName(role)
	if fileName == "" {
		return 0, fmt.Errorf("invalid artwork role")
	}
	var previous *db.ArtworkRecord
	if existing, err := s.repo.ListArtwork(ctx, scope, scopeID); err == nil {
		for index := range existing {
			if existing[index].Role == string(role) {
				copy := existing[index]
				previous = &copy
				break
			}
		}
	}
	best := ranked[0]
	currentJSON, _ := json.Marshal(map[string]any{"previous": previous})
	proposedJSON, _ := json.Marshal(map[string]any{
		"coverUrl": best.URL, "fileName": fileName, "role": role, "scope": scope, "scopeId": scopeID,
		"source": best.Source, "width": best.Width, "height": best.Height, "score": int(math.Round(best.QualityScore)),
		"reason": "Role-aware artwork selection",
	})
	return s.staging.CreateProposal(db.StagingChangeRecord{
		Scope: scope, TargetID: scopeID + ":" + string(role), FolderPath: folder, ChangeType: "artwork",
		Confidence: min(100, max(0, int(math.Round(best.QualityScore)))), CurrentJSON: string(currentJSON), ProposedJSON: string(proposedJSON),
	})
}

func (s *MediaEnrichmentService) RefreshSimilarArtists(ctx context.Context, artistMBID string, limit int) ([]db.SimilarArtistRecord, error) {
	if s.lb == nil {
		return nil, fmt.Errorf("ListenBrainz client is not configured")
	}
	values, err := s.lb.SimilarArtists(ctx, artistMBID, limit)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ReplaceSimilarArtists(ctx, artistMBID, values); err != nil {
		return nil, err
	}
	return values, nil
}

type ListenBrainzClient struct {
	baseURL string
	rc      *httpclient.ResilientClient
}

func NewListenBrainzClient() *ListenBrainzClient {
	rc := httpclient.NewResilientClient(12*time.Second, 1, 200*time.Millisecond, 2)
	_ = rc.SetMaxResponseBytes(1 << 20)
	return &ListenBrainzClient{baseURL: listenBrainzBaseURL, rc: rc}
}

func (c *ListenBrainzClient) SimilarArtists(ctx context.Context, artistMBID string, limit int) ([]db.SimilarArtistRecord, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	if !validMusicBrainzID(artistMBID) {
		return nil, fmt.Errorf("invalid MusicBrainz artist id")
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	endpoint := fmt.Sprintf("%s/1/lb-radio/artist/%s?mode=easy&max_similar_artists=%d&max_recordings_per_artist=1&pop_begin=0&pop_end=100", c.baseURL, url.PathEscape(artistMBID), limit)
	body, status, _, err := c.rc.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", musicBrainzUserAgent)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ListenBrainz similar artists request failed")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("ListenBrainz similar artists HTTP %d", status)
	}
	var payload map[string][]struct {
		SimilarArtistMBID string `json:"similar_artist_mbid"`
		SimilarArtistName string `json:"similar_artist_name"`
		TotalListenCount  int64  `json:"total_listen_count"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload) > 100 {
		return nil, fmt.Errorf("invalid ListenBrainz similar artists response")
	}
	byID := make(map[string]db.SimilarArtistRecord)
	for outerID, recordings := range payload {
		if len(recordings) > 10 {
			return nil, fmt.Errorf("ListenBrainz artist entry exceeds limit")
		}
		for _, recording := range recordings {
			mbid := strings.TrimSpace(recording.SimilarArtistMBID)
			if mbid == "" {
				mbid = strings.TrimSpace(outerID)
			}
			if !validMusicBrainzID(mbid) || mbid == artistMBID || strings.TrimSpace(recording.SimilarArtistName) == "" || recording.TotalListenCount < 0 {
				continue
			}
			value := byID[mbid]
			if value.SimilarMBID == "" || recording.TotalListenCount > value.ListenCount {
				byID[mbid] = db.SimilarArtistRecord{
					ReferenceMBID: artistMBID, SimilarMBID: mbid, Name: strings.TrimSpace(recording.SimilarArtistName),
					ListenCount: recording.TotalListenCount, Provider: "listenbrainz",
				}
			}
		}
	}
	values := make([]db.SimilarArtistRecord, 0, len(byID))
	var maximum int64
	for _, value := range byID {
		if value.ListenCount > maximum {
			maximum = value.ListenCount
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].ListenCount != values[j].ListenCount {
			return values[i].ListenCount > values[j].ListenCount
		}
		return values[i].SimilarMBID < values[j].SimilarMBID
	})
	if len(values) > limit {
		values = values[:limit]
	}
	for index := range values {
		if maximum > 0 {
			values[index].Score = float64(values[index].ListenCount) / float64(maximum)
		}
	}
	return values, nil
}
