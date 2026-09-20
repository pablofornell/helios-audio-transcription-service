package transcript

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestRenderText(t *testing.T) {
	cases := []struct {
		name string
		in   *domain.Result
		want string
	}{
		{
			name: "nil result",
			in:   nil,
			want: "",
		},
		{
			name: "zero segments falls back to Text",
			in:   &domain.Result{Text: "hello there"},
			want: "hello there",
		},
		{
			name: "segments are trimmed and joined with a single space",
			in: &domain.Result{
				Text: "unused when segments present",
				Segments: []domain.Segment{
					{ID: 0, Text: " Hello there."},
					{ID: 1, Text: " How are you? "},
				},
			},
			want: "Hello there. How are you?",
		},
		{
			name: "whitespace-only segment is skipped, not an empty entry",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Text: " Hello."},
					{ID: 1, Text: "   \n\t  "},
					{ID: 2, Text: " Bye."},
				},
			},
			want: "Hello. Bye.",
		},
		{
			name: "interior newline is preserved within a segment",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Text: " Line one\nLine two "},
				},
			},
			want: "Line one\nLine two",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RenderText(tc.in))
		})
	}
}

func TestRenderSRT(t *testing.T) {
	cases := []struct {
		name string
		in   *domain.Result
		want string
	}{
		{
			name: "nil result",
			in:   nil,
			want: "",
		},
		{
			name: "zero segments",
			in:   &domain.Result{Text: "hello there"},
			want: "",
		},
		{
			name: "all-whitespace segment is skipped and does not consume an index",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 1, Text: " Hello."},
					{ID: 1, Start: 1, End: 2, Text: "   "},
					{ID: 2, Start: 2, End: 3, Text: " Bye."},
				},
			},
			want: "1\r\n00:00:00,000 --> 00:00:01,000\r\nHello.\r\n\r\n" +
				"2\r\n00:00:02,000 --> 00:00:03,000\r\nBye.\r\n\r\n",
		},
		{
			name: "timing past one hour",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 3661.5, End: 3662.75, Text: " Long recording."},
				},
			},
			want: "1\r\n01:01:01,500 --> 01:01:02,750\r\nLong recording.\r\n\r\n",
		},
		{
			name: "negative start is clamped to zero",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: -0.5, End: 1, Text: " Clamped."},
				},
			},
			want: "1\r\n00:00:00,000 --> 00:00:01,000\r\nClamped.\r\n\r\n",
		},
		{
			name: "interior newline becomes a CRLF cue line break",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 1, Text: " Line one\nLine two "},
				},
			},
			want: "1\r\n00:00:00,000 --> 00:00:01,000\r\nLine one\r\nLine two\r\n\r\n",
		},
		{
			name: "two-segment fixture, byte for byte",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 2.4, Text: " Hello there."},
					{ID: 1, Start: 2.4, End: 5.1, Text: " General Kenobi."},
				},
			},
			want: "1\r\n00:00:00,000 --> 00:00:02,400\r\nHello there.\r\n\r\n" +
				"2\r\n00:00:02,400 --> 00:00:05,100\r\nGeneral Kenobi.\r\n\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RenderSRT(tc.in))
		})
	}
}

func TestRenderVTT(t *testing.T) {
	cases := []struct {
		name string
		in   *domain.Result
		want string
	}{
		{
			name: "nil result",
			in:   nil,
			want: "",
		},
		{
			name: "zero segments still emits the bare header",
			in:   &domain.Result{Text: "hello there"},
			want: "WEBVTT\n\n",
		},
		{
			name: "all-whitespace segment is skipped",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 1, Text: " Hello."},
					{ID: 1, Start: 1, End: 2, Text: "\n  \n"},
				},
			},
			want: "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n\n",
		},
		{
			name: "timing past one hour",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 3661.5, End: 3662.75, Text: " Long recording."},
				},
			},
			want: "WEBVTT\n\n01:01:01.500 --> 01:01:02.750\nLong recording.\n\n",
		},
		{
			name: "interior newline is preserved as an LF cue line break",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 1, Text: " Line one\nLine two "},
				},
			},
			want: "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nLine one\nLine two\n\n",
		},
		{
			name: "two-segment fixture, byte for byte",
			in: &domain.Result{
				Segments: []domain.Segment{
					{ID: 0, Start: 0, End: 2.4, Text: " Hello there."},
					{ID: 1, Start: 2.4, End: 5.1, Text: " General Kenobi."},
				},
			},
			want: "WEBVTT\n\n" +
				"00:00:00.000 --> 00:00:02.400\nHello there.\n\n" +
				"00:00:02.400 --> 00:00:05.100\nGeneral Kenobi.\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RenderVTT(tc.in))
		})
	}
}

func TestParseFormat(t *testing.T) {
	t.Run("accepted values", func(t *testing.T) {
		cases := map[string]Format{
			"":     FormatJSON,
			"json": FormatJSON,
			"txt":  FormatText,
			"srt":  FormatSRT,
			"vtt":  FormatVTT,
		}
		for in, want := range cases {
			got, err := ParseFormat(in)
			require.NoError(t, err, "input %q", in)
			assert.Equal(t, want, got, "input %q", in)
		}
	})

	t.Run("unknown value is invalid_input naming the field", func(t *testing.T) {
		_, err := ParseFormat("xml")
		require.Error(t, err)
		derr := domain.AsError(err)
		require.NotNil(t, derr)
		assert.Equal(t, domain.ErrCodeInvalidInput, derr.Code)
		assert.Equal(t, "format", derr.Details["field"])
	})
}

func TestFormatContentType(t *testing.T) {
	cases := map[Format]string{
		FormatJSON: "application/json",
		FormatText: "text/plain; charset=utf-8",
		FormatSRT:  "application/x-subrip; charset=utf-8",
		FormatVTT:  "text/vtt; charset=utf-8",
	}
	for f, want := range cases {
		assert.Equal(t, want, f.ContentType(), "format %q", f)
	}
}

func TestRender(t *testing.T) {
	result := &domain.Result{
		Text:     "Hello there.",
		Language: "en",
		Segments: []domain.Segment{
			{ID: 0, Start: 0, End: 1, Text: " Hello there."},
		},
	}

	t.Run("json marshals the Result itself", func(t *testing.T) {
		got, err := Render(result, FormatJSON)
		require.NoError(t, err)

		want, err := json.Marshal(result)
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(got))
	})

	t.Run("txt delegates to RenderText", func(t *testing.T) {
		got, err := Render(result, FormatText)
		require.NoError(t, err)
		assert.Equal(t, RenderText(result), string(got))
	})

	t.Run("srt delegates to RenderSRT", func(t *testing.T) {
		got, err := Render(result, FormatSRT)
		require.NoError(t, err)
		assert.Equal(t, RenderSRT(result), string(got))
	})

	t.Run("vtt delegates to RenderVTT", func(t *testing.T) {
		got, err := Render(result, FormatVTT)
		require.NoError(t, err)
		assert.Equal(t, RenderVTT(result), string(got))
	})

	t.Run("unknown format is invalid_input", func(t *testing.T) {
		_, err := Render(result, Format("bogus"))
		require.Error(t, err)
		assert.Equal(t, domain.ErrCodeInvalidInput, domain.CodeOf(err))
	})
}
