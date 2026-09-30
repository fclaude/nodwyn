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
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/home-after.png` });
  console.log(errors.length ? errors.join("\n") : "no page errors");
  await browser.close();
  demo.kill("SIGTERM");
})().catch((e) => { console.error("FAILED", e.message); process.exit(1); });
