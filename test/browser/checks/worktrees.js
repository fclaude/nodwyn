// Several tasks on one repository (ADR 34): a git workspace starts with
// "In a worktree" checked; each task then works in a worktree of its own,
// on a branch of its own, and is listed under the repository. Switching
// between open tasks shows each as it was, with what happened while it was
// off screen, without reading its log again.
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
  assert.ok(await page.isChecked(".worktree-toggle input"), "a new git workspace did not default to a worktree");
  await page.screenshot({ path: `${out}/worktree-ask.png` });

  // Two tasks, each in a worktree of its own: the console asks for one with
  // worktree.open, never session.open.
  // settle waits for the task's turn to end; a task held by a question has
  // none to wait for.
  const startTask = async (text, settle = true, useWorktree = true) => {
    await page.evaluate(() => (location.hash = "#/"));
    await page.waitForSelector(".worktree-toggle input");
    assert.strictEqual(await page.isChecked(".worktree-toggle input"), useWorktree, `the folder's worktree choice was not kept (${useWorktree})`);
    await page.fill("#task", text);
    await page.press("#task", "Control+Enter");
    await page.waitForFunction(() => location.hash.startsWith("#/task/"));
    await loaded();
    if (settle) await idle();
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
  const tabsFit = async () => {
    for (const [viewport, panel] of [[1440, 344], [1100, 300]]) {
      await page.setViewportSize({ width: viewport, height: 1000 });
      const layout = await page.evaluate(() => {
        const side = document.querySelector(".task-aside");
        const bar = side.querySelector("[role=tablist]");
        const bounds = bar.getBoundingClientRect();
        return {
          width: side.getBoundingClientRect().width,
          tabs: [...bar.querySelectorAll("[role=tab]")].map((b) => ({ label: b.textContent, ...b.getBoundingClientRect().toJSON() })),
          left: bounds.left,
          right: bounds.right,
          overflow: bar.scrollWidth > bar.clientWidth,
        };
      });
      assert.strictEqual(layout.width, panel, `right panel width at ${viewport}px`);
      assert.strictEqual(layout.tabs.length, 5, "all five worktree tabs are present");
      assert.ok(layout.tabs.every((t) => t.top === layout.tabs[0].top && t.left >= layout.left && t.right <= layout.right + 1 && t.width > 0) && !layout.overflow, `Five tabs fit one row at ${panel}px: ${JSON.stringify(layout)}`);
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
  };
  await tabsFit();
  await shows(page, ".task-head .task-meta", "project", branches[second]);
  const worktreeNote = `Working in a git worktree of its own, on ${branches[second]}, from main`;
  const activity = page.locator(".activity-fold").filter({ hasText: worktreeNote });
  assert.strictEqual(await activity.getAttribute("open"), null, "the worktree note starts folded");
  await activity.locator(":scope > summary").click();
  await shows(page, ".transcript", worktreeNote);
  // Adding the folder wrote nothing into it (ADR 22): the repository has
  // nothing untracked, so nothing is said to be left behind.
  assert.ok(!fs.existsSync(path.join(repo, ".nodwyn", "workspace.toml")), "naming the folder wrote a workspace file into it");
  assert.ok(!(await page.innerText(".transcript")).includes("uncommitted"), "something was counted as left behind");
  // The workspace's details live in the Workspace tab.
  await page.click(".task-aside [role=tab]:has-text('Workspace')");
  await shows(page, ".task-aside", "Branch", branches[second], `Its changes stay on ${branches[second]} until it is merged into main`);
  await page.screenshot({ path: `${out}/worktree-task.png` });
  // Review: the task's diff since its base commit, comments written on it,
  // and one request that sends them all and clears them.
  await page.click(".task-aside [role=tab]:has-text('Review')");
  await page.waitForFunction(() => document.querySelector(".task-aside .review")?.innerText.includes("notes/second.md"), null, { timeout: 15000 });
  await shows(page, ".task-aside .review", "Write second", "1 file changed since main at");
  // The file's status is on show with its diff.
  assert.strictEqual(await page.locator(".review-file .badge").first().textContent(), "added");
  // A redraw (an event does one) that leaves the focus where it was: a tab
  // pressed by script does not take it.
  const redraw = () => page.evaluate(() => document.querySelector(".task-aside [role=tab][aria-selected=true]").click());
  const reviewed = page.locator(".review-file .diff-line", { hasText: "Write second" }).first();
  await reviewed.hover();
  await reviewed.locator(".add-comment").click();
  await page.fill(".review-form textarea", "Say mor");
  await page.locator(".review-form textarea").focus();
  await page.evaluate(() => document.querySelector(".review-form textarea").setSelectionRange(3, 3));
  await redraw();
  const typing = await page.evaluate(() => {
    const field = document.querySelector(".review-form textarea");
    return { focused: document.activeElement === field, at: field.selectionStart, value: field.value };
  });
  assert.deepStrictEqual(typing, { focused: true, at: 3, value: "Say mor" }, "a redraw took the focus or the caret from a comment being written");
  await page.click(".review-form button:text-is('Cancel')");
  assert.strictEqual(await page.locator(".review-form").count(), 0, "Cancel left the form open");
  assert.strictEqual(await page.locator(".review-comment").count(), 0, "Cancel kept a comment");
  await reviewed.hover();
  await reviewed.locator(".add-comment").click();
  await page.fill(".review-form textarea", "Say more here");
  await page.click(".review-form button:text-is('Add comment')");
  await page.click(".review-file .diff-head button:text-is('Comment on file')");
  await page.fill(".review-form textarea", "Name the file better");
  await page.click(".review-form button:text-is('Add comment')");
  await shows(page, ".task-aside [role=tab][aria-selected=true]", "Review (2)");
  await tabsFit();
  assert.strictEqual(sent.of("worktree.review").length, 0, "comments were sent before Send review");
  // A comment is taken back with Remove, and written again.
  await page.locator(".review-comment", { hasText: "Name the file better" }).locator("button:text-is('Remove')").click();
  await shows(page, ".task-aside [role=tab][aria-selected=true]", "Review (1)");
  await page.click(".review-file .diff-head button:text-is('Comment on file')");
  await page.fill(".review-form textarea", "Name the file better");
  await page.click(".review-form button:text-is('Add comment')");
  await shows(page, ".task-aside [role=tab][aria-selected=true]", "Review (2)");
  // The overall note keeps the focus and the caret across a redraw too.
  await page.fill("textarea[aria-label='Overall note']", "Mostly fine");
  await page.locator("textarea[aria-label='Overall note']").focus();
  await page.evaluate(() => document.querySelector("textarea[aria-label='Overall note']").setSelectionRange(4, 4));
  await redraw();
  const note = await page.evaluate(() => {
    const field = document.querySelector("textarea[aria-label='Overall note']");
    return { focused: document.activeElement === field, at: field.selectionStart, value: field.value };
  });
  assert.deepStrictEqual(note, { focused: true, at: 4, value: "Mostly fine" }, "a redraw took the focus or the caret from the overall note");
  await page.screenshot({ path: `${out}/worktree-review.png` });
  await page.click("button:text-is('Send review (2)')");
  await until("one review request", async () => sent.of("worktree.review").length === 1);
  const review = sent.of("worktree.review")[0];
  assert.strictEqual(review.id, second);
  // Only the number and the comment go to the task, never the line's text.
  assert.deepStrictEqual(review.comments, [
    { path: "notes/second.md", line: 1, text: "Say more here" },
    { path: "notes/second.md", line: 0, text: "Name the file better" },
  ]);
  assert.strictEqual(review.note, "Mostly fine");
  // They clear when the daemon has accepted the review, not when it is sent.
  await until("the comments cleared once the review was accepted", async () => (await page.locator(".review-comment").count()) === 0);
  assert.ok(sent.of("worktree.diff").length >= 1, "the diff was not asked for");
  await idle();
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
  // A task the daemon will not take a review for, because it is answering
  // something: it writes a file whose lines start with ++ and --, then asks to
  // run a command, which holds the turn.
  const held = await startTask("Hold the line", false);
  await page.waitForSelector(".ask-card", { timeout: 15000 });
  await page.click(".task-aside [role=tab]:has-text('Review')");
  await page.waitForFunction(() => (document.querySelector(".task-aside .review")?.innerText || "").includes("++count;"), null, { timeout: 15000 });
  // The diff numbers the lines as the file does: a line that starts with ++
  // or -- is a line of the file, not a header.
  const lines = await page.$$eval(".review-file .diff-line", (rows) => rows.map((r) => [r.querySelector(".n").textContent, r.querySelector(".t").textContent]));
  assert.deepStrictEqual(lines, [["", "⋯"], ["1", "Hold the line"], ["2", "++count;"], ["3", "-- note"], ["4", "tail"]].slice(0, lines.length), "lines that start with ++ or -- were taken for headers, and the numbers after them shifted");
  assert.strictEqual(lines.length, 5, `the diff's lines: ${JSON.stringify(lines)}`);
  const tail = page.locator(".review-file .diff-line", { hasText: "tail" }).first();
  await tail.hover();
  await tail.locator(".add-comment").click();
  await page.fill(".review-form textarea", "Drop this tail");
  await page.click(".review-form button:text-is('Add comment')");
  // Send waits for the task: the button is disabled and says why, and no
  // request is made that the daemon would refuse.
  assert.ok(await page.locator("button:text-is('Send review (1)')").isDisabled(), "Send review was offered while the task was working");
  await shows(page, ".task-aside .review", "The task is working: send when it is idle.");
  assert.strictEqual(sent.of("worktree.review").length, 1, "a review was sent to a working task");
  // The turn ends: the diff is read again without a Refresh, and Send is back.
  const diffs = sent.of("worktree.diff").length;
  await page.evaluate(() => document.activeElement?.blur());
  await page.keyboard.press("y");
  await idle();
  await until("the diff read again after the turn", async () => sent.of("worktree.diff").length > diffs);
  await until("Send review offered again", async () => page.locator("button:text-is('Send review (1)')").isEnabled());

  // A comment whose line the file no longer has is not hidden: it is listed
  // under its own heading, still counted, and can be removed.
  const heldFile = path.join(repo, ".nodwyn", "worktrees", held.slice(held.lastIndexOf("-") + 1), "notes", "held.md");
  fs.writeFileSync(heldFile, "Hold the line\n");
  await page.click(".task-aside .review button:text-is('Refresh')");
  await page.waitForFunction(() => document.querySelector(".review-orphans")?.innerText.includes("notes/held.md, line 4"), null, { timeout: 15000 });
  await shows(page, ".task-aside [role=tab][aria-selected=true]", "Review (1)");
  await shows(page, ".review-orphans", "Drop this tail");
  await page.locator(".review-orphans button:text-is('Remove')").click();
  assert.strictEqual(await page.locator(".review-orphans").count(), 0, "Remove left the comment in the list");
  // A line that is still there but says something else is marked.
  const title = page.locator(".review-file .diff-line", { hasText: "Hold the line" }).first();
  await title.hover();
  await title.locator(".add-comment").click();
  await page.fill(".review-form textarea", "Reword the title");
  await page.click(".review-form button:text-is('Add comment')");
  fs.writeFileSync(heldFile, "Hold another line\n");
  await page.click(".task-aside .review button:text-is('Refresh')");
  await page.waitForFunction(() => document.querySelector(".review-comment")?.innerText.includes("This line has changed since you commented on it."), null, { timeout: 15000 });
  await page.click("button:text-is('Send review (1)')");
  await until("the second review", async () => sent.of("worktree.review").length === 2);
  assert.deepStrictEqual(sent.of("worktree.review")[1].comments, [{ path: "notes/held.md", line: 1, text: "Reword the title" }]);
  await idle();

  // A closed task is not read: its worktree is read while it is open, so the
  // tab says to resume it, asks nothing and shows no refusal.
  await page.click('.task-actions button:text-is("Close")');
  await page.waitForSelector('.task-actions button:text-is("Resume")');
  const asked = sent.of("worktree.diff").length;
  await shows(page, ".task-aside .review", "Resume this task to review its changes");
  assert.strictEqual(sent.of("worktree.diff").length, asked, "a closed task's diff was asked for");
  assert.ok(!(await page.innerText(".task-aside .review")).includes("not open in this daemon"), "the closed task shows the daemon's refusal");
  await page.screenshot({ path: `${out}/worktree-review-closed.png` });

  // A folder can still opt out, and the next task then starts in that folder.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector(".worktree-toggle input");
  await page.uncheck(".worktree-toggle input");
  const plainOpens = sent.of("session.open").length;
  const worktreeOpens = sent.of("worktree.open").length;
  await startTask("Plain folder task", true, false);
  assert.deepStrictEqual(sent.of("session.open").slice(plainOpens), [{ repo }], "opting out did not start the task in the folder");
  assert.strictEqual(sent.of("worktree.open").length, worktreeOpens, "opting out still opened a worktree");
});
