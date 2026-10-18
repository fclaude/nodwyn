import { manageSchedule } from "./schedule.js";
// The task view: one session's conversation with its approval inline, and
// beside it, in tabs, the plan, this turn's budget, the changes, the
// project notes and the workspace. Every figure comes from the log or from
// session.info; a panel with no source says so instead of guessing.
import { h, icon, repoName, shortPath, clock, minutes, ago, toast, toastError, basename, sizeOf } from "./dom.js";
import { Transcript, planSteps, stepTitle, usd, contextGauge, judgeTally, spanWords } from "./transcript.js";
import { askCard, shortcut, summary, parseDiff, splitDiff, diffView } from "./prompts.js";
import { Attachments, attachmentChip } from "./attach.js";
import { renderInspector } from "./inspectors.js";
import { policyChoices, renderPolicyEditor } from "./policy.js";
import { keychainForm } from "./setup.js";
import { ModelPicker, choiceNamed, effortLabel, judgesOf } from "./modelpicker.js";

const drafts = new Map(); // per task, this tab only
const openSteps = new Map(); // per task, the plan steps the reader unfolded, this tab only
const views = new Set(); // every view not disposed, for keepDraft

// keepDraft puts a message that was not sent into task id's reply box: at
// once in a view that has the task, else when one opens it.
export function keepDraft(id, text) {
  drafts.set(id, text);
  for (const view of views) {
    if (view.item?.id !== id || !view.el) continue;
    view.el.reply.value = text;
    view.controls();
  }
}

export class TaskView {
  constructor(conn, desk, navigate) {
    this.conn = conn;
    this.desk = desk;
    this.navigate = navigate;
    this.item = null;
    this.version = 0;
    this.loading = false;
    this.subscribed = false;
    this.queued = [];
    this.seen = new Map();
    this.historyCursor = 0;
    this.historyThrough = 0;
    this.olderLoading = false;
    this.prompts = new Map(); // live prompt events by ID, in arrival order
    this.resolved = new Set();
    this.info = null;
    this.totalSnapshot = null;
    this.infoVersion = 0; // only the newest session.info read may redraw this task
    this.approveAllPending = null;
    this.limitsPending = undefined; // a limits choice sent and not yet answered
    this.notes = null;
    this.index = null; // index.status: the project's repository index, or null when unknown
    this.asideScroll = new Map();
    // The project notes being edited, or null when they are not. Every
    // event redraws the side panel, so the field is drawn from here and
    // what was typed outlives the redraw.
    this.notesDraft = null;
    // A worktree review keeps its diff and drafts here, so closing the centre
    // view or redrawing it for an event does not discard the reader's work.
    this.review = null;
    // The Changes panel's open History lists, by path: what versions.list
    // answered, or an empty record while it has not. Every event redraws the
    // panel, so each list is drawn from here and outlives the redraw.
    this.histories = new Map();
    this.changeOpen = new Set();
    this.comparisons = new Map();
    this.inspecting = null;
    this.operations = new Map(); // guarded operation id -> handler
    this.early = new Map(); // results that arrived before their id
    this.busy = false;
    this.acting = false;
    this.renaming = false; // the heading is a field for the task's name
    this.deleting = false; // the heading asks whether to delete the task
    this.menuOpen = false; // the heading's menu of rarer actions is open
    this.renameBox = null;
    this.uploading = false; // attachments on their way, an image perhaps waiting for its confirm
    this.changing = false; // a model or effort is being changed (session.model.set)
    this.judging = false; // the judge is being changed (session.judge.set)
    this.modelChanged = false; // a choice made between turns, which the next message is the first to use
    this.logModel = null; // the model and effort the log last names, for a task that is not open
    this.timer = 0;
    this.streams = [];
    this.streamFrame = 0;
    this.failed = false; // the last load failed: the view is not to be kept
    this.scroll = null; // where the conversation was when another page was shown
    this.root = h("div", { class: "task" });
    this.unlisten = conn.onEvent((frame) => this.onFrame(frame));
    views.add(this);
    this.onKey = (event) => {
      if (!this.root.isConnected || this.inspecting || !this.item || this.loading) return;
      shortcut(this.el?.transcript.querySelector(".ask-card"), event);
    };
    document.addEventListener("keydown", this.onKey);
    this.onAway = (event) => {
      if (!this.menuOpen || event.target.closest?.(".more")) return;
      this.menuOpen = false;
      this.root.querySelector(".more")?.removeAttribute("open");
    };
    document.addEventListener("click", this.onAway);
  }

  // ---------- lifecycle ----------
  // open reads task id's conversation. The load in progress is kept, so
  // whoever started it, or a list refresh that reopened the view, can be
  // waited for (resume).
  open(id) {
    this.opening = this.load(id);
    return this.opening;
  }
  async load(id) {
    this.clearStreams();
    const version = ++this.version;
    clearTimeout(this.totalsTimer);
    this.totalsTimer = 0;
    delete this.root.dataset.loaded;
    const previous = this.item;
    if (previous) drafts.set(previous.id, this.el?.reply.value || "");
    if (previous?.open && previous.id !== id) this.conn.call("session.unsubscribe", { id: previous.id }).catch(() => {});
    let item = this.desk.sessions.get(id);
    if (!item) {
      await this.desk.reload();
      if (version !== this.version) return;
      item = this.desk.sessions.get(id);
    }
    if (!item) {
      this.root.replaceChildren(h("div", { class: "page" }, h("div", { class: "page-col" }, h("h1", {}, "No such task"), h("p", { class: "lede" }, "It may have been started by a daemon that is no longer running."), h("div", {}, h("a", { class: "btn", href: "#/" }, "Home")))));
      return;
    }
    this.item = item;
    this.loading = true;
    this.subscribed = false;
    this.asideScroll.clear();
    this.failed = false;
    this.scroll = null;
    this.queued = [];
    this.seen = new Map();
    this.historyCursor = 0;
    this.historyThrough = 0;
    this.olderLoading = false;
    this.prompts = new Map();
    this.resolved = new Set();
    this.info = null;
    this.totalSnapshot = null;
    this.infoVersion++;
    this.switching = false;
    this.approveAllPending = null;
    this.limitsPending = undefined;
    this.notes = null;
    this.index = null;
    // What was written for a review or for the notes outlives a reload of the
    // same task (a failed send or answer reloads it): only another task starts
    // without it, as the reply box's draft does.
    if (previous?.id !== id) {
      this.notesDraft = null;
      this.review = null;
    } else if (this.review) Object.assign(this.review, { loading: false, sending: false, refresh: true });
    this.histories.clear();
    this.changeOpen.clear();
    this.comparisons.clear();
    this.inspecting = null;
    this.renaming = false;
    this.deleting = false;
    this.changing = false;
    this.modelChanged = false;
    this.logModel = null;
    this.operations.clear();
    this.early.clear();
    this.busy = !!item.busy;
    this.build();
    this.el.reply.value = drafts.get(id) || "";
    this.header();
    this.aside();
    this.controls();
    // A long log takes a while to read: the conversation says so until its
    // first event is drawn (render removes a placeholder).
    this.loadingNote = h("p", { class: "hint", role: "status", dataset: { placeholder: "1" } }, "Loading the conversation…");
    this.el.transcript.prepend(this.loadingNote);
    try {
      let page,
        pending = [],
        busy = item.busy;
      if (item.open) {
        const snapshot = await this.conn.call("session.subscribe", { id, after: 0, limit: 500, ...(this.conn.has("log.runs") ? { latest_run: true } : {}) });
        if (version !== this.version) return;
        this.subscribed = true;
        page = snapshot.page;
        pending = snapshot.pending || [];
        busy = snapshot.busy;
      } else page = await this.conn.call("log.read", { id, after: 0, limit: 500, ...(this.conn.has("log.runs") ? { latest_run: true } : {}) });
      if (version !== this.version) return;
      this.historyCursor = page.run?.seq || 0;
      this.historyThrough = page.through;
      // The inherited setup feeds the current model and gauges without
      // inserting old choices into this run's conversation.
      for (const event of page.context || []) {
        this.seen.set(event.seq, event);
        if (["session.start", "session.resume", "session.model"].includes(event.kind)) this.noteModel(event.kind, event.payload || {});
      }
      if (page.earlier) this.historyButton();
      const through = page.through;
      let truncated = false;
      for (;;) {
        if (version !== this.version) return;
        for (const event of page.events) this.render(event);
        truncated ||= page.truncated;
        if (!page.more) break;
        page = await this.conn.call("log.read", { id, after: page.next, through, limit: 500 });
      }
      if (version !== this.version) return;
      this.truncated = truncated;
      this.busy = busy;
      this.loading = false;
      this.loadingNote.remove();
      for (const event of pending) this.render(event);
      for (const event of this.queued) this.render(event);
      this.queued = [];
      const details = Promise.all([item.open ? this.loadInfo() : null, this.loadNotes(), this.loadIndex(), this.loadTotals()]);
      if (this.transcript.empty) this.el.transcript.prepend(h("p", { class: "hint", dataset: { placeholder: "1" } }, item.open ? "Nothing yet. Say what you want done." : "This task has no conversation."));
      this.el.transcript.scrollTop = this.el.transcript.scrollHeight;
      this.refreshAll();
      this.focusReply();
      // The browser checks wait for this mark before they look at a task:
      // its conversation, its details and its notes are drawn.
      await details;
      if (version === this.version) {
        if (!this.subscribed && this.desk.sessions.get(id)?.open && this.conn.has("session.subscribe")) return this.open(id);
        this.root.dataset.loaded = "";
      }
    } catch (error) {
      if (version !== this.version) return;
      // A failed load must not enable sending against uncertain state, and
      // says so where the conversation would be, with a way to try again.
      this.loading = true;
      this.failed = true;
      this.queued = [];
      this.loadingNote.remove();
      this.el.transcript.prepend(
        h(
          "div",
          { class: "callout load-failed", role: "alert" },
          h("div", { class: "stack-sm" }, h("strong", {}, "This conversation could not be loaded."), h("span", {}, error.message)),
          h("button", { type: "button", class: "btn small", onclick: () => this.open(id) }, "Retry"),
        ),
      );
      this.header();
      this.controls();
    }
  }
  historyButton() {
    const button = h("button", { type: "button", class: "btn small earlier-runs", onclick: () => this.loadEarlierRuns() }, "Earlier runs");
    this.el.transcript.prepend(button);
    this.historyControl = button;
  }
  // Earlier runs start as previews. Opening one reads only its fixed range,
  // into its own renderer, so historical choices cannot change the live task.
  async loadEarlierRuns() {
    if (this.olderLoading || !this.item) return;
    const version = this.version, id = this.item.id;
    this.olderLoading = true;
    const button = this.historyControl;
    button.disabled = true;
    button.textContent = "Loading earlier runs…";
    try {
      const page = await this.conn.call("log.runs", { id, before: this.historyCursor, through: this.historyThrough, limit: 5 });
      if (version !== this.version) return;
      const scroll = this.el.transcript;
      const anchor = button.nextElementSibling;
      const top = anchor?.getBoundingClientRect().top;
      const fragment = document.createDocumentFragment();
      for (const run of [...page.runs].reverse()) fragment.append(this.earlierRun(run, version, id));
      button.after(fragment);
      this.historyCursor = page.before;
      button.hidden = !page.more;
      if (anchor && top !== undefined) scroll.scrollTop += anchor.getBoundingClientRect().top - top;
    } catch (error) {
      if (version === this.version) toastError(`Could not read earlier runs: ${error.message}`);
    } finally {
      if (version === this.version) {
        this.olderLoading = false;
        button.disabled = false;
        button.textContent = "Earlier runs";
      }
    }
  }
  earlierRun(run, version, id) {
    const body = h("div", { class: "run-transcript" });
    const state = h("p", { class: "hint" });
    const retry = h("button", { type: "button", class: "btn small", hidden: true, onclick: () => load() }, "Retry loading this run");
    const preview = h("p", { class: "run-preview" }, run.reply || "No reply recorded.");
    const detail = h("details", { class: "earlier-run", dataset: { run: String(run.seq) } },
      h("summary", {}, h("span", {}, `Run · ${new Date(run.ts).toLocaleString()}`), preview), state, retry, body);
    let loading = false, loaded = false;
    const load = async () => {
      if (loading || loaded || version !== this.version) return;
      loading = true; retry.hidden = true; state.textContent = "Loading this run…";
      const events = [];
      try {
        let after = run.seq - 1;
        for (;;) {
          const page = await this.conn.call("log.read", { id, after, through: run.through, limit: 500 });
          if (version !== this.version) return;
          events.push(...page.events);
          if (!page.more) break;
          if (page.next <= after) throw new Error("The history page did not advance.");
          after = page.next;
        }
        const transcript = new Transcript(body);
        for (const event of events) transcript.render(event);
        transcript.end();
        loaded = true; detail.dataset.loaded = ""; state.textContent = "";
      } catch (error) {
        if (version === this.version) {
          state.textContent = `Could not read this run: ${error.message}`;
          retry.hidden = false;
        }
      } finally { loading = false; }
    };
    detail.addEventListener("toggle", () => { if (detail.open) load(); });
    return detail;
  }
  // leave stops showing the task; with unsubscribe false it leaves the
  // subscription alone, as on a new connection, which never had it.
  leave(unsubscribe = true) {
    this.clearStreams();
    if (this.item) drafts.set(this.item.id, this.el?.reply.value || "");
    if (unsubscribe && this.item?.open && this.conn.live) this.conn.call("session.unsubscribe", { id: this.item.id }).catch(() => {});
    this.version++;
    clearTimeout(this.totalsTimer);
    this.totalsTimer = 0;
    this.item = null;
    clearInterval(this.timer);
  }
  // dispose ends the view for good: it stops listening, and leaves its task.
  dispose(unsubscribe = true) {
    this.leave(unsubscribe);
    views.delete(this);
    this.unlisten();
    document.removeEventListener("keydown", this.onKey);
    document.removeEventListener("click", this.onAway);
  }
  // keeps says whether this view can be shown again for task id as it is,
  // without reading the conversation again: the task is open and still
  // subscribed here, so every event since reached the view, and nothing
  // failed to load. Provisional fragments may still be waiting to be drawn.
  keeps(id) {
    return this.item?.id === id && !!this.item.open && this.subscribed && !this.failed && this.conn.live;
  }
  // park takes the view off screen and keeps its subscription. Recorded
  // events keep drawing; provisional fragments wait until they are needed.
  park() {
    if (!this.el || !this.item) return;
    this.flushStreams();
    drafts.set(this.item.id, this.el.reply.value || "");
    const t = this.el.transcript;
    this.scroll = { top: t.scrollTop, end: t.scrollHeight - t.scrollTop - t.clientHeight < 120 };
    clearInterval(this.timer);
    // A key being typed is never kept off screen: switching tasks clears it,
    // as it always did (browser.md).
    for (const input of this.root.querySelectorAll("input[type=password]")) input.value = "";
  }
  // show puts a kept view back on screen with the list's newest word on the
  // task, where its conversation was, or at its end if it followed it.
  show(fresh) {
    if (fresh && this.item) Object.assign(this.item, { open: fresh.open, run: fresh.run, title: fresh.title, archived: fresh.archived, worktree: fresh.worktree, schedule: fresh.schedule });
    this.flushStreams();
    this.refreshAll();
    // Settings may have changed what the task's details say while it was
    // off screen (Don't ask offered or taken away): read them again.
    if (this.item?.open && !this.loading) this.loadInfo();
    const t = this.el.transcript;
    t.scrollTop = !this.scroll || this.scroll.end ? t.scrollHeight : this.scroll.top;
    this.scroll = null;
    this.focusReply();
  }
  // focusReply puts the focus in the message box once the switch is drawn,
  // on the next frame so the box is in the new layout: a word typed now
  // goes to the task's reply. A load that failed, or a view whose box is
  // hidden or disabled, has nothing to answer in yet. A question that is
  // waiting is answered from its card, not the box: its keyboard answers
  // (y, n, …) would be typed as text if the focus were in a field, so the
  // box stays out of the way while one is up.
  focusReply() {
    if (this.failed || this.el?.form.hidden || this.el?.reply.disabled || this.activePrompt()) return;
    setTimeout(() => this.el?.reply?.focus(), 0);
  }
  // Totals come from the durable log's index, even for a closed task. A
  // response for an older task cannot overwrite the one now being shown.
  async loadTotals() {
    if (!this.item || !this.conn.has("session.totals")) return;
    const version = this.version;
    if (this.totalsRead?.version === version) {
      this.totalsRead.dirty = true;
      return this.totalsRead.promise;
    }
    const read = { version, dirty: false };
    this.totalsRead = read;
    read.promise = (async () => {
      try {
        do {
          read.dirty = false;
          const totals = await this.conn.call("session.totals", { id: this.item.id });
          if (version !== this.version) return;
          this.totalSnapshot = totals;
          this.aside();
        } while (read.dirty);
      } catch (error) {
        if (version === this.version) {
          this.totalSnapshot = null;
          toastError(`Could not read totals: ${error.message}`);
          this.aside();
        }
      } finally {
        if (this.totalsRead === read) this.totalsRead = null;
      }
    })();
    return read.promise;
  }
  scheduleTotals() {
    if (!this.conn.has("session.totals") || this.totalsTimer) return;
    this.totalsTimer = setTimeout(() => {
      this.totalsTimer = 0;
      this.loadTotals();
    }, 40);
  }
  async loadInfo() {
    const version = this.version;
    const infoVersion = ++this.infoVersion;
    try {
      const info = await this.conn.call("session.info", { id: this.item.id });
      if (version === this.version && infoVersion === this.infoVersion) {
        this.info = info;
        this.aside();
        this.header();
        this.controls();
      }
    } catch {
      /* a closed session has no current details */
    }
  }
  // loadIndex reads what the project's repository index holds (ADR 50),
  // nodwyn's own data like the notes: the Workspace panel says so.
  async loadIndex() {
    if (!this.conn.has("index.status") || !this.item?.repo) return;
    const version = this.version;
    try {
      const index = await this.conn.call("index.status", { repo: this.desk.project(this.item) });
      if (version === this.version) {
        this.index = index;
        this.aside();
      }
    } catch {
      this.index = null;
    }
  }
  async loadNotes() {
    if (!this.conn.has("notes.read") || !this.item?.repo) return;
    const version = this.version;
    try {
      // Notes are the project's: a task in a worktree has its repository's.
      const notes = await this.conn.call("notes.read", { repo: this.desk.project(this.item) });
      if (version === this.version) {
        this.notes = notes.text;
        this.aside();
      }
    } catch {
      this.notes = null;
    }
  }

  // ---------- events ----------
  onFrame(frame) {
    if (!this.item) return;
    if ((frame.method === "session.closed" || (frame.method === "session.event" && frame.params?.kind === "session.closed")) && frame.params?.id === this.item.id) {
      this.flushStreams();
      this.item.open = false;
      this.subscribed = false;
      this.busy = false;
      this.prompts.clear();
      this.scheduleTotals();
      this.refreshAll();
      if (this.root.isConnected) toast("This task was closed. Its conversation is kept.");
      return;
    }
    if (frame.method !== "session.event" || frame.params?.id !== this.item.id || frame.params.kind === "session.state") return;
    if (this.loading) this.queued.push(frame.params);
    else {
      if (frame.params.kind === "stream.text" || frame.params.kind === "stream.thinking") {
        this.queueStream(frame.params);
        return;
      }
      // A recorded reply or a question must follow every preceding fragment,
      // even when a background tab has stopped receiving animation frames.
      this.flushStreams();
      this.render(frame.params);
      this.refreshAll();
    }
  }
  // Fragments change only the transcript. Joining them before the next
  // frame avoids rebuilding the heading and every panel for each token.
  queueStream(event) {
    let last = this.streams.at(-1);
    if (!last || last.kind !== event.kind) {
      last = { kind: event.kind, chunks: [] };
      this.streams.push(last);
    }
    last.chunks.push(event.payload?.Text ?? event.payload?.text ?? "");
    // A kept view is caught up when shown, or before the next non-stream
    // event; drawing its provisional text while detached serves no reader.
    if (!this.streamFrame && this.root.isConnected) this.streamFrame = requestAnimationFrame(() => this.flushStreams());
  }
  clearStreams() {
    cancelAnimationFrame(this.streamFrame);
    this.streamFrame = 0;
    this.streams = [];
  }
  flushStreams() {
    const streams = this.streams;
    this.clearStreams();
    if (!streams.length) return;
    const view = this.el.transcript;
    const follow = this.root.isConnected && view.scrollHeight - view.scrollTop - view.clientHeight < 120;
    view.querySelector(":scope > [data-placeholder]")?.remove();
    for (const stream of streams) this.transcript.render({ kind: stream.kind, payload: { Text: stream.chunks.join("") } });
    const placed = this.placePrompt();
    if (follow && !placed) view.scrollTop = view.scrollHeight;
  }
  render(event) {
    if (event.seq && this.seen.has(event.seq)) return;
    // Live notifications carry no timestamp; the arrival time stands in.
    if (event.seq && !event.ts) event.ts = new Date().toISOString();
    if (event.seq) {
      this.seen.set(event.seq, event);
      if (!this.loading) this.scheduleTotals();
    }
    const p = event.payload || {};
    const view = this.el.transcript;
    const follow = view.scrollHeight - view.scrollTop - view.clientHeight < 120;
    view.querySelector(":scope > [data-placeholder]")?.remove();
    if (p.prompt_id && ["guard.decision", "turn.checkpoint", "cred.register", "run.checkpoint", "setting.change"].includes(event.kind)) this.resolve(p.prompt_id);
    // "Don't ask" switched, here or on another screen: show it as it is.
    if (event.kind === "approval.change" && p.kind === "all" && !this.loading && this.item?.open) this.loadInfo();
    // A name given here or on another screen: the list and the heading follow.
    if (event.kind === "session.title" && !this.loading) this.desk.reload();
    // A model or effort chosen here or on another screen, or applied as a
    // response ended: the picker shows what the conversation has now.
    if ((event.kind === "schedule.run" || event.kind === "session.schedule" || event.kind === "session.model" || event.kind === "session.judge") && !this.loading && this.item?.open) this.loadInfo();
    // Limits turned off or on here or on another screen: the tick, the
    // heading and the budget follow.
    if (event.kind === "session.limits" && !this.loading && this.item?.open) this.loadInfo();
    // The repository's index built or refreshed: the Workspace tab says so.
    if (event.kind === "index.update" && !this.loading) this.loadIndex();
    if (event.kind === "session.start" || event.kind === "session.resume" || event.kind === "session.model") this.noteModel(event.kind, p);
    if (event.kind === "user.msg") this.modelChanged = false;
    switch (event.kind) {
      case "prompt.confirm":
      case "prompt.secret":
      case "prompt.checkpoint":
      case "prompt.choice":
        // A request is not a turn: an image's confirm comes before any, and
        // no idle follows its answer. Only a status says a turn runs, or the
        // page stayed Working with nothing to stop.
        if (!this.resolved.has(p.ID)) {
          this.prompts.set(p.ID, event);
          if (!this.desk.firstSeen.has(p.ID)) this.desk.firstSeen.set(p.ID, Date.now());
        }
        break;
      case "status": {
        const state = p.State ?? p.state;
        if (state === "idle") {
          this.busy = false;
          this.transcript.end();
          for (const id of this.prompts.keys()) this.resolved.add(id);
          this.prompts.clear();
          // A choice made while the response ran has applied, or said why it
          // could not: the state has it now.
          if (!this.loading && this.item?.open) this.loadInfo();
          // What the turn changed is in the review once it has been
          // opened, without asking for a refresh.
          if (this.review?.files && !this.loading && this.item?.open) this.loadReview();
        } else this.busy = true;
        this.statusDetail = p.Detail ?? p.detail ?? "";
        break;
      }
      case "user.msg":
      case "eval.req":
        if (!this.loading) this.busy = true;
        this.transcript.render(event);
        break;
      case "policy.result":
      case "inspection.result":
        this.operationResult(p);
        break;
      case "prompt.closed":
        this.resolve(p.ID);
        break;
      default:
        this.transcript.render(event);
    }
    const placed = this.placePrompt();
    if (follow && !placed) view.scrollTop = view.scrollHeight;
  }
  resolve(id) {
    this.resolved.add(id);
    this.prompts.delete(id);
  }
  activePrompt() {
    for (const event of this.prompts.values()) if (!this.resolved.has(event.payload.ID)) return event;
    return null;
  }
  // The pending prompt is drawn at the end of the conversation, where the
  // work stopped to ask.
  placePrompt() {
    const view = this.el.transcript;
    const active = this.activePrompt();
    this.transcript.waiting(!!active);
    const current = view.querySelector(":scope > .ask-card");
    if (current && current.dataset.prompt === active?.payload.ID) {
      view.append(current);
      return false;
    }
    current?.remove();
    if (!active) return false;
    const id = active.payload.ID;
    const card = askCard(active, {
      methods: this.conn.methods,
      readOnly: !this.conn.has("session.answer"),
      since: this.desk.firstSeen.get(id),
      who: this.who(),
      reply: (method, params) => this.answer(id, method, params),
    });
    view.append(card);
    // On a phone the card opens at its title; elsewhere the conversation
    // follows to its end, where the card is.
    if (window.matchMedia("(max-width: 760px)").matches) requestAnimationFrame(() => card.scrollIntoView({ block: "start" }));
    else view.scrollTop = view.scrollHeight;
    return true;
  }
  who() {
    const peer = this.conn.hello?.peer || "";
    const via = / via (\S+)/.exec(peer);
    return via ? via[1].replace(/^web:/, "") : "";
  }
  async answer(promptID, method, params) {
    if (this.acting || !this.item) return;
    const item = this.item,
      version = this.version;
    this.acting = true;
    this.controls();
    try {
      await this.conn.call(method, { id: item.id, prompt_id: promptID, ...params });
      if (version === this.version) {
        this.resolve(promptID);
        this.placePrompt();
      }
    } catch (error) {
      toastError(`${error.message} Nothing was retried; check the conversation before answering again.`);
      // Never reuse an uncertain prompt or keep typed secrets after an error.
      if (version === this.version && this.conn.live) this.open(item.id);
    } finally {
      this.acting = false;
      this.controls();
      // The card answered held the focus, and it is gone: the reply box
      // takes it, so the next answer is not a key typed into the page.
      if (!this.el?.transcript.contains(document.activeElement)) this.focusReply();
    }
  }

  // ---------- layout ----------
  build() {
    const transcript = h("div", { class: "transcript", "aria-label": "Conversation", "aria-live": "polite" });
    const reply = h("textarea", {
      id: "reply",
      rows: 1,
      placeholder: "Steer it, or answer in words…",
      oninput: () => {
        reply.style.height = "auto";
        reply.style.height = Math.min(240, reply.scrollHeight) + "px";
        drafts.set(this.item.id, reply.value);
        this.controls();
      },
      onkeydown: (event) => {
        if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !event.altKey) {
          event.preventDefault();
          form.requestSubmit();
        }
      },
    });
    const send = h("button", { type: "submit", class: "send-round", "aria-label": "Send" }, icon("up", 16, 2));
    // Files chosen but not yet sent survive a reload of the same task.
    const kept = this.files?.task === this.item?.id ? this.files.files : [];
    this.files = new Attachments(() => this.controls());
    this.files.task = this.item?.id;
    this.files.files = kept;
    // "Don't ask" is the session's, in Go: no Scheme reaches it, and only
    // this computer turns it on (session.approve_all; a paired device may
    // turn it off, and shows the box only while it is on), and only once
    // Settings › General › Approvals offers it (approve_all_offered; ADR 17, amended
    // 2026-10-05).
    const dontAsk = h(
      "label",
      { class: "dont-ask", title: "Writes and commands in this task are approved without asking until you uncheck this or the task closes. Protected files, writes that look like a key and commands built from tool output still ask; what the guard refuses stays refused." },
      h("input", { type: "checkbox", onchange: (event) => this.setApproveAll(event.target.checked) }),
      h("span", {}, "Don't ask: approve writes and commands"),
    );
    // The task's turn limits (ADR 49): on unless this computer turned them
    // off (session.limits.set, which a paired device's hello does not list).
    // A change applies from the next turn.
    const limits = h(
      "label",
      { class: "dont-ask limits-toggle", title: "Stop this task's turns at the limits in Settings › General: tool calls, tokens, actions and time. Untick to let them run, and wait for your answers, as long as they need; the spend limit still applies. A change applies from the next turn." },
      h("input", { type: "checkbox", onchange: (event) => this.setLimits(event.target.checked) }),
      h("span", {}, "Limits"),
    );
    // The model and effort the next message is answered with, changeable
    // here at any time: while a response runs the change waits for its end.
    const picker = new ModelPicker("task-model", true);
    // The models this task's model may hand work to, chosen when it opened.
    const helpers = h("span", { class: "task-helpers", id: "task-helpers", hidden: true, title: "Chosen when the task started; labels can be edited in Settings › Models at any time" });
    const opts = h("div", { class: "reply-opts" }, picker.el, helpers, dontAsk, limits);
    picker.el.hidden = true;
    const form = h(
      "form",
      { class: "reply", onsubmit: (event) => { event.preventDefault(); this.send(); } },
      h("label", { class: "sr-only", for: "reply" }, "Reply to nodwyn"),
      h("div", { class: "reply-body" }, this.files.list, reply, opts),
      this.files.button(),
      send,
    );
    const main = h("div", { class: "task-main" }, transcript, form);
    const inspector = h("div", { class: "inspector-view", id: "task-inspector", hidden: true });
    main.append(inspector);
    const side = h("aside", { class: "task-aside", "aria-label": "Task details" });
    const head = h("header", { class: "task-head" });
    this.root.replaceChildren(head, h("div", { class: "task-body" }, main, side));
    this.picker = picker;
    this.el = { head, transcript, reply, send, form, side, inspector, main, dontAsk, helpers, limits };
    this.transcript = new Transcript(transcript);
  }
  controls() {
    if (!this.el) return;
    const open = !!this.item?.open;
    const prompt = !!this.activePrompt();
    // On a phone the card that asks gets the screen: the reply box, which
    // would only say to answer it first, is out of the way (style.css).
    this.root.classList.toggle("asking", prompt);
    const lost = open && this.info?.model?.unavailable;
    const delegates = this.info?.delegates || [];
    this.el.helpers.hidden = !open || !delegates.length;
    this.el.helpers.textContent = delegates.length ? `May hand work to: ${delegates.join(", ")}` : "";
    // A saved task can be answered: sending resumes it first.
    const resumable = !open && this.conn.has("session.resume") && !this.desk.keysLocked;
    const canSend = this.conn.has("session.send") && (open || resumable) && !this.loading && !this.acting && !this.uploading && !lost;
    this.el.form.hidden = !this.conn.has("session.send") || !!this.inspecting;
    // While a message or an answer is on its way the reply box is read-only,
    // not disabled: disabling the field being typed in drops the focus to the
    // page, so the next message needed a click, and a bare y or n typed
    // meanwhile answered the next request card.
    const busyHere = this.acting || this.uploading;
    this.el.reply.disabled = !canSend && !busyHere;
    this.el.reply.readOnly = busyHere;
    this.el.reply.placeholder = this.failed
      ? "The conversation did not load: use Retry."
      : !open
      ? this.desk.keysLocked
        ? "Unlock your keys above to continue this task."
        : "This task is saved. Reply to resume it and carry on."
      : lost
        ? "Choose a model below to continue."
      : prompt
        ? "Answer the request above first."
        : this.busy
          ? "Working… stop it to steer."
          : "Steer it, or answer in words…";
    this.el.send.disabled = !canSend || this.busy || prompt || !this.el.reply.value.trim();
    for (const b of this.el.form.querySelectorAll("[aria-label='Attach a file']")) b.disabled = !canSend || !open || this.busy || prompt;
    for (const control of this.el.transcript.querySelectorAll(".ask-card button, .ask-card input")) control.disabled = this.acting || !this.conn.live;
    // A run that answers its own confirms has no questions to stop asking,
    // and a task Settings does not offer the switch to shows it only while
    // it is still on, so it can be turned off.
    const run = this.item?.run;
    const box = this.el.dontAsk;
    // This computer turns it on only once Settings offers it; a paired device
    // (whose hello lacks the methods that are this computer's) turns it off,
    // so it shows the box only while it is on.
    const onComputer = this.conn.has("session.model.set");
    const offered = onComputer ? !!(this.info?.approve_all_offered || this.info?.approve_all) : !!this.info?.approve_all;
    box.hidden = !this.conn.has("session.approve_all") || !open || !offered || !!(run && (run.auto_approve || run.unattended || run.judge));
    const check = box.querySelector("input");
    const approveAll = this.switching ? this.approveAllPending : !!this.info?.approve_all;
    check.checked = !!approveAll;
    check.disabled = !this.info || this.switching || !this.conn.live;
    box.classList.toggle("on", check.checked);
    // A run keeps the limits it started with; an older daemon reports none.
    const limits = this.el.limits;
    limits.hidden = !this.conn.has("session.limits.set") || !open || typeof this.info?.limits !== "boolean" || run?.status === "running";
    const limitsCheck = limits.querySelector("input");
    limitsCheck.checked = this.limitsPending ?? !!this.info?.limits;
    limitsCheck.disabled = this.limitsPending !== undefined || !this.conn.live;
    limits.classList.toggle("on", !limitsCheck.checked);
    this.drawModel();
  }
  // noteModel keeps what the log says the conversation talks to, which is all
  // a task that is not open has to show.
  noteModel(kind, p) {
    if (kind === "session.model") this.logModel = { model: p.model, effort: p.effort || "", queued: !!p.queued };
    else if (!this.logModel || (kind === "session.resume" && this.logModel.model !== p.model)) this.logModel = { model: p.model, effort: "" };
  }
  // drawModel shows the conversation's model and effort, and what a change
  // will do: it takes effect from the next message, and one chosen while a
  // response runs waits for the response to end, which the lines under the
  // picker say.
  drawModel() {
    const picker = this.picker;
    if (!picker || !this.item) return;
    const m = this.info?.model;
    const shown = !!this.item.open && !!m && !!this.desk.settings?.choices && !this.failed;
    picker.el.hidden = !shown;
    if (!shown) return;
    const words = (s) => `${s.model}${s.effort ? ` · ${effortLabel(s.effort)} effort` : ""}`;
    // A choice that is queued is the one the person made last, so it is the one shown.
    const chosen = m.queued || m;
    const selection = m.unavailable ? null : { name: chosen.name || "", server: chosen.server || "", model: chosen.model, effort: chosen.effort || "" };
    const listed = choiceNamed(this.desk, chosen.name);
    const extra = listed ? null : { model: chosen.model, efforts: m.efforts || [], effort: "" };
    const running = this.item.run?.status === "running";
    const canChoose = this.conn.has("session.model.set");
    const notes = [];
    if (m.queued) notes.push({ text: `Chosen while a response is running: ${words(m.queued)} takes effect from your next message. The response now running keeps ${words(m)}.` });
    else if (this.modelChanged) notes.push({ text: `Takes effect from your next message: ${words(m)}.` });
    if (m.notice) notes.push({ kind: "error", text: `The model chosen while the response ran could not be applied: ${m.notice}. The conversation keeps ${words(m)}.` });
    if (m.unavailable) notes.push({ kind: "error", text: `This conversation's model is not available (${m.unavailable}). Nothing is sent until you choose one.` });
    if (!canChoose) notes.push({ text: "The model is chosen on the computer nodwyn runs on." });
    else if (running) notes.push({ text: "Stop the run to change the model." });
    // The judge the task asks (ADR 47) is chosen beside its model, on this
    // computer, between turns: the daemon refuses a change while a response
    // runs, so the select waits for it.
    const judge = this.info?.judge;
    const canJudge = this.conn.has("session.judge.set") && (judgesOf(this.desk).length > 0 || !!judge?.server);
    picker.update({
      desk: this.desk,
      selection,
      extra,
      unavailable: !!m.unavailable,
      disabled: !canChoose || this.changing || this.uploading || running,
      notes,
      onPick: (sel, reset) => this.chooseModel(sel, reset),
      ...(canJudge && judge
        ? {
            judge: judge.server || "",
            judgeDisabled: this.judging || this.busy,
            judgeHint: this.busy ? "The judge can be changed once this response is done." : "",
            onJudge: (name) => this.chooseJudge(name),
          }
        : {}),
    });
  }
  // chooseJudge makes name the task's judge, or none with "", from its next
  // question. Nothing is retried; the picker is drawn again from what the
  // daemon says.
  async chooseJudge(name) {
    const item = this.item,
      version = this.version;
    if (!item || this.judging) return;
    this.judging = true;
    this.controls();
    try {
      const state = await this.conn.call("session.judge.set", { id: item.id, judge: name });
      if (version === this.version && this.info) this.info.judge = state;
      toast(name ? `${name} judges this task from its next question.` : "This task has no judge now: you answer its commands.");
    } catch (error) {
      toastError(`${error.message} Nothing was retried; the task keeps the judge it had.`);
    } finally {
      this.judging = false;
      if (version === this.version) {
        await this.loadInfo();
        this.controls();
      }
    }
  }
  // chooseModel moves the conversation to a model and effort for its next
  // message. The daemon refuses what the model does not take, and queues the
  // choice when a response is running; nothing is retried, and the picker is
  // drawn again from what the daemon says.
  async chooseModel(sel, reset) {
    const item = this.item,
      version = this.version;
    if (!item || this.changing) return;
    this.changing = true;
    this.controls();
    try {
      const params = { id: item.id };
      if (sel.name) params.name = sel.name;
      else Object.assign(params, { server: sel.server, model: sel.model });
      if (sel.effort) params.effort = sel.effort;
      const state = await this.conn.call("session.model.set", params);
      if (version === this.version && this.info) {
        this.info.model = state;
        this.modelChanged = !state.queued;
      }
      if (reset) toast("That model takes other efforts: its own default is chosen.");
    } catch (error) {
      toastError(`${error.message} Nothing was retried; the conversation keeps the model it had.`);
    } finally {
      this.changing = false;
      if (version === this.version) {
        await this.loadInfo();
        this.controls();
      }
    }
  }
  async setApproveAll(on) {
    const id = this.item?.id;
    if (!id || this.switching) return;
    const version = this.version;
    this.switching = true;
    this.approveAllPending = on;
    this.controls();
    try {
      await this.conn.call("session.approve_all", { id, on });
    } catch (error) {
      toastError(error.message);
    } finally {
      if (version !== this.version) return;
      // Keep the clicked state drawn until the latest authoritative details
      // arrive; a concurrent approval.change read cannot replace that result.
      await this.loadInfo();
      this.approveAllPending = null;
      this.switching = false;
      this.controls();
    }
  }
  // setLimits turns the task's turn limits on or off from its next turn
  // (ADR 49), drawing the choice at once and the daemon's answer after.
  async setLimits(on) {
    const id = this.item?.id;
    if (!id || this.limitsPending !== undefined) return;
    const version = this.version;
    this.limitsPending = on;
    this.controls();
    try {
      await this.conn.call("session.limits.set", { id, on });
    } catch (error) {
      toastError(error.message);
    } finally {
      if (version === this.version) {
        await this.loadInfo();
        this.limitsPending = undefined;
        this.controls();
      }
    }
  }
  async send() {
    if (this.el.send.disabled) return;
    const item = this.item,
      text = this.el.reply.value,
      version = this.version;
    if (!item.open) {
      // A saved task is resumed, and the message goes once it is open again:
      // the text stays in the box if resuming fails.
      drafts.set(item.id, text);
      if (!(await this.resume()) || this.item?.id !== item.id) return;
      return this.send();
    }
    this.acting = true;
    this.busy = true;
    this.controls();
    try {
      if (this.files.files.length) {
        // An image waits for its own confirm, answered on this page: the
        // upload holds the composer, not the request card.
        this.acting = false;
        this.uploading = true;
        this.controls();
        try {
          await this.files.upload(item.id);
        } finally {
          this.uploading = false;
          this.acting = true;
        }
      }
      await this.conn.call("session.send", { id: item.id, text });
      if (drafts.get(item.id) === text) drafts.delete(item.id);
      if (version === this.version && this.el.reply.value === text) {
        this.el.reply.value = "";
        this.el.reply.style.height = "auto";
      }
    } catch (error) {
      // A failed reply is not evidence of failed delivery: reload first.
      toastError(`${error.message} Nothing was retried; check the conversation before sending again.`);
      if (version === this.version && this.conn.live) this.open(item.id);
    } finally {
      this.acting = false;
      this.refreshAll();
    }
  }
  refreshAll() {
    this.header();
    this.aside();
    this.controls();
    clearInterval(this.timer);
    // A view kept off screen draws its events but counts no seconds.
    if (this.busy && this.root.isConnected)
      this.timer = setInterval(() => {
        this.meters();
        this.tickTotals();
      }, 1000);
  }
  status() {
    const item = this.item;
    if (!item) return { label: "", cls: "" };
    if (this.failed) return { label: "Not loaded", cls: "warn" };
    if (this.activePrompt()) return { label: "Waiting for you", cls: "warn" };
    if (item.run?.status === "running") return { label: `${item.run.unattended ? "Unattended run" : "Run"}${item.run.judge ? ", judged" : ""} · turn ${item.run.turns} of ${item.run.max_turns}`, cls: "run" };
    if (this.busy) return { label: this.statusDetail && this.statusDetail !== "busy" ? oneWord(this.statusDetail) : "Working", cls: "run" };
    if (item.open) return { label: "Idle", cls: "" };
    return { label: "Saved", cls: "" };
  }
  header() {
    if (!this.el || !this.item) return;
    // A details element says it opened a task after the click, in its toggle
    // event, and a redraw in between built the menu closed again, so a click
    // on one of its entries waited on a menu nobody could see: the menu on
    // the page is what counts.
    const shown = this.root.querySelector(".task-actions .more");
    if (shown) this.menuOpen = shown.open;
    const item = this.item;
    const home = this.desk.home;
    const st = this.status();
    // One primary action for the state the task is in, and one menu for the
    // rest: Stop while it works, Resume once it is saved; Search, Schedule,
    // Close, Archive and Delete are rarer, and Close or Delete beside the
    // other ghost buttons read as no different from Search.
    const actions = h("div", { class: "task-actions" });
    const button = (label, iconName, handler, cls = "") => h("button", { type: "button", class: `btn ${cls}`, disabled: !this.conn.live || this.acting, onclick: handler }, iconName ? icon(iconName, 15) : null, label);
    const menu = [];
    // An item of the menu: choosing it folds the menu away first, so the
    // redraw that follows its action does not leave it open.
    const entry = (label, handler, { danger = false, disabled = false, title = "" } = {}) =>
      h("button", { type: "button", class: `menu-item${danger ? " danger" : ""}`, disabled: disabled || !this.conn.live || this.acting, title: title || undefined, onclick: (event) => { this.menuOpen = false; event.target.closest("details")?.removeAttribute("open"); handler(); } }, label);
    if (this.conn.has("conversation.search") && this.conn.has("log.context")) menu.push(h("a", { class: "menu-item", href: `#/search/${encodeURIComponent(item.id)}`, "aria-label": "Search conversation", onclick: () => (this.menuOpen = false) }, "Search"));
    if (this.conn.has("schedule.read") && (this.conn.has("schedule.set") || item.schedule?.definition)) menu.push(entry("Schedule…", () => manageSchedule(this.conn, item.id, async () => { await this.desk.refresh(); if (this.item?.id === item.id) { this.item = this.desk.sessions.get(item.id) || item; this.header(); if (this.item.open) await this.loadInfo(); } })));
    // A run comes from nodwyn run (ADR 35): Stop ends it, and a message
    // afterwards goes on with the task as a chat. A request waiting outside
    // a turn, an image's confirm, is stopped too: it is declined. It is shown
    // only while there is something to stop.
    if (item.open && this.conn.has("session.abort") && (this.busy || this.activePrompt())) actions.append(button("Stop", "stop", () => this.act("session.abort", "Stopping…")));
    const keysLocked = !item.open && this.desk.keysLocked;
    if (!item.open && this.conn.has("session.resume")) {
      const resume = button("Resume", "up", () => this.resume(), "dark");
      resume.disabled = resume.disabled || keysLocked;
      actions.append(resume);
    }
    if (item.open && this.conn.has("session.close")) menu.push(entry("Close", () => this.act("session.close", "Closing the task; its conversation is kept."), { disabled: this.busy || !!this.activePrompt(), title: "Close the task. Its conversation is kept and can be resumed." }));
    // A closed task is archived or deleted (ADR 39); an open one is closed
    // first, so a task that can ask is never put away.
    if (!item.open && this.conn.has("session.archive")) menu.push(entry(item.archived ? "Unarchive" : "Archive", () => this.archive(!item.archived)));
    if (!item.open && this.conn.has("session.delete")) menu.push(entry("Delete…", () => { this.deleting = true; this.header(); }, { danger: true }));
    if (menu.length) {
      const more = h(
        "details",
        { class: "more", open: this.menuOpen, ontoggle: () => (this.menuOpen = more.open), onkeydown: (event) => { if (event.key === "Escape") { this.menuOpen = false; more.open = false; more.querySelector("summary").focus(); } } },
        h("summary", { class: "btn ghost", "aria-label": "More actions", title: "More actions" }, icon("more", 20, 3)),
        h("div", { class: "more-pop", role: "group", "aria-label": "More actions" }, menu),
      );
      actions.append(more);
    }
    const title = item.title || item.first_message || "Untitled task";
    const meta = h(
      "div",
      { class: "task-meta" },
      icon("folder", 15),
      h("strong", {}, this.desk.folderName(this.desk.project(item))),
      h("span", { class: "mono" }, shortPath(this.desk.project(item), home)),
      item.worktree ? h("span", { class: "badge plain mono", title: `In its own git worktree at ${item.worktree.path}` }, icon("commit", 13), item.worktree.branch) : null,
      item.open && this.info?.limits === false ? h("span", { class: "badge", id: "no-limits", title: "This task's turns have no limit on tool calls, tokens, actions or time; its spend limit still applies." }, "No turn limits") : null,
      item.started ? h("span", { class: "started" }, `· started ${sameDay(item.started) ? clock(item.started) : ago(item.started)}`) : null,
      !item.open && this.logModel ? h("span", { class: "started", id: "saved-model" }, `· ${this.logModel.model}${this.logModel.effort ? ` · ${effortLabel(this.logModel.effort)} effort` : ""}, as it was left`) : null,
      item.archived ? h("span", { class: "badge plain" }, "archived") : null,
    );
    const canRename = this.conn.has("session.rename");
    let titles;
    if (this.renaming) titles = this.renameBox ||= h("div", { class: "titles" }, this.renameForm(), meta);
    else {
      this.renameBox = null;
      titles = h(
        "div",
        { class: "titles" },
        h("div", { class: "title-row" }, h("h1", { ondblclick: canRename ? () => this.startRename() : undefined }, title), canRename ? h("button", { type: "button", class: "icon-btn rename-btn", "aria-label": "Rename task", title: "Rename", onclick: () => this.startRename() }, icon("pencil", 15)) : null),
        meta,
      );
    }
    const rest = [
      st.label ? h("span", { class: `pill ${st.cls}` }, st.cls ? h("span", { class: `dot ${st.cls}` }) : null, st.label) : "",
      actions,
      this.deleting && !item.open
        ? h(
            "div",
            { class: "callout delete-confirm", role: "alert" },
            h("span", {}, "Delete this task for good? Its conversation and its log go; the files it changed stay as they are."),
            h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small dark", disabled: this.acting, onclick: () => this.remove() }, "Delete"), h("button", { type: "button", class: "btn small ghost", onclick: () => { this.deleting = false; this.header(); } }, "Cancel")),
          )
        : "",
      keysLocked ? h("details", { class: "callout", open: true }, h("summary", {}, "Unlock your keys to resume: this task's model needs one."), keychainForm({ conn: this.conn, desk: this.desk }, { onDone: () => this.header() })) : "",
      // Nothing says how much this model holds, so the window keeps its
      // small default and compacts early (ADR 41, amended 2026-10-05): said
      // where it is seen, not only in the log.
      item.open && this.info?.model?.context_unknown
        ? h(
            "div",
            { class: "callout context-unknown", role: "note" },
            h("span", {}, `This task works in a ${(this.desk.settings?.values?.["window.high_water"]?.value || 61440).toLocaleString("en-US")}-token window: nodwyn doesn't know how much context ${this.info.model.model} holds, so it compacts early and forgets more. Set its context length in Settings › Models.`),
            h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small", onclick: () => this.navigate("#/settings/models") }, "Open Settings")),
          )
        : "",
    ];
    // While a name is typed, the heading around it is redrawn but the field
    // stays, with its text and its focus.
    if (titles === this.el.head.firstChild) {
      while (this.el.head.lastChild !== titles) this.el.head.lastChild.remove();
      this.el.head.append(...rest);
    } else this.el.head.replaceChildren(titles, ...rest);
  }
  startRename() {
    this.renaming = true;
    this.header();
  }
  // renameForm is the field the heading becomes: Enter or leaving it saves,
  // Escape keeps the name as it was. An empty name gives the task back the
  // title its first message gives it.
  renameForm() {
    const item = this.item;
    const input = h("input", { class: "input title-input", value: item.title || "", maxlength: "120", "aria-label": "Task name", placeholder: "Name this task", autocomplete: "off" });
    let done = false;
    const finish = (save) => {
      if (done) return;
      done = true;
      this.renaming = false;
      const name = input.value.trim();
      if (save && name !== (item.title || "")) this.rename(item, name);
      else if (this.item === item) this.header();
    };
    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        finish(false);
      }
    });
    input.addEventListener("blur", () => finish(true));
    setTimeout(() => {
      input.focus();
      input.select();
    }, 0);
    return h("form", { class: "rename", onsubmit: (event) => { event.preventDefault(); finish(true); } }, input);
  }
  async rename(item, name) {
    if (name) item.title = name;
    if (this.item === item) this.header();
    try {
      await this.conn.call("session.rename", { id: item.id, title: name });
    } catch (error) {
      toastError(error.message);
    }
    await this.desk.reload();
  }
  async archive(archived) {
    const item = this.item;
    this.acting = true;
    this.controls();
    try {
      await this.conn.call("session.archive", { id: item.id, archived });
      toast(archived ? "Archived. It waits under Archived in the sidebar." : "Back in the task list.");
      await this.desk.refresh();
    } catch (error) {
      toastError(error.message);
    } finally {
      this.acting = false;
      this.refreshAll();
    }
  }
  async remove() {
    const item = this.item;
    this.acting = true;
    this.header();
    try {
      await this.conn.call("session.delete", { id: item.id });
    } catch (error) {
      toastError(error.message);
      this.acting = false;
      this.refreshAll();
      return;
    }
    this.acting = false;
    this.deleting = false;
    toast("Deleted the task.");
    this.navigate("#/");
    await this.desk.reload();
    drafts.delete(item.id);
  }
  async act(method, message) {
    const item = this.item;
    this.acting = true;
    this.controls();
    try {
      toast(message);
      await this.conn.call(method, { id: item.id });
      if (method === "session.close") await this.desk.refresh();
    } catch (error) {
      toastError(error.message);
    } finally {
      this.acting = false;
      this.refreshAll();
    }
  }
  // resume opens the saved task again, and says whether it is open and
  // loaded now.
  async resume() {
    const item = this.item,
      version = this.version;
    this.acting = true;
    this.controls();
    try {
      await this.conn.call("session.resume", { id: item.id });
      await this.desk.refresh();
      this.acting = false;
      // The refresh may already have reopened this view (app.js does, for a
      // task that opens under a view showing it saved): that load is waited
      // for, not repeated. Another task opened meanwhile is never replaced.
      if (version === this.version) await this.open(item.id);
      else await this.opening;
      return this.item?.id === item.id && !!this.item.open && !this.failed;
    } catch (error) {
      toastError(error.message);
      return false;
    } finally {
      this.acting = false;
      this.controls();
    }
  }

  // ---------- aside ----------
  // events is the conversation in log order. Every panel reads it on each
  // redraw, so it is sorted once per new event, and the same list, which
  // nothing changes, is handed out until one arrives.
  events() {
    if (this.sorted?.from !== this.seen || this.sorted.size !== this.seen.size) this.sorted = { from: this.seen, size: this.seen.size, list: [...this.seen.values()].sort((a, b) => a.seq - b.seq) };
    return this.sorted.list;
  }
  lastTurn() {
    const events = this.events();
    const start = events.findLastIndex((e) => e.kind === "user.msg" || e.kind === "eval.req");
    return start < 0 ? null : { start: events[start], tail: events.slice(start + 1) };
  }
  aside(reviewOpening = false) {
    if (!this.el || !this.item) return;
    const side = this.el.side;
    // Each tab keeps its reading position through events and tab switches.
    const previousBody = side.querySelector(".aside-panel");
    if (previousBody) this.asideScroll.set(previousBody.dataset.tab, previousBody.scrollTop);
    // A redraw replaces the project notes field being typed in: the new one
    // takes the focus and the caret, so typing carries on where it was.
    const typing = [...side.querySelectorAll("textarea[data-notes]")].find((field) => field === document.activeElement);
    const caret = typing ? [typing.selectionStart, typing.selectionEnd, typing.scrollTop] : null;
    // A tab button the reader is on keeps the focus across the redraw, as
    // the notes field keeps its caret.
    const focusing = side.querySelector("[role=tab]:focus");
    const tabs = this.asideTabs();
    this.tab ||= tabs[0].id;
    let at = tabs.findIndex((t) => t.id === this.tab);
    if (at < 0) {
      at = 0;
      this.tab = tabs[0].id;
    }
    const bar = h(
      "div",
      { class: "tabs aside-tabs", role: "tablist", "aria-label": "Task details" },
      tabs.map((t, i) =>
        h(
          "button",
          {
            type: "button",
            role: "tab",
            id: `aside-tab-${t.id}`,
            "aria-selected": String(i === at),
            "aria-controls": "aside-panel",
            tabindex: i === at ? 0 : -1,
            onclick: () => {
              this.tab = t.id;
              // The index changes outside the task (another task's search,
              // Forget on Workspaces): read it again when it is looked at.
              if (t.id === "workspace") this.loadIndex();
              this.aside();
            },
            onkeydown: (event) => this.tabKey(event),
          },
          t.label,
        ),
      ),
    );
    const shown = tabs[at].draw().filter(Boolean);
    const body = h(
      "div",
      { class: "aside-panel", dataset: { tab: tabs[at].id }, role: "tabpanel", id: "aside-panel", "aria-labelledby": `aside-tab-${tabs[at].id}` },
      ...(shown.length ? shown : [h("p", { class: "hint" }, tabs[at].empty)]),
    );
    const review = this.item.worktree && this.conn.has("worktree.diff")
      ? h("button", { type: "button", class: "btn small review-open", "aria-expanded": String(this.inspecting === "review"), "aria-controls": "task-inspector", onclick: () => this.openReview() }, this.reviewLabel())
      : null;
    const reviewFocused = side.querySelector(".review-open:focus");
    side.replaceChildren(...[this.monitorPanel(), review, bar, body].filter(Boolean));
    if (reviewFocused) review?.focus({ preventScroll: true });
    body.scrollTop = this.asideScroll.get(tabs[at].id) || 0;
    if (focusing) side.querySelector(`#${focusing.id}`)?.focus();
    const area = caret && side.querySelector(`textarea[data-notes="${typing.dataset.notes}"]`);
    if (area) {
      area.focus({ preventScroll: true });
      area.setSelectionRange(caret[0], caret[1]);
      area.scrollTop = caret[2];
    }
    if (this.inspecting === "review") this.drawReview(reviewOpening);
  }

  // The summary stays visible across tabs; diagnostic figures belong in
  // Statistics, and retained notes belong with the workspace they describe.
  asideTabs() {
    const tabs = [
      { id: "progress", label: "Progress", draw: () => [this.planPanel()], empty: "No plan recorded yet." },
      { id: "changes", label: "Changes", draw: () => [this.changesPanel(), this.attachmentsPanel()], empty: "No approved changes yet." },
      { id: "statistics", label: "Statistics", draw: () => [this.turnPanel(), this.totalsPanel()], empty: "Nothing has run yet." },
      { id: "workspace", label: "Workspace", draw: () => [this.workspacePanel(), this.toolsPanel(), this.detailsPanel(), this.notesPanel()], empty: "Nothing to show." },
    ];
    return tabs;
  }

  // The tabs follow the arrow keys, the tab that takes focus being the one
  // shown (roving tabindex).
  tabKey(event) {
    const step = { ArrowLeft: -1, ArrowRight: 1, Home: -Infinity, End: Infinity }[event.key];
    if (step === undefined) return;
    const tabs = [...(this.el?.side?.querySelectorAll("[role=tab]") || [])];
    if (tabs.length < 2) return;
    const at = tabs.findIndex((t) => t.getAttribute("aria-selected") === "true");
    const next = step === -Infinity ? 0 : step === Infinity ? tabs.length - 1 : Math.max(0, Math.min(tabs.length - 1, at + step));
    if (next === at) return;
    event.preventDefault();
    tabs[next].click();
    this.el.side.querySelector(`#${tabs[next].id}`)?.focus();
  }
  planPanel() {
    const turn = this.lastTurn();
    const at = turn ? turn.tail.findLastIndex((e) => e.kind === "gate.plan") : -1;
    const panel = h("section", { class: "panel" });
    if (at < 0) {
      panel.append(h("h2", {}, "Plan", h("span", { class: "note" }, turn ? "none recorded for this ask" : "")));
      if (!turn) return null;
      return panel;
    }
    const p = turn.tail[at].payload;
    // Where the plan came from matters only when the judge said something
    // about it; "drafted", "not needed" and "judge unavailable" are the
    // plan gate's own codes and told a reader nothing.
    const judged =
      p.noul === "plan" && p.score
        ? `scored ${Number(p.score).toFixed(2)} by the judge`
        : p.noul === "required"
          ? p.score
            ? `required, scored ${Number(p.score).toFixed(2)}`
            : "required for this run"
          : p.noul === "carried"
            ? "carried over from an earlier turn"
            : p.noul === "model"
              ? "written by the model"
              : "";
    const steps = planSteps(p.plan);
    // The model's marks, the latest per step: those a carried plan brings
    // from the run's earlier turns, then those since (plan.step). A mark is
    // the model's word; the transcript holds what shows it.
    const marks = new Map();
    for (const m of p.marks || []) marks.set(m.step, m);
    for (const e of turn.tail.slice(at + 1)) if (e.kind === "plan.step") marks.set(e.payload.step, e.payload);
    const done = [...marks.values()].filter((m) => m.state === "done").length;
    const note = steps.length && marks.size ? `${done} of ${steps.length} done${judged ? ` · ${judged}` : ""}` : judged;
    panel.append(h("h2", {}, "Plan", h("span", { class: "note" }, note)));
    if (steps.length) {
      const next = marks.size ? steps.findIndex((_, i) => !marks.has(i + 1)) : -1;
      // The side is redrawn on every event, so a step the reader opened
      // would fold again at the next one; the fold lives here, per task.
      const opened = openSteps.get(this.item?.id) || new Set();
      openSteps.set(this.item?.id ?? "", opened);
      const items = steps.map((s, i) => {
        const m = marks.get(i + 1);
        const state = m ? m.state : i === next ? "next" : "";
        const said = m ? (m.state === "done" ? "Marked done" : "Skipped") + (m.note ? `: ${m.note}` : "") : undefined;
        // A step with more to say than its title — a detail, or the note
        // the model marked it with — folds: the title stands, and clicking
        // the step shows the whole of it and the note. By default neither
        // shows, so the plan reads at a glance.
        const title = stepTitle(s);
        if (title === s && !m?.note)
          return h("li", { class: state || undefined, title: said }, h("span", {}, h("span", { class: "text" }, s)));
        const alt = title !== s;
        const open = opened.has(s);
        const li = h(
          "li",
          {
            class: [state, "fold", alt && "alt", open && "open"].filter(Boolean).join(" "),
            title: said,
            onclick: () => {
              // Read the set, not the open the render captured: a click
              // that lands after a redraw would act on a stale boolean.
              opened.has(s) ? opened.delete(s) : opened.add(s);
              li.classList.toggle("open");
            },
          },
          h("span", {}, h("span", { class: "text" }, title), alt ? h("span", { class: "more" }, s) : null, m?.note ? h("small", {}, m.note) : null)
        );
        return li;
      });
      panel.append(h("ol", { class: "plan" }, items));
    } else if (p.reason) panel.append(h("p", { class: "hint" }, p.reason));
    return panel;
  }
  // Three fixed slots keep monitoring independent of the plan's length: what
  // a task costs, how long the turn has run and how full the context is. The
  // tool calls and the cache rate are in Statistics. Unknown values stay
  // unknown, and context is fullness, not token usage.
  monitorPanel() {
    const panel = h("section", { class: "task-monitor", "aria-label": "Task monitoring" });
    for (const [key, label] of [["spend", "Total spend"], ["time", "Turn time"], ["context", "Context"]])
      panel.append(h("div", { class: "monitor-stat" }, h("span", { class: "monitor-label" }, label), h("strong", { class: "monitor-value", dataset: { monitor: key } }, "—")));
    panel.append(h("div", { class: "monitor-warning" }));
    this.fillMonitor(panel);
    return panel;
  }
  fillMonitor(panel) {
    const totals = this.totals();
    const turn = this.lastTurn();
    const budgets = this.info?.budgets;
    const context = contextGauge(this.events());
    const values = {
      // A model nothing prices has spent nothing the log can show, which
      // "at least $0.0000" said in the first slot of every task.
      spend: totals ? (totals.unpriced && !totals.cost ? "unpriced" : `${totals.unpriced ? "at least " : ""}${usd(totals.cost, totals.estimated)}`) : "—",
      time: "—",
      context: context.est ? (context.high ? `${Math.round(100 * context.est / context.high)}%` : `~${context.est.toLocaleString()}`) : "—",
    };
    const warnings = [];
    const near = (label, used, limit) => {
      if (limit && used >= limit * 0.9) warnings.push(`${label} ${used >= limit ? "at limit" : "near limit"}`);
    };
    if (turn) {
      const ended = turn.tail.find((e) => e.kind === "turn.end");
      const start = tsOf(turn.start);
      const until = ended ? tsOf(ended) : this.busy ? Date.now() : tsOf(turn.tail.at(-1) || turn.start);
      const elapsed = start ? Math.max(0, until - start) : 0;
      values.time = minutes(elapsed);
      const calls = turn.tail.filter((e) => e.kind === "tool.call").length;
      let tokens = 0;
      for (const e of turn.tail) {
        if (e.kind !== "llm.resp") continue;
        const u = e.payload.usage || {};
        const prompt = u.prompt_tokens || 0;
        const cached = Math.min(Math.max(u.cached_tokens || 0, 0), prompt);
        tokens += prompt - cached + Math.floor(cached / 10) + (u.output_tokens || 0);
      }
      near("Tokens", tokens, budgets?.tokens?.soft);
      near("Time", elapsed, budgets?.wall_ns?.soft / 1e6);
      near("Tool calls", calls, budgets?.tool_calls?.soft);
    }
    near("Spend", this.runSpend(totals) ?? totals?.cost ?? 0, budgets?.cost?.soft / 1e6);
    near("Context", context.est, context.high);
    for (const [key, value] of Object.entries(values)) panel.querySelector(`[data-monitor="${key}"]`).textContent = value;
    panel.querySelector('[data-monitor="spend"]').title = `${totals?.unpriced ? "Lower bound: some requests are unpriced. " : ""}${this.truncated ? "From the log that remains. " : ""}${budgets?.cost?.soft ? `Spend limit: ${usd(budgets.cost.soft / 1e6, false)}.` : "Task-wide model and judge spend."}`;
    panel.querySelector('[data-monitor="time"]').title = "Current turn's elapsed time, including waits for your answer.";
    panel.querySelector('[data-monitor="context"]').title = `${context.agent ? `${context.agent}'s context. ` : ""}${context.high ? `Estimated fullness against ${context.high.toLocaleString()} tokens; older context is trimmed beyond this high water.` : "Context estimate; no high water recorded."}`;
    const warning = panel.querySelector(".monitor-warning");
    warning.textContent = warnings.join(" · ");
    warning.title = warning.textContent;
  }
  turnPanel() {
    const turn = this.lastTurn();
    const budgets = this.info?.budgets;
    if (!turn) return null;
    // By default a soft limit is its hard one, so nothing asks first (ADR 35).
    const asks = budgets && ["tool_calls", "tokens", "actions", "wall_ns", "cost"].some((d) => budgets[d]?.soft < budgets[d]?.hard);
    const note = !budgets ? "counted from the log" : this.info?.limits === false ? "no turn limits" : asks ? "asks before a limit" : "stops at a limit";
    const panel = h("section", { class: "panel" }, h("h2", {}, "This turn", h("span", { class: "note" }, note)));
    const meters = h("div", { class: "stack-md", dataset: { meters: "1" } });
    panel.append(meters);
    this.fillMeters(meters, turn);
    return panel;
  }
  meters() {
    const monitor = this.el?.side.querySelector(".task-monitor");
    if (monitor) this.fillMonitor(monitor);
    const node = this.el?.side.querySelector("[data-meters]");
    const turn = this.lastTurn();
    if (node && turn) this.fillMeters(node, turn);
  }
  fillMeters(node, turn) {
    const budgets = this.info?.budgets;
    const ended = turn.tail.find((e) => e.kind === "turn.end");
    const startTS = new Date(turn.start.ts || turn.start.TS || 0).getTime();
    const endTS = ended ? new Date(ended.ts || ended.TS).getTime() : this.busy ? Date.now() : new Date(turn.tail.at(-1)?.ts || startTS).getTime();
    const elapsed = startTS ? Math.max(0, endTS - startTS) : 0;
    const calls = turn.tail.filter((e) => e.kind === "tool.call").length;
    const split = this.turnSplit(turn);
    // As the token budget counts them (budget.CountedPrompt): a prompt
    // token read from the provider's cache counts a tenth.
    let tokens = 0;
    for (const e of turn.tail) {
      if (e.kind !== "llm.resp") continue;
      const u = e.payload.usage || {};
      const prompt = u.prompt_tokens || 0;
      const cached = Math.min(Math.max(u.cached_tokens || 0, 0), prompt);
      tokens += prompt - cached + Math.floor(cached / 10) + (u.output_tokens || 0);
    }
    const items = [
      limited("Time", elapsed, budgets ? budgets.wall_ns.soft / 1e6 : 0, (ms) => minutes(ms)),
      split.model ? statRow("Model wait", minutes(split.model)) : null,
      split.tools ? statRow("Tool time", minutes(split.tools)) : null,
      limited("Tool calls", calls, budgets?.tool_calls.soft || 0),
      // The budget counts every request's whole prompt, cached tokens at a
      // tenth, and its reply, so this adds the context up once per request;
      // Context below is how big it is. It is "used", not "sent": replies
      // come back, and a cached token is not counted whole. Shown from the
      // turn's start, as Time is: it waited for the first reply, which the
      // explorer's long first requests delay.
      limited("Tokens used", tokens, budgets?.tokens.soft || 0, (n) => n.toLocaleString()),
      this.contextMeter(),
      this.judgeLine(),
    ].filter(Boolean);
    // The bars come first and the plain rows under them, so the panel reads
    // as two groups, not as bars and counters taking turns.
    const bars = (n) => n.classList.contains("meter");
    node.replaceChildren(...items.filter(bars), ...items.filter((n) => !bars(n)));
  }
  // turnSplit is splitTime over one turn's own records: how much of the
  // turn went waiting for a model and how much running tools, its open
  // pairs counting to now while the turn runs and no prompt holds it.
  turnSplit(turn) {
    const ended = turn.tail.find((e) => e.kind === "turn.end");
    const records = [turn.start, ...turn.tail];
    const until = ended ? tsOf(ended) : this.clockUntil(records);
    const { model, tools, openModel, openTool } = splitTime(records);
    return {
      model: model + (openModel !== null ? Math.max(0, until - openModel) : 0),
      tools: Math.max(0, tools + (openTool !== null ? Math.max(0, until - openTool) : 0)),
    };
  }
  // judgeLine is what the judge has used in this task, when it was asked at
  // all: its calls, tokens and, with its prices, what they cost.
  judgeLine() {
    const j = this.conn.has("session.totals") ? this.totalSnapshot?.judge : judgeTally(this.events());
    if (!j) return null;
    if (!j.calls) return null;
    const cost = j.tokens && j.unpriced === 0 ? ` · ${usd(j.cost, true)}` : j.cost > 0 ? ` · at least ${usd(j.cost, true)}` : j.unpriced ? " · not priced" : "";
    return statRow(
      "Judge, this task",
      `${j.calls} call${j.calls === 1 ? "" : "s"} · ${j.tokens.toLocaleString()} tokens${cost}`,
      {},
      "The judge reports what it used, not what it cost; its prices ([judge] price_input and price_output, else its provider's) estimate it. Its tokens are not in the token budget; its cost counts against a spend limit.",
    );
  }
  // contextMeter is how full the conversation's context is, against the
  // window's high water, past which older context is trimmed (compaction
  // comes just before). Estimates, the unit the window and compaction use.
  // The provider's own count stays a tooltip: it differs from the estimate
  // and its line reflowed the panel on every reply. A log that does not say
  // the high water has no limit to count against, so the figure is a row.
  // The window is known from the start: the bar shows from then, empty until
  // the first request. While the explorer, the planner or a sub-agent asks
  // in a context of its own, the bar follows that context, named in its
  // label, and returns to the conversation's when it folds.
  contextMeter() {
    const g = contextGauge(this.events());
    if (!g.est && !g.high) return null;
    if (!g.est) return meterBar("Context", `— / ~${g.high.toLocaleString()}`, 0, g.high, "No model request yet; older context is trimmed past the window's high water, and compaction comes just before it");
    const node = limited(g.agent ? `Context · ${g.agent}` : "Context", g.est, g.high, (n) => `~${n.toLocaleString()}`);
    const said = [g.reported ? `the provider counted ${g.reported.toLocaleString()} tokens` : "", g.compactions ? `compacted ${g.compactions === 1 ? "once" : `${g.compactions} times`}` : ""].filter(Boolean);
    const whose = g.agent ? `the ${g.agent}'s latest request, in a context of its own that folds back into the conversation` : "the latest request in the conversation";
    node.title = [`nodwyn's estimate of ${whose}; older context is trimmed past the window's high water, and compaction comes just before it`, ...said].join("; ");
    return node;
  }
  // totalsPanel is the task's whole-life figures, first in the aside: what
  // its requests cost and how long it ran, added up over the whole log. A
  // spend limit shows itself as the bar behind the total.
  runSpend(totals = this.totals()) {
    const run = this.events().findLast((e) => e.kind === "schedule.run");
    if (run && totals) return Math.max(0, totals.cost - (run.payload.spend_before || 0));
    return this.info?.run_spend;
  }
  totalsPanel() {
    const t = this.totals();
    if (!t) return null;
    const limit = this.info?.budgets?.cost?.hard ? this.info.budgets.cost.soft / 1e6 : 0;
    const runSpend = this.runSpend(t);
    const spend = `${t.unpriced ? "at least " : ""}${usd(t.cost, t.estimated)}`;
    const spendHint = "What the task's model and judge requests cost, as far as the log prices them and as a spend limit counts them; a request without a known cost makes this a lower bound";
    return h(
      "section",
      { class: "panel", dataset: { totals: "1" } },
      h("h2", {}, "Totals", h("span", { class: "note" }, this.truncated ? "from the log that remains" : "")),
      limit && runSpend === undefined ? meterBar("Total spend", `${spend} / ${usd(limit, false)}`, t.cost, limit, spendHint) : statRow("Total spend", spend, {}, spendHint),
      runSpend !== undefined ? (limit ? meterBar("This run", `${usd(runSpend, false)} / ${usd(limit, false)}`, runSpend, limit, "Resets at each scheduled run; human follow-ups count in this run.") : statRow("This run", usd(runSpend, false))) : null,
      this.totalSnapshot ? statRow("Turns", String(this.totalSnapshot.turns), {}, this.totalSnapshot.last_turn ? `Latest turn: ${clock(this.totalSnapshot.last_turn.ts)}` : "") : null,
      this.totalSnapshot?.runs ? statRow("Runs", String(this.totalSnapshot.runs), {}, `Latest run: ${clock(this.totalSnapshot.last_run.ts)}`) : null,
      statRow("Total running time", spanWords(t.ms), { dataset: { running: "1" } }, "How long the task's turns took, wall clock, the time it spent waiting for answers included; a turn still running counts up to now"),
      statRow("Total working time", spanWords(t.work), { dataset: { working: "1" } }, "How long the task's turns took, with the time it spent waiting for a person's answer left out; a turn still running counts up to now unless it waits for one"),
      statRow("Model wait", spanWords(t.model), { dataset: { model: "1" } }, "How long the task waited for answers from models, each request from its sending to its reply; streaming counts as waiting, and a request still being answered counts up to now"),
      t.speed
        ? statRow(
            "Model speed",
            `${t.speed.tps.toFixed(1)} tok/s · ${t.speed.replies} request${t.speed.replies === 1 ? "" : "s"}`,
            {},
            "How fast the models answered: output tokens per second of wall time, from each request to its reply, added up over the requests that reported tokens; whichever model answered, this is the speed the session experienced",
          )
        : null,
      statRow("Token cache rate", t.cache.prompt > 0 ? `${Math.round((100 * t.cache.cached) / t.cache.prompt)}%` : "unknown", {}, "The share of raw prompt tokens read from the provider's cache, added up across the session's model replies that reported cache reads and prompt tokens; missing or unreported usage stays out, and without a positive prompt total the rate is unknown; this is not the share of requests hitting cache or the discounted token budget"),
      statRow("Tool time", spanWords(t.tools), { dataset: { tools: "1" } }, "How long the task's tool calls took to run, from a call to its result, with the wait for a person's approval left out; the time waiting for you to answer is in no figure here"),
    );
  }
  // totals adds the task up over its whole log: what its requests cost, as
  // far as the log prices them, how long it ran and worked, how much of
  // that went waiting for a model and running tools, and how fast the
  // models answered and the share of their prompts read from cache. Null
  // before the first turn: nothing has run to add up.
  totals(evs = this.events()) {
    if (this.conn.has("session.totals")) {
      const t = this.totalSnapshot;
      if (!t?.started) return null;
      const c = t.clock;
      const live = c.open && this.busy ? Math.max(0, Date.now() - Math.max(c.prev, c.open)) : 0;
      const until = this.busy && !this.prompts.size ? Date.now() : c.prev;
      return {
        ...t,
        ms: c.total + live,
        work: c.work + (this.prompts.size ? 0 : live),
        model: c.model + (c.open_model ? Math.max(0, until - c.open_model) : 0),
        tools: c.tools + (c.open_tool ? Math.max(0, until - c.open_tool) : 0),
        speed: t.speed.ms ? { ...t.speed, tps: t.speed.out / (t.speed.ms / 1000) } : null,
      };
    }
    // Older daemons without session.totals still supply the complete log.
    const { spend, split, speed, cache } = this.tally(evs);
    if (!spend) return null;
    // A request or tool still open counts on to now, or stops at the log's
    // last record when a prompt holds the session: nobody's time then.
    const until = this.clockUntil(evs);
    const model = split.model + (split.openModel !== null ? Math.max(0, until - split.openModel) : 0);
    const tools = Math.max(0, split.tools + (split.openTool !== null ? Math.max(0, until - split.openTool) : 0));
    return { ...spend, ...this.turnClocks(evs), model, tools, speed, cache };
  }
  // turnClocks is how long the task ran and worked (walkTurns), and a turn
  // still here in a busy session counts up to now — running time always,
  // working time only while no prompt holds it, a person's wait being
  // neither. A turn the log leaves open in a task nothing runs in was cut
  // off by a crash: it ends where its log does, and does not count up to
  // now.
  turnClocks(evs) {
    const { total, work, open, prev } = this.tally(evs).run;
    if (open === null || !this.busy) return { ms: total, work };
    const live = Math.max(0, Date.now() - Math.max(prev ?? open, open));
    return { ms: total + live, work: this.prompts.size ? work : work + live };
  }
  // tally is the log added up. Totals redraws on every event and ticks
  // once a second, so the log is walked once per new event (events() hands
  // out one list until one arrives), not on every redraw and tick.
  tally(evs) {
    if (this.tallied?.evs !== evs) this.tallied = { evs, spend: spendOf(evs), run: walkTurns(evs), split: splitTime(evs), speed: speedOf(evs), cache: cacheOf(evs) };
    return this.tallied;
  }

  // clockUntil is when a pair still open may count to: now while the
  // session works and no prompt holds it, the log's last record otherwise —
  // the rule turnClocks applies to a running turn's working time.
  clockUntil(evs) {
    if (this.busy && !this.prompts.size) return Date.now();
    return evs.length ? tsOf(evs.at(-1)) : 0;
  }
  // tickTotals redraws the running and working time while the task works;
  // the second tick that moves the turn's meters moves these with it.
  tickTotals() {
    const t = this.totals();
    const panel = t && this.el?.side.querySelector("[data-totals]");
    if (!panel) return;
    const run = panel.querySelector("[data-running]");
    const worked = panel.querySelector("[data-working]");
    const waited = panel.querySelector("[data-model]");
    const tooled = panel.querySelector("[data-tools]");
    if (run) run.textContent = spanWords(t.ms);
    if (worked) worked.textContent = spanWords(t.work);
    if (waited) waited.textContent = spanWords(t.model);
    if (tooled) tooled.textContent = spanWords(t.tools);
  }
  changesPanel() {
    const approvals = new Map();
    const seen = new Set();
    const decisions = this.conn.has("session.totals") ? this.totalSnapshot?.changes || [] : this.events().filter((e) => e.kind === "guard.decision").map((e) => e.payload);
    for (const p of decisions) {
      const allowed = p.decision === "allow" || (p.decision === "confirm" && (p.answer === "yes" || p.answer === "turn"));
      if (p.path && allowed) {
        seen.add(p.path);
        approvals.delete(p.path);
        approvals.set(p.path, p);
      }
    }
    const written = [...approvals.keys()];
    const pending = [...this.prompts.values()].map((e) => e.payload?.Prompt?.Path).filter((path) => path && !seen.has(path));
    if (!written.length && !pending.length) return null;
    const rows = [
      ...pending.map((path) => h("div", { class: "file-row" }, icon("file", 15), h("span", { class: "mono", title: path }, path), h("span", { class: "badge" }, "proposed"))),
      ...written.slice(-12).reverse().flatMap((path) => {
        const row = h(
          "div",
          { class: "file-row" },
          icon("file", 15),
          h("button", { type: "button", class: "btn small ghost change-toggle mono", title: path, "aria-expanded": String(this.changeOpen.has(path)), onclick: () => { this.changeOpen.has(path) ? this.changeOpen.delete(path) : this.changeOpen.add(path); this.aside(); } }, path),
          h("span", { class: "badge plain" }, "approved"),
          // In git too: a file git cannot give back keeps a version (ADR 35).
          this.info && this.conn.has("versions.list")
            ? h("button", { type: "button", class: "btn small ghost", style: "margin-left:auto", onclick: () => this.history(path) }, "History")
            : null,
        );
        return [row, this.changeOpen.has(path) ? this.changeDiff(approvals.get(path).detail, "Approved preview—not proof of a successful write") : null, h("div", { class: "versions" }, this.historyList(path))];
      }),
    ];
    const note = written.length > 12 ? `${written.length} files · latest shown` : this.info?.git ? "git and kept versions are the undo" : this.info ? "previous versions kept" : "writes nodwyn was allowed";
    return h("section", { class: "panel" }, h("h2", {}, "Changes", h("span", { class: "note" }, note)), rows);
  }
  changeDiff(detail, label) {
    if (!detail) return h("p", { class: "hint change-inspector" }, "Preview unavailable: this approval did not record a diff.");
    // The host's "too large" note is a line of its own, while every line of a
    // diff's content starts with a space, + or -: matching the text anywhere
    // would hide the diff of a file that mentions it.
    if (detail.split("\n").includes("(diff too large to show)") || detail.length > 262144) return h("p", { class: "hint change-inspector" }, label, " · Diff too large to show.");
    if (!parseDiff(detail).some((row) => row.kind === "add" || row.kind === "del")) return h("p", { class: "hint change-inspector" }, label, " · No content differences.");
    return h("div", { class: "change-inspector" }, diffView(detail, label));
  }
  async compareVersion(path, hash) {
    const key = JSON.stringify([path, hash]);
    const asked = {};
    const version = this.version;
    const history = this.histories.get(path);
    this.comparisons.set(key, asked);
    this.aside();
    let answer;
    try {
      answer = await this.conn.call("versions.diff", { repo: this.item.repo, path, hash });
    } catch (error) {
      answer = { error: error.message };
    }
    // A reply belongs only to the task, open History and request that asked.
    if (version !== this.version || this.histories.get(path) !== history || this.comparisons.get(key) !== asked) return;
    this.comparisons.set(key, answer);
    this.aside();
  }
  comparison(path, hash) {
    const answer = this.comparisons.get(JSON.stringify([path, hash]));
    if (!answer) return null;
    if (answer.error) return h("p", { class: "error", role: "status" }, answer.error);
    if (!answer.label) return h("p", { class: "hint", role: "status" }, "Comparing with current file…");
    return this.changeDiff(answer.diff, answer.label);
  }
  // toolsPanel lists where this task's tools come from, when some come from
  // MCP servers.
  toolsPanel() {
    const servers = this.info?.mcp || [];
    if (!servers.length) return null;
    const builtin = (this.info.tools || []).filter((t) => !t.name.includes("__")).length;
    const words = { ready: (s) => `${s.tools.length} tool${s.tools.length === 1 ? "" : "s"}`, unapproved: () => "not reviewed", changed: () => "changed: off", unavailable: () => "unavailable", error: () => "unavailable" };
    const where = (s) => (s.transport === "container" ? `contained · network: ${s.network?.length ? s.network.join(", ") : "none"}` : `HTTP · ${s.host}`);
    return h(
      "section",
      { class: "panel" },
      h("h2", {}, "Tools in this task", h("a", { href: "#/connections", class: "note" }, "Manage")),
      h("div", { class: "kv" }, h("span", {}, "nodwyn · built in"), h("span", {}, `${builtin} tools`)),
      servers.map((s) => h("div", { class: "kv", title: s.detail || "" }, h("span", {}, `${s.name} · ${where(s)}`), h("span", {}, (words[s.state] || (() => s.state))(s)))),
    );
  }
  attachmentsPanel() {
    const attached = this.conn.has("session.totals") ? this.totalSnapshot?.attachments || [] : this.events().filter((e) => e.kind === "attach").map((e) => e.payload);
    if (!attached.length) return null;
    return h(
      "section",
      { class: "panel" },
      h("h2", {}, "Attachments"),
      h("div", { class: "attach-list" }, attached.map((a) => attachmentChip(a))),
      h("p", { class: "hint" }, "Read as data. Nothing inside an attachment can grant a permission."),
    );
  }
  notesPanel() {
    if (this.notes === null) return null;
    const canEdit = this.item.open && this.conn.has("session.notes.write") && !this.busy;
    const edit = canEdit && this.notesDraft === null ? h("button", { type: "button", onclick: () => { this.notesDraft = this.notes; this.aside(); } }, "Edit") : null;
    const panel = h("section", { class: "panel" }, h("h2", {}, "What nodwyn knows here", edit));
    if (this.notesDraft !== null) {
      const area = h("textarea", { class: "input", rows: 8, spellcheck: true, "aria-label": "Project notes", dataset: { notes: "1" }, oninput: (event) => (this.notesDraft = event.target.value) });
      area.value = this.notesDraft;
      panel.append(
        area,
        h(
          "div",
          { class: "row-actions" },
          h("button", { type: "button", class: "btn small dark", onclick: () => this.saveNotes(this.notesDraft) }, "Save"),
          h("button", { type: "button", class: "btn small ghost", onclick: () => { this.notesDraft = null; this.aside(); } }, "Cancel"),
        ),
      );
    } else if (this.notes.trim()) panel.append(h("div", { class: "box" }, h("pre", {}, this.notes.trim())));
    else panel.append(h("p", { class: "hint" }, "No notes yet. nodwyn keeps notes about this folder across tasks when it learns something worth keeping."));
    panel.append(h("p", { class: "hint" }, "Notes nodwyn kept from earlier tasks. It checks them; it doesn't take them as instructions."));
    return panel;
  }
  async saveNotes(text) {
    const item = this.item;
    try {
      const started = await this.conn.call("session.notes.write", { id: item.id, text });
      this.expect(started.operation_id, (result) => {
        if (result.Err) toastError(`Notes not saved: ${result.Err}`);
        else {
          this.notesDraft = null;
          toast("Notes saved.");
          this.loadNotes();
        }
      });
    } catch (error) {
      toastError(error.message);
    }
  }
  workspacePanel() {
    const item = this.item;
    const tools = this.info?.tools?.length;
    const ws = this.info?.workspace;
    const named = this.desk.workspace(this.desk.project(item));
    const rows = [h("div", { class: "kv" }, h("span", { class: "mono", title: item.repo }, shortPath(item.repo, this.desk.home)), h("span", {}, "write"))];
    for (const m of ws?.mounts || named?.mounts || []) rows.push(h("div", { class: "kv" }, h("span", { class: "mono", title: m.path }, shortPath(m.path, this.desk.home)), h("span", {}, m.access === "write" ? "write" : "read only")));
    if (tools) rows.push(h("div", { class: "kv" }, h("span", {}, "Tools"), h("span", {}, `${tools} offered to the model`)));
    // Retrieval (ADR 50): what the model's searches of the repository read
    // from, and whether it may search this folder's other tasks, which a
    // person turns on for the folder on Workspaces.
    if (this.index) rows.push(h("div", { class: "kv" }, h("span", {}, "Search index"), h("span", {}, this.index.files ? `${this.index.files} files · ${sizeOf(this.index.bytes)} · ${ago(this.index.updated)}` : "built at the first search")));
    rows.push(h("div", { class: "kv", title: "Whether the model may search this folder's other tasks: turn it on or off for the folder on Workspaces." }, h("span", {}, "Earlier tasks"), h("span", { class: "earlier-tasks" }, named?.earlier_tasks ? "searched by the model" : "not searched")));
    if (this.info?.allowed_writes?.length) rows.push(h("div", { class: "kv" }, h("span", {}, "Writes without asking"), h("span", { class: "mono" }, this.info.allowed_writes.join(", "))));
    if (this.info?.allowed_commands?.length) rows.push(h("div", { class: "kv" }, h("span", {}, "Commands without asking"), h("span", {}, `${this.info.allowed_commands.length} prefixes`)));
    const w = item.worktree;
    if (w) {
      rows.push(h("div", { class: "kv" }, h("span", {}, "Branch"), h("span", { class: "mono" }, w.branch)));
      rows.push(h("div", { class: "kv" }, h("span", {}, "From"), h("span", { class: "mono" }, w.base ? `${w.base} at ${w.base_commit.slice(0, 7)}` : w.base_commit.slice(0, 7))));
    }
    const worktree = w ? `This task works in a git worktree of its own, so other tasks and you can work in ${this.desk.folderName(w.repository)} meanwhile. Its changes stay on ${w.branch} until it is merged${w.base ? ` into ${w.base}` : ""}; closing the task removes the worktree only when nothing in it would be lost.` : "";
    const undo = this.info ? (this.info.git ? "A git repository: git is the undo for what it has committed, and nodwyn keeps the previous version of anything else before it writes. Restore it from Changes." : "Not a git repository, so nodwyn keeps the previous version of a file before it writes. Restore it from Changes.") : "";
    return h("section", { class: "panel" }, h("h2", {}, "Workspace"), rows, worktree ? h("p", { class: "hint" }, worktree) : null, undo ? h("p", { class: "hint" }, undo) : null);
  }
  // Review shares the centre inspector, not its snapshot semantics: events
  // redraw it and an idle task refreshes the diff without reopening it.
  openReview() {
    if (!this.el || !this.item?.worktree || !this.conn.has("worktree.diff")) return;
    const first = !this.review;
    this.review ||= { comments: [], composing: null, draft: "", note: "", loading: false, mode: "unified", scroll: 0, positions: new Map(), focus: null };
    this.inspecting = "review";
    this.el.transcript.hidden = true;
    this.el.inspector.hidden = false;
    this.el.inspector.classList.add("review-view");
    this.controls();
    this.aside(true);
    // A review kept across a reload of the task is read again.
    if ((first || this.review.refresh) && this.item.open) {
      this.review.refresh = false;
      this.loadReview();
    }
  }
  // Nested file diffs and the centre view each keep their reading position.
  // Selection is recorded as it changes, since clicking Close takes focus
  // away before its handler can save the caret in an unfinished draft.
  rememberReview() {
    const state = this.review;
    if (!state || this.inspecting !== "review") return;
    const view = this.el.inspector;
    if (!view.querySelector(".review")) return;
    state.scroll = view.scrollTop;
    for (const body of view.querySelectorAll(".review-file .diff-body")) state.positions.set(body.dataset.path, [body.scrollTop, body.scrollLeft]);
    const field = view.querySelector("textarea:focus");
    if (field) state.focus = { name: field.dataset.notes, caret: [field.selectionStart, field.selectionEnd, field.scrollTop] };
  }
  drawReview(focusDraft = false) {
    if (this.inspecting !== "review" || !this.el) return;
    const state = this.review;
    // Every recorded event redraws the side panel, and this with it: a diff of
    // thousands of lines, a selection in it and the reading place are not
    // thrown away for an event that changed none of what it shows. A draft
    // being typed is not in the signature: its field keeps its own text.
    const shown = JSON.stringify([state.filesId, state.base, state.omitted, state.mode, state.composing, state.comments, state.loading, state.error, state.sending, this.working(), this.item.open, this.item.worktree?.base, this.acting, this.desk.keysLocked]);
    if (!focusDraft && shown === state.shown) return;
    state.shown = shown;
    this.rememberReview();
    const view = this.el.inspector;
    const active = view.contains(document.activeElement) ? document.activeElement : null;
    const control = active?.getAttribute("aria-label");
    const focus = state.focus && { name: state.focus.name, caret: [...state.focus.caret] };
    view.replaceChildren(
      h("div", { class: "bar review-bar" }, h("h2", {}, "Review changes"), h("button", { type: "button", class: "btn small review-close", "aria-label": "Close review", onclick: () => this.closeInspector() }, icon("cross", 14), "Close review")),
      this.reviewPanel(),
    );
    view.scrollTop = state.scroll;
    for (const body of view.querySelectorAll(".review-file .diff-body")) {
      const position = state.positions.get(body.dataset.path);
      if (position) [body.scrollTop, body.scrollLeft] = position;
    }
    const field = focus && [...view.querySelectorAll("textarea[data-notes]")].find((area) => area.dataset.notes === focus.name);
    if (field) {
      if (focusDraft || active?.matches("textarea[data-notes]")) field.focus({ preventScroll: true });
      field.setSelectionRange(focus.caret[0], focus.caret[1]);
      field.scrollTop = focus.caret[2];
    }
    if (control && !active?.matches("textarea[data-notes]")) [...view.querySelectorAll("[aria-label]")].find((node) => node.getAttribute("aria-label") === control)?.focus({ preventScroll: true });
  }
  reviewLabel() {
    const n = this.review?.comments.length;
    return n ? `Review (${n})` : "Review";
  }
  // loadReview reads what the task changed since its base commit. A reply
  // belongs only to the task that asked, and only the latest read counts: a
  // turn that ends while one is under way asks again, and the older answer
  // must not replace the newer. A failed read keeps the diff already shown
  // rather than saying nothing changed.
  async loadReview() {
    const version = this.version;
    const state = this.review;
    if (!state || !this.item?.open || !this.item.worktree || !this.conn.has("worktree.diff")) return;
    const asked = (state.asked = (state.asked || 0) + 1);
    state.loading = true;
    this.aside();
    let answer;
    try {
      answer = await this.conn.call("worktree.diff", { id: this.item.id });
    } catch (error) {
      answer = { error: error.message };
    }
    if (version !== this.version || this.review !== state || state.asked !== asked) return;
    state.loading = false;
    state.error = answer.error || null;
    if (!answer.error) {
      state.filesId = (state.filesId || 0) + 1;
      state.files = answer.changes;
      state.base = answer.base;
      state.omitted = answer.omitted;
    }
    this.aside();
  }
  addComment(path, line, quote) {
    const state = this.review;
    const text = state.draft.trim();
    if (!text) return;
    // The line's text stays here, to notice when the line has changed by the
    // time the review is sent: only the number and the comment go to the task.
    state.comments.push({ path, line, quote, text });
    state.composing = null;
    state.draft = "";
    state.focus = null;
    this.aside();
  }
  // working is whether the task is answering something: a review is refused
  // meanwhile, so Send waits instead of failing after the click.
  working() {
    return this.busy || this.item?.run?.status === "running";
  }
  // sendReview sends every comment as one message to the task and clears them
  // once it is accepted; a refusal keeps them.
  async sendReview() {
    const state = this.review;
    if (!state?.comments.length || state.sending) return;
    state.sending = true;
    state.error = null;
    this.aside();
    try {
      await this.conn.call("worktree.review", { id: this.item.id, note: state.note.trim() || undefined, comments: state.comments.map(({ path, line, text }) => ({ path, line, text })) });
      state.comments = [];
      state.note = "";
      toast("Review sent.");
    } catch (error) {
      state.error = error.message;
    }
    state.sending = false;
    this.aside();
  }
  reviewPanel() {
    const item = this.item;
    const panel = h("section", { class: "panel review", "aria-label": "Worktree review" });
    // The changes are read from the worktree of an open task.
    if (!item.open) {
      panel.append(
        h("p", { class: "hint" }, "Resume this task to review its changes: they are read from its worktree while it is open."),
        h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn dark resume-review", disabled: !this.conn.has("session.resume") || this.desk.keysLocked || this.acting, onclick: async () => { if (await this.resume()) this.openReview(); } }, "Resume and review")),
      );
      return panel;
    }
    const state = this.review;
    panel.append(h("div", { class: "row-actions review-controls" },
      h("div", { class: "review-modes", role: "group", "aria-label": "Diff layout" }, [["unified", "Unified"], ["split", "Side-by-side"]].map(([mode, label]) => h("button", { type: "button", class: "btn small", "aria-label": label, "aria-pressed": String(state.mode === mode), onclick: () => { state.mode = mode; this.aside(); } }, label))),
      h("button", { type: "button", class: "btn small ghost", "aria-label": "Refresh review", disabled: state.loading, onclick: () => this.loadReview() }, "Refresh"),
    ));
    if (state.error) panel.append(h("p", { class: "error", role: "status" }, state.error));
    if (!state.files) {
      if (!state.error) panel.append(h("p", { class: "hint", role: "status" }, "Reading the changes…"));
      return panel;
    }
    const w = item.worktree;
    const total = state.files.length + state.omitted;
    panel.append(h("p", { class: "hint" }, `${total} file${total === 1 ? "" : "s"} changed since ${w.base ? `${w.base} at ` : ""}${state.base.slice(0, 7)}.`, state.omitted ? ` ${state.omitted} not shown: there are more than a review can list.` : ""));
    const statuses = { A: ["added", "ok"], M: ["modified", "plain"], D: ["deleted", "bad"] };
    // Diff content is session content: h() makes it text, never markup.
    const drawn = new Set();
    for (const file of state.files) {
      const body = h("div", { class: `diff-body ${state.mode === "split" ? "diff-split" : "diff-unified"}`, dataset: { path: file.path } });
      const rows = parseDiff(file.diff);
      const comment = (row, line, target) => {
        // Old-only deletions have no current-file position to send.
        if (!row?.new) return;
        const key = `${file.path}:${row.new}`;
        line.append(h("button", { type: "button", class: "add-comment", title: "Add a comment", "aria-label": `Comment on ${file.path} line ${row.new}`, onclick: () => { state.composing = key; state.draft = ""; state.focus = null; this.aside(); } }, "+"));
        for (const c of state.comments.filter((c) => c.path === file.path && c.line === row.new)) {
          drawn.add(c);
          target.append(this.commentCard(c, c.quote === row.text ? "" : "This line has changed since you commented on it."));
        }
        if (state.composing === key) target.append(this.commentForm("comment", () => this.addComment(file.path, row.new, row.text)));
      };
      if (state.mode === "split") {
        body.append(h("div", { class: "review-columns" }, h("span", {}, "Old"), h("span", {}, "New · current file")));
        const cell = (row, side) => h("div", { class: `diff-line ${side} ${row?.kind || "blank"}` }, h("span", { class: "n" }, String(row?.[side === "old" ? "old" : "new"] || "")), h("span", { class: "s" }, row?.sign || ""), h("span", { class: "t" }, row?.text || ""));
        for (const pair of splitDiff(rows)) {
          if (pair.note) {
            body.append(h("div", { class: `diff-line ${pair.note.kind}` }, h("span", { class: "t" }, pair.note.text)));
            continue;
          }
          const current = cell(pair.current, "new");
          const notes = h("div", { class: "review-line-notes" });
          const line = h("div", { class: "diff-pair" }, cell(pair.old, "old"), current, notes);
          comment(pair.current, current, notes);
          body.append(line);
        }
      } else {
        body.append(h("div", { class: "review-columns unified-columns" }, h("span", {}, "Old"), h("span", {}, "New"), h("span", {}, "Current-file comments")));
        for (const row of rows) {
          const line = h("div", { class: `diff-line ${row.kind}` }, h("span", { class: "n old", title: "Old line" }, String(row.old)), h("span", { class: "n new", title: "Current-file line" }, String(row.new)), h("span", { class: "s" }, row.sign), h("span", { class: "t" }, row.text));
          body.append(line);
          comment(row, line, body);
        }
      }
      const whole = state.comments.filter((c) => c.path === file.path && !c.line);
      for (const c of whole) drawn.add(c);
      const [word, tone] = statuses[file.status] || [file.status, "plain"];
      panel.append(h("div", { class: "diff review-file" }, h("div", { class: "diff-head" }, h("span", { class: "mono" }, file.path, " ", h("span", { class: `badge ${tone}` }, word)), h("button", { type: "button", class: "btn small ghost", onclick: () => { state.composing = file.path; state.draft = ""; state.focus = null; this.aside(); } }, "Comment on file")), body, whole.map((c) => this.commentCard(c, "")), state.composing === file.path ? this.commentForm("comment", () => this.addComment(file.path, 0, "")) : null));
    }
    if (!state.files.length) panel.append(h("p", { class: "hint" }, "This task has not changed anything since it branched."));
    // A comment whose file or line the diff no longer shows is still sent, so
    // it is listed where it can be read and removed.
    const orphans = state.comments.filter((c) => !drawn.has(c));
    if (orphans.length) panel.append(h("div", { class: "review-orphans" }, h("h3", {}, "Comments on lines the diff no longer shows"), orphans.map((c) => this.commentCard(c, `${c.path}${c.line ? `, line ${c.line}` : ""}`))));
    if (state.comments.length) {
      const note = h("textarea", { class: "input", rows: 2, "aria-label": "Overall note", placeholder: "Overall note (optional)", dataset: { notes: "note" }, oninput: (event) => (state.note = event.target.value) });
      note.value = state.note;
      panel.append(note, h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small dark", disabled: state.sending || this.working(), onclick: () => this.sendReview() }, `Send review (${state.comments.length})`), this.working() ? h("span", { class: "hint" }, "The task is working: send when it is idle.") : null));
    }
    panel.append(h("p", { class: "hint" }, "Comments go to the task together, as one message. To change direction, close review and write to the task instead."));
    for (const area of panel.querySelectorAll("textarea[data-notes]")) {
      const remember = () => { state.focus = { name: area.dataset.notes, caret: [area.selectionStart, area.selectionEnd, area.scrollTop] }; };
      for (const event of ["focus", "input", "select", "keyup", "click", "blur"]) area.addEventListener(event, remember);
    }
    return panel;
  }
  // commentCard draws a comment, with a line of what is wrong with where it
  // points when something is.
  commentCard(c, remark) {
    return h("div", { class: "review-comment" }, h("div", {}, remark ? h("div", { class: "hint" }, remark) : null, h("span", {}, c.text)), h("button", { type: "button", class: "btn small ghost", "aria-label": "Remove comment", onclick: () => { this.review.comments.splice(this.review.comments.indexOf(c), 1); this.aside(); } }, "Remove"));
  }
  // commentForm is the field a comment is written in. kind names it for
  // drawReview(), which keeps the caret in the field being typed in.
  commentForm(kind, save) {
    const state = this.review;
    const area = h("textarea", { class: "input", rows: 2, spellcheck: true, "aria-label": "Comment", dataset: { notes: kind }, oninput: (event) => (state.draft = event.target.value) });
    area.value = state.draft;
    return h("div", { class: "review-form" }, area, h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small dark", onclick: save }, "Add comment"), h("button", { type: "button", class: "btn small ghost", onclick: () => { state.composing = null; state.focus = null; this.aside(); } }, "Cancel")));
  }
  async history(path) {
    if (this.histories.has(path)) {
      this.histories.delete(path);
      for (const key of this.comparisons.keys()) if (JSON.parse(key)[0] === path) this.comparisons.delete(key);
      this.aside();
      return;
    }
    const version = this.version;
    const asked = {};
    this.histories.set(path, asked);
    this.aside();
    let answer;
    try {
      answer = { versions: await this.conn.call("versions.list", { repo: this.item.repo, path }) };
    } catch (error) {
      answer = { error: error.message };
    }
    // The list may have been closed, reopened or left for another task
    // while it was read: only the request that opened it now fills it.
    if (version !== this.version || this.histories.get(path) !== asked) return;
    this.histories.set(path, answer);
    this.aside();
  }
  // historyList draws an open History list from what versions.list answered,
  // with Restore as the task is now: a run refuses a restore.
  historyList(path) {
    const answer = this.histories.get(path);
    if (!answer) return null;
    if (answer.error) return h("p", { class: "error" }, answer.error);
    if (!answer.versions) return h("p", { class: "hint" }, "Reading kept versions…");
    if (!answer.versions.length) return h("p", { class: "hint" }, "No earlier version is kept: the file did not exist before, or git has it committed and is its undo.");
    const canRestore = this.item.open && this.conn.has("session.restore");
    return [
      ...answer.versions.slice(0, 8).map((v) =>
        h("div", { class: "version-entry" }, h(
          "div",
          { class: "kv version-row" },
          h("span", {}, `${clock(v.time)} · ${ago(v.time)} · ${v.bytes.toLocaleString()} bytes`),
          this.conn.has("versions.diff") ? h("button", { type: "button", class: "btn small ghost", onclick: () => this.compareVersion(path, v.hash) }, "Compare with current file") : null,
          canRestore ? h("button", { type: "button", class: "btn small", disabled: this.busy, onclick: () => this.restore(path, v.hash) }, "Restore") : h("span", { class: "mono" }, v.hash.slice(0, 12)),
        ), this.comparison(path, v.hash)),
      ),
      canRestore ? h("p", { class: "hint" }, "Restoring is a write like any other: it shows the change, and asks first only if writes ask (Settings › General › Approvals). The current content is kept too.") : h("p", { class: "hint" }, "Resume the task to restore a version."),
    ];
  }
  async restore(path, hash) {
    try {
      const started = await this.conn.call("session.restore", { id: this.item.id, path, hash });
      this.expect(started.operation_id, (result) => (result.Err ? toastError(`Not restored: ${result.Err}`) : toast(`Restored ${basename(path)}.`)));
    } catch (error) {
      toastError(error.message);
    }
  }
  detailsPanel() {
    const link = (label, kind) => h("button", { type: "button", class: "btn small", onclick: () => this.inspect(kind) }, label);
    return h("section", { class: "panel" }, h("h2", {}, "Details"), h("div", { class: "links" }, link("Log", "log"), link("Plan record", "plan"), link("Budget", "budget"), link("Policy", "policy"), link("Tools", "tools")));
  }

  // ---------- inspectors (U3), under Details ----------
  async inspect(kind, reread = false) {
    if (this.inspecting === "review") this.rememberReview();
    this.el.inspector.classList.remove("review-view");
    const version = this.version;
    this.inspecting = kind;
    const view = this.el.inspector;
    this.el.transcript.hidden = true;
    view.hidden = false;
    this.controls();
    const title = { log: "Log", plan: "Plan record", budget: "Budget", policy: "Policy", tools: "Tools" }[kind];
    const back = h("button", { type: "button", class: "btn small", onclick: () => this.closeInspector() }, icon("back", 14), "Conversation");
    const refresh = h("button", { type: "button", class: "btn small", onclick: () => this.inspect(kind, true) }, "Refresh view");
    const content = h("div");
    const filter = h("input", { class: "input", placeholder: "kind prefix, e.g. guard.", "aria-label": "Kind prefix" });
    const count = h("input", { class: "input", type: "number", min: 1, max: 1000, value: "50", "aria-label": "Maximum records", style: "width:110px" });
    const form = kind === "log" ? h("form", { onsubmit: (e) => { e.preventDefault(); draw(); } }, filter, count, h("button", { class: "btn small", type: "submit" }, "Filter")) : null;
    view.replaceChildren(...[h("div", { class: "bar" }, back, h("h2", {}, title), refresh), this.historyCursor > 1 ? h("p", { class: "hint" }, "Records from the latest run and its inherited setup. Open earlier runs in Conversation to read their records.") : null, form, content].filter(Boolean));
    if (kind === "policy" && this.policy && !reread) return this.showPolicy(content);
    const snapshot = { events: this.events(), info: null, infoError: "This task is not open. Resume it to see its current configuration.", truncated: this.truncated };
    if (this.item.open && ["budget", "policy", "tools"].includes(kind)) {
      try {
        snapshot.info = await this.conn.call("session.info", { id: this.item.id });
      } catch (error) {
        snapshot.infoError = error.message;
      }
    }
    if (version !== this.version || this.inspecting !== kind) return;
    const draw = () => {
      renderInspector(content, kind, snapshot, filter.value, Number(count.value));
      if (kind === "policy" && this.item.open && this.conn.has("session.policy.read") && this.conn.has("session.policy.write"))
        policyChoices(content, (name) => this.policyOperation(name, false, content));
    };
    draw();
  }
  closeInspector() {
    const review = this.inspecting === "review";
    if (review) this.rememberReview();
    this.inspecting = null;
    this.policy = null;
    this.el.inspector.hidden = true;
    this.el.transcript.hidden = false;
    this.el.inspector.classList.remove("review-view");
    this.controls();
    if (review) {
      this.aside();
      this.el.side.querySelector(".review-open")?.focus({ preventScroll: true });
    }
  }
  showPolicy(content) {
    renderPolicyEditor(
      content,
      this.policy,
      (name) => this.policyOperation(name, false, content),
      () => this.policyOperation(this.policy.name, true, content),
      () => {
        this.policy = null;
        this.inspect("policy", true);
      },
    );
  }
  async policyOperation(name, write, content) {
    if (!this.item.open || this.busy) return toast("Policy files can be edited in an open, idle task.");
    const previous = this.policy;
    const request = { name, doc: write ? previous.doc : null, draft: write ? previous.draft : "", error: "", saved: false };
    this.policy = request;
    this.showPolicy(content);
    const params = { id: this.item.id, name };
    if (write) Object.assign(params, { revision: request.doc.Revision, content: request.draft });
    try {
      const started = await this.conn.call(write ? "session.policy.write" : "session.policy.read", params);
      this.expect(started.operation_id, (result) => {
        if (this.policy !== request) return;
        request.error = result.Err;
        request.saved = result.Saved;
        if (!result.Err && !result.Saved) {
          request.doc = result;
          request.draft = result.Content;
        }
        if (this.inspecting === "policy") this.showPolicy(content);
      });
    } catch (error) {
      request.error = error.message;
      this.showPolicy(content);
    }
  }
  // expect pairs a guarded operation's result event with its handler; the
  // event can arrive before the call that started it returns.
  expect(id, handler) {
    const early = this.early.get(id);
    if (early) {
      this.early.delete(id);
      handler(early);
    } else this.operations.set(id, handler);
  }
  operationResult(result) {
    const handler = this.operations.get(result.ID);
    if (handler) {
      this.operations.delete(result.ID);
      handler(result);
    } else this.early.set(result.ID, result);
  }
}

function oneWord(detail) {
  const text = String(detail || "").replace(/^running /, "Running ");
  return text.length > 40 ? text.slice(0, 39) + "…" : text[0]?.toUpperCase() + text.slice(1);
}
function sameDay(when) {
  return new Date(when).toDateString() === new Date().toDateString();
}

export { summary, basename };

// Two shapes of figure fill the side panels. A meterBar is counted against a
// limit: its label, how far it has got, and a track filling towards the
// limit. A statRow has no limit behind it, so it is a label and a value and
// never a bar without a track.
function meterBar(label, text, used, soft, title = "") {
  return h(
    "div",
    { class: "meter", title: title || undefined },
    h("div", { class: "meter-head" }, h("span", {}, label), h("span", {}, text)),
    h("div", { class: "meter-track" }, h("div", { class: `meter-fill${used > soft ? " over" : ""}`, style: `width:${Math.min(100, (used / soft) * 100)}%` })),
  );
}
function statRow(label, value, attrs = {}, title = "") {
  return h("div", { class: "kv", title: title || undefined }, h("span", {}, label), h("span", { class: "stat-value", ...attrs }, value));
}
// limited is a figure as a bar where a limit is known and as a row where not.
function limited(label, used, soft, fmt = String) {
  return soft ? meterBar(label, `${fmt(used)} / ${fmt(soft)}`, used, soft) : statRow(label, fmt(used));
}

// spendOf is what the task's requests cost, as far as the log prices them:
// null before the first turn, when nothing has run to add up.
function spendOf(evs) {
  let cost = 0,
    unpriced = 0,
    estimated = false,
    started = false;
  for (const e of evs) {
    if (e.kind === "user.msg" || e.kind === "eval.req" || e.kind === "run.turn") started = true;
    if (e.kind !== "llm.resp") continue;
    const u = e.payload?.usage || {};
    // Absent is unknown, never free, as the spend limit counts it.
    if (u.cost !== undefined) {
      cost += u.cost;
      estimated ||= !!u.cost_estimated;
    } else unpriced++;
  }
  if (!started) return null;
  const j = judgeTally(evs);
  cost += j.cost;
  unpriced += j.unpriced;
  // A judge cost is an estimate from the judge's prices, never a report.
  estimated ||= j.cost > 0;
  return { cost, unpriced, estimated };
}

// cacheOf adds raw prompt tokens and cache reads over the replies that
// report both, so the rate is token-weighted across turns and models, not
// a mean of request rates or the budget's discounted token count. Unknown
// usage and replies without a positive prompt count cannot give a share.
function cacheOf(evs) {
  let prompt = 0,
    cached = 0;
  for (const e of evs) {
    if (e.kind !== "llm.resp") continue;
    const u = e.payload?.usage;
    if (!u || u.unreported || u.cached_tokens == null || !(u.prompt_tokens > 0)) continue;
    prompt += u.prompt_tokens;
    cached += u.cached_tokens;
  }
  return { prompt, cached };
}

// speedOf is how fast the task's models answered: output tokens per second
// of wall time, added up over the replies that reported tokens — Σ tokens
// over Σ seconds, the speed the session experienced, not a mean of
// per-request speeds. A request's wall time is its own record (wall_ms,
// newer logs) or the log's timestamps from the request to the reply
// (older logs); a reply with neither, or with no tokens reported, stays
// out, so an open request counts no speed yet.
function speedOf(evs) {
  let out = 0,
    ms = 0,
    replies = 0;
  let open = null;
  for (const e of evs) {
    const ts = tsOf(e);
    if (e.kind === "llm.req") {
      if (ts > 0) open = ts;
    } else if (e.kind === "llm.resp") {
      const u = e.payload?.usage || {};
      let wall = u.wall_ms || 0;
      if (!wall && open !== null && ts > open) wall = ts - open;
      open = null;
      if (wall > 0 && !u.unreported && u.output_tokens > 0) {
        out += u.output_tokens;
        ms += wall;
        replies++;
      }
    }
  }
  if (!ms) return null;
  return { out, ms, replies, tps: out / (ms / 1000) };
}

// tsOf is a record's time in milliseconds; a record without one is time 0.
const tsOf = (e) => new Date(e.ts || e.TS || 0).getTime();

// waitedOn says whether a record ends a wait for a person's answer: the
// judge and an unattended run answer their confirms themselves, so their
// span is the session working, not a person being asked.
function waitedOn(e) {
  const p = e.payload || {};
  if (p.actor === "judge" || p.actor === "unattended") return false;
  if (e.kind === "guard.decision") return p.decision === "confirm";
  if (e.kind === "turn.checkpoint") return true;
  // run.checkpoint, cred.register and setting.change end a prompt's
  // wait only when a prompt was asked (prompt_id); without one they
  // record a decision made without asking.
  return !!p.prompt_id;
}

// walkTurns walks the log as the session lived it: how long its turns ran
// all in (total) and how long they ran without a person's wait (work), plus
// the start of a turn still open at its end (open, else null) with the time
// of its last event (prev). A turn runs from its start to its turn.end; a
// run's next turn bridges the gap between, when the run went on by itself.
// Prompts are never logged (they are ephemeral events): a wait is known
// only from the record that ended it — a guard decision, a checkpoint — so
// it runs from the event before that record to the record itself, which is
// the ask plus the answer; work leaves that interval out, total counts it.
// A turn a crash cut off has no turn.end: the session's next start
// (session.resume, or a session.end) ends it, and the time before that,
// when nothing ran, is nobody's.
function walkTurns(evs) {
  let total = 0,
    work = 0,
    prev = null,
    open = null; // open: the running turn's start
  let gap = null,
    gapWaited = false,
    gapResumed = false; // between turns
  for (const e of evs) {
    const ts = tsOf(e);
    const cut = open !== null && (e.kind === "session.resume" || e.kind === "session.end");
    if (prev !== null && ts > prev && open !== null && !cut) {
      const span = ts - prev;
      total += span;
      if (!waitedOn(e)) work += span;
    }
    if (cut) {
      open = null;
      gap = null;
    }
    if (open === null && gap !== null) {
      if (waitedOn(e)) gapWaited = true;
      // A run brought back by a person, or one that ended, leaves the gap
      // before it to the person who read and decided.
      if (e.kind === "session.resume" || e.kind === "run.state") gapResumed = true;
      if (e.kind === "run.checkpoint" && e.payload?.allowed === false) gap = null;
    }
    if (e.kind === "turn.end") {
      open = null;
      gap = ts;
      gapWaited = false;
      gapResumed = false;
    } else if (e.kind === "user.msg" || e.kind === "eval.req" || e.kind === "run.turn") {
      // A run that went on by itself ran through the gap between its turns.
      if (open === null && gap !== null && e.kind === "run.turn" && !gapWaited && !gapResumed) {
        const span = Math.max(0, ts - gap);
        total += span;
        work += span;
      }
      gap = null;
      open = ts;
    }
    prev = ts;
  }
  return { total, work, open, prev };
}

// splitTime adds a stretch of the log up by what the session was doing in
// it: model is the time from each request to a model to its reply, tools
// the time from each tool.call to its tool.result, with the wait for a
// person's approval taken back out — the ask and the answer sit between a
// call and its result, and work leaves them out the same way. A request
// and its reply are paired in order, a result with its own call by id;
// stretches that overlap are merged, so a pair inside another is not
// counted twice. A pair still open at the stretch's end is not counted
// here: its start comes back as openModel or openTool (null when none),
// and the caller, which knows whether the session still works, counts it
// on to now. A pair a turn left behind — a request whose reply never
// came, a tool its turn outlived — is dropped at the turn's end and the
// session's resume and end: the time a turn that ended left open is
// nobody's, the rule walkTurns applies to a turn it cut off.
function splitTime(evs) {
  const model = [],
    tools = [],
    waits = [];
  const open = []; // requests to models waiting for their reply, in order
  const calls = new Map(); // tool calls waiting for their result, by id
  let prev = null;
  for (const e of evs) {
    const ts = tsOf(e);
    if (e.kind === "llm.req") {
      // Requests are sequential: a second one cannot begin while a first
      // waits, so anything still open here never got its reply — a failed
      // request records no reply (model.go records a note instead) — and
      // its wait ended where nobody recorded it. It is nobody's time, not
      // time up to now.
      open.length = 0;
      if (ts > 0) open.push(ts);
    } else if (e.kind === "llm.resp") {
      const start = open.pop();
      if (start !== undefined && ts > start) model.push([start, ts]);
    } else if (e.kind === "tool.call") {
      if (ts > 0) calls.set(e.payload?.id, ts);
    } else if (e.kind === "tool.result") {
      const start = calls.get(e.payload?.id);
      calls.delete(e.payload?.id);
      if (start !== undefined && ts > start) tools.push([start, ts]);
    } else if (e.kind === "turn.end" || e.kind === "session.resume" || e.kind === "session.end") {
      // The turn is over: a request it never answered or a tool it never
      // finished is nobody's time, the rule walkTurns applies to a turn a
      // crash cut off. What the next turn opens counts from itself, and a
      // pair of the turn that is still running here is untouched.
      open.length = 0;
      calls.clear();
    } else if (prev !== null && ts > prev && waitedOn(e)) waits.push([prev, ts]);
    prev = ts;
  }
  // A span that begins inside another extends it instead of adding a
  // second count of the same time.
  const merge = (spans) => {
    const out = [];
    for (const s of spans.sort((a, b) => a[0] - b[0])) {
      const last = out[out.length - 1];
      if (last && s[0] <= last[1]) last[1] = Math.max(last[1], s[1]);
      else out.push([...s]);
    }
    return out;
  };
  const sum = (spans) => spans.reduce((n, s) => n + s[1] - s[0], 0);
  // What two merged, sorted lists both count: the part of a tool's stretch
  // that was the wait for an approval.
  const overlap = (a, b) => {
    let n = 0,
      i = 0,
      j = 0;
    while (i < a.length && j < b.length) {
      const from = Math.max(a[i][0], b[j][0]),
        to = Math.min(a[i][1], b[j][1]);
      if (to > from) n += to - from;
      if (a[i][1] < b[j][1]) i++;
      else j++;
    }
    return n;
  };
  const ran = merge(tools);
  return {
    model: sum(merge(model)),
    tools: sum(ran) - overlap(ran, merge(waits)),
    openModel: open.length ? open[open.length - 1] : null,
    openTool: calls.size ? Math.min(...calls.values()) : null,
  };
}
