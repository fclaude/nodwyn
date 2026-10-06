# ADR 0005: Keychain crypto

- Status: accepted
- Date: 2026-09-20

## Context
SPEC invariant 3: secrets are credentials, not content. The harness holds
handles; the value lives in a log-excluded encrypted store.

## Decision
`$XDG_CONFIG_HOME/nodwyn/keychain` is an XChaCha20-Poly1305 encrypted file whose
key is derived from a passphrase with argon2id. Values leave the package only
through sink functions that write them into an outgoing request at a bound
position; a value is never a `scheme.Value` and the value type has no
`String()`. An OS keyring backend is a later option, not v0.

## Consequences
- The passphrase is prompted with masked input through the front-end's
  `Answer` channel (ADR 10), never read from an environment variable.
- Losing the passphrase loses the credentials; they are re-registerable.

## Amendment — 2026-09-30: kinds, and more than one process

A credential has a kind (ADR 24): `static`, one value as above, or `oauth2`,
a token set that the keychain refreshes and injects as its access token.
The plaintext document is version 2 when it holds an oauth2 entry, so a
binary that predates them refuses the file rather than rewriting it without
the token sets; a keychain of static keys is still version 1.

The daemon, a terminal session and a `nodwyn credential` command may hold
the same keychain open. Every change takes an advisory lock on the sibling
`keychain.lock`, rereads the file, applies the change and writes it back;
a read rereads the file if another process wrote it. Before this, each
process re-sealed what it had in memory, and the last writer won. The
store can now list its credentials' metadata and delete one.

## Amendment — 2026-10-02: a keychain without a passphrase

Some people will not type a passphrase, and an unattended run or a daemon a
service manager starts has nobody to type it. A keychain may now have none:
`nodwyn keychain init --no-passphrase`, or "Don't protect it with a
passphrase" in the console's Settings. It is the same file, sealed under the
empty passphrase, so nothing about the format changes and a binary that
predates this opens it by being given an empty passphrase. Opening tries
the empty passphrase first (`keychain.OpenWithoutPassphrase`, one key
derivation, about 20 ms) and asks for one only if that fails; the command
line, the TUI, a daemon and the app all open such a keychain without
asking, terminal or not.

What it gives up is said wherever one is made or opened: the AEAD still
protects the file's integrity, but its key is one anyone can derive, so the
keychain is as safe as its file permissions (0600, the user) and no safer.
Anyone who can read the file, or a backup of it, can use every key in it.
It is not recommended, and the console marks it until a passphrase is set.

The passphrase can be set, changed or removed later (`nodwyn keychain
passphrase [--remove]`, Settings in the console, `keychain.passphrase` over
the socket). Each seals the file under a new salt. Changing or removing an
existing passphrase needs the current one, at the console too: being at the
console lets a person use the keys where bindings allow, and removing the
passphrase would let them read every key from the file. Another process
that holds the keychain open can no longer change it after that, and says
to unlock again; reads go on from what it has.

A static key's value can be replaced in place (`Store.Replace`): same id,
same class, so the bindings and settings that name the class carry on after
a key is rotated where it was issued. Values still only go in.

The console's Connections › Credentials manages credentials without a
terminal: the list (classes, kinds, scopes, expiry, the binding rows that
name each class, and what is configured to send it), adding a static key
with exact binding rows for one host, allowing a stored credential for a
host, replacing, removing and revoking. No method returns a value or a
token (protocol.md, "Credentials"), and only a console on this computer may
change anything.

## Amendment 2026-10-06: security review and project identity

[ADR 45](0045-project-trust-and-security-boundaries.md) supersedes the earlier
behaviour where it differs: repository startup and approval files need an external
user trust record; executable identity is resolved, credential transport is a user
setting with secure defaults, keychain decoding and persistence are bounded, and
project paths resolve symlinks consistently. Its compatibility rules apply to
existing sessions, workspaces, notes and worktrees.
