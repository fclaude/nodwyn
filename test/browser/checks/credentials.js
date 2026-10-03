// Credentials from the console: a keychain without a passphrase, a key
// stored and allowed for one host, replaced, the judge key stored through
// its own panel with its details opened inside that panel, an OAuth2
// account connected through a fake provider, a credential removed, the
// passphrase set and removed again; and after a restart, a keychain
// without a passphrase is open without anyone unlocking it.
const { chromium } = require("playwright");
const { spawn } = require("child_process");
const fs = require("fs");
const out = process.argv[2];
const env = { ...process.env };
const canary = "ghp_canary" + "Q7w3E9r1T5y8".repeat(2);
(async () => {
  const root = fs.mkdtempSync("/tmp/nodwyn-cred-");
  for (const d of ["config", "data", "run"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  const kids = [];
  process.on("exit", () => kids.forEach((k) => k.kill("SIGKILL")));
  const line = (proc, re, stream = "stdout") => new Promise((resolve) => { let b = ""; proc[stream].on("data", (d) => { b += d; const m = re.exec(b); if (m) resolve(m.slice(1)); }); });
  const model = spawn(env.DEMO, ["-model-only"]); kids.push(model);
  const [modelURL] = await line(model, /MODEL (\S+)/);
  const provider = spawn(env.DEMO, ["-oauth-only"]); kids.push(provider);
  const [authURL, tokenURL, revokeURL] = await line(provider, /OAUTH (\S+) (\S+) (\S+)/);
  fs.mkdirSync(`${root}/config/nodwyn`, { mode: 0o700 });
  fs.writeFileSync(`${root}/config/nodwyn/config.toml`, `[control]\nprovider = "ollama"\nurl = "${modelURL}"\nmodel = "demo"\n`);
  const xdg = { ...env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: `${root}/run` };
  const startApp = async () => {
    const app = spawn(env.NODWYN, ["app", "--no-open"], { env: xdg }); kids.push(app);
    app.log = ""; app.stderr.on("data", (d) => (app.log += d));
    const [url] = await line(app, /app: open (\S+)/, "stderr");
    return { app, url };
  };
  const stopApp = async (app) => { app.kill("SIGTERM"); await new Promise((r) => app.on("exit", r)); };
  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  const step = (m) => console.log("step:", m);
  const go = (hash) => page.evaluate((h) => (location.hash = h), hash);

  let { app, url } = await startApp();
  await page.goto(url);
  await page.waitForSelector("#task", { timeout: 10000 });
  // Settings: a keychain without a passphrase.
  await go("#/settings");
  await page.waitForSelector("#keychain-none");
  await page.check("#keychain-none");
  if (await page.isVisible("#keychain-pass")) throw new Error("passphrase still asked for");
  await page.click("text=Create the keychain");
  await page.waitForSelector("text=no passphrase", { timeout: 10000 });
  step("keychain created without a passphrase");
  await page.screenshot({ path: `${out}/credentials-settings.png`, fullPage: true });

  // Connections › Credentials: store a key, allowed for one host.
  await go("#/connections/credentials");
  await page.waitForSelector("text=The keychain has no passphrase");
  await page.click("button:has-text('Add a key')");
  await page.fill("#cred-class", "github-token");
  await page.fill("#cred-value", canary);
  await page.fill("#cred-dest", "api.github.com");
  await page.click("button:has-text('Store the key')");
  await page.waitForSelector(".cred-detail td:has-text('api.github.com')", { timeout: 10000 });
  step("key stored and allowed for api.github.com");
  const bindings = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  if (!bindings.includes('class = "github-token"') || !bindings.includes('dest = "api.github.com"')) throw new Error("bindings.toml: " + bindings);
  await page.screenshot({ path: `${out}/credentials-key.png`, fullPage: true });
  await page.fill(".cred-detail input[type=password]", canary + "-rotated");
  await page.click(".cred-detail button:has-text('Replace')");
  await page.waitForSelector("text=has its new value", { timeout: 10000 });
  step("key replaced");

  // The Judge panel: store the judge key through its own field. Its details
  // open inside that panel at once; any shadowing of the detail renderer
  // blanks the whole tab, so this step guards the panel's wiring too.
  await page.fill("#judge #jev-value", "sk-judge-canary-0001");
  await page.click("#judge button:has-text('Store the judge key')");
  await page.waitForSelector("#judge .cred-row:has-text('jev-key')", { timeout: 10000 });
  await page.waitForSelector("#judge .cred-detail td", { timeout: 10000 });
  if (!(await page.isVisible("#judge .cred-detail"))) throw new Error("the judge key's details opened outside its panel");
  step("judge key stored through its own panel, details inside it");
  // And through its row: closing and reopening must keep the tab alive too.
  await page.click("#judge .cred-row:has-text('jev-key')");
  await page.waitForSelector("#judge .cred-detail", { state: "detached", timeout: 10000 });
  await page.click("#judge .cred-row:has-text('jev-key')");
  await page.waitForSelector("#judge .cred-detail td", { timeout: 10000 });
  step("judge key's row reopens its details inside its panel");
  await page.screenshot({ path: `${out}/credentials-judge.png`, fullPage: true });

  // Settings › Judge: with the key stored, the Key row says so and offers
  // no second add, even though the judge itself is off in the demo.
  await go("#/settings");
  await page.waitForSelector("text=Stored in the keychain.", { timeout: 10000 });
  if (await page.isVisible("a:has-text('Judge key')")) throw new Error("Settings still offers to add a stored judge key");
  step("settings reports the stored judge key");

  // Back to Credentials for the OAuth2 account: the check left the page on
  // Settings, where there is no Connect button.
  await go("#/connections/credentials");
  await page.waitForSelector("#judge .cred-row:has-text('jev-key')", { timeout: 10000 });

  // Connect an account through the fake provider: the console opens its
  // consent page, the provider sends the browser back to nodwyn, and the
  // page shows the new credential.
  await page.click("button:has-text('Connect an account')");
  await page.click(".card button.chip:has-text('custom')");
  await page.fill("#oauth-auth", authURL);
  await page.fill("#oauth-token", tokenURL);
  await page.fill("#oauth-revoke", revokeURL);
  await page.fill("#oauth-client", "demo-client");
  await page.fill("#oauth-scopes", "files.read");
  await page.fill("#oauth-class", "drive");
  const consent = context.waitForEvent("page");
  await page.click("button:has-text('Continue to the provider')");
  const tab = await consent;
  await tab.waitForLoadState();
  await tab.waitForSelector("text=nodwyn has the authorization", { timeout: 10000 });
  await tab.close();
  await page.waitForSelector(".cred-detail strong:has-text('drive')", { timeout: 10000 });
  await page.waitForSelector(".cred-detail >> text=refreshed before use");
  step("OAuth2 account connected");
  if ((await page.inputValue(".cred-detail details[open] input.input")) !== "") throw new Error("a custom provider has no API host to suggest");
  await page.fill(".cred-detail details[open] input.input", "api.example.com");
  await page.click(".cred-detail button:has-text('Allow')");
  await page.waitForSelector(".cred-detail td:has-text('files.read')", { timeout: 10000 });
  step("account allowed for a host, with its scopes");
  await page.screenshot({ path: `${out}/credentials-oauth.png`, fullPage: true });

  // Remove the key.
  await page.click(".cred-row:has-text('github-token')");
  await page.click(".cred-detail button:has-text('Remove…')");
  await page.click(".cred-detail .callout button:text-is('Remove')");
  await page.waitForSelector(".cred-row:has-text('github-token')", { state: "detached", timeout: 10000 });
  step("key removed");
  const html = await page.content();
  if (html.includes("canary")) throw new Error("a key value reached the page");

  // A passphrase, then none again.
  await go("#/settings");
  await page.waitForSelector("#keychain-new");
  await page.fill("#keychain-new", "family passphrase");
  await page.fill("#keychain-new-again", "family passphrase");
  await page.click("button:has-text('Set the passphrase')");
  await page.waitForSelector("text=protected by a passphrase", { timeout: 10000 });
  step("passphrase set");
  await page.click("summary:has-text('Change or remove the passphrase')");
  await page.click("button:has-text('Remove the passphrase')");
  await page.waitForSelector("text=Enter the current passphrase");
  await page.fill("#keychain-current", "family passphrase");
  await page.click("button:has-text('Remove the passphrase')");
  await page.waitForSelector("text=· no passphrase", { timeout: 10000 });
  step("passphrase removed with the current one");

  // Restarted, the keychain without a passphrase is open by itself.
  await stopApp(app);
  ({ app, url } = await startApp());
  if (!app.log.includes("the keychain has no passphrase; opened it")) throw new Error("app log: " + app.log);
  await page.goto(url);
  await page.waitForSelector("#task", { timeout: 10000 });
  await go("#/connections/credentials");
  await page.waitForSelector(".cred-row:has-text('drive')", { timeout: 10000 });
  step("restarted: open without unlocking");
  // At phone width, from a page opened there.
  const phone = await context.newPage();
  phone.on("pageerror", (e) => errors.push(e.message));
  await phone.setViewportSize({ width: 390, height: 844 });
  await phone.goto(url.split("#")[0] + "#/connections/credentials");
  await phone.waitForSelector(".cred-row:has-text('drive')", { timeout: 10000 });
  await phone.click(".cred-row:has-text('drive')");
  await phone.waitForSelector(".cred-detail");
  await phone.waitForTimeout(300);
  await phone.screenshot({ path: `${out}/credentials-phone.png`, fullPage: true });
  const overflow = await phone.evaluate(() => [document.documentElement, document.querySelector(".page")].map((n) => n.scrollWidth - n.clientWidth));
  if (overflow.some((px) => px > 0)) {
    const wide = await phone.evaluate(() => [...document.querySelectorAll(".page *")].filter((n) => n.getBoundingClientRect().right > window.innerWidth + 1 && ![...n.children].some((c) => c.getBoundingClientRect().right > window.innerWidth + 1)).slice(0, 8).map((n) => `${n.tagName.toLowerCase()}.${n.className} "${n.textContent.slice(0, 40)}"`));
    throw new Error(`the page scrolls sideways at phone width (${overflow}): ${wide.join("; ")}`);
  }
  await phone.close();

  console.log(errors.length ? "ERRORS\n" + errors.join("\n") : "no page errors");
  await browser.close();
  await stopApp(app);
  fs.rmSync(root, { recursive: true, force: true });
  process.exit(errors.length ? 1 : 0); // the exit handler kills the model and the provider, which would keep node running
})().catch((e) => { console.error("FAILED", e.message); process.exit(1); });
