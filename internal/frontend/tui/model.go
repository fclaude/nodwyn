package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

type entry struct {
	tool     *toolCallView
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
	project                                      string // the folder retrieval keeps for (snapshot.Project)
	branch                                       string // the session's own, in a git worktree
	sessions                                     []rpc.SessionInfo
	selected                                     int
	filter                                       string // narrows the chooser's stored sessions
	loading, active, busy, working, disconnected bool
	width, height                                int
	promptInput                                  textinput.Model
	composer                                     textarea.Model
	viewport                                     viewport.Model
	transcript                                   []entry
	activeTool                                   *toolCallView
	liveText, liveThinking                       string
	thinking                                     bool
	details                                      bool // tool arguments and results inline
	lastSeq                                      uint64
	gauge                                        contextGauge
	prompt                                       *pendingPrompt
	resolved                                     map[string]bool
	status                                       string
	generation                                   int
	operations                                   int
	// gitRepo is whether repo looks like a git repository's top folder, so the
	// chooser offers a worktree; worktree opens a new one at once (--worktree).
	gitRepo, worktree bool
	// attaching is while a file is being added: an image waits for its
	// confirm, so the keyboard stays free to answer it.
	attaching bool
}
type listResult struct {
	items []rpc.SessionInfo
	err   error
}
type attachResult struct {
	snapshot snapshot
	err      error
}
type attachFileResult struct {
	name string
	err  error
}
type nextResult struct {
	event      rpc.ClientEvent
	err        error
	generation int
}
type actionResult struct {
	prompt, kind string
	err          error
	// done is what the status line says when it worked, for an action no
	// log record reports (a folder's setting, not the session's).
	done string
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
	m := &model{ctx: ctx, backend: b, repo: repo, id: id, gitRepo: gitTop(repo), width: 80, height: 24, promptInput: input, composer: newComposer(), viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(18)), thinking: thinking, resolved: map[string]bool{}}
	m.viewport.SoftWrap = true
	m.viewport.FillHeight = true
	m.composer.Focus()
	m.layout()
	return m
}

// gitTop is whether dir has its own .git, a folder or a worktree's file: a
// repository's top folder, which is what a worktree is made from. It only
// decides what the chooser offers; core refuses any other folder in a
// sentence (ADR 34).
func gitTop(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
func (m *model) Init() tea.Cmd {
	m.loading = true
	if m.id != "" {
		return m.attach(m.id)
	}
	if m.worktree {
		return m.openWorktree()
	}
	return m.list()
}
func (m *model) list() tea.Cmd {
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

// openWorktree starts a new session in a git worktree of repo. Checking a
// worktree out runs the repository's hooks and filters, so it may take a
// while.
func (m *model) openWorktree() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
		defer cancel()
		s, err := m.backend.OpenWorktree(ctx, m.repo)
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
		return actionResult{prompt: prompt, kind: kind, err: work(ctx)}
	}
}
func (m *model) restore(s snapshot) {
	m.id, m.repo, m.branch, m.busy, m.active = s.ID, s.Repo, s.Branch, s.Busy, true
	m.project = s.Project
	m.loading, m.disconnected = false, false
	m.lastSeq = 0
	m.gauge = contextGauge{}
	m.transcript = nil
	m.activeTool = nil
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
	if !m.busy {
		m.endToolInterval()
	}
	m.refresh(true)
}

// Update routes one message. Three kinds only mutate and fall through to
// the editor, so a paste or a cursor blink still reaches the composer; the
// rest answer for themselves.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg)
	case listResult:
		m.sessionsListed(msg)
	case actionResult:
		m.actionFinished(msg)
	case attachResult:
		return m, m.attached(msg)
	case attachFileResult:
		m.fileAttached(msg)
	case reconnectResult:
		return m, m.reconnected(msg)
	case nextResult:
		return m, m.eventArrived(msg)
	case inspectResult:
		return m, m.inspected(msg)
	case tea.KeyPressMsg:
		return m, m.keyPressed(msg)
	}
	if m.editing() {
		return m, m.updateEditor(msg)
	}
	return m, nil
}

// editing reports whether the composer or the prompt input has the keyboard.
func (m *model) editing() bool {
	return m.active && !m.loading && !m.working && !m.disconnected &&
		(m.inspector == nil || m.prompt != nil)
}

func (m *model) resize(msg tea.WindowSizeMsg) {
	m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	m.layout()
	m.refresh(false)
}

func (m *model) sessionsListed(msg listResult) {
	m.loading = false
	m.sessions = msg.items
	if msg.err != nil {
		m.status = "Cannot list tasks: " + safe(msg.err.Error())
	}
}

func (m *model) attached(msg attachResult) tea.Cmd {
	m.loading = false
	if msg.err != nil {
		m.status = "Cannot attach: " + safe(msg.err.Error())
		if m.sessions != nil {
			return nil
		}
		// --resume and --worktree skip the chooser, so its list was never
		// read: without it the person could not pick the session they meant.
		m.loading = true
		return m.list()
	}
	m.restore(msg.snapshot)
	m.status = ""
	return m.next()
}

func (m *model) reconnected(msg reconnectResult) tea.Cmd {
	m.loading = false
	if msg.err != nil {
		m.status = "Reconnect failed: " + safe(msg.err.Error())
		return nil
	}
	m.backend = msg.backend
	m.restore(msg.snapshot)
	m.status = "Reconnected; history and current prompts refreshed."
	return m.next()
}

// eventArrived consumes one session event and asks for the next. A stream
// error is terminal for this connection: submitted operations are never
// retried, because the daemon may already have applied them.
func (m *model) eventArrived(msg nextResult) tea.Cmd {
	if msg.generation != m.generation {
		return nil
	}
	if msg.err != nil {
		m.disconnected = true
		m.working = false
		m.prompt = nil
		m.promptInput.Reset()
		m.promptInput.EchoMode = textinput.EchoNormal
		m.status = "Disconnected. Ctrl+Q quits; submitted operations are never retried."
		if m.reconnect != nil {
			m.status = "Disconnected. Ctrl+R reconnects, Ctrl+C or Ctrl+Q quits; submitted operations are never retried."
		}
		m.refresh(false)
		return nil
	}
	m.consume(msg.event)
	m.refresh(false)
	return m.next()
}

func (m *model) inspected(msg inspectResult) tea.Cmd {
	if msg.generation != m.generation || m.inspector == nil || msg.token != m.inspector.token {
		return nil
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
	return nil
}

func (m *model) actionFinished(msg actionResult) {
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
	} else {
		m.status = msg.done
	}
	if msg.kind == "reply" {
		m.clearPrompt(msg.prompt)
	}
	m.refresh(false)
}

// keyPressed applies the keys that work everywhere, then hands the rest to
// whichever surface has focus.
func (m *model) keyPressed(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if cmd, handled := m.globalKey(key); handled {
		return cmd
	}
	if m.loading || m.disconnected {
		return nil
	}
	if !m.active {
		return m.listKey(msg)
	}
	if m.inspector != nil && m.prompt == nil {
		return m.inspectorKey(key)
	}
	if m.working {
		return nil
	}
	if key == "enter" {
		return m.submit()
	}
	return m.updateEditor(msg)
}

// globalKey handles quit, abort, reconnect and scrolling. handled is false
// when the key was ours but its precondition failed, so the surfaces below
// still get a chance at it.
func (m *model) globalKey(key string) (tea.Cmd, bool) {
	switch key {
	case "ctrl+q":
		return tea.Quit, true
	case "ctrl+c":
		// An attachment is cancelled as a turn is: an image's confirm is
		// declined, and nothing is added.
		if m.active && (m.busy || m.attaching) && !m.disconnected {
			b, id := m.backend, m.id
			return m.action("abort", "", func(ctx context.Context) error { return b.Abort(ctx, id) }), true
		}
		// As in a shell, the first press clears what was typed and the next
		// one quits, so a long draft is not lost to a reflex.
		if m.active && m.composer.Value() != "" {
			m.composer.Reset()
			m.status = "Draft cleared. Ctrl+C again quits."
			return nil, true
		}
		return tea.Quit, true
	case "ctrl+r":
		if m.reconnect == nil || !m.active || m.loading || m.working {
			return nil, false
		}
		return m.startReconnect(), true
	case "pgup", "pgdown", "ctrl+end":
		if !m.active {
			// The chooser pages its own list; the viewport is hidden.
			return nil, false
		}
		switch key {
		case "pgup":
			m.viewport.PageUp()
		case "pgdown":
			m.viewport.PageDown()
		default:
			m.viewport.GotoBottom()
		}
		return nil, true
	}
	return nil, false
}

// startReconnect drops the current connection and dials again. The
// generation bump makes every event still in flight on the old stream
// belong to nobody.
func (m *model) startReconnect() tea.Cmd {
	m.loading = true
	m.generation++
	m.inspector = nil
	m.backend.Close()
	m.prompt = nil
	m.promptInput.Reset()
	m.promptInput.EchoMode = textinput.EchoNormal
	return func() tea.Msg {
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

// newRows are the chooser's first rows, each a new session: in a new git
// worktree of the folder first when it is a repository's top folder, as the
// console starts tasks in a git workspace by default (ADR 34), then in the
// folder itself.
func (m *model) newRows() []string {
	folder := safe(m.repo)
	if m.gitRepo {
		return []string{"New task in a new worktree of " + folder, "New task in " + folder}
	}
	return []string{"New task in " + folder}
}

// listed is the stored sessions the filter leaves, in the daemon's order.
func (m *model) listed() []rpc.SessionInfo {
	if m.filter == "" {
		return m.sessions
	}
	var out []rpc.SessionInfo
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.FirstMessage+" "+s.Repo+" "+sessionBranch(s)), strings.ToLower(m.filter)) {
			out = append(out, s)
		}
	}
	return out
}

// sessionBranch is the branch a worktree session works on, empty for any
// other.
func sessionBranch(s rpc.SessionInfo) string {
	if s.Worktree == nil {
		return ""
	}
	return s.Worktree.Repository + " " + s.Worktree.Branch
}

// listKey moves through the session list, below the new-session rows, and
// narrows it as the person types: a long history is reached by what the
// session was about or where, not by counting rows.
func (m *model) listKey(msg tea.KeyPressMsg) tea.Cmd {
	news, shown := len(m.newRows()), m.listed()
	last, page := news+len(shown)-1, max(1, m.height-8)
	switch key := msg.String(); key {
	case "up":
		m.selected = max(0, m.selected-1)
	case "down":
		m.selected = min(last, m.selected+1)
	case "pgup":
		m.selected = max(0, m.selected-page)
	case "pgdown":
		m.selected = min(last, m.selected+page)
	case "home":
		m.selected = 0
	case "end", "ctrl+end":
		m.selected = last
	case "esc":
		m.filter, m.selected = "", 0
	case "backspace":
		if m.filter != "" {
			_, size := utf8.DecodeLastRuneInString(m.filter)
			m.filter = m.filter[:len(m.filter)-size]
			m.selectFirstMatch()
		}
	case "enter":
		m.loading = true
		switch {
		case m.selected >= news:
			return m.attach(shown[m.selected-news].ID)
		case m.gitRepo && m.selected == 0:
			return m.openWorktree()
		}
		return m.attach("")
	default:
		if msg.Text != "" && !strings.ContainsFunc(msg.Text, func(r rune) bool { return !unicode.IsPrint(r) }) {
			m.filter += msg.Text
			m.selectFirstMatch()
		}
	}
	return nil
}

// selectFirstMatch moves the cursor to the first stored session the filter
// leaves, or to the top when it leaves none; the new-session rows stay above
// whatever the filter says.
func (m *model) selectFirstMatch() {
	m.selected = 0
	if len(m.listed()) > 0 {
		m.selected = len(m.newRows())
	}
}

func (m *model) inspectorKey(key string) tea.Cmd {
	switch key {
	case "esc":
		m.inspector = nil
		m.refresh(true)
	case "r":
		if m.inspector.command != "" && !m.inspector.loading && !m.working {
			return m.inspect(m.inspector.command)
		}
	}
	return nil
}

func (m *model) submit() tea.Cmd {
	if m.prompt != nil {
		return m.reply(m.promptInput.Value())
	}
	text := m.composer.Value()
	if text == "" {
		return nil
	}
	fields := strings.Fields(text)
	switch {
	case isInspectorCommand(text):
		return m.inspect(text)
	case fields[0] == ":model":
		return m.chooseModel(fields[1:])
	case fields[0] == ":judge":
		return m.chooseJudge(fields[1:])
	case fields[0] == ":earlier" || fields[0] == ":index":
		return m.retrieval(fields)
	case fields[0] == ":attach":
		return m.attachFile(strings.TrimPrefix(strings.TrimSpace(text), ":attach"))
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
	case text == ":details":
		m.details = !m.details
		m.status = "Tool arguments and results: " + onOff(m.details)
		m.composer.Reset()
		m.refresh(false)
		return nil
	case strings.HasPrefix(text, ":") && !strings.HasPrefix(text, ":eval "):
		m.status = "Unknown command; :help lists this slice's commands."
		return nil
	}
	if m.busy {
		m.status = "This task is busy. Ctrl+C aborts; your draft is kept."
		return nil
	}
	if m.attaching {
		m.status = "A file is being attached. Ctrl+C cancels it; your draft is kept."
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

// chooseModel moves the conversation to a model by its choice's name, with an
// effort or the model's own (ADR 41). The session.model record says what
// happened in the conversation, a switch or a choice waiting for the turn
// under way; a refusal is the status line.
func (m *model) chooseModel(args []string) tea.Cmd {
	if len(args) == 0 || len(args) > 2 {
		m.status = "usage: :model shows the choices; :model NAME [EFFORT] moves the conversation"
		return nil
	}
	name, effort := args[0], ""
	if len(args) == 2 {
		effort = args[1]
	}
	m.composer.Reset()
	b, id := m.backend, m.id
	return m.action("model", "", func(ctx context.Context) error {
		_, err := b.SetModel(ctx, id, name, effort)
		return err
	})
}

// chooseJudge makes a judge the task's, or none (ADR 47). The session.judge
// record says so in the conversation; a refusal is the status line. A
// judge is chosen while the task is idle, which the daemon and core hold.
func (m *model) chooseJudge(args []string) tea.Cmd {
	if len(args) != 1 {
		m.status = "usage: :judge shows the judges; :judge NAME asks that one, :judge none asks none"
		return nil
	}
	if m.busy {
		m.status = "Choose the judge when this turn ends."
		return nil
	}
	name := args[0]
	if name == "none" {
		name = ""
	}
	m.composer.Reset()
	b, id := m.backend, m.id
	return m.action("judge", "", func(ctx context.Context) error {
		_, err := b.SetJudge(ctx, id, name)
		return err
	})
}

// retrieval runs :earlier on, :earlier off and :index forget, which change
// what the session's project keeps for the model's searches (ADR 50); with
// no argument they are views (isInspectorCommand).
func (m *model) retrieval(fields []string) tea.Cmd {
	b, project := m.backend, m.project
	if project == "" {
		project = m.repo
	}
	var done string
	var work func(context.Context) error
	switch strings.Join(fields, " ") {
	case ":earlier on", ":earlier off":
		on := fields[1] == "on"
		done = "Earlier tasks in " + filepath.Base(project) + ": " + map[bool]string{true: "the model may search them from its next call.", false: "not searched from the model's next call."}[on]
		work = func(ctx context.Context) error { return b.SetEarlierTasks(ctx, project, on) }
	case ":index forget":
		done = "Forgot the repository index of " + filepath.Base(project) + ": the next search builds it again."
		work = func(ctx context.Context) error { return b.ForgetIndex(ctx, project) }
	default:
		m.status = "usage: :index shows the repository index, :index forget deletes it; :earlier shows whether earlier tasks are searched, :earlier on or :earlier off changes it"
		return nil
	}
	m.composer.Reset()
	m.operations++
	m.working = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		return actionResult{kind: "retrieval", err: work(ctx), done: done}
	}
}

// attachFile adds the file the rest of the line names. It runs beside the
// keyboard, not as an action, so an image's confirm can be answered while
// the attachment waits for it; the daemon and core bound that wait.
func (m *model) attachFile(arg string) tea.Cmd {
	path, err := attachPath(arg)
	switch {
	case err != nil:
		m.status = err.Error()
		return nil
	case m.busy:
		m.status = "Attach when the task is idle. Ctrl+C aborts busy work."
		return nil
	case m.attaching:
		m.status = "A file is already being attached."
		return nil
	}
	m.composer.Reset()
	m.attaching = true
	name := filepath.Base(path)
	m.status = "Attaching " + safe(name) + "…"
	b, id := m.backend, m.id
	return func() tea.Msg {
		_, err := b.AttachFile(m.ctx, id, path)
		return attachFileResult{name, err}
	}
}

// attachPath is the file :attach names, read as the shell it was typed in
// would read it: ~/ is the home directory, and a relative path is from the
// folder nodwyn was started in.
func attachPath(arg string) (string, error) {
	p := strings.TrimSpace(arg)
	if p == "" {
		return "", errors.New("usage: :attach PATH (the rest of the line is the path)")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// fileAttached ends an attachment. What was added is the attach record in
// the conversation; only a failure needs the status line.
func (m *model) fileAttached(msg attachFileResult) {
	m.attaching = false
	if msg.err != nil {
		m.status = "Not attached: " + safe(msg.name) + ": " + safe(msg.err.Error())
	} else {
		m.status = ""
	}
	m.refresh(false)
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
	if m.attaching && !m.busy {
		state = "attaching"
	}
	title := "nodwyn · " + state
	if m.branch != "" && m.active {
		title += " · " + safe(m.branch)
	}
	if g := m.gauge.title(); g != "" && m.active {
		title += " · " + g
	}
	var body string
	if !m.active {
		title = "nodwyn · choose a task"
		// A list row is two columns: what the session is about on the left,
		// which project and how long ago on the right. The right column is
		// reserved first, so a long first message is clipped instead of
		// pushing the identifying detail off the end.
		type row struct{ label, meta string }
		var rows []row
		for _, label := range m.newRows() {
			rows = append(rows, row{label: label})
		}
		for _, s := range m.listed() {
			label := s.FirstMessage
			if label == "" {
				label = "untitled"
			}
			switch {
			case s.Busy:
				label += " [running]"
			case s.InFlight:
				label += " [interrupted]"
			}
			meta := safe(repoName(s.Repo))
			if w := s.Worktree; w != nil {
				// A worktree's folder is named by the session; the
				// repository and the branch say which work it is.
				meta = safe(repoName(w.Repository) + " · " + w.Branch)
			}
			if age := since(s.LastEvent); age != "" {
				meta += " · " + age
			}
			rows = append(rows, row{safe(label), meta})
		}
		start := max(0, m.selected-max(1, m.height-8)+1)
		end := min(len(rows), start+max(1, m.height-8))
		var lines []string
		for i := start; i < end; i++ {
			mark, marked := "  ", false
			if i == m.selected {
				mark, marked = styleCursor.Render("›")+" ", true
			}
			lines = append(lines, mark+twoColumn(rows[i].label, rows[i].meta, m.width-2, marked))
		}
		hint := "↑/↓ PgUp/PgDn Home/End select · type to filter · Enter open"
		if m.filter != "" {
			hint = "filter: " + safe(m.filter) + " · Backspace edits · Esc clears · Enter open"
			if len(m.listed()) == 0 {
				hint = "No task matches. " + hint
			}
		}
		body = strings.Join(lines, "\n") + "\n\n" + hint
	} else {
		title += " · " + safe(m.id)
		if m.inspector != nil {
			title = "nodwyn · " + m.inspector.title + " · read-only"
		}
		if m.prompt != nil {
			title = "nodwyn · NEEDS YOUR ANSWER · " + m.prompt.title
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
		body = m.viewport.View() + "\n" + styleRule.Render(strings.Repeat("─", m.width)) + "\n" + editor
	}
	help := m.helpLine()
	titleStyle := styleTitle
	if m.prompt != nil {
		titleStyle = styleTitleAlert
	}
	content := bar(titleStyle, clip(title, m.width), m.width) + "\n" + body +
		"\n" + styleStatus.Render(clip(safe(m.status), m.width)) +
		"\n" + styleHelp.Render(clip(help, m.width))
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// helpLine is the footer: the keys that do something now, and above all the
// one that stops what is running or leaves, which a person looks for first.
func (m *model) helpLine() string {
	reconnect := ""
	if m.reconnect != nil {
		reconnect = " · Ctrl+R reconnect (quit detaches)"
	}
	switch {
	case !m.active:
		return "Ctrl+C or Ctrl+Q quit"
	case m.disconnected && m.reconnect != nil:
		return "Ctrl+R reconnects the daemon · Ctrl+C or Ctrl+Q quits (detaches)"
	case m.disconnected:
		return "Ctrl+C or Ctrl+Q quits"
	case m.prompt != nil || m.inspector != nil:
		return "PgUp/PgDn scroll · Ctrl+C abort/quit · Ctrl+Q quit" + reconnect
	case m.busy:
		return "Ctrl+C aborts the turn · Enter keeps your draft · Alt+Enter/Ctrl+J newline · Ctrl+Q quits"
	case m.attaching:
		return "Ctrl+C cancels the attachment · Alt+Enter/Ctrl+J newline · Ctrl+Q quits"
	}
	return "Enter sends · Alt+Enter/Ctrl+J newline · Ctrl+C clears the draft, then quits · Ctrl+Q quits"
}

func onOff(v bool) string {
	if v {
		return "shown"
	}
	return "hidden"
}
func (m *model) refresh(bottom bool) {
	m.layout()
	atBottom := m.viewport.AtBottom()
	var rows []string
	for _, line := range m.transcript {
		if !line.thinking || m.thinking {
			if line.tool != nil {
				rows = append(rows, line.tool.render(m.details))
			} else {
				rows = append(rows, line.text)
			}
		}
	}
	content := strings.Join(rows, "\n")
	if m.thinking && m.liveThinking != "" {
		content += "\nthinking> " + safe(m.liveThinking)
	}
	if m.liveText != "" {
		content += "\nnodwyn> " + safe(m.liveText)
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
