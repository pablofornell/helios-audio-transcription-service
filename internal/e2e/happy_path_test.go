package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// terminalDeadline bounds how long this package's happy-path scenarios wait
// for a submitted job to reach a terminal status. The fake transcriber
// returns instantly and the fixtures are 2-second clips, so the real
// bottleneck is claim/poll latency (tens of milliseconds, per
// newTestConfig) -- a few seconds is already generous slack.
const terminalDeadline = 10 * time.Second

// TestFullPipeline_HappyPath drives one submission through the entire real
// pipeline over HTTP -- multipart upload, probe, normalize, fake
// transcribe, Finish -- and then fetches its transcript in all four
// formats. This is the single most important test in this suite: it is the
// only place every independently-built package (httpapi, app, postgres,
// localfs, audio, jobs, transcript) is proven to actually work together.
func TestFullPipeline_HappyPath(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil)
	assert.Equal(t, domain.StatusPending, created.Status)
	assert.NotEmpty(t, created.ID)

	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)

	require.NotNil(t, final.Result, "a succeeded transcription must carry a Result")
	assert.NotEmpty(t, final.Result.Segments, "expected at least one segment")
	assert.NotEmpty(t, final.Result.Language, "expected a language, auto-detected or forced")
	assert.NotEmpty(t, final.Result.Text, "expected non-empty transcript text")
	assert.Equal(t, domain.TaskTranscribe, final.Result.Task)

	// The probed audio metadata (CONTRACT.md section 1 step 2) must be
	// persisted alongside the upload-known fields -- this is what proves
	// internal/audio's real ffprobe call actually ran and its result made
	// it all the way through jobs.execute -> Queue.Finish -> the stored
	// row, not just that Submit recorded the upload's own SizeBytes/MediaType.
	assert.InDelta(t, 2.0, final.Audio.DurationSeconds, 0.5, "expected the probed duration to be close to the fixture's 2s")
	assert.Equal(t, 44100, final.Audio.SampleRateHz, "expected the source's own probed sample rate, not the normalized 16kHz")
	assert.Positive(t, final.Audio.Channels)
	assert.NotEmpty(t, final.Audio.Codec)

	t.Run("transcript_json", func(t *testing.T) {
		resp := getTranscript(t, ts.base, created.ID, "json")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NotEmpty(t, body)

		var result domain.Result
		require.NoError(t, json.Unmarshal(body, &result))
		assert.NotEmpty(t, result.Text)
		assert.NotEmpty(t, result.Segments)
	})

	t.Run("transcript_txt", func(t *testing.T) {
		resp := getTranscript(t, ts.base, created.ID, "txt")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotEmpty(t, body)
	})

	t.Run("transcript_srt", func(t *testing.T) {
		resp := getTranscript(t, ts.base, created.ID, "srt")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/x-subrip; charset=utf-8", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assertWellFormedSRT(t, string(body))
	})

	t.Run("transcript_vtt", func(t *testing.T) {
		resp := getTranscript(t, ts.base, created.ID, "vtt")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/vtt; charset=utf-8", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assertWellFormedVTT(t, string(body))
	})
}

// assertWellFormedSRT checks the minimal structural shape internal/transcript's
// RenderSRT documents: a 1-based index line, a "-->"-separated CRLF
// timestamp line, then text, each block blank-line separated by CRLF.
func assertWellFormedSRT(t *testing.T, body string) {
	t.Helper()
	require.NotEmpty(t, body)
	require.Contains(t, body, "\r\n", "expected CRLF line endings, per RenderSRT's own doc")
	blocks := strings.Split(strings.TrimRight(body, "\r\n"), "\r\n\r\n")
	require.NotEmpty(t, blocks)

	for i, block := range blocks {
		lines := strings.Split(block, "\r\n")
		require.GreaterOrEqualf(t, len(lines), 3, "block %d: expected at least index/timestamp/text lines, got %q", i, block)
		assert.Equal(t, strings.TrimSpace(lines[0]), lines[0], "block %d: index line should have no stray whitespace", i)
		assert.Contains(t, lines[1], " --> ", "block %d: expected a --> separated timestamp line", i)
		assert.NotEmpty(t, lines[2], "block %d: expected non-empty cue text", i)
	}
}

// assertWellFormedVTT checks the minimal structural shape internal/transcript's
// RenderVTT documents: the mandatory "WEBVTT" header followed by
// "-->"-separated LF timestamp lines and cue text.
func assertWellFormedVTT(t *testing.T, body string) {
	t.Helper()
	require.NotEmpty(t, body)
	require.True(t, strings.HasPrefix(body, "WEBVTT\n\n"), "expected the mandatory WEBVTT header")
	assert.Contains(t, body, " --> ", "expected at least one --> separated timestamp line")
}

// TestWordTimestamps submits with wordTimestamps:true and asserts words
// come back on every segment, end to end through the real HTTP API and the
// fake transcriber's own honouring of that option (see
// internal/transcriber/fake's doc).
func TestWordTimestamps(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	opts := &domain.Options{WordTimestamps: true}
	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", opts)

	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
	require.NotEmpty(t, final.Result.Segments)
	for _, seg := range final.Result.Segments {
		require.NotEmptyf(t, seg.Words, "segment %d: expected word timestamps since Options.WordTimestamps was true", seg.ID)
		for _, w := range seg.Words {
			assert.NotEmpty(t, w.Word)
			assert.GreaterOrEqual(t, w.End, w.Start)
		}
	}
}

// TestThirdLanguageIsTranslated submits audio with a French language hint
// and asserts the language rule end to end: the persisted Result.Task is
// translate and the output language is English, and the fake transcriber's
// own translate-shaped output (a "Translated: " prefix -- see
// internal/transcriber/fake's Transcribe doc) made it all the way back
// through the API. The submitted task is stored as sent but plays no part.
func TestThirdLanguageIsTranslated(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	opts := &domain.Options{Language: "fr", Task: domain.TaskTranscribe}
	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", opts)
	assert.Equal(t, domain.TaskTranscribe, created.Options.Task, "expected the submitted task to be echoed back immediately")

	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
	assert.Equal(t, domain.TaskTranslate, final.Result.Task)
	assert.Equal(t, "fr", final.Result.Language)
	assert.Equal(t, "en", final.Result.OutputLanguage)
	assert.Contains(t, final.Result.Text, "Translated:", "expected the fake transcriber's translate-shaped output")
}

// TestSpanishIsNeverTranslated submits Spanish audio with task:translate and
// asserts the transcript stays Spanish.
func TestSpanishIsNeverTranslated(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	opts := &domain.Options{Language: "es", Task: domain.TaskTranslate}
	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", opts)

	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
	assert.Equal(t, domain.TaskTranscribe, final.Result.Task)
	assert.Equal(t, "es", final.Result.OutputLanguage)
	assert.NotContains(t, final.Result.Text, "Translated:")
}

// TestOptionsDefaults submits with NO options part at all -- guarding
// domain.Options.Normalize's behaviour (an absent/empty Options.Task
// becomes TaskTranscribe) and app.Service.Submit's own default-model
// substitution (an empty Options.Model resolves to Deps.DefaultModel,
// here newTestConfig's Transcriber.DefaultModel).
func TestOptionsDefaults(t *testing.T) {
	requireDocker(t)
	requireFFmpeg(t)

	ts := newTestServer(t)

	created := submitFile(t, ts.base, fixtureGoodMP3, "audio/mpeg", nil /* no options part at all */)
	assert.Equal(t, domain.TaskTranscribe, created.Options.Task, "an absent options part must default to task=transcribe")
	assert.Equal(t, ts.stack.Cfg.Transcriber.DefaultModel, created.Options.Model,
		"an absent options part must resolve Model to the configured default")

	final := waitForStatus(t, ts.base, created.ID, domain.StatusSucceeded, terminalDeadline)
	require.NotNil(t, final.Result)
	assert.Equal(t, domain.TaskTranscribe, final.Result.Task)
	assert.Equal(t, ts.stack.Cfg.Transcriber.DefaultModel, final.Result.Model)
}
