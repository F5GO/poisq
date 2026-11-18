package artistbio

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/logger"
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
	maxJobRetries int
	retryDelay    time.Duration

	mu      sync.Mutex
	waiters map[string]chan struct{}
}

func NewCoordinator(store *Store, openNavDB func() (*sql.DB, error), pipeline artistPipeline) (*Coordinator, error) {
	if store == nil || openNavDB == nil || pipeline == nil {
		return nil, fmt.Errorf("biography coordinator requires store, artist inventory, and pipeline")
	}
	coordinator := &Coordinator{
		store: store, openNavDB: openNavDB, pipeline: pipeline,
		queue: make(chan db.LibraryArtist, defaultBiographyQueueSize), jobTimeout: 150 * time.Second,
		startInterval: 20 * time.Second, maxJobRetries: 2, retryDelay: 15 * time.Second,
		waiters: make(map[string]chan struct{}),
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
	c.mu.Lock()
	if existing := c.waiters[artist.ID]; existing != nil {
		c.mu.Unlock()
		state, _, _ := c.store.JobState(ctx, artist.ID)
		return existing, state, nil
	}
	c.mu.Unlock()
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
			for attempt := 0; attempt <= c.maxJobRetries; attempt++ {
				jobCtx, cancel := context.WithTimeout(ctx, c.jobTimeout)
				err := c.pipeline.Run(jobCtx, artist)
				cancel()
				if err == nil || attempt == c.maxJobRetries || ctx.Err() != nil || safePipelineErrorReason(err) == "collector_empty" {
					break
				}
				logger.Info("[BIO] artist_id=%q artist=%q retry=%d/%d after=%s error=%v", artist.ID, artist.Name, attempt+1, c.maxJobRetries, c.retryDelay, err)
				retryTimer := time.NewTimer(c.retryDelay)
				select {
				case <-ctx.Done():
					retryTimer.Stop()
					c.finish(artist.ID)
					return
				case <-retryTimer.C:
				}
				lastStart = time.Now()
			}
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
