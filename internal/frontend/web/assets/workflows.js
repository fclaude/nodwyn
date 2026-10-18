// The Workflows page (ADR 53): every schedule, each a workflow whose runs go
// to one task, with when it runs, what today's runs came to and how the
// latest ended, in the daemon's words. A new one starts on Home, in its
// Schedule dialog, with every choice a task has (folder, worktree, model,
// limits): there is one way to start a task, and a workflow is one.
import { h, toastError } from "./dom.js";
import { nextLine, scheduleActions, manageSchedule } from "./schedule.js";
import { usd } from "./transcript.js";

// newWorkflow says Home is to open its Schedule dialog once it is drawn,
// which New workflow asks for on its way there.
let newWorkflow = false;
export function takeNewWorkflow() {
  const asked = newWorkflow;
  newWorkflow = false;
  return asked;
}

// todayLine says what today's runs came to, and the day's limit if there is one.
function todayLine(s) {
  const d = s.definition;
  const limit = d.day_cost ? ` of ${usd(d.day_cost)} a day` : "";
  if (!s.today?.runs) return d.day_cost ? `No run today · up to ${usd(d.day_cost)} a day` : "No run today";
  return `Today: ${s.today.runs} run${s.today.runs === 1 ? "" : "s"}${s.today.cost || limit ? ` · ${usd(s.today.cost || 0)}${limit}` : ""}`;
}

export function renderWorkflows(root, ctx) {
  const { conn, desk, navigate } = ctx;
  const redraw = () => renderWorkflows(root, ctx);
  const items = [...desk.sessions.values()]
    .filter((item) => item.schedule?.definition)
    .sort((a, b) => Number(!!a.archived) - Number(!!b.archived) || (a.title || a.first_message || "").localeCompare(b.title || b.first_message || ""));
  const update = (item) => async (method, params = {}) => {
    try {
      await conn.call(method, { id: item.id, ...params });
      await desk.reload();
    } catch (error) {
      toastError(error.message);
    }
    redraw();
  };
  const row = (item) => {
    const s = item.schedule;
    const d = s.definition;
    const last = s.history?.[0];
    return h(
      "article",
      { class: "workflow-row", dataset: { id: item.id } },
      h(
        "div",
        { class: "workflow-main" },
        h("a", { class: "workflow-name", href: `#/task/${item.id}` }, item.title || item.first_message || "Scheduled task"),
        h("p", { class: "workflow-when" }, s.sentence || d.repeat),
        h("p", { class: s.held ? "hint warn" : "hint", title: s.timezone }, item.archived ? "Archived, so paused" : d.paused ? "Paused" : s.held || nextLine(s)),
        h("p", { class: "hint workflow-today" }, todayLine(s), last ? ` · Last: ${last.what}` : ""),
      ),
      h(
        "div",
        { class: "row-actions" },
        ...scheduleActions(conn, d, item.archived, update(item)),
        h("button", { type: "button", class: "btn", onclick: () => manageSchedule(conn, item.id, async () => { await desk.reload(); redraw(); }) }, "Schedule…"),
      ),
    );
  };
  const start = conn.has("schedule.set")
    ? h("button", { type: "button", class: "btn dark", id: "new-workflow", onclick: () => { newWorkflow = true; navigate("#/"); } }, "New workflow")
    : null;
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("div", { class: "page-head" }, h("h1", {}, "Workflows"), start),
        h("p", { class: "lede" }, "A workflow sends its task a message when its time comes, as you would, and each run goes into that task. Here is when each runs next, what today's runs came to and how the latest ended."),
        items.length
          ? h("div", { class: "workflow-list", "aria-label": "Workflows" }, items.map(row))
          : h("p", { class: "empty-note" }, "No workflows yet. New workflow starts one from Home, with its task's folder, model and limits."),
      ),
    ),
  );
}
