// The sidebar: new task, then what needs you, then every other task under
// its folder, the folder with the latest activity first, then the tasks a
// person archived (ADR 39), then the places that are not tasks. With a
// linked host (ADR 54), what needs you is every host's, and each host's
// folders are under its name and state.
import { h, icon, ago, minutes } from "./dom.js";
import { summary } from "./prompts.js";
import { stateWords } from "./modelpicker.js";
import { searchURL } from "./search.js";
import { chosenFolder } from "./home.js";
import { hostState } from "./hosts.js";

let filter = "";
let searching = false;
// The hosts whose archived tasks are shown ("" is this computer's).
const showArchived = new Set();
// Folders showing all their tasks, this tab only.
const expanded = new Set();
// Folders and hosts folded away, remembered by this browser.
const folded = new Set(readFolded());

// perFolder is how many tasks a folder shows before "Show more".
const perFolder = 5;

function readFolded() {
  try {
    const list = JSON.parse(localStorage.getItem("nodwyn-folded") || "[]");
    return Array.isArray(list) ? list : [];
  } catch {
    return [];
  }
}
function saveFolded() {
  try {
    localStorage.setItem("nodwyn-folded", JSON.stringify([...folded]));
  } catch {
    /* this browser keeps nothing: folders unfold on the next load */
  }
}

// byFolder groups tasks, given newest activity first, by folder; a folder
// comes where its newest task does. Two folders with the same name are told
// apart by the folder each is in.
function byFolder(desk, items) {
  const groups = new Map();
  for (const item of items) {
    // A task in a worktree is listed under its repository (ADR 34).
    const folder = desk.project(item);
    if (!groups.has(folder)) groups.set(folder, []);
    groups.get(folder).push(item);
  }
  const seen = new Map();
  for (const repo of groups.keys()) {
    const name = desk.folderName(repo);
    seen.set(name, (seen.get(name) || 0) + 1);
  }
  return [...groups].map(([repo, tasks]) => {
    const name = desk.folderName(repo);
    const parent = String(repo || "").replace(/\/+$/, "").split("/").slice(-2, -1)[0];
    return { repo, name: seen.get(name) > 1 && parent ? `${parent}/${name}` : name, tasks };
  });
}

export function renderSidebar(nav, { desk, hosts, route, conn, rerender }) {
  const current = ["task", "search", "history"].includes(route.name) ? route.id : "";
  const currentHost = route.host || "";
  const several = !!hosts?.several;
  const matches = (item) => !filter || `${item.title} ${item.repo} ${item.worktree?.branch || ""}`.toLowerCase().includes(filter.toLowerCase());
  // row is a task of the host d shows, linked on that host.
  const row = (d, item, sub, dot) =>
    h(
      "a",
      { class: `nav-task ${dot ? "dotted" : "plain"}`, href: d.href(item.id), "aria-current": item.id === current && d.host === currentHost ? "page" : null, title: item.title || item.id },
      h("span", { class: "title" }, dot ? h("span", { class: `dot ${dot}` }) : null, h("span", {}, item.title || "Untitled task")),
      h("span", { class: "sub" }, sub),
    );
  const group = (label, items, count) => (items.length ? h("div", { class: "nav-group" }, h("div", { class: "eyebrow" }, label, count ? h("span", { class: "count" }, String(items.length)) : null), items) : null);
  // A folder's heading folds it; a search shows every match whatever is folded.
  const heading = (name, repo, n, open, toggle) =>
    h("button", { type: "button", class: "folder-head", "aria-expanded": String(open), title: repo || name, disabled: !toggle, onclick: toggle || undefined }, icon(open ? "down" : "right", 14), h("span", { class: "name" }, name), h("span", { class: "n" }, String(n)));

  // What needs you is every host's, each saying its host when there are
  // several.
  const waiting = hosts ? hosts.needsYou() : desk.needsYou().map((item) => ({ item, desk }));
  const needs = waiting.filter(({ item }) => matches(item)).map(({ item, desk: d }) => {
    const first = d.pending.get(item.id)?.[0];
    return row(d, item, `${first ? summary(first).title : "Waiting for your answer"} · ${several ? `${d.host || "This computer"} · ` : ""}${d.folderName(d.project(item))}`, "warn");
  });
  const taskRow = (d, item) => {
    const running = item.open && (item.busy || item.run?.status === "running");
    if (!running) return row(d, item, ago(item.last_event), "");
    const run = item.run?.status === "running" ? item.run : null;
    const left = run ? new Date(run.deadline).getTime() - Date.now() : 0;
    return row(d, item, run ? `${run.unattended ? "Unattended · " : ""}Turn ${run.turns} of ${run.max_turns} · ${left > 0 ? minutes(left) + " left" : "time up"}` : "Working", "run");
  };
  // foldersOf is the folders of the host d shows, each with its tasks. A
  // linked host's folder is folded by its host and path, this computer's by
  // its path, as it always was.
  const foldersOf = (d) => {
    const others = d.all().filter((s) => !s.archived && !(s.open && s.needs_answer) && matches(s));
    return byFolder(d, others).map(({ repo, name, tasks }) => {
      const key = d.host ? `${d.host}\n${repo}` : repo;
      const open = !!filter || !folded.has(key);
      const toggle = () => {
        if (folded.has(key)) folded.delete(key);
        else folded.add(key);
        saveFolded();
        rerender();
      };
      // The task on screen is never hidden behind "Show more".
      const all = !!filter || expanded.has(key) || (d.host === currentHost && tasks.slice(perFolder).some((t) => t.id === current));
      const shown = all ? tasks : tasks.slice(0, perFolder);
      return h(
        "div",
        { class: "nav-group folder" },
        heading(name, repo, tasks.length, open, filter ? null : toggle),
        open ? shown.map((item) => taskRow(d, item)) : null,
        open && shown.length < tasks.length ? h("button", { type: "button", class: "nav-more", onclick: () => { expanded.add(key); rerender(); } }, `Show ${tasks.length - shown.length} more`) : null,
      );
    });
  };
  const archiveOf = (d) => {
    const archived = d.archived().filter(matches);
    if (!archived.length) return null;
    const archiveOpen = showArchived.has(d.host) || !!filter || (d.host === currentHost && archived.some((t) => t.id === current));
    return h(
      "div",
      { class: "nav-group folder archived" },
      heading("Archived", "", archived.length, archiveOpen, filter ? null : () => { if (archiveOpen) showArchived.delete(d.host); else showArchived.add(d.host); rerender(); }),
      archiveOpen ? archived.map((item) => row(d, item, `${d.folderName(d.project(item))} · ${ago(item.last_event)}`, "")) : null,
    );
  };
  // hostSection is a host's heading, with its state, and its folders and
  // archived tasks while it can be read. The heading folds them away, as a
  // folder's does, and its state stays in view; a search shows every match.
  const hostSection = (entry) => {
    const [dot, words, detail] = hostState(entry);
    const live = entry.conn.live;
    // A host's key starts with a tab, which no folder's key (a host's name,
    // a newline and a path, or a path) does.
    const key = `\t${entry.name}`;
    const open = !!filter || !folded.has(key);
    const toggle = () => {
      if (folded.has(key)) folded.delete(key);
      else folded.add(key);
      saveFolded();
      rerender();
    };
    const folders = live && open ? foldersOf(entry.desk) : [];
    return h(
      "section",
      { class: "nav-host", "aria-label": entry.label, dataset: { host: entry.name } },
      h(
        "button",
        { type: "button", class: "host-head", "aria-expanded": String(open), title: detail, disabled: !!filter, onclick: toggle },
        icon(open ? "down" : "right", 14),
        icon(entry.name ? "server" : "laptop", 15),
        h("span", { class: "name" }, entry.label),
        h("span", { class: "state" }, h("span", { class: `dot ${dot}` }), words),
      ),
      live ? (folders.length ? h("div", { class: "folders" }, folders) : null) : h("p", { class: "hint host-hint" }, detail || (entry.conn.state === "down" ? "Its tasks show again when it answers." : "Connecting…")),
      live && open ? archiveOf(entry.desk) : null,
    );
  };
  const folders = several ? [] : foldersOf(desk);
  const archive = several ? null : archiveOf(desk);
  const sections = several ? hosts.all().map(hostSection) : [];

  // What was said in the tasks is searched from the same box, anchored on
  // the task on screen, else the newest task in the folder Home starts in,
  // else the newest: the daemon takes the project from the task it is given,
  // on the host the task is on.
  const folder = chosenFolder();
  const all = desk.all().filter((s) => !s.archived);
  const onScreen = current ? hosts?.get(currentHost)?.desk || (currentHost ? null : desk) : null;
  const anchorDesk = onScreen?.sessions.get(current) ? onScreen : desk;
  const anchor = onScreen?.sessions.get(current) || all.find((s) => desk.project(s) === folder) || all[0] || desk.all()[0];
  const searchWords = () => (anchor && filter.trim() ? searchURL(anchor.id, filter.trim(), "project", anchorDesk) : "");
  const search = h("input", {
    class: "input",
    type: "search",
    placeholder: "Search tasks and conversations",
    "aria-label": "Search tasks and conversations",
    value: filter,
    oninput: () => {
      filter = search.value;
      rerender();
    },
    onkeydown: (event) => {
      if (event.key === "Enter" && searchWords()) {
        event.preventDefault();
        location.hash = searchWords();
        return;
      }
      if (event.key !== "Escape") return;
      searching = false;
      filter = "";
      rerender();
      nav.querySelector(".sidebar-top .icon-btn")?.focus();
    },
  });
  // The sidebar is drawn again on every keystroke and every change on the
  // desk. A search being typed keeps its focus and caret through that, or
  // each key would land before the last, and the page drawn after the
  // sidebar would take the focus for its own field.
  const was = searching && document.activeElement?.matches?.("#sidebar input[type=search]") ? document.activeElement : null;
  const typing = was && { start: was.selectionStart, end: was.selectionEnd };
  const control = desk.control();
  const modelState = !conn.live ? ["bad", "offline"] : !control ? ["", "checking"] : control.state === "ready" ? ["ok", desk.running().length ? "working" : "ready"] : [control.state === "unreachable" ? "bad" : "warn", stateWords[control.state] || control.state];
  const link = (href, name, label) => h("a", { class: "nav-link", href, "aria-current": route.name === href.slice(2) ? "page" : null }, icon(name), h("span", {}, label));

  // Only the task list scrolls: the places and the model's state stay where
  // they are, and the list keeps its place through each redraw.
  const kept = nav.querySelector(".nav-tasks")?.scrollTop || 0;
  const tasks = h(
    "div",
    { class: "nav-tasks" },
    searchWords() ? h("a", { class: "nav-task search-row", href: searchWords() }, h("span", { class: "title" }, icon("search", 14), h("span", {}, `Search conversations for “${filter.trim()}”`)), h("span", { class: "sub" }, "What was said in this folder's tasks")) : null,
    group("Needs you", needs, true),
    folders.length ? h("div", { class: "folders" }, folders) : null,
    archive,
    sections,
    !several && !needs.length && !folders.length && !archive ? h("p", { class: "hint", style: "padding: 0 10px" }, filter ? "No task title matches." : "No tasks yet.") : null,
  );
  nav.replaceChildren(
    ...[
    h(
      "div",
      { class: "sidebar-top" },
      h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
      h("button", { type: "button", class: "icon-btn", "aria-label": "Search", title: "Search tasks and conversations", "aria-expanded": String(searching), onclick: () => { searching = !searching; if (!searching) filter = ""; rerender(); if (searching) nav.querySelector("input[type=search]")?.focus(); } }, icon("search")),
    ),
    searching ? h("div", { class: "search" }, search) : null,
    h("a", { class: "new-task", href: "#/", onclick: () => setTimeout(() => document.getElementById("task")?.focus(), 0) }, icon("plus"), h("span", {}, "New task"), h("kbd", {}, "⌘N")),
    tasks,
    h("div", { class: "nav-links" }, link("#/connections", "plug", "Connections"), link("#/workspaces", "folder", "Workspaces"), link("#/workflows", "clock", "Workflows"), conn.has("host.list") ? link("#/hosts", "hosts", "Hosts") : null, link("#/settings", "sliders", "Settings")),
    h(
      "div",
      { class: "model-line", title: control?.detail || "" },
      h("span", { class: `dot ${modelState[0]}` }),
      h("span", {}, control?.model ? `${control.model}${control.host ? ` · ${control.host}` : ""}` : conn.live ? "Model" : "Disconnected"),
      h("span", { class: "state" }, modelState[1]),
    ),
    ].filter(Boolean),
  );
  tasks.scrollTop = kept;
  if (typing) {
    search.focus();
    search.setSelectionRange(typing.start, typing.end);
  }
}
