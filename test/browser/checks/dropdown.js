// The list a select opens is drawn by the console (dropdown.js), in its
// colours, not by the system: in the Schedule dialog (which it must rise
// above) and in Home's model picker, by mouse and by keyboard, with the
// select's own value and change event as what the page sees.
const assert = require("assert");
const { out, step, until, openOptions, ready, desk, check } = require("../lib");

check(async () => {
  const { url, page } = await desk();
  await page.goto(url);
  await ready(page);
  await page.waitForSelector("#new-task-model-model");

  const pop = () => page.$$eval(".dd-pop .dd-option", (rows) => rows.map((r) => ({ text: r.textContent, selected: r.getAttribute("aria-selected"), active: r.classList.contains("active") })));
  const count = () => page.locator(".dd-pop").count();
  // The page's own surface colour, to tell the console's list from a white system one.
  const surface = await page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.background = "var(--surface)";
    document.body.append(probe);
    const colour = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return colour;
  });
  assert.notStrictEqual(surface, "rgb(255, 255, 255)", "the probe did not resolve the theme's surface");

  // Home's model picker: not in a dialog. A click opens the list with the
  // chosen model marked, and a click on a row changes the select.
  const first = await page.$eval("#new-task-model-model", (s) => [...s.options].map((o) => o.label.trim()));
  await page.click("#new-task-model-model");
  await page.waitForSelector(".dd-pop");
  const rows = await pop();
  assert.deepStrictEqual(rows.map((r) => r.text.trim()), first, "the list does not hold the select's options");
  assert.strictEqual(rows.filter((r) => r.selected === "true").length, 1, "one row is marked chosen");
  assert.ok(rows.find((r) => r.selected === "true").active, "the chosen row is not the active one");
  const style = await page.$eval(".dd-pop", (el) => ({ background: getComputedStyle(el).backgroundColor, position: getComputedStyle(el).position, within: el.getBoundingClientRect().bottom <= innerHeight }));
  assert.strictEqual(style.background, surface, "the list is not drawn in the console's surface colour");
  assert.strictEqual(style.position, "fixed");
  assert.ok(style.within, "the list runs past the window");
  await page.screenshot({ path: `${out}/home-open.png` });
  step("a click opens the console's own list");

  // A click outside closes it and changes nothing.
  const before = await page.inputValue("#new-task-model-model");
  await page.click("h1");
  assert.strictEqual(await count(), 0, "a click outside left the list open");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), before, "closing changed the value");
  step("a click outside closes it");

  // The Schedule dialog: the list is above the modal, and the select's
  // change event is what redraws the dialog.
  await openOptions(page);
  await page.click("#new-task-schedule");
  await page.waitForSelector("#schedule-repeat");
  await page.click("#schedule-repeat");
  await page.waitForSelector(".dd-pop");
  assert.deepStrictEqual((await pop()).map((r) => r.text.trim()), ["At set times", "Every few minutes or hours", "Monthly", "Once"]);
  const onTop = await page.$eval(".dd-pop", (el) => {
    const box = el.getBoundingClientRect();
    return el.contains(document.elementFromPoint(box.left + box.width / 2, box.top + 10));
  });
  assert.ok(onTop, "the list is under the dialog");
  await page.screenshot({ path: `${out}/dialog-open.png` });
  await page.click(".dd-pop .dd-option:has-text('Every few minutes')");
  assert.strictEqual(await count(), 0, "choosing left the list open");
  assert.strictEqual(await page.inputValue("#schedule-repeat"), "every", "the row did not choose");
  await page.waitForSelector("#schedule-every-unit");
  step("a row chosen in the dialog changes the select and its page redraws");

  // The keyboard: Space opens, arrows and a typed letter move, Enter chooses.
  await page.focus("#schedule-repeat");
  await page.keyboard.press("Space");
  await page.waitForSelector(".dd-pop");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  assert.strictEqual(await page.inputValue("#schedule-repeat"), "monthly", "ArrowDown and Enter did not choose the next row");
  await page.keyboard.press("Alt+ArrowDown");
  await page.waitForSelector(".dd-pop");
  await page.keyboard.type("o");
  await page.keyboard.press("Enter");
  assert.strictEqual(await page.inputValue("#schedule-repeat"), "once", "typing a letter did not move to that row");
  step("the keyboard opens, moves and chooses");

  // Escape closes the list alone: the dialog stays, the value stays.
  await page.keyboard.press("Space");
  await page.waitForSelector(".dd-pop");
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("Escape");
  assert.strictEqual(await count(), 0, "Escape left the list open");
  assert.ok(await page.$eval("dialog.schedule-dialog", (d) => d.open), "Escape closed the dialog with the list");
  assert.strictEqual(await page.inputValue("#schedule-repeat"), "once", "Escape chose a row");
  assert.strictEqual(await page.evaluate(() => document.activeElement.id), "schedule-repeat", "the focus left the select");
  step("Escape closes only the list");

  // A redraw that takes the select away takes the list with it.
  await page.keyboard.press("Space");
  await page.waitForSelector(".dd-pop");
  await page.evaluate(() => document.querySelector("#schedule-repeat").remove());
  await until("the list gone with its select", async () => (await count()) === 0);
  step("a list does not outlive its select");
});
