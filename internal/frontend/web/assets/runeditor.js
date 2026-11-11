// A run's two forms share one editor in the timetable and event builders.
// The textarea remains the editable, accessible source of truth; the layer
// beneath it only colours text nodes, so even unfinished or hostile Scheme
// cannot become markup or change the text that is saved.
import { h } from "./dom.js";

const keywords = new Set("define define-values define-syntax syntax-rules lambda let let* letrec let-values if cond case else begin set! quote quasiquote unquote unquote-splicing and or when unless do guard parameterize import export".split(" "));

export function schemeCode(source) {
  const nodes = [];
  const token = (text, kind) => nodes.push(kind ? h("span", { class: `scheme-${kind}` }, text) : text);
  const atom = /[^\s()[\]";'`,]+/y;
  let i = 0;
  while (i < source.length) {
    const start = i;
    const c = source[i];
    if (c === ";") {
      while (i < source.length && source[i] !== "\n") i++;
      token(source.slice(start, i), "comment");
    } else if (source.startsWith("#|", i)) {
      let depth = 1;
      i += 2;
      while (i < source.length && depth) {
        if (source.startsWith("#|", i)) { depth++; i += 2; }
        else if (source.startsWith("|#", i)) { depth--; i += 2; }
        else i++;
      }
      token(source.slice(start, i), "comment");
    } else if (c === '"') {
      i++;
      while (i < source.length) {
        const next = source[i++];
        if (next === "\\" && i < source.length) i++;
        else if (next === '"') break;
      }
      token(source.slice(start, i), "string");
    } else if (source.startsWith("#\\", i)) {
      i += 2;
      if (i < source.length) i++;
      while (i < source.length && !/[\s()[\]]/.test(source[i])) i++;
      token(source.slice(start, i), "literal");
    } else {
      atom.lastIndex = i;
      const match = atom.exec(source);
      if (match) {
        const word = match[0];
        i = atom.lastIndex;
        token(word, keywords.has(word) ? "keyword" : /^(?:#t|#f|#true|#false|[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?|[+-]?\d+\/\d+)$/.test(word) ? "literal" : "");
      } else {
        i++;
        token(c, /[()[\]'`,]/.test(c) ? "paren" : "");
      }
    }
  }
  return h("code", {}, nodes);
}

export function runEditor(definition, { prefix, changed = () => {}, draft = {} }) {
  if (!draft.kind) Object.assign(draft, { kind: definition.program ? "scheme" : "message", message: definition.message || "", program: definition.program || "" });
  const d = draft;
  const kind = h("select", { id: `${prefix}-run-kind`, class: "input", "aria-label": "Run type" },
    h("option", { value: "message", selected: d.kind === "message" }, "Send a message"),
    h("option", { value: "scheme", selected: d.kind === "scheme" }, "Run Scheme"));
  const message = h("textarea", { id: `${prefix}-message`, class: "input", rows: 4, oninput: () => { d.message = message.value; changed(value()); } });
  message.value = d.message;
  const paint = h("pre", { class: "scheme-highlight", "aria-hidden": "true" });
  const colour = () => paint.replaceChildren(schemeCode(program.value + "\n"));
  const program = h("textarea", {
    id: `${prefix}-program`, class: "scheme-input", rows: 9, spellcheck: false, autocapitalize: "off", autocomplete: "off", wrap: "off",
    oninput: () => { d.program = program.value; colour(); changed(value()); },
    onscroll: () => { paint.scrollTop = program.scrollTop; paint.scrollLeft = program.scrollLeft; },
    onkeydown: (event) => {
      if (event.key !== "Tab" || event.shiftKey || event.ctrlKey || event.metaKey || event.altKey) return;
      event.preventDefault();
      program.setRangeText("  ", program.selectionStart, program.selectionEnd, "end");
      program.dispatchEvent(new Event("input", { bubbles: true }));
    },
  });
  program.value = d.program;
  colour();
  const messageField = h("label", { class: "builder-field", for: message.id }, "Message for each run", message);
  const programField = h("div", { class: "builder-field" },
    h("label", { for: program.id }, "Scheme program"), h("div", { class: "scheme-editor" }, paint, program),
    h("p", { class: "hint" }, "Runs in the task's harness with its tools, approvals and limits. Output and the returned value appear in the task. Use add-message and model-turn when you want to ask a model."));
  const value = () => kind.value === "scheme" ? { program: program.value } : { message: message.value };
  const show = () => {
    const scheme = kind.value === "scheme";
    messageField.hidden = scheme;
    message.required = !scheme;
    message.disabled = scheme;
    programField.hidden = !scheme;
    program.required = scheme;
    program.disabled = !scheme;
  };
  kind.addEventListener("change", () => { d.kind = kind.value; show(); changed(value()); });
  show();
  return { node: h("div", { class: "run-editor" }, h("label", { class: "builder-field", for: kind.id }, "Run type", kind), messageField, programField), value };
}
