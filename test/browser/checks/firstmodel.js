// First run of `nodwyn app` on a service that wants a key (ADR 41, amended
// 2026-10-06): no config, no keychain, and a fake Anthropic API at a loopback
// address. The page sets the model up with the forms of Settings › Models, in
// order, and each step is what the daemon was asked for:
//
// - The provider form has the key field and, beside it, the keychain that
//   holds the key, made there: the provider cannot be saved with a key until
//   it exists, and the key typed before is still there once it does.
// - The provider is saved with its key, which is in the keychain and its
//   binding rows (x-api-key), never in config.toml or the page.
// - The model form lists what the provider offers to its key, takes the
//   efforts Claude takes, tests the model without saving it (a test asks for a
//   reply, as the form's Test does in Settings), and saves it as the default
//   at the effort chosen: the New task window then starts on it.
// - Another provider can be started from the model form, for a first one
//   chosen wrongly, and cancelled back to the model without saving anything.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, runtimeDir, launch, watch, requests, shows, until, step, check } = require("../lib");

check(async () => {
  const root = tempDir("nodwyn-firstmodel-");
  for (const d of ["config", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  const chat = start(process.env.DEMO, ["-chat-only"]);
  const [chatURL, goodKey] = await line(chat, /CHAT (\S+) KEY (\S+)/);
  const chatBase = new URL(chatURL).origin;
  // The app reaches nothing beyond this computer: a request to any other host
  // goes to a proxy that is not there, so the Anthropic address the preset
  // fills in is never asked.
  const nowhere = "http://127.0.0.1:9";
  const env = { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir(), HTTPS_PROXY: nowhere, HTTP_PROXY: nowhere, https_proxy: nowhere, http_proxy: nowhere };
  const app = start(process.env.NODWYN, ["app", "--no-open"], { env });
  const [url] = await line(app, /app: open (\S+)/, "stderr");
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1280, height: 1000 } }));
  const sent = requests(page);
  const configFile = `${root}/config/nodwyn/config.toml`;
  const config = () => fs.readFileSync(configFile, "utf8");
  const bindings = () => fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  const passphrase = "family passphrase";

  // The daemon refuses the first keychain.create, as it refuses a passphrase
  // it dislikes; nothing else is touched.
  let refused = false;
  await page.routeWebSocket("**/socket", (socket) => {
    const server = socket.connectToServer();
    socket.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "keychain.create" && !refused) {
        refused = true;
        socket.send(JSON.stringify({ jsonrpc: "2.0", id: frame.id, error: { code: -32602, message: "Use a passphrase of at least 8 characters" } }));
      } else server.send(message);
    });
    server.onMessage((message) => socket.send(message));
  });
  await page.goto(url);
  await page.waitForSelector("#provider-url", { timeout: 10000 });
  await shows(page, "main.home", "Let's connect nodwyn to a model");

  // ---------- the provider, and the keychain its key needs ----------
  await page.click("#preset-anthropic");
  assert.strictEqual(await page.inputValue("#provider-url"), "https://api.anthropic.com/v1");
  assert.strictEqual(await page.locator("#provider-dialect").count(), 0, "Anthropic is offered a dialect");
  await page.fill("#provider-url", chatURL);
  await page.fill("#provider-key", goodKey);
  await page.waitForSelector("#first-keychain #keychain-pass");
  await shows(page, "#first-keychain", "Create the keychain");
  assert.strictEqual(await page.isDisabled("#provider-save"), true, "a key can be saved with no keychain to hold it");
  assert.strictEqual(await page.locator("#provider-form a:has-text('Open the keychain settings')").count(), 0, "setup sends a person to Settings for the keychain");
  await page.screenshot({ path: `${out}/firstmodel-keychain.png` });
  // The minimum is said where a passphrase is chosen, and checked before it is
  // sent; a refusal that does come back leaves both fields as they were typed,
  // so it is put right and sent again rather than typed from the start.
  await shows(page, "#first-keychain", "at least 8 characters");
  assert.strictEqual(await page.getAttribute("#keychain-pass", "minlength"), "8");
  await page.fill("#keychain-pass", passphrase);
  await page.fill("#keychain-again", passphrase);
  await page.click("#first-keychain button:text-is('Create the keychain')");
  await page.waitForSelector("#first-keychain .error:has-text('at least 8 characters')");
  assert.strictEqual(await page.inputValue("#keychain-pass"), passphrase, "a refused passphrase was emptied");
  assert.strictEqual(await page.inputValue("#keychain-again"), passphrase, "the confirmation of a refused passphrase was emptied");
  assert.ok(!fs.existsSync(`${root}/config/nodwyn/keychain`), "a refused keychain was made");
  await page.click("#first-keychain button:text-is('Create the keychain')");
  await page.waitForSelector("#first-keychain", { state: "detached", timeout: 20000 });
  assert.ok(refused, "the first keychain.create was never refused");
  assert.deepStrictEqual(sent.of("keychain.create").at(-1), { passphrase });
  assert.ok(fs.existsSync(`${root}/config/nodwyn/keychain`), "no keychain on disk");
  // The page was drawn again around the keychain being made: what was typed
  // is still in the form, and the key may be saved now.
  assert.strictEqual(await page.inputValue("#provider-key"), goodKey, "the key typed was lost when the keychain was made");
  assert.strictEqual(await page.inputValue("#provider-url"), chatURL, "the address typed was lost when the keychain was made");
  assert.strictEqual(await page.isDisabled("#provider-save"), false, "the provider cannot be saved once the keychain exists");
  step("the keychain was made beside the provider form, and the form kept what was typed");

  await page.click("#provider-save");
  await page.waitForSelector("#model-id", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("settings.provider.write"), [{ provider: "anthropic", url: chatURL, api_key: goodKey }]);
  assert.ok(config().includes(`[servers.127-0-0-1]\nprovider = "anthropic"\nurl = "${chatURL}"\ncredential = "model-127-0-0-1"\n`), "server not written:\n" + config());
  assert.strictEqual(bindings().split('positions = ["header:x-api-key"]').length - 1, 2, "binding rows for x-api-key:\n" + bindings());
  assert.ok(!config().includes(goodKey) && !bindings().includes(goodKey) && !(await page.content()).includes(goodKey), "the key is in a file or in the page");
  await shows(page, ".first-provider", "Anthropic", "with its key stored");
  step("provider saved with its key");

  // ---------- another provider, for a first one chosen wrongly ----------
  await page.click("#first-another");
  await page.waitForSelector("#provider-form");
  assert.strictEqual(await page.locator("#provider-form button:text-is('Cancel')").count(), 1, "a provider form with a provider behind it cannot be cancelled");
  await page.click("#provider-form button:text-is('Cancel')");
  await page.waitForSelector("#model-id");
  assert.strictEqual(sent.of("settings.provider.write").length, 1, "cancelling saved a provider");
  step("another provider can be started, and cancelled back to the model");

  // ---------- the model ----------
  await page.focus("#model-id");
  await page.waitForFunction(() => [...document.querySelectorAll("#model-id-options option")].map((o) => o.value).sort().join() === "fast,reasoner", null, { timeout: 15000 });
  await page.fill("#model-id", "reasoner");
  await page.click("#efforts-lmh");
  await page.selectOption("#model-effort", "high");
  await page.screenshot({ path: `${out}/firstmodel-model.png` });
  await page.click("#model-test");
  await page.waitForSelector("#model-form .test-result.ok", { timeout: 20000 });
  await shows(page, "#model-form .test-result", "reasoner answered");
  assert.strictEqual(sent.of("settings.model.write").length, 0, "testing a model saved it");
  const tested = (await (await fetch(`${chatBase}/_fake/requests`)).json()).at(-1);
  assert.deepStrictEqual([tested.path, tested.key_header, tested.authenticated, tested.answered], ["/v1/messages", "x-api-key", true, 200], "the test request: " + JSON.stringify(tested));
  await page.click("#model-save");
  await page.waitForSelector("#task", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("settings.model.write"), [{ server: "127-0-0-1", model: "reasoner", efforts: ["low", "medium", "high"], effort: "high", settings: { cache_control: "ephemeral" } }]);
  assert.deepStrictEqual(sent.of("settings.control.write"), [{ name: "127-0-0-1-reasoner", effort: "high" }]);
  await until("the default to be written", async () => config().includes('[control]\nserver = "127-0-0-1"\nmodel = "reasoner"\n'));
  assert.ok(!config().includes(goodKey), "the key is in config.toml");
  step("the model tested without being saved, then saved as the default at its effort");

  // The New task window starts on it, at that effort.
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "control");
  assert.strictEqual(await page.inputValue("#new-task-model-effort"), "high", "a new task does not start on the effort chosen");
  await page.screenshot({ path: `${out}/firstmodel-home.png` });
  step("New task starts on the default and its effort");
});
