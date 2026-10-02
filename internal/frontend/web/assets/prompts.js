// Prompt views only describe choices. The daemon validates the live prompt
// ID, applies the answer, and records the answering connection's actor.
import { h, icon, basename, diffStats, oneLine, ago, toolLabel, isMCP } from "./dom.js";
import { usd } from "./transcript.js";

const verbs = { "fs-write": "Write", "fs-edit": "Edit", "shell-exec": "Run", "http-get": "Fetch", "http-post": "Send" };

// summary is what a list says about a waiting prompt: a short title, the
// tool, and a kind a person recognizes.
export function summary(event) {
  const p = event.payload || {};
  if (event.kind === "prompt.secret")
    return { kind: "Credential", tool: p.Class, title: `Add the ${p.Class} credential`, detail: "" };
  if (event.kind === "prompt.choice") return { kind: "Setting", tool: "", title: p.Title, detail: "" };
  if (event.kind === "prompt.checkpoint") {
    const c = p.Checkpoint || {};
    return { kind: "Budget", tool: c.dimension, title: `Keep going past the ${dimension(c.dimension)} limit?`, detail: "" };
  }
  const prompt = p.Prompt || {};
  const verb = verbs[prompt.Prim];
  let title = prompt.Prim || "An operation";
  if (isMCP(prompt.Prim)) {
    const tool = prompt.Prim.split("__").slice(1).join("__").replace(/[_-]+/g, " ");
    return { kind: prompt.Security ? "Security" : "Approval", tool: toolLabel(prompt.Prim), title: tool.charAt(0).toUpperCase() + tool.slice(1), detail: "", mcp: true };
  }
  // An image's own confirm (ADR 22 §3): its reasons say what it is and
  // where it would go.
  if (prompt.Prim === "attach image") title = `Send the image ${oneLine(prompt.Args, 50)} to the model?`;
  else if (prompt.Path) title = `${verb || "Write"} ${basename(prompt.Path)}`;
  else if (prompt.Argv?.length) title = `Run ${oneLine(prompt.Argv.join(" "), 60)}`;
  else if (prompt.Egress?.Host) title = `Send to ${prompt.Egress.Host}`;
  else if (verb) title = `${verb} ${oneLine(prompt.Args, 50)}`;
  return { kind: prompt.Security ? "Security" : "Approval", tool: prompt.Prim, title, detail: prompt.Path ? prompt.Detail || "" : "", path: prompt.Path };
}

function dimension(name) {
  return { tool_calls: "tool-call", tokens: "token", actions: "action", wall: "time", cost: "spend" }[name] || name;
}

// parseDiff turns the guard's unified diff into rows with line numbers.
export function parseDiff(detail) {
  const rows = [];
  let oldLine = 0,
    newLine = 0;
  for (const line of String(detail).split("\n")) {
    if (line.startsWith("---") || line.startsWith("+++")) continue;
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      rows.push({ kind: "hunk", n: "", sign: "", text: hunk[3].trim() || "⋯" });
    } else if (line.startsWith("+")) rows.push({ kind: "add", n: newLine++, sign: "+", text: line.slice(1) });
    else if (line.startsWith("-")) rows.push({ kind: "del", n: oldLine++, sign: "−", text: line.slice(1) });
    else if (line.startsWith("\\")) continue;
    else {
      rows.push({ kind: "ctx", n: newLine || "", sign: "", text: line.startsWith(" ") ? line.slice(1) : line });
      if (newLine) newLine++;
      if (oldLine) oldLine++;
    }
  }
  while (rows.length && rows.at(-1).kind === "ctx" && rows.at(-1).text === "") rows.pop();
  return rows;
}

export function diffView(detail, label) {
  const { plus, minus } = diffStats(detail);
  return h(
    "div",
    { class: "diff" },
    h("div", { class: "diff-head" }, h("span", {}, label || "Proposed change"), h("span", {}, h("span", { class: "plus" }, `+${plus}`), " ", h("span", { class: "minus" }, `−${minus}`))),
    h(
      "div",
      { class: "diff-body" },
      parseDiff(detail).map((row) =>
        h("div", { class: `diff-line ${row.kind}` }, h("span", { class: "n" }, String(row.n)), h("span", { class: "s" }, row.sign), h("span", { class: "t" }, row.text)),
      ),
    ),
  );
}

function position(value) {
  if (!value) return "an unspecified position";
  const [where, name] = value.split(":");
  if (where === "header") return `the ${name || "request"} header`;
  if (where === "query") return `the ${name} query parameter`;
  if (where === "env") return `the ${name} environment variable`;
  return value;
}

// askCard renders one pending prompt as the inline approval. reply(method,
// params) answers it; the card never decides anything itself.
export function askCard(event, { methods, reply, readOnly, since, who }) {
  const p = event.payload || {};
  const s = summary(event);
  const card = h("section", { class: "ask-card", "aria-label": "Needs your answer", dataset: { prompt: p.ID } });
  const head = h(
    "div",
    { class: "card-head" },
    h("span", { class: "eyebrow" }, "Needs your answer"),
    s.tool ? h("span", { class: "tag" }, s.tool) : null,
    s.mcp && (p.Prompt?.Reasons || []).some((r) => r.includes("has not said")) ? h("span", { class: "badge plain" }, "undeclared tool") : null,
    s.kind === "Security" ? h("span", { class: "badge bad" }, "security") : null,
    since ? h("span", { class: "when" }, `asked ${ago(since)}`) : null,
  );
  card.append(head);
  const answers = h("div", { class: "answers" });
  const fine = h("div", { class: "fine" });
  const choice = (label, key, cls, method, params, extra) => {
    const b = h("button", { type: "button", class: `btn big ${cls}`, dataset: { key: key || "" }, onclick: () => reply(method, params) }, extra || label, key ? h("kbd", {}, key) : null);
    answers.append(b);
    return b;
  };
  const enabled = !readOnly && methods.has(event.kind === "prompt.secret" ? "session.secret" : "session.answer");

  if (event.kind === "prompt.confirm") {
    const prompt = p.Prompt || {};
    const title = h("h2", {});
    if (prompt.Path) title.append(`${verbs[prompt.Prim] || "Write"} `, h("span", { class: "mono" }, prompt.Path));
    else if (prompt.Argv?.length) title.append("Run ", h("span", { class: "mono" }, prompt.Argv.join(" ")));
    else title.append(s.title);
    card.append(h("div", { style: "display:flex;flex-direction:column;gap:6px" }, title, prompt.Reasons?.length ? h("ul", { class: "reasons" }, prompt.Reasons.map((r) => h("li", {}, r))) : null));
    if (prompt.Security) card.append(h("p", { class: "security" }, "Security confirmation: this answer applies to this call only."));
    if (s.mcp && !prompt.Egress) {
      // A contained server's reason names it and what it can reach (ADR 23 W3b).
      const box = (prompt.Reasons || []).map((r) => /the contained server (\S+) \(network: ([^)]*)\)/.exec(r)).find(Boolean);
      const to = box ? `the contained server ${box[1]}, which can reach ${box[2] === "none" ? "no network" : box[2]}` : `the MCP server ${toolLabel(prompt.Prim).split("/")[0]}`;
      card.append(h("dl", { class: "facts" }, h("dt", {}, "Sends to"), h("dd", {}, to), h("dt", {}, "Content"), h("dd", {}, `${(prompt.Detail || prompt.Args || "").length.toLocaleString()} characters, below`)));
    }
    if (prompt.Egress) {
      const e = prompt.Egress;
      card.append(
        h(
          "dl",
          { class: "facts" },
          h("dt", {}, "Sends to"),
          h("dd", {}, [e.Host || "an unspecified destination", e.Method ? ` · ${e.Method}` : ""].join("")),
          h("dt", {}, "Credential"),
          h("dd", {}, `${e.Class || "unnamed"} added as ${position(e.Position)}; the model never sees it`),
          // An OAuth2 credential's scopes are what it may do there (ADR 24).
          e.Scopes?.length ? h("dt", {}, "Scopes") : null,
          e.Scopes?.length ? h("dd", {}, e.Scopes.join(" ")) : null,
        ),
      );
    }
    const shown = prompt.Path || prompt.Prim === "attach image" ? "" : prompt.Detail || prompt.Args;
    if (prompt.Path && prompt.Detail) card.append(diffView(prompt.Detail, prompt.Path));
    else if (shown) {
      const long = shown.length > 600;
      const block = h("pre", { class: "args-block" }, long ? shown.slice(0, 600) + "…" : shown);
      card.append(block);
      if (long) {
        const more = h("button", { type: "button", class: "btn small ghost", onclick: () => { block.textContent = shown; more.remove(); } }, "Show all of it");
        card.append(h("div", {}, more));
      }
    }
    if (prompt.Binding && p.Options?.binding) {
      const b = prompt.Binding;
      card.append(h("dl", { class: "facts" }, h("dt", {}, "Save as"), h("dd", {}, `${b.Class} → ${b.Dest} · ${(b.Methods || []).join(", ")} · ${(b.Positions || []).join(", ")}${b.Scopes?.length ? ` · scopes ${b.Scopes.join(" ")}` : ""}`)));
    }
    if (enabled) {
      const options = p.Options || {};
      choice("Allow once", "Y", "accent", "session.answer", { reply: "y" });
      if (options.turn && methods.has("session.approval.create")) {
        choice("For this turn", "T", "", "session.approval.create", { scope: "turn" });
        fine.append(h("p", {}, "For this turn covers ordinary file writes until this turn ends. Protected paths and security confirmations still ask."));
      }
      if (options.session && methods.has("session.approval.create")) {
        choice("", "A", "", "session.approval.create", { scope: "session" }, h("span", {}, "Always under ", h("span", { class: "mono" }, options.target)));
        fine.append(h("p", {}, `Always covers the ${options.kind} prefix ${options.target} until this task is closed.`));
      }
      if (options.binding && prompt.Binding && methods.has("session.binding.create")) {
        choice("Save this binding and allow", "B", "", "session.binding.create", {});
        fine.append(h("p", {}, "The binding is kept beyond this task and allows this call. Other checks still apply."));
      }
      choice("Deny", "N", "danger", "session.answer", { reply: "n" });
      if (!options.turn && !options.session && !options.binding)
        fine.append(h("p", {}, prompt.Security ? "Security confirmations are answered one call at a time." : "No standing answer is offered for this call: it asks every time."));
    }
  } else if (event.kind === "prompt.secret") {
    card.append(h("h2", {}, s.title), h("p", { class: "hint" }, "Stored encrypted in the keychain. Only an opaque handle reaches the model's code; the value is never shown or logged."));
    if (enabled) {
      const input = h("input", { class: "input", id: `secret-${p.ID}`, type: "password", autocomplete: "off", spellcheck: false });
      const form = h(
        "form",
        {
          onsubmit: (ev) => {
            ev.preventDefault();
            const secret = input.value;
            input.value = "";
            reply("session.secret", { secret });
          },
        },
        h("div", { class: "field" }, h("label", { for: input.id }, "Value"), input),
        h("button", { type: "submit", class: "btn accent big" }, "Store credential"),
      );
      card.append(form);
      choice("Decline", "", "danger", "session.secret", { secret: "" });
    }
  } else if (event.kind === "prompt.choice") {
    // A question with named answers whose answer is saved as a setting. An
    // answer marked Local widens what nodwyn does without asking: only this
    // computer, which may change settings, is offered it.
    const here = methods.has("settings.docs.write");
    card.append(h("div", { style: "display:flex;flex-direction:column;gap:6px" }, h("h2", {}, p.Title), p.Reasons?.length ? h("ul", { class: "reasons" }, p.Reasons.map((r) => h("li", {}, r))) : null));
    if (enabled) {
      const options = p.Choices || [];
      options.forEach((o, i) => {
        if (o.Local && !here) return;
        choice(o.Label, String(i + 1), i === 0 ? "accent" : o.Key === "deny" ? "danger" : "", "session.answer", { reply: o.Key });
        fine.append(h("p", {}, h("strong", {}, `${o.Label}: `), o.Detail));
      });
      if (!here && options.some((o) => o.Local)) fine.append(h("p", {}, `${options.filter((o) => o.Local).map((o) => o.Label).join(", ")} can be chosen only on the computer nodwyn runs on.`));
    }
  } else if (event.kind === "prompt.checkpoint") {
    const c = p.Checkpoint || {};
    const amount = (v) => (c.dimension === "wall" ? `${Math.round(v / 6e10)} min` : c.dimension === "cost" ? usd(v / 1e6, false) : String(v));
    card.append(
      h("h2", {}, s.title),
      h("p", { class: "hint" }, `Used ${amount(c.used)} of a soft limit of ${amount(c.limit?.soft)}; the hard limit is ${amount(c.limit?.hard)}${c.state?.priced ? ` · ${c.state.unpriced ? "at least " : ""}${usd(c.state.cost || 0, false)} so far` : ""}${c.state?.judge_calls ? ` · the judge: ${c.state.judge_calls} call${c.state.judge_calls === 1 ? "" : "s"}, ${(c.state.judge_tokens || 0).toLocaleString()} tokens${c.state.judge_cost ? `, ${usd(c.state.judge_cost, true)}` : ""}` : ""}. More budget grants no new permission.`),
    );
    if (enabled) {
      choice("Keep going", "Y", "accent", "session.answer", { reply: "y" });
      const input = h("input", { class: "input", id: `budget-${p.ID}`, required: true, placeholder: c.dimension === "wall" ? "e.g. 30m" : c.dimension === "cost" ? "dollars, e.g. 2.50" : "a new soft limit" });
      card.append(
        h(
          "form",
          { onsubmit: (ev) => { ev.preventDefault(); reply("session.answer", { reply: input.value }); } },
          h("div", { class: "field" }, h("label", { for: input.id }, "Or set a new soft limit"), input),
          h("button", { type: "submit", class: "btn big" }, "Apply"),
        ),
      );
      choice("Stop here", "N", "danger", "session.answer", { reply: "n" });
    }
  }
  if (enabled) {
    card.append(answers);
    fine.append(h("p", {}, `Answer here, in the terminal or on your phone. Each answer is logged with who gave it${who ? ` (this one as ${who})` : ""}.`));
  } else fine.append(h("p", {}, "This view is read-only: answer in the terminal client or an interactive view."));
  card.append(fine);
  return card;
}

// shortcut maps a key press to the card's matching answer, when focus is not
// in a text field: Y, T, A, B, N as the terminal client uses them, and a
// question's answers by number.
export function shortcut(card, event) {
  if (!card || event.metaKey || event.ctrlKey || event.altKey) return false;
  const target = event.target;
  if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return false;
  const key = event.key.toUpperCase();
  const button = [...card.querySelectorAll("button[data-key]")].find((b) => b.dataset.key === key && !b.disabled);
  if (!button) return false;
  event.preventDefault();
  button.click();
  return true;
}

export { icon };
