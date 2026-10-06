// Attaching in the New task window: the chip shows as soon as a file is
// picked, stays drawn and removable through the desk's redraws that keep
// the composer (the redraw a redraw of the lists below takes while the
// focus stays in the ask), a removed file does not go with the task, and a
// kept one does: the daemon asks the image's confirm on the task's page,
// and the model then counts the image it was sent.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, launch, watch, until, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-seed", "-pace=0"]);
  const [url] = await line(demo, /URL (\S+)/);
  const [repo] = await line(demo, /REPO (\S+)/);
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1440, height: 1000 } }));
  const step = (m) => console.log("step:", m);
  const shot = `${tempDir("nodwyn-shot-")}/shot.png`;
  fs.writeFileSync(shot, Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64"));
  const notes = `${tempDir("nodwyn-notes-")}/notes.txt`;
  fs.writeFileSync(notes, "carry this along\n");
  const chips = () => page.$$eval(".ask .attach-list .attach-chip .name", (els) => els.map((e) => e.textContent));

  await page.goto(url);
  await page.waitForSelector("html[data-ready]", { timeout: 15000 });
  // A route there and back is a full redraw, which puts the composer's
  // shared attach input back in the page after the desk's first redraws.
  await page.evaluate(() => { location.hash = "#/workspaces"; location.hash = "#/"; });
  await page.waitForSelector("#task");
  await page.fill("#task", "Describe this screenshot");

  // The chip shows as soon as the file is picked.
  await page.setInputFiles(".ask input[type=file]", shot);
  await until("the image chip in the New task window", async () => (await chips()).includes("shot.png"));
  step("image chip shows");

  // A redraw that keeps the composer must keep the attachment and the
  // attach input with it. Answering the waiting prompt redraws Home at
  // once, and the focus goes back to the ask before the desk redraws
  // again, as a person answering and going on typing has it; the resumed
  // task asks its next thing, which redraws Home once more. A mark on the
  // textarea tells a rebuilt composer (a full redraw, which these must not
  // be) from the kept one.
  await page.click("button:has-text('Allow once')");
  await page.focus("#task");
  await page.evaluate(() => (document.querySelector("#task").dataset.mark = "kept"));
  await until("the answered ask off Home", async () => !(await page.innerText(".home-section[aria-labelledby='needs']")).includes("ls -la"));
  await until("the task's next ask on Home", async () => (await page.innerText(".home-section[aria-labelledby='needs']")).includes("wc -l"), 20000);
  assert.strictEqual(await page.$eval("#task", (el) => el.dataset.mark), "kept", "the composer was rebuilt, not kept: the redraws did not take the path under test");
  assert.ok((await chips()).includes("shot.png"), "the image chip vanished from the New task window");
  assert.ok(await page.$(".ask input[type=file]"), "the attach input vanished with the redraw");
  step("chip and input survive the redraws that keep the composer");

  // A full redraw (a folder picked from the chips) keeps them too.
  await page.click(`button.chip[title="${repo}"]`);
  await until("the image chip after the folder redraw", async () => (await chips()).includes("shot.png"));
  assert.ok(await page.$(".ask input[type=file]"), "the attach input vanished with the redraw");
  step("chip survives a full redraw");

  // A second file shows, and its chip can be removed before the task starts.
  await page.setInputFiles(".ask input[type=file]", notes);
  await until("the text file's chip", async () => (await chips()).includes("notes.txt"));
  await page.click(`.ask button[aria-label="Remove notes.txt"]`);
  await until("the text file's chip gone", async () => !(await chips()).includes("notes.txt"));
  assert.ok((await chips()).includes("shot.png"), "removing one chip took the other");
  step("chip removable");

  // Starting the task carries the kept file: only an image the daemon has
  // asks whether it may go to the model.
  await page.press("#task", "Control+Enter");
  await page.waitForSelector(".task[data-loaded]", { timeout: 15000 });
  await page.waitForSelector(".ask-card h2:has-text('Send the image shot.png to the model?')", { timeout: 15000 });
  step("the kept image asks its confirm");

  // Allowed, the image reaches the model, which counts what it was sent;
  // the message that carried it is on the transcript, the removed file is
  // not.
  await page.click(".ask-card button[data-key='Y']");
  await until("the model counting the image", async () => (await page.innerText(".transcript")).includes("This request carries 1 image(s)."));
  const transcript = await page.innerText(".transcript");
  assert.ok(transcript.includes("Describe this screenshot"), "the typed message did not go with the task");
  assert.ok(!transcript.includes("notes.txt"), "the removed file went with the task anyway");
  await page.screenshot({ path: `${out}/attached.png` });
});
