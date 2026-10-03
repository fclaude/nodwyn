// The seeded task's approvals: the edit went ahead, and Y answers each of
// the two commands' inline approvals, one answer each; the run they fold
// into says when one waits, then how many ran and for how long; the plan
// panel counts the steps marked; the reply renders as Markdown; a follow-up
// sends and is answered; a turn a rate limit ends leaves its plan to
// "continue"; two edits to a protected file in one call ask once and write
// both; Home then has nothing waiting.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, launch, watch, requests, shows, until, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0"]);
  const [url] = await line(demo, /URL (\S+)/);
  const [repo] = await line(demo, /REPO (\S+)/);
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1440, height: 1000 } }));
  const sent = requests(page);
  const step = (m) => console.log("step:", m);
  const transcript = () => page.innerText(".transcript");
  await page.goto(url);
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });
  await page.click(".nav-task.dotted");
  await page.waitForSelector(".task[data-loaded]");
  const id = decodeURIComponent((await page.evaluate(() => location.hash)).replace("#/task/", ""));
  // The plan panel shows every step of the planner's plan, each with its
  // kind, Test: and Docs: included, and counts the steps the model marks:
  // two before the commands it asks about, two more once they are approved.
  const planPanel = () =>
    page.evaluate(() => ({
      head: [...document.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.textContent.startsWith("Plan"))?.querySelector("h2")?.textContent,
      steps: [...document.querySelectorAll("ol.plan li .text")].map((s) => s.textContent),
      items: [...document.querySelectorAll("ol.plan li")].map((li) => li.className),
    }));
  const before = await planPanel();
  assert.deepStrictEqual(before.steps.map((s) => s.split(":")[0]), ["Change", "Test", "Docs", "Check"], "the plan's steps");
  assert.match(before.steps[1], /fails today: /);
  assert.strictEqual(before.head, "Plan2 of 4 done · drafted");
  assert.deepStrictEqual(before.items, ["done", "done", "next", ""]);

  // The edit goes ahead without asking; the two commands after it ask
  // first. While one waits, the run they fold into says so (that it still
  // says so as its clock ticks is the transcript check's).
  const run = () =>
    page.evaluate(() => {
      const g = [...document.querySelectorAll(".activity.commands")].pop();
      return g && { label: g.querySelector(".label").textContent, what: g.querySelector(".what").textContent, open: g.open, rows: [...g.querySelectorAll(".call")].map((r) => ({ name: r.querySelector(".name").textContent, stat: r.querySelector(".stat").textContent })) };
    });
  await shows(page, ".ask-card", "Run ls -la");
  await until("the run saying it waits", async () => (await run())?.label === "Waiting for your answer");
  assert.strictEqual((await run()).rows[0].stat, "waiting for your answer");
  step("card shown");
  // Keyboard answers, as the terminal does.
  await page.keyboard.press("y");
  await page.waitForSelector(".ask-card h2:has-text('Run wc -l app.js')", { timeout: 15000 });
  await until("the run saying one ran and one waits", async () => (await run())?.label === "Ran 1 command, waiting for your answer");
  assert.strictEqual((await run()).rows[1].stat, "waiting for your answer");
  await page.screenshot({ path: `${out}/second-ask.png` });
  await page.keyboard.press("y");
  await page.waitForSelector(".prose strong", { timeout: 15000 });
  await until("the turn's end", async () => (await page.textContent(".task-head .pill")) === "Idle");
  assert.deepStrictEqual(sent.of("session.answer").map((a) => [a.id, a.reply]), [[id, "y"], [id, "y"]], "one Y for each command");
  step("both commands approved; task answered");
  // Both ran: one run, folded, each command in its own row.
  const ran = await run();
  assert.strictEqual(await page.$$eval(".activity.commands", (gs) => gs.length), 1, "the commands fold into one run");
  assert.strictEqual(ran.label, "Ran 2 commands");
  assert.match(ran.what, /^in \d+\.\d s$/);
  assert.strictEqual(ran.open, false, "the finished run is folded");
  assert.deepStrictEqual(ran.rows.map((r) => r.name.replace(/\s+/g, " ").trim()), ["ls -la", "wc -l app.js"]);
  assert.ok(ran.rows.every((r) => /^exit 0/.test(r.stat)), `the run's rows: ${JSON.stringify(ran.rows)}`);
  const after = await page.evaluate(() => ({
    card: !!document.querySelector(".ask-card"),
    edit: [...document.querySelectorAll(".step")].map((s) => s.textContent.replace(/\s+/g, " ").trim()).find((t) => t.startsWith("Edit")),
    changes: [...document.querySelectorAll(".file-row")].map((r) => r.textContent.trim()),
    code: document.querySelector(".prose pre code")?.textContent,
  }));
  assert.strictEqual(after.card, false, "an approval is left");
  assert.match(after.edit, /^Editapp\.jswritten.*went ahead: a write in the folder$/);
  assert.deepStrictEqual(after.changes, ["app.jsHistory"]);
  assert.strictEqual(after.code, 'const meta = [session.repo, when, state].join(" · ");');
  const marked = await planPanel();
  assert.deepStrictEqual(marked.steps, before.steps, "the plan's steps after the approvals");
  assert.strictEqual(marked.head, "Plan3 of 4 done · drafted");
  assert.deepStrictEqual(marked.items, ["done", "done", "skipped", "done"]);
  await page.screenshot({ path: `${out}/after.png` });

  // A follow-up in the same task.
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await until("the follow-up's answer", async () => (await transcript()).includes("Glad that helps"));
  assert.deepStrictEqual(sent.of("session.send"), [{ id, text: "Thanks" }]);
  step("follow-up answered");

  // A turn that a rate limit ends leaves its plan to "continue", which
  // goes on from the step it marked: the model's plan-write was recorded
  // (its plan has every kind), carried, and the steps "continue" marked
  // are counted.
  await page.fill("#reply", "Tidy the notes");
  await page.keyboard.press("Enter");
  await until("rate limit", async () => (await transcript()).includes("Stopped by an error"));
  step("rate limited");
  await page.fill("#reply", "continue");
  await page.keyboard.press("Enter");
  const carried = async () => ({
    went: await page.evaluate(() => [...document.querySelectorAll(".transcript *")].some((e) => e.children.length === 0 && /Went on with the plan · 1 of 4 steps marked/.test(e.textContent))),
    answer: (await transcript()).includes("Tidied"),
    ...(await planPanel()),
  });
  const want = { went: true, answer: true, head: "Plan4 of 4 done · carried over from an earlier turn", items: ["done", "done", "done", "done"] };
  await until("the carried plan", async () => {
    const got = await carried();
    return JSON.stringify({ ...got, steps: undefined }) === JSON.stringify({ ...want, steps: undefined });
  }).catch(async (e) => {
    throw new Error(`${e.message}: ${JSON.stringify(await carried())}`);
  });
  assert.deepStrictEqual((await planPanel()).steps.map((s) => s.split(":")[0]), ["Change", "Test", "Docs", "Check"]);
  step("plan carried");
  await page.screenshot({ path: `${out}/carried-plan.png` });

  // Two changes to one file in one fs-edit call (ADR 38). The Makefile is
  // a protected path, so the call asks although writes in the folder go
  // ahead: the card shows one diff with both edits, nothing is written
  // before the answer, and after it the file holds both.
  const makefile = `${repo}/Makefile`;
  const original = "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n";
  await page.fill("#reply", "Quiet the build");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".ask-card .diff", { timeout: 15000 });
  const card = await page.evaluate(() => ({
    title: document.querySelector(".ask-card h2")?.textContent,
    security: !!document.querySelector(".ask-card .badge.bad"),
    added: [...document.querySelectorAll(".ask-card .diff-line.add .t")].map((e) => e.textContent),
    removed: [...document.querySelectorAll(".ask-card .diff-line.del .t")].map((e) => e.textContent),
  }));
  assert.deepStrictEqual(card, { title: "Edit Makefile", security: true, added: ["\t@go build ./...", "\t@go test ./... >/dev/null"], removed: ["\tgo build ./...", "\tgo test ./..."] }, "the Makefile's security confirm, one diff of both edits");
  await page.screenshot({ path: `${out}/edits-ask.png` });
  assert.strictEqual(fs.readFileSync(makefile, "utf8"), original, "the Makefile changed before the answer");
  // Focus is still in the reply box, where y would be typed: click.
  await page.click(".ask-card button[data-key='Y']");
  await until("answer after the edits", async () => (await transcript()).includes("Quieted"));
  assert.strictEqual(fs.readFileSync(makefile, "utf8"), "build:\n\t@go build ./...\n\ntest:\n\t@go test ./... >/dev/null\n", "the Makefile after the answer");
  assert.deepStrictEqual(sent.of("session.answer").map((a) => a.reply), ["y", "y", "y"]);
  const row = await page.evaluate(() => {
    const step = [...document.querySelectorAll(".step")].find((s) => /Edit\s*Makefile/.test(s.textContent));
    return step && { text: step.textContent.replace(/\s+/g, " ").trim(), args: step.nextElementSibling?.querySelector("pre")?.textContent };
  });
  assert.ok(row && row.text.includes("written") && row.args?.includes('"edits"'), `the transcript does not show the edit and its arguments: ${JSON.stringify(row)}`);
  assert.deepStrictEqual(sent.of("session.send").map((s) => s.text), ["Thanks", "Tidy the notes", "continue", "Quiet the build"]);
  step("edits written");
  await page.screenshot({ path: `${out}/edits-written.png` });

  // Home: nothing waits for an answer now, and the task is with the finished.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  const sidebar = await page.innerText("#sidebar");
  assert.ok(!sidebar.includes("NEEDS YOU"), `the sidebar still has a task waiting:\n${sidebar}`);
  await shows(page, "#view", "RECENTLY FINISHED", "The session list shows");
  await page.screenshot({ path: `${out}/home-after.png` });
});
