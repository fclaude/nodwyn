package core

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The default loop has two profiles (ADR 32, amended 2026-10-17): lean, the
// loop the SWE-bench runs measured, and full, the loop before it. This
// package's tests were written against the second and run it (the profile
// environment is set to full for them, test/fakes/home); the tests here set
// the variable themselves, so none of them is parallel.

const (
	leanPromptStart = "You are a coding agent working inside the repository at "
	fullPromptStart = "You are nodwyn, a coding agent working inside the repository at "
)

// profileRig is a session in a repository with main.go, and init.scm in
// .nodwyn when init is not empty. adjust sets the session's options on top of
// the default configuration: AutoApprove for a session nobody approves,
// NoConfirm for one a person is there for.
func profileRig(t *testing.T, adjust func(*Options), init string, turns ...fake.Turn) *rig {
	t.Helper()
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return profileRigIn(t, repo, adjust, init, turns...)
}

func profileRigIn(t *testing.T, repo string, adjust func(*Options), init string, turns ...fake.Turn) *rig {
	t.Helper()
	if init != "" {
		if err := os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := fake.NewT(t, turns...)
	t.Cleanup(srv.Close)
	logb := &bytes.Buffer{}
	o := Options{Config: config.Default(), Repo: repo, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb}
	if adjust != nil {
		adjust(&o)
	}
	s, err := newInitSession(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: config.CanonicalPath(repo), logb: logb}
}

func unattendedOptions(o *Options) { o.AutoApprove = true }
func attendedOptions(o *Options)   { o.NoConfirm = true }

// toolCatalogue is the session's tools, by name.
func toolCatalogue(r *rig) []string {
	var names []string
	for _, tool := range r.s.loop.Tools() {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// useProfile sets what the environment says of the profile for the test ("" is
// nothing, so the configuration and the way the session is run choose).
func useProfile(t *testing.T, name string) { t.Helper(); t.Setenv(config.ProfileEnv, name) }

// A session nobody approves runs the lean loop unless told otherwise: the
// short prompt that says nobody is watching, the control model offered
// shell-exec and fs-edit alone, and a turn that changed nothing told so once.
func TestAnUnattendedSessionRunsTheLeanLoopByDefault(t *testing.T) {
	useProfile(t, "")
	r := profileRig(t, unattendedOptions, "", fake.Turn{Content: "done"}, fake.Turn{Content: "done"})
	if r.s.profile != config.ProfileLean {
		t.Fatalf("profile %q, want lean", r.s.profile)
	}
	sys := r.s.loop.WindowContext().System
	if !strings.HasPrefix(sys, leanPromptStart+r.repo+".\n") || !strings.Contains(sys, "You work unattended: nobody approves or watches your calls") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	if len(sys) > 2000 {
		t.Errorf("the lean prompt is %d bytes", len(sys))
	}
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d requests, want 2: one, and one more after the single reminder", len(r.srv.Requests))
	}
	for i := range r.srv.Requests {
		if got := offeredTools(t, r, i); !slices.Equal(got, []string{"fs-edit", "shell-exec"}) {
			t.Errorf("request %d offered %v, want fs-edit and shell-exec alone", i, got)
		}
	}
	if !strings.Contains(lastMessage(r, 1), "No file has changed yet") {
		t.Errorf("a turn that changed nothing was not told so: %q", lastMessage(r, 1))
	}
	// The catalogue is the session's, and the lean tools are registered in it
	// for a project that names them in lean-tools.
	for _, name := range []string{"scheme", "outline", "tests-for", "fs-read"} {
		if !slices.Contains(toolCatalogue(r), name) {
			t.Errorf("the lean session's catalogue lacks %s: %v", name, toolCatalogue(r))
		}
	}
}

// A session a person approves runs the loop it always ran: the long prompt
// that is written for someone following the work, and the whole catalogue of
// tools, with none of the lean profile's additions to it.
func TestAnInteractiveSessionRunsTheFullLoopByDefault(t *testing.T) {
	useProfile(t, "full")
	forced := profileRig(t, attendedOptions, "", fake.Turn{Content: "done"})
	useProfile(t, "")
	r := profileRig(t, attendedOptions, "", fake.Turn{Content: "It is a Go main package."})
	if r.s.profile != config.ProfileFull {
		t.Fatalf("profile %q, want full", r.s.profile)
	}
	sys := r.s.loop.WindowContext().System
	if !strings.HasPrefix(sys, fullPromptStart+r.repo+".\n") || strings.Contains(sys, "You work unattended") || !strings.Contains(sys, "The user follows your work") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	if want := strings.ReplaceAll(forced.s.loop.WindowContext().System, forced.repo, r.repo); sys != want {
		t.Error("the prompt differs from the one the full profile gives")
	}
	if got, want := toolCatalogue(r), toolCatalogue(forced); !slices.Equal(got, want) {
		t.Errorf("tools %v, want the full profile's %v", got, want)
	}
	for _, name := range []string{"fs-read", "fs-write", "fs-edit", "fs-list", "index-grep", "shell-exec"} {
		if !slices.Contains(toolCatalogue(r), name) {
			t.Errorf("the catalogue lacks %s: %v", name, toolCatalogue(r))
		}
	}
	for _, name := range []string{"scheme", "outline", "tests-for"} {
		if slices.Contains(toolCatalogue(r), name) {
			t.Errorf("the full catalogue has %s", name)
		}
	}
	for _, tool := range r.s.loop.Tools() {
		if tool.Name == "fs-read" && strings.Contains(string(mustJSON(t, tool.Parameters)), "symbol") {
			t.Errorf("fs-read takes a symbol in the full profile: %s", mustJSON(t, tool.Parameters))
		}
	}
	if e := r.turn("what is main.go?"); e != "" {
		t.Fatal(e)
	}
	if got, all := offeredTools(t, r, 0), toolCatalogue(r); !slices.Equal(got, all) {
		t.Errorf("the request offered %v, want the whole catalogue %v", got, all)
	}
}

// Each profile is a set of values of the loop's switches (docs/scheme.md
// §9.8): the lean one is what the default files define, the full one what
// full.scm defines over them.
func TestEachProfileSetsTheLoopsSwitches(t *testing.T) {
	useProfile(t, "")
	for profile, want := range map[string]map[string]string{
		config.ProfileLean: {"lean-prompt": "#t", "plan-minimal": "#t", "lean-quiet": "#t", "lean-shell-look": "#t", "finish-reminders": "0", "finish-review-limit": "0", "lean-empty-reminders": "1", "lean-tools": `("shell-exec" "fs-edit")`},
		config.ProfileFull: {"lean-prompt": "#f", "plan-minimal": "#f", "lean-quiet": "#f", "lean-shell-look": "#f", "finish-reminders": "2", "finish-review-limit": "2", "lean-empty-reminders": "0"},
	} {
		r := profileRig(t, func(o *Options) { o.NoConfirm = true; o.Config.Loop.Profile = profile }, "")
		for name, value := range want {
			if got := r.eval(name); got != value {
				t.Errorf("%s: %s = %s, want %s", profile, name, got, value)
			}
		}
	}
}

// The full profile on a session nobody approves is the loop before the lean
// ladder: a run's plan goes through the explorer and the planner and is
// reviewed against what the turn did, and the finish review names what the
// check does not test. The lean profile does neither for the same script.
func TestTheFullProfileRestoresTheFullLoopOfAnUnattendedSession(t *testing.T) {
	useProfile(t, "")
	full := func(o *Options) { unattendedOptions(o); o.Config.Loop.Profile = config.ProfileFull }
	plan := `(change "calc.py" "one" "return 10")
(test "calc_test.py" "test_one" "one() is 10" (fails-today "it is 1") (run "grep -q 'def test_one' calc_test.py"))
(none docs "no document describes calc")
(check "grep -q 'return 10' calc.py")`
	script := func(turns ...fake.Turn) []fake.Turn {
		return append(turns,
			write("t", "calc_test.py", "def test_one():\n    pass\n"),
			write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 2\n"),
			shellArgs("c", "bash", "-c", "grep -q 'return 10' calc.py && grep -q 'def test_one' calc_test.py # pytest"),
			fake.Turn{Content: "done"}, // the full profile tells the test passes on the unchanged code too
		)
	}
	r := profileRigIn(t, codeRepo(t), full, "", append(script(fake.Turn{Content: "calc.py defines one()."}, fake.Turn{Content: plan}), fake.Turn{Content: "the test now calls one()."})...)
	if e := r.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if len(r.srv.Requests) != 7 {
		t.Fatalf("%d requests, want 7: the explorer, the planner, the work and the review's reply; last message %q", len(r.srv.Requests), lastMessage(r, len(r.srv.Requests)-1))
	}
	plans := r.gatePlans()
	if len(plans) != 1 || !strings.Contains(plans[0].Plan, "Test: calc_test.py test_one") {
		t.Fatalf("the plan recorded: %+v", plans)
	}
	if got := offeredTools(t, r, 2); !slices.Contains(got, "fs-read") || !slices.Contains(got, "fs-write") {
		t.Errorf("the control model was offered %v, want the whole catalogue", got)
	}
	if !strings.HasPrefix(r.s.loop.WindowContext().System, fullPromptStart) {
		t.Errorf("the prompt is not the full one:\n%s", r.s.loop.WindowContext().System)
	}
	if msg := lastMessage(r, 6); !strings.Contains(msg, "it passes on the unchanged code too") {
		t.Fatalf("the finish review did not name the test that passes before the change: %q", msg)
	}

	// The same work under the lean profile: the one-line plan, no explorer, no
	// planner, and the turn ends on the model's report.
	lean := profileRigIn(t, codeRepo(t), unattendedOptions, "", script()...)
	if e := lean.planned("make one() return 10"); e != "" {
		t.Fatal(e)
	}
	if len(lean.srv.Requests) != 4 {
		t.Fatalf("%d requests, want 4: the work and its report; last message %q", len(lean.srv.Requests), lastMessage(lean, len(lean.srv.Requests)-1))
	}
	if plans := lean.gatePlans(); len(plans) != 1 || !strings.HasPrefix(plans[0].Plan, "1. Make the change the request calls for") || strings.Contains(plans[0].Plan, "calc_test.py") {
		t.Fatalf("the plan recorded: %+v", plans)
	}
}

// The full profile's finish review: a run's turn that ends on a passing
// check is asked about the parts of the change the check does not test; the
// lean profile ends it on the first report. (A turn whose answer a person
// reads, a chat's, is reviewed in both.)
func TestTheFinishReviewRunsInTheFullProfileAndNotInTheLeanOne(t *testing.T) {
	useProfile(t, "")
	plan := `(change "calc.py" "one" "return 10")
(none test "a one-line change")
(none docs "nothing describes it")
(check "bash $TMPDIR/check.sh")`
	work := []fake.Turn{
		write("c", "$TMPDIR/check.sh", "grep -q 'return 10' calc.py\n"),
		write("w", "calc.py", "def one():\n    return 10\n\n\ndef two():\n    return 20\n"),
		shellArgs("t", "bash", "$TMPDIR/check.sh"),
		{Content: "done"}, // the full profile's review says the check does not test two()'s change
	}
	full := profileRigIn(t, codeRepo(t), func(o *Options) { unattendedOptions(o); o.Config.Loop.Profile = config.ProfileFull }, "",
		append(append([]fake.Turn{{Content: "calc.py defines one()."}, {Content: plan}}, work...), fake.Turn{Content: "two() is part of the change; the check now covers it."})...)
	if e := full.planned("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if n := len(full.srv.Requests); n != 7 || !strings.Contains(lastMessage(full, 6), "still passes with each of these parts of your change taken out") {
		t.Fatalf("%d requests, want 7 with the review in the last: %q", n, lastMessage(full, len(full.srv.Requests)-1))
	}
	lean := profileRigIn(t, codeRepo(t), unattendedOptions, "", work...)
	if e := lean.planned("make one return 10 and two return 20"); e != "" {
		t.Fatal(e)
	}
	if n := len(lean.srv.Requests); n != 4 {
		t.Fatalf("%d requests, want 4: the lean profile makes no finish review; last message %q", n, lastMessage(lean, n-1))
	}
}

// NODWYN_PROFILE overrides [loop] profile, and "auto" follows who approves.
func TestTheEnvironmentOverridesTheProfileOfTheConfig(t *testing.T) {
	for _, c := range []struct {
		name, config, env string
		options           func(*Options)
		want              string
	}{
		{"auto, nobody approves", "auto", "", unattendedOptions, config.ProfileLean},
		{"auto, a run nobody answers", "auto", "", func(o *Options) { o.Unattended = true }, config.ProfileLean},
		{"auto, a person approves", "auto", "", attendedOptions, config.ProfileFull},
		{"the default, a person approves", "", "", attendedOptions, config.ProfileFull},
		{"lean, a person approves", "lean", "", attendedOptions, config.ProfileLean},
		{"full, nobody approves", "full", "", unattendedOptions, config.ProfileFull},
		{"full in the environment over lean in the config", "lean", "full", unattendedOptions, config.ProfileFull},
		{"lean in the environment over full in the config", "full", "lean", attendedOptions, config.ProfileLean},
		{"lean in the environment over auto", "auto", "lean", attendedOptions, config.ProfileLean},
		{"auto in the environment over full in the config", "full", "auto", unattendedOptions, config.ProfileLean},
	} {
		useProfile(t, c.env)
		r := profileRig(t, func(o *Options) { c.options(o); o.Config.Loop.Profile = c.config }, "")
		if r.s.profile != c.want {
			t.Errorf("%s: profile %q, want %q", c.name, r.s.profile, c.want)
		}
		wantPrompt := fullPromptStart
		if c.want == config.ProfileLean {
			wantPrompt = leanPromptStart
		}
		if sys := r.s.loop.WindowContext().System; !strings.HasPrefix(sys, wantPrompt) {
			t.Errorf("%s: the prompt starts %q, want %q", c.name, sys[:min(len(sys), 80)], wantPrompt)
		}
	}
}

// A profile that is not one is refused when the session is made, whether the
// environment or the configuration names it.
func TestAnUnknownProfileIsRefused(t *testing.T) {
	useProfile(t, "leaner")
	_, err := New(Options{Config: config.Default(), Repo: t.TempDir(), Provider: llm.NewOllama("http://127.0.0.1:1"), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), config.ProfileEnv+` must be "auto", "lean" or "full"`) {
		t.Fatalf("err = %v", err)
	}
	useProfile(t, "")
	cfg := config.Default()
	cfg.Loop.Profile = "leaner"
	_, err = New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama("http://127.0.0.1:1"), NoConfirm: true, LogWriter: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "loop.profile must be") {
		t.Fatalf("err = %v", err)
	}
}

// A project's init.scm loads after the profile, so it may redefine any switch
// of it: more tools for the lean loop, a number of reminders, or the lean
// prompt in a session a person approves.
func TestAProjectsInitCanRedefineALeanSwitch(t *testing.T) {
	useProfile(t, "")
	r := profileRig(t, unattendedOptions, `(define lean-tools (append lean-tools '("fs-read" "index-grep")))
(define lean-empty-reminders 0)`, fake.Turn{Content: "done"})
	if e := r.turn("fix it"); e != "" {
		t.Fatal(e)
	}
	if got := offeredTools(t, r, 0); !slices.Equal(got, []string{"fs-edit", "fs-read", "index-grep", "shell-exec"}) {
		t.Errorf("offered %v", got)
	}
	if sys := r.s.loop.WindowContext().System; !strings.Contains(sys, "read files, search their contents, edit files and run programs") {
		t.Errorf("the lean prompt does not name the tools offered:\n%s", sys)
	}
	if len(r.srv.Requests) != 1 {
		t.Errorf("%d requests, want 1: the lean profile reminds a turn that changed nothing once, and the project said never", len(r.srv.Requests))
	}

	// Under the full profile the same file turns a lean switch on.
	useProfile(t, "")
	person := profileRig(t, attendedOptions, `(define lean-prompt #t)`, fake.Turn{Content: "hello"})
	if person.s.profile != config.ProfileFull {
		t.Fatalf("profile %q", person.s.profile)
	}
	if sys := person.s.loop.WindowContext().System; !strings.HasPrefix(sys, leanPromptStart) {
		t.Fatalf("the project's lean-prompt did not win over the profile:\n%s", sys)
	}
	if e := person.turn("hi"); e != "" {
		t.Fatal(e)
	}
	if got := offeredTools(t, person, 0); !slices.Equal(got, []string{"fs-edit", "shell-exec"}) {
		t.Errorf("offered %v", got)
	}
}

// The lean profile counts a shell command that only reads as looking, for the
// reading nudge, and the full profile counts the read tools alone, as it did.
func TestShellCommandsThatOnlyLookCountAsLookingInTheLeanProfile(t *testing.T) {
	useProfile(t, "")
	const call = `(define (c line) (list (cons 'name "shell-exec") (cons 'id "x") (cons 'args (json-emit (list (cons "cmd" "bash") (cons "args" (list "-c" line)))))))`
	for _, profile := range []string{config.ProfileLean, config.ProfileFull} {
		r := profileRig(t, func(o *Options) { o.AutoApprove = true; o.Config.Loop.Profile = profile }, "")
		if _, err := r.s.Eval(t.Context(), call); err != nil {
			t.Fatal(err)
		}
		for line, want := range map[string]bool{
			`grep -n foo a.py | head -n 20`:  true,
			`sed -n '10,40p' a.py`:           true,
			`git log --oneline -5`:           true,
			`ls django/forms; wc -l a.py`:    true,
			`cat a.py > b.py`:                false,
			`sed -i s/a/b/ a.py`:             false,
			`python -m pytest tests/ -k foo`: false,
			`python -c 'print(1)'`:           false,
			`git checkout -- a.py`:           false,
			`make test`:                      false,
		} {
			if got := r.eval(`(and (shell-look? (c "`+line+`")) #t)`) == "#t"; got != want {
				t.Errorf("%s: %q looks = %v, want %v", profile, line, got, want)
			}
			// look-call? is what the nudge counts: the lean profile's shell
			// reads, the full profile's none.
			if got := r.eval(`(and (look-call? (c "`+line+`")) #t)`) == "#t"; got != (want && profile == config.ProfileLean) {
				t.Errorf("%s: %q counts as looking = %v", profile, line, got)
			}
		}
	}
}

// A program of the scheme tool is no way round the guard: the same command
// line, written to the same path outside the repository, is refused as the
// model's own call is, and so is a write there; the file is never made. The
// tool is the lean profile's.
func TestTheSchemeToolDoesNotGoRoundTheGuard(t *testing.T) {
	useProfile(t, config.ProfileLean)
	outside := filepath.Join(os.TempDir(), "nodwyn-guard-probe-"+filepath.Base(t.TempDir()))
	defer os.Remove(outside)
	scheme := func(id, code string) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "scheme", Args: map[string]any{"code": code}}}}
	}
	r := profileRigIn(t, goRepo(t), unattendedOptions, `(define lean-tools (append lean-tools '("scheme")))`,
		shellArgs("direct", "bash", "-c", "echo x > "+outside),
		scheme("nested", `(shell-exec "bash" (list "-c" "echo x > `+outside+`"))`),
		scheme("write", `(fs-write "`+outside+`" "x")`),
		fake.Turn{Content: "I cannot write outside the repository."},
		fake.Turn{Content: "I cannot write outside the repository."},
	)
	_ = r.turn("write a file")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("%s was made", outside)
	}
	res := r.results()
	for _, id := range []string{"direct", "nested", "write"} {
		if !strings.Contains(res[id], "outside") {
			t.Errorf("%s was not refused for naming a path outside the repository: %q", id, res[id])
		}
	}
}

// Every primitive a lean session registers is documented in docs/scheme.md as
// a full session's are (TestSessionPrimitivesAreDocumented): the scheme,
// outline and tests-for tools are the lean profile's.
func TestLeanSessionPrimitivesAreDocumented(t *testing.T) {
	useProfile(t, config.ProfileLean)
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "scheme.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := profileRig(t, unattendedOptions, "")
	for _, p := range r.s.interp.Prims() {
		if !strings.Contains(string(doc), "`("+p.Name+" ") && !strings.Contains(string(doc), "`("+p.Name+")") && !strings.Contains(string(doc), "`"+p.Name+"`") {
			t.Errorf("primitive %q is not documented in docs/scheme.md", p.Name)
		}
	}
}
