// First run of `nodwyn app`: no config, no keychain. Set up a model from the
// page with the forms of Settings › Models (a provider, then a model that is
// made the default; a service that wants a key is `firstmodel`), create a
// keychain, switch to another model on the saved server, add
// workspaces (one with a mounted folder, kept in the workspace list and not
// in the folder), attach a file with a secret in it, start
// a task in the folder and answer it; then History stays open through the
// redraws that follow-ups cause, and restores the file it edited, and notes
// being edited keep what was typed through them.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, runtimeDir, launch, watch, requests, shows, until, step, check } = require("../lib");

check(async () => {
  const root = tempDir("nodwyn-first-");
  for (const d of ["config", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  fs.writeFileSync(`${root}/work/hello.txt`, "hello\n");
  const original = 'function sessionRow(session) {\n  const row = node("button", "", "session");\n  row.append(node("span", session.first, "title"));\n  row.append(node("span", session.repo, "meta"));\n  return row;\n}\n';
  fs.writeFileSync(`${root}/work/app.js`, original);
  const model = start(process.env.DEMO, ["-model-only", "-pace=0"]);
  const [modelURL] = await line(model, /MODEL (\S+)/);
  const app = start(process.env.NODWYN, ["app", "--no-open"], { env: { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir() } });
  const [url] = await line(app, /app: open (\S+)/, "stderr");
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1280, height: 860 } }));
  const sent = requests(page);
  // The console's socket goes through here, so that replies to
  // versions.list can be held back while `holding` is set: a History list
  // answered after the panel was redrawn. While `pausing` is set, everything
  // the daemon sends waits in `paused`, so that what it then redraws comes
  // after what the check did meanwhile.
  let holding = false, pausing = false, socket = null;
  const asked = new Set(), held = [], paused = [];
  await page.routeWebSocket(/\/socket$/, (ws) => {
    const server = ws.connectToServer();
    socket = ws;
    ws.onMessage((m) => {
      const frame = JSON.parse(m);
      if (holding && ["versions.list", "versions.diff"].includes(frame.method)) asked.add(frame.id);
      server.send(m);
    });
    server.onMessage((m) => {
      const frame = JSON.parse(m);
      if (!frame.method && asked.has(frame.id)) held.push(m);
      else if (pausing) paused.push(m);
      else ws.send(m);
    });
  });
  await page.goto(url);
  await page.waitForSelector("#provider-url", { timeout: 10000 });
  step("setup shown without a pairing code");
  // It is the provider form of Settings › Models, with its presets, started on
  // Ollama, which asks for no key; there is nothing to cancel back to yet.
  assert.strictEqual(await page.getAttribute("#preset-ollama", "aria-pressed"), "true", "setup does not start on Ollama");
  assert.strictEqual(await page.inputValue("#provider-url"), "http://localhost:11434");
  assert.strictEqual(await page.locator("#provider-key").count(), 0, "Ollama is asked for a key");
  assert.strictEqual(await page.locator("#first-keychain").count(), 0, "a keychain is asked for before a key is");
  for (const preset of ["ollama", "openai", "anthropic", "baseten", "openrouter", "cerebras", "other"]) assert.strictEqual(await page.locator(`#preset-${preset}`).count(), 1, `no ${preset} preset`);
  // Jev answers the judge and carries no model, so the first run does not offer it.
  assert.strictEqual(await page.locator("#preset-jev").count(), 0, "the first run offers the Jev preset");
  assert.strictEqual(await page.locator("[aria-label='Kind of server'] button:text-is('Jev')").count(), 0, "the first run offers the Jev kind");
  assert.strictEqual(await page.locator("#first-model button:text-is('Cancel')").count(), 0, "the first provider form can be cancelled");
  assert.strictEqual(await page.locator("#add-model, #add-provider").count(), 0, "setup shows Settings' own buttons");
  await page.screenshot({ path: `${out}/firstrun-setup.png` });
  // Anthropic takes its own address and a key, kept in a keychain that is made
  // here, beside the form, if there is none. Choosing Ollama back leaves its
  // address as it was and asks for neither.
  await page.click("#preset-anthropic");
  assert.strictEqual(await page.inputValue("#provider-url"), "https://api.anthropic.com/v1");
  assert.strictEqual(await page.isVisible("#provider-key"), true, "Anthropic is not asked for a key");
  assert.strictEqual(await page.isVisible("#first-keychain #keychain-pass"), true, "no keychain to make for the key");
  await page.click("#preset-ollama");
  assert.strictEqual(await page.inputValue("#provider-url"), "http://localhost:11434");
  assert.strictEqual(await page.locator("#provider-key").count(), 0, "Ollama is asked for a key");
  assert.strictEqual(await page.locator("#first-keychain").count(), 0, "Ollama is asked for a keychain");
  await page.fill("#provider-url", modelURL);
  await page.click("#provider-save");
  // The model is the next form, on the provider just saved: the models it
  // lists are offered, and the one chosen is saved and made the default.
  await page.waitForSelector("#model-id", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.provider.write"), [{ provider: "ollama", url: modelURL }]);
  assert.strictEqual(await page.inputValue("#model-provider"), "127-0-0-1", "the saved server is the provider offered");
  assert.strictEqual(await page.locator("#first-another").count(), 1, "no way back to another provider");
  assert.strictEqual(await page.locator("#model-form button:text-is('Cancel')").count(), 0, "the first model form can be cancelled");
  assert.strictEqual(await page.locator("#model-num_ctx").count(), 0, "setup asks for a context length");
  await page.focus("#model-id");
  await page.waitForFunction(() => [...document.querySelectorAll("#model-id-options option")].map((o) => o.value).join() === "demo,fake", null, { timeout: 10000 });
  await page.fill("#model-id", "demo");
  await page.screenshot({ path: `${out}/firstrun-model-setup.png` });
  await page.click("#model-save");
  await page.waitForSelector("#task", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.model.write"), [{ server: "127-0-0-1", model: "demo", efforts: [] }]);
  assert.deepStrictEqual(sent.of("settings.control.write"), [{ name: "127-0-0-1-demo", effort: "" }]);
  const cfg = fs.readFileSync(`${root}/config/nodwyn/config.toml`, "utf8");
  assert.ok(cfg.includes(`[servers.127-0-0-1]\nprovider = "ollama"\nurl = "${modelURL}"\n`) && cfg.includes('[control]\nserver = "127-0-0-1"\nmodel = "demo"\n') && cfg.includes('[models.127-0-0-1-demo]\nserver = "127-0-0-1"\nmodel = "demo"\n'), "config not written: " + cfg);
  step("provider and model saved, the model the default; composer shown");

  // Settings: create a keychain.
  await page.evaluate(() => (location.hash = "#/settings/keys"));
  await page.waitForSelector("#keychain-pass");
  await page.fill("#keychain-pass", "family passphrase");
  await page.fill("#keychain-again", "family passphrase");
  await page.click("text=Create the keychain");
  await page.waitForSelector("text=Unlocked", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("keychain.create"), [{ passphrase: "family passphrase" }]);
  assert.ok(fs.existsSync(`${root}/config/nodwyn/keychain`), "no keychain on disk");
  step("keychain created");
  await page.screenshot({ path: `${out}/firstrun-settings.png`, fullPage: true });

  // Settings › Models: the server saved above is listed as a provider, a
  // model it offers is added from the list it gives, and chosen as the
  // default; the model that was the default stays in the list (ADR 41).
  await page.click("#settings-tab-models");
  await page.waitForSelector("#provider-127-0-0-1");
  await page.click("#add-model");
  assert.strictEqual(await page.inputValue("#model-provider"), "127-0-0-1", "the saved server is the provider offered");
  await page.focus("#model-id");
  await page.waitForFunction(() => [...document.querySelectorAll("#model-id-options option")].map((o) => o.value).join() === "demo,fake", null, { timeout: 10000 });
  await page.fill("#model-id", "fake");
  await page.screenshot({ path: `${out}/firstrun-model.png` });
  await page.click("#model-save");
  await page.waitForSelector("[data-model='127-0-0-1-fake']");
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { server: "127-0-0-1", model: "fake", efforts: [] });
  await page.selectOption("#default-model-model", "127-0-0-1-fake");
  // The bar hides when the save is done and the page is drawn again; leaving
  // before that lets the redraw land over the page the check goes to.
  await page.click(".save-bar button:text-is('Save')");
  await page.waitForSelector(".save-bar", { state: "hidden", timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.control.write")[1], { name: "127-0-0-1-fake", effort: "" });
  const switched = fs.readFileSync(`${root}/config/nodwyn/config.toml`, "utf8");
  assert.ok(switched.includes('[control]\nserver = "127-0-0-1"\nmodel = "fake"\n') && switched.includes('[models.127-0-0-1-demo]\nserver = "127-0-0-1"\nmodel = "demo"\n') && switched.split(modelURL).length === 2, "another model on the saved server: " + switched);
  step("another model on the saved server chosen as the default; the first stays listed");

  // A workspace, added from Home with a folder typed by hand: a name, and
  // no kind to choose (ADR 22).
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#task");
  // Wails installs its bridge after the console has loaded. A late native
  // chooser must become usable and its selected path must reach the form.
  await page.evaluate((folder) => {
    window.nodwynPickFolder = async () => { window.nativePicks = (window.nativePicks || 0) + 1; return folder; };
    window.dispatchEvent(new Event("nodwyn-desktop-ready"));
  }, `${root}/work`);
  await page.click("text=Add a folder");
  if (!(await page.inputValue("#add-path"))) await page.getByRole("button", { name: "Choose…", exact: true }).click();
  assert.strictEqual(await page.inputValue("#add-path"), `${root}/work`, "native selection did not fill the folder");
  assert.strictEqual(await page.evaluate(() => window.nativePicks), 1, "native chooser did not run once");
  // The remaining checks exercise the ordinary typed-path browser flow.
  await page.evaluate(() => { delete window.nodwynPickFolder; });
  await page.fill("#add-name", "Family papers");
  assert.strictEqual(await page.locator("text=What's in it").count(), 0, "the add form still asks what a folder holds");
  await page.click("button:has-text('Add this folder')");
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('Family papers')");
  assert.deepStrictEqual(sent.of("workspace.save"), [{ path: `${root}/work`, name: "Family papers", mounts: [] }]);
  assert.ok(!fs.existsSync(`${root}/work/.nodwyn`), "naming a folder wrote into it");
  step("workspace added from Home");

  // Workspaces: two more added there, one with a mounted folder, and one of
  // them removed. The mount is kept in the workspace list, not the folder.
  for (const d of ["receipts", "letters", "reference"]) fs.mkdirSync(`${root}/${d}`);
  await page.evaluate(() => (location.hash = "#/workspaces"));
  for (const [d, name] of [["receipts", "Receipts"], ["letters", "Letters"]]) {
    await page.click("text=Add a workspace");
    await page.fill("#ws-path", `${root}/${d}`);
    await page.fill("#ws-name", name);
    if (d === "receipts") {
      await page.fill("input[aria-label='Folder to mount']", `${root}/reference`);
      await page.click("form.setup-form button.small:text-is('Add')");
    }
    await page.click("button:has-text('Add this folder')");
    await page.waitForSelector(`.ws-row strong:has-text('${name}')`);
  }
  assert.deepStrictEqual(sent.of("workspace.save")[1], { path: `${root}/receipts`, name: "Receipts", mounts: [{ path: `${root}/reference`, access: "read" }], earlier_tasks: false });
  assert.ok(!fs.existsSync(`${root}/receipts/.nodwyn`), "a mount was written into the folder");
  await page.waitForSelector(".ws-row:has-text('Receipts') >> text=read only");
  await page.click("button[aria-label='Forget Letters']");
  await page.click(".ws-row .end button.danger:not([aria-label])");
  await page.waitForSelector(".ws-row strong:has-text('Letters')", { state: "detached" });
  assert.deepStrictEqual(sent.of("workspace.save").map((w) => w.name), ["Family papers", "Receipts", "Letters"]);
  assert.deepStrictEqual(sent.of("workspace.remove"), [{ path: `${root}/letters` }]);
  assert.ok(fs.statSync(`${root}/letters`).isDirectory(), "forget deleted the folder");
  const registry = fs.readFileSync(`${root}/config/nodwyn/workspaces.toml`, "utf8");
  assert.ok(registry.includes("Family papers") && registry.includes("Receipts") && !registry.includes("Letters"), "workspaces.toml: " + registry);
  assert.ok(registry.includes(`[[workspace.mount]]\npath = "${root}/reference"\naccess = "read"`), "the mount is not in workspaces.toml: " + registry);
  assert.ok(fs.existsSync(`${root}/letters`), "removing a workspace touched its folder");
  step("workspaces added and removed on Workspaces");

  // Searching a folder's earlier tasks is off until ticked in its editor,
  // and saved with its entry in workspaces.toml, never in the folder; Home
  // naming a folder sends no choice, so it never turns it off (ADR 50).
  const receiptsEntry = () => fs.readFileSync(`${root}/config/nodwyn/workspaces.toml`, "utf8").split("[[workspace]]").find((w) => w.includes(`path = "${root}/receipts"`));
  assert.ok(!receiptsEntry().includes("earlier_tasks"), "on before it was turned on: " + receiptsEntry());
  for (const on of [true, false]) {
    await page.click(".ws-row:has-text('Receipts') button:has-text('Edit')");
    assert.strictEqual(await page.isChecked("#ws-earlier"), !on, "the editor does not show the saved choice");
    await page.setChecked("#ws-earlier", on);
    if (on) await page.screenshot({ path: `${out}/firstrun-workspace-editor.png` });
    await page.click("form.setup-form button:has-text('Save')");
    await page.waitForSelector("#ws-earlier", { state: "detached" });
    const last = sent.of("workspace.save").at(-1);
    assert.deepStrictEqual([last.name, last.earlier_tasks], ["Receipts", on]);
    assert.strictEqual(receiptsEntry().includes("earlier_tasks = true"), on, "workspaces.toml: " + receiptsEntry());
    await page.waitForSelector(".ws-row:has-text('Receipts') .ws-earlier", { state: on ? "visible" : "detached" });
  }
  assert.ok(!fs.existsSync(`${root}/receipts/.nodwyn`), "the choice was written into the folder");
  step("earlier tasks turned on and off on Workspaces");
  await page.screenshot({ path: `${out}/firstrun-workspaces.png` });

  // A task in the first workspace, with an attachment holding a token.
  await page.evaluate(() => (location.hash = "#/"));
  await page.waitForSelector("#work-in button[aria-pressed=true]:has-text('Family papers')");
  fs.writeFileSync(`${root}/roadmap.md`, "# Roadmap\nShip cowork.\ntoken: ghp_" + "A1b2C3d4".repeat(5) + "\n");
  const uploads = [];
  page.on("request", (r) => r.method() === "POST" && new URL(r.url()).pathname === "/attach" && uploads.push(Object.fromEntries(new URL(r.url()).searchParams)));
  await page.setInputFiles(".ask input[type=file]", `${root}/roadmap.md`);
  await page.waitForSelector(".ask .attach-chip");
  await page.fill("#task", "The session list shows each session's first message. Make them easy to tell apart.");
  await page.click("button[aria-label='Start the task']");
  // The edit goes ahead in the folder (ADR 35): the two commands ask.
  for (const command of ["Run ls -la", "Run wc -l app.js"]) {
    await page.waitForSelector(`.ask-card h2:has-text('${command}')`, { timeout: 20000 });
    await page.click(".ask-card button[data-key='Y']");
  }
  await page.waitForSelector(".prose strong", { timeout: 20000 });
  const [{ id }] = sent.of("session.send");
  assert.deepStrictEqual(sent.of("session.open"), [{ repo: `${root}/work` }]);
  assert.deepStrictEqual(uploads, [{ id, name: "roadmap.md" }], "the attachment uploaded to the task");
  assert.deepStrictEqual(sent.of("session.answer").map((a) => [a.id, a.reply]), [[id, "y"], [id, "y"]]);
  assert.strictEqual(await page.$(".ask-card"), null, "a third question");
  step("edit went ahead; commands approved; task answered");
  assert.strictEqual(await page.textContent(".turn.user .attach-chip"), "roadmap.mdtext · 1 hidden");
  assert.notStrictEqual(fs.readFileSync(`${root}/work/app.js`, "utf8"), original, "edit not written");
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
  await page.waitForSelector(".task-head .pill:has-text('Idle')", { timeout: 10000 });
  // The Changes panel lives in its tab now; the tab stays chosen while the
  // follow-up's events redraw the side.
  await page.click(".task-aside [role=tab]:has-text('Changes')");
  await page.click(".file-row button:has-text('History')");
  await page.waitForSelector(".versions button:has-text('Restore')");
  let drawn = await page.$(".versions");
  await followUp("Thanks", 1);
  assert.ok(!(await drawn.evaluate((n) => n.isConnected)), "the follow-up did not redraw the Changes panel");
  assert.ok(await page.$(".versions button:has-text('Restore'):enabled"), "History closed when a follow-up redrew the Changes panel");
  step("History stayed open through a follow-up");
  // Closed, it stays closed. Reopened, a reply that comes after a redraw
  // fills the list that is in the page.
  await page.click(".file-row button:has-text('History')");
  assert.strictEqual(await page.$(".versions > *"), null, "History did not close");
  holding = true;
  await page.click(".file-row button:has-text('History')");
  await page.waitForSelector(".versions p:has-text('Reading kept versions')");
  await until("a versions.list reply to hold", () => held.length, 5000);
  drawn = await page.$(".versions");
  await followUp("Thanks again", 2);
  holding = false;
  asked.clear();
  for (const m of held.splice(0)) socket.send(m);
  await page.waitForSelector(".versions button:has-text('Restore'):enabled", { timeout: 5000 }).catch(() => {
    throw new Error("a versions.list reply that came after a redraw did not fill History");
  });
  assert.ok(!(await drawn.evaluate((n) => n.isConnected)), "the second follow-up did not redraw the Changes panel");
  assert.deepStrictEqual(sent.of("session.send").map((s) => s.text), ["The session list shows each session's first message. Make them easy to tell apart.", "Thanks", "Thanks again"]);
  step("a History reply after a redraw filled the list");
  await page.screenshot({ path: `${out}/firstrun-history.png` });

  async function historyVersionComparison() {
    const compare = ".versions button:has-text('Compare with current file')";
    const changed = fs.readFileSync(`${root}/work/app.js`, "utf8");
    await page.click(compare);
    await shows(page, ".versions .change-inspector", "Kept version → current file (read-only comparison)");
    assert.ok(await page.locator(".versions .diff-line.add").count(), "comparison additions missing");
    assert.ok(await page.locator(".versions .diff-line.del").count(), "comparison deletions missing");
    assert.strictEqual(fs.readFileSync(`${root}/work/app.js`, "utf8"), changed, "comparison wrote the file");
    assert.strictEqual(sent.of("versions.diff")[0].path, "app.js");
    holding = true;
    await page.click(compare);
    await until("held comparison reply", () => held.length);
    await page.click(".file-row button:has-text('History')");
    await page.click(".file-row button:has-text('History')");
    await until("held reopened History reply", () => held.length === 2);
    holding = false;
    asked.clear();
    for (const m of held.splice(0)) socket.send(m);
    await page.waitForSelector(compare);
    assert.strictEqual(await page.locator(".versions .diff").count(), 0, "closed History accepted a stale comparison");
    await page.click(compare);
    await shows(page, ".versions .change-inspector", "Kept version → current file");
    await page.screenshot({ path: `${out}/history-comparison.png` });
  }
  await historyVersionComparison();

  // A write in the folder goes ahead without asking (ADR 35), a restore too.
  await page.click(".versions button:has-text('Restore')");
  await until("app.js restored", () => fs.readFileSync(`${root}/work/app.js`, "utf8") === original);
  assert.deepStrictEqual(sent.of("session.restore").map((r) => [r.id, r.path]), [[id, "app.js"]]);
  step("restored without asking");
  await page.screenshot({ path: `${out}/firstrun-restore.png` });

  // Every event redraws the side panel. Notes being edited must come
  // through a follow-up's events, as those that come just after a turn
  // ends, with what was typed, and the field typed in keeps the focus.
  // Notes live with the workspace they describe.
  await page.click(".task-aside [role=tab]:has-text('Workspace')");
  const notesPanel = "section.panel:has(h2:has-text('What nodwyn knows here'))";
  const notes = `${notesPanel} textarea[aria-label='Project notes']`;
  const replies = () => page.evaluate(() => (document.querySelector(".transcript").innerText.match(/Glad that helps/g) || []).length);
  await page.click(`${notesPanel} button:has-text('Edit')`);
  await page.fill(notes, "Tests: node app.test.js");
  const before = await replies();
  pausing = true;
  await page.fill("#reply", "Thanks");
  await page.keyboard.press("Enter");
  await until("the follow-up sent", () => sent.of("session.send").length === 4);
  await page.focus(notes);
  await page.keyboard.press("Control+End");
  const typedIn = await page.$(notes);
  pausing = false;
  for (const m of paused.splice(0)) socket.send(m);
  await until("the follow-up's answer", async () => (await replies()) > before, 20000);
  await page.waitForSelector(".task-head .pill:has-text('Idle')", { timeout: 10000 });
  assert.ok(!(await typedIn.evaluate((n) => n.isConnected)), "the follow-up did not redraw the notes panel");
  assert.strictEqual(await page.inputValue(notes), "Tests: node app.test.js", "notes typed before a follow-up were lost when it redrew the panel");
  await page.keyboard.type(", from the folder");
  assert.strictEqual(await page.inputValue(notes), "Tests: node app.test.js, from the folder", "the redrawn notes field did not keep the focus");
  step("notes being edited kept what was typed, and the focus, through a follow-up");
  await page.screenshot({ path: `${out}/firstrun-notes.png` });
  await page.click(`${notesPanel} button:has-text('Save')`);
  await page.waitForSelector(`${notesPanel} pre:has-text('Tests: node app.test.js, from the folder')`, { timeout: 10000 });
  assert.deepStrictEqual(sent.of("session.notes.write").map((w) => w.text), ["Tests: node app.test.js, from the folder"]);
  step("the notes saved are what was typed");
  // Cancelled, a draft is dropped: Edit starts again from the saved notes.
  await page.click(`${notesPanel} button:has-text('Edit')`);
  await page.fill(notes, "a draft to drop");
  await page.click(`${notesPanel} button:has-text('Cancel')`);
  await page.click(`${notesPanel} button:has-text('Edit')`);
  assert.strictEqual(await page.inputValue(notes), "Tests: node app.test.js, from the folder", "Edit after Cancel did not start from the saved notes");
  await page.click(`${notesPanel} button:has-text('Cancel')`);
  assert.strictEqual(sent.of("session.notes.write").length, 1, "a cancelled draft was saved");
  step("a cancelled draft is dropped");
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
});
