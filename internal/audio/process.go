package audio

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// stderrTailCap bounds how much of a subprocess's stderr this package ever
// holds in memory — the contract calls for "~2 KiB" of tail in a failed
// domain.Error's Details, and ffmpeg/ffprobe can be chatty on a malformed
// input, so only the tail is kept.
const stderrTailCap = 2 * 1024

// maxProbeJSONBytes bounds how much of ffprobe's stdout this package will
// read before giving up. A real -show_format/-show_streams blob is a few
// KiB even for a file with many streams; this is a generous ceiling against
// a runaway or malicious process, not a tuned expectation.
const maxProbeJSONBytes = 8 * 1024 * 1024

// waitDelay is how long, after cmd.Cancel has been invoked, exec.Cmd waits
// for the process to actually exit before forcibly closing its I/O and
// letting Wait return anyway — guards against a process wedged even after
// SIGKILL to its whole group (e.g. blocked on a hung network filesystem).
const waitDelay = 10 * time.Second

// ringBuffer is an io.Writer that keeps only the most recently written cap
// bytes, so capturing a subprocess's stderr costs a bounded, small amount
// of memory regardless of how much it actually writes.
type ringBuffer struct {
	cap int
	buf []byte
}

func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{cap: cap}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	return string(r.buf)
}

// compile-time check that ringBuffer satisfies io.Writer, since cmd.Stderr
// is typed as io.Writer.
var _ io.Writer = (*ringBuffer)(nil)

// runResult is the outcome of one ffmpeg/ffprobe invocation.
type runResult struct {
	// ExitCode is the process's exit status, or -1 if it never started or
	// never reached a normal exit (e.g. it was killed).
	ExitCode int
	// Stderr is the tail of the process's stderr (see stderrTailCap).
	Stderr string
	// Err is nil on a clean, zero-exit-code success. Otherwise it is, in
	// priority order: the run's own context error (context.DeadlineExceeded
	// or context.Canceled) if non-nil, the error starting the process, or
	// the error from waiting on it — exactly the input classify expects.
	Err error
}

// command builds an *exec.Cmd for one subprocess invocation, wired so
// that:
//   - p.cfg.Timeout (derived here as a hard ceiling on top of the caller's
//     ctx, whichever elapses first) or cancellation stops it;
//   - cancellation kills the whole process group, not just the direct
//     child — ffmpeg in particular can spawn helper processes for some
//     filters/formats;
//   - stderr is captured into a bounded ring buffer.
//
// The caller must defer the returned cancel func.
func (p *Processor) command(ctx context.Context, bin string, args []string) (cmd *exec.Cmd, runCtx context.Context, cancel context.CancelFunc, stderr *ringBuffer) {
	runCtx, cancel = context.WithTimeout(ctx, p.cfg.Timeout)

	cmd = exec.CommandContext(runCtx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the whole process group created by
		// Setpgid above, not just the direct child.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	stderr = newRingBuffer(stderrTailCap)
	cmd.Stderr = stderr

	return cmd, runCtx, cancel, stderr
}

// settle turns a finished (or never-started) command into a runResult.
func settle(runCtx context.Context, cmd *exec.Cmd, startErr, waitErr error, stderr *ringBuffer) runResult {
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	var err error
	switch {
	case runCtx.Err() != nil:
		err = runCtx.Err()
	case startErr != nil:
		err = startErr
	case waitErr != nil:
		err = waitErr
	}

	return runResult{ExitCode: exitCode, Stderr: stderr.String(), Err: err}
}

// run runs one invocation to completion, discarding stdout — used by
// Normalize, whose ffmpeg output is a file, not stdout.
func (p *Processor) run(ctx context.Context, bin string, args []string) runResult {
	cmd, runCtx, cancel, stderr := p.command(ctx, bin, args)
	defer cancel()

	startErr := cmd.Start()
	var waitErr error
	if startErr == nil {
		waitErr = cmd.Wait()
	}
	return settle(runCtx, cmd, startErr, waitErr, stderr)
}

// runCaptureStdout runs one invocation and returns its full stdout (bounded
// by maxProbeJSONBytes) — used by Probe to read ffprobe's JSON report. The
// third return value reports whether stdout was truncated at that cap: the
// caller must treat that as its own failure (see Probe), not attempt to
// parse a truncated report.
//
// Truncation kills the child immediately via cancel rather than letting it
// run to a normal exit: io.ReadAll(io.LimitReader(...)) stops reading the
// instant it has maxProbeJSONBytes+1 bytes, but a child that keeps writing
// past that has nowhere for the rest to go — pipes are a bounded OS buffer
// (64 KiB on Linux), so its next write() blocks once that fills, and
// without draining or killing it cmd.Wait() below would then block right
// alongside it until Config.Timeout finally fires. That turns one
// malicious/broken invocation into a worker slot wedged for the full
// timeout instead of failing fast.
func (p *Processor) runCaptureStdout(ctx context.Context, bin string, args []string) ([]byte, bool, runResult) {
	cmd, runCtx, cancel, stderr := p.command(ctx, bin, args)
	defer cancel()

	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, false, settle(runCtx, cmd, pipeErr, nil, stderr)
	}

	startErr := cmd.Start()
	var out []byte
	truncated := false
	if startErr == nil {
		out, _ = io.ReadAll(io.LimitReader(stdoutPipe, maxProbeJSONBytes+1))
		if len(out) > maxProbeJSONBytes {
			truncated = true
			out = out[:maxProbeJSONBytes]
			cancel()
		}
	}
	waitErr := cmd.Wait()

	return out, truncated, settle(runCtx, cmd, startErr, waitErr, stderr)
}
