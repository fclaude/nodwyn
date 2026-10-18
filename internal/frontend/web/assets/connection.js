// The browser's one connection to the bridge. It matches replies to
// requests and hands notifications to listeners. A dropped connection fails
// every outstanding request; nothing is ever retried (browser.md), but for a
// setup request the bridge held back for a touch of the passkey, which is
// sent again once the person gave it, since it never reached the daemon.

// touchCode is the bridge's refusal of a setup request from the person's
// device before its passkey touch (server.go).
const touchCode = -32030;

export class Connection {
  constructor() {
    this.socket = null;
    this.nextID = 0;
    this.waiting = new Map();
    this.methods = new Set();
    this.hello = null;
    this.listeners = new Set();
    this.stateListeners = new Set();
    this.resultListeners = new Set();
    this.state = "idle"; // idle | connecting | live | down | unpaired
    // confirmTouch, set by the page, asks the person for a touch of their
    // passkey and resolves once it is accepted.
    this.confirmTouch = null;
  }
  get live() {
    return this.socket?.readyState === WebSocket.OPEN && this.state === "live";
  }
  has(method) {
    return this.methods.has(method);
  }
  onEvent(fn) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }
  // onResult is told the method of every request that was answered, so a page
  // that depends on what a request changed (the keychain, a key) can read it
  // again without every caller of that request knowing.
  onResult(fn) {
    this.resultListeners.add(fn);
    return () => this.resultListeners.delete(fn);
  }
  onState(fn) {
    this.stateListeners.add(fn);
    return () => this.stateListeners.delete(fn);
  }
  setState(state, detail = "") {
    this.state = state;
    for (const fn of this.stateListeners) fn(state, detail);
  }
  fail(message) {
    for (const wait of this.waiting.values()) {
      clearTimeout(wait.timer);
      wait.reject(new Error(message));
    }
    this.waiting.clear();
  }
  async call(method, params = {}, timeout = 35000) {
    try {
      return await this.send(method, params, timeout);
    } catch (error) {
      if (error.code !== touchCode || !this.confirmTouch) throw error;
      const answer = await this.confirmTouch();
      // A touch that made this device the person's needs a connection that
      // says so: the request is for the person to make again.
      if (answer?.promoted) {
        this.connect();
        throw new Error("Confirmed. This device can now change nodwyn's setup: do it again.");
      }
      return this.send(method, params, timeout);
    }
  }
  send(method, params, timeout) {
    return new Promise((resolve, reject) => {
      if (this.socket?.readyState !== WebSocket.OPEN) {
        reject(new Error("Disconnected. Reconnect to reload; nothing was retried."));
        return;
      }
      const id = ++this.nextID;
      const timer = setTimeout(() => {
        this.waiting.delete(id);
        reject(new Error("Request timed out. Nothing was retried."));
      }, timeout);
      this.waiting.set(id, { resolve, reject, timer, method });
      this.socket.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
    });
  }
  connect() {
    if (this.socket) {
      const old = this.socket;
      this.socket = null;
      old.close();
    }
    this.fail("Reconnecting; no operation was retried.");
    this.setState("connecting");
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const socket = new WebSocket(`${scheme}://${location.host}/socket`);
    this.socket = socket;
    let opened = false;
    socket.onopen = async () => {
      if (this.socket !== socket) return;
      opened = true;
      try {
        const hello = await this.call("hello", { protocol: 1 });
        if (this.socket !== socket) return;
        this.hello = hello;
        this.methods = new Set(hello.methods || []);
        this.setState("live");
      } catch (error) {
        this.setState("down", error.message);
      }
    };
    socket.onmessage = (message) => {
      if (this.socket !== socket) return;
      let frame;
      try {
        frame = JSON.parse(message.data);
      } catch {
        return;
      }
      if (frame.id !== undefined && frame.id !== null && !frame.method) {
        const wait = this.waiting.get(frame.id);
        if (!wait) return;
        clearTimeout(wait.timer);
        this.waiting.delete(frame.id);
        if (frame.error) wait.reject(Object.assign(new Error(frame.error.message), { code: frame.error.code }));
        else {
          wait.resolve(frame.result);
          for (const fn of this.resultListeners) fn(wait.method);
        }
        return;
      }
      for (const fn of this.listeners) fn(frame);
    };
    socket.onclose = (event) => {
      if (this.socket !== socket) return;
      this.fail("Connection lost; no operation was retried.");
      // The bridge says why when it closes on purpose: the daemon is not
      // running (1013), or this device was revoked (1008). A socket
      // refused before it opened otherwise means this browser is not
      // paired; an established one that drops is "down".
      if (event.code === 1013) this.setState("down", event.reason);
      else if (event.code === 1008) this.setState("unpaired", event.reason);
      else this.setState(opened ? "down" : "unpaired");
    };
  }
}

// pair exchanges a pairing code (typed), an invitation or a launch ticket
// (from the URL fragment, which is never sent to a server) for the bridge's
// cookie.
export async function pair(body) {
  const response = await fetch("/pair", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    credentials: "same-origin",
  });
  if (!response.ok) {
    const text = (await response.text()).trim();
    throw new Error(text || "Pairing failed.");
  }
}

// takeInvite removes an invitation from the address bar and returns it: a
// link an invitation came as carries it in the fragment, which is never sent
// to a server.
export function takeInvite() {
  const match = /(?:^#|&)invite=([A-Za-z0-9-]+)/.exec(location.hash);
  if (!match) return "";
  history.replaceState(null, "", location.pathname + "#/");
  return match[1];
}

// takeTicket removes a launch ticket from the address bar and returns it.
export function takeTicket() {
  const match = /(?:^#|&)ticket=([A-Za-z0-9_-]+)/.exec(location.hash);
  if (!match) return "";
  history.replaceState(null, "", location.pathname + "#/");
  return match[1];
}
