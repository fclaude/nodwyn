// Inspectors render a fixed, redacted log snapshot. Current daemon metadata is
// sampled separately; neither view is an authorization source or live budget.
const text = (tag, value, className = "") => {
  const node = document.createElement(tag);
  node.textContent = value;
  node.className = className;
  return node;
};
const json = (value) => text("pre", JSON.stringify(value, null, 2));
function table(headings, rows) {
  const table = document.createElement("table");
  const head = document.createElement("tr");
  for (const heading of headings) head.append(text("th", heading));
  table.append(head);
  for (const row of rows) {
    const tr = document.createElement("tr");
    for (const value of row) tr.append(text("td", value));
    table.append(tr);
  }
  return table;
}
function record(container, event) {
  const item = document.createElement("details");
  item.append(
    text("summary", `#${event.seq} · ${event.kind}`),
    json(event.payload),
  );
  container.append(item);
}
export function renderInspector(
  container,
  kind,
  snapshot,
  filter = "",
  count = 50,
) {
  container.replaceChildren();
  const { events, info, infoError } = snapshot;
  const through = events.at(-1)?.seq || 0;
  container.append(
    text(
      "p",
      `Log snapshot through #${through}. Refresh view to update.`,
      "hint",
    ),
  );
  if (snapshot.truncated)
    container.append(
      text(
        "p",
        "Incomplete log tail: only its valid prefix is available.",
        "warning",
      ),
    );
  if (kind === "log") {
    const matches = events.filter((event) => event.kind.startsWith(filter));
    const shown = matches.slice(-Math.max(1, Math.min(1000, count || 50)));
    container.append(
      text("p", `${matches.length} matching records · showing ${shown.length}`),
    );
    for (const event of shown) record(container, event);
  } else if (kind === "plan") {
    const start = events.findLastIndex((event) => event.kind === "user.msg");
    if (start < 0) {
      container.append(text("p", "No user message recorded."));
      return;
    }
    container.append(
      text("h3", `Latest user message · #${events[start].seq}`),
      text("pre", events[start].payload.text),
    );
    const gate = events
      .slice(start + 1)
      .findLast((event) => event.kind === "gate.plan");
    if (!gate) {
      container.append(
        text("p", "No planning outcome recorded for this message."),
      );
      return;
    }
    const p = gate.payload;
    container.append(
      text("h3", `Recorded outcome · #${gate.seq}`),
      text("p", `Planning gate: ${p.noul}`),
    );
    if (p.noul === "plan" || p.noul === "required")
      container.append(
        text(
          "p",
          `${p.noul === "required" ? "Required plan" : "Plan"} score: ${p.score || 0} · revisions: ${p.revisions || 0}`,
        ),
      );
    else if (p.noul === "proceed")
      container.append(
        text("p", `No plan requested. Need-plan probability: ${p.score || 0}`),
      );
    else if (p.noul === "degraded")
      container.append(text("p", "Gate fallback; no plan score recorded."));
    if (p.reason) container.append(text("p", p.reason));
    if (p.plan) container.append(text("pre", p.plan));
    // The model's step marks after this plan, in the order it made them.
    const marks = events.slice(events.indexOf(gate) + 1).filter((event) => event.kind === "plan.step");
    if (marks.length) {
      container.append(text("h3", "Steps marked by the model"));
      for (const m of marks)
        container.append(text("p", `#${m.seq} · step ${m.payload.step} of ${m.payload.of} ${m.payload.state}${m.payload.note ? `: ${m.payload.note}` : ""}`));
    }
  } else if (kind === "budget") {
    container.append(
      text("h3", "Configured limits · current session lifetime"),
    );
    if (info) {
      const amount = (name, value) =>
        name === "wall_ns" ? `${value / 1e9}s` : String(value);
      container.append(
        table(
          ["Dimension", "Initial soft limit", "Hard limit"],
          Object.entries(info.budgets).map(([name, limit]) => [
            name === "wall_ns" ? "wall" : name,
            amount(name, limit.soft),
            amount(name, limit.hard),
          ]),
        ),
      );
    } else container.append(text("p", infoError));
    container.append(
      text(
        "p",
        "Checkpoint choices apply only within their turn. Recorded usage below is not a live counter or remaining allowance.",
      ),
    );
    const start = events.findLastIndex(
      (event) => event.kind === "user.msg" || event.kind === "eval.req",
    );
    if (start < 0) {
      container.append(text("p", "No submitted turn recorded."));
      return;
    }
    container.append(
      text("h3", `Latest submitted turn · #${events[start].seq}`),
    );
    const tail = events.slice(start + 1);
    const checkpoints = tail.filter(
      (event) => event.kind === "turn.checkpoint",
    );
    if (!checkpoints.length)
      container.append(
        text(
          "p",
          "No checkpoint recorded for this turn; usage is unavailable here.",
        ),
      );
    for (const event of checkpoints) record(container, event);
    container.append(
      text(
        "p",
        tail.some((event) => event.kind === "turn.end")
          ? "Turn end is recorded."
          : "No turn end recorded in this snapshot.",
      ),
    );
  } else if (kind === "policy") {
    if (!info) {
      container.append(text("p", infoError));
      return;
    }
    container.append(
      text("h3", "Last validated bindings"),
      text(
        "p",
        "The guard reloads the binding file before egress. A failed reload denies the call.",
      ),
    );
    if (info.bindings?.length)
      container.append(
        table(
          ["Credential class", "Host", "Methods", "Positions"],
          info.bindings.map((row) => [
            row.Class,
            row.Dest,
            row.Methods.join(", "),
            row.Positions.join(", "),
          ]),
        ),
      );
    else container.append(text("p", "No bindings loaded."));
    container.append(
      text("h3", "Current standing approvals"),
      table(
        ["Kind", "Prefixes"],
        [
          ["Commands", (info.allowed_commands || []).join(", ") || "None"],
          ["Writes", (info.allowed_writes || []).join(", ") || "None"],
        ],
      ),
      text(
        "p",
        `Ordinary writes approved this turn: ${info.turn_writes ? "yes" : "no"}`,
      ),
    );
    if (info.turn_actor)
      container.append(text("p", `Turn approval actor: ${info.turn_actor}`));
    container.append(
      text(
        "p",
        "Policy was sampled from the open session at refresh. File edits require a separate review below when available. Recorded approval receipts do not restore authority on resume.",
      ),
    );
  } else if (kind === "tools") {
    if (!info) {
      container.append(text("p", infoError));
      return;
    }
    container.append(
      text(
        "p",
        `${info.tools.length} tools offered to the model in this session.`,
      ),
    );
    for (const tool of info.tools) {
      const detail = document.createElement("details");
      detail.append(text("summary", tool.name), json(tool));
      container.append(detail);
    }
  }
}
