// Each view of the console at desktop and phone widths, light and dark.
// The screenshots are for people to look at, and compared with nothing:
// what each shot asserts is what its view shows, its background, and what
// the actions in it did.
const assert = require("assert");
const { out, start, line, launch, watch, expectError, requests, shows, until, ready, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0"]);
  const [url] = await line(demo, /URL (\S+)/);
  const base = url.split("/#")[0];
  const browser = await launch();
  // The browser always reports a light system: dark is the console's default,
  // and light only a stored choice, so the dark shots check the default too.
  const shoot = async (name, viewport, theme, look, scheme = "light", prepare = async () => {}) => {
    const ctx = await browser.newContext({ viewport, colorScheme: scheme, deviceScaleFactor: 1 });
    if (theme !== "dark") await ctx.addInitScript((mode) => localStorage.setItem("nodwyn-theme", mode), theme);
    const page = watch(await ctx.newPage(), name);
    await prepare(page);
    // Each page pairs with a ticket of its own, one per line the demo reads.
    const from = demo.stdoutText.length;
    demo.stdin.write("\n");
    const [ticket] = await line(demo, /TICKET (\S+)/, "stdout", from);
    await page.goto(`${base}/#ticket=${ticket}`);
    await ready(page);
    await look(page);
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    // "Match the system" takes the side of the system's scheme.
    const want = (theme === "system" ? scheme : theme) === "light" ? "rgb(245, 242, 234)" : "rgb(23, 22, 19)";
    assert.strictEqual(bg, want, `${name}: the background (${theme}, ${scheme} system)`);
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
      // The judge's field is hidden where there is no judge to choose.
      const fields = [...el.querySelectorAll(".mp-field")].filter((field) => !field.hidden);
      const selects = fields.map((field) => field.querySelector("select").getBoundingClientRect());
      return {
        height: box.height,
        fields: fields.length,
        divider: fields[1] ? getComputedStyle(fields[1]).borderLeftWidth : "",
        contained: selects.every((r) => r.left >= box.left && r.right <= box.right && r.top >= box.top && r.bottom <= box.bottom),
      };
    });
    assert.ok(picker.height <= 46, `the Home picker is ${picker.height}px tall, not a slim paired control`);
    // A model that takes no effort has nothing to choose, so the picker is
    // the model alone; two fields carry a divider.
    assert.strictEqual(picker.fields, 1, "the effort field is offered for a model that takes none");
    assert.ok(await page.$eval("#new-task-model-effort", (el) => el.closest(".mp-field").hidden), "the effort field is not hidden");
    // The composer's bar holds the files, the options, the model and Send:
    // the rare choices wait behind Options, which opens on its own.
    const bar = await page.$eval(".ask-bar", (el) => [...el.children].filter((c) => c.offsetParent !== null).length);
    assert.strictEqual(bar, 4, "the composer's bar holds more than the files, the options, the model and Send");
    assert.ok(!(await page.$eval("#new-task-options", (el) => el.open)), "Options starts open");
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
    await go(page, "#/settings/browser");
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
    const fold = page.locator(".activity-fold").filter({ has: page.locator(".activity:not(.commands):not(.writes)") });
    assert.strictEqual(await fold.getAttribute("open"), null, "routine activity starts folded");
    await fold.locator(":scope > summary").click();
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
  // The composer's bar holds what every task needs; the rare choices are
  // behind Options, which counts those off their defaults, and scheduling
  // opens its own dialog that asks for the message, so it does nothing
  // silently when the box is empty.
  await shoot("home-options", { width: 1280, height: 800 }, "light", async (page) => {
    await page.click("#new-task-options > summary");
    assert.ok(await page.isVisible("#new-task-limits"), "Options did not open");
    await shows(page, "#new-task-options", "In a worktree", "Limits", "Stop each turn at Settings' limits", "Schedule…");
    assert.ok(await page.$eval("#new-task-options .count", (el) => el.hidden), "the count shows with everything at its default");
    await page.uncheck("#new-task-limits");
    assert.strictEqual(await page.textContent("#new-task-options .count"), "1", "the count does not say Limits is off");
    await page.check("#new-task-limits");
    assert.ok(await page.$eval("#new-task-options .count", (el) => el.hidden), "the count stayed after Limits was ticked again");
    await page.screenshot({ path: `${out}/home-options-open.png` });
    assert.ok(await page.isEnabled("#new-task-schedule"), "Schedule is off with an empty box");
    await page.click("#new-task-schedule");
    await page.waitForSelector("dialog.schedule-dialog[open]");
    assert.strictEqual(await page.inputValue("#schedule-message"), "", "the dialog did not start from the empty box");
    await shows(page, "dialog.schedule-dialog", "Schedule");
    await page.screenshot({ path: `${out}/home-schedule-dialog.png` });
    await page.click('button[aria-label="Close schedule"]');
  });
  await shoot("home-options-phone", { width: 390, height: 844 }, "light", async (page) => {
    await page.click("#new-task-options > summary");
    const pop = await page.$eval(".options-pop", (el) => { const r = el.getBoundingClientRect(); return [r.left, r.right, innerWidth]; });
    assert.ok(pop[0] >= 0 && pop[1] <= pop[2], `the options spill off the phone: ${pop}`);
  });
  // Enter starts the task and Shift+Enter breaks the line, as in a task's
  // reply box, and the box says so.
  let enter;
  await shoot("home-enter", { width: 1280, height: 800 }, "light", async (page) => {
    await shows(page, ".ask", "Enter starts the task", "Shift+Enter for a new line");
    await page.fill("#task", "line one");
    await page.press("#task", "Shift+Enter");
    assert.strictEqual(await page.inputValue("#task"), "line one\n", "Shift+Enter did not break the line");
    assert.strictEqual(enter.of("session.open").length, 0, "Shift+Enter started the task");
    await page.keyboard.type("line two");
    await page.press("#task", "Enter");
    await page.waitForFunction(() => location.hash.startsWith("#/task/"));
    await until("the message sent", async () => enter.of("session.send").some((p) => p.text === "line one\nline two"));
  }, "light", async (page) => { enter = requests(page); });
  // On a phone only what waits for an answer comes before the composer.
  await shoot("home-phone-order", { width: 390, height: 844 }, "light", async (page) => {
    const top = (sel) => page.$eval(sel, (el) => el.getBoundingClientRect().top);
    const needs = await top(".home-needs");
    const composer = await top("#task");
    const finished = await top("#done");
    assert.ok(needs < composer && composer < finished, `the phone's order is not Needs you, composer, the rest: ${needs} ${composer} ${finished}`);
    await page.screenshot({ path: `${out}/home-phone-order.png` });
  });
  // The topbar's "1 needs you" is how a phone reaches the waiting task: the
  // sidebar is a drawer there.
  await shoot("phone-needs-you", { width: 390, height: 844 }, "light", async (page) => {
    await page.waitForSelector("#topbar-count:not([hidden])");
    await page.click("#topbar-count");
    await page.waitForSelector(".ask-card");
    assert.ok(/^#\/task\/./.test(await page.evaluate(() => location.hash)), "the pill did not open the waiting task");
  });
  // The light palette is written once, so "Match the system" follows the
  // system's scheme with the same colours the light choice has.
  await shoot("home-system-light", { width: 1440, height: 900 }, "system", home, "light");
  await shoot("home-system-dark", { width: 1440, height: 900 }, "system", home, "dark");
  await shoot("phone", { width: 390, height: 844 }, "light", task);
  const inspector = async (page) => {
    await task(page);
    await page.click(".task-aside [role=tab]:has-text('Changes')");
    await page.focus(".change-toggle");
    await page.keyboard.press("Enter");
    await shows(page, ".change-inspector", "Approved preview—not proof of a successful write");
    assert.ok(await page.locator(".change-inspector .diff-line.add").count(), "preview additions missing");
    await until("inspector bounded inside its task", () => page.evaluate(() => {
      const body = document.querySelector(".change-inspector .diff-body");
      const task = body?.closest(".task");
      return !!task && body.clientWidth <= task.clientWidth;
    }));
    // The side pane is narrow: a line that scrolls out of sight cannot be
    // reviewed, so the lines wrap and the body never scrolls sideways.
    const sideways = await page.$eval(".change-inspector .diff-body", (body) => body.scrollWidth - body.clientWidth);
    assert.ok(sideways <= 0, `a diff line runs ${sideways} px past its pane`);
    // The shot shows the diff itself, not the top of the page above it.
    await page.evaluate(() => document.querySelector(".change-inspector").scrollIntoView({ block: "center" }));
  };
  for (const theme of ["light", "dark"]) {
    await shoot(`changes-${theme}`, { width: 1440, height: 1080 }, theme, inspector);
    await shoot(`changes-phone-${theme}`, { width: 390, height: 844 }, theme, inspector);
  }
  // Real layout and controls, with bounded synthetic diff data: approvals
  // above keep their own renderer and their existing narrow-pane behaviour.
  const review = (name) => async (page) => {
    await task(page);
    await page.evaluate(async () => {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const long = "const veryLongLine = '" + "literal content ".repeat(80) + "';";
      const diff = "--- old\n+++ new\n@@ -1,80 +1,81 @@\n-old heading\n+new heading\n+" + long + "\n" + Array.from({ length: 79 }, (_, i) => ` line ${i + 2}`).join("\n") + "\n";
      const conn = {
        live: true,
        has(method) { return ["session.send", "worktree.diff", "worktree.review"].includes(method); },
        onEvent() { return () => {}; },
        onResult() { return () => {}; },
        async call(method) {
          if (method === "session.subscribe") return { page: { events: [{ seq: 1, kind: "user.msg", payload: { text: "Review the changes" } }], through: 1 }, busy: false };
          if (method === "worktree.diff") return { base: "1234567890", omitted: 0, changes: [
            { path: "src/a-long-file-name-that-should-remain-readable-in-either-layout.js", status: "M", diff },
            { path: "notes/another-file.md", status: "A", diff: "--- /dev/null\n+++ file\n@@ -0,0 +1 @@\n+Another file\n" },
          ] };
          return {};
        },
      };
      const desk = new Desk(conn);
      desk.sessions.set("review-layout", { id: "review-layout", title: "Review layout", repo: "/tmp/review-layout", open: true, worktree: { branch: "nodwyn/layout", base: "main" } });
      const view = new TaskView(conn, desk, () => {});
      document.querySelector("#view").replaceChildren(view.root);
      await view.open("review-layout");
      window.reviewFixture = view;
    });
    await page.fill("#reply", "Kept conversation draft");
    await page.click(".task-aside .review-open, .task-aside [role=tab]:has-text('Review')");
    await page.waitForFunction(() => document.querySelector(".review")?.textContent.includes("new heading"));
    assert.strictEqual(await page.locator(".task-main .inspector-view .review").count(), 1, "review is not in the centre inspector");
    const bounds = async () => {
      const got = await page.evaluate(() => {
        const body = document.querySelector(".review-file .diff-body");
        const pane = document.querySelector(".inspector-view");
        const close = document.querySelector(".review-close");
        const box = close.getBoundingClientRect();
        const old = document.querySelector(".diff-pair > .old")?.getBoundingClientRect();
        const current = document.querySelector(".diff-pair > .new")?.getBoundingClientRect();
        return {
          pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
          paneOverflow: pane.scrollWidth - pane.clientWidth,
          bodyWidth: body.clientWidth, paneWidth: pane.clientWidth,
          close: box.top >= 0 && box.bottom <= innerHeight && box.left >= 0 && box.right <= innerWidth,
          aligned: !old || (old.top === current.top && old.height === current.height && old.right <= current.left),
        };
      });
      assert.ok(got.pageOverflow <= 0 && got.paneOverflow <= 0 && got.bodyWidth <= got.paneWidth, `${name}: review escaped its pane: ${JSON.stringify(got)}`);
      assert.ok(got.close, `${name}: Close review is outside the viewport`);
      assert.ok(got.aligned, `${name}: old and new rows do not align`);
    };
    await page.locator(".review-bar").scrollIntoViewIfNeeded();
    await bounds();
    await shows(page, ".review", "veryLongLine", "Old", "New");
    await page.screenshot({ path: `${out}/${name}-unified.png` });
    await page.click("button:text-is('Side-by-side')");
    assert.ok(await page.locator(".diff-pair .old.del").count(), `${name}: deletions missing from the old column`);
    assert.ok(await page.locator(".diff-pair .new.add").count(), `${name}: additions missing from the new column`);
    await bounds();
    await page.screenshot({ path: `${out}/${name}-split.png` });
    const scrolled = await page.evaluate(() => {
      const body = document.querySelector(".review-file .diff-body");
      body.scrollTop = 200;
      body.scrollLeft = body.scrollWidth;
      const current = document.querySelector(".diff-pair > .new").getBoundingClientRect();
      const box = body.getBoundingClientRect();
      return { top: body.scrollTop, left: body.scrollLeft, newVisible: current.right <= box.right && current.left >= box.left };
    });
    assert.ok(scrolled.top > 0 && scrolled.newVisible, `${name}: cannot scroll the long diff and read its new side: ${JSON.stringify(scrolled)}`);
    await page.evaluate(() => window.reviewFixture.aside());
    const retained = await page.$eval(".review-file .diff-body", (body) => [body.scrollTop, body.scrollLeft]);
    assert.deepStrictEqual(retained, [scrolled.top, scrolled.left], `${name}: redraw lost the diff's reading position`);
    await page.evaluate(() => { document.querySelector(".inspector-view").scrollTop = 100; });
    await bounds();
    await page.click("button:text-is('Unified')");
    await page.click("button:text-is('Close review')");
    assert.ok(await page.locator(".transcript").isVisible(), `${name}: closing did not restore the conversation`);
    assert.ok(await page.locator(".reply").isVisible(), `${name}: closing did not restore reply`);
    assert.strictEqual(await page.inputValue("#reply"), "Kept conversation draft", `${name}: closing lost the reply draft`);
    await page.click(".task-aside .review-open");
    await page.locator(".review-bar").scrollIntoViewIfNeeded();
    await page.click("button:text-is('Side-by-side')");
    await page.evaluate(() => { const body = document.querySelector(".review-file .diff-body"); body.scrollTop = 0; body.scrollLeft = 0; });
    await bounds();
  };
  for (const theme of ["light", "dark"]) {
    await shoot(`review-${theme}`, { width: 1440, height: 1080 }, theme, review(`review-${theme}`));
    await shoot(`review-phone-${theme}`, { width: 390, height: 844 }, theme, review(`review-phone-${theme}`));
  }
  await shoot("settings", { width: 1280, height: 800 }, "light", async (page) => {
    // Settings opens on its first tab; the others are a click, and an
    // address, away.
    await go(page, "#/settings");
    await shows(page, "#view", "Settings", "General", "Models", "Judge", "Approvals", "Writing files", "Running commands");
    await until("settings loaded", () => page.evaluate(() => !document.querySelector("#view").innerText.includes("Loading…")));
    assert.strictEqual(await page.getAttribute("#settings-tab-general", "aria-selected"), "true");
    // What most people never change is folded away, and open on request: the
    // token budget and the reply cap are named for what they are.
    assert.strictEqual(await page.$eval("#advanced", (d) => d.open), false, "Advanced starts open");
    await page.click("#advanced > summary");
    await shows(page, "#view", "Tokens used", "a prompt token the provider read from its cache counted as a tenth", "Fallback reply cap", "and none its provider publishes");
    // Context was a tab of its own: its address still opens its section,
    // and the address bar says where it went.
    await go(page, "#/settings/context");
    await page.waitForSelector("#advanced[open] #section-window");
    assert.strictEqual(await page.evaluate(() => location.hash), "#/settings/general", "the old tab's address was not replaced");
    assert.strictEqual(await page.locator("[id^='settings-tab-']").count(), 5, "tabs that hold one section each remain");
  });
  for (const tab of ["models", "judge"]) {
    await shoot(`settings-${tab}`, { width: 1280, height: 800 }, "light", async (page) => {
      await go(page, `#/settings/${tab}`);
      await page.waitForSelector(`#section-${tab}`);
    });
  }
  // A read that fails is an error that stays, not an older daemon: the first
  // settings.read is refused, the page says so with a Retry, and Retry reads
  // it again, after which the page has its settings.
  let refuse = true;
  const readSettings = [];
  await shoot("settings-read-failed", { width: 1280, height: 800 }, "light", async (page) => {
    const error = page.locator(".toast.error");
    await error.waitFor();
    assert.ok((await error.textContent()).includes("Could not read settings.read: simulated read failure"), await error.textContent());
    await page.screenshot({ path: `${out}/settings-read-failed-error.png` });
    await page.evaluate(() => (location.hash = "#/settings"));
    await page.waitForSelector("#settings-tab-general");
    assert.ok(!(await page.textContent("#view")).includes("live in config.toml"), "a failed read was shown as an older daemon");
    refuse = false;
    await page.click(".toast.error button:text-is('Retry')");
    await until("the settings read again", async () => readSettings.length === 2);
    await until("the error gone", async () => (await error.count()) === 0);
    await until("the settings drawn", async () => (await page.textContent("#view")).includes("Tokens used"));
  }, "light", async (page) => {
    await page.routeWebSocket("**/socket", async (ws) => {
      const server = await ws.connectToServer();
      ws.onMessage((message) => {
        const frame = JSON.parse(message);
        if (frame.method === "settings.read") {
          readSettings.push(frame.id);
          if (refuse) return ws.send(JSON.stringify({ jsonrpc: "2.0", id: frame.id, error: { code: -32000, message: "simulated read failure" } }));
        }
        server.send(message);
      });
      server.onMessage((message) => ws.send(message));
    });
  });
  // A page that throws while it is drawn is replaced by its failure, with a
  // way out, instead of leaving the last page on screen under a sidebar
  // that already moved on.
  await shoot("fault", { width: 1280, height: 800 }, "light", async (page) => {
    await page.evaluate(async () => {
      const { Desk } = await import("/desk.js");
      window.needsYou = Desk.prototype.needsYou;
      Desk.prototype.needsYou = () => { throw new Error("needs exploded"); };
    });
    expectError(/needs exploded/);
    await go(page, "#/connections");
    await page.waitForSelector("#fault");
    await shows(page, "#view", "This page could not be drawn", "needs exploded", "Reload");
    await page.screenshot({ path: `${out}/fault-panel.png` });
    await page.evaluate(async () => (await import("/desk.js")).Desk.prototype.needsYou = window.needsYou);
    await page.click("#fault a:text-is('Home')");
    await page.waitForSelector("#task");
    await shows(page, "#view", "What should we get done?");
  });
  await shoot("connections", { width: 1280, height: 800 }, "light", async (page) => {
    await go(page, "#/connections");
    await page.waitForSelector("text=No MCP servers yet");
    // The built-in row names no count: which tools a task has is its own.
    await shows(page, "#view", "Connections", "BUILT IN", "Files, search, notes and commands", "Add a server");
    assert.ok(!(await page.textContent("#view")).includes("20 tools"), "a tool count from whichever task moved last");
  });
});
