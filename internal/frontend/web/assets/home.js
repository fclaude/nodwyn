// Home: start anything, and see what needs you across every task.
import { h, icon, repoName, shortPath, ago, minutes, diffStats, toast, basename } from "./dom.js";
import { summary } from "./prompts.js";
import { modelForm, keychainForm } from "./setup.js";
import { Attachments, isImage } from "./attach.js";
import { keepDraft } from "./task.js";

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
// The folders whose next task starts in a git worktree of its own (ADR 34),
// this tab only: a choice made once for a folder holds for its next tasks.
const inWorktree = new Set();

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
  if (!chosen) chosen = localStorage.getItem(LAST_KEY) || desk.last || "";
  if (!chosen || !folders.includes(chosen)) chosen = chosen && !folders.includes(chosen) && chosen.startsWith("/") ? chosen : folders[0] || "";
  const control = desk.control();
  // Every task is a chat (ADR 35): a long request is a message like any
  // other, and goes on until it is done.
  const canStart = conn.has("session.open") && conn.has("session.send");
  const where = control?.model ? `${control.model}${control.host ? ` · ${control.host}` : ""}` : "no model configured";

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
    if (first?.kind === "prompt.confirm" && conn.has("session.answer")) {
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
          { style: "display:flex;flex-direction:column;gap:3px" },
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
          ),
        ),
      ),
    );

  const grid = h("div", { class: "home-grid" }, needs, h("div", {}, running, done));
  // The ask being typed survives updates: while focus is in the top half,
  // only the lists below are replaced. This is decided before the composer
  // is built, and the composer is built only for a full redraw: the form
  // carries the shared attach input and attachment list, and building it
  // while the old top stays would steal both out of the live form, leaving
  // an attachment on the page that cannot be seen or removed.
  const main = root.querySelector(":scope > main.home");
  const keepTop = main && ctx.keepTop && main.firstChild?.contains(document.activeElement);
  if (keepTop) {
    main.classList.toggle("urgent", waiting.length > 0);
    main.replaceChild(grid, main.lastChild);
    const label = main.querySelector(".ask-bar .where");
    if (label) label.textContent = where;
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
  const ready = () => !starting && canStart && conn.live && !desk.keysLocked && !!chosen && !!task.value.trim();
  const submit = h("button", { type: "submit", class: "send-round", "aria-label": "Start the task", disabled: true }, icon("up", 18, 2));
  files ||= new Attachments();
  const attach = files.button("Attach");
  // A task in a worktree works on its own checkout and branch, so several
  // can work on one repository without touching each other's files or the
  // one you work in. Only a daemon that offers worktree.open, and a bridge
  // that lets it through, shows the choice; a folder that is not a git
  // repository is refused with a sentence when the task starts.
  const worktree = () => conn.has("worktree.open") && inWorktree.has(chosen);
  const worktreeBox = conn.has("worktree.open") && chosen
    ? h(
        "label",
        { class: "worktree-toggle", title: "Start this task in a new git worktree of the folder, on a branch of its own: it does not touch the files you or other tasks are working on. Its changes stay on that branch until it is merged." },
        h("input", { type: "checkbox", checked: inWorktree.has(chosen), onchange: (event) => { if (event.target.checked) inWorktree.add(chosen); else inWorktree.delete(chosen); } }),
        h("span", {}, "In a worktree"),
      )
    : null;
  const form = h(
    "form",
    {
      class: "ask",
      onsubmit: async (event) => {
        event.preventDefault();
        if (!ready()) return;
        starting = true;
        submit.disabled = true;
        let opened = "",
          sending = false;
        try {
          const info = await conn.call(worktree() ? "worktree.open" : "session.open", { repo: chosen });
          opened = info.id;
          // An image waits for its own confirm, which the task's page shows.
          if (files.files.some(isImage)) navigate(`#/task/${info.id}`);
          if (files.files.length) await files.upload(info.id);
          sending = true;
          await conn.call("session.send", { id: info.id, text: task.value });
          draft = "";
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
      h("span", { class: "where" }, where),
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
        { type: "button", class: "chip", "aria-pressed": String(folder === chosen), title: folder, onclick: () => { setFolder(folder, desk); renderHome(root, ctx); } },
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

  const settingUp = !desk.configured && conn.has("settings.control.write");
  const locked = desk.keychain?.exists && !desk.keychain.unlocked && conn.has("keychain.unlock");
  const top = settingUp
    ? h(
        "div",
        { class: "home-col" },
        h("h1", {}, "Let's connect nodwyn to a model"),
        h("p", { class: "lede" }, "nodwyn thinks with a language model you choose: one running on this computer or your network, or a service you have a key for. Nothing is sent anywhere until you set this up."),
        h("div", { class: "card" }, modelForm(ctx, { onSaved: () => renderHome(root, { ...ctx, keepTop: false }) })),
      )
    : h(
        "div",
        { class: "home-col" },
        h("h1", {}, "What should we get done?"),
        locked ? h("details", { class: "callout", open: desk.keysLocked }, h("summary", {}, desk.keysLocked ? "Unlock your keys to start a task: the model needs one." : "Your saved keys are locked. Unlock them to use them in new tasks."), keychainForm(ctx, { onDone: () => renderHome(root, { ...ctx, keepTop: false }) })) : null,
        canStart ? form : h("div", { class: "callout plain" }, "This view can't start tasks. Use an interactive view or the terminal."),
        canStart ? chips : null,
        canStart ? adding : null,
      );

  // The composer on the page is this one now.
  files.onChange = () => (submit.disabled = !ready());
  root.replaceChildren(h("main", { class: waiting.length ? "home urgent" : "home" }, top, grid));
  if (!draft && canStart && document.activeElement === document.body) task.focus();
  current = { focus: () => task.focus() };
  return current;

  function runningCard(item) {
    const run = item.run?.status === "running" ? item.run : null;
    const card = h(
      "article",
      { class: "card" },
      h(
        "div",
        { style: "display:flex;flex-direction:column;gap:4px" },
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
