// The console installs on a phone as an app (ADR 52): the page links its
// manifest, whose icons load at the sizes it names; its service worker
// registers for the whole console and nothing comes from a cache; a task that
// asks while the page is hidden shows the fixed notice through the worker,
// with the task's id as data it never shows, and answering closes it; the
// worker opening a task brings the console to it. On an iPhone's Safari,
// which has no notifications for a page, Settings says to add nodwyn to the
// Home Screen first.
const assert = require("assert");
const { out, until, step, ready, desk, check } = require("../lib");

check(async () => {
  const { url, ctx, page } = await desk({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  // A headless browser reads "denied" for notifications whatever it is told,
  // and shows none. The page gets a permission granted and a switch turned on,
  // and the worker's notices are kept where the check reads them, as a phone
  // would show them. Whether the page is hidden is the check's to say.
  await ctx.addInitScript(() => {
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => "granted" });
    localStorage.setItem("nodwyn-notify", "on");
    window.hiddenForCheck = false;
    Object.defineProperty(Document.prototype, "hidden", { configurable: true, get: () => window.hiddenForCheck });
    window.shownForCheck = [];
    ServiceWorkerRegistration.prototype.showNotification = async (title, options) => void window.shownForCheck.push({ title, ...options, closed: false });
    ServiceWorkerRegistration.prototype.getNotifications = async ({ tag } = {}) =>
      window.shownForCheck.filter((n) => !n.closed && (!tag || n.tag === tag)).map((n) => ({ close: () => (n.closed = true) }));
  });
  await page.goto(url);
  await ready(page);
  const origin = new URL(url).origin;

  // ---------- the manifest and its icons ----------
  const href = await page.$eval("link[rel=manifest]", (l) => l.href);
  const manifest = await (await page.request.get(href)).json();
  assert.deepStrictEqual([manifest.name, manifest.start_url, manifest.scope, manifest.display], ["nodwyn", "/", "/", "standalone"]);
  for (const icon of manifest.icons) {
    const [w, h] = await page.evaluate(async (src) => {
      const image = new Image();
      image.src = src;
      await image.decode();
      return [image.naturalWidth, image.naturalHeight];
    }, icon.src);
    if (icon.sizes !== "any") assert.strictEqual(`${w}x${h}`, icon.sizes, `${icon.src} loads at ${w}x${h}`);
  }
  assert.ok(manifest.icons.some((i) => i.purpose === "maskable"), "the manifest has no maskable icon");
  step("the page links a manifest whose icons load at the sizes it names");

  // ---------- the service worker ----------
  const registered = await page.evaluate(async () => {
    const r = await navigator.serviceWorker.ready;
    return { script: r.active.scriptURL, scope: r.scope };
  });
  assert.deepStrictEqual(registered, { script: `${origin}/sw.js`, scope: `${origin}/` });
  // It handles no fetch: after a reload every response still comes from the
  // bridge, and no cache holds anything.
  const fromWorker = [];
  page.on("response", (r) => r.fromServiceWorker() && fromWorker.push(r.url()));
  await page.reload();
  await ready(page);
  assert.deepStrictEqual(fromWorker, [], "responses came from the service worker");
  assert.deepStrictEqual(await page.evaluate(() => caches.keys()), [], "the console keeps a cache");
  assert.ok(await page.evaluate(() => navigator.serviceWorker.controller !== null), "the worker does not control the reloaded page");
  step("the service worker registers for the whole console and serves nothing from a cache");

  // ---------- a notice through the worker ----------
  await page.evaluate(() => (window.hiddenForCheck = true));
  await page.fill("#task", "The session list shows each session's first message. Make them easy to tell apart.");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".ask-card .btn.accent", { timeout: 20000 });
  const id = decodeURIComponent((await page.evaluate(() => location.hash)).replace(/^#\/task\//, ""));
  const [notice] = await until("a notice shown through the worker", () => page.evaluate(() => window.shownForCheck.length && window.shownForCheck));
  assert.deepStrictEqual(
    { title: notice.title, body: notice.body, tag: notice.tag, data: notice.data },
    { title: "nodwyn needs your answer", body: "Open nodwyn to review what it asks.", tag: `nodwyn-${id}`, data: { id } },
  );
  await page.screenshot({ path: `${out}/phone-asks.png` });
  step("a task that asks while the page is hidden shows the fixed notice through the worker");
  await page.evaluate(() => (window.hiddenForCheck = false));
  await page.click(".ask-card .btn.accent");
  await until("the notice closed once answered", () => page.evaluate(() => window.shownForCheck[0].closed));
  step("answering closes the notice");

  // ---------- the worker opens a task ----------
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  const [worker] = ctx.serviceWorkers();
  // A worker may bring a window forward only for a click on its notice, so
  // its focus is refused here; the page is asked to open the task first.
  await worker.evaluate((task) => openTask(task).catch(() => {}), id);
  await until("the console on the task the worker opened", () => page.evaluate((task) => location.hash === `#/task/${encodeURIComponent(task)}` || location.hash === `#/task/${task}`, id));
  assert.strictEqual(ctx.pages().length, 1, "the worker opened a second window beside the console");
  step("the worker opening a task brings the console to it");

  // ---------- an iPhone's Safari ----------
  // There a page has no Notification at all, and navigator.standalone is
  // false until nodwyn is added to the Home Screen.
  await ctx.addInitScript(() => {
    delete window.Notification;
    Object.defineProperty(Navigator.prototype, "standalone", { configurable: true, get: () => false });
  });
  await page.reload();
  await ready(page);
  await page.evaluate(() => (location.hash = "#/settings/browser"));
  await page.waitForSelector("#section-browser");
  const row = await page.innerText("#section-browser");
  assert.ok(row.includes("Notifications") && row.includes("add nodwyn to the Home Screen first"), `Settings › This browser says: ${row}`);
  assert.ok(await page.$eval("#section-browser button:text-is('Turn on')", (b) => b.disabled), "the switch is offered where nothing can be shown");
  await page.screenshot({ path: `${out}/iphone-safari-settings.png` });
  step("on an iPhone's Safari, Settings says to add nodwyn to the Home Screen first");
});
