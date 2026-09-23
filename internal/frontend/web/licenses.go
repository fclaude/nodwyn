package web

import _ "embed"

//go:embed LICENSE.websocket
var websocketLicense string

// Licenses preserves the browser transport's notice in standalone binaries.
func Licenses() string { return "\n\nBrowser transport: coder/websocket\n\n" + websocketLicense }
