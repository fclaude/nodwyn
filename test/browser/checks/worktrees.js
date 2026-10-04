// Several tasks on one repository (ADR 34): each started "In a worktree"
// works in a git worktree of its own, on a branch of its own, and is listed
// under the repository; and switching between open tasks shows each as it
// was, with what happened while it was off screen, without reading its log
// again.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { out, start: run, line, launch, watch, requests, shows, until, check } = require("../lib");

check(async () => {
  const demo = run(process.env.DEMO, ["-pace=0"]);
  const step = (m) => console.log("step:", m);
  const [url] = await line(demo, /URL (\S+)/);
  const [repo] = await line(demo, /GIT (\S+)/);
  const browser = await launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = watch(await ctx.newPage());
  const sent = requests(page);
  const ready = (p = page) => p.waitForSelector("html[data-ready]", { timeout: 15000 });
  const idle = (p = page) => p.waitForFunction(() => document.querySelector(".task-head .pill")?.textContent === "Idle", null, { timeout: 15000 });
  const loaded = (p = page) => p.waitForSelector(".task[data-loaded]", { timeout: 15000 });
  const reads = () => sent.filter((r) => r.method === "session.subscribe" || r.method === "log.read").length;
  // The sidebar's folders, each with its tasks' titles.
  const folders = () =>
    page.evaluate(() => [...document.querySelectorAll(".folders .folder")].map((g) => ({ name: g.querySelector(".folder-head .name").textContent, tasks: [...g.querySelectorAll(".nav-task .title span:last-child")].map((s) => s.textContent) })));
  const openTask = async (p, title) => {
    await p.click(`.sidebar .nav-task:has(.title span:text-is("${title}"))`);
    await p.waitForFunction((t) => document.querySelector(".task-head h1")?.textContent === t, title);
  };

  await page.goto(url);
  await ready();
  // The git folder, added on Home as a folder to work in.
  await page.click("button.chip.dashed");
  await page.fill("#add-path", repo);
  await page.fill("#add-name", "project");
  await page.click('.add-folder button:text-is("Add this folder")');
  await page.waitForSelector(`button.chip[title="${repo}"][aria-pressed="true"]`);
  await page.check(".worktree-toggle input");
  await page.screenshot({ path: `${out}/worktree-ask.png` });

  // Two tasks, each in a worktree of its own: the console asks for one with
  // worktree.open, never session.open.
  const startTask = async (text) => {
    await page.evaluate(() => (location.hash = "#/"));
    await page.waitForSelector(".worktree-toggle input");
    assert.ok(await page.isChecked(".worktree-toggle input"), "the folder's choice of a worktree was not kept");
    await page.fill("#task", text);
    await page.press("#task", "Control+Enter");
    await page.waitForFunction(() => location.hash.startsWith("#/task/"));
    await loaded();
    await idle();
    return decodeURIComponent(await page.evaluate(() => location.hash.split("/")[2]));
  };
  const first = await startTask("Write first");
  const second = await startTask("Write second");
  assert.deepStrictEqual(sent.of("worktree.open"), [{ repo }, { repo }], "the tasks were not started with worktree.open");
  assert.strictEqual(sent.of("session.open").length, 0, "a task was started in the folder itself");
  const branches = {};
  for (const id of [first, second]) branches[id] = `nodwyn/${id.slice(id.lastIndexOf("-") + 1)}`;
  // What each wrote is in its own worktree, and not in the repository.
  for (const [id, name] of [[first, "first"], [second, "second"]]) {
    const written = path.join(repo, ".nodwyn", "worktrees", branches[id].slice("nodwyn/".length), "notes", `${name}.md`);
    assert.strictEqual(fs.readFileSync(written, "utf8"), `Write ${name}\n`, `${written}`);
    assert.ok(!fs.existsSync(path.join(repo, "notes", `${name}.md`)), `${name}.md reached the repository's own checkout`);
  }
  await shows(page, ".task-head .task-meta", "project", branches[second]);
  await shows(page, ".transcript", `Working in a git worktree of its own, on ${branches[second]}, from main`);
  // Adding the folder wrote nothing into it (ADR 22): the repository has
  // nothing untracked, so nothing is said to be left behind.
  assert.ok(!fs.existsSync(path.join(repo, ".nodwyn", "workspace.toml")), "naming the folder wrote a workspace file into it");
  assert.ok(!(await page.innerText(".transcript")).includes("uncommitted"), "something was counted as left behind");
  // The workspace's details live in the Workspace tab.
  await page.click(".task-aside [role=tab]:has-text('Workspace')");
  await shows(page, ".task-aside", "Branch", branches[second], `Its changes stay on ${branches[second]} until it is merged into main`);
  await page.screenshot({ path: `${out}/worktree-task.png` });
  // Both are listed under the repository, which Home offers once.
  const listed = await until("both tasks under project", async () => {
    const f = await folders();
    return f.length === 1 && f[0].name === "project" && f[0].tasks.length === 2 && f;
  });
  step(`listed: ${JSON.stringify(listed)}`);
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector(".work-in");
  const chips = await page.$$eval(".work-in button.chip:not(.dashed)", (bs) => bs.map((b) => b.title));
  assert.deepStrictEqual(chips, [repo], "Home lists a worktree as a folder of its own");
  step("two tasks, two worktrees, one folder");

  // Switching: each open task's view is kept with its conversation, and
  // nothing reads it again.
  await openTask(page, "Write first");
  await page.evaluate(() => (document.querySelector(".task .transcript").dataset.kept = "first"));
  // A key typed into a request card is cleared by switching tasks, kept view
  // or not. Only Scheme a person evaluates asks for one, which the demo
  // cannot, so the field stands in for the card's.
  await page.evaluate(() => {
    const field = document.createElement("input");
    field.type = "password";
    field.id = "typed-key";
    field.value = "sk-typed-but-not-sent";
    document.querySelector(".task .transcript").append(field);
  });
  const before = reads();
  await openTask(page, "Write second");
  await openTask(page, "Write first");
  assert.strictEqual(await page.inputValue("#typed-key"), "", "a key typed in a task outlived switching away from it");
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector(".work-in");
  await openTask(page, "Write first");
  assert.strictEqual(reads(), before, "switching back to an open task read its log again");
  assert.strictEqual(await page.evaluate(() => document.querySelector(".task .transcript")?.dataset.kept), "first", "the task's conversation was drawn again");
  // While the first task is off screen, another window sends it a message:
  // its kept view draws the turn as it comes, and shows it on return.
  await openTask(page, "Write second");
  const other = watch(await ctx.newPage(), "other");
  await other.goto(url.replace(/#.*$/, "") + `#/task/${encodeURIComponent(first)}`);
  await ready(other);
  await loaded(other);
  await other.fill("#reply", "Thanks");
  await other.keyboard.press("Enter");
  await other.waitForFunction(() => document.querySelector(".transcript")?.innerText.includes("Glad that helps"), null, { timeout: 15000 });
  await idle(other);
  await other.close();
  await openTask(page, "Write first");
  await shows(page, ".transcript", "Thanks", "Glad that helps");
  assert.strictEqual(reads(), before, "a kept view read its log again to show what happened while it was away");
  assert.strictEqual(await page.evaluate(() => document.querySelector(".task .transcript")?.dataset.kept), "first", "the task's conversation was drawn again");
  await idle();
  step("switched without reading again");

  // A closed task is read from its log, as before; its worktree, which holds
  // what it wrote, stays.
  await page.click('.task-actions button:text-is("Close")');
  await page.waitForSelector('.task-actions button:text-is("Resume")');
  await openTask(page, "Write second");
  const closedReads = reads();
  await openTask(page, "Write first");
  await loaded();
  assert.ok(reads() > closedReads, "a closed task's conversation was not read from its log");
  assert.ok(fs.existsSync(path.join(repo, ".nodwyn", "worktrees", branches[first].slice("nodwyn/".length), "notes", "first.md")), "closing removed a worktree with work in it");
  await page.screenshot({ path: `${out}/worktree-closed.png` });
});
