// The task view: one session's conversation with its approval inline, and
// beside it the plan, this turn's budget, the changes, the project notes and
// the workspace. Every figure comes from the log or from session.info; a
// panel with no source says so instead of guessing.
import { h, icon, repoName, shortPath, clock, minutes, ago, toast, basename } from "./dom.js";
import { Transcript, planSteps } from "./transcript.js";
import { askCard, shortcut, summary } from "./prompts.js";
import { kindLabel } from "./home.js";
import { Attachments, attachmentChip } from "./attach.js";
import { renderInspector } from "./inspectors.js";
import { policyChoices, renderPolicyEditor } from "./policy.js";
import { keychainForm } from "./setup.js";

const drafts = new Map(); // per task, this tab only

export class TaskView {
  constructor(conn, desk, navigate) {
    this.conn = conn;
    this.desk = desk;
    this.navigate = navigate;
    this.item = null;
    this.version = 0;
    this.loading = false;
    this.queued = [];
    this.seen = new Map();
    this.prompts = new Map(); // live prompt events by ID, in arrival order
    this.resolved = new Set();
    this.info = null;
    this.notes = null;
    this.notesEditing = false;
    this.inspecting = null;
    this.operations = new Map(); // guarded operation id -> handler
    this.early = new Map(); // results that arrived before their id
    this.busy = false;
    this.acting = false;
    this.timer = 0;
    this.root = h("div", { class: "task" });
    conn.onEvent((frame) => this.onFrame(frame));
    document.addEventListener("keydown", (event) => {
      if (!this.root.isConnected || this.inspecting || !this.item || this.loading) return;
      shortcut(this.el?.transcript.querySelector(".ask-card"), event);
    });
  }

  // ---------- lifecycle ----------
  async open(id) {
    const version = ++this.version;
    const previous = this.item;
    if (previous) drafts.set(previous.id, this.el?.reply.value || "");
    if (previous?.open && previous.id !== id) this.conn.call("session.unsubscribe", { id: previous.id }).catch(() => {});
    let item = this.desk.sessions.get(id);
    if (!item) {
      await this.desk.refresh().catch(() => {});
      if (version !== this.version) return;
      item = this.desk.sessions.get(id);
    }
    if (!item) {
      this.root.replaceChildren(h("div", { class: "page" }, h("div", { class: "page-col" }, h("h1", {}, "No such task"), h("p", { class: "lede" }, "It may have been started by a daemon that is no longer running."), h("div", {}, h("a", { class: "btn", href: "#/" }, "Home")))));
      return;
    }
    this.item = item;
    this.loading = true;
    this.queued = [];
    this.seen = new Map();
    this.prompts = new Map();
    this.resolved = new Set();
    this.info = null;
    this.notes = null;
    this.notesEditing = false;
    this.inspecting = null;
    this.operations.clear();
    this.early.clear();
    this.busy = !!item.busy;
    this.build();
    this.el.reply.value = drafts.get(id) || "";
    this.header();
    this.aside();
    this.controls();
    try {
      let page,
        pending = [],
        busy = item.busy;
      if (item.open) {
        const snapshot = await this.conn.call("session.subscribe", { id, after: 0, limit: 500 });
        if (version !== this.version) return;
        page = snapshot.page;
        pending = snapshot.pending || [];
        busy = snapshot.busy;
      } else page = await this.conn.call("log.read", { id, after: 0, limit: 500 });
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
      for (const event of pending) this.render(event);
      for (const event of this.queued) this.render(event);
      this.queued = [];
      if (item.open) this.loadInfo();
      this.loadNotes();
      if (this.transcript.empty) this.el.transcript.prepend(h("p", { class: "hint", dataset: { placeholder: "1" } }, item.open ? "Nothing yet. Say what you want done." : "This task has no conversation."));
      this.el.transcript.scrollTop = this.el.transcript.scrollHeight;
      this.refreshAll();
    } catch (error) {
      if (version !== this.version) return;
      // A failed load must not enable sending against uncertain state.
      this.loading = true;
      this.queued = [];
      toast(error.message);
      this.controls();
    }
  }
  leave() {
    if (this.item) drafts.set(this.item.id, this.el?.reply.value || "");
    if (this.item?.open) this.conn.call("session.unsubscribe", { id: this.item.id }).catch(() => {});
    this.version++;
    this.item = null;
    clearInterval(this.timer);
  }
  async loadInfo() {
    const version = this.version;
    try {
      const info = await this.conn.call("session.info", { id: this.item.id });
      if (version === this.version) {
        this.info = info;
        this.aside();
        this.header();
      }
    } catch {
      /* a closed session has no current details */
    }
  }
  async loadNotes() {
    if (!this.conn.has("notes.read") || !this.item?.repo) return;
    const version = this.version;
    try {
      const notes = await this.conn.call("notes.read", { repo: this.item.repo });
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
    if (frame.method === "session.closed" && frame.params?.id === this.item.id) {
      this.item.open = false;
      this.busy = false;
      this.prompts.clear();
      this.refreshAll();
      toast("This task was closed. Its conversation is kept.");
      return;
    }
    if (frame.method !== "session.event" || frame.params?.id !== this.item.id || frame.params.kind === "session.state") return;
    if (this.loading) this.queued.push(frame.params);
    else {
      this.render(frame.params);
      this.refreshAll();
    }
  }
  render(event) {
    if (event.seq && this.seen.has(event.seq)) return;
    // Live notifications carry no timestamp; the arrival time stands in.
    if (event.seq && !event.ts) event.ts = new Date().toISOString();
    if (event.seq) this.seen.set(event.seq, event);
    const p = event.payload || {};
    const view = this.el.transcript;
    const follow = view.scrollHeight - view.scrollTop - view.clientHeight < 120;
    view.querySelector(":scope > [data-placeholder]")?.remove();
    if (p.prompt_id && ["guard.decision", "turn.checkpoint", "cred.register", "run.checkpoint"].includes(event.kind)) this.resolve(p.prompt_id);
    switch (event.kind) {
      case "prompt.confirm":
      case "prompt.secret":
      case "prompt.checkpoint":
        if (!this.resolved.has(p.ID)) {
          this.prompts.set(p.ID, event);
          if (!this.desk.firstSeen.has(p.ID)) this.desk.firstSeen.set(p.ID, Date.now());
          this.busy = true;
        }
        break;
      case "status": {
        const state = p.State ?? p.state;
        if (state === "idle") {
          this.busy = false;
          this.transcript.end();
          for (const id of this.prompts.keys()) this.resolved.add(id);
          this.prompts.clear();
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
      toast(`${error.message} Nothing was retried; check the conversation before answering again.`);
      // Never reuse an uncertain prompt or keep typed secrets after an error.
      if (version === this.version && this.conn.live) this.open(item.id);
    } finally {
      this.acting = false;
      this.controls();
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
    const form = h(
      "form",
      { class: "reply", onsubmit: (event) => { event.preventDefault(); this.send(); } },
      h("label", { class: "sr-only", for: "reply" }, "Reply to nodwyn"),
      h("div", { class: "reply-body" }, this.files.list, reply),
      this.files.button(),
      send,
    );
    const main = h("div", { class: "task-main" }, transcript, form);
    const inspector = h("div", { class: "inspector-view", hidden: true });
    main.append(inspector);
    const side = h("aside", { class: "task-aside", "aria-label": "Task details" });
    const head = h("header", { class: "task-head" });
    this.root.replaceChildren(head, h("div", { class: "task-body" }, main, side));
    this.el = { head, transcript, reply, send, form, side, inspector, main };
    this.transcript = new Transcript(transcript);
  }
  controls() {
    if (!this.el) return;
    const open = !!this.item?.open;
    const prompt = !!this.activePrompt();
    const canSend = this.conn.has("session.send") && open && !this.loading && !this.acting;
    this.el.form.hidden = !this.conn.has("session.send") || !!this.inspecting;
    this.el.reply.disabled = !canSend;
    this.el.reply.placeholder = !open
      ? "This task is saved. Resume it to continue."
      : prompt
        ? "Answer the request above first."
        : this.busy
          ? "Working… stop it to steer."
          : "Steer it, or answer in words…";
    this.el.send.disabled = !canSend || this.busy || prompt || !this.el.reply.value.trim();
    for (const b of this.el.form.querySelectorAll("[aria-label='Attach a file']")) b.disabled = !canSend || this.busy || prompt;
    for (const control of this.el.transcript.querySelectorAll(".ask-card button, .ask-card input")) control.disabled = this.acting || !this.conn.live;
  }
  async send() {
    if (this.el.send.disabled) return;
    const item = this.item,
      text = this.el.reply.value,
      version = this.version;
    this.acting = true;
    this.busy = true;
    this.controls();
    try {
      if (this.files.files.length) await this.files.upload(item.id);
      await this.conn.call("session.send", { id: item.id, text });
      if (drafts.get(item.id) === text) drafts.delete(item.id);
      if (version === this.version && this.el.reply.value === text) {
        this.el.reply.value = "";
        this.el.reply.style.height = "auto";
      }
    } catch (error) {
      // A failed reply is not evidence of failed delivery: reload first.
      toast(`${error.message} Nothing was retried; check the conversation before sending again.`);
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
    if (this.busy) this.timer = setInterval(() => this.meters(), 1000);
  }
  status() {
    const item = this.item;
    if (!item) return { label: "", cls: "" };
    if (this.activePrompt()) return { label: "Waiting for you", cls: "warn" };
    if (item.run?.status === "running") return { label: `${item.run.unattended ? "Unattended run" : "Run"}${item.run.judge ? ", judged" : ""} · turn ${item.run.turns} of ${item.run.max_turns}`, cls: "run" };
    if (this.busy) return { label: this.statusDetail && this.statusDetail !== "busy" ? oneWord(this.statusDetail) : "Working", cls: "run" };
    if (item.open) return { label: "Idle", cls: "" };
    return { label: "Saved", cls: "" };
  }
  header() {
    if (!this.el || !this.item) return;
    const item = this.item;
    const home = this.desk.home;
    const st = this.status();
    const actions = h("div", { class: "task-actions" });
    const button = (label, iconName, handler, cls = "") => h("button", { type: "button", class: `btn ${cls}`, disabled: !this.conn.live || this.acting, onclick: handler }, iconName ? icon(iconName, 15) : null, label);
    if (item.run?.status === "running" && this.conn.has("run.stop")) actions.append(button("Pause", "pause", () => this.act("run.stop", "Pausing the run after this step…")));
    else if (item.open && item.run && item.run.status !== "running" && item.run.status !== "completed" && this.conn.has("run.resume")) actions.append(button("Resume run", "up", () => this.act("run.resume", "Resuming the run…")));
    if (item.open && this.conn.has("session.abort")) {
      const stop = button("Stop", "stop", () => this.act("session.abort", "Stopping…"));
      stop.disabled = stop.disabled || !this.busy;
      actions.append(stop);
    }
    const keysLocked = !item.open && this.desk.keysLocked;
    if (!item.open && this.conn.has("session.resume")) {
      const resume = button("Resume", "up", () => this.resume(), "dark");
      resume.disabled = resume.disabled || keysLocked;
      actions.append(resume);
    }
    if (item.open && this.conn.has("session.close")) {
      const close = button("Close", "", () => this.act("session.close", "Closing the task; its conversation is kept."), "ghost");
      close.disabled = close.disabled || this.busy;
      close.title = "Close the task. Its conversation is kept and can be resumed.";
      actions.append(close);
    }
    const title = item.title || item.first_message || "Untitled task";
    this.el.head.replaceChildren(
      h(
        "div",
        { class: "titles" },
        h("h1", {}, title),
        h(
          "div",
          { class: "task-meta" },
          icon("folder", 15),
          h("strong", {}, this.desk.folderName(item.repo)),
          h("span", { class: "mono" }, shortPath(item.repo, home)),
          item.started ? h("span", { class: "started" }, `· started ${sameDay(item.started) ? clock(item.started) : ago(item.started)}`) : null,
        ),
      ),
      st.label ? h("span", { class: `pill ${st.cls}` }, st.cls ? h("span", { class: `dot ${st.cls}` }) : null, st.label) : "",
      actions,
      keysLocked ? h("details", { class: "callout", open: true }, h("summary", {}, "Unlock your keys to resume: this task's model needs one."), keychainForm({ conn: this.conn, desk: this.desk }, { onDone: () => this.header() })) : "",
    );
  }
  async act(method, message) {
    const item = this.item,
      version = this.version;
    this.acting = true;
    this.controls();
    try {
      toast(message);
      await this.conn.call(method, { id: item.id });
      if (method === "session.close" || method.startsWith("run.")) await this.desk.refresh();
      if (method === "run.resume" && version === this.version) {
        this.item = this.desk.sessions.get(item.id) || item;
      }
    } catch (error) {
      toast(error.message);
    } finally {
      this.acting = false;
      this.refreshAll();
    }
  }
  async resume() {
    const item = this.item,
      version = this.version;
    this.acting = true;
    this.controls();
    try {
      await this.conn.call("session.resume", { id: item.id });
      await this.desk.refresh();
      this.acting = false;
      // Another task may have been opened meanwhile: never replace it.
      if (version === this.version) await this.open(item.id);
    } catch (error) {
      toast(error.message);
    } finally {
      this.acting = false;
      this.controls();
    }
  }

  // ---------- aside ----------
  events() {
    return [...this.seen.values()].sort((a, b) => a.seq - b.seq);
  }
  lastTurn() {
    const events = this.events();
    const start = events.findLastIndex((e) => e.kind === "user.msg" || e.kind === "eval.req");
    return start < 0 ? null : { start: events[start], tail: events.slice(start + 1) };
  }
  aside() {
    if (!this.el || !this.item) return;
    const side = this.el.side;
    const panels = [this.planPanel(), this.turnPanel(), this.changesPanel(), this.toolsPanel(), this.attachmentsPanel(), this.notesPanel(), this.workspacePanel(), this.detailsPanel()];
    side.replaceChildren(...panels.filter(Boolean));
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
    const judged =
      p.noul === "plan"
        ? p.score
          ? `scored ${Number(p.score).toFixed(2)} by the judge`
          : "drafted"
        : p.noul === "proceed"
          ? "not needed"
          : p.noul === "required"
            ? p.score
              ? `required, scored ${Number(p.score).toFixed(2)}`
              : "required for this run"
            : "judge unavailable";
    const steps = planSteps(p.plan);
    // The model's marks since the plan (plan.step), the latest per step. A
    // mark is the model's word; the transcript holds what shows it.
    const marks = new Map();
    for (const e of turn.tail.slice(at + 1)) if (e.kind === "plan.step") marks.set(e.payload.step, e.payload);
    const done = [...marks.values()].filter((m) => m.state === "done").length;
    const note = steps.length && marks.size ? `${done} of ${steps.length} done · ${judged}` : judged;
    panel.append(h("h2", {}, "Plan", h("span", { class: "note" }, note)));
    if (steps.length) {
      const next = marks.size ? steps.findIndex((_, i) => !marks.has(i + 1)) : -1;
      const items = steps.map((s, i) => {
        const m = marks.get(i + 1);
        const state = m ? m.state : i === next ? "next" : "";
        const said = m ? (m.state === "done" ? "Marked done" : "Skipped") + (m.note ? `: ${m.note}` : "") : undefined;
        return h("li", { class: state || undefined, title: said }, h("span", {}, h("span", { class: "text" }, s), m?.note ? h("small", {}, m.note) : null));
      });
      panel.append(h("ol", { class: "plan" }, items));
    } else if (p.reason) panel.append(h("p", { class: "hint" }, p.reason));
    return panel;
  }
  turnPanel() {
    const turn = this.lastTurn();
    const budgets = this.info?.budgets;
    if (!turn) return null;
    const panel = h("section", { class: "panel" }, h("h2", {}, "This turn", h("span", { class: "note" }, budgets ? "asks before a limit" : "counted from the log")));
    const meters = h("div", { style: "display:flex;flex-direction:column;gap:12px", dataset: { meters: "1" } });
    panel.append(meters);
    this.fillMeters(meters, turn);
    return panel;
  }
  meters() {
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
    let tokens = 0;
    for (const e of turn.tail) if (e.kind === "llm.resp") tokens += (e.payload.usage?.prompt_tokens || 0) + (e.payload.usage?.output_tokens || 0);
    const meter = (label, used, soft, fmt = String) => {
      const pct = soft ? Math.min(100, (used / soft) * 100) : 0;
      return h(
        "div",
        { class: "meter" },
        h("div", { class: "meter-head" }, h("span", {}, label), h("span", {}, soft ? `${fmt(used)} / ${fmt(soft)}` : fmt(used))),
        soft ? h("div", { class: "meter-track" }, h("div", { class: `meter-fill${used > soft ? " over" : ""}`, style: `width:${pct}%` })) : null,
      );
    };
    node.replaceChildren(
      ...[
        meter("Time", elapsed, budgets ? budgets.wall_ns.soft / 1e6 : 0, (ms) => minutes(ms)),
        meter("Tool calls", calls, budgets?.tool_calls.soft || 0),
        tokens ? meter("Tokens", tokens, budgets?.tokens.soft || 0, (n) => n.toLocaleString()) : null,
      ].filter(Boolean),
    );
  }
  changesPanel() {
    const written = [];
    const seen = new Set();
    for (const e of this.events()) {
      if (e.kind !== "guard.decision") continue;
      const p = e.payload;
      const allowed = p.decision === "allow" || (p.decision === "confirm" && (p.answer === "yes" || p.answer === "turn"));
      if (p.path && allowed && !seen.has(p.path)) {
        seen.add(p.path);
        written.push(p.path);
      }
    }
    const pending = [...this.prompts.values()].map((e) => e.payload?.Prompt?.Path).filter((path) => path && !seen.has(path));
    if (!written.length && !pending.length) return null;
    const rows = [
      ...pending.map((path) => h("div", { class: "file-row" }, icon("file", 15), h("span", { class: "mono", title: path }, path), h("span", { class: "badge" }, "proposed"))),
      ...written.slice(-12).flatMap((path) => {
        const versions = h("div", { class: "versions" });
        const row = h(
          "div",
          { class: "file-row" },
          icon("file", 15),
          h("span", { class: "mono", title: path }, path),
          this.info && !this.info.git && this.conn.has("versions.list")
            ? h("button", { type: "button", class: "btn small ghost", style: "margin-left:auto", onclick: () => this.history(path, versions) }, "History")
            : h("span", { class: "badge plain" }, "written"),
        );
        return [row, versions];
      }),
    ];
    const note = written.length > 12 ? `${written.length} files · latest shown` : this.info?.git ? "git is the undo" : this.info ? "previous versions kept" : "writes nodwyn was allowed";
    return h("section", { class: "panel" }, h("h2", {}, "Changes", h("span", { class: "note" }, note)), rows);
  }
  // toolsPanel lists where this task's tools come from, when some come from
  // MCP servers.
  toolsPanel() {
    const servers = this.info?.mcp || [];
    if (!servers.length) return null;
    const builtin = (this.info.tools || []).filter((t) => !t.name.includes("__")).length;
    const words = { ready: (s) => `${s.tools.length} tool${s.tools.length === 1 ? "" : "s"}`, unapproved: () => "not reviewed", changed: () => "changed: off", error: () => "unavailable" };
    return h(
      "section",
      { class: "panel" },
      h("h2", {}, "Tools in this task", h("a", { href: "#/connections", class: "note" }, "Manage")),
      h("div", { class: "kv" }, h("span", {}, "nodwyn · built in"), h("span", {}, `${builtin} tools`)),
      servers.map((s) => h("div", { class: "kv", title: s.detail || "" }, h("span", {}, `${s.name} · HTTP · ${s.host}`), h("span", {}, (words[s.state] || (() => s.state))(s)))),
    );
  }
  attachmentsPanel() {
    const attached = this.events().filter((e) => e.kind === "attach").map((e) => e.payload);
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
    const edit = canEdit && !this.notesEditing ? h("button", { type: "button", onclick: () => { this.notesEditing = true; this.aside(); } }, "Edit") : null;
    const panel = h("section", { class: "panel" }, h("h2", {}, "What nodwyn knows here", edit));
    if (this.notesEditing) {
      const area = h("textarea", { class: "input", rows: 8, spellcheck: true, "aria-label": "Project notes" });
      area.value = this.notes;
      panel.append(
        area,
        h(
          "div",
          { class: "row-actions" },
          h("button", { type: "button", class: "btn small dark", onclick: () => this.saveNotes(area.value) }, "Save"),
          h("button", { type: "button", class: "btn small ghost", onclick: () => { this.notesEditing = false; this.aside(); } }, "Cancel"),
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
        if (result.Err) toast(`Notes not saved: ${result.Err}`);
        else {
          this.notesEditing = false;
          toast("Notes saved.");
          this.loadNotes();
        }
      });
    } catch (error) {
      toast(error.message);
    }
  }
  workspacePanel() {
    const item = this.item;
    const tools = this.info?.tools?.length;
    const ws = this.info?.workspace;
    const named = this.desk.workspace(item.repo);
    const kind = ws?.kind || named?.kind;
    const rows = [h("div", { class: "kv" }, h("span", { class: "mono", title: item.repo }, shortPath(item.repo, this.desk.home)), h("span", {}, "write"))];
    for (const m of ws?.mounts || named?.mounts || []) rows.push(h("div", { class: "kv" }, h("span", { class: "mono", title: m.path }, shortPath(m.path, this.desk.home)), h("span", {}, m.access === "write" ? "write" : "read only")));
    if (tools) rows.push(h("div", { class: "kv" }, h("span", {}, "Tools"), h("span", {}, `${tools} offered to the model`)));
    if (this.info?.allowed_writes?.length) rows.push(h("div", { class: "kv" }, h("span", {}, "Writes without asking"), h("span", { class: "mono" }, this.info.allowed_writes.join(", "))));
    if (this.info?.allowed_commands?.length) rows.push(h("div", { class: "kv" }, h("span", {}, "Commands without asking"), h("span", {}, `${this.info.allowed_commands.length} prefixes`)));
    const undo = this.info ? (this.info.git ? "A git repository: git is the undo." : "Not a git repository, so nodwyn keeps the previous version of a file before it writes. Restore it from Changes.") : "";
    return h("section", { class: "panel" }, h("h2", {}, "Workspace", kind ? h("span", { class: "note" }, kindLabel(kind)) : null), rows, undo ? h("p", { class: "hint" }, undo) : null);
  }
  async history(path, node) {
    if (node.childElementCount) {
      node.replaceChildren();
      return;
    }
    node.append(h("p", { class: "hint" }, "Reading kept versions…"));
    let versions = [];
    try {
      versions = await this.conn.call("versions.list", { repo: this.item.repo, path });
    } catch (error) {
      node.replaceChildren(h("p", { class: "error" }, error.message));
      return;
    }
    if (!versions.length) {
      node.replaceChildren(h("p", { class: "hint" }, "No earlier version is kept: the file did not exist before, or git is its undo."));
      return;
    }
    const canRestore = this.item.open && this.conn.has("session.restore");
    node.replaceChildren(
      ...versions.slice(0, 8).map((v) =>
        h(
          "div",
          { class: "kv" },
          h("span", {}, `${clock(v.time)} · ${ago(v.time)} · ${v.bytes.toLocaleString()} bytes`),
          canRestore ? h("button", { type: "button", class: "btn small", disabled: this.busy, onclick: () => this.restore(path, v.hash) }, "Restore") : h("span", { class: "mono" }, v.hash.slice(0, 12)),
        ),
      ),
      canRestore ? h("p", { class: "hint" }, "Restoring shows the change and asks first; the current content is kept too.") : h("p", { class: "hint" }, "Resume the task to restore a version."),
    );
  }
  async restore(path, hash) {
    try {
      const started = await this.conn.call("session.restore", { id: this.item.id, path, hash });
      this.expect(started.operation_id, (result) => toast(result.Err ? `Not restored: ${result.Err}` : `Restored ${basename(path)}.`));
    } catch (error) {
      toast(error.message);
    }
  }
  detailsPanel() {
    const link = (label, kind) => h("button", { type: "button", class: "btn small", onclick: () => this.inspect(kind) }, label);
    return h("section", { class: "panel" }, h("h2", {}, "Details"), h("div", { class: "links" }, link("Log", "log"), link("Plan record", "plan"), link("Budget", "budget"), link("Policy", "policy"), link("Tools", "tools"), link("Friction", "friction")));
  }

  // ---------- inspectors (U3), under Details ----------
  async inspect(kind, reread = false) {
    const version = this.version;
    this.inspecting = kind;
    const view = this.el.inspector;
    this.el.transcript.hidden = true;
    view.hidden = false;
    this.controls();
    const title = { log: "Log", plan: "Plan record", budget: "Budget", policy: "Policy", tools: "Tools", friction: "Friction" }[kind];
    const back = h("button", { type: "button", class: "btn small", onclick: () => this.closeInspector() }, icon("back", 14), "Conversation");
    const refresh = h("button", { type: "button", class: "btn small", onclick: () => this.inspect(kind, true) }, "Refresh view");
    const content = h("div");
    const filter = h("input", { class: "input", placeholder: "kind prefix, e.g. guard.", "aria-label": "Kind prefix" });
    const count = h("input", { class: "input", type: "number", min: 1, max: 1000, value: "50", "aria-label": "Maximum records", style: "width:110px" });
    const form = kind === "log" ? h("form", { onsubmit: (e) => { e.preventDefault(); draw(); } }, filter, count, h("button", { class: "btn small", type: "submit" }, "Filter")) : null;
    view.replaceChildren(...[h("div", { class: "bar" }, back, h("h2", {}, title), refresh), form, content].filter(Boolean));
    if (kind === "friction") return this.friction(content, version);
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
    this.inspecting = null;
    this.policy = null;
    this.el.inspector.hidden = true;
    this.el.transcript.hidden = false;
    this.controls();
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
  async friction(content, version) {
    if (!this.item.open || this.busy || !this.conn.has("session.friction")) {
      content.replaceChildren(h("p", {}, "Reading the friction log needs an open, idle task in an interactive view."));
      return;
    }
    content.replaceChildren(h("p", { class: "hint" }, "Reading docs/friction.md through the task's guard…"));
    try {
      const started = await this.conn.call("session.friction", { id: this.item.id });
      this.expect(started.operation_id, (result) => {
        if (version !== this.version || this.inspecting !== "friction") return;
        content.replaceChildren(h("p", { class: "hint" }, "docs/friction.md · guarded read · refresh to read again"), h("pre", {}, result.Err || result.Text));
      });
    } catch (error) {
      content.replaceChildren(h("p", {}, error.message));
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
