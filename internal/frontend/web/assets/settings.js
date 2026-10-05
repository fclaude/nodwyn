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
  "approval.writes": { kind: "choice", label: "Writing files" },
  "approval.commands": { kind: "choice", label: "Running commands" },
  "budget.tool_calls_soft": { kind: "count", label: "Tool calls, ask at", follows: true },
  "budget.tool_calls_hard": { kind: "count", label: "Tool calls, stop at" },
  "budget.tokens_soft": { kind: "count", label: "Tokens sent, ask at", follows: true },
  "budget.tokens_hard": { kind: "count", label: "Tokens sent, stop at" },
  "budget.actions_soft": { kind: "count", label: "Actions, ask at", follows: true },
  "budget.actions_hard": { kind: "count", label: "Actions, stop at" },
  "budget.wall_soft": { kind: "duration", label: "Time, ask at", follows: true },
  "budget.wall_hard": { kind: "duration", label: "Time, stop at" },
  "budget.cost_soft": { kind: "decimal", label: "Spend, ask at", follows: true },
  "budget.cost_hard": { kind: "decimal", label: "Spend, stop at" },
  "loop.request_timeout": { kind: "duration", label: "A model reply" },
  "loop.shell_timeout": { kind: "duration", label: "A command" },
  "jev.content": { kind: "choice", label: "What the judge sees" },
  "jev.approve_threshold": { kind: "decimal", label: "Allows a command at" },
  "jev.price_input": { kind: "decimal", label: "Judge price of input" },
  "jev.price_output": { kind: "decimal", label: "Judge price of output" },
  "jev.timeout": { kind: "duration", label: "Waits for an answer" },
  "jev.model": { kind: "text", label: "Judge model" },
  "mcp.runtime": { kind: "choice", label: "Container runtime" },
  "window.high_water": { kind: "count", label: "High water" },
  "window.low_water": { kind: "count", label: "Low water" },
  "window.max_result_tokens": { kind: "count", label: "Largest tool result" },
  "window.output_reserve": { kind: "count", label: "Default reply cap" },
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
export function renderSettings(root, ctx) {
  const { conn, desk } = ctx;
  const redraw = () => renderSettings(root, ctx);
  // Redrawn in place (a choice, a save, the models' status): keep the
  // scroll position and the field that has focus.
  const before = root.querySelector(":scope > main.settings-page");
  // Coming to Settings from another page, nothing is half-typed (a key in
  // particular is not kept across a visit).
  if (!before) resetModelsUi();
  const scrolled = before ? before.scrollTop : 0;
  const active = root.contains(document.activeElement) ? document.activeElement : null;
  const focus = active?.id ? { id: active.id, start: active.selectionStart, end: active.selectionEnd } : null;

  // An older daemon reports no values: its settings live in config.toml.
  const known = !!desk.settings?.values;
  const sections = [
    ...(known ? [approvalSection(ctx, redraw), limitsSection(ctx)] : []),
    modelsSection(ctx, root),
    ...(known ? [judgeSection(ctx, redraw)] : []),
    documentsSection(ctx),
    ...(known ? [containersSection(ctx, redraw), windowSection(ctx)] : []),
    keysSection(ctx, root),
    browserSection(ctx),
    connectionSection(ctx),
  ].filter(Boolean);
  const nav = h(
    "nav",
    { class: "settings-nav row-actions", "aria-label": "Settings sections" },
    sections.map((s) => h("button", { type: "button", class: "chip", onclick: () => s.scrollIntoView({ behavior: "smooth", block: "start" }) }, s.querySelector("h2").textContent)),
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
      ...sections,
      bar,
    ),
  );
  root.replaceChildren(page);
  page.scrollTop = scrolled;
  refreshBar();
  if (conn.has("devices.list")) showDevices(root.querySelector("#devices"), ctx);
  if (desk.settings?.docs) showUploads(root.querySelector("#uploads"), ctx);
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
  const message = saveError || errors[0] || `${count} unsaved change${count === 1 ? "" : "s"}. Tasks you start afterwards use them; open tasks keep theirs.`;
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
              toast("Saved. Tasks you start from now on use these settings.");
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
    ...row("Writing files", choice(ctx, "approval.writes", writeChoices, redraw), h("p", { class: "hint" }, writesHint(picked(desk, "approval.writes")))),
    ...row("Running commands", choice(ctx, "approval.commands", commandChoices, redraw), h("p", { class: "hint" }, commandsHint(picked(desk, "approval.commands"), !!desk.settings?.judge))),
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
        ...pair("Tokens sent", "budget.tokens_soft", "budget.tokens_hard"),
        ...pair("Actions", "budget.actions_soft", "budget.actions_hard"),
        ...pair("Time", "budget.wall_soft", "budget.wall_hard"),
      ),
      h("p", { class: "hint" }, "Tokens sent counts every request's prompt in full, so it grows faster than the conversation. Actions are writes, commands and other steps that change something."),
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
    "The providers nodwyn talks to, the models you choose between with each one's own settings, what new tasks start on, and the judge's model. Saved in config.toml with its other lines kept; a key goes to the keychain. Tasks already open keep the model and effort they have, and change them under their own message box.",
    ...modelsRows(ctx, () => renderSettings(root, ctx)),
    ...(values(ctx.desk)["jev.model"] ? judgeModelRows(ctx) : []),
  );
}

// judgeModelRows are the judge's model settings, beside the other models'
// (ADR 41, amended 2026-10-05): which model it is, its prices, with what its
// provider publishes as their placeholders, and how long it may take. What it
// sees and when it allows a command are under Judge.
function judgeModelRows(ctx) {
  const published = ctx.desk.settings?.jev_published || {};
  const price = (x) => (x === undefined || x === null ? "unknown" : String(x));
  return [
    ...row("Judge model", input(ctx, "jev.model"), h("p", { class: "hint" }, `The model at ${ctx.desk.settings?.jev_host || "the judge's address"} that scores plans and answers commands (Judge, below).`)),
    ...row(
      "Judge prices",
      h(
        "div",
        { class: "row-actions" },
        input(ctx, "jev.price_input", { width: "narrow", prefix: "$", placeholder: price(published.price_input) }),
        h("span", { class: "muted" }, "input,"),
        input(ctx, "jev.price_output", { width: "narrow", prefix: "$", placeholder: price(published.price_output) }),
        h("span", { class: "muted" }, "output"),
      ),
      h(
        "p",
        { class: "hint" },
        published.price_input !== undefined
          ? "US dollars per million tokens. The judge reports what each request used, never what it cost, so these estimate it: empty is what its provider publishes. Tasks show the judge's spend, and a spend limit counts it."
          : "US dollars per million tokens. The judge reports what each request used, never what it cost, so these estimate it: tasks show the judge's spend, and a spend limit counts it.",
      ),
    ),
    ...row("Judge waits", input(ctx, "jev.timeout", { width: "narrow" }), h("p", { class: "hint" }, "For each answer. A judge that doesn't answer in time allows nothing: you're asked instead, or in a judged run, it's a no.")),
  ];
}

function judgeSection(ctx, redraw) {
  const { desk } = ctx;
  const content = picked(desk, "jev.content");
  const status =
    content === "none"
      ? "Off: nothing goes to the judge, and plans are drafted without its score."
      : desk.settings?.judge
        ? "Ready: a jev-key is stored and the keychain is unlocked."
        : "Not available: no jev-key is stored, or the keychain is locked.";
  // The row shows only when the Judge panel has a field for it: the
  // judge's host is known and no key is stored. With a stored key the row
  // stays out whatever the judge's content level, so it never offers to
  // add a key that is already there. The label is neutral: the row is
  // about the key, not about enabling the judge.
  const keyAction = desk.settings?.jev_host && !desk.settings?.jev_stored ? h("a", { class: "btn small", href: "#/connections/credentials" }, "Judge key") : null;
  const levels = [
    ["none", "Off"],
    ["plan", "The task and plan"],
    ...(content === "plan+summaries" ? [["plan+summaries", "Plan and summaries"]] : []),
    ["full", "Also what calls would send"],
  ];
  const contentHint = {
    none: "The judge is not asked anything.",
    plan: "It is shown the task and the plan, which is all the plan gate needs. The default.",
    "plan+summaries": "Reserved: for now the judge sees what it sees at “The task and plan”.",
    full: "When it answers a command it also sees what the call would send, and the model may ask it questions itself (judge-* tools). Credentials are never shown to it.",
  };
  return section(
    "judge",
    "Judge",
    "The judge (Jev) scores plans before a task acts, and answers its commands before you're asked (Approvals, above).",
    ...row("Status", h("p", { class: "hint" }, status)),
    ...row("Key", keyAction || h("p", { class: "hint" }, desk.settings?.jev_stored ? "Stored in the keychain." : "Set the judge's address (jev.url) first.")),
    ...row("What it sees", choice(ctx, "jev.content", levels, redraw), h("p", { class: "hint" }, contentHint[content] || "")),
    ...row("Allows a command at", input(ctx, "jev.approve_threshold", { width: "narrow" }), h("p", { class: "hint" }, "The judge's probability of “allow”, from above 0 to 1, at which it lets a command go ahead. Below that, or with no answer, you're asked.")),
    ...row("Model and prices", h("p", { class: "hint" }, h("a", { href: "#section-models" }, "Under Models"), ", with the other models' settings.")),
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
    ...row("Default reply cap", input(ctx, "window.output_reserve", { width: "narrow" }), h("p", { class: "hint" }, "Tokens. The most one reply may take, thinking included, for a model whose own reply cap is empty (Settings › Models); a small model's is at most a quarter of its context. The reply comes out of the model's context, and the window has the rest.")),
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
    conn.has("keychain.passphrase") ? h("details", { class: "box", open: !!status.no_passphrase }, h("summary", {}, status.no_passphrase ? "Set a passphrase" : "Change or remove the passphrase"), passphraseForm(ctx, { onDone: redraw })) : null,
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
