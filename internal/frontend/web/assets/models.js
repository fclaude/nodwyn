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
  { id: "openai", label: "OpenAI", kind: "openai", url: "https://api.openai.com/v1", dialect: "" },
  { id: "openrouter", label: "OpenRouter", kind: "openai", url: "https://openrouter.ai/api/v1", dialect: "" },
  { id: "cerebras", label: "Cerebras", kind: "openai", url: "https://api.cerebras.ai/v1", dialect: "cerebras" },
  { id: "other", label: "Other server", kind: "openai", url: "", dialect: "" },
];
const dialects = [["", "Standard (reasoning_content)"], ["cerebras", "Cerebras (reasoning)"]];

// The words of effort a model may take. A provider decides which it accepts,
// so the sets here are only shortcuts; anything else is typed.
const effortSets = [
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
    model: null, // the model form: {mode, name, server, model, set, custom, effort, isDefault, error, saving}
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
    f.kind === "openai" ? field("provider-dialect", "How it reads the protocol", dialect, "Chosen by you, never guessed from the address. Cerebras needs its own for reasoning to survive tool calls.") : null,
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
      inline ? h("p", { class: "hint" }, "Gives its own address in config.toml, so its efforts are set there. Choosing it as the default on a provider above moves it to one.") : null,
      resultLine(result),
    );
  });
  return row(
    "Models you can choose",
    choices.length ? h("div", { class: "models" }, list) : h("p", { class: "hint" }, "No model yet."),
    ui.model ? modelForm(ctx, redraw) : null,
    editable && !ui.model ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "add-model", disabled: !(desk.settings?.servers || []).some((p) => !p.inline), title: (desk.settings?.servers || []).some((p) => !p.inline) ? "" : "Add a provider first", onclick: () => openModel(null, redraw) }, icon("plus", 14), "Add a model")) : null,
    h("p", { class: "hint" }, "These are the models the New task window and a conversation offer. A model takes an effort setting only if you say which: nodwyn never guesses it from the model's name or the provider, and sends none to a model that declares none."),
  );
}

function setOf(efforts) {
  const found = effortSets.find((s) => s.efforts && s.efforts.length === efforts.length && s.efforts.every((e, i) => e === efforts[i]));
  return found ? found.id : "custom";
}

function openModel(c, redraw) {
  ui.model = c
    ? { mode: "edit", name: c.name, server: c.server, model: c.model, set: setOf(c.efforts || []), custom: (c.efforts || []).join(", "), effort: c.effort || "", isDefault: !!c.default, error: "" }
    : { mode: "add", name: "", server: "", model: "", set: "none", custom: "", effort: "", isDefault: false, error: "" };
  redraw();
}

// effortsOf is the efforts a model form declares.
function effortsOf(f) {
  const set = effortSets.find((s) => s.id === f.set);
  if (set?.efforts) return set.efforts;
  return f.custom
    .split(/[\s,]+/)
    .map((e) => e.trim().toLowerCase())
    .filter(Boolean);
}

function modelForm(ctx, redraw) {
  const { conn, desk } = ctx;
  const f = ui.model;
  const adding = f.mode === "add";
  const providers = (desk.settings?.servers || []).filter((p) => !p.inline);
  if (!f.server) f.server = providers[0]?.name || "";
  const efforts = effortsOf(f);
  const server = h("select", { class: "input", id: "model-provider", disabled: !adding, onchange: () => ((f.server = server.value), askOffered(ctx, f.server, redraw), redraw()) }, providers.map((p) => h("option", { value: p.name }, `${p.name} · ${p.host}`)));
  server.value = f.server;
  const offered = ui.offered.get(f.server);
  // The buttons follow what is typed without a redraw of the form under it.
  const save = h("button", { type: "submit", class: "btn accent", id: "model-save" }, f.saving ? "Saving…" : "Save the model");
  const testButton = h("button", { type: "button", class: "btn", id: "model-test", title: "Sends one short request to this model, without saving it", onclick: () => test(ctx, "form", { server: f.server, model: f.model.trim() }, redraw) }, "Test it");
  const sync = () => {
    save.disabled = f.saving || !f.model.trim() || !f.server;
    testButton.disabled = !f.model.trim() || !f.server || !!ui.results.get("form")?.pending || !conn.has("models.test");
  };
  const id = h("input", { class: "input", id: "model-id", list: "model-id-options", spellcheck: false, autocomplete: "off", required: true, disabled: !adding, oninput: () => ((f.model = id.value), sync()), onfocus: () => askOffered(ctx, f.server, redraw) });
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
          const params = { server: f.server, model: f.model.trim(), efforts };
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
      h("div", { class: "row-actions", role: "group", "aria-label": "Efforts it takes" }, effortSets.map((s) => h("button", { type: "button", class: "chip", id: `efforts-${s.id}`, "aria-pressed": String(f.set === s.id), onclick: () => ((f.set = s.id), redraw()) }, s.label))),
      f.set === "custom" ? custom : null,
      h("p", { class: "hint" }, efforts.length ? `Offered as ${efforts.map(effortLabel).join(", ")}. Sent as the provider's own words: reasoning_effort to a chat-completions server, think to Ollama.` : "No effort setting is sent to this model, and none is offered."),
    ),
    efforts.length && !f.isDefault ? field("model-effort", "Starts at", effort, "The effort a task asks of this model unless one is chosen.") : null,
    efforts.length && f.isDefault ? h("p", { class: "hint" }, "The effort a new task starts at is the default's, above.") : null,
    f.error ? h("p", { class: "error", role: "alert" }, f.error) : null,
    resultLine(testing),
    h("div", { class: "row-actions" }, save, testButton, h("button", { type: "button", class: "btn ghost", onclick: () => ((ui.model = null), redraw()) }, "Cancel")),
  );
  sync();
  return form;
}

// askOffered asks a provider for the models it lists, once, without a key.
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
