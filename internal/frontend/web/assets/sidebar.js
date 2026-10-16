// The sidebar: new task, then what needs you, then every other task under
// its folder, the folder with the latest activity first, then the tasks a
// person archived (ADR 39), then the places that are not tasks.
import { h, icon, ago, minutes } from "./dom.js";
import { summary } from "./prompts.js";
import { stateWords } from "./modelpicker.js";

let filter = "";
let searching = false;
let showArchived = false;
// Folders showing all their tasks, this tab only.
const expanded = new Set();
// Folders folded away, remembered by this browser.
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

export function renderSidebar(nav, { desk, route, conn, rerender }) {
  const current = ["task", "search", "history"].includes(route.name) ? route.id : "";
  const matches = (item) => !filter || `${item.title} ${item.repo} ${item.worktree?.branch || ""}`.toLowerCase().includes(filter.toLowerCase());
  const row = (item, sub, dot) =>
    h(
      "a",
      { class: `nav-task ${dot ? "dotted" : "plain"}`, href: `#/task/${item.id}`, "aria-current": item.id === current ? "page" : null, title: item.title || item.id },
      h("span", { class: "title" }, dot ? h("span", { class: `dot ${dot}` }) : null, h("span", {}, item.title || "Untitled task")),
      h("span", { class: "sub" }, sub),
    );
  const group = (label, items, count) => (items.length ? h("div", { class: "nav-group" }, h("div", { class: "eyebrow" }, label, count ? h("span", { class: "count" }, String(items.length)) : null), items) : null);
  // A folder's heading folds it; a search shows every match whatever is folded.
  const heading = (name, repo, n, open, toggle) =>
    h("button", { type: "button", class: "folder-head", "aria-expanded": String(open), title: repo || name, disabled: !toggle, onclick: toggle || undefined }, icon(open ? "down" : "right", 14), h("span", { class: "name" }, name), h("span", { class: "n" }, String(n)));

  const needs = desk.needsYou().filter(matches).map((item) => {
    const first = desk.pending.get(item.id)?.[0];
    return row(item, `${first ? summary(first).title : "Waiting for your answer"} · ${desk.folderName(desk.project(item))}`, "warn");
  });
  const taskRow = (item) => {
    const running = item.open && (item.busy || item.run?.status === "running");
    if (!running) return row(item, ago(item.last_event), "");
    const run = item.run?.status === "running" ? item.run : null;
    const left = run ? new Date(run.deadline).getTime() - Date.now() : 0;
    return row(item, run ? `${run.unattended ? "Unattended · " : ""}Turn ${run.turns} of ${run.max_turns} · ${left > 0 ? minutes(left) + " left" : "time up"}` : "Working", "run");
  };
  const others = desk.all().filter((s) => !s.archived && !(s.open && s.needs_answer) && matches(s));
  const folders = byFolder(desk, others).map(({ repo, name, tasks }) => {
    const open = !!filter || !folded.has(repo);
    const toggle = () => {
      if (folded.has(repo)) folded.delete(repo);
      else folded.add(repo);
      saveFolded();
      rerender();
    };
    // The task on screen is never hidden behind "Show more".
    const all = !!filter || expanded.has(repo) || tasks.slice(perFolder).some((t) => t.id === current);
    const shown = all ? tasks : tasks.slice(0, perFolder);
    return h(
      "div",
      { class: "nav-group folder" },
      heading(name, repo, tasks.length, open, filter ? null : toggle),
      open ? shown.map(taskRow) : null,
      open && shown.length < tasks.length ? h("button", { type: "button", class: "nav-more", onclick: () => { expanded.add(repo); rerender(); } }, `Show ${tasks.length - shown.length} more`) : null,
    );
  });
  const archived = desk.archived().filter(matches);
  const archiveOpen = showArchived || (!!filter && archived.length > 0) || archived.some((t) => t.id === current);
  const archive = archived.length
    ? h(
        "div",
        { class: "nav-group folder archived" },
        heading("Archived", "", archived.length, archiveOpen, filter ? null : () => { showArchived = !archiveOpen; rerender(); }),
        archiveOpen ? archived.map((item) => row(item, `${desk.folderName(desk.project(item))} · ${ago(item.last_event)}`, "")) : null,
      )
    : null;

  const search = h("input", {
    class: "input",
    type: "search",
    placeholder: "Search tasks",
    "aria-label": "Search tasks",
    value: filter,
    oninput: () => {
      filter = search.value;
      rerender();
    },
    onkeydown: (event) => {
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
    group("Needs you", needs, true),
    folders.length ? h("div", { class: "folders" }, folders) : null,
    archive,
    !needs.length && !folders.length && !archived.length ? h("p", { class: "hint", style: "padding: 0 10px" }, filter ? "No task matches." : "No tasks yet.") : null,
  );
  nav.replaceChildren(
    ...[
    h(
      "div",
      { class: "sidebar-top" },
      h("div", { class: "brand" }, "nodwyn", h("span", {}, ".")),
      h("button", { type: "button", class: "icon-btn", "aria-label": "Search tasks", "aria-expanded": String(searching), onclick: () => { searching = !searching; if (!searching) filter = ""; rerender(); if (searching) nav.querySelector("input[type=search]")?.focus(); } }, icon("search")),
    ),
    searching ? h("div", { class: "search" }, search) : null,
    h("a", { class: "new-task", href: "#/", onclick: () => setTimeout(() => document.getElementById("task")?.focus(), 0) }, icon("plus"), h("span", {}, "New task"), h("kbd", {}, "⌘N")),
    tasks,
    h("div", { class: "nav-links" }, link("#/connections", "plug", "Connections"), link("#/workspaces", "folder", "Workspaces"), link("#/settings", "sliders", "Settings")),
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
