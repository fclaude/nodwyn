// Setting nodwyn up without a terminal (ADR 30): choose the model, and unlock
// or create the keychain that holds keys. The daemon validates and writes;
// these forms only describe the choice.
import { h, toast } from "./dom.js";

const presets = {
  ollama: { url: "http://localhost:11434", hint: "Ollama on this computer, or another one on your network (for example http://workshop:11434)." },
  openai: { url: "https://api.openai.com/v1", hint: "Any server that speaks the OpenAI chat-completions API. Its key is kept in the keychain." },
};

export function modelForm(ctx, { onSaved, compact = false } = {}) {
  const { conn, desk } = ctx;
  const current = desk.settings?.control || {};
  let provider = current.Provider || current.provider || "ollama";
  const url = h("input", { class: "input", id: "model-url", required: true, spellcheck: false, autocomplete: "off" });
  url.value = current.URL || current.url || presets[provider].url;
  const model = h("input", { class: "input", id: "model-name", required: true, spellcheck: false, autocomplete: "off", placeholder: provider === "ollama" ? "for example qwen3:32b" : "for example gpt-5" });
  model.value = current.Model || current.model || "";
  const key = h("input", { class: "input", id: "model-key", type: "password", autocomplete: "off", spellcheck: false, placeholder: current.Credential || current.credential ? "leave empty to keep the saved key" : "" });
  const keyField = h("div", { class: "field" }, h("label", { for: "model-key" }, "API key"), key, h("p", { class: "hint" }, "Stored encrypted in the keychain and sent only to this server's address."));
  const hint = h("p", { class: "hint" }, presets[provider].hint);
  const error = h("p", { class: "error", role: "alert" });
  const choice = (id, label) =>
    h(
      "button",
      {
        type: "button",
        class: "chip",
        "aria-pressed": String(provider === id),
        onclick: (event) => {
          provider = id;
          for (const b of event.target.parentElement.children) b.setAttribute("aria-pressed", String(b === event.target));
          if (Object.values(presets).some((p) => p.url === url.value) || !url.value) url.value = presets[id].url;
          hint.textContent = presets[id].hint;
          keyField.hidden = id !== "openai";
        },
      },
      label,
    );
  keyField.hidden = provider !== "openai";
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
          const params = { provider, url: url.value.trim(), model: model.value.trim() };
          if (provider === "openai" && apiKey) params.api_key = apiKey;
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
    h("div", { class: "row-actions", role: "group", "aria-label": "Kind of model server" }, choice("ollama", "Ollama"), choice("openai", "OpenAI-compatible")),
    hint,
    h("div", { class: "field" }, h("label", { for: "model-url" }, "Server address"), url),
    h("div", { class: "field" }, h("label", { for: "model-name" }, "Model"), model),
    keyField,
    error,
    h("div", { class: "row-actions" }, save),
  );
  if (compact) form.classList.add("compact");
  return form;
}

export function keychainForm(ctx, { onDone } = {}) {
  const { conn, desk } = ctx;
  const status = desk.keychain || {};
  const error = h("p", { class: "error", role: "alert" });
  if (status.unlocked) return h("p", { class: "hint" }, `Unlocked · ${status.credentials} saved credential${status.credentials === 1 ? "" : "s"}.`);
  const creating = !status.exists;
  const pass = h("input", { class: "input", id: "keychain-pass", type: "password", autocomplete: creating ? "new-password" : "current-password", required: true });
  const again = h("input", { class: "input", id: "keychain-again", type: "password", autocomplete: "new-password", required: true });
  const button = h("button", { type: "submit", class: "btn dark" }, creating ? "Create the keychain" : "Unlock");
  return h(
    "form",
    {
      class: "setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        if (creating && pass.value !== again.value) {
          error.textContent = "The two passphrases differ.";
          return;
        }
        const passphrase = pass.value;
        pass.value = "";
        again.value = "";
        button.disabled = true;
        try {
          desk.keychain = await conn.call(creating ? "keychain.create" : "keychain.unlock", { passphrase });
          toast(creating ? "Keychain created." : "Keychain unlocked. Tasks you open from now on can use its keys.");
          onDone?.();
        } catch (e) {
          error.textContent = e.message;
        } finally {
          button.disabled = false;
        }
      },
    },
    h("p", { class: "hint" }, creating ? "Keys for model servers and tools are kept in an encrypted keychain. Choose a passphrase you'll remember: nodwyn can't recover it." : "Your saved keys are locked. Enter the keychain's passphrase to use them in new tasks."),
    h("div", { class: "field" }, h("label", { for: "keychain-pass" }, "Passphrase"), pass),
    creating ? h("div", { class: "field" }, h("label", { for: "keychain-again" }, "The same passphrase again"), again) : null,
    error,
    h("div", { class: "row-actions" }, button),
  );
}
