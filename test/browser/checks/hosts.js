// Several hosts in one window (ADR 54): a laptop's console links to an
// always-on host, workshop, with an invitation made there, and then shows both
// hosts' tasks. Runs on starts a task on workshop, in a folder of workshop's and on
// workshop's model, with a file sent there; Needs you holds what both hosts ask,
// and Allow once answers workshop's task over workshop's socket, which workshop
// records as the laptop's answer. A notice of workshop's task names its host,
// and opening it opens that task on workshop. workshop revoking the laptop says so
// in the sidebar and on Hosts, and Unlink forgets the link.
const assert = require("assert");
const fs = require("fs");
const { execFileSync } = require("child_process");
const { out, start, line, tempDir, runtimeDir, freePort, launch, watch, expectError, shows, until, step, ready, check } = require("../lib");

check(async () => {
  // Two worlds, each with its own config, data, folder and scripted model.
  const world = async (name) => {
    const root = fs.realpathSync(tempDir(`nodwyn-${name}-`));
    for (const d of ["config/nodwyn", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { recursive: true, mode: 0o700 });
    const env = { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir() };
    const model = start(process.env.DEMO, ["-model-only", "-pace=0"]);
    const [modelURL] = await line(model, /MODEL (\S+)/);
    fs.writeFileSync(`${root}/config/nodwyn/config.toml`, `[control]\nurl = "${modelURL}"\nmodel = "demo"\n`);
    return { root, env, modelHost: new URL(modelURL).host };
  };
  const workshop = await world("workshop");
  // A second model on workshop's server, for a link to choose as its default.
  const tarroConfigFile = `${workshop.root}/config/nodwyn/config.toml`;
  fs.appendFileSync(tarroConfigFile, `\n[models.second]\nurl = "${/url = "([^"]+)"/.exec(fs.readFileSync(tarroConfigFile, "utf8"))[1]}"\nmodel = "second"\n`);
  // And a provider, as a link may put models only on one already there.
  fs.appendFileSync(tarroConfigFile, `\n[servers.shelf]\nprovider = "openai"\nurl = "${/url = "([^"]+)"/.exec(fs.readFileSync(tarroConfigFile, "utf8"))[1]}"\n`);
  const laptop = await world("laptop");

  // workshop: a daemon and a console other machines reach, at its address and
  // at a localhost name, where a browser makes passkeys.
  const tarroDaemon = start(process.env.NODWYN, ["daemon", "--no-keychain"], { env: workshop.env });
  await line(tarroDaemon, /(listening)/, "stderr");
  let port, tarroUI;
  const tarroConsole = (...more) => start(process.env.NODWYN, ["ui", "--listen", `127.0.0.1:${port}`, "--name", `localhost:${port}`, "--no-open", ...more], { env: workshop.env });
  for (let attempt = 1; !tarroUI; attempt++) {
    port = await freePort();
    const started = tarroConsole();
    try {
      await line(started, /(Browser view)/, "stderr");
      tarroUI = started;
    } catch (e) {
      if (attempt === 3 || !/address already in use/.test(started.stderrText)) throw e;
    }
  }
  const tarroAddress = `http://127.0.0.1:${port}`;

  // The laptop: a keychain that opens by itself, a daemon, and this
  // computer's console.
  execFileSync(process.env.NODWYN, ["keychain", "init", "--no-passphrase"], { env: laptop.env });
  const laptopDaemon = start(process.env.NODWYN, ["daemon"], { env: laptop.env });
  await line(laptopDaemon, /(listening)/, "stderr");
  const laptopUI = start(process.env.NODWYN, ["ui", "--no-open"], { env: laptop.env });
  const [url] = await line(laptopUI, /Browser view: (\S+)/, "stderr");
  const [code] = await line(laptopUI, /Pairing code[^:]*: (\S+)/, "stderr");

  const browser = await launch();
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  // As in the install check: a headless browser shows no notice, so the
  // page gets a permission granted and the switch on, the worker's notices
  // are kept where the check reads them, and the check says whether the
  // page is hidden.
  await context.addInitScript(() => {
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => "granted" });
    localStorage.setItem("nodwyn-notify", "on");
    window.hiddenForCheck = false;
    Object.defineProperty(Document.prototype, "hidden", { configurable: true, get: () => window.hiddenForCheck });
    window.shownForCheck = [];
    ServiceWorkerRegistration.prototype.showNotification = async (title, options) => void window.shownForCheck.push({ title, ...options, closed: false });
    ServiceWorkerRegistration.prototype.getNotifications = async ({ tag } = {}) =>
      window.shownForCheck.filter((n) => !n.closed && (!tag || n.tag === tag)).map((n) => ({ close: () => (n.closed = true) }));
  });
  const page = watch(await context.newPage(), "laptop");
  // What each socket sent: this computer's, and workshop's through it.
  const sent = { local: [], workshop: [] };
  page.on("websocket", (socket) => {
    const to = new URL(socket.url()).searchParams.get("host") === "workshop" ? sent.workshop : socket.url().endsWith("/socket") ? sent.local : null;
    socket.on("framesent", ({ payload }) => {
      try {
        const frame = JSON.parse(payload);
        if (frame.method && to) to.push({ method: frame.method, params: frame.params });
      } catch {
        /* not JSON-RPC */
      }
    });
  });
  const of = (list, method) => list.filter((r) => r.method === method).map((r) => r.params);
  const uploads = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/attach") uploads.push(new URL(request.url()).search);
  });
  // The page asks before it is paired.
  expectError(/^WebSocket connection to 'ws:\/\/127\.0\.0\.1:\d+\/socket' failed: .*403$/);
  await page.goto(url);
  await page.fill("#pair-code", code);
  await page.click("button[type=submit]");
  await ready(page);
  // With no link the sidebar is as it was: no host headings.
  assert.strictEqual(await page.locator(".nav-host").count(), 0, "a window with no link shows host headings");
  assert.strictEqual(await page.locator("#runs-on").count(), 0, "a window with no link offers Runs on");
  step("the laptop's console, with no link");

  // Add a host, with an invitation from workshop's terminal.
  await page.click(".nav-link[href='#/hosts']");
  await page.waitForSelector("#hosts .host-card");
  await page.click("#add-host");
  const invite = /Invitation: (\S+)/.exec(execFileSync(process.env.NODWYN, ["devices", "invite"], { env: workshop.env }).toString())[1];
  await page.fill("#host-address", tarroAddress);
  await page.fill("#host-as", "laptop");
  await page.fill("#host-name", "workshop");
  await page.fill("#host-invite", invite);
  await page.screenshot({ path: `${out}/add-host.png` });
  await page.click("#add-host-form button[type=submit]");
  await page.waitForSelector("#hosts .host-card[data-host='workshop'] .host-state:has-text('ready')", { timeout: 20000 });
  assert.deepStrictEqual(of(sent.local, "host.link"), [{ name: "workshop", address: tarroAddress, as: "laptop", invite }], "Link sent");
  await shows(page, "#hosts .host-card[data-host='workshop']", "workshop", tarroAddress, "Linked as laptop", "Answers");
  const tarroDevices = fs.readFileSync(`${workshop.root}/config/nodwyn/devices.toml`, "utf8");
  assert.match(tarroDevices, /name = "laptop"[^[]*host = true/, "workshop's devices.toml has the laptop as another nodwyn");
  const hostsFile = () => (fs.existsSync(`${laptop.root}/config/nodwyn/hosts.toml`) ? fs.readFileSync(`${laptop.root}/config/nodwyn/hosts.toml`, "utf8") : "");
  assert.match(hostsFile(), /name = "workshop"/, "hosts.toml names workshop");
  assert.doesNotMatch(hostsFile(), /nodwyn_device/, "hosts.toml holds the token");
  assert.match(fs.readFileSync(`${laptop.root}/config/nodwyn/bindings.toml`, "utf8"), /class = "nodwyn-host-workshop"/, "the token is not bound to workshop");
  await page.screenshot({ path: `${out}/hosts.png` });
  step("workshop is linked, as a device that answers, with its token in the laptop's keychain");

  // The sidebar groups tasks by host.
  await page.waitForSelector(".nav-host[data-host=''] .host-head:has-text('This computer')");
  await page.waitForSelector(".nav-host[data-host='workshop'] .host-head:has-text('ready')");

  // Runs on workshop: workshop's model and workshop's folder, a file sent there, and
  // the task opened over workshop's socket.
  await page.click("a.new-task");
  await page.waitForSelector("#runs-on");
  await page.selectOption("#runs-on", "workshop");
  // Runs on is a native select drawn like the model picker: no native arrow, a CSS chevron.
  const look = await page.$eval("#runs-on", (el) => ({
    tag: el.tagName,
    appearance: getComputedStyle(el).appearance,
    chevron: getComputedStyle(el.parentElement, "::after").content,
    value: el.value,
  }));
  assert.deepStrictEqual(look, { tag: "SELECT", appearance: "none", chevron: '""', value: "workshop" }, "Runs on looks like the model picker");
  assert.strictEqual(await page.evaluate(() => localStorage.getItem("nodwyn-runs-on")), null, "Runs on was kept in this browser alone, not by the daemon");
  const newTaskFile = `${laptop.root}/config/nodwyn/new-task.toml`;
  await until("the daemon to keep Runs on", () => fs.existsSync(newTaskFile) && /runs_on = "workshop"/.test(fs.readFileSync(newTaskFile, "utf8")));
  await page.waitForSelector(".work-in-host:has-text('Folders on workshop')");
  await shows(page, ".ask-bar .where", workshop.modelHost);
  // A device that answers on workshop chooses no model there, as a paired phone does not.
  assert.strictEqual(await page.locator("#new-task-model-model").count(), 0, "the laptop is offered workshop's model picker");
  // It says why, and the link opens workshop's Settings, which say how to change it.
  await page.click("#host-model-why");
  await page.waitForFunction(() => location.hash === "#/host/workshop/settings");
  await page.waitForSelector("#host-settings-refused");
  await shows(page, "#host-settings-refused", "not change workshop's settings or choose the model its tasks use");
  await page.click("a.new-task");
  await page.waitForSelector(".work-in-host:has-text('Folders on workshop')");
  await page.click("text=Add a folder");
  await page.fill("#add-path", `${workshop.root}/work`);
  await page.click("button:has-text('Use this folder')");
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('work')");
  assert.strictEqual(of(sent.workshop, "workspace.save").length, 0, "the laptop saved a workspace on workshop");
  fs.writeFileSync(`${out}/notes.md`, "# Notes\nfor workshop\n");
  await page.setInputFiles(".ask-bar input[type=file]", `${out}/notes.md`);
  await page.fill("#task", "Hold the line on workshop");
  await page.evaluate(() => (window.hiddenForCheck = true));
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => /^#\/host\/workshop\/task\//.test(location.hash), null, { timeout: 20000 });
  const tarroTask = decodeURIComponent((await page.evaluate(() => location.hash)).replace(/^#\/host\/workshop\/task\//, ""));
  assert.deepStrictEqual(of(sent.workshop, "session.open"), [{ repo: `${workshop.root}/work` }], "the task opened on workshop, in its folder");
  assert.strictEqual(of(sent.local, "session.open").length, 0, "the task opened on the laptop");
  assert.ok(uploads.some((q) => /[?&]host=workshop(&|$)/.test(q)), `the file went to workshop: ${uploads}`);
  await until("the file in workshop's folder", () => {
    const dir = `${workshop.root}/work/.nodwyn/attachments`;
    return fs.existsSync(dir) && fs.readdirSync(dir).some((d) => fs.existsSync(`${dir}/${d}/notes.md`));
  });
  await page.waitForSelector(".task-meta .host-chip:has-text('workshop')");
  // Its model cannot be changed from here either, and the picker says why.
  await page.waitForSelector("#task-model .mp-note:has-text('This computer only answers on workshop, so it cannot choose the model')");
  assert.strictEqual(await page.getAttribute("#task-model .mp-note a:has-text(\"workshop's Settings\")", "href"), "#/host/workshop/settings", "the task's note does not link workshop's Settings");
  await page.waitForSelector(".ask-card .btn.accent", { timeout: 20000 });
  await page.screenshot({ path: `${out}/workshop-task.png` });
  step("a task started on workshop, in workshop's folder and on its model, with a file sent there, asks on its page");
  const [notice] = await until("a notice of workshop's task", () => page.evaluate(() => window.shownForCheck.length && window.shownForCheck));
  assert.deepStrictEqual(
    { title: notice.title, tag: notice.tag, data: notice.data },
    { title: "nodwyn needs your answer", tag: `nodwyn-workshop-${tarroTask}`, data: { id: tarroTask, host: "workshop" } },
  );
  await page.evaluate(() => (window.hiddenForCheck = false));
  step("workshop's task asking while the page is hidden shows the fixed notice, naming its host as data");

  // And one here, on this computer. The next task starts on workshop, the host
  // chosen last.
  await page.click("a.new-task");
  await page.waitForSelector("#runs-on");
  assert.strictEqual(await page.inputValue("#runs-on"), "workshop", "the next task does not start on the host chosen last");
  await page.selectOption("#runs-on", "");
  await page.waitForSelector(".work-in-host:has-text('Folders on this computer')");
  await page.click("text=Add a folder");
  await page.fill("#add-path", `${laptop.root}/work`);
  await page.fill("#add-name", "work");
  await page.click("button:has-text('Add this folder')");
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('work')");
  await page.fill("#task", "Hold the line here");
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => /^#\/task\//.test(location.hash), null, { timeout: 20000 });
  assert.deepStrictEqual(of(sent.local, "session.open"), [{ repo: `${laptop.root}/work` }], "the task opened on the laptop");
  await page.waitForSelector(".ask-card .btn.accent", { timeout: 20000 });
  step("a task started on this computer asks too");

  // Needs you is every host's; Allow once answers workshop's over workshop's socket.
  await page.click("a.new-task");
  await until("both hosts' questions on Home", async () => (await page.locator(".home-needs article.card").count()) === 2, () => page.innerText(".home-needs"));
  await shows(page, ".home-needs", "workshop", "This computer", "Hold the line on workshop", "Hold the line here");
  await shows(page, ".nav-group:has(.eyebrow:has-text('Needs you'))", "workshop", "This computer");
  await page.screenshot({ path: `${out}/home.png` });
  const answeredBefore = of(sent.local, "session.answer").length;
  await page.click(".home-needs article.card:has(.host-chip:has-text('workshop')) button:has-text('Allow once')");
  await until("workshop's answer sent", () => of(sent.workshop, "session.answer").length === 1);
  assert.strictEqual(of(sent.local, "session.answer").length, answeredBefore, "workshop's answer went to the laptop");
  const tarroLogs = () => fs.readdirSync(`${workshop.root}/data/nodwyn/sessions`).filter((f) => f.endsWith(".jsonl")).map((f) => fs.readFileSync(`${workshop.root}/data/nodwyn/sessions/${f}`, "utf8")).join("");
  await until("workshop records the laptop's answer", () => /"actor":"unix:uid=\d+,gid=\d+,pid=\d+ via web:laptop"/.test(tarroLogs()));
  await until("workshop's question gone from Home", async () => (await page.locator(".home-needs article.card").count()) === 1);
  await until("workshop's notice closed once answered", () => page.evaluate(() => window.shownForCheck[0].closed));
  step("Needs you holds both hosts' questions, and workshop's answer goes to workshop, recorded as the laptop's");

  // The worker opening workshop's task brings the console to it, on workshop.
  const [worker] = context.serviceWorkers();
  await worker.evaluate((task) => openTask(task, "workshop").catch(() => {}), tarroTask);
  await until("the console on workshop's task", () => page.evaluate((task) => location.hash === `#/host/workshop/task/${task}`, tarroTask));
  await page.waitForSelector(".task-meta .host-chip:has-text('workshop')");
  assert.strictEqual(context.pages().length, 1, "the worker opened a second window beside the console");
  await page.click("a.new-task");
  step("opening the notice opens workshop's task");

  // A host's heading folds its folders away, keeping its state in view, and
  // this browser remembers it across a reload; this computer's is separate.
  const tarroFolders = ".nav-host[data-host='workshop'] .folder-head";
  const tarroHead = ".nav-host[data-host='workshop'] button.host-head";
  assert.ok((await page.locator(tarroFolders).count()) > 0, "workshop shows no folder to fold");
  const localFolders = await page.locator(".nav-host[data-host=''] .folder-head").count();
  await page.click(tarroHead);
  await until("workshop folded", async () => (await page.locator(tarroFolders).count()) === 0);
  assert.strictEqual(await page.getAttribute(tarroHead, "aria-expanded"), "false", "a folded host says it is open");
  await shows(page, ".nav-host[data-host='workshop'] .host-head", "workshop");
  assert.strictEqual(await page.locator(".nav-host[data-host=''] .folder-head").count(), localFolders, "folding workshop folded this computer");
  await page.reload();
  await ready(page);
  await page.waitForSelector(tarroHead);
  assert.strictEqual(await page.locator(tarroFolders).count(), 0, "workshop unfolded on reload");
  await page.click(tarroHead);
  await until("workshop unfolded", async () => (await page.locator(tarroFolders).count()) > 0);
  assert.strictEqual(await page.getAttribute(tarroHead, "aria-expanded"), "true", "an unfolded host says it is folded");
  step("a host's heading folds its folders, and the fold is remembered");

  // At a phone's width the hosts and Runs on fit, and the drawer holds the
  // hosts with their tasks.
  await page.setViewportSize({ width: 390, height: 844 });
  const drawer = (shown) => page.waitForFunction((shown) => (document.getElementById("sidebar").getBoundingClientRect().left >= 0) === shown, shown);
  await drawer(false);
  assert.strictEqual(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "Home overflows a phone");
  await page.screenshot({ path: `${out}/phone-home.png` });
  await page.click("#menu");
  await drawer(true);
  await shows(page, "#sidebar", "This computer", "workshop");
  await page.screenshot({ path: `${out}/phone-drawer.png` });
  await page.click("#scrim", { position: { x: 380, y: 100 } });
  await drawer(false);
  await page.evaluate(() => (location.hash = "#/hosts"));
  await page.waitForSelector("#hosts .host-card[data-host='workshop']");
  await drawer(false);
  assert.strictEqual(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, "Hosts overflows a phone");
  await page.screenshot({ path: `${out}/phone-hosts.png` });
  await page.setViewportSize({ width: 1440, height: 1000 });
  step("nothing sideways at a phone's width");

  // workshop's Settings open in this window, over workshop's socket (ADR 54,
  // amended 2026-10-23). This link only answers there, so the page says why
  // it changes nothing, and shows no control.
  await page.click("#host-settings-workshop");
  await page.waitForFunction(() => location.hash === "#/host/workshop/settings");
  await shows(page, "main.settings-page h1", "workshop's settings");
  // The link was made at an IP address over plain http, where no browser
  // uses a passkey, so the page says that is what to change.
  await shows(page, "#host-settings-refused", "From here you can start tasks on workshop and answer their questions", `This computer reaches workshop at ${tarroAddress}`, "Settings › Keys & devices", "add it again at its https address", "Open workshop's console");
  await shows(page, "#settings-hosts", "This computer", "workshop");
  assert.strictEqual(await page.locator("main.settings-page [role=tablist], main.settings-page .settings-panel input, main.settings-page .settings-panel button").count(), 0, "workshop's Settings show controls to a link that answers");
  assert.ok(of(sent.workshop, "settings.read").length > 0, "workshop's settings were not read over workshop's socket");
  await page.screenshot({ path: `${out}/workshop-settings-answers.png` });
  // This computer's are a click away, with their controls.
  await page.click("#settings-hosts a[data-host='']");
  await page.waitForFunction(() => location.hash === "#/settings");
  await page.waitForSelector("main.settings-page [role=tablist]");
  assert.strictEqual(await page.locator("#host-settings-refused").count(), 0, "this computer's Settings say workshop's refusal");
  assert.strictEqual((await page.innerText("main.settings-page h1")).trim(), "Settings", "this computer's Settings are named for workshop");
  await page.evaluate(() => (location.hash = "#/hosts"));
  await page.waitForSelector("#hosts .host-card[data-host='workshop']");
  step("workshop's Settings, from Hosts, say that a link that answers changes none of them");

  // workshop revokes the laptop: the window says so, and stops trying.
  execFileSync(process.env.NODWYN, ["devices", "revoke", "laptop"], { env: workshop.env });
  await page.waitForSelector("#hosts .host-card[data-host='workshop'] .host-state:has-text('refused')", { timeout: 20000 });
  await page.waitForSelector(".nav-host[data-host='workshop'] .host-head:has-text('refused')");
  step("workshop's revocation shows as refused");

  // Unlink asks first, then forgets the link.
  await page.click("#unlink-workshop");
  await shows(page, "#hosts .host-card[data-host='workshop']", "Unlink workshop?");
  assert.strictEqual(of(sent.local, "host.unlink").length, 0, "Unlink unlinked before it was confirmed");
  await page.click("#unlink-workshop-confirm");
  await until("workshop gone from Hosts", async () => (await page.locator("#hosts .host-card[data-host='workshop']").count()) === 0);
  assert.deepStrictEqual(of(sent.local, "host.unlink"), [{ name: "workshop" }], "Unlink sent");
  assert.doesNotMatch(hostsFile(), /workshop/, "hosts.toml still names workshop");
  assert.doesNotMatch(fs.readFileSync(`${laptop.root}/config/nodwyn/bindings.toml`, "utf8"), /nodwyn-host-workshop/, "the token is still bound");
  assert.strictEqual(await page.locator(".nav-host").count(), 0, "the sidebar still groups by host with no link");
  step("Unlink forgets the link, and the window is as it was");

  // A link vouched for by the person's passkey changes workshop's settings
  // here (ADR 54, amended 2026-10-23). workshop's phone, signed in at the
  // localhost name with a passkey made by Chromium's virtual authenticator,
  // makes the invitation.
  const phoneContext = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const phone = watch(await phoneContext.newPage(), "workshop's phone");
  const cdp = await phoneContext.newCDPSession(phone);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", { options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } });
  const first = /Invitation: (\S+)/.exec(execFileSync(process.env.NODWYN, ["devices", "invite"], { env: workshop.env }).toString())[1];
  expectError(/^WebSocket connection to 'ws:\/\/localhost:\d+\/socket' failed: .*403$/);
  await phone.goto(`http://localhost:${port}/#invite=${first}`);
  await phone.waitForSelector("#pair-invite");
  await phone.fill("#pair-device", "phone");
  await phone.click("button[type=submit]:has-text('Connect and make a passkey')");
  await ready(phone);
  await phone.evaluate(() => (location.hash = "#/settings/keys"));
  await phone.click("#devices-invite");
  // An invitation that may make a passkey waits for a touch, unless the one
  // just made still counts.
  await phone.waitForSelector("#invitation, #touch-confirm");
  if (await phone.locator("#touch-confirm").count()) await phone.click("#touch-confirm");
  await phone.waitForSelector("#invitation");
  const vouched = (await phone.textContent("#invitation strong")).trim();
  await phoneContext.close();

  // An invitation from workshop's terminal, at the localhost name where a
  // passkey could be used, still makes a link that answers: workshop's
  // Settings say the invitation is what to change.
  const plainInvite = /Invitation: (\S+)/.exec(execFileSync(process.env.NODWYN, ["devices", "invite"], { env: workshop.env }).toString())[1];
  await page.click("#add-host");
  await page.fill("#host-address", `http://localhost:${port}`);
  await page.fill("#host-as", "laptop");
  await page.fill("#host-name", "workshop");
  await page.fill("#host-invite", plainInvite);
  await page.click("#add-host-form button[type=submit]");
  await page.waitForSelector("#hosts .host-card[data-host='workshop'] .host-state:has-text('ready')", { timeout: 20000 });
  await shows(page, "#hosts .host-card[data-host='workshop']", "Answers");
  await page.click("#host-settings-workshop");
  await page.waitForSelector("#host-settings-refused");
  await shows(page, "#host-settings-refused", "This link's invitation came from workshop's terminal", "Invite a device", "unlink workshop and add it again with that invitation");
  assert.ok(!(await page.innerText("#host-settings-refused")).includes("This computer reaches workshop at"), "a localhost link is told its address is the fault");
  await page.screenshot({ path: `${out}/workshop-settings-terminal.png` });
  await page.evaluate(() => (location.hash = "#/hosts"));
  await page.click("#unlink-workshop");
  await page.click("#unlink-workshop-confirm");
  await until("workshop unlinked again", async () => (await page.locator("#hosts .host-card[data-host='workshop']").count()) === 0);
  step("a terminal invitation at a localhost name: workshop's Settings say the invitation is what to change");

  await page.click("#add-host");
  await page.fill("#host-address", `http://localhost:${port}`);
  await page.fill("#host-as", "laptop");
  await page.fill("#host-name", "workshop");
  await page.fill("#host-invite", vouched);
  await page.click("#add-host-form button[type=submit]");
  await page.waitForSelector("#hosts .host-card[data-host='workshop'] .host-state:has-text('ready')", { timeout: 20000 });
  await shows(page, "#hosts .host-card[data-host='workshop']", "Makes your choices");
  assert.match(fs.readFileSync(`${workshop.root}/config/nodwyn/devices.toml`, "utf8"), /name = "laptop"[^[]*passkey = "phone"[^[]*host = true/, "workshop's devices.toml has the laptop vouched for by the phone's passkey");
  await page.click("#host-settings-workshop");
  await page.waitForSelector("main.settings-page [role=tablist]");
  await shows(page, "main.settings-page h1", "workshop's settings");
  assert.strictEqual(await page.locator("#host-settings-refused").count(), 0, "workshop's Settings refuse the vouched link");
  assert.deepStrictEqual(await page.$$eval("main.settings-page [role=tab]", (tabs) => tabs.map((t) => t.textContent.trim())), ["General", "Models"], "workshop's Settings tabs");
  // What would widen what a task may do waits for the touch a link cannot
  // give: the approvals are shown and cannot be chosen, and so is never asking
  // about an upload.
  assert.strictEqual(await page.locator("[id='set-approval.writes-ask']").isDisabled(), true, "a link can choose how workshop asks before writes");
  assert.strictEqual(await page.locator("[id='set-approval.dont_ask-true']").isDisabled(), true, "a link can switch Don't ask on at workshop");
  const configOf = (w) => fs.readFileSync(`${w.root}/config/nodwyn/config.toml`, "utf8");
  const laptopConfig = configOf(laptop);
  await page.click("#advanced > summary");
  await page.fill("[id='set-loop.shell_timeout']", "3m");
  await page.waitForSelector(".save-bar:not([hidden]) .btn.accent");
  await page.screenshot({ path: `${out}/workshop-settings.png` });
  await page.click(".save-bar .btn.accent");
  await until("workshop's config.toml to wait 3 minutes for a command", () => /\[loop\][^[]*shell_timeout = "3m"/.test(configOf(workshop)));
  assert.deepStrictEqual(of(sent.workshop, "settings.write"), [{ set: { "loop.shell_timeout": "3m" } }], "the setting went to workshop");
  assert.strictEqual(of(sent.local, "settings.write").length, 0, "workshop's setting went to the laptop");
  assert.strictEqual(configOf(laptop), laptopConfig, "workshop's setting changed the laptop's config.toml");
  await until("the save bar gone", async () => await page.locator(".save-bar").isHidden());
  assert.strictEqual(await page.inputValue("[id='set-loop.shell_timeout']"), "3m", "workshop's Settings do not show the saved value");
  await page.click("main.settings-page [role=tab]:has-text('Models')");
  await page.waitForSelector("#settings-panel-models");
  assert.strictEqual(await page.locator("#add-provider").count(), 0, "a link can add a provider at workshop");
  // The default model is chosen among workshop's providers, as a task's model
  // is, and saved there with the save bar.
  assert.strictEqual(await page.isDisabled("#default-model-model"), false, "a link cannot choose workshop's default model");
  const current = await page.inputValue("#default-model-model");
  const other = (await page.$$eval("#default-model-model option", (os) => os.map((o) => o.value))).find((v) => v && v !== current);
  assert.ok(other, "workshop offers no other model to choose as its default");
  const tarroBefore = configOf(workshop);
  await page.selectOption("#default-model-model", other);
  await shows(page, ".save-bar", "1 unsaved change");
  await page.click(".save-bar .btn.accent");
  await until("workshop's default model to be saved", () => of(sent.workshop, "settings.control.write").length === 1);
  assert.strictEqual(of(sent.workshop, "settings.control.write")[0].name, other, "workshop's default went to another model");
  assert.strictEqual(of(sent.local, "settings.control.write").length, 0, "workshop's default model went to the laptop");
  await until("workshop's config.toml to change its [control]", () => configOf(workshop) !== tarroBefore);
  assert.strictEqual(configOf(laptop), laptopConfig, "workshop's default model changed the laptop's config.toml");
  step("a link vouched for by the person's passkey changes workshop's settings and default model from this window, and not the laptop's, and cannot choose its approvals");

  // The models of workshop's list are the link's to add, change and remove,
  // among the providers workshop has; the providers themselves are not.
  assert.strictEqual(await page.locator("#provider-shelf .provider-actions button:text-is('Edit'), #provider-shelf .provider-actions button:text-is('Remove')").count(), 0, "a link can change or remove a provider at workshop");
  await page.click("#add-model-shelf");
  assert.strictEqual(await page.inputValue("#model-provider"), "shelf", "Add a model on workshop's provider did not choose it");
  await page.fill("#model-id", "extra");
  await page.click("#model-save");
  await page.waitForSelector("[data-model='shelf-extra']");
  assert.deepStrictEqual(of(sent.workshop, "settings.model.write"), [{ server: "shelf", model: "extra", efforts: [] }], "the model went to workshop");
  assert.strictEqual(of(sent.local, "settings.model.write").length, 0, "workshop's model went to the laptop");
  await until("workshop's config.toml to list the model", () => /\[models\.shelf-extra\]\s*server = "shelf"\s*model = "extra"/.test(configOf(workshop)));
  assert.strictEqual(configOf(laptop), laptopConfig, "workshop's model changed the laptop's config.toml");
  await page.click("[data-model='shelf-extra'] button:text-is('Edit')");
  await page.click("#efforts-lmh");
  await page.click("#model-save");
  await until("workshop's model to be changed", () => of(sent.workshop, "settings.model.write").length === 2);
  assert.strictEqual(of(sent.workshop, "settings.model.write")[1].name, "shelf-extra", "the change named another model");
  assert.deepStrictEqual(of(sent.workshop, "settings.model.write")[1].efforts, ["low", "medium", "high"], "the change did not send the efforts");
  assert.strictEqual(of(sent.local, "settings.model.write").length, 0, "a change to workshop's model went to the laptop");
  await page.waitForFunction(() => document.querySelector("[data-model='shelf-extra']")?.textContent.includes("effort: Low"));
  await shows(page, "[data-model='shelf-extra']", "effort: Low · Medium · High");
  // The default model has no Remove, a link's or not.
  await page.click("#add-model-shelf");
  await page.fill("#model-id", "kept");
  await page.click("#model-save");
  await page.waitForSelector("[data-model='shelf-kept']");
  await page.selectOption("#default-model-model", "shelf-kept");
  await page.click(".save-bar .btn.accent");
  await until("workshop's default to be the new model", () => of(sent.workshop, "settings.control.write").length === 2);
  // The default is kept under [control], so its row is the default's.
  await page.waitForFunction(() => document.querySelector("[data-model='control']")?.textContent.includes("kept"));
  await page.waitForSelector("[data-model='control'] .default-badge");
  assert.strictEqual(await page.locator("[data-model='control'] button:text-is('Remove')").count(), 0, "a link can remove workshop's default model");
  const beforeRemoval = configOf(workshop);
  await page.click("[data-model='shelf-extra'] button:text-is('Remove')");
  assert.strictEqual(of(sent.workshop, "settings.model.remove").length, 0, "Remove removed before it was confirmed");
  await page.click("[data-model='shelf-extra'] button:text-is('Remove')");
  await page.waitForSelector("[data-model='shelf-extra']", { state: "detached" });
  assert.deepStrictEqual(of(sent.workshop, "settings.model.remove"), [{ name: "shelf-extra" }], "the removal went to workshop");
  assert.strictEqual(of(sent.local, "settings.model.remove").length, 0, "workshop's removal went to the laptop");
  await until("workshop's config.toml to drop the model", () => configOf(workshop) !== beforeRemoval && !configOf(workshop).includes("shelf-extra"));
  assert.ok(configOf(workshop).includes('model = "kept"'), "the removal took the default model");
  assert.strictEqual(configOf(laptop), laptopConfig, "workshop's removal changed the laptop's config.toml");
  assert.strictEqual(await page.locator("#add-provider").count(), 0, "a link can add a provider at workshop");
  await page.screenshot({ path: `${out}/workshop-models.png` });
  step("the link adds, changes and removes models among workshop's providers, from this window and on workshop, and not its default model or its providers");

  // workshop's console started with --no-link-settings refuses it: its
  // Settings say so once the link is back, and show no control.
  tarroUI.kill("SIGTERM");
  await tarroUI.exited;
  tarroUI = tarroConsole("--no-link-settings");
  await line(tarroUI, /(Browser view)/, "stderr");
  await page.waitForSelector("#host-settings-refused:has-text('workshop does not let other nodwyns change its settings')", { timeout: 30000 });
  assert.strictEqual(await page.locator("main.settings-page [role=tablist], main.settings-page .settings-panel input, main.settings-page .settings-panel button").count(), 0, "workshop's Settings show controls it refuses");
  assert.strictEqual(of(sent.workshop, "settings.write").length, 1, "a refused link sent workshop a setting");
  await page.screenshot({ path: `${out}/workshop-settings-refused.png` });
  step("workshop started with --no-link-settings refuses the link its settings, and Settings say so");
});
