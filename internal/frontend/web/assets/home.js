// Home: start anything, and see what needs you across every task.
import { h, icon, repoName, shortPath, ago, minutes, diffStats, toast, basename } from "./dom.js";
import { summary } from "./prompts.js";
import { modelForm, keychainForm } from "./setup.js";
import { Attachments } from "./attach.js";

let chosen = ""; // the folder the next task starts in, for this tab
let draft = "";
let current = null;
let files = null;
let starting = false; // a start is in flight: one task per ask // the files chosen for the next task, kept across redraws

export function kindLabel(kind) {
  return { docs: "documents", code: "code", mixed: "code and documents" }[kind] || kind || "";
}

// chooseFolder preselects the folder the next task starts in.
export function chooseFolder(folder) {
  chosen = folder;
}

export function renderHome(root, ctx) {
  const { conn, desk, navigate, pickFolder } = ctx;
  const folders = desk.folders();
  if (!chosen || !folders.includes(chosen)) chosen = chosen && !folders.includes(chosen) && chosen.startsWith("/") ? chosen : folders[0] || "";
  const control = desk.control();
  const canStart = conn.has("session.open") && conn.has("session.send");

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
  const submit = h("button", { type: "submit", class: "send-round", "aria-label": "Start the task", disabled: !ready() }, icon("up", 18, 2));
  files ||= new Attachments();
  const where = control?.model ? `${control.model}${control.host ? ` · ${control.host}` : ""}` : "no model configured";
  const form = h(
    "form",
    {
      class: "ask",
      onsubmit: async (event) => {
        event.preventDefault();
        if (!ready()) return;
        starting = true;
        submit.disabled = true;
        try {
          const info = await conn.call("session.open", { repo: chosen });
          if (files.files.length) await files.upload(info.id);
          await conn.call("session.send", { id: info.id, text: task.value });
          draft = "";
          await desk.refresh();
          navigate(`#/task/${info.id}`);
        } catch (error) {
          toast(`${error.message} Nothing was retried; check the task list before starting again.`);
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
      h("button", { type: "button", class: "btn", title: chosen || "Choose a folder", onclick: () => document.getElementById("work-in")?.querySelector("button")?.focus() }, icon("folder", 16), h("strong", { style: "font-weight:500" }, chosen ? desk.folderName(chosen) : "Choose a folder"), chosen && desk.workspace(chosen) ? h("span", { class: "muted" }, kindLabel(desk.workspace(chosen).kind)) : null),
      files.button("Attach"),
      h("span", { class: "where" }, where),
      submit,
    ),
  );

  const chips = h("div", { class: "work-in", id: "work-in" }, h("span", {}, "Work in"));
  for (const folder of [chosen, ...folders.filter((f) => f !== chosen)].filter(Boolean).slice(0, 6)) {
    const ws = desk.workspace(folder);
    chips.append(
      h(
        "button",
        { type: "button", class: "chip", "aria-pressed": String(folder === chosen), title: folder, onclick: () => { chosen = folder; renderHome(root, ctx); } },
        desk.folderName(folder),
        ws ? h("span", { class: "sub" }, kindLabel(ws.kind)) : null,
      ),
    );
  }
  // Adding a folder names it and says what kind of work it holds; where the
  // console may save workspaces, the folder joins the list for next time.
  const adding = h("form", { class: "add-folder card", hidden: true });
  const pathInput = h("input", { class: "input", id: "add-path", placeholder: "/absolute/path/to/a/folder", spellcheck: false, autocomplete: "off", required: true });
  const nameInput = h("input", { class: "input", id: "add-name", placeholder: "for example Receipts 2026", autocomplete: "off" });
  let kind = "docs";
  const kindChoice = (id, label) =>
    h("button", { type: "button", class: "chip", "aria-pressed": String(kind === id), onclick: (e) => { kind = id; for (const b of e.target.parentElement.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b === e.target)); } }, label);
  const addError = h("p", { class: "error", role: "alert" });
  const canSave = conn.has("workspace.save");
  adding.append(
    h("div", { class: "field" }, h("label", { for: "add-path" }, "Folder"), h("div", { class: "row-actions" }, pathInput, pickFolder ? h("button", { type: "button", class: "btn", onclick: async () => { const p = await pickFolder(); if (p) { pathInput.value = p; if (!nameInput.value) nameInput.value = p.split("/").pop(); } } }, "Choose…") : null)),
    canSave ? h("div", { class: "field" }, h("label", { for: "add-name" }, "Name"), nameInput) : null,
    canSave ? h("div", { class: "field" }, h("label", {}, "What's in it"), h("div", { class: "row-actions" }, kindChoice("docs", "Documents"), kindChoice("code", "Code"), kindChoice("mixed", "Both"))) : null,
    canSave ? h("p", { class: "hint" }, "Documents: nodwyn reads, writes and organizes files, runs no programs, and keeps the previous version of a file before changing it. Code: nodwyn can also run programs; git is the undo.") : null,
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
        await conn.call("workspace.save", { path, name: nameInput.value.trim(), kind, mounts: [] });
        await desk.refreshWorkspaces();
      } catch (e) {
        addError.textContent = e.message;
        return;
      }
    }
    chosen = path;
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
          h("div", { class: "card-sub" }, `${item.title || "Untitled task"} · ${desk.folderName(item.repo)}`),
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
                item.changes?.length ? [icon("file", 14), h("span", { class: "mono" }, basename(item.changes[0])), h("span", {}, `${item.changes.length > 1 ? ` and ${item.changes.length - 1} more` : ""} · ${desk.folderName(item.repo)}`)] : [icon("folder", 14), h("span", {}, `No files changed · ${desk.folderName(item.repo)}`)],
              ),
            ),
          ),
        ),
      ),
    );

  const grid = h("div", { class: "home-grid" }, needs, h("div", {}, running, done));
  // The ask being typed survives updates: while focus is in the top half,
  // only the lists below are replaced.
  const main = root.querySelector(":scope > main.home");
  const keepTop = main && ctx.keepTop && main.firstChild?.contains(document.activeElement);
  if (keepTop) {
    main.classList.toggle("urgent", waiting.length > 0);
    main.replaceChild(grid, main.lastChild);
    const label = main.querySelector(".ask-bar .where");
    if (label) label.textContent = where;
    return current;
  }
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
        h("div", { class: "card-sub", style: "padding-left:18px" }, run ? `Supervised run · ${desk.folderName(item.repo)} · asks at each checkpoint` : `Working · ${desk.folderName(item.repo)}`),
      ),
    );
    if (run) {
      const left = new Date(run.deadline).getTime() - Date.now();
      const meter = (label, value, pct) =>
        h("div", { class: "meter" }, h("div", { class: "meter-head" }, h("span", {}, label), h("span", {}, value)), h("div", { class: "meter-track" }, h("div", { class: "meter-fill", style: `width:${Math.max(0, Math.min(100, pct))}%` })));
      card.append(h("div", { class: "meters" }, meter("Turns", `${run.turns} / ${run.max_turns}`, (run.turns / run.max_turns) * 100), meter("Time left", left > 0 ? minutes(left) : "expired", 0)));
    }
    card.append(h("div", { class: "card-foot" }, icon("folder", 15), h("span", { class: "mono", style: "color:var(--ink)" }, shortPath(item.repo, desk.home)), h("span", { style: "margin-left:auto" }, `active ${ago(item.last_event)}`)));
    return card;
  }
}
