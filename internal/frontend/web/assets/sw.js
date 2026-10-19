// The console's service worker (ADR 52). It makes the console installable as
// an app and shows its "needs your answer" notice, which a phone shows no
// other way. It handles no fetch: nothing the console shows is cached, and
// every request reaches the bridge and its checks as before.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

// openTask shows a task, on this computer or on the linked host named host
// (ADR 54): in a console already open, which is asked to open it and then
// brought forward (a worker may focus a window only for a click on its
// notice), or in a new window at its address.
async function openTask(id, host) {
  const task = typeof id === "string" && id !== "" ? id : "";
  const on = typeof host === "string" ? host : "";
  const [open] = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  if (open) {
    if (task) open.postMessage({ open: task, host: on });
    await open.focus();
    return;
  }
  const where = on ? `host/${encodeURIComponent(on)}/` : "";
  await self.clients.openWindow(task ? `/#/${where}task/${encodeURIComponent(task)}` : "/");
}

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(openTask(event.notification.data?.id, event.notification.data?.host));
});
