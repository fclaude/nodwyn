# On your own computer

The simplest way to run nodwyn is as an app on the computer you're sitting at.

```sh
nodwyn app              # a window (a webview build) or your default browser
nodwyn app --browser    # your browser, even in a webview build
nodwyn app --no-open    # print the launch address and open nothing
```

`nodwyn app` is the whole product in one program. It shows the console and, when no daemon is running yet, it starts one inside the same process. Nobody has to run `nodwyn daemon`, copy a pairing code or unlock a keychain at a terminal.

What it does depends on what it finds:

- If a daemon is already running, for example one started at login, the app attaches to it. Closing the window leaves its tasks running.
- If there's none, the app serves tasks itself on the standard socket. Closing it ends those tasks. Their conversations are kept, and they resume later like any task after a shutdown.

While the app runs, `nodwyn tui` and `nodwyn ui` can attach to it, since it listens on the same socket a daemon would.

The page the app opens pairs itself with a one-time ticket carried in the address after the `#`. It never goes to a server, it's removed from the address bar at once, and it's valid for a minute. On a computer other people log in to, pair with the code the app prints to its log instead, using `nodwyn ui --no-open`, because another user could read the ticket in the process list for the moment the browser starts.

## The window

A webview build, `make build-webview` or the macOS app from `make app`, opens its own window. The console in it is the same page. The window adds three things and nothing else: a native folder chooser for **Add a folder**, system notifications when a task needs you (opt-in in Settings), and opening links in your default browser. None of them reads a file the page names.

## Keeping it running

A daemon that the app, `nodwyn tui` or line mode started stops when its window or terminal closes, and your scheduled tasks stop with it. To keep one running, have it start when you log in:

```sh
nodwyn startup on       # or Settings › General › While you're away › Start at login
nodwyn startup          # says whether it's on, and which nodwyn it runs
nodwyn startup off
```

On Linux that writes a systemd user unit, and on macOS a launchd agent. The app then uses that daemon and leaves it running when the window closes. A daemon started at login has no terminal to ask for the keychain's passphrase, so it starts locked. Home says when that holds a scheduled run back, and unlocking there lets the run go ahead.

If you'd like desktop notices when no window is open, Settings › General › While you're away › **Desktop notices from the daemon** turns them on. They say only "nodwyn needs your answer", never what the task is.

## On a phone

A phone can use the console of a computer that serves other devices, which is what [the server page](server.html) sets up. Served over https, the console is also an installable web app: Safari's **Share › Add to Home Screen** on an iPhone, or Chrome's **Install app** on Android, puts it on the Home Screen, where it opens full screen. Turn on Settings › This browser › Notifications in it to be told when a task waits for an answer.

![The console on a phone: Home](shot:phone-home)

Notices arrive while the app is open or in the background. A notice while the app is closed isn't built yet.
