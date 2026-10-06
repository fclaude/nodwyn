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
