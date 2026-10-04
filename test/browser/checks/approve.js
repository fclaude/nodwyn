// The seeded task's approvals: the edit went ahead, and Y answers each of
// the two commands' inline approvals, one answer each; the run they fold
// into says when one waits, then how many ran and for how long; the plan
// panel counts the steps marked; the Totals panel holds its running time
// still while an ask waits and moves as the work goes on, and its rules
// are checked on crafted logs; the reply renders as Markdown; a follow-up
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
  // The Totals panel: the task's whole spend and running time. While the
  // ask waits, the running time holds still — waiting for an answer is not
  // running — and it moves as the work goes on.
  const totals = () =>
    page.evaluate(() => {
      const panel = [...document.querySelectorAll(".panel")].find((p) => p.dataset.totals);
      if (!panel) return null;
      const heads = [...panel.querySelectorAll(".meter-head")].map((m) => m.textContent);
      return { spend: heads[0], time: heads[1] };
    });
  const held = await totals();
  assert.match(held.spend, /^Total spendat least \$0\.0000$/, `the unpriced spend: ${held.spend}`);
  assert.match(held.time, /^Total running time\d/, `the running time: ${held.time}`);
  await new Promise((r) => setTimeout(r, 2300));
  const still = await totals();
  assert.strictEqual(still.time, held.time, `the running time grew while the ask waited: ${held.time} then ${still.time}`);
  step("totals hold while the ask waits");
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
  // The work since the first ask (the commands, the follow-up and the
  // rate-limited turn) moved the running time on.
  const moved = await totals();
  assert.notStrictEqual(moved.time, held.time, `the running time did not move with the work: ${held.time}`);

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

  // The rules the running time adds up with, against the console's own
  // module on crafted logs: a person's wait stays out, the judge's and a
  // run's own answers are not waits, a run that went on by itself ran
  // through the gap between its turns, and a gap across a resume was the
  // person's who read and resumed.
  const rules = await page.evaluate(async () => {
    const { TaskView } = await import("/task.js");
    const view = new TaskView({ onEvent: () => () => {} }, {}, () => {});
    const base = Date.now() - 3600_000;
    const ev = (s, kind, payload = {}) => ({ seq: s, ts: new Date(base + s * 1000).toISOString(), kind, payload });
    const run = (events, busy = false, prompts = 0) => {
      view.busy = busy;
      view.prompts = new Map(prompts ? [["p", {}]] : []);
      return view.runningMs(events);
    };
    const turn = (extra) => [ev(0, "user.msg", { text: "go" }), ev(1, "llm.req"), ev(2, "llm.resp", { usage: {} }), ...extra];
    return {
      plain: run(turn([ev(4, "turn.end")])),
      waited: run(turn([ev(3, "tool.call", { id: "c1", name: "shell-exec" }), ev(8, "guard.decision", { decision: "confirm", answer: "yes" }), ev(9, "tool.result", { id: "c1", ok: true }), ev(10, "turn.end")])),
      judged: run(turn([ev(3, "tool.call", { id: "c1", name: "shell-exec" }), ev(8, "guard.decision", { decision: "confirm", answer: "yes", actor: "judge" }), ev(9, "tool.result", { id: "c1", ok: true }), ev(10, "turn.end")])),
      live: run([ev(0, "user.msg"), ev(2, "llm.req")], true, 1),
      bridged: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: true, actor: "unattended" }), ev(5, "run.turn", { number: 2 }), ev(9, "turn.end")]),
      asked: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: true, prompt_id: "p1" }), ev(5, "run.turn", { number: 2 }), ev(9, "turn.end")]),
      resumed: run([...turn([ev(4, "turn.end")]), ev(30, "session.resume"), ev(31, "run.state", { status: "running" }), ev(32, "run.turn", { number: 1 }), ev(36, "turn.end")]),
      stopped: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: false, actor: "unattended" }), ev(40, "session.resume"), ev(41, "run.state", { status: "running" }), ev(42, "run.turn", { number: 1 }), ev(46, "turn.end")]),
    };
  });
  assert.strictEqual(rules.plain, 4000, `a turn with no wait: ${rules.plain}`);
  assert.strictEqual(rules.waited, 5000, `a person's wait stays out: ${rules.waited}`);
  assert.strictEqual(rules.judged, 10000, `the judge's deciding is running, not a wait: ${rules.judged}`);
  assert.strictEqual(rules.live, 2000, `a prompt still pending holds the open turn: ${rules.live}`);
  assert.strictEqual(rules.bridged, 9000, `a run that went on by itself ran through the gap: ${rules.bridged}`);
  assert.strictEqual(rules.asked, 8000, `a checkpoint the person answered is a wait: ${rules.asked}`);
  assert.strictEqual(rules.resumed, 8000, `a gap across a resume was the person's: ${rules.resumed}`);
  assert.strictEqual(rules.stopped, 8000, `a run that stopped at its checkpoint left the gap to the person: ${rules.stopped}`);
  step("totals rules");

  // Home: nothing waits for an answer now, and the task is with the finished.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  const sidebar = await page.innerText("#sidebar");
  assert.ok(!sidebar.includes("NEEDS YOU"), `the sidebar still has a task waiting:\n${sidebar}`);
  await shows(page, "#view", "RECENTLY FINISHED", "The session list shows");
  await page.screenshot({ path: `${out}/home-after.png` });
});
