// The Workflows page (ADR 53): every workflow, a schedule or one that
// watches a source, with when it runs or what it watches, what today's runs
// came to and how the latest ended, in the daemon's words. A timetable is
// made on Home, in its Schedule dialog, with every choice a task has: there
// is one way to start a task. A workflow that watches a source (pull
// requests, email, a command) is made in the builder here (#/workflows/new),
// which takes the same choices beside its source, its filters, where its runs
// go and what happens with the answer.
import { h, toast, toastError, removal } from "./dom.js";
import { nextLine, scheduleActions, manageSchedule } from "./schedule.js";
import { usd } from "./transcript.js";
import { choicesOf, defaultSelection } from "./modelpicker.js";

// newWorkflow says Home is to open its Schedule dialog once it is drawn,
// which New workflow asks for on its way there.
let newWorkflow = false;
export function takeNewWorkflow() {
  const asked = newWorkflow;
  newWorkflow = false;
  return asked;
}

// todayLine says what today's runs came to, and the day's limit if there is one.
function todayLine(s, dayCost) {
  const limit = dayCost ? ` of ${usd(dayCost)} a day` : "";
  if (!s.today?.runs) return dayCost ? `No run today · up to ${usd(dayCost)} a day` : "No run today";
  return `Today: ${s.today.runs} run${s.today.runs === 1 ? "" : "s"}${s.today.cost || limit ? ` · ${usd(s.today.cost || 0)}${limit}` : ""}`;
}

// removing is the workflow whose Remove is asking. watched is the workflows
// that watch a source as the daemon last said them, or null before it has:
// the page is drawn again whenever the desk changes, and keeps showing these
// while it asks again, so a row does not vanish and come back under the reader.
let removing = "";
let watched = null;

export function renderWorkflows(root, ctx) {
  if (ctx.tab) return renderBuilder(root, ctx);
  const { conn, desk, navigate } = ctx;
  const redraw = () => renderWorkflows(root, ctx);
  const items = [...desk.sessions.values()]
    .filter((item) => item.schedule?.definition)
    .sort((a, b) => Number(!!a.archived) - Number(!!b.archived) || (a.title || a.first_message || "").localeCompare(b.title || b.first_message || ""));
  const update = (item) => async (method, params = {}) => {
    try {
      await conn.call(method, { id: item.id, ...params });
      await desk.reload();
    } catch (error) {
      toastError(error.message);
    }
    redraw();
  };
  const row = (item) => {
    const s = item.schedule;
    const d = s.definition;
    const last = s.history?.[0];
    return h(
      "article",
      { class: "workflow-row", dataset: { id: item.id } },
      h(
        "div",
        { class: "workflow-main" },
        h("a", { class: "workflow-name", href: `#/task/${item.id}` }, item.title || item.first_message || "Scheduled task"),
        h("p", { class: "workflow-when" }, s.sentence || d.repeat),
        h("p", { class: s.held ? "hint warn" : "hint", title: s.timezone }, item.archived ? "Archived, so paused" : d.paused ? "Paused" : s.held || nextLine(s)),
        h("p", { class: "hint workflow-today" }, todayLine(s, d.day_cost), last ? ` · Last: ${last.what}` : ""),
      ),
      h(
        "div",
        { class: "row-actions" },
        ...scheduleActions(conn, d, item.archived, update(item)),
        h("button", { type: "button", class: "btn", onclick: () => manageSchedule(conn, item.id, async () => { await desk.reload(); redraw(); }) }, "Schedule…"),
      ),
    );
  };
  const watching = (view) => {
    const d = view.definition;
    const act = async (method, params = {}) => {
      try {
        await conn.call(method, { id: view.id, ...params });
      } catch (error) {
        toastError(error.message);
      }
      redraw();
    };
    return h(
      "article",
      { class: "workflow-row", dataset: { id: view.id } },
      h(
        "div",
        { class: "workflow-main" },
        h("span", { class: "workflow-name" }, d.name),
        h("p", { class: "workflow-when" }, view.sentence),
        h("p", { class: view.status.startsWith("Could not") ? "hint warn" : "hint" }, view.status),
        h("p", { class: "hint workflow-today" }, todayLine(view, d.day_cost), view.running ? ` · ${view.running} running` : ""),
        view.last_skip ? h("p", { class: "hint warn" }, view.last_skip.reason) : null,
        view.outbox?.length ? h("p", { class: "hint warn" }, `${view.outbox.length} answer${view.outbox.length === 1 ? "" : "s"} waiting to be posted, on Home under Needs you`) : null,
        view.recent.length
          ? h("ul", { class: "workflow-fires", "aria-label": "Recent events" }, view.recent.map((f) => h("li", {}, h("span", { class: "schedule-when" }, f.when), h("a", { href: `#/task/${f.task}` }, f.summary))))
          : null,
      ),
      h(
        "div",
        { class: "row-actions" },
        conn.has("workflow.pause") && (!d.paused || conn.has("workflow.write")) ? h("button", { type: "button", class: "btn", onclick: () => act("workflow.pause", { paused: !d.paused }) }, d.paused ? "Resume" : "Pause") : null,
        conn.has("workflow.check") ? h("button", { type: "button", class: "btn", onclick: () => act("workflow.check") }, "Check now") : null,
        conn.has("workflow.write") ? h("a", { class: "btn", href: `#/workflows/${view.id}` }, "Edit") : null,
        conn.has("workflow.remove")
          ? removal({ label: "Remove", consequence: "Remove the workflow? The tasks it opened and what they did stay.", asking: removing === view.id, ask: () => ((removing = view.id), redraw()), cancel: () => ((removing = ""), redraw()), run: () => ((removing = ""), act("workflow.remove")) })
          : null,
      ),
    );
  };
  const body = h("div", { class: "workflow-list", "aria-label": "Workflows" });
  const paint = () => {
    body.replaceChildren(...(watched || []).map(watching), ...items.map(row));
    if (!body.children.length) body.append(h("p", { class: "empty-note" }, watched === null && conn.has("workflow.list") ? "Reading the workflows…" : "No workflows yet. A timetable starts from Home, with its task's folder, model and limits. One that watches pull requests, email or a command starts here."));
  };
  paint();
  if (conn.has("workflow.list")) {
    conn.call("workflow.list").then((views) => {
      watched = views;
      // Only while this page is the one shown: a later page is not drawn over.
      if (root.contains(body)) paint();
    }).catch((error) => toastError(error.message));
  }
  const start = h(
    "div",
    { class: "row-actions" },
    conn.has("workflow.write") ? h("a", { class: "btn dark", id: "new-watcher", href: "#/workflows/new" }, "Watch for events") : null,
    conn.has("schedule.set") ? h("button", { type: "button", class: "btn", id: "new-workflow", onclick: () => { newWorkflow = true; navigate("#/"); } }, "New timetable") : null,
  );
  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("div", { class: "page-head" }, h("h1", {}, "Workflows"), start),
        h("p", { class: "lede" }, "A workflow sends its task a message when its time comes, or when something happens at a source it watches, and each run goes into a task. Here is when each runs next or what it watches, what today's runs came to and how the latest ended."),
        body,
      ),
    ),
  );
}

const sources = {
  "gitea-pulls": "Pull requests on Gitea",
  "github-pulls": "Pull requests on GitHub",
  gmail: "Email in Gmail",
  command: "A command's output",
};
const filterOps = [["is", "is"], ["is_not", "is not"], ["contains", "contains"], ["not_contains", "does not contain"]];
const pullFields = ["author", "base", "draft", "labels", "title"];
const mailFields = ["from", "to", "subject", "labels", "date"];

// The builder's state outlives a redraw of the page, and is dropped when the
// page is left for another: one workflow is made at a time.
let draft = null;

// words splits a command line into the program and its arguments, honouring
// double quotes; there is no shell, so nothing else is special.
const words = (text) => [...text.matchAll(/"([^"]*)"|(\S+)/g)].map((m) => m[1] ?? m[2]);
const unwords = (list) => (list || []).map((w) => (/\s/.test(w) || w === "" ? `"${w}"` : w)).join(" ");

function renderBuilder(root, ctx) {
  const { conn, desk, navigate, tab } = ctx;
  const editingId = tab === "new" ? "" : tab;
  const folders = desk.folders();
  const defaults = () => ({
    name: "", source: { kind: "gitea-pulls", server: "", repo: "", credential: "", query: "", command: [], on: ["opened", "updated"] }, every: 2, filters: [], message: "", routing: "subject",
    repo: folders[0] || "", worktree: false, model: "", no_limits: false, cost: 0, day_cost: 0, concurrency: 0, existing: false,
    then: { post: "", auto: false, notify: "" },
  });
  if (!draft || draft.id !== editingId) draft = { id: editingId, def: defaults(), loaded: !editingId, test: null, credentials: [] };
  const state = draft;
  const redraw = () => renderBuilder(root, ctx);
  if (!state.loaded) {
    state.loaded = true;
    conn.call("workflow.read", { id: editingId }).then((view) => { state.def = { ...defaults(), ...view.definition, filters: view.definition.filters || [], then: { ...defaults().then, ...(view.definition.then || {}) } }; delete state.def.actor; delete state.def.removed; redraw(); }).catch((error) => { toastError(error.message); navigate("#/workflows"); });
  }
  if (conn.has("credentials.list") && !state.credentials.length) {
    conn.call("credentials.list").then((r) => { state.credentials = [...new Set((r.credentials || []).map((c) => c.class))]; redraw(); }).catch(() => {});
  }
  const d = state.def;
  const set = (patch) => { Object.assign(d, patch); state.test = null; };
  const field = (label, control, hint) => h("label", { class: "builder-field" }, h("span", {}, label), control, hint ? h("span", { class: "hint" }, hint) : null);
  const text = (id, value, onInput, extra = {}) => h("input", { id, class: "input", type: "text", value: value ?? "", oninput: (e) => onInput(e.target.value), ...extra });
  const select = (id, value, options, onChange) => h("select", { id, class: "input", onchange: (e) => { onChange(e.target.value); redraw(); } }, options.map(([v, label]) => h("option", { value: v, selected: v === value }, label)));
  const src = d.source;
  const isPulls = src.kind === "gitea-pulls" || src.kind === "github-pulls";
  const isMail = src.kind === "gmail";

  const credentials = h("datalist", { id: "builder-credentials" }, state.credentials.map((c) => h("option", { value: c })));
  const when = h(
    "section", { class: "builder-card", "aria-label": "When" },
    h("h2", { class: "eyebrow" }, "When"),
    field("Source", select("wf-kind", src.kind, Object.entries(sources), (v) => {
      // What happens with the answer depends on the source: a review for a
      // pull request, a draft reply for an email, nothing for a command.
      const family = (k) => (k === "gitea-pulls" || k === "github-pulls" ? "pulls" : k);
      if (family(v) !== family(src.kind)) { d.then.post = ""; d.then.auto = false; d.filters = []; }
      src.kind = v;
      state.test = null;
    })),
    isMail
      ? [
          field("Token", [text("wf-credential", src.credential, (v) => { src.credential = v; state.test = null; }, { list: "builder-credentials", placeholder: "the credential's name" }), credentials], "An OAuth2 credential for Google, authorized with the gmail.readonly scope (nodwyn credential add --oauth2 google --scopes gmail.readonly) and allowed at gmail.googleapis.com for GET. To save drafts it also needs the gmail.compose scope and a POST binding."),
          field("Server (optional)", text("wf-server", src.server, (v) => { src.server = v; state.test = null; }, { placeholder: "Google's Gmail API" }), "Empty is Google's. The credential is sent only where Connections › Credentials allows it."),
          field("Search", text("wf-query", src.query, (v) => { src.query = v; state.test = null; }, { placeholder: "in:inbox from:(@acme.com) is:unread" }), "As in Gmail's search box. Empty is your inbox. Reading a message or archiving it does not make an event; a new message does."),
          h("div", { class: "row-actions", role: "group", "aria-label": "Fires on" }, h("span", { class: "muted" }, "Fires when"), ...[["opened", "a new email arrives"], ["updated", "a thread gets a reply"]].map(([on, label]) => h("button", { type: "button", class: "chip", id: `wf-on-${on}`, "aria-pressed": String((src.on || []).includes(on)), onclick: () => { const next = new Set(src.on || []); next.has(on) ? next.delete(on) : next.add(on); if (next.size) src.on = [...next]; state.test = null; redraw(); } }, label))),
        ]
      : isPulls
      ? [
          src.kind === "gitea-pulls" ? field("Server", text("wf-server", src.server, (v) => { src.server = v; state.test = null; }, { placeholder: "https://git.example.com" })) : null,
          src.kind === "github-pulls" ? field("Server (optional)", text("wf-server", src.server, (v) => { src.server = v; state.test = null; }, { placeholder: "GitHub's API" }), "Empty is github.com. For GitHub Enterprise, its API address.") : null,
          field("Repository", text("wf-repo", src.repo, (v) => { src.repo = v; state.test = null; }, { placeholder: "owner/name" })),
          field("Token", [text("wf-credential", src.credential, (v) => { src.credential = v; state.test = null; }, { list: "builder-credentials", placeholder: "the credential's name" }), credentials], "A token kept in the keychain and allowed at this host for GET (Connections › Credentials). Nothing is sent with it but the listing."),
          h("div", { class: "row-actions", role: "group", "aria-label": "Fires on" }, h("span", { class: "muted" }, "Fires when a pull request is"), ...[["opened", "opened"], ["updated", "given new commits"]].map(([on, label]) => h("button", { type: "button", class: "chip", id: `wf-on-${on}`, "aria-pressed": String((src.on || []).includes(on)), onclick: () => { const next = new Set(src.on || []); next.has(on) ? next.delete(on) : next.add(on); if (next.size) src.on = [...next]; state.test = null; redraw(); } }, label))),
        ]
      : field("Command", text("wf-command", unwords(src.command), (v) => { src.command = words(v); state.test = null; }, { placeholder: "gh pr list --json number,title" }), "Run in the folder below with no shell, as the program you name: an absolute path, or a name on PATH. It prints one JSON object per line with an id, and a version that changes when the item does."),
    field("Check every (minutes)", h("input", { id: "wf-every", class: "input", type: "number", min: 1, max: 1440, step: 1, value: d.every, oninput: (e) => set({ every: Number(e.target.value) }) })),
    h("label", { class: "check" }, h("input", { id: "wf-existing", type: "checkbox", checked: !!d.existing, onchange: (e) => set({ existing: e.target.checked }) }), "Also handle what is there when this starts (otherwise it is only recorded)"),
  );

  const filters = h(
    "section", { class: "builder-card", "aria-label": "Only if" },
    h("h2", { class: "eyebrow" }, "Only if"),
    ...d.filters.map((f, i) => h(
      "div", { class: "schedule-row builder-filter" },
      h("input", { class: "input", type: "text", list: "builder-fields", value: f.field, "aria-label": "Field", oninput: (e) => { f.field = e.target.value; state.test = null; } }),
      h("select", { class: "input", "aria-label": "Condition", onchange: (e) => { f.op = e.target.value; state.test = null; } }, filterOps.map(([v, label]) => h("option", { value: v, selected: v === f.op }, label))),
      h("input", { class: "input", type: "text", value: f.value, "aria-label": "Value", oninput: (e) => { f.value = e.target.value; state.test = null; } }),
      h("button", { type: "button", class: "icon-btn", "aria-label": "Remove this filter", onclick: () => { d.filters.splice(i, 1); state.test = null; redraw(); } }, "×"),
    )),
    h("datalist", { id: "builder-fields" }, (isPulls ? pullFields : isMail ? mailFields : []).map((f) => h("option", { value: f }))),
    h("button", { type: "button", class: "btn small ghost", id: "wf-add-filter", onclick: () => { d.filters.push({ field: isPulls ? "author" : isMail ? "from" : "", op: "is_not", value: "" }); state.test = null; redraw(); } }, "Add a filter"),
    d.filters.length ? null : h("p", { class: "hint" }, "No filter: every event runs."),
  );

  const choices = choicesOf(desk);
  const model = h("select", { id: "wf-model", class: "input", onchange: (e) => set({ model: e.target.value }) }, [["", `${defaultSelection(desk)?.name || "The default"} (default)`], ...choices.map((c) => [c.name, c.name])].map(([v, label]) => h("option", { value: v, selected: v === (d.model || "") }, label)));
  const doing = h(
    "section", { class: "builder-card", "aria-label": "Do" },
    h("h2", { class: "eyebrow" }, "Do"),
    field("Instruction", h("textarea", { id: "wf-message", class: "input", rows: 4, oninput: (e) => set({ message: e.target.value }) }, d.message), "Sent as each run's request. The event is attached as data, never as instructions."),
    field("Folder", select("wf-repo-folder", d.repo, folders.map((f) => [f, desk.folderName(f)]), (v) => set({ repo: v }))),
    h("label", { class: "check" }, h("input", { id: "wf-worktree", type: "checkbox", checked: !!d.worktree, onchange: (e) => set({ worktree: e.target.checked }) }), "Each task in a worktree of its own"),
    field("Model", model),
    h("label", { class: "check" }, h("input", { id: "wf-limits", type: "checkbox", checked: !d.no_limits, onchange: (e) => set({ no_limits: !e.target.checked }) }), "Limits (stop each turn at Settings' limits)"),
    h("div", { class: "schedule-row schedule-bounds" },
      field("Spend limit per run ($; 0 means none)", h("input", { id: "wf-cost", class: "input", type: "number", min: 0, step: "any", value: d.cost, oninput: (e) => set({ cost: Number(e.target.value) || 0 }) })),
      field("Spend limit per day ($; 0 means none)", h("input", { id: "wf-day-cost", class: "input", type: "number", min: 0, step: "any", value: d.day_cost || 0, oninput: (e) => set({ day_cost: Number(e.target.value) || 0 }) }))),
  );

  const routing = h(
    "section", { class: "builder-card", "aria-label": "Runs go to" },
    h("h2", { class: "eyebrow" }, "Runs go to"),
    h("div", { class: "row-actions", role: "group", "aria-label": "Runs go to" },
      ...[["subject", isPulls ? "A task per pull request" : isMail ? "A task per email thread" : "A task per item"], ["one", "One task for every event"]].map(([v, label]) => h("button", { type: "button", class: "chip", id: `wf-routing-${v}`, "aria-pressed": String(d.routing === v), onclick: () => { set({ routing: v }); redraw(); } }, label))),
    h("p", { class: "hint" }, d.routing === "subject" ? "A later event about the same item arrives in its task as the next run." : "Every event goes into one task, in turn."),
    field("Runs at once", h("input", { id: "wf-concurrency", class: "input", type: "number", min: 0, max: 10, step: 1, value: d.concurrency || 0, oninput: (e) => set({ concurrency: Number(e.target.value) || 0 }) }), "0 is 2. Events past it wait for the next check."),
  );

  // Then: what happens with the answer. A review is posted by the host
  // with a key bound for that call, once the person says yes; a command
  // source has no pull request to post to.
  const chip = (id, label, pressed, onclick) => h("button", { type: "button", class: "chip", id, "aria-pressed": String(pressed), onclick }, label);
  const then = h(
    "section", { class: "builder-card", "aria-label": "Then" },
    h("h2", { class: "eyebrow" }, "Then"),
    isPulls || isMail
      ? h("div", { class: "row-actions", role: "group", "aria-label": "The answer" }, chip("wf-post-none", "Leave the answer in the task", !d.then.post, () => { d.then.post = ""; d.then.auto = false; state.test = null; redraw(); }), isPulls ? chip("wf-post-review", "Post it as a review", d.then.post === "review", () => { d.then.post = "review"; state.test = null; redraw(); }) : chip("wf-post-draft", "Save it as a draft reply", d.then.post === "draft", () => { d.then.post = "draft"; state.test = null; redraw(); }))
      : null,
    d.then.post === "review" || d.then.post === "draft"
      ? [
          h("div", { class: "row-actions", role: "group", "aria-label": "Asking" }, chip("wf-ask", "Ask me first", !d.then.auto, () => { d.then.auto = false; state.test = null; redraw(); }), chip("wf-auto", "Without asking", !!d.then.auto, () => { d.then.auto = true; state.test = null; redraw(); })),
          h("p", { class: "hint" }, d.then.post === "draft"
            ? (d.then.auto
              ? "The reply is saved as a draft in the email's thread as the run ends, to the sender. It is never sent: sending is yours, from Gmail. If it cannot be saved, it waits on Home."
              : "The reply waits on Home, under Needs you, to save, edit or discard. Saving puts a draft in the thread, to the sender; nothing is sent. It needs the gmail.compose scope and a POST binding at gmail.googleapis.com.")
            : (d.then.auto
              ? "The review is posted as the run ends, as a comment (never an approval), with the token bound for POST at the forge. If it cannot be, it waits on Home like any other."
              : "The answer waits on Home, under Needs you, to post, edit or discard. Connections › Credentials must allow the token for POST at the forge's host: listing pull requests is a different grant.")),
        ]
      : null,
    h("div", { class: "row-actions", role: "group", "aria-label": "Tell me" }, h("span", { class: "muted" }, "Desktop notice:"), chip("wf-notify-needs", "When it needs me", !d.then.notify, () => { d.then.notify = ""; state.test = null; redraw(); }), chip("wf-notify-run", "After every run", d.then.notify === "run", () => { d.then.notify = "run"; state.test = null; redraw(); }), chip("wf-notify-never", "Never", d.then.notify === "never", () => { d.then.notify = "never"; state.test = null; redraw(); })),
  );

  const error = h("p", { class: "error", role: "alert" });
  const outcome = h("div", { class: "builder-test", role: "status" });
  const definition = () => ({ ...d, name: d.name.trim() || (isPulls ? `Pull requests on ${src.repo}` : "A workflow"), filters: d.filters.filter((f) => f.field.trim()) });
  const paintTest = () => {
    const t = state.test;
    outcome.replaceChildren(
      ...(t
        ? [
            t.sentence ? h("p", { class: "schedule-sentence" }, t.sentence) : null,
            t.items.length
              ? h("ul", { class: "workflow-items", "aria-label": "What is there now" }, t.items.map((i) => h("li", { class: i.passes ? "passes" : "" }, h("span", {}, `#${i.id} ${i.title}`), h("span", { class: "schedule-when" }, i.passes ? "would run" : i.why))))
              : h("p", { class: "hint" }, "Nothing is there now."),
            h("p", { class: "hint" }, t.note),
          ]
        : []),
    );
  };
  paintTest();
  // The button is kept before the first await: an event's currentTarget is
  // null once its handler has yielded.
  const test = h("button", { type: "button", class: "btn", id: "wf-test", onclick: async () => {
    test.disabled = true;
    error.textContent = "";
    try {
      state.test = await conn.call("workflow.test", { definition: definition() });
      paintTest();
    } catch (e) {
      error.textContent = e.message;
    } finally {
      test.disabled = false;
    }
  } }, "Test the trigger");
  const save = h("button", { type: "button", class: "btn dark", id: "wf-save", onclick: async () => {
    save.disabled = true;
    error.textContent = "";
    try {
      await conn.call("workflow.write", { id: editingId, definition: definition() });
      draft = null;
      toast(editingId ? "Saved." : "Saved. It records what is there at its first check, and runs on what comes after.");
      navigate("#/workflows");
    } catch (e) {
      error.textContent = e.message;
    } finally {
      save.disabled = false;
    }
  } }, editingId ? "Save" : "Create the workflow");

  root.replaceChildren(
    h(
      "main",
      { class: "page" },
      h(
        "div",
        { class: "page-col" },
        h("div", { class: "page-head" }, h("h1", {}, editingId ? "Change a workflow" : "Watch for events"), h("a", { class: "btn ghost", href: "#/workflows", onclick: () => { draft = null; } }, "Back")),
        h("p", { class: "lede" }, "Each answer is a card: when, only if, what to do, and where each run goes. Test the trigger asks the source once and lists what a run would be for, with no model and nothing recorded."),
        field("Name", text("wf-name", d.name, (v) => (d.name = v), { placeholder: "Review pull requests" })),
        when, filters, doing, routing, then,
        h("div", { class: "row-actions" }, test, save),
        error, outcome,
      ),
    ),
  );
}
