// While you're away (ADR 53 §11): Home with locked keys that hold back a
// scheduled run says so where its schedules are, and is unlocked there; Settings › General turns the
// daemon's login item on and off, writing it in the demo's own home, and
// saves the daemon's desktop notices.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { out, requests, until, ready, desk, check } = require("../lib");

check(async () => {
  const { url, repo, page } = await desk({ args: ["-locked", "-pace=0"] });
  const root = path.resolve(repo, "../..");
  const sent = requests(page);
  const step = (m) => console.log("step:", m);
  await page.goto(url); await ready(page);

  // Home: the keys are locked and the model needs one, so the schedule's
  // next run waits for the unlock; the callout and the card say so.
  const card = () => page.textContent('[aria-label="Scheduled tasks"] article');
  await page.waitForSelector('[aria-label="Scheduled tasks"] article');
  assert.ok((await page.textContent('#schedules-held')).includes('scheduled runs wait until you unlock them'), 'Home does not say the scheduled runs are held');
  assert.ok((await card()).includes('Every day at 09:00, starting Fri 16 Oct 2099') && (await card()).includes('Waiting for the keychain'), `the card does not say it is held: ${await card()}`);
  assert.strictEqual(await page.$eval('[aria-label="Scheduled tasks"] article .hint.warn', (el) => getComputedStyle(el).color !== getComputedStyle(el.previousElementSibling).color), true, 'the held line looks like any other');
  await page.screenshot({ path: `${out}/away-locked.png` });
  step("Home says locked keys hold back the scheduled run");

  // Unlocked from that callout, the card says when it runs instead.
  await page.fill('#keychain-pass', 'demo-passphrase');
  await page.click('.home-col .callout button[type=submit]');
  await until('the unlock request', () => sent.of('keychain.unlock').length === 1);
  await until('the card says when it runs', async () => (await card()).includes('Next: Fri 16 Oct 2099 09:00'), async () => card());
  assert.ok(!(await card()).includes('Waiting for the keychain') && !(await page.$('#schedules-held')), 'Home still says the run is held');
  step("unlocked on Home, the run is no longer held");

  // Settings › General: the login item, off, turned on and off at once.
  await page.evaluate(() => (location.hash = '#/settings/general'));
  await page.waitForSelector('#section-away');
  const hint = () => page.textContent('#startup .hint');
  await until('the login item read', async () => (await hint()).startsWith('Off:'), hint);
  const file = process.platform === 'darwin'
    ? path.join(root, 'home', 'Library', 'LaunchAgents', 'nodwyn.daemon.plist')
    : path.join(root, 'home', '.config', 'systemd', 'user', 'nodwyn.service');
  assert.strictEqual(fs.existsSync(file), false);
  await page.click('#startup-toggle');
  await until('turned on', async () => (await hint()).startsWith('On:'), hint);
  assert.deepStrictEqual(sent.of('startup.write').at(-1), { on: true });
  assert.ok(fs.readFileSync(file, 'utf8').includes(path.join(root, 'bin', 'nodwyn')), 'the login item does not run the daemon this console talks to');
  assert.strictEqual(await page.textContent('#startup-toggle'), 'Turn off');
  await page.screenshot({ path: `${out}/away-settings.png` });
  await page.click('#startup-toggle');
  await until('turned off', async () => (await hint()).startsWith('Off:'), hint);
  assert.deepStrictEqual(sent.of('startup.write').at(-1), { on: false });
  assert.strictEqual(fs.existsSync(file), false, 'turning it off left the login item');
  step("start at login turned on and off, in the demo's home");

  // The daemon's notices: a choice that waits in the bar, then is saved.
  await page.click("[id='set-notify.desktop-true']");
  await page.click(".save-bar button:text-is('Save')");
  await until('notices saved', () => sent.of('settings.write').some((p) => p.set['notify.desktop'] === true));
  await until('config.toml has them', () => /\[notify\]\s*\ndesktop = true/.test(fs.readFileSync(path.join(root, 'config', 'config.toml'), 'utf8')));
  await page.reload(); await ready(page); await page.waitForSelector('#section-away');
  assert.strictEqual(await page.getAttribute("[id='set-notify.desktop-true']", 'aria-pressed'), 'true', 'the saved choice did not come back');
  step("the daemon's desktop notices saved, and read back after a reload");

  await page.setViewportSize({ width: 390, height: 844 });
  assert.strictEqual(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'Settings overflows a phone');
  console.log("held schedule on Home and unlock, start at login on and off, daemon notices saved");
});
