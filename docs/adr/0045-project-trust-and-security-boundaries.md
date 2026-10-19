# ADR 0045: User-owned project trust and bounded security boundaries

- Status: accepted
- Date: 2026-10-06

## Context

The October review reproduced startup code authorising itself through a cloned
`allow.toml`, a repository executable inheriting a system command's basename
approval, a bound credential reaching remote HTTP, and an invalid KDF header
panicking before authentication. macOS path aliases separated project settings
and notes. Keychain saves used a predictable temporary name without syncs.

## Decision

1. Repository authors write `.nodwyn/init.scm` and `.nodwyn/allow.toml`; committing
   them does not grant authority. Ordinary startup skips both until the person
   reviews them with `nodwyn trust --repo PATH` and accepts the displayed revision
   with `--accept REVISION`. `--revoke` removes that trust. The host keeps the
   canonical project path and a SHA-256 revision of both files, including absence,
   in `trusted-projects.json` in the user configuration directory. Guarded file tools cannot write this registry, and no Scheme primitive grants trust. The checked bytes are the loaded bytes.
2. Changes to either authority file require another review at startup, resume or
   reload. A managed worktree inherits its main project's trust only for identical
   authority files. Revocation does not undo a running interpreter or its grants:
   close/restart tasks to remove those. A Go embedder's explicit `Options.InitFile`
   remains direct host authorisation of that chosen file. The optimiser keeps an
   edited project init pending review; it does not execute it or restore it merely
   because trust is pending. Other reload failures retain the existing rollback.
3. Trust is a decision about the project's code, not a sandbox or a recursive
   signature. Review Scheme files and scripts the init loads or invokes too. Their
   changes are not fingerprinted. Init still runs with user origin through the
   guard, with confirms denied before a frontend is listening; a standing approval
   can permit a host action at startup. Do not describe init as unable to act.
4. Command approvals bind argv prefixes to resolved executable paths at approval
   time. A different executable with the same basename does not inherit them.
   Shell wrappers and implicit pipeline filters must resolve to the host tools;
   a bare name found inside the repository receives no implicit system approval.
   Explicit repository executable paths may be approved separately. This does not
   hash executable contents or sandbox an approved tool's behaviour.
5. Credential injection requires HTTPS except on localhost or a literal loopback
   address. `policy.allow_insecure_http`, a user-owned boolean defaulting to false,
   permits remote cleartext HTTP when deliberately enabled, in config.toml or
   Settings > Approvals > Credentials over remote HTTP. Scheme cannot set it.
   New/resumed tasks take the setting; provider setup, tests and model lists use the
   daemon's current setting. Bindings, redirects, TLS certificate verification,
   and the special ChatGPT credential destinations still apply. This exception
   does not relax the separate MCP, OAuth endpoint or Google upload URL rules.
6. Before key derivation, keychain input is a regular file of at most 16 MiB and
   KDF parameters must satisfy time 1–10, threads 1–16, memory at least 8 KiB per
   thread and at most 256 MiB, and time × memory at most 512 MiB. Existing default
   and test parameter sets are unchanged. Saves use exclusive random sibling
   files (0600), sync content, rename, then sync the parent directory. Failed
   local entry and passphrase changes before publication restore in-memory state;
   a failure after rename reports uncertain durability without reverting the
   published key. OAuth refresh keeps a received token in memory if saving fails
   and reports the error, since the provider may already have rotated that token.
7. Project identity is an absolute path with existing symlinks resolved. Missing
   suffixes use their resolved ancestor. Workspaces, MCP selections, session
   summaries, resume checks, notes and kept-version lookups share that identity.
   Old session logs and version indexes are read compatibly, without rewriting
   history. Old notes are copied to the canonical key with a backup of the source;
   conflicting notes stop migration for manual reconciliation. Duplicate workspace
   aliases are rejected for reconciliation rather than merging their permissions.

## Validation

Regression tests exercise untrusted startup, executable impersonation, stale and
revoked trust, CLI review/accept/revoke, transport refusal and its explicit
exception, invalid KDF headers, planted temporary symlinks, project aliases and
note migration. The console check saves the transport setting on and off and
reloads to verify each value. Existing strategy fixtures authorise their init
explicitly through the Go host. All checks use fake services and credentials.

The implementation preserves the log format, Scheme primitives, keychain file
format and default KDF. Trust intentionally changes whether repository code and
policy load; it is not silently granted during migration.

## Amendment 2026-10-16: the denylist judges the file a path reaches

`PathDenylist` matched its credential-file globs against the name a call
wrote and followed links only for the protected directories. A link named
`pic.png` or `notes.md` that points at `.env` passed it: `fs-read` read the
key, and `doc-export` copied it into a document as an image (reproduced). It
also matched case-sensitively, while macOS's file system opens `.env` for
`.ENV` and `IsProtected` already ignores case. The globs are now matched,
ignoring case, against both the written name and the name of the file the
path resolves to. Naming a link to a credential file in `index-grep` is now a
refusal, as naming the file is. The regressions are
`TestDenylistFollowsLinksAndIgnoresCase` and the symlinked image in
`TestDocExportRunsPandocWithFixedArguments`.

## Amendment 2026-10-17: a device signed in with a passkey reviews and grants trust

Decision 1 left trust to `nodwyn trust` at the computer's terminal, and the
console offered no grant. On a computer nobody sits at (ADR 30, amended
2026-10-17) that meant an SSH session for every project. Two methods now do
what the command does, for one kind of console:

- `trust.read {repo}` returns the review `nodwyn trust` prints: whether the
  project has `.nodwyn/init.scm` or `allow.toml`, whether the record trusts
  their current revision, the revision, and both files' text whole. A pair
  with a file over 256 KiB is not shown, and `reviewable` is false: trust is a
  review of exactly what will run, so what cannot be read whole here is not
  offered for trust here.
- `trust.set {repo, revision}` grants the revision (empty revokes) through
  `config.TrustProject`, which recomputes it from the files and refuses unless
  they are still exactly what was read. The review is the page the person
  reads; the grant names that revision and nothing else.

Who may call them, and why this is no weaker than the terminal. The files'
author is whoever can commit to the repository, so a grant is code running
as the person. `trust.set` is the **passkey's alone**: the bridge offers it
only to a device whose person is signed in with a passkey and has touched
it, and not to this computer's own console, whose cookie the browser sends
to every server on loopback (ADR 30, amended 2026-10-16), a task's dev
server among them. A device paired by invitation is refused, by the bridge
and by the daemon. `trust.read` is offered to this computer's console and
the person's devices; the console says where an untrusted project is
reviewed (`nodwyn trust --repo PATH`) when it cannot grant. Both name a
folder nodwyn works in (a workspace or a task's root, and not one a task may
not use as root), as `versions.list` does. The record is the same file, in
the config directory the guard keeps from a task's tools, and no Scheme
primitive writes it. The console shows the files as text, never markup. A
grant is said in the console's log with the passkey that touched it. As
before, a running task keeps what it loaded: it must be started again.

The statement in `docs/protocol.md` and `docs/browser.md` that no RPC method
or console button grants trust is replaced accordingly. Not changed: trust
decides about the two files, not the code they load, as Decision 3 says.
Checks: `TestProjectTrustIsReviewedAndGrantedByRevision` (the revision read,
refused when stale or changed, a paired device and an unknown folder
refused, a large pair not shown), the bridge's tier test, and the `remote`
browser check, where a device that only answers is shown nothing, and the
laptop reads the files, trusts the revision it read and revokes after a
confirm.

## Amendment 2026-10-19: the passkey that grants trust is not made with this computer's cookie

The amendment above keeps `trust.set` from this computer's console, whose
cookie reaches every server on loopback. That cookie could still make an
invitation that registers a passkey, and a device it paired with one
granted trust (reproduced on 2026-10-19). An invitation that may register a
passkey now comes from `nodwyn devices invite` or from a device signed in
with a passkey after a touch, never from this computer's console (ADR 30,
amended 2026-10-19, which records the chain, the decision and the checks).
This computer's sign-in is no longer a cookie either (ADR 30, amended
2026-10-19 a second time). The grant stays the passkey's all the same, so that
no other leak of that sign-in, now or later, grants trust.
