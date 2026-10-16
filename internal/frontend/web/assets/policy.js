// The editor holds only a selected session's draft. Authority comes from the
// daemon's guarded operation and subsequent prompt, never these controls.
import { h } from "./dom.js";

const button = (label, action, cls = "") => h("button", { type: "button", class: `btn small ${cls}`.trim(), onclick: action }, label);

export function policyChoices(container, read) {
  container.append(
    h("h3", {}, "Edit policy files"),
    h("p", {}, "Each save requires a separate diff confirmation. Files contain permission scopes, never credential values."),
    h("div", { class: "row-actions" }, button("Edit shared bindings", () => read("bindings")), button("Edit repository approvals", () => read("approvals"))),
  );
}

export function renderPolicyEditor(container, state, read, save, close) {
  const again = () => renderPolicyEditor(container, state, read, save, close);
  const parts = [
    h("h3", {}, state.name === "bindings" ? "Shared credential bindings" : "Repository standing approvals"),
    h(
      "p",
      {},
      state.name === "bindings"
        ? "Persistent shared policy. Sessions reload bindings before egress."
        : "Persistent repository policy. New or resumed sessions load this file; existing sessions retain their current approvals.",
    ),
  ];
  if (state.error) parts.push(h("p", { class: "warning", role: "alert" }, state.error));
  if (state.saved) parts.push(h("p", {}, "Policy saved. Reload the file before another edit."));
  if (state.doc && !state.saved) {
    parts.push(h("p", {}, state.doc.Exists ? "Editing an existing file." : "This save will create the policy file."));
    const input = h("textarea", { id: "policy-content", rows: 15, spellcheck: false, oninput: () => (state.draft = input.value) });
    input.value = state.draft;
    parts.push(h("label", { for: "policy-content" }, "Policy TOML"), input, h("div", { class: "row-actions" }, button("Review and save", save, "dark")));
  } else if (!state.error && !state.saved) parts.push(h("p", {}, "Reading policy through the guard…"));
  // Reloading throws the edits away, so with edits made it asks first: the
  // button beside "Review and save" is one slip from losing a long draft.
  const edited = () => !!state.doc && !state.saved && state.draft !== state.doc.Content;
  if (state.discarding && edited())
    parts.push(
      h(
        "div",
        { class: "callout", role: "alert" },
        h("span", {}, "Discard your edits and read the file again?"),
        h("div", { class: "row-actions" }, button("Discard edits", () => { state.discarding = false; read(state.name); }, "dark"), button("Keep editing", () => { state.discarding = false; again(); }, "ghost")),
      ),
    );
  else
    parts.push(
      h(
        "div",
        { class: "row-actions" },
        button("Reload file and discard edits", () => {
          if (!edited()) return read(state.name);
          state.discarding = true;
          again();
        }),
        button("View current policy", close),
      ),
    );
  container.replaceChildren(...parts);
}
