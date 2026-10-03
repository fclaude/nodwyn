const { chromium } = require("playwright");
const { spawn } = require("child_process");
const fs = require("fs");
const out = process.argv[2];
(async () => {
  const demo = spawn(process.env.DEMO, ["-seed"], { stdio: ["pipe", "pipe", "inherit"] });
  process.on("exit", () => demo.kill("SIGKILL"));
  const step = (m) => console.log("step:", m);
  let buf = "";
  const url = await new Promise((resolve) => demo.stdout.on("data", (d) => { buf += d; const m = /URL (\S+)/.exec(buf); if (m) resolve(m[1]); }));
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  await page.goto(url);
  await page.waitForSelector(".nav-task.dotted", { timeout: 15000 });
  await page.click(".nav-task.dotted");
  await page.waitForSelector(".ask-card");
  // The plan panel shows every step of the planner's plan, each with its
  // kind, Test: and Docs: included, and counts the steps the model marks:
  // two before the command it asks about, two more once it is approved.
  const planPanel = () =>
    page.evaluate(() => ({
      head: [...document.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.textContent.startsWith("Plan"))?.querySelector("h2")?.textContent,
      steps: [...document.querySelectorAll("ol.plan li .text")].map((s) => s.textContent),
      items: [...document.querySelectorAll("ol.plan li")].map((li) => li.className),
    }));
  const before = await planPanel();
  console.log(JSON.stringify(before));
  if (before.steps.map((s) => s.split(":")[0]).join() !== "Change,Test,Docs,Check" || !/fails today: /.test(before.steps[1]) || before.head !== "Plan2 of 4 done · drafted" || before.items.join() !== "done,done,next,")
    throw new Error("the plan panel does not show the plan's steps, two marked, at the approval");
  // Keyboard answer, as the terminal does.
  step("card shown");
  await page.keyboard.press("y");
  step("pressed y");
  try { await page.waitForSelector(".step .result.ok", { timeout: 15000 }); } catch (e) { await page.screenshot({ path: `${out}/stuck.png` }); console.log(await page.evaluate(() => document.querySelector(".transcript").innerText.slice(-1500))); throw e; }
  step("command finished");
  for (let i = 0; i < 40; i++) {
    if (await page.$(".prose strong")) break;
    if (await page.$(".ask-card")) { step("another question: " + (await page.textContent(".ask-card h2"))); await page.screenshot({ path: `${out}/second-ask.png` }); await page.keyboard.press("y"); }
    await page.waitForTimeout(500);
  }
  await page.waitForTimeout(1000);
  const checks = await page.evaluate(() => ({
    card: !!document.querySelector(".ask-card"),
    steps: [...document.querySelectorAll(".step")].map((s) => s.textContent.replace(/\s+/g, " ").trim()),
    changes: [...document.querySelectorAll(".file-row")].map((r) => r.textContent.trim()),
    pill: document.querySelector(".task-head .pill")?.textContent,
    code: document.querySelector(".prose pre code")?.textContent,
  }));
  console.log(JSON.stringify(checks, null, 1));
  const after = await planPanel();
  console.log(JSON.stringify(after));
  if (after.steps.join("\n") !== before.steps.join("\n") || after.head !== "Plan3 of 4 done · drafted" || after.items.join() !== "done,done,skipped,done")
    throw new Error("the plan panel does not count the steps marked after the approval");
  await page.screenshot({ path: `${out}/after.png` });
  // Home shows it finished; follow-up message in the same task.
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await page.waitForTimeout(2500);
  // A turn that a rate limit ends leaves its plan to "continue", which
  // goes on from the step it marked.
  const transcript = () => page.evaluate(() => document.querySelector(".transcript").innerText);
  const until = async (what, ok) => {
    for (let i = 0; i < 40; i++) {
      if (ok(await transcript())) return;
      await page.waitForTimeout(500);
    }
    await page.screenshot({ path: `${out}/stuck.png` });
    throw new Error(`no ${what}:\n${(await transcript()).slice(-1500)}`);
  };
  await page.fill("#reply", "Tidy the notes");
  await page.keyboard.press("Enter");
  await until("rate limit", (t) => t.includes("Stopped by an error"));
  step("rate limited");
  await page.fill("#reply", "continue");
  await page.keyboard.press("Enter");
  await until("answer after continue", (t) => t.includes("Tidied"));
  await page.waitForTimeout(800);
  // The model's plan-write was recorded (its plan has every kind), carried,
  // and the steps "continue" marked are counted.
  const went = await page.evaluate(() => [...document.querySelectorAll(".transcript *")].some((e) => e.children.length === 0 && /Went on with the plan · 1 of 4 steps marked/.test(e.textContent)));
  const carried = await planPanel();
  console.log(JSON.stringify({ went, ...carried }));
  if (!went || carried.head !== "Plan4 of 4 done · carried over from an earlier turn" || carried.items.join() !== "done,done,done,done" || carried.steps.map((s) => s.split(":")[0]).join() !== "Change,Test,Docs,Check")
    throw new Error("the plan was not carried");
  await page.screenshot({ path: `${out}/carried-plan.png` });
  // Two changes to one file in one fs-edit call (ADR 38). The Makefile is
  // a protected path, so the call asks although writes in the folder go
  // ahead: the card shows one diff with both edits, nothing is written
  // before the answer, and after it the file holds both.
  const makefile = `${/REPO (\S+)/.exec(buf)[1]}/Makefile`;
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
  console.log(JSON.stringify(card));
  if (card.title !== "Edit Makefile" || !card.security) throw new Error("not the Makefile's security confirm");
  if (card.added.join("|") !== "\t@go build ./...|\t@go test ./... >/dev/null" || card.removed.join("|") !== "\tgo build ./...|\tgo test ./...") throw new Error("the card does not show one diff of both edits");
  await page.screenshot({ path: `${out}/edits-ask.png` });
  if (fs.readFileSync(makefile, "utf8") !== original) throw new Error("the Makefile changed before the answer");
  // Focus is still in the reply box, where y would be typed: click.
  await page.click(".ask-card button[data-key='Y']");
  await until("answer after the edits", (t) => t.includes("Quieted"));
  const edited = fs.readFileSync(makefile, "utf8");
  if (edited !== "build:\n\t@go build ./...\n\ntest:\n\t@go test ./... >/dev/null\n") throw new Error(`the Makefile after the answer: ${JSON.stringify(edited)}`);
  const row = await page.evaluate(() => {
    const step = [...document.querySelectorAll(".step")].find((s) => /Edit\s*Makefile/.test(s.textContent));
    return step && { text: step.textContent.replace(/\s+/g, " ").trim(), args: step.nextElementSibling?.querySelector("pre")?.textContent };
  });
  console.log(JSON.stringify(row));
  if (!row || !row.text.includes("written") || !row.args?.includes('"edits"')) throw new Error("the transcript does not show the edit and its arguments");
  step("edits written");
  await page.screenshot({ path: `${out}/edits-written.png` });
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/home-after.png` });
  console.log(errors.length ? errors.join("\n") : "no page errors");
  await browser.close();
  demo.kill("SIGTERM");
})().catch((e) => { console.error("FAILED", e.message); process.exit(1); });
