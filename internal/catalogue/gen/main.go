// Command gen writes models.json, the catalogue's snapshot, from models.dev:
// `go generate ./internal/catalogue` asks models.dev for its api.json, or
// `go run ./gen -from api.json` reads a copy. Nothing in nodwyn's build or
// tests runs it; whoever regenerates the snapshot commits the diff, which
// shows what each provider changed.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/fclaude/nodwyn/internal/catalogue"
)

func main() {
	from := flag.String("from", "", "read models.dev's api.json from this file instead of asking models.dev")
	out := flag.String("out", "models.json", "where to write the snapshot")
	flag.Parse()
	if err := run(*from, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(from, out string) error {
	var raw []byte
	var err error
	if from != "" {
		raw, err = os.ReadFile(from)
	} else {
		raw, err = fetch()
	}
	if err != nil {
		return err
	}
	d, err := catalogue.FromModelsDev(raw, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return err
	}
	text, err := catalogue.Encode(d)
	if err != nil {
		return err
	}
	return os.WriteFile(out, text, 0o644)
}

func fetch() ([]byte, error) {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(catalogue.Source)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", catalogue.Source, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
