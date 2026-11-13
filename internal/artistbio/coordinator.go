package artistbio

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/db"
)

const defaultBiographyQueueSize = 100

type artistPipeline interface {
	Run(ctx context.Context, artist db.LibraryArtist) error
}

type Coordinator struct {
	store         *Store
	openNavDB     func() (*sql.DB, error)
	pipeline      artistPipeline
	queue         chan db.LibraryArtist
	jobTimeout    time.Duration
	startInterval time.Duration

	mu      sync.Mutex
	waiters map[string]chan struct{}
}

func NewCoordinator(store *Store, openNavDB func() (*sql.DB, error), pipeline artistPipeline) (*Coordinator, error) {
	if store == nil || openNavDB == nil || pipeline == nil {
		return nil, fmt.Errorf("biography coordinator requires store, artist inventory, and pipeline")
	}
	coordinator := &Coordinator{
		store: store, openNavDB: openNavDB, pipeline: pipeline,
		queue: make(chan db.LibraryArtist, defaultBiographyQueueSize), jobTimeout: 90 * time.Second,
		startInterval: 20 * time.Second, waiters: make(map[string]chan struct{}),
	}
	if _, err := store.RecoverStaleJobs(context.Background(), time.Now(), time.Now()); err != nil {
		return nil, err
	}
	return coordinator, nil
}

func (c *Coordinator) Start(ctx context.Context) {
	go c.run(ctx)
}

// Request validates the artist against Navidrome before quota-consuming work.
// The returned channel closes when the accepted/joined detached job finishes.
func (c *Coordinator) Request(ctx context.Context, artistID string, force bool) (<-chan struct{}, JobState, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return nil, JobState{}, err
	}
	database, err := c.openNavDB()
	if err != nil || database == nil {
		return nil, JobState{}, fmt.Errorf("artist inventory is unavailable")
	}
	artist, found, findErr := db.FindLibraryArtist(ctx, database, artistID)
	_ = database.Close()
	if findErr != nil || !found {
		return nil, JobState{}, fmt.Errorf("artist is not present in inventory")
	}
	state, admitted, err := c.store.QueueJob(ctx, artist.ID, artist.Name, force, time.Now())
	if err != nil {
		return nil, JobState{}, err
	}
	c.mu.Lock()
	if existing := c.waiters[artist.ID]; existing != nil {
		c.mu.Unlock()
		return existing, state, nil
	}
	if !admitted {
		done := make(chan struct{})
		close(done)
		c.mu.Unlock()
		return done, state, nil
	}
	done := make(chan struct{})
	c.waiters[artist.ID] = done
	c.mu.Unlock()
	select {
	case c.queue <- artist:
		return done, state, nil
	default:
		_ = c.store.MarkJobRetryable(context.Background(), artist.ID, "queue_full", time.Now().Add(time.Minute), time.Now())
		c.finish(artist.ID)
		return done, state, fmt.Errorf("biography queue is full")
	}
}

func (c *Coordinator) run(ctx context.Context) {
	var lastStart time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case artist := <-c.queue:
			if delay := c.startInterval - time.Since(lastStart); !lastStart.IsZero() && delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			lastStart = time.Now()
			jobCtx, cancel := context.WithTimeout(ctx, c.jobTimeout)
			_ = c.pipeline.Run(jobCtx, artist)
			cancel()
			c.finish(artist.ID)
		}
	}
}

func (c *Coordinator) finish(artistID string) {
	c.mu.Lock()
	if done := c.waiters[artistID]; done != nil {
		delete(c.waiters, artistID)
		close(done)
	}
	c.mu.Unlock()
}
