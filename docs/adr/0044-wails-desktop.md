# ADR 44: Wails owns the desktop window

Date: 2026-10-05. Status: accepted.

## Context

Native desktop distribution needs a maintained window framework and native
dialogs. The small webview binding left this integration to platform scripts.
Nodwyn already owns a browser console with authenticated, same-origin RPC.
Wails v2's asset-origin model would require adapting that transport. Wails v3
can load the existing loopback URL directly and reports native message origins.

## Decision

1. Pin Wails v3.0.0-beta.28 and replace webview_go behind the existing
   `webview` build tag. Accept the beta dependency explicitly; changes to it
   require another native window run, not just cross-compilation.
2. Keep the existing console, pairing tickets, cookies, CSP and WebSocket
   checks. Do not register Wails task services or redirect console RPC through
   the Wails asset server.
3. Limit the native bridge to folder selection, the fixed attention notice,
   and HTTP(S) links. Only the host writes its JS bootstrap and creates its
   random per-window capability. A repository file cannot choose the origin,
   replace that bootstrap, or mint a capability. Native origin checks and the
   capability gate every request; GTK's missing frame identity makes the
   capability necessary. Replies check the current origin again.
4. Use Wails' folder chooser. Keep the existing platform notification commands
   with fixed text. Select GTK4 or GTK3 through their real build tags and keep
   the macOS universal bundle recipe, adding a verified, zipped bundle output.
5. Exercise the real window and JS-to-Go reply path in a desktop session,
   and the bridge's refusal cases in normal Go tests. Browser checks cover
   the late arrival of the desktop bridge.

## Consequences and remaining work

The dependency is larger and still beta. This migration supplies the desktop
shell on the host's supported systems, Linux and macOS. It does not port Unix
file locking, process groups or authenticated local sockets to Windows. A
Windows-only app package must wait for that host port or an explicitly chosen
WSL backend. The Linux runner still publishes portable binaries and books;
native app publication needs a separately exercised build recipe. macOS signing
remains ad hoc and no notarisation is claimed. The Linux native check is not
evidence that a macOS or Windows app has run.

## Amendment 2026-10-27: Carry the icon into the native app

The console and macOS bundle already have the cream n and blue dot, but the
native Wails options did not carry an icon and the repository provided no
Linux application-menu entry. Embed the console PNG in the native build and
set the GTK application id to `com.nodwyn.app`, matching the new desktop file.
Install the existing SVG and PNG in the hicolor theme with that name. A
source-checkout installer targets an explicitly selected executable and the
person's XDG data directory; it reads no project policy or session content.
The macOS bundle keeps its existing icon. Linux distribution packages can
install the supplied desktop file and the same assets in their prefix.

## Amendment 2026-10-30: The release publishes the macOS app

The "separately exercised build recipe" this ADR left for native app
publication is `scripts/app-macos.sh` on a macOS runner, in `ci.yml` on every
push and in the release (ADR 42, amended 2026-10-30). The script now runs the
bundle's executable after signing it, so a bundle that does not start fails
the job; it does not open the window, which still needs a desktop session.
Homebrew installs the release's bundle as a cask, in place of a formula that
wrapped the portable binary in a script without an icon.

## Amendment 2026-10-31: The native window uses the bundle's icon

The console PNG embedded on 2026-10-27 is a full-bleed dark square. Wails
sets it as the application's icon when the app starts, so on macOS it
replaced the bundle's icon in the Dock with a square larger than its
neighbours, without the margin and rounded corners of the macOS grid. The
embedded `cmd/nodwyn/icon.png` is now the 512-point image of
`packaging/macos/Nodwyn.icns`, so the running app shows the bundle's icon.
The Linux launcher keeps installing the console's icons.

## Amendment 2026-11-02: Linux desktop release and daemon identity

Publish a separate amd64 GTK3/WebKitGTK 4.1 archive on Ubuntu 24.04, carrying
its desktop file, hicolor icons and packaging contract. Portable releases
remain browser builds. Use the same com.nodwyn.app GNOME identity and refresh
icon and desktop caches. Scope Wails activation by effective UID and socket;
a repeat launch only shows and focuses the existing window, ignoring its
arguments and ticket. Browser mode bypasses native activation. No project
file supplies activation policy or authority. The daemon handshake reports
its OS hostname and effective account, displayed in the console header beside
the execution daemon of a linked route; neither a URL nor a link alias proves
identity. Existing authentication and socket ownership checks remain the gate.
Native arm64 publication and automated GNOME dock acceptance are not built.

## Amendment 2026-11-11: The Linux launcher shows the tile too

The console's icon that the Linux launcher kept is a full-bleed dark square,
right for a maskable web-app icon and wrong in a GNOME dock, where it sat
beside rounded tiles as a black box. The launcher now installs
`packaging/linux/com.nodwyn.app.png`: the 1024-pixel image of
`packaging/macos/Nodwyn.icns`, cropped to its tile and shadow (the macOS grid
leaves a fifth of the canvas empty, which a Linux dock does not expect) and
scaled to 512 pixels. No scalable icon is installed, because the tile has no
vector source and hicolor's `scalable/apps` wins over `512x512/apps` at the
sizes a dock asks for. A distribution package drops the SVG. The installer
removes the one an earlier run left in the person's data directory and puts
the PNG in `scalable/apps` there as well, which GTK then chooses over the
SVG of a system package that has not been updated (looked up with
`Gtk.IconTheme` against such a package, at 32 to 256 pixels). The console
and the website keep the square.
