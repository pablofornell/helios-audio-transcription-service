package postgres

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLibraryCursorValidation(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	tr := newTranscription("00000000-0000-0000-0000-000000000001", at)
	tr.Status = domain.StatusSucceeded
	completed := at.Add(time.Microsecond)
	tr.CompletedAt = &completed
	token := encodeLibraryCursor(tr)
	rank, got, id, err := decodeLibraryCursor(token)
	require.NoError(t, err)
	require.Equal(t, 2, rank)
	require.True(t, got.Equal(completed))
	require.Equal(t, tr.ID, id)
	for _, cursor := range []string{"!", encodeCursor(at, tr.ID), base64.RawURLEncoding.EncodeToString([]byte("library|3|" + at.Format(time.RFC3339Nano) + "|" + tr.ID)), base64.RawURLEncoding.EncodeToString([]byte("library|0|bad|" + tr.ID)), base64.RawURLEncoding.EncodeToString([]byte("library|0|" + at.Format(time.RFC3339Nano) + "|bad"))} {
		_, err := (&Store{}).List(context.Background(), ListFilter{Library: true, Cursor: cursor})
		require.Error(t, err)
		require.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
	}
	_, err = (&Store{}).List(context.Background(), ListFilter{Cursor: token})
	require.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
}

// libraryIDs pages through the whole Library listing with the given page size
// and returns the item ids in the order served.
func libraryIDs(t *testing.T, store *Store, limit int) []string {
	t.Helper()
	var got []string
	cursor := ""
	for {
		page, err := store.List(context.Background(), ListFilter{Library: true, Limit: limit, Cursor: cursor})
		require.NoError(t, err)
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if page.NextCursor == "" {
			return got
		}
		cursor = page.NextCursor
		require.Less(t, len(got), 100, "cursor does not advance")
	}
}

func TestStoreLibraryListsFinishedItemsOnly(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	const result = `{"text":"done","language":"en","durationSeconds":1,"model":"m","task":"transcribe","segments":[]}`

	// create inserts one row and then forces its status, result and completion.
	create := func(i int, status domain.Status, resultJSON *string, completed *time.Time) string {
		id := newID()
		require.NoError(t, store.Create(ctx, newTranscription(id, at.Add(time.Duration(i)*time.Microsecond))))
		_, err := testPool.Exec(ctx, "UPDATE transcriptions SET status=$2, result=$3::jsonb, completed_at=$4 WHERE id=$1", id, status, resultJSON, completed)
		require.NoError(t, err)
		return id
	}
	done := func(i int) *time.Time {
		v := at.Add(time.Duration(i) * time.Hour)
		return &v
	}
	res := result
	jsonNull := "null"

	// Every status, each with a result where the schema allows one.
	pending := create(0, domain.StatusPending, &res, nil)
	running := create(1, domain.StatusRunning, &res, nil)
	failed := create(2, domain.StatusFailed, &res, done(2))
	canceled := create(3, domain.StatusCanceled, &res, done(3))
	// A succeeded row without a usable result is not finished.
	noResult := create(4, domain.StatusSucceeded, nil, done(4))
	nullResult := create(5, domain.StatusSucceeded, &jsonNull, done(5))
	// Succeeded rows with stored audio are finished. The last one has no
	// completion time.
	oldest := create(6, domain.StatusSucceeded, &res, done(6))
	newest := create(8, domain.StatusSucceeded, &res, done(8))
	middleA := create(7, domain.StatusSucceeded, &res, done(7))
	middleB := create(7, domain.StatusSucceeded, &res, done(7))
	fallback := create(9, domain.StatusSucceeded, &res, nil)
	// Succeeded rows without stored audio are not listed.
	for _, field := range []string{"source", "filename", "size"} {
		tr := newTranscription(newID(), at)
		switch field {
		case "source":
			tr.SourcePath = " "
		case "filename":
			tr.Audio.Filename = " "
		case "size":
			tr.Audio.SizeBytes = 0
		}
		require.NoError(t, store.Create(ctx, tr))
		_, err := testPool.Exec(ctx, "UPDATE transcriptions SET status='succeeded', result=$2::jsonb, completed_at=now() WHERE id=$1", tr.ID, result)
		require.NoError(t, err)
	}
	for _, id := range []string{pending, running, failed, canceled, noResult, nullResult} {
		require.NotContains(t, libraryIDs(t, store, 100), id)
	}

	// Newest completion first. Equal completion times order by id ascending.
	// The fallback row has no completion time, so it sorts by its creation
	// time, which is older than every completion time above.
	tied := []string{middleA, middleB}
	if tied[0] > tied[1] {
		tied[0], tied[1] = tied[1], tied[0]
	}
	want := []string{newest, tied[0], tied[1], oldest, fallback}
	require.Equal(t, want, libraryIDs(t, store, 100))
	// Cursors must traverse every id once at any page size.
	require.Equal(t, want, libraryIDs(t, store, 1))
	require.Equal(t, want, libraryIDs(t, store, 2))

	// A status filter cannot widen the Library.
	for _, status := range []domain.Status{domain.StatusPending, domain.StatusRunning, domain.StatusFailed, domain.StatusCanceled} {
		page, err := store.List(ctx, ListFilter{Library: true, Status: status})
		require.NoError(t, err)
		require.Empty(t, page.Items, status)
	}
	page, err := store.List(ctx, ListFilter{Library: true, Status: domain.StatusSucceeded})
	require.NoError(t, err)
	require.Len(t, page.Items, len(want))

	// A cursor from the removed active section restarts at the first row.
	legacy := base64.RawURLEncoding.EncodeToString([]byte("library|0|" + at.Format(time.RFC3339Nano) + "|" + newID()))
	page, err = store.List(ctx, ListFilter{Library: true, Cursor: legacy, Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, len(want))

	// The default listing still returns every status, and single reads work.
	defaultPage, err := store.List(ctx, ListFilter{Limit: 100})
	require.NoError(t, err)
	require.Len(t, defaultPage.Items, 14)
	for _, id := range []string{pending, running, failed, canceled, noResult} {
		_, err := store.Get(ctx, id)
		require.NoError(t, err)
	}
}
