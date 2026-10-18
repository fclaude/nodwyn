// Schedule drafts are local UI state; only daemon log records enable a timer.
// The word for all of it is "Schedule", the verb and the noun; "Repeat" is
// only the field that says how often. The daemon says when in words (the
// rule's sentence, each next time, what came of each recent run), so this
// page prints what it is sent and works out no times of its own.
import { h, toastError, removal } from "./dom.js";
import { usd } from "./transcript.js";

export const scheduleTime = (v) => v ? v.slice(0, 16).replace("T", " ") + " " + v.slice(19) : "";

const week = [["mon", "Mon"], ["tue", "Tue"], ["wed", "Wed"], ["thu", "Thu"], ["fri", "Fri"], ["sat", "Sat"], ["sun", "Sun"]];
const ordinals = [["1", "first"], ["2", "second"], ["3", "third"], ["4", "fourth"], ["5", "fifth"], ["-1", "last"]];

// nextLine says when a schedule runs next, from the daemon's words; every
// next time it sent is in the title.
export function nextLine(state) {
  const words = state.next_words || [];
  if (!words.length) return "No automatic time pending.";
  return `Next: ${words[0]}${words.length > 1 ? `, then ${words[1]}` : ""}`;
}

// kindOf is which form of the timetable a rule is written in: once, every so
// often, at set times on days of the week, or monthly.
function kindOf(rule) {
  if (rule.once) return "once";
  if (rule.every) return "every";
  if (rule.dates?.length || rule.nth) return "monthly";
  return "times";
}

// timetable is the form of a rule. A new one runs every day at 09:00 from
// now (the daemon fills in now); an existing one is shown as the daemon's
// normal form has it, so saving it unchanged keeps the schedule's progress.
function timetable(rule, changed) {
  const r = rule || { at: ["09:00"] };
  const kind = h("select", { id: "schedule-repeat", class: "input" }, [["times", "At set times"], ["every", "Every few minutes or hours"], ["monthly", "Monthly"], ["once", "Once"]].map(([value, label]) => h("option", { value, selected: value === kindOf(r) }, label)));
  // Times of day, for set times and monthly.
  const times = h("div", { class: "schedule-times" });
  const addTime = (value) => {
    const input = h("input", { class: "input schedule-time", type: "time", required: true, value, "aria-label": "Time of day" });
    const row = h("span", { class: "schedule-time-row" }, input, h("button", { type: "button", class: "icon-btn", "aria-label": "Remove this time", onclick: () => { if (times.children.length > 1) { row.remove(); changed(); } } }, "×"));
    times.append(row);
  };
  for (const at of r.at?.length ? r.at : ["09:00"]) addTime(at);
  const more = h("button", { type: "button", class: "btn small ghost", onclick: () => { addTime("12:00"); changed(); } }, "Add a time");
  // Days of the week: none picked is every day.
  const picked = new Set(r.nth ? [] : r.days || []);
  const days = h("div", { class: "schedule-days", role: "group", "aria-label": "Days of the week" }, week.map(([day, label]) => h("button", {
    type: "button", class: "chip", dataset: { day }, "aria-pressed": String(picked.has(day)),
    onclick: (event) => { picked.has(day) ? picked.delete(day) : picked.add(day); event.currentTarget.setAttribute("aria-pressed", String(picked.has(day))); changed(); },
  }, label)));
  const daysHint = h("span", { class: "hint" }, "None picked: every day.");
  // Every so often, in minutes or hours, between two times or all day.
  const hours = r.every >= 60 && r.every % 60 === 0;
  const every = h("input", { id: "schedule-every", class: "input", type: "number", min: 1, step: 1, value: r.every ? (hours ? r.every / 60 : r.every) : 30, "aria-label": "How often" });
  const unit = h("select", { id: "schedule-every-unit", class: "input", "aria-label": "Unit" }, h("option", { value: "minutes", selected: !hours }, "minutes"), h("option", { value: "hours", selected: hours }, "hours"));
  const windowed = h("input", { id: "schedule-window", type: "checkbox", checked: !!r.between?.length });
  const between = [h("input", { id: "schedule-between-from", class: "input", type: "time", value: r.between?.[0] || "09:00", "aria-label": "From" }), h("input", { id: "schedule-between-to", class: "input", type: "time", value: r.between?.[1] || "18:00", "aria-label": "To" })];
  // Monthly: on dates, or on the nth weekday.
  const monthly = h("select", { id: "schedule-month", class: "input", "aria-label": "Monthly on" }, h("option", { value: "dates", selected: !r.nth }, "On dates"), h("option", { value: "nth", selected: !!r.nth }, "On a weekday"));
  const dates = h("input", { id: "schedule-dates", class: "input", inputmode: "numeric", placeholder: "1, 15", value: (r.dates || [1]).join(", "), "aria-label": "Dates of the month" });
  const nth = h("select", { id: "schedule-nth", class: "input", "aria-label": "Which" }, ordinals.map(([value, label]) => h("option", { value, selected: Number(value) === (r.nth || 1) }, label)));
  const nthDay = h("select", { id: "schedule-nth-day", class: "input", "aria-label": "Weekday" }, week.map(([day]) => h("option", { value: day, selected: day === (r.nth ? r.days[0] : "mon") }, { mon: "Monday", tue: "Tuesday", wed: "Wednesday", thu: "Thursday", fri: "Friday", sat: "Saturday", sun: "Sunday" }[day])));
  const once = h("input", { id: "schedule-once", class: "input", type: "datetime-local", value: r.once || "" });
  const from = h("input", { id: "schedule-from", class: "input", type: "datetime-local", value: r.from || "" });
  const until = h("input", { id: "schedule-until", class: "input", type: "date", value: r.until || "" });

  const sections = {
    every: h("div", { class: "schedule-row" }, h("span", {}, "Every"), every, unit),
    window: h("div", { class: "schedule-row" }, h("label", { class: "check", for: "schedule-window" }, windowed, "Only between"), between[0], h("span", {}, "and"), between[1]),
    monthly: h("div", { class: "schedule-row" }, monthly, dates, nth, nthDay),
    times: h("div", { class: "schedule-field" }, h("span", {}, "Times of day"), times, more),
    days: h("div", { class: "schedule-field" }, h("span", {}, "Days"), days, daysHint),
    once: h("label", { for: "schedule-once" }, "When", once),
    bounds: h("div", { class: "schedule-row schedule-bounds" }, h("label", { for: "schedule-from" }, "Starts (empty: now)", from), h("label", { for: "schedule-until" }, "Last day (optional)", until)),
  };
  const show = () => {
    const k = kind.value;
    sections.every.hidden = sections.window.hidden = k !== "every";
    sections.monthly.hidden = k !== "monthly";
    dates.hidden = monthly.value !== "dates";
    nth.hidden = nthDay.hidden = monthly.value !== "nth";
    sections.times.hidden = k !== "times" && k !== "monthly";
    sections.days.hidden = k !== "times" && k !== "every";
    sections.once.hidden = k !== "once";
    sections.bounds.hidden = k === "once";
    for (const input of between) input.disabled = !windowed.checked;
  };
  kind.addEventListener("change", show);
  monthly.addEventListener("change", show);
  windowed.addEventListener("change", show);
  show();
  // value is the rule as the daemon takes it; what it leaves out (the first
  // minute) the daemon fills in.
  const value = () => {
    const k = kind.value;
    if (k === "once") return { once: once.value };
    const out = {};
    if (k === "every") {
      out.every = Number(every.value) * (unit.value === "hours" ? 60 : 1);
      if (windowed.checked) out.between = between.map((input) => input.value);
    } else {
      out.at = [...times.querySelectorAll("input")].map((input) => input.value).filter(Boolean);
    }
    if (k === "monthly") {
      if (monthly.value === "nth") Object.assign(out, { nth: Number(nth.value), days: [nthDay.value] });
      else out.dates = dates.value.split(/[\s,]+/).filter(Boolean).map(Number);
    } else if (picked.size) {
      out.days = week.map(([day]) => day).filter((day) => picked.has(day));
    }
    if (from.value) out.from = from.value;
    if (until.value) out.until = until.value;
    return out;
  };
  return { node: h("fieldset", { class: "schedule-timetable" }, h("legend", {}, "Repeat"), kind, ...Object.values(sections)), value };
}

// scheduleEditor is the form of a schedule: what to ask on each run, when,
// and the spend it may reach. A new schedule starts from draft, the message
// already typed, if any; save is given the definition.
export function scheduleEditor(conn, state, save, { draft = "", submitLabel = "Save schedule" } = {}) {
  const d = state?.definition || { message: draft, cost: 0, paused: false };
  const text = h("textarea", { id: "schedule-message", class: "input", rows: 3, required: true });
  text.value = d.message;
  const cost = h("input", { id: "schedule-cost", class: "input", type: "number", min: 0, max: 1000000000, step: "any", required: true, value: d.cost });
  const dayCost = h("input", { id: "schedule-day-cost", class: "input", type: "number", min: 0, max: 1000000000, step: "any", value: d.day_cost || 0 });
  const preview = h("div", { class: "schedule-preview", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  let revision = 0;
  const refresh = async () => {
    const version = ++revision;
    try {
      const next = await conn.call("schedule.next", { ...value(), message: value().message || "Scheduled request" });
      if (version === revision) {
        preview.replaceChildren(h("p", { class: "schedule-sentence" }, next.sentence), h("p", { class: "hint", title: `${next.timezone}: ${next.next_words.join(" · ")}` }, nextLine(next)));
        error.textContent = "";
      }
    } catch (e) { if (version === revision) preview.replaceChildren(h("p", { class: "hint" }, e.message)); }
  };
  const rule = timetable(state?.rule, () => refresh());
  const value = () => ({ message: text.value, rule: rule.value(), cost: Number(cost.value), day_cost: Number(dayCost.value) || 0, paused: !!d.paused });
  const submit = h("button", { type: "submit", class: "btn dark" }, submitLabel);
  const form = h("form", { class: "schedule-form", onsubmit: async (event) => {
    event.preventDefault(); submit.disabled = true;
    try { await save(value()); } catch (e) { error.textContent = e.message; }
    finally { submit.disabled = false; }
  } },
  h("label", { for: "schedule-message" }, "Message for each run", text),
  rule.node,
  h("div", { class: "schedule-row schedule-bounds" }, h("label", { for: "schedule-cost" }, "Spend limit per run ($; 0 means none)", cost), h("label", { for: "schedule-day-cost" }, "Spend limit per day ($; 0 means none)", dayCost)),
  h("p", { class: "hint" }, "Times are this computer's, and the daemon must be running. Runs use normal approvals. Missed times run once, late; busy tasks skip their time."), preview, error, submit);
  form.addEventListener("input", refresh);
  form.addEventListener("change", refresh);
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

// history lists what came of the schedule's recent times, newest first: the
// latest five, and the rest folded beneath them.
function history(state) {
  const item = (e) => h("li", {}, h("span", { class: "schedule-when" }, e.when), h("span", {}, e.what), e.cost ? h("span", { class: "schedule-cost" }, usd(e.cost)) : null);
  const items = state.history || [];
  if (!items.length) return h("p", { class: "hint" }, "No run yet.");
  const list = h("ol", { class: "schedule-history", "aria-label": "Recent runs" }, items.slice(0, 5).map(item));
  if (items.length <= 5) return list;
  return h("div", {}, list, h("details", {}, h("summary", {}, `${items.length - 5} earlier`), h("ol", { class: "schedule-history" }, items.slice(5).map(item))));
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
      body.replaceChildren(h("p", { class: "schedule-status" }, d ? `${d.paused ? "Paused" : state.archived ? "Archived" : "Scheduled"} · ${state.timezone}` : "No schedule"));
      if (d) {
        body.append(h("p", { class: "schedule-sentence" }, state.sentence), h("p", {}, d.message), h("p", { class: "hint", title: state.next_words.length ? `${state.timezone}: ${state.next_words.join(" · ")}` : "" }, nextLine(state)));
        if (state.today?.runs || d.day_cost) body.append(h("p", { class: "hint", id: "schedule-today" }, state.today?.runs ? `Today: ${state.today.runs} run${state.today.runs === 1 ? "" : "s"} · ${usd(state.today.cost || 0)}${d.day_cost ? ` of ${usd(d.day_cost)} a day` : ""}` : `Up to ${usd(d.day_cost)} a day`));
        if (!state.scheduler) body.append(h("p", { class: "hint" }, "This daemon does not hold the scheduler lock."));
        body.append(h(
          "div",
          { class: "row-actions" },
          ...scheduleActions(conn, d, state.archived, update),
          conn.has("schedule.set") ? removal({ label: "Remove schedule", consequence: "Remove the schedule? The task and what it has done stay.", asking: removing, ask: () => ((removing = true), draw()), cancel: () => ((removing = false), draw()), run: () => ((removing = false), update("schedule.set", { remove: true })) }) : null,
        ), h("h3", { class: "eyebrow" }, "Recent runs"), history(state));
      }
      if (conn.has("schedule.set")) {
        const form = scheduleEditor(conn, d ? state : null, async (value) => {
          state = await conn.call("schedule.set", { id, ...value }); await done(); draw();
        });
        if (d) body.append(h("details", { class: "schedule-edit", open: editing, ontoggle: (event) => (editing = event.target.open) }, h("summary", {}, "Edit"), form));
        else body.append(form);
      }
    }
    draw();
  } catch (e) { toastError(e.message); }
}
