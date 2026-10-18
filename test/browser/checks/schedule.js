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
  // Tuesdays and Thursdays at 15:00, from a day far enough ahead that no
  // run starts during the check. The daemon says it in words, and says when
  // it runs next as a label in its own zone.
  assert.strictEqual(await page.inputValue('#schedule-repeat'), 'times', 'a new schedule is not at set times');
  await page.fill('#schedule-from', '2099-10-16T00:00');
  await page.fill('.schedule-time', '15:00');
  await page.click('.schedule-days [data-day=tue]');
  await page.click('.schedule-days [data-day=thu]');
  await page.fill('#schedule-cost', '1.25');
  await until("daemon preview of the rule", () => sent.of('schedule.next').some((p) => JSON.stringify(p.rule) === JSON.stringify({ at: ['15:00'], days: ['tue', 'thu'], from: '2099-10-16T00:00' })));
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.includes('Next: Tue 20 Oct 2099 15:00, then Thu 22 Oct 2099 15:00'));
  const preview = await page.textContent('.schedule-form [role=status]');
  assert.ok(preview.startsWith('Tuesdays and Thursdays at 15:00, starting Fri 16 Oct 2099'), `the preview does not say the rule: ${preview}`);
  // What a schedule cannot be is said where it is chosen, before saving.
  await page.selectOption('#schedule-repeat', 'every');
  await page.fill('#schedule-every', '2');
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.includes('from 5 minutes to 24 hours'));
  await page.selectOption('#schedule-repeat', 'times');
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.startsWith('Tuesdays and Thursdays'));
  await page.click('.schedule-form button[type=submit]');
  await page.waitForSelector('.task[data-loaded]');
  const id = await page.evaluate(() => location.hash.split('/')[2]);
  assert.strictEqual(sent.of('session.send').filter((p) => p.id === id).length, 0, 'creation sent the message immediately');
  const def = sent.of('schedule.set').find((p) => p.id === id);
  assert.strictEqual(def.message, 'Write scheduled'); assert.strictEqual(def.cost, 1.25);
  assert.deepStrictEqual(def.rule, { at: ['15:00'], days: ['tue', 'thu'], from: '2099-10-16T00:00' });
  assert.ok((await page.locator('.transcript').textContent()).includes('Schedule: Tuesdays and Thursdays at 15:00'), 'the transcript does not show the schedule in the daemon\'s words');
  // Home lists it, with pause and run now on the card: pausing there sends the
  // same request as the dialog does, and resuming undoes it.
  const taskHash = await page.evaluate(() => location.hash);
  await page.evaluate(() => location.hash = '#/');
  await page.waitForSelector('[aria-label="Scheduled tasks"] article');
  const card = await page.locator('[aria-label="Scheduled tasks"] article').textContent();
  assert.ok(card.includes('Tuesdays and Thursdays at 15:00') && card.includes('Next: Tue 20 Oct 2099 15:00'), `the card does not say when it runs: ${card}`);
  assert.strictEqual(await page.locator('[aria-label="Scheduled tasks"] article button:text-is("Run now")').count(), 1, 'the card has no Run now');
  await page.click('[aria-label="Scheduled tasks"] article button:text-is("Pause schedule")');
  await until('card paused', async () => (await page.locator('[aria-label="Scheduled tasks"] article').textContent()).includes('Paused'));
  assert.deepStrictEqual(sent.of('schedule.pause').at(-1), { id, paused: true });
  await page.click('[aria-label="Scheduled tasks"] article button:text-is("Resume schedule")');
  await until('card resumed', async () => (await page.locator('[aria-label="Scheduled tasks"] article').textContent()).includes('Next: Tue 20 Oct 2099 15:00'));
  assert.deepStrictEqual(sent.of('schedule.pause').at(-1), { id, paused: false });
  await page.screenshot({ path: `${out}/schedule-home.png` });
  await page.evaluate((hash) => location.hash = hash, taskHash);
  await page.waitForSelector('.task[data-loaded]');
  await page.reload(); await ready(page); await page.waitForSelector('.task[data-loaded]');
  await manage(); await status('Scheduled');
  assert.strictEqual(await page.inputValue('#schedule-message'), 'Write scheduled', 'saved message did not reload');
  // The dialog holds what a person came to see: the status, the rule in
  // words, the message, when it runs next (every next time is a tooltip) and
  // what came of recent runs, with Close at its top right and the edit form
  // folded away until Edit is chosen.
  const dialog = await page.$eval('dialog.schedule-dialog', (el) => {
    const close = el.querySelector('[aria-label="Close schedule"]').getBoundingClientRect(), title = el.querySelector('h2').getBoundingClientRect(), box = el.getBoundingClientRect();
    return { text: el.innerText, closeAtRight: close.right > box.right - 40 && Math.abs(close.top - title.top) < 20, folded: !el.querySelector('.schedule-edit').open, next: el.querySelector('.hint[title]')?.title };
  });
  assert.ok(dialog.closeAtRight, `Close is not at the top right: ${JSON.stringify(dialog)}`);
  assert.ok(dialog.folded && !dialog.text.includes('Spend limit per run'), 'the edit form is not folded away');
  assert.ok(dialog.text.includes('Tuesdays and Thursdays at 15:00, starting Fri 16 Oct 2099') && dialog.text.includes('Next: Tue 20 Oct 2099 15:00') && dialog.next.split(' · ').length === 5, `the dialog does not say when: ${JSON.stringify(dialog)}`);
  const statusLine = await page.textContent('.schedule-status');
  assert.ok(/^Scheduled · [A-Za-z_]+(\/[A-Za-z_+-]+)*$/.test(statusLine) && !statusLine.includes('Local'), `the status line does not name the zone: ${statusLine}`);
  assert.ok(dialog.text.includes('No run yet.'), 'the dialog has no history');
  // The editor opens on the saved rule, and keeps it when nothing changes.
  await page.click('.schedule-edit > summary');
  assert.strictEqual(await page.inputValue('#schedule-repeat'), 'times');
  assert.deepStrictEqual(await page.$$eval('.schedule-days [aria-pressed=true]', (els) => els.map((el) => el.dataset.day)), ['tue', 'thu']);
  await page.click('button:text-is("Pause schedule")'); await status('Paused');
  assert.strictEqual(sent.of('schedule.pause').at(-1).paused, true);
  // Every 30 minutes in working hours on weekdays.
  await page.fill('#schedule-message', 'Write scheduled-again');
  await page.selectOption('#schedule-repeat', 'every');
  await page.fill('#schedule-every', '30');
  await page.check('#schedule-window');
  for (const day of ['mon', 'wed', 'fri']) await page.click(`.schedule-days [data-day=${day}]`);
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.startsWith('Every 30 minutes from 09:00 to 18:00 on weekdays'));
  assert.ok((await page.textContent('.schedule-form [role=status]')).includes('Next: Fri 16 Oct 2099 09:00, then Fri 16 Oct 2099 09:30'));
  await page.screenshot({ path: `${out}/schedule-editor.png` });
  // The first Monday of each month, until the end of the year.
  await page.selectOption('#schedule-repeat', 'monthly');
  await page.selectOption('#schedule-month', 'nth');
  await page.fill('.schedule-time', '10:00');
  await page.fill('#schedule-until', '2099-12-31');
  await page.waitForFunction(() => document.querySelector('.schedule-form [role=status]')?.textContent.startsWith('On the first Monday of each month at 10:00, starting Fri 16 Oct 2099, until Thu 31 Dec 2099'));
  assert.ok((await page.textContent('.schedule-form [role=status]')).includes('Next: Mon 2 Nov 2099 10:00, then Mon 7 Dec 2099 10:00'));
  // Back to the working-hours interval, which is what is saved.
  await page.selectOption('#schedule-repeat', 'every');
  await page.fill('#schedule-until', '');
  await page.click('.schedule-form button[type=submit]');
  await until('edited saved schedule', () => sent.of('schedule.set').some((p) => p.message === 'Write scheduled-again' && JSON.stringify(p.rule) === JSON.stringify({ every: 30, between: ['09:00', '18:00'], days: ['mon', 'tue', 'wed', 'thu', 'fri'], from: '2099-10-16T00:00' })));
  await status('Paused');
  await until('saved sentence', async () => (await page.locator('.schedule-dialog .schedule-sentence').first().textContent()) === 'Every 30 minutes from 09:00 to 18:00 on weekdays, starting Fri 16 Oct 2099');
  await page.click('button:text-is("Resume schedule")'); await status('Scheduled');
  await close();
  await taskMenu(page, "Close");
  await page.waitForSelector('.task-actions button:text-is("Resume")');
  // The menu stays open when the heading is drawn again between the click
  // that opens it and its toggle event, which comes a task later: under load
  // a redraw landed there and the next Schedule… waited on a closed menu.
  // Cancelling a delete draws the heading at once, inside the same task; its
  // click stops at its button, since the redraw under test comes from an
  // event, and a click elsewhere on the page rightly closes the menu.
  await taskMenu(page, "Delete…");
  const kept = await page.evaluate(async () => {
    document.querySelector('.task-actions .more > summary').click();
    const cancel = document.querySelector('.delete-confirm button:not(.dark)');
    cancel.addEventListener('click', (event) => event.stopPropagation());
    cancel.click();
    await new Promise((r) => setTimeout(r, 50));
    return document.querySelector('.task-actions .more').open;
  });
  assert.strictEqual(kept, true, 'a redraw closed the menu that was just opened');
  await page.keyboard.press('Escape');
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
  // The run is in the schedule's history, newest first, in the daemon's words.
  await manage(); await status("Scheduled"); await page.waitForFunction(() => document.querySelector(".schedule-form [role=status] [title]")?.title.includes("2099"));
  const recent = await page.locator('.schedule-history li').first().textContent();
  assert.ok(recent.includes('Finished (Run now)'), `the history does not say what came of the run: ${recent}`);
  await page.screenshot({ path: `${out}/schedule-controls.png` });
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
  await page.selectOption('#schedule-repeat', 'every');
  await page.check('#schedule-window');
  assert.strictEqual(await page.$eval('dialog.schedule-dialog', (el) => el.scrollWidth > el.clientWidth), false, 'the timetable overflows its dialog on a phone');
  await page.screenshot({ path: `${out}/schedule-phone.png` });
  console.log('schedule create on set days, preview in words, persist, pause, edit to an interval and a monthly rule, resume, run with approval and history, and remove');
});
