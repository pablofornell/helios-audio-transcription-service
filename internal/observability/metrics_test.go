package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetrics_OwnsPrivateRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	assert.Same(t, reg, m.Registry())

	// prometheus.DefaultRegisterer must be untouched: registering the same
	// collector names against a second private registry must not conflict,
	// proving Metrics never touched the default registerer.
	other := prometheus.NewRegistry()
	NewMetrics(other) // would panic (MustRegister) if names collided globally
}

func TestTranscriptionSubmitted(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.TranscriptionSubmitted()
	m.TranscriptionSubmitted()

	assert.Equal(t, float64(2), testutil.ToFloat64(m.transcriptionsSubmitted))
}

func TestTranscriptionCompleted(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.TranscriptionCompleted("succeeded")
	m.TranscriptionCompleted("succeeded")
	m.TranscriptionCompleted("failed")

	assert.Equal(t, float64(2), testutil.ToFloat64(m.transcriptionsCompleted.WithLabelValues("succeeded")))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.transcriptionsCompleted.WithLabelValues("failed")))
}

func TestTranscriptionDuration(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.TranscriptionDuration("base", 2*time.Second)
	m.TranscriptionDuration("base", 3*time.Second)
	m.TranscriptionDuration("large-v3", time.Second)

	// Two distinct model label values means two histogram series; a
	// Gather-based read is used (rather than testutil.ToFloat64, which only
	// makes sense for a single float-valued metric) to check each series'
	// sample count.
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	var baseCount, largeCount uint64
	for _, f := range families {
		if f.GetName() != "helios_transcription_duration_seconds" {
			continue
		}
		for _, metric := range f.Metric {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "model" && l.GetValue() == "base" {
					baseCount = metric.GetHistogram().GetSampleCount()
				}
				if l.GetName() == "model" && l.GetValue() == "large-v3" {
					largeCount = metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	assert.Equal(t, uint64(2), baseCount)
	assert.Equal(t, uint64(1), largeCount)
}

func TestTranscriptionAudioSeconds(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.TranscriptionAudioSeconds(12.5)
	m.TranscriptionAudioSeconds(30)

	// Unlabelled, so both observations land in the single series; assert
	// via Gather (CollectAndCount only counts series, always 1 here, not
	// observations).
	assert.Equal(t, 1, testutil.CollectAndCount(m.transcriptionAudio))
	families, err := m.Registry().Gather()
	require.NoError(t, err)
	var sampleCount uint64
	for _, f := range families {
		if f.GetName() != "helios_transcription_audio_seconds" {
			continue
		}
		for _, metric := range f.Metric {
			sampleCount += metric.GetHistogram().GetSampleCount()
		}
	}
	assert.Equal(t, uint64(2), sampleCount)
}

func TestJobsInFlight(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.JobsInFlightInc()
	m.JobsInFlightInc()
	assert.Equal(t, float64(2), testutil.ToFloat64(m.jobsInFlight))

	m.JobsInFlightDec()
	assert.Equal(t, float64(1), testutil.ToFloat64(m.jobsInFlight))

	m.JobsInFlightSet(5)
	assert.Equal(t, float64(5), testutil.ToFloat64(m.jobsInFlight))
}

func TestObserveHTTP(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.ObserveHTTP("GET", "/api/v1/transcriptions/{id}", 200, 50*time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/transcriptions/{id}", 500, 10*time.Millisecond)
	m.ObserveHTTP("POST", "/api/v1/transcriptions", 202, 5*time.Millisecond)

	assert.Equal(t, float64(1), testutil.ToFloat64(m.httpRequests.WithLabelValues("GET", "/api/v1/transcriptions/{id}", "200")))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.httpRequests.WithLabelValues("GET", "/api/v1/transcriptions/{id}", "500")))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.httpRequests.WithLabelValues("POST", "/api/v1/transcriptions", "202")))

	// Two observations into the same (method, route) histogram series,
	// regardless of differing status.
	assert.Equal(t, 2, testutil.CollectAndCount(m.httpDuration))
}

// TestHandler_ServesScrape asserts /metrics (via Handler) actually exposes
// every one of the seven series names CONTRACT.md section 10 fixes, once
// something has been recorded, plus the standard runtime collectors.
func TestHandler_ServesScrape(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.TranscriptionSubmitted()
	m.TranscriptionCompleted("succeeded")
	m.TranscriptionDuration("base", time.Second)
	m.TranscriptionAudioSeconds(5)
	m.JobsInFlightSet(1)
	m.ObserveHTTP("GET", "/healthz", 200, time.Millisecond)

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out := string(body)

	for _, name := range []string{
		"helios_transcriptions_submitted_total",
		"helios_transcriptions_completed_total",
		"helios_transcription_duration_seconds",
		"helios_transcription_audio_seconds",
		"helios_jobs_in_flight",
		"helios_http_requests_total",
		"helios_http_request_duration_seconds",
		// proof the Go runtime + process collectors are wired in too.
		"go_goroutines",
		"process_start_time_seconds",
	} {
		assert.Contains(t, out, name, "scrape missing metric %q", name)
	}
}
