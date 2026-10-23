// Every host the window shows (ADR 54): this computer, and each nodwyn it is
// linked to, with a connection and a desk of its own. A linked host's socket
// goes to this computer's bridge, which relays it through this computer's
// daemon to that host's console, so the page keeps one origin. The host's
// own rules decide what its connection may do: what its hello offers is what
// the pages offer for it.
import { h, icon, ago, toast, removal } from "./dom.js";
import { Connection } from "./connection.js";
import { Desk } from "./desk.js";
import { keychainForm } from "./setup.js";

export class Hosts {
  // onOpen(id, host) opens a task a notice is about, and onLive(name) is
  // told that a linked host's connection is new: the page sets them.
  constructor(conn, desk) {
    this.local = { name: "", label: "This computer", conn, desk, info: null, detail: "" };
    this.linked = new Map(); // name -> {name, label, conn, desk, info, detail}
    this.listeners = new Set();
    // Whether a host's desk is read again after its keys were unlocked here:
    // a link's token is in this computer's keychain.
    conn.onResult((method) => {
      if (/^keychain\.(create|unlock)$/.test(method)) this.refresh().catch(() => {});
    });
  }
  onChange(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  changed(what = "hosts") {
    for (const fn of this.listeners) fn(what);
  }
  // all is every host, this computer first, then the linked ones by name.
  all() {
    return [this.local, ...[...this.linked.values()].sort((a, b) => a.name.localeCompare(b.name))];
  }
  get(name) {
    return name ? this.linked.get(name) || null : this.local;
  }
  // several says whether the window shows more than this computer.
  get several() {
    return this.linked.size > 0;
  }
  // live is every host whose desk can be read now.
  live() {
    return this.all().filter((e) => e.conn.live);
  }
  // refresh reads the links this computer has (host.list) and holds a
  // connection for each: one for a new link, none for one unlinked, and a
  // try at once for one waiting to try again.
  async refresh() {
    const conn = this.local.conn;
    const list = conn.live && conn.has("host.list") ? (await conn.call("host.list")) || [] : [];
    const names = new Set(list.map((info) => info.name));
    for (const [name, e] of this.linked) {
      if (names.has(name)) continue;
      e.conn.close();
      e.desk.reset();
      this.linked.delete(name);
    }
    for (const info of list) {
      let e = this.linked.get(info.name);
      if (!e) {
        e = this.add(info);
        this.linked.set(info.name, e);
      }
      e.info = info;
      if (e.conn.state === "idle" || e.conn.state === "down") e.conn.connect();
    }
    this.changed();
  }
  add(info) {
    const conn = new Connection(info.name);
    const desk = new Desk(conn, info.name);
    const e = { name: info.name, label: info.name, conn, desk, info, detail: "" };
    // A notice of one of its tasks opens that task on this host, and is
    // shown through the page's service worker, as this computer's are.
    desk.notifier.onOpen = (id, host) => this.onOpen?.(id, host);
    desk.notifier.worker = this.local.desk.notifier.worker;
    conn.onState(async (state, detail) => {
      e.detail = detail;
      if (state === "live") {
        // A new connection has none of the old one's subscriptions.
        this.onLive?.(e.name);
        try {
          await desk.start();
        } catch (error) {
          e.detail = error.message;
        }
      } else desk.reset();
      this.changed("state");
    });
    desk.onChange((what) => this.changed(what));
    return e;
  }
  // wake asks every linked host waiting to try again to try now: the page
  // is shown again, or the network is back.
  wake() {
    for (const e of this.linked.values()) e.conn.wake();
  }
  // drop closes every link's connection: this browser is no longer paired
  // with this computer, which they all go through.
  drop() {
    for (const e of this.linked.values()) {
      e.conn.close();
      e.desk.reset();
    }
    this.linked.clear();
    this.changed();
  }
  // needsYou is every host's tasks waiting for an answer, newest first, each
  // with its host's desk.
  needsYou() {
    return this.live()
      .flatMap((e) => e.desk.needsYou().map((item) => ({ item, desk: e.desk, host: e })))
      .sort((a, b) => new Date(b.item.last_event) - new Date(a.item.last_event));
  }
  // outbox is every host's answers waiting to be posted, with their desks.
  outbox() {
    return this.live().flatMap((e) => e.desk.outbox.map((held) => ({ held, desk: e.desk, conn: e.conn, host: e })));
  }
}

// state is what a host's connection says: a dot's class, the words, and the
// detail for its title.
export function hostState(e) {
  if (!e.name) return e.conn.live ? ["ok", "ready", ""] : ["bad", "offline", ""];
  switch (e.conn.state) {
    case "live":
      return ["ok", "ready", ""];
    case "unpaired":
      return ["bad", "refused", e.detail || `${e.name} no longer takes this computer: link it again.`];
    case "down":
      if (e.info?.locked) return ["warn", "keys locked", e.info.locked];
      return ["bad", "not reachable", e.detail || ""];
  }
  return ["", "connecting", ""];
}

// role says what this computer may do on a linked host, as its console
// offers it: the person's choices, or answers alone.
export function hostRole(e) {
  if (!e.name || !e.conn.live) return "";
  return e.conn.has("session.model.set") ? "Makes your choices" : "Answers";
}

// The Add a host form, open across redraws of the page.
let adding = false;
let unlinking = "";
const typed = { address: "", as: "", name: "", invite: "" };

// renderHosts draws the Hosts page: this computer and each linked host, with
// its state, its address, the name this computer has there and what it may
// do there, Add a host and Unlink.
export function renderHosts(root, ctx) {
  const { conn, desk, hosts } = ctx;
  const redraw = () => renderHosts(root, ctx);
  const canLink = conn.has("host.link");
  const card = (e) => {
    const [dot, words, detail] = hostState(e);
    const role = hostRole(e);
    const actions = [];
    if (!e.name) actions.push(h("a", { class: "btn small", href: "#/settings" }, "Settings"));
    else {
      // Its settings open in this window, over its connection, which says
      // whether it lets this computer change them (ADR 54, amended
      // 2026-10-23); the rest of its setup is changed in its own console.
      actions.push(h("a", { class: "btn small", href: e.desk.at("settings"), id: `host-settings-${e.name}` }, "Settings"));
      actions.push(h("a", { class: "btn small", href: e.info.address, target: "_blank", rel: "noopener noreferrer", title: "The rest of its setup is changed there, where your passkey signs you in" }, `Open ${e.name}'s console`));
      if (e.conn.state === "down" || e.conn.state === "unpaired") actions.push(h("button", { type: "button", class: "btn small", onclick: () => e.conn.connect() }, "Try now"));
      if (conn.has("host.unlink"))
        actions.push(
          removal({
            label: "Unlink",
            id: `unlink-${e.name}`,
            consequence: `Unlink ${e.name}? This computer forgets its token, and ${e.name} is asked to forget ${e.info.as}.`,
            asking: unlinking === e.name,
            ask: () => {
              unlinking = e.name;
              redraw();
            },
            cancel: () => {
              unlinking = "";
              redraw();
            },
            run: async () => {
              unlinking = "";
              try {
                const result = await conn.call("host.unlink", { name: e.name });
                toast(result.told ? `Unlinked ${e.name}, which forgot ${e.info.as}.` : `Unlinked ${e.name}. ${result.detail || ""}`.trim());
              } catch (error) {
                toast(error.message);
              }
              await hosts.refresh();
              redraw();
            },
          }),
        );
    }
    const note = e.name
      ? [`Linked as ${e.info.as}${e.info.added ? ` ${ago(e.info.added)}` : ""}.`, e.desk.sessions.size ? `${e.desk.sessions.size} task${e.desk.sessions.size === 1 ? "" : "s"}.` : "", detail].filter(Boolean).join(" ")
      : "Tasks you start here run on this computer, with its folders, models and keys.";
    return h(
      "article",
      { class: "card host-card", dataset: { host: e.name } },
      h("div", { class: "host-icon" }, icon(e.name ? "server" : "laptop", 20)),
      h(
        "div",
        { class: "host-main" },
        h("div", { class: "host-line" }, h("h2", {}, e.label), h("span", { class: "host-state", title: detail }, h("span", { class: `dot ${dot}` }), words), role ? h("span", { class: "badge plain host-role" }, role) : null),
        h("div", { class: "mono host-address" }, e.name ? e.info.address : "nodwyn on this computer"),
        h("p", { class: "hint" }, note),
      ),
      h("div", { class: "row-actions host-actions" }, actions),
    );
  };
  root.replaceChildren(
    h(
      "main",
      { class: "page", id: "hosts" },
      h(
        "div",
        { class: "page-col" },
        h("h1", {}, "Hosts"),
        h("p", { class: "lede" }, "Each host runs its own tasks, with its own folders, models and keys. This window shows them together and sends each answer to the host that asked."),
        canLink && !adding ? h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn", id: "add-host", onclick: () => { adding = true; redraw(); document.getElementById("host-address")?.focus(); } }, icon("plus", 15), "Add a host")) : null,
        adding && canLink ? addHost(ctx, redraw) : null,
        h("section", { class: "stack-md", "aria-label": "Hosts" }, hosts.all().map(card)),
        h("p", { class: "hint" }, "Linking makes this computer a device of the other host, named as you choose. That host decides what it may do: one invited by your device signed in there with your passkey makes your choices for its tasks and changes its settings here, unless that host refuses it, and one invited from its terminal answers. The rest of its setup is changed in its own console, and it can unlink this computer there."),
      ),
    ),
  );
}

// addHost is the form that links this computer to another nodwyn: the
// address of its console, the name this computer has there, what this
// computer calls it, and an invitation made there. The link's token is kept
// in this computer's keychain, so the form asks for it to be open first.
function addHost(ctx, redraw) {
  const { conn, desk, hosts } = ctx;
  if (!desk.keychain?.unlocked && conn.has("keychain.unlock"))
    return h(
      "div",
      { class: "card setup-form", id: "add-host-keychain" },
      h("h2", {}, "Add a host"),
      h("p", { class: "hint" }, "The link's token is kept in this computer's keychain, like a key: open it first."),
      keychainForm(ctx, { onDone: async () => {
        await desk.refreshSetup();
        redraw();
      } }),
      h("div", { class: "row-actions" }, h("button", { type: "button", class: "btn ghost", onclick: () => { adding = false; redraw(); } }, "Cancel")),
    );
  const field = (id, key, attrs) => {
    const input = h("input", { class: "input", id, autocomplete: "off", spellcheck: false, ...attrs, value: typed[key], oninput: () => (typed[key] = input.value) });
    return input;
  };
  const address = field("host-address", "address", { type: "url", required: true, placeholder: "https://workshop.example.net" });
  const as = field("host-as", "as", { required: true, pattern: "[a-z0-9][a-z0-9._\\-]{0,31}", placeholder: "for example laptop" });
  const name = field("host-name", "name", { pattern: "[a-z0-9][a-z0-9._\\-]{0,31}", placeholder: "for example workshop" });
  const invite = field("host-invite", "invite", { required: true, placeholder: "xxxx-xxxx-xxxx-xxxx-xxxx" });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit", class: "btn dark" }, "Link");
  // The host's name defaults to the first part of its address's name.
  const suggested = () => {
    try {
      const host = new URL(address.value.trim()).hostname;
      return /^\d+(\.\d+){3}$/.test(host) ? "" : host.split(".")[0].toLowerCase();
    } catch {
      return "";
    }
  };
  return h(
    "form",
    {
      class: "card setup-form",
      id: "add-host-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const chosen = name.value.trim() || suggested();
        if (!chosen) {
          error.textContent = "Name the host, for example workshop.";
          return;
        }
        submit.disabled = true;
        try {
          await conn.call("host.link", { name: chosen, address: address.value.trim(), as: as.value.trim(), invite: invite.value.trim() }, 30000);
          for (const key of Object.keys(typed)) typed[key] = "";
          adding = false;
          toast(`Linked ${chosen}. Its tasks show in the sidebar under its name.`);
          await hosts.refresh();
        } catch (e) {
          error.textContent = e.message.replace(/^host\.link: /, "");
          submit.disabled = false;
          return;
        }
        redraw();
      },
    },
    h("h2", {}, "Add a host"),
    h("p", { class: "hint" }, "On the other host, make an invitation: ", h("span", { class: "mono" }, "nodwyn devices invite"), " in its terminal, or Settings › Keys & devices in its console. One made by your device signed in there with your passkey lets this computer make your choices for that host's tasks and change its settings; one from its terminal lets it answer."),
    h("div", { class: "field" }, h("label", { for: "host-address" }, "The host's address"), address),
    h("div", { class: "field" }, h("label", { for: "host-as" }, "This computer's name there"), as, h("p", { class: "hint" }, "What the host records each answer you give from here with.")),
    h("div", { class: "field" }, h("label", { for: "host-name" }, "What to call the host here"), name, h("p", { class: "hint" }, "Lower-case letters, digits, dots or dashes; the first part of its address if left empty.")),
    h("div", { class: "field" }, h("label", { for: "host-invite" }, "Invitation"), invite, h("p", { class: "hint" }, "It is good once, for ten minutes.")),
    error,
    h("div", { class: "row-actions" }, submit, h("button", { type: "button", class: "btn ghost", onclick: () => { adding = false; redraw(); } }, "Cancel")),
  );
}
