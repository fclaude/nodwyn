// The desk is what every page reads: the task list, which tasks need an
// answer and what they ask, and whether the models answer. It is refreshed
// from the daemon and updated by the content-free session.watch feed; it is
// never an authority for anything.
import { toastError } from "./dom.js";

export class Desk {
  // host is the linked host this desk shows (ADR 54), or "" for this
  // computer.
  constructor(conn, host = "") {
    this.conn = conn;
    this.host = host;
    this.sessions = new Map(); // id -> session.list entry, merged with watch state
    this.states = new Map(); // id -> session.watch state (by revision)
    this.pending = new Map(); // id -> pending prompt events (attention.list)
    this.firstSeen = new Map(); // prompt id -> when this page first saw it
    this.models = null;
    this.settings = null; // settings.read: the daemon's model configuration
    this.keychain = null; // keychain.status
    this.workspaces = []; // workspace.list: folders the person named
    this.forgotten = new Set(); // workspace.forgotten: folders left out of the suggestions, not out of task history
    this.last = ""; // workspace.active: the folder the daemon last saw picked for a new task
    this.newTask = undefined; // newtask.last: what was chosen last for a new task, once read
    this.outbox = []; // outbox.list: workflow answers waiting for the person to post them (ADR 53)
    this.listeners = new Set();
    this.version = 0;
    this.latest = null; // the newest refresh, which an older one waits for
    this.notifier = new Notifier(host);
    this.attentionTimer = 0;
    conn.onEvent((frame) => {
      if (frame.method === "session.event" && frame.params?.kind === "session.state") this.watch(frame.params.payload);
      if (frame.method === "session.event" && frame.params?.kind === "outbox.changed") this.refreshOutbox();
    });
    // Whether a model can be used depends on its key: the keychain unlocked,
    // a key stored, replaced or allowed, or removed. The pickers and Settings
    // say so as soon as it changes, not at the next check.
    conn.onResult((method) => {
      if (/^(keychain\.(create|unlock|passphrase)|credentials\.(add|replace|remove|bind))$/.test(method)) this.keysChanged();
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
    this.outbox = [];
    this.notifier.reset();
    this.changed("reset");
  }
  async start() {
    await this.refreshSetup();
    await this.refresh();
    await this.refreshOutbox();
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
  // reload is refresh for a caller with nothing to do about a failure (after
  // a rename, a delete, a state change): it says so once, where it would
  // otherwise leave the sidebar as it was without a word.
  reload() {
    return this.refresh().catch((error) => this.report("Could not read the task list", error, () => this.reload()));
  }
  // report shows a failed read as an error that stays. A lost connection says
  // so on its own, and is not reported once for every read it cut short.
  report(what, error, retry) {
    if (this.conn.live) toastError(`${what}: ${error.message}`, retry);
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
    // A task started or resumed in a forgotten folder, here, on another
    // device or in a terminal, brings the folder back (ADR 22): the daemon
    // has cleared its marker, so the list is read again.
    if ([...this.sessions.values()].some((s) => s.open && this.forgotten.has(this.project(s)))) await this.refreshForgotten();
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
  // refreshOutbox reads the answers waiting to be posted. A daemon without
  // workflows has none, and a read that fails leaves what was shown.
  async refreshOutbox() {
    if (!this.conn.has("outbox.list")) return;
    try {
      this.outbox = (await this.conn.call("outbox.list")) || [];
    } catch (error) {
      this.report("Could not read what waits to be posted", error, () => this.refreshOutbox());
      return;
    }
    this.changed("outbox");
  }
  scheduleAttention() {
    clearTimeout(this.attentionTimer);
    this.attentionTimer = setTimeout(() => this.refreshAttention().catch((error) => this.report("Could not read what waits for you", error)), 150);
  }
  // refreshSetup reads what setup shows. A daemon without a method has none
  // of it (conn.has says so: setup happens in config.toml); a read that
  // fails is an error, said, and what was read before stays.
  async refreshSetup() {
    const read = async (method) => {
      if (!this.conn.has(method)) return undefined;
      try {
        return await this.conn.call(method);
      } catch (error) {
        this.report(`Could not read ${method}`, error, () => this.refreshSetup());
        return undefined;
      }
    };
    const settings = await read("settings.read");
    if (settings !== undefined) this.settings = settings;
    const keychain = await read("keychain.status");
    if (keychain !== undefined) this.keychain = keychain;
    // The window's console is on a new loopback port every launch (ADR
    // 30), so the browser's own memory cannot carry the folder picked
    // last across an app restart; the daemon's can.
    const active = await read("workspace.active");
    if (active !== undefined) this.last = active.path || "";
    // So is what the composer chose last for a new task (ADR 41, amended
    // 2026-10-27): its host, model, judge and options.
    const newTask = await read("newtask.last");
    if (newTask !== undefined) this.newTask = newTask;
    await this.refreshWorkspaces();
    this.changed("setup");
  }
  async refreshWorkspaces() {
    if (!this.conn.has("workspace.list")) return;
    try {
      this.workspaces = (await this.conn.call("workspace.list")) || [];
    } catch (error) {
      this.workspaces = [];
      this.report("Could not read the workspaces", error, () => this.refreshWorkspaces());
    }
    await this.refreshForgotten();
    this.changed("workspaces");
  }
  // refreshForgotten reads the folders left out of the suggestions; an older
  // daemon has none.
  async refreshForgotten() {
    if (!this.conn.has("workspace.forgotten")) return;
    try {
      this.forgotten = new Set((await this.conn.call("workspace.forgotten")) || []);
    } catch (error) {
      this.forgotten = new Set();
      this.report("Could not read the forgotten folders", error, () => this.refreshForgotten());
    }
    if (this.forgotten.has(this.last)) this.last = "";
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
  async keysChanged() {
    await this.refreshSetup();
    await this.refreshModels();
  }
  // refreshModels reads whether the models can be used; fresh asks every
  // server again instead of taking what a check a few seconds ago found.
  // The minute's check (quiet) draws nothing again when it finds what the
  // last one did: Settings holds fields being typed into.
  async refreshModels(fresh = false, quiet = false) {
    if (!this.conn.has("models.status")) return;
    const before = JSON.stringify(this.models ?? null);
    try {
      this.models = await this.conn.call("models.status", fresh ? { fresh: true } : {});
    } catch (error) {
      this.models = null;
      // The minute's check says nothing: it would put the same error back
      // every minute after it was dismissed, and the sidebar shows the state.
      if (!quiet) this.report("Could not check the models", error);
    }
    if (!quiet || JSON.stringify(this.models ?? null) !== before) this.changed("models");
  }
  watch(state, notify = true) {
    const previous = this.states.get(state.id);
    if (previous && previous.revision >= state.revision) return;
    this.states.set(state.id, state);
    const item = this.sessions.get(state.id);
    if (!item) {
      // A session opened by another client: fetch the list once.
      if (state.open) this.reload();
      return;
    }
    Object.assign(item, { open: state.open, busy: state.busy, needs_answer: state.needs_answer });
    // A turn that ended or a task that closed has new activity, and perhaps
    // a title: the list, ordered by activity, is read again. So does a change
    // to a task the list has no message of yet: the list read as it started
    // can come before its first message is recorded, and one that asks at
    // once stayed "Untitled task" until its turn ended.
    const untitled = !item.title && !item.first_message;
    if (notify && ((previous && ((previous.busy && !state.busy) || (previous.open && !state.open))) || (untitled && state.open))) this.reload();
    if (!state.open || !state.needs_answer) this.pending.delete(state.id);
    if (!!previous?.needs_answer !== !!state.needs_answer || state.needs_answer) this.scheduleAttention();
    this.notifier.update(state, previous, notify, item);
    this.changed("state");
  }
  // at is the address of a page of this desk's host: this computer's as it
  // always was, a linked host's under #/host/NAME/.
  at(path) {
    return `#/${this.host ? `host/${encodeURIComponent(this.host)}/` : ""}${path}`;
  }
  // href is the address of a task of this desk's host.
  href(id, suffix = "") {
    return this.at(`task/${id}${suffix}`);
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
  // most recent first. A task in a worktree used its repository.
  folders() {
    const seen = new Set(this.workspaces.map((w) => w.path));
    const out = this.workspaces.map((w) => w.path);
    for (const s of this.all()) {
      const folder = this.project(s);
      if (folder && !seen.has(folder) && !this.forgotten.has(folder)) {
        seen.add(folder);
        out.push(folder);
      }
    }
    return out;
  }
  // project is the folder a task belongs to: the repository, for a task
  // working in a git worktree of its own (ADR 34), else its folder.
  project(item) {
    return item?.worktree?.repository || item?.repo || "";
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

// Notifications are generic: they never carry task text, arguments or
// diffs. The choice is kept in this browser, so a person who turned them on
// once is still told the next day that a task waits for an answer. A browser
// asks permission only when it is turned on; a stored "on" counts only while
// that permission is still granted, and is never asked for again unprompted.
// The app needs no permission, so there they are on unless turned off.
const notifyKey = "nodwyn-notify";
const storedNotify = () => {
  try {
    return localStorage.getItem(notifyKey);
  } catch {
    return null;
  }
};

// The choice is one for the page, whichever host's tasks a notifier tells
// of: "on", "off", or null when never chosen.
let notifyChoice = storedNotify();

class Notifier {
  // host is the linked host whose tasks it tells of, or "" for this
  // computer: a notice opens the task on its host.
  constructor(host = "") {
    this.host = host;
    this.open = new Map();
    this.onOpen = null;
    // The service worker's registration, once it is ready (ADR 52): a phone
    // shows a notice only through it, since Android's Chrome refuses
    // `new Notification` and an app on an iPhone's Home Screen has none.
    this.worker = null;
  }
  get native() {
    return typeof window.nodwynNotify === "function";
  }
  get available() {
    return this.native || (typeof Notification !== "undefined" && window.isSecureContext);
  }
  get choice() {
    return notifyChoice;
  }
  set choice(value) {
    notifyChoice = value;
  }
  get enabled() {
    if (this.native) return this.choice !== "off";
    return this.choice === "on" && this.available && Notification.permission === "granted";
  }
  reset() {
    for (const n of this.open.values()) n.close();
    this.open.clear();
  }
  async toggle() {
    if (this.enabled) this.choice = "off";
    else if (this.native) this.choice = "on";
    else {
      try {
        this.choice = (await Notification.requestPermission()) === "granted" ? "on" : "off";
      } catch {
        this.choice = "off";
      }
    }
    try {
      localStorage.setItem(notifyKey, this.choice);
    } catch {
      /* a browser that refuses storage still has the choice for this tab */
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
    const tag = `nodwyn-${this.host ? `${this.host}-` : ""}${state.id}`;
    if (this.worker) {
      // The task's id and host are data, never shown: a click on the notice
      // reaches the worker, which asks this page, or a new one, to open it.
      const worker = this.worker;
      worker.showNotification("nodwyn needs your answer", { body: "Open nodwyn to review what it asks.", tag, data: this.host ? { id: state.id, host: this.host } : { id: state.id } }).catch(() => {});
      this.open.set(state.id, { close: () => worker.getNotifications({ tag }).then((shown) => shown.forEach((n) => n.close()), () => {}) });
      return;
    }
    try {
      const n = new Notification("nodwyn needs your answer", { body: "Open nodwyn to review what it asks.", tag });
      n.onclick = () => {
        window.focus();
        this.onOpen?.(state.id, this.host);
        n.close();
      };
      this.open.set(state.id, n);
    } catch {
      this.choice = "off";
    }
  }
}
