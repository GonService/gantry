//go:build desktop

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"gantry/internal/server"
)

const desktopBuild = true

// Origins the Wails webview uses for its own pages (macOS/Linux and Windows).
var desktopOrigins = []string{"wails://wails", "http://wails.localhost"}

// runDesktop opens the native window. The page itself is served by the Wails
// asset server (so the hidden title bar and drag regions work), while API
// calls and streams go straight to the local server at api.
// winState is the size and place of the window, kept between runs.
type winState struct {
	W, H, X, Y int
}

const (
	winMinW, winMinH = 980, 620
)

// appIcon is the window icon on Linux.
//
//go:embed build/appicon.png
var appIcon []byte

func loadWinState(path string) (winState, bool) {
	var st winState
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &st) != nil || st.W < winMinW || st.H < winMinH || st.W > 10000 || st.H > 10000 {
		return winState{}, false
	}
	return st, true
}

func runDesktop(static fs.FS, api string, streams []string, token, statePath string, lang func() string) error {
	files := http.FileServerFS(static)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/config.js" {
			cfg, _ := json.Marshal(map[string]any{
				"api": api, "streams": streams, "token": token, "version": server.Version, "mode": "desktop", "lang": lang(),
			})
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			fmt.Fprintf(w, "window.GANTRY=%s;\n", cfg)
			return
		}
		files.ServeHTTP(w, r)
	})
	saved, hasSaved := loadWinState(statePath)
	width, height := 1360, 880
	if hasSaved {
		width, height = saved.W, saved.H
	}
	var appCtx context.Context
	return wails.Run(&options.App{
		Title:     "Gantry",
		Width:     width,
		Height:    height,
		MinWidth:  winMinW,
		MinHeight: winMinH,
		OnStartup: func(ctx context.Context) { appCtx = ctx },
		OnDomReady: func(ctx context.Context) {
			screens, err := runtime.ScreenGetAll(ctx)
			if hasSaved {
				// Back where it was, if that place is still on a screen (a
				// monitor may have been unplugged). Wails gives the sizes of
				// the screens, not where they are: side by side is assumed.
				totalW, maxH := 0, 0
				for _, sc := range screens {
					totalW += sc.Size.Width
					maxH = max(maxH, sc.Size.Height)
				}
				if err == nil && totalW > 0 && (saved.X < -(saved.W-120) || saved.X > totalW-120 || saved.Y < -40 || saved.Y > maxH-120) {
					runtime.WindowCenter(ctx)
					return
				}
				runtime.WindowSetPosition(ctx, saved.X, saved.Y)
				return
			}
			// First run: most of the screen (the tables of a cluster need room).
			if err != nil {
				return
			}
			for _, sc := range screens {
				if !sc.IsCurrent {
					continue
				}
				w, h := sc.Size.Width*92/100, sc.Size.Height*92/100
				w, h = min(max(w, winMinW), 1440), min(max(h, winMinH), 920)
				runtime.WindowSetSize(ctx, w, h)
				runtime.WindowCenter(ctx)
			}
		},
		OnBeforeClose: func(ctx context.Context) bool {
			if appCtx != nil && !runtime.WindowIsMaximised(ctx) && !runtime.WindowIsFullscreen(ctx) {
				var st winState
				st.W, st.H = runtime.WindowGetSize(ctx)
				st.X, st.Y = runtime.WindowGetPosition(ctx)
				if st.W >= winMinW && st.H >= winMinH {
					if b, err := json.Marshal(st); err == nil {
						_ = os.MkdirAll(filepath.Dir(statePath), 0o700)
						_ = os.WriteFile(statePath, b, 0o600)
					}
				}
			}
			return false // close
		},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 255},
		CSSDragProperty:  "--wails-draggable",
		CSSDragValue:     "drag",
		AssetServer:      &assetserver.Options{Handler: handler},
		// Linux: the window icon and the name the desktop groups it by (the
		// gantry.desktop file of the package uses the same). The GPU stays off
		// in the webview: with some drivers WebKitGTK draws a blank window.
		Linux: &linux.Options{
			Icon:             appIcon,
			ProgramName:      "gantry",
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "Gantry",
				Message: "Containers e labs locais e remotos · GonService",
			},
		},
	})
}
