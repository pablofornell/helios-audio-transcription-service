// Package e2e proves that HELIOS-ATS's independently-built packages
// actually work together. It boots the real dependency graph --
// internal/postgres against a real Postgres (testcontainers),
// internal/storage/localfs over a temp directory, the real internal/audio
// processor against a real ffmpeg/ffprobe, and the deterministic
// internal/transcriber/fake transcriber (never a real whisper-cli
// subprocess, which needs model weights this suite has no business
// downloading) -- wired together by hand in harness_test.go rather than via
// cmd/helios-transcription-api/wire, and drives it entirely over HTTP:
// submit, poll, cancel, retry, delete, list, fetch a transcript, scrape
// /readyz and /metrics. See CONTRACT.md sections 1, 6 and 7 for the
// pipeline, API and schema this suite pins end to end.
//
// Two external dependencies gate what this package can prove: a reachable
// Docker daemon (for the Postgres testcontainer) and ffmpeg/ffprobe on
// PATH (for the real audio pipeline). Either being absent degrades this
// suite to a skip, never a failure -- see requireDocker and requireFFmpeg.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/pablofornell/helios-audio-transcription-service/internal/postgres"
)

// testDSN is the Postgres connection string every stack this package
// builds dials. One container is shared for the whole run (see TestMain):
// each top-level test truncates the shared table itself (see truncateAll)
// before building its own stack, rather than paying testcontainers' ~4s
// startup cost more than once.
var testDSN string

// dockerUnavailable and ffmpegUnavailable are set by runTests when the
// corresponding dependency could not be reached/found, so every test can
// skip itself via requireDocker/requireFFmpeg instead of dereferencing a
// nil pool or shelling out to a binary that does not exist.
var (
	dockerUnavailable bool
	ffmpegUnavailable bool
)

// fixtureDir holds every audio fixture generateFixtures writes, cleaned up
// when the whole test binary exits.
var fixtureDir string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests exists so the container's Terminate and the fixture directory's
// removal (both deferred) always run before the process exits -- os.Exit
// itself skips deferred calls. Docker or ffmpeg/ffprobe being unavailable
// degrades this suite to a skip (every individual test still runs, and
// immediately calls t.Skip via requireDocker/requireFFmpeg), matching
// internal/postgres's own store_test.go, rather than failing the whole
// package in an environment that genuinely has neither.
func runTests(m *testing.M) int {
	ctx := context.Background()

	if !dockerAvailable() {
		fmt.Fprintln(os.Stderr, "e2e: docker not available, skipping testcontainers-backed suite")
		dockerUnavailable = true
	} else {
		container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
			tcpostgres.WithDatabase("helios_e2e"),
			tcpostgres.WithUsername("helios"),
			tcpostgres.WithPassword("helios"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: start postgres test container:", err)
			return 1
		}
		defer func() {
			_ = container.Terminate(ctx)
		}()

		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: connection string:", err)
			return 1
		}
		testDSN = dsn

		pool, err := pgxPoolFor(ctx, dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: connect pool:", err)
			return 1
		}
		if err := postgres.Migrate(ctx, pool); err != nil {
			pool.Close()
			fmt.Fprintln(os.Stderr, "e2e: run migrations:", err)
			return 1
		}
		testPool = pool
		defer testPool.Close()
	}

	if !ffmpegAndFFprobeAvailable() {
		fmt.Fprintln(os.Stderr, "e2e: ffmpeg/ffprobe not on PATH, skipping audio-pipeline-backed tests")
		ffmpegUnavailable = true
	} else {
		dir, err := os.MkdirTemp("", "helios-e2e-fixtures-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: create fixture dir:", err)
			return 1
		}
		defer os.RemoveAll(dir)
		fixtureDir = dir

		if err := generateFixtures(ctx, dir); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: generate audio fixtures:", err)
			return 1
		}
	}

	return m.Run()
}

// dockerAvailable is a best-effort, fast check for a reachable Docker (or
// Docker-compatible) daemon, mirroring internal/postgres/store_test.go's
// own check.
func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

// ffmpegAndFFprobeAvailable reports whether both binaries this suite's
// real internal/audio.Processor needs are resolvable via PATH.
func ffmpegAndFFprobeAvailable() bool {
	_, ferr := exec.LookPath("ffmpeg")
	_, perr := exec.LookPath("ffprobe")
	return ferr == nil && perr == nil
}

// requireDocker skips t when no Postgres container could be started.
func requireDocker(t *testing.T) {
	t.Helper()
	if dockerUnavailable {
		t.Skip("docker is not available in this environment; skipping testcontainers-backed test")
	}
}

// requireFFmpeg skips t when ffmpeg/ffprobe are not on PATH -- every test
// in this package submits real audio through the real internal/audio
// pipeline, so this gate applies just as broadly as requireDocker.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if ffmpegUnavailable {
		t.Skip("ffmpeg/ffprobe are not on PATH; skipping audio-pipeline-backed test")
	}
}
