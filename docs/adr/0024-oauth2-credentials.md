# ADR 0024: OAuth2 credentials — acquisition, refresh, scopes

- Status: accepted; implemented 2026-09-30 (W4), and from the console
  2026-10-02 (amendment below). Extends ADR 5 and the binding table;
  prerequisite for W5. "Implemented" below says where the code
  differs from this text.
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

## Implemented — 2026-09-30

As decided, with these specifics:

- **Commands.** `nodwyn credential add --oauth2 PROVIDER --client-id ID
  [--scopes A,B] [--class CLASS] [--client-secret] [--no-browser]` acquires;
  `nodwyn credential login ID|CLASS` authorizes a `needs_reauth` credential
  again with the same client, endpoints and scopes; `nodwyn credential revoke
  [--local] ID|CLASS` revokes and deletes (`--local` only deletes);
  `nodwyn credential list` describes every credential, never a secret. They
  open the keychain file directly, at a terminal: authorizing is a user
  action and no primitive or tool reaches it.
- **Profiles** ship for `google` (short scope names expand to
  `https://www.googleapis.com/auth/NAME`; `access_type=offline`), `microsoft`,
  `github` and `notion` (both need a client secret), plus `custom` with
  `--auth-url`, `--token-url`, `--revoke-url` (an MCP server's authorization
  server, for one). No client id ships: the user registers nodwyn with the
  provider and passes theirs. Endpoints must be https, or http on loopback.
  The profiles' endpoints are the providers' documented ones; the tests run
  against a fake provider (`test/fakes/oauth`), not the real services.
- **Storage.** An entry has a kind; an oauth2 entry's token set (client,
  endpoints, granted scopes, tokens, expiry, `needs_reauth`) is in the same
  encrypted blob. A keychain holding one is written as format version 2, so
  an older binary refuses it instead of rewriting it without its token
  sets; a keychain of static keys stays version 1 (ADR 5, amended).
- **Concurrency.** Every change takes an advisory lock on `keychain.lock`
  and rereads the file first, and reads reread a file another process
  wrote, so the daemon, a session in the terminal and a `credential`
  command never write back a token another has already rotated. Refreshes
  of one credential are serialized by the same lock, across processes too:
  the second finds the token the first stored.
- **Refresh** happens in `Store.Refresh`, which the session runs in each of
  its injection sinks after the use was checked against the binding table;
  the keychain refuses to inject an access token that has expired, so a path
  that skipped the refresh fails instead of sending a stale token. A use the
  binding table refuses never reaches the token endpoint. The session logs
  each refresh as `cred.refresh` (class, handle id, token endpoint host, new
  expiry, rotated, ok or error), holding records made while an MCP server
  connects until `session.start`. Inspecting an MCP server from the console
  has no session, so a refresh there is not logged, as nothing else there
  is. A refusal by the token endpoint (`invalid_grant`, `invalid_client`,
  `unauthorized_client`), or an expired token with no refresh token, marks
  the credential `needs_reauth`; a network error does not.
- **Scopes.** A binding row may name `scopes`; it then binds an oauth2
  credential only if every scope the token was granted is among them, and
  the denial names the first that is not. The guard's E3 check and the
  model and MCP authorizers pass the granted scopes; `egress.check` records
  them; the confirm names them (`Prompt.Egress.Scopes`), and a proposed
  binding row pins them. A static key has no scopes, so a row's scopes do
  not affect it.
- **Revocation** posts the refresh token (or the access token, without one)
  to the revocation endpoint (RFC 7009) and deletes the entry; a provider
  without one is said to keep the tokens valid.

Not done: MCP's own authorization discovery (protected-resource metadata,
dynamic client registration). An MCP server that uses OAuth works with a
`custom` credential whose client the user registered.

## Used by W5 — 2026-10-01

`google-doc-upload` (ADR 22, W5 implemented) is the first tool built on
this ADR: the `google` profile's default scope, `drive.file`, is all it
needs, since it only creates documents. Its credential is supplied by Go
like an MCP server's key: the guard judges the call as carrying the
configured class (default `google`) to the upload host, a binding row's
`scopes` bound what the token may hold, and the token is injected through
the session's sink, so a stale one is refreshed and `cred.refresh` logged
before the upload. Its tests run against the fake provider
(`test/fakes/oauth`) and a fake Drive (`test/fakes/drive`).

## Amendment — 2026-10-02: authorizing from the console

The command line was the only place to add an OAuth2 credential or
authorize one again, which left the people ADR 30 has in mind with a
credential that stops working and no way to fix it. The console's
Connections › Credentials now does both (`credentials.authorize`), with the
same flow: the daemon builds the authorization URL and starts the
single-shot loopback listener, answers with the URL at once, and the
console opens it in the person's browser; the provider sends the browser
back to the daemon's loopback, and the console polls `credentials.list`
for the outcome (waiting, done or failed, listed for ten minutes). At most
four wait at once, each for five minutes, and a daemon that stops cancels
them.

It stays a user action: the method is offered to the console on the
computer nodwyn runs on and refused to a paired device, and no primitive or
tool reaches it, so nothing the model does can put a consent screen in
front of the user. The redirect goes to the daemon's loopback, so it works
only from a browser on that computer. A credential authorized this way is
bound from the same page; its rows carry the scopes it was granted. Revoking
from the console asks the provider first, as `credential revoke` does, and
"Remove here only" is its `--local`.


## Amendment 2026-10-05: OpenAI models can use ChatGPT plan quota

An API key uses API billing; people want nodwyn to use their eligible
ChatGPT plan allowance. OpenAI now documents public-client dynamic
registration and plan-authorized Responses requests:
[registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in),
[accounts and sessions](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions),
[models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference).

The shipped `openai` OAuth2 profile, available from the CLI and existing
local-console authorization method, defaults to `dynamic_agent_client`
when no client id is supplied. Authorization uses the fixed OpenAI issuer,
`/auth/callback` on 127.0.0.1, fresh state, OIDC nonce and PKCE, a persistent
host UUID, `agent_name_hint=nodwyn`, and the documented resource and scopes.
The callback must return an issued client id for registration; later login
retains that id and refuses a different one. The token exchange uses the
same resource, verifier and exact redirect URI and takes no client secret.

The keychain verifies the ID token using RS256 or ES256 and the issuer's
JWKS, checks issuer, audience, expiry, nonce and subject, and requires
`resource.invoke` and `chatgpt.tokens.use.direct` in the returned scopes.
Returning login must verify the same subject before replacing credentials.
JWKS are fetched on each sign-in, so no stale cached key prevents rotation.
Discovery and JWKS use bounded, non-redirecting requests to the fixed
issuer; its published revocation endpoint must belong to that issuer too.
No project file selects an authentication endpoint. The person authorizes
in the browser and writes their user configuration and binding rows; neither
a repository nor a model can initiate this flow or select another token host.

The stable host UUID is written inside the keychain before the first
attempt and persists even when consent fails. The person who owns that
keychain can replace it, as they can replace their credentials; it is an
opaque identifier, not authority. This uses format version 3, so older
binaries refuse the file rather than discard the host while rewriting it.
Credentials retain their issued client and verified subject in the encrypted
token set. Optional retained ID-token/login hints and an account picker are
not built: returning login uses OpenAI's account selector, and distinct
credential classes select separate accounts. Raw ID tokens are verified
and discarded, keeping them out of URLs exposed by the CLI and RPC.

Bindings still authorize every use. A plan credential's injection is also
restricted to bearer headers at public `GET https://api.openai.com/v1/models`
and `POST https://api.openai.com/v1/responses`. It cannot be injected into a
query, body, environment, another host or ChatGPT backend endpoint. Users
explicitly choose ChatGPT plan authentication in Settings › Models, connect
an account or select a stored one, then save the provider. The local-only
provider-write RPC accepts an unshadowed OpenAI credential id instead of an
API key, validates the fixed public address and Responses dialect, and saves
its class with exact-host model bindings. Switching to API key authentication
stores a separate key and preserves the OAuth account. Terminal setup still
uses the printed config and bindings blocks.
Model lists recognise visible plan model slugs. Requests ask for streaming
and no server-side storage. The Responses client buffers through the terminal
event before emitting output, preserving whole-output redaction (ADR 25);
missing or failed terminal events fail the request. Compatible servers that
answer JSON are still accepted. Incomplete terminal events retain the
existing length/stop-reason semantics.

Refresh remains serialized and rotates tokens atomically, using the saved
issued client id and resource. A sign-in must return a refresh token and both
plan scopes, but a refresh may omit either, as RFC 6749 section 6 allows, and
OpenAI's documentation does not promise them: the stored refresh token and the
scopes the sign-in verified then stay. A refresh that names scopes without
both plan scopes still sends the credential back to sign-in. Refusing an
omission would mark a working credential for a new sign-in over a field the
provider need not send. The loopback callback keeps what it compares with from
before the listener starts, so a repeated request to it, a reloaded tab, never
reads the client id the first one's exchange has since replaced. Model probes
now check granted scopes and refresh before injection, like session requests.
OpenAI decides eligibility, model entitlements and quota; there is no fallback
to an API key. Existing credential revoke uses the discovered endpoint; local
removal makes no promise of remote revocation. Multiple classes, not an
implicit account switch, are the way to keep several plan accounts configured.

Tests use a synthetic issuer with signed ID tokens and a transport that
refuses all unexpected addresses, and the existing OAuth fake for model
probes. They exercise registration, restart, reauthorization, failed identity
and permission checks, rotation, refresh responses that omit or narrow what
they name, a repeated callback (under the race detector), model listing,
inference, restricted sinks and revocation. Browser checks exercise a
client-id-free OpenAI authorization request with a synthetic refusal and the
existing local-provider success flow. Live account eligibility and quota are
not tested.
