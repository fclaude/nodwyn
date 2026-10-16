// Schedule drafts are local UI state; only daemon log records enable a timer.
// The word for all of it is "Schedule", the verb and the noun; "Repeat" is
// only the field that says how often.
import { h, toast, toastError, removal } from "./dom.js";

const localMinute = () => {
  const d = new Date(Date.now() + 60000);
  return new Date(d - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
export const scheduleTime = (v) => v ? v.slice(0, 16).replace("T", " ") + " " + v.slice(19) : "";
const time = scheduleTime;

const then = { hourly: "then every hour", daily: "then every day", weekdays: "then on weekdays", weekly: "then every week" };
// nextWords says when a schedule next runs in a person's words, on the
// schedule's own wall clock: "tomorrow 9:30 AM, then every day". The daemon
// sends each time with its offset and no zone name a browser would know, so
// the clock and the offset are read from the string itself. The full list of
// times is for the title.
export function nextWords(next, repeat) {
  const m = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d)[^Z+-]*(Z|[+-]\d\d:\d\d)$/.exec(next?.[0] || "");
  if (!m) return next?.length ? time(next[0]) : "";
  const offset = m[6] === "Z" ? 0 : (m[6][0] === "-" ? -1 : 1) * (Number(m[6].slice(1, 3)) * 60 + Number(m[6].slice(4, 6)));
  // The wall clock read as UTC, so that formatting in UTC prints it as it is.
  const wall = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
  const format = (options) => new Intl.DateTimeFormat(undefined, { timeZone: "UTC", ...options }).format(wall);
  const today = Math.floor((Date.now() + offset * 60000) / 86400000);
  const that = Math.floor(wall / 86400000);
  const label = that === today ? "today" : that === today + 1 ? "tomorrow" : format({ weekday: "short", month: "short", day: "numeric" });
  return `${label} ${format({ hour: "numeric", minute: "2-digit" })}${then[repeat] ? `, ${then[repeat]}` : ""}`;
}

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
      if (version === revision) {
        preview.textContent = `Next: ${nextWords(next.next, repeat.value)}`;
        preview.title = `${next.timezone}: ${next.next.map(time).join(" · ")}`;
      }
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
  box.append(h("div", { class: "dialog-head" }, h("h2", {}, "Schedule"), h("button", { type: "button", class: "btn ghost", onclick: () => box.close(), "aria-label": "Close schedule" }, "Close")));
  box.addEventListener("close", () => box.remove());
  document.body.append(box); box.showModal();
  return box;
}

// newSchedule asks for a schedule for a task that is not started: the message
// is a field here, whatever was typed in the composer before is its start.
export function newSchedule(conn, draft, save) {
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
    const body = h("div", { class: "stack-md" }); box.append(body);
    // Removal asks first, as a task's deletion does; the edit form is folded
    // away until it is wanted, so the dialog holds what a person came to see.
    let removing = false;
    let editing = false;
    const update = async (method, params = {}) => {
      try { state = await conn.call(method, { id, ...params }); await done(); draw(); }
      catch (e) { toastError(e.message); }
    };
    function draw() {
      const d = state.definition;
      body.replaceChildren(h("p", { class: "schedule-status" }, d ? `${d.paused ? "Paused" : state.archived ? "Archived" : "Scheduled"} · ${d.repeat} · ${state.timezone}` : "No schedule"));
      if (d) {
        body.append(h("p", {}, d.message), h("p", { class: "hint", title: state.next.length ? `${state.timezone}: ${state.next.map(time).join(" · ")}` : "" }, state.next.length ? `Next: ${nextWords(state.next, d.repeat)}` : "No automatic time pending."));
        if (!state.scheduler) body.append(h("p", { class: "hint" }, "This daemon does not hold the scheduler lock."));
        if (state.last_skip) body.append(h("p", { class: "hint" }, `Skipped ${time(state.last_skip.due)}: ${state.last_skip.reason}`));
        body.append(h(
          "div",
          { class: "row-actions" },
          ...scheduleActions(conn, d, state.archived, update),
          conn.has("schedule.set") ? removal({ label: "Remove schedule", consequence: "Remove the schedule? The task and what it has done stay.", asking: removing, ask: () => ((removing = true), draw()), cancel: () => ((removing = false), draw()), run: () => ((removing = false), update("schedule.set", { remove: true })) }) : null,
        ));
      }
      if (conn.has("schedule.set")) {
        const form = scheduleEditor(conn, d, async (value) => {
          state = await conn.call("schedule.set", { id, ...value }); await done(); draw();
        });
        if (d) body.append(h("details", { class: "schedule-edit", open: editing, ontoggle: (event) => (editing = event.target.open) }, h("summary", {}, "Edit"), form));
        else body.append(form);
      }
    }
    draw();
  } catch (e) { toastError(e.message); }
}
