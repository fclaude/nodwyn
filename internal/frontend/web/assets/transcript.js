// A transcript is a sequence of turns, not of events: the user's message
// opens one, and everything the harness does in reply belongs to it. Calls
// that only look (reads, searches, listings) fold into one line, as do
// writes in a row and commands in a row; the model's marks on its plan join
// one line. Routine activity shares a further fold, so the conversation
// foregrounds what the user and the model say to each other.
import { h, icon, oneLine, sizeOf, toolLabel, isMCP } from "./dom.js";
import { markdown } from "./markdown.js";
import { attachmentChip } from "./attach.js";

const readName = /(^|[-_/.])(read|list|find|grep|search|get|show|status|outline|log|diff|query|fetch|lookup|view|notes|events|info)([-_.]|$)/;
const writes = new Set(["fs-write", "fs-edit", "doc-export"]);
// Retrieval (ADR 50) only looks: recall reads the log back, and
// index-update reads the repository into nodwyn's own index.
const looks = new Set(["recall", "index-update"]);

function category(name) {
  if (writes.has(name)) return "write";
  if (name === "shell-exec") return "command";
  if (readName.test(name) || looks.has(name)) return "read";
  return "other";
}

// What a folded group says it did, in words.
function describe(counts) {
  const parts = [];
  const n = (count, one, many) => (count === 1 ? one : many.replace("#", count));
  if (counts.read) parts.push(n(counts.read, "read 1 file", "read # files"));
  if (counts.search) parts.push(n(counts.search, "searched once", counts.search === 2 ? "searched twice" : "searched # times"));
  if (counts.list) parts.push(n(counts.list, "listed 1 folder", "listed # folders"));
  if (counts.recall) parts.push(n(counts.recall, "recalled 1 event", "recalled # events"));
  if (counts.index) parts.push(n(counts.index, "updated the index", "updated the index # times"));
  if (counts.other) parts.push(n(counts.other, "1 other look", "# other looks"));
  if (counts.failed) parts.push(n(counts.failed, "1 failed", "# failed"));
  return parts.join(" · ");
}
function lookKind(name) {
  if (name === "recall") return "recall";
  if (name === "index-update") return "index";
  if (/grep|search|find|query|lookup/.test(name)) return "search";
  if (/list/.test(name)) return "list";
  if (/read|show|view|get|fetch/.test(name)) return "read";
  return "other";
}

// The calls that build or refresh the repository's index (ADR 50).
const indexing = new Set(["index-search", "index-update"]);

// indexLine says what an index.update did, in words.
export function indexLine(p) {
  const files = (n) => `${n} file${n === 1 ? "" : "s"}`;
  const parts = [`read ${files(p.read || 0)}`];
  if (p.removed) parts.push(`${p.removed} removed`);
  if (p.skipped) parts.push(`${p.skipped} skipped`);
  if (p.refused?.length) parts.push(`left out by the guard: ${p.refused.join(", ")}`);
  if (p.unread) parts.push(`${files(p.unread)} still to read`);
  if (p.partial) parts.push("the repository is larger than an index holds");
  return `Index updated: ${parts.join(" · ")} · holds ${files(p.files || 0)} · ${((p.ms || 0) / 1000).toFixed(1)} s`;
}

// callSummary is a call's arguments in a line, said in words where a tool's
// arguments read badly as values: a search names what it searched, a
// recall the event and its task.
const searched = { repo: "in the repository", conversation: "in this conversation", earlier: "in earlier tasks" };
export function callSummary(name, args) {
  const a = argsObject(args);
  if (name === "index-search" && typeof a.query === "string") return [`“${a.query}”`, searched[a.scope] || a.scope, a.path ? `under ${a.path}` : ""].filter(Boolean).join(" ");
  if (name === "recall" && a.seq != null) return [`seq ${a.seq}`, a.task ? `of task ${a.task}` : "", a.start ? `from line ${a.start}` : ""].filter(Boolean).join(" ");
  if (name === "index-update") return a.path ? `adding ${a.path}` : "the whole repository";
  return argSummary(args);
}

const targetKeys = ["path", "file", "dir", "pkg", "target", "url", "cmd", "name", "glob", "pattern", "query", "channel"];
export function argSummary(args) {
  if (args == null) return "";
  let parsed = args;
  if (typeof parsed === "string") {
    try {
      parsed = JSON.parse(parsed);
    } catch {
      return oneLine(parsed);
    }
  }
  if (typeof parsed !== "object") return oneLine(String(parsed));
  const entries = Object.entries(parsed);
  const rank = (key) => {
    const i = targetKeys.indexOf(key);
    return i < 0 ? targetKeys.length : i;
  };
  entries.sort((a, b) => rank(a[0]) - rank(b[0]));
  const named = entries.some(([key]) => targetKeys.includes(key));
  return oneLine(
    entries
      .map(([key, v]) => [key, typeof v === "string" ? v : JSON.stringify(v)])
      .filter(([key, v]) => !named || targetKeys.includes(key) || v.length <= 48)
      .map(([, v]) => v)
      .filter((v) => v !== "" && v !== "[]" && v !== "{}" && v !== "null")
      .join(" "),
  );
}
function argsObject(args) {
  if (typeof args !== "string") return args || {};
  try {
    return JSON.parse(args);
  } catch {
    return {};
  }
}
function commandLine(args) {
  const a = argsObject(args);
  return [a.cmd, ...(Array.isArray(a.args) ? a.args : [])].filter(Boolean).join(" ");
}
// exitStatus reads what a command's result says, not what it means: exit 0
// is not "the tests passed", so the line shows the status.
function exitStatus(value) {
  const m = /^exit (-?\d+)/.exec(value || "") || /"status"\s*(?:\.|:)\s*(-?\d+)/.exec(value || "");
  return m ? Number(m[1]) : null;
}
function detailsBlock(label, value) {
  return h("details", {}, h("summary", {}, label), h("pre", {}, typeof value === "string" ? value : JSON.stringify(value, null, 2)));
}

// spanWords says how long something took the way a person would: tenths of
// a second while it is short, then seconds, minutes and hours.
export function spanWords(ms) {
  if (ms < 10000) return `${(Math.max(0, ms) / 1000).toFixed(1)} s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? `${m} m ${s % 60} s` : `${m} m`;
  const hr = Math.floor(m / 60);
  return m % 60 ? `${hr} h ${m % 60} m` : `${hr} h`;
}

// commandHeader is what a folded run of commands says, as [label, what]: how
// many ran and for how long together, whether one is running now or waiting
// for an answer, and how many failed. The time is the calls' own, as each
// row shows it, so a wait for an answer counts once the command has run.
function commandHeader(g, now) {
  const ran = (n) => `Ran ${n} ${n === 1 ? "command" : "commands"}`;
  const failed = g.failed ? `${g.failed} failed` : "";
  if (g.waiting) return [g.done ? `${ran(g.done)}, waiting for your answer` : "Waiting for your answer", failed];
  if (g.startedAt) return [g.done ? `${ran(g.done)}, running another` : "Running a command", [spanWords(g.ms + now - g.startedAt), failed].filter(Boolean).join(" · ")];
  return [ran(g.done), [`in ${spanWords(g.ms)}`, failed].filter(Boolean).join(" · ")];
}

// The upload setting's values as Settings and the question name them.
export const uploadLabels = { ask: "allow by hand", allow: "always allow", deny: "deny" };

const auditLabels = {
  "turn.checkpoint": "Budget checkpoint answered",
  "approval.change": "Standing answer changed",
  "binding.change": "Credential binding changed",
  summary: "Summary written",
  "summary.drop": "Summary revoked",
  "cred.register": "Credential registered",
  "context.narrow": "Started a focused context",
  "context.widen": "Back to the whole conversation",
  "context.fold": "Folded a focused context away",
  "run.state": "Run state recorded",
  "run.checkpoint": "Run checkpoint answered",
  note: "Note",
};

// thinking is a reply's thinking, folded, under its first line, so a row
// says what the model was thinking without opening it.
function thinking(text) {
  const gist = text.trim().split("\n")[0];
  return h("details", { class: "thinking" }, h("summary", {}, h("span", { class: "label" }, "Thinking"), gist ? h("span", { class: "gist" }, gist) : null), h("pre", {}, text));
}

// brief is a reply's line of narration between two calls, as against
// something the model tells you: a single short paragraph.
function brief(text) {
  const t = String(text).trim();
  return t.length <= 240 && !t.includes("\n\n") && !t.includes("```");
}

// marksLabel is what a run of the model's marks on its plan says, by state:
// "Steps 1, 2 of 5 marked done".
function marksLabel(marks) {
  const said = [];
  for (const m of marks) {
    const like = said.find((s) => s.of === m.of && s.state === m.state);
    if (like) like.steps.push(m.step);
    else said.push({ of: m.of, state: m.state, steps: [m.step] });
  }
  return said.map((s) => `Step${s.steps.length === 1 ? "" : "s"} ${s.steps.join(", ")} of ${s.of} marked ${s.state}`).join(" · ");
}

export class Transcript {
  constructor(container) {
    this.el = container;
    this.reset();
  }
  reset() {
    this.el.replaceChildren();
    this.turn = null;
    this.fold = null;
    this.group = null;
    this.writeGroup = null;
    this.marks = null;
    this.call = null;
    this.streamText = null;
    this.streamThinking = null;
    this.thought = null;
    this.usage = null;
    this.closeCommands();
  }
  get empty() {
    return !this.el.querySelector(".turn");
  }
  end() {
    this.turn = null;
    this.fold = null;
    this.group = null;
    this.writeGroup = null;
    this.marks = null;
    this.call = null;
    this.streamText = null;
    this.streamThinking = null;
    this.thought = null;
    this.usage = null;
    this.closeCommands();
  }
  agent() {
    if (!this.turn) {
      this.turn = h("div", { class: "turn agent" }, h("div", { class: "who" }, h("span", { class: "dot" }), "nodwyn"));
      this.el.append(this.turn);
    }
    return this.turn;
  }
  // Routine activity stays available without adding a visible row for
  // every judge request, context change, instruction or group of calls.
  activity(...nodes) {
    if (!this.fold) {
      const failed = h("span", { class: "activity-fold-failed" });
      const what = h("span", { class: "activity-fold-what" });
      const body = h("div", { class: "activity-fold-body" });
      const el = h("details", { class: "activity-fold" }, h("summary", {}, h("span", { class: "chev" }, icon("right", 14)), h("span", {}, "Activity"), failed, what), body);
      this.agent().append(el);
      this.fold = { el, body, what, failed };
    }
    this.fold.body.append(...nodes);
    this.paintActivity();
    return this.fold.body;
  }
  // paintActivity writes what a fold holds on its row. A failure is counted
  // on the row rather than opening it: a command that exits 1 is routine
  // (a grep that finds nothing, a test run before the fix), and opening the
  // row for it showed everything else in the stretch too. Its run or group
  // inside still opens, so it is in view once the row is.
  paintActivity(f = this.fold) {
    if (!f) return;
    const parts = [];
    let notes = 0, calls = 0;
    for (const node of f.body.children) {
      if (node.classList.contains("activity")) {
        const label = node.querySelector(".label").textContent;
        // The groups' own failure counts are summed on the row.
        const what = node.querySelector(".what").textContent.split(" · ").filter((s) => !/^\d+ failed$/.test(s)).join(" · ");
        parts.push([label, what].filter(Boolean).join(": "));
      } else if (node.classList.contains("audit")) notes++;
      else if (node.classList.contains("step")) calls++;
    }
    if (notes) parts.unshift(`${notes} ${notes === 1 ? "note" : "notes"}`);
    if (calls) parts.push(`${calls} ${calls === 1 ? "call" : "calls"}`);
    const active = this.call;
    if (active?.fold === f && active.command) parts.push(`${active.command} · ${active.waiting ? "waiting for your answer" : "running…"}`);
    f.what.textContent = parts.join(" · ") || "Tokens and timing";
    const failed = f.body.querySelectorAll(".stat.failed, .result.failed").length;
    f.failed.textContent = failed ? `${failed} failed` : "";
  }
  // absorb takes the narration written between two writes into the Edited
  // group they share: what came after the group's last row is only prose,
  // thinking and the token line (anything else ends the group), and the
  // token line stays last in the turn. A brief line is what the model says
  // as it goes on; the group keeps it in order behind its fold.
  absorb() {
    const g = this.writeGroup;
    this.fold = g.fold;
    for (let n = g.fold.el.nextSibling, next; n; n = next) {
      next = n.nextSibling;
      if (n !== this.usage) g.body.append(n);
    }
    this.thought = null;
  }
  // closeCommands ends the run of commands in view: the next command starts
  // a run of its own, and the clock stops.
  closeCommands() {
    this.commandGroup = null;
    this.stopClock();
  }
  stopClock() {
    if (this.clock) {
      clearInterval(this.clock);
      this.clock = null;
    }
  }
  // paintCommands writes a run's header from its state and the clock.
  paintCommands(g) {
    const [label, what] = commandHeader(g, Date.now());
    g.label.textContent = label;
    g.what.textContent = what;
    this.paintActivity(g.fold);
  }
  // tickClock repaints the open run once a second while a command in it
  // runs or waits (the header says which). A transcript no longer on the
  // page (the person left the task) stops its clock: no result will come to
  // stop it.
  tickClock() {
    const g = this.commandGroup;
    if (!g || !g.startedAt || !this.el.isConnected) {
      this.stopClock();
      return;
    }
    this.paintCommands(g);
  }
  // startClock keeps one timer per transcript while a command runs.
  startClock() {
    if (this.clock) return;
    this.clock = setInterval(() => this.tickClock(), 1000);
  }
  // audit draws a note: behind Activity when it is routine, in view when it
  // is a warning, an error or (outside) news of another kind.
  audit(label, payload, cls = "", outside = cls === "bad" || cls === "warn") {
    const node = h("details", { class: `audit ${cls}` }, h("summary", {}, label));
    if (payload !== undefined) node.append(h("pre", {}, typeof payload === "string" ? payload : JSON.stringify(payload, null, 2)));
    if (outside) {
      this.fold = null;
      this.agent().append(node);
    } else this.activity(node);
    this.group = null;
    this.writeGroup = null;
    this.marks = null;
    this.closeCommands();
    return node;
  }
  // skip draws a call that was skipped without running, with the reason in
  // its line so it is read without opening the note. Calls skipped in a row
  // for the same reason (an explorer out of rounds skips all of its last
  // ones) are one note, the tools listed behind it.
  skip(name, reason) {
    const last = this.fold?.body.lastElementChild;
    const note = last?.classList.contains("skipped") && last.dataset.reason === reason ? last : this.audit("", undefined, "skipped");
    note.dataset.reason = reason;
    note.tools = [...(note.tools || []), toolLabel(name)];
    note.querySelector("summary").textContent = note.tools.length === 1 ? `${note.tools[0]} skipped: ${reason}` : `${note.tools.length} calls skipped: ${reason}`;
    let pre = note.querySelector("pre");
    if (!pre) note.append((pre = h("pre")));
    pre.textContent = note.tools.join("\n");
  }
  // render draws one event; it returns nothing and decides nothing.
  render(event) {
    const p = event.payload || {};
    switch (event.kind) {
      case "user.msg": {
        this.end();
        const turn = h("div", { class: "turn user" }, h("div", { class: "who" }, "You"), h("div", { class: "you-text" }, p.text));
        // Files attached just before belong to this message.
        const last = this.el.lastElementChild;
        if (last?.classList.contains("attached")) {
          turn.append(last.querySelector(".attach-list"));
          last.remove();
        }
        this.el.append(turn);
        break;
      }
      case "attach": {
        this.end();
        let holder = this.el.lastElementChild;
        if (!holder?.classList.contains("attached")) {
          holder = h("div", { class: "turn user attached" }, h("div", { class: "who" }, "You attached"), h("div", { class: "attach-list" }));
          this.el.append(holder);
        }
        holder.querySelector(".attach-list").append(attachmentChip(p));
        break;
      }
      case "harness.msg":
        this.audit("nodwyn told the model", p.text ?? p.Text, "harness-note");
        break;
      case "stream.text": {
        if (!this.streamText) {
          this.fold = null;
          this.streamText = h("div", { class: "prose streaming" });
          this.agent().append(this.streamText);
          this.group = null;
          this.closeCommands();
        }
        this.streamText.textContent += p.Text ?? p.text ?? "";
        break;
      }
      case "stream.thinking": {
        if (!this.streamThinking) {
          const pre = h("pre");
          this.agent().append(h("details", { class: "thinking" }, h("summary", {}, "Thinking…"), pre));
          this.streamThinking = pre;
        }
        this.streamThinking.textContent += p.Text ?? p.text ?? "";
        break;
      }
      case "llm.resp": {
        // Streamed fragments were provisional; the recorded response is what
        // the log holds, so it replaces them.
        this.streamText?.remove();
        this.streamThinking?.closest("details")?.remove();
        this.streamText = null;
        this.streamThinking = null;
        const turn = this.agent();
        const previousFold = this.fold;
        if (p.content) this.fold = null;
        this.thought = null;
        if (p.thinking) {
          const node = thinking(p.thinking);
          // A reply that only thinks and calls tools goes with its calls: into
          // the group of reads it continues, or the one its reads will start.
          // In rows of their own, forty such replies stacked forty "Thinking"
          // lines under one group. Commands fold the same way, and so do
          // writes: the Edited group first takes in the narration before it,
          // so the thought follows it. Left beside the group inside Activity,
          // the thought fell behind the group's later writes.
          const first = (p.tool_calls || [])[0];
          const writing = category(first?.name) === "write" && this.writeGroup && !p.content;
          if (writing) this.absorb();
          const open = category(first?.name) === "command" ? this.commandGroup : writing ? this.writeGroup : this.group;
          if (p.content) turn.append(node);
          else if (open) open.body.append(node);
          else {
            this.activity(node);
            this.thought = node;
          }
        }
        if (p.content) {
          turn.append(markdown(p.content));
          this.group = null;
          this.marks = null;
          this.closeCommands();
          // A line of narration does not end a group of writes: the next write
          // takes it in. A paragraph is the model telling you something, and
          // stays where it is, after the group.
          if (!brief(p.content)) this.writeGroup = null;
        }
        if (p.usage) {
          const u = p.usage;
          const fresh = { in: 0, out: 0, ms: 0, wall: 0, cachedIn: 0, cached: 0, cost: 0, priced: 0, unpriced: 0, estimated: false, requests: 0, largest: 0, reasoning: 0 };
          this.tokens = (this.usage ? this.tokens : fresh) || fresh;
          this.tokens.in += u.prompt_tokens || 0;
          this.tokens.out += u.output_tokens || 0;
          this.tokens.requests++;
          if (u.unreported) this.tokens.unreported = (this.tokens.unreported || 0) + 1;
          this.tokens.largest = Math.max(this.tokens.largest || 0, u.prompt_tokens || 0);
          this.tokens.reasoning += u.reasoning_tokens || 0;
          this.tokens.ms += u.total_ms || 0;
          // The request's wall time, host-measured (newer logs), counts only
          // for a request that reported tokens: the speed below is tokens
          // over the time that produced them.
          if (!u.unreported) this.tokens.wall += u.wall_ms || 0;
          // A provider that reports its prompt cache and cost (OpenRouter)
          // says so; absent is unknown, never 0% or free.
          if (u.cached_tokens !== undefined) {
            this.tokens.cachedIn += u.prompt_tokens || 0;
            this.tokens.cached += u.cached_tokens;
          }
          if (u.cost !== undefined) {
            this.tokens.cost += u.cost;
            this.tokens.priced++;
            this.tokens.estimated ||= !!u.cost_estimated;
          } else this.tokens.unpriced++;
          if (!this.usage) {
            this.usage = h("div", { class: "usage" });
          }
          // Usage belongs behind the activity fold too, not as another
          // verbose line beside the answer. It remains last in its fold.
          const usageBody = this.fold?.body || this.usage.parentElement || previousFold?.body || this.activity();
          usageBody.append(this.usage);
          // Only Ollama reports a duration; a chat-completions server (OpenRouter
          // among them) reports none, so no time rather than "0.0 s".
          const time = this.tokens.ms > 0 ? ` · ${(this.tokens.ms / 1000).toFixed(1)} s with the model` : "";
          const t = this.tokens;
          // tok/s is output tokens over the wall time the requests took,
          // host-measured: every provider gets one, unlike total_ms, which
          // only Ollama reports.
          const speed = t.wall > 0 && t.out > 0 ? ` · ${(t.out / (t.wall / 1000)).toFixed(1)} tok/s` : "";
          const cache = t.cachedIn > 0 ? ` · ${Math.round((100 * t.cached) / t.cachedIn)}% cached` : "";
          const cost = t.priced > 0 ? ` · ${t.unpriced > 0 ? "at least " : ""}${usd(t.cost, t.estimated)}` : "";
          // Each request sends the whole prompt again, so over several the
          // first number adds the prompt up each time: the largest single
          // prompt says how big the context got.
          const reasoning = t.reasoning > 0 ? ` (${t.reasoning.toLocaleString()} reasoning)` : "";
          const over = t.requests > 1 ? ` over ${t.requests} requests · largest prompt ${t.largest.toLocaleString()}` : "";
          const silent = t.unreported ? ` · ${t.unreported === t.requests ? "the server reported no tokens" : `${t.unreported} of them reported no tokens`}` : "";
          this.usage.textContent = t.unreported === t.requests ? `tokens not reported${cost}${time}` : `${t.in.toLocaleString()} → ${t.out.toLocaleString()} tokens${reasoning}${over}${silent}${cache}${cost}${speed}${time}`;
        }
        this.call = null;
        break;
      }
      case "tool.call":
        this.toolCall(p);
        break;
      case "guard.decision":
        this.decision(p);
        break;
      case "index.update":
        this.indexUpdate(p);
        break;
      case "tool.result":
        this.toolResult(p);
        break;
      case "gate.plan": {
        const steps = planSteps(p.plan);
        const label =
          p.noul === "plan" || p.noul === "required"
            ? `Made a plan${steps.length ? ` · ${steps.length} steps` : ""}${p.score ? ` · scored ${Number(p.score).toFixed(2)}` : ""}`
            : p.noul === "carried"
              ? `Went on with the plan · ${(p.marks || []).length} of ${steps.length} steps marked`
              : p.noul === "model"
                ? `Wrote its plan${steps.length ? ` · ${steps.length} steps` : ""}`
                : p.noul === "proceed"
                  ? "No plan needed for this"
                  : "Planned without the plan judge";
        this.audit(label, p.plan || p.reason || undefined);
        break;
      }
      case "run.checkpoint":
        // An unattended run answers its own checkpoints, and says why.
        if (p.actor === "unattended") this.audit(`${p.allowed ? "Went on" : "Stopped"} by itself: ${p.reason}`, p, p.allowed ? "" : "bad");
        else this.audit(auditLabels[event.kind], p);
        break;
      case "plan.step": {
        // The model's own marks: marks in a row are one line, its notes behind
        // the fold, instead of a line each. Anything between two marks (a
        // call, the model's text) starts a new line.
        if (!this.marks) {
          const el = h("details", { class: "audit" }, h("summary", {}));
          this.activity(el);
          this.group = null;
          this.writeGroup = null;
          this.closeCommands();
          this.marks = { el, list: [] };
        }
        const m = this.marks;
        m.list.push(p);
        m.el.querySelector("summary").textContent = marksLabel(m.list);
        const notes = m.list.filter((x) => x.note).map((x) => `${x.step}. ${x.note}`).join("\n");
        if (notes) {
          let pre = m.el.querySelector("pre");
          if (!pre) m.el.append((pre = h("pre")));
          pre.textContent = notes;
        }
        break;
      }
      case "run.turn":
        this.end();
        this.el.append(h("div", { class: "audit" }, `Run · turn ${p.number}`));
        break;
      case "eval.req":
        this.audit("You evaluated Scheme", p.source ?? p);
        break;
      case "eval.result":
        this.audit(p.error ? "Evaluation failed" : "Evaluation finished", p.error || p.value || "(no value)", p.error ? "bad" : "");
        break;
      case "error":
        this.audit(`Stopped: ${p.Err ?? p.err ?? "an error"}`, undefined, "bad");
        break;
      case "output":
        this.audit("Output", p.Text ?? p.text);
        break;
      case "turn.end":
        if (p.outcome && p.outcome !== "completed") {
          const words = { stopped: "Stopped", hard_budget: "Stopped at a hard budget limit", spend_limit: "Stopped at the spend limit", canceled: "Stopped: canceled", error: "Stopped by an error" };
          // Where a turn stopped short is said where it ends, not behind
          // Activity: a limit's stop is shown nowhere else.
          this.audit(words[p.outcome] || `Turn ended: ${p.outcome}`, p.error || undefined, p.outcome === "error" ? "bad" : "", true);
          // Where the work had got to, in view beside the stop, so whether it
          // is safe to go on is not found by reading the transcript.
          if (p.where) this.audit(`Where it stopped: ${p.where}`, undefined, "", true);
        }
        this.end();
        break;
      case "session.resume":
        this.end();
        this.el.append(h("div", { class: "audit" }, "Resumed"));
        break;
      case "session.model": {
        // The model and effort the next turns ask (ADR 41); one chosen while a
        // response ran is queued first, then applied when it ends.
        const what = `${p.model}${p.effort ? ` · ${p.effort} effort` : ""}`;
        this.audit(p.queued ? `Next message: ${what}` : `Model: ${what}`);
        break;
      }
      case "session.judge":
        // The judge the task asks from here on, chosen as it opened or since
        // (ADR 47), and who chose it.
        this.audit(p.server ? `Judge: ${p.server}${p.model ? ` · ${p.model}` : ""}${p.host ? ` at ${p.host}` : ""}` : "No judge: you answer the commands");
        break;
      case "session.limits":
        // The task's turn limits turned off or on (ADR 49): amber while its
        // turns may run, and wait, as long as they need.
        this.audit(p.on ? "Limits: on, turns stop at the limits in Settings" : "Limits: off, turns run as long as they need", p, p.on ? "" : "warn");
        break;
      case "context.narrow":
        // A lasting narrowing, such as a compaction's, is where later turns
        // start; any other ends with its turn.
        // Its system prompt is the session's own, so the details say nothing
        // new; a compaction's numbers are in its context.compact.
        if (p.lasting) this.audit(`The conversation goes on from here${p.agent ? ` (${p.agent})` : ""}`);
        else this.audit(auditLabels[event.kind], p);
        break;
      case "jev.req": {
        // A judge request: what it used, and with the judge's prices what it cost
        // (an estimate: the judge reports no cost). The kind of question is the
        // tool without its judge- prefix; an older log's jev-* show as they are.
        const u = p.usage;
        const kind = String(p.tool || "").replace(/^judge-/, "");
        const used = u ? ` · ${(u.input_tokens || 0).toLocaleString()} → ${(u.output_tokens || 0).toLocaleString()} tokens${u.cost !== undefined ? ` · ${usd(u.cost, true)}` : ""}` : "";
        if (!p.ok && !u) this.audit(`The judge (${kind}) did not answer: ${p.error || "no answer"}`, undefined, "bad");
        else this.audit(`Asked the judge (${kind})${used}`, { model: p.model, answer: p.answer, origin: p.origin, sent: `${(p.bytes || 0).toLocaleString()} bytes`, error: p.error });
        break;
      }
      case "context.compact": {
        // Sizes are the window's estimates, the unit compaction measures in.
        const n = (v) => `~${Number(v || 0).toLocaleString()}`;
        const why = { auto: `past ${n(p.threshold)} tokens`, command: "/compact", requested: "asked for by the strategy" }[p.trigger] || p.trigger;
        if (!p.compacted) this.audit(`Compaction (${why}) gave up: no handover note came back, so the turn goes on in the full context of ${n(p.before_tokens)} tokens`, undefined, "bad");
        else
          this.audit(
            `Compacted the conversation (${why}): context ${n(p.before_tokens)} → ${n(p.after_tokens)} tokens`,
            [`Handover note: ${Number(p.note_tokens || 0).toLocaleString()} tokens${p.truncated ? ", cut short at the output limit" : ""}`, p.focus ? `Asked to keep: ${p.focus}` : "", "Sizes are nodwyn's estimates, the unit compaction measures in; a provider may count a fifth fewer."].filter(Boolean).join("\n"),
            p.truncated ? "bad" : "",
          );
        break;
      }
      case "egress.check":
        // Every model request checks where its key may go; an allowed check
        // is routine and stays in the log. Only a refusal is news.
        if (!p.bound) this.audit(`Refused to send ${p.class || "a credential"} to ${p.host || "that host"}`, p.reason || p, "bad");
        break;
      case "mcp.egress":
        // A contained server's connection through its proxy is routine and
        // stays in the log; one it was not let through is news.
        if (p.dropped) this.audit(`${p.server}: ${p.dropped} more connection attempts were not recorded`, p, "bad");
        else if (!p.allowed) this.audit(`${p.server} could not reach ${p.host}${p.port ? `:${p.port}` : ""}`, p.reason || p, "bad");
        break;
      case "cred.refresh":
        // A refresh is routine; one that failed is why a call was refused.
        if (p.ok) this.audit(`Refreshed ${p.class} at ${p.endpoint}`, p);
        else this.audit(`Could not refresh ${p.class} at ${p.endpoint}`, p.error || p, "bad");
        break;
      case "doc.upload":
        // What left the machine, or why nothing did.
        if (p.ok) this.audit(`Sent ${p.path} to ${p.host}`, p.url || p);
        else if (p.sent) this.audit(`Sent ${p.path} to ${p.host}, which refused it`, p.error || p, "bad");
        else this.audit(`Did not send ${p.path} to ${p.host}`, p.error || p, "bad");
        break;
      case "setting.change": {
        const name = { "docs.google_upload": "Uploads to Google Docs" }[p.key] || p.key;
        const value = uploadLabels[p.value] || p.value;
        if (p.state === "applied") this.audit(`${name}: ${value}, saved in config.toml`, p);
        else this.audit(`${name}: ${value} was not saved`, p.error || p, "bad");
        break;
      }
      case "worktree.create":
      case "worktree.restore":
      case "worktree.remove": {
        // Where the task works, when it has a git worktree of its own (ADR 34).
        const from = p.base ? `${p.base} at ${String(p.base_commit || "").slice(0, 7)}` : `commit ${String(p.base_commit || "").slice(0, 7)}`;
        const left = p.uncommitted ? ` ${p.uncommitted} uncommitted file${p.uncommitted === 1 ? " is" : "s are"} in the repository but not here.` : "";
        // Files left out of the worktree are news (ADR 34): the conversation
        // is where they are counted.
        if (event.kind === "worktree.create") this.audit(`Working in a git worktree of its own, on ${p.branch}, from ${from}.${left}`, p.path, "", !!p.uncommitted);
        else if (event.kind === "worktree.restore") this.audit(`Made its worktree again, on ${p.branch}`, p.path);
        else this.audit(`Removed its worktree and ${p.branch}: nothing in them was unlanded`, p.path);
        break;
      }
      case "approval.change":
        // "Don't ask" switched, rather than one standing answer.
        if (p.kind === "all") this.audit(p.off ? "Don't ask: off, writes and commands ask again" : "Don't ask: on, writes and commands approved without asking", p, p.off ? "" : "warn");
        else this.audit(auditLabels[event.kind], p);
        break;
      default:
        if (auditLabels[event.kind]) this.audit(auditLabels[event.kind], p);
    }
    this.paintActivity();
  }
  toolCall(p) {
    const kind = category(p.name);
    const summaryText = callSummary(p.name, p.args);
    if (kind === "write") {
      // Writes in a row fold into "Edited", as reads fold into "Looked
      // around": the header counts distinct files, each write keeps its row
      // inside with its outcome. A line of narration between two writes goes
      // into the group, in order; a read, a command, a mark or a longer reply
      // ends the group, so the transcript keeps its order.
      this.group = null;
      this.marks = null;
      this.closeCommands();
      if (this.writeGroup) this.absorb();
      else {
        const label = h("span", { class: "label" }, "Edited");
        const what = h("span", { class: "what" });
        const body = h("div", { class: "activity-body" });
        const el = h("details", { class: "activity writes" }, h("summary", {}, h("span", { class: "chev" }, icon("right", 14)), label, what), body);
        this.activity(el);
        if (this.thought) body.append(this.thought);
        this.thought = null;
        this.writeGroup = { el, body, what, fold: this.fold, files: new Set(), failed: 0 };
      }
      const g = this.writeGroup;
      const a = argsObject(p.args);
      const file = a.path || a.target || summaryText;
      if (file) g.files.add(file);
      g.what.textContent = [g.files.size === 1 ? "1 file" : `${g.files.size} files`, g.failed ? `${g.failed} failed` : ""].filter(Boolean).join(" · ");
      const verb = { "fs-edit": "Edit", "doc-export": "Export" }[p.name] || "Write";
      const result = h("span", { class: "result" }, "running…");
      const note = h("span", { class: "aside-note" });
      const step = h("div", { class: "step" }, icon("file", 15), h("span", {}, verb), h("span", { class: "mono" }, file), result, note);
      const detail = h("details", { class: "step-detail" }, h("summary", {}, "Details"), h("pre", {}, typeof p.args === "string" ? p.args : JSON.stringify(p.args, null, 2)));
      g.body.append(step, detail);
      this.call = { id: p.id, name: p.name, kind, step, result, note, detail, writeGroup: g, fold: this.fold };
      return;
    }
    if (kind === "read") {
      this.closeCommands();
      this.writeGroup = null;
      this.marks = null;
      if (!this.group) {
        const label = h("span", { class: "label" }, isMCP(p.name) ? "Gathered" : "Looked around");
        const what = h("span", { class: "what" });
        const names = h("span", { class: "names" });
        const body = h("div", { class: "activity-body" });
        const el = h("details", { class: "activity" }, h("summary", {}, h("span", { class: "chev" }, icon("right", 14)), label, what, names), body);
        this.activity(el);
        if (this.thought) body.append(this.thought);
        this.thought = null;
        this.group = { el, body, what, names, label, counts: {}, tools: new Set(), servers: new Set() };
      }
      const g = this.group;
      g.counts[lookKind(p.name)] = (g.counts[lookKind(p.name)] || 0) + 1;
      g.tools.add(toolLabel(p.name));
      if (isMCP(p.name)) {
        g.servers.add(p.name.split("__")[0]);
        g.label.textContent = "Gathered";
      }
      g.what.textContent = g.servers.size ? `${g.servers.size} server${g.servers.size === 1 ? "" : "s"} · ${describe(g.counts)}` : describe(g.counts);
      g.names.textContent = [...g.tools].slice(0, 3).join(" · ");
      const row = h("details", { class: "call" }, h("summary", {}, h("span", { class: "name" }, toolLabel(p.name)), h("span", { class: "args" }, summaryText), h("span", { class: "stat" }, "…")), h("div", { class: "call-body" }, detailsBlock("Arguments", p.args)));
      g.body.append(row);
      this.call = { id: p.id, name: p.name, kind, row, stat: row.querySelector(".stat"), body: row.querySelector(".call-body"), group: g, fold: this.fold };
      return;
    }
    if (kind === "command") {
      // Commands in a row fold into one run, as reads fold into "Looked
      // around" and writes into "Edited": the header says how many ran
      // and for how long, whether one is running now or waiting for an
      // answer, and how many failed; each command keeps its row inside. A
      // read, a write or a reply's text ends the run, so the transcript
      // keeps its order.
      this.group = null;
      this.writeGroup = null;
      this.marks = null;
      if (!this.commandGroup) {
        const label = h("span", { class: "label" });
        const what = h("span", { class: "what" });
        const body = h("div", { class: "activity-body" });
        const el = h("details", { class: "activity commands" }, h("summary", {}, h("span", { class: "chev" }, icon("right", 14)), label, what), body);
        this.activity(el);
        if (this.thought) body.append(this.thought);
        this.thought = null;
        this.commandGroup = { el, body, what, label, fold: this.fold, done: 0, failed: 0, ms: 0, startedAt: 0, waiting: false };
      }
      const g = this.commandGroup;
      const row = h("details", { class: "call" }, h("summary", {}, h("span", { class: "name" }, commandLine(p.args) || summaryText), h("span", { class: "stat" }, "running…")), h("div", { class: "call-body" }, detailsBlock("Arguments", p.args)));
      g.body.append(row);
      g.startedAt = Date.now();
      g.waiting = false;
      this.paintCommands(g);
      this.startClock();
      this.call = { id: p.id, name: p.name, kind, row, stat: row.querySelector(".stat"), body: row.querySelector(".call-body"), commandGroup: g, command: commandLine(p.args) || summaryText, fold: this.fold };
      this.paintActivity();
      return;
    }
    this.group = null;
    this.writeGroup = null;
    this.marks = null;
    this.closeCommands();
    this.thought = null;
    let line;
    if (isMCP(p.name)) line = [icon("plug", 15), h("span", { class: "mono" }, `${toolLabel(p.name)} ${summaryText}`.trim())];
    else line = [icon("terminal", 15), h("span", { class: "mono" }, `${p.name} ${summaryText}`.trim())];
    const result = h("span", { class: "result" }, "running…");
    const note = h("span", { class: "aside-note" });
    const step = h("div", { class: "step" }, line, result, note);
    const detail = h("details", { class: "step-detail" }, h("summary", {}, "Details"), h("pre", {}, typeof p.args === "string" ? p.args : JSON.stringify(p.args, null, 2)));
    this.activity(step, detail);
    this.call = { id: p.id, name: p.name, kind, step, result, note, detail, command: p.name === "shell-start" ? commandLine(p.args) : "", fold: this.fold };
    this.paintActivity();
  }
  // waiting marks the call in progress as stopped at a question, or not. It
  // is called each time the page places its prompts, so only a change of
  // state repaints. A command waiting for an answer is not running: its run
  // says it waits until the answer comes.
  waiting(on) {
    const c = this.call;
    if (c) {
      c.waiting = on;
      this.paintActivity(c.fold);
    }
    if (c?.commandGroup) {
      const g = c.commandGroup;
      if (g.waiting === on) return;
      g.waiting = on;
      c.stat.textContent = on ? "waiting for your answer" : "running…";
      this.paintCommands(g);
      this.startClock();
      return;
    }
    if (!c?.result || c.result.className !== "result") return;
    c.result.textContent = on ? "waiting for your answer" : "running…";
  }
  decision(p) {
    const c = this.call;
    let text = "";
    if (p.decision === "deny") text = "denied";
    else if (p.checker === "standing") text = (p.reason || "").includes("approval.writes") ? "went ahead: a write in the folder" : "standing approval";
    else if (p.decision === "confirm" && p.actor === "judge") text = p.answer === "yes" ? "the judge allowed" : "the judge refused";
    else if (p.decision === "confirm" && p.actor === "unattended") text = "declined: nobody to ask";
    else if (p.decision === "confirm" && p.scope === "all") text = "allowed: don't ask";
    else if (p.decision === "confirm" && p.actor === "setting") text = "allowed by Settings › Approvals";
    else if (p.decision === "confirm") text = p.answer === "yes" ? (p.scope && p.scope !== "once" ? `you allowed · ${p.scope}` : "you allowed once") : p.answer === "turn" ? "allowed for this turn" : "you denied";
    // An index build reads every file through the guard: those reads are
    // its own, summed by its index.update, and a file left out does not
    // make the search fail. Only a question a person answered marks it.
    if (c && p.origin === "model" && indexing.has(c.name) && p.prim !== c.name && !(p.decision === "confirm" && p.actor !== "not asked")) return;
    if (!c || p.origin !== "model") {
      // A decision outside a tool call (user-origin, a checker's own prompt)
      // is still audit history.
      if (p.decision !== "allow" || p.checker) this.audit(`Guard: ${p.prim} ${p.decision}${p.reason ? ` · ${p.reason}` : ""}`, p, p.decision === "deny" ? "bad" : "");
      return;
    }
    // A wrapper's empty tool arguments do not say which command it runs.
    // The guard's recorded arguments are already redacted for display.
    if ((p.prim === "shell-exec" || p.prim === "shell-start") && p.args && c.kind !== "command") {
      c.command = `${p.prim} ${p.args}`;
      if (!c.commandNode) {
        // Read-style wrappers have a disclosure row, not a step. Keep the
        // nested command in its argument span so replay can finish too.
        if (c.kind === "read") c.commandNode = c.row.querySelector(".args");
        else {
          c.commandNode = h("span", { class: "mono" });
          c.step.append(c.commandNode);
        }
      }
      c.commandNode.textContent = c.command;
      this.paintActivity(c.fold);
    }
    if (!text) return;
    if (c.note) c.note.textContent = text;
    else c.stat.dataset.decision = text;
    if (p.decision === "deny" && c.note) c.result.className = "result failed";
  }
  // indexUpdate puts what a build or refresh of the repository's index did
  // under the call that made it (ADR 50); each read's own guard decision is
  // in the log, not drawn.
  indexUpdate(p) {
    const c = this.call;
    if (c?.body && indexing.has(c.name)) c.body.prepend(h("p", { class: "index-update" }, indexLine(p)));
    else this.audit(indexLine(p), p);
  }
  toolResult(p) {
    let c = this.call;
    if (!c || c.id !== p.id || c.name !== p.name) {
      // A plan-step or plan-write the strategy answered is shown by its
      // plan.step or gate.plan line.
      if (p.name === "plan-step" && p.ok && /^Step \d+ is /.test(p.value || "")) return;
      if (p.name === "plan-write" && p.ok && /^Plan recorded: /.test(p.value || "")) return;
      // A result without its call in view (a skipped or resumed call) stands alone.
      // A call the strategy skipped (skip-tools answers it "Not executed:
      // reason", for the model to read) never ran and failed at nothing: an
      // explorer out of rounds skips its last calls. It is a routine note
      // behind Activity, not a failure drawn in red outside it.
      if (!p.ok && /^Not executed: /.test(p.error || "")) this.skip(p.name, p.error.replace(/^Not executed: /, ""));
      else this.audit(`${toolLabel(p.name)} ${p.ok ? "returned" : "failed"}`, p.ok ? p.value : p.error, p.ok ? "" : "bad");
      return;
    }
    const body = p.ok ? p.value : p.error;
    const ms = p.ms === undefined ? "" : p.ms >= 1000 ? `${(p.ms / 1000).toFixed(1)} s` : `${p.ms} ms`;
    if (c.kind === "read") {
      c.stat.className = `stat ${p.ok ? "ok" : "failed"}`;
      c.stat.textContent = [p.ok ? sizeOf((body || "").length) : "failed", ms, c.stat.dataset.decision].filter(Boolean).join(" · ");
      c.body.append(detailsBlock(p.ok ? "Result" : "Error", body));
      if (!p.ok) {
        c.group.counts.failed = (c.group.counts.failed || 0) + 1;
        c.group.what.textContent = describe(c.group.counts);
      }
    } else if (c.kind === "command") {
      // A finished command freezes its row and adds to its run. A failed or
      // refused one opens its row and the run around it, so it is seen
      // without looking for it.
      const g = c.commandGroup;
      const status = p.ok ? exitStatus(body) : null;
      const failed = !p.ok || (status !== null && status !== 0);
      g.done++;
      if (failed) g.failed++;
      g.ms += typeof p.ms === "number" ? p.ms : Date.now() - g.startedAt;
      g.startedAt = 0;
      g.waiting = false;
      c.stat.textContent = [!p.ok ? "failed" : status !== null ? `exit ${status}` : "done", ms, c.stat.dataset.decision].filter(Boolean).join(" · ");
      c.stat.className = `stat ${failed ? "failed" : "ok"}`;
      c.body.append(detailsBlock(p.ok ? "Result" : "Error", body));
      if (failed) c.row.open = g.el.open = true;
      this.paintCommands(g);
      this.stopClock();
    } else {
      const status = p.ok ? exitStatus(body) : null;
      c.result.replaceChildren();
      if (!p.ok) {
        c.result.className = "result failed";
        c.result.append(icon("cross", 14, 2), "failed");
      } else if (status !== null) {
        c.result.className = `result ${status === 0 ? "ok" : "failed"}`;
        c.result.append(status === 0 ? icon("check", 14, 2) : icon("cross", 14, 2), `exit ${status}`);
      } else {
        c.result.className = "result ok";
        c.result.append(icon("check", 14, 2), c.kind === "write" ? "written" : "done");
      }
      c.note.textContent = [ms, c.note.textContent].filter(Boolean).join(" · ");
      c.detail.append(h("pre", {}, body || ""));
      if (!p.ok) {
        c.detail.open = true;
        // A failed write opens the group around it, so it is seen without
        // looking for it.
        if (c.writeGroup) {
          c.writeGroup.failed++;
          c.writeGroup.what.textContent = [c.writeGroup.files.size === 1 ? "1 file" : `${c.writeGroup.files.size} files`, `${c.writeGroup.failed} failed`].join(" · ");
          c.writeGroup.el.open = true;
        }
      }
    }
    // A bad note between the call and its result starts a new fold: the
    // count belongs on the call's.
    this.call = null;
    this.paintActivity(c.fold);
  }
}

// usd writes a cost as the terminal clients do (plain.USD): ≈ marks an
// estimate from configured prices.
export function usd(c, estimated) {
  const s = c > 0 && c < 0.0001 ? "<$0.0001" : c < 0.01 ? `$${c.toFixed(4)}` : c < 1 ? `$${c.toFixed(3)}` : `$${c.toFixed(2)}`;
  return estimated ? `≈${s}` : s;
}

// contextGauge is how big the context the model holds is, from the log: the
// window's estimate of the latest control-model request and what the
// provider counted for its prompt, the window's high water (the session's,
// or the model's it was moved to), and the compactions so far. While a
// narrowed context is open (the explorer's, the planner's, a sub-agent's)
// and has asked, its latest request is the one followed, and agent names
// it: a task's first requests are the explorer's and the planner's, and the
// gauge stood empty through them (session 20261005T060224Z-2f9eb922's ran
// 830 records before the conversation's first request). Once it folds the
// conversation's own figure is shown again, so a later turn's planner does
// not drop a large conversation's bar to a few thousand tokens.
export function contextGauge(events) {
  const g = { est: 0, reported: 0, high: 0, compactions: 0, agent: "" };
  const conversation = { est: 0, reported: 0 };
  let frames = []; // the narrowed contexts open, innermost last
  let followed = null; // the frame the next llm.resp answers
  for (const e of events) {
    const p = e.payload || {};
    switch (e.kind) {
      case "session.start":
      case "session.resume":
      case "session.model": // the window sized for the model it was moved to (ADR 40)
        if (p.context?.high_water) g.high = p.context.high_water;
        break;
      case "user.msg":
      case "context.widen":
        frames = []; // a narrowed context ends with the turn
        break;
      case "context.narrow":
        if (!p.lasting) frames.push({ agent: p.agent || "narrowed", est: 0, reported: 0 });
        break;
      case "context.fold":
        frames.pop();
        break;
      case "llm.req":
        followed = p.endpoint ? null : frames.at(-1) || conversation;
        if (followed) [followed.est, followed.reported] = [p.est_tokens || 0, 0];
        break;
      case "llm.resp":
        if (followed) followed.reported = p.usage?.prompt_tokens || 0;
        followed = null;
        break;
      case "context.compact":
        g.compactions++;
        if (p.compacted) [conversation.est, conversation.reported] = [p.after_tokens || 0, 0];
        break;
    }
  }
  const shown = frames.at(-1)?.est ? frames.at(-1) : conversation;
  [g.est, g.reported, g.agent] = [shown.est, shown.reported, shown === conversation ? "" : shown.agent];
  return g;
}

// judgeTally adds up the judge's requests among events: calls, tokens, and
// the cost as far as priced ([judge] prices, else its provider's; the judge reports no cost).
export function judgeTally(events) {
  const j = { calls: 0, tokens: 0, cost: 0, unpriced: 0 };
  for (const e of events) {
    if (e.kind !== "jev.req") continue;
    j.calls++;
    const u = e.payload?.usage;
    if (!u) continue;
    j.tokens += (u.input_tokens || 0) + (u.output_tokens || 0);
    if (u.cost !== undefined) j.cost += u.cost;
    else j.unpriced++;
  }
  return j;
}

// planSteps reads a recorded plan's numbered or bulleted lines as steps; a
// plan in prose is one step. The core reads it the same way
// (log.GatePlan.Steps), so a plan.step's number is an index into this.
export function planSteps(plan) {
  const lines = String(plan || "")
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  const steps = lines.filter((l) => /^(\d+[.)]|[-*•])\s+/.test(l)).map((l) => l.replace(/^(\d+[.)]|[-*•])\s+/, ""));
  if (steps.length) return steps;
  return lines.length ? [lines.join(" ")] : [];
}

// stepTitle is the clause of a step that names it: the plan's shape asks
// each step to start "Kind: what it touches — what changes", so the text up
// to the first dash (or, without one, the first sentence or semicolon) is
// the title and the rest reads behind the fold. A step with no boundary
// stays whole while it fits the cap; longer, it is cut at a word and the
// ellipsis says the fold holds the rest.
export function stepTitle(step) {
  const s = String(step || "").trim();
  const at = s.search(/(?:\s+—|\s+–|\s+--|\.\s|;\s)/);
  const head = at < 0 ? s : s.slice(0, at).trimEnd();
  if (head.length > 80) {
    const cut = head.slice(0, 80);
    const word = cut.lastIndexOf(" ");
    return (word > 40 ? cut.slice(0, word) : cut).trimEnd() + " …";
  }
  return head || s;
}
