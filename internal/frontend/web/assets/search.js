// People search their own logs through read-only methods. Historical rendering
// has its own Transcript, so old choices and receipts never reach a live task.
import { h, clock } from "./dom.js";
import { Transcript } from "./transcript.js";

// The scopes: "project" is this task and the project's earlier ones, which
// the daemon answers as two searches (its "conversation" and "earlier"), the
// default since the words a person looks for are as likely in last week's
// task as in this one. A linked host's task is searched there (desk.at, ADR
// 54).
export function searchURL(id, query = "", scope = "project", desk = null) {
  const path = `search/${encodeURIComponent(id)}?${new URLSearchParams({ q: query, scope })}`;
  return desk ? desk.at(path) : `#/${path}`;
}

// describe says what a match is in a person's words, from the daemon's kind
// and its heading ("seq 606, the result of fs-read {...}"), which stays as
// the tooltip.
const tools = { "fs-read": "File read", "fs-write": "File written", "fs-edit": "File edited", "shell-exec": "Command", "fs-list": "Folder listing", "fs-search": "Search of files" };
function describe(match) {
  const head = match.heading.replace(/^seq \d+, /, "");
  const agent = /, in the (.+?)'s context/.exec(head)?.[1];
  const note = /, the note of a compaction/.test(head);
  let what;
  if (match.kind === "user.msg") what = "Your message";
  else if (match.kind === "harness.msg") what = "nodwyn's message";
  else if (match.kind === "llm.resp") what = "A reply";
  else if (match.kind === "gate.plan") what = "The plan";
  else if (match.kind === "summary") what = "Summary of earlier messages";
  else if (match.kind === "attach") what = `Attachment: ${head.replace(/^the attached file /, "").replace(/,.*$/, "")}`;
  else if (match.kind === "tool.result") {
    const call = /^the result of (\S+)(?: (.*?))?(, which failed)?(?:, in the .+)?$/.exec(head);
    let target = "";
    try {
      const args = JSON.parse(call?.[2] || "{}");
      target = args.path || args.command || args.query || "";
    } catch {
      /* arguments cut short for a heading: the name stands alone */
    }
    what = `${tools[call?.[1]] || `Result of ${call?.[1] || "a tool"}`}${target ? `: ${Array.isArray(target) ? target.join(" ") : target}` : ""}${call?.[3] ? " (failed)" : ""}`;
  } else what = head;
  return what + (note ? " (a compaction's note)" : "") + (agent ? ` (in the ${agent}'s context)` : "");
}

// highlight draws text with the words of the query marked, as text nodes and
// marks, never as markup.
function highlight(text, query) {
  const words = [...new Set((query.toLowerCase().match(/[\p{L}\p{N}_]{2,}/gu) || []))].sort((a, b) => b.length - a.length);
  if (!words.length) return [text];
  const found = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "giu");
  return text.split(found).map((part, i) => (i % 2 ? h("mark", {}, part) : part));
}

const when = (time) => new Date(time).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });

export class ConversationSearchView {
  constructor(conn, desk, navigate, route) {
    this.root = h("main", { class: "page conversation-search" });
    this.conn = conn; this.desk = desk; this.navigate = navigate; this.route = route;
    this.version = 0;
  }
  leave() { this.version++; }
  async open() {
    const { id, query, scope } = this.route;
    const item = this.desk.sessions.get(id);
    const input = h("input", { id: "conversation-query", class: "input", type: "search", required: true, maxlength: 4096, value: query, placeholder: "Words from a message, reply or tool result", autocomplete: "off" });
    const where = h("select", { id: "conversation-scope", class: "input" },
      h("option", { value: "project", selected: scope !== "earlier" && scope !== "conversation" }, "This task and earlier ones in this folder"),
      h("option", { value: "conversation", selected: scope === "conversation" }, "This task only"),
      h("option", { value: "earlier", selected: scope === "earlier" }, "Earlier tasks only"));
    const status = h("p", { class: "hint search-status", role: "status" });
    const results = h("div", { class: "search-results" });
    const form = h("form", { class: "conversation-search-form", onsubmit: (event) => {
      event.preventDefault();
      const url = searchURL(id, input.value.trim(), where.value, this.desk);
      if (location.hash === url) this.open();
      else this.navigate(url);
    } }, h("label", { for: "conversation-query" }, "Words to find", input), h("label", { for: "conversation-scope" }, "Where", where), h("button", { type: "submit", class: "btn dark" }, "Search"));
    this.root.replaceChildren(h("div", { class: "page-col" }, h("a", { href: this.desk.href(encodeURIComponent(id)) }, "Back to task"), h("h1", {}, "Search conversations"),
      h("p", { class: "lede" }, item ? `${this.desk.folderName(this.desk.project(item))} · ${item.title || "Untitled task"}` : "Task history"), form,
      h("p", { class: "hint" }, "Searches messages, replies, plans, attachments and tool results. Your search does not change what the model may search."), status, results));
    input.focus();
    if (!query) return;
    if (!this.conn.has("conversation.search") || !this.conn.has("log.context")) { status.textContent = "Conversation search is unavailable on this daemon."; return; }
    const version = ++this.version;
    status.textContent = "Searching conversations…";
    try {
      const ask = (which) => this.conn.call("conversation.search", { id, query, scope: which, limit: 20 });
      // "This task and earlier ones" is both of the daemon's scopes, this
      // task's results first.
      const found = scope === "project"
        ? await Promise.all([ask("conversation"), ask("earlier")]).then(([a, b]) => ({ tasks: [...a.tasks, ...b.tasks], searched_tasks: a.searched_tasks + b.searched_tasks, searched_events: a.searched_events + b.searched_events, unavailable: a.unavailable + b.unavailable }))
        : await ask(scope);
      if (version !== this.version) return;
      const count = found.tasks.reduce((n, task) => n + task.turns.reduce((n, turn) => n + turn.matches.length, 0), 0);
      status.textContent = count ? `${count} best match${count === 1 ? "" : "es"}, in ${found.tasks.length} task${found.tasks.length === 1 ? "" : "s"} (of ${found.searched_tasks} searched).` : "No matching conversation found.";
      if (found.unavailable) status.append(" Some history could not be read; results may be incomplete.");
      for (const task of found.tasks) {
        const section = h("section", { class: "search-task", dataset: { task: task.id } }, h("h2", {}, task.title || "Untitled task", task.archived ? h("span", { class: "badge" }, "Archived") : null));
        for (const turn of task.turns) {
          // "turn seq 4-758 · completed · 12m34s": its sequence numbers mean
          // nothing to a person, the first match's time and how it ended do.
          const ended = turn.heading.replace(/^turn seq \d+-\d*,? ?(· )?/, "");
          const card = h("article", { class: "card search-turn" }, h("h3", { title: turn.heading }, `${when(turn.matches[0].time)} · turn ${ended}`));
          for (const match of turn.matches) {
            const back = new URLSearchParams({ search: id, q: query, scope });
            card.append(h("div", { class: "search-match", dataset: { seq: String(match.seq) } },
              h("a", { class: "search-match-link", href: this.desk.at(`history/${encodeURIComponent(task.id)}/${match.seq}?${back}`), title: match.heading }, describe(match)),
              h("span", { class: "hint" }, ` · ${when(match.time)}`),
              h("pre", { class: "search-snippet" }, match.lines.map((line) => h("span", {}, h("span", { class: "line-number" }, `${line.number}: `), highlight(line.text, query), "\n")))));
          }
          section.append(card);
        }
        results.append(section);
      }
    } catch (error) {
      if (version !== this.version) return;
      status.textContent = `Could not search conversations: ${error.message}`;
      results.append(h("button", { type: "button", class: "btn", onclick: () => this.open() }, "Retry search"));
    }
  }
}

export class ConversationContextView {
  constructor(conn, desk, route) {
    this.conn = conn; this.desk = desk; this.route = route;
    this.root = h("main", { class: "page conversation-context" });
    this.version = 0; this.events = new Map(); this.transcript = null;
  }
  leave() { this.version++; this.transcript?.end(); }
  async open() {
    const version = ++this.version;
    const { id, seq, search, query, scope } = this.route;
    const item = this.desk.sessions.get(id);
    this.body = h("div", { class: "transcript history-transcript", "aria-label": "Matching conversation" });
    this.status = h("p", { class: "hint", role: "status" });
    this.earlier = h("button", { type: "button", class: "btn", hidden: true, onclick: () => this.more(true) }, "Load earlier in this turn");
    this.later = h("button", { type: "button", class: "btn", hidden: true, onclick: () => this.more(false) }, "Load later in this turn");
    this.root.replaceChildren(h("div", { class: "page-col" }, h("div", { class: "row-actions" },
      search ? h("a", { class: "btn ghost", href: searchURL(search, query, scope, this.desk) }, "Back to search results") : null,
      h("a", { class: "btn ghost", href: this.desk.href(encodeURIComponent(id)) }, "Open task")),
      h("h1", {}, item?.title || "Conversation history"), h("p", { class: "hint" }, "Recorded history. Open the task to reply or answer a current request."), this.status, this.earlier, this.body, this.later));
    if (!Number.isSafeInteger(seq) || seq < 1) { this.status.textContent = "Invalid event reference."; return; }
    this.status.textContent = "Loading matching conversation…";
    try {
      this.context = await this.conn.call("log.context", { id, seq });
      if (version !== this.version) return;
      for (const event of this.context.page.events) this.events.set(event.seq, event);
      this.draw(true);
      this.root.dataset.loaded = "";
    } catch (error) {
      if (version !== this.version) return;
      this.status.textContent = `Could not read this conversation: ${error.message}`;
      this.body.replaceChildren(h("button", { type: "button", class: "btn", onclick: () => this.open() }, "Retry conversation"));
    }
  }
  draw(focus = false) {
    this.transcript?.end();
    this.transcript = new Transcript(this.body);
    const events = [...this.events.values()].sort((a, b) => a.seq - b.seq);
    let target;
    for (const event of events) {
      // Only the selected event needs a DOM snapshot. Its new node may be a
      // result inside an earlier call, rather than a new top-level turn.
      const before = event.seq === this.route.seq ? new Set(this.body.querySelectorAll("*")) : null;
      this.transcript.render(event);
      if (before) {
        target = [...this.body.querySelectorAll("*")].find((node) => !before.has(node));
        if (!target) {
          target = h("details", { class: "audit", open: true }, h("summary", {}, `Matching record · ${event.kind}`), h("pre", {}, JSON.stringify(event.payload, null, 2)));
          this.body.append(target);
        }
      }
    }
    this.transcript.end();
    if (target) {
      target.classList.add("conversation-match"); target.dataset.seq = String(this.route.seq);
      for (let node = target; node && node !== this.body; node = node.parentElement) if (node.tagName === "DETAILS") node.open = true;
      if (focus) requestAnimationFrame(() => { if (this.root.isConnected) target.scrollIntoView({ block: "center" }); });
    }
    const c = this.context;
    this.first = events[0]?.seq || c.from; this.last = events.at(-1)?.seq || c.through;
    this.status.textContent = `Run ${clock(c.run.ts)}${c.turn ? ` · turn ${clock(c.turn.ts)}` : ""} · showing records ${this.first}–${this.last} of ${c.from}–${c.through}${c.page.truncated ? " · the log has an incomplete tail" : ""}`;
    this.earlier.hidden = this.first <= c.from; this.later.hidden = this.last >= c.through;
    this.earlier.disabled = this.later.disabled = false;
  }
  async more(earlier) {
    if (this.reading) return;
    this.reading = true;
    const version = this.version;
    this.earlier.disabled = this.later.disabled = true;
    try {
      const after = earlier ? Math.max(this.context.from - 1, this.first - 501) : this.last;
      const through = earlier ? this.first - 1 : Math.min(this.context.through, this.last + 500);
      const page = await this.conn.call("log.read", { id: this.route.id, after, through, limit: 500 });
      if (version !== this.version) return;
      if (!page.events.length || page.next <= after) throw new Error("The history page did not advance.");
      for (const event of page.events) this.events.set(event.seq, event);
      this.draw();
    } catch (error) {
      if (version === this.version) this.status.textContent = `Could not read more conversation: ${error.message}`;
    } finally {
      this.reading = false;
      if (version === this.version) this.earlier.disabled = this.later.disabled = false;
    }
  }
}
