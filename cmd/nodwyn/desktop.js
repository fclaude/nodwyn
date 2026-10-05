// Only the console's top frame receives desktop conveniences. Wails supplies
// the sender's native origin independently of this JavaScript check.
(() => {
  if (window !== window.top || location.origin !== NODWYN_ORIGIN || window.nodwynDesktopReply) return;
  const send = window.chrome?.webview?.postMessage?.bind(window.chrome.webview)
    || window.webkit?.messageHandlers?.external?.postMessage?.bind(window.webkit.messageHandlers.external);
  if (!send) return;
  let next = 0;
  const token = NODWYN_TOKEN;
  const pending = new Map();
  function call(method, url = "") {
    return new Promise((resolve, reject) => {
      const id = ++next;
      const timer = setTimeout(() => { pending.delete(id); reject(new Error("Desktop request timed out")); }, 300000);
      pending.set(id, { resolve, reject, timer });
      send(JSON.stringify({ id, method, url, token }));
    });
  }
  window.nodwynDesktopReply = (result) => {
    const request = pending.get(result.id);
    if (!request) return;
    pending.delete(result.id);
    clearTimeout(request.timer);
    if (result.error) request.reject(new Error(result.error));
    else request.resolve(result.value);
  };
  window.nodwynPickFolder = () => call("pickFolder");
  window.nodwynNotify = () => call("notify");
  window.nodwynOpenExternal = (url) => call("openExternal", url);
  // The console uses its own transport, so it does not import Wails' HTTP
  // runtime. Mark this bridge ready before Wails can deliver queued replies.
  send("wails:runtime:ready");
  window.dispatchEvent(new Event("nodwyn-desktop-ready"));
})();
