// nodwyn ui --listen with other host names (ADR 30, amended 2026-10-17): it
// prints no pairing code; a phone pairs with an invitation from nodwyn devices
// invite, answers, and the log records "via web:phone"; a launch ticket is
// refused remotely. This computer's own page, opened by the launch, invites a
// device that only answers, and makes no passkey even where passkeys work. A
// laptop at a name where passkeys work (desk.localhost
// stands in for https) makes a passkey with its invitation, keeps its sign-in across
// a restart of the console, and confirms with the passkey before it revokes
// the phone; a tablet then signs in with that passkey, with no invitation.
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
  // The browser the launch opens is a stand-in that writes down the address
  // it was given, with the launch ticket in its fragment: this computer's
  // own page, which the check opens itself.
  const bin = tempDir("nodwyn-open-");
  const opened = `${bin}/opened`;
  for (const name of ["open", "xdg-open"]) fs.writeFileSync(`${bin}/${name}`, `#!/bin/sh\nprintf '%s\\n' "$1" > '${opened}'\n`, { mode: 0o755 });
  // The console's address has to be known before it starts, since the phone
  // reaches it by the name --name gives with its port: a port the system
  // picked, tried again in the rare case something took it meanwhile.
  let port, ui;
  const console_ = () => start(process.env.NODWYN, ["ui", "--listen", `127.0.0.1:${port}`, "--name", `desk.test:${port}`, "--name", `desk.localhost:${port}`], { env: { ...env, PATH: `${bin}:${process.env.PATH}` } });
  for (let attempt = 1; !ui; attempt++) {
    port = await freePort();
    const started = console_();
    try {
      await line(started, /(Browser view)/, "stderr");
      ui = started;
    } catch (e) {
      if (attempt === 3 || !/address already in use/.test(started.stderrText)) throw e;
    }
  }
  assert.doesNotMatch(ui.stderrText, /Pairing code/, "a console other machines reach printed a pairing code");
  const invite = () => /Invitation: (\S+)/.exec(execFileSync(process.env.NODWYN, ["devices", "invite"], { env }).toString())[1];
  const code = invite();
  const browser = await launch({ args: ["--host-resolver-rules=MAP desk.test 127.0.0.1"] });
  const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const page = watch(await phone.newPage());
  const sent = requests(page);
  // An unpaired browser learns it is one when its socket is refused.
  expectError(/^WebSocket connection to 'ws:\/\/desk\.test:\d+\/socket' failed: .*403$/);
  await page.goto(`http://desk.test:${port}/`);
  await page.waitForSelector("#pair-invite");
  step("pairing asks for an invitation and a device name");
  // Plain http is no place for a passkey: the page offers none.
  assert.strictEqual(await page.$("text=Connect without a passkey"), null, "a passkey offered over plain http");
  await page.screenshot({ path: `${out}/phone-pair.png` });
  // The browser holds back a name the daemon would refuse.
  await page.fill("#pair-device", "Phone Two");
  assert.strictEqual(await page.$eval("#pair-device", (i) => i.checkValidity()), false, "the device field takes 'Phone Two'");
  await page.fill("#pair-invite", code);
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

  // This computer's own page invites a device that only answers: the
  // browser sends its sign-in to every server on this computer it visits,
  // so an invitation made with it makes no passkey (ADR 30, amended
  // 2026-10-19). At a name where passkeys work, the pairing page then pairs
  // that device without one.
  await until("the launch opened this computer's page", () => fs.existsSync(opened));
  const computerContext = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const computer = watch(await computerContext.newPage(), "computer");
  const computerSent = requests(computer);
  await computer.goto(fs.readFileSync(opened, "utf8").trim());
  await ready(computer);
  await computer.evaluate(() => (location.hash = "#/settings/keys"));
  await computer.click("#devices-invite");
  await computer.waitForSelector("#invitation");
  assert.deepStrictEqual(computerSent.of("devices.invite"), [{ passkey: false }], "this computer's invitation asked for");
  await shows(computer, "#invitation", "The device it lets in answers", "nodwyn devices invite");
  await shows(computer, "#devices", "One made in this computer's browser makes none");
  await computer.screenshot({ path: `${out}/computer-invite.png` });
  const plain = (await computer.textContent("#invitation strong")).trim();
  await computerContext.close();
  const kioskContext = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const kiosk = watch(await kioskContext.newPage(), "kiosk");
  expectError(/^WebSocket connection to 'ws:\/\/desk\.localhost:\d+\/socket' failed: .*403$/);
  // The registration the page tries first is refused, and it pairs instead.
  expectError(/^Failed to load resource: the server responded with a status of 403 \(Forbidden\)$/);
  await kiosk.goto(`http://desk.localhost:${port}/#invite=${plain}`);
  await kiosk.waitForSelector("#pair-invite");
  await kiosk.fill("#pair-device", "kiosk");
  await kiosk.click("button[type=submit]:has-text('Connect and make a passkey')");
  await ready(kiosk);
  const devicesFile = () => fs.readFileSync(`${root}/config/nodwyn/devices.toml`, "utf8");
  const kioskEntry = /name = "kiosk"[^[]*/.exec(devicesFile())?.[0] || "";
  assert.match(kioskEntry, /by = "computer"/, "the kiosk was not let in by this computer");
  assert.doesNotMatch(kioskEntry, /passkey = /, "this computer's invitation signed the kiosk in with a passkey");
  assert.doesNotMatch(devicesFile(), /\[\[passkey\]\]/, "this computer's invitation registered a passkey");
  assert.strictEqual(await kiosk.$("#new-task-model-model"), null, "the kiosk is offered a model picker");
  await kioskContext.close();
  step("this computer's own page invites a device that only answers, with no passkey even where passkeys work");

  // A laptop where passkeys work opens an invitation's link, which makes a
  // passkey there with Chromium's virtual authenticator, the person always
  // present and verified.
  const authenticator = async (context, page) => {
    const cdp = await context.newCDPSession(page);
    await cdp.send("WebAuthn.enable");
    const { authenticatorId } = await cdp.send("WebAuthn.addVirtualAuthenticator", { options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
    return { cdp, authenticatorId };
  };
  const laptopContext = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const desk = watch(await laptopContext.newPage(), "laptop");
  const deskSent = requests(desk);
  const laptopKey = await authenticator(laptopContext, desk);
  expectError(/^WebSocket connection to 'ws:\/\/desk\.localhost:\d+\/socket' failed: .*403$/);
  await desk.goto(`http://desk.localhost:${port}/#invite=${invite()}`);
  await desk.waitForSelector("#pair-invite");
  assert.notStrictEqual(await desk.inputValue("#pair-invite"), "", "the invitation's link did not fill it in");
  await desk.fill("#pair-device", "laptop");
  await desk.screenshot({ path: `${out}/laptop-pair.png` });
  await desk.click("button[type=submit]:has-text('Connect and make a passkey')");
  await desk.waitForSelector("html[data-ready]", { timeout: 10000 });
  assert.match(devicesFile(), /name = "laptop"[^[]*passkey = "laptop"/, "devices.toml has the laptop signed in with its passkey");
  assert.match(devicesFile(), /\[\[passkey\]\]\s+name = "laptop"/, "devices.toml has the laptop's passkey");
  // The person's device chooses a task's model, as this computer does.
  await desk.waitForSelector("#new-task-model-model");
  step("a laptop made a passkey with its invitation, and chooses a task's model");

  // Restarting the console signs nobody out, though it forgets the touch.
  // The phone's page is closed first: it would connect again by itself, and
  // a try that came after its revocation below would be refused (403).
  await page.close();
  ui.kill("SIGTERM");
  await ui.exited;
  ui = console_();
  await line(ui, /(Browser view)/, "stderr");
  await desk.reload();
  await desk.waitForSelector("html[data-ready]", { timeout: 10000 });
  step("the laptop is still signed in after the console restarted");

  // Revoke asks first, and then for the passkey: Cancel sends nothing, the
  // confirm sends it, the bridge holds it for the touch, and the console
  // sends it again once the passkey answered.
  await desk.evaluate(() => (location.hash = "#/settings/keys"));
  await desk.waitForSelector("#devices .list-row:has-text('phone')");
  await shows(desk, "#devices", "laptop (this device)", "signed in with the passkey laptop", "answers only", "Passkey laptop");
  await desk.click("#devices .list-row:has-text('phone') button:text-is('Revoke')");
  await shows(desk, "#devices", "Revoke phone? It is locked out at once, and pairs again only with an invitation.");
  assert.strictEqual(deskSent.of("devices.revoke").length, 0, "Revoke locked the phone out before it was confirmed");
  await desk.click("#devices button:text-is('Cancel')");
  assert.strictEqual(deskSent.of("devices.revoke").length, 0, "Cancel revoked the phone");
  await desk.click("#devices .list-row:has-text('phone') button:text-is('Revoke')");
  await desk.screenshot({ path: `${out}/revoke-confirm.png` });
  await desk.click("#devices button:text-is('Revoke')");
  await desk.waitForSelector("#touch-confirm");
  await desk.screenshot({ path: `${out}/touch.png` });
  assert.ok(fs.readFileSync(`${root}/config/nodwyn/devices.toml`, "utf8").includes('name = "phone"'), "the phone was revoked before the touch");
  await desk.click("#touch-confirm");
  await until("the phone gone from the list", async () => (await desk.locator("#devices .list-row:has-text('phone')").count()) === 0);
  assert.deepStrictEqual(deskSent.of("devices.revoke"), [{ name: "phone" }, { name: "phone" }], "the revocation was not sent once before the touch and once after");
  assert.ok(!devicesFile().includes('name = "phone"'), "devices.toml still has the phone");
  await until("the console's log says who revoked it", () => /web: devices\.revoke by laptop with the passkey laptop: done/.test(ui.stderrText));
  step("Revoke asks first, then for the passkey, and then locks the phone out, said in the console's log");

  // Within the touch, the laptop invites a device that may make a passkey,
  // and is shown the invitation's code and link.
  await desk.click("#devices-invite");
  await desk.waitForSelector("#invitation");
  assert.deepStrictEqual(deskSent.of("devices.invite"), [{ passkey: true }], "the invitation asked for");
  const minted = (await desk.textContent("#invitation strong")).trim();
  await shows(desk, "#invitation", `http://desk.localhost:${port}/#invite=${minted}`, "Good once, until");
  assert.ok(!(await desk.textContent("#devices")).includes("null"), "the devices list shows a null");
  await desk.screenshot({ path: `${out}/invite.png` });
  step("the laptop makes an invitation, with its code and link");

  // A tablet signs in with the passkey made on the laptop, with no
  // invitation: it is the same passkey, as a synced one would be.
  const { credentials } = await laptopKey.cdp.send("WebAuthn.getCredentials", { authenticatorId: laptopKey.authenticatorId });
  assert.strictEqual(credentials.length, 1, "the laptop's authenticator holds one passkey");
  const tabletContext = await browser.newContext({ viewport: { width: 820, height: 1180 } });
  const tablet = watch(await tabletContext.newPage(), "tablet");
  const tabletKey = await authenticator(tabletContext, tablet);
  await tabletKey.cdp.send("WebAuthn.addCredential", { authenticatorId: tabletKey.authenticatorId, credential: credentials[0] });
  expectError(/^WebSocket connection to 'ws:\/\/desk\.localhost:\d+\/socket' failed: .*403$/);
  await tablet.goto(`http://desk.localhost:${port}/`);
  await tablet.waitForSelector("#pair-passkey");
  await tablet.fill("#pair-device", "tablet");
  await tablet.click("#pair-passkey");
  await tablet.waitForSelector("html[data-ready]", { timeout: 10000 });
  assert.match(devicesFile(), /name = "tablet"[^[]*passkey = "laptop"/, "devices.toml has the tablet signed in with the laptop's passkey");
  await until("the console's log says the tablet signed in", () => /web: tablet signed in with the passkey laptop/.test(ui.stderrText));
  step("a tablet signs in with the passkey alone");

  // A desktop uses the laptop's invitation without making a passkey, so it
  // only answers; signing in with the passkey from Settings then makes it
  // the person's, and it chooses a task's model.
  const desktopContext = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const desktop = watch(await desktopContext.newPage(), "desktop");
  const desktopKey = await authenticator(desktopContext, desktop);
  await desktopKey.cdp.send("WebAuthn.addCredential", { authenticatorId: desktopKey.authenticatorId, credential: credentials[0] });
  expectError(/^WebSocket connection to 'ws:\/\/desk\.localhost:\d+\/socket' failed: .*403$/);
  await desktop.goto(`http://desk.localhost:${port}/#invite=${minted}`);
  await desktop.waitForSelector("#pair-invite");
  await desktop.fill("#pair-device", "desktop");
  await desktop.click("text=Connect without a passkey");
  await desktop.waitForSelector("html[data-ready]", { timeout: 10000 });
  const desktopEntry = () => /name = "desktop"[^[]*/.exec(devicesFile())?.[0] || "";
  assert.match(desktopEntry(), /by = "laptop"/, "the desktop was not let in by the laptop");
  assert.doesNotMatch(desktopEntry(), /passkey = /, "the desktop has a passkey without making one");
  assert.strictEqual(await desktop.$("#new-task-model-model"), null, "a device that only answers is offered a model picker");
  // A project's startup files are not offered to a device that only
  // answers: not their state, and not trusting them (ADR 45, amended
  // 2026-10-17). The folder has some, written by whoever can commit to it.
  fs.mkdirSync(`${root}/work/.nodwyn`, { recursive: true });
  fs.writeFileSync(`${root}/work/.nodwyn/init.scm`, '(display "from the project")\n');
  fs.writeFileSync(`${root}/work/.nodwyn/allow.toml`, "");
  await desktop.evaluate(() => (location.hash = "#/workspaces"));
  await desktop.waitForSelector(`.ws-row:has(span.mono[title="${root}/work"])`);
  assert.strictEqual(await desktop.locator("#trust-review-open").count(), 0, "a device that only answers is offered the review of startup files");
  assert.strictEqual(await desktop.locator(".ws-startup").innerText(), "", "a device that only answers is told the state of startup files");
  await desktop.evaluate(() => (location.hash = "#/settings/keys"));
  await desktop.click("#devices-sign-in");
  await until("the desktop signed in with the passkey", () => /passkey = "laptop"/.test(desktopEntry()));
  await desktop.evaluate(() => (location.hash = "#/"));
  await desktop.waitForSelector("#new-task-model-model", { timeout: 10000 });
  step("a device that only answered signs in with the passkey from Settings, and chooses a task's model");

  // The laptop reviews the folder's startup files, whole, and trusts the
  // revision it read. The project's own words are text, never markup.
  await desk.evaluate(() => (location.hash = "#/workspaces"));
  await desk.waitForSelector(`.ws-row:has(span.mono[title="${root}/work"]) #trust-review-open`);
  await shows(desk, ".ws-startup", "are not trusted: tasks here start without them");
  await desk.click("#trust-review-open");
  await desk.waitForSelector("#trust-review");
  await shows(desk, "#trust-review", '(display "from the project")', ".nodwyn/allow.toml", "written by whoever can commit to it");
  const shown = await desk.locator("#trust-review .mono").last().innerText();
  const revision = shown.trim();
  assert.match(revision, /^[0-9a-f]{64}$/, `the revision shown: ${shown}`);
  await desk.screenshot({ path: `${out}/trust-review.png` });
  const trustFile = `${root}/config/nodwyn/trusted-projects.json`;
  assert.ok(!fs.existsSync(trustFile) || !fs.readFileSync(trustFile, "utf8").includes(`${root}/work`), "the project was trusted before it was accepted");
  await desk.click("#trust-accept");
  await until("the project trusted", () => fs.existsSync(trustFile) && fs.readFileSync(trustFile, "utf8").includes(revision));
  assert.deepStrictEqual(deskSent.of("trust.set"), [{ repo: `${root}/work`, revision }], "the revision sent is the one shown");
  await until("the page says the project is trusted", async () => (await desk.locator(".ws-startup").innerText()).includes("are trusted"));
  // Revoking asks first.
  await desk.click("#trust-review-open");
  await desk.click("#trust-review-open");
  await desk.waitForSelector("#trust-review");
  await desk.click("#trust-review button:text-is('Revoke trust')");
  assert.strictEqual(deskSent.of("trust.set").length, 1, "Revoke trust revoked before it was confirmed");
  await desk.click("#trust-review button:text-is('Revoke trust')");
  await until("the trust revoked", () => !fs.readFileSync(trustFile, "utf8").includes(revision));
  assert.deepStrictEqual(deskSent.of("trust.set").at(-1), { repo: `${root}/work`, revision: "" });
  step("the laptop reviews a project's startup files, trusts the revision it read, and revokes it after a confirm");
});
