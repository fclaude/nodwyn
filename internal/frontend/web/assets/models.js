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
import { h, icon, toast } from "./dom.js";
import { ModelPicker, choicesOf, effortLabel, statusOf, defaultSelection } from "./modelpicker.js";

// What each kind of provider needs, said before it is asked for: where it is,
// whether it takes a key and, for a chat-completions server, how it reads the
// protocol. A preset only fills the form in; the daemon is told what the form
// says, and nothing is inferred from an address later (ADR 8, ADR 41).
const kinds = {
  ollama: { label: "Ollama", needs: "Ollama on this computer or your network. It needs an address, and no key." },
  openai: { label: "OpenAI-compatible", needs: "Any server that speaks the OpenAI chat-completions API. It needs an address and, for a hosted one, an API key, which is kept in the keychain." },
};
const presets = [
  { id: "ollama", label: "Ollama", kind: "ollama", url: "http://localhost:11434", dialect: "" },
  { id: "openai", label: "OpenAI", kind: "openai", url: "https://api.openai.com/v1", dialect: "responses" },
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
  { id: "onoff", label: "Off · On (Ollama)", efforts: ["off", "on"] },
  { id: "custom", label: "Other…", efforts: null },
];

const state = {
  words: { ready: "ready", missing: "model not installed", unreachable: "not answering", unconfigured: "not set up", needs_key: "needs its key", locked: "keychain locked", unbound: "key not allowed" },
};
const dotOf = (s) => (s === "ready" ? "ok" : s === "unreachable" ? "bad" : s ? "warn" : "");

let ui = fresh();
function fresh() {
  return {
    provider: null, // the provider form being filled in: {mode, name, preset, kind, url, dialect, key, error, saving}
    model: null, // the model form: {mode, name, server, model, set, custom, effort, isDefault, settings, more, error, saving}
    removing: "", // "provider:NAME" or "model:NAME" waiting for its confirm
    results: new Map(), // "provider:NAME" | "model:NAME" | "form" -> the last test, or {pending: true}
    offered: new Map(), // provider name -> model ids it lists, or the reason it does not
    defaultPick: null, // {name, effort} chosen for the default, not yet saved
    savingDefault: false,
  };
}
// A visit to Settings starts with nothing half-typed.
export function resetModelsUi() {
  ui = fresh();
}

const row = (label, ...value) => [h("div", { class: "label" }, label), h("div", { class: "value" }, ...value)];
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
function defaultRow(ctx, redraw) {
  const { conn, desk } = ctx;
  const choices = choicesOf(desk);
  if (!choices.length) return row("Default for new tasks", h("p", { class: "hint" }, "No model is set up yet. Add a provider and a model below."));
  const current = defaultSelection(desk);
  const selection = ui.defaultPick || current;
  // Models can be listed with none chosen as the default (a config.toml with
  // [models.NAME] entries and no [control]): the picker then shows none, and
  // choosing one is what saves.
  const dirty = !!ui.defaultPick && (!current || ui.defaultPick.name !== current.name || ui.defaultPick.effort !== current.effort);
  const picker = (ui.defaultPicker ||= new ModelPicker("default-model"));
  picker.update({
    desk,
    selection,
    disabled: !conn.has("settings.control.write") || ui.savingDefault,
    onPick: (next) => {
      ui.defaultPick = next;
      redraw();
    },
  });
  const save = h(
    "button",
    {
      type: "button",
      class: "btn accent",
      id: "default-save",
      disabled: !dirty || ui.savingDefault || !conn.has("settings.control.write"),
      onclick: async () => {
        ui.savingDefault = true;
        redraw();
        try {
          // The effort is sent even when empty: the default model's effort is
          // what is asked, never what the model happened to have.
          desk.settings = await conn.call("settings.control.write", { name: selection.name, effort: selection.effort });
          ui.defaultPick = null;
          await desk.refreshModels();
          toast("Saved. New tasks start on this model and effort; open tasks keep theirs.");
        } catch (error) {
          toast(error.message);
        } finally {
          ui.savingDefault = false;
          redraw();
        }
      },
    },
    ui.savingDefault ? "Saving…" : "Save the default",
  );
  return row(
    "Default for new tasks",
    picker.el,
    h("div", { class: "row-actions" }, save, dirty ? h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.defaultPick = null), redraw()) }, "Discard") : null),
    h("p", { class: "hint" }, "New tasks start on this model and effort. A task's own choice, under its message box, changes only that task, and this stays as it is. Tasks already open keep what they started with."),
  );
}

// ---------- providers ----------
function providerRows(ctx, redraw) {
  const { conn, desk } = ctx;
  const providers = desk.settings?.servers || [];
  const editable = canEdit(conn);
  const list = providers.map((p) => {
    const key = `provider:${p.name}`;
    const status = worst(desk, p);
    const result = ui.results.get(key);
    const first = (p.uses || []).find((name) => choicesOf(desk).some((c) => c.name === name)) || (p.uses || [])[0];
    const detail = status.detail || (status.state ? "" : first ? "Not checked yet: Test asks it for a reply." : "No model is on it yet: add one to test it.");
    const confirming = ui.removing === key;
    return h(
      "div",
      { class: "provider-row", id: `provider-${p.name}`, dataset: { provider: p.name } },
      h(
        "div",
        { class: "list-row" },
        h("span", { class: `dot ${dotOf(status.state)}` }),
        h(
          "div",
          { class: "main" },
          h("strong", {}, p.inline ? p.host : p.name),
          h("span", {}, `${kinds[p.provider]?.label || p.provider}${p.dialect ? ` (${p.dialect})` : ""} · ${p.host}${p.credential ? (p.key === "ready" ? " · key stored" : "") : p.provider === "openai" ? " · no key" : ""}`),
          detail ? h("span", { class: status.state && status.state !== "ready" ? "warn-text" : "" }, detail) : null,
        ),
        h("div", { class: "end" }, status.state ? h("span", { class: "state-word" }, state.words[status.state] || status.state) : null),
      ),
      h(
        "div",
        { class: "row-actions provider-actions" },
        h("button", { type: "button", class: "btn small", disabled: !first || !conn.has("models.test") || result?.pending, title: first ? "Sends one short request to a model on this provider, as a task would" : "Add a model on this provider to test it", onclick: () => test(ctx, key, first ? { name: first } : null, redraw) }, "Test"),
        editable && !p.inline ? h("button", { type: "button", class: "btn small", onclick: () => openProvider(p, redraw) }, "Edit") : null,
        editable && !p.inline ? (confirming ? [h("span", { class: "muted" }, "Remove it? Its key stays in the keychain."), h("button", { type: "button", class: "btn small danger", onclick: () => removeProvider(ctx, p.name, redraw) }, "Remove"), h("button", { type: "button", class: "btn small ghost", onclick: () => ((ui.removing = ""), redraw()) }, "Cancel")] : h("button", { type: "button", class: "btn small danger", disabled: (p.uses || []).length > 0, title: (p.uses || []).length ? `Still used by ${p.uses.join(", ")}` : "", onclick: () => ((ui.removing = key), redraw()) }, "Remove")) : null,
      ),
      p.inline ? h("p", { class: "hint" }, "Given by a model's own table in config.toml. Choosing a default model on a provider below moves it to one.") : null,
      resultLine(result),
    );
  });
  const form = ui.provider ? providerForm(ctx, redraw) : null;
  return row(
    "Providers",
    providers.length ? h("div", { class: "providers" }, list) : h("p", { class: "hint" }, "No provider yet. Add the server your models are on."),
    form,
    h(
      "div",
      { class: "row-actions" },
      editable && !ui.provider ? h("button", { type: "button", class: "btn", id: "add-provider", onclick: () => openProvider(null, redraw) }, icon("plus", 14), "Add a provider") : null,
      h("button", { type: "button", class: "btn", id: "check-again", title: "Asks each provider for its models again, without a key", onclick: () => desk.refreshModels(true) }, "Check again"),
    ),
    !editable ? h("p", { class: "hint" }, "Only the computer nodwyn runs on can change providers.") : null,
    h("p", { class: "hint" }, "A key is stored encrypted in the keychain and sent only to its provider's address; config.toml never holds it. A provider's key follows a change of path, not of host."),
  );
}

function openProvider(p, redraw) {
  const preset = p ? "" : "openai";
  const dflt = presets.find((x) => x.id === preset);
  ui.provider = p
    ? { mode: "edit", name: p.name, preset: "", kind: p.provider, url: p.url, dialect: p.dialect || "", key: "", hasKey: !!p.credential, host: p.host, error: "" }
    : { mode: "add", name: "", preset, kind: dflt.kind, url: dflt.url, dialect: dflt.dialect, key: "", hasKey: false, host: "", error: "" };
  redraw();
}

function providerForm(ctx, redraw) {
  const { conn, desk } = ctx;
  const f = ui.provider;
  const adding = f.mode === "add";
  const field = (id, label, control, hint) => h("div", { class: "field" }, h("label", { for: id }, label), control, hint ? h("p", { class: "hint" }, hint) : null);
  const url = h("input", { class: "input", id: "provider-url", spellcheck: false, autocomplete: "off", required: true, value: f.url, placeholder: "https://…", oninput: () => (f.url = url.value) });
  url.value = f.url;
  const key = h("input", { class: "input", id: "provider-key", type: "password", autocomplete: "off", spellcheck: false, placeholder: f.hasKey ? "leave empty to keep the saved key" : "", oninput: () => (f.key = key.value) });
  key.value = f.key;
  const dialect = h("select", { class: "input", id: "provider-dialect", onchange: () => (f.dialect = dialect.value) }, dialects.map(([v, label]) => h("option", { value: v }, label)));
  dialect.value = f.dialect;
  const keychainReady = !!desk.keychain?.unlocked;
  const sameHostNow = (() => {
    try {
      return f.mode === "add" || new URL(f.url).host === f.host;
    } catch {
      return true;
    }
  })();
  const kind = (id) => h("button", { type: "button", class: "chip", "aria-pressed": String(f.kind === id), disabled: !adding, onclick: () => ((f.kind = id), (f.preset = ""), redraw()) }, kinds[id].label);
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
          if (!adding) params.name = f.name;
          if (f.kind === "openai" && f.dialect) params.dialect = f.dialect;
          if (f.kind === "openai" && apiKey) params.api_key = apiKey;
          desk.settings = await conn.call("settings.provider.write", params);
          const saved = (desk.settings.servers || []).find((s) => (adding ? s.url.replace(/\/+$/, "") === params.url.replace(/\/+$/, "") : s.name === f.name));
          // Its list is asked with its key, so a new key or address asks again.
          if (saved) ui.offered.delete(saved.name);
          ui.provider = null;
          await desk.refreshModels();
          const key = saved ? worst(desk, saved) : null;
          toast(saved?.key && saved.key !== "ready" ? `Saved, but ${saved.key_detail}.` : key?.state && key.state !== "ready" ? `Saved, but ${key.detail || state.words[key.state]}.` : adding ? "Provider saved. Add a model on it, then Test." : "Provider saved.");
        } catch (error) {
          f.error = error.message;
          f.saving = false;
        }
        redraw();
      },
    },
    h("h3", { class: "form-title" }, adding ? "Add a provider" : `Change ${f.name}`),
    adding ? h("div", { class: "row-actions", role: "group", "aria-label": "Provider" }, presets.map((p) => h("button", { type: "button", class: "chip", id: `preset-${p.id}`, "aria-pressed": String(f.preset === p.id), onclick: () => ((f.preset = p.id), (f.kind = p.kind), (f.url = p.url), (f.dialect = p.dialect), redraw()) }, p.label))) : null,
    h("div", { class: "row-actions", role: "group", "aria-label": "Kind of server" }, kind("ollama"), kind("openai")),
    h("p", { class: "hint" }, kinds[f.kind].needs),
    field("provider-url", "Address", url, f.kind === "openai" ? "Including the version, such as https://api.openai.com/v1." : "Such as http://localhost:11434, or http://workshop:11434 on your network."),
    f.kind === "openai" ? field("provider-dialect", "How it reads the protocol", dialect, "Chosen by you, never guessed from the address. Cerebras needs its own for reasoning to survive tool calls; OpenAI needs Responses for a reasoning model to take an effort and tools together.") : null,
    f.kind === "openai"
      ? field(
          "provider-key",
          "API key",
          key,
          !keychainReady
            ? "Create or unlock the keychain first: keys are kept there, never in config.toml."
            : f.hasKey
              ? "Stored encrypted and sent only to this address. Type a new key to replace it."
              : "Stored encrypted and sent only to this address. Every model on this provider uses it.",
        )
      : null,
    f.kind === "openai" && !keychainReady ? h("div", { class: "row-actions" }, h("a", { class: "btn small", href: "#/settings", onclick: () => setTimeout(() => document.getElementById("section-keys")?.scrollIntoView({ block: "start" }), 0) }, "Open the keychain settings")) : null,
    !sameHostNow && f.hasKey ? h("p", { class: "warn-text" }, "This is another host. The saved key is not sent there: type its key, or the provider is left without one.") : null,
    f.error ? h("p", { class: "error", role: "alert" }, f.error) : null,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn accent", id: "provider-save", disabled: f.saving || (f.kind === "openai" && !!f.key && !keychainReady) }, f.saving ? "Saving…" : "Save the provider"), h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.provider = null), redraw()) }, "Cancel")),
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
function modelRows(ctx, redraw) {
  const { conn, desk } = ctx;
  const choices = choicesOf(desk);
  const editable = canEdit(conn) && conn.has("settings.model.write");
  const list = choices.map((c) => {
    const key = `model:${c.name}`;
    const status = statusOf(desk, c.name);
    const result = ui.results.get(key);
    const confirming = ui.removing === key;
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
          h("span", {}, `${c.server || c.host} · ${c.efforts?.length ? `effort: ${c.efforts.map(effortLabel).join(" · ")}${c.effort ? `, starts at ${effortLabel(c.effort)}` : ""}` : "no effort setting"}`),
          h("span", { class: "model-settings" }, settingsLine(c.settings, c.provider, c.published)),
          status && status.state !== "ready" ? h("span", { class: "warn-text" }, status.detail || state.words[status.state]) : null,
        ),
        h("div", { class: "end" }, status ? h("span", { class: "state-word" }, state.words[status.state] || status.state) : h("span", { class: "state-word" }, "checking")),
      ),
      h(
        "div",
        { class: "row-actions model-actions" },
        h("button", { type: "button", class: "btn small", disabled: !conn.has("models.test") || result?.pending, title: "Sends one short request to this model, as a task would", onclick: () => test(ctx, key, { name: c.name }, redraw) }, "Test"),
        editable && !inline ? h("button", { type: "button", class: "btn small", onclick: () => openModel(c, redraw) }, "Edit") : null,
        editable && !inline && !c.default ? (confirming ? [h("span", { class: "muted" }, "Remove it from the list?"), h("button", { type: "button", class: "btn small danger", onclick: () => removeModel(ctx, c.name, redraw) }, "Remove"), h("button", { type: "button", class: "btn small ghost", onclick: () => ((ui.removing = ""), redraw()) }, "Cancel")] : h("button", { type: "button", class: "btn small danger", onclick: () => ((ui.removing = key), redraw()) }, "Remove")) : null,
      ),
      inline ? h("p", { class: "hint" }, "Gives its own address in config.toml, so its efforts and settings are set there. Choosing it as the default on a provider above moves it to one.") : null,
      resultLine(result),
    );
  });
  return row(
    "Models you can choose",
    choices.length ? h("div", { class: "models" }, list) : h("p", { class: "hint" }, "No model yet."),
    ui.model ? modelForm(ctx, redraw) : null,
    editable && !ui.model ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "add-model", disabled: !(desk.settings?.servers || []).some((p) => !p.inline), title: (desk.settings?.servers || []).some((p) => !p.inline) ? "" : "Add a provider first", onclick: () => openModel(null, redraw) }, icon("plus", 14), "Add a model")) : null,
    h("p", { class: "hint" }, "These are the models the New task window and a conversation offer. Each has its own settings: its efforts, its context length, its reply cap, its prices and whether it takes images. What a model's own settings leave empty is what its provider publishes, as models.dev collects it, for the providers nodwyn knows (OpenAI, OpenRouter, Cerebras and others); nothing is guessed from a model's name, and a model nothing is known of takes no effort setting unless you say which."),
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
  number("seed", "Seed", { whole: true, signed: true });
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
  for (const name of ["output_field", "cache_key", "cache_control"]) if (s[name]) out[name] = s[name];
  return out;
}

// tokens reads a count as the form holds it, "1,050,000" included; 0 when it
// is empty or does not read.
const tokens = (raw) => Number(String(raw).replace(/[,_\s]/g, "")) || 0;

// windowLine says what a conversation on the model gets from the form's
// context length and reply cap, sized as the daemon sizes it
// (config.Window.ReplyCap, Sized): the reply cap comes out of the context,
// and the window has three quarters of the rest, or the water marks
// Settings › Context window sets when the context holds them.
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
      options.map(([value, text]) => h("button", { type: "button", class: "chip", id: `model-${name}-${value === "" ? "default" : String(value)}`, "aria-pressed": String(current === value), onclick: () => ((s[name] = value), redraw()) }, text)),
    );
  const openai = kind !== "ollama";
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
        : `The most one reply may take, thinking included. It comes out of the context length, and the conversation gets the rest. Empty is ${count(reserve)} (Settings › Context window), or a quarter of a small context. A reasoning model at a high effort may need more.`,
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
            : "Whether an attached image may be sent to this model. A chat-completions server doesn't say, so unless you choose No a model is taken to read images: one that can't fails that request, and what you wrote is kept.",
        )
      : null,
    h(
      "details",
      { class: "more-settings", open: f.more, ontoggle: (event) => (f.more = event.target.open) },
      h("summary", {}, "More settings"),
      h(
        "div",
        { class: "field" },
        h("label", { for: "model-temperature" }, "Sampling"),
        h("div", { class: "row-actions" }, h("span", { class: "muted" }, "Temperature"), box("temperature", "Temperature", { placeholder: "server's" }), h("span", { class: "muted" }, "seed"), box("seed", "Seed", { placeholder: "none" })),
        h("p", { class: "hint" }, "Temperature is 0 to 2; empty leaves both to the server. 0 with a seed makes replies as repeatable as the server allows."),
      ),
      openai
        ? [
            field("model-output_field-default", "Reply cap field", chips("output_field", "Reply cap field", [["", "max_completion_tokens"], ["max_tokens", "max_tokens"]]), "Reasoning models refuse the older max_tokens; some servers know only that one."),
            field("model-cache_key-default", "Session key", chips("cache_key", "Session key", [["", "Header and field"], ["header", "Header only"], ["off", "Not sent"]]), "Names the session to the server, so it can keep the requests on the replica that holds their prompt cache. Header only for a server that refuses fields it doesn't know."),
            field(
              "model-cache_control-default",
              "Prompt cache",
              chips("cache_control", "Prompt cache", [["", "Not asked"], ["ephemeral", "5 minutes"], ["1h", "1 hour"]]),
              "A Claude model, through OpenRouter or another provider, caches its prompt only when asked: without this every request is billed in full, however little changed. Asking has the provider cache the conversation so far for the time chosen, and read it back at a tenth of the price (a write costs a quarter more). Leave it off for a model that caches by itself, which is most others, and for a server that refuses fields it doesn't know.",
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

function openModel(c, redraw) {
  // A model whose efforts are those its provider publishes is shown as such,
  // whether its entry repeats them or says none.
  const same = (a = [], b = []) => a.length === b.length && a.every((e, i) => e === b[i]);
  const published = c?.published || null;
  ui.model = c
    ? { mode: "edit", name: c.name, server: c.server, model: c.model, set: published?.efforts?.length && same(c.efforts, published.efforts) ? "published" : setOf(c.efforts || []), custom: (c.efforts || []).join(", "), effort: c.effort || "", isDefault: !!c.default, settings: settingsForm(c.settings), published, more: false, error: "" }
    : { mode: "add", name: "", server: "", model: "", set: "none", custom: "", effort: "", isDefault: false, settings: settingsForm(), published: null, more: false, error: "" };
  redraw();
}

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

function modelForm(ctx, redraw) {
  const { conn, desk } = ctx;
  const f = ui.model;
  const adding = f.mode === "add";
  const providers = (desk.settings?.servers || []).filter((p) => !p.inline);
  if (!f.server) f.server = providers[0]?.name || "";
  const efforts = effortsOf(f);
  const server = h("select", { class: "input", id: "model-provider", disabled: !adding, onchange: () => ((f.server = server.value), askOffered(ctx, f.server, redraw), askPublished(ctx, f, redraw), redraw()) }, providers.map((p) => h("option", { value: p.name }, `${p.name} · ${p.host}`)));
  server.value = f.server;
  const offered = ui.offered.get(f.server);
  // The buttons follow what is typed without a redraw of the form under it.
  const save = h("button", { type: "submit", class: "btn accent", id: "model-save" }, f.saving ? "Saving…" : "Save the model");
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
          if (!adding && !f.isDefault) params.name = f.name;
          desk.settings = await conn.call("settings.model.write", params);
          ui.model = null;
          ui.results.delete("form");
          await desk.refreshModels();
          toast(`${params.model} saved. Test asks it for a reply.`);
        } catch (error) {
          f.error = error.message;
          f.saving = false;
        }
        redraw();
      },
    },
    h("h3", { class: "form-title" }, adding ? "Add a model" : `Change ${f.model}`),
    field("model-provider", "Provider", server),
    field("model-id", "Model", [id, options], Array.isArray(offered) ? `${f.server} offers ${offered.length} model${offered.length === 1 ? "" : "s"}: type to pick one.` : typeof offered === "string" ? `${offered}. Type the model's name.` : ""),
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
      h("p", { class: "hint" }, efforts.length ? `Offered as ${efforts.map(effortLabel).join(", ")}${f.set === "published" ? ", as its provider publishes them" : ""}. Sent as the provider's own words: reasoning_effort to a chat-completions server, think to Ollama.` : "No effort setting is sent to this model, and none is offered."),
      // Efforts typed before nodwyn knew the provider's may have gone stale.
      f.set !== "published" && f.published?.efforts?.length ? h("p", { class: "hint", id: "efforts-published-differ" }, `Its provider publishes ${f.published.efforts.map(effortLabel).join(", ")}: As published follows them.`) : null,
    ),
    efforts.length && !f.isDefault ? field("model-effort", "Starts at", effort, "The effort a task asks of this model unless one is chosen.") : null,
    efforts.length && f.isDefault ? h("p", { class: "hint" }, "The effort a new task starts at is the default's, above.") : null,
    settingsFields(f, providerOf(desk, f.server)?.provider, redraw, desk),
    f.error ? h("p", { class: "error", role: "alert" }, f.error) : null,
    resultLine(testing),
    h("div", { class: "row-actions" }, save, testButton, h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.model = null), redraw()) }, "Cancel")),
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
  return [...defaultRow(ctx, redraw), ...providerRows(ctx, redraw), ...modelRows(ctx, redraw)];
}
