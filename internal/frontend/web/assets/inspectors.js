// Inspectors render a fixed, redacted log snapshot. Current daemon metadata is
// sampled separately; neither view is an authorization source or live budget.
import { h } from "./dom.js";
const json = (value) => h("pre", {}, JSON.stringify(value, null, 2));
const table = (headings, rows) => h("table", {}, h("tr", {}, headings.map((heading) => h("th", {}, heading))), rows.map((row) => h("tr", {}, row.map((value) => h("td", {}, value)))));
const record = (container, event) => container.append(h("details", {}, h("summary", {}, `#${event.seq} · ${event.kind}`), json(event.payload)));
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
    h("p", { class: "hint" }, `Log snapshot through #${through}. Refresh view to update.`),
  );
  if (snapshot.truncated)
    container.append(
      h("p", { class: "warning" }, "Incomplete log tail: only its valid prefix is available."),
    );
  if (kind === "log") {
    const matches = events.filter((event) => event.kind.startsWith(filter));
    const shown = matches.slice(-Math.max(1, Math.min(1000, count || 50)));
    container.append(
      h("p", {}, `${matches.length} matching records · showing ${shown.length}`),
    );
    for (const event of shown) record(container, event);
  } else if (kind === "plan") {
    const start = events.findLastIndex((event) => event.kind === "user.msg");
    if (start < 0) {
      container.append(h("p", {}, "No user message recorded."));
      return;
    }
    container.append(
      h("h3", {}, `Latest user message · #${events[start].seq}`),
      h("pre", {}, events[start].payload.text),
    );
    const gate = events
      .slice(start + 1)
      .findLast((event) => event.kind === "gate.plan");
    if (!gate) {
      container.append(
        h("p", {}, "No planning outcome recorded for this message."),
      );
      return;
    }
    const p = gate.payload;
    container.append(
      h("h3", {}, `Recorded outcome · #${gate.seq}`),
      h("p", {}, `Planning gate: ${p.noul}`),
    );
    if (p.noul === "plan" || p.noul === "required")
      container.append(
        h("p", {}, `${p.noul === "required" ? "Required plan" : "Plan"} score: ${p.score || 0} · revisions: ${p.revisions || 0}`),
      );
    else if (p.noul === "proceed")
      container.append(
        h("p", {}, `No plan requested. Need-plan probability: ${p.score || 0}`),
      );
    else if (p.noul === "degraded")
      container.append(h("p", {}, "Gate fallback; no plan score recorded."));
    else if (p.noul === "carried")
      container.append(h("p", {}, "The plan, carried from an earlier turn."));
    else if (p.noul === "model")
      container.append(h("p", {}, "The model wrote this plan itself (plan-write)."));
    if (p.reason) container.append(h("p", {}, p.reason));
    if (p.plan) container.append(h("pre", {}, p.plan));
    if (p.marks?.length) {
      container.append(h("h3", {}, "Steps marked in earlier turns"));
      for (const m of p.marks)
        container.append(h("p", {}, `step ${m.step} of ${m.of} ${m.state}${m.note ? `: ${m.note}` : ""}`));
    }
    // The model's step marks after this plan, in the order it made them.
    const marks = events.slice(events.indexOf(gate) + 1).filter((event) => event.kind === "plan.step");
    if (marks.length) {
      container.append(h("h3", {}, "Steps marked by the model"));
      for (const m of marks)
        container.append(h("p", {}, `#${m.seq} · step ${m.payload.step} of ${m.payload.of} ${m.payload.state}${m.payload.note ? `: ${m.payload.note}` : ""}`));
    }
  } else if (kind === "budget") {
    container.append(
      h("h3", {}, "Configured limits · current session lifetime"),
    );
    if (info) {
      const amount = (name, value) =>
        name === "wall_ns" ? `${value / 1e9}s` : name === "cost" ? `$${(value / 1e6).toFixed(2)}` : String(value);
      container.append(
        table(
          ["Dimension", "Initial soft limit", "Hard limit"],
          // A spend limit of 0 is none; it counts the whole task. A turn
          // limit of 0 is none too: the task's limits are off (ADR 49).
          Object.entries(info.budgets).filter(([name, limit]) => name !== "cost" || limit.hard > 0).map(([name, limit]) => [
            name === "wall_ns" ? "wall" : name === "cost" ? "spend (whole task)" : name,
            limit.hard > 0 ? amount(name, limit.soft) : "no limit",
            limit.hard > 0 ? amount(name, limit.hard) : "no limit",
          ]),
        ),
      );
    } else container.append(h("p", {}, infoError));
    container.append(
      h("p", {}, "Checkpoint choices apply only within their turn. Recorded usage below is not a live counter or remaining allowance."),
    );
    const start = events.findLastIndex(
      (event) => event.kind === "user.msg" || event.kind === "eval.req",
    );
    if (start < 0) {
      container.append(h("p", {}, "No submitted turn recorded."));
      return;
    }
    container.append(
      h("h3", {}, `Latest submitted turn · #${events[start].seq}`),
    );
    const tail = events.slice(start + 1);
    const checkpoints = tail.filter(
      (event) => event.kind === "turn.checkpoint",
    );
    if (!checkpoints.length)
      container.append(
        h("p", {}, "No checkpoint recorded for this turn; usage is unavailable here."),
      );
    for (const event of checkpoints) record(container, event);
    container.append(
      h("p", {}, tail.some((event) => event.kind === "turn.end")
          ? "Turn end is recorded."
          : "No turn end recorded in this snapshot."),
    );
  } else if (kind === "policy") {
    if (!info) {
      container.append(h("p", {}, infoError));
      return;
    }
    container.append(
      h("h3", {}, "Last validated bindings"),
      h("p", {}, "The guard reloads the binding file before egress. A failed reload denies the call."),
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
    else container.append(h("p", {}, "No bindings loaded."));
    container.append(
      h("h3", {}, "Current standing approvals"),
      table(
        ["Kind", "Prefixes"],
        [
          ["Commands", (info.allowed_commands || []).join(", ") || "None"],
          ["Writes", (info.allowed_writes || []).join(", ") || "None"],
        ],
      ),
      h("p", {}, `Ordinary writes approved this turn: ${info.turn_writes ? "yes" : "no"}`),
    );
    if (info.turn_actor)
      container.append(h("p", {}, `Turn approval actor: ${info.turn_actor}`));
    container.append(
      h("p", {}, "Policy was sampled from the open session at refresh. File edits require a separate review below when available. Recorded approval receipts do not restore authority on resume."),
    );
  } else if (kind === "tools") {
    if (!info) {
      container.append(h("p", {}, infoError));
      return;
    }
    container.append(
      h("p", {}, `${info.tools.length} tools offered to the model in this session.`),
    );
    for (const tool of info.tools) {
      container.append(h("details", {}, h("summary", {}, tool.name), json(tool)));
    }
  }
}
