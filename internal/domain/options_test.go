package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultOptions(t *testing.T) {
	got := DefaultOptions()
	assert.Equal(t, TaskTranscribe, got.Task)
	assert.Equal(t, 5, got.BeamSize)
	assert.Empty(t, got.Model)
	assert.Empty(t, got.Language)
	assert.Zero(t, got.Temperature)
}

func TestOptionsNormalize(t *testing.T) {
	t.Run("trims and lowercases model and language", func(t *testing.T) {
		got := Options{Model: "  Large-V3 ", Language: " EN "}.Normalize()
		assert.Equal(t, "large-v3", got.Model)
		assert.Equal(t, "en", got.Language)
	})

	t.Run("auto maps to empty", func(t *testing.T) {
		got := Options{Language: "auto"}.Normalize()
		assert.Empty(t, got.Language)

		got = Options{Language: " Auto "}.Normalize()
		assert.Empty(t, got.Language)
	})

	t.Run("zero beam size is filled with the default", func(t *testing.T) {
		got := Options{BeamSize: 0}.Normalize()
		assert.Equal(t, 5, got.BeamSize)
	})

	t.Run("non-zero beam size is left alone", func(t *testing.T) {
		got := Options{BeamSize: 3}.Normalize()
		assert.Equal(t, 3, got.BeamSize)
	})

	t.Run("does not mutate the receiver", func(t *testing.T) {
		orig := Options{Model: "Base"}
		_ = orig.Normalize()
		assert.Equal(t, "Base", orig.Model)
	})
}

func TestOptionsAutoDetectLanguage(t *testing.T) {
	assert.True(t, Options{}.AutoDetectLanguage())
	assert.True(t, Options{Language: ""}.AutoDetectLanguage())
	assert.False(t, Options{Language: "en"}.AutoDetectLanguage())
}

func validOptions() Options {
	return Options{
		Model:       "base",
		Language:    "en",
		Task:        TaskTranscribe,
		BeamSize:    5,
		Temperature: 0,
	}
}

func TestOptionsValidate(t *testing.T) {
	cases := []struct {
		name          string
		opts          Options
		allowedModels []string
		wantErr       bool
		wantField     string
	}{
		{"valid options with no model allowlist", validOptions(), nil, false, ""},
		{
			"valid options with a matching allowlist",
			validOptions(),
			[]string{"tiny", "base", "small"},
			false, "",
		},
		{
			"model not in a non-empty allowlist",
			validOptions(),
			[]string{"tiny", "small"},
			true, "model",
		},
		{
			"empty allowlist skips the model list-membership check",
			func() Options { o := validOptions(); o.Model = "not-a-real-model"; return o }(),
			nil,
			false, "",
		},
		{
			// The finding this guards: an allow-list that parses to empty
			// (e.g. HELIOS_TRANSCRIBER_ALLOWED_MODELS=" ") must not turn
			// into "any model name is fine" -- a traversing model name
			// reaches filepath.Join(ModelDir, "ggml-"+model+".bin") in
			// whispercpp.modelPath and can escape ModelDir. The shape
			// check applies regardless of allowedModels.
			"traversing model name is rejected even with an empty allowlist",
			func() Options { o := validOptions(); o.Model = "../../../etc/passwd"; return o }(),
			nil,
			true, "model",
		},
		{
			"traversing model name is rejected with a non-empty allowlist too",
			func() Options { o := validOptions(); o.Model = "../../../etc/passwd"; return o }(),
			[]string{"tiny", "base"},
			true, "model",
		},
		{
			"model name with a slash is rejected",
			func() Options { o := validOptions(); o.Model = "sub/dir"; return o }(),
			nil,
			true, "model",
		},
		{
			"model name starting with a dot is rejected",
			func() Options { o := validOptions(); o.Model = ".hidden"; return o }(),
			nil,
			true, "model",
		},
		{
			"empty task is invalid",
			func() Options { o := validOptions(); o.Task = ""; return o }(),
			nil,
			true, "task",
		},
		{
			"unknown task is invalid",
			func() Options { o := validOptions(); o.Task = "summarize"; return o }(),
			nil,
			true, "task",
		},
		{"beam size 1 is valid (lower bound)", func() Options { o := validOptions(); o.BeamSize = 1; return o }(), nil, false, ""},
		{"beam size 16 is valid (upper bound)", func() Options { o := validOptions(); o.BeamSize = 16; return o }(), nil, false, ""},
		{"beam size 0 is invalid", func() Options { o := validOptions(); o.BeamSize = 0; return o }(), nil, true, "beamSize"},
		{"beam size 17 is invalid", func() Options { o := validOptions(); o.BeamSize = 17; return o }(), nil, true, "beamSize"},
		{"beam size negative is invalid", func() Options { o := validOptions(); o.BeamSize = -1; return o }(), nil, true, "beamSize"},
		{"temperature 0 is valid (lower bound)", func() Options { o := validOptions(); o.Temperature = 0; return o }(), nil, false, ""},
		{"temperature 1 is valid (upper bound)", func() Options { o := validOptions(); o.Temperature = 1; return o }(), nil, false, ""},
		{"temperature negative is invalid", func() Options { o := validOptions(); o.Temperature = -0.1; return o }(), nil, true, "temperature"},
		{"temperature above 1 is invalid", func() Options { o := validOptions(); o.Temperature = 1.1; return o }(), nil, true, "temperature"},
		{
			"initial prompt at exactly 1024 bytes is valid",
			func() Options { o := validOptions(); o.InitialPrompt = strings.Repeat("a", 1024); return o }(),
			nil, false, "",
		},
		{
			"initial prompt over 1024 bytes is invalid",
			func() Options { o := validOptions(); o.InitialPrompt = strings.Repeat("a", 1025); return o }(),
			nil, true, "initialPrompt",
		},
		{"empty language is valid (auto-detect)", func() Options { o := validOptions(); o.Language = ""; return o }(), nil, false, ""},
		{"2-letter language is valid", func() Options { o := validOptions(); o.Language = "en"; return o }(), nil, false, ""},
		{"3-letter language is valid", func() Options { o := validOptions(); o.Language = "eng"; return o }(), nil, false, ""},
		{"1-letter language is invalid", func() Options { o := validOptions(); o.Language = "e"; return o }(), nil, true, "language"},
		{"4-letter language is invalid", func() Options { o := validOptions(); o.Language = "engl"; return o }(), nil, true, "language"},
		{"uppercase language is invalid", func() Options { o := validOptions(); o.Language = "EN"; return o }(), nil, true, "language"},
		{"numeric language is invalid", func() Options { o := validOptions(); o.Language = "e1"; return o }(), nil, true, "language"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate(tc.allowedModels)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, ErrCodeInvalidInput, CodeOf(err))
			var domErr *Error
			require.ErrorAs(t, err, &domErr)
			assert.Equal(t, tc.wantField, domErr.Details["field"])
		})
	}
}

// A caller who posts `{}` -- or omits the options field entirely -- is asking
// for a plain transcription with sensible defaults, not an invalid_input
// error. Normalize is what makes that true, so guard it directly: this is
// the single most common submission shape there is.
func TestNormalizeMakesZeroOptionsValid(t *testing.T) {
	t.Parallel()

	got := Options{}.Normalize()

	require.Equal(t, TaskTranscribe, got.Task)
	require.Equal(t, defaultBeamSize, got.BeamSize)
	require.True(t, got.AutoDetectLanguage())
	require.NoError(t, got.Validate([]string{"tiny", "base"}),
		"a zero Options must normalize into a valid request even when models are restricted")
}

// An empty Model means "whatever the operator configured as the default".
// Validate cannot see that configuration, so it must not reject the empty
// string -- app.Service resolves it afterwards.
func TestValidateAcceptsEmptyModelAgainstAllowList(t *testing.T) {
	t.Parallel()

	opts := DefaultOptions()
	require.Equal(t, "", opts.Model)
	require.NoError(t, opts.Validate([]string{"tiny", "base"}))

	opts.Model = "nonexistent"
	require.Error(t, opts.Validate([]string{"tiny", "base"}),
		"a model that is named but not allowed must still be rejected")
}
