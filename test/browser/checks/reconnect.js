// A console whose socket drops connects again by itself and reloads the open
// task from its log, sending nothing again: a paired phone across a restart
// of `nodwyn ui` and of the daemon. A browser the console no longer takes (this
// computer's, paired with the run that stopped, and a revoked phone) goes to
// pairing and stops trying. The pages' clocks are paused while the
// connection is down, so each try comes when the check lets it.
const assert = require("assert");
const fs = require("fs");
const { execFileSync } = require("child_process");
const { out, start, line, tempDir, runtimeDir, freePort, launch, watch, expectError, requests, shows, until, step, ready, check } = require("../lib");

check(async () => {
  const root = fs.realpathSync(tempDir("nodwyn-reconnect-"));
  for (const d of ["config/nodwyn", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { recursive: true, mode: 0o700 });
  fs.writeFileSync(`${root}/work/README.md`, "A folder to describe.\n");
  const env = { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir() };
  const model = start(process.env.DEMO, ["-model-only", "-pace=0"]);
  const [modelURL] = await line(model, /MODEL (\S+)/);
  fs.writeFileSync(`${root}/config/nodwyn/config.toml`, `[control]\nurl = "${modelURL}"\nmodel = "demo"\n`);
  const startDaemon = async () => {
    const daemon = start(process.env.NODWYN, ["daemon", "--no-keychain"], { env });
    await line(daemon, /(listening)/, "stderr");
    return daemon;
  };
  let daemon = await startDaemon();
  // The console is started again on the port it had, as a service would
  // be, so the pages' addresses still reach it; the first port is one the
  // system picked, tried again in the rare case something took it meanwhile.
  let port, ui, code;
  const startUI = async () => {
    ui = start(process.env.NODWYN, ["ui", "--listen", `127.0.0.1:${port}`, "--name", `desk.test:${port}`, "--no-open"], { env });
    [code] = await line(ui, /Pairing code[^:]*: (\S+)/, "stderr");
  };
  for (let attempt = 1; !code; attempt++) {
    port = await freePort();
    try {
      await startUI();
    } catch (e) {
      if (attempt === 3 || !/address already in use/.test(ui.stderrText)) throw e;
    }
  }
  const stop = async (kid) => {
    kid.kill("SIGTERM");
    await kid.exited;
  };

  const browser = await launch({ args: ["--host-resolver-rules=MAP desk.test 127.0.0.1"] });
  // Each page counts the sockets it makes, so a check that it made no more
  // does not wait for one that might come.
  const counted = async (options) => {
    const context = await browser.newContext(options);
    await context.addInitScript(() => {
      const Native = window.WebSocket;
      window.socketsMade = 0;
      window.WebSocket = class extends Native {
        constructor(...args) {
          super(...args);
          window.socketsMade++;
        }
      };
    });
    await context.clock.install();
    return context;
  };
  const sockets = (page) => page.evaluate(() => window.socketsMade);
  // pause stops a page's timers, so the next try to connect waits for the
  // check.
  const pause = async (page) => page.context().clock.pauseAt(await page.evaluate(() => Date.now() + 1000));
  // staysUnpaired fails if a page that went to pairing tries to connect
  // again: on its own in the next ten minutes, when the network comes back,
  // or when it is shown again.
  const staysUnpaired = async (page, what) => {
    const made = await sockets(page);
    await pause(page);
    await page.context().clock.runFor(10 * 60 * 1000);
    await page.evaluate(() => {
      window.dispatchEvent(new Event("online"));
      document.dispatchEvent(new Event("visibilitychange"));
    });
    assert.strictEqual(await sockets(page), made, `${what} tried to connect again`);
    assert.ok(await page.isVisible("#pair-code"), `${what} left the pairing form`);
  };

  const phoneContext = await counted({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const phone = watch(await phoneContext.newPage(), "phone");
  const sent = requests(phone);
  // An unpaired browser learns it is one when its socket is refused.
  expectError(/^WebSocket connection to 'ws:\/\/desk\.test:\d+\/socket' failed: .*403$/);
  await phone.goto(`http://desk.test:${port}/`);
  await phone.fill("#pair-code", code);
  await phone.fill("#pair-device", "phone");
  await phone.click("button[type=submit]");
  await ready(phone);
  await phone.click("text=Add a folder");
  await phone.fill("#add-path", `${root}/work`);
  await phone.click("button:has-text('Use this folder')");
  await phone.waitForSelector(`#work-in button[aria-pressed=true]:has-text('work')`);
  await phone.fill("#task", "Describe this folder");
  await phone.click("button[aria-label='Start the task']");
  const transcript = () => phone.innerText(".transcript");
  await until("the phone's task answered", async () => (await transcript()).includes("This request carries 0 image(s)"), 20000);
  const id = await phone.evaluate(() => decodeURIComponent(location.hash.split("/")[2] || ""));
  assert.ok(id, "the phone's task has no address");
  // What reads the task's conversation: its subscription while it is open,
  // its log once it is closed.
  const reads = () => sent.filter((r) => ["session.subscribe", "log.read"].includes(r.method) && r.params?.id === id).length;
  step("a paired phone has a task open");

  // Shown again while live, the phone asks the connection to answer, and
  // keeps it when it does.
  const hellos = sent.of("hello").length;
  let made = await sockets(phone);
  await phone.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await until("the phone asked its connection to answer", () => sent.of("hello").length === hellos + 1);
  assert.strictEqual(await sockets(phone), made, "the phone connected again though its connection answered");
  step("shown again, the phone checks its connection and keeps it");

  const computerContext = await counted({ viewport: { width: 1280, height: 900 } });
  const computer = watch(await computerContext.newPage(), "computer");
  expectError(/^WebSocket connection to 'ws:\/\/127\.0\.0\.1:\d+\/socket' failed: .*403$/);
  await computer.goto(`http://127.0.0.1:${port}/`);
  await computer.fill("#pair-code", code);
  await computer.click("button[type=submit]");
  await ready(computer);
  step("this computer's browser is paired too");

  // nodwyn ui stops and starts again. The phone's pairing outlives it; this
  // computer's was for the run that stopped.
  await pause(phone);
  await pause(computer);
  made = await sockets(phone);
  const asked = { open: sent.of("session.open").length, send: sent.of("session.send").length, subscribe: sent.of("session.subscribe").length, reads: reads() };
  await stop(ui);
  await phone.waitForSelector("#offline:not([hidden])");
  await shows(phone, "#offline", "Disconnected; nothing that was on its way is sent again. Trying again in 1 s.", "Reconnect now");
  assert.ok(await phone.isVisible(".transcript"), "the phone's task left the page while it was disconnected");
  await phone.screenshot({ path: `${out}/phone-disconnected.png` });
  step("the phone says it lost its connection and when it tries again");
  code = "";
  await startUI();
  // The network coming back ends the wait at once: the phone's clock has
  // not moved.
  await phone.evaluate(() => window.dispatchEvent(new Event("online")));
  assert.strictEqual(await sockets(phone), made + 1, "the network back, the phone did not connect at once");
  await phone.context().clock.resume();
  await until("the phone's open task subscribed to again", () => sent.of("session.subscribe").length > asked.subscribe);
  await ready(phone);
  await phone.waitForSelector(".task[data-loaded]");
  assert.ok(await phone.isHidden("#offline"), "the phone still says it is disconnected");
  assert.ok((await transcript()).includes("This request carries 0 image(s)"), "the reloaded task lost its reply");
  assert.strictEqual(sent.of("session.subscribe").at(-1).id, id, "the phone subscribed to another task");
  assert.strictEqual(sent.of("session.open").length, asked.open, "a task was started again");
  assert.strictEqual(sent.of("session.send").length, asked.send, "a message was sent again");
  assert.strictEqual(await sockets(phone), made + 1, "the phone made more than one socket to come back");
  await phone.screenshot({ path: `${out}/phone-back.png` });
  step("the phone comes back by itself and reloads its task from the log, sending nothing again");

  // This computer's next try is refused before its socket opens, and the
  // console says it does not take this browser: it pairs again.
  expectError(/^WebSocket connection to 'ws:\/\/127\.0\.0\.1:\d+\/socket' failed: .*403$/);
  await computer.context().clock.runFor(1000);
  await computer.waitForSelector("#pair-code");
  await staysUnpaired(computer, "this computer's browser, paired with the stopped run,");
  step("this computer's browser, paired with the run that stopped, goes to pairing and stops trying");

  // The daemon stops: the console tells the phone so (1013), and the phone
  // asks no more often than every five seconds.
  await pause(phone);
  const before = reads();
  made = await sockets(phone);
  await stop(daemon);
  await phone.waitForSelector("#offline:not([hidden])");
  await phone.context().clock.runFor(1000);
  assert.strictEqual(await sockets(phone), made + 1, "the phone did not try again a second later");
  await until("the phone told the daemon is not running", async () => (await phone.innerText("#offline")).includes("nodwyn is not running on this computer. Trying again in 5 s."), () => phone.innerText("#offline"));
  step("with the daemon stopped, the phone says so and waits five seconds");
  // This computer's browser, paired again while the daemon is stopped, was
  // never live: the gate says why and when it tries again, and Reconnect now
  // does not wait.
  await computer.fill("#pair-code", code);
  await computer.click("button[type=submit]");
  await computer.waitForSelector("#gate .retry-text");
  await shows(computer, "#gate", "Connection lost", "connects again by itself", "nodwyn is not running on this computer", "Trying again in 5 s.", "Reconnect now");
  await computer.screenshot({ path: `${out}/computer-not-running.png` });
  step("paired while the daemon is stopped, this computer's browser says so and when it tries again");
  daemon = await startDaemon();
  const computerMade = await sockets(computer);
  await computer.context().clock.resume();
  await computer.click("#gate button:text-is('Reconnect now')");
  await ready(computer);
  assert.strictEqual(await sockets(computer), computerMade + 1, "Reconnect now made more than one socket");
  step("Reconnect now connects it at once");
  await phone.context().clock.runFor(5000);
  assert.strictEqual(await sockets(phone), made + 2, "the phone did not try again five seconds later");
  await phone.context().clock.resume();
  await until("the phone's task read again", () => reads() > before);
  await ready(phone);
  await phone.waitForSelector(".task[data-loaded]");
  assert.ok((await transcript()).includes("This request carries 0 image(s)"), "the task reloaded after the daemon's restart lost its reply");
  assert.strictEqual(sent.of("session.open").length, asked.open, "a task was started again");
  step("the daemon back, the phone reconnects and reloads its task");

  // Revoked while its console is open, the phone is told (1008) and goes to
  // pairing, and tries no more.
  execFileSync(process.env.NODWYN, ["devices", "revoke", "phone"], { env });
  await phone.waitForSelector("#pair-device", { timeout: 15000 });
  await shows(phone, "#gate", "this device was revoked");
  await staysUnpaired(phone, "the revoked phone");
  await phone.screenshot({ path: `${out}/phone-revoked.png` });
  step("the revoked phone goes to pairing and stops trying");
});
