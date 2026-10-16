// Create, persist, pause, edit, run and remove a schedule through the console.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { out, requests, openOptions, taskMenu, until, ready, desk, check } = require("../lib");
check(async () => {
  const { url, repo, page } = await desk({ timezoneId: "Pacific/Honolulu" });
  const sent = requests(page);
  const manage = () => taskMenu(page, "Schedule…");
  const close = () => page.click('button[aria-label="Close schedule"]');
  const status = async (text) => until(text, async () => (await page.locator('.schedule-status').textContent()).includes(text));
  await page.goto(url); await ready(page);
  await page.evaluate(() => location.hash = '#/settings');
  await page.waitForSelector('#section-approval');
  await page.click("[id='set-approval.writes-ask']");
  await page.click(".save-bar button:has-text('Save')");
  await until('write approval setting saved', async () => sent.of('settings.write').length === 1 && await page.$eval('.save-bar', (el) => el.hidden));
  await page.evaluate(() => location.hash = '#/');
  // Nothing is scheduled yet, and Home says so. Scheduling is one of Home's
  // Options, and opens the dialog with the composer empty: the message is a
  // field there, and required.
  await page.waitForSelector('#task');
  assert.ok((await page.locator('[aria-label="Scheduled tasks"]').textContent()).includes('Nothing scheduled.'), 'Home does not say that nothing is scheduled');
  assert.strictEqual(await page.inputValue('#task'), '');
  await openOptions(page);
  await page.click('#new-task-schedule', { timeout: 5000 });
  assert.strictEqual(await page.getAttribute('#schedule-message', 'required'), '', 'the schedule dialog does not require its message');
  assert.strictEqual(await page.inputValue('#schedule-message'), '');
  await page.click('button[aria-label="Close schedule"]');
  // What was typed in the composer is where the message starts.
  await page.fill('#task', 'Write scheduled');
  await openOptions(page);
  await page.click('#new-task-schedule', { timeout: 5000 });
  assert.strictEqual(await page.inputValue('#schedule-message'), 'Write scheduled', 'the composer text was not carried to the dialog');
  await page.selectOption('#schedule-repeat', 'weekly');
  await page.fill('#schedule-at', '2099-10-16T09:30');
  await page.fill('#schedule-cost', '1.25');
  await until("daemon next-time preview", () => sent.of('schedule.next').some((p) => p.at === '2099-10-16T09:30' && p.repeat === 'weekly'));
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.includes('2099-10-16 09:30'));
  await page.click('.schedule-form button[type=submit]');
  await page.waitForSelector('.task[data-loaded]');
  const id = await page.evaluate(() => location.hash.split('/')[2]);
  assert.strictEqual(sent.of('session.send').filter((p) => p.id === id).length, 0, 'creation sent the message immediately');
  const def = sent.of('schedule.set').find((p) => p.id === id);
  assert.strictEqual(def.message, 'Write scheduled'); assert.strictEqual(def.cost, 1.25);
  // Home lists it, with pause and run now on the card: pausing there sends the
  // same request as the dialog does, and resuming undoes it.
  const taskHash = await page.evaluate(() => location.hash);
  await page.evaluate(() => location.hash = '#/');
  await page.waitForSelector('[aria-label="Scheduled tasks"] article');
  assert.ok((await page.locator('[aria-label="Scheduled tasks"] article').textContent()).includes('Next 2099-10-16 09:30'), 'the card does not say when it runs next');
  assert.strictEqual(await page.locator('[aria-label="Scheduled tasks"] article button:text-is("Run now")').count(), 1, 'the card has no Run now');
  await page.click('[aria-label="Scheduled tasks"] article button:text-is("Pause schedule")');
  await until('card paused', async () => (await page.locator('[aria-label="Scheduled tasks"] article').textContent()).includes('Paused'));
  assert.deepStrictEqual(sent.of('schedule.pause').at(-1), { id, paused: true });
  await page.click('[aria-label="Scheduled tasks"] article button:text-is("Resume schedule")');
  await until('card resumed', async () => (await page.locator('[aria-label="Scheduled tasks"] article').textContent()).includes('Next 2099-10-16 09:30'));
  assert.deepStrictEqual(sent.of('schedule.pause').at(-1), { id, paused: false });
  await page.screenshot({ path: `${out}/schedule-home.png` });
  await page.evaluate((hash) => location.hash = hash, taskHash);
  await page.waitForSelector('.task[data-loaded]');
  await page.reload(); await ready(page); await page.waitForSelector('.task[data-loaded]');
  await manage(); await status('Scheduled');
  assert.strictEqual(await page.inputValue('#schedule-message'), 'Write scheduled', 'saved message did not reload');
  await page.click('button:text-is("Pause schedule")'); await status('Paused');
  assert.strictEqual(sent.of('schedule.pause').at(-1).paused, true);
  await page.fill('#schedule-message', 'Write scheduled-again');
  await page.selectOption('#schedule-repeat', 'daily');
  await page.click('.schedule-form button[type=submit]');
  await until('edited saved schedule', () => sent.of('schedule.set').some((p) => p.message === 'Write scheduled-again' && p.repeat === 'daily'));
  await status('Paused');
  await page.click('button:text-is("Resume schedule")'); await status('Scheduled');
  await close();
  await taskMenu(page, "Close");
  await page.waitForSelector('.task-actions button:text-is("Resume")');
  // Earlier priced work belongs to the task total, not to the next run.
  const logPath = path.join(repo, '../../data/sessions', `${id}.jsonl`);
  const records = fs.readFileSync(logPath, 'utf8').trim().split('\n').map(JSON.parse);
  fs.appendFileSync(logPath, JSON.stringify({ session:id, seq:records.at(-1).seq + 1, ts:new Date().toISOString(), kind:'llm.resp', payload:{ content:'Earlier work', usage:{ cost:5 } } }) + '\n');
  await manage(); await status('Scheduled');
  await page.click('button:text-is("Run now")');
  await until('run-now request', () => sent.of('schedule.run').some((p) => p.id === id));
  await close();
  // A scheduled write must still ask, and must not change a file before it
  // is allowed. This also checks live delivery for the run marker.
  await page.waitForSelector('.ask-card', { timeout: 15000 });
  const target = path.join(repo, 'notes/scheduled-again.md');
  assert.strictEqual(fs.existsSync(target), false, 'scheduled write bypassed approval');
  await page.screenshot({ path: `${out}/schedule-approval.png` });
  await page.click('.ask-card button:text-is("Allow once")');
  await until('scheduled file written', () => fs.existsSync(target));
  assert.ok(fs.readFileSync(target, 'utf8').includes('Write scheduled-again'));
  await page.waitForFunction(() => document.querySelector('.task-head .pill')?.textContent === 'Idle');
  assert.ok((await page.locator('.transcript').textContent()).includes('Run now'), 'run marker missing');
  await page.click('#aside-tab-statistics');
  await until('per-run statistics', async () => (await page.locator('[data-totals]').textContent()).includes('This run'));
  const figures = await page.locator('[data-totals]').textContent();
  assert.match(figures, /Total spend.*\$5\.00/);
  assert.match(figures, /This run\$0\.0000/, 'this run inherited the whole-task spend');
  await manage(); await status("Scheduled"); await page.waitForFunction(() => document.querySelector(".schedule-form [role=status]")?.textContent.includes("2099")); await page.screenshot({ path: `${out}/schedule-controls.png` });
  // Removing asks first, as every removal does: Cancel keeps it.
  await page.click('button:text-is("Remove schedule")');
  assert.ok((await page.locator('.schedule-dialog').textContent()).includes('Remove the schedule? The task and what it has done stay.'));
  assert.strictEqual(sent.of('schedule.set').filter((p) => p.remove).length, 0, 'Remove schedule removed it before it was confirmed');
  await page.click('.schedule-dialog button:text-is("Cancel")');
  await status('Scheduled');
  await page.click('button:text-is("Remove schedule")');
  await page.click('button:text-is("Remove schedule")'); await status('No schedule');
  assert.strictEqual(sent.of('schedule.set').filter((p) => p.remove).length, 1);
  await close();
  await page.reload(); await ready(page); await page.waitForSelector('.task[data-loaded]');
  await manage(); await status('No schedule');
  await close();
  await page.setViewportSize({ width: 390, height: 844 });
  await manage();
  assert.strictEqual(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'schedule controls overflow phone');
  await page.screenshot({ path: `${out}/schedule-phone.png` });
  console.log('schedule create, preview, persist, pause, edit, resume, run with approval and remove');
});
