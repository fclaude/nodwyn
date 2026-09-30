//go:build !webview

package main

import (
	"context"
	"fmt"
	"os"
)

// openWindow, in a build without the webview tag, uses the default browser
// and runs until interrupted.
func openWindow(ctx context.Context, url string, _ bool) error {
	if err := openBrowser(ctx, url); err != nil {
		fmt.Fprintln(os.Stderr, "app: open this address in your browser:", url)
	}
	fmt.Fprintln(os.Stderr, "app: running; Ctrl-C quits")
	<-ctx.Done()
	return nil
}
