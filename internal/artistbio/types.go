// Package artistbio хранит проверенные данные для обогащения карточек исполнителей.
package artistbio

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	MaxBiographyBytes  = 64 << 10
	MaxProviderIDBytes = 512
	MaxSourceURLBytes  = 4 << 10
	MaxEvidenceBytes   = 32 << 10
	maxArtistNameBytes = 2 << 10
	maxLanguageBytes   = 32
)

type Provider string

const (
	ProviderYandexMusic  Provider = "yandex_music"
	ProviderDeezer       Provider = "deezer"
	ProviderAppleMusic   Provider = "apple_music"
	ProviderWikipedia    Provider = "wikipedia"
	ProviderTheAudioDB   Provider = "theaudiodb"
	ProviderLastFM       Provider = "lastfm"
	ProviderDiscogs      Provider = "discogs"
	ProviderOfficial     Provider = "official"
	ProviderNavidrome    Provider = "navidrome"
	ProviderMusicBrainz  Provider = "musicbrainz"
	ProviderLocalLibrary Provider = "local_library"
	ProviderManual       Provider = "manual"
)

func (p Provider) valid() bool {
	switch p {
	case ProviderYandexMusic, ProviderDeezer, ProviderAppleMusic, ProviderWikipedia,
		ProviderTheAudioDB, ProviderLastFM, ProviderDiscogs, ProviderOfficial,
		ProviderNavidrome, ProviderMusicBrainz, ProviderLocalLibrary, ProviderManual:
		return true
	default:
		return false
	}
}

type ResolutionStatus string

const (
	ResolutionPending   ResolutionStatus = "pending"
	ResolutionResolved  ResolutionStatus = "resolved"
	ResolutionUncertain ResolutionStatus = "uncertain"
)

func (s ResolutionStatus) valid() bool {
	return s == ResolutionPending || s == ResolutionResolved || s == ResolutionUncertain
}

type CandidateStatus string

const (
	CandidateFetched            CandidateStatus = "fetched"
	CandidateTranslationPending CandidateStatus = "translation_pending"
	CandidateAccepted           CandidateStatus = "accepted"
	CandidateRejected           CandidateStatus = "rejected"
)

func (s CandidateStatus) valid() bool {
	switch s {
	case CandidateFetched, CandidateTranslationPending, CandidateAccepted, CandidateRejected:
		return true
	default:
		return false
	}
}

type ArtistIdentity struct {
	ArtistID         string
	ArtistName       string
	MusicBrainzID    string
	Status           ResolutionStatus
	ResolvedSource   Provider
	ResolvedArtistID string
	Confidence       float64
	EvidenceJSON     string
	UpdatedAt        time.Time
}

func (a ArtistIdentity) Validate() error {
	if err := validateRequired("artist id", a.ArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if err := validateOptional("artist name", a.ArtistName, maxArtistNameBytes); err != nil {
		return err
	}
	if err := validateOptional("musicbrainz id", a.MusicBrainzID, MaxProviderIDBytes); err != nil {
		return err
	}
	if !a.Status.valid() {
		return fmt.Errorf("invalid resolution status %q", a.Status)
	}
	if a.ResolvedSource != "" && !a.ResolvedSource.valid() {
		return fmt.Errorf("invalid resolved source %q", a.ResolvedSource)
	}
	if err := validateOptional("resolved artist id", a.ResolvedArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if err := validateConfidence("identity confidence", a.Confidence); err != nil {
		return err
	}
	return validateEvidence(a.EvidenceJSON)
}

type Candidate struct {
	ID                 int64
	ArtistID           string
	Provider           Provider
	SourceArtistID     string
	SourceURL          string
	OriginalText       string
	RussianText        string
	OriginalLanguage   string
	DetectedLanguage   string
	LanguageConfidence float64
	RussianShare       float64
	MatchConfidence    float64
	Status             CandidateStatus
	EvidenceJSON       string
	FetchedAt          time.Time
	UpdatedAt          time.Time
}

func (c Candidate) Validate() error {
	if err := validateRequired("artist id", c.ArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if !c.Provider.valid() {
		return fmt.Errorf("invalid provider %q", c.Provider)
	}
	if c.Provider == ProviderManual {
		return fmt.Errorf("manual biographies must use the separate override")
	}
	if err := validateRequired("source artist id", c.SourceArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if err := validateSourceURL(c.SourceURL); err != nil {
		return err
	}
	if err := validateRequired("original biography", c.OriginalText, MaxBiographyBytes); err != nil {
		return err
	}
	if err := validateOptional("russian biography", c.RussianText, MaxBiographyBytes); err != nil {
		return err
	}
	if err := validateOptional("original language", c.OriginalLanguage, maxLanguageBytes); err != nil {
		return err
	}
	if err := validateOptional("detected language", c.DetectedLanguage, maxLanguageBytes); err != nil {
		return err
	}
	if err := validateConfidence("language confidence", c.LanguageConfidence); err != nil {
		return err
	}
	if err := validateConfidence("russian share", c.RussianShare); err != nil {
		return err
	}
	if err := validateConfidence("match confidence", c.MatchConfidence); err != nil {
		return err
	}
	if !c.Status.valid() {
		return fmt.Errorf("invalid candidate status %q", c.Status)
	}
	return validateEvidence(c.EvidenceJSON)
}

type ManualOverride struct {
	ArtistID  string
	Text      string
	SourceURL string
	UpdatedAt time.Time
}

func (m ManualOverride) Validate() error {
	if err := validateRequired("artist id", m.ArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if err := validateOptional("manual biography", m.Text, MaxBiographyBytes); err != nil {
		return err
	}
	if strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.SourceURL) != "" {
		return fmt.Errorf("manual source url requires biography text")
	}
	return validateSourceURL(m.SourceURL)
}

type EffectiveBiography struct {
	ArtistID    string
	Text        string
	Source      Provider
	SourceURL   string
	CandidateID int64
	Manual      bool
	Translated  bool
	UpdatedAt   time.Time
}

func validateRequired(name, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return validateOptional(name, value, maxBytes)
}

func validateOptional(name, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	return nil
}

func validateConfidence(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return fmt.Errorf("%s must be finite and between 0 and 1", name)
	}
	return nil
}

func validateEvidence(value string) error {
	if len(value) > MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds %d bytes", MaxEvidenceBytes)
	}
	if value != "" && !json.Valid([]byte(value)) {
		return fmt.Errorf("evidence is not valid json")
	}
	return nil
}

func validateSourceURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > MaxSourceURLBytes {
		return fmt.Errorf("source url exceeds %d bytes", MaxSourceURLBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid source url: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("source url must be absolute http or https")
	}
	if parsed.User != nil {
		return fmt.Errorf("source url must not contain user information")
	}
	return nil
}
