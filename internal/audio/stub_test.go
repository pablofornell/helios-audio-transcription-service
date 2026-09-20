package audio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeScript writes body verbatim as an executable script under
// t.TempDir() and returns its absolute path, suitable for Config.FFmpegBin
// or Config.FFprobeBin — mirrors BLUEPRINT ytdlp's writeRawScript. No test
// in this package touches a real ffmpeg/ffprobe binary for exit-code or
// timeout/kill classification: every such case runs a generated shell
// script instead, driven entirely by Config pointing at it.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub.sh")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}
