// The browser's one connection to the bridge. It matches replies to
// requests and hands notifications to listeners. A dropped connection fails
// every outstanding request, which is never sent again, and is made again by
// itself after a wait (browser.md).

// A connection that drops is made again after a wait that doubles from
// retryFirst up to retryLast. A daemon that is not running (1013) is asked
// no more often than every retrySlow, since someone has to start it.
const retryFirst = 1000;
const retrySlow = 5000;
const retryLast = 30000;
// aliveWait is how long a connection that looks live has to answer when the
// page is shown again or the network comes back: after a sleep a socket can
// look open with nobody at the other end.
const aliveWait = 10000;

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
    this.state = "idle"; // idle | connecting | live | down (until retryAt) | unpaired
    this.retries = 0; // connections tried since the last one that went live
    this.retryTimer = 0;
    this.retryAt = 0;
    this.checking = false;
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
  call(method, params = {}, timeout = 35000) {
    return new Promise((resolve, reject) => {
      if (this.socket?.readyState !== WebSocket.OPEN) {
        reject(new Error("Disconnected. Reconnect to reload; nothing was retried."));
        return;
      }
      const id = ++this.nextID;
      const timer = setTimeout(() => {
        this.waiting.delete(id);
        reject(Object.assign(new Error("Request timed out. Nothing was retried."), { timedOut: true }));
      }, timeout);
      this.waiting.set(id, { resolve, reject, timer, method });
      this.socket.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
    });
  }
  connect() {
    clearTimeout(this.retryTimer);
    this.retryAt = 0;
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
        this.retries = 0;
        this.setState("live");
      } catch (error) {
        // A socket that closed has been seen to already; one still open
        // that could not say hello is closed and tried again.
        if (this.socket !== socket) return;
        this.socket = null;
        socket.close();
        this.retry(error.message);
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
        if (frame.error) wait.reject(new Error(frame.error.message));
        else {
          wait.resolve(frame.result);
          for (const fn of this.resultListeners) fn(wait.method);
        }
        return;
      }
      for (const fn of this.listeners) fn(frame);
    };
    socket.onclose = async (event) => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.fail("Connection lost; no operation was retried.");
      // The bridge says why when it closes on purpose: this device was
      // revoked (1008), or the daemon is not running (1013).
      if (event.code === 1008) {
        this.retries = 0;
        this.setState("unpaired", event.reason);
        return;
      }
      if (event.code === 1013) {
        this.retry(event.reason, retrySlow);
        return;
      }
      // A socket refused before it opened does not say why. The bridge
      // does: a browser it does not take (never paired, revoked, or paired
      // with a run of the console that has since stopped) pairs again, and
      // one that could not reach it tries again.
      if (!opened && (await paired()) === false) {
        if (this.socket) return;
        this.retries = 0;
        this.setState("unpaired");
        return;
      }
      if (!this.socket) this.retry();
    };
  }
  // retry connects again after the wait for this attempt; retryAt says
  // when, for the page to show.
  retry(detail = "", least = 0) {
    const wait = Math.max(least, Math.min(retryLast, retryFirst * 2 ** this.retries));
    this.retries++;
    clearTimeout(this.retryTimer);
    this.retryAt = Date.now() + wait;
    this.retryTimer = setTimeout(() => this.connect(), wait);
    this.setState("down", detail);
  }
  // wake is told that the page is shown again or the network is back. A
  // wait to reconnect ends at once, and a connection that looks live is
  // asked to answer. A request already waiting will time out by itself, and
  // the bridge relays one request at a time, so none is asked then.
  wake() {
    if (this.state === "down") {
      this.connect();
      return;
    }
    if (!this.live || this.checking || this.waiting.size) return;
    const socket = this.socket;
    this.checking = true;
    this.call("hello", { protocol: 1 }, aliveWait)
      .catch((error) => {
        if (error.timedOut && this.socket === socket) this.connect();
      })
      .finally(() => (this.checking = false));
  }
}

// paired asks the bridge whether it takes this browser: true or false, or
// null when the bridge cannot be reached, which a proxy in front of a
// stopped console (tailscale serve) answers with a 5xx.
async function paired() {
  try {
    const response = await fetch("/paired", { credentials: "same-origin", cache: "no-store" });
    if (response.status >= 500) return null;
    return response.ok && (await response.json()).paired === true;
  } catch {
    return null;
  }
}

// pair exchanges a pairing code (typed) or a launch ticket (from the URL
// fragment, which is never sent to a server) for the bridge's cookie.
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

// takeTicket removes a launch ticket from the address bar and returns it.
export function takeTicket() {
  const match = /(?:^#|&)ticket=([A-Za-z0-9_-]+)/.exec(location.hash);
  if (!match) return "";
  history.replaceState(null, "", location.pathname + "#/");
  return match[1];
}
