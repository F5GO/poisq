package artistbio_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/artistbio"
)

func TestStore_PrepareCatalogEvidenceIsBoundedStableAndExternalOnly(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	base := "The artist began a professional music career in the late nineteen nineties and became known for distinctive recordings and concert performances. "
	providers := []artistbio.Provider{
		artistbio.ProviderOfficial, artistbio.ProviderYandexMusic, artistbio.ProviderWikipedia,
		artistbio.ProviderAppleMusic, artistbio.ProviderDeezer,
	}
	for index, provider := range providers {
		unique := "This provider documents a distinct part of the artist history including releases, collaborations, performances, and musical development under source marker " + strings.Repeat("x", 140+index) + ". "
		text := unique + strings.Repeat(base, 14)
		if _, err := store.PutCandidate(ctx, artistbio.Candidate{
			ArtistID: "artist", Provider: provider, SourceArtistID: string(provider), SourceURL: "https://example.com/" + string(provider),
			OriginalText: text, OriginalLanguage: "en", DetectedLanguage: "en", LanguageConfidence: 1,
			MatchConfidence: 1, Status: artistbio.CandidateAccepted, UpdatedAt: time.Unix(int64(index+1), 0),
		}); err != nil {
			t.Fatalf("put %s: %v", provider, err)
		}
	}
	// Generated summaries are deliberately excluded from their own future input.
	if _, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderAISummary, SourceArtistID: "old", OriginalText: strings.Repeat("Русское описание исполнителя и его творчества. ", 10),
		OriginalLanguage: "ru", DetectedLanguage: "ru", LanguageConfidence: 1, RussianShare: 1,
		MatchConfidence: 1, Status: artistbio.CandidateAccepted,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.PrepareCatalogEvidence(ctx, "artist")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PrepareCatalogEvidence(ctx, "artist")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 4 || first.Origins != 4 || first.Version == "" || first.Version != second.Version {
		t.Fatalf("unexpected deterministic evidence: %#v secondVersion=%q", first, second.Version)
	}
	totalBytes := 0
	for _, item := range first.Items {
		if item.Provider == artistbio.ProviderAISummary || item.Provider == artistbio.ProviderLocalLibrary || item.Provider == artistbio.ProviderManual {
			t.Fatalf("ineligible origin included: %s", item.Provider)
		}
		if utf8.RuneCountInString(item.Text) > 1200 {
			t.Fatalf("extract exceeds rune bound: %d", utf8.RuneCountInString(item.Text))
		}
		totalBytes += len(item.Text)
	}
	if totalBytes > 4<<10 {
		t.Fatalf("catalog payload exceeds byte bound: %d", totalBytes)
	}
	if !first.Sufficient {
		t.Fatalf("large independent catalog evidence should be sufficient: %#v", first)
	}
}

func TestStore_PrepareCatalogEvidenceRequiresTwoUsefulOrigins(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("The musician released recordings and performed concerts for an international audience. ", 8)
	if _, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderWikipedia, SourceArtistID: "wiki", SourceURL: "https://example.com/wiki",
		OriginalText: text, OriginalLanguage: "en", DetectedLanguage: "en", LanguageConfidence: 1,
		MatchConfidence: 1, Status: artistbio.CandidateAccepted,
	}); err != nil {
		t.Fatal(err)
	}
	evidence, err := store.PrepareCatalogEvidence(ctx, "artist")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Origins != 1 || evidence.Sufficient {
		t.Fatalf("one origin became sufficient: %#v", evidence)
	}
}
