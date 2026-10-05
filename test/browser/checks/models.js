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
//   sees that model with that effort. A choice left unsent survives going to
//   Settings and back and a reload, and a model removed meanwhile is said to be
//   gone instead of silently replaced.
// - In the conversation the effort is changed between messages; one chosen
//   while a response is held waits for it, says so, and the provider sees the
//   old effort on the response under way and the new one on the next. The
//   choice survives a reload.
// - A closed task whose model was removed opens without a model: nothing is
//   sent until one is chosen.
// - The app restarted with the keychain locked says which models need it, a
//   conversation resumed after unlocking has the effort it was left on, and a
//   provider that stops answering is said not to answer: a task started on it
//   fails there, and nothing is ever asked of the other model.
const assert = require("assert");
const fs = require("fs");
const { out, start, line, tempDir, runtimeDir, launch, watch, requests, until, check } = require("../lib");

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
  let { app, url } = await startApp();
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1280, height: 1000 } }));
  const sent = requests(page);
  const step = (m) => console.log("step:", m);
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
  await go("#/settings");
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
  await page.click("#preset-other");
  assert.strictEqual(await page.inputValue("#provider-dialect"), "", "another server does not keep the standard dialect");
  await page.fill("#provider-url", chatURL);
  await shows("#provider-form", "Create or unlock the keychain first");
  await page.click("#provider-form >> text=Cancel");
  await page.waitForSelector("#keychain-pass");
  await page.fill("#keychain-pass", "family passphrase");
  await page.fill("#keychain-again", "family passphrase");
  await page.click("text=Create the keychain");
  await page.waitForSelector("text=Unlocked", { timeout: 20000 });
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
  await shows("#model-form", "did not accept the key (401): replace it. Type the model's name.");
  assert.strictEqual(await page.locator("#model-id-options option").count(), 0, "a provider that refused the key offered models");
  await page.fill("#model-id", "reasoner");
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

  // ---------- the key is replaced; the same test now passes ----------
  await page.click("#provider-127-0-0-1 button:text-is('Edit')");
  await page.fill("#provider-key", goodKey);
  await page.click("#provider-save");
  await page.waitForSelector("#provider-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.provider.write")[1], { name: "127-0-0-1", provider: "openai", url: chatURL, api_key: goodKey });
  assert.ok(!(await page.content()).includes(goodKey), "the page shows the key");
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

  // ---------- the default model and its effort ----------
  await page.selectOption("#default-model-model", "127-0-0-1-reasoner");
  assert.strictEqual(await page.isDisabled("#default-model-effort"), false);
  await page.selectOption("#default-model-effort", "high");
  await page.click("#default-save");
  await page.waitForSelector("#default-save:disabled");
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
  await page.click("#model-form .more-settings > summary");
  await page.click("#model-cache_control-ephemeral");
  await page.locator("#model-form").screenshot({ path: `${out}/models-settings-form.png` });
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), {
    server: "127-0-0-1",
    model: "reasoner",
    efforts: ["low", "medium", "high"],
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
  await page.click("#model-form .more-settings > summary");
  assert.strictEqual(await page.getAttribute("#model-cache_control-ephemeral", "aria-pressed"), "true");
  await page.click("#model-form >> text=Cancel");
  // The other model takes no images: said for it alone.
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Edit')");
  await page.click("#model-vision-false");
  await page.click("#model-save");
  await page.waitForSelector("#model-form", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "127-0-0-1-fast", server: "127-0-0-1", model: "fast", efforts: [], settings: { vision: false } });
  assert.ok(block("models.127-0-0-1-fast").includes("vision = false\n") && !/vision/.test(block("models.127-0-0-1-reasoner")), "vision is not the fast model's alone:\n" + config());
  await shows("[data-model='127-0-0-1-fast']", "takes no images");
  assert.ok(!(await page.innerText("[data-model='control']")).includes("takes no images"), "the default took the other model's word on images");
  await page.locator("#section-models").screenshot({ path: `${out}/models-settings-per-model.png` });
  step("each model has its own context, reply cap, prices, images and ask to cache; [control] holds none of them");

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
  await go("#/settings");
  await page.waitForSelector("#default-model");
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "127-0-0-1-fast", "the choice was lost going to Settings and back");
  await page.reload();
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "127-0-0-1-fast", "the choice was lost in a reload");
  step("an unsent choice survives navigation and a reload");

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

  // The default is back for the next task: a choice is consumed by the task it was for.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  assert.strictEqual(await page.inputValue("#new-task-model-model"), "control", "a new task did not start on the default");

  // Close the first task; it is resumed after its model is removed, below.
  await go(`#/task/${fastId}`);
  await page.waitForSelector(".task[data-loaded]");
  await page.click(".task-actions button:text-is('Close')");
  await page.waitForSelector(".task-actions button:text-is('Resume')");
  await shows(".task-meta", "fast, as it was left");
  step("a closed task shows the model it was left on");

  // ---------- a task on the default model, with another effort ----------
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.selectOption("#new-task-model-effort", "low");
  await page.fill("#task", "Say hello");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".task[data-loaded]", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.open").at(-1), { repo: `${root}/work`, name: "control", effort: "low" });
  await idle();
  const id = sent.of("session.send").at(-1).id;
  await page.waitForFunction(() => document.querySelector(".transcript").innerText.includes("Answered by reasoner with low effort."));
  assert.strictEqual(await page.inputValue("#task-model-model"), "control");
  assert.strictEqual(await page.inputValue("#task-model-effort"), "low");
  await shows(".transcript", "Model: reasoner · low effort");
  step("a task started with an effort asks the provider for it");
  // The provider says neither the context nor the cost; the model's own
  // settings do: the window is sized for 1,050,000 tokens less its
  // 128,000-token reply cap, as the form said, and the reply is priced (an
  // estimate, ≈).
  await page.waitForFunction(() => document.body.textContent.includes("691,500"), null, { timeout: 10000 });
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
  assert.match(await page.innerText(".transcript"), /\d+% cached/, "the transcript does not say the prompt was read from the cache");
  step("a model told to cache its prompt asks for it on every request, and is read back from the cache");
  step("a choice made while a response ran waited for it: the provider saw the old effort, then the new");

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
    // The console offers no way to ask for it; the daemon refuses it all the same.
    const socket = new WebSocket(`ws://${location.host}/socket`);
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

  // ---------- a model that is removed while it is in use ----------
  // The task on "fast" is closed; "fast" leaves the list; opening the task
  // again does not hand it to another model.
  await go("#/settings");
  await page.waitForSelector("[data-model='127-0-0-1-fast']");
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Remove')");
  await page.click("[data-model='127-0-0-1-fast'] button:text-is('Remove')"); // the confirm
  await page.waitForSelector("[data-model='127-0-0-1-fast']", { state: "detached" });
  assert.deepStrictEqual(sent.of("settings.model.remove"), [{ name: "127-0-0-1-fast" }]);
  assert.ok(!config().includes("fast"), "the model is still in config.toml");
  // The unsent choice was "fast" once: it is gone, and the page says so.
  await go("#/");
  await page.waitForSelector("#new-task-model-model");
  await page.evaluate(() => localStorage.setItem("nodwyn-new-task-model", JSON.stringify({ name: "127-0-0-1-fast", effort: "" })));
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
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  ({ app, url } = await startApp());
  await page.goto(url);
  await page.waitForSelector("#new-task-model-model", { timeout: 15000 });
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
  step("after a restart, locked keys are said and the effort comes back with the conversation");

  // ---------- a provider that does not answer ----------
  chat.kill("SIGTERM");
  await chat.exited;
  await go("#/settings");
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
  await page.goto(url);
  await go("#/settings");
  await page.waitForSelector("#default-model-model", { timeout: 15000 });
  assert.strictEqual(await selected("#default-model-model"), "No model");
  assert.strictEqual(await page.isDisabled("#default-save"), true, "nothing to save yet");
  await shows("[data-model='demo']", "demo");
  await page.selectOption("#default-model-model", "demo");
  await page.click("#default-save");
  await page.waitForSelector("text=Saved. New tasks start on this model", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("settings.control.write").at(-1), { name: "demo", effort: "" });
  assert.ok(config().includes('[control]\nprovider = "ollama"'), "no default written:\n" + config());
  step("models listed with no default: none is shown as chosen, and choosing one saves it");

  // ---------- what a provider publishes ----------
  // A model on OpenAI's address whose entry says nothing of it has what
  // OpenAI publishes, as nodwyn's snapshot of models.dev has it (ADR 41,
  // amended 2026-10-05): its context, its reply cap, its efforts, its prices.
  // The expectations are read from that snapshot, so regenerating it does not
  // change what this checks.
  const published = JSON.parse(fs.readFileSync(`${__dirname}/../../../internal/catalogue/models.json`, "utf8")).providers.openai;
  const [first, second, third] = Object.keys(published)
    .filter((id) => published[id].context && published[id].output && published[id].price && published[id].efforts?.length > 1)
    .sort();
  const n = (x) => x.toLocaleString("en-US");
  const label = (e) => (e === "xhigh" ? "Extra high" : e[0].toUpperCase() + e.slice(1));
  const expect = (id) => {
    const m = published[id];
    const cap = Math.min(m.output, Math.floor(m.context / 4));
    const tier = m.price.tiers?.length ? ` (more past ${n(m.price.tiers[0].above)})` : "";
    return { m, cap, line: `context ${n(m.context)} tokens · replies up to ${n(cap)} tokens · $${m.price.input} in, $${m.price.output} out per million tokens${tier}${m.images ? "" : " · takes no images"} · as published` };
  };
  app.kill("SIGTERM");
  assert.strictEqual(await app.exited, 0, "the app's exit on SIGTERM");
  fs.writeFileSync(configFile, `${config()}\n[servers.openai]\nprovider = "openai"\ndialect = "responses"\nurl = "https://api.openai.com/v1"\n\n[models.openai-a]\nserver = "openai"\nmodel = "${first}"\n\n[models.openai-c]\nserver = "openai"\nmodel = "${third}"\nefforts = ["low", "high"]\n`);
  ({ app, url } = await startApp());
  await page.goto(url);
  await go("#/settings");
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
  assert.deepStrictEqual(sent.of("settings.model.write").at(-1), { name: "openai-c", server: "openai", model: third, efforts: [], settings: {} });
  assert.ok(!(config().split("[models.openai-c]")[1] || "").split("\n[")[0].includes("efforts"), "the typed efforts stayed:\n" + config());
  // A model added on the provider takes its published efforts once its id is
  // given, and saves none of its own: the provider's apply.
  const b = expect(second);
  await page.click("#add-model");
  await page.selectOption("#model-provider", "openai");
  await page.fill("#model-id", second);
  await page.press("#model-id", "Tab");
  await page.waitForSelector("#efforts-published[aria-pressed=true]", { timeout: 10000 });
  assert.deepStrictEqual(sent.of("models.published").at(-1), { server: "openai", model: second });
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
  step("a model OpenAI publishes has its context, reply cap, efforts and prices without any being typed, and nothing was sent to OpenAI");

  // ---------- the phone: nothing sideways ----------
  await page.setViewportSize({ width: 390, height: 844 });
  for (const hash of ["#/", "#/settings", `#/task/${fastId}`]) {
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

  async function shows(selector, ...texts) {
    const text = await page.innerText(selector);
    for (const want of texts) assert.ok(text.includes(want), `${selector} does not show ${JSON.stringify(want)}; it shows:\n${text.slice(0, 1200)}`);
  }
});
