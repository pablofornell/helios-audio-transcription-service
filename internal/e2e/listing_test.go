package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// TestListPagination_AndStatusFilter submits several jobs against a stack
// whose worker pool is deliberately never started (see newStack's doc),
// so every row stays deterministically pending for the whole test --
// nothing this test asserts about ordering, page counts or the status
// filter can flake against a background worker racing to finish one.
func TestListPagination_AndStatusFilter(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	cfg := newTestConfig(t)
	st := newRawStack(t, cfg, nil) // pool never started: every row stays pending.
	base := serveStack(t, st)

	const n = 5
	ids := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		created := submitFile(t, base, fixtureGoodMP3, "audio/mpeg", nil)
		require.Equal(t, domain.StatusPending, created.Status)
		ids[created.ID] = true
	}

	t.Run("pages_across_the_whole_set_with_no_duplicates_or_gaps", func(t *testing.T) {
		seen := make(map[string]bool, n)
		cursor := ""
		pages := 0
		for {
			query := "limit=2"
			if cursor != "" {
				query += "&cursor=" + cursor
			}
			page := listTranscriptions(t, base, query)
			pages++
			require.LessOrEqualf(t, len(page.Items), 2, "page %d: expected at most the requested limit of 2 items", pages)

			for i, item := range page.Items {
				assert.True(t, ids[item.ID], "page %d: unexpected id %s not among this test's own submissions", pages, item.ID)
				assert.False(t, seen[item.ID], "page %d: id %s appeared on an earlier page too", pages, item.ID)
				seen[item.ID] = true
				if i > 0 {
					prev := page.Items[i-1]
					assert.Falsef(t, item.CreatedAt.After(prev.CreatedAt),
						"page %d: item %d (createdAt=%s) is newer than the previous item (createdAt=%s); expected created_at DESC order",
						pages, i, item.CreatedAt, prev.CreatedAt)
				}
			}

			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			require.Lessf(t, pages, n+2, "paged more times (%d) than should be possible for %d rows at 2 per page", pages, n)
		}
		assert.Equal(t, n, len(seen), "expected every submitted transcription to be seen exactly once across all pages")
	})

	t.Run("status_filter_pending_returns_everything_submitted", func(t *testing.T) {
		page := listTranscriptions(t, base, fmt.Sprintf("status=pending&limit=%d", n+5))
		assert.Len(t, page.Items, n)
		for _, item := range page.Items {
			assert.Equal(t, domain.StatusPending, item.Status)
			assert.True(t, ids[item.ID])
		}
	})

	t.Run("status_filter_succeeded_returns_nothing", func(t *testing.T) {
		page := listTranscriptions(t, base, "status=succeeded")
		assert.Empty(t, page.Items, "no job has run at all -- the pool was never started -- so none can have succeeded")
	})

	t.Run("status_filter_invalid_value_is_rejected", func(t *testing.T) {
		resp := doJSON(t, http.MethodGet, base+"/api/v1/transcriptions?status=not-a-real-status", nil)
		defer resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		derr := decodeError(t, resp)
		assert.Equal(t, domain.ErrCodeInvalidInput, derr.Code)
	})
}
