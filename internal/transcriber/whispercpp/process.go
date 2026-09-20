package whispercpp

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// stderrTailCap bounds how much of whisper-cli's stderr this package ever
// holds in memory — the contract calls for "the last ~2 KiB of stderr" in a
// failed domain.Error's Details.
const stderrTailCap = 2 * 1024

// waitDelay is how long, after cmd.Cancel has been invoked, exec.Cmd waits
// for the process to actually exit before forcibly closing its I/O and
// letting Wait return anyway — guards against a process wedged even after
// SIGKILL.
const waitDelay = 10 * time.Second

// ringBuffer is an io.Writer that keeps only the most recently written cap
// bytes, so capturing whisper-cli's stderr costs a bounded, small amount of
// memory regardless of how much it actually writes.
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

var _ io.Writer = (*ringBuffer)(nil)

// runResult is the outcome of one whisper-cli invocation.
type runResult struct {
	// ExitCode is the process's exit status, or -1 if it never started or
	// never reached a normal exit (e.g. it was killed).
	ExitCode int
	// Stderr is the tail of the process's stderr (see stderrTailCap).
	Stderr string
	// Err is nil on a clean, zero-exit-code success. Otherwise it is, in
	// priority order: the run's own context error (context.DeadlineExceeded
	// or context.Canceled) if non-nil, the error starting the process, or
	// the error from waiting on it.
	Err error
}

// run runs one whisper-cli invocation to completion. whisper-cli writes its
// result to <outBase>.json (see buildArgs), not stdout, so this never needs
// to capture stdout — only the exit status and stderr tail matter here.
//
// It kills the whole process group, not just the direct child, on
// cancellation or Config.Timeout, and captures a bounded stderr tail —
// mirroring BLUEPRINT ytdlp's process.go and internal/audio's process.go.
func (t *Transcriber) run(ctx context.Context, args []string) runResult {
	runCtx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, t.cfg.Binary, args...)
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

	stderr := newRingBuffer(stderrTailCap)
	cmd.Stderr = stderr

	startErr := cmd.Start()
	var waitErr error
	if startErr == nil {
		waitErr = cmd.Wait()
	}

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
