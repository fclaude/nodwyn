// The desk is what every page reads: the task list, which tasks need an
// answer and what they ask, and whether the models answer. It is refreshed
// from the daemon and updated by the content-free session.watch feed; it is
// never an authority for anything.

export class Desk {
  constructor(conn) {
    this.conn = conn;
    this.sessions = new Map(); // id -> session.list entry, merged with watch state
    this.states = new Map(); // id -> session.watch state (by revision)
    this.pending = new Map(); // id -> pending prompt events (attention.list)
    this.firstSeen = new Map(); // prompt id -> when this page first saw it
    this.models = null;
    this.settings = null; // settings.read: the daemon's model configuration
    this.keychain = null; // keychain.status
    this.workspaces = []; // workspace.list: folders the person named
    this.listeners = new Set();
    this.version = 0;
    this.latest = null; // the newest refresh, which an older one waits for
    this.notifier = new Notifier();
    this.attentionTimer = 0;
    conn.onEvent((frame) => {
      if (frame.method === "session.event" && frame.params?.kind === "session.state") this.watch(frame.params.payload);
    });
  }
  onChange(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  changed(what) {
    for (const fn of this.listeners) fn(what);
  }
  reset() {
    this.sessions.clear();
    this.states.clear();
    this.pending.clear();
    this.notifier.reset();
    this.changed("reset");
  }
  async start() {
    await this.refreshSetup();
    await this.refresh();
    if (this.conn.has("session.watch")) {
      const states = await this.conn.call("session.watch");
      for (const state of states || []) this.watch(state, false);
    }
    // An unreachable model answers slowly, so the first page does not wait
    // for the models; this says when they have answered.
    this.modelsChecked = this.refreshModels();
  }
  refresh() {
    this.latest = this.load(++this.version);
    return this.latest;
  }
  // load reads the list for refresh. When a newer refresh has started
  // meanwhile, its list wins, and this one waits for it: whoever awaited a
  // refresh then finds at least what the list held when they asked. A task
  // just started, whose state notifications each start a refresh, was
  // otherwise "No such task" when its page awaited one that was overtaken.
  async load(version) {
    const list = (await this.conn.call("session.list")) || [];
    if (version !== this.version) return this.latest;
    this.sessions.clear();
    for (const item of list) {
      const state = this.states.get(item.id);
      if (state) Object.assign(item, { open: state.open, busy: state.busy, needs_answer: state.needs_answer });
      this.sessions.set(item.id, item);
    }
    await this.refreshAttention();
    this.changed("list");
  }
  async refreshAttention() {
    if (!this.conn.has("attention.list")) return;
    const items = (await this.conn.call("attention.list")) || [];
    this.pending.clear();
    for (const item of items) {
      this.pending.set(item.id, item.pending || []);
      for (const ev of item.pending || []) if (!this.firstSeen.has(ev.payload?.ID)) this.firstSeen.set(ev.payload?.ID, Date.now());
    }
    this.changed("attention");
  }
  scheduleAttention() {
    clearTimeout(this.attentionTimer);
    this.attentionTimer = setTimeout(() => this.refreshAttention().catch(() => {}), 150);
  }
  async refreshSetup() {
    try {
      if (this.conn.has("settings.read")) this.settings = await this.conn.call("settings.read");
      if (this.conn.has("keychain.status")) this.keychain = await this.conn.call("keychain.status");
      await this.refreshWorkspaces();
    } catch {
      /* an older daemon: setup happens in config.toml */
    }
    this.changed("setup");
  }
  async refreshWorkspaces() {
    if (!this.conn.has("workspace.list")) return;
    try {
      this.workspaces = (await this.conn.call("workspace.list")) || [];
    } catch {
      this.workspaces = [];
    }
    this.changed("workspaces");
  }
  // workspace returns the named workspace at path, if any.
  workspace(path) {
    return this.workspaces.find((w) => w.path === path) || null;
  }
  // folderName is what a folder is called: its workspace name, or its last
  // path element.
  folderName(path) {
    const ws = this.workspace(path);
    if (ws) return ws.name;
    const parts = String(path || "").replace(/\/+$/, "").split("/");
    return parts[parts.length - 1] || path || "—";
  }
  // configured says whether a model is set up, when the daemon can say.
  get configured() {
    const c = this.settings?.control;
    return !c || !!(c.Model || c.model);
  }
  // keysLocked: the model needs a key from a keychain that is still locked,
  // so the daemon refuses to open tasks until this console unlocks it.
  get keysLocked() {
    const c = this.settings?.control;
    return !!(c && (c.Credential || c.credential) && this.keychain?.exists && !this.keychain.unlocked && this.conn.has("keychain.unlock"));
  }
  async refreshModels() {
    if (!this.conn.has("models.status")) return;
    try {
      this.models = await this.conn.call("models.status");
    } catch {
      this.models = null;
    }
    this.changed("models");
  }
  watch(state, notify = true) {
    const previous = this.states.get(state.id);
    if (previous && previous.revision >= state.revision) return;
    this.states.set(state.id, state);
    const item = this.sessions.get(state.id);
    if (!item) {
      // A session opened by another client: fetch the list once.
      if (state.open) this.refresh().catch(() => {});
      return;
    }
    Object.assign(item, { open: state.open, busy: state.busy, needs_answer: state.needs_answer });
    // A turn that ended or a task that closed has new activity, and perhaps
    // a title: the list, ordered by activity, is read again.
    if (notify && previous && ((previous.busy && !state.busy) || (previous.open && !state.open))) this.refresh().catch(() => {});
    if (!state.open || !state.needs_answer) this.pending.delete(state.id);
    if (!!previous?.needs_answer !== !!state.needs_answer || state.needs_answer) this.scheduleAttention();
    this.notifier.update(state, previous, notify, item);
    this.changed("state");
  }
  // Lists, newest activity first.
  all() {
    return [...this.sessions.values()].sort((a, b) => new Date(b.last_event) - new Date(a.last_event));
  }
  needsYou() {
    return this.all().filter((s) => s.open && s.needs_answer);
  }
  running() {
    return this.all().filter((s) => s.open && !s.needs_answer && (s.busy || s.run?.status === "running"));
  }
  recent() {
    return this.all().filter((s) => !s.archived && !(s.open && (s.needs_answer || s.busy || s.run?.status === "running")));
  }
  // archived: the tasks a person put away (ADR 39).
  archived() {
    return this.all().filter((s) => s.archived);
  }
  // Folders to work in: named workspaces first, then folders tasks used,
  // most recent first.
  folders() {
    const seen = new Set(this.workspaces.map((w) => w.path));
    const out = this.workspaces.map((w) => w.path);
    for (const s of this.all()) if (s.repo && !seen.has(s.repo)) {
      seen.add(s.repo);
      out.push(s.repo);
    }
    return out;
  }
  // home is the user's home directory as the task folders suggest it, so
  // paths can be shown as ~/…; the protocol does not report it.
  get home() {
    for (const folder of this.folders()) {
      const m = /^(\/Users\/[^/]+|\/home\/[^/]+)(\/|$)/.exec(folder);
      if (m) return m[1];
    }
    return "";
  }
  control() {
    return this.models?.find((m) => m.name === "control") || null;
  }
}

// Desktop notifications are opt-in and generic: they never carry task text,
// arguments or diffs. The choice lasts for this tab.
class Notifier {
  constructor() {
    this.enabled = false;
    this.open = new Map();
    this.onOpen = null;
  }
  get native() {
    return typeof window.nodwynNotify === "function";
  }
  get available() {
    return this.native || (typeof Notification !== "undefined" && window.isSecureContext);
  }
  reset() {
    for (const n of this.open.values()) n.close();
    this.open.clear();
  }
  async toggle() {
    if (this.enabled) this.enabled = false;
    else if (this.native) this.enabled = true;
    else {
      try {
        this.enabled = (await Notification.requestPermission()) === "granted";
      } catch {
        this.enabled = false;
      }
    }
    if (!this.enabled) this.reset();
    return this.enabled;
  }
  update(state, previous, notify) {
    if (!state.open || !state.needs_answer) {
      this.open.get(state.id)?.close();
      this.open.delete(state.id);
      return;
    }
    if (!notify || previous?.needs_answer || !this.enabled) return;
    if (this.native) {
      if (document.hasFocus()) return;
      window.nodwynNotify("nodwyn needs your answer", "Open nodwyn to review what it asks.").catch(() => {});
      return;
    }
    if (!document.hidden || Notification.permission !== "granted") return;
    try {
      const n = new Notification("nodwyn needs your answer", { body: "Open nodwyn to review what it asks.", tag: `nodwyn-${state.id}` });
      n.onclick = () => {
        window.focus();
        this.onOpen?.(state.id);
        n.close();
      };
      this.open.set(state.id, n);
    } catch {
      this.enabled = false;
    }
  }
}
