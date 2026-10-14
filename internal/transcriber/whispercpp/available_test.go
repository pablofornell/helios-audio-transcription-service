package whispercpp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

func TestAvailable_success(t *testing.T) {
	modelDir := t.TempDir()
	newModel(t, modelDir, "large-v3-turbo-q5_0")
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, DefaultModel: "large-v3-turbo-q5_0", Timeout: 5 * time.Second})
	require.NoError(t, tr.Available(context.Background()))
}

// TestAvailable_defaultModelMissing is the review-confirmed finding: an
// empty (but listable) ModelDir used to report Available successfully, so
// a fresh deploy with no weights showed a green /readyz and then failed
// every job with model_unavailable. Available must itself verify the
// configured default model's weight file exists, not just that the
// directory can be listed.
func TestAvailable_defaultModelMissing(t *testing.T) {
	modelDir := t.TempDir() // empty: no ggml-large-v3-turbo-q5_0.bin written
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, DefaultModel: "large-v3-turbo-q5_0", Timeout: 5 * time.Second})
	err := tr.Available(context.Background())
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
}

// TestAvailable_defaultModelEmptySkipsCheck documents the escape hatch for
// a Transcriber built without a configured default (most tests in this
// package, and any caller that has none to check).
func TestAvailable_defaultModelEmptySkipsCheck(t *testing.T) {
	modelDir := t.TempDir() // empty, deliberately
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	require.NoError(t, tr.Available(context.Background()))
}

func TestAvailable_binaryNotFound(t *testing.T) {
	tr := New(Config{Binary: filepath.Join(t.TempDir(), "does-not-exist"), ModelDir: t.TempDir(), Timeout: 5 * time.Second})
	err := tr.Available(context.Background())
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeInternal, domain.CodeOf(err))
}

func TestAvailable_modelDirNotReadable(t *testing.T) {
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")
	tr := New(Config{Binary: bin, ModelDir: filepath.Join(t.TempDir(), "does-not-exist"), Timeout: 5 * time.Second})
	err := tr.Available(context.Background())
	require.Error(t, err)
	assert.Equal(t, domain.ErrCodeModelUnavailable, domain.CodeOf(err))
}

func TestAvailable_binaryResolvedViaAbsolutePath(t *testing.T) {
	modelDir := t.TempDir()
	// An absolute path (not on PATH) must still resolve via exec.LookPath.
	bin := writeScript(t, "#!/usr/bin/env bash\nexit 0\n")
	require.True(t, filepath.IsAbs(bin))
	require.NoError(t, os.Chmod(bin, 0o755))

	tr := New(Config{Binary: bin, ModelDir: modelDir, Timeout: 5 * time.Second})
	require.NoError(t, tr.Available(context.Background()))
}
