package artistbio

import (
	"strings"
	"testing"
)

func TestIdentity_Bounds(t *testing.T) {
	ctx := IdentityContext{ArtistID: "artist", Name: "Кино"}
	for i := 0; i < MaxIdentityAlbums+5; i++ {
		ctx.Albums = append(ctx.Albums, IdentityRelease{Title: "Альбом " + strings.Repeat("x", i%3), Year: 1990 + i})
	}
	for i := 0; i < MaxIdentityTracks+5; i++ {
		ctx.Tracks = append(ctx.Tracks, "Трек")
	}
	bounded, err := BoundIdentityContext(ctx)
	if err != nil {
		t.Fatalf("bound valid context: %v", err)
	}
	if len(bounded.Albums) > MaxIdentityAlbums || len(bounded.Tracks) != 1 {
		t.Fatalf("context bounds/dedup failed: albums=%d tracks=%d", len(bounded.Albums), len(bounded.Tracks))
	}

	ctx.Name = strings.Repeat("x", MaxIdentityNameBytes+1)
	if _, err := BoundIdentityContext(ctx); err == nil {
		t.Fatal("oversized artist name accepted")
	}
}

func TestIdentity_Normalize(t *testing.T) {
	if got, want := normalizeIdentityText("  Чаи\u0306ф\t"), normalizeIdentityText("Чайф"); got != want {
		t.Fatalf("NFC/whitespace normalization mismatch: got %q want %q", got, want)
	}
	if !identityNamesEquivalent("Максим", "Maksim") {
		t.Fatal("Cyrillic/Latin alias was not recognized")
	}
	if normalizeISRC(" ru-a01-23-12345 ") != "RUA012312345" {
		t.Fatal("ISRC was not normalized")
	}
}

func TestIdentity_Score(t *testing.T) {
	ctx := IdentityContext{
		ArtistID: "nav-1", Name: "Кино", MusicBrainzID: "mbid-kino",
		Albums: []IdentityRelease{{Title: "Группа крови", Year: 1988}, {Title: "Звезда по имени Солнце", Year: 1989}},
		ISRCs:  []string{"RUA012312345"}, Country: "RU",
	}

	exact := ScoreIdentityCandidate(ctx, IdentityCandidate{
		ID: "candidate-exact", Provider: ProviderMusicBrainz, Name: "Кино", MusicBrainzID: "mbid-kino",
	})
	if exact.Confidence != 1 || !exact.Decisive || !containsString(exact.EvidenceCodes, "id:musicbrainz") {
		t.Fatalf("exact MBID did not win: %#v", exact)
	}

	releases := ScoreIdentityCandidate(ctx, IdentityCandidate{
		ID: "candidate-releases", Provider: ProviderMusicBrainz, Name: "Kino", Country: "RU",
		Aliases: []string{"Кино"}, Albums: ctx.Albums,
	})
	if releases.Confidence < AutoResolveConfidence || !releases.Decisive {
		t.Fatalf("two matching releases did not resolve: %#v", releases)
	}

	nameOnly := ScoreIdentityCandidate(ctx, IdentityCandidate{
		ID: "candidate-name", Provider: ProviderMusicBrainz, Name: "Кино", ProviderScore: 1,
	})
	if nameOnly.Decisive || nameOnly.Confidence >= AutoResolveConfidence {
		t.Fatalf("name-only candidate became decisive: %#v", nameOnly)
	}
}

func TestIdentity_Contradictions(t *testing.T) {
	ctx := IdentityContext{
		ArtistID: "nav-1", Name: "Луна", MusicBrainzID: "expected-mbid",
		ISRCs: []string{"RUA012312345"}, Albums: []IdentityRelease{{Title: "Маг-ни-ты", Year: 2018}},
	}
	candidate := IdentityCandidate{
		ID: "wrong", Provider: ProviderMusicBrainz, Name: "Луна", MusicBrainzID: "different-mbid",
		ISRCs: []string{"USAAA9912345"}, Albums: []IdentityRelease{{Title: "Маг-ни-ты", Year: 1990}},
	}
	score := ScoreIdentityCandidate(ctx, candidate)
	if score.Decisive || score.Confidence >= 0.8 || len(score.ContradictionCodes) == 0 {
		t.Fatalf("contradictions did not block resolution: %#v", score)
	}
}

func TestIdentity_ReissueYearRemainsReleaseEvidence(t *testing.T) {
	ctx := IdentityContext{ArtistID: "nav-1", Name: "Кино", Albums: []IdentityRelease{
		{Title: "Группа крови", Year: 2024},
		{Title: "Звезда по имени Солнце", Year: 2024},
	}}
	candidate := IdentityCandidate{
		ID: "kino", Provider: ProviderMusicBrainz, Name: "Кино", ProviderScore: 1,
		Albums: []IdentityRelease{{Title: "Группа крови", Year: 1988}, {Title: "Звезда по имени Солнце", Year: 1989}},
	}
	score := ScoreIdentityCandidate(ctx, candidate)
	if !score.Decisive || score.Confidence < AutoResolveConfidence || !containsString(score.EvidenceCodes, "album:year_variant") {
		t.Fatalf("reissue years blocked exact release evidence: %#v", score)
	}
	if len(score.ContradictionCodes) != 0 {
		t.Fatalf("reissue years became identity contradictions: %#v", score)
	}
}

func TestIdentity_DeterministicAmbiguity(t *testing.T) {
	ctx := IdentityContext{ArtistID: "nav-1", Name: "Кино"}
	result := ResolveIdentityDeterministically(ctx, []IdentityCandidate{
		{ID: "one", Provider: ProviderMusicBrainz, Name: "Кино", ProviderScore: 1},
		{ID: "two", Provider: ProviderDeezer, Name: "Кино", ProviderScore: 1},
	})
	if result.Status != ResolutionUncertain || result.CandidateID != "" {
		t.Fatalf("same-name ambiguity was guessed: %#v", result)
	}
}
