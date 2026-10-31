// Model providers, models and efforts (ADR 41), from the console and against
// two fake providers: an Ollama that takes no key, and a chat-completions
// server that wants one, lists two models (one takes an effort, one does not)
// and refuses what a real one refuses. Starting from a config.toml that names
// one inline Ollama model, as every config did before servers:
//
// - Settings › Models adds the second provider with a wrong key; a test
//   request says the server did not accept it, the key is replaced, and the
//   test passes. The key is in the keychain and bindings.toml's rows, never in
//   config.toml or the page. The provider lists its models only to its key,
//   as OpenAI does: with the wrong key the form says so, with the right one it
//   offers them. The OpenAI preset chooses its Responses API, and the
//   provider moved to it is asked there, with the model's effort.
// - Two models are added, one with its efforts; the default is changed to it,
//   with an effort, and the model it replaces stays in the list.
// - Each model has its own settings: the default is given a context length,
//   a reply cap (the form says what the conversation is left with, and
//   refuses a cap that leaves nothing), prices and the ask to cache its
//   prompt, which its entry holds and not [control]; the other model is said
//   to take no images, and nothing of either reaches the other. A task on the
//   default is sized for that context less that reply cap and priced at those
//   prices, though the provider reports neither.
// - The New task window offers the models, shows each one's efforts (none for
//   the model that takes none), starts a task on the choice, and the provider
//   sees that model with that effort. A choice survives going to Settings and
//   back, a reload, the task it was for and an app restart: the next task
//   starts with it, its judge and helpers too, which the daemon keeps.
//   A model removed meanwhile is said to be gone instead of silently replaced.
// - In the conversation the effort is changed between messages; one chosen
//   while a response is held waits for it, says so, and the provider sees the
//   old effort on the response under way and the new one on the next. The
//   choice survives a reload. A task on the model whose context nobody
//   publishes says it works in the default window, and leads to Settings.
// - A closed task whose model was removed opens without a model: nothing is
//   sent until one is chosen.
// - Anthropic's own API is a provider kind of its own: its preset offers no
//   dialect, its key is bound as x-api-key and not as a bearer token, a model
//   added on it starts with the ask to cache its prompt chosen and is offered
//   no seed and no reply cap field, and a test of it goes to /v1/messages
//   with its effort as adaptive thinking.
// - The app restarted with the keychain locked says which models need it, a
//   conversation resumed after unlocking has the effort it was left on, and a
//   provider that stops answering is said not to answer: a task started on it
//   fails there, and nothing is ever asked of the other model.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, runtimeDir, launch, watch, requests, openOptions, taskMenu, until, step, ready, check } = require("../lib");

check(async () => {
  const root = tempDir("nodwyn-models-");
  for (const d of ["config", "data", "work"]) fs.mkdirSync(`${root}/${d}`, { mode: 0o700 });
  fs.writeFileSync(`${root}/work/hello.txt`, "hello\n");
  const ollama = start(process.env.DEMO, ["-model-only", "-pace=0"]);
  const [ollamaURL] = await line(ollama, /MODEL (\S+)/);
  const chat = start(process.env.DEMO, ["-chat-only"]);
  const [chatURL, goodKey] = await line(chat, /CHAT (\S+) KEY (\S+)/);
  const chatBase = new URL(chatURL).origin;
  const wrongKey = "sk-wrong-key-0000000000";
  fs.mkdirSync(`${root}/config/nodwyn`, { mode: 0o700 });
  const configFile = `${root}/config/nodwyn/config.toml`;
  // What the daemon keeps for the next task (newtask.last), as a person
  // editing it by hand would write it.
  const keepForNext = (model) => fs.writeFileSync(`${root}/config/nodwyn/new-task.toml`, `runs_on = ""\n\n[[host]]\nhost = ""\nmodel = "${model}"\n`);
  fs.writeFileSync(configFile, `[control]\nprovider = "ollama"\nurl = "${ollamaURL}"\nmodel = "demo"\n`);
  // The app reaches nothing beyond this computer: a request to any other host
  // goes to a proxy that is not there (Go never proxies the loopback the fakes
  // listen on), so the OpenAI provider added at the end is never asked.
  const nowhere = "http://127.0.0.1:9";
  const xdg = { ...process.env, XDG_CONFIG_HOME: `${root}/config`, XDG_DATA_HOME: `${root}/data`, XDG_RUNTIME_DIR: runtimeDir(), HTTPS_PROXY: nowhere, HTTP_PROXY: nowhere, https_proxy: nowhere, http_proxy: nowhere };
  const startApp = async () => {
    const app = start(process.env.NODWYN, ["app", "--no-open"], { env: xdg });
    const [url] = await line(app, /app: open (\S+)/, "stderr");
    return { app, url };
  };
  // A restarted app listens on the port it had, so its address differs from
  // the page's only by the launch ticket after #, which a browser takes as a
  // move within the page: it is loaded afresh, as a new window would be.
  const reopen = async (url) => {
    await page.goto("about:blank");
    await page.goto(url);
  };
  let { app, url } = await startApp();
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1280, height: 1000 } }));
  const sent = requests(page);
  // saveBar saves what waits in Settings' bar, and waits for the page to have
  // finished with it. The toast is the last thing before the settings page
  // redraws itself, and that redraw replaces whatever page is shown
  // (renderSettings draws straight into the view): a wait that a toast still
  // up from an earlier save satisfies returns before it, and the redraw then
  // lands over the task the check goes back to. The toast is hidden first, so
  // the wait sees this save's own.
  const saveBar = async () => {
    await page.evaluate(() => {
      const toast = document.querySelector("#toast");
      if (toast) toast.hidden = true;
    });
    await page.click(".save-bar button:text-is('Save')");
    await page.waitForFunction(() => {
      const toast = document.querySelector("#toast");
      return !!toast && !toast.hidden && toast.textContent.startsWith("Saved.") && document.querySelector(".save-bar")?.hidden;
    }, null, { timeout: 10000 });
  };
  // saveDefault chooses a default model and effort in Settings and saves it
  // with the bar.
  const saveDefault = async (name, effort) => {
    await page.selectOption("#default-model-model", name);
    if (effort) await page.selectOption("#default-model-effort", effort);
    await saveBar();
  };
  const go = (hash) => page.evaluate((h) => (location.hash = h), hash);
  const config = () => fs.readFileSync(configFile, "utf8");
  // What the chat provider was sent, as [model, effort] ("-" for none).
  const provider = async () => (await (await fetch(`${chatBase}/_fake/requests`)).json()).map((r) => [r.model, r.has_effort ? r.effort : "-", r.authenticated, r.answered]);
  const selected = (selector) => page.$eval(selector, (s) => s.selectedOptions[0]?.textContent);
  const idle = () => page.waitForFunction(() => document.querySelector(".task-head .pill")?.textContent === "Idle", null, { timeout: 20000 });
  const noteText = () => page.$eval(".reply-opts .mp-notes", (n) => n.textContent).catch(() => "");
  const openPicker = async () => {
    await page.waitForSelector("#task-model summary");
    if (!(await page.$eval("#task-model details", (d) => d.open))) await page.click("#task-model summary");
    await page.waitForSelector("#task-model-model");
  };
  const fits = async (selector) => {
    assert.ok(await page.$eval(selector, (el) => {
      const r = el.getBoundingClientRect();
      return r.left >= 0 && r.right <= innerWidth && el.scrollWidth <= el.clientWidth;
    }), `${selector} overflows the viewport`);
  };

  await page.goto(url);
  await page.waitForSelector("#task", { timeout: 10000 });

  // ---------- Settings › Models, from a config that names one inline model ----------
  await go("#/settings/models");
  await page.waitForSelector("#section-models");
  await page.waitForSelector("#default-model");
  assert.strictEqual(await page.inputValue("#default-model-model"), "control");
  assert.ok((await selected("#default-model-model")).startsWith("demo (default)"), "the default model is shown");
  assert.strictEqual(await page.isDisabled("#default-model-effort"), true, "a model that takes no effort offers none");
  assert.strictEqual(await selected("#default-model-effort"), "Not configurable");
  const inline = await page.textContent("#section-models .providers");
  assert.ok(inline.includes("Ollama") && inline.includes("127.0.0.1"), "the inline provider is listed: " + inline);
  assert.ok(inline.includes("config.toml"), "an inline provider says where it is changed");
  await page.waitForSelector(".provider-row .state-word:text-is('ready')", { timeout: 15000 });
  step("an inline config lists its provider and default, ready");
  await page.screenshot({ path: `${out}/models-settings-before.png`, fullPage: true });

  // The keychain: a key needs one, and the form says so until it is there.
  await page.click("#add-provider");
  // The form starts on the OpenAI preset, which asks for OpenAI's Responses
  // API; another server keeps the standard dialect.
  assert.strictEqual(await page.inputValue("#provider-url"), "https://api.openai.com/v1");
  assert.strictEqual(await page.inputValue("#provider-dialect"), "responses", "the OpenAI preset does not choose its Responses API");
  // Baseten is an OpenAI-compatible server at its own address, on chat completions.
  await page.click("#preset-baseten");
  assert.strictEqual(await page.inputValue("#provider-url"), "https://inference.baseten.co/v1");
  assert.strictEqual(await page.inputValue("#provider-dialect"), "", "Baseten is given a dialect");
  await page.click("#preset-other");
  assert.strictEqual(await page.inputValue("#provider-dialect"), "", "another server does not keep the standard dialect");
  await page.fill("#provider-url", chatURL);
  await shows("#provider-form", "Create or unlock the keychain first");
  await page.click("#provider-form >> text=Cancel");
  // The keychain is under Keys & devices, a tab of its own.
  await go("#/settings/keys");
  await page.waitForSelector("#keychain-pass");
  await page.fill("#keychain-pass", "family passphrase");
  await page.fill("#keychain-again", "family passphrase");
  await page.click("text=Create the keychain");
  await page.waitForSelector("text=Unlocked", { timeout: 20000 });
  await go("#/settings/models");
  await page.waitForSelector("#section-models");
  step("keychain created");

  // ---------- a provider with a wrong key ----------
  await page.click("#add-provider");
  await page.click("#preset-other");
  await page.fill("#provider-url", chatURL);
  await page.fill("#provider-key", wrongKey);
  await page.click("#provider-save");
  await page.waitForSelector("#provider-127-0-0-1");
  assert.deepStrictEqual(sent.of("settings.provider.write"), [{ provider: "openai", url: chatURL, api_key: wrongKey }]);
  const cfg1 = config();
  assert.ok(cfg1.includes(`[servers.127-0-0-1]\nprovider = "openai"\nurl = "${chatURL}"\ncredential = "model-127-0-0-1"\n`), "server not written:\n" + cfg1);
  const bindings = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  assert.ok(!cfg1.includes(wrongKey) && !bindings.includes(wrongKey) && !(await page.content()).includes(wrongKey), "the key is in a file or in the page");
  // Two rows: POST for the model's requests, GET for its server's model list.
  assert.ok(bindings.split('dest = "127.0.0.1"').length - 1 === 2 && bindings.includes('methods = ["POST"]') && bindings.includes('methods = ["GET"]') && bindings.includes("header:Authorization:Bearer"), "binding rows:\n" + bindings);
  assert.strictEqual(await page.isDisabled("#provider-127-0-0-1 button:text-is('Test')"), true, "a provider with no model cannot be tested");
  step("provider saved: key in the keychain and bound for its requests and its list, none in config.toml or the page");

  // ---------- a model, tested against the wrong key, then saved ----------
  // The provider lists its models only to its key, as OpenAI does: asked
  // with the wrong one, the form says so and the name is typed.
  await page.click("#add-model");
  await page.selectOption("#model-provider", "127-0-0-1");
  await page.focus("#model-id");
  await until("the provider refusing the key", async () => (await page.textContent("#model-form")).includes("did not accept the key (401)"));
  await shows("#model-form", "did not accept the key (401): replace it. Type the model's name.");
  assert.strictEqual(await page.locator("#model-id-options option").count(), 0, "a provider that refused the key offered models");
  await page.fill("#model-id", "reasoner");
  await page.click("#model-more > summary");
  await page.click("#efforts-lmh");
  await page.selectOption("#model-effort", "medium");
  await page.click("#model-test");
  await page.waitForSelector("#model-form .test-result[data-state=rejected]", { timeout: 20000 });
  await shows("#model-form .test-result", "did not accept the key (401)");
  assert.ok(!(await page.content()).includes(wrongKey), "the page shows the key");
  await page.screenshot({ path: `${out}/models-settings-rejected.png`, fullPage: true });
  await page.click("#model-save");
  await page.waitForSelector("[data-model='127-0-0-1-reasoner']");
  assert.deepStrictEqual(sent.of("settings.model.write")[0], { server: "127-0-0-1", model: "reasoner", efforts: ["low", "medium", "high"], effort: "medium" });
  await shows("[data-model='127-0-0-1-reasoner']", "effort: Low · Medium · High, starts at Medium");
  await page.click("#add-model");
  await page.selectOption("#model-provider", "127-0-0-1");
  await page.fill("#model-id", "fast");
  await page.click("#model-save");
  await page.waitForSelector("[data-model='127-0-0-1-fast']");
  assert.deepStrictEqual(sent.of("settings.model.write")[1], { server: "127-0-0-1", model: "fast", efforts: [] });
  await shows("[data-model='127-0-0-1-fast']", "no effort setting");
  step("a model with efforts and one without are listed");
  // A test's result belongs to the form it was made in: tested, then
  // cancelled, the next Add a model opens without it.
  await page.click("#add-model");
  await page.selectOption("#model-provider", "127-0-0-1");
  await page.fill("#model-id", "tried");
  await page.click("#model-test");
  await page.waitForSelector("#model-form .test-result:not([data-state=pending])", { timeout: 20000 });
  await page.click("#model-form button:text-is('Cancel')");
  await page.click("#add-model");
  await page.waitForSelector("#model-form");
  assert.strictEqual(await page.locator("#model-form .test-result").count(), 0, "a cancelled form's test result showed in the next one");
  await page.click("#model-form button:text-is('Cancel')");
  // Add a model on a provider opens the form in that provider's card, where
  // its button was: below every provider, it was out of sight once there
  // were a few. The card says which provider, so the form keeps it.
  await page.click("#add-model-127-0-0-1");
  await page.waitForSelector("#provider-127-0-0-1 .provider-models > #model-form");
  assert.strictEqual(await page.locator("#add-model-127-0-0-1").count(), 0, "the provider's Add button stayed beside its form");
  assert.strictEqual(await page.inputValue("#model-provider"), "127-0-0-1");
  assert.ok(await page.isDisabled("#model-provider"), "a form opened in a provider's card can be moved to another provider");
  assert.ok(await page.locator("#model-form").evaluate((f) => { const r = f.getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; }), "the form opened out of sight");
  await page.click("#model-form button:text-is('Cancel')");
  // Add a model under the list keeps the form beside its own button, with
  // the provider to choose.
  await page.click("#add-model");
  await page.waitForSelector("#model-form");
  assert.strictEqual(await page.locator(".provider-card #model-form").count(), 0, "Add a model under the list opened in a card");
  assert.ok(!(await page.isDisabled("#model-provider")), "Add a model under the list has its provider fixed");
  await page.click("#model-form button:text-is('Cancel')");
  step("Add a model on a provider opens in its card; Add a model under the list opens below it");

  // ---------- the key is replaced under Credentials; the same test now passes ----------
  // A key's value is changed in one place: the provider's form has no field
  // for a key that is stored, and links to it.
  await page.click("#provider-127-0-0-1 button:text-is('Edit')");
  // Edit opens the provider's form in its card, where it was clicked, not
  // below every provider; the card's Edit is gone while it is open.
  await page.waitForSelector("#provider-127-0-0-1 > #provider-form");
  assert.strictEqual(await page.locator("#provider-127-0-0-1 > .provider-actions button:text-is('Edit')").count(), 0, "the card's Edit stayed beside its open form");
  assert.ok(await page.locator("#provider-form").evaluate((f) => { const r = f.getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; }), "the provider form opened out of sight");
  assert.strictEqual(await page.locator("#provider-key").count(), 0, "the provider form takes a new key as well as Credentials");
  await shows("#provider-form", "Replace it under Credentials");
  await page.click("#provider-form a:text-is('Replace it under Credentials')");
  await page.waitForSelector("#model-keys .cred-row", { timeout: 10000 });
  await page.click("#model-keys .cred-row >> nth=0");
  await page.fill("#model-keys .cred-detail input[type=password]", goodKey);
  await page.click("#model-keys .cred-detail button:has-text('Replace')");
  await page.waitForSelector("text=has its new value", { timeout: 10000 });
  assert.strictEqual(sent.of("credentials.replace").at(-1).value, goodKey, "the key sent was not the one typed");
  assert.ok(!(await page.content()).includes(goodKey), "the page shows the key");
  await go("#/settings/models");
  await page.waitForSelector("#provider-127-0-0-1");
  await page.click("#provider-127-0-0-1 button:text-is('Test')");
  await page.waitForSelector("#provider-127-0-0-1 .test-result.ok", { timeout: 20000 });
  await shows("#provider-127-0-0-1 .test-result", "answered");
  await page.click("[data-model='127-0-0-1-reasoner'] button:text-is('Test')");
  await page.waitForSelector("[data-model='127-0-0-1-reasoner'] .test-result.ok", { timeout: 20000 });
  await shows("[data-model='127-0-0-1-reasoner'] .test-result", "reasoner answered", "with Medium effort");
  assert.deepStrictEqual((await provider()).filter((r) => r[2]), [["fast", "-", true, 200], ["reasoner", "medium", true, 200]], "only requests with the right key were accepted, each with the effort it was meant for");
  const bindings2 = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  assert.strictEqual(bindings2.split("[[bind]]").length - 1, 2, "replacing the key added a binding row:\n" + bindings2);
  step("the key replaced in place; the provider answers with the model's effort");
  // Its list is asked again, with the new key, and offers its models.
  const listed = sent.of("models.offered").length;
  await page.click("#add-model");
  await page.selectOption("#model-provider", "127-0-0-1");
  await page.focus("#model-id");
  await page.waitForFunction(() => [...document.querySelectorAll("#model-id-options option")].map((o) => o.value).join() === "fast,reasoner", null, { timeout: 10000 });
  assert.deepStrictEqual(sent.of("models.offered").slice(listed), [{ server: "127-0-0-1" }], "the list was not asked again after the key changed");
  await shows("#model-form", "127-0-0-1 offers 2 models");
  await page.click("#model-form >> text=Cancel");
  step("with the right key the provider's list offers its models");
  await page.screenshot({ path: `${out}/models-settings-after.png`, fullPage: true });

  // ---------- the provider moved to OpenAI's Responses API, and back ----------
  // OpenAI takes an effort beside tools only there for some reasoning models
  // (ADR 8, amended 2026-10-05): a test of the model is then sent there, with
  // its effort, and nothing else about the provider changes.
  const lastRequest = async () => (await (await fetch(`${chatBase}/_fake/requests`)).json()).at(-1);
  await page.click("#provider-127-0-0-1 button:text-is('Edit')");
  await page.selectOption("#provider-dialect", "responses");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { name: "127-0-0-1", provider: "openai", url: chatURL, dialect: "responses" });
  assert.ok(config().includes(`[servers.127-0-0-1]\nprovider = "openai"\ndialect = "responses"\nurl = "${chatURL}"\ncredential = "model-127-0-0-1"\n`), "dialect not written, or the key's credential lost:\n" + config());
  await shows("#provider-127-0-0-1", "OpenAI-compatible (responses)");
  await page.click("[data-model='127-0-0-1-reasoner'] button:text-is('Test')");
  await until("the test to reach /v1/responses", async () => (await lastRequest()).path === "/v1/responses");
  const viaResponses = await lastRequest();
  assert.deepStrictEqual([viaResponses.model, viaResponses.effort, viaResponses.authenticated, viaResponses.answered], ["reasoner", "medium", true, 200], "the Responses request: " + JSON.stringify(viaResponses));
  await page.waitForSelector("[data-model='127-0-0-1-reasoner'] .test-result.ok", { timeout: 20000 });
  await shows("[data-model='127-0-0-1-reasoner'] .test-result", "reasoner answered", "with Medium effort");
  await page.screenshot({ path: `${out}/models-settings-responses.png`, fullPage: true });
  await page.click("#provider-127-0-0-1 button:text-is('Edit')");
  assert.strictEqual(await page.inputValue("#provider-dialect"), "responses", "the form does not show the saved dialect");
  await page.selectOption("#provider-dialect", "");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { name: "127-0-0-1", provider: "openai", url: chatURL });
  assert.ok(!config().includes("dialect"), "the standard dialect left one in config.toml:\n" + config());
  step("a provider on OpenAI's Responses API is asked there, with the model's effort, and moved back");

  // ---------- a second provider at an address already in use ----------
  // Add a provider opens its form below the list, where its button was, so a
  // window shorter than the list still shows it.
  await page.setViewportSize({ width: 1280, height: 500 });
  await page.click("#add-provider");
  await page.waitForSelector("#provider-form");
  assert.strictEqual(await page.locator(".provider-card #provider-form").count(), 0, "Add a provider opened in a provider's card");
  assert.ok(await page.locator("#provider-form").evaluate((f) => { const r = f.getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; }), "Add a provider's form opened out of sight");
  await page.click("#provider-form >> text=Cancel");
  await page.setViewportSize({ width: 1280, height: 1000 });
  step("a provider's Edit opens in its card; Add a provider opens below the list, in sight");
  const providersBefore = await page.locator("[data-provider]").count();
  await page.click("#add-provider");
  await page.click("#preset-other");
  await page.fill("#provider-url", chatURL);
  await page.click("#provider-save");
  await page.waitForFunction((n) => document.querySelectorAll("[data-provider]").length === n, providersBefore + 1);
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { name: "openai", provider: "openai", url: chatURL });
  await page.click("#add-provider");
  await page.click("#preset-other");
  await page.fill("#provider-url", chatURL);
  await page.click("#provider-more > summary");
  await page.fill("#provider-name", "127-0-0-1");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form .error");
  assert.ok((await page.innerText("#provider-form .error")).includes("exists"), "a name in use was accepted");
  await page.fill("#provider-name", "plan-test");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-plan-test");
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { name: "plan-test", provider: "openai", url: chatURL });
  assert.strictEqual((await page.locator("[data-provider]").count()), providersBefore + 2, "two servers at one address were merged");
  for (const name of ["openai", "plan-test"]) {
    await page.click(`#provider-${name} button:text-is('Remove')`);
    await page.click(`#provider-${name} button:text-is('Remove')`);
    await page.waitForSelector(`#provider-${name}`, { state: "detached" });
  }
  step("a second provider can share an address, and a name in use is refused");

  // ---------- the default model and its effort ----------
  await page.selectOption("#default-model-model", "127-0-0-1-reasoner");
  assert.strictEqual(await page.isDisabled("#default-model-effort"), false);
  await page.selectOption("#default-model-effort", "high");
  // The default waits in the bar with every other setting: nothing is written
  // until it is saved.
  await shows(".save-bar", "1 unsaved change");
  assert.strictEqual(sent.of("settings.control.write").length, 0, "the default was written before the bar saved it");
  await saveBar();
  assert.deepStrictEqual(sent.of("settings.control.write").at(-1), { name: "127-0-0-1-reasoner", effort: "high" });
  const cfg2 = config();
  assert.ok(cfg2.includes('[control]\nserver = "127-0-0-1"\nmodel = "reasoner"\neffort = "high"\n'), "default not written:\n" + cfg2);
  assert.ok(cfg2.includes("[models.127-0-0-1-demo]"), "the model that was the default left the list:\n" + cfg2);
  assert.ok((await selected("#default-model-model")).startsWith("reasoner (default)"), "the default is shown");
  step("default chosen with its effort; the previous default is still listed");

  // ---------- each model's own settings ----------
  // The default is a Claude route, which caches a prompt only when asked
  // (ADR 9, 2026-10-05), on a server that, as OpenAI's and Cerebras' do, says
  // neither how large the model's context is nor what a request cost: the
  // model's own settings say all three (ADR 41, amended 2026-10-05).
  const block = (table) => (config().split(`[${table}]\n`)[1] || "").split("\n[")[0];
  await shows("[data-model='control']", "context as the provider says · no prices");
  await page.click("[data-model='control'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  // Edit opens the form right under the model's row, not below every provider.
  assert.strictEqual(await page.locator("#provider-127-0-0-1 [data-model='control'] + #model-form").count(), 1, "Edit did not open the form under its model");
  assert.strictEqual(await page.getAttribute("#model-vision-true", "aria-pressed"), "true", "a model is not taken to read images until it is said not to");
  await page.fill("#model-num_ctx", "1,050,000");
  // The reply cap comes out of the context, and the form says what the
  // conversation is left with as it is typed (config.Window.Sized).
  await shows("#model-window", "Of 1,050,000 tokens, each reply may take 16,384; the conversation is summarized or trimmed past 775,212");
  await page.fill("#model-max_output", "1,050,000");
  await shows("#model-window", "leaves nothing of a 1,050,000-token context");
  await page.click("#model-save");
  await shows("#model-form .error", "The reply cap (1,050,000) must be below the context length");
  await page.fill("#model-max_output", "128,000");
  await shows("#model-window", "Of 1,050,000 tokens, each reply may take 128,000; the conversation is summarized or trimmed past 691,500, with room up to 922,000");
  await page.fill("#model-price_input", "5");
  await page.fill("#model-price_cached_input", "0.5");
  await page.fill("#model-price_output", "lots");
  await page.click("#model-save");
  await shows("#model-form .error", "The price of output takes a number");
  await page.fill("#model-price_output", "40");
  await page.click("#model-more-settings > summary");
  await page.click("#model-cache_control-ephemeral");
  await page.locator("#model-form").screenshot({ path: `${out}/models-settings-form.png` });
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), {
    server: "127-0-0-1",
    model: "reasoner",
    efforts: ["low", "medium", "high"],
    labels: [],
    settings: { num_ctx: 1050000, max_output: 128000, price_input: 5, price_cached_input: 0.5, price_output: 40, cache_control: "ephemeral" },
  });
  const reasonerBlock = block("models.127-0-0-1-reasoner");
  for (const line of ["num_ctx = 1050000", "max_output = 128000", 'cache_control = "ephemeral"', "price_input = 5", "price_cached_input = 0.5", "price_output = 40"]) {
    assert.ok(reasonerBlock.includes(line + "\n"), `the model's entry lacks ${line}:\n` + config());
  }
  assert.ok(!/num_ctx|max_output|price_|cache_control|vision/.test(block("control")), "[control] holds a copy of the model's settings:\n" + config());
  await shows("[data-model='control']", "context 1,050,000 tokens · replies up to 128,000 tokens · $5 in, $40 out per million tokens");
  await shows("[data-model='127-0-0-1-fast']", "context as the provider says · no prices");
  // The form shows what was saved.
  await page.click("[data-model='control'] button:text-is('Edit')");
  assert.strictEqual(await page.inputValue("#model-num_ctx"), "1,050,000");
  assert.strictEqual(await page.inputValue("#model-max_output"), "128,000");
  await shows("#model-window", "past 691,500");
  assert.strictEqual(await page.inputValue("#model-price_output"), "40");
  await page.click("#model-more-settings > summary");
  assert.strictEqual(await page.getAttribute("#model-cache_control-ephemeral", "aria-pressed"), "true");
  assert.strictEqual(await page.locator("#model-output_field-default").count(), 1, "a chat-completions model has no reply cap field");
  await page.click("#model-form >> text=Cancel");
  // The other model takes no images: said for it alone.
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Edit')");
  await page.click("#model-vision-false");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "127-0-0-1-fast", server: "127-0-0-1", model: "fast", efforts: [], labels: [], settings: { vision: false } });
  assert.ok(block("models.127-0-0-1-fast").includes("vision = false\n") && !/vision/.test(block("models.127-0-0-1-reasoner")), "vision is not the fast model's alone:\n" + config());
  await shows("[data-model='127-0-0-1-fast']", "takes no images");
  assert.ok(!(await page.innerText("[data-model='control']")).includes("takes no images"), "the default took the other model's word on images");
  await page.locator("#section-models").screenshot({ path: `${out}/models-settings-per-model.png` });
  step("each model has its own context, reply cap, prices, images and ask to cache; [control] holds none of them");

  // ---------- labels ----------
  // A model says what it is for in words of its own; a hint button adds one,
  // what is typed is made lower case, and the entry in config.toml holds them.
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Edit')");
  assert.strictEqual(await page.inputValue("#model-labels"), "", "a model nobody labelled has labels");
  await page.click("#model-form [data-label=cheap]");
  assert.strictEqual(await page.getAttribute("#model-form [data-label=cheap]", "aria-pressed"), "true");
  await page.fill("#model-labels", "Fast, cheap  REVIEW");
  assert.strictEqual(await page.getAttribute("#model-form [data-label=fast]", "aria-pressed"), "true", "typing a label did not press its hint");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1).labels, ["fast", "cheap", "review"]);
  assert.ok(block("models.127-0-0-1-fast").includes('labels = ["fast", "cheap", "review"]\n'), "the entry lacks its labels:\n" + config());
  await shows("[data-model='127-0-0-1-fast'] .model-labels", "review");
  // Editing it again shows them, and clearing the field clears them.
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Edit')");
  assert.strictEqual(await page.inputValue("#model-labels"), "fast, cheap, review");
  await page.fill("#model-labels", "");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1).labels, []);
  assert.ok(!/labels/.test(block("models.127-0-0-1-fast")), "cleared labels stayed in the entry:\n" + config());
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Edit')");
  await page.fill("#model-labels", "fast, review");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  step("a model's labels are edited in Settings, lower-cased, kept in its entry and cleared by emptying the field");

  // ---------- the New task window ----------
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  // The folder the tasks work in, which Home asks for when none is known.
  await page.click("text=Add a folder");
  await page.fill("#add-path", `${root}/work`);
  await page.click("button:has-text('Add this folder')");
  await page.waitForSelector("#work-in button[aria-pressed=true]");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "control");
  assert.strictEqual(await page.inputValue("#new-task-model-effort"), "high", "a new task starts on the default effort");
  assert.deepStrictEqual(
    await page.$$eval("#new-task-model-effort option", (o) => o.map((x) => x.value)),
    ["low", "medium", "high"],
    "the efforts are the model's own",
  );
  await page.selectOption("#new-task-model-model", "127-0-0-1-fast");
  assert.strictEqual(await page.isDisabled("#new-task-model-effort"), true, "a model that takes none offers none");
  assert.strictEqual(await selected("#new-task-model-effort"), "Not configurable");
  await page.setViewportSize({ width: 390, height: 844 });
  await fits("#new-task-model");
  assert.ok(await page.isVisible("#new-task-model-model"), "new-task choices remain visible");
  await page.screenshot({ path: `${out}/models-home-mobile.png` });
  await page.setViewportSize({ width: 1280, height: 1000 });
  // An unsent choice survives leaving the page and a reload, and is not the default.
  await go("#/settings/models");
  await page.waitForSelector("#default-model");
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "127-0-0-1-fast", "the choice was lost going to Settings and back");
  await page.reload();
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "127-0-0-1-fast", "the choice was lost in a reload");
  step("an unsent choice survives navigation and a reload");

  // Helpers: by default a task works with its own model alone; the model it is
  // on is not offered as its own helper, and ticking then unticking sends none.
  await openOptions(page);
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: none/);
  await page.click("#new-task-helpers summary");
  assert.deepStrictEqual(await page.$$eval("#new-task-helpers .helper", (e) => e.map((x) => x.dataset.model)), ["127-0-0-1-demo"], "the helpers on offer are the models besides the one chosen and the default");
  await page.check("#new-task-helpers [data-model='127-0-0-1-demo'] input");
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: 1/);
  await page.uncheck("#new-task-helpers [data-model='127-0-0-1-demo'] input");
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: none/);
  // A task on the model that takes no effort sends none: the provider would refuse it.
  await page.fill("#task", "Say a word");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.open").at(-1), { repo: `${root}/work`, name: "127-0-0-1-fast" });
  await page.waitForSelector(".prose, .turn.assistant", { timeout: 20000 });
  await idle();
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by fast with default effort."));
  const fastTask = sent.of("session.open").length;
  assert.strictEqual(await page.inputValue("#task-model-model"), "127-0-0-1-fast");
  assert.strictEqual(await page.isDisabled("#task-model-effort"), true);
  const fastId = sent.of("session.send").at(-1).id;
  step("a task on a model with no efforts sends none");
  // Neither the model's entry, its server's list nor any provider says how
  // much "fast" holds: the task keeps the default window and says so where
  // it is seen, with the way to Settings (ADR 41, amended 2026-10-05).
  await page.waitForSelector(".task-head .context-unknown");
  await shows(".task-head .context-unknown", "61,440-token window", "doesn't know how much context fast holds");
  await page.screenshot({ path: `${out}/models-context-unknown.png` });
  await page.click(".task-head .context-unknown button:text-is('Open Settings')");
  await page.waitForSelector("#default-model");
  assert.match(await page.evaluate(() => location.hash), /^#\/settings/);
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  step("a task whose model's context nobody knows says it works in the default window");

  // The model chosen last is the next task's too (ADR 41, amended
  // 2026-10-27), and the daemon kept it.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "127-0-0-1-fast", "the next task did not start on the model chosen last");
  assert.match(fs.readFileSync(`${root}/config/nodwyn/new-task.toml`, "utf8"), /model = "127-0-0-1-fast"/, "the daemon did not keep the choice");

  // Close the first task; it is resumed after its model is removed, below.
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  await taskMenu(page, "Close");
  await page.waitForSelector(".task-actions button:text-is('Resume')");
  await shows(".task-meta", "fast, as it was left");
  step("a closed task shows the model it was left on");

  // ---------- a task on the default model, with another effort ----------
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.selectOption("#new-task-model-model", "control");
  await page.selectOption("#new-task-model-effort", "low");
  await openOptions(page);
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: none/, "helpers unticked for one task were kept for the next");
  await page.click("#new-task-helpers summary");
  await shows("#new-task-helpers [data-model='127-0-0-1-fast']", "review");
  await page.check("#new-task-helpers [data-model='127-0-0-1-fast'] input");
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: 1/);
  await page.screenshot({ path: `${out}/models-home-helpers.png` });
  await page.fill("#task", "Say hello");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.open").at(-1), { repo: `${root}/work`, name: "control", effort: "low", delegates: ["127-0-0-1-fast"] });
  await shows("#task-helpers", "May hand work to: 127-0-0-1-fast");
  step("a task is started with the helpers ticked for it, and shows them");
  await idle();
  const id = sent.of("session.send").at(-1).id;
  // The next task starts with them ticked, as they were chosen last.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-effort"), "low", "the next task did not start on the effort chosen last");
  await openOptions(page);
  assert.match(await page.innerText("#new-task-helpers summary"), /Helpers: 1/, "the helpers ticked last were not kept for the next task");
  await go(`#/task/${id}`);
  await page.waitForSelector(".task[data-loaded]");
  step("the next task starts with the model, effort and helpers chosen last");
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by reasoner with low effort."));
  assert.strictEqual(await page.inputValue("#task-model-model"), "control");
  assert.strictEqual(await page.inputValue("#task-model-effort"), "low");
  await openActivity("Model: reasoner · low effort");
  await shows(".transcript", "Model: reasoner · low effort");
  step("a task started with an effort asks the provider for it");
  // The provider says neither the context nor the cost; the model's own
  // settings do: the window is sized for 1,050,000 tokens less its
  // 128,000-token reply cap, as the form said, and the reply is priced (an
  // estimate, ≈).
  await page.click(".task-aside [role=tab]:has-text('Statistics')");
  await page.waitForFunction(() => document.body.textContent.includes("691,500"), null, { timeout: 10000 });
  assert.strictEqual(await page.$(".task-head .context-unknown"), null, "a task sized for its model's context says the context is not known");
  await page.waitForFunction(() => /≈\$\d/.test(document.querySelector(".transcript").textContent), null, { timeout: 10000 });
  step("the task on the default model is sized for its context and priced at its prices");
  assert.strictEqual(await page.isVisible("#task-model-model"), false, "chat choices start collapsed");
  await shows("#task-model summary", "reasoner");
  await shows("#task-model summary", "Low");
  await page.focus("#task-model summary");
  await page.press("#task-model summary", "Enter");
  assert.ok(await page.isVisible("#task-model-model"), "keyboard opens chat choices");
  await page.press("#task-model summary", "Enter");
  assert.strictEqual(await page.isVisible("#task-model-model"), false, "keyboard closes chat choices");
  await page.screenshot({ path: `${out}/models-conversation-compact.png` });
  await openPicker();

  // ---------- the effort is changed between messages ----------
  await page.selectOption("#task-model-effort", "high");
  await page.waitForFunction(() => document.querySelector(".reply-opts .mp-notes")?.textContent.includes("Takes effect from your next message"));
  assert.deepStrictEqual(sent.of("session.model.set").at(-1), { id, name: "control", effort: "high" });
  await shows("#task-model summary", "High");
  await page.click("#task-model summary");
  assert.ok(await page.isVisible("#task-model .mp-notes"), "change notes stay visible when collapsed");
  await page.fill("#reply", "Again");
  await page.press("#reply", "Enter");
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by reasoner with high effort."));
  await idle();
  assert.ok(!(await noteText()).includes("Takes effect"), "the note about the next message outlived the message");
  step("an effort changed between messages applies to the next one");

  // ---------- a choice made while a response is held ----------
  await fetch(`${chatBase}/_fake/hold`, { method: "POST" });
  await page.fill("#reply", "Slowly");
  await page.press("#reply", "Enter");
  await until("the request to be held", async () => (await (await fetch(`${chatBase}/_fake/waiting`)).json()) === 1);
  await page.waitForFunction(() => document.querySelector(".task-head .pill")?.textContent !== "Idle");
  const unchanged = await noteText();
  assert.ok(!unchanged.includes("The response now running keeps") && !unchanged.includes("next message"), "an unchanged running response shows a change notice: " + unchanged);
  await openPicker();
  await page.selectOption("#task-model-effort", "medium");
  await page.waitForFunction(() => document.querySelector(".reply-opts .mp-notes")?.textContent.includes("Chosen while a response is running"));
  assert.deepStrictEqual(sent.of("session.model.set").at(-1), { id, name: "control", effort: "medium" });
  const held = await noteText();
  assert.ok(held.includes("Medium effort takes effect from your next message") && held.includes("keeps reasoner · High effort"), "the note does not say when it applies: " + held);
  assert.strictEqual(await page.inputValue("#task-model-effort"), "medium", "the picker does not show the choice made");
  await page.screenshot({ path: `${out}/models-conversation-queued.png` });
  await fetch(`${chatBase}/_fake/release`, { method: "POST" });
  await idle();
  await page.waitForFunction(() => !document.querySelector(".reply-opts .mp-notes")?.textContent.includes("Chosen while"));
  assert.strictEqual(await page.inputValue("#task-model-effort"), "medium");
  await page.fill("#reply", "Once more");
  await page.press("#reply", "Enter");
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by reasoner with medium effort."));
  await idle();
  assert.deepStrictEqual(
    (await provider()).filter((r) => r[3] === 200 && r[0] === "reasoner").map((r) => r[1]).slice(-5),
    ["medium", "low", "high", "high", "medium"],
    "the provider saw the wrong efforts (its first is the Test above)",
  );
  await openActivity("Next message: reasoner · medium effort");
  await shows(".transcript", "Next message: reasoner · medium effort");
  // This conversation is on the default model, which asks its provider to
  // cache the prompt: every request carried the ask, the provider read what a
  // request wrote from the next one, the console says so, and the model that
  // did not ask was never sent it.
  const seen = await (await fetch(`${chatBase}/_fake/requests`)).json();
  const asked = seen.filter((r) => r.cache_control);
  assert.ok(asked.length >= 4 && asked.every((r) => r.model === "reasoner" && r.cache_control === '{"type":"ephemeral"}' && r.answered === 200), "requests that asked to cache: " + JSON.stringify(asked));
  assert.ok(asked.at(-1).cached_tokens > asked.at(-1).prompt_tokens / 2, "the last request read little of its prompt from the cache: " + JSON.stringify(asked.at(-1)));
  assert.ok(seen.filter((r) => r.model === "fast").every((r) => !r.cache_control), "a model that did not ask was sent the ask");
  await openActivity(/\d+% cached/);
  assert.match(await page.innerText(".transcript"), /\d+% cached/, "the transcript does not say the prompt was read from the cache");
  step("a model told to cache its prompt asks for it on every request, and is read back from the cache");
  step("a choice made while a response ran waited for it: the provider saw the old effort, then the new");

  // ---------- a message queued or steered while a response is held ----------
  // Send is off while a response runs. Queue sends the reply to wait for the
  // response's end, and Steer stops the response and sends it next; each is
  // one session.send with its mode, and the text shows as queued until its
  // own user.msg arrives.
  const userTexts = () => page.$$eval(".transcript .turn.user .you-text", (els) => els.map((e) => e.textContent));
  const queuedTexts = () => page.$$eval("#queued-list .queued-item .you-text", (els) => els.map((e) => e.textContent));
  const waiting = async () => (await (await fetch(`${chatBase}/_fake/waiting`)).json());
  assert.strictEqual(await page.isVisible("#reply-queue"), false, "Queue shows while the task is idle");
  await fetch(`${chatBase}/_fake/hold`, { method: "POST" });
  await page.fill("#reply", "Hold on");
  await page.press("#reply", "Enter");
  await until("the request to be held", async () => (await waiting()) === 1);
  await page.waitForSelector("#reply-queue", { state: "visible" });
  assert.strictEqual(await page.isDisabled("#reply-queue"), true, "Queue is on with nothing to send");
  assert.strictEqual(await page.isDisabled("#reply-steer"), true, "Steer is on with nothing to send");
  await page.fill("#reply", "next");
  assert.strictEqual(await page.isDisabled("button[aria-label='Send']"), true, "Send is on while a response runs");
  assert.strictEqual(await page.isDisabled("#reply-queue"), false, "Queue is off with a message to send");
  assert.strictEqual(await page.isDisabled("#reply-steer"), false, "Steer is off with a message to send");
  assert.match(await page.getAttribute("#reply", "placeholder"), /queue/i, "the reply box does not say a message can be queued");
  const calls = sent.of("session.send").length;
  await page.click("#reply-queue");
  await until("the queue call", async () => sent.of("session.send").length === calls + 1);
  assert.deepStrictEqual(sent.of("session.send").at(-1), { id, text: "next", mode: "queue" });
  await until("the reply box to empty", async () => (await page.inputValue("#reply")) === "");
  assert.deepStrictEqual(await queuedTexts(), ["next"], "the queued message is not listed");
  assert.ok(!(await userTexts()).includes("next"), "a queued message ran while the response did");
  assert.strictEqual(await page.isDisabled("#reply-steer"), true, "Steer is on after its message was sent");
  await page.screenshot({ path: `${out}/models-conversation-queued-message.png` });
  await page.fill("#reply", "then");
  await page.click("#reply-steer");
  await until("the steer call", async () => sent.of("session.send").length === calls + 2);
  assert.deepStrictEqual(sent.of("session.send").at(-1), { id, text: "then", mode: "steer" });
  // The response held is stopped and the steering message is the next turn,
  // ahead of the one queued before it.
  await until("the steering message to start", async () => (await userTexts()).includes("then"));
  assert.deepStrictEqual(await queuedTexts(), ["next"], "the message that started is still listed, or the one that waits is not");
  await fetch(`${chatBase}/_fake/release`, { method: "POST" });
  await until("both messages to have been answered", async () => (await userTexts()).includes("next") && (await page.$eval(".task-head .pill", (p) => p.textContent)) === "Idle");
  assert.deepStrictEqual((await userTexts()).slice(-3), ["Hold on", "then", "next"], "the messages did not run in the order steer, queue");
  assert.deepStrictEqual(await queuedTexts(), [], "a message that ran is still listed as queued");
  assert.strictEqual(await page.isVisible("#reply-queue"), false, "Queue shows while the task is idle");
  step("a message queued while a response runs waits for it, and a steering one stops it and goes first");

  // Reloaded, the conversation shows what it has, from the daemon and its log.
  await page.reload();
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  assert.strictEqual(await page.inputValue("#task-model-effort"), "medium");
  assert.strictEqual(await page.inputValue("#task-model-model"), "control");
  assert.strictEqual(await page.isVisible("#task-model-model"), false, "reload restores a compact chat picker");
  await shows("#task-model summary", "Medium");
  await page.setViewportSize({ width: 390, height: 844 });
  await fits("#task-model");
  await openPicker();
  await fits("#task-model .mp-controls");
  await page.screenshot({ path: `${out}/models-conversation-mobile.png` });
  await page.setViewportSize({ width: 1280, height: 1000 });
  step("the selection survives a reload");

  // ---------- an effort the model does not take is refused, not dropped ----------
  const refused = await page.evaluate(async (taskId) => {
    // The console offers no way to ask for it; the daemon refuses it all the
    // same. The socket signs in as the console's does, with the session the
    // page keeps (ADR 30, amended 2026-10-19).
    const socket = new WebSocket(`ws://${location.host}/socket`, ["nodwyn", `nodwyn.session.${localStorage.getItem("nodwyn.session")}`]);
    await new Promise((resolve) => (socket.onopen = resolve));
    const ask = (id, method, params) => new Promise((resolve) => {
      socket.onmessage = (m) => { const f = JSON.parse(m.data); if (f.id === id) resolve(f); };
      socket.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
    });
    await ask(1, "hello", { protocol: 1 });
    const reply = await ask(2, "session.model.set", { id: taskId, name: "control", effort: "extreme" });
    socket.close();
    return reply.error?.message || "";
  }, id);
  assert.ok(refused.includes("does not take the effort") && refused.includes("low, medium, high"), "refusal: " + refused);

  // ---------- a model that becomes the default while a conversation is on it ----------
  // The conversation names the [models.NAME] entry it was opened on. Made the
  // default, that entry is listed once, as "control" (ModelChoices): the
  // conversation talks to that model still, and the picker says so rather
  // than calling the entry gone.
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  await page.click(".task-actions button:text-is('Resume')");
  await page.waitForSelector(".task-actions .menu-item:text-is('Close')", { state: "attached" });
  await page.waitForSelector("#task-model-model", { state: "attached" });
  await go("#/settings/models");
  await page.waitForSelector("#default-model");
  await saveDefault("127-0-0-1-fast");
  assert.deepStrictEqual(sent.of("settings.control.write").at(-1), { name: "127-0-0-1-fast", effort: "" });
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  // The picker is redrawn when the change reaches the page, and a redraw
  // replaces the element: wait for what it says, not for the element. The
  // entry is no longer one of the choices once it is the default.
  await until("the conversation's picker to have read the change", async () => !(await page.$$eval("#task-model-model option", (os) => os.map((o) => o.value))).includes("127-0-0-1-fast"));
  assert.ok(!(await noteText()).includes("no longer in your list"), "the conversation on the model that became the default is said to be gone: " + (await noteText()));
  assert.strictEqual(await page.inputValue("#task-model-model"), "control", "the picker does not show the model that is now the default");
  assert.ok((await selected("#task-model-model")).startsWith("fast (default)"), "the picker does not show the default: " + (await selected("#task-model-model")));
  const answered = await page.locator(".prose").count();
  await page.fill("#reply", "Still here");
  await page.press("#reply", "Enter");
  await page.waitForFunction((n) => document.querySelectorAll(".prose").length > n, answered, { timeout: 20000 });
  await idle();
  step("a conversation on a model that became the default says so, and still sends");
  // A choice kept for the next task that names the entry is not gone either:
  // the daemon still lists the name, and the choice it is has become the
  // default.
  await go("#/");
  keepForNext("127-0-0-1-fast");
  await page.reload();
  await ready(page);
  assert.ok(!(await page.innerText("#new-task-model")).includes("no longer in your list"), "a choice kept for the next task on the model that became the default is said to be gone: " + (await page.innerText("#new-task-model")));
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "control", "the kept choice does not show the model that is now the default");
  await page.fill("#task", "Anything");
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), false, "a task cannot start on the model that became the default");
  await page.fill("#task", "");
  step("a choice kept for the next task on that model is not called gone");
  // The default moves back; the conversation is on its entry again.
  await go("#/settings/models");
  await page.waitForSelector("#default-model");
  await saveDefault("127-0-0-1-reasoner", "high");
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  await until("the conversation's picker to have read the change", async () => (await page.$$eval("#task-model-model option", (os) => os.map((o) => o.value))).includes("127-0-0-1-fast"));
  assert.strictEqual(await page.inputValue("#task-model-model"), "127-0-0-1-fast", "the conversation did not return to its entry");
  assert.ok(!(await noteText()).includes("no longer in your list"), "the conversation on the entry is said to be gone: " + (await noteText()));
  await taskMenu(page, "Close");
  await page.waitForSelector(".task-actions button:text-is('Resume')");
  step("with the default moved back, the conversation names its entry again");

  // ---------- a model that is removed while it is in use ----------
  // The task on "fast" is closed; "fast" leaves the list; opening the task
  // again does not hand it to another model.
  await go("#/settings/models");
  await page.waitForSelector("[data-model='127-0-0-1-fast']");
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Remove')");
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Remove')"); // the confirm
  await page.waitForSelector("[data-model='127-0-0-1-fast']", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.remove"), [{ name: "127-0-0-1-fast" }]);
  assert.ok(!config().includes("fast"), "the model is still in config.toml");
  // The unsent choice was "fast" once: it is gone, and the page says so.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  keepForNext("127-0-0-1-fast");
  await page.reload();
  await page.waitForSelector("#new-task-model-model");
  await shows("#new-task-model", "127-0-0-1-fast is no longer in your list of models");
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), true, "a task can start on a model that is gone");
  await page.fill("#task", "Anything");
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), true, "a task can start on a model that is gone");
  await page.selectOption("#new-task-model-model", "control");
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), false);
  await page.fill("#task", "");
  step("a model removed from the list is said to be gone, not replaced");

  // The closed task on the removed model opens without one.
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  await page.click(".task-actions button:text-is('Resume')");
  await page.waitForSelector("#task-model-model");
  await shows(".reply-opts .mp-notes", "not available");
  assert.strictEqual(await selected("#task-model-model"), "Choose a model…");
  assert.strictEqual(await page.isDisabled("#reply"), true, "a conversation with no model can be written to");
  assert.strictEqual(await page.getAttribute("#reply", "placeholder"), "Choose a model below to continue.");
  await page.screenshot({ path: `${out}/models-conversation-unavailable.png` });
  await page.selectOption("#task-model-model", "control");
  await page.waitForFunction(() => !document.querySelector("#reply").disabled);
  assert.deepStrictEqual(sent.of("session.model.set").at(-1), { id: fastId, name: "control", effort: "high" });
  await page.fill("#reply", "Back again");
  await page.press("#reply", "Enter");
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by reasoner with high effort."));
  step("a conversation whose model is gone asked nothing until one was chosen for it");

  // ---------- the app restarted: the keychain is locked ----------
  // A choice made for the next task is kept across it, by the daemon: the
  // window's own storage is one browser's, for one address.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.selectOption("#new-task-model-effort", "medium");
  await until("the daemon to keep the effort", async () => /effort = "medium"/.test(fs.readFileSync(`${root}/config/nodwyn/new-task.toml`, "utf8")));
  assert.strictEqual(await page.evaluate(() => Object.keys(localStorage).filter((k) => k.startsWith("nodwyn-new-task")).length), 0, "the choice was kept in this browser");
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  ({ app, url } = await startApp());
  await reopen(url);
  await page.waitForSelector("#new-task-model-model", { timeout: 15000 });
  await until("the effort chosen before the restart", async () => (await page.inputValue("#new-task-model-effort")) === "medium");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "control");
  // The model's key is in the keychain, which is locked: the picker says which
  // models that is true of, and a task cannot start until it is unlocked.
  await page.waitForFunction(() => [...document.querySelectorAll("#new-task-model-model option")].some((o) => o.textContent.includes("keychain locked")), null, { timeout: 15000 });
  await shows("#new-task-model", "The keychain is locked");
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), true);
  await page.screenshot({ path: `${out}/models-home-locked.png` });
  // The conversation left on the default model, resumed after unlocking.
  await go(`#/task/${id}`);
  await page.waitForSelector(".task[data-loaded]");
  await shows(".task-meta", "reasoner · Medium effort, as it was left");
  await shows(".task-head", "Unlock your keys to resume");
  await page.fill("#keychain-pass", "family passphrase");
  await page.click(".task-head button:text-is('Unlock')");
  await page.waitForSelector(".task-actions button:text-is('Resume'):not([disabled])", { timeout: 20000 });
  await page.click(".task-actions button:text-is('Resume')");
  await openPicker();
  assert.strictEqual(await page.inputValue("#task-model-model"), "control");
  assert.strictEqual(await page.inputValue("#task-model-effort"), "medium", "a resumed conversation lost its effort");
  await page.waitForFunction(() => ![...document.querySelectorAll("#task-model-model option")].some((o) => o.textContent.includes("keychain locked")));
  step("after a restart, locked keys are said, the choice for the next task is kept, and the effort comes back with the conversation");

  // ---------- Anthropic's own API ----------
  // A provider of the Anthropic kind reads its key in x-api-key, takes no
  // dialect, and offers its models no seed and no reply cap field; a model
  // added on it starts with the ask to cache its prompt chosen, and a test of
  // it goes to the Messages API with its effort (ADR 8, amended 2026-10-05).
  // The fake chat provider speaks that API at the same address.
  await go("#/settings/models");
  await page.waitForSelector("#add-provider");
  await page.click("#add-provider");
  await page.click("#preset-anthropic");
  assert.strictEqual(await page.inputValue("#provider-url"), "https://api.anthropic.com/v1");
  assert.strictEqual(await page.locator("#provider-dialect").count(), 0, "Anthropic is offered a dialect");
  await page.fill("#provider-url", chatURL);
  await page.fill("#provider-key", goodKey);
  await page.click("#provider-save");
  // Named after its host, and the host's name is taken by earlier providers.
  await until("the provider to be written", async () => /\[servers\.[^\]]+\]\nprovider = "anthropic"/.test(config()));
  const claude = /\[servers\.([^\]]+)\]\nprovider = "anthropic"/.exec(config())[1];
  await page.waitForSelector(`#provider-${claude}`);
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { provider: "anthropic", url: chatURL, api_key: goodKey });
  assert.ok(config().includes(`[servers.${claude}]\nprovider = "anthropic"\nurl = "${chatURL}"\ncredential = "model-${claude}"\n`), "server not written:\n" + config());
  const claudeBindings = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8");
  assert.strictEqual(claudeBindings.split('positions = ["header:x-api-key"]').length - 1, 2, "binding rows for x-api-key:\n" + claudeBindings);
  assert.ok(!config().includes(goodKey) && !claudeBindings.includes(goodKey) && !(await page.content()).includes(goodKey), "the key is in a file or in the page");
  await shows(`#provider-${claude}`, "Anthropic", "key stored");
  await page.click("#add-model");
  await page.selectOption("#model-provider", claude);
  // More settings are opened first: the form redraws as the provider answers
  // what it lists and publishes, and keeps them open once they are.
  await page.click("#model-more-settings > summary");
  await page.fill("#model-id", "reasoner");
  assert.strictEqual(await page.getAttribute("#model-cache_control-ephemeral", "aria-pressed"), "true", "a model on Anthropic does not start with the ask to cache");
  for (const gone of ["#model-seed", "#model-output_field-default", "#model-cache_key-default"]) assert.strictEqual(await page.locator(gone).count(), 0, `${gone} is offered for Anthropic`);
  await shows("#model-form", "Its API has no seed");
  await page.click("#model-more > summary");
  await page.click("#efforts-lmh");
  await page.selectOption("#model-effort", "high");
  await page.click("#model-save");
  await page.waitForSelector(`[data-model='${claude}-reasoner']`);
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { server: "127-0-0-1-2", model: "reasoner", efforts: ["low", "medium", "high"], effort: "high", settings: { cache_control: "ephemeral" } });
  assert.ok(!/seed|output_field|cache_key/.test(config().split("[models.127-0-0-1-2-reasoner]")[1] || ""), "a setting Anthropic does not read was written:\n" + config());
  await page.click(`[data-model='${claude}-reasoner'] button:text-is('Test')`);
  await page.waitForSelector(`[data-model='${claude}-reasoner'] .test-result.ok`, { timeout: 20000 });
  await shows(`[data-model='${claude}-reasoner'] .test-result`, "reasoner answered", "with High effort");
  const viaMessages = await lastRequest();
  assert.deepStrictEqual([viaMessages.path, viaMessages.key_header, viaMessages.authenticated, viaMessages.effort, viaMessages.answered], ["/v1/messages", "x-api-key", true, "high", 200], "the Messages request: " + JSON.stringify(viaMessages));
  assert.ok(viaMessages.thinking.includes("adaptive") && viaMessages.cache_control.includes("ephemeral"), "thinking or the ask to cache is missing: " + JSON.stringify(viaMessages));
  await page.screenshot({ path: `${out}/models-settings-anthropic.png`, fullPage: true });
  await page.click(`[data-model='${claude}-reasoner'] button:text-is('Remove')`);
  await page.click(`[data-model='${claude}-reasoner'] button:text-is('Remove')`);
  await page.waitForSelector(`[data-model='${claude}-reasoner']`, { state: "detached" });
  await page.click(`#provider-${claude} button:text-is('Remove')`);
  await page.click(`#provider-${claude} button:text-is('Remove')`);
  await page.waitForSelector(`#provider-${claude}`, { state: "detached" });
  step("an Anthropic provider has no dialect, its key bound as x-api-key, its models no seed or reply cap field, and a test goes to the Messages API with its effort");

  // ---------- a provider that does not answer ----------
  chat.kill("SIGTERM");
  await chat.exited;
  await go("#/settings/models");
  await page.waitForSelector("#provider-127-0-0-1");
  await page.click("#check-again");
  await page.waitForSelector("#provider-127-0-0-1 .state-word:text-is('not answering')", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("models.status").at(-1), { fresh: true });
  await shows("#provider-127-0-0-1", "No answer from");
  await shows("[data-model='control']", "not answering");
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.waitForFunction(() => [...document.querySelectorAll("#new-task-model-model option")].some((o) => o.textContent.includes("not answering")));
  await shows("#new-task-model", "No answer from");
  // It may be back by the time the task is asked: a warning, not a stop. The
  // task fails where the model is and says so; it is not given to another one.
  assert.strictEqual(await page.isDisabled("button[aria-label='Start the task']"), true, "nothing typed yet");
  await page.fill("#task", "Anything at all");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Stopped:"), null, { timeout: 30000 });
  await idle();
  assert.ok(!/REQUEST POST \/api\/chat/.test(ollama.stdoutText), "a request that failed on the chosen provider was asked of another model");
  step("a provider that does not answer is said so, and its task fails there, not on another model");

  // ---------- models listed, none chosen as the default ----------
  // A config.toml with [models.NAME] and no [control] lists the model and says
  // none is the default; choosing one is what saves it.
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  fs.writeFileSync(configFile, `[models.demo]\nprovider = "ollama"\nurl = "${ollamaURL}"\nmodel = "demo"\n`);
  ({ app, url } = await startApp());
  await reopen(url);
  await go("#/settings/models");
  await page.waitForSelector("#default-model-model", { timeout: 15000 });
  assert.strictEqual(await selected("#default-model-model"), "No model");
  assert.strictEqual(await page.$eval(".save-bar", (b) => b.hidden), true, "nothing to save yet");
  await shows("[data-model='demo']", "demo");
  await page.selectOption("#default-model-model", "demo");
  await saveBar();
  assert.deepStrictEqual(sent.of("settings.control.write").at(-1), { name: "demo", effort: "" });
  assert.ok(config().includes('[control]\nprovider = "ollama"'), "no default written:\n" + config());
  step("models listed with no default: none is shown as chosen, and choosing one saves it");

  // ---------- what a provider publishes ----------
  // A model on OpenAI's address whose entry says nothing of it has what
  // OpenAI publishes, as nodwyn's snapshot of models.dev has it (ADR 41,
  // amended 2026-10-05): its context, its reply cap, its efforts, its prices.
  // The expectations are read from that snapshot, so regenerating it does not
  // change what this checks.
  const snapshot = JSON.parse(fs.readFileSync(`${__dirname}/../../../internal/catalogue/models.json`, "utf8")).providers;
  const published = snapshot.openai;
  const [first, second, third] = Object.keys(published)
    .filter((id) => published[id].context && published[id].output && published[id].price && published[id].efforts?.length > 1)
    .sort();
  // A model its provider publishes as reading no images.
  const textOnly = Object.keys(published)
    .filter((id) => published[id].context && !published[id].images)
    .sort()[0];
  // A Claude model with efforts, and a model on Baseten, found by their addresses.
  const usable = (table) => Object.keys(table).filter((id) => table[id].context && table[id].output && table[id].price).sort();
  const claudeModel = usable(snapshot.anthropic).find((id) => snapshot.anthropic[id].efforts?.length > 1);
  const basetenModel = usable(snapshot.baseten)[0];
  const n = (x) => x.toLocaleString("en-US");
  const label = (e) => (e === "xhigh" ? "Extra high" : e[0].toUpperCase() + e.slice(1));
  const expect = (id, table = published) => {
    const m = table[id];
    const cap = Math.min(m.output, Math.floor(m.context / 4));
    const tier = m.price.tiers?.length ? ` (more past ${n(m.price.tiers[0].above)})` : "";
    return { m, cap, line: `context ${n(m.context)} tokens · replies up to ${n(cap)} tokens · $${m.price.input} in, $${m.price.output} out per million tokens${tier}${m.images ? "" : " · takes no images"} · as published` };
  };
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  fs.writeFileSync(configFile, `${config()}\n[servers.openai]\nprovider = "openai"\ndialect = "responses"\nurl = "https://api.openai.com/v1"\n\n[models.openai-a]\nserver = "openai"\nmodel = "${first}"\n\n[models.openai-c]\nserver = "openai"\nmodel = "${third}"\nefforts = ["low", "high"]\n\n[models.openai-text]\nserver = "openai"\nmodel = "${textOnly}"\n\n[servers.anthropic]\nprovider = "anthropic"\nurl = "https://api.anthropic.com/v1"\n\n[models.claude-a]\nserver = "anthropic"\nmodel = "${claudeModel}"\n\n[servers.baseten]\nprovider = "openai"\nurl = "https://inference.baseten.co/v1"\n\n[models.baseten-a]\nserver = "baseten"\nmodel = "${basetenModel}"\n`);
  ({ app, url } = await startApp());
  await reopen(url);
  await go("#/settings/models");
  await page.waitForSelector("[data-model='openai-a']", { timeout: 15000 });
  const a = expect(first);
  await shows("[data-model='openai-a']", a.line);
  await page.click("[data-model='openai-a'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  assert.strictEqual(await page.getAttribute("#efforts-published", "aria-pressed"), "true", "the published efforts are not the model's");
  await shows("#model-form", `Offered as ${a.m.efforts.map(label).join(", ")}, as its provider publishes them`);
  assert.strictEqual(await page.getAttribute("#model-num_ctx", "placeholder"), n(a.m.context));
  assert.strictEqual(await page.getAttribute("#model-max_output", "placeholder"), n(a.cap));
  assert.strictEqual(await page.getAttribute("#model-price_input", "placeholder"), String(a.m.price.input));
  await shows("#model-window", `Of ${n(a.m.context)} tokens, each reply may take ${n(a.cap)}; the conversation is summarized or trimmed past ${n(Math.floor(((a.m.context - a.cap) * 3) / 4))}`);
  await page.locator("#model-form").screenshot({ path: `${out}/models-published-form.png` });
  await page.click("#model-form >> text=Cancel");
  // Efforts typed by hand before nodwyn knew the provider's are kept, and the
  // form says what the provider publishes; As published follows it, and the
  // entry no longer lists its own.
  await page.click("[data-model='openai-c'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  assert.strictEqual(await page.getAttribute("#efforts-published", "aria-pressed"), "false");
  await shows("#efforts-published-differ", `Its provider publishes ${published[third].efforts.map(label).join(", ")}`);
  await page.click("#efforts-published");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "openai-c", server: "openai", model: third, efforts: [], labels: [], settings: {} });
  assert.ok(!(config().split("[models.openai-c]")[1] || "").split("\n[")[0].includes("efforts"), "the typed efforts stayed:\n" + config());
  // A model its provider publishes as reading no images shows No until its
  // entry says otherwise, and Yes is saved, so the entry overrides the
  // catalogue.
  await shows("[data-model='openai-text']", "takes no images");
  await page.click("[data-model='openai-text'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  assert.strictEqual(await page.getAttribute("#model-vision-false", "aria-pressed"), "true", "a model published as reading no images shows Yes");
  await shows("#model-form", "Unless you choose, it is what its provider publishes: it reads none.");
  await page.click("#model-vision-true");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "openai-text", server: "openai", model: textOnly, efforts: [], labels: [], settings: { vision: true } });
  assert.ok((config().split("[models.openai-text]")[1] || "").split("\n[")[0].includes("vision = true\n"), "Yes was not saved:\n" + config());
  await page.waitForFunction(() => !document.querySelector("[data-model='openai-text']")?.innerText.includes("takes no images"), null, { timeout: 10000 });
  // A price typed alone is kept, and the other is still the published one.
  // On the Responses API, which reads no reply cap field and has no seed,
  // the form offers no field and says so of the seed.
  await page.click("[data-model='openai-a'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  await page.click("#model-more-settings > summary");
  assert.strictEqual(await page.locator("#model-output_field-default").count(), 0, "a Responses model is offered a reply cap field it does not read");
  await shows("#model-form", "The Responses API has no seed");
  await page.fill("#model-price_output", "99");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "openai-a", server: "openai", model: first, efforts: [], labels: [], settings: { price_output: 99 } });
  assert.ok((config().split("[models.openai-a]")[1] || "").split("\n[")[0].includes("price_output = 99"), "the output price was not saved:\n" + config());
  await shows("[data-model='openai-a']", `$${a.m.price.input} in, $99 out per million tokens`);
  // A model added on the provider takes its published efforts once its id is
  // given, and saves none of its own: the provider's apply.
  const b = expect(second);
  await page.click("#add-model");
  await page.selectOption("#model-provider", "openai");
  await page.fill("#model-id", second);
  await page.press("#model-id", "Tab");
  // The efforts are under the fold, which is closed when adding a model.
  await page.waitForSelector("#efforts-published[aria-pressed=true]", { state: "attached", timeout: 10000 });
  assert.deepStrictEqual(sent.of("models.published").at(-1), { server: "openai", model: second });
  await page.click("#model-more > summary");
  await shows("#model-form", `Offered as ${b.m.efforts.map(label).join(", ")}, as its provider publishes them`);
  await page.click("#model-save");
  await page.waitForSelector(`[data-model='openai-${second.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}']`, { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { server: "openai", model: second, efforts: [] });
  const added = (config().split(`model = "${second}"`)[1] || "").split("\n[")[0];
  assert.ok(!/efforts|num_ctx|max_output|price_/.test(added), "the published settings were written to the entry:\n" + config());
  await shows(`[data-model='openai-${second.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}']`, b.line);
  // The New task window offers its published efforts.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.selectOption("#new-task-model-model", `openai-${second.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`);
  assert.deepStrictEqual(
    (await page.$$eval("#new-task-model-effort option", (o) => o.map((x) => x.value))).filter(Boolean),
    b.m.efforts,
    "the New task window does not offer the published efforts",
  );
  // Anthropic's and Baseten's models are described the same way, by their own
  // addresses; a Claude model's efforts read as what its API takes them for.
  await go("#/settings/models");
  await page.waitForSelector("[data-model='claude-a']");
  await shows("[data-model='claude-a']", expect(claudeModel, snapshot.anthropic).line);
  await shows("[data-model='baseten-a']", expect(basetenModel, snapshot.baseten).line);
  await page.click("[data-model='claude-a'] button:text-is('Edit')");
  await page.waitForSelector("#model-form");
  await shows("#model-form", `Offered as ${snapshot.anthropic[claudeModel].efforts.map(label).join(", ")}, as its provider publishes them`, "output_config.effort, with adaptive thinking, to Anthropic");
  assert.strictEqual(await page.getAttribute("#model-price_input", "placeholder"), String(snapshot.anthropic[claudeModel].price.input));
  await page.click("#model-form >> text=Cancel");
  step("a model Anthropic or Baseten publishes has its context, reply cap, efforts and prices without any being typed");

  // ---------- the judge (ADR 46, ADR 47) ----------
  // Settings › Judge sets a judge up in place: Jev with its key alone, since
  // its model and its price are known, so nobody types either. Its key is
  // stored and allowed for typesafe.ai by POST alone; it is no model
  // provider, so Models neither lists it nor offers it for a model; and
  // added when there is no judge, it becomes the default. A provider without
  // a key is no judge. A new task and a conversation choose their judge
  // beside their model. A judge model config.toml names that the provider
  // does not answer to is said, and put right in one click; a price can be
  // set for the default judge. The judge is put back to none at the end, so
  // nothing later asks typesafe.ai anything. The app was last started with
  // the keychain locked, and a key needs it.
  await go("#/settings/keys");
  await page.waitForSelector("#section-keys #keychain-pass", { timeout: 10000 });
  await page.fill("#section-keys #keychain-pass", "family passphrase");
  await page.click("#section-keys button:text-is('Unlock')");
  await page.waitForSelector("#section-keys >> text=Unlocked", { timeout: 20000 });
  await go("#/settings/judge");
  await page.waitForSelector("#section-judge");
  await shows("#section-judge", "New tasks start with no judge", "No judge is set up yet.");
  assert.deepStrictEqual(await page.$$eval("#section-judge [aria-label='Judge provider'] button", (b) => b.map((x) => x.textContent)), ["No judge"], "a provider without a key offered as a judge");
  assert.strictEqual(await page.locator("[id='set-judge.model']").count(), 0, "the judge's model is a field to type");
  await page.fill("#judge-key-jev", "sk-judge-canary-0001");
  await page.click("#judge-add-jev");
  await page.waitForSelector("#judge-Jev", { timeout: 10000 });
  await page.waitForSelector(".toast >> text=Jev added. Save to have new tasks ask it.", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.provider.write").at(-1), { name: "Jev", provider: "jev", url: "https://api.typesafe.ai", api_key: "sk-judge-canary-0001" });
  // The key went at once, as keys do; making the judge the default is a
  // setting, so it waits in the bar like the others.
  await shows(".save-bar", "1 unsaved change");
  assert.strictEqual(sent.of("settings.write").length, 0, "the default judge was written before the bar saved it");
  await saveBar();
  assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "judge.server": "Jev" } });
  assert.ok(config().includes('[servers.Jev]\nprovider = "jev"\nurl = "https://api.typesafe.ai"\ncredential = "model-api-typesafe-ai"\n'), "the Jev provider in config.toml:\n" + config());
  assert.ok(/\[judge\]\nserver = "Jev"\n/.test(config()), "the default judge is not in [judge]:\n" + config());
  assert.ok(!config().includes("sk-judge-canary"), "the Jev key is in config.toml");
  const jevRows = fs.readFileSync(`${root}/config/nodwyn/bindings.toml`, "utf8").split("[[bind]]").filter((b) => b.includes('class = "model-api-typesafe-ai"'));
  assert.ok(jevRows.length === 1 && /methods = \["POST"\]/.test(jevRows[0]) && jevRows[0].includes('dest = "api.typesafe.ai"'), "the Jev key's binding rows:\n" + jevRows.join("[[bind]]"));
  await shows("#judge-Jev", "Jev · api.typesafe.ai · jev-latest", "$0.042 per million input tokens, output free", "ready");
  await shows("#section-judge", "New tasks ask Jev. Its key is stored, unlocked and allowed for api.typesafe.ai");
  assert.ok(await page.isDisabled("#judge-Jev button:text-is('Remove')"), "the default judge can be removed");
  assert.strictEqual(await page.locator("#judge-add-card-jev").count(), 0, "Jev is offered again once it is set up");
  await page.locator("#section-judge").screenshot({ path: `${out}/models-judge.png` });
  await go("#/settings/models");
  await page.waitForSelector("#section-models");
  assert.strictEqual(await page.locator("#provider-Jev").count(), 0, "Models lists the Jev provider");
  await page.click("#add-model");
  assert.ok(!(await page.$$eval("#model-provider option", (o) => o.map((x) => x.value))).includes("Jev"), "a model can be put on the Jev provider");
  await page.click("#model-form >> text=Cancel");
  await page.click("#add-provider");
  assert.strictEqual(await page.locator("#preset-jev").count(), 0, "Models offers Jev as a model provider");
  await page.click("#provider-form >> text=Cancel");
  // The judge new tasks ask is set in one place, the Judge tab: Models only
  // links to it.
  assert.strictEqual(await page.isVisible("#default-model-judge"), false, "Models sets the default judge as well as Judge");
  assert.ok(await page.locator("#default-model a[href='#/settings/judge'], #section-models a[href='#/settings/judge']").count(), "Models links to the Judge tab");
  step("Jev is added as a judge in place, its model and price known, and becomes the default; Models keeps it out");

  // A new task and a conversation choose their approvals beside their model
  // (ADR 35, amended 2026-10-30): who answers commands says whether the
  // judge is asked at all, so with one judge set up there is no judge to
  // pick, and asking or allowing all asks it nothing: the tasks here start
  // on "Allow all", so nothing asks typesafe.ai.
  await go("#/");
  await page.waitForSelector("#new-task-model-commands");
  assert.strictEqual(await page.inputValue("#new-task-model-commands"), "judge", "a new task does not start as Settings answers commands");
  assert.strictEqual(await page.locator("#new-task-model-judge").isVisible(), false, "a judge is offered to pick with only one set up");
  assert.strictEqual(await page.locator("#new-task-commands").count(), 0, "Options still has a Commands select");
  await page.selectOption("#new-task-model-commands", "allow");
  await until("the choice kept for the next task", () => sent.of("newtask.last").at(-1)?.choices?.hosts?.some((e) => e.host === "" && e.commands === "allow"));
  await page.fill("#task", "Say hello");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  assert.strictEqual(sent.of("session.open").at(-1).commands, "allow", "who answers commands was not sent");
  const judged = sent.of("session.send").at(-1).id;
  await idle();
  await openPicker();
  assert.strictEqual(await page.inputValue("#task-model-commands"), "allow", "the task does not answer its commands as it was opened to");
  assert.strictEqual(await page.locator("#task-model-judge").isVisible(), false, "a judge is offered beside Allow all");
  await shows("#task-model summary", "allow all");
  await page.selectOption("#task-model-commands", "judge-auto");
  await page.waitForSelector(".toast >> text=Approvals: judge only, from this task's next command.", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("session.commands.set").at(-1), { id: judged, commands: "judge-auto" });
  assert.strictEqual(sent.of("session.judge.set").length, 0, "the task's judge was chosen again though it had one");
  await shows("#task-model summary", "judge only · judged by Jev");
  step("a new task and a conversation choose their approvals beside their model, and the judge only beside the answers that ask it");

  // The next task starts with the choice made last, after an app restart
  // too.
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  fs.writeFileSync(configFile, config().replace('[judge]\nserver = "Jev"\n', '[judge]\nserver = "Jev"\nmodel = "Jev"\n'));
  ({ app, url } = await startApp());
  await reopen(url);
  await page.waitForSelector("#task", { timeout: 15000 });
  await page.waitForSelector("#new-task-model-commands", { timeout: 15000 });
  assert.strictEqual(await page.inputValue("#new-task-model-commands"), "allow", "the next task did not start with the approvals chosen last");
  await page.selectOption("#new-task-model-commands", "judge");
  await until("the default kept for the next task", () => !sent.of("newtask.last").at(-1)?.choices?.hosts?.some((e) => e.commands));
  step("the approvals chosen last are the next task's, after an app restart too");
  await go("#/settings/judge");
  await page.waitForSelector("#judge-model-warning", { timeout: 10000 });
  await shows("#judge-model-warning", "config.toml asks Jev for the model “Jev”", "Jev answers to jev-latest");
  await shows("#judge-Jev", "Jev · api.typesafe.ai · Jev", "price unknown");
  await page.click("#judge-model-fix");
  await page.click(".save-bar button:text-is('Save')");
  await page.waitForSelector(".save-bar", { state: "hidden", timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "judge.model": null } });
  assert.ok(!/^model = "Jev"/m.test(config()), "the wrong judge model stays:\n" + config());
  await page.waitForSelector("#judge-model-warning", { state: "detached", timeout: 10000 });
  await shows("#judge-Jev", "Jev · api.typesafe.ai · jev-latest", "$0.042 per million input tokens");
  step("a judge model the provider does not answer to is said, and put right in one click");

  // A price for the default judge is its own, under a fold: it overrides
  // the published one, and empty puts that back.
  await page.click("#judge-tuning > summary");
  assert.strictEqual(await page.getAttribute("[id='set-judge.price_input']", "placeholder"), "0.042", "the published input price");
  await page.fill("[id='set-judge.price_input']", "0.05");
  await page.click(".save-bar button:text-is('Save')");
  await page.waitForSelector(".save-bar", { state: "hidden", timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "judge.price_input": 0.05 } });
  assert.ok(/\[judge\][^[]*price_input = 0\.05/.test(config()), "the judge's price is not in [judge]:\n" + config());
  await shows("#judge-Jev", "$0.05 per million input tokens");
  await page.click("#section-judge [id='set-judge.server-default']");
  await page.click(".save-bar button:text-is('Save')");
  await page.waitForSelector(".save-bar", { state: "hidden", timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.write").at(-1), { set: { "judge.server": null } });
  assert.ok(!/^server = "Jev"/m.test(config()), "the judge still names Jev:\n" + config());
  await page.waitForSelector("#section-judge >> text=New tasks start with no judge", { timeout: 10000 });
  step("a price for the default judge overrides the published one, and the default is put back to none");

  // With no default judge, an answer that asks one says a judge is still to
  // be chosen, and choosing such an answer chooses the judge that can be
  // asked, so the answer picked is the one the task gets.
  await go("#/");
  await page.waitForSelector("#new-task-model-commands");
  assert.strictEqual(await page.inputValue("#new-task-model-commands"), "judge", "the approvals Settings starts tasks with");
  await page.waitForSelector("#new-task-model-judge", { state: "visible" });
  assert.strictEqual(await page.inputValue("#new-task-model-judge"), "", "a judge is shown as chosen with no default");
  await page.selectOption("#new-task-model-commands", "judge-auto");
  await until("the judge kept with the approvals", () => sent.of("newtask.last").at(-1)?.choices?.hosts?.some((e) => e.host === "" && e.commands === "judge-auto" && e.judge === "Jev"));
  // Jev's key is locked since the restart: it is chosen all the same, and
  // the picker says it cannot be asked yet.
  await shows("#new-task-model .mp-notes", "Until it can be asked, you answer the commands.");
  await page.selectOption("#new-task-model-commands", "allow");
  await page.waitForFunction(() => !document.querySelector("#new-task-model .mp-notes").textContent.includes("you answer the commands"), null, { timeout: 10000 });
  await page.selectOption("#new-task-model-commands", "judge");
  step("an answer that asks a judge, chosen with none, chooses one, and says when it cannot be asked yet");

  // The ChatGPT setup UI uses synthetic RPC consent and account replies;
  // provider persistence and binding are checked by the RPC integration test.
  // Intercept before reloading so no sign-in can reach a real account.
  const planCalls = [];
  let planSettings, planSaved = false, planConsent = 0, planPolls = 0, planAuthCredential = "fake-plan-one", planUnlocked = false;
  const planAccounts = [{ id: "fake-plan-one", class: "chatgpt", kind: "oauth2", provider: "openai", refreshable: true }, { id: "fake-plan-two", class: "work-chatgpt", kind: "oauth2", provider: "openai", refreshable: true }];
  await page.routeWebSocket("**/socket", (socket) => {
    const server = socket.connectToServer();
    const methods = new Map();
    const reply = (frame, result, error) => socket.send(JSON.stringify({ jsonrpc: "2.0", id: frame.id, ...(error ? { error: { code: -32000, message: error } } : { result }) }));
    socket.onMessage((message) => {
      const frame = JSON.parse(message);
      if (frame.method === "keychain.unlock") {
        planUnlocked = true;
        return reply(frame, { exists: true, unlocked: true, credentials: 2 });
      }
      if (frame.method === "keychain.status") return reply(frame, { exists: true, unlocked: planUnlocked, credentials: 2 });
      if (frame.method === "credentials.list") {
        planPolls++;
        return reply(frame, { keychain: { exists: true, unlocked: planUnlocked }, credentials: !planUnlocked ? [] : planConsent > 1 ? planAccounts : [planAccounts[1]], authorizations: planConsent > 1 ? [{ id: "fake-plan-auth", state: planPolls > 2 ? "done" : "waiting", credential: planAuthCredential }] : [] });
      }
      if (frame.method === "credentials.authorize") {
        planCalls.push(frame);
        planConsent++;
        planAuthCredential = frame.params.id || "fake-plan-one";
        if (planConsent === 1) return reply(frame, null, "Fake OpenAI consent refused");
        return reply(frame, { id: "fake-plan-auth", url: "about:blank#fake-consent" });
      }
      if (frame.method === "settings.provider.write") {
        planCalls.push(frame);
        const p = planSettings.servers.find((p) => p.name === frame.params.name);
        if (frame.params.credential) {
          p.authentication = "chatgpt";
          p.credential = planAccounts.find((c) => c.id === frame.params.credential).class;
        } else {
          delete p.authentication;
          p.credential = "fake-static-class";
        }
        p.key = "ready";
        planSaved = true;
        return reply(frame, planSettings);
      }
      if (frame.method === "settings.read" && planSaved) return reply(frame, planSettings);
      methods.set(frame.id, frame.method);
      server.send(message);
    });
    server.onMessage((message) => {
      const frame = JSON.parse(message);
      if (methods.get(frame.id) === "settings.read" && frame.result) {
        if (planSaved) return reply(frame, planSettings);
        planSettings = structuredClone(frame.result);
      }
      socket.send(message);
    });
  });
  await go("#/settings/models");
  await page.reload();
  await page.waitForSelector("#provider-openai");
  await page.click("#provider-openai button:text-is('Edit')");
  // The form opens in the card whose Edit was clicked, and the keychain's
  // form beside it, in that card and outside the provider form.
  await page.waitForSelector("#provider-openai > #provider-form");
  assert.strictEqual(await page.locator(".provider-card:not(#provider-openai) #provider-form").count(), 0, "the provider form opened in another card");
  await page.click("#provider-auth-chatgpt");
  await page.waitForSelector("#provider-account");
  assert.ok(await page.isDisabled("#provider-connect-chatgpt"), "sign-in allowed with a locked keychain");
  assert.ok(await page.isDisabled("#provider-save"));
  assert.strictEqual(await page.locator("#provider-openai > #provider-keychain").count(), 1, "the keychain's form is not beside the provider form in its card");
  assert.strictEqual(await page.locator("#provider-form #provider-keychain").count(), 0, "the keychain's form is inside the provider form");
  await page.fill("#keychain-pass", "fake-ui-passphrase");
  await page.click("button:text-is('Unlock')");
  await page.waitForSelector("#provider-account option[value='fake-plan-two']", { state: "attached" });
  assert.ok(await page.isDisabled("#provider-url"));
  assert.ok(await page.isDisabled("#provider-dialect"));
  assert.strictEqual(await page.locator("#provider-key").count(), 0, "plan setup asks for an API key");
  assert.ok(await page.isDisabled("#provider-save"), "plan provider saves before an account is selected");
  await page.click("#provider-connect-chatgpt");
  await page.waitForSelector("#provider-form .error");
  assert.ok((await page.innerText("#provider-form .error")).includes("Fake OpenAI consent refused"));
  assert.deepStrictEqual(planCalls[0].params, { provider: "openai", class: "chatgpt" });
  await page.click("#provider-connect-chatgpt");
  await page.waitForFunction(() => document.querySelector("#provider-account")?.value === "fake-plan-one");
  assert.ok(await page.isEnabled("#provider-save"));
  await page.locator("#provider-form").screenshot({ path: `${out}/models-chatgpt-connected.png` });
  await page.selectOption("#provider-account", "fake-plan-two");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form", { state: "hidden" });
  assert.deepStrictEqual(planCalls.filter((f) => f.method === "settings.provider.write").at(-1).params, { provider: "openai", url: "https://api.openai.com/v1", name: "openai", dialect: "responses", credential: "fake-plan-two" });
  await shows("#provider-openai", "ChatGPT plan");
  await page.click("#provider-openai button:text-is('Edit')");
  await page.waitForFunction(() => document.querySelector("#provider-account")?.value === "fake-plan-two");
  await page.click("#provider-reauthorize");
  await page.waitForFunction(() => !document.querySelector("#provider-connect-chatgpt")?.disabled);
  assert.strictEqual(await page.inputValue("#provider-account"), "fake-plan-two");
  assert.deepStrictEqual(planCalls.filter((f) => f.method === "credentials.authorize").at(-1).params, { id: "fake-plan-two" });
  await page.click("#provider-auth-api_key");
  await page.waitForSelector("#provider-key");
  assert.ok(await page.isDisabled("#provider-save"), "switching to API billing silently retained the plan");
  await page.fill("#provider-key", "fake-switch-api-key");
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form", { state: "hidden" });
  const switched = planCalls.filter((f) => f.method === "settings.provider.write").at(-1).params;
  assert.strictEqual(switched.api_key, "fake-switch-api-key");
  assert.ok(!("credential" in switched));
  assert.ok(!(await page.innerText("#provider-openai")).includes("ChatGPT plan"));
  step("ChatGPT provider UI handles refused consent, sign-in completion, account selection, reauthorization, save and switching to API key billing with fake RPC replies");

  // ---------- the phone: nothing sideways ----------
  await page.setViewportSize({ width: 390, height: 844 });
  for (const hash of ["#/", "#/settings", "#/settings/models", "#/settings/judge", `#/task/${fastId}`]) {
    await go(hash);
    await page.waitForTimeout(300);
    const wide = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    assert.ok(wide <= 1, `${hash} is ${wide}px wider than the phone`);
  }
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.screenshot({ path: `${out}/models-home-phone.png` });
  assert.deepStrictEqual(fs.existsSync(`${root}/work/.nodwyn`), false, "choosing a model wrote into the folder");
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");

  // Status notes and usage remain readable after opening their Activity
  // row, rather than needing to be permanently visible in the conversation.
  async function openActivity(text) {
    const fold = page.locator(".activity-fold").filter({ hasText: text }).last();
    if (await fold.getAttribute("open") === null) await fold.locator(":scope > summary").click();
  }

  async function shows(selector, ...texts) {
    const text = await page.innerText(selector);
    for (const want of texts) assert.ok(text.includes(want), `${selector} does not show ${JSON.stringify(want)}; it shows:\n${text.slice(0, 1200)}`);
  }
});
