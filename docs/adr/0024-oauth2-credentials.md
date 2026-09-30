# ADR 0024: OAuth2 credentials — acquisition, refresh, scopes

- Status: **proposed** (extends ADR 5 and the binding table; prerequisite for W5)
- Date: 2026-09-22

## Context
ADR 5 models a credential as one opaque value: registered once through
masked input, stored encrypted, injected at a bound position, never a Scheme
value. Google Docs — and most services a cowork workspace touches — use
OAuth2 instead, which breaks four of those assumptions:

1. **It is not one value.** A token set is an access token, a refresh token,
   an expiry, a set of scopes, and the endpoints they belong to.
2. **It expires.** Something must refresh it, which means the harness makes
   an HTTP request carrying a secret, from *inside* the credential store,
   with no Scheme and no tool call involved.
3. **Acquisition needs a browser.** Authorization Code with PKCE means
   opening a browser, catching a redirect, and exchanging a code.
4. **It rotates.** Many providers return a new refresh token on every
   refresh; the store is written during a session, concurrently with tool
   calls.

What must not change: the handle stays opaque (`#<cred:class:id>`), no token
ever becomes a Scheme value, `Inject*` stays the only way a secret leaves
the package, the guard still sees only class and destination, and the judge
never sees any of it.

## Decision

### 1. A credential has a kind
`static` (ADR 5, unchanged) or `oauth2`. An `oauth2` entry stores a token
set inside the same encrypted blob: `client_id`, optional `client_secret`,
`auth_endpoint`, `token_endpoint`, optional `revoke_endpoint`, `scopes`,
`access_token`, `refresh_token`, `expires_at`. Scheme sees the same handle
as before; nothing in the language changes.

**Provider profiles** ship for the common services (`google`, `microsoft`,
`github`, `notion`), supplying endpoints and a *narrow* default scope set —
`drive.file`, not `drive` — so the easy path is the least privileged one. A
custom provider is declared in config.

### 2. Acquisition is Authorization Code + PKCE, over a loopback redirect
```
nodwyn credential add --oauth2 google --scopes drive.file,documents
```
nodwyn generates a PKCE verifier and a `state` nonce, starts a **single-shot
listener on 127.0.0.1 with a random port, serving only `/callback`, with a
five-minute deadline**, opens the authorization URL, verifies `state` on the
redirect, exchanges the code with the verifier, stores the token set, and
returns the handle.

- **Public client by default.** A desktop application's "client secret" is
  not secret; PKCE is the actual protection. A confidential client is
  supported for users who bring one.
- **User origin only**, like `credential-register`: a model-originated call
  is refused, so no tool can put a consent screen in front of the user
  mid-turn.
- The loopback listener is a new inbound surface. It is bound to loopback,
  answers one request, checks `state`, and closes.

### 3. Refresh happens inside the keychain, bound and logged
At injection time, if the access token expires within a skew (60s), the
keychain refreshes first, then injects. Rules:

- **The refresh is egress and is authorized by the same binding row** that
  authorizes the credential's use: refreshing a credential is part of that
  credential's lifecycle, not a separate grant. A second row per class was
  considered and rejected — it would add a row nobody can reason about and
  a confirm nobody would read. The refusal to weaken auditing instead:
  **every refresh is logged** as `cred.refresh` (class, handle id, endpoint,
  new expiry, ok/error) and never carries a token.
- **Serialized per credential**, so parallel tool calls refresh once.
- **Rotation is honoured**: a new refresh token replaces the old one, written
  through the existing atomic write-then-rename.
- **Failure is terminal, not interactive.** A revoked or expired refresh
  token marks the credential `needs_reauth`; every use then denies with a
  message naming the command to re-authorize. nodwyn never opens a browser
  mid-turn.

### 4. Scopes are part of the authority the user sees
A binding row for an `oauth2` class may name `scopes`, meaning *this
credential may be used at this destination with at most these scopes*; a
token minted with more is refused at load time with a message saying which
scope is unauthorized. Every confirm involving an oauth2 credential names
**class, destination and scopes** — "Google Drive, full account" and "Google
Drive, files this app created" are the difference the user actually needs to
see, and class alone does not carry it (SPEC Risk 2).

### 5. Revocation
`nodwyn credential revoke <id>` calls the provider's revocation endpoint when
the profile has one, then deletes locally. Deleting locally without revoking
leaves a live token at the provider; the command says so when no revocation
endpoint exists.

## Consequences
- The keychain gains an HTTP client and a clock. It stays the only package
  that can read a token, and the value still has no accessor.
- A refresh is the one egress that happens without a confirm. It is bounded
  by: the binding row, a fixed endpoint from the provider profile, no
  payload of ours beyond the refresh token, and a log line every time.
- `cred.refresh` joins the fixed event kinds (ADR 2).
- W5 (documents, Google Docs) depends on this phase; W3 gains it for MCP
  servers that authenticate with OAuth rather than an API key.
- The exfil suite gains: a refresh to an unbound token endpoint is denied; a
  token never appears in the log, the context, or a Jev payload; a
  `needs_reauth` credential denies rather than prompting.

## Accepted limits
- **A refresh has no confirm.** Making it interactive would mean a browser
  or a prompt in the middle of a turn; the alternative chosen is binding
  plus logging.
- **Scope enforcement is the provider's.** nodwyn refuses to *use* a token
  outside its declared scopes, but a token that was minted too broadly is
  broad at the provider. Narrow defaults are the mitigation.
- **Tokens at rest are as safe as the passphrase**, exactly as in ADR 5.
