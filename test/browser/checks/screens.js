const { chromium } = require("playwright");
const { spawn } = require("child_process");
const out = process.argv[2];
(async () => {
  const demo = spawn(process.env.DEMO, ["-seed"], { stdio: ["pipe", "pipe", "inherit"] });
  const lines = [];
  const waitLine = (prefix) => new Promise((resolve) => {
    const check = () => { const l = lines.find((x) => x.startsWith(prefix)); if (l) { lines.splice(lines.indexOf(l), 1); resolve(l.slice(prefix.length + 1)); } else setTimeout(check, 50); };
    check();
  });
  demo.stdout.on("data", (d) => lines.push(...d.toString().split("\n").filter(Boolean)));
  const url = await waitLine("URL");
  const base = url.split("/#")[0];
  const browser = await chromium.launch();
  const errors = [];
  // The browser always reports a light system: dark is the console's default,
  // and light only a stored choice, so the dark shots check the default too.
  const shoot = async (name, viewport, theme, path, fn) => {
    const ctx = await browser.newContext({ viewport, colorScheme: "light", deviceScaleFactor: 1 });
    if (theme === "light") await ctx.addInitScript(() => localStorage.setItem("nodwyn-theme", "light"));
    const page = await ctx.newPage();
    page.on("console", (m) => { if (m.type() === "error" || m.type() === "warning") errors.push(`${name}: ${m.text()}`); });
    page.on("pageerror", (e) => errors.push(`${name}: ${e.message}`));
    demo.stdin.write("\n");
    const ticket = await waitLine("TICKET");
    await page.goto(`${base}/#ticket=${ticket}`);
    await page.waitForSelector(".sidebar .brand", { timeout: 10000 });
    await page.waitForTimeout(3500);
    if (path) { await page.evaluate((p) => (location.hash = p), path); await page.waitForTimeout(1200); }
    if (fn) await fn(page);
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const want = theme === "light" ? "rgb(245, 242, 234)" : "rgb(23, 22, 19)";
    if (bg !== want) throw new Error(`${name}: background ${bg}, want ${want} (${theme})`);
    await page.screenshot({ path: `${out}/${name}.png`, fullPage: false });
    await ctx.close();
  };
  await shoot("home", { width: 1440, height: 900 }, "light");
  const taskHash = async (page) => {
    const href = await page.getAttribute(".nav-group a.nav-task.dotted", "href");
    await page.evaluate((p) => (location.hash = p), href);
    await page.waitForSelector(".ask-card", { timeout: 10000 });
    await page.waitForTimeout(800);
  };
  // Thinking is hidden unless this browser chose to show it.
  const visibleThinking = (page) => page.$$eval(".thinking", (ns) => ns.filter((n) => n.getClientRects().length > 0).length);
  await shoot("task", { width: 1440, height: 1080 }, "light", null, async (page) => {
    await taskHash(page);
    const thoughts = await page.$$eval(".thinking", (ns) => ns.length);
    if (thoughts !== 2 || (await visibleThinking(page)) !== 0) throw new Error(`by default ${await visibleThinking(page)} of ${thoughts} thinking rows show`);
  });
  // Settings › This browser shows it and hides it again, and remembers.
  const showThinking = async (page) => {
    await page.evaluate(() => (location.hash = "#/settings"));
    await page.click('button.chip:has-text("Shown")');
    const shown = await page.evaluate(() => [document.documentElement.dataset.thinking, localStorage.getItem("nodwyn-thinking")]);
    if (shown.join() !== "shown,shown") throw new Error(`after Shown: ${shown}`);
  };
  await shoot("settings-thinking", { width: 1280, height: 800 }, "light", null, async (page) => {
    await showThinking(page);
    await page.click('button.chip:has-text("Hidden")');
    const hidden = await page.evaluate(() => [document.documentElement.dataset.thinking, localStorage.getItem("nodwyn-thinking")]);
    if (hidden[0] !== undefined || hidden[1] !== null) throw new Error(`after Hidden: ${hidden}`);
    await page.click('button.chip:has-text("Shown")');
    await page.locator('button.chip:has-text("Shown")').scrollIntoViewIfNeeded();
  });
  // A reply that only thinks and reads goes with its reads: its thinking is
  // a row in their group, never a line of its own under it.
  const activity = async (page) => {
    await showThinking(page);
    await taskHash(page);
    await page.click(".turn.agent .activity > summary");
    await page.locator(".turn.agent .activity").scrollIntoViewIfNeeded();
    const inGroup = await page.$$eval(".turn.agent .activity-body > .thinking", (ns) => ns.map((n) => n.querySelector("summary").textContent));
    const alone = await page.$$eval(".turn.agent > .thinking", (ns) => ns.length);
    if (inGroup.length !== 2 || alone !== 0) throw new Error(`thinking in the group: ${JSON.stringify(inGroup)}; on its own: ${alone}`);
    if ((await visibleThinking(page)) !== 2) throw new Error(`shown, ${await visibleThinking(page)} thinking rows show`);
    if (inGroup[0] !== "ThinkingStart by finding where the list is drawn.") throw new Error(`thinking row reads ${JSON.stringify(inGroup[0])}`);
  };
  await shoot("task-activity", { width: 1440, height: 1080 }, "dark", null, activity);
  await shoot("phone-activity", { width: 390, height: 844 }, "light", null, activity);
  await shoot("task-dark", { width: 1440, height: 1080 }, "dark", null, taskHash);
  await shoot("home-dark", { width: 1440, height: 900 }, "dark");
  await shoot("phone", { width: 390, height: 844 }, "light", null, taskHash);
  await shoot("settings", { width: 1280, height: 800 }, "light", "#/settings");
  await shoot("connections", { width: 1280, height: 800 }, "light", "#/connections");
  await browser.close();
  demo.kill("SIGTERM");
  console.log(errors.length ? errors.join("\n") : "no console errors");
})().catch((e) => { console.error(e); process.exit(1); });
