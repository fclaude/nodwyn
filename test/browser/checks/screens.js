// Each view of the console at desktop and phone widths, light and dark.
// The screenshots are for people to look at, and compared with nothing:
// what each shot asserts is what its view shows, its background, and what
// the actions in it did.
const assert = require("assert");
const { out, start, line, launch, watch, shows, until, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0"]);
  const [url] = await line(demo, /URL (\S+)/);
  const base = url.split("/#")[0];
  const browser = await launch();
  // The browser always reports a light system: dark is the console's default,
  // and light only a stored choice, so the dark shots check the default too.
  const shoot = async (name, viewport, theme, look) => {
    const ctx = await browser.newContext({ viewport, colorScheme: "light", deviceScaleFactor: 1 });
    if (theme === "light") await ctx.addInitScript(() => localStorage.setItem("nodwyn-theme", "light"));
    const page = watch(await ctx.newPage(), name);
    // Each page pairs with a ticket of its own, one per line the demo reads.
    const from = demo.stdoutText.length;
    demo.stdin.write("\n");
    const [ticket] = await line(demo, /TICKET (\S+)/, "stdout", from);
    await page.goto(`${base}/#ticket=${ticket}`);
    await page.waitForSelector("html[data-ready]", { timeout: 10000 });
    await look(page);
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const want = theme === "light" ? "rgb(245, 242, 234)" : "rgb(23, 22, 19)";
    assert.strictEqual(bg, want, `${name}: the background (${theme})`);
    if (viewport.width < 600) {
      const sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      assert.ok(sideways <= 0, `${name}: the page scrolls ${sideways} px sideways at phone width`);
    }
    await page.screenshot({ path: `${out}/${name}.png`, fullPage: false });
    await ctx.close();
  };
  const go = (page, hash) => page.evaluate((h) => (location.hash = h), hash);

  // Home: the task waiting for an answer and the finished one, under its
  // folder, in the sidebar and on the page, with the model answering.
  const home = async (page) => {
    await shows(page, "#sidebar", "NEEDS YOU", "Run ls -la · console", "receipts", "Summarize what is in this folder", "ready");
    await shows(page, "#view", "What should we get done?", "Run ls -la", "Allow once", "RECENTLY FINISHED", "Summarize what is in this folder");
    const picker = await page.$eval("#new-task-model .mp-controls", (el) => {
      const box = el.getBoundingClientRect();
      const fields = [...el.querySelectorAll(".mp-field")];
      const selects = fields.map((field) => field.querySelector("select").getBoundingClientRect());
      return {
        height: box.height,
        divider: getComputedStyle(fields[1]).borderLeftWidth,
        contained: selects.every((r) => r.left >= box.left && r.right <= box.right && r.top >= box.top && r.bottom <= box.bottom),
      };
    });
    assert.ok(picker.height <= 46, `the Home picker is ${picker.height}px tall, not a slim paired control`);
    assert.strictEqual(picker.divider, "1px", "a divider separates model and effort");
    assert.ok(picker.contained, "the dropdowns extend outside their paired control");
    await page.focus("#new-task-model-model");
    const focus = await page.$eval("#new-task-model-model", (el) => ({ visible: el.matches(":focus-visible"), outline: getComputedStyle(el).outlineStyle }));
    assert.ok(focus.visible && focus.outline !== "none", "the model has no visible keyboard focus");
    await page.keyboard.press("Tab");
    // The synthetic world's model takes no effort, so Tab skips that disabled control.
    assert.ok(await page.$eval("#new-task-model-effort", (el) => el.disabled), "the model with no efforts should leave effort disabled");
    assert.ok(await page.$eval("button[aria-label='Start the task']", (el) => el.disabled), "an empty task should not be sent");
  };
  await shoot("home", { width: 1440, height: 900 }, "light", home);

  // The task that waits: its approval, and its plan beside it.
  const task = async (page) => {
    const href = await page.getAttribute(".nav-group a.nav-task.dotted", "href");
    await go(page, href);
    await page.waitForSelector(".task[data-loaded]", { timeout: 10000 });
    await shows(page, ".ask-card", "Run ls -la", "Allow once", "Deny");
    await shows(page, ".task aside", "PLAN", "2 of 4 done");
  };
  // Thinking is hidden unless this browser chose to show it.
  const visibleThinking = (page) => page.$$eval(".thinking", (ns) => ns.filter((n) => n.getClientRects().length > 0).length);
  await shoot("task", { width: 1440, height: 1080 }, "light", async (page) => {
    await task(page);
    const thoughts = await page.$$eval(".thinking", (ns) => ns.length);
    assert.ok(thoughts === 2 && (await visibleThinking(page)) === 0, `by default ${await visibleThinking(page)} of ${thoughts} thinking rows show`);
  });
  // Settings › This browser shows it and hides it again, and remembers.
  const thinking = () => document.documentElement.dataset.thinking + "," + localStorage.getItem("nodwyn-thinking");
  const showThinking = async (page) => {
    await go(page, "#/settings");
    await page.click('button.chip:has-text("Shown")');
    assert.strictEqual(await page.evaluate(thinking), "shown,shown", "after Shown");
  };
  await shoot("settings-thinking", { width: 1280, height: 800 }, "light", async (page) => {
    await showThinking(page);
    await page.click('button.chip:has-text("Hidden")');
    assert.strictEqual(await page.evaluate(thinking), "undefined,null", "after Hidden");
    await page.click('button.chip:has-text("Shown")');
    assert.strictEqual(await page.evaluate(thinking), "shown,shown", "after Shown again");
    await page.locator('button.chip:has-text("Shown")').scrollIntoViewIfNeeded();
  });
  // A reply that only thinks and reads goes with its reads: its thinking is
  // a row in their group, never a line of its own under it. The turn also
  // holds an Edited group of its own; the reads group is the one named
  // Looked around.
  const reads = ".turn.agent .activity:not(.commands):not(.writes)";
  const activity = async (page) => {
    await showThinking(page);
    await task(page);
    await page.click(`${reads} > summary`);
    await page.waitForSelector(`${reads}[open]`);
    await page.locator(reads).scrollIntoViewIfNeeded();
    const inGroup = await page.$$eval(`${reads} .activity-body > .thinking`, (ns) => ns.map((n) => n.querySelector("summary").textContent));
    const alone = await page.$$eval(".turn.agent > .thinking", (ns) => ns.length);
    assert.ok(inGroup.length === 2 && alone === 0, `thinking in the group: ${JSON.stringify(inGroup)}; on its own: ${alone}`);
    assert.strictEqual(await visibleThinking(page), 2, "thinking rows shown");
    assert.strictEqual(inGroup[0], "ThinkingStart by finding where the list is drawn.");
  };
  await shoot("task-activity", { width: 1440, height: 1080 }, "dark", activity);
  await shoot("phone-activity", { width: 390, height: 844 }, "light", activity);
  await shoot("task-dark", { width: 1440, height: 1080 }, "dark", task);
  await shoot("home-dark", { width: 1440, height: 900 }, "dark", home);
  await shoot("home-phone", { width: 390, height: 844 }, "light", home);
  await shoot("home-phone-dark", { width: 390, height: 844 }, "dark", home);
  await shoot("phone", { width: 390, height: 844 }, "light", task);
  await shoot("settings", { width: 1280, height: 800 }, "light", async (page) => {
    await go(page, "#/settings");
    await shows(page, "#view", "Settings", "Approvals", "Writing files", "Running commands");
    await until("settings loaded", () => page.evaluate(() => !document.querySelector("#view").innerText.includes("Loading…")));
  });
  await shoot("connections", { width: 1280, height: 800 }, "light", async (page) => {
    await go(page, "#/connections");
    await page.waitForSelector("text=No MCP servers yet");
    await shows(page, "#view", "Connections", "BUILT IN", "16 tools", "Add a server");
  });
});
