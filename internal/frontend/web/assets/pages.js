// The pages that are not tasks: Connections, Workspaces and Settings. They
// show what the daemon reports; where it reports nothing yet, they say so.
import { h, icon, repoName, shortPath, ago, toast } from "./dom.js";
import { chooseFolder } from "./home.js";
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
}

// ---------- workspaces ----------
export function renderWorkspaces(root, ctx) {
  const { desk, navigate } = ctx;
  const byFolder = new Map();
  for (const item of desk.all()) {
    const entry = byFolder.get(item.repo) || { tasks: 0, last: item.last_event, open: 0 };
    entry.tasks++;
    if (item.open) entry.open++;
    byFolder.set(item.repo, entry);
  }
  const rows = [...byFolder.entries()].map(([folder, entry]) =>
    h(
      "div",
      { class: "list-row" },
      icon("folder", 18),
      h("div", { class: "main" }, h("strong", {}, repoName(folder)), h("span", { class: "mono", title: folder }, shortPath(folder, desk.home))),
      h("div", { class: "end" }, `${entry.tasks} task${entry.tasks === 1 ? "" : "s"} · ${ago(entry.last)}`, h("button", { type: "button", class: "btn small", onclick: () => { chooseFolder(folder); navigate("#/"); } }, "New task here")),
    ),
  );
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Workspaces"),
        h("p", { class: "lede" }, "The folders nibb has worked in. Each task is pinned to its folder: it can read and write there and nowhere else."),
        rows.length ? h("div", { class: "list" }, rows) : h("div", { class: "callout plain" }, "No folders yet. Start a task from Home and choose a folder."),
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
  const source = h("p", { class: "hint" }, `As loaded by the task “${open.title || open.id}” in ${repoName(open.repo)}.`);
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
