package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nibb/internal/frontend/rpc"
)

type entry struct {
	text     string
	thinking bool
}

type model struct {
	inspector                                    *inspector
	inspectionToken                              int
	ctx                                          context.Context
	backend                                      backend
	reconnect                                    func(context.Context) (backend, error)
	repo, id                                     string
	sessions                                     []rpc.SessionInfo
	selected                                     int
	loading, active, busy, working, disconnected bool
	width, height                                int
	promptInput                                  textinput.Model
	composer                                     textarea.Model
	viewport                                     viewport.Model
	transcript                                   []entry
	liveText, liveThinking                       string
	thinking                                     bool
	lastSeq                                      uint64
	prompt                                       *pendingPrompt
	resolved                                     map[string]bool
	status                                       string
	generation                                   int
	operations                                   int
}
type listResult struct {
	items []rpc.SessionInfo
	err   error
}
type attachResult struct {
	snapshot snapshot
	err      error
}
type nextResult struct {
	event      rpc.ClientEvent
	err        error
	generation int
}
type actionResult struct {
	prompt, kind string
	err          error
}
type reconnectResult struct {
	backend  backend
	snapshot snapshot
	err      error
}

func newModel(ctx context.Context, b backend, repo, id string, thinking bool) *model {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "Type a displayed choice, then Enter"
	input.SetVirtualCursor(true)
	input.CharLimit = 0
	m := &model{ctx: ctx, backend: b, repo: repo, id: id, width: 80, height: 24, promptInput: input, composer: newComposer(), viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(18)), thinking: thinking, resolved: map[string]bool{}}
	m.viewport.SoftWrap = true
	m.viewport.FillHeight = true
	m.composer.Focus()
	m.layout()
	return m
}
func (m *model) Init() tea.Cmd {
	m.loading = true
	if m.id != "" {
		return m.attach(m.id)
	}
	return func() tea.Msg { items, err := m.backend.List(m.ctx); return listResult{items, err} }
}
func (m *model) attach(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		s, err := m.backend.Attach(ctx, m.repo, id)
		return attachResult{s, err}
	}
}
func (m *model) next() tea.Cmd {
	b, gen := m.backend, m.generation
	return func() tea.Msg { ev, err := b.Next(m.ctx); return nextResult{ev, err, gen} }
}
func (m *model) action(kind, prompt string, work func(context.Context) error) tea.Cmd {
	m.operations++
	m.working = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		return actionResult{prompt, kind, work(ctx)}
	}
}
func (m *model) restore(s snapshot) {
	m.id, m.repo, m.busy, m.active = s.ID, s.Repo, s.Busy, true
	m.loading, m.disconnected = false, false
	m.lastSeq = 0
	m.transcript = nil
	m.liveText = ""
	m.liveThinking = ""
	m.prompt = nil
	m.inspector = nil
	m.resolved = map[string]bool{}
	m.promptInput.Reset()
	m.promptInput.EchoMode = textinput.EchoNormal
	m.promptInput.Blur()
	m.composer.Focus()
	for _, e := range s.History {
		m.consume(rpc.ClientEvent{ID: s.ID, Kind: string(e.Kind), Seq: e.Seq, Payload: e.Payload})
	}
	m.busy = s.Busy
	for _, e := range s.Pending {
		m.consume(e)
	}
	m.refresh(true)
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.layout()
		m.refresh(false)
	case listResult:
		m.loading = false
		m.sessions = msg.items
		if msg.err != nil {
			m.status = "Cannot list sessions: " + safe(msg.err.Error())
		}
	case attachResult:
		m.loading = false
		if msg.err != nil {
			m.status = "Cannot attach: " + safe(msg.err.Error())
			return m, nil
		}
		m.restore(msg.snapshot)
		m.status = ""
		return m, m.next()
	case reconnectResult:
		m.loading = false
		if msg.err != nil {
			m.status = "Reconnect failed: " + safe(msg.err.Error())
			return m, nil
		}
		m.backend = msg.backend
		m.restore(msg.snapshot)
		m.status = "Reconnected; history and current prompts refreshed."
		return m, m.next()
	case nextResult:
		if msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.disconnected = true
			m.working = false
			m.prompt = nil
			m.promptInput.Reset()
			m.promptInput.EchoMode = textinput.EchoNormal
			m.status = "Disconnected. Ctrl+R reconnects; submitted operations are never retried."
			m.refresh(false)
			return m, nil
		}
		m.consume(msg.event)
		m.refresh(false)
		return m, m.next()
	case inspectResult:
		if msg.generation != m.generation || m.inspector == nil || msg.token != m.inspector.token {
			return m, nil
		}
		m.inspector.loading = false
		if msg.err != nil {
			m.inspector.content = "Cannot inspect: " + safe(msg.err.Error())
		} else {
			m.inspector.content = safe(msg.content)
		}
		m.refresh(false)
		if m.prompt == nil {
			m.viewport.GotoTop()
		}
		return m, nil
	case actionResult:
		m.operations = max(0, m.operations-1)
		m.working = m.operations > 0
		if msg.err != nil {
			if msg.kind == "friction" && m.inspector != nil && m.inspector.reading {
				m.inspector.reading = false
				m.inspector.loading = false
				m.inspector.content = "Friction read failed: " + safe(msg.err.Error())
			}
			if msg.kind == "send" || msg.kind == "friction" {
				m.busy = false
			}
			m.status = "Request failed: " + safe(msg.err.Error()) + ". Inspect state before retrying."
			if msg.kind == "reply" {
				m.clearPrompt(msg.prompt)
			}
		} else {
			m.status = ""
			if msg.kind == "reply" {
				m.clearPrompt(msg.prompt)
			}
		}
		m.refresh(false)
	case tea.KeyPressMsg:
		key := msg.String()
		switch key {
		case "ctrl+q":
			return m, tea.Quit
		case "ctrl+c":
			if m.active && m.busy && !m.disconnected {
				b, id := m.backend, m.id
				return m, m.action("abort", "", func(ctx context.Context) error { return b.Abort(ctx, id) })
			}
			if m.busy {
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+r":
			if m.reconnect != nil && m.active && !m.loading && !m.working {
				m.loading = true
				m.generation++
				m.inspector = nil
				m.backend.Close()
				m.prompt = nil
				m.promptInput.Reset()
				m.promptInput.EchoMode = textinput.EchoNormal
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
					defer cancel()
					b, err := m.reconnect(ctx)
					if err != nil {
						return reconnectResult{err: err}
					}
					s, err := b.Attach(ctx, m.repo, m.id)
					if err != nil {
						b.Close()
						return reconnectResult{err: err}
					}
					return reconnectResult{backend: b, snapshot: s}
				}
			}
		case "pgup":
			m.viewport.PageUp()
			return m, nil
		case "pgdown":
			m.viewport.PageDown()
			return m, nil
		case "ctrl+end":
			m.viewport.GotoBottom()
			return m, nil
		}
		if m.loading || m.disconnected {
			return m, nil
		}
		if !m.active {
			switch key {
			case "up":
				m.selected = max(0, m.selected-1)
			case "down":
				m.selected = min(len(m.sessions), m.selected+1)
			case "enter":
				m.loading = true
				id := ""
				if m.selected > 0 {
					id = m.sessions[m.selected-1].ID
				}
				return m, m.attach(id)
			}
			return m, nil
		}
		if m.inspector != nil && m.prompt == nil {
			switch key {
			case "esc":
				m.inspector = nil
				m.refresh(true)
			case "r":
				if m.inspector.command != "" && !m.inspector.loading && !m.working {
					return m, m.inspect(m.inspector.command)
				}
			}
			return m, nil
		}
		if m.working {
			return m, nil
		}
		if key == "enter" {
			return m, m.submit()
		}
		return m, m.updateEditor(msg)
	}
	if m.active && !m.loading && !m.working && !m.disconnected && (m.inspector == nil || m.prompt != nil) {
		return m, m.updateEditor(msg)
	}
	return m, nil
}
func (m *model) submit() tea.Cmd {
	if m.prompt != nil {
		return m.reply(m.promptInput.Value())
	}
	text := m.composer.Value()
	if text == "" {
		return nil
	}
	switch {
	case isInspectorCommand(text):
		return m.inspect(text)
	case text == ":q" || text == ":quit" || text == ":exit":
		return tea.Quit
	case text == ":help" || text == ":h":
		m.inspectionToken++
		m.inspector = &inspector{title: "help", token: m.inspectionToken, content: helpText}
		m.composer.Reset()
		m.refresh(false)
		m.viewport.GotoTop()
		return nil
	case text == ":thinking":
		m.thinking = !m.thinking
		m.composer.Reset()
		m.refresh(false)
		return nil
	case strings.HasPrefix(text, ":") && !strings.HasPrefix(text, ":eval "):
		m.status = "Unknown command; :help lists this slice's commands."
		return nil
	}
	if m.busy {
		m.status = "Session is busy. Ctrl+C aborts; your draft is kept."
		return nil
	}
	eval := strings.HasPrefix(text, ":eval ")
	source := text
	if eval {
		source = strings.TrimPrefix(text, ":eval ")
	}
	m.composer.Reset()
	m.busy = true
	b, id := m.backend, m.id
	return m.action("send", "", func(ctx context.Context) error { return b.Send(ctx, id, source, eval) })
}
func (m *model) View() tea.View {
	if m.width < 30 || m.height < 8 {
		v := tea.NewView("Resize terminal (30×8 minimum).\nCtrl+Q quits.")
		v.AltScreen = true
		return v
	}
	state := "idle"
	if m.busy {
		state = "running"
	}
	if m.loading {
		state = "loading"
	}
	if m.disconnected {
		state = "disconnected"
	}
	title := "nibb · " + state
	var body string
	if !m.active {
		title = "nibb · choose a session"
		rows := []string{"New session in " + safe(m.repo)}
		for _, s := range m.sessions {
			label := s.FirstMessage
			if label == "" {
				label = s.ID
			}
			state := ""
			if s.Busy {
				state = " [running]"
			} else if s.InFlight {
				state = " [interrupted]"
			}
			rows = append(rows, safe(label)+state+" · "+safe(s.Repo))
		}
		start := max(0, m.selected-max(1, m.height-8)+1)
		end := min(len(rows), start+max(1, m.height-8))
		var lines []string
		for i := start; i < end; i++ {
			mark := "  "
			if i == m.selected {
				mark = "› "
			}
			lines = append(lines, clip(mark+rows[i], m.width))
		}
		body = strings.Join(lines, "\n") + "\n\n↑/↓ select · Enter open · Ctrl+Q quit"
	} else {
		title += " · " + safe(m.id)
		if m.inspector != nil {
			title = "nibb · " + m.inspector.title + " · read-only"
		}
		if m.prompt != nil {
			title = "nibb · NEEDS YOUR ANSWER · " + m.prompt.title
		}
		editor := m.composer.View()
		if m.prompt != nil {
			editor = m.promptInput.View()
		}
		if m.inspector != nil && m.prompt == nil {
			editor = "Esc returns to conversation"
			if m.inspector.command != "" {
				editor += " · r refreshes"
			}
		}
		body = m.viewport.View() + "\n" + strings.Repeat("─", m.width) + "\n" + editor
	}
	help := "PgUp/PgDn scroll · Ctrl+C abort/quit · Ctrl+Q quit"
	if m.reconnect != nil {
		help += " · Ctrl+R reconnect (quit detaches)"
	}
	if m.active && m.prompt == nil && m.inspector == nil {
		help = "Enter sends · Alt+Enter/Ctrl+J newline · Ctrl+Q quits"
	}
	content := clip(title, m.width) + "\n" + body + "\n" + clip(safe(m.status), m.width) + "\n" + clip(help, m.width)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
func (m *model) refresh(bottom bool) {
	m.layout()
	atBottom := m.viewport.AtBottom()
	var rows []string
	for _, line := range m.transcript {
		if !line.thinking || m.thinking {
			rows = append(rows, line.text)
		}
	}
	content := strings.Join(rows, "\n")
	if m.thinking && m.liveThinking != "" {
		content += "\nthinking> " + safe(m.liveThinking)
	}
	if m.liveText != "" {
		content += "\nassistant> " + safe(m.liveText)
	}
	if m.inspector != nil {
		content = m.inspector.content
	}
	if m.prompt != nil {
		content = m.prompt.detail + "\n\n" + m.prompt.choices
	}
	m.viewport.SetContent(content)
	if bottom || atBottom {
		m.viewport.GotoBottom()
	}
}
func (m *model) append(text string) { m.transcript = append(m.transcript, entry{text: safe(text)}) }
func (m *model) clearPrompt(id string) {
	m.resolved[id] = true
	if m.prompt != nil && m.prompt.id == id {
		m.prompt = nil
		m.promptInput.Reset()
		m.promptInput.EchoMode = textinput.EchoNormal
		m.promptInput.Blur()
		m.composer.Focus()
	}
}

var _ tea.Model = (*model)(nil)
