// The seeded task's approvals: the edit went ahead, the first command's
// inline approval is answered by the "Don't ask" switch while it waits,
// once Settings offers the switch behind its warning,
// and the second then runs without a card; the runs say when one waits
// and what ran for how long; the plan
// panel counts the steps marked; the Totals panel holds its running time
// still while an ask waits and moves as the work goes on, one ask answered
// by the "Don't ask" switch keeps its wait out of the working time, and
// the rules behind both are checked on crafted logs; the reply renders as
// Markdown; a follow-up sends and is answered; a turn a rate limit ends
// leaves its plan to "continue"; two edits to a protected file in one call
// ask once and write both; Home then has nothing waiting; and a task
// started from Home with an image whose confirm is denied, or stopped, is
// idle and takes a message.
const assert = require("assert");
const fs = require("fs");
const { out, tempDir, expectError, requests, shows, until, step, ready, desk, check } = require("../lib");

check(async () => {
  const { url, repo, page } = await desk();
  const sent = requests(page);
  let socket, approveReplyID = null, heldApproveReply = null, failNextApprove = false;
  let approvalEventSeen = false, approvalInfoIDs = new Set(), heldApprovalInfo = null;
  await page.routeWebSocket("**/socket", (ws) => {
    const server = ws.connectToServer();
    socket = ws;
    ws.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "session.approve_all") {
        if (failNextApprove) {
          failNextApprove = false;
          ws.send(JSON.stringify({ jsonrpc: "2.0", id: frame.id, error: { code: -32000, message: "simulated approval refusal" } }));
          return;
        }
        approveReplyID = frame.id;
      } else if (frame.method === "session.info" && approvalEventSeen && approveReplyID !== null) {
        approvalInfoIDs.add(frame.id);
      }
      server.send(message);
    });
    server.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "session.event" && frame.params?.kind === "approval.change" && frame.params?.payload?.kind === "all") {
        approvalEventSeen = true;
        ws.send(message);
      } else if (approvalInfoIDs.has(frame.id)) heldApprovalInfo = message;
      else if (frame.id === approveReplyID) heldApproveReply = message;
      else ws.send(message);
    });
  });
  const transcript = () => page.innerText(".transcript");
  await page.goto(url);
  await ready(page);
  await page.click(".nav-task.dotted");
  await page.waitForSelector(".task[data-loaded]");
  const id = decodeURIComponent((await page.evaluate(() => location.hash)).replace("#/task/", ""));
  // The demo's throwaway world lives under /tmp/nodwyn-demo-*; the session's
  // log there is what the Totals panel adds up.
  const sessionLog = (sid) => {
    for (const root of fs.readdirSync("/tmp").filter((d) => d.startsWith("nodwyn-demo-"))) {
      const p = `/tmp/${root}/data/sessions/${sid}.jsonl`;
      if (fs.existsSync(p)) return p;
    }
    throw new Error(`no session log for ${sid}`);
  };
  // The plan panel shows every step of the planner's plan, each with its
  // kind, Test: and Docs: included, folded to its title until it is
  // clicked, and counts the steps the model marks: two before the commands
  // it asks about, two more once they are approved.
  const planPanel = () =>
    page.evaluate(() => ({
      head: [...document.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.textContent.startsWith("Plan"))?.querySelector("h2")?.textContent,
      steps: [...document.querySelectorAll("ol.plan li .text")].map((s) => s.textContent),
      items: [...document.querySelectorAll("ol.plan li")].map((li) => li.className),
      folds: [...document.querySelectorAll("ol.plan li")].map((li) => {
        if (!li.classList.contains("fold")) return null;
        const text = li.querySelector(".text");
        const more = li.querySelector(".more");
        const note = li.querySelector("small");
        return {
          open: li.classList.contains("open"),
          body: more?.textContent,
          shown: more ? getComputedStyle(more).display !== "none" : undefined,
          noteShown: note ? getComputedStyle(note).display !== "none" : undefined,
          titleShown: getComputedStyle(text).display !== "none",
          // The title sits on the chevron's line: the fold's arrow is not
          // a row of its own above it.
          line: Math.round(text.getBoundingClientRect().top - li.getBoundingClientRect().top),
        };
      }),
    }));
  const before = await planPanel();
  assert.deepStrictEqual(before.steps.map((s) => s.split(":")[0]), ["Change", "Test", "Docs", "Check"], "the plan's steps");
  assert.strictEqual(before.head, "Plan2 of 4 done · drafted");
  assert.deepStrictEqual(before.items, ["done fold alt", "done fold alt", "next fold alt", ""]);
  // A step longer than its title folds: the title stands on the chevron's
  // line, the whole step and the mark's note wait hidden until the step is
  // clicked. The Check step says all it has to say in its title and is not
  // marked yet, so it does not fold; the Test step's fold holds why its
  // test fails on the unchanged code.
  assert.ok(before.folds.slice(0, 3).every((f) => f && !f.open && !f.shown && f.titleShown && !f.noteShown && f.line < 4), `the folded steps: ${JSON.stringify(before.folds)}`);
  assert.strictEqual(before.folds[3], null, "the Check step does not fold");
  assert.match(before.folds[1].body, /fails today: /, "the Test step's fold");
  // Clicking a step opens it and shows the whole of it in place of its
  // title, the mark's note with it; clicking it again folds it back.
  await page.evaluate(() => document.querySelectorAll("ol.plan li")[0].click());
  const opened = await planPanel();
  assert.ok(opened.folds[0].open && opened.folds[0].shown && !opened.folds[0].titleShown && opened.folds[0].noteShown, `the opened step: ${JSON.stringify(opened.folds[0])}`);
  assert.match(opened.folds[0].body, /^Change: app\.js sessionRow — under each title/);
  step("a step opens on click");
  await page.evaluate(() => document.querySelectorAll("ol.plan li")[0].click());
  assert.ok(!(await planPanel()).folds[0].open, "the step folded again");

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
  // The Totals panel: the task's whole spend, its running time (a wait for
  // an answer still runs) and its working time (a wait is not work). While
  // the ask waits the running line moves and the working line holds; the
  // work after the answers moves both.
  const totals = () =>
    page.evaluate(() => {
      const panel = [...document.querySelectorAll(".panel")].find((p) => p.dataset.totals);
      if (!panel) return null;
      const text = (name) => [...panel.querySelectorAll(".meter,.kv")].find((n) => n.firstElementChild?.textContent === name || n.querySelector(".meter-head span")?.textContent === name)?.textContent;
      return { spend: text("Total spend"), running: text("Total running time"), working: text("Total working time") };
    });
  await page.click(".task-aside [role=tab]:has-text('Statistics')");
  const held = await totals();
  assert.match(held.spend, /^Total spendat least \$0\.0000$/, `the unpriced spend: ${held.spend}`);
  assert.match(held.running, /^Total running time\d/, `the running time: ${held.running}`);
  assert.match(held.working, /^Total working time\d/, `the working time: ${held.working}`);
  await new Promise((r) => setTimeout(r, 2300));
  const still = await totals();
  assert.notStrictEqual(still.running, held.running, `the running time held while the ask waited: ${held.running}`);
  assert.strictEqual(still.working, held.working, `the working time grew while the ask waited: ${held.working} then ${still.working}`);
  step("totals split while the ask waits");
  // The ask is answered through the "Don't ask" switch, as a person
  // stopping the asking does. While it waits the clocks split as with any
  // ask; the switch's record names the question whose wait it ended
  // (prompt_id), so the wait stays out of the working time and the
  // working line keeps its gap below the running one — where a wait
  // folded into the work would make the two match, as the report's
  // session showed. What the switch answered, nothing asks again: the
  // second command runs without a card.
  //
  // The switch is there only once Settings offers it (approval.dont_ask,
  // off by default; ADR 17, amended 2026-10-05): until then the task has
  // none. Offering it shows the warning before anything is saved; Save
  // sends the setting; back on the task the switch is offered, off, and
  // the ask still waits for it.
  assert.ok(await page.$eval(".dont-ask", (el) => el.hidden), "the switch showed while Settings did not offer it");
  const taskHash = await page.evaluate(() => location.hash);
  await page.evaluate(() => (location.hash = "#/settings"));
  await page.waitForSelector("#section-approval");
  assert.strictEqual(await page.$("#dont-ask-warning"), null, "the warning showed while Don't ask was not offered");
  await page.click("[id='set-approval.dont_ask-true']");
  await page.waitForSelector("#dont-ask-warning");
  await shows(page, "#dont-ask-warning", "Don't ask hands a task the keys to your account.", "SSH private key");
  await page.locator("#dont-ask-warning").scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${out}/dont-ask-warning.png` });
  await page.click(".save-bar button:has-text('Save')");
  await until("the setting saved", async () => sent.of("settings.write").length === 1);
  assert.deepStrictEqual(sent.of("settings.write")[0], { set: { "approval.dont_ask": true } }, "what Save sent");
  await until("the saved toast", async () => (await page.textContent(".toast"))?.includes("Every task now offers Don't ask"));
  assert.ok(await page.$("#dont-ask-warning"), "the warning went once Don't ask was saved as offered");
  await page.evaluate((hash) => (location.hash = hash), taskHash);
  await until("the switch offered", async () => (await page.$(".dont-ask")) && !(await page.$eval(".dont-ask", (el) => el.hidden)));
  assert.strictEqual(await page.$eval(".dont-ask input", (el) => el.checked), false, "the offered switch starts off");
  await shows(page, ".ask-card", "Run ls -la");
  step("Don't ask offered in Settings, behind its warning");
  await page.$eval(".dont-ask input", (el) => el.click());
  assert.deepStrictEqual(await page.$eval(".dont-ask", (el) => ({ checked: el.querySelector("input").checked, on: el.classList.contains("on"), disabled: el.querySelector("input").disabled })), { checked: true, on: true, disabled: true }, "the tick appears immediately and stays while the server decides");
  await until("the switch cleared the waiting ask", async () => !(await page.$(".ask-card")));
  await until("the approval.change info reload held", async () => heldApprovalInfo !== null);
  socket.send(heldApprovalInfo);
  heldApprovalInfo = null;
  approvalEventSeen = false;
  await until("the approval.change info reload applied", async () => page.$eval(".dont-ask input", (el) => el.checked && el.disabled));
  assert.deepStrictEqual(await page.$eval(".dont-ask", (el) => ({ checked: el.querySelector("input").checked, on: el.classList.contains("on"), disabled: el.querySelector("input").disabled })), { checked: true, on: true, disabled: true }, "approval.change info reload keeps the tick while the RPC reply is pending");
  socket.send(heldApproveReply);
  heldApproveReply = null;
  await until("the switch state reloaded", async () => !(await page.$eval(".dont-ask input", (el) => el.disabled)));
  await page.screenshot({ path: `${out}/switched.png` });
  // An overlapping older info read must not overwrite the newest result.
  const newestInfoWins = await page.evaluate(async () => {
    const { TaskView } = await import("/task.js");
    const pending = [];
    const view = Object.create(TaskView.prototype);
    Object.assign(view, {
      conn: { call: () => new Promise((resolve) => pending.push(resolve)) },
      item: { id: "race" }, version: 1, infoVersion: 0, info: null,
      aside() {}, header() {}, controls() {},
    });
    const old = view.loadInfo();
    const fresh = view.loadInfo();
    pending[1]({ approve_all: true });
    await fresh;
    pending[0]({ approve_all: false });
    await old;
    return view.info.approve_all;
  });
  assert.strictEqual(newestInfoWins, true, "an older session.info reply cannot overwrite the newest state");
  await until("the ask cleared by the switch", async () => !(await page.$(".ask-card")));
  await page.waitForSelector(".prose strong", { timeout: 15000 });
  await until("the turn's end", async () => (await page.textContent(".task-head .pill")) === "Idle");
  assert.deepStrictEqual(sent.of("session.answer").map((a) => [a.id, a.reply]), [], "the switch answered; no Y was sent");
  step("the ask answered by the switch; task answered");
  // A refused switch-off restores the server's on-state and reports the refusal.
  failNextApprove = true;
  await page.$eval(".dont-ask input", (el) => el.click());
  assert.deepStrictEqual(await page.$eval(".dont-ask", (el) => ({ checked: el.querySelector("input").checked, on: el.classList.contains("on"), disabled: el.querySelector("input").disabled })), { checked: false, on: false, disabled: true }, "the requested off-state appears while pending");
  await until("the refusal", async () => (await page.textContent(".toast.error"))?.includes("simulated approval refusal"));
  // An error stays until it is dismissed: a confirmation shown after it does
  // not replace it, it is announced as an alert, and it does not sit over
  // the composer.
  assert.strictEqual(await page.getAttribute(".toast.error", "role"), "alert");
  await page.evaluate(async () => (await import("/dom.js")).toast("Something else."));
  await page.waitForSelector("#toast:not([hidden])");
  assert.strictEqual(await page.locator(".toast.error").count(), 1, "a later toast replaced the error");
  const clear = await page.evaluate(() => document.querySelector("#toasts").getBoundingClientRect().bottom <= document.querySelector("form.reply").getBoundingClientRect().top);
  assert.ok(clear, "the messages cover the composer");
  await page.click(".toast.error [aria-label=Dismiss]");
  assert.strictEqual(await page.locator(".toast.error").count(), 0, "Dismiss left the error on screen");
  await until("the refused switch restored", async () => page.$eval(".dont-ask", (el) => el.querySelector("input").checked && el.classList.contains("on") && !el.querySelector("input").disabled));
  await page.screenshot({ path: `${out}/switch-refused.png` });
  // The panel ticks only while the turn runs, and a turn faster than a
  // tick leaves it stale, so a tab away and back redraws it from the
  // settled log before the two lines are read. What must hold is that
  // the working time stays below the running one by the switch's wait,
  // where a wait folded into the work would make the two read the same,
  // as the report's session showed. The heads carry their labels, so the
  // values are what compare.
  const value = (head) => head.replace(/^Total (running|working) time/, "");
  await page.click(".task-aside [role=tab]:has-text('Changes')");
  await page.click(".task-aside [role=tab]:has-text('Statistics')");
  const switched = await totals();
  assert.notStrictEqual(value(switched.working), value(switched.running), `the working time matched the running time once the switch answered: ${switched.running} / ${switched.working}`);
  // The same on the log the panel adds up: the switch's wait, read off
  // the log as the span from the command's call to its confirm, stays out
  // of the working time. A switch whose answer folded its wait in would
  // leave the working time within the span of the running one.
  const events = fs.readFileSync(sessionLog(id), "utf8").trim().split("\n").map((l) => JSON.parse(l)).map(({ seq, ts, kind, payload }) => ({ seq, ts, kind, payload }));
  const at = (e) => new Date(e.ts).getTime();
  let asked1 = null,
    waited = 0;
  for (const e of events) {
    if (e.kind === "tool.call" && e.payload?.name === "shell-exec") asked1 = at(e);
    if (asked1 !== null && e.kind === "guard.decision" && e.payload?.decision === "confirm") {
      waited += at(e) - asked1;
      asked1 = null;
    }
  }
  const clocks = await page.evaluate(async (events) => {
    const { TaskView } = await import("/task.js");
    const view = new TaskView({ onEvent: () => () => {}, has: () => false }, {}, () => {});
    view.busy = false;
    view.prompts = new Map();
    return view.tally(events).run;
  }, events);
  assert.ok(clocks.total - clocks.work >= waited - 500, `the switch's wait (${waited} ms) stayed out of the working time: ${clocks.work} of ${clocks.total}`);

  // Both ran: the switch's note between them ends the run they would
  // fold into, so each shows in a run of its own (commands folding into
  // one run is the transcript check's).
  const ran = await run();
  assert.strictEqual(await page.$$eval(".activity.commands", (gs) => gs.length), 2, "the switch's note between the commands ends the run");
  assert.strictEqual(ran.label, "Ran 1 command");
  assert.match(ran.what, /^in \d+\.\d s$/);
  assert.strictEqual(ran.open, false, "the finished run is folded");
  assert.deepStrictEqual(ran.rows.map((r) => r.name.replace(/\s+/g, " ").trim()), ["wc -l app.js"]);
  assert.ok(ran.rows.every((r) => /^exit 0/.test(r.stat)), `the run's rows: ${JSON.stringify(ran.rows)}`);
  // The changes are in their tab now; Progress comes back for the plan below.
  await page.click(".task-aside [role=tab]:has-text('Changes')");
  const after = await page.evaluate(() => ({
    card: !!document.querySelector(".ask-card"),
    edit: [...document.querySelectorAll(".step")].map((s) => s.textContent.replace(/\s+/g, " ").trim()).find((t) => t.startsWith("Edit")),
    changes: [...document.querySelectorAll(".file-row")].map((r) => r.textContent.trim()),
    code: document.querySelector(".prose pre code")?.textContent,
  }));
  assert.strictEqual(after.card, false, "an approval is left");
  assert.match(after.edit, /^Editapp\.jswritten.*went ahead: a write in the folder$/);
  assert.deepStrictEqual(after.changes, ["app.jsapprovedHistory"]);
  async function changesPreviewInspector() {
    await page.focus(".change-toggle");
    await page.keyboard.press("Enter");
    await shows(page, ".change-inspector", "Approved preview—not proof of a successful write");
    assert.ok(await page.locator(".change-inspector .diff-line.add").count(), "recorded additions missing");
    assert.ok(await page.locator(".change-inspector .diff-line.del").count(), "recorded deletions missing");
    await page.click(".task-aside [role=tab]:has-text('Progress')");
    await page.click(".task-aside [role=tab]:has-text('Changes')");
    assert.strictEqual(await page.getAttribute(".change-toggle", "aria-expanded"), "true", "preview folded during redraw");
    await page.screenshot({ path: `${out}/changes-preview.png` });
  }
  await changesPreviewInspector();
  assert.strictEqual(after.code, 'const meta = [session.repo, when, state].join(" · ");');
  await page.click(".task-aside [role=tab]:has-text('Progress')");
  const marked = await planPanel();
  assert.deepStrictEqual(marked.steps, before.steps, "the plan's steps after the approvals");
  assert.strictEqual(marked.head, "Plan3 of 4 done · drafted");
  assert.deepStrictEqual(marked.items, ["done fold alt", "done fold alt", "skipped fold alt", "done fold"]);
  // The Check step's title is its whole text; marked with a note, the note
  // goes behind its fold, and the title stays when it opens.
  await page.evaluate(() => document.querySelectorAll("ol.plan li")[3].click());
  const noted = await planPanel();
  assert.ok(noted.folds[3].open && noted.folds[3].noteShown && noted.folds[3].titleShown && !noted.folds[3].shown, `the Check step's fold: ${JSON.stringify(noted.folds[3])}`);
  await page.evaluate(() => document.querySelectorAll("ol.plan li")[3].click());
  assert.ok(!(await planPanel()).folds[3].open, "the Check step folded again");
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
  const want = { went: true, answer: true, head: "Plan4 of 4 done · carried over from an earlier turn", items: ["done fold alt", "done fold alt", "done fold alt", "done fold"] };
  await until("the carried plan", async () => {
    const got = await carried();
    return JSON.stringify({ ...got, steps: undefined, folds: undefined }) === JSON.stringify({ ...want, steps: undefined, folds: undefined });
  }).catch(async (e) => {
    throw new Error(`${e.message}: ${JSON.stringify(await carried())}`);
  });
  assert.deepStrictEqual((await planPanel()).steps.map((s) => s.split(":")[0]), ["Change", "Test", "Docs", "Check"]);
  step("plan carried");
  await page.screenshot({ path: `${out}/carried-plan.png` });
  // The work since the first ask (the commands, the follow-up and the
  // rate-limited turn) moved both clocks on: the wait counted for running,
  // not for working, and the work after it counted for both.
  await page.click(".task-aside [role=tab]:has-text('Statistics')");
  const moved = await totals();
  assert.notStrictEqual(moved.running, held.running, `the running time did not move with the wait and the work: ${held.running}`);
  assert.notStrictEqual(moved.working, held.working, `the working time did not move with the work: ${held.working}`);

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
  // Home offers this one Review only: a security confirmation is answered
  // where its reasons and diff are, not from a line on Home.
  const makefileTask = await page.evaluate(() => location.hash);
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("article.card.warn:has-text('Makefile')");
  const makefileCard = await page.$eval("article.card.warn:has-text('Makefile') .card-actions", (e) => e.textContent);
  assert.ok(makefileCard.includes("Review") && !makefileCard.includes("Allow once"), `Home offered for the Makefile: ${makefileCard}`);
  await page.evaluate((hash) => (location.hash = hash), makefileTask);
  await page.waitForSelector(".ask-card .diff");
  // Keyboard answers, as the terminal does — the reply box's focus let go
  // first, since a key pressed into a text field is text, not an answer.
  await page.evaluate(() => document.activeElement?.blur());
  await page.keyboard.press("y");
  await until("answer after the edits", async () => (await transcript()).includes("Quieted"));
  await until("answer after the edits", async () => (await transcript()).includes("Quieted"));
  await until("answer after the edits", async () => (await transcript()).includes("Quieted"));
  assert.strictEqual(fs.readFileSync(makefile, "utf8"), "build:\n\t@go build ./...\n\ntest:\n\t@go test ./... >/dev/null\n", "the Makefile after the answer");
  assert.deepStrictEqual(sent.of("session.answer").map((a) => a.reply), ["y"]);
  const row = await page.evaluate(() => {
    const step = [...document.querySelectorAll(".step")].find((s) => /Edit\s*Makefile/.test(s.textContent));
    return step && { text: step.textContent.replace(/\s+/g, " ").trim(), args: step.nextElementSibling?.querySelector("pre")?.textContent };
  });
  assert.ok(row && row.text.includes("written") && row.args?.includes('"edits"'), `the transcript does not show the edit and its arguments: ${JSON.stringify(row)}`);
  assert.deepStrictEqual(sent.of("session.send").map((s) => s.text), ["Thanks", "Tidy the notes", "continue", "Quiet the build"]);
  step("edits written");
  await page.screenshot({ path: `${out}/edits-written.png` });

  // The rules both clocks add up with, against the console's own module on
  // crafted logs: a person's wait stays out of the working time and stays
  // in the running time, the judge's and a run's own answers are not waits,
  // a run that went on by itself ran through the gap between its turns, and
  // a gap across a resume was the person's who read and resumed. A turn a
  // crash cut off (no turn.end) ends where the log does: the time until the
  // resume was nobody's, and a task nothing runs in now does not count up
  // to now.
  const rules = await page.evaluate(async () => {
    const { TaskView } = await import("/task.js");
    const view = new TaskView({ onEvent: () => () => {}, has: () => false }, {}, () => {});
    const base = Date.now() - 3600_000;
    const ev = (s, kind, payload = {}) => ({ seq: s, ts: new Date(base + s * 1000).toISOString(), kind, payload });
    const run = (events, busy = false, prompts = 0) => {
      view.busy = busy;
      view.prompts = new Map(prompts ? [["p", {}]] : []);
      return view.turnClocks(events);
    };
    const turn = (extra) => [ev(0, "user.msg", { text: "go" }), ev(1, "llm.req"), ev(2, "llm.resp", { usage: {} }), ...extra];
    return {
      plain: run(turn([ev(4, "turn.end")])),
      waited: run(turn([ev(3, "tool.call", { id: "c1", name: "shell-exec" }), ev(8, "guard.decision", { decision: "confirm", answer: "yes" }), ev(9, "tool.result", { id: "c1", ok: true }), ev(10, "turn.end")])),
      judged: run(turn([ev(3, "tool.call", { id: "c1", name: "shell-exec" }), ev(8, "guard.decision", { decision: "confirm", answer: "yes", actor: "judge" }), ev(9, "tool.result", { id: "c1", ok: true }), ev(10, "turn.end")])),
      live: run([ev(0, "user.msg"), ev(2, "llm.req")], true, 1),
      bridged: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: true, actor: "unattended" }), ev(5, "run.turn", { number: 2 }), ev(9, "turn.end")]),
      asked: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: true, prompt_id: "p1" }), ev(5, "run.turn", { number: 2 }), ev(9, "turn.end")]),
      switched: run(turn([ev(3, "tool.call", { id: "c1", name: "shell-exec" }), ev(8, "approval.change", { kind: "all", scope: "session", prompt_id: "p1" }), ev(8, "guard.decision", { decision: "confirm", answer: "yes", prompt_id: "p1" }), ev(9, "tool.result", { id: "c1", ok: true }), ev(10, "turn.end")])),
      resumed: run([...turn([ev(4, "turn.end")]), ev(30, "session.resume"), ev(31, "run.state", { status: "running" }), ev(32, "run.turn", { number: 1 }), ev(36, "turn.end")]),
      stopped: run([...turn([ev(4, "turn.end")]), ev(4.5, "run.checkpoint", { number: 1, allowed: false, actor: "unattended" }), ev(40, "session.resume"), ev(41, "run.state", { status: "running" }), ev(42, "run.turn", { number: 1 }), ev(46, "turn.end")]),
      crashed: run([...turn([]), ev(1000, "session.resume"), ev(1001, "user.msg", { text: "again" }), ev(1002, "llm.req"), ev(1004, "turn.end")]),
      saved: run(turn([])),
    };
  });
  assert.strictEqual(rules.plain.ms, 4000, `a turn with no wait runs 4 s: ${rules.plain.ms}`);
  assert.strictEqual(rules.plain.work, 4000, `a turn with no wait works 4 s: ${rules.plain.work}`);
  assert.strictEqual(rules.waited.ms, 10000, `a person's wait stays in the running time: ${rules.waited.ms}`);
  assert.strictEqual(rules.waited.work, 5000, `a person's wait stays out of the working time: ${rules.waited.work}`);
  assert.strictEqual(rules.judged.ms, 10000, `the judge's deciding runs: ${rules.judged.ms}`);
  assert.strictEqual(rules.judged.work, 10000, `the judge's deciding is working, not a wait: ${rules.judged.work}`);
  assert.ok(rules.live.ms > 3500000, `a prompt still pending holds the working time, not the running time: ${rules.live.ms}`);
  assert.strictEqual(rules.live.work, 2000, `a prompt still pending holds the working time: ${rules.live.work}`);
  assert.strictEqual(rules.bridged.ms, 9000, `a run that went on by itself ran through the gap: ${rules.bridged.ms}`);
  assert.strictEqual(rules.bridged.work, 9000, `a run that went on by itself worked through the gap: ${rules.bridged.work}`);
  assert.strictEqual(rules.asked.ms, 8000, `a checkpoint the person answered leaves the gap to them: ${rules.asked.ms}`);
  assert.strictEqual(rules.asked.work, 8000, `a checkpoint the person answered leaves the gap to them: ${rules.asked.work}`);
  assert.strictEqual(rules.switched.ms, 10000, `a switch that answered a waiting ask keeps the wait in the running time: ${rules.switched.ms}`);
  assert.strictEqual(rules.switched.work, 5000, `a switch that answered a waiting ask keeps the wait out of the working time: ${rules.switched.work}`);
  assert.strictEqual(rules.resumed.ms, 8000, `a gap across a resume was the person's: ${rules.resumed.ms}`);
  assert.strictEqual(rules.resumed.work, 8000, `a gap across a resume was the person's: ${rules.resumed.work}`);
  assert.strictEqual(rules.stopped.ms, 8000, `a run that stopped at its checkpoint left the gap to the person: ${rules.stopped.ms}`);
  assert.strictEqual(rules.stopped.work, 8000, `a run that stopped at its checkpoint left the gap to the person: ${rules.stopped.work}`);
  assert.strictEqual(rules.crashed.ms, 5000, `a turn a crash cut off ends at the resume: ${rules.crashed.ms}`);
  assert.strictEqual(rules.crashed.work, 5000, `a turn a crash cut off ends at the resume: ${rules.crashed.work}`);
  assert.strictEqual(rules.saved.ms, 2000, `a cut-off turn in a task nothing runs in does not count up to now: ${rules.saved.ms}`);
  assert.strictEqual(rules.saved.work, 2000, `a cut-off turn in a task nothing runs in does not count up to now: ${rules.saved.work}`);
  // The panel's tick, once a second while a turn runs, adds up a long log
  // once, not on every tick: a thousand ticks over 20,000 events take a
  // moment, where walking and sorting the log each time took seconds.
  const ticks = await page.evaluate(async () => {
    const { TaskView } = await import("/task.js");
    const view = new TaskView({ onEvent: () => () => {}, has: () => false }, {}, () => {});
    const base = Date.now() - 3600_000;
    let reads = 0;
    for (let s = 1; s <= 20000; s++) view.seen.set(s, { seq: s, ts: new Date(base + s * 100).toISOString(), kind: s === 1 ? "user.msg" : "llm.resp", payload: { get usage() { reads++; return { cost: 0.001, prompt_tokens: 100, cached_tokens: 25 }; } } });
    view.busy = true;
    const first = view.totals();
    const tallied = view.tallied;
    const initialReads = reads;
    const began = performance.now();
    for (let i = 0; i < 1000; i++) view.totals();
    const ms = performance.now() - began;
    const rate = (panel) => [...panel.querySelectorAll(".kv")].find((n) => n.firstElementChild.textContent === "Token cache rate")?.lastElementChild.textContent ?? null;
    let rendered;
    for (let i = 0; i < 1000; i++) rendered = rate(view.totalsPanel());
    const reused = view.tallied === tallied && view.totals().cache === first.cache && reads === initialReads;
    // A newly recorded response invalidates the same cached aggregation
    // the clocks use, and changes the rendered rate, not just its inputs.
    view.seen.set(20001, { seq: 20001, ts: new Date(base + 20001 * 100).toISOString(), kind: "llm.resp", payload: { usage: { prompt_tokens: 1999900, cached_tokens: 1999900 } } });
    const updated = rate(view.totalsPanel());
    const result = { ms, cost: first.cost, cache: first.cache, rendered, reused, refreshed: view.tallied !== tallied && reads > initialReads, updated, newCache: view.totals().cache };
    view.dispose();
    return result;
  });
  assert.ok(Math.abs(ticks.cost - 19.999) < 1e-6, `the long log's spend: ${ticks.cost}`);
  assert.ok(ticks.ms < 250, `a thousand ticks over a long log took ${Math.round(ticks.ms)} ms`);
  assert.deepStrictEqual(ticks.cache, { prompt: 1999900, cached: 499975 }, "session cache totals reuse: whole-session raw token totals");
  assert.strictEqual(ticks.rendered, "25%", "session cache totals reuse: repeated Totals renders show the rate");
  assert.ok(ticks.reused, "session cache totals reuse: a thousand ticks and renders do not read usage again");
  assert.ok(ticks.refreshed, "session cache totals reuse: new usage invalidates the aggregation");
  assert.deepStrictEqual(ticks.newCache, { prompt: 3999800, cached: 2499875 }, "session cache totals reuse: new usage reaches token totals");
  assert.strictEqual(ticks.updated, "63%", "session cache totals reuse: new usage updates the rendered percentage");
  step("session cache totals reuse");
  step("totals rules");

  // Home: nothing waits for an answer now, and the task is with the finished.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  const sidebar = await page.innerText("#sidebar");
  assert.ok(!sidebar.includes("NEEDS YOU"), `the sidebar still has a task waiting:\n${sidebar}`);
  await shows(page, "#view", "RECENTLY FINISHED", "The session list shows");
  await page.screenshot({ path: `${out}/home-after.png` });

  // An image's confirm comes before any turn (ADR 22), so no idle follows
  // its answer. A task started from Home with an image, denied or stopped,
  // is idle afterwards, not Working with nothing to stop: the image is left
  // out, the message unsent, and the task takes the next one.
  const shot = `${tempDir("nodwyn-shot-")}/shot.png`;
  fs.writeFileSync(shot, Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64"));
  const head = () =>
    page.evaluate(() => {
      const button = (label) => [...document.querySelectorAll(".task-actions button")].find((b) => b.textContent.trim() === label);
      return { pill: document.querySelector(".task-head .pill")?.textContent, stop: button("Stop")?.disabled, close: button("Close")?.disabled };
    });
  const settles = (what, want) =>
    until(what, async () => JSON.stringify(await head()) === JSON.stringify(want), head);
  const withImage = async (text) => {
    await page.evaluate(() => (location.hash = "#/"));
    await page.waitForSelector("#task");
    await page.click(`button.chip[title="${repo}"]`);
    await page.fill("#task", text);
    await page.setInputFiles(".ask input[type=file]", shot);
    await page.waitForSelector(".ask .attach-chip");
    // The bridge answers the refused upload 422, which the page logs.
    expectError(/^Failed to load resource: the server responded with a status of 422 \(Unprocessable Entity\)$/);
    await page.press("#task", "Control+Enter");
    await page.waitForSelector(".ask-card h2:has-text('Send the image shot.png to the model?')", { timeout: 15000 });
    await settles("Stop while the image's confirm waits", { pill: "Waiting for you", stop: false, close: true });
    return decodeURIComponent((await page.evaluate(() => location.hash)).replace("#/task/", ""));
  };
  const idle = { pill: "Idle", stop: true, close: false };
  const denied = await withImage("Describe this screenshot");
  await page.click(".ask-card button[data-key='N']");
  await settles("the task idle after the image was denied", idle);
  await until("the note that the image was declined", async () => (await page.textContent(".transcript")).includes("image not attached: shot.png, declined"));
  assert.deepStrictEqual(sent.of("session.answer").map((a) => [a.id, a.reply]).at(-1), [denied, "n"]);
  assert.ok(!sent.of("session.send").some((s) => s.id === denied), "the message went without its image");
  // The message that was not sent waits in the task's reply box, where the
  // person is, and Home starts afresh: no draft, and no image chosen to be
  // asked about again.
  await until("the message in the task's reply box", async () => (await page.inputValue("#reply")) === "Describe this screenshot");
  await page.screenshot({ path: `${out}/image-denied.png` });
  await page.fill("#reply", "Describe the folder instead");
  await page.keyboard.press("Enter");
  await until("the answer without the image", async () => (await transcript()).includes("This request carries 0 image(s)"));
  await settles("the task idle after its answer", idle);
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  const home = await page.evaluate(() => ({ draft: document.querySelector("#task").value, chosen: document.querySelectorAll(".ask .attach-chip").length }));
  assert.deepStrictEqual(home, { draft: "", chosen: 0 }, "Home after the message went to the task");
  step("image denied; the message waited in the task, which went on");

  const stopped = await withImage("Describe this screenshot again");
  await page.click(".task-actions button:has-text('Stop')");
  await settles("the task idle after Stop", idle);
  await until("the note that the image was canceled", async () => (await page.textContent(".transcript")).includes("image not attached: shot.png, canceled"));
  assert.deepStrictEqual(sent.of("session.abort").at(-1), { id: stopped });
  assert.ok(!sent.of("session.send").some((s) => s.id === stopped), "the message went without its image");
  step("image stopped");

  // The transport exception is off, saved explicitly, and retained after a
  // reload; turning it off again sends and persists false, not an empty string.
  await page.evaluate(() => (location.hash = "#/settings/keys"));
  await page.waitForSelector("[id='set-policy.allow_insecure_http-default']");
  assert.strictEqual(await page.getAttribute("[id='set-policy.allow_insecure_http-default']", "aria-pressed"), "true");
  for (const allow of [true, false]) {
    await page.click(`[id='set-policy.allow_insecure_http-${allow || "default"}']`);
    const beforeSave = sent.of("settings.write").length;
    await page.click(".save-bar button:has-text('Save')");
    await until("the transport setting saved", async () => sent.of("settings.write").length === beforeSave + 1 && await page.$eval(".save-bar", (el) => el.hidden));
    assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "policy.allow_insecure_http": allow } });
    await page.reload();
    await ready(page);
    await page.waitForSelector(`[id='set-policy.allow_insecure_http-${allow || "default"}'][aria-pressed=true]`);
  }
  step("credential transport setting defaults off, saves on and off, and survives reload");

});
