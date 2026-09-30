// The sidebar: new task, then what needs you, what is running and what is
// recent, then the places that are not tasks.
import { h, icon, repoName, ago, minutes } from "./dom.js";
import { summary } from "./prompts.js";

let filter = "";
let searching = false;

export function renderSidebar(nav, { desk, route, conn, rerender }) {
  const current = route.name === "task" ? route.id : "";
  const matches = (item) => !filter || `${item.title} ${item.repo}`.toLowerCase().includes(filter.toLowerCase());
  const row = (item, sub, dot) =>
    h(
      "a",
      { class: `nav-task ${dot ? "dotted" : "plain"}`, href: `#/task/${item.id}`, "aria-current": item.id === current ? "page" : null, title: item.title || item.id },
      h("span", { class: "title" }, dot ? h("span", { class: `dot ${dot}` }) : null, h("span", {}, item.title || "Untitled task")),
      h("span", { class: "sub" }, sub),
    );
  const group = (label, items, count) => (items.length ? h("div", { class: "nav-group" }, h("div", { class: "eyebrow" }, label, count ? h("span", { class: "count" }, String(items.length)) : null), items) : null);

  const needs = desk.needsYou().filter(matches).map((item) => {
    const first = desk.pending.get(item.id)?.[0];
    return row(item, `${first ? summary(first).title : "Waiting for your answer"} · ${desk.folderName(item.repo)}`, "warn");
  });
  const running = desk.running().filter(matches).map((item) => {
    const run = item.run?.status === "running" ? item.run : null;
    const left = run ? new Date(run.deadline).getTime() - Date.now() : 0;
    return row(item, run ? `Turn ${run.turns} of ${run.max_turns} · ${left > 0 ? minutes(left) + " left" : "time up"}` : `Working · ${desk.folderName(item.repo)}`, "run");
  });
  const recent = desk.recent().filter(matches).slice(0, filter ? 30 : 8).map((item) => row(item, `${desk.folderName(item.repo)} · ${ago(item.last_event)}`, ""));

  const search = h("input", {
    class: "input",
    type: "search",
    placeholder: "Search tasks",
    "aria-label": "Search tasks",
    value: filter,
    oninput: () => {
      filter = search.value;
      rerender();
      nav.querySelector("input[type=search]")?.focus();
    },
  });
  const control = desk.control();
  const modelState = !conn.live ? ["bad", "offline"] : !control ? ["", "checking"] : control.state === "ready" ? ["ok", desk.running().length ? "working" : "ready"] : control.state === "missing" ? ["warn", "model missing"] : control.state === "unconfigured" ? ["warn", "not set up"] : ["bad", "unreachable"];
  const link = (href, name, label) => h("a", { class: "nav-link", href, "aria-current": route.name === href.slice(2) ? "page" : null }, icon(name), h("span", {}, label));

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
    group("Needs you", needs, true),
    group("Running", running),
    group(filter ? "Matches" : "Recent", recent),
    !needs.length && !running.length && !recent.length ? h("p", { class: "hint", style: "padding: 0 10px" }, filter ? "No task matches." : "No tasks yet.") : null,
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
}
