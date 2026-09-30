// Attaching files: the page keeps the chosen files until the message is
// sent, then uploads each to the bridge, which hands it to the daemon. The
// daemon extracts, redacts and records it; nothing here reads the content.
import { h, icon, sizeOf } from "./dom.js";

const MAX = 20 << 20;

export class Attachments {
  constructor(onChange) {
    this.files = [];
    this.onChange = onChange;
    this.input = h("input", { type: "file", multiple: true, hidden: true, onchange: () => this.add(this.input.files) });
    this.list = h("div", { class: "attach-list" });
  }
  button(label = "") {
    return h("button", { type: "button", class: label ? "btn" : "icon-btn", "aria-label": "Attach a file", title: "Attach a file", onclick: () => this.input.click() }, icon("clip", label ? 16 : 17), label || null, this.input);
  }
  add(list) {
    for (const file of list) {
      if (file.size > MAX) {
        this.error = `${file.name} is larger than 20 MB.`;
        continue;
      }
      if (!this.files.some((f) => f.name === file.name && f.size === file.size)) this.files.push(file);
    }
    this.input.value = "";
    this.draw();
  }
  clear() {
    this.files = [];
    this.error = "";
    this.draw();
  }
  draw() {
    this.list.replaceChildren(
      ...this.files.map((file, i) =>
        h(
          "span",
          { class: "attach-chip" },
          icon("file", 14),
          h("span", { class: "name" }, file.name),
          h("span", { class: "size" }, sizeOf(file.size)),
          h("button", { type: "button", class: "icon-btn", "aria-label": `Remove ${file.name}`, onclick: () => { this.files.splice(i, 1); this.draw(); } }, icon("cross", 12)),
        ),
      ),
      ...(this.error ? [h("span", { class: "error" }, this.error)] : []),
    );
    this.onChange?.();
  }
  // upload sends every file to a session; it stops at the first failure,
  // leaving the rest chosen, and never retries.
  async upload(sessionID) {
    const receipts = [];
    while (this.files.length) {
      const file = this.files[0];
      const response = await fetch(`/attach?id=${encodeURIComponent(sessionID)}&name=${encodeURIComponent(file.name)}`, {
        method: "POST",
        body: file,
        credentials: "same-origin",
        headers: { "Content-Type": "application/octet-stream" },
      });
      const text = await response.text();
      if (!response.ok) throw new Error(`${file.name}: ${text.trim() || response.statusText}`);
      receipts.push(JSON.parse(text));
      this.files.shift();
      this.draw();
    }
    return receipts;
  }
}

// chip describes a recorded attachment.
export function attachmentChip(a) {
  const bits = [a.type === "text" ? "text" : (a.type || "").toUpperCase()];
  if (a.pages) bits.push(`${a.pages} ${a.type === "xlsx" ? "sheet" : a.type === "pptx" ? "slide" : "page"}${a.pages === 1 ? "" : "s"}`);
  if (a.redactions?.length) bits.push(`${a.redactions.length} hidden`);
  return h("span", { class: "attach-chip done", title: a.stored || a.name }, icon("clip", 14), h("span", { class: "name" }, a.name), h("span", { class: "size" }, bits.join(" · ")));
}
