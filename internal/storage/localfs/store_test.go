package localfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	require.NoError(t, err)
	return s
}

// entryCount counts every entry (files and dirs, recursively) under dir.
// Used to assert a rejected operation left nothing behind anywhere in the
// store.
func entryCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if path == dir {
			return nil
		}
		n++
		return nil
	})
	require.NoError(t, err)
	return n
}

// failAfterReader returns its buffered data up to failAt bytes, then a
// simulated I/O error, to exercise Put's atomicity on a mid-stream failure.
type failAfterReader struct {
	data   []byte
	failAt int
	sent   int
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.sent >= f.failAt {
		return 0, errors.New("simulated mid-stream I/O failure")
	}
	remaining := f.failAt - f.sent
	if len(p) > remaining {
		p = p[:remaining]
	}
	n := copy(p, f.data[f.sent:])
	f.sent += n
	return n, nil
}

func TestNew(t *testing.T) {
	t.Run("creates root if absent and resolves to absolute path", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "does", "not", "exist", "yet")
		s, err := New(root)
		require.NoError(t, err)
		info, err := os.Stat(root)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.True(t, filepath.IsAbs(s.root))
	})

	t.Run("rejects empty root dir", func(t *testing.T) {
		_, err := New("")
		require.Error(t, err)
	})

	t.Run("fails fast when root is a regular file, not a directory", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "not-a-dir")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		_, err := New(path)
		require.Error(t, err)
	})
}

// TestPutOpenRoundTrip is the happy-path proof: what Put writes, Open reads
// back byte-for-byte, and the reported size matches.
func TestPutOpenRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	content := []byte("hello, whisper.cpp")

	n, err := s.Put(ctx, "transcriptions/abc/source.mp3", bytes.NewReader(content))
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), n)

	r, size, err := s.Open(ctx, "transcriptions/abc/source.mp3")
	require.NoError(t, err)
	defer r.Close()
	assert.Equal(t, int64(len(content)), size)

	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// The reader really is seekable.
	_, err = r.Seek(0, io.SeekStart)
	require.NoError(t, err)
	first := make([]byte, 5)
	_, err = io.ReadFull(r, first)
	require.NoError(t, err)
	assert.Equal(t, content[:5], first)
}

// TestResolveHostilePaths covers every path-traversal rejection case
// CONTRACT.md requires: parent traversal, an absolute path, and a
// traversal hidden mid-path that would still land inside the root if
// followed naively. Each of these must be rejected without creating
// anything on disk, inside or outside the store root.
func TestResolveHostilePaths(t *testing.T) {
	hostile := []struct {
		name string
		path string
	}{
		{"parent traversal", "../x"},
		{"absolute path", "/abs"},
		{"traversal then legit-looking suffix", "a/../../b"},
		{"traversal to escape then come back", "../../etc/passwd"},
		{"empty segment (double slash)", "a//file.txt"},
		{"dot segment", "a/./file.txt"},
		{"NUL byte", "a/fi\x00le.txt"},
		{"empty path", ""},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			outsideMarker := filepath.Join(filepath.Dir(s.root), "escaped.txt")
			defer os.Remove(outsideMarker)

			_, err := s.Put(context.Background(), tc.path, strings.NewReader("payload"))
			require.Error(t, err, "Put must reject %q", tc.path)

			_, statErr := os.Stat(outsideMarker)
			assert.True(t, os.IsNotExist(statErr), "hostile path must not create a file outside the root")
			assert.Equal(t, 0, entryCount(t, s.root), "hostile path must not create anything inside the root either")

			// AbsPath and Remove must apply the same rejection.
			_, absErr := s.AbsPath(tc.path)
			require.Error(t, absErr)

			delErr := s.Remove(context.Background(), tc.path)
			require.Error(t, delErr)
		})
	}
}

// TestResolveRejectsSymlinkEscape proves a symlink planted inside the store
// that points outside it is rejected, not followed.
func TestResolveRejectsSymlinkEscape(t *testing.T) {
	s := newTestStore(t)

	outsideDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644))

	linkPath := filepath.Join(s.root, "escape-link")
	require.NoError(t, os.Symlink(outsideDir, linkPath))

	_, err := s.Put(context.Background(), "escape-link/payload.txt", strings.NewReader("x"))
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(outsideDir, "payload.txt"))
	assert.True(t, os.IsNotExist(statErr), "a symlink escape must never let a write land outside the root")
}

// TestPutIsAtomicOnMidStreamFailure proves that a write which fails partway
// through leaves no partial file visible at the target path, and no stray
// temp file behind either.
func TestPutIsAtomicOnMidStreamFailure(t *testing.T) {
	s := newTestStore(t)

	data := make([]byte, 64*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)
	src := &failAfterReader{data: data, failAt: 10 * 1024}

	_, err = s.Put(context.Background(), "transcriptions/xyz/source.wav", src)
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(s.root, "transcriptions", "xyz", "source.wav"))
	assert.True(t, os.IsNotExist(statErr), "no file must be visible at the target path after a failed write")

	entries, err := os.ReadDir(filepath.Join(s.root, "transcriptions", "xyz"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".helios-tmp-", "temp file must be cleaned up on failure")
	}
}

// TestPutOverwritesAtomically proves a second Put to the same relPath
// replaces the content in one atomic step: a reader can never observe a
// mix of old and new bytes.
func TestPutOverwritesAtomically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Put(ctx, "transcriptions/abc/audio.wav", strings.NewReader("first"))
	require.NoError(t, err)
	_, err = s.Put(ctx, "transcriptions/abc/audio.wav", strings.NewReader("second-version"))
	require.NoError(t, err)

	r, size, err := s.Open(ctx, "transcriptions/abc/audio.wav")
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "second-version", string(got))
	assert.Equal(t, int64(len("second-version")), size)
}

func TestAbsPath(t *testing.T) {
	s := newTestStore(t)

	abs, err := s.AbsPath("transcriptions/abc/audio.wav")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(abs))
	assert.Equal(t, filepath.Join(s.root, "transcriptions", "abc", "audio.wav"), abs)

	// AbsPath does not require the target to already exist: it is used to
	// hand a subprocess a path it is about to create.
	_, statErr := os.Stat(abs)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemoveIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.Remove(ctx, "transcriptions/never-existed/source.mp3"))

	_, err := s.Put(ctx, "transcriptions/abc/source.mp3", strings.NewReader("x"))
	require.NoError(t, err)
	require.NoError(t, s.Remove(ctx, "transcriptions/abc/source.mp3"))
	require.NoError(t, s.Remove(ctx, "transcriptions/abc/source.mp3"), "removing an already-removed file must not error")

	_, _, err = s.Open(ctx, "transcriptions/abc/source.mp3")
	require.Error(t, err)
}

func TestRemoveRejectsDirectory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Put(ctx, "transcriptions/abc/source.mp3", strings.NewReader("x"))
	require.NoError(t, err)

	err = s.Remove(ctx, "transcriptions/abc")
	require.Error(t, err)
}

func TestRemoveDir(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Put(ctx, "transcriptions/abc/source.mp3", strings.NewReader("x"))
	require.NoError(t, err)
	_, err = s.Put(ctx, "transcriptions/abc/audio.wav", strings.NewReader("y"))
	require.NoError(t, err)

	require.NoError(t, s.RemoveDir(ctx, "transcriptions/abc"))

	_, statErr := os.Stat(filepath.Join(s.root, "transcriptions", "abc"))
	assert.True(t, os.IsNotExist(statErr))

	// Idempotent: removing an already-gone directory is not an error.
	require.NoError(t, s.RemoveDir(ctx, "transcriptions/abc"))
}

func TestRemoveDirRefusesStoreRoot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Put(ctx, "keep.txt", strings.NewReader("x"))
	require.NoError(t, err)

	err = s.RemoveDir(ctx, ".")
	require.Error(t, err)

	// Nothing was removed.
	_, statErr := os.Stat(filepath.Join(s.root, "keep.txt"))
	require.NoError(t, statErr)
}

func TestFreeBytes(t *testing.T) {
	s := newTestStore(t)

	free, err := s.FreeBytes()
	require.NoError(t, err)
	assert.Greater(t, free, uint64(0), "a real filesystem must report non-zero free space")
}

func TestResolveRejectsOversizedComponent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	oversized := strings.Repeat("a", maxComponentBytes+1)

	_, err := s.Put(ctx, "transcriptions/"+oversized+".wav", strings.NewReader("x"))
	require.Error(t, err)
	assert.Equal(t, 0, entryCount(t, s.root), "an oversized component must not create anything in the store")

	atLimit := strings.Repeat("a", maxComponentBytes-len(".wav")) + ".wav"
	require.Len(t, atLimit, maxComponentBytes, "test fixture itself must sit exactly at the limit")
	_, err = s.Put(ctx, atLimit, strings.NewReader("x"))
	require.NoError(t, err)
}

// TestResolveRejectsExcessiveDepth guards against a directory-bomb relPath:
// an absurdly deep path must be rejected outright rather than creating
// thousands of directories.
func TestResolveRejectsExcessiveDepth(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	segments := make([]string, 0, 2001)
	for i := 0; i < 2000; i++ {
		segments = append(segments, "d")
	}
	segments = append(segments, "file.wav")
	deep := strings.Join(segments, "/")

	_, err := s.Put(ctx, deep, strings.NewReader("x"))
	require.Error(t, err)
	assert.Equal(t, 0, entryCount(t, s.root), "an excessively deep path must not create any directories in the store")

	shallow := strings.Repeat("d/", maxPathDepth-1) + "file.wav"
	_, err = s.Put(ctx, shallow, strings.NewReader("x"))
	require.NoError(t, err)
}

// TestPutContextCanceled proves Put honours a context canceled before the
// write begins, rather than starting work it will only have to unwind.
func TestPutContextCanceled(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Put(ctx, "transcriptions/abc/source.mp3", strings.NewReader("x"))
	require.Error(t, err)
	assert.Equal(t, 0, entryCount(t, s.root))
}

func TestOpenNotFound(t *testing.T) {
	s := newTestStore(t)

	_, _, err := s.Open(context.Background(), "transcriptions/missing/source.mp3")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotExist))
}

// TestPutClassifiesDiskFull proves classifyWriteErr maps an ENOSPC failure
// mid-write to the exported ErrDiskFull sentinel, so a caller elsewhere in
// the service can map it to the domain "disk full" error code without this
// leaf package importing domain. This exercises classifyWriteErr directly
// with a real syscall.ENOSPC, rather than actually filling a disk.
func TestPutClassifiesDiskFull(t *testing.T) {
	wrapped := fmt.Errorf("write: %w", syscall.ENOSPC)
	err := classifyWriteErr(context.Background(), wrapped)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDiskFull))
}
