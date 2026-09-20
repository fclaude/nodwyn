# ADR 0005: Keychain crypto

- Status: accepted
- Date: 2026-09-20

## Context
SPEC invariant 3: secrets are credentials, not content. The harness holds
handles; the value lives in a log-excluded encrypted store.

## Decision
`$XDG_CONFIG_HOME/nibb/keychain` is an XChaCha20-Poly1305 encrypted file whose
key is derived from a passphrase with argon2id. Values leave the package only
through sink functions that write them into an outgoing request at a bound
position; a value is never a `scheme.Value` and the value type has no
`String()`. An OS keyring backend is a later option, not v0.

## Consequences
- The passphrase is prompted with masked input through the front-end's
  `Answer` channel (ADR 10), never read from an environment variable.
- Losing the passphrase loses the credentials; they are re-registerable.
