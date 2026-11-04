package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// ---------------------------------------------------------------------------
// callLog: a tiny, concurrency-safe recorder every fake uses so tests can
// assert call ordering (e.g. "Store.Put was never reached") without
// depending on timing.
// ---------------------------------------------------------------------------

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func newCallLog() *callLog { return &callLog{} }

func (l *callLog) record(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, name)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

// countOf returns how many times name appears in calls.
func countOf(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// fakeClock / fakeIDGenerator
// ---------------------------------------------------------------------------

// fakeClock is a manually-advanced Clock, so tests can control exactly how
// much time appears to pass without a real sleep.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

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

// fakeIDGenerator returns a pre-seeded sequence of IDs, then falls back to
// a deterministic, easily-recognisable counter-derived UUID for any call
// beyond the seed -- never a random one, so a test failure is reproducible,
// and always a well-formed UUID string, since validateID (Get, Delete,
// Cancel, Retry, OpenSource) must accept whatever Submit just minted.
type fakeIDGenerator struct {
	mu   sync.Mutex
	seed []string
	i    int
	next int
}

func newFakeIDGenerator(seed ...string) *fakeIDGenerator {
	return &fakeIDGenerator{seed: seed}
}

func (g *fakeIDGenerator) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.i < len(g.seed) {
		id := g.seed[g.i]
		g.i++
		return id
	}
	g.next++
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", g.next)
}

// last returns the most recently returned ID, or "" if NewID has never
// been called -- tests that don't seed an ID up front (e.g. Submit's
// cleanup tests, which must know what directory to check) still need to
// find out what Service actually minted.
func (g *fakeIDGenerator) last() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next > 0 {
		return fmt.Sprintf("00000000-0000-0000-0000-%012d", g.next)
	}
	if g.i > 0 && g.i <= len(g.seed) {
		return g.seed[g.i-1]
	}
	return ""
}

// ---------------------------------------------------------------------------
// memDB: the shared in-memory table fakeRepository and fakeQueue both
// operate on, mirroring how one Postgres table backs both app.Repository
// and app.Queue in production.
// ---------------------------------------------------------------------------

type memDB struct {
	mu   sync.Mutex
	rows map[string]*domain.Transcription

	// cancelRequested mirrors the `cancel_requested` column (see
	// CONTRACT.md section 7): it is not part of domain.Transcription
	// itself (see ports.go's Repository.RequestCancel doc: a running job
	// is merely "flagged so its worker notices at the next heartbeat"),
	// so it lives here rather than on the cloned row every Get returns.
	cancelRequested map[string]bool

	// leaseSeq mints the per-claim lease token, mirroring the UUID the real
	// Claim generates. A counter is enough here and keeps failures readable.
	leaseSeq int
}

func newMemDB() *memDB {
	return &memDB{
		rows:            make(map[string]*domain.Transcription),
		cancelRequested: make(map[string]bool),
	}
}

// seed inserts t directly, bypassing any validation, so tests can put a
// transcription straight into an arbitrary state (e.g. StatusFailed) without
// going through Submit.
func (m *memDB) seed(t *domain.Transcription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[t.ID] = cloneTranscription(t)
}

func cloneTranscription(t *domain.Transcription) *domain.Transcription {
	if t == nil {
		return nil
	}
	c := *t
	if t.Result != nil {
		r := *t.Result
		r.Segments = append([]domain.Segment(nil), t.Result.Segments...)
		c.Result = &r
	}
	if t.Error != nil {
		e := *t.Error
		if t.Error.Details != nil {
			e.Details = make(map[string]any, len(t.Error.Details))
			for k, v := range t.Error.Details {
				e.Details[k] = v
			}
		}
		c.Error = &e
	}
	if t.StartedAt != nil {
		v := *t.StartedAt
		c.StartedAt = &v
	}
	if t.CompletedAt != nil {
		v := *t.CompletedAt
		c.CompletedAt = &v
	}
	if t.PriorityAt != nil {
		v := *t.PriorityAt
		c.PriorityAt = &v
	}
	if t.NextAttemptAt != nil {
		v := *t.NextAttemptAt
		c.NextAttemptAt = &v
	}
	if t.LeaseOwner != nil {
		v := *t.LeaseOwner
		c.LeaseOwner = &v
	}
	if t.LeaseToken != nil {
		v := *t.LeaseToken
		c.LeaseToken = &v
	}
	if t.LeaseExpiresAt != nil {
		v := *t.LeaseExpiresAt
		c.LeaseExpiresAt = &v
	}
	return &c
}

func (m *memDB) create(t *domain.Transcription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[t.ID]; ok {
		return domain.New(domain.ErrCodeInternal, fmt.Sprintf("transcription %s already exists", t.ID))
	}
	m.rows[t.ID] = cloneTranscription(t)
	return nil
}

func (m *memDB) get(id string) (*domain.Transcription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return nil, domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	return cloneTranscription(row), nil
}

func (m *memDB) list(f ListFilter) Page {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := make([]*domain.Transcription, 0, len(m.rows))
	for _, row := range m.rows {
		if f.Status != "" && row.Status != f.Status {
			continue
		}
		items = append(items, cloneTranscription(row))
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID > items[j].ID
	})

	limit := f.Limit
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	return Page{Items: items[:limit]}
}

func (m *memDB) delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[id]; !ok {
		return domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	delete(m.rows, id)
	delete(m.cancelRequested, id)
	return nil
}

func (m *memDB) requestCancel(id string) (*domain.Transcription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return nil, domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	if row.Status.Terminal() {
		return nil, domain.New(domain.ErrCodeConflict, fmt.Sprintf("transcription %s is already terminal", id))
	}
	if row.Status == domain.StatusPending {
		row.Status = domain.StatusCanceled
		now := time.Now().UTC()
		row.CompletedAt = &now
	} else {
		// Running: merely flagged, so its worker notices at the next
		// heartbeat -- see memDB's own cancelRequested doc.
		m.cancelRequested[id] = true
	}
	return cloneTranscription(row), nil
}

func (m *memDB) requeue(id string) (*domain.Transcription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return nil, domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	if row.Status != domain.StatusFailed && row.Status != domain.StatusCanceled {
		return nil, domain.New(domain.ErrCodeConflict, fmt.Sprintf("transcription %s is not failed or canceled", id))
	}
	row.Status = domain.StatusPending
	row.Attempts = 0
	row.Error = nil
	row.CompletedAt = nil
	delete(m.cancelRequested, id)
	return cloneTranscription(row), nil
}

func (m *memDB) prioritize(id string, now time.Time) (*domain.Transcription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return nil, domain.New(domain.ErrCodeNotFound, fmt.Sprintf("transcription %s not found", id))
	}
	if row.Status != domain.StatusFailed && row.Status != domain.StatusPending {
		return nil, domain.New(domain.ErrCodeConflict, fmt.Sprintf("transcription %s is not pending or failed", id))
	}
	row.Status = domain.StatusPending
	row.Attempts = 0
	row.Error = nil
	row.StartedAt = nil
	row.CompletedAt = nil
	row.PriorityAt = &now
	delete(m.cancelRequested, id)
	return cloneTranscription(row), nil
}

// claim/heartbeat/finish/releaseExpired/purgeTerminalBefore below back
// fakeQueue's Queue methods. app.Service itself never calls Queue (Cancel
// and Retry go through Repository -- see Deps.Queue's own doc) -- they
// exist only so fakeQueue satisfies the Queue interface, with behaviour
// plausible enough that a future internal/jobs test could reuse this fake.

func (m *memDB) claim(owner string, lease time.Duration, now time.Time) *domain.Transcription {
	m.mu.Lock()
	defer m.mu.Unlock()

	var ids []string
	for id := range m.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic order for a fake with no real "due" clock semantics.

	for _, id := range ids {
		row := m.rows[id]
		// A pending row still flagged cancel_requested (the state left
		// behind when a retryable failure races RequestCancel -- see
		// finish's own cancel_requested handling below) must never be
		// claimed: mirrors the real Claim's `AND NOT cancel_requested`
		// predicate. Such a row is unclaimable forever until
		// releaseExpired reaps it -- see that method's own doc.
		if row.Status != domain.StatusPending || m.cancelRequested[id] {
			continue
		}
		row.Status = domain.StatusRunning
		row.Attempts++
		if row.StartedAt == nil {
			t := now
			row.StartedAt = &t
		}
		o := owner
		row.LeaseOwner = &o
		// A fresh token per claim, exactly as the real Claim does. The owner
		// string is deliberately NOT the guard: a pool reuses one owner for
		// its whole life, so guarding on it lets a stale execution of the
		// same process pass Heartbeat and Finish for a job that has since
		// been re-claimed. Modelling that faithfully here is the only reason
		// this fake can prove anything about lease safety.
		m.leaseSeq++
		token := fmt.Sprintf("lease-token-%d", m.leaseSeq)
		row.LeaseToken = &token
		exp := now.Add(lease)
		row.LeaseExpiresAt = &exp
		return cloneTranscription(row)
	}
	return nil
}

func (m *memDB) heartbeat(id, leaseToken string, lease time.Duration, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != domain.StatusRunning || row.LeaseToken == nil || *row.LeaseToken != leaseToken {
		return false, domain.New(domain.ErrCodeConflict, fmt.Sprintf("lease on %s is not held under token %s", id, leaseToken))
	}
	exp := now.Add(lease)
	row.LeaseExpiresAt = &exp
	return m.cancelRequested[id], nil
}

func (m *memDB) finish(req domain.FinishRequest, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[req.ID]
	if !ok || row.Status != domain.StatusRunning || row.LeaseToken == nil || *row.LeaseToken != req.LeaseToken {
		return domain.New(domain.ErrCodeConflict, fmt.Sprintf("lease on %s is not held under token %s", req.ID, req.LeaseToken))
	}

	status, result, errVal := req.Status, req.Result, req.Err
	// Mirror the real Store.Finish's succeeded->canceled downgrade
	// (CONTRACT.md, and postgres/queue.go's own doc): a job the API already
	// answered 200 to a cancel for must never be recorded succeeded, even
	// when the worker's own outcome -- computed before it noticed the
	// cancellation -- was a genuine success. The downgrade also nulls the
	// discarded result and replaces the error with a canceled-after-
	// completion marker rather than the (nil) error a real success would
	// have carried, so a canceled row here is indistinguishable from any
	// other canceled row, exactly like the real Finish.
	if m.cancelRequested[req.ID] && status == domain.StatusSucceeded {
		status = domain.StatusCanceled
		result = nil
		errVal = domain.New(domain.ErrCodeCanceled, "canceled after this attempt had already completed")
	}

	row.Status = status
	row.Result = result
	row.Error = errVal
	if req.Audio != nil {
		row.Audio = *req.Audio
	}
	row.LeaseOwner = nil
	row.LeaseToken = nil
	row.LeaseExpiresAt = nil
	if status == domain.StatusPending {
		row.NextAttemptAt = req.NextAttemptAt
	} else {
		t := now
		row.CompletedAt = &t
		delete(m.cancelRequested, req.ID)
	}
	return nil
}

// releaseExpired mirrors the real ReleaseExpired's two jobs (see
// postgres/queue.go's own doc): recovering (or, out of attempts, failing)
// every running row whose lease has lapsed, AND separately reaping every
// pending row still flagged cancel_requested straight to StatusCanceled --
// the state a job lands in when a retryable failure raced RequestCancel and
// the worker's Finish lost that race, landing back on pending with the flag
// still set. Without the second half, such a row would sit forever:
// unclaimable by claim's own `cancel_requested` check above, but never
// actually resolved either. Both count toward the same returned total, just
// as the real ReleaseExpired documents.
func (m *memDB) releaseExpired(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, row := range m.rows {
		switch {
		case row.Status == domain.StatusRunning && row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(now):
			row.LeaseOwner = nil
			row.LeaseToken = nil
			row.LeaseExpiresAt = nil
			if row.Attempts < row.MaxAttempts {
				row.Status = domain.StatusPending
				t := now
				row.NextAttemptAt = &t
			} else {
				row.Status = domain.StatusFailed
				row.Error = domain.New(domain.ErrCodeTimeout, "lease expired")
				t := now
				row.CompletedAt = &t
			}
			n++
		case row.Status == domain.StatusPending && m.cancelRequested[id]:
			row.Status = domain.StatusCanceled
			t := now
			row.CompletedAt = &t
			delete(m.cancelRequested, id)
			n++
		}
	}
	return n
}

// releaseForShutdown mirrors the real ReleaseForShutdown (see
// app.Queue.ReleaseForShutdown): same lease guard as heartbeat/finish, but
// on success the row goes back to pending WITHOUT its Attempts being
// consumed -- giving the claim back rather than recording a failed attempt.
func (m *memDB) releaseForShutdown(id, leaseToken string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != domain.StatusRunning || row.LeaseToken == nil || *row.LeaseToken != leaseToken {
		return domain.New(domain.ErrCodeConflict, fmt.Sprintf("lease on %s is not held under token %s", id, leaseToken))
	}
	row.Status = domain.StatusPending
	if row.Attempts > 0 {
		row.Attempts--
	}
	row.LeaseOwner = nil
	row.LeaseToken = nil
	row.LeaseExpiresAt = nil
	t := now
	row.NextAttemptAt = &t
	return nil
}

func (m *memDB) purgeTerminalBefore(cutoff time.Time) []domain.PurgedTranscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.PurgedTranscription
	for id, row := range m.rows {
		if !row.Status.Terminal() || row.CompletedAt == nil || !row.CompletedAt.Before(cutoff) {
			continue
		}
		out = append(out, domain.PurgedTranscription{ID: row.ID, SourcePath: row.SourcePath})
		delete(m.rows, id)
		delete(m.cancelRequested, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------------------------------------------------------------------------
// fakeRepository
// ---------------------------------------------------------------------------

type fakeRepository struct {
	db    *memDB
	calls *callLog

	createErr map[string]error
	// createErrAny, when set, fails every Create regardless of ID -- for
	// tests that need Submit's cleanup path but do not know the freshly
	// minted ID ahead of time to key createErr by it.
	createErrAny     error
	getErr           map[string]error
	deleteErr        map[string]error
	requestCancelErr map[string]error
	requeueErr       map[string]error
	prioritizeErr    map[string]error

	mu             sync.Mutex
	lastListFilter ListFilter
}

var _ Repository = (*fakeRepository)(nil)

func newFakeRepository(db *memDB, calls *callLog) *fakeRepository {
	return &fakeRepository{db: db, calls: calls}
}

func (r *fakeRepository) Create(ctx context.Context, t *domain.Transcription) error {
	r.calls.record("repo.Create")
	if r.createErrAny != nil {
		return r.createErrAny
	}
	if err, ok := r.createErr[t.ID]; ok {
		return err
	}
	return r.db.create(t)
}

func (r *fakeRepository) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	r.calls.record("repo.Get")
	if err, ok := r.getErr[id]; ok {
		return nil, err
	}
	return r.db.get(id)
}

func (r *fakeRepository) List(ctx context.Context, f ListFilter) (Page, error) {
	r.calls.record("repo.List")
	r.mu.Lock()
	r.lastListFilter = f
	r.mu.Unlock()
	return r.db.list(f), nil
}

func (r *fakeRepository) Delete(ctx context.Context, id string) error {
	r.calls.record("repo.Delete")
	if err, ok := r.deleteErr[id]; ok {
		return err
	}
	return r.db.delete(id)
}

func (r *fakeRepository) RequestCancel(ctx context.Context, id string) (*domain.Transcription, error) {
	r.calls.record("repo.RequestCancel")
	if err, ok := r.requestCancelErr[id]; ok {
		return nil, err
	}
	return r.db.requestCancel(id)
}

func (r *fakeRepository) Requeue(ctx context.Context, id string) (*domain.Transcription, error) {
	r.calls.record("repo.Requeue")
	if err, ok := r.requeueErr[id]; ok {
		return nil, err
	}
	return r.db.requeue(id)
}

func (r *fakeRepository) Prioritize(ctx context.Context, id string) (*domain.Transcription, error) {
	r.calls.record("repo.Prioritize")
	if err, ok := r.prioritizeErr[id]; ok {
		return nil, err
	}
	return r.db.prioritize(id, time.Now().UTC())
}

// ---------------------------------------------------------------------------
// fakeQueue
// ---------------------------------------------------------------------------

type fakeQueue struct {
	db    *memDB
	clock Clock
	calls *callLog
}

var _ Queue = (*fakeQueue)(nil)

func newFakeQueue(db *memDB, clock Clock, calls *callLog) *fakeQueue {
	return &fakeQueue{db: db, clock: clock, calls: calls}
}

func (q *fakeQueue) Claim(ctx context.Context, owner string, lease time.Duration) (*domain.Transcription, error) {
	q.calls.record("queue.Claim")
	return q.db.claim(owner, lease, q.clock.Now()), nil
}

func (q *fakeQueue) Heartbeat(ctx context.Context, id, leaseToken string, lease time.Duration) (bool, error) {
	q.calls.record("queue.Heartbeat")
	return q.db.heartbeat(id, leaseToken, lease, q.clock.Now())
}

func (q *fakeQueue) Finish(ctx context.Context, req domain.FinishRequest) error {
	q.calls.record("queue.Finish")
	return q.db.finish(req, q.clock.Now())
}

func (q *fakeQueue) ReleaseExpired(ctx context.Context) (int, error) {
	q.calls.record("queue.ReleaseExpired")
	return q.db.releaseExpired(q.clock.Now()), nil
}

func (q *fakeQueue) ReleaseForShutdown(ctx context.Context, id, leaseToken string) error {
	q.calls.record("queue.ReleaseForShutdown")
	return q.db.releaseForShutdown(id, leaseToken, q.clock.Now())
}

func (q *fakeQueue) PurgeTerminalBefore(ctx context.Context, cutoff time.Time) ([]domain.PurgedTranscription, error) {
	q.calls.record("queue.PurgeTerminalBefore")
	return q.db.purgeTerminalBefore(cutoff), nil
}

// ---------------------------------------------------------------------------
// fakeStore
// ---------------------------------------------------------------------------

type nopSeekCloser struct {
	*bytes.Reader
}

func (nopSeekCloser) Close() error { return nil }

// fakeStore is an in-memory Store: relPath -> bytes. It exists so Submit,
// Delete and OpenSource can be tested without touching a real filesystem --
// internal/storage/localfs is exercised by its own tests, not these.
type fakeStore struct {
	mu    sync.Mutex
	files map[string][]byte
	calls *callLog

	// freeBytes is what FreeBytes reports. Defaults to a generous value;
	// tests that need to exercise the disk-full path override it.
	freeBytes uint64

	putErr       map[string]error
	freeBytesErr error
	removeDirErr error
}

var _ Store = (*fakeStore)(nil)

func newFakeStore(calls *callLog) *fakeStore {
	return &fakeStore{
		files:     make(map[string][]byte),
		calls:     calls,
		freeBytes: 1 << 40, // 1 TiB: far above any test's MinFreeBytes unless overridden.
	}
}

func (s *fakeStore) Put(ctx context.Context, relPath string, r io.Reader) (int64, error) {
	s.calls.record("store.Put")
	if err, ok := s.putErr[relPath]; ok {
		return 0, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, domain.Wrap(domain.ErrCodeInternal, "fakeStore: read", err)
	}
	s.mu.Lock()
	s.files[relPath] = data
	s.mu.Unlock()
	return int64(len(data)), nil
}

func (s *fakeStore) Open(ctx context.Context, relPath string) (io.ReadSeekCloser, int64, error) {
	s.calls.record("store.Open")
	s.mu.Lock()
	data, ok := s.files[relPath]
	s.mu.Unlock()
	if !ok {
		return nil, 0, domain.New(domain.ErrCodeNotFound, fmt.Sprintf("fakeStore: %s not found", relPath))
	}
	return nopSeekCloser{bytes.NewReader(data)}, int64(len(data)), nil
}

func (s *fakeStore) AbsPath(relPath string) (string, error) {
	s.calls.record("store.AbsPath")
	return "/fake-store-root/" + relPath, nil
}

func (s *fakeStore) Remove(ctx context.Context, relPath string) error {
	s.calls.record("store.Remove")
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, relPath)
	return nil
}

func (s *fakeStore) RemoveDir(ctx context.Context, relDir string) error {
	s.calls.record("store.RemoveDir")
	if s.removeDirErr != nil {
		return s.removeDirErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := relDir + "/"
	for k := range s.files {
		if strings.HasPrefix(k, prefix) || k == relDir {
			delete(s.files, k)
		}
	}
	return nil
}

func (s *fakeStore) FreeBytes() (uint64, error) {
	s.calls.record("store.FreeBytes")
	if s.freeBytesErr != nil {
		return 0, s.freeBytesErr
	}
	return s.freeBytes, nil
}

// hasPrefix reports whether any stored file's path starts with relDir,
// for tests asserting a directory was (or was not) actually cleaned up.
func (s *fakeStore) hasPrefix(relDir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := relDir + "/"
	for k := range s.files {
		if strings.HasPrefix(k, prefix) || k == relDir {
			return true
		}
	}
	return false
}

// get returns the stored bytes at relPath, for tests asserting exactly
// what Submit wrote.
func (s *fakeStore) get(relPath string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[relPath]
	return data, ok
}
