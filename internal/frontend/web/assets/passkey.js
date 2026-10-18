// Passkeys (ADR 30, amended 2026-10-17): registering one with an invitation,
// signing a new browser in with one, and the touch a device signed in with
// one gives before it changes nodwyn's setup. The bridge issues each
// challenge and checks each answer; the page only carries them to the
// browser's own passkey prompt and back.
import { h } from "./dom.js";

const b64 = (buffer) => btoa(String.fromCharCode(...new Uint8Array(buffer))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
const unb64 = (text) => Uint8Array.from(atob(text.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));

// passkeysHere says whether this browser can use passkeys at this address:
// the bridge must say so (pairing().passkey) as well.
export const passkeysHere = () => typeof PublicKeyCredential !== "undefined" && window.isSecureContext;

async function post(path, body) {
  const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), credentials: "same-origin" });
  if (!response.ok) throw new Error((await response.text()).trim() || "The passkey was not accepted.");
  return response.status === 204 ? {} : response.json();
}

// pairing asks the bridge how this browser can pair: {shared, passkey,
// passkeys}. A bridge too old to say is a console that takes the code.
export async function pairing() {
  try {
    const response = await fetch("/pair", { credentials: "same-origin" });
    if (response.ok) return await response.json();
  } catch {
    /* an older bridge, or none */
  }
  return { shared: false, passkey: false, passkeys: false };
}

// The browser's own prompt says what it needs; a person who closes it has
// cancelled, which is said without the browser's wording.
function cancelled(error) {
  return error?.name === "NotAllowedError" || error?.name === "AbortError" ? new Error("The passkey was not used; nothing was changed.") : error;
}

// register makes a passkey for device with an invitation, and pairs it.
export async function register(invite, device) {
  const options = await post("/passkey/options", { invite });
  let credential;
  try {
    credential = await navigator.credentials.create({
      publicKey: {
        rp: { id: options.rp_id, name: "nodwyn" },
        user: { id: unb64(options.user), name: device, displayName: `nodwyn on ${device}` },
        challenge: unb64(options.challenge),
        pubKeyCredParams: options.algorithms.map((alg) => ({ type: "public-key", alg })),
        authenticatorSelection: { residentKey: "preferred", userVerification: "required" },
        attestation: "none",
        timeout: 120000,
      },
    });
  } catch (error) {
    throw cancelled(error);
  }
  const key = credential.response.getPublicKey?.();
  if (!key) throw new Error("This browser cannot hand over the passkey's key; use another browser, or pair without a passkey.");
  return post("/passkey/register", {
    invite,
    device,
    challenge: options.challenge,
    id: b64(credential.rawId),
    client_data: b64(credential.response.clientDataJSON),
    authenticator_data: b64(credential.response.getAuthenticatorData()),
    public_key: b64(key),
  });
}

// assert answers a passkey challenge: a sign-in for a new device named
// device, or a touch from a paired one (device left out). It resolves to
// {device, promoted}: promoted is a device that is the person's from now on,
// whose connection must be made again.
export async function assert(device) {
  const options = await post("/passkey/options", {});
  let credential;
  try {
    credential = await navigator.credentials.get({
      publicKey: {
        challenge: unb64(options.challenge),
        rpId: options.rp_id,
        allowCredentials: options.credentials.map((id) => ({ type: "public-key", id: unb64(id) })),
        userVerification: "required",
        timeout: 120000,
      },
    });
  } catch (error) {
    throw cancelled(error);
  }
  const body = {
    challenge: options.challenge,
    id: b64(credential.rawId),
    client_data: b64(credential.response.clientDataJSON),
    authenticator_data: b64(credential.response.authenticatorData),
    signature: b64(credential.response.signature),
  };
  if (device) body.device = device;
  return post("/passkey/assert", body);
}

// confirmTouch asks the person for a touch of their passkey before a change
// to nodwyn's setup from this device, in a dialog of its own: the browser
// lets a page start a passkey prompt only from a click, and the request
// that asked for it is past its click by then. It resolves once the touch
// is accepted, and rejects if the person cancels.
export function confirmTouch() {
  return new Promise((resolve, reject) => {
    const error = h("p", { class: "error", role: "alert" });
    const use = h("button", { type: "button", class: "btn accent", id: "touch-confirm" }, "Use your passkey");
    const box = h(
      "dialog",
      { class: "passkey-dialog", "aria-label": "Confirm with your passkey" },
      h("div", { class: "dialog-head" }, h("h2", {}, "Confirm it's you")),
      h("p", {}, "Changing nodwyn's setup from this device takes your passkey. Once confirmed, setup changes go through for 15 minutes after the last one."),
      error,
      h("div", { class: "row-actions" }, use, h("button", { type: "button", class: "btn ghost", onclick: () => box.close() }, "Cancel")),
    );
    let done = false;
    use.addEventListener("click", async () => {
      use.disabled = true;
      error.textContent = "";
      try {
        const answer = await assert();
        done = true;
        box.close();
        resolve(answer);
      } catch (e) {
        error.textContent = e.message;
        use.disabled = false;
      }
    });
    box.addEventListener("close", () => {
      box.remove();
      if (!done) reject(new Error("Not confirmed; nothing was changed."));
    });
    document.body.append(box);
    box.showModal();
    use.focus();
  });
}
