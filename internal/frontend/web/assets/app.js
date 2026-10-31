// Entry point: pairing, the connection, the router and the shell. Views are
// redrawn from the desk's state; the task view keeps its own conversation.
import { $, h, icon, toastError } from "./dom.js";
import { Connection, pair, takeTicket, takeInvite } from "./connection.js";
import { pairing, passkeysHere, register, assert, confirmTouch } from "./passkey.js";
import { Desk } from "./desk.js";
import { renderSidebar } from "./sidebar.js";
import { renderHome } from "./home.js";
import { ConversationSearchView, ConversationContextView } from "./search.js";
import { TaskView } from "./task.js";
import { renderWorkspaces, renderConnections } from "./pages.js";
import { renderWorkflows } from "./workflows.js";
import { Hosts, renderHosts } from "./hosts.js";
import { installDropdowns } from "./dropdown.js";
import { renderSettings, applyTheme, storedTheme, applyThinking, storedThinking } from "./settings.js";

applyTheme(storedTheme());
applyThinking(storedThinking());
const conn = new Connection();
const desk = new Desk(conn);
// This computer and the nodwyns it is linked to, each with a connection and
// a desk of its own (ADR 54).
const hosts = new Hosts(conn, desk);
// One view per task. An open task's view is kept when another page is shown:
// its subscription stays and its events keep arriving, so coming back
// catches up at once instead of reading its whole log again, which took
// seconds for a long task. The least recently shown go past keptViews.
const views = new Map(); // host and task id (viewKey) -> TaskView, least recently shown first
const keptViews = 8;
let task = null; // the view on screen, when a task is
let route = parse();
let homeState = null;
let conversationView = null;

// parse reads the route from the address. A task of a linked host, its
// search and history, and its settings are under #/host/NAME/ (ADR 54);
// everything else is this computer's.
function parse() {
  const hash = location.hash.replace(/^#/, "");
  const [path, queryString = ""] = hash.split("?");
  const query = new URLSearchParams(queryString);
  let parts = path.split("/").filter(Boolean);
  let host = "";
  if (parts[0] === "host" && parts[1]) {
    host = decodeURIComponent(parts[1]);
    parts = parts.slice(2);
  }
  if (parts[0] === "search" && parts[1]) return { host, name: "search", id: decodeURIComponent(parts[1]), query: query.get("q") || "", scope: query.get("scope") || "project" };
  if (parts[0] === "history" && parts[1] && parts[2]) return { host, name: "history", id: decodeURIComponent(parts[1]), seq: Number(parts[2]), search: query.get("search") || "", query: query.get("q") || "", scope: query.get("scope") || "project" };
  if (parts[0] === "task" && parts[1]) return { host, name: "task", id: decodeURIComponent(parts[1]), review: parts[2] === "review" };
  if (parts[0] === "settings") return { host, name: "settings", tab: parts[1] };
  if (["workspaces", "connections", "workflows", "hosts"].includes(parts[0])) return { host: "", name: parts[0], tab: parts[1] };
  return { host: "", name: "home" };
}
// viewKey keys a task's view by its host and id: two hosts may each have a
// task of one id.
const viewKey = (host, id) => `${host}\n${id}`;
function navigate(hash) {
  if (location.hash === hash) render();
  else location.hash = hash;
}
window.addEventListener("hashchange", () => {
  const next = parse();
  conversationView?.leave();
  conversationView = null;
  if (route.name === "task" && (next.name !== "task" || next.id !== route.id || next.host !== route.host)) putAway(task);
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
// invited is an invitation that came in the address bar, for the pairing
// page; way is how the bridge said this browser can pair (passkey.js).
let invited = "";
let way = null;
async function showPairing(message = "") {
  way ||= await pairing();
  if (way.shared) return showInvitation(message);
  const input = h("input", { class: "input", id: "pair-code", type: "password", autocomplete: "off", spellcheck: false, required: true });
  const error = h("p", { class: "error", role: "alert" }, message);
  gate([
    h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
    h("h1", {}, "Connect this browser"),
    h("p", {}, "Paste the pairing code nodwyn printed where it was started. The code stays between this browser and your computer and is never added to a task."),
    h(
      "form",
      {
        onsubmit: async (event) => {
          event.preventDefault();
          const code = input.value;
          input.value = "";
          error.textContent = "";
          try {
            await pair({ code });
            conn.connect();
          } catch (e) {
            error.textContent = e.message || "Pairing failed. Copy the code again from where nodwyn is running.";
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: "pair-code" }, "Pairing code"), input),
      h("button", { type: "submit", class: "btn accent big" }, "Connect"),
    ),
    error,
  ]);
  input.focus();
}

// showInvitation pairs a browser with a console other machines reach (ADR
// 30, amended 2026-10-17): with an invitation, which over https also makes
// a passkey on this device, or with a passkey made on another.
function showInvitation(message) {
  const usePasskeys = way.passkey && passkeysHere();
  const invite = h("input", { class: "input", id: "pair-invite", autocomplete: "off", spellcheck: false, value: invited, placeholder: "xxxx-xxxx-xxxx-xxxx-xxxx" });
  // Browsers compile a pattern with the v flag, where a hyphen in a class
  // must be escaped: unescaped, the pattern is ignored.
  const device = h("input", { class: "input", id: "pair-device", autocomplete: "off", spellcheck: false, required: true, pattern: "[a-z0-9][a-z0-9._\\-]{0,31}", placeholder: "for example phone" });
  const error = h("p", { class: "error", role: "alert" }, message);
  const name = () => device.value.trim().toLowerCase();
  const attempt = async (work) => {
    error.textContent = "";
    if (!device.reportValidity()) return;
    try {
      await work();
      invited = "";
      conn.connect();
    } catch (e) {
      error.textContent = e.message || "Pairing failed.";
    }
  };
  // An invitation that may register a passkey does so here; a plain one
  // pairs a device that answers.
  const join = (passkey) =>
    attempt(async () => {
      if (!invite.value.trim()) throw new Error("Enter the invitation.");
      if (!passkey) return pair({ invite: invite.value.trim(), device: name() });
      try {
        await register(invite.value.trim(), name());
      } catch (e) {
        if (!/without a passkey/.test(e.message)) throw e;
        await pair({ invite: invite.value.trim(), device: name() });
      }
    });
  gate([
    h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
    h("h1", {}, "Connect this device"),
    h("p", {}, "Name this device: every answer you give from here is recorded with that name. Then enter the invitation that ", h("span", { class: "mono" }, "nodwyn devices invite"), " printed on nodwyn's computer, or that a device already paired made in Settings › Keys & devices."),
    h(
      "form",
      { onsubmit: (event) => (event.preventDefault(), join(usePasskeys)) },
      h("div", { class: "field" }, h("label", { for: "pair-device" }, "This device's name"), device, h("p", { class: "hint" }, "Lower-case letters, digits, dots or dashes.")),
      h("div", { class: "field" }, h("label", { for: "pair-invite" }, "Invitation"), invite, h("p", { class: "hint" }, usePasskeys ? "It makes a passkey on this device, which signs in your other devices too; the device can then change nodwyn's setup after a touch of it." : "It is good once, for ten minutes.")),
      h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn accent big" }, usePasskeys ? "Connect and make a passkey" : "Connect"), usePasskeys ? h("button", { type: "button", class: "btn ghost", onclick: () => join(false) }, "Connect without a passkey") : null),
    ),
    usePasskeys && way.passkeys
      ? h("div", { class: "stack-md" }, h("p", {}, "Made a passkey on another device already? Sign in with it, with no invitation."), h("button", { type: "button", class: "btn", id: "pair-passkey", onclick: () => attempt(() => assert(name())) }, "Sign in with a passkey"))
      : null,
    error,
  ]);
  device.focus();
}
function showDown(detail) {
  gate([
    h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
    h("h1", {}, "Connection lost"),
    h("p", {}, "nodwyn isn't answering. This page connects again by itself, and the conversation reloads from its record; nothing that was on its way is sent again."),
    detail ? h("p", { class: "error" }, detail) : null,
    h("p", { class: "retry-text", "aria-live": "off" }, retryText()),
    h("button", { type: "button", class: "btn accent big", onclick: () => conn.connect() }, "Reconnect now"),
  ]);
}
// retryText says what a connection that is not live is doing: waiting
// for its next try, or trying.
function retryText() {
  if (conn.state !== "down") return "Reconnecting…";
  const seconds = Math.max(1, Math.ceil((conn.retryAt - Date.now()) / 1000));
  return `Trying again in ${seconds} s.`;
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

// showTask puts the view of task id, on the host entry shows, on screen:
// the kept one as it is, or one that reads the conversation.
function showTask(entry, id, review = false) {
  const key = viewKey(entry.name, id);
  let view = views.get(key);
  views.delete(key);
  if (task && task !== view) putAway(task);
  if (view?.keeps(id)) {
    views.set(key, view);
    task = view;
    $("view").replaceChildren(view.root);
    view.show(entry.desk.sessions.get(id));
    if (review) view.openReview();
    else if (view.inspecting === "review" && view.activePrompt()) view.closeInspector();
  } else {
    view ||= new TaskView(entry.conn, entry.desk, navigate);
    views.set(key, view);
    task = view;
    $("view").replaceChildren(view.root);
    view.open(id).then(() => {
      // A navigation may finish after the reader has already left the task.
      if (review && task === view && route.name === "task" && route.id === id && route.host === entry.name && route.review && !view.failed) view.openReview();
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
// dropViews ends every view of a host whose connection is new, every host's
// when host is undefined: a new connection has none of their subscriptions,
// so there is nothing to unsubscribe.
function dropViews(host) {
  for (const [key, view] of views) {
    if (host !== undefined && view.desk.host !== host) continue;
    view.dispose(false);
    views.delete(key);
    if (task === view) task = null;
  }
  if (host === undefined || route.host === host) {
    conversationView?.leave();
    conversationView = null;
  }
}

let lastRoute = "";
// render draws the shell and the route. what is the desk change that asked
// for it ("" when something else did). A page that throws while it is drawn
// leaves the old one on screen with the sidebar already redrawn, so the
// failure is drawn instead, with a way out (see fault).
function render(routeChanged = false, what = "") {
  try {
    draw(routeChanged, what);
  } catch (error) {
    fault(error);
  }
}
function fault(error) {
  console.error(error);
  // The next render draws the route afresh instead of finding it done.
  lastRoute = "";
  $("view").replaceChildren(
    h(
      "main",
      { class: "page", id: "fault" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "This page could not be drawn"),
        h("p", { class: "lede" }, "Something on it was not as nodwyn expected. Nothing you started was changed."),
        h("p", { class: "error mono", role: "alert" }, String(error?.message || error)),
        h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn dark", onclick: () => location.reload() }, "Reload"), h("a", { class: "btn", href: "#/" }, "Home")),
      ),
    ),
  );
}
function draw(routeChanged, what) {
  if (conn.state !== "live") return;
  $("gate").hidden = true;
  $("shell").hidden = false;
  renderSidebar($("sidebar"), { desk, hosts, route, conn, rerender: () => render() });
  // What waits, on every host (ADR 54).
  const waiting = hosts.needsYou();
  const attention = waiting.length + hosts.outbox().length;
  $("topbar-count").textContent = attention ? `${attention} need${attention === 1 ? "s" : ""} you` : "";
  $("topbar-count").hidden = !attention;
  // On a phone the sidebar is a drawer, so this is the way to what waits.
  $("topbar-count").href = attention === 1 && waiting.length === 1 ? waiting[0].desk.href(waiting[0].item.id) : "#/";
  document.title = attention ? `(${attention}) nodwyn` : "nodwyn";
  const view = $("view");
  const key = ["task", "search", "history"].includes(route.name) ? `${route.name}:${route.host}:${route.id}` : route.name === "settings" ? `settings:${route.host}` : route.name;
  const ctx = { conn, desk, hosts, navigate, rerender: () => render(), pickFolder: window.nodwynPickFolder };
  // A task of a linked host, or its settings, is drawn once that host's
  // desk is read.
  const entry = hosts.get(route.host);
  if (route.host && ["task", "search", "history", "settings"].includes(route.name) && !entry?.conn.live) {
    if (task) putAway(task);
    conversationView?.leave();
    conversationView = null;
    view.replaceChildren(hostAway(route.host, entry, route.name === "settings" ? "settings" : "task"));
    lastRoute = "";
    return;
  }
  if (route.name === "task") {
    if (key !== lastRoute || routeChanged || !task) showTask(entry, route.id, route.review);
    else {
      // The list changed: keep the conversation, refresh what the task shows.
      const fresh = entry.desk.sessions.get(route.id);
      if (fresh && task.item && task.item.id === fresh.id) {
        Object.assign(task.item, { open: fresh.open, run: fresh.run, title: fresh.title, archived: fresh.archived, worktree: fresh.worktree, schedule: fresh.schedule });
        if (!fresh.open) task.subscribed = false;
        // A scheduled run can reopen a task while its closed transcript is
        // on screen. Its new stream needs a subscription and fixed snapshot.
        // A load that failed is tried again then too, and otherwise only by
        // its Retry: tried again at every change of the list, it went back
        // to "Loading the conversation…" and failed again over and over,
        // under the person's eyes, wherever other tasks were working.
        if (fresh.open && !task.subscribed && (task.failed ? !task.loadedOpen : !task.loading)) task.open(fresh.id);
        else task.refreshAll();
      }
    }
  } else if (route.name === "search" || route.name === "history") {
    if (key !== lastRoute || routeChanged || !conversationView) {
      conversationView?.leave();
      conversationView = route.name === "search" ? new ConversationSearchView(entry.conn, entry.desk, navigate, route) : new ConversationContextView(entry.conn, entry.desk, route);
      view.replaceChildren(conversationView.root);
      conversationView.open();
    }
  } else if (route.name === "home") {
    homeState = renderHome(view, { ...ctx, keepTop: key === lastRoute && !routeChanged });
  } else if (key !== lastRoute || routeChanged) {
    if (route.name === "settings") renderSettings(view, settingsCtx(ctx, entry));
    else if (route.name === "workspaces") renderWorkspaces(view, ctx);
    else if (route.name === "connections") renderConnections(view, { ...ctx, tab: route.tab });
    else if (route.name === "workflows") renderWorkflows(view, { ...ctx, tab: route.tab });
    else if (route.name === "hosts") renderHosts(view, ctx);
  } else if (route.name === "hosts" && !editing(view)) {
    // A host's state changes while the page is open.
    renderHosts(view, ctx);
  } else if (route.name === "workflows" && !route.tab) {
    // Its rows follow the list: a run starting, ending or skipped changes
    // what they say.
    renderWorkflows(view, ctx);
  } else if (route.name === "settings" && (!what || what === "setup" || what === "models")) {
    // Settings holds fields being typed into: a task's progress elsewhere
    // does not redraw it.
    renderSettings(view, settingsCtx(ctx, entry));
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

// settingsCtx is what Settings draws from: this computer's connection and
// desk, or a linked host's, whose settings are read and saved on that host
// (ADR 54, amended 2026-10-23).
function settingsCtx(ctx, entry) {
  if (!route.host) return { ...ctx, tab: route.tab };
  return { ...ctx, conn: entry.conn, desk: entry.desk, host: route.host, hostAddress: entry.info?.address || "", tab: route.tab };
}

// hostAway is what a linked host's task or settings show while the host
// cannot be read: not linked, connecting, or not reachable, and why.
function hostAway(name, entry, what = "task") {
  const [title, words] = !entry
    ? [`${name} is not linked`, "This computer has no host of that name. Link it on Hosts."]
    : entry.conn.state === "unpaired"
      ? [`${name} refuses this computer`, entry.detail || "Link it again on Hosts."]
      : entry.conn.state === "down"
        ? [`${name} is not reachable`, entry.info?.locked || entry.detail || `This page shows its ${what} once it answers.`]
        : [`Connecting to ${name}…`, `${what === "settings" ? "Its settings show" : "The task shows"} once it answers.`];
  return h("main", { class: "page", id: "host-away" }, h("div", { class: "page-col" }, h("h1", {}, title), h("p", { class: "lede" }, words), h("div", { class: "row-actions" }, h("a", { class: "btn", href: "#/hosts" }, "Hosts"), h("a", { class: "btn ghost", href: "#/" }, "Home"))));
}

// editing reports whether a field in root has the focus or holds what was
// typed into it, which a redraw would lose.
function editing(root) {
  if (root.contains(document.activeElement) && document.activeElement.matches("input, textarea, select")) return true;
  return [...root.querySelectorAll("input, textarea")].some((el) => (el.type === "checkbox" || el.type === "radio" ? el.checked !== el.defaultChecked : el.value !== el.defaultValue));
}

desk.onChange((what) => render(false, what));
hosts.onChange((what) => render(false, what));
// A throw outside a render (a click's handler, a request nobody waits for)
// would otherwise reach the console only; a connection that dropped says so
// on its own banner.
window.addEventListener("unhandledrejection", (event) => {
  if (conn.live) toastError(String(event.reason?.message || event.reason));
});
window.addEventListener("error", (event) => {
  if (event.message && !event.message.startsWith("ResizeObserver")) toastError(event.message);
});
// A notice opens its task, on its host (ADR 54).
const openTask = (id, host = "") => navigate((hosts.get(host)?.desk || desk).href(id));
desk.notifier.onOpen = openTask;
hosts.onOpen = openTask;
hosts.onLive = (name) => {
  dropViews(name);
  if (route.host === name) lastRoute = "";
};
// While the connection waits for its next try, the banner over the page (or
// the gate, before it was ever live) counts down the seconds. The count is
// not announced; the banner's reason is, when it changes.
let retryTick = 0;
function showRetry(detail = "") {
  if (conn.state === "down") setText($("offline-text"), detail ? detail.replace(/\.?$/, ".") : "Disconnected; nothing that was on its way is sent again.");
  for (const el of document.querySelectorAll(".retry-text")) setText(el, retryText());
}
// setText changes what el says only when it differs, so the banner's reason
// is not read out again every second.
function setText(el, text) {
  if (el.textContent !== text) el.textContent = text;
}
// firstVersion is the daemon's version at this page's first connection. A
// reconnect that finds another (nodwyn upgraded on an always-on computer,
// its console restarted with it) leaves the page running the code it loaded
// with, so the page says so and offers Reload. It never reloads by itself:
// the drafts are only in this tab's memory (ADR 30, amended 2026-10-17).
let firstVersion = null;
function showUpdated() {
  const version = conn.hello?.version || "";
  firstVersion ??= version;
  $("updated").hidden = version === firstVersion;
  setText($("updated-text"), `nodwyn was restarted as ${version}, and this page still runs the console it loaded before. Reload to run the new one; a reload loses text you have not sent.`);
}
// The service worker makes the console installable as an app and shows its
// notices (ADR 52); a click on one asks the page to open its task. It caches
// nothing, so a browser that refuses it loses only those.
if ("serviceWorker" in navigator && window.isSecureContext) {
  navigator.serviceWorker
    .register("/sw.js", { scope: "/" })
    .then(() => navigator.serviceWorker.ready)
    .then((registration) => {
      for (const entry of hosts.all()) entry.desk.notifier.worker = registration;
    })
    .catch((error) => console.warn("nodwyn: no service worker:", error.message));
  navigator.serviceWorker.addEventListener("message", (event) => {
    const id = event.data?.open;
    const host = typeof event.data?.host === "string" ? event.data.host : "";
    if (typeof id === "string" && id !== "") openTask(id, host);
  });
}
conn.onState(async (state, detail) => {
  // A page that was live keeps its banner while it reconnects.
  $("offline").hidden = state === "live" || $("shell").hidden;
  clearInterval(retryTick);
  if (state === "down") retryTick = setInterval(() => showRetry(detail), 1000);
  showRetry(detail);
  if (state === "unpaired") {
    desk.reset();
    // Every link goes through this computer's console, which no longer
    // takes this browser.
    hosts.drop();
    showPairing(detail);
  } else if (state === "down") {
    desk.reset();
    if ($("shell").hidden) showDown(detail);
  } else if (state === "live") {
    showUpdated();
    dropViews();
    lastRoute = "";
    delete document.documentElement.dataset.ready;
    try {
      await desk.start();
    } catch (error) {
      toastError(error.message);
    }
    // The hosts this computer is linked to, each read on its own.
    hosts.refresh().catch((error) => toastError(`Could not read the linked hosts: ${error.message}`));
    render(true);
    // The browser checks wait for this mark before they look at a page: the
    // desk's first refresh is drawn and the models have answered.
    await desk.modelsChecked;
    if (conn.state === "live") document.documentElement.dataset.ready = "";
  }
});
setInterval(() => {
  for (const entry of hosts.live()) entry.desk.refreshModels(false, true);
}, 60000);

installDropdowns();

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
  window.nodwynOpenExternal(link.href).catch((error) => toastError(String(error)));
});

$("menu").append(icon("menu", 20));
$("menu").addEventListener("click", openMenu);
$("reconnect").addEventListener("click", () => conn.connect());
$("reload").addEventListener("click", () => location.reload());
// A phone that wakes, or a laptop back on a network, reconnects at once
// rather than at its next try, and a connection that only looks live is
// found out (Connection.wake).
window.addEventListener("online", () => {
  conn.wake();
  hosts.wake();
});
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible") return;
  conn.wake();
  hosts.wake();
});

// Wails can install the native bridge after the first console render.
window.addEventListener("nodwyn-desktop-ready", () => render(true));

// A setup change from the person's device asks for a touch of the passkey.
conn.confirmTouch = confirmTouch;

// A launch ticket (from nodwyn app or nodwyn ui) pairs this browser without a
// typed code. It is used once, and removed from the address bar first. An
// invitation's link fills in the pairing page.
(async () => {
  invited = takeInvite();
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
