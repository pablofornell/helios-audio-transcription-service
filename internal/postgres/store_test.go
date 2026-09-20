package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// testPool is shared by every test in this package: one Postgres container
// for the whole run, with each test truncating its own table via
// truncateAll (see helpers_test.go).
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests exists so container/pool teardown (deferred) always runs before
// the process exits -- os.Exit itself skips deferred calls. If Docker is
// not available in this environment, the whole suite is skipped rather than
// failed: every individual test still runs (and immediately calls t.Skip
// via requireDocker), so `go test` reports the package as skipped, not red.
func runTests(m *testing.M) int {
	if !dockerAvailable() {
		fmt.Fprintln(os.Stderr, "postgres: docker not available, skipping testcontainers suite")
		dockerUnavailable = true
		return m.Run()
	}

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("helios_test"),
		tcpostgres.WithUsername("helios"),
		tcpostgres.WithPassword("helios"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres: start test container:", err)
		return 1
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres: connection string:", err)
		return 1
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "postgres: connect pool:", err)
		return 1
	}
	defer pool.Close()

	if err := Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "postgres: run migrations:", err)
		return 1
	}

	testPool = pool
	return m.Run()
}

// dockerUnavailable is set by runTests when no Docker daemon could be
// reached, so every test can skip itself via requireDocker instead of
// panicking on a nil testPool.
var dockerUnavailable bool

// dockerAvailable is a best-effort, fast check for a reachable Docker (or
// Docker-compatible, e.g. Podman via DOCKER_HOST) daemon, so this suite
// degrades to a skip instead of a hard failure in a sandboxed environment
// that genuinely has no container runtime.
func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

// requireDocker skips t when the package-level container never started.
func requireDocker(t *testing.T) {
	t.Helper()
	if dockerUnavailable {
		t.Skip("docker is not available in this environment; skipping testcontainers-backed test")
	}
}

// TestStore_CreateAndGet_RoundTrip pins that every field Create writes,
// including nested Options/Audio/Result/Error JSONB, survives a Get
// unchanged -- the JSONB fidelity CONTRACT.md's test list calls for.
func TestStore_CreateAndGet_RoundTrip(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	id := newID()
	want := newTranscription(id, now)
	want.Options = domain.Options{
		Model:          "small",
		Language:       "en",
		Task:           domain.TaskTranslate,
		WordTimestamps: true,
		InitialPrompt:  "proper nouns: Helios, whisper.cpp",
		BeamSize:       8,
		Temperature:    0.4,
	}
	want.Audio = domain.Audio{
		Filename:        "meeting.mp3",
		MediaType:       "audio/mpeg",
		SizeBytes:       2048,
		DurationSeconds: 61.5,
		SampleRateHz:    16000,
		Channels:        1,
		Codec:           "mp3",
	}

	require.NoError(t, store.Create(ctx, want))
	assert.Equal(t, domain.StatusPending, want.Status, "Create must set Status to pending on the caller's struct")

	got, err := store.Get(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.StatusPending, got.Status)
	assert.Equal(t, want.Options, got.Options)
	assert.Equal(t, want.Audio, got.Audio)
	assert.Equal(t, want.SourcePath, got.SourcePath)
	assert.Nil(t, got.Result)
	assert.Nil(t, got.Error)
	assert.Equal(t, 0, got.Attempts)
	assert.Equal(t, want.MaxAttempts, got.MaxAttempts)
	assert.WithinDuration(t, now, got.CreatedAt, time.Microsecond)
	assert.WithinDuration(t, now, got.UpdatedAt, time.Microsecond)
	assert.Nil(t, got.StartedAt)
	assert.Nil(t, got.CompletedAt)
	assert.Nil(t, got.LeaseOwner)
	assert.Nil(t, got.LeaseToken)
	assert.Nil(t, got.LeaseExpiresAt)

	// Finish a job with a Result and an Error present at different times so
	// this test also pins their JSONB round trip, not just Options/Audio's.
	result := &domain.Result{
		Text:            "hello there general kenobi",
		Language:        "en",
		DurationSeconds: 3.2,
		Model:           "small",
		Task:            domain.TaskTranslate,
		Segments: []domain.Segment{
			{
				ID: 0, Start: 0, End: 1.5, Text: "hello there",
				Words: []domain.Word{
					{Word: "hello", Start: 0, End: 0.5, Probability: 0.98},
					{Word: "there", Start: 0.5, End: 1.5, Probability: 0.91},
				},
			},
			{ID: 1, Start: 1.5, End: 3.2, Text: "general kenobi"},
		},
	}

	claimed, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, id, claimed.ID)
	require.NotNil(t, claimed.LeaseToken)

	probed := want.Audio
	probed.DurationSeconds = 61.9
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: id, LeaseToken: *claimed.LeaseToken, Status: domain.StatusSucceeded,
		Result: result, Audio: &probed,
	}))

	got, err = store.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, got.Status)
	require.NotNil(t, got.Result)
	assert.Equal(t, *result, *got.Result)
	assert.Nil(t, got.Error)
	assert.Equal(t, probed, got.Audio, "Finish must persist req.Audio when non-nil")
	require.NotNil(t, got.CompletedAt)
}

// TestStore_Get_NotFound pins Get's ErrCodeNotFound contract.
func TestStore_Get_NotFound(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	_, err := store.Get(context.Background(), newID())
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))
}

// TestStore_Delete removes the row outright and reports ErrCodeNotFound
// for an id that is not there. The second half matters more than it looks:
// app.Service.Delete does not pre-check existence, it relies on this method
// to tell it the row was missing, so a silently-successful no-op delete
// would make the API answer 204 for an id that never existed.
func TestStore_Delete(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	id := newID()
	require.NoError(t, store.Create(ctx, newTranscription(id, time.Now())))
	require.NoError(t, store.Delete(ctx, id))

	_, err := store.Get(ctx, id)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))

	err = store.Delete(ctx, id)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err),
		"deleting an already-gone id must report not_found, not succeed")
}

// TestStore_List_PaginationAcrossThreePages pins the keyset (created_at,
// id) tiebreak: five rows sharing one created_at, paged two at a time
// across three pages, must each appear exactly once and in a stable order.
func TestStore_List_PaginationAcrossThreePages(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	fixed := time.Now().UTC().Truncate(time.Microsecond)

	const total = 5
	ids := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscription(id, fixed)))
		ids[id] = true
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for i := 0; i < total+2; i++ {
		page, err := store.List(ctx, ListFilter{Limit: 2, Cursor: cursor})
		require.NoError(t, err)
		pages++
		assert.LessOrEqual(t, len(page.Items), 2)
		for _, tr := range page.Items {
			seen[tr.ID]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	assert.GreaterOrEqual(t, pages, 3, "5 rows at 2 per page must take at least 3 pages")
	assert.Len(t, seen, total)
	for id := range ids {
		assert.Equal(t, 1, seen[id], "transcription %s should appear exactly once across pages", id)
	}
}

// TestStore_List_MalformedCursor pins ErrCodeInvalidInput for an
// undecodable cursor, per CONTRACT.md section 7.
func TestStore_List_MalformedCursor(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	_, err := store.List(context.Background(), ListFilter{Cursor: "not valid base64!!"})
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

// TestStore_List_CursorWithNonUUIDIdIsInvalidInput pins finding 6: a cursor
// that decodes cleanly (valid base64, valid "<ts>|<id>" shape) but whose id
// half is not a UUID must still be rejected as ErrCodeInvalidInput at
// decodeCursor, before List's `(created_at, id) < ($1, $2::uuid)` query
// ever runs. Before the fix, this id reached the database, failed the
// ::uuid cast with SQLSTATE 22P02, and mapErr's default branch turned that
// into ErrCodeInternal -- an ordinary bad request answered as a 500.
func TestStore_List_CursorWithNonUUIDIdIsInvalidInput(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)

	cursor := encodeCursor(time.Now().UTC(), "not-a-uuid")
	_, err := store.List(context.Background(), ListFilter{Cursor: cursor})
	assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

// TestStore_List_StatusFilter pins that Status narrows the listing.
func TestStore_List_StatusFilter(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create and claim the soon-to-be-running row first: Claim always picks
	// the oldest due pending row, so claiming it here -- before the row
	// meant to stay pending even exists -- pins which one ends up running
	// regardless of Claim's own ordering.
	running := newTranscription(newID(), now)
	require.NoError(t, store.Create(ctx, running))
	_, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)

	pending := newTranscription(newID(), now.Add(time.Second))
	require.NoError(t, store.Create(ctx, pending))

	page, err := store.List(ctx, ListFilter{Status: domain.StatusPending})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, pending.ID, page.Items[0].ID)

	page, err = store.List(ctx, ListFilter{Status: domain.StatusSucceeded})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// TestStore_List_LimitClamped pins Limit's [1,100] clamp and its 20
// default.
func TestStore_List_LimitClamped(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		require.NoError(t, store.Create(ctx, newTranscription(newID(), now.Add(time.Duration(i)*time.Second))))
	}

	page, err := store.List(ctx, ListFilter{Limit: -5})
	require.NoError(t, err)
	assert.Len(t, page.Items, 3, "a non-positive limit must fall back to the default rather than returning nothing")

	page, err = store.List(ctx, ListFilter{Limit: 1000})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(page.Items), listMaxLimit)
}

// TestStore_PurgeTerminalBefore_ReturnsPurgedRowsAndLeavesFreshOnes pins
// PurgeTerminalBefore's eligibility (terminal + completed_at < cutoff) and
// that it hands back exactly what it deleted, untouched otherwise.
func TestStore_PurgeTerminalBefore_ReturnsPurgedRowsAndLeavesFreshOnes(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	now := time.Now().UTC()

	old := now.Add(-48 * time.Hour)
	oldID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(oldID, old)))
	claimedOld, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.Equal(t, oldID, claimedOld.ID)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: oldID, LeaseToken: *claimedOld.LeaseToken, Status: domain.StatusFailed,
		Err: domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1"),
	}))
	// Backdate completed_at so it is old enough to purge; Finish always
	// stamps now().
	_, err = testPool.Exec(ctx, `UPDATE transcriptions SET completed_at = $2 WHERE id = $1`, oldID, old)
	require.NoError(t, err)

	freshID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(freshID, now)))
	claimedFresh, err := store.Claim(ctx, "w1", time.Minute)
	require.NoError(t, err)
	require.Equal(t, freshID, claimedFresh.ID)
	require.NoError(t, store.Finish(ctx, domain.FinishRequest{
		ID: freshID, LeaseToken: *claimedFresh.LeaseToken, Status: domain.StatusFailed,
		Err: domain.New(domain.ErrCodeToolFailure, "whisper-cli exited 1"),
	}))

	pendingID := newID()
	require.NoError(t, store.Create(ctx, newTranscription(pendingID, old)))

	cutoff := now.Add(-24 * time.Hour)
	purged, err := store.PurgeTerminalBefore(ctx, cutoff)
	require.NoError(t, err)
	require.Len(t, purged, 1)
	assert.Equal(t, oldID, purged[0].ID)

	_, err = store.Get(ctx, oldID)
	assert.Equal(t, domain.ErrCodeNotFound, domain.CodeOf(err))

	_, err = store.Get(ctx, freshID)
	assert.NoError(t, err, "a recently-completed terminal row must survive an older cutoff")

	_, err = store.Get(ctx, pendingID)
	assert.NoError(t, err, "a non-terminal row must never be purged regardless of age")
}

// TestStore_PurgeTerminalBefore_RespectsBatchLimit pins finding 5's batching
// requirement: a single call never deletes more than purgeBatchLimit rows,
// even when far more are eligible, so one overdue sweep cannot hold an
// unbounded transaction open. Rows are seeded directly with a bulk SQL
// INSERT rather than purgeBatchLimit+ individual Store.Create round trips,
// which would make this test unreasonably slow.
func TestStore_PurgeTerminalBefore_RespectsBatchLimit(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()

	const extra = 50
	old := time.Now().UTC().Add(-48 * time.Hour)
	_, err := testPool.Exec(ctx, `
		INSERT INTO transcriptions (
			id, status, options, audio, source_path,
			attempts, max_attempts, next_attempt_at,
			created_at, updated_at, completed_at
		)
		SELECT gen_random_uuid(), 'succeeded', '{}'::jsonb, '{}'::jsonb,
		       'transcriptions/batch/source.mp3',
		       1, 3, now(), $1, $1, $1
		FROM generate_series(1, $2)`,
		old, purgeBatchLimit+extra,
	)
	require.NoError(t, err)

	cutoff := time.Now().UTC()
	first, err := store.PurgeTerminalBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Len(t, first, purgeBatchLimit, "one call must purge at most purgeBatchLimit rows")

	second, err := store.PurgeTerminalBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Len(t, second, extra, "a later sweep must pick up whatever the first call's batch limit left behind")

	third, err := store.PurgeTerminalBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.Empty(t, third, "nothing eligible must remain once both batches have run")
}
