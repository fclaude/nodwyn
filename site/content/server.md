# On a server

nodwyn can run on a computer nobody sits at, like a home server, and be used from a laptop, a phone and any number of terminals. Tasks live in the daemon on the server, so they keep going when every client has disconnected.

You run two processes as your own user: `nodwyn daemon` owns the tasks, keychain and schedules; `nodwyn ui` serves the console to other devices.

## Start both at boot

Linux's user services stop at logout unless you ask them not to, and the daemon's socket lives in `$XDG_RUNTIME_DIR`, which goes at logout too. Turn lingering on once:

```sh
loginctl enable-linger "$USER"
nodwyn startup on           # the daemon, as a user service
```

The daemon starts locked if the keychain has a passphrase, because nobody is there to type it. You unlock it from a device later, and a schedule that came due meanwhile waits and then runs once, late.

Then run the console:

```sh
nodwyn ui --no-open --listen 127.0.0.1:7777 --name https://nodwyn.example.net
```

`--listen` is where the console accepts connections, and `--name` is the address people type. Without `--no-open` it would try to open a browser on a machine with no screen.

There isn't a shipped service unit for the console yet. Here's an example to adapt, assuming you installed `nodwyn` in `~/.local/bin`. Put it in `~/.config/systemd/user/nodwyn-ui.service`, next to the `nodwyn.service` that `nodwyn startup on` writes:

```ini
[Unit]
Description=nodwyn console
After=nodwyn.service

[Service]
ExecStart=%h/.local/bin/nodwyn ui --no-open --listen 127.0.0.1:7777 --name https://nodwyn.example.net
Restart=on-failure

[Install]
WantedBy=default.target
```

Then `systemctl --user enable --now nodwyn-ui.service`.

## Give it https

Devices can pair and answer over plain http, but nobody can sign in with a passkey over it, so the setup choices stay on the server. For the full set, give the console https in one of three ways.

You can use any of these:

- **A proxy:** End TLS in front of the console with Caddy, nginx with certbot, a VPN's https or `tailscale serve`. Set `--name https://NAME` to the address devices use.
- **Certificate files:** Give the console a certificate and key from an ACME client or your own CA with `--tls-cert` and `--tls-key`. It reads them again when they're renewed.
- **A certificate managed by nodwyn:** On a server the internet reaches on port 443 with no proxy in front, `--acme nodwyn.example.net --acme-accept-terms` asks Let's Encrypt for a certificate and renews it. A service unit needs `AmbientCapabilities=CAP_NET_BIND_SERVICE` to bind port 443. Use `--acme-directory` to try another CA, such as Let's Encrypt's staging service, first.

## Pair a device

Pairing takes no code. Once, on the server:

```sh
nodwyn devices invite
```

It prints an invitation. Open the console's address on your laptop or phone, name the device, enter the invitation and choose **Connect and make a passkey**. Creating the first passkey needs an invitation from the server's terminal. After that, your other devices can sign in with the passkey or use an invitation from Settings › Keys & devices on a device that's already signed in.

![Settings, Keys & devices, where devices are invited and listed](shot:settings-devices)

A device stays signed in while you use it and signs out after 90 days of inactivity. Restarting or upgrading nodwyn signs nobody out. A device signed in with the passkey can make the choices the computer makes: a task's model, judge, limits and schedule, and unlocking the keychain. Before it changes setup it asks for a touch of the passkey, once per sitting of changes. A device that paired with a plain invitation can start and answer tasks and no more.

The console's journal records every sign-in, invitation and setup change, with the device that made it. On the server, `nodwyn devices` lists devices and `nodwyn devices revoke NAME` ends one. Settings removes a lost phone's passkey.

## After a restart

Unlock the keychain from any device signed in with the passkey. A provider's OAuth sign-in works from your laptop too. The provider's page ends on an address that won't load there, and pasting that address into the card nodwyn shows (**Finish**) completes it. `nodwyn trust` and `nodwyn credential add --oauth2` still need a terminal on the server, and a project's startup files are reviewed from Workspaces on a device signed in with the passkey.

To have the keychain open by itself after a reboot, give the daemon's unit its passphrase as a systemd credential:

```sh
systemd-ask-password -n | sudo systemd-creds encrypt --with-key=host+tpm2 --name=nodwyn-keychain - /etc/nodwyn/keychain.cred
# in the daemon's unit, which must then be a system service (User=you):
#   LoadCredentialEncrypted=nodwyn-keychain:/etc/nodwyn/keychain.cred
```

Know what that gives you. The keychain file is protected at rest, against a copy, a backup or a disk pulled out. While nodwyn runs, any process of your account can read the decrypted passphrase, so against code running as you it's as open as a keychain with no passphrase. That's why it's opt-in. The manual has the user-service variant.

## One window for several computers

If you also run nodwyn on your laptop, its window can show the server's tasks beside its own, with everything that needs you under one **Needs you**, and a new task started on whichever computer should run it. The laptop links to the server once from **Hosts**, with an invitation made on the server.

On the server, make the invitation with `nodwyn devices invite`, or with **Invite a device** in Settings › Keys & devices of a console signed in with your passkey. The second kind lets the laptop make your choices for the server's tasks, and the first lets it start and answer tasks. On the laptop, open **Hosts**, choose **Add a host**, and enter the server's address, the name the server should record your answers under (such as `laptop`), what to call the server here, and the invitation. The laptop keeps the link's token in its own keychain.

![Home, with a task asking for an answer](shot:home)

From then on the sidebar groups tasks by host, **Runs on** on Home picks the host a new task starts on, with that host's folders and models, and you answer the server's tasks from the laptop. The server decides what the laptop may do. To refuse the settings a linked laptop could otherwise change, start the server's console with `--no-link-settings`.

## Pointing terminals at it

Terminals on the server attach to the same daemon. [The next page](terminals.html) is about them.
