package rpc

import (
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
)

// A refused choice must not install Settings' newer judge configuration:
// the task keeps both its judge and what that judge sees until a choice
// succeeds, including while a turn is answering.
func TestRejectedJudgeChoiceKeepsTheTasksConfiguration(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "busy"}[busy], func(t *testing.T) {
			o := options(t)
			if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
				t.Fatal(err)
			}
			st, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, nil)
			if err != nil {
				t.Fatal(err)
			}
			o.Keychain = st
			model := &heldProvider{started: make(chan struct{}), release: make(chan struct{})}
			o.Provider = model
			path, _ := start(t, o)
			c := connect(t, path)
			c.hello()
			c.ok("settings.provider.write", map[string]any{"name": "jev", "provider": "jev", "url": "https://judge.example.invalid", "api_key": "sk-judge-canary-0001"})
			var task struct {
				ID    string          `json:"id"`
				Judge core.JudgeState `json:"judge"`
			}
			into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "judge": "jev"}), &task)
			if task.Judge.Server != "jev" || !task.Judge.Available {
				t.Fatalf("initial judge: %+v", task.Judge)
			}
			if busy {
				c.ok("session.send", map[string]any{"id": task.ID, "text": "hello"})
				waitStarted(t, model)
				t.Cleanup(func() { close(model.release) })
			}
			c.ok("settings.write", map[string]any{"set": map[string]any{"judge.content": config.JudgeContentNone}})
			if r := c.call("session.judge.set", map[string]any{"id": task.ID, "judge": "missing"}); r.Error == nil {
				t.Fatal("the invalid judge was accepted")
			}
			into(t, c.ok("session.info", map[string]any{"id": task.ID}), &task)
			if task.Judge.Server != "jev" || !task.Judge.Available {
				t.Fatalf("a refused choice changed the judge: %+v", task.Judge)
			}
		})
	}
}

// A task chooses its judge as it opens and between turns (ADR 47), among
// the judges settings.read lists with their model and prices, which nobody
// types. A ChatGPT plan's sign-in is listed as one the judge cannot use and
// refused wherever it is chosen, and a paired device chooses no judge.
func TestTasksChooseTheirJudge(t *testing.T) {
	o := options(t)
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID := planCredential(t, st, "chatgpt")
	o.Keychain = st
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	c.ok("settings.provider.write", map[string]any{"name": "jev", "provider": "jev", "url": "https://api.typesafe.ai", "api_key": "sk-judge-canary-0001"})
	c.ok("settings.provider.write", map[string]any{"name": "openai-api", "provider": "openai", "url": "https://api.openai.com/v1", "dialect": "responses", "api_key": "sk-openai-canary-0002"})
	c.ok("settings.provider.write", map[string]any{"name": "openai-plan", "provider": "openai", "url": "https://api.openai.com/v1", "dialect": "responses", "credential": planID})
	c.ok("settings.write", map[string]any{"set": map[string]any{"judge.server": "jev"}})

	var settings settingsResult
	into(t, c.ok("settings.read", map[string]any{}), &settings)
	byName := map[string]judgeChoice{}
	for _, j := range settings.Judges {
		byName[j.Name] = j
	}
	if j := byName["jev"]; j.State != stateReady || j.Model != "jev-latest" || j.Host != "api.typesafe.ai" || !j.Default || j.PriceInput == nil || *j.PriceInput != 0.042 {
		t.Fatalf("Jev: %+v", j)
	}
	if j := byName["openai-api"]; j.State != stateReady || j.Model != "gpt-6-luna" || j.PriceInput == nil || *j.PriceInput != 0.10 || j.Default {
		t.Fatalf("OpenAI with a key: %+v", j)
	}
	if j := byName["openai-plan"]; j.State != statePlan || !strings.Contains(j.Detail, "API key") {
		t.Fatalf("OpenAI with a plan: %+v", j)
	}
	if r := c.call("settings.write", map[string]any{"set": map[string]any{"judge.server": "openai-plan"}}); r.Error == nil || !strings.Contains(r.Error.Message, "ChatGPT plan") {
		t.Fatalf("a judge on a plan was saved: %+v", r.Error)
	}

	var task struct {
		ID    string          `json:"id"`
		Judge core.JudgeState `json:"judge"`
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir(), "judge": "openai-api"}), &task)
	if task.Judge.Server != "openai-api" || task.Judge.Model != "gpt-6-luna" || task.Judge.Host != "api.openai.com" || !task.Judge.Available {
		t.Fatalf("opened with %+v", task.Judge)
	}
	var defaulted struct {
		Judge core.JudgeState `json:"judge"`
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &defaulted)
	if defaulted.Judge.Server != "jev" {
		t.Fatalf("a task opened without a choice has %+v", defaulted.Judge)
	}
	if r := c.call("session.open", map[string]any{"repo": t.TempDir(), "judge": "openai-plan"}); r.Error == nil || !strings.Contains(r.Error.Message, "ChatGPT plan") {
		t.Fatalf("a task opened on a plan judge: %+v", r.Error)
	}

	var state core.JudgeState
	into(t, c.ok("session.judge.set", map[string]any{"id": task.ID, "judge": "jev"}), &state)
	if state.Server != "jev" || state.Model != "jev-latest" {
		t.Fatalf("switched to %+v", state)
	}
	if r := c.call("session.judge.set", map[string]any{"id": task.ID, "judge": "openai-plan"}); r.Error == nil || !strings.Contains(r.Error.Message, "ChatGPT plan") {
		t.Fatalf("switched to a plan judge: %+v", r.Error)
	}
	into(t, c.ok("session.judge.set", map[string]any{"id": task.ID, "judge": ""}), &state)
	if state.Server != "" {
		t.Fatalf("none is %+v", state)
	}
	into(t, c.ok("session.info", map[string]any{"id": task.ID}), &task)
	if task.Judge.Server != "" {
		t.Fatalf("session.info says %+v", task.Judge)
	}

	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.open", map[string]any{"repo": t.TempDir(), "judge": "jev"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device opened a task with a judge it chose: %+v", r.Error)
	}
	if r := phone.call("session.judge.set", map[string]any{"id": task.ID, "judge": "jev"}); r.Error == nil || !strings.Contains(r.Error.Message, "paired device") {
		t.Fatalf("a device chose a task's judge: %+v", r.Error)
	}
}

// A task opened before any judge was set up is given one set up since, from
// the console's picker, without closing and resuming it (ADR 47, amended
// 2026-10-28).
func TestTaskOpenedBeforeAnyJudgeChoosesOneSetUpSince(t *testing.T) {
	o := options(t)
	if err := os.MkdirAll(o.Paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := keychain.Create(o.Paths.Keychain, keychain.FastKDF, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.Keychain = st
	path, _ := start(t, o)
	c := connect(t, path)
	c.hello()
	var task struct {
		ID    string          `json:"id"`
		Judge core.JudgeState `json:"judge"`
	}
	into(t, c.ok("session.open", map[string]any{"repo": t.TempDir()}), &task)
	if task.Judge.Server != "" {
		t.Fatalf("opened with %+v", task.Judge)
	}
	c.ok("settings.provider.write", map[string]any{"name": "jev", "provider": "jev", "url": "https://api.typesafe.ai", "api_key": "sk-judge-canary-0001"})
	var state core.JudgeState
	into(t, c.ok("session.judge.set", map[string]any{"id": task.ID, "judge": "jev"}), &state)
	if state.Server != "jev" || state.Model != "jev-latest" || !state.Available {
		t.Fatalf("chose %+v", state)
	}
	into(t, c.ok("session.info", map[string]any{"id": task.ID}), &task)
	if task.Judge.Server != "jev" {
		t.Fatalf("session.info says %+v", task.Judge)
	}
}
