package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pablofornell/helios-audio-transcription-service/internal/domain"
	"github.com/pablofornell/helios-audio-transcription-service/internal/httpclient"
	"github.com/pablofornell/helios-audio-transcription-service/internal/transcript"
)

// This file is the desktop app's "server" setting end to end: where it is
// persisted (serverConfigPath and the load/save functions below),
// validated (cleanServerHost, validateServerPort), applied live without a
// restart (remoteClient, serverManager), and the three App methods
// (GetServerSettings, SetServerSettings, TestServerConnection) that expose
// all of it to the frontend. Unlike every other App method (see app.go's
// doc), these do more than translate a call onto transcriptionAPI -- there
// is no such method for "which server", since that question only makes
// sense to this desktop process, never to a server itself -- so the logic
// lives here instead of behind that interface.
//
// Ported near-verbatim from the sibling ingestion service's desktop/
// serversettings.go, which this file's own comments still largely quote:
// the persistence format, validation rules and live-swap mechanics are all
// unchanged. The one thing dropped is that sibling's restartEventForwarder
// call after a successful SetServerSettings -- this service has no SSE
// event stream to move onto the new server (see app.go's own doc), so
// swapping remoteClient's live *httpclient.Client is the entire "apply
// live, no restart" story here.

// serverConfigDirName and serverConfigFileName make up the path this
// setting is persisted at: os.UserConfigDir()/helios/ats-desktop.json
// (e.g. ~/.config/helios/ats-desktop.json on Linux).
//
// The directory is shared ("helios", the organisation) but the FILE is
// per-service, and that split is load-bearing. HELIOS-SMIS's desktop app
// stores its own server at helios/desktop.json on this same machine; when
// this file was first copied from it, it kept that exact name and this
// client silently inherited SMIS's host and port -- pointing an audio
// transcription client at the social-media ingestion server. The visible
// symptom was a 404 on /api/v1/transcriptions. The invisible one is worse:
// two services' desktop clients cannot agree on one host:port, and a
// client that picks up the wrong one will happily upload a user's audio to
// a server that was never meant to receive it. Sibling services share the
// directory; they must never share the file.
const serverConfigDirName = "helios"
const serverConfigFileName = "ats-desktop.json"

// minServerPort and maxServerPort bound the Port field SetServerSettings
// and TestServerConnection accept -- the full range a TCP listener can
// actually bind to.
const (
	minServerPort = 1
	maxServerPort = 65535
)

// invalidSavedSettingsWarning is ServerSettingsDTO.Warning's exact text
// when desktop.json exists but could not be used (see newServerManager) --
// shown by the frontend's Server section so a user whose saved value was
// silently discarded still finds out, rather than just seeing the
// env/default value with no explanation for why their own save "didn't
// stick".
const invalidSavedSettingsWarning = "Saved server settings were invalid and ignored"

// serverSettingsFile is desktop.json's on-disk shape: enough to rebuild
// the exact base URL httpclient.Config.BaseURL wants (see
// buildServerURLWithScheme), not just the plain-http host/port pair this
// feature started out with. It is never the wire shape GetServerSettings/
// SetServerSettings hand the frontend (ServerSettingsDTO, below) -- that
// one also carries the derived URL, Editable and Warning, none of which
// belong in a file whose only job is remembering the target across
// launches.
//
// Scheme is "" or "http" (both mean plain http) or "https"; any other
// value fails validation on load (see newServerManager) exactly like an
// invalid Host or Port would. Path is the URL path prefix beyond
// "scheme://host:port", or "" for none.
type serverSettingsFile struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme,omitempty"`
	Path   string `json:"path,omitempty"`
}

// serverConfigPath returns the absolute path desktop.json is read from and
// written to.
func serverConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("desktop: find user config directory: %w", err)
	}
	return filepath.Join(dir, serverConfigDirName, serverConfigFileName), nil
}

// loadServerSettingsFile reads the persisted host/port. ok is false, with a
// nil error, exactly when nothing has been saved yet (no such file) --
// every later caller (newServerManager in particular) treats that as "fall
// back to HELIOS_API_URL/the default", not as a problem to report. A file
// that exists but fails to parse is reported as an error so the caller can
// decide how loudly to treat it -- newServerManager logs and falls back
// rather than failing startup over it.
func loadServerSettingsFile() (serverSettingsFile, bool, error) {
	path, err := serverConfigPath()
	if err != nil {
		return serverSettingsFile{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return serverSettingsFile{}, false, nil
		}
		return serverSettingsFile{}, false, fmt.Errorf("desktop: read %q: %w", path, err)
	}
	var f serverSettingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return serverSettingsFile{}, false, fmt.Errorf("desktop: parse %q: %w", path, err)
	}
	return f, true, nil
}

// saveServerSettingsFile persists f to serverConfigPath(), atomically and
// durably: it writes the encoded settings to a temp file inside the same
// directory, fsyncs that temp file so its content has actually reached
// disk before it is ever visible under the real name, then renames it over
// the target path, so a reader -- or a crash mid-write -- can never
// observe a partially-written or truncated desktop.json.
func saveServerSettingsFile(f serverSettingsFile) error {
	path, err := serverConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("desktop: create %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("desktop: encode server settings: %w", err)
	}

	tmp, err := os.CreateTemp(dir, serverConfigFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("desktop: create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("desktop: write %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("desktop: sync %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("desktop: close %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("desktop: rename %q to %q: %w", tmpName, path, err)
	}
	succeeded = true
	return nil
}

// cleanServerHost validates and normalises a user-supplied host: trims
// surrounding whitespace and, since typing the whole URL out of habit is
// the single most likely way to fill this field, recognises a leading
// "http://" or "https://" -- returning it as typedScheme instead of just
// discarding it. typedScheme is "" when the host carried no such prefix at
// all, meaning "the caller should keep whatever scheme was already in
// effect" -- assembleCandidateURL is where that distinction actually
// matters.
//
// Anything left in the host itself that is not a bare hostname or IPv4
// address is rejected with a *domain.Error (domain.ErrCodeInvalidInput).
func cleanServerHost(host string) (cleanHost string, typedScheme string, err error) {
	h := strings.TrimSpace(host)
	switch {
	case strings.HasPrefix(h, "https://"):
		typedScheme = "https"
		h = strings.TrimPrefix(h, "https://")
	case strings.HasPrefix(h, "http://"):
		typedScheme = "http"
		h = strings.TrimPrefix(h, "http://")
	}
	if h == "" {
		return "", "", domain.New(domain.ErrCodeInvalidInput, "server host must not be empty")
	}
	if strings.ContainsAny(h, " \t\r\n") {
		return "", "", domain.New(domain.ErrCodeInvalidInput, "server host must not contain whitespace")
	}
	if strings.Contains(h, "://") {
		return "", "", domain.New(domain.ErrCodeInvalidInput, "server host must not include a scheme other than http/https")
	}
	if strings.ContainsAny(h, "/?#") {
		return "", "", domain.New(domain.ErrCodeInvalidInput, "server host must not include a path")
	}
	if strings.Contains(h, ":") {
		return "", "", domain.New(domain.ErrCodeInvalidInput, "server host must not include a port — use the Port field")
	}
	if strings.ContainsAny(h, "@%\\") {
		return "", "", domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server host must not contain %q, %q or %q", "@", "%", `\`))
	}
	if _, err := url.ParseRequestURI(buildServerURL(h, minServerPort)); err != nil {
		return "", "", domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server host %q is not a valid hostname", h))
	}
	return h, typedScheme, nil
}

// normalizeFileScheme validates and normalises serverSettingsFile.Scheme:
// "" and "http" both mean plain http, "https" means itself, and anything
// else is rejected -- a *domain.Error (domain.ErrCodeInvalidInput), the
// same classification an invalid Host or Port gets, so newServerManager's
// "fall back and warn" handling treats all three identically.
func normalizeFileScheme(s string) (string, error) {
	switch s {
	case "", "http":
		return "http", nil
	case "https":
		return "https", nil
	default:
		return "", domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server scheme must be \"http\" or \"https\", got %q", s))
	}
}

// validateSavedPath rejects a serverSettingsFile.Path that set (the only
// writer of this field) could never have produced: anything other than ""
// or a leading "/" with no query or fragment.
func validateSavedPath(path string) error {
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		return domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server path %q must start with \"/\"", path))
	}
	if strings.ContainsAny(path, "?#") {
		return domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server path %q must not contain a query or fragment", path))
	}
	return nil
}

// schemeForFile is normalizeFileScheme's inverse for writing, not reading:
// "http" is written as "" (so the common case keeps producing the exact
// same file this format always has), anything else is written unchanged.
func schemeForFile(scheme string) string {
	if scheme == "http" {
		return ""
	}
	return scheme
}

// assembleCandidateURL validates host/port exactly as cleanServerHost/
// validateServerPort do, resolves the scheme to actually use, builds the
// resulting URL with fallbackPath, and validates that whole assembled URL
// via url.ParseRequestURI before ever handing it back. Both
// serverManager.set (fallbackScheme/fallbackPath = m.scheme/m.pathPrefix)
// and TestServerConnection (the same manager's current values, or plain
// "http"/"") build their candidate URL through this one function, so the
// two can never disagree about what a given host/port resolves to.
func assembleCandidateURL(host string, port int, fallbackScheme, fallbackPath string) (cleanHost string, scheme string, assembledURL string, err error) {
	cleanHost, typedScheme, err := cleanServerHost(host)
	if err != nil {
		return "", "", "", err
	}
	if err := validateServerPort(port); err != nil {
		return "", "", "", err
	}

	scheme = fallbackScheme
	if scheme == "" {
		scheme = "http"
	}
	if typedScheme != "" {
		scheme = typedScheme
	}

	assembledURL = buildServerURLWithScheme(scheme, cleanHost, port, fallbackPath)
	if _, parseErr := url.ParseRequestURI(assembledURL); parseErr != nil {
		return "", "", "", domain.Wrap(domain.ErrCodeInvalidInput, fmt.Sprintf("assembled server URL %q is invalid", assembledURL), parseErr)
	}
	return cleanHost, scheme, assembledURL, nil
}

// validateServerPort rejects a port outside the range a TCP listener can
// actually bind to, as a *domain.Error (domain.ErrCodeInvalidInput).
func validateServerPort(port int) error {
	if port < minServerPort || port > maxServerPort {
		return domain.New(domain.ErrCodeInvalidInput, fmt.Sprintf("server port must be between %d and %d, got %d", minServerPort, maxServerPort, port))
	}
	return nil
}

// buildServerURL assembles a plain "http://host:port" base URL: no scheme
// override, no path. cleanServerHost is its only production caller.
func buildServerURL(host string, port int) string {
	return fmt.Sprintf("http://%s:%d", host, port)
}

// buildServerURLWithScheme assembles "scheme://host:port" plus pathPrefix.
func buildServerURLWithScheme(scheme, host string, port int, pathPrefix string) string {
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, port, pathPrefix)
}

// splitServerURL best-effort parses rawURL (HELIOS_API_URL, or
// defaultAPIURL when that is unset) into everything newServerManager needs
// from it: a host/port GetServerSettings can pre-fill, and the
// scheme/path beyond them that serverManager.set must go on preserving
// across a Host/Port-only save. ok is false, with every other return
// zeroed, when rawURL does not parse into something with a hostname at
// all, or carries a port that is not a plain integer.
func splitServerURL(rawURL string) (host string, port int, scheme, pathPrefix string, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", 0, "", "", false
	}
	scheme = u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	p := 80
	if scheme == "https" {
		p = 443
	}
	if portStr := u.Port(); portStr != "" {
		parsed, err := strconv.Atoi(portStr)
		if err != nil {
			return "", 0, "", "", false
		}
		p = parsed
	}
	return u.Hostname(), p, scheme, u.EscapedPath(), true
}

// remoteClient is transcriptionAPI backed by a *httpclient.Client that can
// be swapped out for a fresh one at any time -- SetServerSettings' whole
// "applied live, no restart" promise. current() takes a read lock just
// long enough to copy the pointer out; every call below then runs against
// that snapshot with no lock held, so a concurrent setBaseURL can never
// block or corrupt an in-flight request -- it only decides which server
// the *next* call reaches. httpclient.Client itself is never mutated after
// construction (see httpclient.New), which is what makes handing out that
// snapshot safe.
type remoteClient struct {
	mu     sync.RWMutex
	client *httpclient.Client
}

var _ transcriptionAPI = (*remoteClient)(nil)

// newRemoteClient returns a remoteClient initially pointed at baseURL.
func newRemoteClient(baseURL string) *remoteClient {
	return &remoteClient{client: httpclient.New(httpclient.Config{BaseURL: baseURL})}
}

// current returns the *httpclient.Client currently live. See this type's
// doc for why it is safe to use after the lock is released.
func (r *remoteClient) current() *httpclient.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

// setBaseURL builds a fresh *httpclient.Client for baseURL and swaps it
// in. The old client is simply dropped -- it owns no resource that needs
// closing.
func (r *remoteClient) setBaseURL(baseURL string) {
	next := httpclient.New(httpclient.Config{BaseURL: baseURL})
	r.mu.Lock()
	r.client = next
	r.mu.Unlock()
}

func (r *remoteClient) Submit(ctx context.Context, req httpclient.SubmitRequest) (*domain.Transcription, error) {
	return r.current().Submit(ctx, req)
}

func (r *remoteClient) Get(ctx context.Context, id string) (*domain.Transcription, error) {
	return r.current().Get(ctx, id)
}

func (r *remoteClient) List(ctx context.Context, f domain.ListFilter) (domain.Page, error) {
	return r.current().List(ctx, f)
}

func (r *remoteClient) Delete(ctx context.Context, id string) error {
	return r.current().Delete(ctx, id)
}

func (r *remoteClient) Cancel(ctx context.Context, id string) (*domain.Transcription, error) {
	return r.current().Cancel(ctx, id)
}

func (r *remoteClient) Retry(ctx context.Context, id string) (*domain.Transcription, error) {
	return r.current().Retry(ctx, id)
}

func (r *remoteClient) GetTranscript(ctx context.Context, id string, format transcript.Format) ([]byte, error) {
	return r.current().GetTranscript(ctx, id, format)
}

func (r *remoteClient) OpenAudio(ctx context.Context, id string) (io.ReadSeekCloser, httpclient.AudioInfo, error) {
	return r.current().OpenAudio(ctx, id)
}

// Health reports r's current target's reachability and readiness via
// GET /readyz, translating a transport failure (the server is down,
// refused the connection, or the request timed out) into
// HealthReport{OK: false, ...} rather than a returned error -- see
// HealthReport's own doc for why every caller of transcriptionAPI.Health
// can rely on that.
func (r *remoteClient) Health(ctx context.Context) (HealthReport, error) {
	report, err := r.current().Readyz(ctx)
	if err != nil {
		return healthReportFromTransportErr(err), nil
	}
	return healthReportFromReady(report), nil
}

// serverManager owns "remote" mode's live connection target end to end:
// the remoteClient App.api (and so every bound method) actually calls, and
// the host/port/URL/warning GetServerSettings reports back.
// newServerManager constructs exactly one, from newAdapter; App.server
// holds it.
//
// mu guards every field below as one unit, and set (below) holds it for
// its entire body -- validate, persist, swap and field-update together --
// so two concurrent SetServerSettings calls can never interleave into a
// state where desktop.json and the live client disagree about which
// server is current. current() takes the same lock, briefly, to hand back
// a consistent snapshot of all four fields together.
type serverManager struct {
	client *remoteClient

	mu   sync.Mutex
	host string
	port int
	// url is the exact base URL currently dialled -- httpclient.Config.
	// BaseURL itself, verbatim, never reconstructed from host/port.
	// GetServerSettings' BaseURL field always returns this.
	url string
	// scheme and pathPrefix are whatever url carries beyond "host:port".
	// pathPrefix is preserved across a save unconditionally; scheme is
	// preserved only when the user's typed Host carries no explicit
	// "http://"/"https://" of its own (assembleCandidateURL's
	// typedScheme). Both are seeded once, at construction
	// (newServerManager), and updated by every successful set()
	// thereafter -- and, since desktop.json persists both, survive a
	// relaunch exactly as saved.
	scheme     string
	pathPrefix string
	// warning explains why the saved desktop.json, if any, was not used --
	// see newServerManager -- or "" when there is nothing to explain.
	// Cleared by a successful set.
	warning string
}

// newServerManager builds the serverManager newAdapter installs:
// desktop.json's saved host/port when one exists and is valid, else
// HELIOS_API_URL (or defaultAPIURL) -- "saved setting > HELIOS_API_URL env
// > default".
//
// Nothing here can fail this process's startup. A desktop.json this
// process cannot even locate, read, or parse, or one whose content fails
// validation, is logged as a warning and treated exactly like "nothing
// saved yet" -- falling all the way back to HELIOS_API_URL/the default --
// with that same explanation threaded onto the returned serverManager's
// warning field, for GetServerSettings to surface to the Settings UI.
func newServerManager() *serverManager {
	saved, ok, loadErr := loadServerSettingsFile()
	warning := ""
	if loadErr != nil {
		path, _ := serverConfigPath()
		logWarnf(nil, "desktop: could not read %q (%s); using HELIOS_API_URL/the default instead — save again in Settings to replace it", path, loadErr)
		warning = invalidSavedSettingsWarning
		ok = false
	}
	if ok {
		cleanHost, _, hostErr := cleanServerHost(saved.Host)
		portErr := validateServerPort(saved.Port)
		scheme, schemeErr := normalizeFileScheme(saved.Scheme)
		pathErr := validateSavedPath(saved.Path)
		if hostErr != nil || portErr != nil || schemeErr != nil || pathErr != nil {
			path, _ := serverConfigPath()
			logWarnf(nil, "desktop: %q is invalid (host: %v; port: %v; scheme: %v; path: %v); using HELIOS_API_URL/the default instead — save again in Settings to replace it", path, hostErr, portErr, schemeErr, pathErr)
			warning = invalidSavedSettingsWarning
		} else {
			assembledURL := buildServerURLWithScheme(scheme, cleanHost, saved.Port, saved.Path)
			if _, parseErr := url.ParseRequestURI(assembledURL); parseErr != nil {
				path, _ := serverConfigPath()
				logWarnf(nil, "desktop: %q assembles to an invalid URL %q (%s); using HELIOS_API_URL/the default instead — save again in Settings to replace it", path, assembledURL, parseErr)
				warning = invalidSavedSettingsWarning
			} else {
				return &serverManager{
					client:     newRemoteClient(assembledURL),
					host:       cleanHost,
					port:       saved.Port,
					url:        assembledURL,
					scheme:     scheme,
					pathPrefix: saved.Path,
				}
			}
		}
	}

	baseURL := getEnv("HELIOS_API_URL", defaultAPIURL)
	host, port, scheme, pathPrefix, parsed := splitServerURL(baseURL)
	if !parsed {
		host, port, scheme, pathPrefix = "", 0, "http", ""
	}
	return &serverManager{
		client:     newRemoteClient(baseURL),
		host:       host,
		port:       port,
		url:        baseURL,
		scheme:     scheme,
		pathPrefix: pathPrefix,
		warning:    warning,
	}
}

// current returns the live host, port, scheme, exact base URL, and any
// warning from a desktop.json that could not be used -- GetServerSettings'
// whole answer besides Editable.
func (m *serverManager) current() (host string, port int, scheme string, url string, warning string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.host, m.port, m.scheme, m.url, m.warning
}

// currentSchemeAndPath returns just the two fields TestServerConnection
// needs as assembleCandidateURL's fallbackScheme/fallbackPath, so a Test
// connection click resolves an untyped scheme (and any path prefix)
// exactly the way a real set() would.
func (m *serverManager) currentSchemeAndPath() (scheme, pathPrefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scheme, m.pathPrefix
}

// set validates host/port, resolves the scheme to use, persists the
// result to desktop.json, and repoints m.client at it -- in that order,
// all under one lock held for the whole call, so a failure at any step
// leaves both the file and the live client exactly as they were.
func (m *serverManager) set(host string, port int) (cleanHost string, cleanPort int, scheme string, assembledURL string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanHost, scheme, assembledURL, err = assembleCandidateURL(host, port, m.scheme, m.pathPrefix)
	if err != nil {
		return "", 0, "", "", err
	}

	if err := saveServerSettingsFile(serverSettingsFile{
		Host:   cleanHost,
		Port:   port,
		Scheme: schemeForFile(scheme),
		Path:   m.pathPrefix,
	}); err != nil {
		return "", 0, "", "", domain.Wrap(domain.ErrCodeInternal, "save server settings", err)
	}

	m.client.setBaseURL(assembledURL)
	m.host, m.port, m.scheme, m.url, m.warning = cleanHost, port, scheme, assembledURL, ""

	return cleanHost, port, scheme, assembledURL, nil
}

// ServerSettingsDTO is the wire shape GetServerSettings/SetServerSettings
// return. Host/Port/Scheme/BaseURL describe the live target this process
// talks to right now -- never a value pending a save that has not
// happened yet, so the frontend can always trust what it is shown.
// Editable is always true in this remote-only client (there is exactly one
// server setting, and App.server is never nil -- see newAdapter); it is
// still carried, rather than dropped, so the frontend's Settings screen
// does not need a separate build-time flag to know whether to render the
// form at all. Warning is non-empty exactly once, right after startup,
// when desktop.json existed but could not be used (see newServerManager).
type ServerSettingsDTO struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Scheme   string `json:"scheme"`
	BaseURL  string `json:"baseUrl"`
	Editable bool   `json:"editable"`
	Warning  string `json:"warning,omitempty"`
}

// GetServerSettings reports the live remote server target: exactly the
// baseURL every other bound method's a.api call currently reaches, via
// a.server.client. It never fails on its own -- reading in-memory state
// cannot -- so the error return is always nil; it exists only to match the
// promise-rejecting convention every other bound method uses.
func (a *App) GetServerSettings() (ServerSettingsDTO, error) {
	if a.server == nil {
		return ServerSettingsDTO{Editable: false}, nil
	}
	host, port, scheme, u, warning := a.server.current()
	return ServerSettingsDTO{Host: host, Port: port, Scheme: scheme, BaseURL: u, Editable: true, Warning: warning}, nil
}

// SetServerSettings validates host/port, persists them, and repoints every
// subsequent call through a.api at the new server immediately (see
// serverManager.set) -- no restart needed. Unlike the sibling ingestion
// service, there is no live event stream to move onto the new server
// afterwards (see app.go's own doc on why this service has none at all):
// the frontend's own polling loop (lib/store.ts) picks up the new server
// on its next List call.
//
// It returns a *domain.Error with domain.ErrCodeInvalidInput for a
// malformed host or an out-of-range port, and domain.ErrCodeConflict when
// this process has no server setting to change at all (a.server == nil,
// which never happens in production -- see GetServerSettings' doc); the
// frontend is expected to disable this action rather than rely on that
// error, but a defensive call must still fail cleanly rather than panic on
// a nil a.server.
func (a *App) SetServerSettings(host string, port int) (ServerSettingsDTO, error) {
	if a.server == nil {
		return ServerSettingsDTO{}, domain.New(domain.ErrCodeConflict, "server settings are not editable in this mode")
	}
	cleanHost, cleanPort, scheme, u, err := a.server.set(host, port)
	if err != nil {
		return ServerSettingsDTO{}, err
	}
	return ServerSettingsDTO{Host: cleanHost, Port: cleanPort, Scheme: scheme, BaseURL: u, Editable: true}, nil
}

// testConnectionTimeout bounds TestServerConnection's own readiness call --
// far shorter than internal/httpclient's own defaultRequestTimeout, which
// still governs every ordinary request this app makes once a server is
// actually configured. A user clicking Test connection against a host
// whose port silently drops packets is watching this one button spin in
// real time; making them wait out the full 30s of defaultRequestTimeout
// before finding out would read as "hung", not "unreachable". A var, not a
// const, solely so this file's own tests can shrink it rather than a test
// actually waiting out 5 real seconds.
var testConnectionTimeout = 5 * time.Second

// TestServerConnection checks whether host:port answers before the user
// commits to it via SetServerSettings. It builds a throwaway
// *httpclient.Client against the exact candidate URL a set(host, port)
// call would build right now -- assembleCandidateURL, the same function
// set itself uses, given a.server's own current scheme/path as fallback
// (or plain "http"/"" when a.server is nil) -- and calls its Readyz,
// bounded by testConnectionTimeout rather than Readyz's own much longer
// defaultRequestTimeout, without ever touching a.server's live client, so
// a failed or merely exploratory test can never disrupt whatever server
// this process is actually talking to.
//
// A malformed host/port is a *domain.Error (domain.ErrCodeInvalidInput),
// exactly as SetServerSettings would report it. An unreachable server is
// not an error at all -- it comes back as HealthReportDTO{OK: false, ...}
// with a "connection" check naming the transport failure, so the frontend
// can render it with the same code path it already has for Health. When
// that failure is specifically testConnectionTimeout expiring, the
// check's Detail is replaced with a message naming the host/port actually
// tested and how long this app waited, rather than the raw
// `Get "http://host:port/readyz": context deadline exceeded` a bare
// transport error reads as; every other transport failure keeps whatever
// detail Readyz itself produced.
func (a *App) TestServerConnection(host string, port int) (HealthReportDTO, error) {
	fallbackScheme, fallbackPath := "http", ""
	if a.server != nil {
		fallbackScheme, fallbackPath = a.server.currentSchemeAndPath()
	}
	cleanHost, _, testURL, err := assembleCandidateURL(host, port, fallbackScheme, fallbackPath)
	if err != nil {
		return HealthReportDTO{}, err
	}

	ctx, cancel := context.WithTimeout(a.runtimeCtx(), testConnectionTimeout)
	defer cancel()

	client := httpclient.New(httpclient.Config{BaseURL: testURL})
	report, err := client.Readyz(ctx)
	if err != nil {
		health := healthReportFromTransportErr(err)
		// ctx's own deadline (testConnectionTimeout), not Readyz's much
		// longer internal one, is what actually bounds this call -- so
		// ctx.Err() being context.DeadlineExceeded here means Readyz gave
		// up because nothing answered within testConnectionTimeout
		// specifically, not merely that the connection was refused or
		// failed to resolve.
		if ctx.Err() == context.DeadlineExceeded {
			if check, ok := health.Checks[connectionCheckName]; ok && !check.OK {
				check.Detail = fmt.Sprintf(
					"no response from %s:%d after %s — check the port, or whether the server is behind a reverse proxy (e.g. https on 443)",
					cleanHost, port, testConnectionTimeout,
				)
				health.Checks[connectionCheckName] = check
			}
		}
		return healthReportDTOFromReport(health), nil
	}
	return healthReportDTOFromReport(healthReportFromReady(report)), nil
}
