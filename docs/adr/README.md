# Architecture decision records

One decision per file. Amend by editing and noting the change; never delete.

- [0042](./0042-gitea-releases.md) — Gitea distributes binaries and books; GitHub CI is manual-only
- [0044](./0044-wails-desktop.md) — Wails owns the native window while console RPC keeps its origin and authentication

- [0001](./0001-value-representation.md) — Value representation
- [0002](./0002-log-format.md) — Log format and event kinds
- [0003](./0003-tool-calls-are-scheme-applications.md) — Model tool calls are Scheme applications
- [0004](./0004-prim-metadata.md) — Primitive metadata
- [0005](./0005-keychain-crypto.md) — Keychain crypto (amended: a keychain may have no passphrase; credentials managed in the console)
- [0006](./0006-unbound-egress-default.md) — Unbound egress defaults to deny
- [0007](./0007-scheme-scope.md) — Scheme scope: written from scratch, documented first
- [0008](./0008-llm-provider-ollama-first.md) — LLM client is a Provider interface, Ollama first
- [0009](./0009-context-and-cache-discipline.md) — Context and cache discipline
- [0010](./0010-core-frontend-boundary.md) — Core/front-end boundary
- [0011](./0011-terminal-stack.md) — Terminal stack
- [0012](./0012-two-binaries.md) — Two binaries, always
- [0013](./0013-promotion.md) — Promotion
- [0014](./0014-init-scm-convention.md) — `.nodwyn/init.scm` convention
- [0015](./0015-friction-log.md) — Friction log
- [0016](./0016-fallback-is-allowed.md) — Fallback is allowed and honest
- [0017](./0017-standing-command-approvals.md) — Standing command approvals
- [0018](./0018-turn-budgets-and-long-runs.md) — Turn budgets and long-running turns (implemented, unattended runs and judged approval included; amended by 0035)
- [0019](./0019-jev-sees-content-never-secrets.md) — Jev sees content, never secrets (amends SPEC; amended: what the judge is told about a call; what needs a plan, a chat's earlier messages, and looking)
- [0020](./0020-jev-egress-primitives-register-check.md) — Jev as egress primitives; register-check (amends plan P5; implemented, amended for the judge tools and judged approval)
- [0021](./0021-ui-after-v0.md) — A real UI after v0: daemon, protocol, clients (implemented)
- [0022](./0022-cowork-workspaces-attachments-mcp.md) — Cowork: workspaces, attachments, MCP servers (implemented; amended: a chat-completions model takes images unless it says not)
- [0023](./0023-untrusted-mcp-servers.md) — Untrusted MCP servers: isolation, pinning, fail-closed (implemented: remote servers, and contained ones in podman or docker)
- [0024](./0024-oauth2-credentials.md) — OAuth2 credentials: acquisition, refresh, scopes (implemented, from the console too; W5 builds on it)
- [0025](./0025-ingress-redaction.md) — Log-derived ingress redaction and buffered model output
- [0026](./0026-the-loop-is-scheme.md) — The agent loop is a Scheme procedure
- [0027](./0027-models-and-narrowed-contexts.md) — Configured models and narrowed contexts
- [0028](./0028-scratch-directory.md) — A scratch directory beside the repository (amended: in the data directory, excepted from the denylist)
- [0029](./0029-optional-tool-parameters.md) — Optional tool parameters
- [0030](./0030-one-app-three-ways-to-reach-it.md) — One app, three ways to reach it: desktop, browser, phone
- [0031](./0031-renamed-to-nodwyn.md) — The project is renamed nodwyn
- [0032](./0032-working-habits.md) — The default loop works the way pil did
- [0033](./0033-go-126-base.md) — Go 1.26 is the base
- [0034](./0034-sessions-in-git-worktrees.md) — Sessions in git worktrees (partly implemented: a session in a worktree from the console)
- [0035](./0035-chat-is-the-way-to-work.md) — A chat is the way to work, and it asks less (amends 17, 18, 30)
- [0036](./0036-agents-md-in-the-system-prompt.md) — A repository's AGENTS.md joins the system prompt
- [0037](./0037-validate-before-done.md) — Work on nodwyn is validated before it is called done
- [0038](./0038-several-edits-in-one-call.md) — fs-edit takes several edits to one file in one call
- [0039](./0039-named-archived-deleted-tasks.md) — Tasks are named, archived and deleted, and listed by folder (amends 2)
- [0040](./0040-a-conversations-model-is-a-choice.md) — A conversation's model is a choice
- [0041](./0041-model-providers-and-efforts.md) — Providers, models and efforts are chosen in the console (amends 8, 40; amended: a model's settings are its own; its list is asked with its key and its reply cap comes out of its context; what providers publish fills what an entry leaves unset; the judge's model settings sit with the models'; Anthropic and Baseten are providers to choose)
- [0043](./0043-fs-edit-works-on-the-text-the-model-reads.md) — fs-edit works on the text the model reads, so a line with a credential on it can be edited (amends 25, 38)

- [0045](./0045-project-trust-and-security-boundaries.md) — User-owned project trust, executable identity, credential transport settings, keychain bounds and persistence, and canonical project paths
- [0046](./0046-the-judge-is-a-providers.md) — The judge is a provider's: Jev or OpenAI's Decisions API, chosen in Settings, with `[jev]` become `[judge]` and one set of judge primitives, `judge-yes-no`, `judge-score` and `judge-choice` (amends 19, 20, 41)
- [0047](./0047-a-tasks-judge-is-a-choice.md) — A task's judge is a choice beside its model, a ChatGPT plan's sign-in is no judge, its model is not typed, and Settings is tabs with one for the judge (amends 30, 41, 46)
- [0048](./0048-experimental-terminal.md) — A terminal for the model, experimental and unguarded: `term-open` and five more primitives, on a pseudo-terminal and an emulator, behind `[experimental] terminal`; temporary, to measure what it is worth before making it safe
