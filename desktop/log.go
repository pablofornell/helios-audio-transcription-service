package main

import (
	"context"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// logf routes a message to the Wails runtime logger via ctx, falling back
// to stderr when ctx is nil. The fallback matters here specifically:
// runtime.LogErrorf calls through to a helper that os.Exit(1)s the whole
// process if handed a ctx that was never populated by Wails' own runtime
// setup (see wailsapp/wails/v2/pkg/runtime.getLogger) — a logging call is
// not allowed to be the thing that takes this app down, so this package
// never calls runtime.LogErrorf with anything but the ctx OnStartup itself
// received.
func logf(ctx context.Context, format string, args ...any) {
	if ctx == nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	runtime.LogErrorf(ctx, format, args...)
}

// logWarnf is logf's Warning-level sibling, for something that is working
// as designed rather than failing — a saved server setting that could not
// be used (serversettings.go's newServerManager) is the one caller today —
// but that a user reporting unexpected behaviour needs to be able to find
// in the log, same nil-ctx/os.Exit(1) hazard and fallback as logf.
func logWarnf(ctx context.Context, format string, args ...any) {
	if ctx == nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	runtime.LogWarningf(ctx, format, args...)
}

// logInfof is logf's Info-level sibling, for something a user might need
// to find later even though nothing went wrong, same nil-ctx/os.Exit(1)
// hazard and fallback as logf.
func logInfof(ctx context.Context, format string, args ...any) {
	if ctx == nil {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	runtime.LogInfof(ctx, format, args...)
}
