// The editor holds only a selected session's draft. Authority comes from the
// daemon's guarded operation and subsequent prompt, never these controls.
const text = (tag, value) => {
  const node = document.createElement(tag);
  node.textContent = value;
  return node;
};
const button = (label, action) => {
  const node = text("button", label);
  node.type = "button";
  node.addEventListener("click", action);
  return node;
};
export function policyChoices(container, read) {
  container.append(
    text("h3", "Edit policy files"),
    text(
      "p",
      "Each save requires a separate diff confirmation. Files contain permission scopes, never credential values.",
    ),
    button("Edit shared bindings", () => read("bindings")),
    button("Edit repository approvals", () => read("approvals")),
  );
}
export function renderPolicyEditor(container, state, read, save, close) {
  container.replaceChildren(
    text(
      "h3",
      state.name === "bindings"
        ? "Shared credential bindings"
        : "Repository standing approvals",
    ),
    text(
      "p",
      state.name === "bindings"
        ? "Persistent shared policy. Sessions reload bindings before egress."
        : "Persistent repository policy. New or resumed sessions load this file; existing sessions retain their current approvals.",
    ),
  );
  if (state.error) container.append(text("p", state.error));
  if (state.saved)
    container.append(
      text("p", "Policy saved. Reload the file before another edit."),
    );
  if (state.doc && !state.saved) {
    container.append(
      text(
        "p",
        state.doc.Exists
          ? "Editing an existing file."
          : "This save will create the policy file.",
      ),
    );
    const label = text("label", "Policy TOML");
    const input = document.createElement("textarea");
    input.id = "policy-content";
    input.rows = 15;
    input.spellcheck = false;
    input.value = state.draft;
    input.addEventListener("input", () => {
      state.draft = input.value;
    });
    label.htmlFor = input.id;
    container.append(label, input, button("Review and save", save));
  } else if (!state.error && !state.saved)
    container.append(text("p", "Reading policy through the guard…"));
  container.append(
    button("Reload file and discard edits", () => read(state.name)),
    button("View current policy", close),
  );
}
