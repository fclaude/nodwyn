// The pages that are not tasks: Connections, Workspaces and Settings. They
// show what the daemon reports; where it reports nothing yet, they say so.
import { h, icon, repoName, shortPath, ago, toast } from "./dom.js";
import { chooseFolder, kindLabel } from "./home.js";
import { modelForm, keychainForm } from "./setup.js";

// ---------- theme (a per-browser convenience) ----------
export function storedTheme() {
  try {
    return localStorage.getItem("nibb-theme") || "";
  } catch {
    return "";
  }
}
export function applyTheme(mode) {
  const root = document.documentElement;
  if (mode === "light" || mode === "dark") root.dataset.theme = mode;
  else delete root.dataset.theme;
  try {
    if (mode) localStorage.setItem("nibb-theme", mode);
    else localStorage.removeItem("nibb-theme");
  } catch {
    /* a browser that refuses storage still gets the theme for this tab */
  }
}

function modelRows(desk) {
  if (!desk.models) return [h("p", { class: "hint" }, "Checking the configured models…")];
  const words = { ready: "answers", missing: "server answers; model not installed", unreachable: "does not answer", unconfigured: "not set up" };
  return [
    h(
      "div",
      { class: "list" },
      desk.models.map((m) =>
        h(
          "div",
          { class: "list-row" },
          h("span", { class: `dot ${m.state === "ready" ? "ok" : m.state === "unreachable" ? "bad" : "warn"}` }),
          h("div", { class: "main" }, h("strong", {}, m.name === "control" ? "Main model" : m.name), h("span", {}, m.model ? `${m.model} · ${m.provider}${m.host ? ` at ${m.host}` : ""}` : "No model configured")),
          h("div", { class: "end" }, m.detail || words[m.state] || m.state),
        ),
      ),
    ),
    h("p", { class: "hint" }, "A reachability check: nibb asks each model server for its list of models and sends no credential."),
  ];
}

// ---------- settings ----------
export function renderSettings(root, ctx) {
  const { conn, desk, rerender } = ctx;
  const theme = storedTheme();
  const themeButton = (mode, label) => h("button", { type: "button", class: "chip", "aria-pressed": String(theme === mode), onclick: () => { applyTheme(mode); rerender(); } }, label);
  const notifier = desk.notifier;
  const notifyNote = !notifier.available
    ? "This browser can't show desktop notifications here (they need a secure page)."
    : typeof Notification !== "undefined" && Notification.permission === "denied"
      ? "Notifications are blocked for this page in the browser's settings."
      : "Only “nibb needs your answer”: never the task, the arguments or a diff.";
  const peer = conn.hello?.peer || "";
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Settings"),
        h(
          "div",
          { class: "settings-grid" },
          h("div", { class: "label" }, "Appearance"),
          h("div", { class: "value" }, h("div", { class: "row-actions" }, themeButton("", "Match the system"), themeButton("light", "Light"), themeButton("dark", "Dark")), h("p", { class: "hint" }, "Kept in this browser only.")),
          h("div", { class: "label" }, "Desktop notifications"),
          h(
            "div",
            { class: "value" },
            h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", disabled: !notifier.available || !conn.live, onclick: async () => { await notifier.toggle(); rerender(); } }, notifier.enabled ? "Turn off for this tab" : "Turn on for this tab")),
            h("p", { class: "hint" }, notifyNote),
          ),
          h("div", { class: "label" }, "Models"),
          h(
            "div",
            { class: "value" },
            modelRows(desk),
            h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small", onclick: () => desk.refreshModels() }, "Check again")),
            conn.has("settings.control.write")
              ? h("details", { class: "box" }, h("summary", {}, "Change the main model"), modelForm(ctx, { onSaved: () => renderSettings(root, ctx) }), h("p", { class: "hint" }, `Saved in ${desk.settings?.config_file || "config.toml"}; other settings there are kept. Tasks already open keep the model they started with.`))
              : null,
          ),
          conn.has("keychain.status") ? h("div", { class: "label" }, "Keychain") : null,
          conn.has("keychain.status") ? h("div", { class: "value" }, conn.has("keychain.unlock") ? keychainForm(ctx, { onDone: () => renderSettings(root, ctx) }) : h("p", { class: "hint" }, desk.keychain?.unlocked ? "Unlocked." : "Locked.")) : null,
          conn.has("devices.list") ? h("div", { class: "label" }, "Other devices") : null,
          conn.has("devices.list") ? h("div", { class: "value", id: "devices" }, h("p", { class: "hint" }, "Loading…")) : null,
          h("div", { class: "label" }, "This connection"),
          h(
            "div",
            { class: "value" },
            h("div", { class: "kv" }, h("span", {}, "nibb"), h("span", { class: "mono" }, conn.hello?.version || "—")),
            h("div", { class: "kv" }, h("span", {}, "Answers are logged as"), h("span", { class: "mono", title: peer }, peer || "—")),
            h("div", { class: "kv" }, h("span", {}, "Mode"), h("span", {}, conn.has("session.send") ? "interactive" : "read-only")),
          ),
        ),
      ),
    ),
  );
  if (conn.has("devices.list")) showDevices(root.querySelector("#devices"), ctx);
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
                toast(`${d.name} can no longer reach nibb.`);
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

// ---------- workspaces ----------
let editing = ""; // the folder whose editor is open
export function renderWorkspaces(root, ctx) {
  const { desk, navigate, conn } = ctx;
  const counts = new Map();
  for (const item of desk.all()) {
    const entry = counts.get(item.repo) || { tasks: 0, last: item.last_event };
    entry.tasks++;
    counts.set(item.repo, entry);
  }
  const canEdit = conn.has("workspace.save");
  const row = (folder, ws) => {
    const count = counts.get(folder);
    const mounts = ws?.mounts || [];
    const node = h(
      "div",
      { class: "list-row ws-row" },
      icon("folder", 18),
      h(
        "div",
        { class: "main" },
        h("strong", {}, ws ? ws.name : desk.folderName(folder), ws ? h("span", { class: "badge plain", style: "margin-left:8px" }, kindLabel(ws.kind)) : null),
        h("span", { class: "mono", title: folder }, shortPath(folder, desk.home)),
        mounts.length ? h("span", {}, mounts.map((m) => `${shortPath(m.path, desk.home)} (${m.access === "write" ? "read and write" : "read only"})`).join(" · ")) : null,
        ws?.error ? h("span", { class: "error" }, ws.error) : null,
        ws ? h("span", {}, ws.git ? "A git repository: git is the undo." : "Not a git repository: nibb keeps the previous version of a file before it changes it.") : null,
      ),
      h(
        "div",
        { class: "end" },
        count ? `${count.tasks} task${count.tasks === 1 ? "" : "s"} · ${ago(count.last)}` : "no tasks yet",
        h("button", { type: "button", class: "btn small", onclick: () => { chooseFolder(folder); navigate("#/"); } }, "New task here"),
        canEdit ? h("button", { type: "button", class: "btn small ghost", onclick: () => { editing = editing === folder ? "" : folder; renderWorkspaces(root, ctx); } }, ws ? "Edit" : "Name it") : null,
      ),
    );
    return editing === folder ? [node, editor(folder, ws)] : [node];
  };
  const editor = (folder, ws) => {
    let kind = ws?.kind || "code";
    const mounts = (ws?.mounts || []).map((m) => ({ ...m }));
    const name = h("input", { class: "input", id: "ws-name", value: ws?.name || desk.folderName(folder) });
    const error = h("p", { class: "error", role: "alert" });
    const list = h("div", { style: "display:flex;flex-direction:column;gap:8px" });
    const drawMounts = () =>
      list.replaceChildren(
        ...mounts.map((m, i) =>
          h(
            "div",
            { class: "row-actions" },
            h("span", { class: "mono", style: "flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis" }, m.path),
            h("select", { class: "input", "aria-label": "Access", onchange: (e) => (m.access = e.target.value) }, h("option", { value: "read", selected: m.access !== "write" }, "read only"), h("option", { value: "write", selected: m.access === "write" }, "read and write")),
            h("button", { type: "button", class: "btn small ghost", onclick: () => { mounts.splice(i, 1); drawMounts(); } }, "Remove"),
          ),
        ),
      );
    drawMounts();
    const mountPath = h("input", { class: "input", placeholder: "/absolute/path or ~/folder", "aria-label": "Folder to mount", spellcheck: false });
    const kindChoice = (id, label) => h("button", { type: "button", class: "chip", "aria-pressed": String(kind === id), onclick: (e) => { kind = id; for (const b of e.target.parentElement.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b === e.target)); } }, label);
    const form = h(
      "form",
      {
        class: "card setup-form",
        style: "margin: 0 16px 16px",
        onsubmit: async (event) => {
          event.preventDefault();
          try {
            await conn.call("workspace.save", { path: folder, name: name.value.trim(), kind, mounts });
            editing = "";
            await desk.refreshWorkspaces();
            toast("Saved. Tasks you start here from now on use it.");
            renderWorkspaces(root, ctx);
          } catch (e) {
            error.textContent = e.message;
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: "ws-name" }, "Name"), name),
      h("div", { class: "field" }, h("label", {}, "What's in it"), h("div", { class: "row-actions" }, kindChoice("docs", "Documents"), kindChoice("code", "Code"), kindChoice("mixed", "Both"))),
      h("div", { class: "field" }, h("label", {}, "Other folders nibb may use here"), list, h("div", { class: "row-actions" }, mountPath, ctx.pickFolder ? h("button", { type: "button", class: "btn small", onclick: async () => { const p = await ctx.pickFolder(); if (p) mountPath.value = p; } }, "Choose…") : null, h("button", { type: "button", class: "btn small", onclick: () => { const p = mountPath.value.trim(); if (p) { mounts.push({ path: p, access: "read" }); mountPath.value = ""; drawMounts(); } } }, "Add"))),
      h("p", { class: "hint" }, "Kept in this folder's .nibb/workspace.toml, which nibb itself never writes. A read-only folder can be read but never changed."),
      error,
      h(
        "div",
        { class: "row-actions" },
        h("button", { type: "submit", class: "btn dark" }, "Save"),
        h("button", { type: "button", class: "btn ghost", onclick: () => { editing = ""; renderWorkspaces(root, ctx); } }, "Cancel"),
        ws ? h("button", { type: "button", class: "btn danger", style: "margin-left:auto", onclick: async () => { try { await conn.call("workspace.remove", { path: folder }); editing = ""; await desk.refreshWorkspaces(); renderWorkspaces(root, ctx); } catch (e) { error.textContent = e.message; } } }, "Forget the name") : null,
      ),
    );
    return form;
  };
  const named = desk.workspaces.flatMap((ws) => row(ws.path, ws));
  const others = desk.folders().filter((f) => !desk.workspace(f)).flatMap((f) => row(f, null));
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Workspaces"),
        h("p", { class: "lede" }, "The folders nibb works in. A task is pinned to its folder and the folders mounted there: it reads and writes inside them and nowhere else."),
        named.length ? h("div", { class: "list" }, named) : null,
        others.length ? h("h2", { class: "section-title" }, named.length ? "Other folders tasks used" : "Folders tasks used") : null,
        others.length ? h("div", { class: "list" }, others) : null,
        !named.length && !others.length ? h("div", { class: "callout plain" }, "No folders yet. On Home, choose Add a folder.") : null,
      ),
    ),
  );
}

// ---------- connections ----------
let connectionsTab = "tools";
export async function renderConnections(root, ctx) {
  const { conn, desk, rerender } = ctx;
  const tab = (id, label) => h("button", { type: "button", role: "tab", "aria-selected": String(connectionsTab === id), onclick: () => { connectionsTab = id; rerender(); } }, label);
  const body = h("div", { style: "display:flex;flex-direction:column;gap:24px" });
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Connections"),
        h("p", { class: "lede" }, "What nibb can reach and what it may do there. Every tool crosses the same guard, whether it is built in or comes from elsewhere."),
        h("div", { class: "tabs", role: "tablist" }, tab("tools", "Tools"), tab("credentials", "Credentials"), tab("models", "Models")),
        body,
      ),
    ),
  );
  if (connectionsTab === "models") {
    body.append(...modelRows(desk));
    return;
  }
  // Tools and bindings are a session's: read them from an open task.
  const open = desk.all().find((s) => s.open);
  if (!open) {
    body.append(h("div", { class: "callout plain" }, "Open or resume a task to see the tools and credential bindings it has. Each task loads them when it starts."));
    return;
  }
  body.append(h("p", { class: "hint" }, "Loading…"));
  let info;
  try {
    info = await conn.call("session.info", { id: open.id });
  } catch (error) {
    body.replaceChildren(h("p", { class: "error" }, error.message));
    return;
  }
  const source = h("p", { class: "hint" }, `As loaded by the task “${open.title || open.id}” in ${desk.folderName(open.repo)}.`);
  if (connectionsTab === "credentials") {
    const rows = (info.bindings || []).map((b) => h("tr", {}, h("td", {}, h("span", { class: "mono" }, b.Class)), h("td", {}, b.Dest), h("td", {}, (b.Methods || []).join(", ")), h("td", {}, (b.Positions || []).join(", "))));
    body.replaceChildren(
      source,
      rows.length
        ? h("table", { class: "table" }, h("thead", {}, h("tr", {}, h("th", {}, "Credential"), h("th", {}, "Only for"), h("th", {}, "Methods"), h("th", {}, "Added as"))), h("tbody", {}, rows))
        : h("div", { class: "callout plain" }, "No credential bindings. A credential is used only where a binding allows it; nibb adds it at the last step and the model never sees the value."),
      h("p", { class: "hint" }, "Edit bindings from a task: Details › Policy."),
    );
    return;
  }
  const tools = info.tools || [];
  const external = tools.filter((t) => t.name.includes("/"));
  const builtin = tools.filter((t) => !t.name.includes("/"));
  body.replaceChildren(
    source,
    h("h2", { class: "section-title" }, "Offered to the model"),
    h(
      "div",
      { class: "list" },
      h("div", { class: "list-row" }, icon("terminal", 18), h("div", { class: "main" }, h("strong", {}, "nibb"), h("span", {}, `${builtin.length} tools · files, search, commands, notes, and the ones this folder defines`))),
      external.length ? h("div", { class: "list-row" }, icon("plug", 18), h("div", { class: "main" }, h("strong", {}, "MCP servers"), h("span", {}, `${external.length} tools`))) : null,
    ),
    h(
      "table",
      { class: "table" },
      h("thead", {}, h("tr", {}, h("th", {}, "Tool"), h("th", {}, "What it does"))),
      h("tbody", {}, tools.map((t) => h("tr", {}, h("td", {}, h("span", { class: "mono" }, t.name)), h("td", {}, t.description || "")))),
    ),
  );
  void toast;
}
