// The composer starts a task in the folder picked last: a pick on Home, or
// from Workspaces' "New task here", is what a fresh tab and a reload of
// this browser start in, not the first folder on the list again. And it
// survives an app restart, whose window gets a console on a new port — an
// origin this browser has never seen — so the pick that carries over there
// is the daemon's, not any browser's.
const assert = require("assert");
const { out, start, line, tempDir, launch, watch, requests, check } = require("../lib");

check(async () => {
  // One root for both runs, as an app restart reuses its setup.
  const root = tempDir("nodwyn-demo-");
  const demo = start(process.env.DEMO, ["-seed", "-pace=0", "-dir", root]);
  const step = (m) => console.log("step:", m);
  const [url] = await line(demo, /URL (\S+)/);
  const [consoleRepo] = await line(demo, /REPO (\S+)/);
  const receipts = consoleRepo.replace(/\/console$/, "/receipts");
  const browser = await launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = watch(await ctx.newPage());
  const pressed = () => page.$eval("#work-in button.chip[aria-pressed=true]", (b) => b.title);
  const home = async () => {
    await page.waitForSelector("html[data-ready]", { timeout: 15000 });
    await page.waitForSelector("#work-in button.chip[aria-pressed=true]");
  };
  const pick = async (folder) => {
    await page.click(`#work-in button.chip[title="${folder}"]`);
    await page.waitForFunction((f) => document.querySelector("#work-in button.chip[aria-pressed=true]").title === f, folder);
  };

  await page.goto(url);
  await home();
  // The seed's last active folder comes first, as before any pick.
  assert.strictEqual(await pressed(), consoleRepo, "the composer did not start in the first folder offered");
  step("starts in the first folder offered");

  // A pick on Home replaces the remembered one with the newer pick.
  await pick(consoleRepo);
  await page.reload();
  await home();
  assert.strictEqual(await pressed(), consoleRepo, "the folder picked last on Home did not survive a reload");
  step("a pick on Home sticks across a reload");

  // "New task here" on Workspaces picks the other folder, and a reload of
  // this browser starts there, not in the first folder again.
  await page.evaluate(() => (location.hash = "#/workspaces"));
  await page.click(`.ws-row:has(span.mono[title="${receipts}"]) button:text-is("New task here")`);
  await page.waitForSelector("#work-in button.chip[aria-pressed=true]");
  assert.strictEqual(await pressed(), receipts, "New task here did not pick its folder");
  await page.reload();
  await home();
  assert.strictEqual(await pressed(), receipts, "the folder New task here picked did not survive a reload");
  step("New task here sticks across a reload");

  // An app restart gives the window a console on a new port: an origin
  // this browser has never seen, whose localStorage holds nothing. A
  // second demo on the same root is such a restart — what a fresh tab
  // there starts in is the daemon's memory of the pick, not any browser's.
  await page.close();
  demo.kill("SIGTERM");
  await demo.exited;
  const again = start(process.env.DEMO, ["-pace=0", "-dir", root]);
  const [restarted] = await line(again, /URL (\S+)/);
  const fresh = watch(await ctx.newPage());
  const sent = requests(fresh);
  await fresh.goto(restarted);
  await fresh.waitForSelector("html[data-ready]", { timeout: 15000 });
  await fresh.waitForSelector("#work-in button.chip[aria-pressed=true]");
  assert.ok(sent.of("workspace.active").length, "the fresh window never asked the daemon for the folder picked last");
  assert.strictEqual(
    await fresh.$eval("#work-in button.chip[aria-pressed=true]", (b) => b.title),
    receipts,
    "the folder picked before the restart was not what a fresh window starts in",
  );
  step("the folder picked last survives an app restart");
  await fresh.screenshot({ path: `${out}/lastfolder.png` });
});
