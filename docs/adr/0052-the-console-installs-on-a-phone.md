# ADR 0052: The console installs on a phone as a web app

- Status: accepted; §1–§5 implemented 2026-10-17; Web Push and several
  hosts in one app not built (§6)
- Date: 2026-10-17
- Amends: 30 (the phone view becomes an app on the Home Screen)

## Context

The person wants nodwyn always on, on a server, and to use it from any
computer and from a phone (ADR 30's `nodwyn ui --listen`). A phone already
reaches the console in its browser: it pairs with the code and a name, and
answers as `via web:phone`. Asked how to make that a phone app, we weighed
four ways on 2026-10-17:

- **A web app the console serves** (a manifest and a service worker): no
  store, no signing, no second code base. A phone adds it to the Home Screen
  and it opens full screen. An iPhone shows notifications only to such an
  app (iOS 16.4 and later), and only through its service worker.
- **A native shell around the same pages** (Capacitor): a store listing, an
  Apple developer account, signing for every release, and push through
  Apple's and Google's services with keys someone has to hold. For a tool
  people host themselves, nobody is in place to hold them.
- **Wails' mobile targets** (ADR 44 chose Wails v3 for the desktop): not
  stable at the pinned beta.
- **Tauri's mobile targets:** a Rust tool chain beside Go's, for the same
  window on the same pages.

The console already lays itself out for a phone (ADR 30 §6), and every
notice it shows is the fixed "nodwyn needs your answer". But Android's
Chrome refuses `new Notification` on a page and requires a service
worker's `showNotification`; an iPhone's Home Screen app has only the
latter. So on a phone the console's notices never appeared.

## Decision

1. **The console is an installable web app.** The bridge serves a manifest
   (`manifest.json`: name, `standalone` display, the console's dark
   colours, start at `/`), its icons (`icons/`: an SVG and PNGs at 180, 192
   and 512 pixels, the 512 one also maskable) and a service worker
   (`sw.js`), all from the embedded assets, like the rest of the page. The
   page links the manifest, the Apple touch icon and the theme colour.
2. **The service worker handles no fetch.** It caches nothing, so no task,
   prompt or diff is ever kept in a cache, and every request still reaches
   the bridge and its host, origin and pairing checks as before. It is
   there to make the console installable and to show its notices.
3. **Notices are shown through the service worker when one is
   registered,** with the same fixed text, the task's tag, and the task's
   id as data that is never shown. A click on one focuses an open console
   and opens that task, or opens the console at that task. Without a
   service worker, a notice is shown as before. A prompt that stops
   waiting closes its notice either way.
4. **An app and its notices need a secure page:** https (`--tls-cert`, or a
   proxy that ends TLS, `--name https://NAME`) or this computer's loopback.
   Over plain http to another address the console works as before but
   neither installs as an app nor shows notices; Settings › This browser
   says why, and on an iPhone or iPad it says to add nodwyn to the Home
   Screen first. Its notification switch is called Notifications, since it
   is no longer only a desktop's.
5. **The page keeps out of the screen's edges.** The page already asks for
   the whole screen (`viewport-fit=cover`); in an app on the Home Screen
   nothing else keeps the composer from the home indicator, so the page
   and the phone's task drawer are padded by the screen's safe areas, which
   are zero on a desktop.

## 6. Not built

- **Web Push**, so that a notice arrives while the app is closed. The bridge
  would send a fixed, encrypted notice through the push service of the
  phone's browser (Apple's, Google's or Mozilla's), which is an egress
  decision of its own, with its keys in the keychain.
- **Several hosts in one app**, the design canvas of 2026-10-17: tasks of
  this computer and of an always-on host side by side, and a phone that
  reaches every host through one.
- **Pairing without the code,** which the remote-first trust proposal of
  2026-10-17 covers.

## Consequences

- No store and no signing: an update of nodwyn updates the app, since the
  page and its worker come from the bridge. The worker is fetched again on
  every load (`no-store`), and takes over at once.
- An app on an iPhone's Home Screen keeps its own storage, apart from
  Safari's, so a phone paired in Safari may have to pair again in the app.
  This was not tried on a device.
- The safe-area padding and the install itself were not tried on a phone:
  Chromium's checks see insets of zero and cannot add an app to a Home
  Screen.
