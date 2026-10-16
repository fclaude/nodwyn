// Connections › Credentials: the keychain's credentials, where each may be
// sent and what uses it (ADR 5, ADR 24). Values go in and never come back:
// the daemon answers with metadata only, and the keychain adds a key to a
// request at the last step, where a binding allows it.
import { h, icon, ago, clock, shortPath, toast, removal } from "./dom.js";
import { keychainForm, noPassphraseWarning } from "./setup.js";

let selected = ""; // the credential whose detail is open: its id, and where it is shown
let selectedPanel = ""; // "models" or "": the subpanel whose detail is open
let adding = ""; // "key" or "oauth2": the add form that is open
let addingPanel = ""; // the subpanel whose add form is open: "" is the generic one below
let draft = {}; // what the add-a-key form was given before a redraw
let unbinding = ""; // the binding row whose Remove waits for its confirm
let poll = 0;
const dismissed = new Set(); // failed authorizations the person has read
const begun = new Set(); // authorizations this page started and has not yet said how they ended

const methods = ["GET", "POST", "PUT", "PATCH", "DELETE"];
const positions = [
  ["header:Authorization:Bearer", "Authorization: Bearer …"],
  ["header:x-api-key", "x-api-key header"],
  ["header", "another header…"],
  ["query", "a query parameter…"],
  ["body", "the request body"],
  ["env", "an environment variable (contained MCP server)…"],
];

export async function renderCredentials(body, ctx, redraw) {
  const { conn } = ctx;
  let list;
  try {
    list = await conn.call("credentials.list");
  } catch (error) {
    body.append(h("p", { class: "error" }, error.message));
    return;
  }
  if (!body.isConnected) return;
  const canEdit = conn.has("credentials.add");
  const keychain = list.keychain;
  ctx.desk.keychain = keychain;
  // Settings reads the judge's and the providers' key states from the
  // desk's settings: refresh them now that a key may have been stored, so
  // they agree with this tab.
  if (conn.has("settings.read")) {
    try {
      ctx.desk.settings = await conn.call("settings.read");
    } catch {
      /* an older daemon: setup happens in config.toml */
    }
  }
  if (!keychain.unlocked) {
    body.append(
      h(
        "div",
        { class: "callout plain", style: "flex-direction:column" },
        h("strong", {}, keychain.exists ? "The keychain is locked." : "There is no keychain yet."),
        conn.has(keychain.exists ? "keychain.unlock" : "keychain.create") ? keychainForm(ctx, { onDone: redraw }) : h("p", { class: "hint" }, "Unlock it on the computer nodwyn runs on."),
      ),
    );
    return;
  }
  // A provider may send the person back before this page first looks, so an
  // authorization it began can already be over: say how it ended, as the poll
  // below does for one it saw waiting.
  for (const id of begun) {
    const a = list.authorizations.find((b) => b.id === id);
    if (a?.state === "waiting") continue;
    begun.delete(id);
    if (a) concluded(a, list);
  }
  const waiting = list.authorizations.filter((a) => a.state === "waiting");
  // The providers' keys, the judge's among them (ADR 46), have their own
  // subpanel, so the generic list below is only the keys no provider sends:
  // the daemon marks what a configured provider sends.
  const modelCreds = list.credentials.filter((c) => c.model);
  const otherCreds = list.credentials.filter((c) => !c.model);
  const modelMissing = list.missing.filter((m) => m.model);
  const otherMissing = list.missing.filter((m) => !m.model);
  // A detail opens in the subpanel whose row was clicked, not at the bottom
  // of the page: opening one panel's closes the others'.
  const open = (panel, id) => {
    if (selected === id && selectedPanel === panel) {
      selected = "";
      selectedPanel = "";
    } else {
      selected = id;
      selectedPanel = panel;
    }
    adding = "";
    addingPanel = "";
    redraw();
  };
  const credential = list.credentials.find((c) => c.id === selected);
  body.append(
    ...[
      keychain.no_passphrase
        ? h("div", { class: "callout" }, h("div", {}, h("strong", {}, "The keychain has no passphrase. "), noPassphraseWarning, " ", h("a", { href: "#/settings/keys" }, "Set one in Settings"), "."))
        : null,
      list.bindings_error ? h("div", { class: "callout" }, h("div", {}, h("strong", {}, "bindings.toml does not load: "), list.bindings_error, ". Where each credential may go is shown as nodwyn last loaded it.")) : null,
      modelsCard(list, modelCreds, modelMissing, credential && selectedPanel === "models" ? credential : null, ctx, redraw, (id) => open("models", id)),
      h("h2", { class: "section-title", id: "other-keys" }, "Other keys & accounts"),
      ...otherMissing.map((m) =>
        h(
          "div",
          { class: "callout" },
          h("div", {}, h("strong", {}, `${sentence(m.used_by)} ${m.used_by.length === 1 ? "sends" : "send"} `), h("span", { class: "mono" }, m.class), h("strong", {}, ", which the keychain doesn't hold. "), "Those requests fail until you add it."),
          canEdit ? h("button", { type: "button", class: "btn small", style: "margin-left:auto", onclick: () => { adding = "key"; addingPanel = ""; draft = { class: m.class }; redraw(); } }, "Add it") : null,
        ),
      ),
      ...list.authorizations.filter((a) => !dismissed.has(a.id)).map((a) => authorizationCard(a, redraw)),
      otherCreds.length
        ? h("div", { class: "list" }, otherCreds.map((c) => row(c, redraw, (id) => open("", id))))
        : h("div", { class: "callout plain" }, "No other keys yet. A key you add here, or with a model or an MCP server, is kept in the keychain and goes only where you allow it."),
      canEdit
        ? h(
            "div",
            { class: "row-actions" },
            h("button", { type: "button", class: "btn", "aria-pressed": String(adding === "key" && !addingPanel), onclick: () => { adding = adding === "key" && !addingPanel ? "" : "key"; addingPanel = ""; selected = ""; selectedPanel = ""; redraw(); } }, icon("plus", 15), "Add a key"),
            conn.has("credentials.authorize") ? h("button", { type: "button", class: "btn", "aria-pressed": String(adding === "oauth2"), onclick: () => { adding = adding === "oauth2" ? "" : "oauth2"; addingPanel = ""; selected = ""; selectedPanel = ""; redraw(); } }, icon("plus", 15), "Connect an account (OAuth2)") : null,
          )
        : h("p", { class: "hint" }, "Credentials are added and changed on the computer nodwyn runs on."),
      canEdit && adding === "key" && !addingPanel ? addKey(ctx, redraw) : null,
      canEdit && adding === "oauth2" ? addAccount(ctx, list, redraw) : null,
      credential && !selectedPanel ? detail(credential, list, ctx, redraw) : null,
    ].filter(Boolean),
  );
  body.append(h("p", { class: "hint" }, "A credential is sent only where a binding allows it, and the model never sees the value. Bindings are kept in ", h("span", { class: "mono" }, shortPath(list.bindings_file, ctx.desk.home)), "; a host's Remove takes it back, and a row written in a form that button cannot cut is edited in that file, or in a task's Details › Policy."));
  // While the provider has not sent the person back, look again.
  clearTimeout(poll);
  if (waiting.length) {
    poll = setTimeout(async () => {
      if (!body.isConnected) return;
      let next;
      try {
        next = await conn.call("credentials.list");
      } catch {
        return;
      }
      for (const a of waiting) {
        const now = next.authorizations.find((b) => b.id === a.id);
        if (!now || now.state === "waiting") continue;
        begun.delete(a.id);
        concluded(now, next);
      }
      if (body.isConnected) redraw();
    }, 1500);
  }
}

// concluded says how an authorization ended, in list: a new credential is
// opened where it is shown, a refusal is told.
function concluded(a, list) {
  if (a.state === "done") {
    toast(`${a.class} is authorized.`);
    selected = a.credential;
    const done = list.credentials.find((c) => c.id === selected);
    selectedPanel = done?.model ? "models" : "";
    adding = "";
    addingPanel = "";
  } else if (a.state === "failed") toast(`${a.class}: ${a.error}`);
}

// sentence joins names as a person would: "a, b and c".
function sentence(names) {
  const capital = (s) => s.charAt(0).toUpperCase() + s.slice(1);
  if (names.length < 2) return capital(names[0] || "");
  return capital(`${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`);
}

// modelsCard is the Provider keys subpanel: the credentials the providers
// send, for their models and the judge. A key is first entered with its
// provider, in Settings, which also allows it for the provider's host; from
// then on its value is changed here, and only here. open is the credential
// whose detail shows in this panel, if any.
function modelsCard(list, creds, missing, open, ctx, redraw, pick) {
  const { conn } = ctx;
  const canEdit = conn.has("credentials.add");
  const showAdd = canEdit && adding === "key" && addingPanel === "models";
  return h(
    "section",
    { class: "card", id: "model-keys" },
    h("h2", { class: "section-title" }, "Provider keys"),
    ...missing.map((m) =>
      h(
        "div",
        { class: "callout" },
        h("div", {}, h("strong", {}, `${sentence(m.used_by)} ${m.used_by.length === 1 ? "sends" : "send"} `), h("span", { class: "mono" }, m.class), h("strong", {}, ", which the keychain doesn't hold. "), "Those requests fail until you add it."),
        canEdit ? h("button", { type: "button", class: "btn small", style: "margin-left:auto", onclick: () => { adding = "key"; addingPanel = "models"; draft = { class: m.class }; selected = ""; selectedPanel = ""; redraw(); } }, "Add it") : null,
      ),
    ),
    creds.length ? h("div", { class: "list" }, creds.map((c) => row(c, redraw, pick))) : missing.length ? null : h("div", { class: "callout plain" }, "No provider keys yet. Add a provider in Settings › Models and its key is stored and allowed at once."),
    open ? detail(open, list, ctx, redraw) : null,
    showAdd ? addKey(ctx, redraw) : null,
    h("div", { class: "row-actions" }, h("a", { class: "btn small", href: "#/settings/models" }, "Change the providers")),
  );
}

function kindWords(c) {
  if (c.kind !== "oauth2") return "API key";
  return `OAuth2 · ${c.provider}${c.scopes?.length ? ` · ${c.scopes.map(shortScope).join(", ")}` : ""}`;
}

// shortScope shows a Google scope URL by its last part.
const shortScope = (s) => s.replace(/^https:\/\/www\.googleapis\.com\/auth\//, "");

function tokenWords(c) {
  if (c.kind !== "oauth2") return "";
  if (c.needs_reauth) return "The provider refused to refresh it: authorize it again.";
  if (!c.expires) return "The access token does not expire.";
  const until = new Date(c.expires);
  const when = until.toDateString() === new Date().toDateString() ? clock(c.expires) : until.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  return c.refreshable ? `Access token until ${when}, refreshed before use.` : `Access token until ${when}; with no refresh token, authorize it again after that.`;
}

// grouped merges binding rows that differ only by method, as the console
// writes one row per method.
function grouped(bindings) {
  const out = [];
  for (const b of bindings) {
    const same = out.find((o) => o.dest === b.dest && o.positions.join() === b.positions.join() && (o.scopes || []).join() === (b.scopes || []).join());
    if (same) same.methods = [...new Set([...same.methods, ...b.methods])];
    else out.push({ ...b, methods: [...b.methods] });
  }
  return out;
}

function hosts(c) {
  return [...new Set(c.bindings.map((b) => b.dest))];
}

function row(c, redraw, pick) {
  const where = hosts(c);
  const state = c.needs_reauth ? ["bad", "needs authorizing"] : c.shadowed ? ["warn", "never used"] : where.length ? ["ok", where.length === 1 ? where[0] : `${where.length} hosts`] : ["warn", "allowed nowhere"];
  const select = pick || ((id) => { selected = selected === id ? "" : id; selectedPanel = ""; adding = ""; addingPanel = ""; redraw(); });
  return h(
    "button",
    { type: "button", class: `list-row cred-row${selected === c.id ? " selected" : ""}`, onclick: () => select(c.id) },
    icon("key", 18),
    h("div", { class: "main" }, h("strong", { class: "mono" }, c.class), h("span", {}, `${kindWords(c)} · added ${ago(c.created)}${c.used_by.length ? ` · ${c.used_by.join(", ")}` : ""}`)),
    h("div", { class: "end" }, h("span", { class: `dot ${state[0]}` }), state[1]),
  );
}

function detail(c, list, ctx, redraw) {
  const { conn } = ctx;
  const canEdit = conn.has("credentials.add");
  const panel = h("section", { class: "card cred-detail" });
  // Taking back a host is the safe direction: it narrows where the key may go,
  // so each row has its own Remove, asked twice like every removal.
  const canUnbind = canEdit && conn.has("credentials.unbind");
  const unbind = async (b) => {
    unbinding = "";
    try {
      await conn.call("credentials.unbind", { id: c.id, dest: b.dest, positions: b.positions, ...(b.scopes?.length ? { scopes: b.scopes } : {}) });
      toast(`${c.class} may no longer go to ${b.dest}.`);
    } catch (e) {
      toast(e.message);
    }
    redraw();
  };
  const rows = grouped(c.bindings).map((b) => {
    const key = `${c.id} ${b.dest} ${b.positions.join(",")} ${(b.scopes || []).join(",")}`;
    return h(
      "tr",
      {},
      h("td", {}, b.dest),
      h("td", {}, b.methods.join(", ")),
      h("td", {}, h("span", { class: "mono" }, b.positions.join(", "))),
      c.kind === "oauth2" ? h("td", {}, b.scopes?.length ? b.scopes.map(shortScope).join(", ") : "any") : null,
      canUnbind
        ? h("td", {}, h("div", { class: "row-actions" }, removal({ label: "Remove", name: `Stop allowing ${c.class} for ${b.dest}`, consequence: `Stop allowing ${c.class} for ${b.dest}? Requests that carry it there are refused.`, asking: unbinding === key, ask: () => ((unbinding = key), redraw()), cancel: () => ((unbinding = ""), redraw()), run: () => unbind(b) })))
        : null,
    );
  });
  const provider = list.providers.find((p) => p.name === c.provider);
  panel.append(
    ...[
      h(
        "div",
        { class: "card-head" },
        h("div", { style: "display:flex;flex-direction:column;gap:2px" }, h("strong", { class: "mono", style: "font-size:16px" }, c.class), h("span", { class: "hint" }, `${kindWords(c)} · added ${new Date(c.created).toLocaleDateString()} · id `, h("span", { class: "mono" }, c.id))),
      ),
      c.kind === "oauth2" ? h("p", { class: c.needs_reauth ? "error" : "hint" }, tokenWords(c), c.token_host ? ` Tokens come from ${c.token_host}.` : "") : null,
      c.shadowed ? h("div", { class: "callout" }, "Another credential of this class was added before it, and a lookup by class finds that one: this one is never sent. Remove one of them.") : null,
      h("p", { class: "hint" }, c.used_by.length ? `Used by ${c.used_by.join(", ")}.` : "Nothing configured names it. A tool or a folder's Scheme can still ask for it by its class."),
      h("h2", { class: "section-title" }, "Where it may go"),
      rows.length
        ? h("div", { class: "table-wrap" }, h("table", { class: "table" }, h("thead", {}, h("tr", {}, h("th", {}, "Host"), h("th", {}, "Methods"), h("th", {}, "Added as"), c.kind === "oauth2" ? h("th", {}, "Scopes") : null, canUnbind ? h("th", {}, "") : null)), h("tbody", {}, rows)))
        : h("p", { class: "hint" }, "Nowhere yet: nodwyn refuses every request that would carry it until you allow a host."),
      canEdit ? allowForm(c, provider, ctx, redraw) : null,
      canEdit ? h("h2", { class: "section-title" }, c.kind === "oauth2" ? "Authorization" : "Value") : null,
      canEdit && c.kind !== "oauth2" ? replaceForm(c, ctx, redraw) : null,
      canEdit && c.kind === "oauth2" && conn.has("credentials.authorize")
        ? h("div", { class: "row-actions" }, h("button", { type: "button", class: c.needs_reauth ? "btn dark" : "btn", onclick: () => authorize(ctx, { id: c.id }, redraw) }, "Authorize again"), h("span", { class: "hint" }, "With the same client, endpoints and scopes; it replaces the tokens."))
        : null,
      canEdit ? removeControls(c, ctx, redraw) : null,
    ].filter(Boolean),
  );
  return panel;
}

// positionField is a choice of where a key goes in a request, with the
// name a header, query parameter or variable needs.
function positionField(id, initial = "header:Authorization:Bearer") {
  const fixed = positions.find(([v]) => v === initial);
  const [kind, ...rest] = initial.split(":");
  const select = h("select", { class: "input", id, "aria-label": "Added as" }, positions.map(([value, label]) => h("option", { value, selected: fixed ? value === initial : value === kind }, label)));
  const name = h("input", { class: "input", placeholder: "name", "aria-label": "Its name", spellcheck: false, value: fixed ? "" : rest.join(":") });
  const sync = () => (name.hidden = !["header", "query", "env"].includes(select.value));
  select.addEventListener("change", sync);
  sync();
  return {
    node: h("div", { class: "row-actions" }, select, name),
    value: () => (name.hidden ? select.value : `${select.value}:${name.value.trim()}`),
  };
}

function methodChips(initial = ["GET", "POST"]) {
  const chosen = new Set(initial);
  const node = h(
    "div",
    { class: "row-actions", role: "group", "aria-label": "Methods" },
    methods.map((m) => h("button", { type: "button", class: "chip", "aria-pressed": String(chosen.has(m)), onclick: (e) => { if (chosen.has(m)) chosen.delete(m); else chosen.add(m); e.target.setAttribute("aria-pressed", String(chosen.has(m))); } }, m)),
  );
  return { node, value: () => methods.filter((m) => chosen.has(m)) };
}

function allowForm(c, provider, ctx, redraw) {
  const { conn } = ctx;
  const dest = h("input", { class: "input", id: `allow-${c.id}`, placeholder: provider?.api_host || "api.example.com", value: !c.bindings.length && provider?.api_host ? provider.api_host : "", required: true, spellcheck: false, autocomplete: "off" });
  const chips = methodChips(["GET", "POST"]);
  const position = positionField(`allow-pos-${c.id}`);
  const error = h("p", { class: "error", role: "alert" });
  return h(
    "details",
    { class: "box", open: !c.bindings.length },
    h("summary", {}, "Allow it for a host"),
    h(
      "form",
      {
        class: "setup-form",
        onsubmit: async (event) => {
          event.preventDefault();
          error.textContent = "";
          try {
            await conn.call("credentials.bind", { id: c.id, dest: dest.value.trim(), methods: chips.value(), position: position.value() });
            toast(`${c.class} may now go to ${dest.value.trim()}.`);
            redraw();
          } catch (e) {
            error.textContent = e.message;
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: `allow-${c.id}` }, "Host"), dest, h("p", { class: "hint" }, "Exactly this host name: no scheme, path or wildcard.")),
      h("div", { class: "field" }, h("label", {}, "Methods"), chips.node),
      h("div", { class: "field" }, h("label", { for: `allow-pos-${c.id}` }, "Added as"), position.node),
      c.kind === "oauth2" && c.scopes?.length ? h("p", { class: "hint" }, `Allowed with the scopes it was granted (${c.scopes.map(shortScope).join(", ")}): a token granted more is refused.`) : null,
      error,
      h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn dark" }, "Allow")),
    ),
  );
}

function replaceForm(c, ctx, redraw) {
  const { conn } = ctx;
  const value = h("input", { class: "input", id: `replace-${c.id}`, type: "password", autocomplete: "off", spellcheck: false, required: true, placeholder: "the new key" });
  const error = h("p", { class: "error", role: "alert" });
  return h(
    "form",
    {
      class: "setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const secret = value.value;
        value.value = "";
        try {
          await conn.call("credentials.replace", { id: c.id, value: secret });
          toast(`${c.class} has its new value. Tasks use it from their next request.`);
          redraw();
        } catch (e) {
          error.textContent = e.message;
        }
      },
    },
    h("div", { class: "field" }, h("label", { for: `replace-${c.id}` }, "Replace the key"), h("div", { class: "row-actions" }, value, h("button", { type: "submit", class: "btn" }, "Replace")), h("p", { class: "hint" }, "After rotating it where it was issued. It keeps its class, so its bindings and what uses it carry on.")),
    error,
  );
}

function removeControls(c, ctx, redraw) {
  const { conn } = ctx;
  const holder = h("div", { class: "row-actions" });
  const remove = async (revoke) => {
    try {
      const result = await conn.call("credentials.remove", { id: c.id, revoke });
      selected = "";
      selectedPanel = "";
      toast(
        result.revoked
          ? `Revoked ${c.class} at ${c.provider} and removed it here.`
          : c.kind === "oauth2"
            ? `Removed ${c.class} here. Its tokens stay valid at ${c.provider} until they expire, or until you remove nodwyn in your ${c.provider} account.`
            : `Removed ${c.class}. nodwyn can't revoke a key: revoke it where it was issued.`,
      );
      redraw();
    } catch (e) {
      toast(e.message);
    }
  };
  const ask = () => {
    const consequence = c.used_by.length ? `${sentence(c.used_by)} ${c.used_by.length === 1 ? "uses" : "use"} it: those requests fail until another ${c.class} is added. ` : "";
    holder.replaceChildren(
      h(
        "div",
        { class: "callout", style: "flex-direction:column;width:100%" },
        h("div", {}, h("strong", {}, `Remove ${c.class}? `), consequence, "Its rows in bindings.toml go with it."),
        h(
          "div",
          { class: "row-actions" },
          c.kind === "oauth2" ? h("button", { type: "button", class: "btn dark", onclick: () => remove(true) }, "Revoke and remove") : null,
          h("button", { type: "button", class: c.kind === "oauth2" ? "btn" : "btn dark", onclick: () => remove(false) }, c.kind === "oauth2" ? "Remove here only" : "Remove"),
          h("button", { type: "button", class: "btn ghost", onclick: () => holder.replaceChildren(button) }, "Cancel"),
        ),
      ),
    );
  };
  const button = h("button", { type: "button", class: "btn small danger", onclick: ask }, "Remove…");
  holder.append(button);
  return holder;
}

function addKey(ctx, redraw) {
  const { conn } = ctx;
  const cls = h("input", { class: "input", id: "cred-class", required: true, spellcheck: false, autocomplete: "off", placeholder: "for example github-token", pattern: "[A-Za-z0-9][A-Za-z0-9._\\-]{0,63}", value: draft.class || "" });
  const value = h("input", { class: "input", id: "cred-value", type: "password", required: true, autocomplete: "off", spellcheck: false });
  const dest = h("input", { class: "input", id: "cred-dest", placeholder: "api.example.com", spellcheck: false, autocomplete: "off" });
  const chips = methodChips(["GET", "POST"]);
  const position = positionField("cred-pos");
  const error = h("p", { class: "error", role: "alert" });
  return h(
    "form",
    {
      class: "card setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const params = { class: cls.value.trim(), value: value.value };
        if (dest.value.trim()) Object.assign(params, { dest: dest.value.trim(), methods: chips.value(), position: position.value() });
        try {
          const list = await conn.call("credentials.add", params);
          value.value = "";
          toast(params.dest ? `${params.class} is stored, and allowed for ${params.dest}.` : `${params.class} is stored. Allow it for a host before anything can use it.`);
          const added = list.credentials.find((c) => c.class === params.class)?.id || "";
          selected = added;
          // A provider's key's detail belongs to its own panel, wherever it
          // was added; anything else opens below.
          const addedCred = list.credentials.find((c) => c.id === added);
          selectedPanel = addedCred?.model ? "models" : "";
          addingPanel = "";
          adding = "";
          draft = {};
          redraw();
        } catch (e) {
          error.textContent = e.message;
        }
      },
    },
    h("div", { class: "field" }, h("label", { for: "cred-class" }, "Class"), cls, h("p", { class: "hint" }, "The name settings, bindings and tools know it by. Letters, digits, dots, dashes or underscores.")),
    h("div", { class: "field" }, h("label", { for: "cred-value" }, "Key"), value, h("p", { class: "hint" }, "Stored encrypted in the keychain. Nothing shows it again: not this page, not a task, not the model.")),
    h("h2", { class: "section-title" }, "Allow it for"),
    h("div", { class: "field" }, h("label", { for: "cred-dest" }, "Host"), dest, h("p", { class: "hint" }, "Optional: leave it empty and nothing can send the key until you allow a host.")),
    h("div", { class: "field" }, h("label", {}, "Methods"), chips.node),
    h("div", { class: "field" }, h("label", { for: "cred-pos" }, "Added as"), position.node),
    error,
    h("div", { class: "row-actions" }, h("button", { type: "submit", class: "btn dark" }, "Store the key"), h("button", { type: "button", class: "btn ghost", onclick: () => { adding = ""; addingPanel = ""; draft = {}; redraw(); } }, "Cancel")),
  );
}

function addAccount(ctx, list, redraw) {
  // openai is a ChatGPT plan, connected on its provider under Settings ›
  // Models, so it has one entrance and not two.
  const names = [...list.providers.map((p) => p.name).filter((name) => name !== "openai"), "custom"];
  let provider = names[0];
  const cls = h("input", { class: "input", id: "oauth-class", spellcheck: false, autocomplete: "off", placeholder: provider });
  const client = h("input", { class: "input", id: "oauth-client", required: true, spellcheck: false, autocomplete: "off" });
  const secret = h("input", { class: "input", id: "oauth-secret", type: "password", autocomplete: "off", spellcheck: false });
  const secretHint = h("p", { class: "hint" });
  const scopes = h("input", { class: "input", id: "oauth-scopes", spellcheck: false, autocomplete: "off" });
  const authURL = h("input", { class: "input", id: "oauth-auth", spellcheck: false, placeholder: "https://auth.example.com/authorize" });
  const tokenURL = h("input", { class: "input", id: "oauth-token", spellcheck: false, placeholder: "https://auth.example.com/token" });
  const revokeURL = h("input", { class: "input", id: "oauth-revoke", spellcheck: false, placeholder: "optional" });
  const custom = h(
    "div",
    { class: "fields", hidden: true },
    h("div", { class: "field" }, h("label", { for: "oauth-auth" }, "Authorization endpoint"), authURL),
    h("div", { class: "field" }, h("label", { for: "oauth-token" }, "Token endpoint"), tokenURL),
    h("div", { class: "field" }, h("label", { for: "oauth-revoke" }, "Revocation endpoint"), revokeURL),
  );
  const error = h("p", { class: "error", role: "alert" });
  const accountHint = h("p", { class: "hint" });
  const continueButton = h("button", { type: "submit", class: "btn dark" }, "Continue to the provider");
  const describe = () => {
    const p = list.providers.find((x) => x.name === provider);
    cls.placeholder = provider;
    scopes.placeholder = p?.scopes?.length ? `${p.scopes.join(", ")} (the narrowest useful set)` : "as the provider names them, comma-separated";
    secretHint.textContent = p?.needs_secret ? `${provider} issues no token without the client's secret.` : "Only if the provider insists on one: PKCE protects the exchange without it.";
    secret.required = !!p?.needs_secret;
    accountHint.textContent = "nodwyn opens the provider's consent page in your browser; when you agree, the provider sends you back to nodwyn on this computer, and the tokens go into the keychain. Register nodwyn as an OAuth client with the provider first, with a loopback redirect (http://127.0.0.1). A ChatGPT plan is not connected here but under Settings › Models, on its provider.";
    custom.hidden = provider !== "custom";
    authURL.required = tokenURL.required = provider === "custom";
  };
  describe();
  return h(
    "form",
    {
      class: "card setup-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const params = { provider, client_id: client.value.trim() };
        if (cls.value.trim()) params.class = cls.value.trim();
        if (secret.value) params.client_secret = secret.value;
        if (scopes.value.trim()) params.scopes = scopes.value.split(/[\s,]+/).filter(Boolean);
        if (provider === "custom") Object.assign(params, { auth_url: authURL.value.trim(), token_url: tokenURL.value.trim(), ...(revokeURL.value.trim() ? { revoke_url: revokeURL.value.trim() } : {}) });
        secret.value = "";
        const failed = await authorize(ctx, params, redraw);
        if (failed) error.textContent = failed;
      },
    },
    accountHint,
    h(
      "div",
      { class: "row-actions", role: "group", "aria-label": "Provider" },
      names.map((name) => h("button", { type: "button", class: "chip", "aria-pressed": String(name === provider), onclick: (e) => { provider = name; for (const b of e.target.parentElement.children) b.setAttribute("aria-pressed", String(b === e.target)); describe(); } }, name)),
    ),
    custom,
    h("div", { class: "field" }, h("label", { for: "oauth-client" }, "Client id"), client),
    h("div", { class: "field" }, h("label", { for: "oauth-secret" }, "Client secret"), secret, secretHint),
    h("div", { class: "field" }, h("label", { for: "oauth-scopes" }, "Scopes"), scopes),
    h("div", { class: "field" }, h("label", { for: "oauth-class" }, "Class"), cls, h("p", { class: "hint" }, "Default: the provider's name. Google Docs uploads look for google.")),
    error,
    h("div", { class: "row-actions" }, continueButton, h("button", { type: "button", class: "btn ghost", onclick: () => { adding = ""; addingPanel = ""; redraw(); } }, "Cancel")),
  );
}

// authorize starts an authorization and redraws to show its card; it
// returns the daemon's refusal, if any.
async function authorize(ctx, params, redraw) {
  try {
    const started = await ctx.conn.call("credentials.authorize", params);
    begun.add(started.id);
    // A page opened from a click handler is not a pop-up; one opened after
    // the round trip may be blocked, so the card keeps the link too.
    if (typeof window.nodwynOpenExternal === "function") window.nodwynOpenExternal(started.url).catch(() => {});
    else window.open(started.url, "_blank", "noopener");
    redraw();
    return "";
  } catch (e) {
    if (params.id) toast(e.message);
    return e.message;
  }
}

function authorizationCard(a, redraw) {
  if (a.state === "waiting")
    return h(
      "div",
      { class: "callout plain auth-card" },
      h("span", { class: "run-dot", style: "margin-top:6px" }),
      h("div", { style: "display:flex;flex-direction:column;gap:8px" }, h("div", {}, h("strong", {}, `Waiting for ${a.provider} `), `to send you back with ${a.class}. Agree on the provider's page; nodwyn waits up to five minutes.`), h("div", { class: "row-actions" }, h("a", { class: "btn small", href: a.url, target: "_blank", rel: "noopener" }, `Open ${a.provider}'s page`))),
    );
  if (a.state === "failed")
    return h(
      "div",
      { class: "callout" },
      h("div", {}, h("strong", {}, `${a.class} was not authorized: `), a.error),
      h("button", { type: "button", class: "btn small ghost", style: "margin-left:auto", onclick: () => { dismissed.add(a.id); redraw(); } }, "Dismiss"),
    );
  return null;
}
