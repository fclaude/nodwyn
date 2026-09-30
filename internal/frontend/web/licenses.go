package web

import (
	"embed"
	"strings"
)

//go:embed licenses/*.txt
var licenseFiles embed.FS

// notices names each embedded third-party component and its license file.
var notices = []struct{ name, file string }{
	{"Browser transport: coder/websocket", "websocket.txt"},
	{"Console typeface: Newsreader (SIL Open Font License 1.1)", "font-newsreader.txt"},
	{"Console typeface: Instrument Sans (SIL Open Font License 1.1)", "font-instrument-sans.txt"},
	{"Console typeface: IBM Plex Mono (SIL Open Font License 1.1)", "font-ibm-plex-mono.txt"},
}

// Licenses preserves the console's third-party notices in standalone binaries.
func Licenses() string {
	var b strings.Builder
	for _, n := range notices {
		text, _ := licenseFiles.ReadFile("licenses/" + n.file)
		b.WriteString("\n\n" + n.name + "\n\n" + string(text))
	}
	return b.String()
}
