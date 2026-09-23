// Prompt views only describe choices. The daemon validates the live prompt ID,
// applies the answer, and records the authenticated transport's actor.
const node = (tag, value, className = "") => {
  const element = document.createElement(tag);
  element.textContent = value;
  element.className = className;
  return element;
};
export function renderPrompt(container, event, enabled, reply) {
  container.replaceChildren();
  container.hidden = !event;
  if (!event) return;
  const p = event.payload;
  const buttons = node("div", "", "actions");
  const button = (label, method, params) => {
    const b = node("button", label);
    b.type = "button";
    b.addEventListener("click", () => reply(method, params));
    buttons.append(b);
  };
  container.append(node("div", "NEEDS YOUR ANSWER", "eyebrow"));
  if (event.kind === "prompt.confirm") {
    const prompt = p.Prompt;
    container.append(node("h2", prompt.Prim));
    const reasons = node("ul", "");
    for (const reason of prompt.Reasons || [])
      reasons.append(node("li", reason));
    container.append(reasons, node("pre", prompt.Args, "prompt-arguments"));
    if (prompt.Security)
      container.append(
        node(
          "p",
          "Security confirmation · this answer applies only to this call.",
        ),
      );
    if (prompt.Egress) {
      const detail = node("details", "");
      detail.append(
        node("summary", "Destination and credential scope"),
        node("pre", JSON.stringify(prompt.Egress, null, 2)),
      );
      container.append(detail);
    }
    if (prompt.Detail) {
      const diff = node("pre", "", "diff");
      for (const line of prompt.Detail.split("\n")) {
        const kind =
          line.startsWith("+") && !line.startsWith("+++")
            ? "addition"
            : line.startsWith("-") && !line.startsWith("---")
              ? "deletion"
              : "context";
        diff.append(node("span", line + "\n", kind));
      }
      container.append(
        node("h3", prompt.Path ? "Proposed change" : "Details"),
        diff,
      );
    }
    if (enabled) {
      button("Allow once", "session.answer", { reply: "y" });
      button("Deny", "session.answer", { reply: "n" });
    }
  } else if (event.kind === "prompt.secret") {
    container.append(
      node("h2", `Credential · ${p.Class}`),
      node(
        "p",
        "Stored by the shared keychain. Only an opaque handle reaches Scheme. Input is masked and cleared on submit, session switch, or disconnect.",
      ),
    );
    if (enabled) {
      const form = document.createElement("form");
      const label = node("label", "Secret value");
      const input = document.createElement("input");
      input.id = "prompt-secret";
      label.htmlFor = input.id;
      input.type = "password";
      input.autocomplete = "off";
      input.spellcheck = false;
      const submit = node("button", "Store credential");
      submit.type = "submit";
      form.append(label, input, submit);
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        const secret = input.value;
        input.value = "";
        reply("session.secret", { secret });
      });
      container.append(form);
      button("Decline credential", "session.secret", { secret: "" });
    }
  } else if (event.kind === "prompt.checkpoint") {
    const checkpoint = p.Checkpoint;
    const amount = (v) =>
      checkpoint.dimension === "wall" ? `${v / 1e9}s` : String(v);
    container.append(
      node("h2", `Budget checkpoint · ${checkpoint.dimension}`),
      node(
        "p",
        `Used ${amount(checkpoint.used)} · soft ${amount(checkpoint.limit.soft)} · hard ${amount(checkpoint.limit.hard)}`,
      ),
      node("p", "More budget does not grant tool authority."),
    );
    const usage = node("details", "");
    usage.append(
      node("summary", "Usage at checkpoint"),
      node("pre", JSON.stringify(checkpoint.state, null, 2)),
    );
    container.append(usage);
    if (enabled) {
      button("Continue with more soft budget", "session.answer", {
        reply: "y",
      });
      button("Stop turn", "session.answer", { reply: "n" });
      const form = document.createElement("form");
      const label = node("label", "New absolute soft limit");
      const input = document.createElement("input");
      input.id = "prompt-budget";
      label.htmlFor = input.id;
      input.required = true;
      input.placeholder =
        checkpoint.dimension === "wall"
          ? "e.g. 10m"
          : "Above current usage, at most hard limit";
      const submit = node("button", "Apply soft limit");
      submit.type = "submit";
      form.append(label, input, submit);
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        reply("session.answer", { reply: input.value });
      });
      container.append(form);
    }
  }
  if (!enabled)
    container.append(
      node("p", "Read-only view. Answer this prompt in the terminal client."),
    );
  container.append(buttons);
}
