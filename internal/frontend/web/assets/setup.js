// Setting nodwyn up without a terminal (ADR 30): choose the model, and unlock
// or create the keychain that holds keys. The daemon validates and writes;
// these forms only describe the choice.
import { h, toast } from "./dom.js";

const presets = {
  ollama: { url: "http://localhost:11434", hint: "Ollama on this computer, or another one on your network (for example http://workshop:11434)." },
  openai: { url: "https://api.openai.com/v1", hint: "Any server that speaks the OpenAI chat-completions API. Its key is kept in the keychain." },
};

const hostOf = (address) => {
  try {
    return new URL(address).host;
  } catch {
    return address;
  }
};

// modelForm chooses the main model: a model on a server config.toml already
// has, picked from what the server lists, or one on another server, which
// the daemon then saves as a server of its own with its key.
export function modelForm(ctx, { onSaved, compact = false } = {}) {
  const { conn, desk } = ctx;
  const current = desk.settings?.control || {};
  const servers = desk.settings?.servers || [];
  // server is the configured server chosen, or "" for another one.
  let server = servers.some((s) => s.name === current.Server) ? current.Server : "";
  let provider = server ? "ollama" : current.Provider || current.provider || "ollama";
  const url = h("input", { class: "input", id: "model-url", spellcheck: false, autocomplete: "off" });
  url.value = (!server && (current.URL || current.url)) || presets[provider].url;
  const options = h("datalist", { id: "model-options" });
  const model = h("input", { class: "input", id: "model-name", list: "model-options", required: true, spellcheck: false, autocomplete: "off", onfocus: () => list() });
  model.value = current.Model || current.model || "";
  const offered = h("p", { class: "hint", id: "model-offered" });
  const key = h("input", { class: "input", id: "model-key", type: "password", autocomplete: "off", spellcheck: false, placeholder: current.Credential || current.credential ? "leave empty to keep the saved key" : "" });
  const keyField = h("div", { class: "field" }, h("label", { for: "model-key" }, "API key"), key, h("p", { class: "hint" }, "Stored encrypted in the keychain and sent only to this server's address. Every model you choose on this server uses it."));
  const hint = h("p", { class: "hint" });
  const error = h("p", { class: "error", role: "alert" });
  const kind = (id) => servers.find((s) => s.name === id)?.provider || provider;

  // What the chosen server lists fills the model's suggestions, asked for
  // when the model field is reached: Settings redraws this form often, and
  // a list can be long. Another server is listed once it is saved.
  let listed = null;
  let asked = 0;
  const list = async () => {
    if (listed === server) return;
    listed = server;
    const ask = ++asked;
    options.replaceChildren();
    offered.textContent = "";
    if (!server || !conn.has("models.offered")) return;
    offered.textContent = `Asking ${server} for its models…`;
    try {
      const names = await conn.call("models.offered", { server });
      if (ask !== asked) return;
      options.replaceChildren(...names.map((n) => h("option", { value: n })));
      offered.textContent = names.length ? `${server} offers ${names.length} model${names.length === 1 ? "" : "s"}: type to pick one.` : `${server} lists no models.`;
    } catch (e) {
      if (ask === asked) offered.textContent = `${e.message}. Type the model's name.`;
    }
  };
  const show = () => {
    const other = !server;
    newServer.hidden = !other;
    keyField.hidden = !other || provider !== "openai";
    url.required = other;
    const s = servers.find((s) => s.name === server);
    hint.textContent = other ? presets[provider].hint : `${s.provider === "openai" ? "OpenAI-compatible" : "Ollama"} at ${hostOf(s.url)}${s.credential ? ", with its key from the keychain" : ""}.`;
    model.placeholder = kind(server) === "ollama" ? "for example qwen3:32b" : "for example gpt-5";
  };
  const pressed = (event) => {
    for (const b of event.currentTarget.parentElement.children) b.setAttribute("aria-pressed", String(b === event.currentTarget));
  };
  const serverChoice = (id, label, sub) =>
    h(
      "button",
      {
        type: "button",
        class: "chip",
        "aria-pressed": String(server === id),
        onclick: (event) => {
          pressed(event);
          if (server !== id) model.value = "";
          server = id;
          show();
          list();
        },
      },
      label,
      sub ? h("span", { class: "sub" }, ` ${sub}`) : null,
    );
  const choice = (id, label) =>
    h(
      "button",
      {
        type: "button",
        class: "chip",
        "aria-pressed": String(provider === id),
        onclick: (event) => {
          pressed(event);
          provider = id;
          if (Object.values(presets).some((p) => p.url === url.value) || !url.value) url.value = presets[id].url;
          show();
        },
      },
      label,
    );
  const newServer = h(
    "div",
    { class: "fields" },
    h("div", { class: "row-actions", role: "group", "aria-label": "Kind of model server" }, choice("ollama", "Ollama"), choice("openai", "OpenAI-compatible")),
    h("div", { class: "field" }, h("label", { for: "model-url" }, "Server address"), url),
  );
  const save = h("button", { type: "submit", class: "btn accent big" }, "Save and check");
  const form = h(
    "form",
    {
      class: "setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        save.disabled = true;
        const apiKey = key.value;
        key.value = "";
        try {
          const params = server ? { server, model: model.value.trim() } : { provider, url: url.value.trim(), model: model.value.trim() };
          if (!server && provider === "openai" && apiKey) params.api_key = apiKey;
          desk.settings = await conn.call("settings.control.write", params);
          await desk.refreshModels();
          const status = desk.control();
          toast(status?.state === "ready" ? "Saved. The model answers." : status?.detail ? `Saved, but ${status.detail}.` : "Saved.");
          onSaved?.();
        } catch (e) {
          error.textContent = e.message;
        } finally {
          save.disabled = false;
        }
      },
    },
    servers.length
      ? h(
          "div",
          { class: "row-actions", role: "group", "aria-label": "Model server" },
          servers.map((s) => serverChoice(s.name, s.name, hostOf(s.url))),
          serverChoice("", "Another server"),
        )
      : null,
    hint,
    newServer,
    keyField,
    h("div", { class: "field" }, h("label", { for: "model-name" }, "Model"), model, options, offered),
    error,
    h("div", { class: "row-actions" }, save),
  );
  if (compact) form.classList.add("compact");
  show();
  return form;
}

// What a keychain without a passphrase gives up, said wherever one is made.
export const noPassphraseWarning = "Anyone who can read the keychain file, or a backup of it, can use every key in it.";

export function keychainForm(ctx, { onDone } = {}) {
  const { conn, desk } = ctx;
  const status = desk.keychain || {};
  const error = h("p", { class: "error", role: "alert" });
  if (status.unlocked) return h("p", { class: "hint" }, `Unlocked · ${status.credentials} saved credential${status.credentials === 1 ? "" : "s"}${status.no_passphrase ? " · no passphrase" : ""}.`);
  const creating = !status.exists;
  let without = false;
  const pass = h("input", { class: "input", id: "keychain-pass", type: "password", autocomplete: creating ? "new-password" : "current-password", required: !!creating });
  const again = h("input", { class: "input", id: "keychain-again", type: "password", autocomplete: "new-password", required: true });
  const button = h("button", { type: "submit", class: "btn dark" }, creating ? "Create the keychain" : "Unlock");
  const passFields = h(
    "div",
    { class: "fields" },
    h("div", { class: "field" }, h("label", { for: "keychain-pass" }, "Passphrase"), pass),
    creating ? h("div", { class: "field" }, h("label", { for: "keychain-again" }, "The same passphrase again"), again) : null,
  );
  const warning = h("p", { class: "hint", hidden: true }, `Not recommended. ${noPassphraseWarning} It opens by itself when nodwyn starts, and you can set a passphrase later in Settings.`);
  const intro = h("p", { class: "hint" }, creating ? "Keys for model servers and tools are kept in an encrypted keychain. Choose a passphrase you'll remember: nodwyn can't recover it." : "Your saved keys are locked. Enter the keychain's passphrase to use them in new tasks.");
  const optOut = creating
    ? h(
        "label",
        { class: "check" },
        h("input", {
          type: "checkbox",
          id: "keychain-none",
          onchange: (event) => {
            without = event.target.checked;
            passFields.hidden = without;
            warning.hidden = !without;
            intro.textContent = without ? "Keys for model servers and tools are kept in the keychain." : "Keys for model servers and tools are kept in an encrypted keychain. Choose a passphrase you'll remember: nodwyn can't recover it.";
            pass.required = again.required = !without;
          },
        }),
        "Don't protect it with a passphrase",
      )
    : null;
  return h(
    "form",
    {
      class: "setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        if (creating && !without && pass.value !== again.value) {
          error.textContent = "The two passphrases differ.";
          return;
        }
        const passphrase = pass.value;
        pass.value = "";
        again.value = "";
        button.disabled = true;
        try {
          desk.keychain = await conn.call(creating ? "keychain.create" : "keychain.unlock", creating && without ? { no_passphrase: true } : { passphrase });
          toast(creating ? (without ? "Keychain created, without a passphrase." : "Keychain created.") : "Keychain unlocked. Tasks you open from now on can use its keys.");
          onDone?.();
        } catch (e) {
          error.textContent = e.message;
          pass.focus();
        } finally {
          button.disabled = false;
        }
      },
    },
    intro,
    passFields,
    optOut,
    warning,
    error,
    h("div", { class: "row-actions" }, button),
  );
}

// passphraseForm sets, changes or removes the open keychain's passphrase.
// One that has a passphrase asks for it first.
export function passphraseForm(ctx, { onDone } = {}) {
  const { conn, desk } = ctx;
  const has = !desk.keychain?.no_passphrase;
  const error = h("p", { class: "error", role: "alert" });
  const current = h("input", { class: "input", id: "keychain-current", type: "password", autocomplete: "current-password", required: has });
  const pass = h("input", { class: "input", id: "keychain-new", type: "password", autocomplete: "new-password", required: true, minLength: 8 });
  const again = h("input", { class: "input", id: "keychain-new-again", type: "password", autocomplete: "new-password", required: true });
  const submit = async (params, done) => {
    error.textContent = "";
    for (const input of [current, pass, again]) input.value = "";
    try {
      desk.keychain = await conn.call("keychain.passphrase", params);
      toast(done);
      onDone?.();
    } catch (e) {
      error.textContent = e.message;
    }
  };
  const remove = has
    ? h(
        "button",
        {
          type: "button",
          class: "btn small danger",
          onclick: () => {
            if (!current.value) {
              error.textContent = "Enter the current passphrase to remove it.";
              current.focus();
              return;
            }
            submit({ current: current.value, no_passphrase: true }, "The keychain has no passphrase now.");
          },
        },
        "Remove the passphrase",
      )
    : null;
  return h(
    "form",
    {
      class: "setup-form",
      onsubmit: (event) => {
        event.preventDefault();
        if (pass.value !== again.value) {
          error.textContent = "The two passphrases differ.";
          return;
        }
        const params = { passphrase: pass.value };
        if (has) params.current = current.value;
        submit(params, has ? "Passphrase changed." : "The keychain has a passphrase now.");
      },
    },
    has ? h("div", { class: "field" }, h("label", { for: "keychain-current" }, "Current passphrase"), current) : null,
    h("div", { class: "field" }, h("label", { for: "keychain-new" }, has ? "New passphrase" : "Passphrase"), pass),
    h("div", { class: "field" }, h("label", { for: "keychain-new-again" }, "The same passphrase again"), again),
    h("p", { class: "hint" }, "Tasks already open keep their keys. When nodwyn next starts it asks for the new passphrase."),
    error,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn dark" }, has ? "Change the passphrase" : "Set the passphrase"), remove),
    has ? h("p", { class: "hint" }, `Removing it is not recommended: ${noPassphraseWarning.charAt(0).toLowerCase()}${noPassphraseWarning.slice(1)}`) : null,
  );
}
