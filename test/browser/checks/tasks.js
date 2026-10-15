// The task list (ADR 39): tasks under their folders, the folder last active
// first; a task renamed, archived and brought back, and deleted, each kept
// across a reload, which reads the list from the daemon again.
const assert = require("assert");
const { out, start: run, line, launch, watch, requests, until, check } = require("../lib");

check(async () => {
  const demo = run(process.env.DEMO, ["-seed", "-pace=0"]);
  const step = (m) => console.log("step:", m);
  const [url] = await line(demo, /URL (\S+)/);
  const [consoleRepo] = await line(demo, /REPO (\S+)/);
  const repos = { console: consoleRepo, receipts: consoleRepo.replace(/\/console$/, "/receipts") };
  const browser = await launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = watch(await ctx.newPage());
  const sent = requests(page);
  const fail = (why) => {
    throw new Error(why);
  };
  const ready = () => page.waitForSelector("html[data-ready]", { timeout: 15000 });
  // What the sidebar shows: the tasks waiting for an answer, each folder with
  // its tasks in order, and the archive.
  const sidebar = () =>
    page.evaluate(() => {
      const titles = (el) => [...el.querySelectorAll(".nav-task .title span:last-child")].map((s) => s.textContent);
      const needs = [...document.querySelectorAll(".sidebar > .nav-group:not(.folder)")].flatMap(titles);
      const folders = [...document.querySelectorAll(".folders .folder")].map((g) => ({
        name: g.querySelector(".folder-head .name").textContent,
        open: g.querySelector(".folder-head").getAttribute("aria-expanded") === "true",
        tasks: titles(g),
        more: g.querySelector(".nav-more")?.textContent || "",
      }));
      const a = document.querySelector(".nav-group.archived");
      return { needs, folders, archived: a ? { n: a.querySelector(".folder-head .n").textContent, tasks: titles(a) } : null };
    });
  // settled waits until the sidebar shows what ok wants, and returns it.
  const settled = async (what, ok) => {
    let last;
    await until(what, async () => ok((last = await sidebar()))).catch((e) => fail(`${e.message}: ${JSON.stringify(last)}`));
    return last;
  };
  const folder = (s, name) => s.folders.find((f) => f.name === name);
  const idle = () => page.waitForFunction(() => document.querySelector(".task-head .pill")?.textContent === "Idle", null, { timeout: 15000 });
  const start = async (where, text) => {
    await page.evaluate(() => (location.hash = "#/"));
    await page.click(`button.chip[title="${repos[where]}"]`);
    await page.fill("#task", text);
    await page.press("#task", "Control+Enter");
    await page.waitForFunction(() => location.hash.startsWith("#/task/"));
    await page.waitForSelector(".task-head h1");
    await idle();
  };
  const openTask = async (title) => {
    const row = `.sidebar .nav-task:has(.title span:text-is("${title}"))`;
    // Each Show more redraws the sidebar, so each is found again.
    while (!(await page.$(row)) && (await page.$(".nav-more"))) await page.click(".nav-more");
    await page.click(row);
    await page.waitForFunction((t) => document.querySelector(".task-head h1")?.textContent === t, title);
  };
  const button = (label) => page.click(`.task-actions button:text-is("${label}")`);

  await page.goto(url);
  await ready();
  // A refresh of the list that a newer one overtakes waits for it: whoever
  // awaited the older finds what the newer list holds. A task just started
  // was otherwise "No such task" now and then. Drawn with the console's own
  // Desk, on a connection whose first list comes back after the second was
  // asked for.
  const overtaken = await page.evaluate(async () => {
    const { Desk } = await import("/desk.js");
    const lists = [];
    const conn = { has: () => false, onEvent: () => {}, onResult: () => {}, call: (method) => (method === "session.list" ? new Promise((resolve) => lists.push(resolve)) : Promise.resolve([])) };
    const desk = new Desk(conn);
    const older = desk.refresh();
    desk.refresh();
    let seen = null;
    older.then(() => (seen = desk.sessions.has("new")));
    lists[0]([]);
    await new Promise((r) => setTimeout(r, 0));
    const before = seen;
    lists[1]([{ id: "new" }]);
    await older;
    return { before, seen };
  });
  // Nothing before the newer list came back; then the new task.
  assert.deepStrictEqual(overtaken, { before: null, seen: true }, "an overtaken refresh did not wait for the newer list");
  let s = await settled("the seeded tasks", (s) => s.needs.length === 1 && folder(s, "receipts")?.tasks.length === 1);
  step(`seeded: ${JSON.stringify(s)}`);

  // Five more tasks in receipts, then one in console: console's folder, the
  // latest active, comes first, and receipts shows five of its six.
  for (let i = 1; i <= 5; i++) await start("receipts", `Receipt question ${i}`);
  await start("console", "Hello from the console");
  s = await settled("folders by activity", (s) => s.folders.map((f) => f.name).join() === "console,receipts" && folder(s, "receipts").tasks.length === 5);
  if (folder(s, "receipts").more !== "Show 1 more" || folder(s, "receipts").tasks[0] !== "Receipt question 5") await fail(`receipts: ${JSON.stringify(folder(s, "receipts"))}`);
  await page.screenshot({ path: `${out}/tasks-grouped.png` });
  await page.click('.folder:has(.name:text-is("receipts")) .nav-more');
  await settled("show more", (s) => folder(s, "receipts").tasks.length === 6 && !folder(s, "receipts").more);
  // A message in an older task makes its folder the latest active again.
  await openTask("Summarize what is in this folder");
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await idle();
  s = await settled("receipts first after a message", (s) => s.folders.map((f) => f.name).join() === "receipts,console" && folder(s, "receipts").tasks[0] === "Summarize what is in this folder");
  step("ordered by last activity");

  // Rename from the heading: Escape keeps the name, Enter saves it.
  await page.click(".rename-btn");
  await page.fill(".title-input", "Not this");
  await page.press(".title-input", "Escape");
  if ((await page.textContent(".task-head h1")) !== "Summarize what is in this folder") await fail("Escape renamed the task");
  await page.click(".rename-btn");
  await page.fill(".title-input", "Receipts overview");
  await page.press(".title-input", "Enter");
  await settled("renamed in the sidebar", (s) => folder(s, "receipts").tasks[0] === "Receipts overview");
  if ((await page.textContent(".task-head h1")) !== "Receipts overview") await fail("heading not renamed");
  const renamed = sent.of("session.rename");
  assert.deepStrictEqual(renamed.map((r) => r.title), ["Receipts overview"], "Escape sent no rename");
  await page.screenshot({ path: `${out}/tasks-renamed.png` });
  step("renamed");

  // A task picked from the sidebar takes the focus to its message box,
  // ready for a word, and a word typed goes to that task. Coming back to a
  // kept task does the same, and a word not sent is kept.
  await page.fill("#reply", "Not sent yet");
  const replyFocused = () => page.waitForFunction(() => document.activeElement?.id === "reply", null, { timeout: 15000 });
  await openTask("Hello from the console");
  await replyFocused().catch((e) => fail(`focus not in the message box when switching to the console task: ${e.message}`));
  await page.screenshot({ path: `${out}/tasks-switch-focus.png` });
  await page.fill("#reply", "Still here");
  await page.keyboard.press("Enter");
  await idle();
  if (sent.of("session.send").at(-1)?.text !== "Still here") await fail(`the word typed on a switched task went somewhere else: ${JSON.stringify(sent.of("session.send").at(-1))}`);
  s = await settled("console first after the reply", (s) => s.folders.map((f) => f.name).join() === "console,receipts");
  if (!folder(s, "console").tasks.includes("Hello from the console")) await fail(`console: ${JSON.stringify(folder(s, "console"))}`);
  await openTask("Receipts overview");
  await replyFocused().catch((e) => fail(`focus not in the message box when coming back to the kept task: ${e.message}`));
  if ((await page.inputValue("#reply")) !== "Not sent yet") await fail("the word not sent was lost by the switch");
  await page.evaluate(() => (document.getElementById("reply").value = ""));
  await idle();
  step("the message box is focused on the task you switch to");

  // Archive: an open task is closed first; then it leaves its folder for
  // the archive, and Unarchive brings it back.
  if (await page.$('.task-actions button:text-is("Archive")')) await fail("an open task offers Archive");
  await button("Close");
  await page.waitForSelector('.task-actions button:text-is("Archive")');
  await button("Archive");
  s = await settled("archived", (s) => !folder(s, "receipts").tasks.includes("Receipts overview") && s.archived?.n === "1");
  if (!s.archived.tasks.includes("Receipts overview") || !(await page.$('.task-head .badge:text-is("archived")'))) await fail(`archive: ${JSON.stringify(s.archived)}`);
  assert.deepStrictEqual(sent.of("session.archive"), [{ id: renamed[0].id, archived: true }]);
  await page.screenshot({ path: `${out}/tasks-archived.png` });
  await page.reload();
  await ready();
  s = await settled("archived after reload", (s) => s.archived?.n === "1" && s.archived.tasks.includes("Receipts overview") && !folder(s, "receipts").tasks.includes("Receipts overview"));
  await button("Unarchive");
  s = await settled("unarchived", (s) => !s.archived && folder(s, "receipts").tasks.includes("Receipts overview"));
  assert.deepStrictEqual(sent.of("session.archive").map((a) => a.archived), [true, false]);
  step("archived and brought back");

  // Delete asks first; Cancel keeps the task, Delete removes it for good.
  await openTask("Receipt question 1");
  await button("Close");
  await page.waitForSelector('.task-actions button:text-is("Delete…")');
  await button("Delete…");
  await page.click('.delete-confirm button:text-is("Cancel")');
  if (await page.$(".delete-confirm")) await fail("Cancel left the question");
  assert.strictEqual(sent.of("session.delete").length, 0, "Cancel deleted");
  await button("Delete…");
  await page.screenshot({ path: `${out}/tasks-delete.png` });
  await page.click('.delete-confirm button:text-is("Delete")');
  await page.waitForFunction(() => location.hash === "#/");
  await settled("deleted", (s) => !folder(s, "receipts").tasks.includes("Receipt question 1"));
  assert.strictEqual(sent.of("session.delete").length, 1, "one delete");
  await page.reload();
  await ready();
  s = await settled("deleted after reload", (s) => folder(s, "receipts") && !folder(s, "receipts").tasks.includes("Receipt question 1") && folder(s, "receipts").tasks.includes("Receipts overview"));
  step("deleted");

  // Limits (ADR 49): ticked on Home for every new task, and a task started
  // with them ticked sends no choice. Unticked, the task opens with no turn
  // limits, which the daemon then reports (the heading's badge comes from
  // session.info), and the task's own tick turns them on again, and off,
  // between turns; a reload reads them from the daemon again.
  assert.ok(sent.of("session.open").length >= 6 && sent.of("session.open").every((p) => !("limits" in p)), `a task started with Limits ticked sent a choice: ${JSON.stringify(sent.of("session.open"))}`);
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#new-task-limits");
  assert.strictEqual(await page.isChecked("#new-task-limits"), true, "Limits starts unticked on Home");
  await page.click(`button.chip[title="${repos.receipts}"]`);
  await page.fill("#task", "Run as long as it needs");
  await page.uncheck("#new-task-limits");
  assert.ok(await page.$eval(".limits-toggle", (el) => el.classList.contains("on")), "unticked Limits does not read as a warning on Home");
  await page.screenshot({ path: `${out}/tasks-limits-home.png` });
  await page.press("#task", "Control+Enter");
  await page.waitForFunction(() => location.hash.startsWith("#/task/"));
  await idle();
  const unlimited = sent.of("session.open").at(-1);
  if (unlimited.repo !== repos.receipts || unlimited.limits !== false) await fail(`the task did not open with its limits off: ${JSON.stringify(unlimited)}`);
  await page.waitForSelector("#no-limits");
  const limitsTick = () => page.$eval(".limits-toggle:not(.worktree-toggle)", (el) => ({ shown: !el.hidden, checked: el.querySelector("input").checked, enabled: !el.querySelector("input").disabled }));
  await until("the task's tick shows its limits off", async () => JSON.stringify(await limitsTick()) === JSON.stringify({ shown: true, checked: false, enabled: true }));
  await page.screenshot({ path: `${out}/tasks-limits-off.png` });
  await page.click(".limits-toggle:not(.worktree-toggle) input");
  await until("the limits turned on", async () => !(await page.$("#no-limits")) && (await limitsTick()).checked && (await limitsTick()).enabled);
  assert.deepStrictEqual(sent.of("session.limits.set"), [{ id: sent.of("session.send").at(-1).id, on: true }], "one choice, for this task");
  await page.reload();
  await ready();
  await openTask("Run as long as it needs");
  await until("the limits read from the daemon after a reload", async () => (await limitsTick()).checked && (await limitsTick()).enabled);
  assert.strictEqual(await page.$("#no-limits"), null, "the badge came back after a reload with the limits on");
  await page.click(".limits-toggle:not(.worktree-toggle) input");
  await page.waitForSelector("#no-limits");
  assert.deepStrictEqual(sent.of("session.limits.set").map((p) => p.on), [true, false], "the choices sent");
  // Each choice, the one it opened with included, is a line in the
  // conversation, read back from the log.
  const limitsLines = () => page.$eval(".transcript", (el) => (el.textContent.match(/Limits: (on|off)/g) || []).join());
  await until("the choices in the conversation", async () => (await limitsLines()) === "Limits: off,Limits: on,Limits: off").catch(async (e) => fail(`${e.message}: ${await limitsLines()}`));
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#new-task-limits");
  assert.strictEqual(await page.isChecked("#new-task-limits"), true, "Home kept Limits unticked for the next task");
  step("a task's limits off from Home, then on and off again in the task");

  // A folded folder stays folded across a reload, in this browser.
  await page.click('.folder-head:has(.name:text-is("console"))');
  await settled("folded", (s) => !folder(s, "console").open && folder(s, "console").tasks.length === 0);
  await page.reload();
  await ready();
  await settled("folded after reload", (s) => folder(s, "console") && !folder(s, "console").open);
  await page.click('.folder-head:has(.name:text-is("console"))');
  await settled("unfolded", (s) => folder(s, "console").open && folder(s, "console").tasks.length === 1);
  // A search shows its matches in every folder, the archive included.
  await page.click('.sidebar-top button[aria-label="Search tasks"]');
  await page.fill(".search input", "question 2");
  await settled("search", (s) => s.folders.length === 1 && folder(s, "receipts").tasks.join() === "Receipt question 2");
  await page.screenshot({ path: `${out}/tasks-search.png` });

  // The same list at phone width, in the drawer.
  await page.fill(".search input", "");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.click("#menu");
  await page.waitForSelector(".shell.menu-open");
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState === "finished"));
  await page.screenshot({ path: `${out}/tasks-phone.png` });
});
