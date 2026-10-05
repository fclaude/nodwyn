//go:build webview

package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed desktop.js
var desktopJS string

func desktopScript(origin, token string) string {
	quoted, _ := json.Marshal(origin)
	capability, _ := json.Marshal(token)
	return strings.NewReplacer("NODWYN_ORIGIN", string(quoted), "NODWYN_TOKEN", string(capability)).Replace(desktopJS)
}

// openWindow keeps the console on its authenticated loopback origin. Only the
// three desktop conveniences use Wails; tasks still use the existing RPC bridge.
func openWindow(ctx context.Context, address string, forceBrowser bool) error {
	if forceBrowser {
		if err := openBrowser(ctx, address); err != nil {
			fmt.Fprintln(os.Stderr, "app: open this address in your browser:", address)
		}
		<-ctx.Done()
		return nil
	}
	origin, err := desktopOrigin(address)
	if err != nil {
		return err
	}
	var app *application.App
	var capability [32]byte
	if _, err := rand.Read(capability[:]); err != nil {
		return err
	}
	bridge := desktopBridge{origin: origin, token: hex.EncodeToString(capability[:])}
	app = application.New(application.Options{
		Name: "nodwyn",
		Mac:  application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		RawMessageHandler: func(window application.Window, message string, source *application.OriginInfo) {
			if source == nil {
				return
			}
			bridge.handle(source.Origin, source.TopOrigin, source.IsMainFrame, message, window.ExecJS)
		},
	})
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "nodwyn", Title: "nodwyn", Width: 1280, Height: 860, MinWidth: 760, MinHeight: 560,
		URL: address, JS: desktopScript(origin, bridge.token),
	})
	bridge.pickFolder = func() (string, error) {
		return app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).
			SetTitle("Choose a folder for nodwyn to work in").AttachToWindow(window).PromptForSingleSelection()
	}
	bridge.openExternal = app.Browser.OpenURL
	bridge.notify = notify
	window.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) { app.Quit() })
	closed := make(chan struct{})
	defer close(closed)
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go func() {
			select {
			case <-ctx.Done():
				app.Quit()
			case <-closed:
			}
		}()
	})
	return app.Run()
}

// notify sends only the fixed attention notice; session text never reaches an
// operating-system notification or a script interpreter.
func notify() error {
	const title = "nodwyn needs your answer"
	const body = "Open nodwyn to review what it asks."
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body).Run()
	case "linux":
		return exec.Command("notify-send", title, body).Run()
	}
	return nil
}
