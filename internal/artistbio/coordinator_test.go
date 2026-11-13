package artistbio_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/db"
	_ "modernc.org/sqlite"
)

type completingArtistPipeline struct {
	store *artistbio.Store
	mu    sync.Mutex
	calls int
}

func (p *completingArtistPipeline) Run(ctx context.Context, artist db.LibraryArtist) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if err := p.store.MarkJobRunning(ctx, artist.ID, time.Now()); err != nil {
		return err
	}
	return p.store.CompleteNoData(ctx, artist.ID, "empty", time.Now())
}

func TestCoordinatorCoalescesRequestsAndPersistsTerminalState(t *testing.T) {
	store, err := artistbio.NewStore(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	navPath := filepath.Join(t.TempDir(), "navidrome.db")
	nav, err := sql.Open("sqlite", navPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nav.Exec(`CREATE TABLE artist(id TEXT PRIMARY KEY, name TEXT NOT NULL); INSERT INTO artist VALUES ('artist','Артист')`); err != nil {
		t.Fatal(err)
	}
	_ = nav.Close()
	openNav := func() (*sql.DB, error) { return sql.Open("sqlite", navPath) }
	pipeline := &completingArtistPipeline{store: store}
	coordinator, err := artistbio.NewCoordinator(store, openNav, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done1, state, err := coordinator.Request(ctx, "artist", false)
	if err != nil || state.Status != artistbio.JobQueued {
		t.Fatalf("first request: state=%#v err=%v", state, err)
	}
	done2, _, err := coordinator.Request(ctx, "artist", true)
	if err != nil || done1 != done2 {
		t.Fatalf("concurrent rebuild did not join: same=%v err=%v", done1 == done2, err)
	}
	coordinator.Start(ctx)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("detached job did not finish")
	}
	pipeline.mu.Lock()
	calls := pipeline.calls
	pipeline.mu.Unlock()
	if calls != 1 {
		t.Fatalf("pipeline ran %d times", calls)
	}
	terminal, found, err := store.JobState(ctx, "artist")
	if err != nil || !found || terminal.Status != artistbio.JobNoData {
		t.Fatalf("terminal state: %#v found=%v err=%v", terminal, found, err)
	}
	done3, _, err := coordinator.Request(ctx, "artist", false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done3:
	default:
		t.Fatal("terminal request should return immediately")
	}
}
