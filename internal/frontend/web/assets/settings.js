// Settings: what nodwyn does unless a task says otherwise. Most of it is
// config.toml, read from settings.read and changed with settings.write on
// the computer nodwyn runs on: the daemon validates and writes, tasks
// started afterwards get the result, and open ones keep what they started
// with. Appearance and notifications belong to this browser.
//
// One rule for saving: a choice made here waits in the bar at the foot of the
// page and is saved with the others. What acts at once is a key or the
// passphrase (a secret is never staged), a provider or a model, each its own
// form with its own Save, and the choices of This browser, which change
// nothing on the computer.
import { h, icon, toast, toastError, ago, tablist, removal } from "./dom.js";
import { keychainForm, passphraseForm, noPassphraseWarning } from "./setup.js";
import { modelsRows, resetModelsUi, stagedDefault, discardDefault } from "./models.js";
import { judgeStateWords, judgeKinds, judgeLabel, commandsChoices } from "./modelpicker.js";
import { uploadLabels } from "./transcript.js";
import { pairing, passkeysHere, assert } from "./passkey.js";

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
const notifyChoices = [[false, "Off"], [true, "When no nodwyn window is open"]];

function writesHint(writes) {
  if (writes === "ask") return "Every write waits for your answer.";
  return "Anywhere in the task's folder, each change shown with its diff; Restore or nodwyn undo puts a file back.";
}

function commandsHint(commands, judge) {
  if (commands === "ask") return "You answer each command that no allow list covers, and the judge is asked nothing, its plan score included.";
  if (commands === "judge-auto") return judge ? "The judge answers every command, one naming a path outside the task's folder or built from what a tool returned included, and what it does not allow is refused: you're never asked about a command. Writes, sending out and keys still ask you." : "No judge is set up, so every command is refused.";
  if (commands === "allow") return "Commands, and tools that act, run without asking, one naming a path outside the task's folder or built from what a tool returned included: with writes also free, nobody is looking. What nodwyn refuses outright (credential files, unbound sending, a key on a command line) stays refused, and the judge is asked nothing, its plan score included.";
  return judge ? "The judge decides what it can, a command naming a path outside the task's folder included; you're asked about the rest, and told what it answered." : "No judge is set up, so you're asked.";
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
  "notify.desktop": { kind: "choice", label: "Desktop notices from the daemon" },
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

// The Google Docs upload answer is not a config.toml setting: it has its own
// method, and its edit waits in the same map under this name.
const docsUpload = "docs.google_upload";

// changes is what the edits would change: set, as settings.write takes it;
// docs, the upload answer for settings.docs.write (undefined when it does not
// change); and control, the default model and effort the Models tab staged,
// for settings.control.write. count is how many there are in all.
function changes(desk) {
  const set = {};
  const errors = [];
  const docs = edits.has(docsUpload) && edits.get(docsUpload) !== (desk.settings?.docs?.google_upload || "") ? edits.get(docsUpload) : undefined;
  const control = stagedDefault(desk);
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
  return { set, errors, docs, control, count: Object.keys(set).length + (docs === undefined ? 0 : 1) + (control ? 1 : 0) };
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
    disabled: !editable(ctx.conn) || hostLocked(ctx, name),
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
          disabled: !editable(ctx.conn) || hostLocked(ctx, name),
          title: hostLocked(ctx, name) ? `Changed in ${ctx.host}'s own console` : null,
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
// models, the judge (ADR 47), keys and devices, and this browser. The tab is
// in the address (#/settings/judge), so a link opens it and a reload keeps
// it; an unknown one is the first.
const tabs = [
  { id: "general", label: "General" },
  { id: "models", label: "Models" },
  { id: "judge", label: "Judge" },
  { id: "keys", label: "Keys & devices" },
  { id: "browser", label: "This browser" },
];
// hostTabs are a linked host's (ADR 54, amended 2026-10-23): its settings
// and models, which another nodwyn that is the person's may change, and the
// judge it asks by default, chosen among the host's own. What the judge
// sees and how sure it must be, with the host's keychain, credentials and
// devices, are changed in its own console; this browser is this computer's.
const hostTabs = tabs.filter((t) => ["general", "models", "judge"].includes(t.id));
// hostLocked is whether a setting is one a linked host does not let this
// window change: what a task may do without asking, the policy and the
// judge widen it, so they wait for a touch of the passkey at the host's own
// console (the host's linkWidens refuses them).
const hostLocked = (ctx, name) => !!ctx.host && name !== "judge.server" && /^(policy|judge)\./.test(name);
// moved are tabs that held one rarely used section each and were folded into
// General's Advanced fold; their addresses still open it.
const moved = new Set(["context", "tools"]);
// tabOf is the tab a route names among shown, else the first.
export const tabOf = (name, shown = tabs) => (shown.some((t) => t.id === name) ? name : shown[0].id);
let shownTab = "";
// shownHost is the host whose settings the page shows ("" for this
// computer): the edits waiting in the bar are that host's, and go when
// another host's are shown.
let shownHost = "";

// advancedOpen is whether General's Advanced fold is open, and
// passphraseOpen whether the passphrase fold is open on Keys & devices.
let advancedOpen = false;
let passphraseOpen = false;

export function renderSettings(root, ctx) {
  const { conn, desk } = ctx;
  // host names the linked host whose settings these are (ADR 54, amended
  // 2026-10-23), read and written over its connection; "" is this computer.
  const host = ctx.host || "";
  const redraw = () => renderSettings(root, ctx);
  const shownTabs = host ? hostTabs : tabs;
  const tab = tabOf(ctx.tab, shownTabs);
  // Another host's settings are another page: nothing typed for one is
  // saved to the other.
  if (host !== shownHost) {
    edits.clear();
    discardDefault();
    saveError = "";
    shownHost = host;
    root.querySelector(":scope > main.settings-page")?.remove();
  }
  // Redrawn in place (a choice, a save, the models' status): keep the
  // scroll position and the field that has focus.
  const before = root.querySelector(":scope > main.settings-page");
  // Coming to Settings from another page, nothing is half-typed (a key in
  // particular is not kept across a visit).
  if (!before) {
    resetModelsUi();
    resetJudgeUi();
    // A task that asked may have saved an answer to Google Docs uploads since
    // the desk read the settings.
    if (desk.settings?.docs && conn.has("settings.read")) {
      conn.call("settings.read").then((settings) => {
        desk.settings = settings;
        if (root.querySelector(":scope > main.settings-page")) redraw();
      }).catch(() => {});
    }
  }
  // The address of a tab that was folded into General opens its fold.
  if (ctx.tab && moved.has(ctx.tab)) {
    advancedOpen = true;
    history.replaceState(null, "", desk.at("settings/general"));
  }
  // Another tab starts at its top; a redraw of the same one stays where it was.
  const scrolled = before && shownTab === tab ? before.scrollTop : 0;
  shownTab = tab;
  const active = root.contains(document.activeElement) ? document.activeElement : null;
  const focus = active?.id ? { id: active.id, start: active.selectionStart, end: active.selectionEnd } : null;

  // An older daemon reports no values: its settings live in config.toml.
  const known = !!desk.settings?.values;
  // A daemon that has the method and failed to answer is not an older one.
  const unknown = (older) => h("p", { class: "hint" }, ctx.conn.has("settings.read") ? "The settings could not be read: retry from the message on screen, or reload." : older);
  // Only the tab shown is drawn; the edits waiting in the bar are kept across
  // tabs, so a change made in one is saved with one made in another.
  const build = {
    general: () => (known ? [approvalSection(ctx, redraw), awaySection(ctx, redraw), advancedFold(ctx, redraw)] : [unknown("This daemon's settings live in config.toml.")]),
    models: () => [modelsSection(ctx, root)],
    judge: () => (known ? [judgeSection(ctx, redraw, root)] : [unknown("This daemon's judge is set in config.toml.")]),
    keys: () => [keysSection(ctx, root, redraw)],
    browser: () => [browserSection(ctx), connectionSection(ctx)],
  };
  // A linked host that does not let this computer change its settings shows
  // none, and says why.
  const refused = host && known && !editable(conn);
  const sections = refused ? [] : build[tab]().filter(Boolean);
  const nav = refused ? null : tablist({ label: "Settings", prefix: "settings", tabs: shownTabs, current: tab, href: (id) => desk.at(`settings/${id}`) });
  const bar = h("div", { class: "save-bar", role: "region", "aria-label": "Unsaved settings", hidden: true });
  refreshBar = () => drawBar(bar, ctx, redraw);

  const page = h(
    "main",
    { class: "page settings-page" },
    h(
      "div",
      { class: "page-col" },
      h("h1", {}, host ? `${host}'s settings` : "Settings"),
      hostPicker(ctx),
      host
        ? h("p", { class: "lede", id: "save-rule" }, `The settings tasks on ${host} start with, read from ${host} and saved there. A choice made here waits in the bar at the foot of the page and is saved with the others; a model is saved in its own form. The policy, what the judge sees and how sure it must be, the providers, the keychain, credentials and devices are changed in ${host}'s own console, after a touch of your passkey.`)
        : h("p", { class: "lede", id: "save-rule" }, "A choice made here waits in the bar at the foot of the page and is saved with the others. Keys, the passphrase and starting at login act at once, a provider or a model is saved in its own form, and what is under “This browser” applies at once, to this browser alone."),
      refused ? hostRefusal(ctx) : null,
      !host && known && !editable(conn) ? h("div", { class: "callout plain" }, "These are the settings tasks start with. Only the computer nodwyn runs on can change them.") : null,
      nav,
      h("div", { class: "settings-panel", role: "tabpanel", "aria-labelledby": `settings-tab-${tab}`, id: `settings-panel-${tab}` }, ...sections),
      bar,
    ),
  );
  root.replaceChildren(page);
  page.scrollTop = scrolled;
  refreshBar();
  const devices = root.querySelector("#devices");
  if (devices) showDevices(devices, ctx);
  const login = root.querySelector("#startup");
  if (login) showStartup(login, ctx);
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

// hostPicker chooses whose settings are shown, when the window shows more
// than this computer (ADR 54): each host's are read and saved there.
function hostPicker(ctx) {
  const { hosts } = ctx;
  if (!hosts?.several) return null;
  const current = ctx.host || "";
  return h(
    "nav",
    { class: "row-actions settings-hosts", id: "settings-hosts", "aria-label": "Whose settings" },
    hosts.all().map((e) => h("a", { class: "chip", href: e.desk.at("settings"), dataset: { host: e.name }, "aria-current": e.name === current ? "page" : null }, e.label)),
  );
}

// hostRefusal says why a linked host's settings cannot be changed from here:
// this computer only answers there, or the host refuses its links the
// settings (--no-link-settings), and where they are changed instead.
function hostRefusal(ctx) {
  const { conn, host, hostAddress } = ctx;
  const open = hostAddress ? h("div", { class: "row-actions" }, h("a", { class: "btn small", href: hostAddress, target: "_blank", rel: "noopener noreferrer" }, `Open ${host}'s console`)) : null;
  if (conn.has("session.model.set"))
    return h("div", { class: "callout plain", id: "host-settings-refused" }, h("p", {}, `${host} does not let other nodwyns change its settings: its console was started with --no-link-settings, or is older than this one.`), open);
  // A link that only answers was let in by an invitation that vouched with
  // no passkey (ADR 54 §5), or linked at an address where none can be used.
  // People read the earlier one-sentence account of this as a riddle, so
  // this says what the link can do, why, and the steps that change it.
  const plain = !!hostAddress && !passkeyAddress(hostAddress);
  return h(
    "div",
    { class: "callout plain", id: "host-settings-refused" },
    h("p", {}, `From here you can start tasks on ${host} and answer their questions, but not change ${host}'s settings or choose the model its tasks use.`),
    plain
      ? h("p", {}, `This computer reaches ${host} at ${hostAddress}. A browser can use a passkey only at an https address or a localhost name, so a link made at this one never makes your choices, whatever invitation made it.`)
      : h("p", {}, `${host} lets a linked computer make those choices only when its invitation came from a browser signed in to ${host} with your passkey. This link's invitation came from ${host}'s terminal, or from ${host}'s own browser, which do not use a passkey.`),
    h("p", {}, "To make your choices here:"),
    h(
      "ol",
      {},
      h("li", {}, `Open ${host}'s console at its https address, signed in with your passkey. A browser without one gets one by pairing with an invitation from `, h("span", { class: "mono" }, "nodwyn devices invite"), ` in ${host}'s terminal.`),
      h("li", {}, "There, in Settings › Keys & devices, choose Invite a device."),
      h("li", {}, `Here, on `, h("a", { href: "#/hosts" }, "Hosts"), `, unlink ${host} and add it again${plain ? " at its https address" : ""} with that invitation.`),
    ),
    open,
  );
}

// passkeyAddress reports whether a link made at address can carry a passkey,
// as the host's bridge judges it (passkeyHost): https or a localhost name,
// and a name rather than an IP address.
function passkeyAddress(address) {
  try {
    const { protocol, hostname } = new URL(address);
    if (/^\d+(\.\d+){3}$/.test(hostname) || hostname.startsWith("[")) return false;
    return protocol === "https:" || hostname === "localhost" || hostname.endsWith(".localhost");
  } catch {
    return false;
  }
}

// loosening says which of the approvals and the upload answer waiting in the
// bar let a task do more unasked than the host does now. A linked host has
// them saved without the touch of a passkey, so this window asks once more
// before sending them; the check is this console's and guards a misclick, not
// a stolen link.
let confirming = false;
function loosening(set, docs) {
  const out = [];
  if (set["approval.writes"] === "allow") out.push("write files without asking");
  if (["allow", "judge-auto"].includes(set["approval.commands"])) out.push(set["approval.commands"] === "allow" ? "run commands without asking" : "run commands the judge allows, with nobody asked");
  if (set["approval.dont_ask"] === true) out.push("offer Don't ask in every task");
  if (docs === "allow") out.push("upload to Google Docs without asking");
  return out;
}

// drawBar shows the unsaved edits and saves them together. The config.toml
// settings go in one settings.write, so changes that only make sense together
// (a soft limit above the old hard one) are judged together; the default
// model and the Docs upload answer have methods of their own, and each is
// forgotten once saved, so a refusal of a later one leaves only what failed.
function drawBar(bar, ctx, redraw) {
  const { conn, desk } = ctx;
  const { set, errors, docs, control, count } = changes(desk);
  bar.hidden = !editable(conn) || (count === 0 && errors.length === 0 && !saveError);
  if (bar.hidden) return;
  const now = "approval.dont_ask" in set ? ", but follow Don't ask at once" : "";
  const risky = ctx.host ? loosening(set, docs) : [];
  if (!risky.length) confirming = false;
  const asked = risky.length > 1 ? `${risky.slice(0, -1).join(", ")}, and ${risky.at(-1)}` : risky[0];
  const message = confirming ? `Let tasks on ${ctx.host} ${asked}? It is saved there now, with no touch of your passkey.` : saveError || errors[0] || `${count} unsaved change${count === 1 ? "" : "s"}. Tasks you start afterwards use them; open tasks keep theirs${now}.`;
  bar.replaceChildren(
    h("p", { class: saveError || errors.length ? "error" : "", role: saveError || errors.length ? "alert" : "status" }, message),
    h(
      "div",
      { class: "row-actions" },
      h("button", { type: "button", class: "btn ghost", onclick: () => { edits.clear(); discardDefault(); saveError = ""; confirming = false; redraw(); } }, "Discard"),
      h(
        "button",
        {
          type: "button",
          class: "btn accent",
          disabled: saving || count === 0 || errors.length > 0,
          onclick: async () => {
            if (risky.length && !confirming) {
              confirming = true;
              drawBar(bar, ctx, redraw);
              return;
            }
            confirming = false;
            saving = true;
            saveError = "";
            drawBar(bar, ctx, redraw);
            try {
              if (control) {
                // The effort is sent even when empty: the default model's effort
                // is what is asked, never what the model happened to have.
                desk.settings = await conn.call("settings.control.write", { name: control.name, effort: control.effort });
                discardDefault();
                await desk.refreshModels();
              }
              if (docs !== undefined) {
                desk.settings = await conn.call("settings.docs.write", { google_upload: docs });
                edits.delete(docsUpload);
              }
              if (Object.keys(set).length) desk.settings = await conn.call("settings.write", { set });
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
        saving ? "Saving…" : confirming ? "Yes, save" : "Save",
      ),
    ),
  );
}

function approvalSection(ctx, redraw) {
  const { desk } = ctx;
  return section(
    "approval",
    "Approvals",
    "What a task does without asking you. Whatever these say, a write to a protected file (git hooks, CI, the Makefile, scripts, .nodwyn/) and content that looks like a credential always ask, and nothing outside the task's folder is written. Who answers commands answers every command, one naming a path outside the folder or built from what a tool returned included; each task can be given its own, as it starts or since. “Always”, in a question, covers that command or folder for the rest of the task.",
    ...row("Writing files", choice(ctx, "approval.writes", writeChoices, redraw), h("p", { class: "hint" }, writesHint(picked(desk, "approval.writes")))),
    ...row("Running commands", choice(ctx, "approval.commands", commandsChoices, redraw), h("p", { class: "hint" }, commandsHint(picked(desk, "approval.commands"), !!desk.settings?.judge))),
    ...row(
      "Don't ask in tasks",
      choice(ctx, "approval.dont_ask", dontAskChoices, redraw),
      String(picked(desk, "approval.dont_ask")) === "true"
        ? dontAskWarning()
        : h("p", { class: "hint" }, "A task has no switch that stops the questions."),
    ),
    ...docsRow(ctx, redraw),
  );
}

// docsRow is the answer to a task's upload to Google Docs: asked each time,
// always allowed or never. It is staged like the settings, and only drawn once
// there is a Google credential or an answer to show.
function docsRow(ctx, redraw) {
  const { conn, desk } = ctx;
  const docs = desk.settings?.docs;
  if (!docs || !(docs.google || docs.google_upload)) return [];
  const editableDocs = conn.has("settings.docs.write");
  const current = edits.has(docsUpload) ? edits.get(docsUpload) : docs.google_upload || "";
  const chip = (value) =>
    h(
      "button",
      {
        type: "button",
        class: "chip",
        id: `set-${docsUpload}-${value}`,
        "aria-pressed": String(current === value),
        // On a linked host never asking is chosen here too, after the save
        // bar's second click (loosening).
        disabled: !editableDocs,
        onclick: () => {
          edits.set(docsUpload, value);
          saveError = "";
          redraw();
        },
      },
      uploadLabels[value].replace(/^./, (c) => c.toUpperCase()),
    );
  return row(
    "Google Docs uploads",
    h("div", { class: "row-actions", role: "group", "aria-label": "Google Docs uploads" }, chip("ask"), chip("allow"), chip("deny")),
    h(
      "p",
      { class: "hint" },
      current ? "" : "Not set yet: the first upload asks. ",
      "Allow by hand asks before each upload, and --yolo, --judge and unattended runs never upload. Always allow uploads without asking, wherever the binding table allows it. Deny never uploads.",
      docs.google ? "" : " No Google credential is stored, so no task can upload.",
      editableDocs ? "" : " Only the computer nodwyn runs on can change this.",
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

// waitingSection is how long a model reply and a command may take before
// they are given up as hung. A task's limits are not here: each task has
// its own, none unless set on the task (ADR 49, amended 2026-10-27).
function waitingSection(ctx) {
  return section(
    "waiting",
    "Waiting",
    "How long nodwyn waits before it gives up a call that hangs. To stop a long task, set a limit on the task itself, in its panel.",
    ...row(
      "Up to",
      h("div", { class: "row-actions" }, h("span", { class: "muted" }, "A model reply up to"), input(ctx, "loop.request_timeout", { width: "narrow" }), h("span", { class: "muted" }, "and a command up to"), input(ctx, "loop.shell_timeout", { width: "narrow" })),
    ),
  );
}

// advancedFold holds what most people never change: how long a call may
// hang, the context window and the container runtime. It is open when the address
// that was folded into it asked for it, or when something in it waits in the
// bar, so a change is never hidden.
function advancedFold(ctx, redraw) {
  const waiting = [...edits.keys()].some((name) => name.startsWith("loop.") || name.startsWith("window.") || name === "mcp.runtime");
  return h(
    "details",
    { class: "advanced", id: "advanced", open: advancedOpen || waiting, ontoggle: (event) => (advancedOpen = event.target.open) },
    h("summary", {}, "Advanced: waiting, context window, containers"),
    waitingSection(ctx),
    windowSection(ctx),
    containersSection(ctx, redraw),
  );
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
const judgeKeys = { jev: "", openai: "", busy: "", removing: "" };
export function resetJudgeUi() {
  Object.assign(judgeKeys, { jev: "", openai: "", busy: "", removing: "" });
}

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
  // Models: it is removed here. An OpenAI server is a model provider too,
  // kept under Models. Either one's key is replaced under Credentials.
  const server = (desk.settings?.servers || []).find((s) => s.name === j.name);
  const jev = j.provider === "jev" && server && conn.has("settings.provider.write");
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
        // A linked host's credentials are changed in its own console.
        ctx.host ? null : h("a", { class: "btn small", href: "#/connections/credentials" }, "Replace the key"),
        removal({ label: "Remove", consequence: "Remove it? Its key stays in the keychain.", asking: judgeKeys.removing === j.name, ask: () => ((judgeKeys.removing = j.name), redraw()), cancel: () => ((judgeKeys.removing = ""), redraw()), run: remove, disabled: j.default, title: j.default ? "New tasks ask it: choose another default first" : "" }),
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
        h("span", {}, `${judgeKinds[j.provider] || j.provider} · ${j.host} · ${j.model}`),
        h("span", { class: "judge-price" }, judgePrice(j)),
        j.state !== "ready" ? h("span", { class: "warn-text" }, j.detail || judgeStateWords[j.state] || j.state) : null,
      ),
      h("div", { class: "end" }, h("span", { class: "state-word" }, judgeStateWords[j.state] || j.state)),
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
    // The default is a setting, so it waits in the bar like any other: adding
    // the key is what acts at once.
    if (!values(desk)["judge.server"]?.value && made?.state === "ready") {
      edits.set("judge.server", name);
      toast(`${name} added. Save to have new tasks ask it.`);
    } else toast(made?.state && made.state !== "ready" ? `${name} added, but ${made.detail || judgeStateWords[made.state]}.` : `${name} added. Choose it for new tasks above, or for a task beside its model.`);
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
    unlocked ? null : ctx.host ? h("p", { class: "hint" }, `Keys are kept in ${ctx.host}'s keychain: unlock it first, from Home with ${ctx.host} chosen to run on.`) : h("p", { class: "hint" }, "Keys are kept in the keychain: ", h("a", { href: "#/settings/keys" }, "create or unlock it"), " first."),
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
        ? "No task asks the judge, because what it sees is set to nothing. Choose what it sees under Tuning to turn it on."
        : chosen?.state === "ready"
          ? `New tasks ask ${server}. Its key is stored, unlocked and allowed for ${chosen.host}; whether the service takes it shows on its first answer.`
          : `New tasks ask ${server}, which can't be asked now: ${chosen?.detail || "it is not set up"}. You answer instead.`;
  // Off is No judge, one way to turn it off: the two levels are all the
  // console offers. A hand-written config.toml may hold another (none, or the
  // reserved plan+summaries); it then shows none pressed, and choosing one
  // replaces it.
  const levels = [["plan", "The task and plan"], ["full", "Also what calls would send"]];
  const odd = !levels.some(([value]) => value === content);
  const contentHint = {
    plan: "It is shown the task and the plan. The default.",
    full: "When it answers a command it also sees what the call would send, and the model may ask it questions itself (judge-* tools). Credentials are never shown to it.",
  };
  const kinds = new Set(judges.map((j) => (j.provider === "openai" && j.state === "plan" ? "plan" : j.provider)));
  // A judge model or prices in config.toml are the default judge's: shown
  // only where they say something its provider does not.
  const model = values(desk)["judge.model"];
  const own = (name) => !!values(desk)[name]?.set;
  const providerModel = chosen?.provider_model || "";
  const wrongModel = chosen && model?.set && model.value && model.value !== providerModel && edits.get("judge.model") !== "";
  // The tuning is folded away unless something in it is not at its default,
  // or waits in the bar, or the judge has no price to count with.
  const tuned = ["judge.content", "judge.approve_threshold", "judge.timeout", "judge.price_input", "judge.price_output"].some((name) => own(name) || edits.has(name));
  const page = section(
    "judge",
    "Judge",
    "The judge scores a task's plan before it acts, and answers its commands before you're asked (General › Approvals). Each task asks the judge chosen for it beside its model; new tasks start with the default here.",
    ...row("Status", h("p", { class: "hint", id: "judge-status" }, status)),
    ...row(
      "Default for new tasks",
      choice(ctx, "judge.server", [["", "No judge"], ...judges.map((j) => [j.name, `${judgeLabel(j)}${j.state !== "ready" ? ` (${judgeStateWords[j.state] || j.state})` : ""}`])], redraw),
      h("p", { class: "hint" }, "A task's own choice, beside its model, changes only that task. Tasks already open keep theirs."),
    ),
    ...row(
      "Judges",
      judges.length ? h("div", { class: "judges" }, judges.map((j) => judgeCard(ctx, j, redraw))) : h("p", { class: "hint" }, "No judge is set up yet."),
      kinds.has("jev") ? null : addJudgeCard(ctx, "jev", redraw),
      kinds.has("openai") ? null : addJudgeCard(ctx, "openai", redraw),
      h("p", { class: "hint" }, "An OpenAI judge is on a model provider, listed under ", h("a", { href: ctx.desk.at("settings/models") }, "Models"), ". A key is stored encrypted in the keychain and sent only to its judge's address; ", ctx.host ? `${ctx.host}'s Credentials, in its own console,` : h("a", { href: "#/connections/credentials" }, "Credentials"), " replaces it."),
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
  );
  page.append(
    h(
      "details",
      { class: "judge-tuning", id: "judge-tuning", open: tuned || odd || (chosen && chosen.price_input === undefined) },
      h("summary", {}, "Tuning: what it sees, how sure it must be, what it costs"),
      h(
        "div",
        { class: "settings-grid" },
        ...row("What it sees", choice(ctx, "judge.content", levels, redraw), h("p", { class: "hint" }, odd ? `config.toml has “${content}”, which nothing here sets: choose one of these to replace it.` : contentHint[content] || "")),
        ...row("Allows a command at", input(ctx, "judge.approve_threshold", { width: "narrow" }), h("p", { class: "hint" }, "How sure the judge must be that a command is safe, from above 0 to 1, for it to go ahead. Below that, or with no answer, you're asked.")),
        ...row("Waits for an answer", input(ctx, "judge.timeout", { width: "narrow" }), h("p", { class: "hint" }, "For each answer. A judge that doesn't answer in time allows nothing: you're asked instead, or in a judged run, it's a no.")),
        ...(saved
          ? row(
              "Your own prices",
              h(
                "div",
                { class: "row-actions" },
                input(ctx, "judge.price_input", { width: "narrow", prefix: "$", placeholder: settings.judge_published?.price_input !== undefined ? String(settings.judge_published.price_input) : "unknown" }),
                h("span", { class: "muted" }, "input,"),
                input(ctx, "judge.price_output", { width: "narrow", prefix: "$", placeholder: settings.judge_published?.price_output !== undefined ? String(settings.judge_published.price_output) : "unknown" }),
                h("span", { class: "muted" }, "output, per million tokens"),
              ),
              h("p", { class: "hint" }, `For ${saved} only, if its published price is wrong. The judge reports what each request used, never what it cost, so a price estimates it. Empty is what its provider publishes; a spend limit counts it.`),
            )
          : []),
      ),
    ),
  );
  return page;
}

// awaySection is what goes on while nobody is at nodwyn (ADR 53 §11):
// whether the daemon starts at login, so scheduled runs need no window open,
// and whether it says on this computer's desktop when a task needs an answer.
// Both are this computer's: a paired device sees neither.
function awaySection(ctx, redraw) {
  const { conn } = ctx;
  const notices = choice(ctx, "notify.desktop", notifyChoices, redraw);
  if (!conn.has("startup.read") && !notices) return null;
  return section(
    "away",
    "While you're away",
    "Scheduled runs happen in nodwyn's daemon. These keep it running and let it tell you when a task waits for you.",
    ...(conn.has("startup.read") ? row("Start at login", h("div", { id: "startup" }, h("p", { class: "hint" }, "Reading…"))) : []),
    ...(notices
      ? row(
          "Desktop notices from the daemon",
          notices,
          h("p", { class: "hint" }, "Only “nodwyn needs your answer” and “nodwyn can't run a scheduled task”, never the task. Shown when no nodwyn window here is open: an open window gives its own (This browser). On Linux it uses notify-send."),
        )
      : []),
  );
}

// showStartup fills in whether the daemon starts at login (startup.read),
// with the button that turns it on or off at once: a file in this computer's
// home, which only this computer is offered.
async function showStartup(node, ctx) {
  const { conn } = ctx;
  let state;
  const set = async (on) => {
    try {
      state = await conn.call("startup.write", { on });
      toast(on ? "Turned on: nodwyn's daemon starts at your next login." : "Turned off: nodwyn's daemon no longer starts at login.");
    } catch (error) {
      toastError(error.message);
    }
    draw();
  };
  const draw = () => {
    if (!state.offered) return node.replaceChildren(h("p", { class: "hint" }, "This daemon offers no login item: start nodwyn from the terminal with nodwyn startup on."));
    const on = state.on && !state.other;
    node.replaceChildren(
      h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "startup-toggle", disabled: !conn.live || (!on && !!state.why), onclick: () => set(!on) }, on ? "Turn off" : "Turn on")),
      h("p", { class: "hint" }, on
        ? `On: ${state.path} starts nodwyn's daemon when you log in, so scheduled runs go on with no window open.`
        : state.why || (state.on
          ? `${state.path} starts another nodwyn at login; Turn on makes it start this one (${state.program}).`
          : "Off: scheduled runs happen only while nodwyn is open. Turned on, the daemon starts at your next login.")),
    );
  };
  try {
    state = await conn.call("startup.read");
  } catch (error) {
    return node.replaceChildren(h("p", { class: "error" }, error.message));
  }
  draw();
}

function containersSection(ctx, redraw) {
  return section(
    "containers",
    "Containers",
    "",
    ...row(
      "Runtime",
      choice(ctx, "mcp.runtime", [["", "Automatic"], ["podman", "Podman"], ["docker", "Docker"]], redraw),
      h("p", { class: "hint" }, "What contained MCP servers run in (Connections). Automatic uses podman if it's installed, else docker."),
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

function keysSection(ctx, root, redraw) {
  const { conn } = ctx;
  const known = !!ctx.desk.settings?.values;
  const devices = conn.has("devices.list") || conn.has("devices.invite");
  if (!conn.has("keychain.status") && !devices && !known) return null;
  return section(
    "keys",
    "Keys & devices",
    "",
    conn.has("keychain.status") ? row("Keychain", keychainSettings(ctx, () => renderSettings(root, ctx))) : [],
    // A credential is sent only where a binding allows it; this is the one
    // exception to the HTTPS the guard otherwise requires, so it sits with the
    // keys rather than among the approvals.
    known
      ? row(
          "Credentials over remote HTTP",
          choice(ctx, "policy.allow_insecure_http", [[false, "Require HTTPS"], [true, "Allow HTTP"]], redraw),
          h("p", { class: "hint" }, "Off by default. Allow HTTP only for servers you trust on an unencrypted connection: anyone on the route can read or change credentials and content. Localhost is always available, and certificate checks, bindings and redirect refusals still apply."),
        )
      : [],
    devices ? [h("div", { class: "label" }, "Other devices"), h("div", { class: "value", id: "devices" }, h("p", { class: "hint" }, "Loading…"))] : [],
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
  // Safari on an iPhone or iPad has no notifications for a page, only for an
  // app added to the Home Screen (ADR 52); navigator.standalone is false
  // there and nowhere else.
  const notifyNote = !notifier.available
    ? navigator.standalone === false
      ? "On an iPhone or iPad, add nodwyn to the Home Screen first (Share › Add to Home Screen): Safari shows notifications only to apps there."
      : "This browser can't show notifications here (they need a secure page)."
    : typeof Notification !== "undefined" && Notification.permission === "denied"
      ? "Notifications are blocked for this page in the browser's settings."
      : "Only “nodwyn needs your answer”: never the task, the arguments or a diff. Kept in this browser; the app has them on until you turn them off.";
  return section(
    "browser",
    "This browser",
    "",
    ...row("Appearance", h("div", { class: "row-actions" }, themeButton("dark", "Dark"), themeButton("light", "Light"), themeButton("system", "Match the system")), h("p", { class: "hint" }, "Dark unless you choose otherwise. Kept in this browser only.")),
    ...row("The model's thinking", h("div", { class: "row-actions" }, thinkingButton(false, "Hidden"), thinkingButton(true, "Shown")), h("p", { class: "hint" }, "Hidden unless you choose otherwise: shown, it sits with the calls it led to, under its first line. The log keeps it either way. Kept in this browser only.")),
    ...row(
      "Notifications",
      h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", disabled: !notifier.available || !conn.live, onclick: async () => { await notifier.toggle(); rerender(); } }, notifier.enabled ? "Turn off" : "Turn on")),
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

// revoking is the device, and removing the passkey, whose button waits for
// its confirm: a device revoked is locked out at once and pairs again only
// with an invitation, and a passkey removed signs nothing in again.
let revoking = "";
let removing = "";
// invitation is the last one made here, shown until it expires.
let invitation = null;

async function showDevices(node, ctx) {
  const { conn } = ctx;
  const peer = conn.hello?.peer || "";
  const me = /via web:([a-z0-9._-]+)/.exec(peer)?.[1] || "";
  const person = / passkey:/.test(peer);
  let list = [];
  let keys = [];
  let way = { passkey: false, passkeys: false };
  try {
    [list, keys, way] = await Promise.all([conn.has("devices.list") ? conn.call("devices.list") : [], conn.has("passkeys.list") ? conn.call("passkeys.list") : [], pairing()]);
  } catch (error) {
    node.replaceChildren(h("p", { class: "error" }, error.message));
    return;
  }
  const redraw = () => showDevices(node, ctx);
  const act = async (work) => {
    try {
      await work();
    } catch (error) {
      toast(error.message);
    }
    redraw();
  };
  const revoke = (name) =>
    act(async () => {
      revoking = "";
      await conn.call("devices.revoke", { name });
      toast(`${name} can no longer reach nodwyn.`);
    });
  const removeKey = (name) =>
    act(async () => {
      removing = "";
      await conn.call("passkeys.remove", { name });
      toast(`The passkey ${name} signs nothing in from now on.`);
    });
  // An invitation from the person's device may make a passkey on the device
  // that uses it, after a touch. One from this computer or from a device that
  // only answers lets in a device that only answers: the first passkey comes
  // from nodwyn devices invite, so that no leak of this computer's sign-in
  // makes the person (ADR 30, amended 2026-10-19).
  const invite = () =>
    act(async () => {
      const made = await conn.call("devices.invite", { passkey: person });
      invitation = { ...made, at: Date.now() };
    });
  // A device paired before it had a passkey, here where passkeys work,
  // becomes the person's with a touch of one made on another device.
  const signIn = () =>
    act(async () => {
      const answer = await assert();
      if (answer.promoted) conn.connect();
    });
  const loopback = ["127.0.0.1", "localhost", "[::1]"].includes(location.hostname);
  const live = invitation && new Date(invitation.expires).getTime() > Date.now();
  // replaceChildren writes a null as the text "null", where h() leaves it out.
  const draw = () =>
    node.replaceChildren(
      ...[
        me && !person && way.passkey && way.passkeys && passkeysHere()
          ? h("div", { class: "callout plain" }, h("div", {}, "This device only answers. Sign it in with your passkey, made on another device, and it can make this computer's choices too."), h("button", { type: "button", class: "btn small", id: "devices-sign-in", onclick: signIn }, "Sign in with a passkey"))
          : null,
        conn.has("devices.invite")
          ? h(
              "div",
              { class: "stack-md" },
              h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "devices-invite", onclick: invite }, "Invite a device")),
              live
                ? h(
                    "div",
                    { class: "box", id: "invitation" },
                    h("p", {}, "Invitation: ", h("strong", { class: "mono" }, invitation.code)),
                    loopback ? null : h("p", { class: "hint" }, "Or open this link on the device: ", h("span", { class: "mono" }, `${location.origin}/#invite=${invitation.code}`)),
                    h("p", { class: "hint" }, `Good once, until ${new Date(invitation.expires).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}. Keep it off chats and screenshots.`),
                    me ? null : h("p", { class: "hint" }, "The device it lets in answers. For one that makes a passkey, run ", h("span", { class: "mono" }, "nodwyn devices invite"), " in a terminal on this computer."),
                  )
                : null,
            )
          : null,
        conn.has("devices.list")
          ? list.length
            ? h(
                "div",
                { class: "list" },
                list.map((d) =>
                  h(
                    "div",
                    { class: "list-row" },
                    h("div", { class: "main" }, h("strong", {}, d.name, d.name === me ? " (this device)" : ""), h("span", {}, [`paired ${ago(d.paired)}`, `last seen ${ago(d.last_seen)}`, d.host ? "another nodwyn, linked to this one" : "", d.passkey ? `${d.host ? "vouched for" : "signed in"} with the passkey ${d.passkey}` : "answers only", d.by ? `let in by ${d.by}` : ""].filter(Boolean).join(" · "))),
                    h("div", { class: "end" }, removal({ label: "Revoke", consequence: `Revoke ${d.name}? It is locked out at once, and pairs again only with an invitation.`, asking: revoking === d.name, ask: () => ((revoking = d.name), draw()), cancel: () => ((revoking = ""), draw()), run: () => revoke(d.name) })),
                  ),
                ),
              )
            : h("p", { class: "hint" }, "No other device is paired.")
          : null,
        keys.length
          ? h(
              "div",
              { class: "list", id: "passkeys" },
              keys.map((k) =>
                h(
                  "div",
                  { class: "list-row" },
                  h("div", { class: "main" }, h("strong", {}, `Passkey ${k.name}`), h("span", {}, `added ${ago(k.added)} · last used ${ago(k.last_used)}`)),
                  h("div", { class: "end" }, removal({ label: "Remove", consequence: `Remove the passkey ${k.name}? It signs nothing in from then on, and the devices signed in with it only answer.`, asking: removing === k.name, ask: () => ((removing = k.name), draw()), cancel: () => ((removing = ""), draw()), run: () => removeKey(k.name) })),
                ),
              ),
            )
          : null,
        h(
          "p",
          { class: "hint" },
          "Browsers on other machines, such as your phone, reach nodwyn when it runs with ",
          h("span", { class: "mono" }, "nodwyn ui --listen ADDRESS:PORT"),
          " or ",
          h("span", { class: "mono" }, "--name"),
          ", and pair with an invitation: from ",
          h("span", { class: "mono" }, "nodwyn devices invite"),
          " on its computer, or Invite a device here. A paired device answers and sends messages. Over https an invitation from ",
          h("span", { class: "mono" }, "nodwyn devices invite"),
          " or from a device signed in with your passkey also makes a passkey on the device, and a device signed in with one makes this computer's choices too, and changes settings and keys after a touch of it. One made in this computer's browser makes none: your first passkey comes from the terminal.",
        ),
      ].filter(Boolean),
    );
  draw();
}
