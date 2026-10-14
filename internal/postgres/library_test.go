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

func TestStoreLibraryGlobalOrderAndAudioEligibility(t *testing.T) {
	requireDocker(t)
	truncateAll(t)
	store := New(testPool)
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	statuses := []domain.Status{domain.StatusPending, domain.StatusRunning, domain.StatusSucceeded, domain.StatusFailed, domain.StatusCanceled, domain.StatusRunning, domain.StatusPending, domain.StatusSucceeded}
	ids := make([]string, len(statuses))
	for i, status := range statuses {
		id := newID()
		ids[i] = id
		tr := newTranscription(id, at.Add(time.Duration(i)*time.Microsecond))
		require.NoError(t, store.Create(ctx, tr))
		var completed *time.Time
		if status.Terminal() && i != 4 {
			value := at.Add(time.Duration(i) * time.Microsecond)
			completed = &value
		}
		_, err := testPool.Exec(ctx, "UPDATE transcriptions SET status=$2, completed_at=$3 WHERE id=$1", id, status, completed)
		require.NoError(t, err)
	}
	// Equal timestamps must still traverse every id once.
	_, err := testPool.Exec(ctx, "UPDATE transcriptions SET created_at=$2 WHERE id=$1", ids[5], at.Add(time.Microsecond))
	require.NoError(t, err)
	for _, field := range []string{"source", "filename", "size"} {
		tr := newTranscription(newID(), at)
		if field == "source" {
			tr.SourcePath = " "
		}
		if field == "filename" {
			tr.Audio.Filename = " "
		}
		if field == "size" {
			tr.Audio.SizeBytes = 0
		}
		require.NoError(t, store.Create(ctx, tr))
	}
	running := []string{ids[1], ids[5]}
	if running[0] > running[1] {
		running[0], running[1] = running[1], running[0]
	}
	want := append(running, ids[0], ids[6], ids[7], ids[4], ids[3], ids[2])
	var got []string
	cursor := ""
	for {
		page, err := store.List(ctx, ListFilter{Library: true, Limit: 1, Cursor: cursor})
		require.NoError(t, err)
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		require.LessOrEqual(t, len(got), len(want))
	}
	require.Equal(t, want, got)
	page, err := store.List(ctx, ListFilter{Library: true, Status: domain.StatusPending})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, ids[0], page.Items[0].ID)
	require.Equal(t, ids[6], page.Items[1].ID)
	defaultPage, err := store.List(ctx, ListFilter{})
	require.NoError(t, err)
	require.Len(t, defaultPage.Items, 11)
	require.Equal(t, ids[7], defaultPage.Items[0].ID)
}
