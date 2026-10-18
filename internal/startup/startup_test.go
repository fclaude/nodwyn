package startup

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/test/fakes/must"
)

func place(t *testing.T, goos string) Place {
	t.Helper()
	home := t.TempDir()
	return Place{GOOS: goos, Home: home, ConfigHome: filepath.Join(home, ".config")}
}

// On Linux the login item is a systemd user unit and the link that makes
// default.target want it, as systemctl --user enable makes; turning it off
// removes both, and a status says which nodwyn it runs.
func TestLinuxLoginItemIsAUserUnit(t *testing.T) {
	p := place(t, "linux")
	exe := "/opt/nod wyn/bin/nodwyn%1$"
	if item := p.Status(exe); !item.Supported || item.On {
		t.Fatalf("before: %+v", item)
	}
	if err := p.Enable(exe, []string{"PATH=/usr/bin:/home/me/go/bin", "XDG_DATA_HOME=/data/100%"}); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(p.ConfigHome, "systemd", "user", "nodwyn.service")
	raw, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart="/opt/nod wyn/bin/nodwyn%%1$$" daemon`,
		`Environment="PATH=/usr/bin:/home/me/go/bin"`,
		`Environment="XDG_DATA_HOME=/data/100%%"`,
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the unit has no %q:\n%s", want, raw)
		}
	}
	link := filepath.Join(p.ConfigHome, "systemd", "user", "default.target.wants", "nodwyn.service")
	if target, err := os.Readlink(link); err != nil || target != unit {
		t.Fatalf("link %q %v", target, err)
	}
	if item := p.Status(exe); !item.On || item.Other || item.Path != unit {
		t.Fatalf("on: %+v", item)
	}
	if item := p.Status("/usr/bin/nodwyn"); !item.On || !item.Other {
		t.Fatalf("another nodwyn: %+v", item)
	}
	// Turning it on again, for another nodwyn, replaces it.
	if err := p.Enable("/usr/bin/nodwyn", nil); err != nil {
		t.Fatal(err)
	}
	if item := p.Status("/usr/bin/nodwyn"); !item.On || item.Other {
		t.Fatalf("replaced: %+v", item)
	}
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{unit, link} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s stayed: %v", path, err)
		}
	}
	if item := p.Status("/usr/bin/nodwyn"); item.On {
		t.Fatalf("off: %+v", item)
	}
}

// On macOS it is a launchd agent, run at load and again if it fails.
func TestMacLoginItemIsALaunchAgent(t *testing.T) {
	p := place(t, "darwin")
	exe := "/Applications/<nodwyn>&.app/nodwyn"
	if err := p.Enable(exe, []string{"PATH=/opt/homebrew/bin:/usr/bin"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(p.Home, "Library", "LaunchAgents", "nodwyn.daemon.plist")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Dict struct {
			Keys    []string `xml:"key"`
			Strings []string `xml:"string"`
			Arrays  []struct {
				Strings []string `xml:"string"`
			} `xml:"array"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(raw, &plist); err != nil {
		t.Fatalf("not a plist: %v\n%s", err, raw)
	}
	if len(plist.Dict.Arrays) != 1 || strings.Join(plist.Dict.Arrays[0].Strings, " ") != exe+" daemon" || plist.Dict.Strings[0] != "nodwyn.daemon" {
		t.Fatalf("%+v", plist.Dict)
	}
	for _, want := range []string{"<key>RunAtLoad</key>", "<key>SuccessfulExit</key>", "<string>/opt/homebrew/bin:/usr/bin</string>"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the agent has no %q:\n%s", want, raw)
		}
	}
	if item := p.Status(exe); !item.On || item.Other || item.Path != file {
		t.Fatalf("%+v", item)
	}
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("the agent stayed")
	}
}

// A file nodwyn did not write is left alone, turning on or off.
func TestALoginItemNodwynDidNotWriteStays(t *testing.T) {
	p := place(t, "linux")
	unit := filepath.Join(p.ConfigHome, "systemd", "user", "nodwyn.service")
	must.MkdirAll(t, filepath.Dir(unit), 0o755)
	must.WriteFile(t, unit, []byte("[Service]\nExecStart=/usr/local/bin/my-own-nodwyn\n"), 0o644)
	if err := p.Enable("/usr/bin/nodwyn", nil); err == nil {
		t.Fatal("replaced a unit nodwyn did not write")
	}
	if err := p.Disable(); err == nil {
		t.Fatal("removed a unit nodwyn did not write")
	}
	if raw, _ := os.ReadFile(unit); !strings.Contains(string(raw), "my-own-nodwyn") {
		t.Fatal("the unit changed")
	}
}

func TestNoLoginItemElsewhere(t *testing.T) {
	p := place(t, "windows")
	if item := p.Status("/x/nodwyn"); item.Supported || item.On {
		t.Fatalf("%+v", item)
	}
	if err := p.Enable("/x/nodwyn", nil); err == nil {
		t.Fatal("wrote a login item on a system it has none for")
	}
	if err := (Place{GOOS: "linux"}).Enable("nodwyn", nil); err == nil {
		t.Fatal("named a program by a relative path")
	}
}

func TestEnvironmentAndTemporaryBuilds(t *testing.T) {
	env := Environment(func(name string) (string, bool) {
		return map[string]string{"PATH": "/usr/bin", "XDG_CONFIG_HOME": "", "HOME": "/home/me", "XDG_DATA_HOME": "/d"}[name], name != "XDG_CONFIG_HOME"
	})
	if strings.Join(env, ";") != "PATH=/usr/bin;XDG_DATA_HOME=/d" {
		t.Fatal(env)
	}
	if !Temporary("/tmp/go-build123/b001/exe/nodwyn") || Temporary("/home/me/go/bin/nodwyn") {
		t.Fatal("temporary builds")
	}
}
