package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// tableHeader matches a TOML table or array-of-tables header line.
var tableHeader = regexp.MustCompile(`^\s*\[\[?\s*([A-Za-z0-9_."-]+)\s*\]\]?\s*(#.*)?$`)

// SetControl returns raw (config.toml's contents) with its [control] table
// replaced by m, keeping every other line — comments included — as written.
// A file without [control] gains one at the end. The result is parsed and
// validated before it is returned, so a caller never writes a file nodwyn
// would refuse to load.
func SetControl(raw []byte, m Model) ([]byte, Config, error) {
	block := controlBlock(m)
	lines := strings.SplitAfter(string(raw), "\n")
	var out strings.Builder
	replaced, skipping := false, false
	for _, line := range lines {
		if match := tableHeader.FindStringSubmatch(strings.TrimRight(line, "\r\n")); match != nil {
			name := strings.Trim(match[1], `"`)
			if skipping {
				skipping = false
			}
			if name == "control" || strings.HasPrefix(name, "control.") {
				if !replaced {
					out.WriteString(block)
					replaced = true
				}
				skipping = true
				continue
			}
		}
		if !skipping {
			out.WriteString(line)
		}
	}
	text := out.String()
	if !replaced {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if text != "" {
			text += "\n"
		}
		text += block
	}
	cfg, err := Parse([]byte(text))
	if err != nil {
		return nil, Config{}, err
	}
	return []byte(text), cfg, nil
}

func controlBlock(m Model) string {
	var b strings.Builder
	b.WriteString("[control]\n")
	field := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s = %s\n", name, quote(value))
		}
	}
	field("provider", m.Provider)
	field("url", m.URL)
	field("model", m.Model)
	if m.NumCtx > 0 {
		fmt.Fprintf(&b, "num_ctx = %d\n", m.NumCtx)
	}
	field("keep_alive", m.KeepAlive)
	field("credential", m.Credential)
	if m.MaxOutput > 0 {
		fmt.Fprintf(&b, "max_output = %d\n", m.MaxOutput)
	}
	field("output_field", m.OutputField)
	if m.Temperature != nil {
		fmt.Fprintf(&b, "temperature = %s\n", strconv.FormatFloat(*m.Temperature, 'f', -1, 64))
	}
	if m.Seed != nil {
		fmt.Fprintf(&b, "seed = %d\n", *m.Seed)
	}
	if m.Vision {
		b.WriteString("vision = true\n")
	}
	price := func(name string, p *float64) {
		if p != nil {
			fmt.Fprintf(&b, "%s = %s\n", name, strconv.FormatFloat(*p, 'f', -1, 64))
		}
	}
	price("price_input", m.PriceInput)
	price("price_cached_input", m.PriceCachedInput)
	price("price_output", m.PriceOutput)
	b.WriteString("\n")
	return b.String()
}

// quote writes a TOML basic string. JSON's escapes are a subset of TOML's.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// WriteFile replaces path with raw atomically, mode 0600, creating its
// directory privately. The configuration holds no secret, but it names
// where content goes, so it is the user's alone.
func WriteFile(path string, raw []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("config: path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("config: %s is not a regular file", path)
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr, os.Chmod(tmp.Name(), 0o600)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
