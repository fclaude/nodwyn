// A task's own limits (ADR 49, amended 2026-10-27): none unless the person
// sets one, on the task's panel or on the command line. Most tasks have
// none, so the panel says so in one line and offers to add one; each limit
// set is a chip to change or remove. The task view keeps the state (what is
// being edited, whether the menu is open); this module draws it and reads
// what was typed.
import { h, icon } from "./dom.js";

// The kinds of limit, in the order they are offered. key is the field of
// session.info's limits; per says what a limit counts over.
export const LIMIT_KINDS = [
  { key: "tool_calls", label: "Tool calls", per: "a turn", example: "200", hint: "The turn stops after this many tool calls." },
  { key: "actions", label: "Writes and commands", per: "a turn", example: "50", hint: "The turn stops after this many file writes and commands." },
  { key: "tokens", label: "Tokens", per: "a turn", example: "2000000", hint: "The turn stops once its model requests have used this many tokens (cached ones count a tenth)." },
  { key: "wall_ns", label: "Time", per: "a turn", example: "1h", hint: "The turn stops after this long, waits for your answers included: 45m, 1h30m, 2h." },
  { key: "cost", label: "Spend", per: "for the task", example: "5.00", hint: "The task stops once what its requests cost, as far as known, reaches this many US dollars." },
];

export function limitKind(key) {
  return LIMIT_KINDS.find((k) => k.key === key);
}

// limitValue writes a limit as the chip shows it.
export function limitValue(key, value) {
  if (key === "wall_ns") return limitDraft(key, value).replace(/([hm])(?=\d)/g, "$1 ");
  if (key === "cost") return `$${value.toFixed(2)}`;
  return Number(value).toLocaleString();
}

// limitsText is a task's limits in a line, as the conversation records
// them: "none", or each one set, such as "tool calls 40, time 1h a turn;
// spend $2.00 for the task", in the shape budget.Task's String has.
export function limitsText(limits) {
  const turn = LIMIT_KINDS.filter((k) => k.per === "a turn" && limits?.[k.key] > 0).map((k) => `${k.label.toLowerCase()} ${limitValue(k.key, limits[k.key])}`);
  const parts = turn.length ? [`${turn.join(", ")} a turn`] : [];
  if (limits?.cost > 0) parts.push(`spend ${limitValue("cost", limits.cost)} for the task`);
  return parts.join("; ") || "none";
}

// limitDraft is what the editor starts from for a limit already set: a
// time in hours, minutes and seconds, those that are not 0, as parseLimit
// reads it back.
function limitDraft(key, value) {
  if (key === "wall_ns") {
    const seconds = Math.round(value / 1e9);
    const [hours, mins, secs] = [Math.floor(seconds / 3600), Math.floor((seconds % 3600) / 60), seconds % 60];
    return `${hours ? `${hours}h` : ""}${mins ? `${mins}m` : ""}${secs || !seconds ? `${secs}s` : ""}`;
  }
  return String(value);
}

// parseLimit reads what was typed for a limit: a whole number of calls,
// actions or tokens (commas and spaces ignored), a duration (1h30m, 45m,
// 90s; a bare number is minutes) or dollars ($5, 2.50). It throws, saying
// what it takes, for anything else.
export function parseLimit(key, text) {
  const raw = String(text).trim().toLowerCase().replace(/[,\s_]/g, "");
  if (key === "wall_ns") {
    if (/^\d+(\.\d+)?$/.test(raw) && Number(raw) > 0) return Math.round(Number(raw) * 6e10);
    const parts = raw.match(/^(?:(\d+(?:\.\d+)?)h)?(?:(\d+(?:\.\d+)?)m)?(?:(\d+(?:\.\d+)?)s)?$/);
    const ns = parts ? ((Number(parts[1]) || 0) * 3600 + (Number(parts[2]) || 0) * 60 + (Number(parts[3]) || 0)) * 1e9 : 0;
    if (!raw || !ns) throw new Error("A time is a duration such as 45m, 1h30m or 2h.");
    return Math.round(ns);
  }
  if (key === "cost") {
    const usd = Number(raw.replace(/^\$/, ""));
    if (!raw || !Number.isFinite(usd) || usd < 0.000001) throw new Error("A spend limit is US dollars, such as 5 or 2.50.");
    return usd;
  }
  const n = Number(raw);
  if (!/^\d+$/.test(raw) || !Number.isSafeInteger(n) || n < 1) throw new Error(`${limitKind(key).label} takes a whole number, such as ${limitKind(key).example}.`);
  return n;
}

// limitsPanel draws the task's limits. state: limits (session.info's), edit
// ({key, draft} while one is typed), menu (the kinds offered, open), busy (a
// change on its way), locked (a run keeps its limits), device (a paired
// device, which may add a limit or lower one and nothing else). on: menu
// (open or shut the kinds to add), edit (start typing one), draft (what is
// typed so far), cancel, save and remove.
export function limitsPanel(state, on) {
  const limits = state.limits || {};
  const set = LIMIT_KINDS.filter((k) => limits[k.key] > 0);
  const unset = LIMIT_KINDS.filter((k) => !(limits[k.key] > 0) && k.key !== state.edit?.key);
  const disabled = state.busy || state.locked;
  const add = unset.length && !state.locked
    ? h(
        "button",
        {
          type: "button",
          class: "limit-add",
          dataset: { limitFocus: "add" },
          "aria-expanded": String(!!state.menu),
          "aria-controls": "limit-menu",
          disabled: state.busy,
          onclick: () => on.menu(!state.menu),
        },
        icon("plus", 13),
        set.length ? "Add" : "Add a limit",
      )
    : null;
  const head = h(
    "div",
    { class: "limits-head" },
    h("span", { class: "monitor-label" }, "Limits"),
    set.length || state.edit ? null : h("span", { class: "limits-none" }, "None: turns run as long as they need"),
    add,
  );
  const chips = set.length
    ? h(
        "div",
        { class: "limit-chips" },
        set.map((k) => {
          if (state.edit?.key === k.key) return null;
          const value = h("span", {}, `${k.label} ${limitValue(k.key, limits[k.key])}`, h("span", { class: "sub" }, ` ${k.per}`));
          return h(
            "span",
            { class: "chip limit-chip", dataset: { limit: k.key } },
            state.locked
              ? value
              : h("button", { type: "button", class: "limit-value", dataset: { limitFocus: `edit-${k.key}` }, disabled, title: `Change the ${k.label.toLowerCase()} limit`, onclick: () => on.edit(k.key, limitDraft(k.key, limits[k.key])) }, value),
            state.locked || state.device
              ? null
              : h("button", { type: "button", class: "icon-btn limit-remove", dataset: { limitFocus: `remove-${k.key}` }, disabled, "aria-label": `Remove the ${k.label.toLowerCase()} limit`, title: "Remove this limit", onclick: () => on.remove(k.key) }, icon("cross", 12)),
          );
        }),
      )
    : null;
  const menu = state.menu && !state.edit && unset.length
    ? h(
        "div",
        { class: "limit-menu", id: "limit-menu", role: "group", "aria-label": "Add a limit" },
        unset.map((k) => h("button", { type: "button", class: "chip dashed", dataset: { limitFocus: `add-${k.key}` }, title: k.hint, onclick: () => on.edit(k.key, "") }, `${k.label}`, h("span", { class: "sub" }, k.per))),
      )
    : null;
  let editor = null;
  if (state.edit) {
    const k = limitKind(state.edit.key);
    const input = h("input", {
      id: "limit-edit-input",
      class: "input",
      dataset: { limitFocus: "input" },
      inputmode: k.key === "wall_ns" ? "text" : "decimal",
      autocomplete: "off",
      placeholder: k.example,
      value: state.edit.draft,
      disabled: state.busy,
      "aria-describedby": "limit-edit-hint",
      oninput: (event) => on.draft(event.target.value),
      onkeydown: (event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          on.cancel();
        }
      },
    });
    editor = h(
      "form",
      { class: "limit-editor", onsubmit: (event) => { event.preventDefault(); on.save(k.key, input.value); } },
      h("label", { for: "limit-edit-input" }, `${k.label} `, h("span", { class: "sub" }, k.per)),
      h("div", { class: "limit-editor-row" }, input, h("button", { type: "submit", class: "btn small", disabled: state.busy }, "Set"), h("button", { type: "button", class: "btn small ghost", dataset: { limitFocus: "cancel" }, onclick: () => on.cancel() }, "Cancel")),
      h("p", { class: "hint", id: "limit-edit-hint" }, k.hint, state.device ? " From this device you can add a limit or lower one." : ""),
    );
  }
  const note = state.locked && set.length ? h("p", { class: "hint" }, "A run keeps these limits until it ends.") : null;
  return h("section", { class: `task-limits${set.length ? " has-limits" : ""}`, "aria-label": "Limits" }, head, chips, menu, editor, note);
}
