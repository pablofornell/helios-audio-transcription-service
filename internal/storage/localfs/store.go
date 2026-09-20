// Package localfs implements a store rooted at a directory on the local
// filesystem (typically a Docker volume). It is a leaf package: it imports
// nothing else under internal/, so it can be wired against the app.Store
// port declared elsewhere without creating an import cycle.
//
// The central concern of this package is path safety: every relPath it is
// given transitively originates from a caller-supplied identifier or
// filename, so it must be treated as attacker-controlled. resolve is the
// single chokepoint every exported method routes through to turn a relPath
// into a filesystem path that is provably inside the store root, following
// symlinks where they exist and rejecting anything that would otherwise
// escape.
package localfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// dirPerm and filePerm are the permissions used for directories and regular
// files this store creates. The volume is not shared with anything outside
// the Helios process, so group/other access is not needed.
const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// maxComponentBytes and maxPathDepth bound relPath the way the package doc
// requires: every relPath is attacker-controlled (it is transitively an
// uploaded filename or a job ID), so both a single absurdly long path
// segment and an absurdly deep one must be rejected as invalid input rather
// than handed to the filesystem, which would otherwise either fail with an
// unclassified ENAMETOOLONG or happily create an unbounded number of
// directories for a single relPath.
const (
	// maxComponentBytes matches ext4/xfs's NAME_MAX (255 bytes), the
	// tightest common limit.
	maxComponentBytes = 255
	// maxPathDepth caps the number of segments relPath may have. Real
	// artifact paths this package builds (transcriptions/<id>/<file>) are
	// a handful of segments deep; 32 leaves generous room for that while
	// still making a directory-bomb relPath rejected outright.
	maxPathDepth = 32
)

// maxTempNameAttempts bounds how many random names createTemp tries before
// giving up. With a 128-bit random suffix per attempt, a collision on the
// first try is already astronomically unlikely; this only guards against a
// broken RNG looping forever.
const maxTempNameAttempts = 100

// ErrDiskFull is returned (wrapped) by Put when a write fails because the
// underlying device is out of space (ENOSPC). It is a distinguishable
// sentinel so a caller can map it to a domain-level "disk full" error code
// without this leaf package importing domain itself.
var ErrDiskFull = errors.New("localfs: disk full")

// ErrNotExist is returned (wrapped) by Open when relPath does not resolve
// to an existing file. It mirrors fs.ErrNotExist so callers can also use
// errors.Is(err, fs.ErrNotExist) interchangeably; it exists as its own
// sentinel so this package's contract does not depend on os's exact
// wrapping behaviour.
var ErrNotExist = errors.New("localfs: not found")

// Store implements a path-hardened store over a directory rooted at an
// absolute, symlink-resolved path on the local filesystem.
type Store struct {
	// root is the store's root directory, already made absolute and
	// resolved through any symlinks at New time. Every resolved path this
	// Store operates on either equals root or has root+separator as a
	// prefix.
	root string
}

// New constructs a Store rooted at rootDir, creating it if it does not
// already exist. It fails fast if rootDir cannot be resolved to a writable
// directory, so a misconfigured volume mount is caught at startup rather
// than on the first upload.
func New(rootDir string) (*Store, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, fmt.Errorf("localfs: root dir is required")
	}

	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("localfs: resolve root dir %q: %w", rootDir, err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("localfs: create root dir %q: %w", abs, err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("localfs: resolve symlinks in root dir %q: %w", abs, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("localfs: stat root dir %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("localfs: root %q is not a directory", root)
	}

	s := &Store{root: root}
	if err := s.probeWritable(); err != nil {
		return nil, err
	}
	return s, nil
}

// probeWritable fails fast if the store root is not actually writable
// (e.g. a read-only bind mount), rather than deferring that discovery to
// the first Put.
func (s *Store) probeWritable() error {
	f, err := os.CreateTemp(s.root, ".helios-writable-*")
	if err != nil {
		return fmt.Errorf("localfs: root %q is not writable: %w", s.root, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// resolve validates relPath and turns it into an absolute filesystem path
// guaranteed to be inside s.root. It never returns a path outside s.root,
// and it resolves symlinks along the way so a symlink placed inside the
// store that points outside it is rejected rather than followed.
//
// relPath is treated as hostile input: every rejection below exists
// because it is reachable from caller-supplied data (an uploaded filename,
// a job ID echoed back into a path).
func (s *Store) resolve(relPath string) (absPath string, err error) {
	if relPath == "" {
		return "", fmt.Errorf("localfs: path is empty")
	}
	if strings.IndexByte(relPath, 0) >= 0 {
		return "", fmt.Errorf("localfs: path contains a NUL byte")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("localfs: path %q must be relative", relPath)
	}

	segments := strings.Split(relPath, "/")
	if len(segments) > maxPathDepth {
		return "", fmt.Errorf("localfs: path %q has %d segments, over the limit of %d", relPath, len(segments), maxPathDepth)
	}
	for _, seg := range segments {
		switch seg {
		case "":
			return "", fmt.Errorf("localfs: path %q has an empty segment", relPath)
		case ".":
			return "", fmt.Errorf("localfs: path %q has a '.' segment", relPath)
		case "..":
			return "", fmt.Errorf("localfs: path %q attempts to escape the store root", relPath)
		}
		if len(seg) > maxComponentBytes {
			return "", fmt.Errorf("localfs: path %q has a segment longer than %d bytes", relPath, maxComponentBytes)
		}
	}

	canonicalRel := filepath.Clean(relPath)
	candidate := filepath.Join(s.root, canonicalRel)

	// Walk up from candidate to the longest existing ancestor so a symlink
	// anywhere on that ancestor's path is actually resolved, rather than
	// trusted at face value; components below it cannot yet be symlinks
	// because they do not exist.
	existing := candidate
	for existing != s.root {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}

	realExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return "", fmt.Errorf("localfs: path %q: a component is not a directory: %w", relPath, err)
		}
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENAMETOOLONG) {
			return "", fmt.Errorf("localfs: path %q: %w", relPath, err)
		}
		return "", fmt.Errorf("localfs: resolve symlinks for %q: %w", relPath, err)
	}
	if !withinRoot(s.root, realExisting) {
		return "", fmt.Errorf("localfs: path %q escapes the store root via a symlink", relPath)
	}

	rest, err := filepath.Rel(existing, candidate)
	if err != nil {
		return "", fmt.Errorf("localfs: relativize %q: %w", relPath, err)
	}
	final := filepath.Clean(filepath.Join(realExisting, rest))
	if !withinRoot(s.root, final) {
		return "", fmt.Errorf("localfs: path %q escapes the store root", relPath)
	}

	return final, nil
}

// withinRoot reports whether p is root itself or lies inside it.
func withinRoot(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// createTemp creates a new, exclusively-named regular file directly inside
// dir, using a random name independent of the eventual target name -
// relPath's base name can legitimately be close to maxComponentBytes, and a
// temp name that concatenates a fixed prefix plus a random suffix onto it
// can overflow the filesystem's own NAME_MAX even though both the target
// name and the temp scheme are individually within bounds. It returns the
// open file, which the caller must eventually remove or rename away.
func createTemp(dir string) (tmp *os.File, err error) {
	var lastErr error
	for i := 0; i < maxTempNameAttempts; i++ {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, fmt.Errorf("localfs: generate temp file name: %w", err)
		}
		candidate := filepath.Join(dir, ".helios-tmp-"+hex.EncodeToString(suffix[:]))

		f, openErr := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
		if openErr == nil {
			return f, nil
		}
		if !os.IsExist(openErr) {
			return nil, fmt.Errorf("localfs: create temp file %q: %w", candidate, openErr)
		}
		lastErr = openErr
	}
	return nil, fmt.Errorf("localfs: exhausted temp file name attempts in %q: %w", dir, lastErr)
}

// classifyWriteErr maps a failure that occurred while streaming a write:
// disk-full and context cancellation get their own sentinels so callers
// (and any retry policy above them) can tell them apart from an
// unclassified I/O failure.
func classifyWriteErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("localfs: write failed, disk full: %w: %w", ErrDiskFull, err)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("localfs: write canceled: %w", ctx.Err())
	}
	return fmt.Errorf("localfs: write failed: %w", err)
}

// ctxReader wraps an io.Reader so a canceled ctx aborts a Read in progress
// (checked once per call, not preemptively) instead of the copy running to
// completion regardless of cancellation.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Put streams r into relPath, creating any parent directories that do not
// yet exist. The write goes to a temp file in the same directory as the
// final target and is renamed into place only once it is complete and
// fsynced, so a concurrent reader (or a crash mid-write) never observes a
// partial file at relPath. It returns the number of bytes written.
func (s *Store) Put(ctx context.Context, relPath string, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("localfs: context already canceled: %w", err)
	}
	target, err := s.resolve(relPath)
	if err != nil {
		return 0, err
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return 0, fmt.Errorf("localfs: create parent dir %q: %w", dir, err)
	}

	tmp, err := createTemp(dir)
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	n, copyErr := io.Copy(tmp, ctxReader{ctx: ctx, r: r})
	if copyErr != nil {
		return 0, classifyWriteErr(ctx, copyErr)
	}
	if err := tmp.Sync(); err != nil {
		return 0, classifyWriteErr(ctx, err)
	}
	if err := tmp.Close(); err != nil {
		return 0, classifyWriteErr(ctx, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return 0, fmt.Errorf("localfs: rename %q to %q: %w", tmpName, target, err)
	}
	succeeded = true

	if dirHandle, openErr := os.Open(dir); openErr == nil {
		// Best-effort: not every filesystem supports fsync on a directory
		// (notably some overlay/tmpfs setups). A failure here does not
		// undo an already-durable rename, so it is not reported as Put's
		// error.
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}

	return n, nil
}

// Open returns a seekable, closeable reader over the file at relPath and
// its size.
func (s *Store) Open(ctx context.Context, relPath string) (io.ReadSeekCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("localfs: context already canceled: %w", err)
	}
	target, err := s.resolve(relPath)
	if err != nil {
		return nil, 0, err
	}

	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, 0, fmt.Errorf("localfs: %q not found: %w: %w", relPath, ErrNotExist, err)
		}
		return nil, 0, fmt.Errorf("localfs: open %q: %w", relPath, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("localfs: stat %q: %w", relPath, err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("localfs: %q is a directory: %w", relPath, ErrNotExist)
	}

	return f, info.Size(), nil
}

// AbsPath resolves relPath to an absolute filesystem path, for handing to a
// subprocess (ffmpeg, whisper-cli) that needs a real path rather than a
// stream. It does not require relPath to already exist, so it can also be
// used for a path a subprocess is about to create (e.g. a normalized WAV
// output).
func (s *Store) AbsPath(relPath string) (string, error) {
	return s.resolve(relPath)
}

// Remove deletes the file at relPath. Removing a path that does not exist
// is not an error: Remove's contract is "this path is gone", which is
// already true.
func (s *Store) Remove(ctx context.Context, relPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("localfs: context already canceled: %w", err)
	}
	target, err := s.resolve(relPath)
	if err != nil {
		return err
	}

	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		return fmt.Errorf("localfs: stat %q: %w", relPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("localfs: %q is a directory, use RemoveDir", relPath)
	}

	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("localfs: remove %q: %w", relPath, err)
	}
	return nil
}

// RemoveDir removes relDir and everything under it. It refuses to remove
// the store root itself, so a caller passing "" or "." cannot wipe the
// entire store.
func (s *Store) RemoveDir(ctx context.Context, relDir string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("localfs: context already canceled: %w", err)
	}
	target, err := s.resolve(relDir)
	if err != nil {
		return err
	}
	if target == s.root {
		return fmt.Errorf("localfs: refusing to delete the store root (relDir %q)", relDir)
	}

	if err := os.RemoveAll(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("localfs: remove dir %q: %w", relDir, err)
	}
	return nil
}

// FreeBytes returns the number of bytes currently free on the store's
// volume, via statfs(2).
func (s *Store) FreeBytes() (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.root, &st); err != nil {
		return 0, fmt.Errorf("localfs: statfs %q: %w", s.root, err)
	}
	// Bavail and Bsize are both unsigned on linux/amd64 but signed on some
	// other unix targets; the explicit conversions keep this correct
	// either way.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
