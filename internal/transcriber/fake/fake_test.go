package fake

import (
	"context"
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
	req := newReq(domain.Options{Model: "base", Task: domain.TaskTranscribe, WordTimestamps: true})

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

func TestTranscribe_taskHonoured(t *testing.T) {
	tr := New(Config{})

	transcribe, err := tr.Transcribe(context.Background(), newReq(domain.Options{Task: domain.TaskTranscribe}))
	require.NoError(t, err)
	assert.NotContains(t, transcribe.Text, "Translated:")
	assert.Equal(t, domain.TaskTranscribe, transcribe.Task)

	translate, err := tr.Transcribe(context.Background(), newReq(domain.Options{Task: domain.TaskTranslate}))
	require.NoError(t, err)
	assert.Contains(t, translate.Text, "Translated:")
	assert.Equal(t, domain.TaskTranslate, translate.Task)
}

func TestTranscribe_modelEchoedFromRequest(t *testing.T) {
	tr := New(Config{})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{Model: "large-v3"}))
	require.NoError(t, err)
	assert.Equal(t, "large-v3", got.Model)
}

func TestTranscribe_languageDefaultsToEnglishWhenAutoDetect(t *testing.T) {
	tr := New(Config{})
	got, err := tr.Transcribe(context.Background(), newReq(domain.Options{Language: ""}))
	require.NoError(t, err)
	assert.Equal(t, "en", got.Language)

	got2, err := tr.Transcribe(context.Background(), newReq(domain.Options{Language: "es"}))
	require.NoError(t, err)
	assert.Equal(t, "es", got2.Language)
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
