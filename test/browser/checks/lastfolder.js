// The composer starts a task in the folder picked last: a pick on Home, or
// from Workspaces' "New task here", is what a fresh tab and a reload of
// this browser start in, not the first folder on the list again.
const assert = require("assert");
const { out, start, line, launch, watch, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0"]);
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

  // A pick on Home replaces the remembered one with the newer pick.
  await pick(consoleRepo);
  await page.reload();
  await home();
  assert.strictEqual(await pressed(), consoleRepo, "the folder picked last on Home did not survive a reload");
  step("a pick on Home sticks across a reload");
  await page.screenshot({ path: `${out}/lastfolder.png` });
});
