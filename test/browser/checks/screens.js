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
  const shoot = async (name, viewport, scheme, path, fn) => {
    const ctx = await browser.newContext({ viewport, colorScheme: scheme, deviceScaleFactor: 1 });
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
  await shoot("task", { width: 1440, height: 1080 }, "light", null, taskHash);
  await shoot("task-dark", { width: 1440, height: 1080 }, "dark", null, taskHash);
  await shoot("home-dark", { width: 1440, height: 900 }, "dark");
  await shoot("phone", { width: 390, height: 844 }, "light", null, taskHash);
  await shoot("settings", { width: 1280, height: 800 }, "light", "#/settings");
  await shoot("connections", { width: 1280, height: 800 }, "light", "#/connections");
  await browser.close();
  demo.kill("SIGTERM");
  console.log(errors.length ? errors.join("\n") : "no console errors");
})().catch((e) => { console.error(e); process.exit(1); });
