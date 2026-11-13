package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/db"
	_ "modernc.org/sqlite"
)

func biographyAPIServer(t *testing.T) (*AdminServer, *artistbio.Store) {
	t.Helper()
	navPath := filepath.Join(t.TempDir(), "navidrome.db")
	nav, err := sql.Open("sqlite", navPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nav.Exec(`CREATE TABLE artist(id TEXT PRIMARY KEY, name TEXT NOT NULL); INSERT INTO artist(id,name) VALUES ('kino','Кино'),('other','Другой');`); err != nil {
		t.Fatal(err)
	}
	_ = nav.Close()
	poisq, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = poisq.Close() })
	store, _ := artistbio.NewStore(poisq)
	server := NewAdminServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, func() (*sql.DB, error) {
		return sql.Open("sqlite", navPath)
	})
	server.SetBiographyStore(store)
	return server, store
}

func biographyRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, "http://localhost:4535"+target, strings.NewReader(body))
	req.Host = "localhost:4535"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestArtistBiographyAPIListAndDetail(t *testing.T) {
	server, store := biographyAPIServer(t)
	list := httptest.NewRecorder()
	server.ServeHTTP(list, biographyRequest(http.MethodGet, "/api/v1/artist-biographies?query=%D0%BA%D0%B8%D0%BD%D0%BE", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"kino"`) {
		t.Fatalf("list: code=%d body=%s", list.Code, list.Body.String())
	}
	if err := store.PutIdentity(context.Background(), artistbio.ArtistIdentity{ArtistID: "kino", ArtistName: "Кино", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	id, err := store.PutCandidate(context.Background(), artistbio.Candidate{
		ArtistID: "kino", Provider: artistbio.ProviderWikipedia, SourceArtistID: "wiki",
		SourceURL: "https://ru.wikipedia.org/wiki/Kino", OriginalText: strings.Repeat("Российская группа выпустила альбом и выступала на фестивалях. ", 2),
		DetectedLanguage: "ru", LanguageConfidence: 0.99, RussianShare: 1, MatchConfidence: 1,
		Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(context.Background(), "kino", id, time.Now()); err != nil {
		t.Fatal(err)
	}
	detail := httptest.NewRecorder()
	server.ServeHTTP(detail, biographyRequest(http.MethodGet, "/api/v1/artist-biographies/detail?id=kino", ""))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"manualActive":false`) || !strings.Contains(detail.Body.String(), `"automatic"`) {
		t.Fatalf("detail: code=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestArtistBiographyAPISaveResetAndCSRF(t *testing.T) {
	server, store := biographyAPIServer(t)
	if err := store.PutIdentity(context.Background(), artistbio.ArtistIdentity{ArtistID: "kino", ArtistName: "Кино", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	automaticText := strings.Repeat("Российская рок-группа выпустила альбом и выступала на крупных фестивалях. ", 2)
	candidateID, err := store.PutCandidate(context.Background(), artistbio.Candidate{
		ArtistID: "kino", Provider: artistbio.ProviderDeezer, SourceArtistID: "42",
		SourceURL: "https://www.deezer.com/artist/42", OriginalText: automaticText,
		DetectedLanguage: "ru", LanguageConfidence: 0.99, RussianShare: 1, MatchConfidence: 1,
		Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(context.Background(), "kino", candidateID, time.Now()); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("Российская музыкальная группа записала известный альбом и выступала на крупных фестивалях. ", 2)
	payload, _ := json.Marshal(map[string]string{"artistId": "kino", "text": text, "sourceUrl": "https://example.com/kino"})
	saveReq := biographyRequest(http.MethodPut, "/api/v1/artist-biographies/detail", string(payload))
	saveReq.Header.Set("Origin", "http://localhost:4535")
	save := httptest.NewRecorder()
	server.ServeHTTP(save, saveReq)
	if save.Code != http.StatusOK || !strings.Contains(save.Body.String(), `"manualActive":true`) {
		t.Fatalf("save: code=%d body=%s", save.Code, save.Body.String())
	}
	effective, found, err := store.EffectiveBiography(context.Background(), "kino")
	if err != nil || !found || !effective.Manual || effective.Text != strings.TrimSpace(text) {
		t.Fatalf("effective manual: %#v found=%v err=%v", effective, found, err)
	}

	csrfReq := biographyRequest(http.MethodPut, "/api/v1/artist-biographies/detail", string(payload))
	csrfReq.Header.Set("Origin", "https://evil.example")
	csrf := httptest.NewRecorder()
	server.ServeHTTP(csrf, csrfReq)
	if csrf.Code != http.StatusForbidden {
		t.Fatalf("CSRF code=%d", csrf.Code)
	}

	resetReq := biographyRequest(http.MethodDelete, "/api/v1/artist-biographies/detail?id=kino", "")
	resetReq.Header.Set("Origin", "http://localhost:4535")
	reset := httptest.NewRecorder()
	server.ServeHTTP(reset, resetReq)
	if reset.Code != http.StatusOK || strings.Contains(reset.Body.String(), `"manualActive":true`) {
		t.Fatalf("reset: code=%d body=%s", reset.Code, reset.Body.String())
	}
	restored, found, err := store.EffectiveBiography(context.Background(), "kino")
	if err != nil || !found || restored.Manual || restored.Text != automaticText || restored.CandidateID != candidateID {
		t.Fatalf("automatic biography was not restored exactly: %#v found=%v err=%v", restored, found, err)
	}
	if _, found, err := store.Candidate(context.Background(), "kino", artistbio.ProviderDeezer, "42"); err != nil || !found {
		t.Fatalf("automatic candidate was removed: found=%v err=%v", found, err)
	}
}

func TestArtistBiographyAPIStrictJSONAndLanguage(t *testing.T) {
	server, _ := biographyAPIServer(t)
	tests := []string{
		`{}`,
		`{"artistId":"kino","text":"short","unknown":true}`,
		`{"artistId":"kino","artistId":"other","text":"` + strings.Repeat("Русский текст ", 10) + `"}`,
		`{"artistId":"kino","text":"` + strings.Repeat("The English biography describes an artist and albums. ", 3) + `"}`,
		`{"artistId":"kino","text":"Український музичний гурт був заснований у Києві. Його учасники створили кілька відомих альбомів і виступали на фестивалях України."}`,
		`{"artistId":"kino","text":"Беларускі музычны гурт быў заснаваны ў Мінску. Яго ўдзельнікі запісалі некалькі вядомых альбомаў і выступалі на фестывалях Беларусі."}`,
		`{"artistId":"kino","text":"Российская группа выпустила новый studio album and performed at European festivals вместе с известными музыкантами."}`,
		`{"artistId":"kino","text":"` + strings.Repeat("слово слово слово слово ", 30) + `"}`,
		`{"artistId":"kino","text":"<script>alert(1)</script>` + strings.Repeat(" российская музыкальная группа", 10) + `"}`,
		`{"artistId":"kino","text":"` + strings.Repeat("я", artistbio.MaxBiographyBytes+1) + `"}`,
		`{"artistId":"kino","text":"` + strings.Repeat("Российская группа записала альбом и выступала на фестивалях. ", 3) + `","sourceUrl":"file:///etc/passwd"}`,
		string([]byte{'{', '"', 'a', 'r', 't', 'i', 's', 't', 'I', 'd', '"', ':', '"', 'k', 'i', 'n', 'o', '"', ',', '"', 't', 'e', 'x', 't', '"', ':', '"', 0xff, '"', '}'}),
	}
	for index, body := range tests {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, biographyRequest(http.MethodPut, "/api/v1/artist-biographies/detail", body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("case %d code=%d body=%s", index, recorder.Code, recorder.Body.String())
		}
	}
}

func TestArtistBiographyAPIMethodsAndUnavailable(t *testing.T) {
	server, _ := biographyAPIServer(t)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, biographyRequest(http.MethodPost, "/api/v1/artist-biographies/detail", `{}`))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method code=%d", recorder.Code)
	}
	unavailable := NewAdminServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	recorder = httptest.NewRecorder()
	unavailable.ServeHTTP(recorder, biographyRequest(http.MethodGet, "/api/v1/artist-biographies/detail?id=kino", ""))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable code=%d", recorder.Code)
	}
}

func TestArtistBiographyAPIConcurrentReads(t *testing.T) {
	server, store := biographyAPIServer(t)
	if err := store.PutIdentity(context.Background(), artistbio.ArtistIdentity{ArtistID: "kino", ArtistName: "Кино", Status: artistbio.ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	automaticText := strings.Repeat("Российская группа записала альбом и выступила на музыкальном фестивале. ", 2)
	candidateID, err := store.PutCandidate(context.Background(), artistbio.Candidate{
		ArtistID: "kino", Provider: artistbio.ProviderWikipedia, SourceArtistID: "kino",
		SourceURL: "https://ru.wikipedia.org/wiki/Kino", OriginalText: automaticText,
		DetectedLanguage: "ru", LanguageConfidence: 0.99, RussianShare: 1, MatchConfidence: 1,
		Status: artistbio.CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(context.Background(), "kino", candidateID, time.Now()); err != nil {
		t.Fatal(err)
	}
	manualText := strings.TrimSpace(strings.Repeat("Российский музыкальный коллектив выпустил известную пластинку и дал серию концертов. ", 2))
	payload, _ := json.Marshal(map[string]string{"artistId": "kino", "text": manualText})

	start := make(chan struct{})
	errors := make(chan error, 4)
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for range 25 {
				effective, found, err := store.EffectiveBiography(context.Background(), "kino")
				if err != nil || !found {
					errors <- fmt.Errorf("concurrent read: found=%v err=%w", found, err)
					return
				}
				if effective.Text != automaticText && effective.Text != manualText {
					errors <- fmt.Errorf("observed partial biography")
					return
				}
			}
		}()
	}
	close(start)
	for range 5 {
		save := httptest.NewRecorder()
		server.ServeHTTP(save, biographyRequest(http.MethodPut, "/api/v1/artist-biographies/detail", string(payload)))
		if save.Code != http.StatusOK {
			t.Fatalf("save during concurrent reads: code=%d body=%s", save.Code, save.Body.String())
		}
		reset := httptest.NewRecorder()
		server.ServeHTTP(reset, biographyRequest(http.MethodDelete, "/api/v1/artist-biographies/detail?id=kino", ""))
		if reset.Code != http.StatusOK {
			t.Fatalf("reset during concurrent reads: code=%d body=%s", reset.Code, reset.Body.String())
		}
	}
	readers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}
