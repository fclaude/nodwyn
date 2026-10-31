// Home: start anything, and see what needs you across every task, on every
// host the window shows (ADR 54).
import { h, icon, repoName, shortPath, ago, minutes, diffStats, toast, toastError, basename } from "./dom.js";
import { newSchedule, nextLine, scheduleTime, scheduleActions } from "./schedule.js";
import { takeNewWorkflow } from "./workflows.js";
import { outboxCard } from "./outbox.js";
import { summary } from "./prompts.js";
import { keychainForm } from "./setup.js";
import { firstModel } from "./models.js";
import { Attachments, isImage } from "./attach.js";
import { keepDraft } from "./task.js";
import { ModelPicker, defaultSelection, choicesOf, blocked, defaultJudge, judgesOf, defaultCommands, commandsChoices } from "./modelpicker.js";
import { hostState } from "./hosts.js";

// The folder the next task starts in, for this tab. A tab that has not
// picked yet starts in the folder picked last: this browser's, else the
// daemon's — which survives an app restart, where the window's console is
// on a new loopback port and this browser remembers nothing (ADR 30).
const LAST_KEY = "nodwyn-last-folder";
let draft = "";
// Whether New task's Options and its Helpers fold are open. Home is drawn
// again whole at some changes (a reconnection, a model's state), and a fold
// the person had opened closed under them: the checkbox they were about to
// tick was gone. Starting a task closes them for the next.
let optionsOpen = false;
let helpersOpen = false;
let current = null;
let files = null; // the files chosen for the next task, kept across redraws
let starting = false; // a start is in flight: one task per ask

// What the composer chose last for a new task, which the next one starts
// with (ADR 41, amended 2026-10-27): the host it runs on and, for each host,
// its model and effort, its judge, its helpers and, for each folder,
// whether it opens in a worktree. A field left out is that host's default:
// Settings' model, effort and judge, no helpers, and a worktree for a known
// git workspace. A task starts without limits (ADR 49, amended 2026-10-27). This computer's daemon keeps it
// (newtask.last), so the app's window, another browser and a console on
// another address start with it too, where this browser's storage, which is
// one origin's, has nothing (ADR 30); a daemon without the method leaves it
// to this browser. A linked host's choices are kept here too, under its
// name, since they are this person's.
const KEPT_KEY = "nodwyn-new-task";
let kept = null; // {runs_on, hosts: [{host, model, effort, judge, helpers, worktree}]}, once read
let keeper = null; // this computer's {conn, desk}, which reads and keeps it
// loadKept reads the choices once, or is null while the daemon's are not
// read yet.
function loadKept() {
  if (kept) return kept;
  const { conn, desk } = keeper;
  let read = null;
  if (conn.has("newtask.last")) {
    if (desk.newTask === undefined) return null;
    read = desk.newTask;
  } else {
    try {
      read = JSON.parse(localStorage.getItem(KEPT_KEY) || "null");
    } catch {
      /* a browser that refuses storage starts on the defaults */
    }
  }
  kept = { runs_on: typeof read?.runs_on === "string" ? read.runs_on : "", hosts: Array.isArray(read?.hosts) ? structuredClone(read.hosts) : [] };
  return kept;
}
// choicesFor is what was chosen for host's next task: {} for its defaults.
function choicesFor(host) {
  return loadKept()?.hosts.find((e) => e.host === host) || { host };
}
// keep changes host's choices (or, for null, the record itself) and keeps
// them. A choice made before the daemon's were read starts a record anew:
// it is the newest.
function keep(host, change) {
  if (!loadKept()) kept = { runs_on: "", hosts: [] };
  if (host === null) change(kept);
  else {
    let entry = kept.hosts.find((e) => e.host === host);
    if (!entry) kept.hosts.push((entry = { host }));
    change(entry);
  }
  const { conn } = keeper;
  if (conn.has("newtask.last")) conn.call("newtask.last", { choices: kept }).catch((error) => toastError(`The choice is not kept for the next task: ${error.message}`));
  else {
    try {
      localStorage.setItem(KEPT_KEY, JSON.stringify(kept));
    } catch {
      /* kept for this tab only */
    }
  }
}

// The worktree choices kept for a host, newest last, are at most this many,
// within the 256 the daemon takes.
const maxWorktreeChoices = 200;

// The host a new task starts on (ADR 54): "" for this computer, else a
// linked host's name.
const loadRunsOn = () => loadKept()?.runs_on || "";
function setRunsOn(name) {
  keep(null, (k) => (k.runs_on = name));
}
// keyFor is the name a choice is kept under in this browser: this
// computer's as it always was, a linked host's with the host's name, since
// its folders are its own.
const keyFor = (key, host) => (host ? `${key}@${host}` : key);
// What this tab chose for each host's next task: its folder.
const perHost = new Map();
function stateOf(host = "") {
  let st = perHost.get(host);
  if (!st) perHost.set(host, (st = { chosen: "" }));
  return st;
}

// The model and effort chosen for the next task, or null for the default of
// Settings (ADR 41). Picking the default again keeps no model, so the next
// task follows Settings' default if it changes. It never changes the default.
function pickedOf(host) {
  const c = choicesFor(host);
  return typeof c.model === "string" && c.model ? { name: c.model, effort: typeof c.effort === "string" ? c.effort : "" } : null;
}
// The judge chosen for the next task (ADR 47): "" is no judge; null is the
// default of Settings.
function pickedJudgeOf(host) {
  const judge = choicesFor(host).judge;
  return typeof judge === "string" ? judge : null;
}
// Who answers the next task's commands (ADR 35, amended 2026-10-30), or null
// for Settings' approval.commands.
function pickedCommandsOf(host) {
  const commands = choicesFor(host).commands;
  return typeof commands === "string" && commands ? commands : null;
}
function pickCommands(mode, desk) {
  keep(desk.host, (e) => {
    if (mode === defaultCommands(desk)) delete e.commands;
    else e.commands = mode;
  });
}
function pickJudge(name, desk) {
  keep(desk.host, (e) => {
    if (name === null || name === defaultJudge(desk)) delete e.judge;
    else e.judge = name;
  });
}

// The models the next task's model may hand work to (ADR 27, amended
// 2026-10-09 and 2026-10-27): none by default, and those ticked last after.
// They are the models of the host they were ticked for; the model the task
// talks to is not offered, since it is the one delegating, nor the default,
// which is listed as "control", the name a task gives the model it talks to.
const helpersOffered = (desk, driving) => choicesOf(desk).filter((c) => c.name !== driving && c.name !== "control");
function helpersOf(desk, driving) {
  const offered = new Set(helpersOffered(desk, driving).map((c) => c.name));
  const helpers = choicesFor(desk.host).helpers;
  return Array.isArray(helpers) ? helpers.filter((name) => offered.has(name)) : [];
}

// helpersBox is the control for them: a fold that says how many are chosen,
// and lists the other models with the labels each carries.
function helpersBox(desk, driving, changed) {
  const others = helpersOffered(desk, driving);
  if (!others.length) return null;
  const count = h("span", {});
  const say = () => {
    const n = helpersOf(desk, driving).length;
    count.textContent = n ? `Helpers: ${n}` : "Helpers: none";
  };
  say();
  const tick = (name, on) =>
    keep(desk.host, (e) => {
      const rest = (Array.isArray(e.helpers) ? e.helpers : []).filter((n) => n !== name);
      if (on) rest.push(name);
      if (rest.length) e.helpers = rest;
      else delete e.helpers;
    });
  const chosen = new Set(helpersOf(desk, driving));
  return h(
    "details",
    { class: "helpers", id: "new-task-helpers", open: helpersOpen, ontoggle: (event) => (helpersOpen = event.target.open) },
    h("summary", { title: "Other models this task's model may hand pieces of work to" }, count),
    h(
      "div",
      { class: "helpers-pop" },
      h("p", { class: "hint" }, "By default the task works with its own model alone. Tick a model to let it hand self-contained pieces of work to it; it chooses by the labels. What it sends there leaves for that model's provider."),
      others.map((c) =>
        h(
          "label",
          { class: "helper", dataset: { model: c.name } },
          h("input", { type: "checkbox", checked: chosen.has(c.name), onchange: (event) => { tick(c.name, event.target.checked); say(); changed(); } }),
          h("span", {}, c.model),
          (c.labels || []).map((l) => h("span", { class: "badge plain" }, l)),
        ),
      ),
    ),
  );
}

function pick(selection, desk) {
  const dflt = defaultSelection(desk);
  keep(desk.host, (e) => {
    if (!selection || (dflt && selection.name === dflt.name && selection.effort === dflt.effort)) {
      delete e.model;
      delete e.effort;
    } else {
      e.model = selection.name;
      if (selection.effort) e.effort = selection.effort;
      else delete e.effort;
    }
  });
}

// setFolder picks the folder the next task starts in on desk's host, in
// this tab, and remembers it — in this browser for a fresh tab, and with the
// daemon (workspace.active, local consoles only) for a fresh window or tab
// that this browser has never seen.
function setFolder(folder, desk) {
  const host = desk?.host || "";
  stateOf(host).chosen = folder;
  localStorage.setItem(keyFor(LAST_KEY, host), folder);
  if (desk?.conn?.has("workspace.active")) desk.conn.call("workspace.active", { path: folder }).catch(() => {});
}

// chosenFolder is the folder Home would start the next task in on this
// computer, as far as this tab knows: what the sidebar's search takes its
// project from.
export function chosenFolder() {
  return stateOf("").chosen || localStorage.getItem(LAST_KEY) || "";
}

// chooseFolder preselects the folder the next task starts in.
export function chooseFolder(folder, desk) {
  setFolder(folder, desk);
}

// forgetFolder stops preselecting a folder of host's that left the list,
// and drops it as the last pick, so a fresh tab does not start there either.
export function forgetFolder(folder, host = "") {
  const st = stateOf(host);
  if (st.chosen === folder) st.chosen = "";
  if (localStorage.getItem(keyFor(LAST_KEY, host)) === folder) localStorage.removeItem(keyFor(LAST_KEY, host));
}

export function renderHome(root, ctx) {
  const { navigate, hosts } = ctx;
  // The host a new task starts on (ADR 54), and what the composer offers
  // follows it: its folders, its models and the choices its console lets
  // this window make. A host that is gone or not reachable leaves the
  // choice made, and the composer says why it cannot start.
  const entries = hosts ? hosts.all() : [{ name: "", label: "This computer", conn: ctx.conn, desk: ctx.desk }];
  keeper = entries[0];
  const several = entries.length > 1;
  const target = entries.find((e) => e.name === loadRunsOn()) || entries[0];
  const { conn, desk } = target;
  const st = stateOf(target.name);
  // A folder chooser picks a folder on this computer.
  const pickFolder = target.name ? null : ctx.pickFolder;
  // What the forms on this page act on: the host a task starts on.
  const tctx = { ...ctx, conn, desk, pickFolder };
  // The lists below are every host's that can be read, each task with its
  // host's desk.
  const live = entries.filter((e) => e.conn.live);
  const everyHost = (list) => live.flatMap((e) => list(e.desk).map((item) => ({ item, desk: e.desk, conn: e.conn })));
  const newest = (a, b) => new Date(b.item.last_event) - new Date(a.item.last_event);
  // hostChip names the host of a task in a list, when there are several.
  const hostChip = (d) => (several ? h("span", { class: "host-chip" }, icon(d.host ? "server" : "laptop", 12), d.host || "This computer") : null);
  const folders = conn.live ? desk.folders() : [];
  // A tab that has not picked yet starts in the folder picked last: this
  // browser's, else the daemon's, which an app restart keeps.
  // A remembered pick of a folder forgotten since is dropped; a folder
  // picked in this tab is kept, forgotten or not, since a device that cannot
  // add folders chooses one by its path, and using it brings it back.
  if (!st.chosen) {
    st.chosen = localStorage.getItem(keyFor(LAST_KEY, target.name)) || desk.last || "";
    if (desk.forgotten.has(st.chosen)) {
      forgetFolder(st.chosen, target.name);
      st.chosen = "";
    }
  }
  if (!st.chosen || !folders.includes(st.chosen)) st.chosen = st.chosen && !folders.includes(st.chosen) && st.chosen.startsWith("/") ? st.chosen : folders[0] || "";
  const control = desk.control();
  // Every task is a chat (ADR 35): a long request is a message like any
  // other, and goes on until it is done.
  // A linked host that cannot be read now keeps its composer, which says so.
  const canStart = (conn.has("session.open") && conn.has("session.send")) || (!!target.name && !conn.live);
  const where = !conn.live && target.name ? `${target.name} is not reachable` : control?.model ? `${control.model}${control.host ? ` · ${control.host}` : ""}` : "no model configured";
  // This computer chooses where a task's content goes (ADR 30, ADR 40): a
  // paired device starts tasks on the default model and says which it is.
  const canChoose = conn.has("session.model.set") && choicesOf(desk).length > 0;
  const selection = () => pickedOf(target.name) || defaultSelection(desk);
  // The judge is chosen beside the model (ADR 47), on this computer only,
  // and only where there is a judge to choose.
  const canDelegate = canChoose && conn.has("session.judge.set");
  const canJudge = canChoose && conn.has("session.judge.set") && judgesOf(desk).length > 0;
  const judgeChoice = () => pickedJudgeOf(target.name) ?? defaultJudge(desk);
  // Who answers its commands is chosen beside them, on this computer, for a
  // daemon that takes it.
  const canAnswer = canChoose && conn.has("session.commands.set");
  const commandsChoice = () => pickedCommandsOf(target.name) ?? defaultCommands(desk);

  // Needs you: each waiting task says what it asks.
  const needs = h("section", { class: "home-section home-needs", "aria-labelledby": "needs" }, h("h2", { class: "eyebrow", id: "needs" }, "Needs you"));
  const waiting = everyHost((d) => d.needsYou()).sort(newest);
  const held = live.flatMap((e) => e.desk.outbox.map((item) => ({ item, desk: e.desk, conn: e.conn })));
  if (!waiting.length && !held.length) needs.append(h("p", { class: "empty-note" }, "Nothing is waiting for you."));
  // A workflow's answer waits here for a yes before anything is posted.
  for (const { item, desk: d, conn: c } of held) needs.append(outboxCard(c, d, item));
  for (const { item, desk: d, conn: c } of waiting) {
    const events = d.pending.get(item.id) || [];
    const first = events[0];
    const s = first ? summary(first) : { kind: "Approval", tool: "", title: "Waiting for your answer" };
    const stats = s.detail ? diffStats(s.detail) : null;
    const since = first ? d.firstSeen.get(first.payload.ID) : null;
    const actions = h("div", { class: "card-actions" }, h("a", { class: "btn small dark", href: d.href(item.id) }, s.path ? "Review the change" : "Review"));
    // Allow once from here only for a plain command or write: a security
    // confirmation or a send to another host needs what the task's card
    // shows (the reasons, the destination, the diff), so it gets Review.
    const plain = first?.kind === "prompt.confirm" && !first.payload?.Prompt?.Security && !first.payload?.Prompt?.Egress;
    if (plain && c.has("session.answer")) {
      actions.append(
        h(
          "button",
          {
            type: "button",
            class: "btn small",
            onclick: async (event) => {
              event.target.disabled = true;
              try {
                await c.call("session.answer", { id: item.id, prompt_id: first.payload.ID, reply: "y" });
                toast("Allowed once.");
                d.scheduleAttention();
              } catch (error) {
                toastError(`${error.message} Nothing was retried.`);
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
          h("div", { class: "card-sub" }, hostChip(d), `${item.title || "Untitled task"} · ${d.folderName(d.project(item))}`),
        ),
        actions,
      ),
    );
  }

  const running = h("section", { class: "home-section", "aria-labelledby": "running" }, h("h2", { class: "eyebrow", id: "running" }, "Running"));
  const active = everyHost((d) => d.running()).sort(newest);
  if (!active.length) running.append(h("p", { class: "empty-note" }, "Nothing is running."));
  for (const { item, desk: d } of active) running.append(runningCard(item, d));

  const done = h("section", { class: "home-section", "aria-labelledby": "done" }, h("h2", { class: "eyebrow", id: "done" }, "Recently finished"));
  const recent = everyHost((d) => d.recent()).sort(newest).slice(0, 5);
  if (!recent.length) done.append(h("p", { class: "empty-note" }, "Finished tasks will appear here."));
  else
    done.append(
      h(
        "ul",
        { class: "finished" },
        recent.map(({ item, desk: d, conn: c }) =>
          h(
            "li",
            {},
            h(
              "a",
              { href: d.href(item.id) },
              h("div", { class: "row" }, h("span", {}, item.title || "Untitled task"), h("span", {}, ago(item.last_event))),
              h(
                "div",
                { class: "what" },
                hostChip(d),
                item.changes?.length ? [icon("file", 14), h("span", { class: "mono" }, basename(item.changes[0])), h("span", {}, `${item.changes.length > 1 ? ` and ${item.changes.length - 1} more` : ""} · ${d.folderName(d.project(item))}`)] : [icon("folder", 14), h("span", {}, `No files changed · ${d.folderName(d.project(item))}`)],
              ),
            ),
            item.worktree && c.has("worktree.diff") ? h("a", { class: "btn small ghost", href: d.href(item.id, "/review") }, "Review") : null,
          ),
        ),
      ),
    );

  const schedules = h("section", { class: "home-section", "aria-label": "Scheduled tasks" }, h("h2", { class: "eyebrow" }, "Scheduled", " ", h("a", { class: "eyebrow-link", href: "#/workflows" }, "All workflows")));
  for (const { item, desk: d, conn: c } of everyHost((d) => [...d.sessions.values()])) if (item.schedule?.definition && !item.archived) {
    const s = item.schedule;
    const update = async (method, params = {}) => {
      try {
        await c.call(method, { id: item.id, ...params });
        await d.refresh();
      } catch (error) {
        toast(error.message);
      }
    };
    schedules.append(h("article", { class: "card" }, hostChip(d), h("a", { href: d.href(item.id) }, item.title || item.first_message || "Scheduled task"), h("p", { class: "hint" }, s.sentence || s.definition.repeat), h("p", { class: s.held ? "hint warn" : "hint", title: s.timezone }, s.definition.paused ? "Paused" : s.held || (s.next_words ? nextLine(s) : s.next?.length ? `Next ${scheduleTime(s.next[0])} · ${s.timezone}` : "No time pending")), h("div", { class: "row-actions" }, ...scheduleActions(c, s.definition, item.archived, update))));
  }
  if (!schedules.querySelector("article")) schedules.append(h("p", { class: "empty-note" }, "Nothing scheduled."));
  // The daemon says of each schedule whether locked keys hold back its next
  // run: said once here, where the schedules are, and unlocked above. This
  // part of Home is drawn again when the list changes; the top is not while
  // a passphrase is being typed.
  else if (live.some((e) => [...e.desk.sessions.values()].some((item) => item.schedule?.held && !item.archived))) schedules.querySelector("h2").after(h("p", { class: "hint warn", id: "schedules-held" }, "Your keys are locked, so scheduled runs wait until you unlock them, and then run once, late."));
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
      sync();
    },
    // Enter starts the task and Shift+Enter breaks the line, as in a task's
    // reply box; Cmd/Ctrl+Enter still starts it.
    onkeydown: (event) => {
      if (event.key === "Enter" && !event.shiftKey && !event.altKey && !event.isComposing) {
        event.preventDefault();
        form.requestSubmit();
      }
    },
  });
  task.value = draft;
  // unready says why a task cannot be started or scheduled yet, apart from
  // its message, or is "" when it can: a button that does nothing and says
  // nothing reads as a broken one.
  const unready = () =>
    starting ? "A task is starting." : !conn.live ? (target.name ? `${target.name} is not reachable.` : "Disconnected.") : desk.keysLocked ? "Unlock your keys first." : !st.chosen ? "Choose a folder first." : canChoose && blocked(desk, selection()) ? "The chosen model cannot be used yet." : "";
  const ready = () => canStart && !unready() && !!task.value.trim();
  const submit = h("button", { type: "submit", class: "send-round", "aria-label": "Start the task", disabled: true }, icon("up", 18, 2));
  // sync draws what depends on whether a task can start now.
  const sync = () => {
    submit.disabled = !ready();
    if (scheduleButton) {
      const why = unready();
      scheduleButton.disabled = !!why;
      scheduleButton.title = why || "Run this task later, or on a repeat";
    }
  };
  files ||= new Attachments();
  const attach = files.button("Attach");
  // A task in a worktree works on its own checkout and branch, so several
  // can work on one repository without touching each other's files or the
  // one you work in. Only a daemon that offers worktree.open, and a bridge
  // that lets it through, shows the choice; only known git workspaces default
  // to it because other folders cannot be opened as worktrees.
  const worktreeDefault = () => !!desk.workspace(st.chosen)?.git;
  // A named workspace says whether it is a git repository; a folder a task
  // merely used does not, so it is left to the daemon, which refuses.
  const notGit = !!desk.workspace(st.chosen) && !desk.workspace(st.chosen).git;
  // A choice made for this folder before is kept for the next task there.
  const worktreeChoice = () => {
    const list = choicesFor(target.name).worktree;
    return Array.isArray(list) ? list.find((w) => w.folder === st.chosen) : undefined;
  };
  const worktree = () => conn.has("worktree.open") && !notGit && (worktreeChoice() ? !!worktreeChoice().on : worktreeDefault());
  const keepWorktree = (on) =>
    keep(target.name, (e) => {
      const rest = (Array.isArray(e.worktree) ? e.worktree : []).filter((w) => w.folder !== st.chosen);
      rest.push({ folder: st.chosen, on });
      e.worktree = rest.slice(-maxWorktreeChoices);
    });
  const worktreeBox = conn.has("worktree.open") && st.chosen
    ? h(
        "label",
        { class: "worktree-toggle", title: notGit ? "Only a git repository can have worktrees, and this folder is not one." : "Start this task in a new git worktree of the folder, on a branch of its own: it does not touch the files you or other tasks are working on. Its changes stay on that branch until it is merged." },
        h("input", { type: "checkbox", disabled: notGit, checked: worktree(), onchange: (event) => { keepWorktree(event.target.checked); count(); } }),
        h("span", {}, "In a worktree"),
      )
    : null;
  // The rare choices sit behind one control, which says how many differ from
  // the defaults, so the bar holds what every task needs: the files, the
  // model and Send.
  const scheduleButton = conn.has("schedule.set")
    ? h("button", { type: "button", class: "btn small", id: "new-task-schedule", onclick: () => {
        options.open = false;
        newSchedule(conn, task.value, (d) => startTask(new Event("submit", { cancelable: true }), d));
      } }, "Schedule…")
    : null;
  const optionsCount = h("span", { class: "count", hidden: true });
  const count = () => {
    const changed = (worktreeBox && !notGit && worktree() !== worktreeDefault() ? 1 : 0) + (canDelegate && helpersOf(desk, selection()?.name).length ? 1 : 0) + (canAnswer && commandsChoice() !== defaultCommands(desk) ? 1 : 0);
    optionsCount.textContent = String(changed);
    optionsCount.hidden = !changed;
  };
  // Who answers the task's commands waits behind Options too, so the
  // composer's bar keeps its one slim control.
  const commandsBox = canAnswer
    ? h(
        "label",
        { class: "commands-choice" },
        h("span", {}, "Commands"),
        h(
          "select",
          { class: "input", id: "new-task-commands", onchange: (event) => { pickCommands(event.target.value, desk); count(); } },
          commandsChoices.map(([value, text]) => h("option", { value, selected: value === commandsChoice() }, `${text}${value === defaultCommands(desk) ? " (default)" : ""}`)),
        ),
      )
    : null;
  const option = (control, hint) => (control ? h("div", { class: "option" }, control, h("p", { class: "hint" }, hint)) : null);
  // The model and effort the task starts on. What is shown is what is sent.
  const picker = new ModelPicker("new-task-model");
  // The helpers on offer follow the model chosen, which is not one of them.
  const helpersSlot = h("span", { class: "helpers-slot" });
  const drawPicker = () => {
    const sel = selection();
    if (canDelegate) helpersSlot.replaceChildren(...[helpersBox(desk, sel.name, count)].filter(Boolean));
    count();
    picker.update({
      desk,
      selection: sel,
      onPick: (next, reset) => {
        pick(next, desk);
        drawPicker();
        sync();
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
  const options = h(
    "details",
    { class: "options", id: "new-task-options", open: optionsOpen, ontoggle: () => (optionsOpen = options.open), onkeydown: (event) => { if (event.key === "Escape") options.open = false; } },
    h("summary", { class: "btn", title: "Worktree, helpers, commands and scheduling" }, "Options", optionsCount),
    h(
      "div",
      { class: "options-pop stack-md" },
      option(worktreeBox, notGit ? "Only a git repository can have worktrees, and this folder is not one." : "Work on a branch of its own, so nothing you or other tasks are editing is touched."),
      helpersSlot,
      option(commandsBox, "Who answers this task's commands: the judge, then you; the judge alone; you; or nobody. Settings › General sets the default."),
      option(scheduleButton, "Run this task later, or on a repeat, instead of now."),
    ),
  );
  options.hidden = !worktreeBox && !scheduleButton && !canDelegate && !commandsBox;
  count();
  // Runs on chooses the host the task starts on (ADR 54): the folders, the
  // models and the choices above follow it. A host that cannot be read now
  // is offered, saying so, and the composer says why it cannot start.
  const runsOnPicker = several
    ? h(
        "label",
        { class: "runs-on", title: "The host this task starts on, with its folders, models and keys" },
        h("span", { class: "runs-on-label" }, "Runs on"),
        h(
          "select",
          {
            id: "runs-on",
            onchange: (event) => {
              setRunsOn(event.target.value);
              renderHome(root, { ...ctx, keepTop: false });
              root.querySelector("#runs-on")?.focus();
            },
          },
          entries.map((e) => h("option", { value: e.name, selected: e === target }, e.name ? `${e.name}${e.conn.live ? "" : ` (${hostState(e)[1]})`}` : "This computer")),
        ),
      )
    : null;
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
        // A schedule carries its own message, written in its dialog.
        if (scheduling ? !canStart || unready() : !ready()) return;
        const text = scheduling ? scheduling.message : task.value;
        starting = true;
        submit.disabled = true;
        let opened = "",
          sending = false;
        try {
          // A model or effort chosen on this page is sent as it is. Left
          // alone, the picker shows the default and the task starts on it, as
          // a task always did: the daemon pins its effort in the log.
          const params = { repo: st.chosen };
          const sel = canChoose ? pickedOf(target.name) : null;
          if (sel) {
            params.name = sel.name;
            if (sel.effort) params.effort = sel.effort;
          }
          // So is a judge chosen here; left alone, the task asks the
          // default of Settings, as a task always did.
          if (canJudge && pickedJudgeOf(target.name) !== null) params.judge = pickedJudgeOf(target.name);
          // And who answers its commands.
          if (canAnswer && pickedCommandsOf(target.name) !== null) params.commands = pickedCommandsOf(target.name);
          // And the helpers ticked, if any.
          const helpers = canDelegate ? helpersOf(desk, selection()?.name) : [];
          if (helpers.length) params.delegates = [...helpers].sort();
          // A task starts without limits; they are set on its panel, for
          // the few tasks that want one (ADR 49, amended 2026-10-27).
          const info = await conn.call(worktree() ? "worktree.open" : "session.open", params);
          opened = info.id;
          // An image waits for its own confirm, which the task's page shows.
          if (files.files.some(isImage)) navigate(desk.href(info.id));
          if (files.files.length) await files.upload(info.id, target.name);
          sending = true;
          if (scheduling) await conn.call("schedule.set", { id: info.id, ...scheduling });
          else await conn.call("session.send", { id: info.id, text });
          draft = "";
          optionsOpen = helpersOpen = false;
          // What was chosen stays for the next task (ADR 41, amended
          // 2026-10-27).
          await desk.refresh();
          navigate(desk.href(info.id));
        } catch (error) {
          if (opened && !sending) {
            // The task opened, but an attachment was refused (an image's
            // confirm declined) before the message went: the message waits
            // in the task's reply box, to send there with or without the
            // files, and Home starts afresh rather than asking again.
            keepDraft(opened, text);
            draft = "";
            files.clear();
            toastError(`${error.message} The message was not sent: it waits in the task's reply box.`);
            navigate(desk.href(opened));
          } else {
            // The daemon's refusal before any task opened says so plainly;
            // a lost connection or a timeout may have started one, so those
            // still send the person to the task list.
            const refused = !opened && !/retried|timed out|Disconnected|Connection lost|Reconnecting/i.test(error.message);
            toastError(refused ? `The task did not start: ${error.message.replace(/^[a-z]+(\.[a-z]+)+: /, "")}` : `${error.message} Nothing was retried; check the task list before starting again.`);
          }
          desk.reload();
        } finally {
          starting = false;
          sync();
        }
      },
    },
    h("label", { class: "sr-only", for: "task" }, "Describe the task"),
    task,
    files.list,
    h(
      "div",
      { class: "ask-bar" },
      attach,
      options,
      runsOnPicker,
      canChoose ? picker.el : h("span", { class: "where" }, where),
      // A link that only answers on a linked host chooses no model there;
      // without a word, the missing picker read as a fault.
      target.name && conn.live && !conn.has("session.model.set")
        ? h("a", { class: "where-why", id: "host-model-why", href: desk.at("settings"), title: `This computer only answers on ${target.name}: its Settings say how to make your choices there` }, "Why can't I choose?")
        : null,
      submit,
    ),
    h("p", { class: "hint ask-hint" }, several ? `Enter starts the task on ${target.name || "this computer"} · Shift+Enter for a new line` : "Enter starts the task · Shift+Enter for a new line"),
  );
  sync();

  const chips = h("div", { class: "work-in", id: "work-in" }, h("span", {}, "Work in"));
  const listed = [st.chosen, ...folders.filter((f) => f !== st.chosen)].filter(Boolean);
  for (const folder of listed.slice(0, 6)) {
    chips.append(
      h(
        "button",
        {
          type: "button",
          class: "chip",
          "aria-pressed": String(folder === st.chosen),
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
  // The rest are on Workspaces, each with New task here: this computer's.
  if (listed.length > 6 && !target.name) chips.append(h("a", { class: "chip", href: "#/workspaces", style: "text-decoration:none", title: "Every folder, on Workspaces" }, `${listed.length - 6} more`));
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
  // The folders are the host's the task runs on.
  if (several) chips.append(h("span", { class: "hint work-in-host" }, target.name ? `Folders on ${target.name}` : "Folders on this computer"));

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
        h("div", { class: "card" }, firstModel(tctx, redrawSetup)),
      )
    : h(
        "div",
        { class: "home-col" },
        h("h1", {}, "What should we get done?"),
        locked ? h("details", { class: "callout", open: true }, h("summary", {}, desk.keysLocked ? "Unlock your keys to start a task: the model needs one." : "Your saved keys are locked. Unlock them to use them in new tasks."), keychainForm(tctx, { onDone: async () => {
          // The daemon said of each schedule whether locked keys hold back
          // its next run; unlocked, the list read before is out of date.
          await desk.reload();
          renderHome(root, { ...ctx, keepTop: false });
        } })) : null,
        canStart ? form : h("div", { class: "callout plain" }, "This view can't start tasks. Use an interactive view or the terminal."),
        canStart ? chips : null,
        canStart ? adding : null,
      );

  // The composer on the page is this one now.
  files.onChange = sync;
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
  // New workflow on the Workflows page comes here for the Schedule dialog,
  // which starts a task with every choice above.
  if (takeNewWorkflow() && scheduleButton && !scheduleButton.disabled) scheduleButton.click();
  current = {
    focus,
    // Redrawn when the desk changes under a page being typed into.
    refresh: () => {
      if (canChoose) drawPicker();
      sync();
    },
  };
  return current;

  function runningCard(item, desk) {
    const run = item.run?.status === "running" ? item.run : null;
    const card = h(
      "article",
      { class: "card" },
      h(
        "div",
        { class: "stack-sm" },
        h("div", { style: "display:flex;align-items:center;gap:10px" }, h("span", { class: "run-dot" }), h("a", { href: desk.href(item.id), style: "font-size:15px;font-weight:600;text-decoration:none" }, item.title || "Untitled task"), hostChip(desk)),
        // A run comes from nodwyn run, a batch tool for scripts (ADR 35).
        h("div", { class: "card-sub", style: "padding-left:18px" }, run ? `${run.unattended ? "Unattended run" : "Run"} from nodwyn run · ${desk.folderName(desk.project(item))}` : `Working · ${desk.folderName(desk.project(item))}`),
      ),
    );
    if (run) {
      const deadline = run.deadline && !run.deadline.startsWith("0001-") ? new Date(run.deadline).getTime() : null;
      const left = deadline === null ? null : deadline - Date.now();
      const meter = (label, value, pct) =>
        h("div", { class: "meter" }, h("div", { class: "meter-head" }, h("span", {}, label), h("span", {}, value)), h("div", { class: "meter-track" }, h("div", { class: "meter-fill", style: `width:${Math.max(0, Math.min(100, pct))}%` })));
      card.append(h("div", { class: "meters" }, meter("Turns", run.max_turns > 0 ? `${run.turns} / ${run.max_turns}` : `${run.turns} · no limit`, run.max_turns > 0 ? (run.turns / run.max_turns) * 100 : 0), meter("Time left", left === null ? "no limit" : left > 0 ? minutes(left) : "expired", 0)));
    }
    card.append(h("div", { class: "card-foot" }, icon("folder", 15), h("span", { class: "mono", style: "color:var(--ink)" }, shortPath(desk.project(item), desk.home)), item.worktree ? h("span", { class: "mono branch" }, item.worktree.branch) : null, h("span", { style: "margin-left:auto" }, `active ${ago(item.last_event)}`)));
    return card;
  }
}
