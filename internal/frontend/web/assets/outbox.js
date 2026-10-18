// The outbox (ADR 53 §7): the answers a workflow's runs wrote and that wait
// for a person to post them. Each is a card on Home's Needs you with Post,
// Edit, Discard and Open task. The host posts, with a key bound for exactly
// that call; nothing here sends anything anywhere but the decision.
import { h, toast, toastError } from "./dom.js";
import { dialog } from "./schedule.js";

// preview is what a card shows of the answer: its first lines.
const preview = (body) => {
  const text = body.trim().replace(/\s+\n/g, "\n");
  return text.length > 280 ? `${text.slice(0, 280).trimEnd()}…` : text;
};

export function outboxCard(conn, desk, item) {
  const decide = async (decision, body) => {
    try {
      await conn.call("outbox.decide", { workflow: item.workflow, id: item.id, decision, ...(body !== undefined ? { body } : {}) });
      toast(decision === "post" ? "Posted." : "Discarded. Nothing was posted.");
    } catch (error) {
      toastError(error.message);
    }
    await desk.refreshOutbox();
  };
  const can = conn.has("outbox.decide");
  // Editing is this computer's: the daemon refuses another's words from a
  // paired device, which is offered no Edit.
  const edit = () => {
    const box = dialog("Edit before posting");
    const text = h("textarea", { id: "outbox-body", class: "input", rows: 12, "aria-label": "The review" });
    text.value = item.body;
    const post = h("button", { type: "button", class: "btn dark", id: "outbox-post-edited", onclick: async () => {
      post.disabled = true;
      try {
        await conn.call("outbox.decide", { workflow: item.workflow, id: item.id, decision: "post", body: text.value });
        toast("Posted.");
        box.close();
      } catch (error) {
        toastError(error.message);
        post.disabled = false;
      }
      await desk.refreshOutbox();
    } }, "Post this");
    box.append(h("div", { class: "stack-md" }, h("p", { class: "hint" }, item.summary), text, h("div", { class: "row-actions" }, post)));
    text.focus();
  };
  return h(
    "article",
    { class: "card warn outbox-card", dataset: { id: item.id } },
    h("div", { class: "card-head" }, h("span", { class: "badge" }, "Ready to post"), h("span", { class: "mono", style: "font-size:12px;color:var(--warn-ink-2)" }, item.name), h("span", { class: "when" }, item.when)),
    h("div", { class: "stack-sm" }, h("div", { class: "card-title" }, h("span", {}, `Review for #${item.item}`)), h("div", { class: "card-sub" }, item.summary)),
    h("p", { class: "outbox-body" }, preview(item.body)),
    h(
      "div",
      { class: "card-actions" },
      can ? h("button", { type: "button", class: "btn small dark", onclick: (event) => { event.target.disabled = true; decide("post").finally(() => (event.target.disabled = false)); } }, "Post review") : null,
      can && conn.has("workflow.set") ? h("button", { type: "button", class: "btn small", onclick: edit }, "Edit") : null,
      can ? h("button", { type: "button", class: "btn small ghost", onclick: () => decide("discard") }, "Discard") : null,
      h("a", { class: "btn small ghost", href: `#/task/${item.task}` }, "Open task"),
    ),
  );
}
