package config

import (
	"reflect"
	"strings"
	"testing"
)

const effortConfig = `
[servers.openai]
provider = "openai"
url = "https://api.openai.com/v1"
credential = "openai-key"

[servers.local]
url = "http://localhost:11434"

[control]
server = "openai"
model = "gpt-5"
effort = "high"

[models.gpt-5]
server = "openai"
model = "gpt-5"
efforts = ["low", "medium", "high"]
effort = "medium"

[models.small]
server = "local"
model = "qwen3:8b"
`

// Efforts are a fact about a model at an address, listed once: [control]
// takes them from the entry for the same model, and a model nobody declared
// them for takes none.
func TestEffortsComeFromTheEntryForTheSameModel(t *testing.T) {
	c, err := Parse([]byte(effortConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low", "medium", "high"}
	if got := c.Efforts(c.Control); !reflect.DeepEqual(got, want) {
		t.Errorf("control's efforts: %v, want those of its entry %v", got, want)
	}
	if got := c.Efforts(c.Models["gpt-5"]); !reflect.DeepEqual(got, want) {
		t.Errorf("the entry's efforts: %v", got)
	}
	if got := c.Efforts(c.Models["small"]); got != nil {
		t.Errorf("a model that declares none takes %v", got)
	}
	// A model the config names only by server and id, as a conversation
	// may choose one, still finds them.
	if got := c.Efforts(Model{Server: "openai", Model: "gpt-5"}); !reflect.DeepEqual(got, want) {
		t.Errorf("a model chosen by server and id: %v", got)
	}
	if got := c.Efforts(Model{Server: "openai", Model: "gpt-5-mini"}); got != nil {
		t.Errorf("another model on the same server takes %v", got)
	}
	// Another server's model of the same id is another model.
	if got := c.Efforts(Model{Provider: "openai", URL: "https://elsewhere.example/v1", Model: "gpt-5"}); got != nil {
		t.Errorf("the same id at another address takes %v", got)
	}
}

func TestSameModelIgnoresATrailingSlashAndSpellsOutTheDefaultProvider(t *testing.T) {
	var c Config
	if !c.SameModel(Model{URL: "http://h:1/", Model: "m"}, Model{Provider: "ollama", URL: "http://h:1", Model: "m"}) {
		t.Error("a trailing slash or an unset provider made one model two")
	}
	if c.SameModel(Model{URL: "http://h:1", Model: "m"}, Model{Provider: "openai", URL: "http://h:1", Model: "m"}) {
		t.Error("two providers at one address are one model")
	}
	if c.SameModel(Model{}, Model{}) {
		t.Error("two unset models are one model")
	}
}

func TestEffortsAreValidatedWithTheConfig(t *testing.T) {
	for name, tc := range map[string]struct{ toml, want string }{
		"a default no entry declares": {"[control]\nurl = \"http://h\"\nmodel = \"m\"\neffort = \"high\"\n", "declares no efforts"},
		"a default outside the list":  {"[control]\nurl = \"http://h\"\nmodel = \"m\"\nefforts = [\"low\"]\neffort = \"high\"\n", "not one the model takes (low)"},
		"a word that is not a word":   {"[control]\nurl = \"http://h\"\nmodel = \"m\"\nefforts = [\"Very High\"]\n", "short lower-case word"},
		"an effort listed twice":      {"[control]\nurl = \"http://h\"\nmodel = \"m\"\nefforts = [\"low\", \"low\"]\n", "listed twice"},
		"a model entry too":           {"[models.a]\nurl = \"http://h\"\nmodel = \"m\"\neffort = \"low\"\n", "models.a: effort \"low\""},
	} {
		if _, err := Parse([]byte(tc.toml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
		}
	}
}

func TestModelTablesEditInPlace(t *testing.T) {
	raw := []byte("# my models\n" + effortConfig)
	m := Model{Server: "local", Model: "gpt-oss:20b", Efforts: []string{"low", "medium", "high"}, Effort: "low", NumCtx: 32768}
	out, c, err := SetModel(raw, "oss", m)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Models["oss"]; !reflect.DeepEqual(got.Efforts, m.Efforts) || got.Effort != "low" || got.NumCtx != 32768 || got.URL != "http://localhost:11434" {
		t.Errorf("saved model: %+v", got)
	}
	if !strings.HasPrefix(string(out), "# my models\n") || !strings.Contains(string(out), "[models.gpt-5]\nserver = \"openai\"") {
		t.Errorf("the rest of the file changed:\n%s", out)
	}
	// Saving it again replaces it, and removing it leaves the others.
	m.Effort = "high"
	out, c, err = SetModel(out, "oss", m)
	if err != nil || c.Models["oss"].Effort != "high" || strings.Count(string(out), "[models.oss]") != 1 {
		t.Fatalf("replace: %v\n%s", err, out)
	}
	out, c, err = RemoveModel(out, "oss")
	if err != nil {
		t.Fatal(err)
	}
	if _, there := c.Models["oss"]; there || strings.Contains(string(out), "oss") || c.Models["gpt-5"].Effort != "medium" {
		t.Errorf("after removal:\n%s", out)
	}
	if again, _, err := RemoveModel(out, "oss"); err != nil || string(again) != string(out) {
		t.Errorf("removing a model that is not there changed the file: %v", err)
	}
	if _, _, err := SetModel(raw, "bad name", m); err == nil {
		t.Error("a model named with a space was written")
	}
	if _, _, err := SetModel(raw, "control", m); err == nil {
		t.Error("a [models.control] was written: control is the default model's name")
	}
}

func TestRemoveServerRefusesOneAModelNames(t *testing.T) {
	if _, _, err := RemoveServer([]byte(effortConfig), "openai"); err == nil || !strings.Contains(err.Error(), `server "openai" is not configured`) {
		t.Errorf("a server the control model names was removed: %v", err)
	}
	// Once nothing names it, it goes.
	out, c, err := SetModel([]byte("[servers.spare]\nprovider = \"openai\"\nurl = \"https://spare.example/v1\"\n\n[servers.keep]\nurl = \"http://h\"\n"), "m", Model{Server: "keep", Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	out, c, err = RemoveServer(out, "spare")
	if err != nil {
		t.Fatal(err)
	}
	if _, there := c.Servers["spare"]; there || c.Servers["keep"].URL != "http://h" || strings.Contains(string(out), "spare") {
		t.Errorf("after removal:\n%s", out)
	}
}

// A model's efforts and default survive the control table's rewrite, which
// is what saving the default model does.
func TestControlKeepsItsEffortsWhenRewritten(t *testing.T) {
	m := Model{Server: "openai", Model: "gpt-5", Efforts: []string{"low", "high"}, Effort: "low"}
	out, c, err := SetControl([]byte(effortConfig), m)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Control.Efforts, []string{"low", "high"}) || c.Control.Effort != "low" {
		t.Errorf("control: %+v\n%s", c.Control, out)
	}
	if !strings.Contains(string(out), "efforts = [\"low\", \"high\"]\neffort = \"low\"\n") {
		t.Errorf("not written as TOML a person would:\n%s", out)
	}
}
