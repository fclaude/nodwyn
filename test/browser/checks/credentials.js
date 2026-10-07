// Credentials from the console: a keychain without a passphrase, a key
// stored and allowed for one host, replaced, the Jev key saved with its
// provider and shown with the providers' keys, its details opened inside
// that panel, the judge put on it and its key's state reported, an OAuth2
// account connected through a fake provider, a credential removed, the
// passphrase set and removed again; and after a restart, a keychain
// without a passphrase is open without anyone unlocking it.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, runtimeDir, launch, watch, requests, check } = require("../lib");
const env = { ...process.env };
const canary = "ghp_canary" + "Q7w3E9r1T5y8".repeat(2);
check(async () => {
  const root = tempDir("nodwyn-cred-");
  for (const d of ["config", "data"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  const model = start(env.DEMO, ["-model-only", "-pace=0"]);
  const [modelURL] = await line(model, /MODEL (\S+)/);
  const provider = start(env.DEMO, ["-oauth-only"]);
  const [authURL, tokenURL, revokeURL] = await line(provider, /OAUTH (\S+) (\S+) (\S+)/);
  fs.mkdirSync(`${root}/config/nodwyn`, { mode: 0o700 });
  fs.writeFileSync(`${root}/config/nodwyn/config.toml`, `[control]\nprovider = "ollama"\nurl = "${modelURL}"\nmodel = "demo"\n`);
  const xdg = { ...env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir() };
  const startApp = async () => {
    const app = start(env.NODWYN, ["app", "--no-open"], { env: xdg });
    const [url] = await line(app, /app: open (\S+)/, "stderr");
    return { app, url };
  };
  const stopApp = async (app) => {
    app.kill("SIGTERM");
    assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  };
  const browser = await launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = watch(await context.newPage());
  const sent = requests(page);
  const step = (m) => console.log("step:", m);
  const go = (hash) => page.evaluate((h) => (location.hash = h), hash);

  // The OpenAI action is refused by a synthetic RPC reply, before it can
  // contact OpenAI; the generic success flow below uses the local OAuth fake.
  const planRequests = [];
  let holdList = false;
  await page.routeWebSocket("**/socket", (socket) => {
    const server = socket.connectToServer();
    socket.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "credentials.authorize" && frame.params.provider === "openai") {
        planRequests.push(frame.params);
        socket.send(JSON.stringify({ jsonrpc: "2.0", id: frame.id, error: { code: -32000, message: "Fake OpenAI consent unavailable" } }));
      } else if (frame.method === "credentials.authorize" && frame.params.provider === "custom") {
        holdList = true;
        server.send(message);
      } else if (frame.method === "credentials.list" && holdList) {
        // The page's first look at the new authorization waits until consent
        // was given, so it finds the authorization already done: the new
        // credential must open all the same, not only when the page saw it wait.
        holdList = false;
        setTimeout(() => server.send(message), 2000);
      } else server.send(message);
    });
    server.onMessage((message) => socket.send(message));
  });
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
  assert.deepStrictEqual(sent.of("keychain.create"), [{ no_passphrase: true }]);
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
  assert.deepStrictEqual(sent.of("credentials.add"), [{ class: "github-token", value: canary, dest: "api.github.com", methods: ["GET", "POST"], position: "header:Authorization:Bearer" }]);
  const bindings = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  assert.ok(bindings.includes('class = "github-token"') && bindings.includes('dest = "api.github.com"'), "bindings.toml: " + bindings);
  step("key stored and allowed for api.github.com");
  await page.screenshot({ path: `${out}/credentials-key.png`, fullPage: true });
  await page.fill(".cred-detail input[type=password]", canary + "-rotated");
  await page.click(".cred-detail button:has-text('Replace')");
  await page.waitForSelector("text=has its new value", { timeout: 10000 });
  const [replaced] = sent.of("credentials.replace");
  assert.deepStrictEqual(sent.of("credentials.replace"), [{ id: replaced.id, value: canary + "-rotated" }]);
  step("key replaced");

  // The judge's key is a provider's (ADR 46): the Jev key is saved with its
  // provider in Settings › Models and listed with the providers' keys, not
  // in a panel of its own, allowed for typesafe.ai by POST alone. Its
  // details open inside that panel at once; any shadowing of the detail
  // renderer blanks the whole tab, so this step guards the panel's wiring
  // too.
  await go("#/settings");
  await page.click("#add-provider");
  await page.click("#preset-jev");
  await page.fill("#provider-name", "jev");
  await page.fill("#provider-key", "sk-judge-canary-0001");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-jev", { timeout: 10000 });
  await go("#/connections/credentials");
  await page.waitForSelector("#model-keys .cred-row:has-text('model-api-typesafe-ai')", { timeout: 10000 });
  if (await page.locator("#judge").count()) throw new Error("the Judge subpanel is still shown");
  await page.click("#model-keys .cred-row:has-text('model-api-typesafe-ai')");
  await page.waitForSelector("#model-keys .cred-detail td:has-text('api.typesafe.ai')", { timeout: 10000 });
  const jevDetail = await page.innerText("#model-keys .cred-detail");
  assert.ok(jevDetail.includes("POST") && !jevDetail.includes("GET"), "the Jev key is allowed for more than POST:\n" + jevDetail);
  step("the Jev key is saved with its provider and shown with the providers' keys, details inside that panel");
  // And through its row: closing and reopening must keep the tab alive too.
  await page.click("#model-keys .cred-row:has-text('model-api-typesafe-ai')");
  await page.waitForSelector("#model-keys .cred-detail", { state: "detached", timeout: 10000 });
  await page.click("#model-keys .cred-row:has-text('model-api-typesafe-ai')");
  await page.waitForSelector("#model-keys .cred-detail td", { timeout: 10000 });
  step("the Jev key's row reopens its details inside its panel");
  await page.screenshot({ path: `${out}/credentials-judge.png`, fullPage: true });

  // Settings › Judge: put on the Jev provider, the judge's key is ready.
  await go("#/settings");
  await page.waitForSelector("#section-judge >> text=Off: no provider is chosen", { timeout: 10000 });
  await page.click("#section-judge [id='set-judge.server-jev']");
  await page.click(".save-bar button:text-is('Save')");
  await page.waitForSelector("#section-judge >> text=Key ready: jev's key is stored, unlocked and allowed for api.typesafe.ai.", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "judge.server": "jev" } });
  step("settings reports the judge's key ready once the judge is on its provider");
  // A key stored but not allowed for the judge's host is not ready: the
  // guard would refuse the judge's first request. Settings are read once
  // per connection, so the page is reloaded to see the file's change.
  const bindingsFile = `${root}/config/nodwyn/bindings.toml`;
  const allBindings = fs.readFileSync(bindingsFile, "utf8");
  fs.writeFileSync(bindingsFile, allBindings.split("[[bind]]").filter((b) => !b.includes('class = "model-api-typesafe-ai"')).join("[[bind]]"));
  await page.reload();
  await page.waitForSelector("#section-judge >> text=Not available: The key is stored but not allowed for api.typesafe.ai", { timeout: 10000 });
  fs.writeFileSync(bindingsFile, allBindings);
  step("settings says a judge key not allowed for the judge's host is not available");

  // Back to Credentials for the OAuth2 account: the check left the page on
  // Settings, where there is no Connect button. The Jev key is now the
  // judge's too.
  await go("#/connections/credentials");
  await page.waitForSelector("#model-keys .cred-row:has-text('model-api-typesafe-ai') >> text=the judge", { timeout: 10000 });

  // Connect an account through the fake provider: the console opens its
  // consent page, the provider sends the browser back to nodwyn, and the
  // page shows the new credential.
  await page.click("button:has-text('Connect an account')");
  await page.click(".card button.chip:text-is('openai')");
  assert.strictEqual(await page.getAttribute("#oauth-client", "required"), null);
  assert.ok(await page.isDisabled("#oauth-secret"));
  await page.click("button:text-is('Continue with ChatGPT')");
  await page.waitForSelector(".setup-form .error:has-text('Fake OpenAI consent unavailable')");
  assert.deepStrictEqual(planRequests, [{ provider: "openai", client_id: "" }]);
  step("ChatGPT sign-in sends a request without a client id and shows a refusal");
  await page.screenshot({ path: `${out}/credentials-chatgpt.png`, fullPage: true });
  await page.click(".card button.chip:has-text('custom')");
  assert.strictEqual(await page.getAttribute("#oauth-client", "required"), "");
  assert.ok(await page.isEnabled("#oauth-secret"));
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
  assert.deepStrictEqual(sent.of("credentials.authorize"), [{ provider: "custom", client_id: "demo-client", class: "drive", scopes: ["files.read"], auth_url: authURL, token_url: tokenURL, revoke_url: revokeURL }]);
  step("OAuth2 account connected");
  if ((await page.inputValue(".cred-detail details[open] input.input")) !== "") throw new Error("a custom provider has no API host to suggest");
  await page.fill(".cred-detail details[open] input.input", "api.example.com");
  await page.click(".cred-detail button:has-text('Allow')");
  await page.waitForSelector(".cred-detail td:has-text('files.read')", { timeout: 10000 });
  const [bound] = sent.of("credentials.bind");
  assert.deepStrictEqual(sent.of("credentials.bind"), [{ id: bound.id, dest: "api.example.com", methods: ["GET", "POST"], position: "header:Authorization:Bearer" }]);
  step("account allowed for a host, with its scopes");
  await page.screenshot({ path: `${out}/credentials-oauth.png`, fullPage: true });

  // Remove the key.
  await page.click(".cred-row:has-text('github-token')");
  await page.click(".cred-detail button:has-text('Remove…')");
  await page.click(".cred-detail .callout button:text-is('Remove')");
  await page.waitForSelector(".cred-row:has-text('github-token')", { state: "detached", timeout: 10000 });
  assert.deepStrictEqual(sent.of("credentials.remove"), [{ id: replaced.id, revoke: false }], "the key replaced is the key removed");
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

  // Hold the last startup reply so typing straddles the final Home redraw.
  // A person may have already selected the password before startup settles.
  await stopApp(app);
  ({ app, url } = await startApp());
  let releaseStartup;
  const heldStartup = new Promise((resolve) => {
    releaseStartup = resolve;
  });
  await page.routeWebSocket("**/socket", (socket) => {
    const server = socket.connectToServer();
    let watchID;
    socket.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "session.watch") watchID = frame.id;
      server.send(message);
    });
    server.onMessage(async (message) => {
      if (JSON.parse(message).id === watchID && watchID !== undefined) await heldStartup;
      socket.send(message);
    });
  });
  await page.goto(url);
  await page.waitForSelector("#task");
  const initiallyFocused = await page.evaluate(() => document.activeElement?.id === "keychain-pass");
  if (!(await page.isVisible("#keychain-pass"))) await page.click("details:has(#keychain-pass) > summary");
  await page.focus("#keychain-pass");
  await page.keyboard.type("family ");
  releaseStartup();
  await page.waitForSelector("html[data-ready]");
  assert.strictEqual(await page.evaluate(() => document.activeElement?.id), "keychain-pass", "startup moved password focus to chat");
  await page.keyboard.type("passphrase");
  assert.strictEqual(await page.inputValue("#keychain-pass"), "family passphrase", "startup dropped part of the password");
  assert.strictEqual(await page.inputValue("#task"), "", "password text reached chat");
  assert.ok(initiallyFocused, "startup did not focus the password field");
  await page.keyboard.press("Control+n");
  await page.waitForFunction(() => document.activeElement?.id === "keychain-pass");
  await page.fill("#keychain-pass", "wrong passphrase");
  await page.click("button:text-is('Unlock')");
  await page.waitForFunction(() => document.querySelector(".setup-form .error")?.textContent);
  await page.keyboard.type("family passphrase");
  assert.strictEqual(await page.inputValue("#keychain-pass"), "family passphrase", "retry typing left the password field");
  await page.keyboard.press("Enter");
  await page.waitForSelector("#keychain-pass", { state: "detached" });
  assert.deepStrictEqual(sent.of("keychain.unlock"), [{ passphrase: "wrong passphrase" }, { passphrase: "family passphrase" }]);
  assert.strictEqual(await page.inputValue("#task"), "");
  step("startup and retry keep password typing out of chat");
  await go("#/settings");
  await page.waitForSelector("summary:has-text('Change or remove the passphrase')");
  await page.click("summary:has-text('Change or remove the passphrase')");
  await page.click("button:has-text('Remove the passphrase')");
  await page.waitForSelector("text=Enter the current passphrase");
  await page.fill("#keychain-current", "family passphrase");
  await page.click("button:has-text('Remove the passphrase')");
  await page.waitForSelector("text=· no passphrase", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("keychain.passphrase"), [{ passphrase: "family passphrase" }, { current: "family passphrase", no_passphrase: true }]);
  step("passphrase removed with the current one");

  // Restarted, the keychain without a passphrase is open by itself.
  await stopApp(app);
  ({ app, url } = await startApp());
  if (!app.stderrText.includes("the keychain has no passphrase; opened it")) throw new Error("app log: " + app.stderrText);
  await page.goto(url);
  await page.waitForSelector("#task", { timeout: 10000 });
  await go("#/connections/credentials");
  await page.waitForSelector(".cred-row:has-text('drive')", { timeout: 10000 });
  step("restarted: open without unlocking");
  // At phone width, from a page opened there.
  const phone = watch(await context.newPage(), "phone");
  await phone.setViewportSize({ width: 390, height: 844 });
  await phone.goto(url.split("#")[0] + "#/connections/credentials");
  await phone.waitForSelector(".cred-row:has-text('drive')", { timeout: 10000 });
  await phone.click(".cred-row:has-text('drive')");
  await phone.waitForSelector(".cred-detail td");
  await phone.screenshot({ path: `${out}/credentials-phone.png`, fullPage: true });
  const overflow = await phone.evaluate(() => [document.documentElement, document.querySelector(".page")].map((n) => n.scrollWidth - n.clientWidth));
  if (overflow.some((px) => px > 0)) {
    const wide = await phone.evaluate(() => [...document.querySelectorAll(".page *")].filter((n) => n.getBoundingClientRect().right > window.innerWidth + 1 && ![...n.children].some((c) => c.getBoundingClientRect().right > window.innerWidth + 1)).slice(0, 8).map((n) => `${n.tagName.toLowerCase()}.${n.className} "${n.textContent.slice(0, 40)}"`));
    throw new Error(`the page scrolls sideways at phone width (${overflow}): ${wide.join("; ")}`);
  }
  await phone.close();
  await stopApp(app);
});
