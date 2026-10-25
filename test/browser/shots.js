// The console's screenshots for the README and the website, taken of the
// demo's synthetic world (a scripted model, a scratch folder, temporary
// directories), never of anyone's work. Not a check: nothing here asserts,
// and run.sh does not list it. scripts/screenshots.sh builds the demo, runs
// this and turns the PNGs it leaves in OUT-DIR into the WebP files under
// docs/images.
//
//   DEMO=path/to/demo node test/browser/shots.js OUT-DIR
const { out, desk, ready, shows, check, line } = require("./lib");

// Every shot is taken in both themes: the README and the site show the one
// the reader's system prefers.
const themes = ["light", "dark"];

check(async () => {
  for (const theme of themes) {
    const { demo, url, page, ctx, browser } = await desk({ colorScheme: theme, viewport: { width: 1440, height: 900 } });
    // Dark is the console's default; light is a stored choice.
    await ctx.addInitScript((mode) => localStorage.setItem("nodwyn-theme", mode), theme);
    const base = url.split("/#")[0];
    const open = async (target) => {
      await page.goto(target);
      await ready(page);
    };
    const shot = (name) => page.screenshot({ path: `${out}/${name}-${theme}.png` });
    const go = async (hash) => {
      await page.evaluate((h) => (location.hash = h), hash);
      await page.waitForTimeout(500);
    };

    await open(url);
    await shows(page, "#view", "What should we get done?");
    await shot("home");

    // The task that waits for an answer: the approval, and the plan beside it.
    const href = await page.getAttribute(".nav-group a.nav-task.dotted", "href");
    await go(href);
    await page.waitForSelector(".task[data-loaded]", { timeout: 10000 });
    await shows(page, ".ask-card", "Allow once");
    // Tall enough for the whole approval card above the reply box.
    await page.setViewportSize({ width: 1440, height: 1040 });
    await page.waitForTimeout(300);
    await shot("task");

    // The same task with its routine activity opened.
    const fold = page.locator(".activity-fold").first();
    if (await fold.count()) await fold.locator(":scope > summary").click();
    await page.waitForTimeout(300);
    await shot("task-activity");
    await page.setViewportSize({ width: 1440, height: 900 });

    await go("#/settings/general");
    await shot("settings-general");
    await go("#/settings/models");
    await shot("settings-models");
    await go("#/settings/judge");
    await shot("settings-judge");
    await go("#/settings/keys");
    await shot("settings-devices");
    await go("#/connections/credentials");
    await shot("connections");
    await go("#/workspaces");
    await shot("workspaces");
    await go("#/workflows");
    await shot("workflows");

    // Home with the composer's options open, and the schedule dialog.
    await go("#/");
    await page.click("#new-task-options > summary");
    await shot("home-options");
    await page.fill("#task", "Every weekday at nine, check the open pull requests");
    await page.click("#new-task-schedule");
    await page.waitForSelector("dialog.schedule-dialog[open]");
    await page.waitForTimeout(300);
    await shot("schedule");
    await ctx.close();

    // The phone: Home and the waiting task.
    const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: theme, deviceScaleFactor: 2 });
    await phone.addInitScript((mode) => localStorage.setItem("nodwyn-theme", mode), theme);
    const small = await phone.newPage();
    const from = demo.stdoutText.length;
    demo.stdin.write("\n");
    const [ticket] = await line(demo, /TICKET (\S+)/, "stdout", from);
    await small.goto(`${base}/#ticket=${ticket}`);
    await ready(small);
    await small.waitForTimeout(500);
    await small.screenshot({ path: `${out}/phone-home-${theme}.png` });
    await small.click("#topbar-count");
    await small.waitForSelector(".ask-card");
    await small.waitForTimeout(500);
    await small.screenshot({ path: `${out}/phone-task-${theme}.png` });
    await phone.close();
    await browser.close();
    demo.kill("SIGTERM");
  }
});
