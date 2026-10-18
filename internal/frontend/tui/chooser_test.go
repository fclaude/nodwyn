package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

func letter(text string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(text)[0], Text: text} }

// chooserModel is the chooser over n stored sessions, as the daemon lists
// them: even ones in /work/alpha, odd ones in /work/beta.
func chooserModel(n int) *model {
	m := newModel(context.Background(), nil, "/repo", "", false)
	m.height = 20
	for i := 0; i < n; i++ {
		repo := "/work/beta"
		if i%2 == 0 {
			repo = "/work/alpha"
		}
		m.sessions = append(m.sessions, rpc.SessionInfo{ID: fmt.Sprintf("s%03d", i), Repo: repo, FirstMessage: fmt.Sprintf("task number %d", i)})
	}
	return m
}

// After a disconnect during a turn Ctrl+C used to be swallowed: busy stayed
// set, so the key neither aborted nor quit, and only Ctrl+Q or Ctrl+R left.
func TestCtrlCQuitsAfterADisconnectDuringATurn(t *testing.T) {
	m := testModel()
	m.busy = true
	m.Update(nextResult{err: errors.New("EOF"), generation: m.generation})
	if !m.disconnected {
		t.Fatal("not disconnected")
	}
	if !strings.Contains(m.status, "Ctrl+R reconnects") || !strings.Contains(m.helpLine(), "Ctrl+R reconnects the daemon · Ctrl+C or Ctrl+Q detaches") {
		t.Fatalf("the disconnect does not offer the reconnect, or says quitting does more than detach: %q, %q", m.status, m.helpLine())
	}
	_, cmd := m.Update(ctrlC())
	if cmd == nil {
		t.Fatal("Ctrl+C did nothing after the disconnect")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("Ctrl+C did not quit")
	}
}

// Quitting leaves a task running in a daemon another process serves, and
// ends it with one this process serves: the footer says which.
func TestTheFooterSaysWhatQuittingDoes(t *testing.T) {
	m := testModel()
	if !strings.Contains(m.helpLine(), "Ctrl+Q detaches; tasks go on in the daemon") {
		t.Fatalf("attached: %q", m.helpLine())
	}
	m.owned = true
	m.busy = true
	if !strings.Contains(m.helpLine(), "Ctrl+Q quits, ending the tasks this process serves") {
		t.Fatalf("serving: %q", m.helpLine())
	}
}

// The first Ctrl+C clears an unsent draft and the second quits, and a busy
// turn's footer says Ctrl+C aborts it.
func TestCtrlCClearsADraftBeforeQuitting(t *testing.T) {
	m := testModel()
	m.composer.SetValue("a long\nmessage")
	if _, cmd := m.Update(ctrlC()); cmd != nil || m.composer.Value() != "" {
		t.Fatalf("the draft survived the first Ctrl+C or it quit: %q", m.composer.Value())
	}
	_, cmd := m.Update(ctrlC())
	if cmd == nil {
		t.Fatal("the second Ctrl+C did nothing")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("the second Ctrl+C did not quit")
	}
	m.busy = true
	if !strings.Contains(m.View().Content, "Ctrl+C aborts the turn") {
		t.Fatal("a running turn's footer does not say how to stop it")
	}
}

// A --resume that fails still offers the stored sessions, so the one meant
// can be picked without quitting.
func TestFailedResumeOffersTheStoredSessions(t *testing.T) {
	o := testOptions(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b := testBackend(t, o)
	first := newModel(ctx, b, o.Repo, "", false)
	first.Update(first.Init()())
	press(first, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !first.active {
		t.Fatalf("no session to list: %s", first.status)
	}
	m := newModel(ctx, b, o.Repo, "no-such-session", false)
	if _, cmd := m.Update(m.Init()()); cmd != nil {
		m.Update(cmd())
	}
	if m.active || !strings.HasPrefix(m.status, "Cannot attach") {
		t.Fatalf("the resume did not fail: active=%v %q", m.active, m.status)
	}
	if len(m.sessions) == 0 || m.loading {
		t.Fatalf("no stored sessions after the failed resume (loading %v)", m.loading)
	}
	if !strings.Contains(m.View().Content, "untitled") {
		t.Fatalf("the chooser does not show the stored session:\n%s", m.View().Content)
	}
}

// The chooser pages, jumps and filters; PgDn used to scroll a hidden viewport.
func TestChooserPagesJumpsAndFilters(t *testing.T) {
	m := chooserModel(400)
	press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.selected != m.height-8 {
		t.Fatalf("PgDn moved to %d", m.selected)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.selected != 400 { // the new-session row comes first
		t.Fatalf("End moved to %d", m.selected)
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	press(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.selected != 0 {
		t.Fatalf("Home moved to %d", m.selected)
	}
	for _, r := range "number 31" {
		press(m, letter(string(r)))
	}
	if got := m.listed(); len(got) != 11 { // 31 and 310 to 319
		t.Fatalf("the filter left %d sessions", len(got))
	}
	if m.selected != 1 || !strings.Contains(m.View().Content, "filter: number 31") {
		t.Fatalf("the cursor is on %d:\n%s", m.selected, m.View().Content)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || !m.loading {
		t.Fatal("Enter did not open the first match")
	}
	m.loading = false
	for range 3 {
		press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	if m.filter != "number" {
		t.Fatalf("Backspace left %q", m.filter)
	}
	// The repository is searched too.
	m.filter = ""
	for _, r := range "alpha" {
		press(m, letter(string(r)))
	}
	if got := m.listed(); len(got) != 200 {
		t.Fatalf("the repository filter left %d", len(got))
	}
	press(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.filter != "" || m.selected != 0 || len(m.listed()) != 400 {
		t.Fatalf("Esc did not clear the filter: %q %d", m.filter, m.selected)
	}
}
