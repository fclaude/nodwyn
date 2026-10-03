// The task list (ADR 39): tasks under their folders, the folder last active
// first; a task renamed, archived and brought back, and deleted, each kept
// across a reload, which reads the list from the daemon again.
const { chromium } = require("playwright");
const { spawn } = require("child_process");
const out = process.argv[2];
(async () => {
  const demo = spawn(process.env.DEMO, ["-seed"], { stdio: ["pipe", "pipe", "inherit"] });
  process.on("exit", () => demo.kill("SIGKILL"));
  const step = (m) => console.log("step:", m);
  let buf = "";
  const [url, consoleRepo] = await new Promise((resolve) =>
    demo.stdout.on("data", (d) => {
      buf += d;
      const u = /URL (\S+)/.exec(buf), r = /REPO (\S+)/.exec(buf);
      if (u && r) resolve([u[1], r[1]]);
    }),
  );
  const repos = { console: consoleRepo, receipts: consoleRepo.replace(/\/console$/, "/receipts") };
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  const fail = async (why) => {
    await page.screenshot({ path: `${out}/tasks-failed.png` });
    throw new Error(why);
  };
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
  const until = async (what, ok, tries = 40) => {
    let last;
    for (let i = 0; i < tries; i++) {
      last = await sidebar();
      if (ok(last)) return last;
      await page.waitForTimeout(250);
    }
    await fail(`${what}: ${JSON.stringify(last)}`);
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
    if (!(await page.$(row))) for (const more of await page.$$(".nav-more")) await more.click();
    await page.click(row);
    await page.waitForFunction((t) => document.querySelector(".task-head h1")?.textContent === t, title);
  };
  const button = (label) => page.click(`.task-actions button:text-is("${label}")`);

  await page.goto(url);
  await page.waitForSelector(".sidebar .nav-task", { timeout: 15000 });
  let s = await until("the seeded tasks", (s) => s.needs.length === 1 && folder(s, "receipts")?.tasks.length === 1);
  step(`seeded: ${JSON.stringify(s)}`);

  // Five more tasks in receipts, then one in console: console's folder, the
  // latest active, comes first, and receipts shows five of its six.
  for (let i = 1; i <= 5; i++) await start("receipts", `Receipt question ${i}`);
  await start("console", "Hello from the console");
  s = await until("folders by activity", (s) => s.folders.map((f) => f.name).join() === "console,receipts" && folder(s, "receipts").tasks.length === 5);
  if (folder(s, "receipts").more !== "Show 1 more" || folder(s, "receipts").tasks[0] !== "Receipt question 5") await fail(`receipts: ${JSON.stringify(folder(s, "receipts"))}`);
  await page.screenshot({ path: `${out}/tasks-grouped.png` });
  await page.click('.folder:has(.name:text-is("receipts")) .nav-more');
  await until("show more", (s) => folder(s, "receipts").tasks.length === 6 && !folder(s, "receipts").more);
  // A message in an older task makes its folder the latest active again.
  await openTask("Summarize what is in this folder");
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await idle();
  s = await until("receipts first after a message", (s) => s.folders.map((f) => f.name).join() === "receipts,console" && folder(s, "receipts").tasks[0] === "Summarize what is in this folder");
  step("ordered by last activity");

  // Rename from the heading: Escape keeps the name, Enter saves it.
  await page.click(".rename-btn");
  await page.fill(".title-input", "Not this");
  await page.press(".title-input", "Escape");
  if ((await page.textContent(".task-head h1")) !== "Summarize what is in this folder") await fail("Escape renamed the task");
  await page.click(".rename-btn");
  await page.fill(".title-input", "Receipts overview");
  await page.press(".title-input", "Enter");
  await until("renamed in the sidebar", (s) => folder(s, "receipts").tasks[0] === "Receipts overview");
  if ((await page.textContent(".task-head h1")) !== "Receipts overview") await fail("heading not renamed");
  await page.screenshot({ path: `${out}/tasks-renamed.png` });
  step("renamed");

  // Archive: an open task is closed first; then it leaves its folder for
  // the archive, and Unarchive brings it back.
  if (await page.$('.task-actions button:text-is("Archive")')) await fail("an open task offers Archive");
  await button("Close");
  await page.waitForSelector('.task-actions button:text-is("Archive")');
  await button("Archive");
  s = await until("archived", (s) => !folder(s, "receipts").tasks.includes("Receipts overview") && s.archived?.n === "1");
  if (!s.archived.tasks.includes("Receipts overview") || !(await page.$('.task-head .badge:text-is("archived")'))) await fail(`archive: ${JSON.stringify(s.archived)}`);
  await page.screenshot({ path: `${out}/tasks-archived.png` });
  await page.reload();
  await page.waitForSelector(".sidebar .nav-task", { timeout: 15000 });
  s = await until("archived after reload", (s) => s.archived?.n === "1" && s.archived.tasks.includes("Receipts overview") && !folder(s, "receipts").tasks.includes("Receipts overview"));
  await button("Unarchive");
  s = await until("unarchived", (s) => !s.archived && folder(s, "receipts").tasks.includes("Receipts overview"));
  step("archived and brought back");

  // Delete asks first; Cancel keeps the task, Delete removes it for good.
  await openTask("Receipt question 1");
  await button("Close");
  await page.waitForSelector('.task-actions button:text-is("Delete…")');
  await button("Delete…");
  await page.click('.delete-confirm button:text-is("Cancel")');
  if (await page.$(".delete-confirm")) await fail("Cancel left the question");
  await button("Delete…");
  await page.screenshot({ path: `${out}/tasks-delete.png` });
  await page.click('.delete-confirm button:text-is("Delete")');
  await page.waitForFunction(() => location.hash === "#/");
  await until("deleted", (s) => !folder(s, "receipts").tasks.includes("Receipt question 1"));
  await page.reload();
  await page.waitForSelector(".sidebar .nav-task", { timeout: 15000 });
  s = await until("deleted after reload", (s) => folder(s, "receipts") && !folder(s, "receipts").tasks.includes("Receipt question 1") && folder(s, "receipts").tasks.includes("Receipts overview"));
  step("deleted");

  // A folded folder stays folded across a reload, in this browser.
  await page.click('.folder-head:has(.name:text-is("console"))');
  await until("folded", (s) => !folder(s, "console").open && folder(s, "console").tasks.length === 0);
  await page.reload();
  await page.waitForSelector(".sidebar .folder-head", { timeout: 15000 });
  await until("folded after reload", (s) => folder(s, "console") && !folder(s, "console").open);
  await page.click('.folder-head:has(.name:text-is("console"))');
  await until("unfolded", (s) => folder(s, "console").open && folder(s, "console").tasks.length === 1);
  // A search shows its matches in every folder, the archive included.
  await page.click('.sidebar-top button[aria-label="Search tasks"]');
  await page.fill(".search input", "question 2");
  await until("search", (s) => s.folders.length === 1 && folder(s, "receipts").tasks.join() === "Receipt question 2");
  await page.screenshot({ path: `${out}/tasks-search.png` });

  // The same list at phone width, in the drawer.
  await page.fill(".search input", "");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.click("#menu");
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${out}/tasks-phone.png` });
  console.log(errors.length ? errors.join("\n") : "no page errors");
  await browser.close();
  demo.kill("SIGTERM");
  if (errors.length) process.exit(1);
})().catch((e) => {
  console.error("FAILED", e.message);
  process.exit(1);
});
