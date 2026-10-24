// Settings › Models (ADR 41): the model providers nodwyn talks to, the models
// a person chooses between and the efforts each takes, and the default new
// tasks start on. The daemon validates and writes config.toml; a key goes to
// the keychain and is never shown again. Nothing here is an authority: what
// is drawn comes from settings.read and models.status, and every change is a
// request the daemon may refuse, in words this page shows as they are.
//
// The forms are drawn from state kept here, not from their own fields, so a
// redraw while one is being typed into (the models' readiness arrives
// minutes after the page, and again on every check) loses nothing, and a
// key typed is kept out of the page's own state the moment it is sent.
import { h, icon, toast, removal } from "./dom.js";
import { ModelPicker, choicesOf, effortLabel, statusOf, defaultSelection, stateWords } from "./modelpicker.js";
import { keychainForm } from "./setup.js";

// What each kind of provider needs, said before it is asked for: where it is,
// whether it takes a key and, for a chat-completions server, how it reads the
// protocol. A preset only fills the form in; the daemon is told what the form
// says, and nothing is inferred from an address later (ADR 8, ADR 41).
const kinds = {
  ollama: { label: "Ollama", needs: "Ollama on this computer or your network. It needs an address, and no key." },
  openai: { label: "OpenAI-compatible", needs: "Any server that speaks OpenAI's API, chat completions or Responses. It needs an address and, for a hosted one, an API key or an eligible ChatGPT account, kept in the keychain." },
  anthropic: { label: "Anthropic", needs: "Claude on Anthropic's own API, the Messages API. It needs its address and an API key, which is kept in the keychain and sent in the x-api-key header." },
};
// hasKey says whether a kind of server takes a key.
const hasKey = (kind) => kind === "openai" || kind === "anthropic";
// carriesModels says whether models can be on a provider: a Jev server only
// answers the judge (ADR 46), and is kept under Settings › Judge, so the
// lists here never show one.
const carriesModels = (p) => !p.inline && p.provider !== "jev";
const presets = [
  { id: "ollama", label: "Ollama", kind: "ollama", url: "http://localhost:11434", dialect: "" },
  { id: "openai", label: "OpenAI", kind: "openai", url: "https://api.openai.com/v1", dialect: "responses" },
  { id: "anthropic", label: "Anthropic", kind: "anthropic", url: "https://api.anthropic.com/v1", dialect: "" },
  { id: "baseten", label: "Baseten", kind: "openai", url: "https://inference.baseten.co/v1", dialect: "" },
  { id: "openrouter", label: "OpenRouter", kind: "openai", url: "https://openrouter.ai/api/v1", dialect: "" },
  { id: "cerebras", label: "Cerebras", kind: "openai", url: "https://api.cerebras.ai/v1", dialect: "cerebras" },
  { id: "other", label: "Other server", kind: "openai", url: "", dialect: "" },
];
const dialects = [["", "Standard (reasoning_content)"], ["cerebras", "Cerebras (reasoning)"], ["responses", "OpenAI Responses (/responses)"]];

// The words of effort a model may take. A provider decides which it accepts,
// so the sets here are only shortcuts; anything else is typed.
// effortSets are the choices of a model's efforts. "As published" is offered
// when the catalogue describes the model: its efforts are then the
// provider's, kept current with nodwyn, and none are written to its entry.
const effortSets = [
  { id: "published", label: "As published", efforts: null },
  { id: "none", label: "Not configurable", efforts: [] },
  { id: "lmh", label: "Low · Medium · High", efforts: ["low", "medium", "high"] },
  { id: "mlmh", label: "Minimal · Low · Medium · High", efforts: ["minimal", "low", "medium", "high"] },
  { id: "lmhxm", label: "Low · Medium · High · Extra high · Max", efforts: ["low", "medium", "high", "xhigh", "max"] },
  { id: "onoff", label: "Off · On (Ollama)", efforts: ["off", "on"] },
  { id: "custom", label: "Other…", efforts: null },
];

const dotOf = (s) => (s === "ready" ? "ok" : s === "unreachable" ? "bad" : s ? "warn" : "");

let ui = fresh();
function fresh() {
  return {
    provider: null, // the provider form being filled in: {mode, name, preset, kind, url, dialect, key, error, saving}
    model: null, // the model form: {mode, name, server, model, set, custom, effort, isDefault, settings, more, error, saving}
    removing: "", // "provider:NAME" or "model:NAME" waiting for its confirm
    results: new Map(), // "provider:NAME" | "model:NAME" | "form" -> the last test, or {pending: true}
    offered: new Map(), // provider name -> model ids it lists, or the reason it does not
  };
}
// A visit to Settings starts with nothing half-typed.
export function resetModelsUi() {
  ui = fresh();
}

const row = (label, ...value) => [h("div", { class: "label" }, label), h("div", { class: "value" }, ...value)];
// canEdit is for the providers: where a task's content goes is the touch's, so
// a link to another host is never offered it (ADR 54). The models of the list
// are the model methods' own, which a link is offered (modelsEditable).
const canEdit = (conn) => conn.has("settings.provider.write");

// providerOf is the provider a model is on, from the settings' providers.
const providerOf = (desk, name) => (desk.settings?.servers || []).find((s) => s.name === name) || null;

// worst is the state of a provider as its models show it: the key's, then the
// first model that cannot be used, else ready; "" when nothing has been checked.
function worst(desk, provider) {
  if (provider.key && provider.key !== "ready") return { state: provider.key, detail: provider.key_detail };
  const states = (provider.uses || []).map((name) => statusOf(desk, name)).filter(Boolean);
  const bad = states.find((s) => s.state !== "ready");
  if (bad) return { state: bad.state, detail: bad.detail };
  return states.length ? { state: "ready", detail: "" } : { state: "", detail: "" };
}

// ---------- the default for new tasks ----------

// The default model and effort chosen and not yet saved. Settings' save bar
// saves them with every other setting (settings.js), so the choice waits here
// as the bar's other edits wait there: across a redraw, and a visit to Settings.
let defaultPick = null; // {name, effort}

// stagedDefault is the default the person chose, when it differs from the
// one saved; null when nothing waits. Models can be listed with none chosen
// as the default (a config.toml with [models.NAME] entries and no
// [control]): choosing one is then a change.
export function stagedDefault(desk) {
  const current = defaultSelection(desk);
  return defaultPick && (!current || defaultPick.name !== current.name || defaultPick.effort !== current.effort) ? defaultPick : null;
}
export function discardDefault() {
  defaultPick = null;
}

// defaultRow is what a new task starts on: a model and its effort, chosen as
// a task chooses them. The judge new tasks ask is the Judge tab's.
function defaultRow(ctx, redraw) {
  const { conn, desk } = ctx;
  if (!choicesOf(desk).length) return row("Default for new tasks", h("p", { class: "hint" }, "No model is set up yet. Add a provider and a model below."));
  const picker = (ui.defaultPicker ||= new ModelPicker("default-model"));
  picker.update({
    desk,
    selection: defaultPick || defaultSelection(desk),
    disabled: !conn.has("settings.control.write"),
    onPick: (next) => {
      defaultPick = next;
      redraw();
    },
  });
  return row(
    "Default for new tasks",
    picker.el,
    h("p", { class: "hint" }, "New tasks start on this model and effort, and ask the default ", ctx.host ? "judge" : h("a", { href: desk.at("settings/judge") }, "judge"), ". A task's own choice, under its message box, changes only that task. Tasks already open keep what they started with."),
  );
}

// A provider is a card with its models under it (ADR 41): the server, its key
// and whether it answers, then each model a task may choose on it, and Add a
// model there. A Jev server carries no model and is the judge's, under
// Settings › Judge (ADR 47).
function providerRows(ctx, redraw) {
  const { conn, desk } = ctx;
  const providers = (desk.settings?.servers || []).filter((p) => p.provider !== "jev");
  const editable = canEdit(conn);
  const choices = choicesOf(desk);
  // A model is on the provider it names, or, given by its own address, on the
  // inline provider at that host.
  const onProvider = (p) => choices.filter((c) => (c.server ? c.server === p.name : p.inline && c.host === p.host));
  const placed = new Set(providers.flatMap((p) => onProvider(p).map((c) => c.name)));
  const strays = choices.filter((c) => !placed.has(c.name));
  const modelsEditable = conn.has("settings.model.write");
  // The model form is drawn where it was opened (openModel), and below the
  // list when it was opened there or its place is no longer drawn.
  let formPlaced = false;
  const formAt = (place) => (ui.model?.place === place ? ((formPlaced = true), modelForm(ctx, redraw)) : null);
  // A key is kept in the keychain, which is under Keys & devices: a provider
  // form that needs it while it is locked, or not made yet, has its form
  // beside it, as the first run does, rather than a way off the page. It is
  // a form of its own: forms do not nest.
  const needsKeychain = ui.provider && (hasKey(ui.provider.kind) || ui.provider.authentication === "chatgpt") && !desk.keychain?.unlocked;
  // The provider form changing a provider is drawn in its card, where its
  // Edit was, and the one adding a provider below the list, where Add a
  // provider was: below every provider it was out of sight once there were
  // a few. A card whose provider is gone leaves it below the list.
  const editing = (p) => ui.provider?.mode === "edit" && ui.provider.name === p.name;
  let providerPlaced = false;
  const providerForms = () => {
    providerPlaced = true;
    return [providerForm(ctx, redraw), needsKeychain ? h("div", { class: "callout plain", id: "provider-keychain" }, keychainForm(ctx, { onDone: redraw })) : null];
  };
  const list = providers.map((p) => {
    const key = `provider:${p.name}`;
    const status = worst(desk, p);
    const result = ui.results.get(key);
    const first = (p.uses || []).find((name) => choicesOf(desk).some((c) => c.name === name)) || (p.uses || [])[0];
    const detail = status.detail || (status.state ? "" : first ? "Not checked yet: Test asks it for a reply." : "No model is on it yet: add one to test it.");
    const users = [...(p.uses || []), ...(p.judge ? ["the judge"] : [])];
    const models = onProvider(p);
    return h(
      "div",
      { class: "provider-row provider-card", id: `provider-${p.name}`, dataset: { provider: p.name } },
      h(
        "div",
        { class: "list-row" },
        h("span", { class: `dot ${dotOf(status.state)}` }),
        h(
          "div",
          { class: "main" },
          h("strong", {}, p.inline ? p.host : p.name),
          h("span", {}, `${kinds[p.provider]?.label || p.provider}${p.dialect ? ` (${p.dialect})` : ""} · ${p.host}${p.credential ? (p.authentication === "chatgpt" ? " · ChatGPT plan" : p.key === "ready" ? " · key stored" : "") : hasKey(p.provider) ? " · no key" : ""}${p.judge ? " · the judge's" : ""}`),
          detail ? h("span", { class: status.state && status.state !== "ready" ? "warn-text" : "" }, detail) : null,
        ),
        h("div", { class: "end" }, status.state ? h("span", { class: "state-word" }, stateWords[status.state] || status.state) : null),
      ),
      h(
        "div",
        { class: "row-actions provider-actions" },
        h("button", { type: "button", class: "btn small", disabled: !first || !conn.has("models.test") || result?.pending, title: first ? "Sends one short request to a model on this provider, as a task would" : "Add a model on this provider to test it", onclick: () => test(ctx, key, first ? { name: first } : null, redraw) }, "Test"),
        editable && !p.inline && !editing(p) ? h("button", { type: "button", class: "btn small", onclick: () => openProvider(ctx, p, redraw) }, "Edit") : null,
        editable && !p.inline ? removal({ label: "Remove", consequence: "Remove it? Its key stays in the keychain.", asking: ui.removing === key, ask: () => ((ui.removing = key), redraw()), cancel: () => ((ui.removing = ""), redraw()), run: () => removeProvider(ctx, p.name, redraw), disabled: users.length > 0, title: users.length ? `Still used by ${users.join(", ")}` : "" }) : null,
      ),
      editing(p) ? providerForms() : null,
      p.inline ? h("p", { class: "hint" }, "Given by a model's own table in config.toml. Choosing a default model on a provider moves it to one.") : null,
      resultLine(result),
      h(
        "div",
        { class: "models provider-models" },
        models.length ? models.map((c) => [modelRow(ctx, c, redraw), formAt(`model:${c.name}`)]) : h("p", { class: "hint" }, p.inline ? "" : "No model on it yet."),
        formAt(`provider:${p.name}`),
        modelsEditable && !p.inline && carriesModels(p) && !ui.model
          ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn small", id: `add-model-${p.name}`, dataset: { addModel: p.name }, onclick: () => openModel(null, redraw, p.name) }, icon("plus", 12), `Add a model on ${p.name}`))
          : null,
      ),
    );
  });
  return row(
    "Providers and their models",
    providers.length ? h("div", { class: "providers" }, list) : h("p", { class: "hint" }, "No provider yet. Add the server your models are on."),
    strays.length ? h("div", { class: "models provider-models strays" }, h("p", { class: "hint" }, "On no provider listed here:"), strays.map((c) => modelRow(ctx, c, redraw))) : null,
    ui.provider && !providerPlaced ? providerForms() : null,
    ui.model && !formPlaced ? modelForm(ctx, redraw) : null,
    h(
      "div",
      { class: "row-actions" },
      editable && !ui.provider ? h("button", { type: "button", class: "btn", id: "add-provider", onclick: () => openProvider(ctx, null, redraw) }, icon("plus", 14), "Add a provider") : null,
      modelsEditable && !ui.model ? h("button", { type: "button", class: "btn", id: "add-model", disabled: !providers.some(carriesModels), title: providers.some(carriesModels) ? "" : "Add a provider first", onclick: () => openModel(null, redraw) }, icon("plus", 14), "Add a model") : null,
      h("button", { type: "button", class: "btn", id: "check-again", title: "Asks each provider for its models again, without a key", onclick: () => desk.refreshModels(true) }, "Check again"),
    ),
    !editable ? h("p", { class: "hint" }, modelsEditable ? "Models can be added, changed and removed here among the providers listed. Only the computer nodwyn runs on can change providers." : "Only the computer nodwyn runs on can change providers.") : null,
    h(
      "p",
      { class: "hint" },
      "A key is stored encrypted in the keychain and sent only to its provider's address; config.toml never holds it. Each model has its own settings: its efforts, its context length, its reply cap, its prices and whether it takes images. What they leave empty is what its provider publishes, as models.dev collects it, so a known model needs none typed; nothing is guessed from a model's name.",
    ),
  );
}

// providerDraft is a provider form's state: the provider being changed, or a
// new one starting from a preset.
function providerDraft(p, preset = "openai") {
  const dflt = presets.find((x) => x.id === preset);
  const f = p
    ? { mode: "edit", name: p.name, preset: "", kind: p.provider, url: p.url, dialect: p.dialect || "", key: "", hasKey: !!p.credential, host: p.host, error: "" }
    : { mode: "add", name: "", preset, kind: dflt.kind, url: dflt.url, dialect: dflt.dialect, key: "", hasKey: false, host: "", error: "" };
  return Object.assign(f, { more: false, authentication: p?.authentication || "api_key", credential: "", credentialClass: p?.authentication === "chatgpt" ? p.credential : "", accounts: null, authorization: null, connecting: false });
}

function openProvider(ctx, p, redraw) {
  ui.provider = providerDraft(p);
  if (ui.provider.authentication === "chatgpt") loadAccounts(ctx, ui.provider, redraw);
  redraw();
}

async function loadAccounts(ctx, f, redraw) {
  f.accountsLoading = true;
  f.accountsUnlocked = !!ctx.desk.keychain?.unlocked;
  try {
    const list = await ctx.conn.call("credentials.list", {});
    if (ui.provider !== f) return;
    f.accountsLoading = false;
    f.accountsUnlocked = !!list.keychain?.unlocked;
    f.accounts = list.credentials;
    ctx.desk.keychain = list.keychain;
    if (!f.credential && f.credentialClass) f.credential = f.accounts.find((c) => c.class === f.credentialClass && !c.shadowed)?.id || "";
    if (f.authorization) {
      const a = list.authorizations.find((a) => a.id === f.authorization.id);
      if (a?.state === "done") {
        f.credential = a.credential;
        f.authorization = null;
        f.connecting = false;
      } else if (a?.state === "failed") {
        f.error = a.error;
        f.authorization = null;
        f.connecting = false;
      }
    }
    redraw();
    if (f.authorization) setTimeout(() => {
      if (ui.provider === f && document.getElementById("provider-form")) loadAccounts(ctx, f, redraw);
    }, 1000);
  } catch (error) {
    if (ui.provider !== f) return;
    f.error = error.message;
    f.accountsLoading = false;
    f.connecting = false;
    f.authorization = null;
    redraw();
  }
}

async function connectChatGPT(ctx, f, redraw, id = "") {
  f.connecting = true;
  f.error = "";
  redraw();
  try {
    let params = { id };
    if (!id) {
      const classes = new Set((f.accounts || []).map((c) => c.class));
      let name = "chatgpt";
      for (let n = 2; classes.has(name); n++) name = `chatgpt-${n}`;
      params = { provider: "openai", class: name };
    }
    const started = await ctx.conn.call("credentials.authorize", params);
    if (ui.provider !== f) return;
    f.authorization = started;
    if (typeof window.nodwynOpenExternal === "function") window.nodwynOpenExternal(started.url).catch(() => {});
    else window.open(started.url, "_blank", "noopener");
    await loadAccounts(ctx, f, redraw);
  } catch (error) {
    if (ui.provider !== f) return;
    f.error = error.message;
    f.connecting = false;
    redraw();
  }
}

// suggestName is the name a new provider gets when none is typed: its kind
// (chatgpt for a plan), made unique among taken.
function suggestName(f, taken) {
  const base = f.kind === "openai" && f.authentication === "chatgpt" ? "chatgpt" : f.kind;
  let name = base;
  for (let n = 2; taken.has(name); n++) name = `${base}-${n}`;
  return name;
}

// providerForm is the form that adds a provider or changes one. first is the
// first-run page's: it has nothing to cancel back to until a provider exists,
// and the keychain a key needs is made beside it, not in Settings.
function providerForm(ctx, redraw, first = false) {
  const { conn, desk } = ctx;
  const f = ui.provider;
  const adding = f.mode === "add";
  const cancellable = !first || (desk.settings?.servers || []).some((p) => !p.inline);
  const plan = f.kind === "openai" && f.authentication === "chatgpt";
  const account = (f.accounts || []).find((c) => c.id === f.credential);
  const field = (id, label, control, hint) => h("div", { class: "field" }, h("label", { for: id }, label), control, hint ? h("p", { class: "hint" }, hint) : null);
  const url = h("input", { class: "input", id: "provider-url", spellcheck: false, autocomplete: "off", required: true, disabled: plan, value: f.url, placeholder: "https://…", oninput: () => (f.url = url.value) });
  url.value = f.url;
  const key = h("input", { class: "input", id: "provider-key", type: "password", autocomplete: "off", spellcheck: false, placeholder: f.hasKey ? "leave empty to keep the saved key" : "", oninput: () => {
    f.key = key.value;
    const save = document.getElementById("provider-save");
    if (save) save.disabled = f.saving || f.connecting || (!!f.credentialClass && !f.key) || ((!!f.key || !!f.credentialClass) && !desk.keychain?.unlocked);
  } });
  key.value = f.key;
  const dialect = h("select", { class: "input", id: "provider-dialect", disabled: plan, onchange: () => (f.dialect = dialect.value) }, dialects.map(([v, label]) => h("option", { value: v }, label)));
  dialect.value = f.dialect;
  const keychainReady = !!desk.keychain?.unlocked;
  if (plan && keychainReady && f.accounts !== null && !f.accountsUnlocked && !f.accountsLoading) loadAccounts(ctx, f, redraw);
  const sameHostNow = (() => {
    try {
      return f.mode === "add" || new URL(f.url).host === f.host;
    } catch {
      return true;
    }
  })();
  const form = h(
    "form",
    {
      class: "setup-form provider-form",
      id: "provider-form",
      onsubmit: async (event) => {
        event.preventDefault();
        f.error = "";
        f.saving = true;
        const apiKey = f.key;
        f.key = "";
        redraw();
        try {
          const params = { provider: f.kind, url: f.url.trim() };
          // Servers are keyed by name, so a second may share an address (an
          // API key and a ChatGPT plan at api.openai.com); a new one never
          // takes the name of one that is there, which would change it.
          const taken = new Set((desk.settings.servers || []).map((s) => s.name));
          const sameAddress = (desk.settings.servers || []).some((s) => s.url.replace(/\/+$/, "") === params.url.replace(/\/+$/, "") && s.provider === params.provider);
          if (adding) {
            // Left empty, the daemon names a provider after its host; at an
            // address that is taken it would change that provider instead, so
            // a name is sent.
            if (f.name.trim() || sameAddress) params.name = f.name.trim() || suggestName(f, taken);
            if (taken.has(params.name)) throw new Error(`A provider named ${params.name} exists: choose another name, or change that one.`);
          } else params.name = f.name;
          if (f.kind === "openai" && f.dialect) params.dialect = f.dialect;
          if (hasKey(f.kind) && apiKey) params.api_key = apiKey;
          if (plan) params.credential = f.credential;
          desk.settings = await conn.call("settings.provider.write", params);
          const saved = (desk.settings.servers || []).find((s) => (params.name ? s.name === params.name : s.url.replace(/\/+$/, "") === params.url.replace(/\/+$/, "")));
          // Its list is asked with its key, so a new key or address asks again.
          if (saved) ui.offered.delete(saved.name);
          ui.provider = null;
          await desk.refreshModels();
          const key = saved ? worst(desk, saved) : null;
          toast(saved?.key && saved.key !== "ready" ? `Saved, but ${saved.key_detail}.` : key?.state && key.state !== "ready" ? `Saved, but ${key.detail || stateWords[key.state]}.` : adding ? (first ? "Provider saved. Choose its model." : "Provider saved. Add a model on it, then Test.") : "Provider saved.");
        } catch (error) {
          f.error = error.message;
          f.saving = false;
        }
        redraw();
      },
    },
    h("h3", { class: "form-title" }, adding ? (first ? "Where does the model run?" : "Add a provider") : `Change ${f.name}`),
    adding ? h("div", { class: "row-actions", role: "group", "aria-label": "Provider" }, presets.map((p) => h("button", { type: "button", class: "chip", id: `preset-${p.id}`, "aria-pressed": String(f.preset === p.id), onclick: () => ((f.preset = p.id), (f.kind = p.kind), (f.url = p.url), (f.dialect = p.dialect), (f.authentication = "api_key"), redraw()) }, p.label))) : null,
    // The presets say the kind (Other server is the OpenAI-compatible one), so
    // there is one choice to make, not a preset and then a kind.
    h("p", { class: "hint" }, kinds[f.kind].needs),
    f.kind === "openai" ? h("div", { class: "row-actions", role: "group", "aria-label": "Authentication" },
      ["api_key", "chatgpt"].map((auth) => h("button", { type: "button", class: "chip", id: `provider-auth-${auth}`, disabled: f.saving || f.connecting, "aria-pressed": String(f.authentication === auth), onclick: () => {
        f.authentication = auth;
        f.error = "";
        f.key = "";
        if (auth === "chatgpt") {
          f.url = "https://api.openai.com/v1";
          f.dialect = "responses";
          loadAccounts(ctx, f, redraw);
        }
        redraw();
      } }, auth === "chatgpt" ? "ChatGPT plan" : "API key"))) : null,
    field("provider-url", "Address", url, f.kind === "ollama" ? "Such as http://localhost:11434, or http://workshop:11434 on your network." : `Including the version, such as ${f.kind === "anthropic" ? "https://api.anthropic.com/v1" : "https://api.openai.com/v1"}.`),
    f.kind === "openai" ? field("provider-dialect", "How it reads the protocol", dialect, "Chosen by you, never guessed from the address. Cerebras needs its own for reasoning to survive tool calls; OpenAI needs Responses for a reasoning model to take an effort and tools together.") : null,
    // A key's value is changed in one place, Credentials (it is stored there
    // whichever page took it): this form takes a key for a provider that has
    // none, one at another address, or one replacing a ChatGPT plan.
    hasKey(f.kind) && !plan
      ? adding || !f.hasKey || !sameHostNow || f.credentialClass
        ? field("provider-key", "API key", key, !keychainReady ? "Create or unlock the keychain first: keys are kept there, never in config.toml." : "Stored encrypted and sent only to this address. Every model on this provider uses it.")
        : h("div", { class: "field" }, h("label", {}, "API key"), h("p", { class: "hint", id: "provider-key-stored" }, "Stored encrypted and sent only to this address. ", ctx.host ? `Replace it under Credentials in ${ctx.host}'s own console` : h("a", { href: "#/connections/credentials" }, "Replace it under Credentials"), "."))
      : null,
    plan ? h("div", { class: "chatgpt-setup" },
      h("p", { class: "hint" }, "Use your eligible ChatGPT plan quota. Sign in with OpenAI in your browser; no API key or client id is needed. Save the provider to allow model requests and model discovery. OpenAI decides model access and remaining quota."),
      f.accounts === null ? h("p", { class: "hint" }, "Loading accounts…") : field("provider-account", "Connected account", (() => {
        const select = h("select", { class: "input", id: "provider-account", disabled: f.connecting || !keychainReady, onchange: () => { f.credential = select.value; redraw(); } },
          h("option", { value: "" }, "Choose an account or connect below"),
          f.accounts.filter((c) => c.kind === "oauth2" && c.provider === "openai" && !c.shadowed).map((c) => h("option", { value: c.id }, `${c.class}${c.needs_reauth ? " · sign in again" : ""}`)));
        select.value = f.credential;
        return select;
      })()),
      h("div", { class: "row-actions" },
        h("button", { type: "button", class: "btn", id: "provider-connect-chatgpt", disabled: !keychainReady || !conn.has("credentials.authorize") || f.accounts === null || f.connecting || f.saving, onclick: () => connectChatGPT(ctx, f, redraw) }, f.connecting ? "Waiting for sign-in…" : "Continue with ChatGPT"),
        account ? h("button", { type: "button", class: "btn small", id: "provider-reauthorize", disabled: !keychainReady || f.connecting || f.saving, onclick: () => connectChatGPT(ctx, f, redraw, account.id) }, "Sign in again") : null),
      f.authorization ? h("p", { class: "hint" }, "Finish sign-in in your browser. ", h("a", { href: f.authorization.url, target: "_blank", rel: "noopener" }, "Open the sign-in page")) : null,
      account?.needs_reauth ? h("p", { class: "warn-text" }, "This account needs you to sign in again before saving.") : null) : null,
    !plan && !sameHostNow && f.hasKey ? h("p", { class: "warn-text" }, "This is another host. The saved key is not sent there: type its key, or the provider is left without one.") : null,
    !plan && f.credentialClass && !f.key ? h("p", { class: "hint" }, "Type an API key to switch this provider from its ChatGPT plan. The connected account stays in the keychain.") : null,
    // Almost nobody names a provider: one left unnamed is named after its
    // address, so the field waits under a fold.
    adding
      ? h(
          "details",
          { class: "more-settings", id: "provider-more", open: f.more },
          // The state is set as the summary is clicked, not when the fold reports it: a redraw between the two (the models answer) would otherwise close it.
          h("summary", { onclick: () => (f.more = !f.more) }, "More: a name for it"),
          field("provider-name", "Name", h("input", { class: "input", id: "provider-name", spellcheck: false, autocomplete: "off", value: f.name, placeholder: "named after its address", oninput: (event) => (f.name = event.target.value) }), "What models name it. Two providers can share an address, such as an API key and a ChatGPT plan at OpenAI."),
        )
      : null,
    f.error ? h("p", { class: "error", role: "alert" }, f.error) : null,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn accent", id: "provider-save", disabled: f.saving || f.connecting || (plan && (!keychainReady || !account || account.needs_reauth)) || (!plan && !!f.credentialClass && !f.key) || (hasKey(f.kind) && !!f.key && !keychainReady) }, f.saving ? "Saving…" : "Save the provider"), cancellable ? h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.provider = null), redraw()) }, "Cancel") : null),
  );
  return form;
}

async function removeProvider(ctx, name, redraw) {
  const { conn, desk } = ctx;
  ui.removing = "";
  try {
    desk.settings = await conn.call("settings.provider.remove", { name });
    await desk.refreshModels();
    toast(`${name} removed. Its key stays in the keychain: Connections › Credentials removes it.`);
  } catch (error) {
    toast(error.message);
  }
  redraw();
}

// ---------- models ----------
// modelRow is one model a person can choose, under the provider it is on:
// its efforts, its settings as far as anyone says, whether it can be used,
// and Test, Edit and Remove.
function modelRow(ctx, c, redraw) {
  const { conn, desk } = ctx;
  const editable = conn.has("settings.model.write");
  const key = `model:${c.name}`;
  const status = statusOf(desk, c.name);
  const result = ui.results.get(key);
  const inline = !c.server;
  return h(
    "div",
    { class: "model-row", dataset: { model: c.name } },
    h(
      "div",
      { class: "list-row" },
      h("span", { class: `dot ${dotOf(status?.state)}` }),
      h(
        "div",
        { class: "main" },
        h("strong", {}, c.model, c.default ? h("span", { class: "badge plain default-badge" }, "default") : null),
        h("span", {}, c.efforts?.length ? `effort: ${c.efforts.map(effortLabel).join(" · ")}${c.effort ? `, starts at ${effortLabel(c.effort)}` : ""}` : "no effort setting"),
        c.labels?.length ? h("span", { class: "model-labels" }, c.labels.map((l) => h("span", { class: "badge plain" }, l))) : null,
        h("span", { class: "model-settings" }, settingsLine(c.settings, c.provider, c.published)),
        status && status.state !== "ready" ? h("span", { class: "warn-text" }, status.detail || stateWords[status.state]) : null,
      ),
      h("div", { class: "end" }, status ? h("span", { class: "state-word" }, stateWords[status.state] || status.state) : h("span", { class: "state-word" }, "checking")),
    ),
    h(
      "div",
      { class: "row-actions model-actions" },
      h("button", { type: "button", class: "btn small", disabled: !conn.has("models.test") || result?.pending, title: "Sends one short request to this model, as a task would", onclick: () => test(ctx, key, { name: c.name }, redraw) }, "Test"),
      editable && !inline ? h("button", { type: "button", class: "btn small", onclick: () => openModel(c, redraw) }, "Edit") : null,
      editable && !inline && !c.default ? removal({ label: "Remove", consequence: "Remove it from the list?", asking: ui.removing === key, ask: () => ((ui.removing = key), redraw()), cancel: () => ((ui.removing = ""), redraw()), run: () => removeModel(ctx, c.name, redraw) }) : null,
    ),
    inline ? h("p", { class: "hint" }, "Gives its own address in config.toml, so its efforts and settings are set there. Choosing it as the default moves it to a provider.") : null,
    resultLine(result),
  );
}

// settingsLine says briefly what a model's settings are: how large its
// context is, how long a reply may be, what it costs and whether it takes
// images, its own where it has them, else what its provider publishes
// (published), as far as either says.
function settingsLine(s = {}, kind, published = null) {
  const p = published || {};
  const n = (x) => x.toLocaleString("en-US");
  const parts = [];
  let fromCatalogue = false;
  const num_ctx = s.num_ctx || p.num_ctx;
  const max_output = s.max_output || p.max_output;
  fromCatalogue ||= (!s.num_ctx && !!p.num_ctx) || (!s.max_output && !!p.max_output);
  parts.push(num_ctx ? `context ${n(num_ctx)} tokens` : kind === "ollama" ? "context the window's size" : "context as the provider says");
  if (max_output) parts.push(`replies up to ${n(max_output)} tokens`);
  // Each price is the entry's, else the published one (config.Rates); the
  // published tier applies only while the entry sets no price.
  const own = s.price_input !== undefined || s.price_cached_input !== undefined || s.price_output !== undefined;
  const price_input = s.price_input ?? p.price_input;
  const price_output = s.price_output ?? p.price_output;
  if (price_input !== undefined && price_output !== undefined) {
    parts.push(`$${price_input} in, $${price_output} out per million tokens${!own && p.tier_above ? ` (more past ${n(p.tier_above)})` : ""}`);
    fromCatalogue ||= s.price_input === undefined || s.price_output === undefined;
  } else parts.push("no prices");
  if (s.vision === false || (s.vision === undefined && published && !p.images)) parts.push("takes no images");
  if (fromCatalogue) parts.push("as published");
  return parts.join(" · ");
}

// settingsForm is a model's settings as its form holds them: numbers as
// typed, so a redraw keeps what is half-typed.
function settingsForm(s = {}) {
  const text = (v) => (v === undefined || v === null ? "" : String(v));
  const count = (v) => (v ? v.toLocaleString("en-US") : "");
  return {
    num_ctx: count(s.num_ctx),
    max_output: count(s.max_output),
    temperature: text(s.temperature),
    seed: text(s.seed),
    keep_alive: s.keep_alive || "",
    // The entry's own answer, undefined when it gives none: the form then
    // shows what applies, and saves only an answer chosen.
    vision: s.vision,
    output_field: s.output_field || "",
    cache_key: s.cache_key || "",
    cache_control: s.cache_control || "",
    price_input: text(s.price_input),
    price_cached_input: text(s.price_cached_input),
    price_output: text(s.price_output),
  };
}

// settingsOf reads a form's settings for settings.model.write, all of them:
// what is empty is left out, which leaves it unset, and what only the other
// kind of server reads is not sent. A number that does not read is refused
// here, in words, before anything is sent; the daemon judges the rest.
function settingsOf(f, kind) {
  const s = f.settings;
  const out = {};
  const number = (name, label, { whole = false, signed = false } = {}) => {
    const raw = s[name].replace(/[,_\s]/g, "");
    if (!raw) return;
    const n = Number(raw);
    if (!Number.isFinite(n) || (whole && !Number.isInteger(n)) || (!signed && n < 0)) {
      throw new Error(`${label} takes ${whole ? "a whole number" : "a number"}${signed ? "" : " of 0 or more"}, not “${s[name]}”.`);
    }
    out[name] = n;
  };
  number("num_ctx", "Context length", { whole: true });
  number("max_output", "Reply cap", { whole: true });
  if (out.num_ctx && out.max_output >= out.num_ctx) throw new Error(`The reply cap (${s.max_output}) must be below the context length (${s.num_ctx}): the reply comes out of it.`);
  number("temperature", "Temperature");
  // Anthropic's API has no seed.
  if (kind !== "anthropic") number("seed", "Seed", { whole: true, signed: true });
  number("price_input", "The price of input");
  number("price_cached_input", "The price of cached input");
  number("price_output", "The price of output");
  if (kind === "ollama") {
    if (s.keep_alive.trim()) out.keep_alive = s.keep_alive.trim();
    return out;
  }
  // Only a choice is written: unset, the model follows what its provider
  // publishes, so Yes can say a model reads images its catalogue entry says
  // it does not.
  if (s.vision !== undefined) out.vision = s.vision;
  // Anthropic reads the ask to cache the prompt, and neither of the other two.
  for (const name of kind === "anthropic" ? ["cache_control"] : ["output_field", "cache_key", "cache_control"]) if (s[name]) out[name] = s[name];
  return out;
}

// tokens reads a count as the form holds it, "1,050,000" included; 0 when it
// is empty or does not read.
const tokens = (raw) => Number(String(raw).replace(/[,_\s]/g, "")) || 0;

// windowLine says what a conversation on the model gets from the form's
// context length and reply cap, sized as the daemon sizes it
// (config.Window.ReplyCap, Sized): the reply cap comes out of the context,
// and the window has three quarters of the rest, or the water marks
// Settings › General › Advanced sets when the context holds them.
function windowLine(f, kind, desk) {
  const v = desk.settings?.values || {};
  const reserve = Number(v["window.output_reserve"]?.value) || 16384;
  const high = Number(v["window.high_water"]?.value) || 61440;
  const fixed = !!(v["window.high_water"]?.set || v["window.low_water"]?.set);
  // What the form leaves empty is what the provider publishes, as a session
  // takes it (config.WithPublished): its context, and its reply limit at
  // most a quarter of the context.
  const published = f.published || {};
  let context = tokens(f.settings.num_ctx) || published.num_ctx || 0;
  let own = tokens(f.settings.max_output);
  if (!own && published.max_output) own = published.num_ctx && context !== published.num_ctx ? Math.min(published.max_output, Math.floor(context / 4)) : published.max_output;
  // A context too small for the window and the default reply gives the
  // reply a quarter of it at most.
  const cap = own || (context && context < high + reserve ? Math.min(reserve, Math.floor(context / 4)) : reserve);
  const n = (x) => x.toLocaleString("en-US");
  // An Ollama model whose context is unset is asked for exactly the window
  // and its reply (WindowNumCtx).
  if (!context && kind === "ollama") context = high + cap;
  if (!context) return `Each reply may take up to ${n(cap)} tokens. With the context length empty, the window is sized from the provider's list when it says, else kept at its default (summarized past ${n(high)} tokens).`;
  const room = context - cap;
  if (room <= 0) return `A reply cap of ${n(cap)} leaves nothing of a ${n(context)}-token context: lower it.`;
  let water = high;
  if ((!fixed && Math.floor((room * 3) / 4) > high) || high > room) water = Math.floor((room * 3) / 4);
  return `Of ${n(context)} tokens, each reply may take ${n(cap)}; the conversation is summarized or trimmed past ${n(water)}${room > water ? `, with room up to ${n(room)} to do it` : ""}.`;
}

// settingsFields are the model form's fields for its own settings: the ones
// a person most often needs first (its context and the reply cap that comes
// out of it, its prices, images), the rest folded under More settings.
function settingsFields(f, kind, redraw, desk) {
  const s = f.settings;
  const field = (forId, label, control, hint) => h("div", { class: "field" }, h("label", { for: forId }, label), control, hint ? h("p", { class: "hint" }, hint) : null);
  // The window line follows the context and the reply cap as they are typed,
  // without a redraw of the form under it.
  const summary = h("p", { class: "hint", id: "model-window" }, windowLine(f, kind, desk));
  const resize = () => (summary.textContent = windowLine(f, kind, desk));
  const box = (name, label, { placeholder = "", prefix = "", suffix = "", numeric = true, width = "narrow", sizes = false } = {}) => {
    const node = h("input", { class: `input setting ${width}`, id: `model-${name}`, "aria-label": label, inputmode: numeric ? "decimal" : null, autocomplete: "off", spellcheck: false, placeholder, oninput: () => ((s[name] = node.value), sizes && resize()) });
    node.value = s[name];
    if (!prefix && !suffix) return node;
    return h("span", { class: "affix" }, prefix ? h("span", { class: "unit" }, prefix) : null, node, suffix ? h("span", { class: "unit" }, suffix) : null);
  };
  const chips = (name, label, options, current = s[name]) =>
    h(
      "div",
      { class: "row-actions", role: "group", "aria-label": label },
      options.map(([value, text]) => h("button", { type: "button", class: "chip", id: `model-${name}-${value === "" ? "default" : String(value)}`, "aria-pressed": String(current === value), onclick: () => ((s[name] = value), name === "cache_control" && (f.cacheChosen = true), redraw()) }, text)),
    );
  const openai = kind !== "ollama";
  const claude = kind === "anthropic";
  // The Responses API caps a reply with max_output_tokens whatever the reply
  // cap field says, and has no seed (internal/llm/responses.go).
  const responses = openai && providerOf(desk, f.server)?.dialect === "responses";
  const reserve = Number(desk.settings?.values?.["window.output_reserve"]?.value) || 16384;
  const published = f.published || {};
  const count = (x) => (x ? x.toLocaleString("en-US") : "");
  const price = (x) => (x === undefined || x === null ? "" : String(x));
  return [
    field(
      "model-num_ctx",
      "Context length",
      box("num_ctx", "Context length", { placeholder: count(published.num_ctx) || (openai ? "the provider's" : "the window's"), suffix: "tokens", width: "", sizes: true }),
      published.num_ctx
        ? `How many tokens the model holds at once, its replies included. Empty is the ${count(published.num_ctx)} its provider publishes.`
        : claude
          ? "How many tokens the model holds at once, its replies included. Empty asks Anthropic's model list, which gives each model's context window."
        : openai
          ? "How many tokens the model holds at once, its replies included. Empty asks the provider's model list, which OpenRouter's answers and OpenAI's and Cerebras' don't."
          : "How many tokens the model holds at once, its replies included. Empty asks Ollama for the window and a reply.",
    ),
    field(
      "model-max_output",
      "Reply cap",
      box("max_output", "Reply cap", { placeholder: count(published.max_output) || count(reserve), suffix: "tokens", width: "", sizes: true }),
      published.max_output
        ? `The most one reply may take, thinking included. It comes out of the context length, and the conversation gets the rest. Empty is ${count(published.max_output)}: the longest reply its provider publishes, at most a quarter of the context.`
        : `The most one reply may take, thinking included. It comes out of the context length, and the conversation gets the rest. Empty is ${count(reserve)}, the fallback reply cap (Settings › General › Advanced), or a quarter of a small context. A reasoning model at a high effort may need more.`,
    ),
    summary,
    h(
      "div",
      { class: "field" },
      h("label", { for: "model-price_input" }, "Prices"),
      h(
        "div",
        { class: "row-actions" },
        box("price_input", "Price of input", { prefix: "$", placeholder: price(published.price_input) || "unknown" }),
        h("span", { class: "muted" }, "input,"),
        box("price_cached_input", "Price of cached input", { prefix: "$", placeholder: price(published.price_cached_input) || "as input" }),
        h("span", { class: "muted" }, "cached input,"),
        box("price_output", "Price of output", { prefix: "$", placeholder: price(published.price_output) || "unknown" }),
        h("span", { class: "muted" }, "output"),
      ),
      h(
        "p",
        { class: "hint" },
        published.price_input !== undefined
          ? `US dollars per million tokens, to estimate what a request cost when the provider doesn't report it. Each one empty is what it publishes.${published.price_cache_write !== undefined || published.tier_above ? ` While all are, ${[published.price_cache_write !== undefined ? `writing to its cache costs $${published.price_cache_write}` : "", published.tier_above ? `a prompt past ${count(published.tier_above)} tokens costs more` : ""].filter(Boolean).join(" and ")}, as published; once you type one, only the prices here apply.` : ""} They feed the spend limit; nothing else is enforced with them.`
          : "US dollars per million tokens, to estimate what a request cost when the provider doesn't report it: OpenRouter reports it, OpenAI and Cerebras don't. They feed the spend limit; nothing else is enforced with them.",
      ),
    ),
    openai
      ? field(
          "model-vision-true",
          "Takes images",
          chips("vision", "Takes images", [[true, "Yes"], [false, "No"]], s.vision ?? (f.published ? published.images : true)),
          f.published
            ? `Whether an attached image may be sent to this model. Unless you choose, it is what its provider publishes: ${published.images ? "it reads them" : "it reads none"}.`
            : `Whether an attached image may be sent to this model. ${claude ? "Anthropic's API doesn't" : "A chat-completions server doesn't"} say, so unless you choose No a model is taken to read images: one that can't fails that request, and what you wrote is kept.`,
        )
      : null,
    h(
      "details",
      { class: "more-settings", id: "model-more-settings", open: f.more, ontoggle: (event) => (f.more = event.target.open) },
      h("summary", {}, "More settings"),
      h(
        "div",
        { class: "field" },
        h("label", { for: "model-temperature" }, "Sampling"),
        h("div", { class: "row-actions" }, h("span", { class: "muted" }, "Temperature"), box("temperature", "Temperature", { placeholder: "server's" }), claude ? null : [h("span", { class: "muted" }, "seed"), box("seed", "Seed", { placeholder: "none" })]),
        h(
          "p",
          { class: "hint" },
          claude
            ? "Temperature is 0 to 1; empty leaves it to Anthropic, which is right for the newer Claude models: they refuse any temperature. Its API has no seed."
            : responses
            ? "Temperature is 0 to 2; empty leaves it to the server. The Responses API has no seed: one set here is still sent, and a server that refuses it fails every request."
            : "Temperature is 0 to 2; empty leaves both to the server. 0 with a seed makes replies as repeatable as the server allows.",
        ),
      ),
      openai
        ? [
            responses || claude ? null : field("model-output_field-default", "Reply cap field", chips("output_field", "Reply cap field", [["", "max_completion_tokens"], ["max_tokens", "max_tokens"]]), "Reasoning models refuse the older max_tokens; some servers know only that one."),
            claude ? null : field("model-cache_key-default", "Session key", chips("cache_key", "Session key", [["", "Header and field"], ["header", "Header only"], ["off", "Not sent"]]), "Names the session to the server, so it can keep the requests on the replica that holds their prompt cache. Header only for a server that refuses fields it doesn't know."),
            field(
              "model-cache_control-default",
              "Prompt cache",
              chips("cache_control", "Prompt cache", [["", "Not asked"], ["ephemeral", "5 minutes"], ["1h", "1 hour"]]),
              claude
                ? "Claude caches its prompt only when asked: without this every request is billed in full, however little changed. Asking has Anthropic cache the conversation so far for the time chosen and read it back at a tenth of the price (a write costs a quarter more for 5 minutes, twice for an hour). A model you add here starts with 5 minutes chosen; Not asked leaves it off."
                : "A Claude model, through OpenRouter or another provider, caches its prompt only when asked: without this every request is billed in full, however little changed. Asking has the provider cache the conversation so far for the time chosen, and read it back at a tenth of the price (a write costs a quarter more). Leave it off for a model that caches by itself, which is most others, and for a server that refuses fields it doesn't know.",
            ),
          ]
        : field("model-keep_alive", "Keep loaded", box("keep_alive", "Keep loaded", { placeholder: "Ollama's", numeric: false }), "How long Ollama keeps the model in memory between requests, such as 30m, or -1 for always."),
    ),
  ];
}

function setOf(efforts) {
  const found = effortSets.find((s) => s.efforts && s.efforts.length === efforts.length && s.efforts.every((e, i) => e === efforts[i]));
  return found ? found.id : "custom";
}

// modelDraft is a model form's state: the model being changed, or a new one.
function modelDraft(c) {
  // A model whose efforts are those its provider publishes is shown as such,
  // whether its entry repeats them or says none.
  const same = (a = [], b = []) => a.length === b.length && a.every((e, i) => e === b[i]);
  const published = c?.published || null;
  return c
    ? { mode: "edit", name: c.name, server: c.server, model: c.model, set: published?.efforts?.length && same(c.efforts, published.efforts) ? "published" : setOf(c.efforts || []), custom: (c.efforts || []).join(", "), effort: c.effort || "", labels: (c.labels || []).join(", "), isDefault: !!c.default, settings: settingsForm(c.settings), published, more: false, error: "" }
    : { mode: "add", name: "", server: "", model: "", set: "none", custom: "", effort: "", labels: "", isDefault: false, settings: settingsForm(), published: null, more: false, error: "" };
}

function openModel(c, redraw, server = "") {
  // A test's result is the form's it was made in: a new form starts without
  // one, or it showed the last model's green "reply: ok".
  ui.results.delete("form");
  ui.model = modelDraft(c);
  if (server) ui.model.server = server;
  // The form opens where it was asked for: under the model Edit was clicked
  // on, or in the card of the provider whose Add a model was. Below every
  // provider it was out of sight once there were a few.
  ui.model.place = c ? `model:${c.name}` : server ? `provider:${server}` : "";
  redraw();
}

// labelWords are the labels a form's text holds: lower-case words, as the
// daemon requires, split on commas and spaces, each once.
export function labelWords(text) {
  return [...new Set(String(text || "").toLowerCase().split(/[\s,]+/).filter(Boolean))];
}

// labelHints are words people tend to want, offered as buttons that add or
// remove them. What a label means is the person's: nothing reads these words
// but the model that picks a helper by them.
const labelHints = ["fast", "cheap", "strong", "code", "long-context", "vision"];

// effortsOf is the efforts a model form offers: as published, the provider's.
function effortsOf(f) {
  if (f.set === "published") return f.published?.efforts || [];
  const set = effortSets.find((s) => s.id === f.set);
  if (set?.efforts) return set.efforts;
  return f.custom
    .split(/[\s,]+/)
    .map((e) => e.trim().toLowerCase())
    .filter(Boolean);
}

// askPublished asks what the provider publishes about the model the form
// names, for its placeholders and its efforts; a form adding a model whose
// efforts nobody chose yet takes the published ones.
async function askPublished(ctx, f, redraw) {
  const { conn } = ctx;
  const model = f.model.trim();
  const key = `${f.server}\u0000${model}`;
  if (!f.server || !model || f.publishedFor === key || !conn.has("models.published")) return;
  f.publishedFor = key;
  let found = null;
  try {
    found = await conn.call("models.published", { server: f.server, model });
  } catch {
    found = null;
  }
  if (ui.model !== f || f.publishedFor !== key) return;
  f.published = found;
  if (!f.setChosen) f.set = found?.efforts?.length ? "published" : f.set === "published" ? "none" : f.set;
  redraw();
}

// modelForm is the form that adds a model or changes one. first is the
// first-run page's: the model it saves is also the default, which is what
// that page is for, and the settings Settings › Models keeps (context,
// prices, sampling) are left to it, since a model answers without them.
function modelForm(ctx, redraw, first = false) {
  const { conn, desk } = ctx;
  const f = ui.model;
  const adding = f.mode === "add";
  const providers = (desk.settings?.servers || []).filter(carriesModels);
  if (!f.server) f.server = providers[0]?.name || "";
  // Claude is billed in full on every request unless it is asked to cache, so a
  // form adding a model on Anthropic's API starts with the ask chosen, as it
  // would be saved in the entry: nothing infers it later, and Not asked leaves
  // it off.
  if (adding && !f.cacheChosen) f.settings.cache_control = providerOf(desk, f.server)?.provider === "anthropic" ? "ephemeral" : "";
  const efforts = effortsOf(f);
  // A form in a provider's card adds a model on that provider: the card says
  // which, and another provider's model would not appear where it is added.
  const server = h("select", { class: "input", id: "model-provider", disabled: !adding || !!f.place, onchange: () => ((f.server = server.value), askOffered(ctx, f.server, redraw), askPublished(ctx, f, redraw), redraw()) }, providers.map((p) => h("option", { value: p.name }, `${p.name} · ${p.host}`)));
  server.value = f.server;
  // The models the provider lists are asked for as the form opens, so there
  // is something to pick from without a click in the field first.
  if (adding && f.server && !ui.offered.has(f.server)) askOffered(ctx, f.server, redraw);
  const offered = ui.offered.get(f.server);
  // What most models need none of is folded away when adding, open when
  // changing. Clicking the summary sets it at once, since the form is drawn
  // again as the provider answers and a fold left to report itself would close.
  const extrasOpen = f.extras ?? !adding;
  // A short list is offered as buttons; a long one (OpenRouter's) is typed to.
  const listed = Array.isArray(offered) && offered.length > 0 && offered.length <= 8;
  // The buttons follow what is typed without a redraw of the form under it.
  const save = h("button", { type: "submit", class: "btn accent", id: "model-save" }, f.saving ? "Saving…" : first ? "Save and use it" : "Save the model");
  const testButton = h("button", { type: "button", class: "btn", id: "model-test", title: "Sends one short request to this model, without saving it", onclick: () => test(ctx, "form", { server: f.server, model: f.model.trim() }, redraw) }, "Test it");
  const sync = () => {
    save.disabled = f.saving || !f.model.trim() || !f.server;
    testButton.disabled = !f.model.trim() || !f.server || !!ui.results.get("form")?.pending || !conn.has("models.test");
  };
  const id = h("input", { class: "input", id: "model-id", list: "model-id-options", spellcheck: false, autocomplete: "off", required: true, disabled: !adding, oninput: () => ((f.model = id.value), sync()), onchange: () => askPublished(ctx, f, redraw), onfocus: () => askOffered(ctx, f.server, redraw) });
  id.value = f.model;
  const options = h("datalist", { id: "model-id-options" }, Array.isArray(offered) ? offered.map((n) => h("option", { value: n })) : null);
  const custom = h("input", { class: "input", id: "model-efforts", spellcheck: false, autocomplete: "off", placeholder: "for example low, medium, high", oninput: () => (f.custom = custom.value) });
  custom.value = f.custom;
  const effort = h("select", { class: "input", id: "model-effort", onchange: () => (f.effort = effort.value) }, [h("option", { value: "" }, "Provider default"), ...efforts.map((e) => h("option", { value: e }, effortLabel(e)))]);
  effort.value = efforts.includes(f.effort) ? f.effort : "";
  if (f.effort && !efforts.includes(f.effort)) f.effort = "";
  const labels = h("input", { class: "input", id: "model-labels", spellcheck: false, autocomplete: "off", placeholder: "for example fast, cheap", oninput: () => ((f.labels = labels.value), syncLabels()) });
  labels.value = f.labels;
  const hintButtons = labelHints.map((w) => h("button", { type: "button", class: "chip", "data-label": w, onclick: () => {
    const words = labelWords(f.labels);
    f.labels = (words.includes(w) ? words.filter((x) => x !== w) : [...words, w]).join(", ");
    labels.value = f.labels;
    syncLabels();
  } }, w));
  const syncLabels = () => {
    const words = labelWords(f.labels);
    for (const b of hintButtons) b.setAttribute("aria-pressed", String(words.includes(b.dataset.label)));
  };
  syncLabels();
  const field = (forId, label, control, hint) => h("div", { class: "field" }, h("label", { for: forId }, label), control, hint ? h("p", { class: "hint" }, hint) : null);
  const testing = ui.results.get("form");
  const form = h(
    "form",
    {
      class: "setup-form model-form",
      id: "model-form",
      onsubmit: async (event) => {
        event.preventDefault();
        f.error = "";
        f.saving = true;
        redraw();
        try {
          // As published, the entry declares none, so the provider's apply.
          const params = { server: f.server, model: f.model.trim(), efforts: f.set === "published" ? [] : efforts };
          // The form holds all of a model's settings, so a change sends them
          // all; a model added with none typed sends none, which keeps those of
          // a model the list has already.
          const settings = settingsOf(f, providerOf(desk, f.server)?.provider);
          if (!adding || Object.keys(settings).length) params.settings = settings;
          // The default model's own effort is the default's above; a model
          // of the list starts at the one chosen here.
          if (!f.isDefault && f.effort) params.effort = f.effort;
          // Labels belong to the entry: sent when the form has some or edits a
          // model, so clearing the field clears them, while a model added with
          // none typed keeps those of one the list has already.
          if (!adding || labelWords(f.labels).length) params.labels = labelWords(f.labels);
          if (!adding && !f.isDefault) params.name = f.name;
          desk.settings = await conn.call("settings.model.write", params);
          if (first) {
            // The entry the daemon named is the default, at the effort chosen:
            // sent even when empty, as Settings sends the default's.
            const saved = choicesOf(desk).find((c) => c.server === params.server && c.model === params.model);
            if (!saved) throw new Error(`${params.model} was saved, but is not in the list of models: choose it as the default in Settings › Models.`);
            desk.settings = await conn.call("settings.control.write", { name: saved.name, effort: f.effort });
          }
          ui.model = null;
          ui.results.delete("form");
          await desk.refreshModels();
          toast(first ? `${params.model} is the default for new tasks.` : `${params.model} saved. Test asks it for a reply.`);
        } catch (error) {
          f.error = error.message;
          f.saving = false;
        }
        redraw();
      },
    },
    h("h3", { class: "form-title" }, adding ? (first ? "Which model?" : "Add a model") : `Change ${f.model}`),
    // The first run has just said which provider this is, so it is not asked
    // again; the select stays for the form to read.
    first ? h("div", { hidden: true }, server) : field("model-provider", "Provider", server),
    field(
      "model-id",
      "Model",
      [id, options, listed ? h("div", { class: "row-actions", role: "group", "aria-label": "Models it offers" }, offered.map((name) => h("button", { type: "button", class: "chip", dataset: { offered: name }, "aria-pressed": String(f.model.trim() === name), onclick: () => ((f.model = name), redraw(), askPublished(ctx, f, redraw)) }, name))) : null],
      Array.isArray(offered) ? `${f.server} offers ${offered.length} model${offered.length === 1 ? "" : "s"}: ${listed ? "pick one, or type another" : "type to pick one"}.` : typeof offered === "string" ? `${offered}. Type the model's name.` : "",
    ),
    // What most models need none of: the efforts it takes (the provider's, as
    // published, when it says), what it is for and the effort it starts at.
    // Closed on the first run and when adding; open when changing a model.
    h(
      "details",
      { class: "more-settings", id: "model-more", open: extrasOpen },
      h("summary", { onclick: () => (f.extras = !extrasOpen) }, "More: efforts it takes, what it is for, the effort it starts at"),
      h(
        "div",
        { class: "field" },
        h("label", {}, "Efforts it takes"),
        h(
          "div",
          { class: "row-actions", role: "group", "aria-label": "Efforts it takes" },
          effortSets.filter((s) => s.id !== "published" || f.published?.efforts?.length).map((s) => h("button", { type: "button", class: "chip", id: `efforts-${s.id}`, "aria-pressed": String(f.set === s.id), onclick: () => ((f.set = s.id), (f.setChosen = true), redraw()) }, s.label)),
        ),
        f.set === "custom" ? custom : null,
        h("p", { class: "hint" }, efforts.length ? `Offered as ${efforts.map(effortLabel).join(", ")}${f.set === "published" ? ", as its provider publishes them" : ""}. Sent as the provider's own words: ${{ anthropic: "output_config.effort, with adaptive thinking, to Anthropic (Claude 4.6 and later)", ollama: "think to Ollama" }[providerOf(desk, f.server)?.provider] || "reasoning_effort to a chat-completions server"}.` : "No effort setting is sent to this model, and none is offered."),
        // Efforts typed before nodwyn knew the provider's may have gone stale.
        f.set !== "published" && f.published?.efforts?.length ? h("p", { class: "hint", id: "efforts-published-differ" }, `Its provider publishes ${f.published.efforts.map(effortLabel).join(", ")}: As published follows them.`) : null,
      ),
      h(
        "div",
        { class: "field" },
        h("label", { for: "model-labels" }, "What it is for"),
        labels,
        h("div", { class: "row-actions", role: "group", "aria-label": "Common labels" }, hintButtons),
        h("p", { class: "hint" }, "Words of your own, such as fast or strong, separated by commas. A task you let hand work to other models shows the model its helpers by these, so it can choose without knowing their names; a change reaches tasks already open."),
      ),
      efforts.length && !f.isDefault ? field("model-effort", "Starts at", effort, first ? "The effort a new task asks of it unless one is chosen." : "The effort a task asks of this model unless one is chosen.") : null,
      efforts.length && f.isDefault ? h("p", { class: "hint" }, "The effort a new task starts at is the default's, above.") : null,
    ),
    first ? h("p", { class: "hint" }, "Its context length, prices and the rest of its settings are what its provider publishes, and Settings › Models changes them once it answers.") : settingsFields(f, providerOf(desk, f.server)?.provider, redraw, desk),
    f.error ? h("p", { class: "error", role: "alert" }, f.error) : null,
    resultLine(testing),
    h("div", { class: "row-actions" }, save, testButton, first ? null : h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.model = null), ui.results.delete("form"), redraw()) }, "Cancel")),
  );
  sync();
  return form;
}

// askOffered asks a provider for the models it lists, once until the provider
// is saved again: the daemon asks with the provider's key, as OpenAI and
// Cerebras list their models only to one.
async function askOffered(ctx, name, redraw) {
  const { conn } = ctx;
  if (!name || ui.offered.has(name) || !conn.has("models.offered")) return;
  ui.offered.set(name, `Asking ${name} for its models…`);
  try {
    ui.offered.set(name, await conn.call("models.offered", { server: name }));
  } catch (error) {
    ui.offered.set(name, error.message);
  }
  redraw();
}

async function removeModel(ctx, name, redraw) {
  const { conn, desk } = ctx;
  ui.removing = "";
  try {
    desk.settings = await conn.call("settings.model.remove", { name });
    await desk.refreshModels();
    toast("Removed from the list.");
  } catch (error) {
    toast(error.message);
  }
  redraw();
}

// ---------- testing ----------
// test sends one short request to a model and keeps what came back, in the
// daemon's words: a key the server did not accept, a model it does not have,
// an effort it refuses, or a reply.
async function test(ctx, key, params, redraw) {
  const { conn } = ctx;
  if (!params) return;
  ui.results.set(key, { pending: true });
  redraw();
  try {
    ui.results.set(key, await conn.call("models.test", params, 70000));
  } catch (error) {
    ui.results.set(key, { ok: false, state: "error", detail: error.message });
  }
  redraw();
}

function resultLine(result) {
  if (!result) return null;
  if (result.pending) return h("p", { class: "test-result", role: "status" }, "Asking the model for a reply…");
  return h(
    "p",
    { class: `test-result ${result.ok ? "ok" : "bad"}`, role: result.ok ? "status" : "alert", dataset: { state: result.state } },
    h("span", { class: `dot ${result.ok ? "ok" : "bad"}` }),
    result.ok ? `${result.detail}${result.effort ? ` with ${effortLabel(result.effort)} effort` : ""}.` : `${result.detail}`,
  );
}

// modelsRows is the body of Settings › Models, as [label, value] pairs.
export function modelsRows(ctx, redraw) {
  return [...defaultRow(ctx, redraw), ...providerRows(ctx, redraw)];
}

// firstModel is the first-run page's model setup: the forms of Settings ›
// Models one after the other, a provider and then a model on it, which
// becomes the default. There is one way to set a model up, so the page that
// welcomes a person does not describe another. The state is the module's, as
// Settings keeps it, so a redraw while a form is typed into loses nothing.
export function firstModel(ctx, redraw) {
  const { desk } = ctx;
  const providers = (desk.settings?.servers || []).filter(carriesModels);
  // Ollama first: it needs no key, and nothing leaves the computer until a
  // person picks a service.
  if (!providers.length && !ui.provider) ui.provider = providerDraft(null, "ollama");
  if (providers.length && !ui.provider && !ui.model) ui.model = modelDraft(null);
  const body = [];
  if (ui.provider) {
    body.push(providerForm(ctx, redraw, true));
    // A key is kept in the keychain, which the person may not have made yet:
    // made here, beside the form that needs it. It is a form of its own: forms
    // do not nest.
    if (hasKey(ui.provider.kind) && !desk.keychain?.unlocked) {
      body.push(h("div", { class: "callout plain", id: "first-keychain" }, keychainForm(ctx, { onDone: redraw })));
    }
  } else {
    const p = providerOf(desk, ui.model.server);
    body.push(
      h(
        "div",
        { class: "first-provider" },
        h("p", { class: "hint" }, p ? `${kinds[p.provider]?.label || p.provider} at ${p.host}${p.credential ? (p.authentication === "chatgpt" ? ", with your ChatGPT plan" : p.key === "ready" ? ", with its key stored" : "") : ""}.` : ""),
        h("button", { type: "button", class: "btn small", id: "first-another", onclick: () => ((ui.provider = providerDraft(null, "ollama")), redraw()) }, "Use another provider"),
      ),
      modelForm(ctx, redraw, true),
    );
  }
  return h("div", { id: "first-model" }, ...body);
}
