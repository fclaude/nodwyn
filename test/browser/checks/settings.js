// Settings: one rule for saving (a choice waits in the bar and is saved with
// the others), five tabs, the old addresses of the tabs that were folded into
// General, what most people never change folded away, the judge's one way to
// be off, and the notification choice a browser keeps. The device list, which
// only a console with listening devices has, is checked by `remote`.
const assert = require("assert");
const { out, start, line, launch, watch, requests, shows, until, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0", "-docs"]);
  const [url] = await line(demo, /URL (\S+)/);
  const browser = await launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  // A headless browser keeps no permission to show notifications and reads
  // "denied" whatever it was told, so the world gets one that does what a
  // person's browser does: the permission is asked for once, kept across
  // reloads, and can be taken back. What is checked is what nodwyn remembers.
  await context.addInitScript(() => {
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => localStorage.getItem("test-permission") || "default" });
    Notification.requestPermission = async () => (localStorage.setItem("test-permission", "granted"), "granted");
  });
  const page = watch(await context.newPage());
  const sent = requests(page);
  const step = (m) => console.log("step:", m);
  const go = (hash) => page.evaluate((h) => (location.hash = h), hash);
  const barText = () => page.$eval(".save-bar", (b) => (b.hidden ? "" : b.textContent));
  await page.goto(url);
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });

  // ---------- the tabs, and the rule said once ----------
  await go("#/settings");
  await page.waitForSelector("#section-approval");
  assert.deepStrictEqual(await page.$$eval("[id^='settings-tab-']", (tabs) => tabs.map((t) => t.textContent)), ["General", "Models", "Judge", "Keys & devices", "This browser"]);
  await shows(page, "#save-rule", "waits in the bar at the foot of the page and is saved with the others", "Keys and the passphrase act at once");
  // What a task does without asking leads the page; the transport exception
  // is a key matter and sits on Keys & devices.
  const labels = await page.$$eval("#section-approval .label", (ls) => ls.map((l) => l.textContent));
  assert.deepStrictEqual(labels.slice(0, 3), ["Writing files", "Running commands", "Don't ask in tasks"], "the approvals' order");
  assert.strictEqual(await page.locator("[id='set-policy.allow_insecure_http-default']").count(), 0, "the transport exception is among the approvals");
  await go("#/settings/keys");
  await page.waitForSelector("[id='set-policy.allow_insecure_http-default']");
  await shows(page, "#section-keys", "Credentials over remote HTTP");
  step("five tabs; approvals lead General; the transport exception is with the keys");

  // ---------- the old tabs' addresses open General's fold ----------
  await go("#/settings/tools");
  await page.waitForSelector("#advanced[open] #section-containers");
  assert.strictEqual(await page.evaluate(() => location.hash), "#/settings/general");
  await go("#/settings/context");
  await page.waitForSelector("#advanced[open] #section-window");
  step("the folded tabs' addresses still open their sections");

  // ---------- one save rule: Docs uploads wait in the bar ----------
  await go("#/settings/browser");
  await go("#/settings/general");
  await page.waitForSelector("[id='set-docs.google_upload-allow']");
  await page.click("[id='set-docs.google_upload-allow']");
  assert.strictEqual(sent.of("settings.docs.write").length, 0, "the upload answer was written before the bar saved it");
  assert.match(await barText(), /1 unsaved change/);
  await page.click(".save-bar button:text-is('Discard')");
  assert.strictEqual(await page.getAttribute("[id='set-docs.google_upload-allow']", "aria-pressed"), "false", "Discard kept the choice");
  // With a setting from the same page: one bar, both saved by one click.
  await page.click("[id='set-docs.google_upload-allow']");
  await page.click("[id='set-approval.writes-ask']");
  assert.match(await barText(), /2 unsaved changes/);
  await page.screenshot({ path: `${out}/settings-bar.png` });
  await page.click(".save-bar button:text-is('Save')");
  await until("both saved", async () => sent.of("settings.docs.write").length === 1 && sent.of("settings.write").length === 1 && (await barText()) === "");
  assert.deepStrictEqual(sent.of("settings.docs.write"), [{ google_upload: "allow" }]);
  assert.deepStrictEqual(sent.of("settings.write"), [{ set: { "approval.writes": "ask" } }]);
  await page.reload();
  await page.waitForSelector("[id='set-docs.google_upload-allow'][aria-pressed=true]");
  await page.waitForSelector("[id='set-approval.writes-ask'][aria-pressed=true]");
  step("the Docs upload answer waits in the bar with a setting, and one Save writes both");

  // ---------- what most people never change is folded away ----------
  assert.strictEqual(await page.$eval("#advanced", (d) => d.open), false, "Advanced starts open");
  await page.click("#advanced > summary");
  await page.fill("[id='set-window.high_water']", "50000");
  assert.match(await barText(), /1 unsaved change/);
  // The fold stays open for a change that waits in it.
  await go("#/settings/judge");
  await go("#/settings/general");
  await page.waitForSelector("#advanced[open]");
  await page.click(".save-bar button:text-is('Discard')");
  step("limits, the context window and the container runtime are one closed fold");

  // ---------- the judge: one way to be off, and its tuning folded ----------
  await go("#/settings/judge");
  await page.waitForSelector("#section-judge");
  assert.strictEqual(await page.$eval("#judge-tuning", (d) => d.open), false, "Tuning starts open");
  await page.click("#judge-tuning > summary");
  assert.deepStrictEqual(await page.$$eval("#section-judge [aria-label='What the judge sees'] button", (b) => b.map((x) => x.textContent)), ["The task and plan", "Also what calls would send"], "the judge's content is offered other levels than these");
  const judgeText = await page.innerText("#section-judge");
  assert.ok(!judgeText.includes("Reserved") && !judgeText.includes("Nothing (off)") && !judgeText.includes("probability"), "the judge's page still has the reserved level, a second off switch or the internal word");
  await page.screenshot({ path: `${out}/settings-judge.png`, fullPage: true });
  step("the judge offers two levels and no second off switch; its tuning is a fold");

  // ---------- notifications are remembered by the browser ----------
  await go("#/settings/browser");
  await page.waitForSelector("#section-browser");
  await shows(page, "#section-browser", "Notifications");
  await page.click("#section-browser button:text-is('Turn on')");
  await page.waitForSelector("#section-browser button:text-is('Turn off')");
  assert.strictEqual(await page.evaluate(() => localStorage.getItem("nodwyn-notify")), "on");
  await page.reload();
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });
  await go("#/settings/browser");
  await page.waitForSelector("#section-browser button:text-is('Turn off')");
  await page.click("#section-browser button:text-is('Turn off')");
  await page.waitForSelector("#section-browser button:text-is('Turn on')");
  assert.strictEqual(await page.evaluate(() => localStorage.getItem("nodwyn-notify")), "off");
  await page.reload();
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });
  await go("#/settings/browser");
  await page.waitForSelector("#section-browser button:text-is('Turn on')");
  // A choice of on counts only while the browser still allows them, and is
  // never asked for again without a click.
  await page.click("#section-browser button:text-is('Turn on')");
  await page.waitForSelector("#section-browser button:text-is('Turn off')");
  await page.evaluate(() => localStorage.setItem("test-permission", "denied"));
  await page.reload();
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });
  await go("#/settings/browser");
  await page.waitForSelector("#section-browser button:text-is('Turn on')");
  assert.strictEqual(await page.evaluate(() => localStorage.getItem("test-permission")), "denied", "a permission the person took back was asked for again");
  step("the notification choice survives a reload, on and off, and follows the browser's permission");
});
