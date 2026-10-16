// Schedule drafts are local UI state; only daemon log records enable a timer.
import { h, toast } from "./dom.js";

const localMinute = () => {
  const d = new Date(Date.now() + 60000);
  return new Date(d - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
export const scheduleTime = (v) => v ? v.slice(0, 16).replace("T", " ") + " " + v.slice(19) : "";
const time = scheduleTime;

export function scheduleEditor(conn, definition, save, message = true) {
  const d = definition || { message: "", repeat: "daily", at: localMinute(), cost: 0, paused: false };
  const text = h("textarea", { id: "schedule-message", class: "input", rows: 3, required: message });
  text.value = d.message;
  const repeat = h("select", { id: "schedule-repeat", class: "input" }, [["once", "Once"], ["hourly", "Every hour"], ["daily", "Every day"], ["weekdays", "Weekdays"], ["weekly", "Every week"]].map(([value, label]) => h("option", { value, selected: value === d.repeat }, label)));
  const at = h("input", { id: "schedule-at", class: "input", type: "datetime-local", required: true, value: d.at });
  const cost = h("input", { id: "schedule-cost", class: "input", type: "number", min: 0, max: 1000000000, step: "any", required: true, value: d.cost });
  const preview = h("p", { class: "hint", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  const value = () => ({ message: message ? text.value : d.message || "Scheduled request", repeat: repeat.value, at: at.value, cost: Number(cost.value), paused: !!d.paused });
  let revision = 0;
  const refresh = async () => {
    const version = ++revision;
    try {
      const next = await conn.call("schedule.next", { ...value(), message: value().message || "Scheduled request" });
      if (version === revision) preview.textContent = `${next.timezone}: ${next.next.map(time).join(" · ")}`;
    } catch (e) { if (version === revision) preview.textContent = e.message; }
  };
  const submit = h("button", { type: "submit", class: "btn dark" }, "Save schedule");
  const form = h("form", { class: "schedule-form", onsubmit: async (event) => {
    event.preventDefault(); submit.disabled = true;
    try { await save(value()); } catch (e) { error.textContent = e.message; }
    finally { submit.disabled = false; }
  } },
  message ? h("label", { for: "schedule-message" }, "Message for each run", text) : null,
  h("label", { for: "schedule-repeat" }, "Repeat", repeat),
  h("label", { for: "schedule-at" }, "First time on this computer", at),
  h("label", { for: "schedule-cost" }, "Spend limit per run ($; 0 means none)", cost),
  h("p", { class: "hint" }, "The daemon must be running. Runs use normal approvals. Missed times run once, late; busy tasks skip their time."), preview, error, submit);
  form.addEventListener("input", refresh);
  refresh();
  return form;
}

function dialog(title) {
  const box = h("dialog", { class: "schedule-dialog", "aria-label": title });
  box.append(h("h2", {}, title), h("button", { type: "button", class: "btn ghost", onclick: () => box.close(), "aria-label": "Close schedule" }, "Close"));
  box.addEventListener("close", () => box.remove());
  document.body.append(box); box.showModal();
  return box;
}

export function newSchedule(conn, save) {
  const box = dialog("Schedule this task");
  box.append(scheduleEditor(conn, null, async (d) => { box.close(); await save(d); }, false));
}

export async function manageSchedule(conn, id, done) {
  try {
    let state = await conn.call("schedule.read", { id });
    const box = dialog("Task schedule");
    const body = h("div", {}); box.append(body);
    const update = async (method, params = {}) => {
      try { state = await conn.call(method, { id, ...params }); await done(); draw(); }
      catch (e) { toast(e.message); }
    };
    function draw() {
      const d = state.definition;
      body.replaceChildren(h("p", { class: "schedule-status" }, d ? `${d.paused ? "Paused" : state.archived ? "Archived" : "Scheduled"} · ${d.repeat} · ${state.timezone}` : "No schedule"));
      if (d) {
        body.append(h("p", {}, d.message), h("p", { class: "hint" }, state.next.length ? `Next: ${state.next.map(time).join(" · ")}` : "No automatic time pending."));
        if (!state.scheduler) body.append(h("p", { class: "hint" }, "This daemon does not hold the scheduler lock."));
        if (state.last_skip) body.append(h("p", { class: "hint" }, `Skipped ${time(state.last_skip.due)}: ${state.last_skip.reason}`));
        const actions = h("div", { class: "row-actions" });
        if (conn.has("schedule.pause") && (!d.paused || conn.has("schedule.set"))) actions.append(h("button", { type: "button", class: "btn", onclick: () => update("schedule.pause", { paused: !d.paused }) }, d.paused ? "Resume schedule" : "Pause schedule"));
        if (conn.has("schedule.run")) actions.append(h("button", { type: "button", class: "btn", disabled: state.archived, onclick: () => update("schedule.run") }, "Run now"));
        if (conn.has("schedule.set")) actions.append(h("button", { type: "button", class: "btn ghost", onclick: () => update("schedule.set", { remove: true }) }, "Remove schedule"));
        body.append(actions);
      }
      if (conn.has("schedule.set")) body.append(scheduleEditor(conn, d, async (value) => {
        state = await conn.call("schedule.set", { id, ...value }); await done(); draw();
      }));
    }
    draw();
  } catch (e) { toast(e.message); }
}
