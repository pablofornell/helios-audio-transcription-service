package jobs

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
// fakeQueue: an in-memory app.Queue double. It deliberately claims strictly
// one row per call, mirroring app.Queue.Claim's real (nil, nil)-means-idle
// contract — no test here needs batch claiming, and Pool's own concurrency
// ceiling is exercised by repeated single claims instead (see
// TestPool_ConcurrencyCeiling).
//
// Heartbeat and Finish honestly enforce the real lease guard: a row's
// status must be StatusRunning AND the caller-supplied lease token must
// match the token the most recent Claim minted for that row (see
// Transcription.LeaseToken), or the call fails with ErrCodeConflict. This
// is deliberate and load-bearing: an earlier version of this fake ignored
// lease identity entirely (it only ever checked the owner string, which is
// shared by every claim a Pool ever makes, or nothing at all), which made
// every lease assertion in this package vacuous -- inverting the guard in
// Pool itself left the whole suite green. See
// TestFakeQueue_StaleLeaseToken_RejectedByHeartbeatAndFinish.
//
// Finish and ReleaseForShutdown also honestly enforce ctx: a call made with
// an already-done context returns ctx.Err() rather than silently
// succeeding, exactly like the real Postgres-backed pool.Exec would (pgx
// checks ctx before ever reaching the network). This is equally
// load-bearing and for the same reason as the lease guard: an earlier
// version of this fake ignored ctx entirely, which meant replacing
// job.go's `context.WithTimeout(context.WithoutCancel(jobCtx), ...)` with
// `context.WithTimeout(jobCtx, ...)` -- silently turning every
// forced-shutdown Finish/ReleaseForShutdown into a write against an
// already-canceled context that would never actually reach the database --
// left this whole suite green. See
// TestFakeQueue_Finish_RejectsAlreadyDoneContext and
// TestPool_Shutdown_ForceCancelsAfterDeadline's succeedID case.
// ---------------------------------------------------------------------------

type fakeQueue struct {
	mu sync.Mutex

	rows       map[string]*domain.Transcription
	claimQueue []string // FIFO of ids still pending and claimable

	claimErr   error
	claimCount int
	tokenSeq   int // source of fresh, unique lease tokens minted by Claim

	// claimDelay, when set, makes Claim sleep (real time.Sleep, deliberately
	// NOT synctest-virtualized) before returning -- widening the window a
	// real database round trip would have, for stress-testing the race
	// FIX 4 closes (see TestPool_Shutdown_NeverStartsJobClaimedDuringStopping).
	claimDelay time.Duration

	// heartbeatSeq[id] scripts what successive Heartbeat calls for id
	// return; once exhausted, the last entry repeats. A nil/empty sequence
	// means "never canceled, no error".
	heartbeatSeq map[string][]hbResult
	heartbeatN   map[string]int

	releaseExpiredFn func() (int, error)
	releaseExpiredN  int

	finishCalls []domain.FinishRequest
	finishErr   map[string]error

	releaseForShutdownCalls []releaseForShutdownCall
	releaseForShutdownErr   map[string]error
}

// releaseForShutdownCall records one ReleaseForShutdown attempt, mirroring
// finishCalls's "record every attempt, including rejected ones" role.
type releaseForShutdownCall struct {
	id         string
	leaseToken string
}

type hbResult struct {
	canceled bool
	err      error
}

var _ app.Queue = (*fakeQueue)(nil)

func newFakeQueue() *fakeQueue {
	return &fakeQueue{
		rows:                  make(map[string]*domain.Transcription),
		heartbeatSeq:          make(map[string][]hbResult),
		heartbeatN:            make(map[string]int),
		finishErr:             make(map[string]error),
		releaseForShutdownErr: make(map[string]error),
	}
}

// seed inserts t (cloned) as a pending, claimable row.
func (q *fakeQueue) seed(t *domain.Transcription) {
	q.mu.Lock()
	defer q.mu.Unlock()
	c := *t
	q.rows[c.ID] = &c
	q.claimQueue = append(q.claimQueue, c.ID)
}

// Claim mints a fresh lease token for the row it claims — a monotonically
// increasing counter is good enough for a fake, real Claim implementations
// mint a UUID — and stores it as the row's ONLY current lease credential.
// It is deliberately unrelated to owner: two Claims made under the same
// owner string (a restarted or re-claiming pool, see finding 1) still get
// distinct tokens, which is the entire property this fake exists to model.
func (q *fakeQueue) Claim(ctx context.Context, owner string, lease time.Duration) (*domain.Transcription, error) {
	q.mu.Lock()
	delay := q.claimDelay
	q.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.claimCount++
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	if len(q.claimQueue) == 0 {
		return nil, nil
	}

	id := q.claimQueue[0]
	q.claimQueue = q.claimQueue[1:]

	q.tokenSeq++
	token := fmt.Sprintf("lease-token-%d", q.tokenSeq)

	row := q.rows[id]
	row.Status = domain.StatusRunning
	row.Attempts++
	o := owner
	row.LeaseOwner = &o
	row.LeaseToken = &token

	c := *row
	return &c, nil
}

// checkLease reports whether leaseToken is still the current lease credential
// for id and the row is running -- the same predicate a real
// `WHERE id=$1 AND lease_token=$2 AND status='running'` guards on. Callers
// must hold q.mu.
func (q *fakeQueue) checkLease(id, leaseToken string) error {
	row, ok := q.rows[id]
	if !ok {
		return domain.New(domain.ErrCodeNotFound, "fakeQueue: unknown id").WithDetails("id", id)
	}
	if row.Status != domain.StatusRunning || row.LeaseToken == nil || *row.LeaseToken != leaseToken {
		return domain.New(domain.ErrCodeConflict, "fakeQueue: lease token mismatch or row not running").
			WithDetails("id", id)
	}
	return nil
}

// Heartbeat rejects a leaseToken that is not the row's CURRENT token with
// ErrCodeConflict, exactly like the real guard -- a stale execution
// carrying a previous claim's token (even under the same owner string) is
// refused, never allowed to extend a lease it no longer holds.
func (q *fakeQueue) Heartbeat(ctx context.Context, id, leaseToken string, lease time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := q.checkLease(id, leaseToken); err != nil {
		return false, err
	}

	n := q.heartbeatN[id]
	q.heartbeatN[id] = n + 1

	seq := q.heartbeatSeq[id]
	if len(seq) == 0 {
		return false, nil
	}
	if n >= len(seq) {
		n = len(seq) - 1
	}
	res := seq[n]
	return res.canceled, res.err
}

// Finish rejects a req.LeaseToken that is not the row's CURRENT token, or a
// row that is not currently running, with ErrCodeConflict -- the same
// guard Heartbeat applies, and for the same reason (see checkLease). It
// also rejects an already-done ctx with ctx.Err(), checked first and
// BEFORE the lease guard -- see this type's own doc for why that ctx
// honesty is exactly as load-bearing as the lease guard itself. finishCalls
// records every attempt, including rejected ones, so tests can assert on
// what a worker tried to write even when the write itself did not land.
func (q *fakeQueue) Finish(ctx context.Context, req domain.FinishRequest) error {
	if err := ctx.Err(); err != nil {
		q.mu.Lock()
		q.finishCalls = append(q.finishCalls, req)
		q.mu.Unlock()
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.finishCalls = append(q.finishCalls, req)
	if err, ok := q.finishErr[req.ID]; ok {
		return err
	}
	if err := q.checkLease(req.ID, req.LeaseToken); err != nil {
		return err
	}

	row := q.rows[req.ID]
	row.Status = req.Status
	row.Error = req.Err
	row.Result = req.Result
	if req.Audio != nil {
		row.Audio = *req.Audio
	}
	row.NextAttemptAt = req.NextAttemptAt
	// Every Finish outcome -- terminal or a scheduled retry -- ends this
	// claim: the lease credential it was guarded by must never be reusable
	// again, matching the real Finish's contract of always clearing it.
	row.LeaseOwner = nil
	row.LeaseToken = nil
	return nil
}

// ReleaseForShutdown mirrors Finish exactly: ctx honesty first (see this
// type's own doc), then the same lease guard (checkLease), then -- on
// success -- the row goes back to StatusPending WITHOUT its Attempts being
// consumed, matching the real ReleaseForShutdown's contract (see
// app.Queue.ReleaseForShutdown).
func (q *fakeQueue) ReleaseForShutdown(ctx context.Context, id, leaseToken string) error {
	if err := ctx.Err(); err != nil {
		q.mu.Lock()
		q.releaseForShutdownCalls = append(q.releaseForShutdownCalls, releaseForShutdownCall{id: id, leaseToken: leaseToken})
		q.mu.Unlock()
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.releaseForShutdownCalls = append(q.releaseForShutdownCalls, releaseForShutdownCall{id: id, leaseToken: leaseToken})
	if err, ok := q.releaseForShutdownErr[id]; ok {
		return err
	}
	if err := q.checkLease(id, leaseToken); err != nil {
		return err
	}

	row := q.rows[id]
	row.Status = domain.StatusPending
	if row.Attempts > 0 {
		row.Attempts--
	}
	row.LeaseOwner = nil
	row.LeaseToken = nil
	q.claimQueue = append(q.claimQueue, id)
	return nil
}

func (q *fakeQueue) ReleaseExpired(ctx context.Context) (int, error) {
	q.mu.Lock()
	q.releaseExpiredN++
	fn := q.releaseExpiredFn
	q.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return 0, nil
}

// PurgeTerminalBefore is never called by this package; it exists only so
// fakeQueue satisfies app.Queue.
func (q *fakeQueue) PurgeTerminalBefore(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
	return nil, nil
}

// expireLease simulates what a lease-expiry sweep (app.Queue.ReleaseExpired)
// does to one specific row: return it to pending and clear its lease
// identity, so a subsequent Claim mints a brand new token. Tests use this
// to set up a "stale execution" -- a worker still holding the previous
// claim's token after the row has moved on -- without needing this fake to
// model real wall-clock lease timers.
func (q *fakeQueue) expireLease(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	row, ok := q.rows[id]
	if !ok {
		return
	}
	row.Status = domain.StatusPending
	row.LeaseOwner = nil
	row.LeaseToken = nil
	q.claimQueue = append(q.claimQueue, id)
}

// resetAttemptsAndReseed simulates what Store.Requeue does to a row after a
// manual retry (see postgres/repository.go's Requeue): Attempts is reset to
// 0 -- unlike an ordinary re-claim after a lease expiry, which INCREMENTS
// Attempts instead (see expireLease/Claim) -- and the row goes back to
// pending, claimable again. It exists so tests can reproduce the exact
// scenario FIX 3 is about: a retry's next claim can land on the SAME
// Attempts number a still-live stale execution from before the requeue
// used, which is precisely why wavRelPath must not be keyed on Attempts.
func (q *fakeQueue) resetAttemptsAndReseed(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	row, ok := q.rows[id]
	if !ok {
		return
	}
	row.Status = domain.StatusPending
	row.Attempts = 0
	row.LeaseOwner = nil
	row.LeaseToken = nil
	q.claimQueue = append(q.claimQueue, id)
}

func (q *fakeQueue) claims() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.claimCount
}

func (q *fakeQueue) finishesFor(id string) []domain.FinishRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []domain.FinishRequest
	for _, c := range q.finishCalls {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func (q *fakeQueue) totalFinishes() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.finishCalls)
}

// releaseForShutdownCallsFor returns every ReleaseForShutdown attempt made
// for id, in order, including rejected ones -- mirrors finishesFor.
func (q *fakeQueue) releaseForShutdownCallsFor(id string) []releaseForShutdownCall {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []releaseForShutdownCall
	for _, c := range q.releaseForShutdownCalls {
		if c.id == id {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// fakeStore: a minimal app.Store double. Only AbsPath and Remove are
// exercised by Pool; every other method is unused and returns a zero value.
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	absErr    map[string]error
	removeErr map[string]error
	removed   []string
}

var _ app.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{
		absErr:    make(map[string]error),
		removeErr: make(map[string]error),
	}
}

func (s *fakeStore) AbsPath(relPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.absErr[relPath]; ok {
		return "", err
	}
	return "/store/" + relPath, nil
}

func (s *fakeStore) Remove(ctx context.Context, relPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, relPath)
	if err, ok := s.removeErr[relPath]; ok {
		return err
	}
	return nil
}

func (s *fakeStore) removedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.removed...)
}

func (s *fakeStore) RemoveDir(ctx context.Context, relDir string) error { return nil }
func (s *fakeStore) Put(ctx context.Context, relPath string, r io.Reader) (int64, error) {
	return 0, nil
}
func (s *fakeStore) Open(ctx context.Context, relPath string) (io.ReadSeekCloser, int64, error) {
	return nil, 0, fmt.Errorf("fakeStore: Open not used")
}
func (s *fakeStore) FreeBytes() (uint64, error) { return 0, nil }

// ---------------------------------------------------------------------------
// fakeAudioProcessor: a minimal app.AudioProcessor double.
// ---------------------------------------------------------------------------

type fakeAudioProcessor struct {
	mu sync.Mutex

	probeFn     func(ctx context.Context, absPath string) (domain.Audio, error)
	normalizeFn func(ctx context.Context, srcAbs, dstAbs string) error

	probeCalls     []string
	normalizeCalls []normalizeCall
}

type normalizeCall struct {
	src, dst string
}

var _ app.AudioProcessor = (*fakeAudioProcessor)(nil)

func newFakeAudioProcessor() *fakeAudioProcessor {
	return &fakeAudioProcessor{}
}

func (a *fakeAudioProcessor) Probe(ctx context.Context, absPath string) (domain.Audio, error) {
	a.mu.Lock()
	a.probeCalls = append(a.probeCalls, absPath)
	fn := a.probeFn
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, absPath)
	}
	return domain.Audio{DurationSeconds: 12.5, SampleRateHz: 44100, Channels: 2, Codec: "mp3"}, nil
}

func (a *fakeAudioProcessor) Normalize(ctx context.Context, srcAbs, dstAbs string) error {
	a.mu.Lock()
	a.normalizeCalls = append(a.normalizeCalls, normalizeCall{srcAbs, dstAbs})
	fn := a.normalizeFn
	a.mu.Unlock()
	if fn != nil {
		return fn(ctx, srcAbs, dstAbs)
	}
	return nil
}

// ---------------------------------------------------------------------------
// fakeTranscriber: a minimal app.Transcriber double.
// ---------------------------------------------------------------------------

type fakeTranscriber struct {
	mu sync.Mutex

	transcribeFn func(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error)
	calls        []domain.TranscribeRequest
}

var _ app.Transcriber = (*fakeTranscriber)(nil)

func newFakeTranscriber() *fakeTranscriber {
	return &fakeTranscriber{}
}

func (tr *fakeTranscriber) Transcribe(ctx context.Context, req domain.TranscribeRequest) (*domain.Result, error) {
	tr.mu.Lock()
	tr.calls = append(tr.calls, req)
	fn := tr.transcribeFn
	tr.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return &domain.Result{Text: "hello there", Language: "en", Model: req.Options.Model, Task: req.Options.Task}, nil
}

func (tr *fakeTranscriber) Available(ctx context.Context) error { return nil }

func (tr *fakeTranscriber) callCount() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.calls)
}

// ---------------------------------------------------------------------------
// fakeClock
// ---------------------------------------------------------------------------

// fakeClock is a manually-advanced app.Clock, so NextAttemptAt assertions
// don't depend on when the test happened to run.
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

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------------------
// fixedRand: a deterministic Rand for asserting backoff delays exactly.
// ---------------------------------------------------------------------------

// fixedRand always returns frac*n, clamped to n-1 exactly like a real
// Int64N (which draws from [0, n), never n itself), so a test that installs
// it on Pool.rand can compute the exact jittered delay Pool.backoff will
// produce.
type fixedRand struct {
	frac float64
}

func (r fixedRand) Int64N(n int64) int64 {
	v := int64(r.frac * float64(n))
	if v >= n {
		v = n - 1
	}
	return v
}
