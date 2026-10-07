// Setting nodwyn up without a terminal (ADR 30): unlock or create the keychain
// that holds keys, and set its passphrase. The daemon validates and writes;
// these forms only describe the choice. The model is chosen in models.js, on
// the first run as in Settings (ADR 41).
import { h, toast } from "./dom.js";

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
