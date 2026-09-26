import { renderPrompt } from "./prompts.js";
import { renderInspector } from "./inspectors.js";
import { policyChoices, renderPolicyEditor } from "./policy.js";
import { Attention } from "./attention.js";
const $ = (id) => document.getElementById(id);
const text = (tag, value, className = "") => {
  const node = document.createElement(tag);
  node.textContent = value;
  node.className = className;
  return node;
};
const field = (obj, name) =>
  obj?.[name] ?? obj?.[name[0].toUpperCase() + name.slice(1)];
let socket,
  nextID = 0,
  selected = null,
  selection = 0,
  loading = false;
let awaitingReply = "",
  baseNotice = "";
let queued = [],
  seen = new Map(),
  streamText = null,
  streamThinking = null,
  toolCard = null;
const pending = new Map();
const drafts = new Map();
const sessionItems = new Map();
let listVersion = 0;
const attention = new Attention(
  $("notifications"),
  $("attention-summary"),
  (state) => {
    const item = sessionItems.get(state.id);
    if (!item) {
      if (state.open) refresh(false).catch((error) => notice(error.message));
      return;
    }
    Object.assign(item, {
      open: state.open,
      busy: state.busy,
      needs_answer: state.needs_answer,
    });
    for (const button of $("sessions").children) {
      if (button.dataset.session === state.id) {
        button.querySelector(".state").textContent = sessionLabel(item);
        button.classList.toggle("needs-answer", !!item.needs_answer);
      }
    }
    if (selected?.id === state.id) {
      selected.open = state.open;
      selected.busy = state.busy;
      controls();
    }
  },
  async (id) => {
    if (!sessionItems.has(id)) await refresh(false);
    const item = sessionItems.get(id);
    if (item) selectSession(item).catch((error) => notice(error.message));
  },
);
// A sidebar row has room for the project and how long ago, not a full
// path and a locale timestamp; the row's tooltip keeps both.
function repoName(path) {
  if (!path) return "—";
  const parts = path.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || path;
}
function ago(when) {
  const then = new Date(when).getTime();
  if (!then) return "";
  const s = Math.max(0, (Date.now() - then) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d`;
  return new Date(when).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  });
}
function sessionLabel(item) {
  return item.needs_answer
    ? "needs you"
    : item.busy
      ? "running"
      : item.open
        ? "idle"
        : "saved";
}

let view = "conversation",
  inspection = null,
  inspectionVersion = 0,
  historyTruncated = false;
let policy = null;
function showPolicyEditor() {
  if (!policy || view !== "policy") return;
  renderPolicyEditor(
    $("inspector-content"),
    policy,
    (name) => policyOperation(name, false),
    () => policyOperation(policy.name, true),
    () => {
      policy = null;
      inspect("policy");
    },
  );
  controls();
}
function policyResult(result) {
  if (!policy || policy.version !== selection) return;
  if (!policy.operation) {
    policy.early.set(result.ID, result);
    return;
  }
  if (result.ID !== policy.operation) return;
  policy.error = result.Err;
  policy.saved = result.Saved;
  if (!result.Err && !result.Saved) {
    policy.doc = result;
    policy.draft = result.Content;
  }
  showPolicyEditor();
}
async function policyOperation(name, write) {
  if (!selected?.open || selected.busy || policy?.starting || activePrompt)
    return;
  const method = write ? "session.policy.write" : "session.policy.read";
  if (!methods.has(method)) return;
  const previous = policy;
  const request = {
    name,
    version: selection,
    operation: null,
    early: new Map(),
    starting: true,
    doc: write ? previous.doc : null,
    draft: write ? previous.draft : "",
    error: "",
    saved: false,
  };
  policy = request;
  showPolicyEditor();
  const params = { id: selected.id, name };
  if (write) {
    params.revision = request.doc.Revision;
    params.content = request.draft;
  }
  try {
    const reply = await call(method, params);
    if (policy !== request || request.version !== selection) return;
    request.operation = reply.operation_id;
    const result = request.early.get(request.operation);
    request.early.clear();
    if (result) policyResult(result);
  } catch (error) {
    if (policy === request && request.version === selection)
      request.error = error.message;
  } finally {
    request.starting = false;
    if (policy === request) showPolicyEditor();
  }
}
let friction = null;
function frictionResult(result) {
  if (!friction || friction.version !== selection) return;
  if (!friction.operation) {
    friction.early.set(result.ID, result);
    return;
  }
  if (result.ID !== friction.operation) return;
  friction.result = result;
  if (view === "friction") showFrictionResult();
}
function showFrictionResult() {
  if (!friction?.result) return;
  const result = friction.result;
  $("inspector-content").replaceChildren(
    text(
      "p",
      "docs/friction.md · guarded read · refresh to read again",
      "hint",
    ),
    text("pre", result.Err || result.Text),
  );
}
async function readFriction() {
  if (!selected?.open || selected.busy || !methods.has("session.friction")) {
    $("inspector-content").replaceChildren(
      text(
        "p",
        "A friction read requires an idle, open session and interactive browser mode on a daemon that supports this operation.",
      ),
    );
    return;
  }
  if (friction?.starting) return;
  const request = {
    starting: true,
    version: selection,
    operation: null,
    early: new Map(),
    result: null,
  };
  friction = request;
  controls();
  try {
    const reply = await call("session.friction", { id: selected.id });
    if (friction !== request || request.version !== selection) return;
    request.operation = reply.operation_id;
    const result = request.early.get(request.operation);
    request.early.clear();
    if (result) frictionResult(result);
  } catch (error) {
    if (
      friction === request &&
      request.version === selection &&
      view === "friction"
    )
      $("inspector-content").replaceChildren(text("p", error.message));
  } finally {
    request.starting = false;
    controls();
  }
}
function setView(kind) {
  view = kind;
  $("transcript").hidden = kind !== "conversation";
  $("inspector").hidden = kind === "conversation";
  for (const button of $("view-tabs").children)
    button.setAttribute("aria-pressed", String(button.dataset.view === kind));
  controls();
}
function redrawInspector() {
  if (!inspection) return;
  renderInspector(
    $("inspector-content"),
    view,
    inspection,
    $("log-kind").value,
    Number($("log-count").value),
  );
  if (
    view === "policy" &&
    selected?.open &&
    methods.has("session.policy.read") &&
    methods.has("session.policy.write")
  )
    policyChoices($("inspector-content"), (name) =>
      policyOperation(name, false),
    );
}
async function inspect(kind, reread = false) {
  const token = ++inspectionVersion,
    version = selection;
  if (kind === "conversation") {
    setView(kind);
    return;
  }
  if (!selected || loading || activePrompt) return;
  inspection = null;
  setView(kind);
  $("inspector-title").textContent = kind[0].toUpperCase() + kind.slice(1);
  $("log-filter").hidden = kind !== "log";
  $("inspector-content").replaceChildren(
    text(
      "p",
      kind === "friction"
        ? "Reading through the session guard…"
        : "Loading snapshot…",
    ),
  );
  if (kind === "policy" && policy?.version === selection) {
    showPolicyEditor();
    return;
  }
  if (kind === "friction") {
    if (!reread && friction?.version === selection && friction.result)
      showFrictionResult();
    else await readFriction();
    return;
  }
  const snapshot = {
    events: [...seen.values()].sort((a, b) => a.seq - b.seq),
    info: null,
    infoError:
      "No open session. Resume in a client to inspect current configuration.",
    truncated: historyTruncated,
  };
  if (selected.open && ["budget", "policy", "tools"].includes(kind)) {
    try {
      snapshot.info = await call("session.info", { id: selected.id });
    } catch (error) {
      snapshot.infoError = error.message;
    }
  }
  if (token !== inspectionVersion || version !== selection || activePrompt)
    return;
  inspection = snapshot;
  redrawInspector();
  controls();
}
let activePrompt = null;
let resolvedPrompts = new Set();
function clearPrompt() {
  if (activePrompt) resolvedPrompts.add(activePrompt.payload.ID);
  activePrompt = null;
  awaitingReply = "";
  renderPrompt($("prompt-panel"), null);
}
function showPrompt(event) {
  const id = event.payload.ID;
  if (!id || resolvedPrompts.has(id) || activePrompt?.payload.ID === id) return;
  activePrompt = event;
  inspectionVersion++;
  setView("conversation");
  awaitingReply = id;
  renderPrompt($("prompt-panel"), event, methods, (method, params) => {
    const sessionID = selected.id,
      version = selection;
    action(async () => {
      try {
        await call(method, { id: sessionID, prompt_id: id, ...params });
        resolvedPrompts.add(id);
        if (version === selection && awaitingReply === id) {
          clearPrompt();
          notice(baseNotice);
        }
      } catch (error) {
        // Never keep secret input or reuse an uncertain prompt after an error.
        if (version === selection && socket?.readyState === WebSocket.OPEN)
          await refresh();
        throw error;
      }
    });
  });
}
let methods = new Set(),
  actionPending = false;
// The sidebar states the mode once. The banner carries news, not a
// standing description, so in read-write mode its base text is empty.
function modeLine() {
  return methods.has("session.send")
    ? "Read-write · every operation is approved here"
    : "Read-only · send and answer in the terminal client";
}
function modeNotice() {
  return methods.has("session.send")
    ? ""
    : "Read-only view. Send messages and answer prompts in the terminal client.";
}
function controls() {
  const connected = socket?.readyState === WebSocket.OPEN;
  const ready = connected && !loading && !actionPending;
  $("new-session").hidden = !methods.has("session.open");
  $("create").disabled = !ready;
  for (const control of $("prompt-panel").querySelectorAll("button,input"))
    control.disabled = !ready;
  $("composer").hidden =
    !methods.has("session.send") || !!activePrompt || view !== "conversation";
  for (const button of $("view-tabs").children)
    button.disabled = !ready || !selected || !!activePrompt;
  $("inspect-refresh").hidden = view === "policy" && !!policy;
  for (const control of $("inspector-content").querySelectorAll(
    "button,textarea",
  ))
    control.disabled = !ready || selected?.busy || !!policy?.starting;
  $("inspect-refresh").disabled =
    !ready ||
    !selected ||
    (view === "friction" && (selected.busy || friction?.starting));
  for (const control of $("log-filter").elements)
    control.disabled = !ready || !inspection;
  $("message").disabled = !ready || !selected?.open;
  $("send").disabled =
    !ready || !selected?.open || selected.busy || !$("message").value.trim();
  $("resume").hidden =
    !selected || selected.open || !methods.has("session.resume");
  $("resume").disabled = !ready;
  $("abort").hidden = !selected?.open || !methods.has("session.abort");
  $("abort").disabled = !ready || !selected?.busy;
  $("close-session").hidden = !selected?.open || !methods.has("session.close");
  $("close-session").disabled = !ready || selected?.busy;
}
function busy(value) {
  if (selected) selected.busy = value;
  controls();
}
function call(method, params = {}) {
  return new Promise((resolve, reject) => {
    if (socket?.readyState !== WebSocket.OPEN) {
      reject(new Error("Disconnected. Reconnect to reload history."));
      return;
    }
    const id = ++nextID;
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error("Request timed out. Nothing was retried."));
    }, 35000);
    pending.set(id, { resolve, reject, timer });
    socket.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
  });
}
function connect() {
  if (socket) socket.close();
  for (const wait of pending.values()) {
    clearTimeout(wait.timer);
    wait.reject(new Error("Reconnecting; no operation was retried."));
  }
  pending.clear();
  attention.reset();
  const connection = new WebSocket(`ws://${location.host}/socket`);
  socket = connection;
  connectionState("", "Connecting…");
  $("reconnect").hidden = true;
  connection.onopen = async () => {
    if (socket !== connection) return;
    $("pairing").hidden = true;
    $("workspace").hidden = false;
    connectionState("live", "local daemon");
    try {
      const hello = await call("hello", { protocol: 1 });
      if (socket !== connection) return;
      methods = new Set(hello.methods);
      $("mode").textContent = modeLine();
      controls();
      await refresh();
      if (socket !== connection) return;
      if (methods.has("session.watch")) {
        attention.reset(true);
        const states = await call("session.watch");
        if (socket !== connection) return;
        for (const state of states) attention.update(state, false);
      }
    } catch (error) {
      notice(error.message);
    }
  };
  connection.onmessage = (event) => {
    if (socket !== connection) return;
    const frame = JSON.parse(event.data);
    if (frame.id) {
      const wait = pending.get(frame.id);
      if (!wait) return;
      clearTimeout(wait.timer);
      pending.delete(frame.id);
      if (frame.error) wait.reject(new Error(frame.error.message));
      else wait.resolve(frame.result);
    } else if (frame.params?.kind === "session.state") {
      attention.update(frame.params.payload);
    } else if (frame.params?.id === selected?.id) {
      if (loading) queued.push(frame.params);
      else render(frame.params);
    }
  };
  connection.onclose = () => {
    if (socket !== connection) return;
    selection++;
    attention.reset();
    friction = null;
    policy = null;
    clearPrompt();
    loading = false;
    queued = [];
    for (const wait of pending.values()) {
      clearTimeout(wait.timer);
      wait.reject(new Error("Connection lost; no operation was retried."));
    }
    pending.clear();
    controls();
    connectionState("down", "Disconnected");
    $("reconnect").hidden = false;
    if ($("workspace").hidden) $("pairing").hidden = false;
    else
      notice(
        "Connection lost. Reconnect to reload the durable transcript. No operations were retried.",
      );
  };
}
// The theme follows the system until the reader overrides it; the choice
// is this browser's, so it lives in localStorage and never in a session.
function applyTheme(mode) {
  const root = document.documentElement;
  if (mode) root.dataset.theme = mode;
  else delete root.dataset.theme;
  const dark =
    mode === "dark" ||
    (!mode && matchMedia("(prefers-color-scheme: dark)").matches);
  $("theme").textContent = dark ? "Light" : "Dark";
}
function initTheme() {
  let stored = null;
  try {
    stored = localStorage.getItem("nibb-theme");
  } catch {
    stored = null;
  }
  applyTheme(stored);
  $("theme").addEventListener("click", () => {
    const dark = !(
      document.documentElement.dataset.theme === "light" ||
      (!document.documentElement.dataset.theme &&
        !matchMedia("(prefers-color-scheme: dark)").matches)
    );
    const next = dark ? "light" : "dark";
    applyTheme(next);
    try {
      localStorage.setItem("nibb-theme", next);
    } catch {
      /* a browser that refuses storage still gets the theme for this tab */
    }
  });
}
function connectionState(state, label) {
  const node = $("connection");
  node.dataset.state = state;
  node.textContent = label;
}
function notice(value) {
  $("notice").textContent = value;
}
async function refresh(refreshSelected = true) {
  const version = ++listVersion,
    connection = socket;
  const sessions = (await call("session.list")) || [];
  if (version !== listVersion || socket !== connection) return;
  sessionItems.clear();
  for (const item of sessions) {
    const state = attention.states.get(item.id);
    if (state)
      Object.assign(item, {
        open: state.open,
        busy: state.busy,
        needs_answer: state.needs_answer,
      });
    sessionItems.set(item.id, item);
  }
  $("sessions").replaceChildren();
  if (!sessions.length)
    $("sessions").append(
      text(
        "p",
        methods.has("session.open")
          ? "No sessions yet. Create one above."
          : "No sessions yet. Start one in the terminal.",
        "hint",
      ),
    );
  for (const item of sessions) {
    const button = text(
      "button",
      "",
      "session" + (selected?.id === item.id ? " selected" : ""),
    );
    button.dataset.session = item.id;
    button.classList.toggle("needs-answer", !!item.needs_answer);
    button.append(
      text("span", item.first_message || "Untitled session", "session-title"),
    );
    button.append(text("span", sessionLabel(item), "state"));
    button.append(
      text("span", `${repoName(item.repo)} · ${ago(item.last_event)}`, "session-meta"),
    );
    button.title = `${item.repo}\n${new Date(item.last_event).toLocaleString()}`;
    button.addEventListener("click", () =>
      selectSession(item).catch((error) => notice(error.message)),
    );
    $("sessions").append(button);
  }
  if (refreshSelected && selected) {
    const current = sessions.find((item) => item.id === selected.id);
    if (current) await selectSession(current);
  }
}
async function selectSession(item) {
  const version = ++selection,
    previous = selected;
  const previousView =
    previous?.id === item.id && view !== "friction" ? view : "conversation";
  friction = null;
  policy = null;
  inspectionVersion++;
  inspection = null;
  historyTruncated = false;
  setView("conversation");
  if (previous) drafts.set(previous.id, $("message").value);
  selected = item;
  $("message").value = drafts.get(item.id) || "";
  loading = true;
  controls();
  clearPrompt();
  resolvedPrompts = new Set();
  queued = [];
  seen = new Map();
  streamText = null;
  streamThinking = null;
  toolCard = null;
  $("transcript").replaceChildren();
  $("session-title").textContent = item.first_message || "Untitled session";
  $("repo").textContent = item.repo;
  $("activity").textContent = item.busy
    ? "Running"
    : item.open
      ? "Idle"
      : "Saved";
  notice("Loading transcript…");
  for (const button of $("sessions").children)
    button.classList.toggle("selected", button.dataset.session === item.id);
  try {
    if (previous?.open)
      await call("session.unsubscribe", { id: previous.id }).catch(() => {});
    if (version !== selection) return;
    let page,
      snapshotBusy = item.busy,
      prompts = [];
    if (item.open) {
      const snapshot = await call("session.subscribe", {
        id: item.id,
        after: 0,
        limit: 200,
      });
      if (version !== selection) return;
      page = snapshot.page;
      prompts = snapshot.pending;
      snapshotBusy = snapshot.busy;
    } else page = await call("log.read", { id: item.id, after: 0, limit: 200 });
    const through = page.through;
    let truncated = false;
    while (true) {
      if (version !== selection) return;
      for (const event of page.events) render(event);
      truncated ||= page.truncated;
      if (!page.more) break;
      page = await call("log.read", {
        id: item.id,
        after: page.next,
        through,
        limit: 200,
      });
    }
    if (version !== selection) return;
    baseNotice = truncated
      ? "Incomplete log tail: showing its valid prefix. Use the terminal client to recover it."
      : modeNotice();
    historyTruncated = truncated;
    selected.busy = snapshotBusy;
    $("activity").textContent = item.open
      ? snapshotBusy
        ? "Running"
        : "Idle"
      : "Saved";
    notice(baseNotice);
    for (const event of prompts) render(event);
    loading = false;
    for (const event of queued) render(event);
    queued = [];
    controls();
    if (!$("transcript").children.length)
      $("transcript").append(text("p", "No conversation yet.", "hint"));
    if (!activePrompt && previousView !== "conversation")
      await inspect(previousView);
  } catch (error) {
    if (version === selection) {
      // A failed history load must not enable sending against uncertain state.
      loading = true;
      queued = [];
      controls();
      notice(error.message);
    }
  }
}
// A transcript is a sequence of turns, not a sequence of events: the
// user's message opens one, everything the harness does in reply belongs
// to it, and going idle closes it. Telemetry lives in the turn's footer,
// where it can be read but does not compete with the answer.
let agentBlock = null;
function transcript() {
  const view = $("transcript");
  view.querySelector(":scope > .empty")?.remove();
  view.querySelector(":scope > p.hint")?.remove();
  return view;
}
function endTurn() {
  agentBlock = null;
  streamText = null;
  streamThinking = null;
  toolCard = null;
}
function userTurn(content) {
  endTurn();
  const node = text("article", "", "turn user");
  node.append(text("div", "you", "turn-label"), text("div", content, "message"));
  transcript().append(node);
  return node;
}
// harnessTurn shows what the loop's strategy told the model (add-message):
// the model reads it as a user message, so it sits where the user's would.
function harnessTurn(content) {
  endTurn();
  const node = text("article", "", "turn harness");
  node.append(text("div", "nibb → model", "turn-label"), text("div", content, "message"));
  transcript().append(node);
  return node;
}
// agentTurn returns the open reply block, opening one when the harness
// speaks or acts without a preceding message of its own.
function agentTurn() {
  if (!agentBlock) {
    agentBlock = text("article", "", "turn agent");
    agentBlock.append(text("div", "nibb", "turn-label"));
    transcript().append(agentBlock);
  }
  return agentBlock;
}
function entry(label, content, kind = "") {
  const node = text("div", "", kind === "hint" ? "turn-foot" : `audit ${kind}`);
  if (label) node.append(text("span", label + " ", "eyebrow"));
  if (content !== undefined) node.append(text("span", content));
  agentTurn().append(node);
  return node;
}
function detail(parent, label, value) {
  const node = document.createElement("details");
  node.append(
    text("summary", label),
    text(
      "pre",
      typeof value === "string" ? value : JSON.stringify(value, null, 2),
    ),
  );
  parent.append(node);
  return node;
}
// Event kinds are the log's vocabulary; a reader wants the harness's.
const auditLabels = {
  "gate.plan": "plan gate",
  "turn.checkpoint": "budget checkpoint",
  "egress.check": "egress check",
  "approval.change": "approval changed",
  "binding.change": "binding changed",
  summary: "summary written",
  "summary.drop": "summary revoked",
  "cred.register": "credential registered",
};
function sizeOf(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / 1048576).toFixed(1)} MB`;
}
// argSummary renders a tool call's arguments as the one line a reader
// scans. What identifies a call is its target, so those keys lead; the
// rest follows on the same line, and the full arguments are one click
// away in the row's body.
const targetKeys = ["path", "file", "dir", "pkg", "target", "url", "cmd", "name", "glob", "pattern"];
function argSummary(args) {
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
  const line = entries
    .map(([key, v]) => [key, typeof v === "string" ? v : JSON.stringify(v)])
    // Once a call names its target, a long prose argument (the new text of
    // an edit, a file's contents) says nothing a reader scanning the line
    // needs; the row's body still holds every argument in full.
    .filter(([key, v]) => !named || targetKeys.includes(key) || v.length <= 48)
    .map(([, v]) => v)
    .filter((v) => v !== "" && v !== "[]" && v !== "{}" && v !== "null")
    .join(" ");
  return oneLine(line);
}
function oneLine(value, limit = 140) {
  const flat = value.replace(/\s+/g, " ").trim();
  return flat.length > limit ? flat.slice(0, limit - 1) + "…" : flat;
}
// toolRow is one collapsed line: name, arguments, and — once the result
// arrives — its verdict, size and duration.
function toolRow(name, args) {
  const row = document.createElement("details");
  row.className = "tool-row";
  const summary = document.createElement("summary");
  summary.append(
    text("span", name, "tool-name"),
    text("span", argSummary(args), "tool-args"),
    text("span", "…", "tool-stat"),
  );
  row.append(summary, text("div", "", "tool-body"));
  if (args !== undefined && args !== null && args !== "")
    detail(row.querySelector(".tool-body"), "Arguments", args);
  agentTurn().append(row);
  return row;
}
function render(event) {
  if (event.seq && seen.has(event.seq)) return;
  if (event.seq) seen.set(event.seq, event);
  const p = event.payload || {},
    kind = event.kind;
  if (
    p.prompt_id &&
    ["guard.decision", "turn.checkpoint", "cred.register"].includes(kind)
  ) {
    resolvedPrompts.add(p.prompt_id);
    if (awaitingReply === p.prompt_id) {
      clearPrompt();
      notice(baseNotice);
    }
  }
  const viewport = $("transcript");
  const follow =
    viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 90;
  switch (kind) {
    case "policy.result":
      policyResult(p);
      break;
    case "inspection.result":
      frictionResult(p);
      break;
    case "user.msg":
      if (!loading) busy(true);
      userTurn(p.text);
      break;
    case "harness.msg":
      harnessTurn(field(p, "text"));
      break;
    case "stream.text":
      if (!streamText) {
        streamText = text("div", "", "message");
        agentTurn().append(streamText);
      }
      streamText.textContent += field(p, "text");
      break;
    case "stream.thinking":
      if (!streamThinking)
        streamThinking = detail(agentTurn(), "Thinking", "").querySelector(
          "pre",
        );
      streamThinking.textContent += field(p, "text");
      break;
    case "llm.resp": {
      // The streamed fragments were provisional; the recorded response is
      // what the log holds, so it replaces them.
      streamText?.remove();
      streamThinking?.closest("details").remove();
      streamText = null;
      streamThinking = null;
      const turn = agentTurn();
      if (p.thinking) detail(turn, "Thinking", p.thinking);
      if (p.content) turn.append(text("div", p.content, "message"));
      if (p.usage)
        turn.append(
          text(
            "div",
            `${p.usage.prompt_tokens || 0}→${p.usage.output_tokens || 0} tok · ${((p.usage.total_ms || 0) / 1000).toFixed(1)}s`,
            "turn-foot",
          ),
        );
      toolCard = null;
      break;
    }
    case "tool.call": {
      const row = toolRow(p.name, p.args);
      toolCard = { id: p.id, name: p.name, node: row };
      break;
    }
    case "guard.decision": {
      const verdict = p.decision || "recorded";
      const row = p.origin === "model" ? toolCard?.node : null;
      if (row) {
        // The verdict belongs on the call it judged, not beside it.
        const summary = row.querySelector("summary");
        summary.insertBefore(
          text("span", verdict, `badge ${verdict}`),
          summary.querySelector(".tool-stat"),
        );
        if (p.reason)
          row
            .querySelector(".tool-body")
            .prepend(text("div", p.reason, "turn-foot"));
        break;
      }
      const node = entry("guard", `${p.prim || "operation"} · ${p.reason || ""}`, verdict);
      node.append(text("span", " "), text("span", verdict, `badge ${verdict}`));
      break;
    }
    case "tool.result": {
      const row =
        toolCard?.id === p.id && toolCard.name === p.name
          ? toolCard.node
          : toolRow(p.name, undefined);
      const body = p.ok ? p.value : p.error;
      const stat = row.querySelector(".tool-stat");
      stat.className = `tool-stat ${p.ok ? "ok" : "failed"}`;
      stat.textContent = [
        p.ok ? sizeOf((body || "").length) : "error",
        p.ms === undefined ? null : `${p.ms} ms`,
      ]
        .filter(Boolean)
        .join(" · ");
      detail(row.querySelector(".tool-body"), p.ok ? "Result" : "Error", body);
      if (!p.ok) row.open = true;
      toolCard = null;
      break;
    }
    case "prompt.confirm":
    case "prompt.secret":
    case "prompt.checkpoint":
      if (resolvedPrompts.has(p.ID)) break;
      busy(true);
      showPrompt(event);
      if (awaitingReply) {
        $("activity").textContent = "Needs your answer";
        notice("Review the pending prompt below before choosing an answer.");
      }
      controls();
      break;
    case "status":
      if (field(p, "state") === "idle") {
        busy(false);
        endTurn();
        $("activity").textContent = "Idle";
        if (awaitingReply) {
          clearPrompt();
          notice(baseNotice);
        }
      } else {
        busy(true);
        if (!awaitingReply)
          $("activity").textContent = field(p, "detail") || field(p, "state");
      }
      break;
    case "error":
      entry("error", field(p, "err"), "deny");
      break;
    case "session.closed":
      selected.open = false;
      clearPrompt();
      busy(false);
      $("activity").textContent = "Saved";
      notice("Session closed. Its transcript remains available.");
      break;
    case "gate.plan":
    case "turn.checkpoint":
    case "egress.check":
    case "approval.change":
    case "binding.change":
    case "summary":
    case "summary.drop":
    case "cred.register": {
      const node = entry(auditLabels[kind] || kind, undefined);
      detail(node, "Recorded details", p);
      break;
    }
    case "turn.end":
    case "session.resume":
      toolCard = null;
      break;
    case "eval.req":
    case "eval.result": {
      if (kind === "eval.req" && !loading) {
        busy(true);
        $("activity").textContent = "Evaluating";
      }
      toolCard = null;
      const node = entry(kind, undefined, "audit");
      detail(node, "User evaluation", p);
      break;
    }
  }
  if (follow) viewport.scrollTop = viewport.scrollHeight;
}
$("pair-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = $("pair-code");
  const code = input.value;
  input.value = "";
  $("pair-error").textContent = "";
  try {
    const response = await fetch("/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code }),
    });
    if (!response.ok)
      throw new Error(
        "Pairing failed. Copy the code from the current nibb ui terminal.",
      );
    connect();
  } catch (error) {
    $("pair-error").textContent = error.message;
  }
});
$("refresh").addEventListener("click", () =>
  refresh().catch((error) => notice(error.message)),
);
$("reconnect").addEventListener("click", connect);
for (const button of $("view-tabs").children)
  button.addEventListener("click", () => inspect(button.dataset.view));
$("inspect-refresh").addEventListener("click", () => {
  if (view === "friction") inspect("friction", true);
  else refresh().catch((error) => notice(error.message));
});
$("log-filter").addEventListener("submit", (event) => {
  event.preventDefault();
  redrawInspector();
});
$("message").addEventListener("input", () => {
  if (selected) drafts.set(selected.id, $("message").value);
  controls();
});
async function action(work) {
  if (actionPending) return;
  actionPending = true;
  controls();
  try {
    await work();
  } catch (error) {
    notice(
      `${error.message} Nothing was retried; inspect the transcript before sending again.`,
    );
  } finally {
    actionPending = false;
    controls();
  }
}
$("new-form").addEventListener("submit", (event) => {
  event.preventDefault();
  action(async () => {
    const version = selection;
    const info = await call("session.open", {
      repo: $("new-repo").value.trim(),
    });
    if (version === selection) await selectSession({ ...info, open: true });
    await refresh();
  });
});
$("resume").addEventListener("click", () =>
  action(async () => {
    const item = selected,
      version = selection;
    const info = await call("session.resume", { id: item.id });
    if (version === selection)
      await selectSession({ ...item, ...info, open: true });
    await refresh();
  }),
);
$("close-session").addEventListener("click", () =>
  action(async () => {
    await call("session.close", { id: selected.id });
    await refresh();
  }),
);
$("abort").addEventListener("click", () =>
  action(async () => {
    await call("session.abort", { id: selected.id });
  }),
);
$("composer").addEventListener("submit", (event) => {
  event.preventDefault();
  if ($("send").disabled) return;
  const item = selected,
    version = selection,
    draft = $("message").value;
  action(async () => {
    busy(true);
    $("activity").textContent = "Sending…";
    try {
      await call("session.send", { id: item.id, text: draft });
      if (drafts.get(item.id) === draft) drafts.delete(item.id);
      if (selected?.id === item.id && $("message").value === draft)
        $("message").value = "";
    } catch (error) {
      // State may already have changed at the daemon; a failed reply is not
      // evidence of failed delivery. Reload before enabling another send.
      if (version === selection && socket?.readyState === WebSocket.OPEN)
        await refresh();
      throw error;
    }
  });
});
// A composer that only sends on a button click is a composer you fight.
$("message").addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || !(event.metaKey || event.ctrlKey)) return;
  event.preventDefault();
  $("composer").requestSubmit();
});
initTheme();
connect();
