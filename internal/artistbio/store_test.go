package artistbio_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open poisq database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestBiographySchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poisq.db")
	first, err := db.OpenPoisqDatabase(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := first.Exec(`INSERT INTO settings(key, value) VALUES ('schema-sentinel', 'preserved')`); err != nil {
		t.Fatalf("insert existing-data sentinel: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	second, err := db.OpenPoisqDatabase(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	for _, table := range []string{"artist_bio_identity", "artist_bio_candidates", "artist_biographies"} {
		var count int
		if err := second.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist once, got %d", table, count)
		}
	}

	var sentinel string
	if err := second.QueryRow(`SELECT value FROM settings WHERE key = 'schema-sentinel'`).Scan(&sentinel); err != nil {
		t.Fatalf("read existing-data sentinel: %v", err)
	}
	if sentinel != "preserved" {
		t.Fatalf("existing data changed: %q", sentinel)
	}
}

func TestArtistIdentityValidation(t *testing.T) {
	valid := artistbio.ArtistIdentity{
		ArtistID:       "navidrome-artist-id",
		ArtistName:     "Кино",
		Status:         artistbio.ResolutionPending,
		Confidence:     0,
		EvidenceJSON:   `{}`,
		ResolvedSource: artistbio.ProviderMusicBrainz,
		UpdatedAt:      time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	cases := map[string]artistbio.ArtistIdentity{
		"empty artist id": func() artistbio.ArtistIdentity { v := valid; v.ArtistID = " "; return v }(),
		"oversized artist": func() artistbio.ArtistIdentity {
			v := valid
			v.ArtistID = strings.Repeat("x", artistbio.MaxProviderIDBytes+1)
			return v
		}(),
		"invalid status":     func() artistbio.ArtistIdentity { v := valid; v.Status = "resolved-ish"; return v }(),
		"invalid source":     func() artistbio.ArtistIdentity { v := valid; v.ResolvedSource = "unknown"; return v }(),
		"invalid confidence": func() artistbio.ArtistIdentity { v := valid; v.Confidence = math.NaN(); return v }(),
		"invalid evidence":   func() artistbio.ArtistIdentity { v := valid; v.EvidenceJSON = `{`; return v }(),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCandidateValidationBoundaries(t *testing.T) {
	valid := artistbio.Candidate{
		ArtistID:           "artist-id",
		Provider:           artistbio.ProviderDeezer,
		SourceArtistID:     "deezer-42",
		SourceURL:          "https://www.deezer.com/artist/42",
		OriginalText:       "Настоящее описание исполнителя.",
		OriginalLanguage:   "ru",
		DetectedLanguage:   "ru",
		LanguageConfidence: 0.99,
		RussianShare:       1,
		MatchConfidence:    0.98,
		Status:             artistbio.CandidateAccepted,
		EvidenceJSON:       `{"albums":["Группа крови"]}`,
		FetchedAt:          time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}

	cases := map[string]artistbio.Candidate{
		"oversized text": func() artistbio.Candidate {
			v := valid
			v.OriginalText = strings.Repeat("я", artistbio.MaxBiographyBytes+1)
			return v
		}(),
		"userinfo url":     func() artistbio.Candidate { v := valid; v.SourceURL = "https://user:pass@example.com/bio"; return v }(),
		"file url":         func() artistbio.Candidate { v := valid; v.SourceURL = "file:///etc/passwd"; return v }(),
		"invalid provider": func() artistbio.Candidate { v := valid; v.Provider = "random"; return v }(),
		"infinite score":   func() artistbio.Candidate { v := valid; v.MatchConfidence = math.Inf(1); return v }(),
		"negative share":   func() artistbio.Candidate { v := valid; v.RussianShare = -0.01; return v }(),
		"invalid evidence": func() artistbio.Candidate { v := valid; v.EvidenceJSON = `not-json`; return v }(),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestManualOverrideValidation(t *testing.T) {
	empty := artistbio.ManualOverride{ArtistID: "artist-id"}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty optional override rejected: %v", err)
	}

	invalid := []artistbio.ManualOverride{
		{ArtistID: "", Text: "описание"},
		{ArtistID: "artist-id", Text: strings.Repeat("x", artistbio.MaxBiographyBytes+1)},
		{ArtistID: "artist-id", Text: "описание", SourceURL: "javascript:alert(1)"},
	}
	for i, value := range invalid {
		if err := value.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestBiographySchemaForeignKeys(t *testing.T) {
	database := openTestDB(t)
	var enabled int
	if err := database.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign keys disabled: %d", enabled)
	}
}

func TestStore_CandidateUpsert(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	identity := artistbio.ArtistIdentity{
		ArtistID:     "artist-'--id",
		ArtistName:   "Кино'); DROP TABLE settings; --",
		Status:       artistbio.ResolutionResolved,
		Confidence:   0.99,
		EvidenceJSON: `{}`,
		UpdatedAt:    time.Now(),
	}
	if err := store.PutIdentity(ctx, identity); err != nil {
		t.Fatalf("put identity: %v", err)
	}

	candidate := artistbio.Candidate{
		ArtistID:           identity.ArtistID,
		Provider:           artistbio.ProviderDeezer,
		SourceArtistID:     "42' OR 1=1 --",
		SourceURL:          "https://www.deezer.com/artist/42",
		OriginalText:       "Первоначальное описание'); DELETE FROM settings; --",
		OriginalLanguage:   "ru",
		DetectedLanguage:   "ru",
		LanguageConfidence: 0.98,
		RussianShare:       1,
		MatchConfidence:    0.97,
		Status:             artistbio.CandidateAccepted,
		EvidenceJSON:       `{}`,
		FetchedAt:          time.Now(),
	}
	firstID, err := store.PutCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("put candidate: %v", err)
	}
	if firstID <= 0 {
		t.Fatalf("invalid candidate id: %d", firstID)
	}

	candidate.OriginalText = "Обновлённое описание"
	candidate.MatchConfidence = 0.995
	secondID, err := store.PutCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("refresh candidate: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("upsert created duplicate: first=%d second=%d", firstID, secondID)
	}

	got, found, err := store.Candidate(ctx, candidate.ArtistID, candidate.Provider, candidate.SourceArtistID)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if !found {
		t.Fatal("candidate not found")
	}
	if got.OriginalText != candidate.OriginalText || got.MatchConfidence != candidate.MatchConfidence {
		t.Fatalf("candidate was not refreshed: %#v", got)
	}

	var settingsExists int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'settings'`).Scan(&settingsExists); err != nil {
		t.Fatalf("query settings table: %v", err)
	}
	if settingsExists != 1 {
		t.Fatal("SQL metacharacters altered the schema")
	}
}

func TestStore_SelectAutomatic(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()

	putIdentity := func(id string) {
		t.Helper()
		if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{
			ArtistID: id, Status: artistbio.ResolutionResolved, Confidence: 1, EvidenceJSON: `{}`, UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("put identity %s: %v", id, err)
		}
	}
	putCandidate := func(artistID, sourceID string, status artistbio.CandidateStatus) int64 {
		t.Helper()
		id, err := store.PutCandidate(ctx, artistbio.Candidate{
			ArtistID: artistID, Provider: artistbio.ProviderWikipedia, SourceArtistID: sourceID,
			SourceURL: "https://ru.wikipedia.org/wiki/Artist", OriginalText: "Описание " + artistID,
			OriginalLanguage: "ru", DetectedLanguage: "ru", LanguageConfidence: 1, RussianShare: 1,
			MatchConfidence: 1, Status: status, EvidenceJSON: `{}`, FetchedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("put candidate %s: %v", sourceID, err)
		}
		return id
	}

	putIdentity("artist-a")
	putIdentity("artist-b")
	acceptedA := putCandidate("artist-a", "wiki-a", artistbio.CandidateAccepted)
	acceptedB := putCandidate("artist-b", "wiki-b", artistbio.CandidateAccepted)
	fetchedA := putCandidate("artist-a", "wiki-pending", artistbio.CandidateFetched)

	if err := store.SelectAutomatic(ctx, "artist-a", acceptedA, time.Now()); err != nil {
		t.Fatalf("select own candidate: %v", err)
	}
	selected, found, err := store.AutomaticCandidate(ctx, "artist-a")
	if err != nil {
		t.Fatalf("read automatic candidate: %v", err)
	}
	if !found || selected.ID != acceptedA {
		t.Fatalf("unexpected automatic candidate: found=%v candidate=%#v", found, selected)
	}

	if err := store.SelectAutomatic(ctx, "artist-a", acceptedB, time.Now()); !errors.Is(err, artistbio.ErrCandidateArtistMismatch) {
		t.Fatalf("cross-artist selection error = %v", err)
	}
	if err := store.SelectAutomatic(ctx, "artist-a", fetchedA, time.Now()); !errors.Is(err, artistbio.ErrCandidateNotAccepted) {
		t.Fatalf("unaccepted selection error = %v", err)
	}
	if err := store.SelectAutomatic(ctx, "artist-a", 999999, time.Now()); !errors.Is(err, artistbio.ErrCandidateNotFound) {
		t.Fatalf("missing selection error = %v", err)
	}

	selected, found, err = store.AutomaticCandidate(ctx, "artist-a")
	if err != nil || !found || selected.ID != acceptedA {
		t.Fatalf("failed selections changed automatic candidate: found=%v candidate=%#v err=%v", found, selected, err)
	}
}

func TestStore_ManualOverrideAndEffectiveBiography(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{
		ArtistID: "artist", Status: artistbio.ResolutionResolved, Confidence: 1, EvidenceJSON: `{}`,
	}); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	candidateID, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderDeezer, SourceArtistID: "deezer-1",
		SourceURL: "https://www.deezer.com/artist/1", OriginalText: "Автоматическое описание",
		OriginalLanguage: "ru", DetectedLanguage: "ru", LanguageConfidence: 1,
		RussianShare: 1, MatchConfidence: 1, Status: artistbio.CandidateAccepted, EvidenceJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("put candidate: %v", err)
	}
	if err := store.SelectAutomatic(ctx, "artist", candidateID, time.Now()); err != nil {
		t.Fatalf("select automatic: %v", err)
	}

	automatic, found, err := store.EffectiveBiography(ctx, "artist")
	if err != nil || !found {
		t.Fatalf("read automatic effective biography: found=%v err=%v", found, err)
	}
	if automatic.Text != "Автоматическое описание" || automatic.Manual || automatic.Source != artistbio.ProviderDeezer {
		t.Fatalf("unexpected automatic effective biography: %#v", automatic)
	}

	manual := artistbio.ManualOverride{
		ArtistID: "artist", Text: "Ручное описание", SourceURL: "https://artist.example/about", UpdatedAt: time.Now(),
	}
	if err := store.SetManualOverride(ctx, manual); err != nil {
		t.Fatalf("set manual override: %v", err)
	}
	effective, found, err := store.EffectiveBiography(ctx, "artist")
	if err != nil || !found {
		t.Fatalf("read manual effective biography: found=%v err=%v", found, err)
	}
	if effective.Text != manual.Text || !effective.Manual || effective.Source != artistbio.ProviderManual {
		t.Fatalf("manual override did not win: %#v", effective)
	}

	if err := store.SetManualOverride(ctx, artistbio.ManualOverride{ArtistID: "artist"}); err != nil {
		t.Fatalf("clear manual override: %v", err)
	}
	effective, found, err = store.EffectiveBiography(ctx, "artist")
	if err != nil || !found {
		t.Fatalf("read effective biography after clear: found=%v err=%v", found, err)
	}
	if effective.Text != automatic.Text || effective.Manual || effective.CandidateID != candidateID {
		t.Fatalf("automatic biography was not restored: %#v", effective)
	}
}

func TestStore_EffectiveBiographyWithoutContent(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{
		ArtistID: "artist", Status: artistbio.ResolutionPending, EvidenceJSON: `{}`,
	}); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	if err := store.SetManualOverride(ctx, artistbio.ManualOverride{ArtistID: "artist"}); err != nil {
		t.Fatalf("store empty optional override: %v", err)
	}
	if result, found, err := store.EffectiveBiography(ctx, "artist"); err != nil || found || result.Text != "" {
		t.Fatalf("empty override became content: found=%v result=%#v err=%v", found, result, err)
	}
}

func TestStore_ConcurrentEffectiveReads(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	candidateID, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderLastFM, SourceArtistID: "lastfm-1",
		OriginalText: "Автоматическое описание", OriginalLanguage: "ru", DetectedLanguage: "ru",
		LanguageConfidence: 1, RussianShare: 1, MatchConfidence: 1, Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatalf("put candidate: %v", err)
	}
	if err := store.SelectAutomatic(ctx, "artist", candidateID, time.Now()); err != nil {
		t.Fatalf("select automatic: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			text := ""
			if i%2 == 0 {
				text = "Ручное описание"
			}
			if err := store.SetManualOverride(ctx, artistbio.ManualOverride{ArtistID: "artist", Text: text}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 80; i++ {
			result, found, err := store.EffectiveBiography(ctx, "artist")
			if err != nil {
				errCh <- err
				return
			}
			if !found || (result.Text != "Автоматическое описание" && result.Text != "Ручное описание") {
				errCh <- errors.New("effective read observed partial state")
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestStore_QueryPlanUsesArtistIndexes(t *testing.T) {
	database := openTestDB(t)
	rows, err := database.Query(`EXPLAIN QUERY PLAN
		SELECT b.artist_id, b.manual_text, b.manual_source_url, b.manual_updated_at,
			b.updated_at, COALESCE(c.id, 0), COALESCE(c.provider, ''),
			COALESCE(c.source_url, ''), COALESCE(c.original_text, ''),
			COALESCE(c.russian_text, ''), COALESCE(c.updated_at, 0)
		FROM artist_biographies b
		LEFT JOIN artist_bio_candidates c ON c.id = b.automatic_candidate_id AND c.artist_id = b.artist_id
		WHERE b.artist_id = ?`, "artist")
	if err != nil {
		t.Fatalf("explain effective biography query: %v", err)
	}
	defer rows.Close()

	var details strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details.WriteString(detail)
		details.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	plan := details.String()
	if strings.Contains(plan, "SCAN b") || !strings.Contains(plan, "artist_id") {
		t.Fatalf("effective query does not use the artist index:\n%s", plan)
	}
}

func TestStore_RejectsManualCandidateProvider(t *testing.T) {
	candidate := artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderManual, SourceArtistID: "manual",
		OriginalText: "Описание", Status: artistbio.CandidateAccepted,
	}
	if err := candidate.Validate(); err == nil {
		t.Fatal("manual provider was accepted as an automatic candidate")
	}
}

func TestStore_CancellationAndErrorsDoNotLeakBiography(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.PutIdentity(cancelled, artistbio.ArtistIdentity{
		ArtistID: "artist", Status: artistbio.ResolutionPending,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %v", err)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	const secretText = "private biography body must not appear in errors"
	_, err = store.PutCandidate(context.Background(), artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderDeezer, SourceArtistID: "42",
		OriginalText: secretText, Status: artistbio.CandidateFetched,
	})
	if err == nil {
		t.Fatal("expected closed database error")
	}
	if strings.Contains(err.Error(), secretText) {
		t.Fatalf("database error leaked biography body: %v", err)
	}
}

func TestStore_IdentityReadUsesArtistKey(t *testing.T) {
	database := openTestDB(t)
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	want := artistbio.ArtistIdentity{
		ArtistID: "artist-id", ArtistName: "Кино", MusicBrainzID: "mbid",
		Status: artistbio.ResolutionResolved, ResolvedSource: artistbio.ProviderMusicBrainz,
		ResolvedArtistID: "mbid", Confidence: 0.99, EvidenceJSON: `{"codes":["id:musicbrainz"]}`,
		UpdatedAt: time.Now().Truncate(time.Second),
	}
	if err := store.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	got, found, err := store.Identity(context.Background(), want.ArtistID)
	if err != nil || !found {
		t.Fatalf("read identity: found=%v err=%v", found, err)
	}
	if got.ArtistID != want.ArtistID || got.MusicBrainzID != want.MusicBrainzID || got.EvidenceJSON != want.EvidenceJSON || got.Confidence != want.Confidence {
		t.Fatalf("identity mismatch: got=%#v want=%#v", got, want)
	}

	rows, err := database.Query(`EXPLAIN QUERY PLAN SELECT artist_id FROM artist_bio_identity WHERE artist_id = ?`, want.ArtistID)
	if err != nil {
		t.Fatalf("explain identity lookup: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(detail)
	}
	if strings.Contains(plan.String(), "SCAN artist_bio_identity") {
		t.Fatalf("identity lookup scans table: %s", plan.String())
	}
}

func TestStoreCandidateValidationAndAutomaticSelection(t *testing.T) {
	database := openTestDB(t)
	store, _ := artistbio.NewStore(database)
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatalf("put identity: %v", err)
	}
	put := func(provider artistbio.Provider, sourceID, text string, confidence float64) int64 {
		t.Helper()
		id, err := store.PutCandidate(ctx, artistbio.Candidate{
			ArtistID: "artist", Provider: provider, SourceArtistID: sourceID,
			OriginalText: text, MatchConfidence: confidence, Status: artistbio.CandidateFetched,
		})
		if err != nil {
			t.Fatalf("put candidate: %v", err)
		}
		return id
	}
	directID := put(artistbio.ProviderDeezer, "direct", strings.Repeat("Российская музыкальная группа выпустила известный альбом и выступала на фестивалях. ", 2), 0.98)
	translatedID := put(artistbio.ProviderWikipedia, "translated", strings.Repeat("The music group released a well known album and performed at festivals. ", 2), 0.98)

	candidates, err := store.Candidates(ctx, "artist", 8)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("list candidates: count=%d err=%v", len(candidates), err)
	}
	translation := strings.Repeat("Музыкальная группа выпустила известный альбом и выступала на крупных фестивалях страны. ", 2)
	if err := store.ApplyValidationAndSelect(ctx, artistbio.CandidateValidation{
		ArtistID: "artist", CandidateID: translatedID, RussianText: translation,
		DetectedLanguage: "en", LanguageConfidence: 0.98, RussianShare: 0.99,
		Status: artistbio.CandidateAccepted, EvidenceJSON: `{"validation":["translation:grounded"]}`,
	}); err != nil {
		t.Fatalf("validate translation: %v", err)
	}
	if err := store.ApplyValidationAndSelect(ctx, artistbio.CandidateValidation{
		ArtistID: "artist", CandidateID: directID, DetectedLanguage: "ru",
		LanguageConfidence: 0.98, RussianShare: 0.99, Status: artistbio.CandidateAccepted,
		EvidenceJSON: `{"validation":["lang:ru"]}`,
	}); err != nil {
		t.Fatalf("validate direct russian: %v", err)
	}
	selected, found, err := store.AutomaticCandidate(ctx, "artist")
	if err != nil || !found || selected.ID != directID {
		t.Fatalf("direct russian did not win tie: found=%v selected=%#v err=%v", found, selected, err)
	}

	if err := store.SetManualOverride(ctx, artistbio.ManualOverride{ArtistID: "artist", Text: "Ручной приоритет"}); err != nil {
		t.Fatalf("set manual: %v", err)
	}
	if err := store.ApplyValidationAndSelect(ctx, artistbio.CandidateValidation{
		ArtistID: "artist", CandidateID: translatedID, DetectedLanguage: "en",
		LanguageConfidence: 0.5, RussianShare: 0, Status: artistbio.CandidateRejected,
		EvidenceJSON: `{"validation":["translation:rejected"]}`,
	}); err != nil {
		t.Fatalf("reject candidate: %v", err)
	}
	effective, found, err := store.EffectiveBiography(ctx, "artist")
	if err != nil || !found || !effective.Manual || effective.Text != "Ручной приоритет" {
		t.Fatalf("validation changed manual precedence: %#v found=%v err=%v", effective, found, err)
	}
}

func TestStoreCandidateValidationRejectsWrongArtistAndUnsafeRussian(t *testing.T) {
	database := openTestDB(t)
	store, _ := artistbio.NewStore(database)
	ctx := context.Background()
	for _, artistID := range []string{"a", "b"} {
		if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: artistID, Status: artistbio.ResolutionResolved}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "a", Provider: artistbio.ProviderWikipedia, SourceArtistID: "source",
		OriginalText: strings.Repeat("The artist recorded an album and performed concerts. ", 3),
		Status:       artistbio.CandidateFetched,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner := artistbio.CandidateValidation{
		ArtistID: "b", CandidateID: id, DetectedLanguage: "en", Status: artistbio.CandidateRejected,
	}
	if err := store.ApplyValidationAndSelect(ctx, wrongOwner); !errors.Is(err, artistbio.ErrCandidateArtistMismatch) {
		t.Fatalf("wrong owner error = %v", err)
	}
	unsafe := artistbio.CandidateValidation{
		ArtistID: "a", CandidateID: id, RussianText: strings.Repeat("English translation text. ", 5),
		DetectedLanguage: "en", LanguageConfidence: 0.99, RussianShare: 1, Status: artistbio.CandidateAccepted,
	}
	if err := store.ApplyValidationAndSelect(ctx, unsafe); err == nil {
		t.Fatal("non-russian accepted text passed store defense")
	}
}

func TestStoreAcceptedCandidateIsNotOverwrittenByUnvalidatedRefresh(t *testing.T) {
	database := openTestDB(t)
	store, _ := artistbio.NewStore(database)
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved}); err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("Российская музыкальная группа записала несколько альбомов и дала много концертов. ", 2)
	id, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderDeezer, SourceArtistID: "1",
		OriginalText: original, DetectedLanguage: "ru", LanguageConfidence: 0.99,
		RussianShare: 1, MatchConfidence: 0.99, Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(ctx, "artist", id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutCandidate(ctx, artistbio.Candidate{
		ArtistID: "artist", Provider: artistbio.ProviderDeezer, SourceArtistID: "1",
		OriginalText: "unvalidated replacement", MatchConfidence: 1, Status: artistbio.CandidateFetched,
	}); err != nil {
		t.Fatal(err)
	}
	effective, found, err := store.EffectiveBiography(ctx, "artist")
	if err != nil || !found || effective.Text != original {
		t.Fatalf("unvalidated refresh replaced selected text: %#v found=%v err=%v", effective, found, err)
	}
}

func TestStoreConcurrentSelection(t *testing.T) {
	database := openTestDB(t)
	store, _ := artistbio.NewStore(database)
	ctx := context.Background()
	if err := store.PutIdentity(ctx, artistbio.ArtistIdentity{ArtistID: "artist", Status: artistbio.ResolutionResolved}); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 2)
	for index := range ids {
		id, err := store.PutCandidate(ctx, artistbio.Candidate{
			ArtistID: "artist", Provider: artistbio.ProviderDeezer,
			SourceArtistID: string(rune('a' + index)), OriginalText: strings.Repeat("Российская группа выпустила альбом и выступала на фестивалях. ", 2),
			MatchConfidence: 0.95 + float64(index)/100, Status: artistbio.CandidateFetched,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[index] = id
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(candidateID int64) {
			defer wg.Done()
			_ = store.ApplyValidationAndSelect(ctx, artistbio.CandidateValidation{
				ArtistID: "artist", CandidateID: candidateID, DetectedLanguage: "ru",
				LanguageConfidence: 0.99, RussianShare: 1, Status: artistbio.CandidateAccepted,
			})
		}(id)
	}
	wg.Wait()
	selected, found, err := store.AutomaticCandidate(ctx, "artist")
	if err != nil || !found || selected.ID != ids[1] {
		t.Fatalf("deterministic winner mismatch: %#v found=%v err=%v", selected, found, err)
	}
}
