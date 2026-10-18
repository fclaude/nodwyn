// Package startup starts nodwyn's daemon when the person logs in, so that
// scheduled runs go on with no window open (ADR 52 §11): a systemd user unit
// on Linux, a launchd agent on macOS. It writes or removes those files when a
// person asks, on this computer, and never starts or stops a daemon: the one
// it writes starts at the next login.
package startup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Place is where the files go: the system's kind and the home and config
// directories of the person. Here is this process's; tests and the browser
// demo give their own, so they never write a real login item.
type Place struct {
	GOOS       string
	Home       string
	ConfigHome string
}

// Item is the login item as the files have it.
type Item struct {
	// Supported is false on a system nodwyn has no login item for.
	Supported bool `json:"supported"`
	// On says the file that starts the daemon at login is there.
	On bool `json:"on"`
	// Path is that file.
	Path string `json:"path,omitempty"`
	// Other says the file runs another nodwyn than the one asked about.
	Other bool `json:"other,omitempty"`
}

// mark is the first line of every file this package writes: a file without
// it is someone else's, and Disable leaves it alone.
const mark = "Written by nodwyn: starts its daemon at login (nodwyn startup off removes it)."

// label names the launchd agent.
const label = "nodwyn.daemon"

// Here is the place of this process: its system, home and XDG config home.
func Here(goos string) (Place, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Place{}, err
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	return Place{GOOS: goos, Home: home, ConfigHome: config}, nil
}

func (p Place) supported() bool { return p.GOOS == "linux" || p.GOOS == "darwin" }

// file is the login item's file; link, on Linux, the link that makes
// default.target want it, as systemctl --user enable would make.
func (p Place) file() (file, link string) {
	if p.GOOS == "darwin" {
		return filepath.Join(p.Home, "Library", "LaunchAgents", label+".plist"), ""
	}
	dir := filepath.Join(p.ConfigHome, "systemd", "user")
	return filepath.Join(dir, "nodwyn.service"), filepath.Join(dir, "default.target.wants", "nodwyn.service")
}

// Status reads the login item; exe is the nodwyn it should run.
func (p Place) Status(exe string) Item {
	if !p.supported() {
		return Item{}
	}
	file, link := p.file()
	item := Item{Supported: true, Path: file}
	raw, err := os.ReadFile(file)
	if err != nil {
		return item
	}
	if link != "" {
		if _, err := os.Stat(link); err != nil {
			return item
		}
	}
	item.On = true
	item.Other = !strings.Contains(string(raw), p.command(exe))
	return item
}

// command is how the file names exe, as Render writes it.
func (p Place) command(exe string) string {
	if p.GOOS == "darwin" {
		return "<string>" + xmlText(exe) + "</string>"
	}
	return "ExecStart=" + unitWord(exe) + " daemon"
}

// Render is the file that starts exe's daemon at login, with env (NAME=value)
// in its environment: the daemon started at login does not have the shell's.
func (p Place) Render(exe string, env []string) string {
	var b strings.Builder
	if p.GOOS == "darwin" {
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
		b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
		b.WriteString("<!-- " + mark + " -->\n")
		b.WriteString("<plist version=\"1.0\">\n<dict>\n")
		b.WriteString("\t<key>Label</key>\n\t<string>" + label + "</string>\n")
		b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n\t\t" + p.command(exe) + "\n\t\t<string>daemon</string>\n\t</array>\n")
		if len(env) > 0 {
			b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
			for _, kv := range env {
				name, value, _ := strings.Cut(kv, "=")
				b.WriteString("\t\t<key>" + xmlText(name) + "</key>\n\t\t<string>" + xmlText(value) + "</string>\n")
			}
			b.WriteString("\t</dict>\n")
		}
		// Started at login, and again if it stops on an error; not when it
		// was stopped on purpose.
		b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
		b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
		b.WriteString("</dict>\n</plist>\n")
		return b.String()
	}
	b.WriteString("# " + mark + "\n")
	b.WriteString("[Unit]\nDescription=nodwyn daemon: scheduled runs, and the tasks its clients open\n\n")
	b.WriteString("[Service]\n" + p.command(exe) + "\n")
	for _, kv := range env {
		b.WriteString("Environment=" + envWord(kv) + "\n")
	}
	b.WriteString("Restart=on-failure\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// unitWord quotes a word of ExecStart=: systemd expands specifiers (%) and
// variables ($) there, and splits on spaces.
func unitWord(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

// envWord quotes an assignment of Environment=, where systemd expands
// specifiers but not variables.
func envWord(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
	return `"` + r.Replace(s) + `"`
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Enable writes the login item for exe, replacing one nodwyn wrote before.
func (p Place) Enable(exe string, env []string) error {
	if !p.supported() {
		return fmt.Errorf("nodwyn has no login item on %s", p.GOOS)
	}
	if !filepath.IsAbs(exe) {
		return fmt.Errorf("the daemon's program must be an absolute path, not %q", exe)
	}
	file, link := p.file()
	if err := p.ours(file); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(p.Render(exe, env)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	if link == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if target, err := os.Readlink(link); err == nil && target == file {
		return nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(file, link)
}

// Disable removes the login item nodwyn wrote; one it did not write stays.
func (p Place) Disable() error {
	if !p.supported() {
		return nil
	}
	file, link := p.file()
	if err := p.ours(file); err != nil {
		return err
	}
	if link != "" {
		if target, err := os.Readlink(link); err == nil && target == file {
			if err := os.Remove(link); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ours refuses a file at the login item's place that nodwyn did not write.
func (p Place) ours(file string) error {
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(raw), mark) {
		return fmt.Errorf("%s was not written by nodwyn; it is left as it is", file)
	}
	return nil
}

// Environment is what a login item passes on of this process's environment:
// PATH, since a daemon started at login has the system's short one and the
// tools a task runs are often elsewhere, and the XDG homes nodwyn finds its
// configuration and data by, when set.
func Environment(lookup func(string) (string, bool)) []string {
	var env []string
	for _, name := range []string{"PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if value, ok := lookup(name); ok && value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// Temporary says exe is a build `go run` made, which is gone once it exits:
// a login item must not name it.
func Temporary(exe string) bool {
	return strings.Contains(filepath.ToSlash(exe), "/go-build")
}
