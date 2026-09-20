package whispercpp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// sampleOJ is a -oj (no tokens) fixture, shaped exactly like whisper-cli's
// real output_json (verified live against examples/cli/cli.cpp upstream).
const sampleOJ = `{
  "result": {"language": "en"},
  "transcription": [
    {
      "timestamps": {"from": "00:00:00,000", "to": "00:00:02,400"},
      "offsets": {"from": 0, "to": 2400},
      "text": " Hello there."
    },
    {
      "timestamps": {"from": "00:00:02,400", "to": "00:00:05,000"},
      "offsets": {"from": 2400, "to": 5000},
      "text": " How are you?"
    }
  ]
}`

// sampleOJF is a -ojf (tokens included) fixture with a sub-word split
// ("Hel"+"lo") and one special token to be dropped.
const sampleOJF = `{
  "result": {"language": "en"},
  "transcription": [
    {
      "offsets": {"from": 0, "to": 2400},
      "text": " Hello there.",
      "tokens": [
        {"text": "[_BEG_]", "id": 50363, "p": 1.0},
        {"text": " Hel", "offsets": {"from": 0, "to": 400}, "id": 1, "p": 0.9},
        {"text": "lo", "offsets": {"from": 400, "to": 700}, "id": 2, "p": 0.95},
        {"text": " there.", "offsets": {"from": 700, "to": 1200}, "id": 3, "p": 0.99},
        {"text": "[_TT_5]", "id": 50368, "p": 1.0}
      ]
    }
  ]
}`

func TestParseWhisperJSON_ojMode_noTokensEvenIfPresent(t *testing.T) {
	// opts.WordTimestamps is false even though sampleOJF happens to carry
	// tokens; -oj-mode output should never populate Words.
	got, err := parseWhisperJSON([]byte(sampleOJF), domain.Options{WordTimestamps: false})
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	assert.Empty(t, got.Segments[0].Words)
}

func TestParseWhisperJSON_basicSegments(t *testing.T) {
	got, err := parseWhisperJSON([]byte(sampleOJ), domain.Options{})
	require.NoError(t, err)

	assert.Equal(t, "en", got.Language)
	require.Len(t, got.Segments, 2)

	assert.Equal(t, 0, got.Segments[0].ID)
	assert.Equal(t, 0.0, got.Segments[0].Start)
	assert.Equal(t, 2.4, got.Segments[0].End)
	assert.Equal(t, "Hello there.", got.Segments[0].Text)

	assert.Equal(t, 1, got.Segments[1].ID)
	assert.Equal(t, "How are you?", got.Segments[1].Text)

	assert.Equal(t, "Hello there. How are you?", got.Text)
	assert.Equal(t, 5.0, got.DurationSeconds, "duration is the last segment's end")
}

func TestParseWhisperJSON_emptyTranscriptionArray(t *testing.T) {
	got, err := parseWhisperJSON([]byte(`{"result":{"language":"en"},"transcription":[]}`), domain.Options{})
	require.NoError(t, err)
	assert.Empty(t, got.Segments)
	assert.Equal(t, "", got.Text)
	assert.Equal(t, 0.0, got.DurationSeconds)
}

func TestParseWhisperJSON_segmentWithNoTokens(t *testing.T) {
	data := `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1000},"text":" hi","tokens":[]}]}`
	got, err := parseWhisperJSON([]byte(data), domain.Options{WordTimestamps: true})
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	assert.Empty(t, got.Segments[0].Words)
	assert.Equal(t, "hi", got.Segments[0].Text)
}

func TestParseWhisperJSON_specialTokensDropped(t *testing.T) {
	got, err := parseWhisperJSON([]byte(sampleOJF), domain.Options{WordTimestamps: true})
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	words := got.Segments[0].Words
	for _, w := range words {
		assert.NotContains(t, w.Word, "[_")
		assert.NotContains(t, w.Word, "<|")
	}
}

func TestParseWhisperJSON_endOfTextSpecialTokenDropped(t *testing.T) {
	data := `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1000},"text":" hi","tokens":[
		{"text":" hi","offsets":{"from":0,"to":500},"p":0.9},
		{"text":"<|endoftext|>","p":1.0}
	]}]}`
	got, err := parseWhisperJSON([]byte(data), domain.Options{WordTimestamps: true})
	require.NoError(t, err)
	require.Len(t, got.Segments[0].Words, 1)
	assert.Equal(t, "hi", got.Segments[0].Words[0].Word)
}

func TestParseWhisperJSON_subWordPiecesMergeIntoWholeWords(t *testing.T) {
	got, err := parseWhisperJSON([]byte(sampleOJF), domain.Options{WordTimestamps: true})
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	words := got.Segments[0].Words
	require.Len(t, words, 2, "the [_BEG_]/[_TT_5] special tokens must be dropped and 'Hel'+'lo' merged into one word")

	assert.Equal(t, "Hello", words[0].Word)
	assert.Equal(t, 0.0, words[0].Start, "start comes from the first sub-piece")
	assert.Equal(t, 0.7, words[0].End, "end is extended to the last merged sub-piece's end")
	assert.Equal(t, 0.9, words[0].Probability, "probability is the first sub-piece's, not overwritten by later merges")

	assert.Equal(t, "there.", words[1].Word)
	assert.Equal(t, 0.7, words[1].Start)
	assert.Equal(t, 1.2, words[1].End)
	assert.Equal(t, 0.99, words[1].Probability)
}

func TestParseWhisperJSON_tokenWithoutOffsetsToleratesMissingField(t *testing.T) {
	data := `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1000},"text":" hi","tokens":[
		{"text":" hi","p":0.9}
	]}]}`
	got, err := parseWhisperJSON([]byte(data), domain.Options{WordTimestamps: true})
	require.NoError(t, err)
	require.Len(t, got.Segments[0].Words, 1)
	assert.Equal(t, "hi", got.Segments[0].Words[0].Word)
	assert.Equal(t, 0.0, got.Segments[0].Words[0].Start)
	assert.Equal(t, 0.0, got.Segments[0].Words[0].End)
}

func TestParseWhisperJSON_overOneHourOffsets(t *testing.T) {
	// 1h02m03.456s = 3723456 ms.
	data := `{"result":{"language":"en"},"transcription":[{"offsets":{"from":3723456,"to":3725000},"text":" late segment"}]}`
	got, err := parseWhisperJSON([]byte(data), domain.Options{})
	require.NoError(t, err)
	require.Len(t, got.Segments, 1)
	assert.Equal(t, 3723.456, got.Segments[0].Start)
	assert.Equal(t, 3725.0, got.Segments[0].End)
	assert.Equal(t, 3725.0, got.DurationSeconds)
}

func TestParseWhisperJSON_malformedJSON(t *testing.T) {
	_, err := parseWhisperJSON([]byte("not json"), domain.Options{})
	require.Error(t, err)
}

func TestParseWhisperJSON_textIsSpaceNormalizedAndTrimmed(t *testing.T) {
	data := `{"result":{"language":"en"},"transcription":[{"offsets":{"from":0,"to":1000},"text":"   hi   there  "}]}`
	got, err := parseWhisperJSON([]byte(data), domain.Options{})
	require.NoError(t, err)
	assert.Equal(t, "hi there", got.Segments[0].Text)
	assert.Equal(t, "hi there", got.Text)
}

func TestIsSpecialToken(t *testing.T) {
	cases := map[string]bool{
		"[_BEG_]":               true,
		"[_EOT_]":               true,
		"[_TT_5]":               true,
		"[_LANG_en]":            true,
		"<|endoftext|>":         true,
		"<|startoftranscript|>": true,
		" Hello":                false,
		"lo":                    false,
		"":                      false,
	}
	for text, want := range cases {
		t.Run(text, func(t *testing.T) {
			assert.Equal(t, want, isSpecialToken(text))
		})
	}
}

func TestNormalizeSpaces(t *testing.T) {
	assert.Equal(t, "a b c", normalizeSpaces("  a   b\tc\n"))
	assert.Equal(t, "", normalizeSpaces("   "))
}
