package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns every Prometheus series Helios exports and the private
// registry they are registered against. It is built once at startup
// (NewMetrics) and threaded through the app, exposing typed helper methods
// rather than raw label maps — that keeps a label name typo from ever
// compiling, which matters once several packages are calling in.
type Metrics struct {
	registry *prometheus.Registry

	transcriptionsSubmitted prometheus.Counter
	transcriptionsCompleted *prometheus.CounterVec
	transcriptionDuration   *prometheus.HistogramVec
	transcriptionAudio      prometheus.Histogram
	jobsInFlight            prometheus.Gauge
	httpRequests            *prometheus.CounterVec
	httpDuration            *prometheus.HistogramVec
}

// NewMetrics builds a Metrics bound to reg, a private *prometheus.Registry
// (never prometheus.DefaultRegisterer, so tests get fully isolated
// registries), and registers every series below plus the standard Go
// runtime and process collectors.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		registry: reg,

		transcriptionsSubmitted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "helios_transcriptions_submitted_total",
			Help: "Total number of transcriptions submitted.",
		}),

		transcriptionsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "helios_transcriptions_completed_total",
			Help: "Total number of transcriptions that reached a terminal state, by status.",
		}, []string{"status"}),

		transcriptionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "helios_transcription_duration_seconds",
			Help:    "Wall-clock duration of a completed transcription run, by model.",
			Buckets: prometheus.DefBuckets,
		}, []string{"model"}),

		transcriptionAudio: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "helios_transcription_audio_seconds",
			Help:    "Duration of the source audio transcribed.",
			Buckets: prometheus.DefBuckets,
		}),

		jobsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "helios_jobs_in_flight",
			Help: "Number of transcription jobs currently being worked.",
		}),

		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "helios_http_requests_total",
			Help: "Total number of HTTP requests handled, by method, route and status.",
		}, []string{"method", "route", "status"}),

		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "helios_http_request_duration_seconds",
			Help:    "Duration of HTTP requests handled, by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}

	reg.MustRegister(
		m.transcriptionsSubmitted,
		m.transcriptionsCompleted,
		m.transcriptionDuration,
		m.transcriptionAudio,
		m.jobsInFlight,
		m.httpRequests,
		m.httpDuration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// Handler returns the promhttp handler serving a scrape of m's private
// registry. Mount it at /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry returns m's private *prometheus.Registry, for callers that need
// to register their own collectors against the same registry Handler
// serves.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// TranscriptionSubmitted records that a new transcription was accepted.
func (m *Metrics) TranscriptionSubmitted() {
	m.transcriptionsSubmitted.Inc()
}

// TranscriptionCompleted records that a transcription reached a terminal
// status.
func (m *Metrics) TranscriptionCompleted(status string) {
	m.transcriptionsCompleted.WithLabelValues(status).Inc()
}

// TranscriptionDuration records how long a completed transcription run
// took, by the model that produced it.
func (m *Metrics) TranscriptionDuration(model string, d time.Duration) {
	m.transcriptionDuration.WithLabelValues(model).Observe(d.Seconds())
}

// TranscriptionAudioSeconds records the duration of the source audio a
// completed transcription covered.
func (m *Metrics) TranscriptionAudioSeconds(seconds float64) {
	m.transcriptionAudio.Observe(seconds)
}

// JobsInFlightSet sets the current number of transcription jobs being
// worked.
func (m *Metrics) JobsInFlightSet(n int) {
	m.jobsInFlight.Set(float64(n))
}

// JobsInFlightInc increments the number of transcription jobs being worked
// by one.
func (m *Metrics) JobsInFlightInc() {
	m.jobsInFlight.Inc()
}

// JobsInFlightDec decrements the number of transcription jobs being worked
// by one.
func (m *Metrics) JobsInFlightDec() {
	m.jobsInFlight.Dec()
}

// ObserveHTTP records one completed HTTP request: method, the matched
// route pattern, status code and how long it took. route SHOULD be the
// router's matched pattern (e.g. "/api/v1/transcriptions/{id}"), never the
// raw request path, to keep the label space bounded.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	statusStr := strconv.Itoa(status)
	m.httpRequests.WithLabelValues(method, route, statusStr).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}
