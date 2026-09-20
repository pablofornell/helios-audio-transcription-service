// Command helios-ats-desktop is the Wails v2 desktop shell for HELIOS-ATS:
// a native window bound to the same REST API internal/httpapi exposes,
// talked to over HTTP via internal/httpclient (see ports.go's
// transcriptionAPI and mode.go's newAdapter — there is no in-process mode;
// this client is always remote). The UI itself lives in desktop/frontend,
// built separately against desktop/DESIGN.md; this program's job is a
// correct, buildable shell and the bindings that frontend calls.
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	ad := newAdapter()
	app := NewApp(ad)

	runErr := wails.Run(&options.App{
		Title:  "HELIOS-ATS",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 27, B: 27, A: 1},
		OnStartup:        app.OnStartup,
		Bind: []interface{}{
			app,
		},
		// See errors.go's formatError doc: without this, every error
		// returned from a bound method would reach the frontend as a
		// flattened string, losing domain.ErrorCode entirely.
		ErrorFormatter: formatError,
	})
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "helios-ats-desktop:", runErr)
		os.Exit(1)
	}
}
