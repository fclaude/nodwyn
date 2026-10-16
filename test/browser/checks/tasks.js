// The task list (ADR 39): tasks under their folders, the folder last active
// first; a task renamed, archived and brought back, and deleted, each kept
// across a reload, which reads the list from the daemon again.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { out, requests, until, step, ready, desk, check } = require("../lib");

check(async () => {
  const { url, repo: consoleRepo, ctx, page } = await desk();
  const repos = { console: consoleRepo, receipts: consoleRepo.replace(/\/console$/, "/receipts") };
  const sent = requests(page);
  const fail = (why) => {
    throw new Error(why);
  };
  // What the sidebar shows: the tasks waiting for an answer, each folder with
  // its tasks in order, and the archive.
  const sidebar = () =>
    page.evaluate(() => {
      const titles = (el) => [...el.querySelectorAll(".nav-task .title span:last-child")].map((s) => s.textContent);
      const needs = [...document.querySelectorAll(".sidebar .nav-tasks > .nav-group:not(.folder)")].flatMap(titles);
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
    await until(what, async () => ok((last = await sidebar())), () => last);
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
  const locationID = (url) => decodeURIComponent(url.split("#/task/")[1]);
  const button = (label) => page.click(`.task-actions button:text-is("${label}")`);

  await page.goto(url);
  await ready(page);
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
  // The reply box keeps the focus while the message is on its way, so the
  // next message is typed straight in and no key lands on the page, where a
  // bare y or n answered the next request card. The page holds the message
  // back 400 ms, as a daemon further away would: the demo answers within a
  // frame, before the browser moves the focus off a disabled field.
  await page.evaluate(() => {
    const send = WebSocket.prototype.send;
    WebSocket.prototype.send = function (data) {
      if (typeof data === "string" && data.includes('"session.send"')) {
        WebSocket.prototype.send = send;
        setTimeout(() => send.call(this, data), 400);
        return;
      }
      return send.call(this, data);
    };
  });
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await page.waitForTimeout(200);
  assert.strictEqual(await page.evaluate(() => document.activeElement?.id || document.activeElement?.tagName), "reply", "the reply box lost the focus while the message was on its way");
  await idle();
  s = await settled("receipts first after a message", (s) => s.folders.map((f) => f.name).join() === "receipts,console" && folder(s, "receipts").tasks[0] === "Summarize what is in this folder");
  step("ordered by last activity");
  // Totals are requested through the real bridge after the reply and survive
  // reopening. The turn count also proves this is the indexed response.
  await page.click('#aside-tab-statistics');
  const totalsTurns = () => page.locator('[data-totals] .kv').filter({ has: page.locator('span:text-is("Turns")') }).textContent();
  await until("indexed totals after the reply", async () => (await totalsTurns()) === "Turns2");
  assert.ok(sent.of("session.totals").some((p) => p.id === locationID(page.url())), "the console never requested session.totals");
  const totalsBeforeReload = await page.locator('[data-totals]').textContent();
  await page.reload();
  await ready(page);
  await page.waitForSelector('.task[data-loaded]');
  await page.click('#aside-tab-statistics');
  assert.strictEqual(await page.locator('[data-totals]').textContent(), totalsBeforeReload, "reopening changed the indexed totals");
  await page.screenshot({ path: `${out}/tasks-indexed-totals.png` });


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
  await ready(page);
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
  await ready(page);
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
  await ready(page);
  await openTask("Run as long as it needs");
  await until("the limits read from the daemon after a reload", async () => (await limitsTick()).checked && (await limitsTick()).enabled);
  assert.strictEqual(await page.$("#no-limits"), null, "the badge came back after a reload with the limits on");
  await page.click(".limits-toggle:not(.worktree-toggle) input");
  await page.waitForSelector("#no-limits");
  assert.deepStrictEqual(sent.of("session.limits.set").map((p) => p.on), [true, false], "the choices sent");
  // Each choice, the one it opened with included, is a line in the
  // conversation, read back from the log.
  const limitsLines = () => page.$eval(".transcript", (el) => (el.textContent.match(/Limits: (on|off)/g) || []).join());
  await until("the choices in the conversation", async () => (await limitsLines()) === "Limits: off,Limits: on,Limits: off", limitsLines);
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#new-task-limits");
  assert.strictEqual(await page.isChecked("#new-task-limits"), true, "Home kept Limits unticked for the next task");
  step("a task's limits off from Home, then on and off again in the task");

  // Only the task list scrolls: Settings and the model's state stay in view
  // beside a list longer than the window, and a redraw keeps the list's place.
  await page.setViewportSize({ width: 1440, height: 520 });
  const frame = () =>
    page.evaluate(() => {
      const list = document.querySelector(".nav-tasks"), side = document.querySelector("#sidebar");
      const bottom = (sel) => document.querySelector(sel).getBoundingClientRect().bottom;
      return { scrolls: list.scrollHeight > list.clientHeight, sideOverflow: side.scrollHeight - side.clientHeight, links: bottom(".nav-links"), model: bottom(".model-line"), height: innerHeight, top: list.scrollTop };
    });
  const long = await frame();
  assert.ok(long.scrolls, `the task list does not scroll: ${JSON.stringify(long)}`);
  assert.ok(long.sideOverflow <= 0 && long.links <= long.height && long.model <= long.height, `the places or the model's state scrolled away: ${JSON.stringify(long)}`);
  await page.evaluate(() => (document.querySelector(".nav-tasks").scrollTop = 60));
  await page.evaluate(() => dispatchEvent(new HashChangeEvent("hashchange")));
  assert.strictEqual((await frame()).top, 60, "a redraw moved the task list");
  await page.setViewportSize({ width: 1440, height: 1000 });
  step("only the task list scrolls in the sidebar");
  // A folded folder stays folded across a reload, in this browser.
  await page.click('.folder-head:has(.name:text-is("console"))');
  await settled("folded", (s) => !folder(s, "console").open && folder(s, "console").tasks.length === 0);
  await page.reload();
  await ready(page);
  await settled("folded after reload", (s) => folder(s, "console") && !folder(s, "console").open);
  await page.click('.folder-head:has(.name:text-is("console"))');
  await settled("unfolded", (s) => folder(s, "console").open && folder(s, "console").tasks.length === 1);
  // A search shows its matches in every folder, the archive included.
  // It is typed key by key, as a person types: the sidebar is drawn again
  // on each key, and the field keeps its focus and caret through that (each
  // key used to land before the last, and Home took the focus).
  await page.click('.sidebar-top button[aria-label="Search tasks"]');
  await page.keyboard.type("question 2", { delay: 15 });
  await settled("search", (s) => s.folders.length === 1 && folder(s, "receipts").tasks.join() === "Receipt question 2");
  assert.strictEqual(await page.$eval(".search input", (el) => el.value), "question 2", "the search did not read as typed");
  assert.ok(await page.$eval(".search input", (el) => el === document.activeElement), "the search lost the focus while typing");
  await page.screenshot({ path: `${out}/tasks-search.png` });

  // A saved task with eight driver runs opens at the latest one. Older
  // runs are previews until expanded, and both directions retain fixed ends.
  // Escape clears the search and closes it.
  await page.keyboard.press("Escape");
  await page.waitForSelector(".search input", { state: "detached" });
  const sessions = path.join(path.dirname(path.dirname(consoleRepo)), "data", "sessions");
  const sample = fs.readdirSync(sessions).filter((f) => f.endsWith(".jsonl")).flatMap((f) => fs.readFileSync(path.join(sessions, f), "utf8").trim().split("\n").map(JSON.parse)).find((e) => e.kind === "session.start");
  const historyID = "browser-run-history", history = [], boundaries = [];
  const record = (kind, payload) => history.push({ seq: history.length + 1, ts: new Date(Date.UTC(2026, 9, 14, 12, 0, history.length)).toISOString(), session: historyID, kind, payload });
  record("session.start", { ...sample.payload, repo: consoleRepo });
  record("session.title", { title: "History paging" });
  for (let n = 1; n <= 8; n++) {
    boundaries.push(history.length + 1);
    record("run.state", { task: "History paging", status: "running", turns: 0, max_turns: 1 });
    record("run.turn", { number: 1 });
    record("user.msg", { text: `History request ${n}` });
    if (n >= 7) {
      record("session.model", { model: n === 7 ? "historic-only" : "demo", ...(n === 8 ? { name: "control" } : {}) });
      for (let j = 0; j < 550; j++) record("guard.decision", { prim: "fs-read", origin: "model", decision: "allow" });
    }
    record("llm.resp", { content: `Final reply ${n} <script>not markup</script>`, usage: { prompt_tokens: 100, cached_tokens: 50, cost: 0.01 } });
    record("turn.end", { outcome: "completed" });
    record("run.state", { task: "History paging", status: "completed", turns: 1, max_turns: 1 });
  }
  fs.writeFileSync(path.join(sessions, `${historyID}.jsonl`), history.map((e) => JSON.stringify(e)).join("\n") + "\n", { mode: 0o600 });
  await page.reload(); await ready(page);
  const begin = sent.of("log.read").length;
  await page.evaluate((id) => location.hash = `#/task/${id}`, historyID);
  await page.waitForSelector('.task[data-loaded]');
  assert.deepStrictEqual(await page.locator('.transcript > .turn.user .you-text').allTextContents(), ["History request 8"], "the initial conversation includes an earlier run");
  const initialReads = sent.of("log.read").slice(begin);
  assert.ok(initialReads[0].latest_run && initialReads.slice(1).every((p) => p.after >= boundaries[7]), "initial log pages started before the latest run");
  await page.click('#aside-tab-statistics');
  assert.ok((await page.locator('[data-totals]').textContent()).includes("Turns8Runs8"), "whole-task totals were narrowed to the latest run");
  const beforeOlder = sent.of("log.read").length;
  await page.click('button.earlier-runs');
  await page.waitForSelector('.earlier-run');
  assert.strictEqual(await page.locator('.earlier-run').count(), 5, "one page of earlier previews");
  assert.strictEqual(sent.of("log.read").length, beforeOlder, "fetching previews fetched their transcripts");
  assert.ok((await page.locator('.earlier-run').last().textContent()).includes("Final reply 7 <script>not markup</script>"), "the collapsed run lost its last reply");
  const newestOld = page.locator('.earlier-run').last();
  await newestOld.locator(':scope > summary').click();
  await newestOld.locator('.run-transcript .you-text').waitFor();
  assert.strictEqual(await newestOld.locator('.run-transcript .you-text').textContent(), "History request 7", "expansion loaded the wrong range");
  const historicalReads = sent.of("log.read").slice(beforeOlder);
  assert.ok(historicalReads.length >= 2 && historicalReads.every((p) => p.after >= boundaries[6] - 1 && p.through === boundaries[7] - 1), "older pagination escaped its run");
  assert.strictEqual(await newestOld.locator('script').count(), 0, "a reply preview became markup");
  await newestOld.locator(':scope > summary').click();
  const closedReads = sent.of("log.read").length;
  await newestOld.locator(':scope > summary').click();
  await page.waitForTimeout(50);
  assert.strictEqual(sent.of("log.read").length, closedReads, "reopening reread a loaded run");
  await page.click('button.earlier-runs');
  await until("all earlier runs", async () => await page.locator('.earlier-run').count() === 7);
  assert.strictEqual(await page.isVisible('button.earlier-runs'), false, "the start still offers more runs");
  await page.screenshot({ path: `${out}/tasks-earlier-runs.png` });
  // Resuming establishes a latest-run subscription, then a new message is
  // delivered once while older run panels remain independent.
  await button("Resume");
  await page.waitForSelector('.task[data-loaded]');
  assert.strictEqual(sent.of("session.subscribe").at(-1).latest_run, true, "resume subscribed from the beginning");
  await page.fill('#reply', "Thanks"); await page.press('#reply', 'Enter'); await idle();
  await until("live reply after latest-run load", async () => (await page.locator('.transcript > .turn.agent').last().textContent()).includes("Glad that helps"));
  assert.strictEqual(await page.locator('.transcript > .turn.user .you-text').count(), 2, "live delivery lost or duplicated a user message");
  await page.reload(); await ready(page); await page.waitForSelector('.task[data-loaded]');
  assert.strictEqual(await page.locator('.transcript > .turn.user .you-text').count(), 2, "reopening lost the human follow-up to the latest run");
  step("latest run, lazy earlier pages, fixed ranges and live delivery");

  // The reload above cleared the search; show the same list in the phone drawer.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.click("#menu");
  await page.waitForSelector(".shell.menu-open");
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState === "finished"));
  await page.screenshot({ path: `${out}/tasks-phone.png` });
  await page.click("#scrim", { position: { x: 380, y: 100 } });
  await page.click("button.earlier-runs");
  await until("phone previews", async () => await page.locator('.earlier-run').count() === 5);
  const phoneRun = page.locator('.earlier-run').last();
  await phoneRun.locator(':scope > summary').click();
  await phoneRun.locator('.run-transcript .you-text').waitFor();
  assert.strictEqual(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "earlier run history scrolls the phone sideways");
  await page.screenshot({ path: `${out}/tasks-earlier-runs-phone.png` });

});
