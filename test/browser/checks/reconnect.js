// A console whose socket drops connects again by itself and reloads the open
// task from its log, sending nothing again: a paired phone across a restart
// of `nodwyn ui` and of the daemon. A browser the console no longer takes (this
// computer's, paired with the run that stopped, and a revoked phone) goes to
// pairing and stops trying. The pages' clocks are paused while the
// connection is down, so each try comes when the check lets it. The daemon
// comes back from a build that reports another version, as `make promote`
// leaves an always-on computer, and the pages say so and offer Reload.
const assert = require("assert");
const fs = require("fs");
const http = require("http");
const path = require("path");
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
  // The upgrade the daemon is restarted with: the same code, reporting
  // another version. Only the link differs, so it builds while the check
  // pairs its pages.
  const next = `${root}/nodwyn-next`;
  const building = start("go", ["build", "-ldflags", "-X main.version=check-next", "-o", next, "./cmd/nodwyn"], { cwd: path.resolve(__dirname, "../../..") });
  const startDaemon = async (binary = process.env.NODWYN) => {
    const daemon = start(binary, ["daemon", "--no-keychain"], { env });
    await line(daemon, /(listening)/, "stderr");
    return daemon;
  };
  let daemon = await startDaemon();
  // Two consoles, each started again on the port it had, as a service
  // would be, so the pages' addresses still reach them: one other machines
  // reach, which the phone pairs with by invitation, and this computer's on
  // loopback alone, which takes the pairing code (one other machines reach
  // takes none, ADR 30, amended 2026-10-17). The first port is one the
  // system picked, tried again in the rare case something took it meanwhile;
  // the loopback console keeps its own (console-port).
  let port, ui, local, localURL, code;
  const startUI = async () => {
    ui = start(process.env.NODWYN, ["ui", "--listen", `127.0.0.1:${port}`, "--name", `desk.test:${port}`, "--no-open"], { env });
    await line(ui, /(Browser view)/, "stderr");
  };
  const startLocal = async () => {
    local = start(process.env.NODWYN, ["ui", "--no-open"], { env });
    [localURL, code] = await line(local, /Browser view: (\S+)[\s\S]*Pairing code[^:]*: (\S+)/, "stderr");
  };
  for (let attempt = 1, started = false; !started; attempt++) {
    port = await freePort();
    try {
      await startUI();
      started = true;
    } catch (e) {
      if (attempt === 3 || !/address already in use/.test(ui.stderrText)) throw e;
    }
  }
  await startLocal();
  const invite = () => /Invitation: (\S+)/.exec(execFileSync(process.env.NODWYN, ["devices", "invite"], { env }).toString())[1];
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
    assert.ok(await page.isVisible("#pair-code, #pair-invite"), `${what} left the pairing form`);
  };

  const phoneContext = await counted({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const phone = watch(await phoneContext.newPage(), "phone");
  const sent = requests(phone);
  // The requests each of the phone's sockets has sent and not had answered:
  // the console asks its connection to answer only when none is waiting.
  const unanswered = new Map();
  phone.on("websocket", (socket) => {
    const ids = new Set();
    unanswered.set(socket, ids);
    socket.on("close", () => unanswered.delete(socket));
    const frame = (payload) => {
      try {
        return JSON.parse(payload);
      } catch {
        return {};
      }
    };
    socket.on("framesent", ({ payload }) => {
      const f = frame(payload);
      if (f.method && f.id != null) ids.add(f.id);
    });
    socket.on("framereceived", ({ payload }) => {
      const f = frame(payload);
      if (!f.method && f.id != null) ids.delete(f.id);
    });
  });
  const waiting = () => [...unanswered.values()].reduce((n, ids) => n + ids.size, 0);
  // An unpaired browser learns it is one when its socket is refused.
  expectError(/^WebSocket connection to 'ws:\/\/desk\.test:\d+\/socket' failed: .*403$/);
  await phone.goto(`http://desk.test:${port}/`);
  await phone.fill("#pair-invite", invite());
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
  // The task's last requests may still be waiting: shown again then, the
  // phone asks nothing, so it is shown again once none is.
  await until("the phone asked its connection to answer", async () => {
    if (sent.of("hello").length > hellos) return true;
    if (!waiting()) await phone.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
    return false;
  });
  assert.strictEqual(sent.of("hello").length, hellos + 1, "the phone asked its connection to answer more than once");
  assert.strictEqual(await sockets(phone), made, "the phone connected again though its connection answered");
  step("shown again, the phone checks its connection and keeps it");

  const computerContext = await counted({ viewport: { width: 1280, height: 900 } });
  const computer = watch(await computerContext.newPage(), "computer");
  expectError(/^WebSocket connection to 'ws:\/\/127\.0\.0\.1:\d+\/socket' failed: .*403$/);
  await computer.goto(`${localURL}/`);
  await computer.fill("#pair-code", code);
  await computer.click("button[type=submit]");
  await ready(computer);
  step("this computer's browser is paired too");

  // The page keeps that sign-in in its origin's storage and sends it itself
  // (ADR 30, amended 2026-10-19). A server on another port of the loopback
  // address that the same browser visits, as a task's dev server would be,
  // is sent no cookie and finds no session in its own storage; the console,
  // loaded again, is still paired.
  const visits = [];
  const devServer = http.createServer((req, res) => {
    visits.push(req.headers);
    res.end("<!doctype html><title>a dev server</title>");
  });
  await new Promise((resolve) => devServer.listen(0, "127.0.0.1", resolve));
  const devPage = await computerContext.newPage();
  await devPage.goto(`http://127.0.0.1:${devServer.address().port}/`);
  const found = await devPage.evaluate(() => ({ cookie: document.cookie, session: localStorage.getItem("nodwyn.session") }));
  await devPage.close();
  await new Promise((resolve) => devServer.close(resolve));
  assert.ok(visits.length > 0, "the dev server was not visited");
  assert.deepStrictEqual(visits.map((h) => h.cookie || ""), visits.map(() => ""), "a server on another loopback port was sent a cookie");
  assert.deepStrictEqual(found, { cookie: "", session: null }, "a page on another loopback port found this computer's sign-in");
  assert.deepStrictEqual(await computerContext.cookies(), [], "the browser holds a cookie for this computer's console");
  assert.match(await computer.evaluate(() => localStorage.getItem("nodwyn.session") || ""), /^[A-Za-z0-9_-]{43}$/, "the console's page keeps no session");
  await computer.reload();
  await ready(computer);
  assert.strictEqual(await computer.$("#pair-code"), null, "the console loaded again asks to pair");
  step("a server on another loopback port is sent nothing of this computer's sign-in, which the page keeps");

  // Both consoles stop and start again. The phone's pairing outlives its;
  // this computer's was for the run that stopped.
  await pause(phone);
  await pause(computer);
  made = await sockets(phone);
  const asked = { open: sent.of("session.open").length, send: sent.of("session.send").length, subscribe: sent.of("session.subscribe").length, reads: reads() };
  await stop(ui);
  await stop(local);
  await phone.waitForSelector("#offline:not([hidden])");
  await shows(phone, "#offline", "Disconnected; nothing that was on its way is sent again. Trying again in 1 s.", "Reconnect now");
  assert.ok(await phone.isVisible(".transcript"), "the phone's task left the page while it was disconnected");
  await phone.screenshot({ path: `${out}/phone-disconnected.png` });
  step("the phone says it lost its connection and when it tries again");
  await startUI();
  await startLocal();
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
  // The daemon it reached again is the one it left, so its code is still
  // the console's.
  assert.ok(await phone.isHidden("#updated"), "after a plain restart the phone says nodwyn was upgraded");
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
  // asks no more often than every five seconds. The phone is writing a
  // reply, which an upgrade's banner must not take from it.
  const unsent = "A reply not yet sent";
  await phone.fill("#reply", unsent);
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
  assert.strictEqual(await building.exited, 0, `the upgrade did not build:\n${building.stderrText}`);
  daemon = await startDaemon(next);
  const computerMade = await sockets(computer);
  await computer.context().clock.resume();
  await computer.click("#gate button:text-is('Reconnect now')");
  await ready(computer);
  assert.strictEqual(await sockets(computer), computerMade + 1, "Reconnect now made more than one socket");
  // This page was first live with the daemon before the upgrade, and its
  // pairing again did not reload it.
  await computer.waitForSelector("#updated:not([hidden])");
  await shows(computer, "#updated", "nodwyn was restarted as check-next", "Reload");
  step("Reconnect now connects it at once, to an upgraded daemon, which it says");
  await phone.context().clock.runFor(5000);
  assert.strictEqual(await sockets(phone), made + 2, "the phone did not try again five seconds later");
  await phone.context().clock.resume();
  await until("the phone's task read again", () => reads() > before);
  await ready(phone);
  await phone.waitForSelector(".task[data-loaded]");
  assert.ok((await transcript()).includes("This request carries 0 image(s)"), "the task reloaded after the daemon's restart lost its reply");
  assert.strictEqual(sent.of("session.open").length, asked.open, "a task was started again");
  step("the daemon back, the phone reconnects and reloads its task");

  // The daemon came back as another version than the one the phone was
  // loaded with: it says so and offers Reload, without reloading by itself,
  // and the reply being written is still there.
  await phone.waitForSelector("#updated:not([hidden])");
  await shows(phone, "#updated", "nodwyn was restarted as check-next", "this page still runs the console it loaded before", "a reload loses text you have not sent", "Reload");
  assert.ok(await phone.isHidden("#offline"), "the upgraded phone still says it is disconnected");
  assert.strictEqual(await phone.inputValue("#reply"), unsent, "the reply being written was lost");
  assert.strictEqual(await sockets(phone), made + 2, "the phone reloaded by itself");
  await phone.screenshot({ path: `${out}/phone-upgraded.png` });
  step("reconnected to an upgraded daemon, the phone says so, offers Reload and keeps its reply");
  // Reloaded, the page is first live with the upgraded daemon, so there is
  // nothing to say. The draft was only in the old page's memory, never in
  // the browser's storage (ADR 30, amended 2026-10-17).
  const loaded = phone.waitForEvent("load");
  await phone.click("#reload");
  await loaded;
  await ready(phone);
  await phone.waitForSelector(".task[data-loaded]");
  assert.ok(await phone.isHidden("#updated"), "the reloaded phone still says nodwyn was upgraded");
  assert.strictEqual(await phone.inputValue("#reply"), "", "the reload kept a draft");
  const stored = await phone.evaluate(() => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]));
  assert.ok(!stored.includes(unsent), `the draft was written to the browser's storage: ${stored}`);
  step("Reload runs the new console, which has nothing to say, and the draft went with the old page");

  // Revoked while its console is open, the phone is told (1008) and goes to
  // pairing, and tries no more.
  execFileSync(process.env.NODWYN, ["devices", "revoke", "phone"], { env });
  await phone.waitForSelector("#pair-device", { timeout: 15000 });
  await shows(phone, "#gate", "this device was revoked");
  await staysUnpaired(phone, "the revoked phone");
  await phone.screenshot({ path: `${out}/phone-revoked.png` });
  step("the revoked phone goes to pairing and stops trying");
});
