// Settings: what nodwyn does unless a task says otherwise. Most of it is
// config.toml, read from settings.read and changed with settings.write on
// the computer nodwyn runs on: the daemon validates and writes, tasks
// started afterwards get the result, and open ones keep what they started
// with. Appearance and notifications belong to this browser.
import { h, icon, toast, ago } from "./dom.js";
import { keychainForm, passphraseForm, noPassphraseWarning } from "./setup.js";
import { modelsRows, resetModelsUi } from "./models.js";
import { uploadLabels } from "./transcript.js";

// ---------- theme (a per-browser convenience; dark unless chosen) ----------
const themes = ["dark", "light", "system"];
export function storedTheme() {
  try {
    const mode = localStorage.getItem("nodwyn-theme");
    return themes.includes(mode) ? mode : "dark";
  } catch {
    return "dark";
  }
}
export function applyTheme(mode) {
  if (!themes.includes(mode)) mode = "dark";
  document.documentElement.dataset.theme = mode;
  try {
    if (mode === "dark") localStorage.removeItem("nodwyn-theme");
    else localStorage.setItem("nodwyn-theme", mode);
  } catch {
    /* a browser that refuses storage still gets the theme for this tab */
  }
}

// ---------- the model's thinking (a per-browser convenience; hidden unless chosen) ----------
// Most of the time nobody reads it, and a row per reply buries the work, so
// the transcript keeps it out of sight, as the TUI does without :thinking.
// Shown or hidden is a class on the page, so it applies to what is already
// drawn without drawing it again.
export function storedThinking() {
  try {
    return localStorage.getItem("nodwyn-thinking") === "shown";
  } catch {
    return false;
  }
}
export function applyThinking(shown) {
  if (shown) document.documentElement.dataset.thinking = "shown";
  else delete document.documentElement.dataset.thinking;
  try {
    if (shown) localStorage.setItem("nodwyn-thinking", "shown");
    else localStorage.removeItem("nodwyn-thinking");
  } catch {
    /* a browser that refuses storage still gets the choice for this tab */
  }
}

// What a task does without asking: [approval] in config.toml (ADR 35).
const writeChoices = [["allow", "Without asking"], ["ask", "Ask me"]];
const dontAskChoices = [[false, "Not offered"], [true, "Offered in each task"]];
const commandChoices = [["judge", "The judge, then me"], ["ask", "Ask me"], ["allow", "Without asking"]];

function writesHint(writes) {
  if (writes === "ask") return "Every write waits for your answer. “Always” covers its folder for the rest of the task.";
  return "Anywhere in the task's folder. Each change is shown with its diff, and the file's previous version is kept, so Restore or nodwyn undo puts it back; git keeps what it has committed.";
}

function commandsHint(commands, judge) {
  if (commands === "ask") return "You answer each command no allow list covers. “Always” covers that command for the rest of the task.";
  if (commands === "allow") return "Commands, and tools that act such as an MCP server's, run without asking. With writes also free, the model can write code and run it with nobody looking.";
  return `The judge is shown the task, its plan and the command, and lets it run above its threshold (Judge, below). When it doesn't, you're asked, and told what it answered. “Always” covers that command for the rest of the task.${judge ? "" : " No judge is set up now, so you're asked."}`;
}

// ---------- what each setting takes ----------
// count: a whole number of 0 or more; integer: any whole number; decimal: a
// number of 0 or more; duration: Go's, such as 90s, 20m or 2h; text: as typed;
// choice: one of the chips. The daemon judges every value again.
const specs = {
  "policy.allow_insecure_http": { kind: "choice", label: "Credentials over remote HTTP" },
  "approval.writes": { kind: "choice", label: "Writing files" },
  "approval.commands": { kind: "choice", label: "Running commands" },
  "approval.dont_ask": { kind: "choice", label: "Don't ask in tasks" },
  "budget.tool_calls_soft": { kind: "count", label: "Tool calls, ask at", follows: true },
  "budget.tool_calls_hard": { kind: "count", label: "Tool calls, stop at" },
  "budget.tokens_soft": { kind: "count", label: "Tokens used, ask at", follows: true },
  "budget.tokens_hard": { kind: "count", label: "Tokens used, stop at" },
  "budget.actions_soft": { kind: "count", label: "Actions, ask at", follows: true },
  "budget.actions_hard": { kind: "count", label: "Actions, stop at" },
  "budget.wall_soft": { kind: "duration", label: "Time, ask at", follows: true },
  "budget.wall_hard": { kind: "duration", label: "Time, stop at" },
  "budget.cost_soft": { kind: "decimal", label: "Spend, ask at", follows: true },
  "budget.cost_hard": { kind: "decimal", label: "Spend, stop at" },
  "loop.request_timeout": { kind: "duration", label: "A model reply" },
  "loop.shell_timeout": { kind: "duration", label: "A command" },
  "judge.server": { kind: "choice", label: "Judge provider" },
  "judge.content": { kind: "choice", label: "What the judge sees" },
  "judge.approve_threshold": { kind: "decimal", label: "Allows a command at" },
  "judge.price_input": { kind: "decimal", label: "Judge price of input" },
  "judge.price_output": { kind: "decimal", label: "Judge price of output" },
  "judge.timeout": { kind: "duration", label: "Waits for an answer" },
  "judge.model": { kind: "text", label: "Judge model" },
  "mcp.runtime": { kind: "choice", label: "Container runtime" },
  "window.high_water": { kind: "count", label: "High water" },
  "window.low_water": { kind: "count", label: "Low water" },
  "window.max_result_tokens": { kind: "count", label: "Largest tool result" },
  "window.output_reserve": { kind: "count", label: "Fallback reply cap" },
};

// Unsaved edits by setting: the text typed into a field, or the value a
// choice picked ("" for the default). They survive the page being redrawn
// while tasks run, and go to the daemon together.
const edits = new Map();
let saveError = "";
let saving = false;
let refreshBar = () => {};

const values = (desk) => desk.settings?.values || {};
const same = (a, b) => (a ?? null) === (b ?? null);
const grouped = (n) => (Number.isSafeInteger(n) && Math.abs(n) >= 10000 ? n.toLocaleString("en-US") : String(n));

// shown is how a value reads in its field.
function shown(value, kind) {
  if (value === null || value === undefined) return "";
  if (kind === "count" || kind === "integer") return grouped(value);
  return String(value);
}

// initial is what a field shows before an edit: empty when the default
// applies, so clearing a field puts the default back. A soft limit the file
// leaves unset follows its hard one, and shows empty too.
function initial(v, spec) {
  if (spec.follows && !v.set) return "";
  return v.set || !same(v.value, v.default) ? shown(v.value, spec.kind) : "";
}

function parse(kind, text) {
  const plain = text.replace(/[,_\s]/g, "").replace(/^\$/, "");
  switch (kind) {
    case "count":
    case "integer": {
      const ok = kind === "count" ? /^\d+$/ : /^-?\d+$/;
      if (!ok.test(plain) || !Number.isSafeInteger(Number(plain))) return { error: kind === "count" ? "a whole number" : "a whole number, such as 42" };
      return { value: Number(plain) };
    }
    case "decimal":
      if (!/^(\d+\.?\d*|\.\d+)$/.test(plain)) return { error: "a number, such as 0.5" };
      return { value: Number(plain) };
    case "duration":
      if (!/^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/.test(text.trim())) return { error: "a duration, such as 90s, 20m or 2h" };
      return { value: text.trim() };
  }
  return { value: text.trim() };
}

// changes is what the edits would change, as settings.write takes them.
function changes(desk) {
  const set = {};
  const errors = [];
  for (const [name, edit] of edits) {
    const v = values(desk)[name];
    const spec = specs[name];
    if (!v || !spec) continue;
    if (spec.kind === "choice") {
      if (!same(edit, v.value)) set[name] = edit === "" ? null : edit;
      continue;
    }
    const text = String(edit).trim();
    if (text === initial(v, spec)) continue;
    if (text === "") {
      set[name] = null;
      continue;
    }
    const parsed = parse(spec.kind, text);
    if (parsed.error) errors.push(`${spec.label} takes ${parsed.error}.`);
    else if (!same(parsed.value, v.value) || !v.set) set[name] = parsed.value;
  }
  return { set, errors };
}

// ---------- fields ----------
function editable(conn) {
  return conn.has("settings.write");
}

// input is one setting's field: it shows what config.toml sets, or is
// empty with the default as its placeholder.
function input(ctx, name, { placeholder, width = "", prefix = "", suffix = "" } = {}) {
  const v = values(ctx.desk)[name];
  const spec = specs[name];
  if (!v || !spec) return null;
  const numeric = spec.kind === "count" || spec.kind === "integer" || spec.kind === "decimal";
  const node = h("input", {
    class: `input setting${width ? ` ${width}` : ""}`,
    id: `set-${name}`,
    "aria-label": spec.label,
    inputmode: spec.kind === "decimal" ? "decimal" : numeric ? "numeric" : null,
    autocomplete: "off",
    spellcheck: false,
    disabled: !editable(ctx.conn),
    placeholder: placeholder ?? (shown(v.default, spec.kind) || "—"),
    oninput: () => {
      edits.set(name, node.value);
      saveError = "";
      refreshBar();
    },
  });
  node.value = edits.has(name) ? edits.get(name) : initial(v, spec);
  if (!prefix && !suffix) return node;
  return h("span", { class: "affix" }, prefix ? h("span", { class: "unit" }, prefix) : null, node, suffix ? h("span", { class: "unit" }, suffix) : null);
}

// choice is a setting picked from chips; "" is the default.
function choice(ctx, name, options, redraw) {
  const v = values(ctx.desk)[name];
  if (!v) return null;
  const current = edits.has(name) ? edits.get(name) : (v.value ?? "");
  return h(
    "div",
    { class: "row-actions", role: "group", "aria-label": specs[name].label },
    options.map(([value, label]) =>
      h(
        "button",
        {
          type: "button",
          class: "chip",
          id: `set-${name}-${value || "default"}`,
          "aria-pressed": String(String(current) === String(value)),
          disabled: !editable(ctx.conn),
          onclick: () => {
            edits.set(name, value);
            saveError = "";
            redraw();
          },
        },
        label,
      ),
    ),
  );
}

// picked is a choice's value as edited, else as set.
function picked(desk, name) {
  return edits.has(name) ? edits.get(name) : (values(desk)[name]?.value ?? "");
}

const row = (label, ...value) => [h("div", { class: "label" }, label), h("div", { class: "value" }, ...value)];
const section = (id, title, intro, ...rows) => h("section", { class: "settings-section", id: `section-${id}`, "aria-labelledby": `heading-${id}` }, h("h2", { id: `heading-${id}` }, title), intro ? h("p", { class: "hint intro" }, intro) : null, h("div", { class: "settings-grid" }, ...rows));

// ---------- the page ----------

// tabs are Settings' pages, each a few sections: what a task does, the
// models, the judge (ADR 47), the context window, documents and containers,
// keys and devices, and this browser. The tab is in the address
// (#/settings/judge), so a link opens it and a reload keeps it; an unknown
// one is the first.
const tabs = [
  { id: "general", label: "General" },
  { id: "models", label: "Models" },
  { id: "judge", label: "Judge" },
  { id: "context", label: "Context" },
  { id: "tools", label: "Documents & containers" },
  { id: "keys", label: "Keys & devices" },
  { id: "browser", label: "This browser" },
];
// tabOf is the tab a route names, else the first.
export const tabOf = (name) => (tabs.some((t) => t.id === name) ? name : tabs[0].id);
let shownTab = "";

// passphraseOpen is whether the passphrase fold is open on Keys & devices.
let passphraseOpen = false;

export function renderSettings(root, ctx) {
  const { conn, desk } = ctx;
  const redraw = () => renderSettings(root, ctx);
  const tab = tabOf(ctx.tab);
  // Redrawn in place (a choice, a save, the models' status): keep the
  // scroll position and the field that has focus.
  const before = root.querySelector(":scope > main.settings-page");
  // Coming to Settings from another page, nothing is half-typed (a key in
  // particular is not kept across a visit).
  if (!before) {
    resetModelsUi();
    resetJudgeUi();
  }
  // Another tab starts at its top; a redraw of the same one stays where it was.
  const scrolled = before && shownTab === tab ? before.scrollTop : 0;
  shownTab = tab;
  const active = root.contains(document.activeElement) ? document.activeElement : null;
  const focus = active?.id ? { id: active.id, start: active.selectionStart, end: active.selectionEnd } : null;

  // An older daemon reports no values: its settings live in config.toml.
  const known = !!desk.settings?.values;
  // Only the tab shown is drawn; the edits waiting in the bar are kept across
  // tabs, so a change made in one is saved with one made in another.
  const build = {
    general: () => (known ? [approvalSection(ctx, redraw), limitsSection(ctx)] : [h("p", { class: "hint" }, "This daemon's settings live in config.toml.")]),
    models: () => [modelsSection(ctx, root)],
    judge: () => (known ? [judgeSection(ctx, redraw, root)] : [h("p", { class: "hint" }, "This daemon's judge is set in config.toml.")]),
    context: () => (known ? [windowSection(ctx)] : []),
    tools: () => [documentsSection(ctx), ...(known ? [containersSection(ctx, redraw)] : [])],
    keys: () => [keysSection(ctx, root)],
    browser: () => [browserSection(ctx), connectionSection(ctx)],
  };
  const sections = build[tab]().filter(Boolean);
  const nav = h(
    "nav",
    { class: "settings-tabs", role: "tablist", "aria-label": "Settings" },
    tabs.map((t) => h("a", { class: "settings-tab", role: "tab", id: `settings-tab-${t.id}`, href: `#/settings/${t.id}`, "aria-selected": String(t.id === tab), "aria-current": t.id === tab ? "page" : null }, t.label)),
  );
  const bar = h("div", { class: "save-bar", role: "region", "aria-label": "Unsaved settings", hidden: true });
  refreshBar = () => drawBar(bar, ctx, redraw);

  const page = h(
    "main",
    { class: "page settings-page" },
    h(
      "div",
      { class: "page-col" },
      h("h1", {}, "Settings"),
      known && !editable(conn) ? h("div", { class: "callout plain" }, "These are the settings tasks start with. Only the computer nodwyn runs on can change them.") : null,
      nav,
      h("div", { class: "settings-panel", role: "tabpanel", "aria-labelledby": `settings-tab-${tab}`, id: `settings-panel-${tab}` }, ...sections),
      bar,
    ),
  );
  root.replaceChildren(page);
  page.scrollTop = scrolled;
  refreshBar();
  const devices = root.querySelector("#devices");
  if (devices && conn.has("devices.list")) showDevices(devices, ctx);
  const uploads = root.querySelector("#uploads");
  if (uploads && desk.settings?.docs) showUploads(uploads, ctx);
  if (focus) {
    const node = document.getElementById(focus.id);
    node?.focus({ preventScroll: true });
    try {
      if (focus.start !== null && focus.start !== undefined) node?.setSelectionRange(focus.start, focus.end);
    } catch {
      /* not a text field */
    }
  }
}

// drawBar shows the unsaved edits and saves them together: one
// settings.write, so changes that only make sense together (a soft limit
// above the old hard one) are judged together.
function drawBar(bar, ctx, redraw) {
  const { conn, desk } = ctx;
  const { set, errors } = changes(desk);
  const count = Object.keys(set).length;
  bar.hidden = !editable(conn) || (count === 0 && errors.length === 0 && !saveError);
  if (bar.hidden) return;
  const now = "approval.dont_ask" in set ? ", but follow Don't ask at once" : "";
  const message = saveError || errors[0] || `${count} unsaved change${count === 1 ? "" : "s"}. Tasks you start afterwards use them; open tasks keep theirs${now}.`;
  bar.replaceChildren(
    h("p", { class: saveError || errors.length ? "error" : "", role: saveError || errors.length ? "alert" : "status" }, message),
    h(
      "div",
      { class: "row-actions" },
      h("button", { type: "button", class: "btn ghost", onclick: () => { edits.clear(); saveError = ""; redraw(); } }, "Discard"),
      h(
        "button",
        {
          type: "button",
          class: "btn accent",
          disabled: saving || count === 0 || errors.length > 0,
          onclick: async () => {
            saving = true;
            saveError = "";
            drawBar(bar, ctx, redraw);
            try {
              desk.settings = await conn.call("settings.write", { set });
              edits.clear();
              // "Don't ask" is the one setting open tasks follow at once.
              if ("approval.dont_ask" in set) toast(set["approval.dont_ask"] ? "Saved. Every task now offers Don't ask, off until you switch it on there." : "Saved. Don't ask is no longer offered, and is off in every open task.");
              else toast("Saved. Tasks you start from now on use these settings.");
            } catch (error) {
              // The daemon names the setting; the method's name is noise here.
              saveError = error.message.replace(/^settings\.write: /, "");
            } finally {
              saving = false;
            }
            redraw();
          },
        },
        saving ? "Saving…" : "Save",
      ),
    ),
  );
}

function approvalSection(ctx, redraw) {
  const { desk } = ctx;
  return section(
    "approval",
    "Approvals",
    "What a task does without asking you. Whatever these say, a write to a protected file (git hooks, CI, the Makefile, scripts, .nodwyn/), content that looks like a credential, and a command built from what a tool returned always ask, and nothing outside the task's folder is written.",
    ...row("Credentials over remote HTTP", choice(ctx, "policy.allow_insecure_http", [[false, "Require HTTPS"], [true, "Allow HTTP"]], redraw), h("p", { class: "hint" }, "Off by default. Allow HTTP only for servers you choose to trust on an unencrypted connection: anyone on the route can read or change credentials and content. Localhost remains available. Saved settings apply to new or resumed tasks; certificate checks, bindings and redirect refusals still apply.")),
    ...row("Writing files", choice(ctx, "approval.writes", writeChoices, redraw), h("p", { class: "hint" }, writesHint(picked(desk, "approval.writes")))),
    ...row("Running commands", choice(ctx, "approval.commands", commandChoices, redraw), h("p", { class: "hint" }, commandsHint(picked(desk, "approval.commands"), !!desk.settings?.judge))),
    ...row(
      "Don't ask in tasks",
      choice(ctx, "approval.dont_ask", dontAskChoices, redraw),
      String(picked(desk, "approval.dont_ask")) === "true"
        ? dontAskWarning()
        : h("p", { class: "hint" }, "A task has no switch that stops the questions. Commands go to whoever “Running commands” names, one at a time."),
    ),
  );
}

// dontAskWarning is what a person reads while "Don't ask" is offered: what
// it lets a task do, from what models did with it here (ADR 17, amended
// 2026-10-05).
function dontAskWarning() {
  return h(
    "div",
    { class: "danger-callout", id: "dont-ask-warning", role: "note", "aria-label": "Warning about Don't ask" },
    h("span", { class: "danger-icon" }, icon("warn", 22, 2)),
    h(
      "div",
      { class: "danger-body" },
      h("strong", {}, "Don't ask hands a task the keys to your account."),
      h("p", {}, "Each task gets a Don't ask switch. Once it is on, every write and command the model asks for runs as you — no judge, no question, nobody looking — until you switch it off or the task closes. Only protected files, content that looks like a key and commands built from tool output still ask."),
      h("p", {}, "What models did here while it was on, none of it asked:"),
      h(
        "ul",
        {},
        h("li", {}, "loaded your SSH private key into an agent to sign their commits;"),
        h("li", {}, "read, through the shell, a file the guard had just refused them;"),
        h("li", {}, "turned commit signing off to get a commit through;"),
        h("li", {}, "rebased your own checkout of the repository."),
      ),
      h("p", {}, "Switch it on only in a task you are watching, and off when it is done. Choosing Not offered switches it off in every open task."),
    ),
  );
}

function limitsSection(ctx) {
  const pair = (label, soft, hard, opts = {}) => [
    h("div", { class: "limit-name" }, label),
    input(ctx, soft, { width: "narrow", placeholder: "the stop", ...opts.soft }),
    input(ctx, hard, { width: "narrow", ...opts.hard }),
  ];
  const v = values(ctx.desk);
  const costOn = v["budget.cost_hard"]?.value > 0;
  return section(
    "limits",
    "Limits",
    "Each turn — a message and the work it sets off — stops at “stop at”. It asks you whether to go on only if you set an “ask at” below that. Leave a field empty for its default.",
    ...row(
      "Each turn",
      h(
        "div",
        { class: "limits" },
        h("div", {}),
        h("div", { class: "limit-head" }, "Ask at"),
        h("div", { class: "limit-head" }, "Stop at"),
        ...pair("Tool calls", "budget.tool_calls_soft", "budget.tool_calls_hard"),
        ...pair("Tokens used", "budget.tokens_soft", "budget.tokens_hard"),
        ...pair("Actions", "budget.actions_soft", "budget.actions_hard"),
        ...pair("Time", "budget.wall_soft", "budget.wall_hard"),
      ),
      h("p", { class: "hint" }, "Tokens used adds up each request's prompt and reply, a prompt token the provider read from its cache counted as a tenth. Every request sends the whole conversation again, so it grows faster than the conversation. Actions are writes, commands and other steps that change something."),
    ),
    ...row(
      "Spend",
      h(
        "div",
        { class: "limits" },
        h("div", {}),
        h("div", { class: "limit-head" }, "Ask at"),
        h("div", { class: "limit-head" }, "Stop at"),
        ...pair("The whole task", "budget.cost_soft", "budget.cost_hard", { soft: { prefix: "$", placeholder: costOn ? "the stop" : "—" }, hard: { prefix: "$", placeholder: "off" } }),
      ),
      h("p", { class: "hint" }, "In US dollars, for what a task's model requests cost as far as known: as the provider reports it, or from each model's own prices (Models, below). It counts every turn of a task, and is off until “stop at” is set. A request nobody priced isn't counted."),
    ),
    ...row(
      "Waiting",
      h("div", { class: "row-actions" }, h("span", { class: "muted" }, "A model reply up to"), input(ctx, "loop.request_timeout", { width: "narrow" }), h("span", { class: "muted" }, "and a command up to"), input(ctx, "loop.shell_timeout", { width: "narrow" })),
    ),
  );
}

export function modelRows(desk) {
  if (!desk.models) return [h("p", { class: "hint" }, "Checking the configured models…")];
  const words = { ready: "answers", missing: "server answers; model not installed", unreachable: "does not answer", unconfigured: "not set up", needs_key: "needs its key", locked: "keychain locked", unbound: "key not allowed" };
  return [
    h(
      "div",
      { class: "list" },
      desk.models.map((m) =>
        h(
          "div",
          { class: "list-row" },
          h("span", { class: `dot ${m.state === "ready" ? "ok" : m.state === "unreachable" ? "bad" : "warn"}` }),
          h("div", { class: "main" }, h("strong", {}, m.name === "control" ? "Default model" : m.name), h("span", {}, m.model ? `${m.model} · ${m.provider}${m.host ? ` at ${m.host}` : ""}` : "No model configured")),
          h("div", { class: "end" }, m.detail || words[m.state] || m.state),
        ),
      ),
    ),
    h("p", { class: "hint" }, "A reachability check: nodwyn asks each model server for its list of models and sends no credential."),
  ];
}

function modelsSection(ctx, root) {
  return section(
    "models",
    "Models",
    "The providers nodwyn talks to and the models on each, with each model's own settings, and what new tasks start on. Saved in config.toml with its other lines kept; a key goes to the keychain. Tasks already open keep the model, effort and judge they have, and change them under their own message box.",
    ...modelsRows(ctx, () => renderSettings(root, ctx)),
  );
}

// ---------- the judge (ADR 46, ADR 47) ----------

// What the Judge tab is typing: a key for a judge being added, by kind, kept
// out of the page's state the moment it is sent, and dropped when Settings
// is left.
const judgeKeys = { jev: "", openai: "", busy: "", replacing: "", replacement: "", removing: "" };
export function resetJudgeUi() {
  Object.assign(judgeKeys, { jev: "", openai: "", busy: "", replacing: "", replacement: "", removing: "" });
}

const judgeWords = { ready: "ready", needs_key: "needs its key", locked: "keychain locked", unbound: "key not allowed", plan: "needs an API key" };
const judgeKind = { jev: "Jev", openai: "OpenAI Decisions" };
const usd = (x) => `$${x}`;

// judgePrice says what a judge's questions cost, as far as anyone says.
function judgePrice(j) {
  if (j.price_input === undefined || j.price_output === undefined) return "price unknown: its requests are counted, not priced";
  return `${usd(j.price_input)} per million input tokens, ${j.price_output ? `${usd(j.price_output)} per million output tokens` : "output free"}`;
}

// judgeCard is one judge a task may ask: where its questions go, the model
// they ask, what they cost and whether it can be asked now. Its model is its
// provider's judge model, which is all either API answers to, so nobody
// types it.
function judgeCard(ctx, j, redraw) {
  const { conn, desk } = ctx;
  const dot = j.state === "ready" ? "ok" : "warn";
  // A Jev server carries no model, so it is kept here rather than under
  // Models: its key is replaced and it is removed here. An OpenAI server is
  // a model provider too, kept under Models.
  const server = (desk.settings?.servers || []).find((s) => s.name === j.name);
  const jev = j.provider === "jev" && server && conn.has("settings.provider.write");
  const replacing = judgeKeys.replacing === j.name;
  const key = h("input", { class: "input", id: `judge-replace-${j.name}`, type: "password", autocomplete: "off", spellcheck: false, placeholder: "New API key", oninput: () => (judgeKeys.replacement = key.value) });
  key.value = judgeKeys.replacement;
  const replace = async () => {
    const value = judgeKeys.replacement;
    judgeKeys.replacement = "";
    judgeKeys.busy = j.name;
    redraw();
    try {
      desk.settings = await conn.call("settings.provider.write", { name: j.name, provider: "jev", url: server.url, api_key: value });
      judgeKeys.replacing = "";
      toast(`${j.name}'s key replaced.`);
      await desk.refreshModels();
    } catch (error) {
      toast(error.message);
    } finally {
      judgeKeys.busy = "";
      redraw();
    }
  };
  const remove = async () => {
    judgeKeys.removing = "";
    try {
      desk.settings = await conn.call("settings.provider.remove", { name: j.name });
      toast(`${j.name} removed. Its key stays in the keychain: Connections › Credentials removes it.`);
      await desk.refreshModels();
    } catch (error) {
      toast(error.message);
    }
    redraw();
  };
  const actions = jev
    ? h(
        "div",
        { class: "row-actions" },
        replacing
          ? [key, h("button", { type: "button", class: "btn small", disabled: !!judgeKeys.busy, onclick: replace }, judgeKeys.busy === j.name ? "Saving…" : "Save the key"), h("button", { type: "button", class: "btn small ghost", onclick: () => ((judgeKeys.replacing = ""), (judgeKeys.replacement = ""), redraw()) }, "Cancel")]
          : h("button", { type: "button", class: "btn small", onclick: () => ((judgeKeys.replacing = j.name), redraw()) }, "Replace the key"),
        judgeKeys.removing === j.name
          ? [h("span", { class: "muted" }, "Remove it? Its key stays in the keychain."), h("button", { type: "button", class: "btn small danger", onclick: remove }, "Remove"), h("button", { type: "button", class: "btn small ghost", onclick: () => ((judgeKeys.removing = ""), redraw()) }, "Cancel")]
          : h("button", { type: "button", class: "btn small danger", disabled: j.default, title: j.default ? "New tasks ask it: choose another default first" : "", onclick: () => ((judgeKeys.removing = j.name), redraw()) }, "Remove"),
      )
    : null;
  return h(
    "div",
    { class: "judge-card", id: `judge-${j.name}`, dataset: { judge: j.name } },
    h(
      "div",
      { class: "list-row" },
      h("span", { class: `dot ${dot}` }),
      h(
        "div",
        { class: "main" },
        h("strong", {}, j.name, j.default ? h("span", { class: "badge plain default-badge" }, "default") : null),
        h("span", {}, `${judgeKind[j.provider] || j.provider} · ${j.host} · ${j.model}`),
        h("span", { class: "judge-price" }, judgePrice(j)),
        j.state !== "ready" ? h("span", { class: "warn-text" }, j.detail || judgeWords[j.state] || j.state) : null,
      ),
      h("div", { class: "end" }, h("span", { class: "state-word" }, judgeWords[j.state] || j.state)),
    ),
    actions,
  );
}

// addJudge adds the provider a judge of kind is on, with its key, and makes
// it the default when there is none. It is settings.provider.write, as
// adding it under Models is: the key goes to the keychain, bound for that
// host, and config.toml gets the [servers.NAME].
async function addJudge(ctx, kind, redraw) {
  const { conn, desk } = ctx;
  const key = judgeKeys[kind];
  judgeKeys[kind] = "";
  judgeKeys.busy = kind;
  redraw();
  const taken = new Set((desk.settings?.servers || []).map((s) => s.name));
  const base = kind === "jev" ? "Jev" : "OpenAI-API";
  let name = base;
  for (let n = 2; taken.has(name); n++) name = `${base}-${n}`;
  const params =
    kind === "jev"
      ? { name, provider: "jev", url: "https://api.typesafe.ai", api_key: key }
      : { name, provider: "openai", url: "https://api.openai.com/v1", dialect: "responses", api_key: key };
  try {
    desk.settings = await conn.call("settings.provider.write", params);
    const made = (desk.settings.judges || []).find((j) => j.name === name);
    if (!values(desk)["judge.server"]?.value && made?.state === "ready") {
      desk.settings = await conn.call("settings.write", { set: { "judge.server": name } });
      toast(`${name} added, and new tasks ask it.`);
    } else toast(made?.state && made.state !== "ready" ? `${name} added, but ${made.detail || judgeWords[made.state]}.` : `${name} added. Choose it for new tasks above, or for a task beside its model.`);
    await desk.refreshModels();
  } catch (error) {
    toast(error.message);
  } finally {
    judgeKeys.busy = "";
    redraw();
  }
}

// addJudgeCard offers a judge of kind that is not set up: what it is, and a
// field for its key. A ChatGPT plan cannot ask the Decisions API, which the
// card says when that is what the person has.
function addJudgeCard(ctx, kind, redraw) {
  const { conn, desk } = ctx;
  const unlocked = !!desk.keychain?.unlocked;
  const plans = (desk.settings?.servers || []).filter((s) => s.provider === "openai" && s.authentication === "chatgpt");
  const what =
    kind === "jev"
      ? "Jev, TypeSafe's judge: it answers the judge's questions and nothing else. Paste the API key from typesafe.ai: its model and its price are known, so you type neither."
      : `OpenAI's Decisions API, which takes an OpenAI API key${plans.length ? `: ${plans.map((p) => p.name).join(", ")} signs in with a ChatGPT plan, which only reaches its chat models` : ""}. Its model and its price are known, so you type neither. The key is also listed under Models, where models can be put on it.`;
  const input = h("input", {
    class: "input",
    id: `judge-key-${kind}`,
    type: "password",
    autocomplete: "off",
    spellcheck: false,
    placeholder: kind === "jev" ? "Jev API key" : "OpenAI API key (sk-…)",
    disabled: !unlocked || !!judgeKeys.busy,
    oninput: () => {
      judgeKeys[kind] = input.value;
      button.disabled = !input.value.trim() || !unlocked || !!judgeKeys.busy;
    },
  });
  input.value = judgeKeys[kind];
  const button = h(
    "button",
    { type: "submit", class: "btn", id: `judge-add-${kind}`, disabled: !judgeKeys[kind].trim() || !unlocked || !!judgeKeys.busy },
    judgeKeys.busy === kind ? "Adding…" : kind === "jev" ? "Add Jev" : "Add the OpenAI key",
  );
  return h(
    "form",
    { class: "judge-card judge-add", id: `judge-add-card-${kind}`, onsubmit: (event) => (event.preventDefault(), addJudge(ctx, kind, redraw)) },
    h("div", { class: "main" }, h("strong", {}, kind === "jev" ? "Jev" : "OpenAI Decisions"), h("p", { class: "hint" }, what)),
    conn.has("settings.provider.write") ? h("div", { class: "row-actions" }, input, button) : null,
    unlocked ? null : h("p", { class: "hint" }, "Keys are kept in the keychain: ", h("a", { href: "#/settings/keys" }, "create or unlock it"), " first."),
  );
}

function judgeSection(ctx, redraw, root) {
  const { desk } = ctx;
  const settings = desk.settings || {};
  const judges = settings.judges || [];
  const content = picked(desk, "judge.content");
  const saved = values(desk)["judge.server"]?.value || "";
  const server = picked(desk, "judge.server");
  const chosen = judges.find((j) => j.name === server);
  const status = !server
    ? "New tasks start with no judge: their plans are drafted without its score, and you answer every command they ask."
    : server !== saved
      ? `Save to have new tasks ask ${server}.`
      : content === "none"
        ? "Off in config.toml (judge.content = \"none\"): no task asks the judge."
        : chosen?.state === "ready"
          ? `New tasks ask ${server}. Its key is stored, unlocked and allowed for ${chosen.host}; whether the service takes it shows on its first answer.`
          : `New tasks ask ${server}, which can't be asked now: ${chosen?.detail || "it is not set up"}. You answer instead.`;
  const levels = [["plan", "The task and plan"], ...(content === "plan+summaries" ? [["plan+summaries", "Plan and summaries"]] : []), ["full", "Also what calls would send"], ...(content === "none" ? [["none", "Nothing (off)"]] : [])];
  const contentHint = {
    none: "The judge is not asked anything, by any task.",
    plan: "It is shown the task and the plan, which is all the plan gate needs. The default.",
    "plan+summaries": "Reserved: for now the judge sees what it sees at “The task and plan”.",
    full: "When it answers a command it also sees what the call would send, and the model may ask it questions itself (judge-* tools). Credentials are never shown to it.",
  };
  const kinds = new Set(judges.map((j) => (j.provider === "openai" && j.state === "plan" ? "plan" : j.provider)));
  // A judge model or prices in config.toml are the default judge's: shown
  // only where they say something its provider does not.
  const model = values(desk)["judge.model"];
  const own = (name) => !!values(desk)[name]?.set;
  const providerModel = chosen?.provider_model || "";
  const wrongModel = chosen && model?.set && model.value && model.value !== providerModel && edits.get("judge.model") !== "";
  return section(
    "judge",
    "Judge",
    "The judge scores a task's plan before it acts, and answers its commands before you're asked (General › Approvals). Each task asks the judge chosen for it beside its model; new tasks start with the default here.",
    ...row("Status", h("p", { class: "hint", id: "judge-status" }, status)),
    ...row(
      "Default for new tasks",
      choice(ctx, "judge.server", [["", "No judge"], ...judges.map((j) => [j.name, `${j.name}${judgeKind[j.provider] && judgeKind[j.provider] !== j.name ? ` · ${judgeKind[j.provider]}` : ""}${j.state !== "ready" ? ` (${judgeWords[j.state] || j.state})` : ""}`])], redraw),
      h("p", { class: "hint" }, "A task's own choice, beside its model, changes only that task. Tasks already open keep theirs."),
    ),
    ...row(
      "Judges",
      judges.length ? h("div", { class: "judges" }, judges.map((j) => judgeCard(ctx, j, redraw))) : h("p", { class: "hint" }, "No judge is set up yet."),
      kinds.has("jev") ? null : addJudgeCard(ctx, "jev", redraw),
      kinds.has("openai") ? null : addJudgeCard(ctx, "openai", redraw),
      h("p", { class: "hint" }, "An OpenAI judge is on a model provider, whose key is replaced under ", h("a", { href: "#/settings/models" }, "Models"), ". A key is stored encrypted in the keychain and sent only to its judge's address."),
    ),
    ...(wrongModel
      ? row(
          "Its model",
          h(
            "div",
            { class: "callout warn", id: "judge-model-warning" },
            h("p", {}, `config.toml asks ${server} for the model “${model.value}”, which its API does not answer to, so its questions fail and are not priced. ${chosen.provider === "jev" ? "Jev" : "OpenAI's Decisions API"} answers to ${providerModel}.`),
            h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "judge-model-fix", onclick: () => (edits.set("judge.model", ""), (saveError = ""), redraw()) }, `Use ${providerModel}`)),
          ),
        )
      : []),
    ...row("What it sees", choice(ctx, "judge.content", levels, redraw), h("p", { class: "hint" }, contentHint[content] || "")),
    ...row("Allows a command at", input(ctx, "judge.approve_threshold", { width: "narrow" }), h("p", { class: "hint" }, "The judge's probability of “allow”, from above 0 to 1, at which it lets a command go ahead. Below that, or with no answer, you're asked.")),
    ...row("Waits for an answer", input(ctx, "judge.timeout", { width: "narrow" }), h("p", { class: "hint" }, "For each answer. A judge that doesn't answer in time allows nothing: you're asked instead, or in a judged run, it's a no.")),
    ...(saved
      ? row(
          "Your own prices",
          h(
            "details",
            { class: "judge-prices", open: own("judge.price_input") || own("judge.price_output") || (chosen && chosen.price_input === undefined) },
            h("summary", {}, `For ${saved} only, if its published price is wrong`),
            h(
              "div",
              { class: "row-actions" },
              input(ctx, "judge.price_input", { width: "narrow", prefix: "$", placeholder: settings.judge_published?.price_input !== undefined ? String(settings.judge_published.price_input) : "unknown" }),
              h("span", { class: "muted" }, "input,"),
              input(ctx, "judge.price_output", { width: "narrow", prefix: "$", placeholder: settings.judge_published?.price_output !== undefined ? String(settings.judge_published.price_output) : "unknown" }),
              h("span", { class: "muted" }, "output, per million tokens"),
            ),
            h("p", { class: "hint" }, "The judge reports what each request used, never what it cost, so a price estimates it. Empty is what its provider publishes. Tasks show the judge's spend, and a spend limit counts it."),
          ),
        )
      : []),
  );
}

function documentsSection(ctx) {
  if (!ctx.desk.settings?.docs) return null;
  return section("documents", "Documents", "", ...row("Google Docs uploads", h("div", { class: "value", id: "uploads" })));
}

function containersSection(ctx, redraw) {
  return section(
    "containers",
    "Containers",
    "",
    ...row(
      "Runtime",
      choice(ctx, "mcp.runtime", [["", "Automatic"], ["podman", "Podman"], ["docker", "Docker"]], redraw),
      h("p", { class: "hint" }, "What contained MCP servers run in (Connections › Tools). Automatic uses podman if it's installed, else docker."),
    ),
  );
}

function windowSection(ctx) {
  const v = values(ctx.desk);
  const fixed = v["window.high_water"]?.set || v["window.low_water"]?.set;
  return section(
    "window",
    "Context window",
    "How much of the conversation each model request carries. Past the high water, the task is summarized or its oldest parts trimmed, down to the low water. Most people never change these.",
    ...row(
      "Water marks",
      h("div", { class: "row-actions" }, h("span", { class: "muted" }, "High"), input(ctx, "window.high_water", { width: "narrow", placeholder: "grows with the model" }), h("span", { class: "muted" }, "low"), input(ctx, "window.low_water", { width: "narrow", placeholder: "grows with the model" })),
      h("p", { class: "hint" }, fixed ? "Set, they stay as set unless a model's context cannot hold them; then they shrink to fit it. Clear both to have them follow each model's context again." : "Unset, they follow the model's context less its reply cap: the high water at three quarters of what is left."),
    ),
    ...row("Largest tool result", input(ctx, "window.max_result_tokens", { width: "narrow" }), h("p", { class: "hint" }, "Tokens. A larger result is shown by its beginning and end.")),
    ...row("Fallback reply cap", input(ctx, "window.output_reserve", { width: "narrow" }), h("p", { class: "hint" }, "Tokens. The most one reply may take, thinking included, for a model with no reply cap of its own (Settings › Models) and none its provider publishes; a small model's is at most a quarter of its context. The reply comes out of the model's context, and the window has the rest.")),
  );
}

function keysSection(ctx, root) {
  const { conn, desk } = ctx;
  if (!conn.has("keychain.status") && !conn.has("devices.list")) return null;
  return section(
    "keys",
    "Keys and devices",
    "",
    conn.has("keychain.status") ? row("Keychain", keychainSettings(ctx, () => renderSettings(root, ctx))) : [],
    conn.has("devices.list") ? [h("div", { class: "label" }, "Other devices"), h("div", { class: "value", id: "devices" }, h("p", { class: "hint" }, "Loading…"))] : [],
  );
}

// keychainSettings is the keychain's state, and what can be done about it
// from here: create or unlock it, and set, change or remove its passphrase.
function keychainSettings(ctx, redraw) {
  const { conn, desk } = ctx;
  const status = desk.keychain || {};
  if (!status.unlocked) return conn.has("keychain.unlock") ? keychainForm(ctx, { onDone: redraw }) : h("p", { class: "hint" }, "Locked.");
  const credentials = `${status.credentials} saved credential${status.credentials === 1 ? "" : "s"}`;
  return [
    h("p", { class: "hint" }, status.no_passphrase ? `Open · ${credentials} · no passphrase.` : `Unlocked · ${credentials} · protected by a passphrase.`),
    status.no_passphrase ? h("div", { class: "callout" }, h("div", {}, h("strong", {}, "It has no passphrase. "), noPassphraseWarning)) : null,
    h("div", { class: "row-actions" }, h("a", { class: "btn small", href: "#/connections/credentials" }, icon("key", 15), "Manage credentials")),
    conn.has("keychain.passphrase")
      ? h(
          "details",
          // The fold stays as the person left it when the page is drawn again.
          { class: "box", open: !!status.no_passphrase || passphraseOpen, ontoggle: (event) => (passphraseOpen = event.target.open) },
          h("summary", {}, status.no_passphrase ? "Set a passphrase" : "Change or remove the passphrase"),
          passphraseForm(ctx, { onDone: () => { passphraseOpen = false; redraw(); } }),
        )
      : null,
  ];
}

function browserSection(ctx) {
  const { conn, desk, rerender } = ctx;
  const theme = storedTheme();
  const themeButton = (mode, label) => h("button", { type: "button", class: "chip", "aria-pressed": String(theme === mode), onclick: () => { applyTheme(mode); rerender(); } }, label);
  const shown = storedThinking();
  const thinkingButton = (value, label) => h("button", { type: "button", class: "chip", "aria-pressed": String(shown === value), onclick: () => { applyThinking(value); rerender(); } }, label);
  const notifier = desk.notifier;
  const notifyNote = !notifier.available
    ? "This browser can't show desktop notifications here (they need a secure page)."
    : typeof Notification !== "undefined" && Notification.permission === "denied"
      ? "Notifications are blocked for this page in the browser's settings."
      : "Only “nodwyn needs your answer”: never the task, the arguments or a diff.";
  return section(
    "browser",
    "This browser",
    "",
    ...row("Appearance", h("div", { class: "row-actions" }, themeButton("dark", "Dark"), themeButton("light", "Light"), themeButton("system", "Match the system")), h("p", { class: "hint" }, "Dark unless you choose otherwise. Kept in this browser only.")),
    ...row("The model's thinking", h("div", { class: "row-actions" }, thinkingButton(false, "Hidden"), thinkingButton(true, "Shown")), h("p", { class: "hint" }, "Hidden unless you choose otherwise: shown, it sits with the calls it led to, under its first line. The log keeps it either way. Kept in this browser only.")),
    ...row(
      "Desktop notifications",
      h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", disabled: !notifier.available || !conn.live, onclick: async () => { await notifier.toggle(); rerender(); } }, notifier.enabled ? "Turn off for this tab" : "Turn on for this tab")),
      h("p", { class: "hint" }, notifyNote),
    ),
  );
}

function connectionSection(ctx) {
  const { conn } = ctx;
  const peer = conn.hello?.peer || "";
  return section(
    "connection",
    "This connection",
    "",
    ...row(
      "nodwyn",
      h("div", { class: "kv" }, h("span", {}, "Version"), h("span", { class: "mono" }, conn.hello?.version || "—")),
      h("div", { class: "kv" }, h("span", {}, "Answers are logged as"), h("span", { class: "mono", title: peer }, peer || "—")),
      h("div", { class: "kv" }, h("span", {}, "Mode"), h("span", {}, conn.has("session.send") ? "interactive" : "read-only")),
    ),
  );
}

// showUploads is how an upload to Google Docs is handled. A task that asked
// may have saved an answer since the page loaded, so it reads it again.
async function showUploads(node, ctx) {
  const { conn, desk } = ctx;
  const draw = () => {
    const docs = desk.settings?.docs || {};
    const current = docs.google_upload || "";
    const editable = conn.has("settings.docs.write");
    const choose = async (value) => {
      try {
        desk.settings = await conn.call("settings.docs.write", { google_upload: value });
      } catch (error) {
        toast(error.message);
      }
      draw();
    };
    const chip = (value) => h("button", { type: "button", class: "chip", "aria-pressed": String(current === value), disabled: !editable, onclick: () => choose(value) }, uploadLabels[value].replace(/^./, (c) => c.toUpperCase()));
    node.replaceChildren(
      h("div", { class: "row-actions" }, chip("ask"), chip("allow"), chip("deny")),
      h(
        "p",
        { class: "hint" },
        current ? "" : "Not set yet: the first upload asks. ",
        "Allow by hand asks before each upload, and --yolo, --judge and unattended runs never upload. Always allow uploads without asking, wherever the binding table allows it. Deny never uploads.",
        docs.google ? "" : " No Google credential is configured, so no task can upload yet.",
        editable ? "" : " Only the computer nodwyn runs on can change this.",
      ),
    );
  };
  draw();
  try {
    desk.settings = await conn.call("settings.read");
    draw();
  } catch {
    /* keep what the page loaded with */
  }
}

async function showDevices(node, ctx) {
  const { conn } = ctx;
  let list;
  try {
    list = await conn.call("devices.list");
  } catch (error) {
    node.replaceChildren(h("p", { class: "error" }, error.message));
    return;
  }
  const rows = list.map((d) =>
    h(
      "div",
      { class: "list-row" },
      h("div", { class: "main" }, h("strong", {}, d.name), h("span", {}, `paired ${ago(d.paired)} · last seen ${ago(d.last_seen)}`)),
      h(
        "div",
        { class: "end" },
        h(
          "button",
          {
            type: "button",
            class: "btn small danger",
            onclick: async () => {
              try {
                await conn.call("devices.revoke", { name: d.name });
                toast(`${d.name} can no longer reach nodwyn.`);
                showDevices(node, ctx);
              } catch (error) {
                toast(error.message);
              }
            },
          },
          "Revoke",
        ),
      ),
    ),
  );
  node.replaceChildren(
    rows.length ? h("div", { class: "list" }, rows) : h("p", { class: "hint" }, "No other device is paired."),
    h("p", { class: "hint" }, "Paired browsers on other machines, such as your phone. They can answer and send messages, never change settings, keys or policy."),
  );
}
