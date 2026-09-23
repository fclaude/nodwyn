import { renderPrompt } from "./prompts.js";
import { renderInspector } from "./inspectors.js";
import { policyChoices, renderPolicyEditor } from "./policy.js";
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
  baseNotice =
    "Read-only view. Send messages and answer prompts in the terminal client.";
let queued = [],
  seen = new Map(),
  streamText = null,
  streamThinking = null,
  toolCard = null;
const pending = new Map();
const drafts = new Map();
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
function modeNotice() {
  return methods.has("session.send")
    ? "Review each requested operation in its approval panel."
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
  const connection = new WebSocket(`ws://${location.host}/socket`);
  socket = connection;
  $("connection").textContent = "Connecting…";
  $("reconnect").hidden = true;
  connection.onopen = async () => {
    if (socket !== connection) return;
    $("pairing").hidden = true;
    $("workspace").hidden = false;
    $("connection").textContent = "Connected · local daemon";
    try {
      const hello = await call("hello", { protocol: 1 });
      methods = new Set(hello.methods);
      $("mode").textContent = modeNotice();
      controls();
      await refresh();
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
    } else if (frame.params?.id === selected?.id) {
      if (loading) queued.push(frame.params);
      else render(frame.params);
    }
  };
  connection.onclose = () => {
    if (socket !== connection) return;
    selection++;
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
    $("connection").textContent = "Disconnected";
    $("reconnect").hidden = false;
    if ($("workspace").hidden) $("pairing").hidden = false;
    else
      notice(
        "Connection lost. Reconnect to reload the durable transcript. No operations were retried.",
      );
  };
}
function notice(value) {
  $("notice").textContent = value;
}
async function refresh() {
  const sessions = (await call("session.list")) || [];
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
    button.append(
      text("span", item.first_message || "Untitled session", "session-title"),
    );
    button.append(
      text(
        "span",
        item.busy ? "Running" : item.open ? "Open · idle" : "Saved",
        "state",
      ),
    );
    button.append(text("span", item.repo, "session-meta"));
    button.append(
      text("span", new Date(item.last_event).toLocaleString(), "session-meta"),
    );
    button.addEventListener("click", () =>
      selectSession(item).catch((error) => notice(error.message)),
    );
    $("sessions").append(button);
  }
  if (selected) {
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
function entry(label, content, kind = "") {
  const node = text("article", "", `entry ${kind}`);
  node.append(text("div", label, "entry-label"));
  if (content !== undefined) node.append(text("div", content, "message"));
  $("transcript").querySelector(":scope > p.hint")?.remove();
  $("transcript").append(node);
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
      toolCard = null;
      if (!loading) busy(true);
      entry("You", p.text, "user");
      break;
    case "stream.text":
      if (!streamText)
        streamText = entry("nibb · responding", "").querySelector(".message");
      streamText.textContent += field(p, "text");
      break;
    case "stream.thinking":
      if (!streamThinking) {
        const node = entry("nibb");
        detail(node, "Thinking", "");
        streamThinking = node.querySelector("pre");
      }
      streamThinking.textContent += field(p, "text");
      break;
    case "llm.resp": {
      streamText?.closest("article").remove();
      streamThinking?.closest("article").remove();
      streamText = null;
      streamThinking = null;
      if (p.content || p.thinking) {
        const node = entry("nibb", p.content || "");
        if (p.thinking) detail(node, "Thinking", p.thinking);
      }
      if (p.usage)
        entry(
          "Usage",
          `${p.usage.prompt_tokens || 0} input tokens · ${p.usage.output_tokens || 0} output tokens · ${p.usage.total_ms || 0} ms`,
          "hint",
        );
      toolCard = null;
      break;
    }
    case "tool.call": {
      const node = entry(`Tool · ${p.name}`, undefined, "tool");
      detail(node, "Arguments", p.args);
      toolCard = { id: p.id, name: p.name, node };
      break;
    }
    case "guard.decision": {
      const node =
        (p.origin === "model" ? toolCard?.node : null) ||
        entry("Guard", undefined, "audit");
      const line = text("div", `${p.prim || "Operation"} · ${p.reason || ""}`);
      line.append(
        text("span", p.decision || "recorded", `badge ${p.decision || ""}`),
      );
      node.append(line);
      break;
    }
    case "tool.result": {
      const node =
        toolCard?.id === p.id && toolCard.name === p.name
          ? toolCard.node
          : entry(`Result · ${p.name}`, undefined, "tool");
      detail(node, p.ok ? "Result" : "Error", p.ok ? p.value : p.error);
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
      entry("Error", field(p, "err"), "audit");
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
      const node = entry(kind, undefined, "audit");
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
connect();
