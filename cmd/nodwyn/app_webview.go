//go:build webview

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	webview "github.com/webview/webview_go"
)

// openWindow shows the console in a native window (WebKit on macOS) and
// returns when the window closes. The page gets three host functions: a
// folder chooser, a notification, and opening a link in the default
// browser. None of them reads or writes anything the page names.
func openWindow(ctx context.Context, address string, forceBrowser bool) error {
	if forceBrowser {
		if err := openBrowser(ctx, address); err != nil {
			fmt.Fprintln(os.Stderr, "app: open this address in your browser:", address)
		}
		<-ctx.Done()
		return nil
	}
	w := webview.New(false)
	if w == nil {
		return errors.New("app: no window available; try nodwyn app --browser")
	}
	defer w.Destroy()
	w.SetTitle("nodwyn")
	w.SetSize(1280, 860, webview.HintNone)
	w.SetSize(760, 560, webview.HintMin)
	_ = w.Bind("nodwynPickFolder", pickFolder)
	_ = w.Bind("nodwynNotify", notify)
	_ = w.Bind("nodwynOpenExternal", openExternal)
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			w.Dispatch(w.Terminate)
		case <-closed:
		}
	}()
	w.Navigate(address)
	w.Run()
	return nil
}

// pickFolder asks the operating system for a folder; "" when the person
// cancels.
func pickFolder() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "Choose a folder for nodwyn to work in")`)
	case "linux":
		cmd = exec.Command("zenity", "--file-selection", "--directory", "--title=Choose a folder for nodwyn to work in")
	default:
		return "", errors.New("no folder chooser on this system; type the path instead")
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", nil // canceled
	}
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(strings.TrimSpace(string(out)), "/")
	if path == "" {
		path = "/"
	}
	return path, nil
}

// notify shows a system notification. Its text is the page's generic
// notice, passed as arguments, never spliced into a script.
func notify(title, body string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body).Run()
	case "linux":
		return exec.Command("notify-send", title, body).Run()
	}
	return nil
}

// openExternal opens an http(s) link in the default browser.
func openExternal(link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("only web links open outside nodwyn")
	}
	return openBrowser(context.Background(), u.String())
}
