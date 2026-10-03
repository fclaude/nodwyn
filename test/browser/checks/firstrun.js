// First run of `nodwyn app`: no config, no keychain. Set up a model from the
// page, create a keychain, start a task in a folder, and answer it.
const { chromium } = require("playwright");
const { spawn } = require("child_process");
const fs = require("fs");
const out = process.argv[2];
const env = { ...process.env };
(async () => {
  const root = fs.mkdtempSync("/tmp/nodwyn-first-");
  for (const d of ["config", "data", "run", "work"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  fs.writeFileSync(`${root}/work/hello.txt`, "hello\n");
  const original = 'function sessionRow(session) {\n  const row = node("button", "", "session");\n  row.append(node("span", session.first, "title"));\n  row.append(node("span", session.repo, "meta"));\n  return row;\n}\n';
  fs.writeFileSync(`${root}/work/app.js`, original);
  const kids = [];
  process.on("exit", () => kids.forEach((k) => k.kill("SIGKILL")));
  const line = (proc, re, stream = "stdout") => new Promise((resolve) => { let b = ""; proc[stream].on("data", (d) => { b += d; const m = re.exec(b); if (m) resolve(m[1]); }); });
  const model = spawn(env.DEMO, ["-model-only"]); kids.push(model);
  const modelURL = await line(model, /MODEL (\S+)/);
  const app = spawn(env.NODWYN, ["app", "--no-open"], { env: { ...env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: `${root}/run` } }); kids.push(app);
  let log = ""; app.stderr.on("data", (d) => (log += d));
  const url = await line(app, /app: open (\S+)/, "stderr");
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  const step = (m) => console.log("step:", m);
  // The console's socket goes through here, so that replies to
  // versions.list can be held back while `holding` is set: a History list
  // answered after the panel was redrawn.
  let holding = false, socket = null;
  const asked = new Set(), held = [];
  await page.routeWebSocket(/\/socket$/, (ws) => {
    const server = ws.connectToServer();
    socket = ws;
    ws.onMessage((m) => {
      const frame = JSON.parse(m);
      if (holding && frame.method === "versions.list") asked.add(frame.id);
      server.send(m);
    });
    server.onMessage((m) => {
      const frame = JSON.parse(m);
      if (!frame.method && asked.has(frame.id)) held.push(m);
      else ws.send(m);
    });
  });
  await page.goto(url);
  await page.waitForSelector("#model-url", { timeout: 10000 });
  step("setup shown without a pairing code");
  await page.screenshot({ path: `${out}/firstrun-setup.png` });
  await page.fill("#model-url", modelURL);
  await page.fill("#model-name", "demo");
  await page.click("text=Save and check");
  await page.waitForSelector("#task", { timeout: 10000 });
  step("model saved; composer shown");
  const cfg = fs.readFileSync(`${root}/config/nodwyn/config.toml`, "utf8");
  if (!cfg.includes(`[servers.127-0-0-1]\nprovider = "ollama"\nurl = "${modelURL}"\n`) || !cfg.includes('[control]\nserver = "127-0-0-1"\nmodel = "demo"\n')) throw new Error("config not written: " + cfg);
  // Settings: create a keychain.
  await page.evaluate(() => (location.hash = "#/settings"));
  await page.waitForSelector("#keychain-pass");
  await page.fill("#keychain-pass", "family passphrase");
  await page.fill("#keychain-again", "family passphrase");
  await page.click("text=Create the keychain");
  await page.waitForSelector("text=Unlocked", { timeout: 20000 });
  step("keychain created");
  await page.screenshot({ path: `${out}/firstrun-settings.png`, fullPage: true });
  // Settings › Models: the server saved above is chosen by its name, its
  // models are what it lists, and another of them is saved by the name.
  await page.click("summary:has-text('Change the main model')");
  await page.waitForSelector("button.chip[aria-pressed=true]:has-text('127-0-0-1')");
  if (await page.isVisible("#model-url")) throw new Error("a saved server asks for its address again");
  await page.focus("#model-name");
  await page.waitForFunction(() => [...document.querySelectorAll("#model-options option")].map((o) => o.value).join() === "demo,fake", null, { timeout: 10000 });
  console.log("offered:", await page.textContent("#model-offered"));
  await page.fill("#model-name", "fake");
  await page.screenshot({ path: `${out}/firstrun-model.png` });
  await page.click("text=Save and check");
  await page.waitForSelector("text=Saved. The model answers.", { timeout: 10000 });
  const switched = fs.readFileSync(`${root}/config/nodwyn/config.toml`, "utf8");
  if (!switched.includes('[control]\nserver = "127-0-0-1"\nmodel = "fake"\n') || switched.split(modelURL).length !== 2) throw new Error("another model on the server: " + switched);
  step("another model on the saved server chosen by name");
  // Start a task in a folder typed by hand.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  await page.click("text=Add a folder");
  await page.fill("#add-path", `${root}/work`);
  await page.fill("#add-name", "Family papers");
  await page.click("button:has-text('Documents')");
  await page.click("button:has-text('Add this folder')");
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('Family papers')");
  step("documents workspace added");
  if (!fs.readFileSync(`${root}/work/.nodwyn/workspace.toml`, "utf8").includes('kind = "docs"')) throw new Error("workspace.toml");
  // Workspaces: two more added there, and one of them removed.
  for (const d of ["receipts", "letters"]) fs.mkdirSync(`${root}/${d}`);
  await page.evaluate(() => (location.hash = "#/workspaces"));
  for (const [d, name] of [["receipts", "Receipts"], ["letters", "Letters"]]) {
    await page.click("text=Add a workspace");
    await page.fill("#ws-path", `${root}/${d}`);
    await page.fill("#ws-name", name);
    await page.click("button:has-text('Add this folder')");
    await page.waitForSelector(`.ws-row strong:has-text('${name}')`);
  }
  await page.click("button[aria-label='Remove Letters']");
  await page.click(".ws-row .end button.danger:not([aria-label])");
  await page.waitForSelector(".ws-row strong:has-text('Letters')", { state: "detached" });
  const registry = fs.readFileSync(`${root}/config/nodwyn/workspaces.toml`, "utf8");
  if (!registry.includes("Family papers") || !registry.includes("Receipts") || registry.includes("Letters")) throw new Error("workspaces.toml: " + registry);
  if (!fs.existsSync(`${root}/letters`)) throw new Error("removing a workspace touched its folder");
  step("workspaces added and removed on Workspaces");
  await page.screenshot({ path: `${out}/firstrun-workspaces.png` });
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('Family papers')");
  fs.writeFileSync(`${root}/roadmap.md`, "# Roadmap\nShip cowork.\ntoken: ghp_" + "A1b2C3d4".repeat(5) + "\n");
  await page.setInputFiles(".ask input[type=file]", `${root}/roadmap.md`);
  await page.waitForSelector(".ask .attach-chip");
  await page.fill("#task", "The session list shows each session's first message. Make them easy to tell apart.");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".ask-card", { timeout: 20000 });
  await page.keyboard.press("y");
  for (let i = 0; i < 40 && !(await page.$(".prose strong")); i++) {
    if (await page.$(".ask-card")) await page.keyboard.press("y");
    await page.waitForTimeout(400);
  }
  step("edit approved; task answered");
  console.log("attachment chip:", await page.textContent(".turn.user .attach-chip"));
  if (fs.readFileSync(`${root}/work/app.js`, "utf8") === original) throw new Error("edit not written");
  await page.screenshot({ path: `${out}/firstrun-task.png` });
  // Every event redraws the side panel. A follow-up's events, as those that
  // come just after a turn ends, must leave an open History list open, and
  // its Restore enabled once the turn is over (a run refuses a restore).
  const followUp = async (text, replies) => {
    await page.fill("#reply", text);
    await page.keyboard.press("Enter");
    await page.waitForFunction((n) => (document.querySelector(".transcript").innerText.match(/Glad that helps/g) || []).length >= n, replies, { timeout: 20000 });
    await page.waitForSelector(".task-head .pill:has-text('Idle')", { timeout: 10000 });
  };
  await page.click(".file-row button:has-text('History')");
  await page.waitForSelector(".versions button:has-text('Restore')");
  let drawn = await page.$(".versions");
  await followUp("Thanks", 1);
  if (await drawn.evaluate((n) => n.isConnected)) throw new Error("the follow-up did not redraw the Changes panel");
  if (!(await page.$(".versions button:has-text('Restore'):enabled"))) throw new Error("History closed when a follow-up redrew the Changes panel");
  step("History stayed open through a follow-up");
  // Closed, it stays closed. Reopened, a reply that comes after a redraw
  // fills the list that is in the page.
  await page.click(".file-row button:has-text('History')");
  if (await page.$(".versions > *")) throw new Error("History did not close");
  holding = true;
  await page.click(".file-row button:has-text('History')");
  await page.waitForSelector(".versions p:has-text('Reading kept versions')");
  for (let i = 0; i < 50 && !held.length; i++) await page.waitForTimeout(100);
  if (!held.length) throw new Error("no versions.list reply to hold");
  drawn = await page.$(".versions");
  await followUp("Thanks again", 2);
  holding = false;
  asked.clear();
  for (const m of held.splice(0)) socket.send(m);
  await page.waitForSelector(".versions button:has-text('Restore'):enabled", { timeout: 5000 }).catch(() => {
    throw new Error("a versions.list reply that came after a redraw did not fill History");
  });
  if (await drawn.evaluate((n) => n.isConnected)) throw new Error("the second follow-up did not redraw the Changes panel");
  step("a History reply after a redraw filled the list");
  await page.screenshot({ path: `${out}/firstrun-history.png` });
  // A write in the folder goes ahead without asking (ADR 35), a restore too.
  await page.click(".versions button:has-text('Restore')");
  for (let i = 0; i < 25 && fs.readFileSync(`${root}/work/app.js`, "utf8") !== original; i++) await page.waitForTimeout(200);
  step("restored without asking");
  await page.screenshot({ path: `${out}/firstrun-restore.png` });
  console.log("restored:", fs.readFileSync(`${root}/work/app.js`, "utf8") === original);
  console.log(errors.length ? "ERRORS\n" + errors.join("\n") : "no page errors");
  await browser.close();
  app.kill("SIGTERM");
  await new Promise((r) => app.on("exit", r));
  console.log("app exited; log tail:", log.split("\n").slice(-4).join(" | "));
  process.exit(0); // the exit handler kills the scripted model, which would keep node running
})().catch((e) => { console.error("FAILED", e.message); process.exit(1); });
