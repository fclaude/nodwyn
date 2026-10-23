// Styled dropdowns for every <select>. The browser draws a select's popup
// itself, and in the app window (WebKitGTK, WKWebView) that popup is the
// system's: white, whatever the page's colours, and out of reach of CSS. So
// the popup is drawn here, from the select's own options, and the select
// stays the source of truth: its value, its disabled state and its input and
// change events are the browser's, which is what every page already reads and
// what a test's selectOption sets. Nothing builds or registers a select for
// this: the listeners are on the document, so a select a page draws later is
// covered too.

import { h, icon } from "./dom.js";

let current = null; // { select, pop, rows, active, typed, typedAt, watch }

// A phone or tablet keeps the system's picker, which is better there than
// any list drawn by a page.
const finePointer = () => matchMedia("(pointer: fine)").matches;
const plain = (select) => select instanceof HTMLSelectElement && !select.multiple && select.size <= 1 && !select.disabled;

export function installDropdowns() {
  document.addEventListener("mousedown", onMouseDown, true);
  document.addEventListener("keydown", onKeyDown, true);
  window.addEventListener("resize", () => current && place());
  window.addEventListener("blur", () => close(false));
  // A page that scrolls under an open list moves it with the select.
  document.addEventListener("scroll", (event) => current && !current.pop.contains(event.target) && place(), true);
}

function onMouseDown(event) {
  if (current && current.pop.contains(event.target)) {
    // Keep the focus on the select while a row is pressed.
    event.preventDefault();
    return;
  }
  const select = event.target.closest?.("select");
  if (current && select !== current.select) close(false);
  if (event.button !== 0 || !plain(select) || !finePointer()) return;
  event.preventDefault();
  select.focus();
  if (current) close(false);
  else open(select);
}

function onKeyDown(event) {
  if (current) return openKey(event);
  const select = event.target;
  if (!plain(select) || event.ctrlKey || event.metaKey) return;
  const opens = event.key === " " || event.key === "F4" || (event.altKey && (event.key === "ArrowDown" || event.key === "ArrowUp"));
  if (!opens) return;
  event.preventDefault();
  open(select);
}

// openKey handles a key while the list is open: it is all the list's.
function openKey(event) {
  const c = current;
  const move = (to) => (event.preventDefault(), event.stopPropagation(), activate(to));
  switch (event.key) {
    case "ArrowDown": return move(step(c.active, 1));
    case "ArrowUp": return move(step(c.active, -1));
    case "Home": return move(step(-1, 1));
    case "End": return move(step(c.rows.length, -1));
    case "Enter":
    case " ":
      event.preventDefault();
      event.stopPropagation();
      if (c.typed && event.key === " ") return typeAhead(" ");
      return choose(c.rows[c.active]);
    case "Escape":
      event.preventDefault();
      event.stopPropagation();
      return close(true);
    case "Tab":
      return close(false);
  }
  if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
    event.preventDefault();
    event.stopPropagation();
    typeAhead(event.key);
  }
}

function open(select) {
  const rows = [];
  const pop = h("div", { class: "dd-pop", role: "listbox", "aria-label": select.getAttribute("aria-label") || null });
  const add = (option, disabled) => {
    const mark = h("span", { class: "dd-mark" }, option.selected ? icon("check", 13, 2) : null);
    const row = h(
      "div",
      { class: "dd-option", role: "option", id: `dd-option-${rows.length}`, "aria-selected": String(option.selected), "aria-disabled": disabled ? "true" : null },
      mark,
      h("span", { class: "dd-label" }, option.label || "\u00a0"),
    );
    row.addEventListener("click", () => choose(rows.find((r) => r.row === row)));
    row.addEventListener("mouseover", () => !disabled && activate(rows.findIndex((r) => r.row === row)));
    rows.push({ row, option, disabled });
    return row;
  };
  for (const child of select.children) {
    if (child.hidden) continue;
    if (child instanceof HTMLOptGroupElement) {
      pop.append(h("div", { class: "dd-group", role: "presentation" }, child.label));
      for (const option of child.children) if (!option.hidden) pop.append(add(option, option.disabled || child.disabled));
    } else if (child instanceof HTMLOptionElement) pop.append(add(child, child.disabled));
  }
  if (!rows.length) return;
  // Inside an open dialog, which the page's own layers cannot rise above.
  (select.closest("dialog[open]") || document.body).append(pop);
  // The list goes when a redraw takes its select out of the page.
  const watch = new MutationObserver(() => select.isConnected || close(false));
  watch.observe(document.body, { childList: true, subtree: true });
  current = { select, pop, rows, active: -1, typed: "", typedAt: 0, watch };
  select.setAttribute("aria-expanded", "true");
  activate(Math.max(0, rows.findIndex((r) => r.option.selected)));
  place();
}

// place puts the list under the select, or over it when there is more room
// there, and closes it when the select has scrolled out of sight.
function place() {
  const { select, pop } = current;
  const box = select.getBoundingClientRect();
  if (box.bottom < 0 || box.top > innerHeight) return close(false);
  const margin = 8;
  pop.style.minWidth = `${box.width}px`;
  pop.style.maxWidth = `${innerWidth - 2 * margin}px`;
  pop.style.maxHeight = "";
  const below = innerHeight - box.bottom - margin;
  const above = box.top - margin;
  const wanted = Math.min(pop.scrollHeight + 2, 320);
  const up = below < wanted && above > below;
  const room = Math.min(up ? above : below, 320);
  pop.style.maxHeight = `${Math.max(room, 80)}px`;
  pop.style.left = `${Math.max(margin, Math.min(box.left, innerWidth - pop.offsetWidth - margin))}px`;
  pop.style.top = up ? `${Math.max(margin, box.top - 4 - pop.offsetHeight)}px` : `${box.bottom + 4}px`;
}

const step = (from, by) => {
  for (let i = from + by; i >= 0 && i < current.rows.length; i += by) if (!current.rows[i].disabled) return i;
  return from;
};

function activate(index) {
  const c = current;
  if (index < 0 || index >= c.rows.length || c.rows[index].disabled) return;
  c.rows[c.active]?.row.classList.remove("active");
  c.active = index;
  const { row } = c.rows[index];
  row.classList.add("active");
  c.select.setAttribute("aria-activedescendant", row.id);
  row.scrollIntoView({ block: "nearest" });
}

// typeAhead moves to the next row whose label starts with what was typed in
// the last moment, as a native list does.
function typeAhead(key) {
  const c = current;
  const now = Date.now();
  c.typed = now - c.typedAt > 700 ? key : c.typed + key;
  c.typedAt = now;
  const word = c.typed.toLowerCase();
  const from = c.typed.length === 1 ? c.active + 1 : c.active;
  for (let i = 0; i < c.rows.length; i++) {
    const at = (from + i) % c.rows.length;
    const { option, disabled } = c.rows[at];
    if (!disabled && option.label.trim().toLowerCase().startsWith(word)) return activate(at);
  }
}

function choose(entry) {
  if (!entry || entry.disabled) return;
  const { select } = current;
  const changed = !entry.option.selected;
  close(true);
  if (!changed) return;
  select.selectedIndex = entry.option.index;
  select.dispatchEvent(new Event("input", { bubbles: true }));
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

function close(refocus) {
  if (!current) return;
  const { select, pop, watch } = current;
  current = null;
  watch.disconnect();
  pop.remove();
  select.removeAttribute("aria-expanded");
  select.removeAttribute("aria-activedescendant");
  if (refocus && select.isConnected) select.focus();
}
