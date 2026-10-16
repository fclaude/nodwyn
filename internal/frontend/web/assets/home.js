// Home: start anything, and see what needs you across every task.
import { h, icon, repoName, shortPath, ago, minutes, diffStats, toast, basename } from "./dom.js";
import { newSchedule, scheduleTime, scheduleActions } from "./schedule.js";
import { summary } from "./prompts.js";
import { keychainForm } from "./setup.js";
import { firstModel } from "./models.js";
import { Attachments, isImage } from "./attach.js";
import { keepDraft } from "./task.js";
import { ModelPicker, defaultSelection, choicesOf, blocked, defaultJudge, judgesOf } from "./modelpicker.js";

// The folder the next task starts in, for this tab. A tab that has not
// picked yet starts in the folder picked last: this browser's, else the
// daemon's — which survives an app restart, where the window's console is
// on a new loopback port and this browser remembers nothing (ADR 30).
const LAST_KEY = "nodwyn-last-folder";
let chosen = "";
let draft = "";
let current = null;
let files = null; // the files chosen for the next task, kept across redraws
let starting = false; // a start is in flight: one task per ask
// A choice made for a folder in this tab; otherwise a known git workspace
// defaults to a worktree.
const worktreeChoices = new Map();

// The model and effort chosen for the next task, if they are not the default
// (ADR 41): kept while the window is left and come back to, and in this
// browser across a reload, since it is an unsent choice like the message. It
// is dropped when the task starts, and when the default is picked again, so a
// new task always begins on the default of Settings unless it is changed
// there and then. It never changes the default.
const NEW_KEY = "nodwyn-new-task-model";
let picked = null; // {name, effort}, or null for the default
let pickedLoaded = false;
function loadPicked() {
  if (pickedLoaded) return;
  pickedLoaded = true;
  try {
    const v = JSON.parse(localStorage.getItem(NEW_KEY) || "null");
    if (v && typeof v.name === "string" && typeof v.effort === "string") picked = { name: v.name, effort: v.effort };
  } catch {
    /* a browser that refuses storage starts on the default */
  }
}
// The judge chosen for the next task, if it is not the default (ADR 47), kept
// as the model's choice is and dropped as it is. "" is no judge; null is the
// default of Settings.
const NEW_JUDGE_KEY = "nodwyn-new-task-judge";
let pickedJudge = null;
let pickedJudgeLoaded = false;
function loadPickedJudge() {
  if (pickedJudgeLoaded) return;
  pickedJudgeLoaded = true;
  try {
    const v = JSON.parse(localStorage.getItem(NEW_JUDGE_KEY) || "null");
    if (typeof v === "string") pickedJudge = v;
  } catch {
    /* a browser that refuses storage starts on the default */
  }
}
function pickJudge(name, desk) {
  pickedJudge = name === null || name === defaultJudge(desk) ? null : name;
  try {
    if (pickedJudge !== null) localStorage.setItem(NEW_JUDGE_KEY, JSON.stringify(pickedJudge));
    else localStorage.removeItem(NEW_JUDGE_KEY);
  } catch {
    /* kept for this tab only */
  }
}

// The models the next task's model may hand work to (ADR 27, amended
// 2026-10-09). Never kept between tasks, not even in this tab's storage: where
// a task's work may go is chosen for each, and the default is the task's own
// model alone.
const pickedHelpers = new Set();

// Whether the next task's turns stop at Settings' limits (ADR 49). Ticked by
// default, and ticked again for each new task: running without them is a
// choice made for one task, kept only across redraws of this page.
let limitsOn = true;

// helpersBox is the control for it: a fold that says how many are chosen, and
// lists the other models with the labels each carries. The model the task
// talks to is not offered, since it is the one delegating.
function helpersBox(desk, driving) {
  // The default is listed as "control", which in a task names the model the
  // task talks to, so it is not offered: its own entry's name is not here.
  const others = choicesOf(desk).filter((c) => c.name !== driving && c.name !== "control");
  for (const name of [...pickedHelpers]) if (!others.some((c) => c.name === name)) pickedHelpers.delete(name);
  if (!others.length) return null;
  const count = h("span", {});
  const say = () => (count.textContent = pickedHelpers.size ? `Helpers: ${pickedHelpers.size}` : "Helpers: none");
  say();
  return h(
    "details",
    { class: "helpers", id: "new-task-helpers" },
    h("summary", { title: "Other models this task's model may hand pieces of work to" }, count),
    h(
      "div",
      { class: "helpers-pop" },
      h("p", { class: "hint" }, "By default the task works with its own model alone. Tick a model to let it hand self-contained pieces of work to it; it chooses by the labels. What it sends there leaves for that model's provider."),
      others.map((c) =>
        h(
          "label",
          { class: "helper", dataset: { model: c.name } },
          h("input", { type: "checkbox", checked: pickedHelpers.has(c.name), onchange: (event) => { if (event.target.checked) pickedHelpers.add(c.name); else pickedHelpers.delete(c.name); say(); } }),
          h("span", {}, c.model),
          (c.labels || []).map((l) => h("span", { class: "badge plain" }, l)),
        ),
      ),
    ),
  );
}

function pick(selection, desk) {
  const dflt = defaultSelection(desk);
  picked = selection && !(dflt && selection.name === dflt.name && selection.effort === dflt.effort) ? selection : null;
  try {
    if (picked) localStorage.setItem(NEW_KEY, JSON.stringify(picked));
    else localStorage.removeItem(NEW_KEY);
  } catch {
    /* kept for this tab only */
  }
}

// setFolder picks the folder the next task starts in, in this tab, and
// remembers it — in this browser for a fresh tab, and with the daemon
// (workspace.active, local consoles only) for a fresh window or tab that
// this browser has never seen.
function setFolder(folder, desk) {
  chosen = folder;
  localStorage.setItem(LAST_KEY, folder);
  if (desk?.conn?.has("workspace.active")) desk.conn.call("workspace.active", { path: folder }).catch(() => {});
}

// chooseFolder preselects the folder the next task starts in.
export function chooseFolder(folder, desk) {
  setFolder(folder, desk);
}

// forgetFolder stops preselecting a folder that left the list, and drops it
// as the last pick, so a fresh tab does not start there either.
export function forgetFolder(folder) {
  if (chosen === folder) chosen = "";
  if (localStorage.getItem(LAST_KEY) === folder) localStorage.removeItem(LAST_KEY);
}

export function renderHome(root, ctx) {
  const { conn, desk, navigate, pickFolder } = ctx;
  const folders = desk.folders();
  // A tab that has not picked yet starts in the folder picked last: this
  // browser's, else the daemon's, which an app restart keeps.
  // A remembered pick of a folder forgotten since is dropped; a folder
  // picked in this tab is kept, forgotten or not, since a device that cannot
  // add folders chooses one by its path, and using it brings it back.
  if (!chosen) {
    chosen = localStorage.getItem(LAST_KEY) || desk.last || "";
    if (desk.forgotten.has(chosen)) {
      forgetFolder(chosen);
      chosen = "";
    }
  }
  if (!chosen || !folders.includes(chosen)) chosen = chosen && !folders.includes(chosen) && chosen.startsWith("/") ? chosen : folders[0] || "";
  const control = desk.control();
  // Every task is a chat (ADR 35): a long request is a message like any
  // other, and goes on until it is done.
  const canStart = conn.has("session.open") && conn.has("session.send");
  const where = control?.model ? `${control.model}${control.host ? ` · ${control.host}` : ""}` : "no model configured";
  // This computer chooses where a task's content goes (ADR 30, ADR 40): a
  // paired device starts tasks on the default model and says which it is.
  loadPicked();
  loadPickedJudge();
  const canChoose = conn.has("session.model.set") && choicesOf(desk).length > 0;
  const selection = () => picked || defaultSelection(desk);
  // The judge is chosen beside the model (ADR 47), on this computer only,
  // and only where there is a judge to choose.
  const canDelegate = canChoose && conn.has("session.judge.set");
  const canJudge = canChoose && conn.has("session.judge.set") && judgesOf(desk).length > 0;
  const judgeChoice = () => (pickedJudge !== null ? pickedJudge : defaultJudge(desk));

  // Needs you: each waiting task says what it asks.
  const needs = h("section", { class: "home-section", "aria-labelledby": "needs" }, h("h2", { class: "eyebrow", id: "needs" }, "Needs you"));
  const waiting = desk.needsYou();
  if (!waiting.length) needs.append(h("p", { class: "empty-note" }, "Nothing is waiting for you."));
  for (const item of waiting) {
    const events = desk.pending.get(item.id) || [];
    const first = events[0];
    const s = first ? summary(first) : { kind: "Approval", tool: "", title: "Waiting for your answer" };
    const stats = s.detail ? diffStats(s.detail) : null;
    const since = first ? desk.firstSeen.get(first.payload.ID) : null;
    const actions = h("div", { class: "card-actions" }, h("a", { class: "btn small dark", href: `#/task/${item.id}` }, s.path ? "Review the change" : "Review"));
    // Allow once from here only for a plain command or write: a security
    // confirmation or a send to another host needs what the task's card
    // shows (the reasons, the destination, the diff), so it gets Review.
    const plain = first?.kind === "prompt.confirm" && !first.payload?.Prompt?.Security && !first.payload?.Prompt?.Egress;
    if (plain && conn.has("session.answer")) {
      actions.append(
        h(
          "button",
          {
            type: "button",
            class: "btn small",
            onclick: async (event) => {
              event.target.disabled = true;
              try {
                await conn.call("session.answer", { id: item.id, prompt_id: first.payload.ID, reply: "y" });
                toast("Allowed once.");
                desk.scheduleAttention();
              } catch (error) {
                toast(`${error.message} Nothing was retried.`);
                event.target.disabled = false;
              }
            },
          },
          "Allow once",
        ),
      );
    }
    needs.append(
      h(
        "article",
        { class: "card warn" },
        h("div", { class: "card-head" }, h("span", { class: "badge" }, s.kind), s.tool ? h("span", { class: "mono", style: "font-size:12px;color:var(--warn-ink-2)" }, s.tool) : null, since ? h("span", { class: "when" }, ago(since)) : null),
        h(
          "div",
          { class: "stack-sm" },
          h("div", { class: "card-title" }, h("span", {}, s.title), stats ? [h("span", { class: "plus" }, `+${stats.plus}`), h("span", { class: "minus" }, `−${stats.minus}`)] : null),
          h("div", { class: "card-sub" }, `${item.title || "Untitled task"} · ${desk.folderName(desk.project(item))}`),
        ),
        actions,
      ),
    );
  }

  const running = h("section", { class: "home-section", "aria-labelledby": "running" }, h("h2", { class: "eyebrow", id: "running" }, "Running"));
  const active = desk.running();
  if (!active.length) running.append(h("p", { class: "empty-note" }, "Nothing is running."));
  for (const item of active) running.append(runningCard(item));

  const done = h("section", { class: "home-section", "aria-labelledby": "done" }, h("h2", { class: "eyebrow", id: "done" }, "Recently finished"));
  const recent = desk.recent().slice(0, 5);
  if (!recent.length) done.append(h("p", { class: "empty-note" }, "Finished tasks will appear here."));
  else
    done.append(
      h(
        "ul",
        { class: "finished" },
        recent.map((item) =>
          h(
            "li",
            {},
            h(
              "a",
              { href: `#/task/${item.id}` },
              h("div", { class: "row" }, h("span", {}, item.title || "Untitled task"), h("span", {}, ago(item.last_event))),
              h(
                "div",
                { class: "what" },
                item.changes?.length ? [icon("file", 14), h("span", { class: "mono" }, basename(item.changes[0])), h("span", {}, `${item.changes.length > 1 ? ` and ${item.changes.length - 1} more` : ""} · ${desk.folderName(desk.project(item))}`)] : [icon("folder", 14), h("span", {}, `No files changed · ${desk.folderName(desk.project(item))}`)],
              ),
            ),
            item.worktree && conn.has("worktree.diff") ? h("a", { class: "btn small ghost", href: `#/task/${item.id}/review` }, "Review") : null,
          ),
        ),
      ),
    );

  const schedules = h("section", { class: "home-section", "aria-label": "Scheduled tasks" }, h("h2", { class: "eyebrow" }, "Scheduled"));
  for (const item of desk.sessions.values()) if (item.schedule?.definition && !item.archived) {
    const s = item.schedule;
    const update = async (method, params = {}) => {
      try {
        await conn.call(method, { id: item.id, ...params });
        await desk.refresh();
      } catch (error) {
        toast(error.message);
      }
    };
    schedules.append(h("article", { class: "card" }, h("a", { href: `#/task/${item.id}` }, item.title || item.first_message || "Scheduled task"), h("p", { class: "hint" }, s.definition.paused ? "Paused" : s.next?.length ? `Next ${scheduleTime(s.next[0])} · ${s.timezone}` : "No time pending"), h("div", { class: "row-actions" }, ...scheduleActions(conn, s.definition, item.archived, update))));
  }
  if (!schedules.querySelector("article")) schedules.append(h("p", { class: "empty-note" }, "Nothing scheduled."));
  const grid = h("div", { class: "home-grid" }, needs, h("div", {}, running, schedules, done));
  // The ask being typed survives updates: while focus is in the top half,
  // only the lists below are replaced. This is decided before the composer
  // is built, and the composer is built only for a full redraw: the form
  // carries the shared attach input and attachment list, and building it
  // while the old top stays would steal both out of the live form, leaving
  // an attachment on the page that cannot be seen or removed.
  const main = root.querySelector(":scope > main.home");
  const locked = desk.keychain?.exists && !desk.keychain.unlocked && conn.has("keychain.unlock");
  // Startup finishes with a full redraw, but a passphrase already being
  // typed must stay in its masked field rather than move into the chat.
  const unlocking = locked && main?.querySelector("#keychain-pass")?.closest("form")?.contains(document.activeElement);
  const keepTop = main && (ctx.keepTop || unlocking) && main.firstChild?.contains(document.activeElement);
  if (keepTop) {
    main.classList.toggle("urgent", waiting.length > 0);
    main.replaceChild(grid, main.lastChild);
    const label = main.querySelector(".ask-bar .where");
    if (label) label.textContent = where;
    // The models' readiness arrives after the page is drawn, and changes
    // while it is open: the picker says so without a redraw of the composer.
    current?.refresh?.(desk);
    return current;
  }

  const task = h("textarea", {
    id: "task",
    rows: 3,
    placeholder: "Fix a failing test, tidy a folder, answer an email, reconcile a spreadsheet…",
    oninput: () => {
      draft = task.value;
      submit.disabled = !ready();
    },
    onkeydown: (event) => {
      if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        form.requestSubmit();
      }
    },
  });
  task.value = draft;
  const canRun = () => !starting && canStart && conn.live && !desk.keysLocked && !!chosen && !(canChoose && blocked(desk, selection()));
  const ready = () => canRun() && !!task.value.trim();
  const submit = h("button", { type: "submit", class: "send-round", "aria-label": "Start the task", disabled: true }, icon("up", 18, 2));
  files ||= new Attachments();
  const attach = files.button("Attach");
  // A task in a worktree works on its own checkout and branch, so several
  // can work on one repository without touching each other's files or the
  // one you work in. Only a daemon that offers worktree.open, and a bridge
  // that lets it through, shows the choice; only known git workspaces default
  // to it because other folders cannot be opened as worktrees.
  const worktreeDefault = () => !!desk.workspace(chosen)?.git;
  const worktree = () => conn.has("worktree.open") && (worktreeChoices.has(chosen) ? worktreeChoices.get(chosen) : worktreeDefault());
  const worktreeBox = conn.has("worktree.open") && chosen
    ? h(
        "label",
        { class: "worktree-toggle", title: "Start this task in a new git worktree of the folder, on a branch of its own: it does not touch the files you or other tasks are working on. Its changes stay on that branch until it is merged." },
        h("input", { type: "checkbox", checked: worktreeChoices.has(chosen) ? worktreeChoices.get(chosen) : worktreeDefault(), onchange: (event) => worktreeChoices.set(chosen, event.target.checked) }),
        h("span", {}, "In a worktree"),
      )
    : null;
  // Turning a task's limits off is this computer's choice, as Settings'
  // limits are (ADR 49): a console that cannot reach session.limits.set,
  // a paired device's, starts every task with them on and shows no box.
  const limitsBox = conn.has("session.limits.set")
    ? h(
        "label",
        { class: `worktree-toggle limits-toggle${limitsOn ? "" : " on"}`, title: "Stop each turn at the limits in Settings › General: tool calls, tokens, actions and time. Untick to let this task run, and wait for your answers, as long as it needs; its spend limit still applies." },
        h("input", {
          type: "checkbox",
          id: "new-task-limits",
          checked: limitsOn,
          onchange: (event) => {
            limitsOn = event.target.checked;
            event.target.parentElement.classList.toggle("on", !limitsOn);
          },
        }),
        h("span", {}, "Limits"),
      )
    : null;
  // The model and effort the task starts on. What is shown is what is sent.
  const picker = new ModelPicker("new-task-model");
  // The helpers on offer follow the model chosen, which is not one of them.
  const helpersSlot = h("span", { class: "helpers-slot" });
  const drawPicker = () => {
    const sel = selection();
    if (canDelegate) helpersSlot.replaceChildren(...[helpersBox(desk, sel.name)].filter(Boolean));
    picker.update({
      desk,
      selection: sel,
      onPick: (next, reset) => {
        pick(next, desk);
        drawPicker();
        submit.disabled = !ready();
        if (reset) toast("That model takes other efforts: its own default is chosen.");
      },
      ...(canJudge
        ? {
            judge: judgeChoice(),
            onJudge: (name) => {
              pickJudge(name, desk);
              drawPicker();
            },
          }
        : {}),
    });
  };
  // startTask starts the task and sends its message, or, given a schedule,
  // schedules it instead. The schedule is an argument, not page state: one
  // whose start failed stayed set, and the next plain Send scheduled.
  let startTask;
  const form = h(
    "form",
    {
      class: "ask",
      onsubmit: startTask = async (event, scheduling = null) => {
        event.preventDefault();
        // A schedule brings its own message, so the composer may be empty.
        if (scheduling ? !canRun() : !ready()) return;
        starting = true;
        submit.disabled = true;
        let opened = "",
          sending = false;
        try {
          // A model or effort chosen on this page is sent as it is. Left
          // alone, the picker shows the default and the task starts on it, as
          // a task always did: the daemon pins its effort in the log.
          const params = { repo: chosen };
          const sel = canChoose ? picked : null;
          if (sel) {
            params.name = sel.name;
            if (sel.effort) params.effort = sel.effort;
          }
          // So is a judge chosen here; left alone, the task asks the
          // default of Settings, as a task always did.
          if (canJudge && pickedJudge !== null) params.judge = pickedJudge;
          // And the helpers ticked for this task, if any.
          if (canDelegate && pickedHelpers.size) params.delegates = [...pickedHelpers].sort();
          // Limits are on unless unticked: only then is the choice sent.
          if (limitsBox && !limitsOn) params.limits = false;
          const info = await conn.call(worktree() ? "worktree.open" : "session.open", params);
          opened = info.id;
          // An image waits for its own confirm, which the task's page shows.
          if (files.files.some(isImage)) navigate(`#/task/${info.id}`);
          if (files.files.length) await files.upload(info.id);
          sending = true;
          if (scheduling) await conn.call("schedule.set", { id: info.id, ...scheduling });
          else await conn.call("session.send", { id: info.id, text: task.value });
          draft = "";
          pick(null, desk);
          pickJudge(null, desk);
          pickedHelpers.clear();
          limitsOn = true;
          await desk.refresh();
          navigate(`#/task/${info.id}`);
        } catch (error) {
          if (opened && !sending) {
            // The task opened, but an attachment was refused (an image's
            // confirm declined) before the message went: the message waits
            // in the task's reply box, to send there with or without the
            // files, and Home starts afresh rather than asking again.
            keepDraft(opened, task.value);
            draft = "";
            files.clear();
            toast(`${error.message} The message was not sent: it waits in the task's reply box.`);
            navigate(`#/task/${opened}`);
          } else toast(`${error.message} Nothing was retried; check the task list before starting again.`);
          desk.refresh().catch(() => {});
        } finally {
          starting = false;
          submit.disabled = !ready();
        }
      },
    },
    h("label", { class: "sr-only", for: "task" }, "Describe the task"),
    task,
    files.list,
    h(
      "div",
      { class: "ask-bar" },
      h("button", { type: "button", class: "btn", title: chosen || "Choose a folder", onclick: () => document.getElementById("work-in")?.querySelector("button")?.focus() }, icon("folder", 16), h("strong", { style: "font-weight:500" }, chosen ? desk.folderName(chosen) : "Choose a folder")),
      attach,
      worktreeBox,
      limitsBox,
      canChoose ? picker.el : h("span", { class: "where" }, where),
      helpersSlot,
      conn.has("schedule.set") ? h("button", { type: "button", class: "icon-btn", "aria-label": "Schedule", title: "Schedule", onclick: () => {
        // Ready is a folder and a model that can be used: the message is asked
        // for in the dialog, so the composer may still be empty.
        if (!canRun()) return toast("Choose a folder and a model that can be used first.");
        newSchedule(conn, (d) => startTask(new Event("submit", { cancelable: true }), d), task.value);
      } }, icon("clock", 17)) : null,
      submit,
    ),
  );
  submit.disabled = !ready();

  const chips = h("div", { class: "work-in", id: "work-in" }, h("span", {}, "Work in"));
  const listed = [chosen, ...folders.filter((f) => f !== chosen)].filter(Boolean);
  for (const folder of listed.slice(0, 6)) {
    chips.append(
      h(
        "button",
        {
          type: "button",
          class: "chip",
          "aria-pressed": String(folder === chosen),
          title: folder,
          // A pick changes the whole top, the chips, the ask bar's folder and
          // the worktree box, so it redraws it, whatever redraw came before;
          // the chip picked keeps the focus.
          onclick: () => {
            setFolder(folder, desk);
            renderHome(root, { ...ctx, keepTop: false });
            root.querySelector("#work-in button.chip[aria-pressed=true]")?.focus();
          },
        },
        desk.folderName(folder),
      ),
    );
  }
  // The rest are on Workspaces, each with New task here.
  if (listed.length > 6) chips.append(h("a", { class: "chip", href: "#/workspaces", style: "text-decoration:none", title: "Every folder, on Workspaces" }, `${listed.length - 6} more`));
  // Adding a folder names it; where the console may save workspaces, the
  // folder joins the list for next time.
  const adding = h("form", { class: "add-folder card", hidden: true });
  const pathInput = h("input", { class: "input", id: "add-path", placeholder: "/absolute/path/to/a/folder", spellcheck: false, autocomplete: "off", required: true });
  const nameInput = h("input", { class: "input", id: "add-name", placeholder: "for example Receipts 2026", autocomplete: "off" });
  const addError = h("p", { class: "error", role: "alert" });
  const canSave = conn.has("workspace.save");
  adding.append(
    h("div", { class: "field" }, h("label", { for: "add-path" }, "Folder"), h("div", { class: "row-actions" }, pathInput, pickFolder ? h("button", { type: "button", class: "btn", onclick: async () => { const p = await pickFolder(); if (p) { pathInput.value = p; if (!nameInput.value) nameInput.value = p.split("/").pop(); } } }, "Choose…") : null)),
    canSave ? h("div", { class: "field" }, h("label", { for: "add-name" }, "Name"), nameInput) : null,
    canSave ? h("p", { class: "hint" }, "nodwyn reads, writes and organizes the files there and can run programs. Unless git has a file committed and unchanged, it keeps the previous version before changing it.") : null,
    addError,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn dark" }, canSave ? "Add this folder" : "Use this folder"), h("button", { type: "button", class: "btn ghost", onclick: () => (adding.hidden = true) }, "Cancel")),
  );
  adding.addEventListener("submit", async (event) => {
    event.preventDefault();
    const path = pathInput.value.trim().replace(/\/+$/, "");
    if (!path.startsWith("/")) {
      addError.textContent = "Give the folder's full path, starting with /.";
      return;
    }
    if (canSave) {
      try {
        await conn.call("workspace.save", { path, name: nameInput.value.trim(), mounts: [] });
        await desk.refreshWorkspaces();
      } catch (e) {
        addError.textContent = e.message;
        return;
      }
    }
    setFolder(path, desk);
    renderHome(root, { ...ctx, keepTop: false });
  });
  chips.append(
    h(
      "button",
      {
        type: "button",
        class: "chip dashed",
        onclick: async () => {
          adding.hidden = false;
          if (pickFolder && !pathInput.value) {
            const path = await pickFolder();
            if (path) {
              pathInput.value = path;
              nameInput.value = path.split("/").pop();
            }
          }
          (pathInput.value ? nameInput : pathInput).focus();
        },
      },
      icon("plus", 14),
      "Add a folder",
    ),
  );

  // The first run sets a model up with the forms of Settings › Models, which
  // write a provider, a model and the default: all three are this computer's.
  const settingUp = !desk.configured && ["settings.provider.write", "settings.model.write", "settings.control.write"].every((method) => conn.has(method));
  // The forms ask the daemon and draw again when it answers: a page left
  // meanwhile is not drawn over.
  const redrawSetup = () => {
    if (root.querySelector("#first-model")) renderHome(root, { ...ctx, keepTop: false });
  };
  const top = settingUp
    ? h(
        "div",
        { class: "home-col" },
        h("h1", {}, "Let's connect nodwyn to a model"),
        h("p", { class: "lede" }, "nodwyn thinks with a language model you choose: one running on this computer or your network, or a service you have a key for. Nothing is sent anywhere until you set this up."),
        h("div", { class: "card" }, firstModel(ctx, redrawSetup)),
      )
    : h(
        "div",
        { class: "home-col" },
        h("h1", {}, "What should we get done?"),
        locked ? h("details", { class: "callout", open: true }, h("summary", {}, desk.keysLocked ? "Unlock your keys to start a task: the model needs one." : "Your saved keys are locked. Unlock them to use them in new tasks."), keychainForm(ctx, { onDone: () => renderHome(root, { ...ctx, keepTop: false }) })) : null,
        canStart ? form : h("div", { class: "callout plain" }, "This view can't start tasks. Use an interactive view or the terminal."),
        canStart ? chips : null,
        canStart ? adding : null,
      );

  // The composer on the page is this one now.
  files.onChange = () => (submit.disabled = !ready());
  // Setting a model up is typed into, and any change on the desk draws the
  // page again: the field being typed in keeps the focus, as in Settings.
  const focused = settingUp && root.contains(document.activeElement) && document.activeElement.id ? document.activeElement : null;
  const typing = focused ? { id: focused.id, start: focused.selectionStart, end: focused.selectionEnd } : null;
  root.replaceChildren(h("main", { class: waiting.length ? "home urgent" : "home" }, top, grid));
  if (typing) {
    const node = document.getElementById(typing.id);
    node?.focus({ preventScroll: true });
    try {
      if (typing.start !== null && typing.start !== undefined) node?.setSelectionRange(typing.start, typing.end);
    } catch {
      /* not a text field */
    }
  }
  const focus = () => (root.querySelector("#keychain-pass") || task).focus();
  // Not while setting up: the keychain form beside the provider's key is not
  // the field to land in.
  if (!draft && canStart && !settingUp && document.activeElement === document.body) focus();
  if (canChoose) drawPicker();
  current = {
    focus,
    // Redrawn when the desk changes under a page being typed into.
    refresh: () => {
      if (canChoose) drawPicker();
      submit.disabled = !ready();
    },
  };
  return current;

  function runningCard(item) {
    const run = item.run?.status === "running" ? item.run : null;
    const card = h(
      "article",
      { class: "card" },
      h(
        "div",
        { class: "stack-sm" },
        h("div", { style: "display:flex;align-items:center;gap:10px" }, h("span", { class: "run-dot" }), h("a", { href: `#/task/${item.id}`, style: "font-size:15px;font-weight:600;text-decoration:none" }, item.title || "Untitled task")),
        // A run comes from nodwyn run, a batch tool for scripts (ADR 35).
        h("div", { class: "card-sub", style: "padding-left:18px" }, run ? `${run.unattended ? "Unattended run" : "Run"} from nodwyn run · ${desk.folderName(desk.project(item))}` : `Working · ${desk.folderName(desk.project(item))}`),
      ),
    );
    if (run) {
      const left = new Date(run.deadline).getTime() - Date.now();
      const meter = (label, value, pct) =>
        h("div", { class: "meter" }, h("div", { class: "meter-head" }, h("span", {}, label), h("span", {}, value)), h("div", { class: "meter-track" }, h("div", { class: "meter-fill", style: `width:${Math.max(0, Math.min(100, pct))}%` })));
      card.append(h("div", { class: "meters" }, meter("Turns", `${run.turns} / ${run.max_turns}`, (run.turns / run.max_turns) * 100), meter("Time left", left > 0 ? minutes(left) : "expired", 0)));
    }
    card.append(h("div", { class: "card-foot" }, icon("folder", 15), h("span", { class: "mono", style: "color:var(--ink)" }, shortPath(desk.project(item), desk.home)), item.worktree ? h("span", { class: "mono branch" }, item.worktree.branch) : null, h("span", { style: "margin-left:auto" }, `active ${ago(item.last_event)}`)));
    return card;
  }
}
