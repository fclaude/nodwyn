// nodwyn ui --listen with another host name: a phone pairs by name, answers,
// and the log records "via web:phone"; a launch ticket is refused remotely.
const assert = require("assert");
const fs = require("fs");
const { execFileSync } = require("child_process");
const { out, start, line, tempDir, runtimeDir, freePort, launch, watch, expectError, requests, shows, until, step, ready, check } = require("../lib");

check(async () => {
  // The real path: the folder the phone types is compared with the forgotten
  // marker, which the daemon keeps with symbolic links resolved.
  const root = fs.realpathSync(tempDir("nodwyn-remote-"));
  for (const d of ["config/nodwyn", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { recursive: true, mode: 0o700 });
  fs.writeFileSync(`${root}/work/app.js`, 'function sessionRow(session) {\n  const row = node("button", "", "session");\n  row.append(node("span", session.first, "title"));\n  row.append(node("span", session.repo, "meta"));\n  return row;\n}\n');
  const env = { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir() };
  const model = start(process.env.DEMO, ["-model-only", "-pace=0"]);
  const [modelURL] = await line(model, /MODEL (\S+)/);
  fs.writeFileSync(`${root}/config/nodwyn/config.toml`, `[control]\nurl = "${modelURL}"\nmodel = "demo"\n`);
  // The folder the phone works in was forgotten on this computer: the phone
  // may still choose it by its path, and its task there brings it back.
  fs.writeFileSync(`${root}/config/nodwyn/workspaces.toml`, `forgotten = ["${root}/work"]\n`, { mode: 0o600 });
  const daemon = start(process.env.NODWYN, ["daemon", "--no-keychain"], { env });
  await line(daemon, /(listening)/, "stderr");
  // The console's address has to be known before it starts, since the phone
  // reaches it by the name --name gives with its port: a port the system
  // picked, tried again in the rare case something took it meanwhile.
  let port, code;
  for (let attempt = 1; !code; attempt++) {
    port = await freePort();
    const ui = start(process.env.NODWYN, ["ui", "--listen", `127.0.0.1:${port}`, "--name", `desk.test:${port}`, "--no-open"], { env });
    try {
      [code] = await line(ui, /Pairing code[^:]*: (\S+)/, "stderr");
    } catch (e) {
      if (attempt === 3 || !/address already in use/.test(ui.stderrText)) throw e;
    }
  }
  const browser = await launch({ args: ["--host-resolver-rules=MAP desk.test 127.0.0.1"] });
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const page = watch(await phone.newPage());
  const sent = requests(page);
  // An unpaired browser learns it is one when its socket is refused.
  expectError(/^WebSocket connection to 'ws:\/\/desk\.test:\d+\/socket' failed: .*403$/);
  await page.goto(`http://desk.test:${port}/`);
  await page.waitForSelector("#pair-device");
  step("pairing asks for a device name");
  await page.screenshot({ path: `${out}/phone-pair.png` });
  // The browser holds back a name the daemon would refuse.
  await page.fill("#pair-device", "Phone Two");
  assert.strictEqual(await page.$eval("#pair-device", (i) => i.checkValidity()), false, "the device field takes 'Phone Two'");
  await page.fill("#pair-code", code);
  await page.fill("#pair-device", "phone");
  await page.click("button[type=submit]");
  await ready(page);
  await page.waitForSelector("#task");
  assert.match(fs.readFileSync(`${root}/config/nodwyn/devices.toml`, "utf8"), /name = "phone"/, "devices.toml names the phone");
  step("paired as phone");

  await page.click("text=Add a folder");
  await page.fill("#add-path", `${root}/work`);
  await page.click("button:has-text('Use this folder')");
  await page.waitForSelector(`#work-in button[aria-pressed=true]:has-text('work')`);
  // Where a task's content goes is chosen on the computer nodwyn runs on (ADR
  // 30, ADR 40): the phone is told the default model, and has no picker.
  assert.strictEqual(await page.$("#new-task-model-model"), null, "the phone is offered a model picker");
  // Nor does it lift a task's limits (ADR 49): its tasks start with them on.
  assert.strictEqual(await page.$("#new-task-limits"), null, "the phone is offered Limits on Home");
  await shows(page, ".ask-bar .where", "demo");
  await page.fill("#task", "The session list shows each session's first message. Make them easy to tell apart.");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".ask-card .btn.accent", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.open"), [{ repo: `${root}/work` }], "the task opened in the folder added");
  await shows(page, ".ask-card .fine", "as phone");
  step("approval shown on the phone, naming it");
  // In the conversation the phone sees the model, and may not change it. On a
  // phone's width the reply box is out of the way while a request waits, so
  // this is read at the width of a tablet, which this paired device also is.
  await page.setViewportSize({ width: 800, height: 844 });
  await page.waitForSelector("#task-model summary");
  assert.strictEqual(await page.isVisible("#task-model-model"), false, "the phone's model fields start collapsed");
  await shows(page, ".reply-opts .mp-notes", "chosen on the computer nodwyn runs on");
  await page.click("#task-model summary");
  await page.waitForSelector("#task-model-model");
  assert.strictEqual(await page.isDisabled("#task-model-model"), true, "the phone can change a task's model");
  assert.strictEqual(await page.isDisabled("#task-model-effort"), true, "the phone can change a task's effort");
  await shows(page, ".reply-opts .mp-notes", "chosen on the computer nodwyn runs on");
  assert.strictEqual(sent.of("session.model.set").length, 0, "the phone asked to change a model");
  assert.strictEqual(await page.isVisible(".reply-opts .limits-toggle"), false, "the phone is offered a task's Limits");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: `${out}/phone-approve.png` });
  await page.click(".ask-card .btn.accent");
  // The answered write folds into its Edited group, so the verdict sits
  // inside the group's closed body: wait for the row's outcome in the
  // document, not on screen.
  await page.waitForFunction(() => [...document.querySelectorAll(".activity.writes .step .result")].some((r) => /^(written|failed)/.test(r.textContent.trim())), null, { timeout: 20000 });
  assert.strictEqual(sent.of("session.answer").length, 1, "one answer sent");
  step("answered from the phone");

  // What the phone may not do: pair another browser with a launch ticket.
  expectError(/^Failed to load resource: the server responded with a status of 403 \(Forbidden\)$/);
  const refused = await page.evaluate(async () => {
    const r = await fetch("/pair", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ticket: "x" }) });
    return r.status;
  });
  assert.strictEqual(refused, 403, "a launch ticket from the phone");
  step("a launch ticket is refused remotely");

  await page.evaluate(() => (location.hash = "#/workspaces"));
  await page.waitForSelector(`.ws-row:has(span.mono[title="${root}/work"])`);
  assert.ok(!fs.readFileSync(`${root}/config/nodwyn/workspaces.toml`, "utf8").includes(`${root}/work`), "the phone's task left its folder forgotten");
  step("a folder forgotten on the computer is used from the phone and comes back");
  assert.strictEqual(await page.getByRole("button", { name: /^Forget/ }).count(), 0, "the phone can forget folders");
  assert.strictEqual(sent.of("workspace.remove").length, 0, "the phone asked to forget a folder");
  step("forgetting folders is offered only on this computer");

  const logs = () => fs.readdirSync(`${root}/data/nodwyn/sessions`).map((f) => fs.readFileSync(`${root}/data/nodwyn/sessions/${f}`, "utf8")).join("");
  await until("answer logged as the phone", () => /"actor":"unix:uid=\d+,gid=\d+,pid=\d+ via web:phone"/.test(logs()));
  step("the log names the phone");
  const devices = execFileSync(process.env.NODWYN, ["devices"], { env }).toString().trim();
  assert.match(devices, /^phone\s+paired /m, "nodwyn devices lists the phone");

  // A browser on this computer, paired with the code, lists the phone under
  // Keys & devices. Revoking locks it out at once and pairing it again needs
  // a terminal, so Revoke asks first: Cancel sends nothing, the confirm does.
  const desk = watch(await (await browser.newContext({ viewport: { width: 1280, height: 900 } })).newPage(), "computer");
  const deskSent = requests(desk);
  // Unpaired, its socket is refused first.
  expectError(/^WebSocket connection to 'ws:\/\/127\.0\.0\.1:\d+\/socket' failed: .*403$/);
  await desk.goto(`http://127.0.0.1:${port}/`);
  await desk.fill("#pair-code", code);
  await desk.click("button[type=submit]");
  await desk.waitForSelector("html[data-ready]", { timeout: 10000 });
  await desk.evaluate(() => (location.hash = "#/settings/keys"));
  await desk.waitForSelector("#devices .list-row:has-text('phone')");
  await desk.click("#devices .list-row:has-text('phone') button:text-is('Revoke')");
  await shows(desk, "#devices", "Revoke phone? It is locked out at once, and pairing it again needs a terminal.");
  assert.strictEqual(deskSent.of("devices.revoke").length, 0, "Revoke locked the phone out before it was confirmed");
  await desk.click("#devices button:text-is('Cancel')");
  assert.strictEqual(deskSent.of("devices.revoke").length, 0, "Cancel revoked the phone");
  await desk.click("#devices .list-row:has-text('phone') button:text-is('Revoke')");
  await desk.screenshot({ path: `${out}/revoke-confirm.png` });
  await desk.click("#devices button:text-is('Revoke')");
  await until("the phone gone from the list", async () => (await desk.locator("#devices .list-row").count()) === 0);
  assert.deepStrictEqual(deskSent.of("devices.revoke"), [{ name: "phone" }]);
  // With none paired, the page says how to pair one.
  await shows(desk, "#devices", "No other device is paired.", "nodwyn ui --listen ADDRESS:PORT", "pairing code");
  step("Revoke asks first and then locks the phone out; with none paired, the page says how to pair one");
});
