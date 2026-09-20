package retention

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/app"
	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// ---------------------------------------------------------------------------
// fakeQueue: a minimal app.Queue double. Only PurgeTerminalBefore is
// exercised by Sweeper; every other method is unused and returns a zero
// value.
// ---------------------------------------------------------------------------

type fakeQueue struct {
	mu sync.Mutex

	purgeFn     func(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error)
	purgeCalls  int
	lastCutoffs []time.Time
}

var _ app.Queue = (*fakeQueue)(nil)

func newFakeQueue() *fakeQueue {
	return &fakeQueue{}
}

func (q *fakeQueue) PurgeTerminalBefore(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
	q.mu.Lock()
	q.purgeCalls++
	q.lastCutoffs = append(q.lastCutoffs, cutoff)
	fn := q.purgeFn
	q.mu.Unlock()
	if fn != nil {
		return fn(ctx, cutoff)
	}
	return nil, nil
}

// purgeCallCount is a lock-guarded read, needed by any test driving
// Sweeper.Run from a separate goroutine than the one asserting against this
// fake's state.
func (q *fakeQueue) purgeCallCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.purgeCalls
}

func (q *fakeQueue) lastCutoff() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.lastCutoffs) == 0 {
		return time.Time{}
	}
	return q.lastCutoffs[len(q.lastCutoffs)-1]
}

func (q *fakeQueue) Claim(ctx context.Context, owner string, lease time.Duration) (*domain.Transcription, error) {
	return nil, nil
}
func (q *fakeQueue) Heartbeat(ctx context.Context, id, owner string, lease time.Duration) (bool, error) {
	return false, nil
}
func (q *fakeQueue) Finish(ctx context.Context, req domain.FinishRequest) error { return nil }
func (q *fakeQueue) ReleaseExpired(ctx context.Context) (int, error)            { return 0, nil }
func (q *fakeQueue) ReleaseForShutdown(ctx context.Context, id, leaseToken string) error {
	return nil
}

// ---------------------------------------------------------------------------
// fakeStore: a minimal app.Store double. Only RemoveDir is exercised by
// Sweeper; every other method is unused and returns a zero value.
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	removeDirErr map[string]error
	removedDirs  []string
}

var _ app.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{removeDirErr: make(map[string]error)}
}

func (s *fakeStore) RemoveDir(ctx context.Context, relDir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removedDirs = append(s.removedDirs, relDir)
	if err, ok := s.removeDirErr[relDir]; ok {
		return err
	}
	return nil
}

// removedDirsSnapshot is a lock-guarded read; see fakeQueue.purgeCallCount
// for why this matters for a Sweeper.Run-driven test.
func (s *fakeStore) removedDirsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removedDirs...)
}

func (s *fakeStore) Put(ctx context.Context, relPath string, r io.Reader) (int64, error) {
	return 0, nil
}
func (s *fakeStore) Open(ctx context.Context, relPath string) (io.ReadSeekCloser, int64, error) {
	return nil, 0, fmt.Errorf("fakeStore: Open not used")
}
func (s *fakeStore) AbsPath(relPath string) (string, error)           { return "", nil }
func (s *fakeStore) Remove(ctx context.Context, relPath string) error { return nil }
func (s *fakeStore) FreeBytes() (uint64, error)                       { return 0, nil }

// ---------------------------------------------------------------------------
// fakeClock
// ---------------------------------------------------------------------------

// fakeClock is a manually-advanced app.Clock, so the cutoff Sweeper computes
// doesn't depend on when the test happened to run.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

var _ app.Clock = (*fakeClock)(nil)

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
