// Several tasks on one repository (ADR 34): a git workspace starts with
// "In a worktree" checked; each task then works in a worktree of its own,
// on a branch of its own, and is listed under the repository. Switching
// between open tasks shows each as it was, with what happened while it was
// off screen, without reading its log again.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { execFileSync } = require("child_process");
const { out, watch, requests, shows, openOptions, until, step, ready, desk, check } = require("../lib");

// Held replies make races and refusals deterministic without a real daemon
// failure. The legacy opener also runs these guards on the unchanged code.
async function reviewSafeguards(page) {
  return page.evaluate(async () => {
    const { TaskView } = await import("/task.js");
    const { Desk } = await import("/desk.js");
    const reads = [], reviews = [], sent = [];
    const conn = {
      live: true,
      methods: new Set(["session.send", "worktree.diff", "worktree.review"]),
      has(method) { return this.methods.has(method); },
      onEvent() { return () => {}; },
      onResult() { return () => {}; },
      call(method, params) {
        sent.push({ method, params });
        if (method === "worktree.diff") return new Promise((resolve, reject) => reads.push({ resolve, reject }));
        if (method === "worktree.review") return new Promise((resolve, reject) => reviews.push({ resolve, reject }));
        if (method === "session.subscribe") return Promise.resolve({ page: { events: [], through: 0 }, busy: false });
        return Promise.resolve({});
      },
    };
    const desk = new Desk(conn);
    desk.sessions.set("review-check", { id: "review-check", repo: "/tmp/review-check", open: true, worktree: { base: "main" } });
    const task = new TaskView(conn, desk, () => {});
    document.querySelector("#view").replaceChildren(task.root);
    await task.open("review-check");
    const open = () => {
      if (task.openReview) task.openReview();
      else { task.tab = "review"; task.aside(); }
    };
    const settle = () => new Promise((resolve) => requestAnimationFrame(resolve));
    const answer = (changes) => ({ base: "1234567890", changes, omitted: 0 });
    const file = (name) => ({ path: `${name}.txt`, status: "A", diff: "--- /dev/null\n+++ file\n@@ -0,0 +1,90 @@\n" + Array.from({ length: 90 }, (_, i) => `+${name} ${i + 1}`).join("\n") + "\n" });
    const guards = {}, modes = {};
    open();
    await settle();
    guards.loading = task.review.loading && document.querySelector(".review").textContent.includes("Reading the changes…") && [...document.querySelectorAll(".review button")].find((b) => b.textContent === "Refresh").disabled;
    reads[0].resolve(answer([file("initial")]));
    await settle();
    const older = task.loadReview(), newer = task.loadReview();
    reads[2].resolve(answer([file("newest"), file("another-file")]));
    await newer;
    reads[1].resolve(answer([file("stale")]));
    await older;
    guards.stale = task.review.files[0].path === "newest.txt" && !document.querySelector(".review").textContent.includes("stale.txt");
    const failed = task.loadReview();
    reads[3].reject(new Error("Refresh failed"));
    await failed;
    guards.failedRefresh = task.review.files[0].path === "newest.txt" && document.querySelector(".review").textContent.includes("Refresh failed") && !document.querySelector(".review").textContent.includes("has not changed anything");
    document.querySelector(".review .add-comment").click();
    let area = document.querySelector(".review-form textarea");
    area.value = "Unfinished comment";
    area.dispatchEvent(new Event("input"));
    area.focus();
    area.setSelectionRange(2, 6);
    const body = document.querySelector(".review-file .diff-body");
    body.scrollTop = 160;
    task.aside();
    area = document.querySelector(".review-form textarea");
    guards.draft = area.value === "Unfinished comment" && document.activeElement === area && area.selectionStart === 2 && area.selectionEnd === 6;
    if (task.openReview) {
      task.el.inspector.scrollTop = 80;
      const centreScroll = task.el.inspector.scrollTop;
      modes.scroll = document.querySelector(".review-file .diff-body").scrollTop === 160;
      task.closeInspector();
      open();
      area = document.querySelector(".review-form textarea");
      modes.centreReopen = centreScroll > 0 && task.el.inspector.scrollTop === centreScroll;
      modes.reopen = area.value === "Unfinished comment" && area.selectionStart === 2 && area.selectionEnd === 6 && document.activeElement === area && document.querySelector(".review-file .diff-body").scrollTop === 160;
    }
    document.querySelector(".review-form button").click();
    document.querySelector(".review-file .diff-head button").click();
    area = document.querySelector(".review-form textarea");
    area.value = "File comment";
    area.dispatchEvent(new Event("input"));
    document.querySelector(".review-form button").click();
    const note = document.querySelector("textarea[aria-label='Overall note']");
    note.value = "Overall draft";
    note.dispatchEvent(new Event("input"));
    task.busy = true;
    task.aside();
    const send = () => [...document.querySelectorAll(".review button")].find((b) => b.textContent.startsWith("Send review"));
    send().click();
    guards.busy = send().disabled && reviews.length === 0;
    task.busy = false;
    task.aside();
    const refused = task.sendReview();
    guards.pending = task.review.comments.length === 2 && task.review.note === "Overall draft" && send().disabled;
    reviews[0].reject(new Error("Review refused"));
    await refused;
    guards.refused = task.review.comments.length === 2 && task.review.note === "Overall draft" && !send().disabled;
    const accepted = task.sendReview();
    const request = sent.filter((r) => r.method === "worktree.review").at(-1).params;
    guards.batched = request.comments.length === 2 && request.comments[0].line === 1 && request.comments[1].line === 0 && request.note === "Overall draft" && !JSON.stringify(request).includes("newest 1") && request.comments.every((c) => Object.keys(c).sort().join() === "line,path,text");
    reviews[1].resolve({});
    await accepted;
    guards.accepted = task.review.comments.length === 0 && task.review.note === "";

    if (task.openReview) {
      const complex = task.loadReview();
      reads.at(-1).resolve(answer([
        { path: "<img src=x>.txt", status: "M", diff: "--- old\n+++ new\n@@ -3,5 +7,4 @@ first hunk\n context\n-old one\n-old two\n-old three\n+<img src=x onerror=alert(1)>\n+new two\n after\n@@ -30,3 +40,5 @@ second hunk\n anchor\n-gone\n+replacement\n+extra one\n+extra two\n tail\n\\ No newline at end of file\n" },
        { path: "deleted.txt", status: "D", diff: "--- old\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-gone\n-old\n" },
        { path: "binary.dat", status: "M", diff: "(binary file: no lines to show)" },
      ]));
      await complex;
      const pick = (label) => [...document.querySelectorAll(".review button")].find((b) => b.textContent === label).click();
      modes.unified = document.querySelector(".diff-unified .diff-line.del .n.new").textContent === "" && document.querySelector(".diff-unified .diff-line.del .n.old").textContent === "4";
      pick("Side-by-side");
      const pairs = [...document.querySelector(".review-file").querySelectorAll(".diff-pair")].map((pair) => [...pair.querySelectorAll(":scope > .diff-line .n")].map((n) => n.textContent));
      modes.alignment = JSON.stringify(pairs) === JSON.stringify([["3", "7"], ["4", "8"], ["5", "9"], ["6", ""], ["7", "10"], ["30", "40"], ["31", "41"], ["", "42"], ["", "43"], ["32", "44"]]);
      modes.hunks = document.querySelector(".review-file").querySelectorAll(".hunk").length === 2;
      modes.literal = document.querySelector(".review").textContent.includes("<img src=x onerror=alert(1)>") && document.querySelector(".review").textContent.includes("<img src=x>.txt") && !document.querySelector(".review img, .review script");
      modes.notes = document.querySelector(".review").textContent.includes("binary file: no lines to show") && [...document.querySelectorAll(".review-file")][2].querySelectorAll(".add-comment").length === 0;
      modes.oldOnly = document.querySelectorAll(".diff-line.old .add-comment, .diff-line.new.blank .add-comment").length === 0 && [...document.querySelectorAll(".review-file")][1].querySelectorAll(".add-comment").length === 0;
      document.querySelector("button[aria-label='Comment on <img src=x>.txt line 8']").click();
      area = document.querySelector(".review-form textarea");
      area.value = "Current line draft";
      area.dispatchEvent(new Event("input"));
      area.focus();
      area.setSelectionRange(5, 5);
      pick("Unified");
      area = document.querySelector(".review-form textarea");
      modes.modeDraft = area.value === "Current line draft" && area.selectionStart === 5 && document.activeElement === area;
      document.querySelector(".review-form button").click();
      modes.target = task.review.comments[0].line === 8 && task.review.comments[0].path === "<img src=x>.txt";
      const modeButton = [...document.querySelectorAll(".review button")].find((b) => b.textContent === "Side-by-side");
      modeButton.focus();
      modeButton.click();
      modes.controlFocus = document.activeElement?.getAttribute("aria-label") === "Side-by-side";
      const late = task.loadReview();
      task.closeInspector();
      reads.at(-1).resolve(answer([file("late")]));
      await late;
      modes.lateClose = task.inspecting === null && task.el.inspector.hidden && !task.el.transcript.hidden && !task.el.form.hidden;
    }
    open();
    const navigated = task.loadReview();
    task.version++;
    reads.at(-1).resolve(answer([file("wrong-task")]));
    await navigated;
    guards.navigation = task.review.files[0].path !== "wrong-task.txt";
    task.item.open = false;
    const count = reads.length;
    task.aside();
    guards.closed = reads.length === count && document.querySelector(".review").textContent.includes("Resume this task");
    task.dispose();
    return { guards, modes };
  });
}

check(async () => {
  const { url, repo, ctx, page } = await desk({ args: ["-pace=0"], repo: "GIT" });
  const sent = requests(page);
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
  await ready(page);
  const sandbox = watch(await ctx.newPage(), "review safeguards");
  await sandbox.goto(url.replace(/#.*$/, "") + "#/settings");
  await ready(sandbox);
  const safeguards = await reviewSafeguards(sandbox);
  console.log("review safeguards:", JSON.stringify(safeguards));
  for (const [name, passed] of Object.entries(safeguards.guards)) assert.ok(passed, `review safeguard: ${name}`);
  await sandbox.close();
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
    await page.waitForSelector(".worktree-toggle input", { state: "attached" });
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
      assert.strictEqual(layout.tabs.length, 4, "all four task detail tabs are present");
      assert.strictEqual(await page.locator(".task-aside .review-open").count(), 1, "worktree review has a separate opener");
      assert.ok(layout.tabs.every((t) => t.top === layout.tabs[0].top && t.left >= layout.left && t.right <= layout.right + 1 && t.width > 0) && !layout.overflow, `Four tabs fit one row at ${panel}px: ${JSON.stringify(layout)}`);
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
  // Review opens in the centre, without losing the conversation's draft.
  await page.fill("#reply", "Conversation draft");
  await page.click(".task-aside .review-open, .task-aside [role=tab]:has-text('Review')");
  await page.waitForFunction(() => document.querySelector(".review")?.innerText.includes("notes/second.md"), null, { timeout: 15000 });
  assert.strictEqual(await page.locator(".task-main .inspector-view .review").count(), 1, "review must open in the centre inspector, not in the sidebar");
  await shows(page, ".task-main .inspector-view .review", "Write second", "1 file changed since main at");
  assert.strictEqual(await page.locator(".task-aside .review").count(), 0, "review still occupies the sidebar");
  assert.ok(await page.locator(".task-main .transcript").isHidden(), "the conversation was not replaced by review");
  assert.ok(await page.locator(".task-main .reply").isHidden(), "reply is offered inside review");
  assert.strictEqual(await page.getAttribute("button:text-is('Unified')", "aria-pressed"), "true", "review does not default to Unified");
  await page.click("button:text-is('Side-by-side')");
  await shows(page, ".review-columns", "Old", "New");
  assert.strictEqual(await page.locator(".review-file .diff-line.old .add-comment").count(), 0, "old-file lines offer comments");
  const current = page.locator(".review-file .diff-line.new", { hasText: "Write second" });
  assert.strictEqual(await current.locator(".n").textContent(), "1", "the new column is not numbered as the current file");
  await page.click("button:text-is('Unified')");
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
  // Closing and reopening retains the unfinished comment and its caret.
  await page.click("button:text-is('Close review')");
  assert.ok(await page.locator(".task-main .inspector-view").isHidden(), "Close review left the inspector visible");
  assert.ok(await page.locator(".task-main .transcript").isVisible(), "Close review did not restore the conversation");
  assert.ok(await page.locator(".task-main .reply").isVisible(), "Close review did not restore reply");
  assert.strictEqual(await page.inputValue("#reply"), "Conversation draft", "review replaced the conversation draft");
  await page.click(".task-aside .review-open");
  const reopened = await page.evaluate(() => {
    const field = document.querySelector(".review-form textarea");
    return { focused: document.activeElement === field, at: field.selectionStart, value: field.value };
  });
  assert.deepStrictEqual(reopened, typing, "reopening lost the unfinished comment or caret");
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
  await shows(page, ".task-aside .review-open", "Review (2)");
  await tabsFit();
  assert.strictEqual(sent.of("worktree.review").length, 0, "comments were sent before Send review");
  // A comment is taken back with Remove, and written again.
  await page.locator(".review-comment", { hasText: "Name the file better" }).locator("button:text-is('Remove')").click();
  await shows(page, ".task-aside .review-open", "Review (1)");
  await page.click(".review-file .diff-head button:text-is('Comment on file')");
  await page.fill(".review-form textarea", "Name the file better");
  await page.click(".review-form button:text-is('Add comment')");
  await shows(page, ".task-aside .review-open", "Review (2)");
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
  // Mode changes keep saved comments, the overall note and its caret.
  await page.evaluate(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "Side-by-side").click());
  assert.strictEqual(await page.locator(".review-comment").count(), 2, "switching modes lost saved comments");
  assert.strictEqual(await page.inputValue("textarea[aria-label='Overall note']"), "Mostly fine");
  assert.ok(await page.$eval("textarea[aria-label='Overall note']", (field) => document.activeElement === field && field.selectionStart === 4), "switching modes lost note focus or caret");
  await page.click("button:text-is('Close review')");
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector(".finished");
  await page.locator(`.finished li:has(a[href='#/task/${second}']) a:text-is('Review')`).click();
  await page.waitForSelector(".task-main .inspector-view .review");
  assert.strictEqual(await page.getAttribute("button:text-is('Side-by-side')", "aria-pressed"), "true", "Home lost the chosen mode");
  assert.strictEqual(await page.locator(".review-comment").count(), 2, "Home lost saved comments");
  assert.strictEqual(await page.inputValue("textarea[aria-label='Overall note']"), "Mostly fine", "Home lost the note");
  await page.click("button:text-is('Unified')");
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
  // A direct Home review route also opens correctly in a fresh task view.
  const direct = watch(await ctx.newPage(), "direct review");
  await direct.goto(url.replace(/#.*$/, "") + `#/task/${second}/review`);
  await ready(direct);
  await loaded(direct);
  await direct.waitForFunction(() => document.querySelector(".task-main .review")?.textContent.includes("notes/second.md"));
  assert.ok(await direct.locator(".task-main .reply").isHidden(), "the direct review route left the reply form visible");
  await direct.click("button:text-is('Close review')");
  assert.ok(await direct.locator(".task-main .transcript").isVisible(), "the direct review route did not close to the conversation");
  await direct.close();
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
  await page.click(".task-aside .review-open");
  await page.waitForFunction(() => (document.querySelector(".task-main .inspector-view .review")?.innerText || "").includes("++count;"), null, { timeout: 15000 });
  // The diff numbers the lines as the file does: a line that starts with ++
  // or -- is a line of the file, not a header.
  const lines = await page.$$eval(".review-file .diff-line", (rows) => rows.map((r) => [r.querySelector(".n.new").textContent, r.querySelector(".t").textContent]));
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
  await shows(page, ".task-main .inspector-view .review", "The task is working: send when it is idle.");
  assert.strictEqual(sent.of("worktree.review").length, 1, "a review was sent to a working task");
  // The turn ends: the diff is read again without a Refresh, and Send is back.
  const diffs = sent.of("worktree.diff").length;
  // Home must return to the pending approval, even if review was parked.
  await page.evaluate(() => (location.hash = "#/"));
  await page.locator(`.home-section:has(#needs) a[href='#/task/${held}']`).click();
  await page.waitForSelector(".task[data-loaded]");
  assert.ok(await page.locator(".task-main .inspector-view").isHidden(), "Home's approval Review link left worktree review open");
  assert.ok(await page.locator(".ask-card").isVisible(), "Home's approval Review link hid the pending question");
  await page.evaluate(() => document.activeElement?.blur());
  await page.keyboard.press("y");
  await idle();
  await until("the diff read again after the turn", async () => sent.of("worktree.diff").length > diffs);
  await page.click(".task-aside .review-open");
  await until("Send review offered again", async () => page.locator("button:text-is('Send review (1)')").isEnabled());

  // A comment whose line the file no longer has is not hidden: it is listed
  // under its own heading, still counted, and can be removed.
  const heldFile = path.join(repo, ".nodwyn", "worktrees", held.slice(held.lastIndexOf("-") + 1), "notes", "held.md");
  fs.writeFileSync(heldFile, "Hold the line\n");
  await page.click(".task-main .inspector-view .review button:text-is('Refresh')");
  await page.waitForFunction(() => document.querySelector(".review-orphans")?.innerText.includes("notes/held.md, line 4"), null, { timeout: 15000 });
  await shows(page, ".task-aside .review-open", "Review (1)");
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
  await page.click(".task-main .inspector-view .review button:text-is('Refresh')");
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
  await shows(page, ".task-main .inspector-view .review", "Resume this task to review its changes");
  assert.strictEqual(sent.of("worktree.diff").length, asked, "a closed task's diff was asked for");
  assert.ok(!(await page.innerText(".task-main .inspector-view .review")).includes("not open in this daemon"), "the closed task shows the daemon's refusal");
  await page.screenshot({ path: `${out}/worktree-review-closed.png` });

  // A folder can still opt out, and the next task then starts in that folder.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector(".worktree-toggle input", { state: "attached" });
  await openOptions(page);
  await page.uncheck(".worktree-toggle input");
  const plainOpens = sent.of("session.open").length;
  const worktreeOpens = sent.of("worktree.open").length;
  await startTask("Plain folder task", true, false);
  assert.deepStrictEqual(sent.of("session.open").slice(plainOpens), [{ repo }], "opting out did not start the task in the folder");
  assert.strictEqual(sent.of("worktree.open").length, worktreeOpens, "opting out still opened a worktree");

  // Only a git repository can have worktrees. A named folder that is not one
  // has the box disabled, with the reason; and a refusal before any task
  // opened says the task did not start, instead of sending the person to a
  // task list that has nothing new in it.
  const plain = path.join(path.dirname(repo), "plain-folder");
  const vanished = path.join(path.dirname(repo), "vanished-repo");
  fs.mkdirSync(plain);
  fs.mkdirSync(vanished);
  execFileSync("git", ["init", "-q", vanished]);
  await page.evaluate(() => (location.hash = "#/"));
  for (const dir of [plain, vanished]) {
    await page.click("button.chip.dashed");
    await page.fill("#add-path", dir);
    await page.click('.add-folder button:text-is("Add this folder")');
    await page.waitForSelector(`button.chip[title="${dir}"][aria-pressed="true"]`);
  }
  assert.ok(await page.isChecked(".worktree-toggle input"), "a git folder did not default to a worktree");
  fs.rmSync(path.join(vanished, ".git"), { recursive: true });
  await page.fill("#task", "Start in a repository that is gone");
  const refused = sent.of("worktree.open").length;
  await page.press("#task", "Control+Enter");
  await page.waitForSelector(".toast.error");
  const said = await page.textContent(".toast.error .toast-text");
  assert.ok(said.startsWith("The task did not start: ") && said.includes("not a git repository") && !said.includes("task list") && !said.includes("worktree.open"), said);
  assert.strictEqual(sent.of("worktree.open").length, refused + 1, "the start was not asked");
  assert.strictEqual(await page.inputValue("#task"), "Start in a repository that is gone", "the message did not stay on Home");
  await page.click(".toast.error [aria-label=Dismiss]");
  await page.click(`button.chip[title="${plain}"]`);
  await page.waitForSelector(`button.chip[title="${plain}"][aria-pressed="true"]`);
  const box = await page.$eval(".worktree-toggle", (el) => ({ disabled: el.querySelector("input").disabled, checked: el.querySelector("input").checked, title: el.title }));
  assert.deepStrictEqual([box.disabled, box.checked, box.title.includes("not one")], [true, false, true], "a folder that is not a git repository offers a worktree");
  await openOptions(page);
  await page.screenshot({ path: `${out}/worktree-not-git.png` });
  for (const [name, passed] of Object.entries(safeguards.modes)) assert.ok(passed, `review mode safeguard: ${name}`);
});
