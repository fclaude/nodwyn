# Linux GNOME desktop package contract

The release adds `nodwyn-linux-amd64-desktop.tar.gz`, built on Ubuntu 24.04
amd64 with `CGO_ENABLED=1` and `-tags=webview,gtk3` using pinned Wails v3.
Portable archives keep their names and browser behaviour. No native arm64
release is claimed. Rebuild from source on other architectures.

Runtime dependencies:

- Ubuntu 24.04+: `libgtk-3-0t64`, `libwebkit2gtk-4.1-0`,
  `libayatana-appindicator3-1`, `libnotify-bin`, `xdg-utils`,
  `hicolor-icon-theme`, `desktop-file-utils`. Build dependencies:
  `build-essential`, `pkg-config`, `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`,
  `libayatana-appindicator3-dev`, and the Go version in go.mod.
- Arch: `gtk3`, `webkit2gtk-4.1`, `libayatana-appindicator`, `libnotify`,
  `xdg-utils`, `hicolor-icon-theme`, `desktop-file-utils`. Build dependencies:
  `base-devel`, `pkgconf`, `go`.

The distro resolves transitive libraries. Do not alias WebKit pkg-config
names or disable WebKit's sandbox. Package builders should generate shared
library dependencies as well as declaring these requirements.

Install the archive's `nodwyn` and `nodwyn-egress` to `/usr/bin` (0755),
and its `share/` tree to `/usr/share` (files 0644). The desktop entry runs
`nodwyn app`; with `/usr/bin` on GNOME's PATH this is `/usr/bin/nodwyn app`.
The application id, desktop basename, icon name and StartupWMClass are
`com.nodwyn.app`. Do not set DBusActivatable: activation is handled by Wails.
Use distro package hooks/triggers for:

```
update-desktop-database /usr/share/applications
gtk-update-icon-cache --force --ignore-theme-index /usr/share/icons/hicolor
```

Run cache updates after removal too. Arch's standard desktop/icon hooks can
supply these; avoid duplicate hooks. A source installation can instead run
`python3 scripts/install-desktop.py /absolute/path/to/nodwyn` from the checkout.
It installs per-user entries and icons and refreshes available caches.

Check for an overriding `~/.local/share/applications/com.nodwyn.app.desktop`
and stale `nodwyn.desktop` before diagnosing the system package. Update its
Exec to the installed native binary. The installer migrates a legacy entry
to a hidden alias; re-pin the canonical application from GNOME's menu.

Native repeated launches activate the existing window for the same account
and socket. Different socket selections have separate activation identities;
activation never forwards a launch ticket or changes the existing target.
`nodwyn app --browser` remains explicit browser mode. Never hardcode a web
port, Raven's endpoint, or another account's socket in the personal launcher.
The authenticated daemon handshake supplies the account and host shown in
the console; linked views separately show the execution daemon's identity.
Older daemons display “identity unavailable” until upgraded.

Before promotion, test the installed menu icon, native window, repeated
activation, and dock pin/group on GNOME Wayland and Xorg. Exercise the native
bridge with `NODWYN_TEST_DESKTOP=1 go test -tags=webview,gtk3 ./cmd/nodwyn
-run '^TestWailsWindowRoundTrip$' -count=1 -timeout=2m`. A headless CI build
is not desktop acceptance. Keep authentication and per-account sockets intact.
