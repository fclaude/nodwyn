const { chromium } = require("playwright");
const { spawn } = require("child_process");
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
  const carried = await page.evaluate(() => ({
    went: [...document.querySelectorAll(".transcript *")].some((e) => e.children.length === 0 && /Went on with the plan · 1 of 2 steps marked/.test(e.textContent)),
    plan: [...document.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.textContent.startsWith("Plan"))?.querySelector("h2")?.textContent,
    items: [...document.querySelectorAll("ol.plan li")].map((li) => li.className),
  }));
  console.log(JSON.stringify(carried));
  if (!carried.went || carried.plan !== "Plan2 of 2 done · carried over from an earlier turn" || carried.items.join() !== "done,done") throw new Error("the plan was not carried");
  await page.screenshot({ path: `${out}/carried-plan.png` });
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/home-after.png` });
  console.log(errors.length ? errors.join("\n") : "no page errors");
  await browser.close();
  demo.kill("SIGTERM");
})().catch((e) => { console.error("FAILED", e.message); process.exit(1); });
