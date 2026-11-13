package artistbio_test

import (
	"context"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/artistbio"
)

func TestStore_JobLifecycleAndCooldown(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	state, admitted, err := store.QueueJob(ctx, "artist", "Артист", false, now)
	if err != nil || !admitted || state.Status != artistbio.JobQueued {
		t.Fatalf("initial admission: state=%#v admitted=%v err=%v", state, admitted, err)
	}
	if _, admitted, err := store.QueueJob(ctx, "artist", "Артист", false, now.Add(time.Second)); err != nil || admitted {
		t.Fatalf("active job was admitted twice: admitted=%v err=%v", admitted, err)
	}
	if _, admitted, err := store.QueueJob(ctx, "artist", "Артист", true, now.Add(time.Second)); err != nil || admitted {
		t.Fatalf("forced request did not join active job: admitted=%v err=%v", admitted, err)
	}
	if err := store.MarkJobRunning(ctx, "artist", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(time.Hour)
	if err := store.MarkJobRetryable(ctx, "artist", "quota_exhausted", retryAt, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(ctx, "artist", "Артист", false, now.Add(4*time.Second)); err != nil || admitted {
		t.Fatalf("cooldown was ignored: admitted=%v err=%v", admitted, err)
	}
	state, admitted, err = store.QueueJob(ctx, "artist", "Артист", true, now.Add(5*time.Second))
	if err != nil || !admitted || state.Status != artistbio.JobQueued || state.LastError != "" {
		t.Fatalf("forced rebuild: state=%#v admitted=%v err=%v", state, admitted, err)
	}
	if err := store.MarkJobRunning(ctx, "artist", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteNoData(ctx, "artist", "version-1", now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.JobState(ctx, "artist")
	if err != nil || !found || state.Status != artistbio.JobNoData || state.InputVersion != "version-1" {
		t.Fatalf("terminal no-data state: state=%#v found=%v err=%v", state, found, err)
	}
	if _, admitted, err := store.QueueJob(ctx, "artist", "Артист", false, now.Add(2*time.Hour)); err != nil || admitted {
		t.Fatalf("terminal state triggered repeated work: admitted=%v err=%v", admitted, err)
	}
}

func TestStore_CommitAISummaryIsAtomicAndPreservesManual(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, admitted, err := store.QueueJob(ctx, "artist", "Кино", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue: admitted=%v err=%v", admitted, err)
	}
	if err := store.SetManualOverride(ctx, artistbio.ManualOverride{
		ArtistID: "artist", Text: "Ручное описание исполнителя содержит проверенные пользователем сведения о музыкальном творчестве.",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkJobRunning(ctx, "artist", time.Now()); err != nil {
		t.Fatal(err)
	}
	summary := "Российская рок-группа получила известность благодаря самобытному звучанию и песням, вошедшим в историю отечественной музыки. Саммари создано ИИ на основе проверенных источников: Wikipedia, Яндекс Музыка."
	candidateID, err := store.CommitAISummary(ctx, "artist", "version-1", summary, `{"sources":["wikipedia","yandex_music"]}`, time.Now())
	if err != nil || candidateID <= 0 {
		t.Fatalf("commit summary: id=%d err=%v", candidateID, err)
	}
	effective, found, err := store.EffectiveBiography(ctx, "artist")
	if err != nil || !found || !effective.Manual || effective.Source != artistbio.ProviderManual {
		t.Fatalf("manual priority was lost: %#v found=%v err=%v", effective, found, err)
	}
	if err := store.SetManualOverride(ctx, artistbio.ManualOverride{ArtistID: "artist"}); err != nil {
		t.Fatal(err)
	}
	effective, found, err = store.EffectiveBiography(ctx, "artist")
	if err != nil || !found || effective.Text != summary || effective.Source != artistbio.ProviderAISummary {
		t.Fatalf("AI summary was not selected: %#v found=%v err=%v", effective, found, err)
	}
	state, found, err := store.JobState(ctx, "artist")
	if err != nil || !found || state.Status != artistbio.JobReady || state.InputVersion != "version-1" {
		t.Fatalf("job did not commit atomically: %#v found=%v err=%v", state, found, err)
	}
}

func TestStore_CleansLegacyLocalLibraryAndSelectsExternal(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `INSERT INTO artist_bio_identity
		(artist_id, artist_name, status, evidence_json) VALUES ('artist', 'Артист', 'resolved', '{}')`); err != nil {
		t.Fatal(err)
	}
	result, err := database.ExecContext(ctx, `INSERT INTO artist_bio_candidates
		(artist_id, provider, source_artist_id, original_text, original_language, detected_language,
		 language_confidence, russian_share, match_confidence, status, evidence_json)
		VALUES ('artist', 'local_library', 'artist', 'Локальная справка', 'ru', 'ru', 1, 1, 1, 'accepted', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	localID, _ := result.LastInsertId()
	result, err = database.ExecContext(ctx, `INSERT INTO artist_bio_candidates
		(artist_id, provider, source_artist_id, original_text, original_language, detected_language,
		 language_confidence, russian_share, match_confidence, status, evidence_json)
		VALUES ('artist', 'wikipedia', 'wiki', 'Настоящее внешнее описание исполнителя и его музыкальной деятельности.', 'ru', 'ru', 1, 1, 0.95, 'accepted', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	externalID, _ := result.LastInsertId()
	if _, err := database.ExecContext(ctx, `INSERT INTO artist_biographies (artist_id, automatic_candidate_id) VALUES ('artist', ?)`, localID); err != nil {
		t.Fatal(err)
	}
	store, err := artistbio.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	selected, found, err := store.AutomaticCandidate(ctx, "artist")
	if err != nil || !found || selected.ID != externalID || selected.Provider != artistbio.ProviderWikipedia {
		t.Fatalf("external replacement not selected: %#v found=%v err=%v", selected, found, err)
	}
	var localCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_bio_candidates WHERE provider = 'local_library'`).Scan(&localCount); err != nil || localCount != 0 {
		t.Fatalf("legacy candidate survived: count=%d err=%v", localCount, err)
	}
	cleanup, err := store.CleanupLegacyLocalLibrary(ctx)
	if err != nil || cleanup.Candidates != 0 || cleanup.Selections != 0 {
		t.Fatalf("cleanup is not idempotent: %#v err=%v", cleanup, err)
	}
}

func TestStore_RecoversStaleJobsWithoutStartingWork(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	if _, admitted, err := store.QueueJob(ctx, "artist", "Артист", false, old); err != nil || !admitted {
		t.Fatalf("queue stale job: admitted=%v err=%v", admitted, err)
	}
	recovered, err := store.RecoverStaleJobs(ctx, time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recover: count=%d err=%v", recovered, err)
	}
	state, found, err := store.JobState(ctx, "artist")
	if err != nil || !found || state.Status != artistbio.JobRetryable || state.LastError != "restart_recovery" {
		t.Fatalf("recovered state: %#v found=%v err=%v", state, found, err)
	}
}
