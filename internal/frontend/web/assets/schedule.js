// Schedule drafts are local UI state; only daemon log records enable a timer.
// The word for all of it is "Schedule", the verb and the noun; "Repeat" is
// only the field that says how often.
import { h, toast, removal } from "./dom.js";

const localMinute = () => {
  const d = new Date(Date.now() + 60000);
  return new Date(d - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
export const scheduleTime = (v) => v ? v.slice(0, 16).replace("T", " ") + " " + v.slice(19) : "";
const time = scheduleTime;

// scheduleEditor is the form of a schedule: what to ask on each run, how
// often, from when and the spend it may reach. A new schedule starts from
// draft, the message already typed, if any; save is given the definition.
export function scheduleEditor(conn, definition, save, { draft = "", submitLabel = "Save schedule" } = {}) {
  const d = definition || { message: draft, repeat: "daily", at: localMinute(), cost: 0, paused: false };
  const text = h("textarea", { id: "schedule-message", class: "input", rows: 3, required: true });
  text.value = d.message;
  const repeat = h("select", { id: "schedule-repeat", class: "input" }, [["once", "Once"], ["hourly", "Every hour"], ["daily", "Every day"], ["weekdays", "Weekdays"], ["weekly", "Every week"]].map(([value, label]) => h("option", { value, selected: value === d.repeat }, label)));
  const at = h("input", { id: "schedule-at", class: "input", type: "datetime-local", required: true, value: d.at });
  const cost = h("input", { id: "schedule-cost", class: "input", type: "number", min: 0, max: 1000000000, step: "any", required: true, value: d.cost });
  const preview = h("p", { class: "hint", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  const value = () => ({ message: text.value, repeat: repeat.value, at: at.value, cost: Number(cost.value), paused: !!d.paused });
  let revision = 0;
  const refresh = async () => {
    const version = ++revision;
    try {
      const next = await conn.call("schedule.next", { ...value(), message: value().message || "Scheduled request" });
      if (version === revision) preview.textContent = `${next.timezone}: ${next.next.map(time).join(" · ")}`;
    } catch (e) { if (version === revision) preview.textContent = e.message; }
  };
  const submit = h("button", { type: "submit", class: "btn dark" }, submitLabel);
  const form = h("form", { class: "schedule-form", onsubmit: async (event) => {
    event.preventDefault(); submit.disabled = true;
    try { await save(value()); } catch (e) { error.textContent = e.message; }
    finally { submit.disabled = false; }
  } },
  h("label", { for: "schedule-message" }, "Message for each run", text),
  h("label", { for: "schedule-repeat" }, "Repeat", repeat),
  h("label", { for: "schedule-at" }, "First time on this computer", at),
  h("label", { for: "schedule-cost" }, "Spend limit per run ($; 0 means none)", cost),
  h("p", { class: "hint" }, "The daemon must be running. Runs use normal approvals. Missed times run once, late; busy tasks skip their time."), preview, error, submit);
  form.addEventListener("input", refresh);
  refresh();
  return form;
}

function dialog() {
  const box = h("dialog", { class: "schedule-dialog", "aria-label": "Schedule" });
  box.append(h("h2", {}, "Schedule"), h("button", { type: "button", class: "btn ghost", onclick: () => box.close(), "aria-label": "Close schedule" }, "Close"));
  box.addEventListener("close", () => box.remove());
  document.body.append(box); box.showModal();
  return box;
}

// newSchedule asks for a schedule for a task that is not started: the message
// is a field here, whatever was typed in the composer before is its start.
export function newSchedule(conn, save, draft = "") {
  const box = dialog();
  box.append(scheduleEditor(conn, null, async (d) => { box.close(); await save(d); }, { draft, submitLabel: "Schedule" }));
}

// scheduleActions are the buttons that act on a task's schedule, shared by its
// dialog and by Home's list: pause or resume, and run now. update(method,
// params) sends it and draws whatever shows the schedule again.
export function scheduleActions(conn, d, archived, update) {
  const buttons = [];
  if (conn.has("schedule.pause") && (!d.paused || conn.has("schedule.set"))) buttons.push(h("button", { type: "button", class: "btn", onclick: () => update("schedule.pause", { paused: !d.paused }) }, d.paused ? "Resume schedule" : "Pause schedule"));
  if (conn.has("schedule.run")) buttons.push(h("button", { type: "button", class: "btn", disabled: archived, onclick: () => update("schedule.run") }, "Run now"));
  return buttons;
}

export async function manageSchedule(conn, id, done) {
  try {
    let state = await conn.call("schedule.read", { id });
    const box = dialog();
    const body = h("div", {}); box.append(body);
    let removing = false;
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
        body.append(h(
          "div",
          { class: "row-actions" },
          ...scheduleActions(conn, d, state.archived, update),
          conn.has("schedule.set") ? removal({ label: "Remove schedule", consequence: "Remove the schedule? The task and what it has done stay.", asking: removing, ask: () => ((removing = true), draw()), cancel: () => ((removing = false), draw()), run: () => ((removing = false), update("schedule.set", { remove: true })) }) : null,
        ));
      }
      if (conn.has("schedule.set")) body.append(scheduleEditor(conn, d, async (value) => {
        state = await conn.call("schedule.set", { id, ...value }); await done(); draw();
      }));
    }
    draw();
  } catch (e) { toast(e.message); }
}
