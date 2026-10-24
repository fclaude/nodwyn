package web

import (
	"embed"
	"strings"
)

//go:embed licenses/*.txt
var licenseFiles embed.FS

// notices names each embedded third-party component and its license file.
var notices = []struct{ name, file string }{
	{"Console typeface: Newsreader (SIL Open Font License 1.1)", "font-newsreader.txt"},
	{"Console typeface: Instrument Sans (SIL Open Font License 1.1)", "font-instrument-sans.txt"},
	// The two monospace files are a subset of IBM Plex Mono, so they are a modified
	// version and carry another name, as the licence's reserved font name asks.
	{"Console typeface: Nodwyn Console Mono, a subset of IBM Plex Mono (SIL Open Font License 1.1)", "font-ibm-plex-mono.txt"},
	// The model catalogue is a snapshot of models.dev's data, which asks for its
	// notice to travel with copies.
	{"Model catalogue: models.dev (MIT)", "models-dev.txt"},
	// Every Go library linked into the binary, written by scripts/licenses.py
	// from go list, so a dependency added to go.mod brings its notice.
	{"Go libraries", "go-modules.txt"},
}

// Licenses preserves the console's third-party notices in standalone binaries.
func Licenses() string {
	var b strings.Builder
	for _, n := range notices {
		text, err := licenseFiles.ReadFile("licenses/" + n.file)
		if err != nil {
			// A notice that is missing says so, instead of printing nothing.
			text = []byte("(this build lacks the notice: " + err.Error() + ")")
		}
		b.WriteString("\n\n" + n.name + "\n\n" + string(text))
	}
	return b.String()
}
