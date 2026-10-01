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
