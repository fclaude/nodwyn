// Entry point: pairing, the connection, the router and the shell. Views are
// redrawn from the desk's state; the task view keeps its own conversation.
import { $, h, icon, toast } from "./dom.js";
import { Connection, pair, takeTicket } from "./connection.js";
import { Desk } from "./desk.js";
import { renderSidebar } from "./sidebar.js";
import { renderHome } from "./home.js";
import { ConversationSearchView, ConversationContextView } from "./search.js";
import { TaskView } from "./task.js";
import { renderWorkspaces, renderConnections } from "./pages.js";
import { renderSettings, applyTheme, storedTheme, applyThinking, storedThinking } from "./settings.js";

applyTheme(storedTheme());
applyThinking(storedThinking());
const conn = new Connection();
const desk = new Desk(conn);
// One view per task. An open task's view is kept when another page is shown:
// its subscription stays and its events keep arriving, so coming back
// catches up at once instead of reading its whole log again, which took
// seconds for a long task. The least recently shown go past keptViews.
const views = new Map(); // task id -> TaskView, least recently shown first
const keptViews = 8;
let task = null; // the view on screen, when a task is
let route = parse();
let homeState = null;
let conversationView = null;

function parse() {
  const hash = location.hash.replace(/^#/, "");
  const [path, queryString = ""] = hash.split("?");
  const query = new URLSearchParams(queryString);
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "search" && parts[1]) return { name: "search", id: decodeURIComponent(parts[1]), query: query.get("q") || "", scope: query.get("scope") || "conversation" };
  if (parts[0] === "history" && parts[1] && parts[2]) return { name: "history", id: decodeURIComponent(parts[1]), seq: Number(parts[2]), search: query.get("search") || "", query: query.get("q") || "", scope: query.get("scope") || "conversation" };
  if (parts[0] === "task" && parts[1]) return { name: "task", id: decodeURIComponent(parts[1]), review: parts[2] === "review" };
  if (["settings", "workspaces", "connections"].includes(parts[0])) return { name: parts[0], tab: parts[1] };
  return { name: "home" };
}
function navigate(hash) {
  if (location.hash === hash) render();
  else location.hash = hash;
}
window.addEventListener("hashchange", () => {
  const next = parse();
  conversationView?.leave();
  conversationView = null;
  if (route.name === "task" && (next.name !== "task" || next.id !== route.id)) putAway(task);
  route = next;
  closeMenu();
  render(true);
});

// ---------- gate screens ----------
function gate(content) {
  $("gate").replaceChildren(h("div", { class: "gate-card" }, content));
  $("gate").hidden = false;
  $("shell").hidden = true;
}
const local = ["127.0.0.1", "localhost", "[::1]"].includes(location.hostname);
function showPairing(message = "") {
  const input = h("input", { class: "input", id: "pair-code", type: "password", autocomplete: "off", spellcheck: false, required: true });
  // Browsers compile a pattern with the v flag, where a hyphen in a class
  // must be escaped: unescaped, the pattern is ignored.
  const device = h("input", { class: "input", id: "pair-device", autocomplete: "off", spellcheck: false, required: !local, pattern: "[a-z0-9][a-z0-9._\\-]{0,31}", placeholder: "for example phone" });
  const error = h("p", { class: "error", role: "alert" }, message);
  gate([
    h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
    h("h1", {}, "Connect this browser"),
    h("p", {}, local ? "Paste the pairing code nodwyn printed where it was started. The code stays between this browser and your computer and is never added to a task." : "Paste the pairing code nodwyn printed on its computer, and name this device. Every answer you give from here is recorded with that name."),
    h(
      "form",
      {
        onsubmit: async (event) => {
          event.preventDefault();
          const code = input.value;
          input.value = "";
          error.textContent = "";
          try {
            await pair(local ? { code } : { code, device: device.value.trim().toLowerCase() });
            conn.connect();
          } catch (e) {
            error.textContent = e.message || "Pairing failed. Copy the code again from where nodwyn is running.";
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: "pair-code" }, "Pairing code"), input),
      local ? null : h("div", { class: "field" }, h("label", { for: "pair-device" }, "This device's name"), device, h("p", { class: "hint" }, "Lower-case letters, digits, dots or dashes. It stays paired until revoked on the computer.")),
      h("button", { type: "submit", class: "btn accent big" }, "Connect"),
    ),
    error,
  ]);
  input.focus();
}
function showDown(detail) {
  gate([
    h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
    h("h1", {}, "Connection lost"),
    h("p", {}, "nodwyn isn't answering. Nothing was retried: when it's back, reconnect and the conversation reloads from its record."),
    detail ? h("p", { class: "error" }, detail) : null,
    h("button", { type: "button", class: "btn accent big", onclick: () => conn.connect() }, "Reconnect"),
  ]);
}

// ---------- shell ----------
function closeMenu() {
  $("shell").classList.remove("menu-open");
  $("scrim")?.remove();
}
function openMenu() {
  $("shell").classList.add("menu-open");
  const scrim = h("div", { class: "scrim", id: "scrim", onclick: closeMenu });
  $("shell").append(scrim);
}

// showTask puts task id's view on screen: the kept one as it is, or one
// that reads the conversation.
function showTask(id, review = false) {
  let view = views.get(id);
  views.delete(id);
  if (task && task !== view) putAway(task);
  if (view?.keeps(id)) {
    views.set(id, view);
    task = view;
    $("view").replaceChildren(view.root);
    view.show(desk.sessions.get(id));
    if (review) view.openReview();
    else if (view.inspecting === "review" && view.activePrompt()) view.closeInspector();
  } else {
    view ||= new TaskView(conn, desk, navigate);
    views.set(id, view);
    task = view;
    $("view").replaceChildren(view.root);
    view.open(id).then(() => {
      // A navigation may finish after the reader has already left the task.
      if (review && task === view && route.name === "task" && route.id === id && route.review && !view.failed) view.openReview();
    });
  }
  for (const [other, kept] of views) {
    if (views.size <= keptViews) break;
    if (kept === task) continue;
    kept.dispose();
    views.delete(other);
  }
}
// putAway takes a task's view off screen: kept while its task is open,
// ended otherwise, since a saved task's view has nothing coming to it.
function putAway(view) {
  if (!view) return;
  const id = view.item?.id;
  if (id && view.keeps(id)) view.park();
  else {
    view.dispose();
    for (const [key, kept] of views) if (kept === view) views.delete(key);
  }
  if (task === view) task = null;
}
// dropViews ends every view: a new connection has none of their
// subscriptions, so there is nothing to unsubscribe.
function dropViews() {
  for (const view of views.values()) view.dispose(false);
  views.clear();
  task = null;
  conversationView?.leave();
  conversationView = null;
}

let lastRoute = "";
// render draws the shell and the route. what is the desk change that asked
// for it ("" when something else did).
function render(routeChanged = false, what = "") {
  if (conn.state !== "live") return;
  $("gate").hidden = true;
  $("shell").hidden = false;
  renderSidebar($("sidebar"), { desk, route, conn, rerender: () => render() });
  const attention = desk.needsYou().length;
  $("topbar-count").textContent = attention ? `${attention} need${attention === 1 ? "s" : ""} you` : "";
  $("topbar-count").hidden = !attention;
  document.title = attention ? `(${attention}) nodwyn` : "nodwyn";
  const view = $("view");
  const key = ["task", "search", "history"].includes(route.name) ? `${route.name}:${route.id}` : route.name;
  const ctx = { conn, desk, navigate, rerender: () => render(), pickFolder: window.nodwynPickFolder };
  if (route.name === "task") {
    if (key !== lastRoute || routeChanged || !task) showTask(route.id, route.review);
    else {
      // The list changed: keep the conversation, refresh what the task shows.
      const fresh = desk.sessions.get(route.id);
      if (fresh && task.item && task.item.id === fresh.id) {
        Object.assign(task.item, { open: fresh.open, run: fresh.run, title: fresh.title, archived: fresh.archived, worktree: fresh.worktree, schedule: fresh.schedule });
        if (!fresh.open) task.subscribed = false;
        // A scheduled run can reopen a task while its closed transcript is
        // on screen. Its new stream needs a subscription and fixed snapshot.
        if (fresh.open && !task.subscribed && (!task.loading || task.failed)) task.open(fresh.id);
        else task.refreshAll();
      }
    }
  } else if (route.name === "search" || route.name === "history") {
    if (key !== lastRoute || routeChanged || !conversationView) {
      conversationView?.leave();
      conversationView = route.name === "search" ? new ConversationSearchView(conn, desk, navigate, route) : new ConversationContextView(conn, desk, route);
      view.replaceChildren(conversationView.root);
      conversationView.open();
    }
  } else if (route.name === "home") {
    homeState = renderHome(view, { ...ctx, keepTop: key === lastRoute && !routeChanged });
  } else if (key !== lastRoute || routeChanged) {
    if (route.name === "settings") renderSettings(view, { ...ctx, tab: route.tab });
    else if (route.name === "workspaces") renderWorkspaces(view, ctx);
    else if (route.name === "connections") renderConnections(view, { ...ctx, tab: route.tab });
  } else if (route.name === "settings" && (!what || what === "setup" || what === "models")) {
    // Settings holds fields being typed into: a task's progress elsewhere
    // does not redraw it.
    renderSettings(view, { ...ctx, tab: route.tab });
  } else if ((route.name === "workspaces" || route.name === "connections") && !editing(view)) {
    // Workspaces and Connections read the desk too (the models' state, the
    // folders): drawn once, they stayed on "Checking the configured
    // models…" until the person went away and back. They follow it, unless
    // a field on them is being edited.
    if (route.name === "workspaces") renderWorkspaces(view, ctx);
    else renderConnections(view, { ...ctx, tab: route.tab });
  }
  lastRoute = key;
}

// editing reports whether a field in root has the focus or holds what was
// typed into it, which a redraw would lose.
function editing(root) {
  if (root.contains(document.activeElement) && document.activeElement.matches("input, textarea, select")) return true;
  return [...root.querySelectorAll("input, textarea")].some((el) => (el.type === "checkbox" || el.type === "radio" ? el.checked !== el.defaultChecked : el.value !== el.defaultValue));
}

desk.onChange((what) => render(false, what));
desk.notifier.onOpen = (id) => navigate(`#/task/${id}`);
conn.onState(async (state, detail) => {
  $("offline").hidden = state === "live" || state === "connecting" || $("shell").hidden;
  if (state === "unpaired") {
    desk.reset();
    showPairing(detail);
  } else if (state === "down") {
    desk.reset();
    if ($("shell").hidden) showDown(detail);
    else $("offline").hidden = false;
  } else if (state === "live") {
    dropViews();
    lastRoute = "";
    delete document.documentElement.dataset.ready;
    try {
      await desk.start();
    } catch (error) {
      toast(error.message);
    }
    render(true);
    // The browser checks wait for this mark before they look at a page: the
    // desk's first refresh is drawn and the models have answered.
    await desk.modelsChecked;
    if (conn.state === "live") document.documentElement.dataset.ready = "";
  }
});
setInterval(() => {
  if (conn.live) desk.refreshModels(false, true);
}, 60000);

// ⌘N / Ctrl+N: a new task, as the sidebar promises.
document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "n" && !event.shiftKey) {
    event.preventDefault();
    navigate("#/");
    setTimeout(() => homeState?.focus(), 0);
  }
});

// In the app window, links leave through the host: a web view opens no
// second window of its own.
document.addEventListener("click", (event) => {
  const link = event.target.closest?.("a[target=_blank]");
  if (!link || typeof window.nodwynOpenExternal !== "function") return;
  event.preventDefault();
  window.nodwynOpenExternal(link.href).catch((error) => toast(String(error)));
});

$("menu").append(icon("menu", 20));
$("menu").addEventListener("click", openMenu);
$("reconnect").addEventListener("click", () => conn.connect());

// Wails can install the native bridge after the first console render.
window.addEventListener("nodwyn-desktop-ready", () => render(true));

// A launch ticket (from nodwyn app or nodwyn ui) pairs this browser without a
// typed code. It is used once, and removed from the address bar first.
(async () => {
  const ticket = takeTicket();
  if (ticket) {
    try {
      await pair({ ticket });
    } catch {
      /* an expired or used ticket falls back to the pairing code */
    }
  }
  conn.connect();
})();
