"use strict";
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
  seen = new Set(),
  streamText = null,
  streamThinking = null,
  toolCard = null;
const pending = new Map();
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
    loading = false;
    queued = [];
    for (const wait of pending.values()) {
      clearTimeout(wait.timer);
      wait.reject(new Error("Connection lost; no operation was retried."));
    }
    pending.clear();
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
      text("p", "No sessions yet. Start one in the terminal.", "hint"),
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
  selected = item;
  loading = true;
  awaitingReply = "";
  queued = [];
  seen = new Set();
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
      $("activity").textContent = snapshot.busy ? "Running" : "Idle";
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
      : "Read-only view. Send messages and answer prompts in the terminal client.";
    notice(baseNotice);
    for (const event of prompts) render(event);
    loading = false;
    for (const event of queued) render(event);
    queued = [];
    if (!$("transcript").children.length)
      $("transcript").append(text("p", "No conversation yet.", "hint"));
  } catch (error) {
    if (version === selection) {
      loading = false;
      queued = [];
      notice(error.message);
    }
  }
}
function entry(label, content, kind = "") {
  const node = text("article", "", `entry ${kind}`);
  node.append(text("div", label, "entry-label"));
  if (content !== undefined) node.append(text("div", content, "message"));
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
  if (event.seq) seen.add(event.seq);
  const p = event.payload || {},
    kind = event.kind;
  const viewport = $("transcript");
  const follow =
    viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 90;
  switch (kind) {
    case "user.msg":
      toolCard = null;
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
      if (awaitingReply && p.prompt_id === awaitingReply) {
        awaitingReply = "";
        notice(baseNotice);
        $("activity").textContent = "Running";
      }
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
      awaitingReply = p.ID ?? p.id;
      $("activity").textContent = "Needs your answer";
      notice(
        "This session needs your answer. Use the terminal client; this browser view cannot approve or submit secrets.",
      );
      break;
    case "status":
      if (field(p, "state") === "idle") {
        $("activity").textContent = "Idle";
        if (awaitingReply) {
          awaitingReply = "";
          notice(baseNotice);
        }
      } else if (!awaitingReply)
        $("activity").textContent = field(p, "detail") || field(p, "state");
      break;
    case "error":
      entry("Error", field(p, "err"), "audit");
      break;
    case "session.closed":
      $("activity").textContent = "Saved";
      notice("Session closed. Its transcript remains available.");
      break;
    case "gate.plan":
    case "turn.checkpoint":
    case "egress.check":
    case "approval.change":
    case "binding.change":
    case "summary":
    case "summary.drop": {
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
      if (kind === "eval.req") $("activity").textContent = "Evaluating";
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
connect();
