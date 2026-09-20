package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// This file is a deliberately thin remnant of the sibling ingestion
// service's own mode.go, which switched an entire adapter (in-process
// "local" vs. HTTP "remote") on HELIOS_MODE. This client has exactly one
// mode: it always talks to a running helios-transcription-api over HTTP
// (see docs/DESKTOP.md's "remote mode only" decision) -- there is no
// in-process wire.Build path here at all, so there is no switch left to
// make. What remains is newAdapter (this file's one piece of startup
// wiring) and systemDownloadDir, which DefaultSaveDirectory (reveal.go)
// still needs regardless of mode.

// defaultAPIURL is HELIOS_API_URL's default: a helios-transcription-api
// instance listening on its own default HELIOS_HTTP_ADDR (":7462") on the
// same machine.
const defaultAPIURL = "http://localhost:7462"

// adapter bundles the transcriptionAPI this process talks to with the
// serverManager that owns it, so main can build both in one call and hand
// them to NewApp. It has no teardown of its own -- a *httpclient.Client
// owns no resource that needs closing (see remoteClient.setBaseURL) --
// unlike the sibling's adapter, which also carried an in-process worker
// pool's shutdown func in "local" mode.
type adapter struct {
	api    transcriptionAPI
	server *serverManager
}

// newAdapter builds the adapter main hands to NewApp: a serverManager
// resolving the live target from desktop.json, HELIOS_API_URL, or
// defaultAPIURL (see newServerManager's own doc for the exact precedence),
// and its remoteClient as the transcriptionAPI every bound method calls
// through.
func newAdapter() *adapter {
	server := newServerManager()
	return &adapter{api: server.client, server: server}
}

// getEnv returns the value of the environment variable name, or def when
// it is unset or empty.
func getEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// xdgDownloadDirEnv is the environment variable most desktop session
// managers export with the user's chosen downloads directory, per
// freedesktop.org's XDG user directories spec.
const xdgDownloadDirEnv = "XDG_DOWNLOAD_DIR"

// userDirsRelPath is where XDG_DOWNLOAD_DIR itself is recorded when it is
// not (or no longer) exported into the environment: xdg-user-dirs-update
// writes it here, relative to the user's home directory, at desktop-session
// login on most Linux distributions.
const userDirsRelPath = ".config/user-dirs.dirs"

// downloadsSubdir is the plain, convention-only downloads directory name
// checked directly under home when neither $XDG_DOWNLOAD_DIR nor
// user-dirs.dirs yields one.
const downloadsSubdir = "Downloads"

// systemDownloadDir resolves the user's system downloads directory, in the
// order freedesktop.org's XDG user directories convention defines:
//
//  1. $XDG_DOWNLOAD_DIR, if set and an absolute path.
//  2. The XDG_DOWNLOAD_DIR entry in ~/.config/user-dirs.dirs, parsed
//     directly (see parseUserDirsDownload) rather than by shelling out to
//     xdg-user-dir, which is not installed on every distribution.
//  3. ~/Downloads, if it already exists.
//
// It is DefaultSaveDirectory's (reveal.go) one source of truth for what to
// suggest, and reveal.go's audioCacheRoot uses os.UserCacheDir() instead
// -- a different, app-owned directory this function has no involvement in.
func systemDownloadDir() (string, error) {
	if v := os.Getenv(xdgDownloadDirEnv); v != "" && filepath.IsAbs(v) {
		return v, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}

	userDirsPath := filepath.Join(home, userDirsRelPath)
	if dir, ok := parseUserDirsDownload(userDirsPath, home); ok {
		return dir, nil
	}

	downloads := filepath.Join(home, downloadsSubdir)
	if info, err := os.Stat(downloads); err == nil && info.IsDir() {
		return downloads, nil
	}

	return "", fmt.Errorf("no absolute %s, no usable XDG_DOWNLOAD_DIR in %s, and %s does not exist", xdgDownloadDirEnv, userDirsPath, downloads)
}

// userDirsDownloadLine matches an XDG_DOWNLOAD_DIR assignment in a
// user-dirs.dirs file, e.g. `XDG_DOWNLOAD_DIR="$HOME/Downloads"` -- the
// exact form xdg-user-dirs-update writes, and the only one
// parseUserDirsDownload accepts.
var userDirsDownloadLine = regexp.MustCompile(`^\s*XDG_DOWNLOAD_DIR\s*=\s*"([^"]*)"\s*$`)

// parseUserDirsDownload reads path -- normally ~/.config/user-dirs.dirs, a
// shell-sourceable file xdg-user-dirs-update writes at desktop-session
// login -- and returns its XDG_DOWNLOAD_DIR entry, with a leading $HOME
// expanded to home. It reports ok == false rather than an error for every
// way this can fail to yield a usable directory: a missing or unreadable
// file, a file with no such line, or a value that is not an absolute path
// once expanded.
func parseUserDirsDownload(path, home string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		m := userDirsDownloadLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := m[1]
		if v == "$HOME" || strings.HasPrefix(v, "$HOME/") {
			v = home + strings.TrimPrefix(v, "$HOME")
		}
		if !filepath.IsAbs(v) {
			return "", false
		}
		return filepath.Clean(v), true
	}
	return "", false
}
