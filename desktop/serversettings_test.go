package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
)

// withConfigHome points os.UserConfigDir() at a fresh t.TempDir() for the
// duration of a test, exactly the way mode_test.go's cases point
// os.UserHomeDir() at one via $HOME -- so a real ~/.config/helios/
// desktop.json on the machine running this test can never leak in, and
// every case starts from "nothing saved yet".
func withConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// newTestServerManager builds a serverManager directly, bypassing
// newServerManager's env-var/desktop.json precedence entirely, for tests
// that only care about serverManager.set/current or App's own behaviour.
// Using this instead of relying on HELIOS_API_URL/defaultAPIURL also means
// these tests never depend on anything actually listening (or not) on the
// real machine's localhost:7462.
func newTestServerManager(host string, port int) *serverManager {
	u := buildServerURL(host, port)
	return &serverManager{client: newRemoteClient(u), host: host, port: port, url: u, scheme: "http"}
}

// unusedTCPAddr returns a 127.0.0.1 host/port pair nothing is listening
// on: it opens a TCP listener just to learn a free port from the OS, then
// closes it immediately.
func unusedTCPAddr(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup: listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	if err := l.Close(); err != nil {
		t.Fatalf("setup: close listener: %v", err)
	}
	return "127.0.0.1", addr.Port
}

func TestServerConfigPath(t *testing.T) {
	dir := withConfigHome(t)
	got, err := serverConfigPath()
	if err != nil {
		t.Fatalf("serverConfigPath() error: %v", err)
	}
	want := filepath.Join(dir, "helios", "ats-desktop.json")
	if got != want {
		t.Fatalf("serverConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadServerSettingsFile_MissingIsNotError(t *testing.T) {
	withConfigHome(t)
	_, ok, err := loadServerSettingsFile()
	if err != nil {
		t.Fatalf("loadServerSettingsFile() error = %v, want nil for a missing file", err)
	}
	if ok {
		t.Fatalf("loadServerSettingsFile() ok = true, want false for a missing file")
	}
}

func TestLoadServerSettingsFile_MalformedIsError(t *testing.T) {
	dir := withConfigHome(t)
	path := filepath.Join(dir, "helios", "ats-desktop.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("setup: write: %v", err)
	}

	_, _, err := loadServerSettingsFile()
	if err == nil {
		t.Fatalf("loadServerSettingsFile() error = nil, want an error for a malformed file")
	}
}

func TestSaveThenLoadServerSettingsFile_Roundtrip(t *testing.T) {
	withConfigHome(t)
	want := serverSettingsFile{Host: "example.com", Port: 9090}

	if err := saveServerSettingsFile(want); err != nil {
		t.Fatalf("saveServerSettingsFile() error: %v", err)
	}

	got, ok, err := loadServerSettingsFile()
	if err != nil {
		t.Fatalf("loadServerSettingsFile() error: %v", err)
	}
	if !ok {
		t.Fatalf("loadServerSettingsFile() ok = false after a save")
	}
	if got != want {
		t.Fatalf("loadServerSettingsFile() = %+v, want %+v", got, want)
	}
}

// TestSaveServerSettingsFile_Atomic proves saveServerSettingsFile leaves no
// temp file behind, on either the first save (no prior desktop.json) or a
// second one that overwrites it.
func TestSaveServerSettingsFile_Atomic(t *testing.T) {
	dir := withConfigHome(t)

	if err := saveServerSettingsFile(serverSettingsFile{Host: "a.example", Port: 1111}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := saveServerSettingsFile(serverSettingsFile{Host: "b.example", Port: 2222}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "helios"))
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("config dir has %d entries, want exactly 1 (desktop.json, no leftover temp file): %v", len(entries), entries)
	}
	if entries[0].Name() != serverConfigFileName {
		t.Fatalf("config dir's one entry is %q, want %q", entries[0].Name(), serverConfigFileName)
	}
}

func TestCleanServerHost(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		want       string
		wantScheme string
		wantErr    bool
	}{
		{name: "plain hostname", host: "example.com", want: "example.com"},
		{name: "surrounding whitespace trimmed", host: "  example.com  ", want: "example.com"},
		{name: "http prefix returns \"http\" as the typed scheme", host: "http://example.com", want: "example.com", wantScheme: "http"},
		{name: "https prefix returns \"https\" as the typed scheme", host: "https://example.com", want: "example.com", wantScheme: "https"},
		{name: "IPv4 literal", host: "127.0.0.1", want: "127.0.0.1"},
		{name: "empty", host: "", wantErr: true},
		{name: "whitespace only", host: "   ", wantErr: true},
		{name: "internal whitespace", host: "exa mple.com", wantErr: true},
		{name: "path included", host: "example.com/api", wantErr: true},
		{name: "query included", host: "example.com?x=1", wantErr: true},
		{name: "port included", host: "example.com:8080", wantErr: true},
		{name: "other scheme", host: "ftp://example.com", wantErr: true},
		{name: "userinfo separator", host: "user@example.com", wantErr: true},
		{name: "percent", host: "example.com%00", wantErr: true},
		{name: "backslash", host: `example.com\evil`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotScheme, err := cleanServerHost(tt.host)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("cleanServerHost(%q) = %q, %q, nil, want an error", tt.host, got, gotScheme)
				}
				if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
					t.Fatalf("cleanServerHost(%q) error code = %q, want %q", tt.host, domain.CodeOf(err), domain.ErrCodeInvalidInput)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanServerHost(%q) unexpected error: %v", tt.host, err)
			}
			if got != tt.want {
				t.Fatalf("cleanServerHost(%q) host = %q, want %q", tt.host, got, tt.want)
			}
			if gotScheme != tt.wantScheme {
				t.Fatalf("cleanServerHost(%q) scheme = %q, want %q", tt.host, gotScheme, tt.wantScheme)
			}
		})
	}
}

func TestValidateServerPort(t *testing.T) {
	tests := []struct {
		port    int
		wantErr bool
	}{
		{port: 0, wantErr: true},
		{port: -1, wantErr: true},
		{port: 1, wantErr: false},
		{port: 8080, wantErr: false},
		{port: 65535, wantErr: false},
		{port: 65536, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.port), func(t *testing.T) {
			err := validateServerPort(tt.port)
			if tt.wantErr && err == nil {
				t.Fatalf("validateServerPort(%d) = nil, want an error", tt.port)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateServerPort(%d) unexpected error: %v", tt.port, err)
			}
			if tt.wantErr && err != nil && domain.CodeOf(err) != domain.ErrCodeInvalidInput {
				t.Fatalf("validateServerPort(%d) error code = %q, want %q", tt.port, domain.CodeOf(err), domain.ErrCodeInvalidInput)
			}
		})
	}
}

func TestBuildServerURL(t *testing.T) {
	got := buildServerURL("example.com", 8080)
	want := "http://example.com:8080"
	if got != want {
		t.Fatalf("buildServerURL() = %q, want %q", got, want)
	}
}

func TestBuildServerURLWithScheme(t *testing.T) {
	got := buildServerURLWithScheme("https", "example.com", 8443, "/gateway")
	want := "https://example.com:8443/gateway"
	if got != want {
		t.Fatalf("buildServerURLWithScheme() = %q, want %q", got, want)
	}
}

func TestNormalizeFileScheme(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "http"},
		{in: "http", want: "http"},
		{in: "https", want: "https"},
		{in: "ftp", wantErr: true},
		{in: "HTTPS", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizeFileScheme(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeFileScheme(%q) = %q, nil, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeFileScheme(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeFileScheme(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateSavedPath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{path: "", wantErr: false},
		{path: "/gateway", wantErr: false},
		{path: "/a/b/c", wantErr: false},
		{path: "gateway", wantErr: true},
		{path: "/a?x=1", wantErr: true},
		{path: "/a#frag", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateSavedPath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateSavedPath(%q) = nil, want an error", tt.path)
				}
				if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
					t.Fatalf("validateSavedPath(%q) error code = %q, want %q", tt.path, domain.CodeOf(err), domain.ErrCodeInvalidInput)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSavedPath(%q) unexpected error: %v", tt.path, err)
			}
		})
	}
}

func TestSchemeForFile(t *testing.T) {
	if got := schemeForFile("http"); got != "" {
		t.Fatalf(`schemeForFile("http") = %q, want ""`, got)
	}
	if got := schemeForFile("https"); got != "https" {
		t.Fatalf(`schemeForFile("https") = %q, want "https"`, got)
	}
}

func TestAssembleCandidateURL(t *testing.T) {
	t.Run("untyped scheme falls back to fallbackScheme/fallbackPath", func(t *testing.T) {
		host, scheme, url, err := assembleCandidateURL("example.com", 8443, "https", "/gateway")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if host != "example.com" || scheme != "https" || url != "https://example.com:8443/gateway" {
			t.Fatalf("= (%q, %q, %q), want (\"example.com\", \"https\", \"https://example.com:8443/gateway\")", host, scheme, url)
		}
	})

	t.Run("a typed scheme prefix overrides fallbackScheme, not fallbackPath", func(t *testing.T) {
		host, scheme, url, err := assembleCandidateURL("http://example.com", 8080, "https", "/gateway")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if host != "example.com" || scheme != "http" || url != "http://example.com:8080/gateway" {
			t.Fatalf("= (%q, %q, %q), want (\"example.com\", \"http\", \"http://example.com:8080/gateway\")", host, scheme, url)
		}
	})

	t.Run("empty fallbackScheme defaults to http", func(t *testing.T) {
		_, scheme, url, err := assembleCandidateURL("example.com", 8080, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scheme != "http" || url != "http://example.com:8080" {
			t.Fatalf("= (%q, %q), want (\"http\", \"http://example.com:8080\")", scheme, url)
		}
	})

	t.Run("invalid host is rejected before anything else", func(t *testing.T) {
		if _, _, _, err := assembleCandidateURL("", 8080, "http", ""); err == nil {
			t.Fatalf("error = nil, want a validation error for an empty host")
		}
	})

	t.Run("invalid port is rejected", func(t *testing.T) {
		if _, _, _, err := assembleCandidateURL("example.com", 0, "http", ""); err == nil {
			t.Fatalf("error = nil, want a validation error for an invalid port")
		}
	})
}

func TestSplitServerURL(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		wantHost   string
		wantPort   int
		wantScheme string
		wantPath   string
		wantOK     bool
	}{
		{name: "http with explicit port", rawURL: "http://localhost:8080", wantHost: "localhost", wantPort: 8080, wantScheme: "http", wantOK: true},
		{name: "https with no explicit port defaults to 443", rawURL: "https://example.com", wantHost: "example.com", wantPort: 443, wantScheme: "https", wantOK: true},
		{name: "http with no explicit port defaults to 80", rawURL: "http://example.com", wantHost: "example.com", wantPort: 80, wantScheme: "http", wantOK: true},
		{name: "path is preserved, not ignored", rawURL: "http://example.com:9090/api/v1", wantHost: "example.com", wantPort: 9090, wantScheme: "http", wantPath: "/api/v1", wantOK: true},
		{name: "https with a path and no explicit port", rawURL: "https://example.com/gateway", wantHost: "example.com", wantPort: 443, wantScheme: "https", wantPath: "/gateway", wantOK: true},
		{name: "not a URL at all", rawURL: "\x7f not a url", wantOK: false},
		{name: "no hostname", rawURL: "not-a-url-with-no-scheme-or-host", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, scheme, path, ok := splitServerURL(tt.rawURL)
			if ok != tt.wantOK {
				t.Fatalf("splitServerURL(%q) ok = %v, want %v (host=%q, port=%d)", tt.rawURL, ok, tt.wantOK, host, port)
			}
			if !tt.wantOK {
				if host != "" || port != 0 || scheme != "" || path != "" {
					t.Fatalf("splitServerURL(%q) = (%q, %d, %q, %q), want all zeroed when ok is false", tt.rawURL, host, port, scheme, path)
				}
				return
			}
			if host != tt.wantHost || port != tt.wantPort || scheme != tt.wantScheme || path != tt.wantPath {
				t.Fatalf("splitServerURL(%q) = (%q, %d, %q, %q), want (%q, %d, %q, %q)",
					tt.rawURL, host, port, scheme, path, tt.wantHost, tt.wantPort, tt.wantScheme, tt.wantPath)
			}
		})
	}
}

// readyzServer returns an httptest.Server answering GET /readyz with ok,
// and that server's host and port as newServerManager/serverManager.set
// would accept them.
func readyzServer(t *testing.T, ok bool) (srv *httptest.Server, host string, port int) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if ok {
			_, _ = w.Write([]byte(`{"ok":true,"checks":{}}`))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":false,"checks":{"database":{"ok":false,"detail":"down"}}}`))
		}
	}))
	host, port, err := hostPortOf(t, srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("parse httptest server URL %q: %v", srv.URL, err)
	}
	return srv, host, port
}

// hostPortOf wraps splitServerURL with an error return for test call
// sites, rather than the ok-bool splitServerURL itself uses.
func hostPortOf(t *testing.T, rawURL string) (string, int, error) {
	t.Helper()
	host, port, _, _, ok := splitServerURL(rawURL)
	if !ok {
		return "", 0, errors.New("could not parse URL")
	}
	return host, port, nil
}

// TestNewServerManager_Precedence covers "saved setting > HELIOS_API_URL
// env > default", plus the rule that a malformed/invalid desktop.json must
// never be fatal: newServerManager falls back and warns, rather than
// failing this process's startup.
func TestNewServerManager_Precedence(t *testing.T) {
	t.Run("nothing set: falls back to defaultAPIURL", func(t *testing.T) {
		withConfigHome(t)
		os.Unsetenv("HELIOS_API_URL")

		m := newServerManager()
		host, port, scheme, url, warning := m.current()
		if host != "localhost" || port != 7462 {
			t.Fatalf("current() = (%q, %d), want (\"localhost\", 7462) from defaultAPIURL", host, port)
		}
		if scheme != "http" {
			t.Fatalf("current() scheme = %q, want %q", scheme, "http")
		}
		if url != "http://localhost:7462" {
			t.Fatalf("current() url = %q, want %q", url, "http://localhost:7462")
		}
		if warning != "" {
			t.Fatalf("current() warning = %q, want empty", warning)
		}
	})

	t.Run("HELIOS_API_URL set, no saved file: env wins over default", func(t *testing.T) {
		withConfigHome(t)
		t.Setenv("HELIOS_API_URL", "http://other-host:9191")

		m := newServerManager()
		host, port, _, _, _ := m.current()
		if host != "other-host" || port != 9191 {
			t.Fatalf("current() = (%q, %d), want (\"other-host\", 9191) from HELIOS_API_URL", host, port)
		}
	})

	t.Run("saved file present: overrides HELIOS_API_URL", func(t *testing.T) {
		withConfigHome(t)
		t.Setenv("HELIOS_API_URL", "http://ignored-by-saved-file:1234")
		if err := saveServerSettingsFile(serverSettingsFile{Host: "saved-host", Port: 4321}); err != nil {
			t.Fatalf("setup: save: %v", err)
		}

		m := newServerManager()
		host, port, _, _, warning := m.current()
		if host != "saved-host" || port != 4321 {
			t.Fatalf("current() = (%q, %d), want (\"saved-host\", 4321) from the saved file", host, port)
		}
		if warning != "" {
			t.Fatalf("current() warning = %q, want empty for a valid saved file", warning)
		}
	})

	t.Run("saved file with an invalid port falls back with a warning, not a fatal error", func(t *testing.T) {
		withConfigHome(t)
		os.Unsetenv("HELIOS_API_URL")
		if err := saveServerSettingsFile(serverSettingsFile{Host: "example.com", Port: 0}); err != nil {
			t.Fatalf("setup: save: %v", err)
		}

		m := newServerManager()
		host, port, _, _, warning := m.current()
		if host != "localhost" || port != 7462 {
			t.Fatalf("current() = (%q, %d), want the defaultAPIURL fallback (\"localhost\", 7462)", host, port)
		}
		if warning != invalidSavedSettingsWarning {
			t.Fatalf("current() warning = %q, want %q", warning, invalidSavedSettingsWarning)
		}
	})

	t.Run("saved file with an invalid scheme falls back with a warning, not a fatal error", func(t *testing.T) {
		withConfigHome(t)
		os.Unsetenv("HELIOS_API_URL")
		if err := saveServerSettingsFile(serverSettingsFile{Host: "example.com", Port: 8080, Scheme: "ftp"}); err != nil {
			t.Fatalf("setup: save: %v", err)
		}

		m := newServerManager()
		host, port, _, _, warning := m.current()
		if host != "localhost" || port != 7462 {
			t.Fatalf("current() = (%q, %d), want the defaultAPIURL fallback (\"localhost\", 7462)", host, port)
		}
		if warning != invalidSavedSettingsWarning {
			t.Fatalf("current() warning = %q, want %q", warning, invalidSavedSettingsWarning)
		}
	})

	t.Run("saved file with an invalid path falls back with a warning, and the fallback is actually saveable", func(t *testing.T) {
		withConfigHome(t)
		os.Unsetenv("HELIOS_API_URL")
		if err := saveServerSettingsFile(serverSettingsFile{Host: "example.com", Port: 8080, Path: "gateway"}); err != nil {
			t.Fatalf("setup: save: %v", err)
		}

		m := newServerManager()
		host, port, _, _, warning := m.current()
		if host != "localhost" || port != 7462 {
			t.Fatalf("current() = (%q, %d), want the defaultAPIURL fallback (\"localhost\", 7462)", host, port)
		}
		if warning != invalidSavedSettingsWarning {
			t.Fatalf("current() warning = %q, want %q", warning, invalidSavedSettingsWarning)
		}
		if _, _, _, _, err := m.set("good-host.example", 9090); err != nil {
			t.Fatalf("set() after an invalid-path fallback: unexpected error: %v", err)
		}
	})

	t.Run("malformed desktop.json falls back with a warning, not a fatal error", func(t *testing.T) {
		dir := withConfigHome(t)
		os.Unsetenv("HELIOS_API_URL")
		path := filepath.Join(dir, "helios", "ats-desktop.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("setup: mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatalf("setup: write: %v", err)
		}

		m := newServerManager()
		host, port, _, _, warning := m.current()
		if host != "localhost" || port != 7462 {
			t.Fatalf("current() = (%q, %d), want the defaultAPIURL fallback (\"localhost\", 7462)", host, port)
		}
		if warning != invalidSavedSettingsWarning {
			t.Fatalf("current() warning = %q, want %q", warning, invalidSavedSettingsWarning)
		}
	})

	t.Run("a successful save clears the warning; a rejected one does not", func(t *testing.T) {
		withConfigHome(t)
		if err := saveServerSettingsFile(serverSettingsFile{Host: "example.com", Port: 0}); err != nil {
			t.Fatalf("setup: save: %v", err)
		}
		m := newServerManager()
		if _, _, _, _, warning := m.current(); warning == "" {
			t.Fatalf("setup sanity check: expected a warning before saving again")
		}

		if _, _, _, _, err := m.set("", 9090); err == nil {
			t.Fatalf("set(\"\", 9090) error = nil, want a validation error")
		}
		if _, _, _, _, warning := m.current(); warning == "" {
			t.Fatalf("current() warning = \"\" after a REJECTED save, want it to still be set")
		}

		if _, _, _, _, err := m.set("good-host.example", 9090); err != nil {
			t.Fatalf("set() error: %v", err)
		}
		if _, _, _, _, warning := m.current(); warning != "" {
			t.Fatalf("current() warning = %q after a successful save, want empty", warning)
		}
	})
}

func TestServerManager_Set_PersistsAndSwapsLive(t *testing.T) {
	withConfigHome(t)
	m := newTestServerManager("old-host.example", 1111)

	host, port, scheme, url, err := m.set("new-host.example", 2222)
	if err != nil {
		t.Fatalf("set() error: %v", err)
	}
	if host != "new-host.example" || port != 2222 || scheme != "http" || url != "http://new-host.example:2222" {
		t.Fatalf("set() = (%q, %d, %q, %q), want new-host.example/2222/http/http://new-host.example:2222", host, port, scheme, url)
	}

	saved, ok, err := loadServerSettingsFile()
	if err != nil || !ok {
		t.Fatalf("loadServerSettingsFile() = %+v, %v, %v, want a persisted file", saved, ok, err)
	}
	if saved.Host != "new-host.example" || saved.Port != 2222 {
		t.Fatalf("persisted file = %+v, want Host=new-host.example Port=2222", saved)
	}
}

func TestServerManager_Set_RejectsInvalidInputWithoutSideEffects(t *testing.T) {
	dir := withConfigHome(t)
	m := newTestServerManager("old-host.example", 1111)

	if _, _, _, _, err := m.set("", 2222); err == nil {
		t.Fatalf("set(\"\", 2222) error = nil, want a validation error")
	}

	if _, err := os.Stat(filepath.Join(dir, "helios", "ats-desktop.json")); !os.IsNotExist(err) {
		t.Fatalf("a rejected set() must not have written desktop.json at all, stat error = %v", err)
	}
	host, port, _, _, _ := m.current()
	if host != "old-host.example" || port != 1111 {
		t.Fatalf("current() after a rejected set() = (%q, %d), want the unchanged original", host, port)
	}
}

func TestApp_GetSetServerSettings(t *testing.T) {
	withConfigHome(t)

	m := newTestServerManager("initial-host.example", 1234)
	a := &App{api: m.client, server: m}

	got, err := a.GetServerSettings()
	if err != nil {
		t.Fatalf("GetServerSettings() error: %v", err)
	}
	if !got.Editable {
		t.Fatalf("GetServerSettings().Editable = false, want true")
	}
	if got.Host != "initial-host.example" || got.Port != 1234 {
		t.Fatalf("GetServerSettings() = (%q, %d), want (\"initial-host.example\", 1234)", got.Host, got.Port)
	}
	if got.Warning != "" {
		t.Fatalf("GetServerSettings().Warning = %q, want empty", got.Warning)
	}

	if _, err := a.SetServerSettings("", 8080); err == nil {
		t.Fatalf("SetServerSettings(\"\", 8080) error = nil, want a validation error")
	}
	if _, err := a.SetServerSettings("example.com", 0); domain.CodeOf(err) != domain.ErrCodeInvalidInput {
		t.Fatalf("SetServerSettings(\"example.com\", 0) error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
	}

	updated, err := a.SetServerSettings("example.com", 9999)
	if err != nil {
		t.Fatalf("SetServerSettings(\"example.com\", 9999) error: %v", err)
	}
	if updated.Host != "example.com" || updated.Port != 9999 || !updated.Editable {
		t.Fatalf("SetServerSettings() = %+v, want {Host:example.com Port:9999 Editable:true ...}", updated)
	}
	if updated.BaseURL != "http://example.com:9999" {
		t.Fatalf("SetServerSettings().BaseURL = %q, want %q", updated.BaseURL, "http://example.com:9999")
	}

	got2, err := a.GetServerSettings()
	if err != nil {
		t.Fatalf("GetServerSettings() error: %v", err)
	}
	if got2.Host != "example.com" || got2.Port != 9999 {
		t.Fatalf("GetServerSettings() after SetServerSettings = (%q, %d), want (\"example.com\", 9999)", got2.Host, got2.Port)
	}
}

// TestApp_GetSetServerSettings_NilServer proves App is defensive against a
// nil a.server (never happens in production — newAdapter always builds
// one — but a bound method must still fail cleanly rather than panic).
func TestApp_GetSetServerSettings_NilServer(t *testing.T) {
	a := &App{api: &fakeAPI{}, ctx: context.Background()}

	got, err := a.GetServerSettings()
	if err != nil {
		t.Fatalf("GetServerSettings() error: %v", err)
	}
	if got.Editable {
		t.Fatalf("GetServerSettings().Editable = true, want false when a.server is nil")
	}

	_, err = a.SetServerSettings("example.com", 8080)
	if domain.CodeOf(err) != domain.ErrCodeConflict {
		t.Fatalf("SetServerSettings() error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeConflict)
	}
}

// TestApp_TestServerConnection covers all three of TestServerConnection's
// outcomes: a reachable server, an unreachable one (no error — a false
// HealthReportDTO, exactly like Health's own contract), and invalid input
// (an error, before any request is even attempted).
func TestApp_TestServerConnection(t *testing.T) {
	a := &App{api: &fakeAPI{}, ctx: context.Background()}

	t.Run("reachable", func(t *testing.T) {
		srv, host, port := readyzServer(t, true)
		defer srv.Close()

		report, err := a.TestServerConnection(host, port)
		if err != nil {
			t.Fatalf("TestServerConnection() error: %v", err)
		}
		if !report.OK {
			t.Fatalf("TestServerConnection().OK = false, want true against a reachable server")
		}
	})

	t.Run("unreachable is a false report, not an error", func(t *testing.T) {
		srv, host, port := readyzServer(t, true)
		srv.Close()

		report, err := a.TestServerConnection(host, port)
		if err != nil {
			t.Fatalf("TestServerConnection() unexpected error for an unreachable server: %v", err)
		}
		if report.OK {
			t.Fatalf("TestServerConnection().OK = true, want false against a closed server")
		}
	})

	t.Run("invalid host is rejected before any request", func(t *testing.T) {
		_, err := a.TestServerConnection("", 8080)
		if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
			t.Fatalf("TestServerConnection(\"\", 8080) error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
		}
	})

	t.Run("invalid port is rejected before any request", func(t *testing.T) {
		_, err := a.TestServerConnection("example.com", 0)
		if domain.CodeOf(err) != domain.ErrCodeInvalidInput {
			t.Fatalf("TestServerConnection(\"example.com\", 0) error code = %q, want %q", domain.CodeOf(err), domain.ErrCodeInvalidInput)
		}
	})
}

// TestApp_TestServerConnection_UsesManagerSchemeAndPath proves
// TestServerConnection builds its candidate URL exactly the way a real
// set(host, port) call against a.server would.
func TestApp_TestServerConnection_UsesManagerSchemeAndPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"checks":{}}`))
	}))
	defer srv.Close()
	host, port, err := hostPortOf(t, srv.URL)
	if err != nil {
		t.Fatalf("parse httptest server URL: %v", err)
	}

	m := &serverManager{
		client:     newRemoteClient(buildServerURLWithScheme("http", host, port, "/gateway")),
		host:       host,
		port:       port,
		scheme:     "http",
		pathPrefix: "/gateway",
	}
	a := &App{api: &fakeAPI{}, server: m, ctx: context.Background()}

	report, err := a.TestServerConnection(host, port)
	if err != nil {
		t.Fatalf("TestServerConnection() error: %v", err)
	}
	if !report.OK {
		t.Fatalf("TestServerConnection().OK = false, want true — the manager's own path prefix (%q) should have been used", "/gateway")
	}
}

// TestApp_TestServerConnection_TypedSchemeOverridesManagerScheme proves a
// typed "http://"/"https://" prefix on the Host argument still wins over
// a.server's own current scheme.
func TestApp_TestServerConnection_TypedSchemeOverridesManagerScheme(t *testing.T) {
	srv, host, port := readyzServer(t, true)
	defer srv.Close()

	m := &serverManager{
		client: newRemoteClient(buildServerURL(host, port)),
		host:   host,
		port:   port,
		scheme: "https", // deliberately wrong for srv, which is plain http
	}
	a := &App{api: &fakeAPI{}, server: m, ctx: context.Background()}

	report, err := a.TestServerConnection("http://"+host, port)
	if err != nil {
		t.Fatalf("TestServerConnection() error: %v", err)
	}
	if !report.OK {
		t.Fatalf("TestServerConnection().OK = false, want true — the typed \"http://\" prefix should have overridden the manager's \"https\"")
	}
}

// blackholeListener returns the host/port of a TCP listener that accepts
// every connection but never writes a single byte back.
func blackholeListener(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup: listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// TestApp_TestServerConnection_TimesOutQuicklyWithFriendlyMessage proves
// TestServerConnection gives up after testConnectionTimeout specifically
// (shrunk here so the test itself does not wait out 5 real seconds), well
// under 30s regardless, and the "connection" check's Detail reads as an
// explanation a user can act on rather than the raw transport error text.
func TestApp_TestServerConnection_TimesOutQuicklyWithFriendlyMessage(t *testing.T) {
	original := testConnectionTimeout
	testConnectionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { testConnectionTimeout = original })

	host, port := blackholeListener(t)
	a := &App{api: &fakeAPI{}, ctx: context.Background()}

	start := time.Now()
	report, err := a.TestServerConnection(host, port)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("TestServerConnection() unexpected error: %v", err)
	}
	if report.OK {
		t.Fatalf("TestServerConnection().OK = true, want false against a listener that never responds")
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("TestServerConnection() took %s, want well under 30s (roughly testConnectionTimeout=%s)", elapsed, testConnectionTimeout)
	}

	check, ok := report.Checks[connectionCheckName]
	if !ok {
		t.Fatalf("TestServerConnection().Checks has no %q entry: %+v", connectionCheckName, report.Checks)
	}
	wantHostPort := fmt.Sprintf("%s:%d", host, port)
	if !strings.Contains(check.Detail, wantHostPort) {
		t.Fatalf("connection check detail = %q, want it to name %q", check.Detail, wantHostPort)
	}
	if !strings.Contains(check.Detail, testConnectionTimeout.String()) {
		t.Fatalf("connection check detail = %q, want it to name the %s timeout actually used", check.Detail, testConnectionTimeout)
	}
	if strings.Contains(check.Detail, "context deadline exceeded") {
		t.Fatalf("connection check detail = %q, still contains the raw transport error text it should have replaced", check.Detail)
	}
}

// TestApp_TestServerConnection_RefusedConnectionKeepsRawDetail proves the
// friendly testConnectionTimeout message replaces the "connection" check's
// Detail only when that timeout specifically is what failed the request —
// a connection actively refused fails immediately, well before
// testConnectionTimeout can elapse, and must keep whatever detail Readyz
// itself produced.
func TestApp_TestServerConnection_RefusedConnectionKeepsRawDetail(t *testing.T) {
	host, port := unusedTCPAddr(t)
	a := &App{api: &fakeAPI{}, ctx: context.Background()}

	report, err := a.TestServerConnection(host, port)
	if err != nil {
		t.Fatalf("TestServerConnection() unexpected error: %v", err)
	}
	if report.OK {
		t.Fatalf("TestServerConnection().OK = true, want false against an address nothing listens on")
	}
	check, ok := report.Checks[connectionCheckName]
	if !ok {
		t.Fatalf("TestServerConnection().Checks has no %q entry: %+v", connectionCheckName, report.Checks)
	}
	if strings.Contains(check.Detail, "no response from") {
		t.Fatalf("connection check detail = %q, want the raw transport error kept — a refused connection never hit testConnectionTimeout", check.Detail)
	}
}

// TestServerSettingsFileIsNotSharedWithSiblingServices pins the one thing
// that actually went wrong in practice. This file was copied from
// HELIOS-SMIS's desktop app, which persists its own server at
// helios/desktop.json. Keeping that name meant this client read SMIS's
// host and port on any machine where both apps run -- an audio
// transcription client aimed at the social-media ingestion server. The
// name must stay service-specific, and "desktop.json" specifically must
// never come back.
func TestServerSettingsFileIsNotSharedWithSiblingServices(t *testing.T) {
	if serverConfigFileName == "desktop.json" {
		t.Fatal("desktop.json is HELIOS-SMIS's settings file; this service must not read or overwrite it")
	}
	if !strings.Contains(serverConfigFileName, "ats") {
		t.Fatalf("settings filename %q must name the service it belongs to", serverConfigFileName)
	}

	dir := withConfigHome(t)

	// A sibling's settings file sitting right next to ours must be ignored
	// entirely, not adopted as our server.
	siblingDir := filepath.Join(dir, serverConfigDirName)
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatalf("mkdir sibling config dir: %v", err)
	}
	sibling := filepath.Join(siblingDir, "desktop.json")
	const siblingBody = `{"host":"hel-smis.home.lan","port":443,"scheme":"https"}`
	if err := os.WriteFile(sibling, []byte(siblingBody), 0o644); err != nil {
		t.Fatalf("write sibling settings: %v", err)
	}

	mgr := newServerManager()
	host, _, _, _, _ := mgr.current()
	if host == "hel-smis.home.lan" {
		t.Fatal("a sibling service's settings file must never become this client's server")
	}

	// And ours must be written under its own name, leaving the sibling's
	// file byte-for-byte alone.
	if _, _, _, _, err := mgr.set("localhost", 7462); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(siblingDir, serverConfigFileName)); err != nil {
		t.Fatalf("our own settings file must be created: %v", err)
	}
	got, err := os.ReadFile(sibling)
	if err != nil {
		t.Fatalf("read sibling settings back: %v", err)
	}
	if string(got) != siblingBody {
		t.Fatalf("sibling settings file was modified:\n got: %s\nwant: %s", got, siblingBody)
	}
}
