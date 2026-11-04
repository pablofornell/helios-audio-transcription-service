package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func newReq(opts domain.Options) domain.TranscribeRequest {
	return domain.TranscribeRequest{
		AudioPath: "/data/transcriptions/abc-123/audio.wav",
		Options:   opts,
	}
}

func TestTranscribe_deterministic(t *testing.T) {
	tr := New(Config{})
	req := newReq(domain.Options{Model: "large-v3-turbo-q5_0", Task: domain.TaskTranscribe, WordTimestamps: true})

	first, err := tr.Transcribe(context.Background(), req)
	require.NoError(t, err)
	second, err := tr.Transcribe(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, first, second, "the same request must always produce byte-identical results")
}

func TestTranscribe_variesByAudioPathBasename(t *testing.T) {
	// Production's fixed storage layout (transcriptions/<id>/audio.wav —
	// see CONTRACT.md section 5) means every real job's basename is
	// literally "audio", so this proves the derivation itself is sensitive
	// to the basename rather than assuming two different job directories
	// happen to differ in a way that shows up in the text.
	tr := New(Config{})
	a, err := tr.Transcribe(context.Background(), newReq(domain.Options{}))
	require.NoError(t, err)

	reqB := newReq(domain.Options{})
	reqB.AudioPath = "/data/transcriptions/other-job/sample.wav"
	b, err := tr.Transcribe(context.Background(), reqB)
	require.NoError(t, err)

	assert.NotEqual(t, a.Text, b.Text)
}

func TestTranscribe_wordTimestampsHonoured(t *testing.T) {
	tr := New(Config{})

	withWords, err := tr.Transcribe(context.Background(), newReq(domain.Options{WordTimestamps: true}))
	require.NoError(t, err)
	for _, seg := range withWords.Segments {
		assert.NotEmpty(t, seg.Words)
	}

	withoutWords, err := tr.Transcribe(context.Background(), newReq(domain.Options{WordTimestamps: false}))
	require.NoError(t, err)
	for _, seg := range withoutWords.Segments {
		assert.Empty(t, seg.Words)
	}
}

func TestTranscribe_wordsCoverEachSegmentSpanAndAreOrdered(t *testing.T) {
	tr := New(Config{})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{WordTimestamps: true}))
	require.NoError(t, err)

	for _, seg := range got.Segments {
		require.NotEmpty(t, seg.Words)
		assert.Equal(t, seg.Start, seg.Words[0].Start)
		assert.Equal(t, seg.End, seg.Words[len(seg.Words)-1].End)
		for i := 1; i < len(seg.Words); i++ {
			assert.Equal(t, seg.Words[i-1].End, seg.Words[i].Start, "words must tile the segment span with no gaps or overlaps")
		}
	}
}

// The language rule (domain.PlanOutput) sets the output from the detected
// language. Options.Task and Options.Language do not: Spanish is never
// translated, a third language always is.
func TestTranscribe_outputFollowsLanguageRule(t *testing.T) {
	for _, tt := range []struct {
		name      string
		detected  string
		opts      domain.Options
		wantLang  string
		wantOut   string
		translate bool
	}{
		{"default is english", "", domain.Options{Task: domain.TaskTranscribe}, "en", "en", false},
		{"spanish, task=translate", "es", domain.Options{Task: domain.TaskTranslate}, "es", "es", false},
		{"english, task=translate", "en", domain.Options{Task: domain.TaskTranslate}, "en", "en", false},
		{"french, task=transcribe", "fr", domain.Options{Task: domain.TaskTranscribe}, "fr", "en", true},
		{"hint es on french audio", "fr", domain.Options{Language: "es"}, "fr", "en", true},
		{"hint fr on spanish audio", "es", domain.Options{Language: "fr"}, "es", "es", false},
		{"unknown hint xx on english audio", "en", domain.Options{Language: "xx"}, "en", "en", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tr := New(Config{DetectedLanguage: tt.detected})
			got, err := tr.Transcribe(context.Background(), newReq(tt.opts))
			require.NoError(t, err)
			assert.Equal(t, tt.wantLang, got.Language)
			assert.Equal(t, tt.wantOut, got.OutputLanguage)
			assert.Equal(t, tt.translate, strings.Contains(got.Text, "Translated:"))
			if tt.translate {
				assert.Equal(t, domain.TaskTranslate, got.Task)
			} else {
				assert.Equal(t, domain.TaskTranscribe, got.Task)
			}
		})
	}
}

func TestTranscribe_modelEchoedFromRequest(t *testing.T) {
	tr := New(Config{})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{Model: "large-v3-turbo-q5_0"}))
	require.NoError(t, err)
	assert.Equal(t, "large-v3-turbo-q5_0", got.Model)
}

func TestTranscribe_detectedLanguageDefaultsToEnglishAndIgnoresHint(t *testing.T) {
	tr := New(Config{})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{Language: ""}))
	require.NoError(t, err)
	assert.Equal(t, "en", got.Language)

	got2, err := tr.Transcribe(context.Background(), newReq(domain.Options{Language: "es"}))
	require.NoError(t, err)
	assert.Equal(t, "en", got2.Language, "a hint does not change the detected language")
}

func TestTranscribe_forcedFailure(t *testing.T) {
	tr := New(Config{FailWith: domain.ErrCodeToolFailure, FailMessage: "boom in test-land"})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{}))
	assert.Nil(t, got)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeToolFailure, domain.CodeOf(err))
	assert.Contains(t, err.Error(), "boom in test-land")
}

func TestTranscribe_delayCanBeCanceledMidway(t *testing.T) {
	tr := New(Config{Delay: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	got, err := tr.Transcribe(ctx, newReq(domain.Options{}))
	elapsed := time.Since(start)

	assert.Nil(t, got)
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeCanceled, domain.CodeOf(err))
	assert.Less(t, elapsed, 5*time.Second)
}

func TestTranscribe_delayTimeoutClassifiesCorrectly(t *testing.T) {
	tr := New(Config{Delay: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := tr.Transcribe(ctx, newReq(domain.Options{}))
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeTimeout, domain.CodeOf(err))
}

func TestAvailable_alwaysNil(t *testing.T) {
	tr := New(Config{})
	assert.NoError(t, tr.Available(context.Background()))
}
