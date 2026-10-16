// The pages that are not tasks: Connections and Workspaces (Settings is
// settings.js). They show what the daemon reports; where it reports nothing
// yet, they say so.
import { h, icon, repoName, shortPath, ago, toast, sizeOf } from "./dom.js";
import { chooseFolder, forgetFolder } from "./home.js";
import { modelRows } from "./settings.js";
import { renderCredentials } from "./credentials.js";

// ---------- workspaces ----------
let editing = ""; // the folder whose editor is open
let addingWorkspace = false; // the editor for a folder not on the list yet is open
let removing = ""; // the folder whose forgetting waits for confirmation
export function renderWorkspaces(root, ctx) {
  const { desk, navigate, conn } = ctx;
  const counts = new Map();
  for (const item of desk.all()) {
    const entry = counts.get(desk.project(item)) || { tasks: 0, last: item.last_event };
    entry.tasks++;
    counts.set(desk.project(item), entry);
  }
  const canEdit = conn.has("workspace.save");
  const canRemove = conn.has("workspace.remove");
  const redraw = () => renderWorkspaces(root, ctx);
  // Removing forgets the name and the mounted folders; the folder and its
  // files stay, and tasks there keep their history.
  const remove = async (folder) => {
    const name = desk.folderName(folder);
    try {
      await conn.call("workspace.remove", { path: folder });
      removing = "";
      if (editing === folder) editing = "";
      forgetFolder(folder);
      await desk.refreshWorkspaces();
      toast(`Forgot ${name}. The folder, its files and its tasks are as they were.`);
    } catch (e) {
      toast(e.message);
    }
    redraw();
  };
  const row = (folder, ws) => {
    const count = counts.get(folder);
    const mounts = ws?.mounts || [];
    const actions =
      removing === folder
        ? [
            h("span", { class: "confirm-text" }, "Forget this folder? Its files and tasks stay."),
            h("button", { type: "button", class: "btn small danger", onclick: () => remove(folder) }, "Forget"),
            h("button", { type: "button", class: "btn small ghost", onclick: () => { removing = ""; redraw(); } }, "Keep"),
          ]
        : [
            h("button", { type: "button", class: "btn small", onclick: () => { chooseFolder(folder, desk); navigate("#/"); } }, "New task here"),
            canEdit ? h("button", { type: "button", class: "btn small ghost", onclick: () => { editing = editing === folder ? "" : folder; addingWorkspace = false; removing = ""; redraw(); } }, ws ? "Edit" : "Name it") : null,
            canRemove ? h("button", { type: "button", class: "btn small danger", "aria-label": `Forget ${desk.folderName(folder)}`, onclick: () => { removing = folder; redraw(); } }, "Forget") : null,
          ];
    const node = h(
      "div",
      { class: "list-row ws-row" },
      icon("folder", 18),
      h(
        "div",
        { class: "main" },
        h("strong", {}, ws ? ws.name : desk.folderName(folder)),
        h("span", { class: "mono", title: folder }, shortPath(folder, desk.home)),
        mounts.length ? h("span", {}, mounts.map((m) => `${shortPath(m.path, desk.home)} (${m.access === "write" ? "read and write" : "read only"})`).join(" · ")) : null,
        ws ? h("span", {}, ws.git ? "A git repository: git is the undo for what it has committed, and nodwyn keeps the rest." : "Not a git repository: nodwyn keeps the previous version of a file before it changes it.") : null,
        ws?.earlier_tasks ? h("span", { class: "ws-earlier" }, "The model may search the earlier tasks here.") : null,
        indexLine(folder),
        h("span", { class: "ws-count" }, count ? `${count.tasks} task${count.tasks === 1 ? "" : "s"} · ${ago(count.last)}` : "No tasks yet"),
      ),
      h("div", { class: "end" }, actions),
    );
    return editing === folder ? [node, editor(folder, ws)] : [node];
  };
  // indexLine says what the folder's repository index holds (ADR 50), read
  // after the row is drawn, with a way to forget it: it is built again at
  // the next search, so forgetting loses nothing but the time to build it.
  const indexLine = (folder) => {
    const line = h("span", { class: "ws-index" });
    if (!conn.has("index.status")) return line;
    conn.call("index.status", { repo: folder }).then((info) => {
      if (!info.bytes) return;
      const forget = conn.has("index.forget")
        ? h("button", { type: "button", class: "btn small ghost", onclick: async () => {
            try {
              await conn.call("index.forget", { repo: folder });
              toast("Forgot the search index. The next search builds it again.");
            } catch (e) {
              toast(e.message);
            }
            redraw();
          } }, "Forget the index")
        : null;
      line.replaceChildren(`Search index: ${info.files} files · ${sizeOf(info.bytes)}${info.updated ? ` · updated ${ago(info.updated)}` : ""}${info.trees > 1 ? ` · ${info.trees} working trees` : ""}`, forget || "");
    }).catch(() => {});
    return line;
  };
  // editor names a folder and the folders outside it its tasks may use;
  // with no folder it adds one, and asks for its path first.
  const editor = (folder, ws) => {
    const mounts = (ws?.mounts || []).map((m) => ({ ...m }));
    const name = h("input", { class: "input", id: "ws-name", value: ws?.name || (folder ? desk.folderName(folder) : ""), placeholder: folder ? "" : "for example Receipts 2026", autocomplete: "off" });
    const pathInput = folder ? null : h("input", { class: "input", id: "ws-path", placeholder: "/absolute/path/to/a/folder", spellcheck: false, autocomplete: "off", required: true });
    const setPath = (p) => {
      pathInput.value = p;
      if (!name.value) name.value = p.replace(/\/+$/, "").split("/").pop();
    };
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
    // Searching earlier tasks is the person's choice for each folder, kept
    // with its name on this computer (ADR 50 §9); off unless ticked here.
    const earlier = h("input", { type: "checkbox", id: "ws-earlier", checked: !!ws?.earlier_tasks });
    const form = h(
      "form",
      {
        class: "card setup-form",
        style: folder ? "margin: 0 16px 16px" : "",
        onsubmit: async (event) => {
          event.preventDefault();
          const path = folder || pathInput.value.trim().replace(/\/+$/, "");
          if (!path.startsWith("/")) {
            error.textContent = "Give the folder's full path, starting with /.";
            return;
          }
          const existing = folder ? null : desk.workspace(path);
          if (existing) {
            error.textContent = `That folder is already on the list as ${existing.name}: edit it there.`;
            return;
          }
          try {
            await conn.call("workspace.save", { path, name: name.value.trim(), mounts, earlier_tasks: earlier.checked });
            editing = "";
            addingWorkspace = false;
            await desk.refreshWorkspaces();
            toast(folder ? "Saved. Tasks you start here from now on use it." : "Added. Start a task there with New task here, or from Home.");
            redraw();
          } catch (e) {
            error.textContent = e.message;
          }
        },
      },
      folder ? null : h("div", { class: "field" }, h("label", { for: "ws-path" }, "Folder"), h("div", { class: "row-actions" }, pathInput, ctx.pickFolder ? h("button", { type: "button", class: "btn", onclick: async () => { try { const p = await ctx.pickFolder(); if (p) setPath(p); } catch (e) { error.textContent = String(e?.message || e); } } }, "Choose…") : null)),
      h("div", { class: "field" }, h("label", { for: "ws-name" }, "Name"), name),
      h("div", { class: "field" }, h("label", {}, "Other folders nodwyn may use here"), list, h("div", { class: "row-actions" }, mountPath, ctx.pickFolder ? h("button", { type: "button", class: "btn small", onclick: async () => { const p = await ctx.pickFolder(); if (p) mountPath.value = p; } }, "Choose…") : null, h("button", { type: "button", class: "btn small", onclick: () => { const p = mountPath.value.trim(); if (p) { mounts.push({ path: p, access: "read" }); mountPath.value = ""; drawMounts(); } } }, "Add"))),
      h("div", { class: "field" }, h("label", { class: "check" }, earlier, "Let the model search the earlier tasks here"), h("p", { class: "hint" }, "Off, a task sees only its own conversation. On, the model can search this folder's other tasks and read back what was asked in them, its replies and what its tools returned, from the logs on this computer.")),
      h("p", { class: "hint" }, "Kept on this computer with the workspace's name, not in the folder. A read-only folder can be read but never changed."),
      error,
      h(
        "div",
        { class: "row-actions" },
        h("button", { type: "submit", class: "btn dark" }, folder ? "Save" : "Add this folder"),
        h("button", { type: "button", class: "btn ghost", onclick: () => { editing = ""; addingWorkspace = false; redraw(); } }, "Cancel"),
      ),
    );
    return form;
  };
  const named = desk.workspaces.flatMap((ws) => row(ws.path, ws));
  const others = desk.folders().filter((f) => !desk.workspace(f)).flatMap((f) => row(f, null));
  const adder = addingWorkspace && canEdit ? editor("", null) : null;
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Workspaces"),
        h("p", { class: "lede" }, "The folders nodwyn works in. A task is pinned to its folder and the folders mounted there: it reads and writes inside them and nowhere else."),
        canEdit && !adder ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", onclick: () => { addingWorkspace = true; editing = ""; removing = ""; redraw(); document.getElementById("ws-path")?.focus(); } }, icon("plus", 15), "Add a workspace")) : null,
        adder,
        named.length ? h("div", { class: "list" }, named) : null,
        others.length ? h("h2", { class: "section-title" }, named.length ? "Other folders tasks used" : "Folders tasks used") : null,
        others.length ? h("div", { class: "list" }, others) : null,
        !named.length && !others.length && !adder ? h("div", { class: "callout plain" }, canEdit ? "No workspaces yet. Add the folders you want nodwyn to work in." : "No folders yet. On Home, choose Add a folder.") : null,
      ),
    ),
  );
}

// ---------- connections ----------
let connectionsTab = "tools";
let selectedServer = "";
let adding = false;
const inspections = new Map(); // server name -> { tools, manifest, approved, changed } or { error }

const connectionsTabs = ["tools", "credentials", "workspaces", "models"];

export async function renderConnections(root, ctx) {
  const { conn, desk } = ctx;
  if (connectionsTabs.includes(ctx.tab)) connectionsTab = ctx.tab;
  const tab = (id, label) => h("button", { type: "button", role: "tab", "aria-selected": String(connectionsTab === id), onclick: () => ctx.navigate(`#/connections/${id}`) }, label);
  const body = h("div", { style: "display:flex;flex-direction:column;gap:24px" });
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Connections"),
        h("p", { class: "lede" }, "What nodwyn can reach and what it may do there. Tools from MCP servers cross the same guard as the built-in ones."),
        h("div", { class: "tabs", role: "tablist" }, tab("tools", "Tools"), tab("credentials", "Credentials"), tab("workspaces", "Workspaces"), tab("models", "Models")),
        body,
      ),
    ),
  );
  if (connectionsTab === "models") {
    body.append(...modelRows(desk));
    return;
  }
  if (connectionsTab === "workspaces") {
    const rows = desk.workspaces.map((w) =>
      h("div", { class: "list-row" }, icon("folder", 18), h("div", { class: "main" }, h("strong", {}, w.name), h("span", {}, `${shortPath(w.path, desk.home)}${w.mounts?.length ? ` · ${w.mounts.length} mounted folder${w.mounts.length === 1 ? "" : "s"}` : ""}`))),
    );
    body.append(
      rows.length ? h("div", { class: "list" }, rows) : h("p", { class: "hint" }, "No named workspaces yet."),
      h("div", { class: "row-actions" }, h("a", { class: "btn", href: "#/workspaces" }, "Manage workspaces")),
    );
    return;
  }
  if (connectionsTab === "credentials" && conn.has("credentials.list")) return renderCredentials(body, ctx, () => renderConnections(root, ctx));
  const open = desk.all().find((s) => s.open);
  let info = null;
  if (open) {
    try {
      info = await conn.call("session.info", { id: open.id });
    } catch {
      info = null;
    }
  }
  if (connectionsTab === "credentials") return credentials(body, info, open, desk);
  return tools(body, ctx, info, () => renderConnections(root, ctx));
}

function credentials(body, info, open, desk) {
  if (!info) {
    body.append(h("div", { class: "callout plain" }, "Open or resume a task to see the credential bindings it loaded."));
    return;
  }
  const rows = (info.bindings || []).map((b) => h("tr", {}, h("td", {}, h("span", { class: "mono" }, b.Class)), h("td", {}, b.Dest), h("td", {}, (b.Methods || []).join(", ")), h("td", {}, (b.Positions || []).join(", "))));
  body.append(
    h("p", { class: "hint" }, `As loaded by “${open.title || open.id}” in ${desk.folderName(desk.project(open))}.`),
    rows.length
      ? h("table", { class: "table" }, h("thead", {}, h("tr", {}, h("th", {}, "Credential"), h("th", {}, "Only for"), h("th", {}, "Methods"), h("th", {}, "Added as"))), h("tbody", {}, rows))
      : h("div", { class: "callout plain" }, "No credential bindings. A credential is used only where a binding allows it; nodwyn adds it at the last step and the model never sees the value."),
    h("p", { class: "hint" }, "Edit bindings from a task: Details › Policy."),
  );
}

async function tools(body, ctx, info, redraw) {
  const { conn, desk } = ctx;
  let servers = [];
  if (conn.has("mcp.list")) {
    try {
      servers = await conn.call("mcp.list");
    } catch (error) {
      body.append(h("p", { class: "error" }, error.message));
    }
  }
  const builtin = (info?.tools || []).filter((t) => !t.name.includes("__"));
  const canEdit = conn.has("mcp.save");
  // Whether contained servers can run here, and whose image is present.
  let runtime = null;
  if (servers.some(contained) && conn.has("mcp.runtime")) {
    try {
      runtime = await conn.call("mcp.runtime");
    } catch (error) {
      runtime = { runtime: "", detail: error.message, images: {} };
    }
  }
  const serverRow = (server) => {
    const on = Object.values(server.tools || {}).filter((t) => t.on).length;
    const inspection = inspections.get(server.name);
    const missing = contained(server) && runtime?.images?.[server.name] === "missing";
    const state =
      contained(server) && runtime && !runtime.runtime
        ? ["bad", "no runtime"]
        : missing
          ? ["warn", "needs pull"]
          : !server.manifest
            ? ["warn", "not reviewed"]
            : inspection?.changed
              ? ["warn", "changed"]
              : inspection?.error
                ? ["bad", contained(server) ? "does not start" : "unreachable"]
                : ["ok", "pinned"];
    const where = contained(server) ? `Contained · ${imageName(server.image)} · network: ${networkWords(server)}` : `HTTP · ${host(server.url)}`;
    return h(
      "button",
      { type: "button", class: `list-row server-row${selectedServer === server.name ? " selected" : ""}`, onclick: () => { selectedServer = selectedServer === server.name ? "" : server.name; adding = false; redraw(); } },
      icon(contained(server) ? "box" : "plug", 18),
      h("div", { class: "main" }, h("strong", {}, server.name), h("span", {}, `${where} · ${on} tool${on === 1 ? "" : "s"} on`)),
      h("div", { class: "end" }, h("span", { class: `dot ${state[0]}` }), state[1]),
    );
  };
  body.append(
    ...[
    h("h2", { class: "section-title" }, "Built in"),
    h("div", { class: "list" }, h("div", { class: "list-row" }, icon("terminal", 18), h("div", { class: "main" }, h("strong", {}, "nodwyn"), h("span", {}, info ? `${builtin.length} tools · files, search, notes${builtin.some((t) => t.name === "shell-exec") ? ", commands" : ""}, and the ones a folder defines` : "Open a task to see its tools.")))),
    h("h2", { class: "section-title" }, "MCP servers"),
    servers.length ? h("div", { class: "list" }, servers.map(serverRow)) : h("p", { class: "hint" }, "No MCP servers yet. A server's tools stay off until you review them and choose which to offer."),
    canEdit ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", onclick: () => { adding = !adding; selectedServer = ""; redraw(); } }, icon("plus", 15), "Add a server")) : null,
    ].filter(Boolean),
  );
  if (adding && canEdit) body.append(addServer(ctx, redraw));
  const server = servers.find((s) => s.name === selectedServer);
  if (server) body.append(await serverDetail(server, ctx, redraw, runtime));
  body.append(h("div", { class: "callout plain" }, "An undeclared tool is treated as sending out and irreversible: every call asks you, and anything its credential isn't allowed to reach is refused."));
  void desk;
}

function host(u) {
  try {
    return new URL(u).host;
  } catch {
    return u;
  }
}

// A contained server runs here, in a container (ADR 23 W3b).
const contained = (server) => server.transport === "container";

// imageName is an image reference short enough for a row: its last path
// part and the start of its digest.
function imageName(ref = "") {
  const [name, digest = ""] = ref.split("@");
  return `${name.split("/").pop()}@${digest.slice(0, 19)}…`;
}

function networkWords(server) {
  return server.network?.length ? server.network.join(", ") : "none";
}

// words splits a field into its entries: commas, spaces or new lines.
const words = (text) => text.split(/[\s,]+/).filter(Boolean);

function addServer(ctx, redraw) {
  const { conn, desk } = ctx;
  const name = h("input", { class: "input", id: "mcp-name", placeholder: "for example linear", required: true, pattern: "[a-z][a-z0-9\\-]{0,31}" });
  const url = h("input", { class: "input", id: "mcp-url", placeholder: "https://mcp.example.com/mcp", required: true, spellcheck: false });
  const image = h("input", { class: "input", id: "mcp-image", placeholder: "ghcr.io/example/mcp-notes@sha256:…", spellcheck: false });
  const network = h("input", { class: "input", id: "mcp-network", placeholder: "none; or api.example.com, uploads.example.com:8443", spellcheck: false });
  const mounts = h("input", { class: "input", id: "mcp-mounts", placeholder: "none; or docs, out:write", spellcheck: false });
  const command = h("input", { class: "input", id: "mcp-command", placeholder: "the image's own", spellcheck: false });
  const args = h("textarea", { class: "input", id: "mcp-args", rows: 2, placeholder: "one per line; the image's own if empty", spellcheck: false });
  const envName = h("input", { class: "input", id: "mcp-env", placeholder: "for example NOTION_TOKEN", spellcheck: false });
  const key = h("input", { class: "input", id: "mcp-key", type: "password", autocomplete: "off", placeholder: "only if the server needs one" });
  const keyHint = h("p", { class: "hint" });
  const error = h("p", { class: "error", role: "alert" });
  const folders = desk.folders();
  const chosen = new Set();
  let kind = "http";
  const remoteFields = h("div", { class: "fields" }, h("div", { class: "field" }, h("label", { for: "mcp-url" }, "Server address"), url, h("p", { class: "hint" }, "A remote server over HTTPS. nodwyn runs nothing of it on this computer.")));
  const containedFields = h(
    "div",
    { class: "fields", hidden: true },
    h("div", { class: "field" }, h("label", { for: "mcp-image" }, "Image"), image, h("p", { class: "hint" }, "Pinned by its digest, never a tag: the server cannot change under you. Pull it from the server's page once added; a task never downloads one.")),
    h("div", { class: "field" }, h("label", { for: "mcp-network" }, "Hosts it may reach"), network, h("p", { class: "hint" }, "Through nodwyn's proxy, which lets through these hosts alone (ports 443 and 80, or the one you write) and records every attempt. Empty: no network at all.")),
    h("div", { class: "field" }, h("label", { for: "mcp-mounts" }, "Folders it sees"), mounts, h("p", { class: "hint" }, "Folders of the workspace, read-only unless you write :write; it sees them under /workspace. Empty: none of your files.")),
    h("div", { class: "field" }, h("label", { for: "mcp-command" }, "Command"), command, args, h("p", { class: "hint" }, "Only to replace what the image runs; no shell.")),
    h("div", { class: "field" }, h("label", { for: "mcp-env" }, "Key variable"), envName, h("p", { class: "hint" }, "The environment variable the server reads its key from, if it needs one.")),
  );
  const describeKey = () => {
    keyHint.textContent =
      kind === "container"
        ? "Kept in the keychain and given to the server when it starts, as the variable above, only if it is allowed for every host it may reach."
        : "Kept in the keychain and sent only to this server's address, as a bearer Authorization header.";
  };
  describeKey();
  const choose = (value) => (e) => {
    kind = value;
    for (const b of e.target.parentElement.children) b.setAttribute("aria-pressed", String(b === e.target));
    remoteFields.hidden = kind !== "http";
    containedFields.hidden = kind !== "container";
    url.required = kind === "http";
    image.required = kind === "container";
    describeKey();
  };
  return h(
    "form",
    {
      class: "card setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const server = { name: name.value.trim(), workspaces: [...chosen], tools: {} };
        if (kind === "container") {
          Object.assign(server, { transport: "container", image: image.value.trim(), network: words(network.value), mounts: words(mounts.value), args: args.value.split("\n").map((a) => a.trim()).filter(Boolean) });
          if (command.value.trim()) server.command = command.value.trim();
          if (key.value) server.position = `env:${envName.value.trim()}`;
        } else server.url = url.value.trim();
        const params = { server };
        if (key.value) params.api_key = key.value;
        key.value = "";
        try {
          await conn.call("mcp.save", params);
          adding = false;
          selectedServer = params.server.name;
          redraw();
        } catch (e) {
          error.textContent = e.message;
        }
      },
    },
    h("div", { class: "field" }, h("label", { for: "mcp-name" }, "Name"), name, h("p", { class: "hint" }, "Lower-case letters, digits and dashes. Its tools appear as name/tool.")),
    h(
      "div",
      { class: "field" },
      h("label", {}, "Where it runs"),
      h(
        "div",
        { class: "row-actions" },
        h("button", { type: "button", class: "chip", "aria-pressed": "true", onclick: choose("http") }, "Remote, over HTTPS"),
        h("button", { type: "button", class: "chip", "aria-pressed": "false", onclick: choose("container") }, "Contained, on this computer"),
      ),
    ),
    remoteFields,
    containedFields,
    h("div", { class: "field" }, h("label", { for: "mcp-key" }, "API key"), key, keyHint),
    h("div", { class: "field" }, h("label", {}, "Offer it in"), h("div", { class: "row-actions" }, folders.map((f) => h("button", { type: "button", class: "chip", "aria-pressed": "false", onclick: (e) => { const on = e.target.getAttribute("aria-pressed") !== "true"; e.target.setAttribute("aria-pressed", String(on)); if (on) chosen.add(f); else chosen.delete(f); } }, desk.folderName(f))))),
    error,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn dark" }, "Add and review its tools"), h("button", { type: "button", class: "btn ghost", onclick: () => { adding = false; redraw(); } }, "Cancel")),
  );
}

// containedWords says what a contained server can reach, for its page.
function containedWords(server, runtime) {
  const mounts = (server.mounts || []).map((m) => (m.endsWith(":write") ? `${m.slice(0, -6)} (may write)` : `${m.replace(/:read$/, "")} (read-only)`));
  const reach = server.network?.length ? `no way out but nodwyn's proxy, to ${server.network.join(", ")}` : "no network at all";
  const folders = mounts.length ? `the workspace folders ${mounts.join(", ")}` : "none of your files";
  const how = runtime?.runtime ? ` with ${runtime.runtime}` : "";
  return `A contained server: nodwyn runs it on this computer${how}, in a container from this image, with ${reach}, and ${folders}. It gets the arguments of the calls you allow, and is removed when the task ends.`;
}

async function serverDetail(server, ctx, redraw, runtime) {
  const { conn, desk } = ctx;
  const canEdit = conn.has("mcp.save");
  const isContained = contained(server);
  const panel = h("section", { class: "card server-detail" }, h("p", { class: "hint" }, isContained ? "Starting it in a container to read its tools…" : `Connecting to ${host(server.url)} to read its tools…`));
  // A contained server whose image is not here, or with no runtime to run
  // it, is not started: the page says what is missing instead.
  const missing = isContained && runtime?.images?.[server.name] === "missing";
  const noRuntime = isContained && runtime && !runtime.runtime;
  let inspection = inspections.get(server.name);
  if (!inspection && (missing || noRuntime)) inspection = { error: noRuntime ? runtime.detail : "its image is not on this computer" };
  if (!inspection && conn.has("mcp.inspect")) {
    try {
      inspection = await conn.call("mcp.inspect", { name: server.name });
    } catch (error) {
      inspection = { error: error.message };
    }
    inspections.set(server.name, inspection);
  }
  const draft = JSON.parse(JSON.stringify(server));
  draft.tools ||= {};
  draft.workspaces ||= [];
  const liveNames = (inspection?.tools || []).map((t) => t.name);
  const names = [...new Set([...liveNames, ...Object.keys(draft.tools)])].sort();
  const effectSelect = (name) =>
    h(
      "select",
      { class: "input", "aria-label": `What ${name} does`, disabled: !canEdit, onchange: (e) => { draft.tools[name] = { ...(draft.tools[name] || {}), effect: e.target.value }; } },
      h("option", { value: "", selected: !draft.tools[name]?.effect }, "not declared: asks every call"),
      h("option", { value: "read", selected: draft.tools[name]?.effect === "read" }, "reads"),
      h("option", { value: "write", selected: draft.tools[name]?.effect === "write" }, "changes things"),
    );
  const offered = (name) => {
    const box = h("input", { type: "checkbox", "aria-label": `Offer ${name}`, checked: !!draft.tools[name]?.on, disabled: !canEdit || !liveNames.includes(name), onchange: (e) => { draft.tools[name] = { ...(draft.tools[name] || {}), on: e.target.checked }; } });
    return box;
  };
  const rows = names.map((name) => {
    const live = inspection?.tools?.find((t) => t.name === name);
    return h(
      "tr",
      {},
      h("td", {}, h("span", { class: "mono" }, name), live?.description ? h("div", { class: "hint", style: "max-width:360px" }, live.description.slice(0, 180)) : h("div", { class: "hint" }, liveNames.length ? "no longer offered by the server" : "")),
      h("td", {}, effectSelect(name)),
      h("td", {}, offered(name)),
    );
  });
  const pull = async (e) => {
    e.target.disabled = true;
    e.target.textContent = "Pulling…";
    try {
      // The daemon gives a pull thirty minutes, and the bridge a little more.
      await conn.call("mcp.pull", { name: server.name }, 32 * 60 * 1000);
      inspections.delete(server.name);
      toast("Pulled. Review its tools before nodwyn offers any.");
      redraw();
    } catch (err) {
      toast(err.message);
      e.target.disabled = false;
      e.target.textContent = "Pull the image";
    }
  };
  const banner = missing
    ? h(
        "div",
        { class: "callout" },
        h("div", {}, h("strong", {}, "Its image is not on this computer. "), "Pulling downloads it from its registry, by its digest; nothing else in nodwyn downloads a server, and a task never does."),
        canEdit && conn.has("mcp.pull") ? h("button", { type: "button", class: "btn dark", style: "margin-left:auto", onclick: pull }, "Pull the image") : null,
      )
    : noRuntime
      ? h("div", { class: "callout" }, `It cannot run here: ${runtime.detail}. Remote servers still work.`)
      : inspection?.error
    ? h("div", { class: "callout" }, isContained ? `It did not start: ${inspection.error}` : `Could not reach it: ${inspection.error}`)
    : !server.manifest
      ? h("div", { class: "callout" }, h("div", {}, h("strong", {}, "Review these tools before nodwyn offers any. "), "Approving pins this exact tool list: if the server changes a name, description or schema, its tools stop until you review again."))
      : inspection?.changed
        ? h("div", { class: "callout" }, h("div", {}, h("strong", {}, "Its tool list changed since you approved it. "), "Its tools are off in new tasks until you review and approve the new list. ", h("span", { class: "mono" }, `${(inspection.approved || "").slice(0, 8)}… → ${(inspection.manifest || "").slice(0, 8)}…`)))
        : null;
  const approve = canEdit && inspection?.manifest && (inspection.changed || !server.manifest)
    ? h(
        "button",
        {
          type: "button",
          class: "btn dark",
          onclick: async () => {
            try {
              await conn.call("mcp.save", { server: draft });
              await conn.call("mcp.approve", { name: server.name, manifest: inspection.manifest });
              inspections.delete(server.name);
              toast("Approved. New tasks in its workspaces offer the tools you turned on.");
              redraw();
            } catch (e) {
              toast(e.message);
            }
          },
        },
        "Approve this tool list",
      )
    : null;
  const workspaceChips = h(
    "div",
    { class: "row-actions" },
    desk.folders().map((f) => h("button", { type: "button", class: "chip", "aria-pressed": String(draft.workspaces.includes(f)), disabled: !canEdit, onclick: (e) => { const on = !draft.workspaces.includes(f); draft.workspaces = on ? [...draft.workspaces, f] : draft.workspaces.filter((x) => x !== f); e.target.setAttribute("aria-pressed", String(on)); } }, desk.folderName(f))),
  );
  panel.replaceChildren(
    ...[
    h(
      "div",
      { class: "card-head" },
      h("div", { style: "display:flex;flex-direction:column;gap:2px" }, h("strong", { style: "font-size:17px" }, server.name), h("span", { class: "hint mono" }, isContained ? server.image : server.url)),
      canEdit ? h("button", { type: "button", class: "btn small danger", style: "margin-left:auto", onclick: async () => { try { await conn.call("mcp.remove", { name: server.name }); selectedServer = ""; inspections.delete(server.name); redraw(); } catch (e) { toast(e.message); } } }, "Disconnect") : null,
    ),
    h("p", { class: "hint" }, isContained ? containedWords(server, runtime) : "A remote server: nodwyn sends it the arguments of the calls you allow, and runs nothing of it on this computer."),
    banner,
    h("div", { class: "field" }, h("label", {}, "Offered in"), workspaceChips),
    names.length ? h("table", { class: "table" }, h("thead", {}, h("tr", {}, h("th", {}, "Tool"), h("th", {}, "What it does to the world"), h("th", {}, "Offered"))), h("tbody", {}, rows)) : null,
    server.credential
      ? h(
          "div",
          { class: "file-row" },
          icon("key", 15),
          h(
            "div",
            {},
            h("span", { class: "mono" }, server.credential),
            " in the keychain. ",
            h(
              "span",
              { class: "hint" },
              isContained
                ? `Given to the server when it starts, as the ${server.position.replace(/^env:/, "")} variable, only if it is allowed for every host it may reach (${networkWords(server)}). It stays valid as long as the token does; the model never sees the value.`
                : `Only for ${host(server.url)} · POST · as ${server.position}. nodwyn adds it at the last step; the model never sees the value.`,
            ),
          ),
        )
      : h("p", { class: "hint" }, isContained ? "No credential: the server starts with no key." : "No credential: requests go without one."),
    canEdit
      ? h(
          "div",
          { class: "row-actions" },
          h("button", { type: "button", class: "btn", onclick: async () => { try { await conn.call("mcp.save", { server: draft }); toast("Saved. New tasks use it."); inspections.delete(server.name); redraw(); } catch (e) { toast(e.message); } } }, "Save"),
          approve,
          h("button", { type: "button", class: "btn ghost", onclick: () => { inspections.delete(server.name); redraw(); } }, "Check again"),
        )
      : null,
    ].filter(Boolean),
  );
  return panel;
}
