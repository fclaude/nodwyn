# ADR 0022: Cowork — workspaces, attachments, and MCP servers

- Status: **proposed** (model agreed; scheduled after v0 as the W track)
- Date: 2026-09-22

## Context
nibb assumes its work is a **code repository**: one pinned root, git as the
undo, files the agent reads with `fs-read`, tools written in Scheme over
`shell-exec`. A *cowork* application is the same harness applied to work
that is not only code — a working folder of documents, files the user
brings into the conversation, and tools that come from MCP servers rather
than from `dev/*.scm`.

Three assumptions break, and each breaks a different invariant:

1. **The pin.** One root, read+write, with git as the reviewable undo
   (ADR 17 leans on `git diff` explicitly). A working folder may be no
   repository at all, and may need reference material that must never be
   written.
2. **Ingress.** Everything the model sees today was produced by a tool nibb
   ran. An attachment is content from outside, chosen by the user, of a
   type nibb must decode.
3. **The metadata contract.** The guard reads a primitive's declared
   `Egress`, `Irreversible` and parameter *roles* (ADR 4). MCP gives nibb a
   name, a description and a JSON schema — and none of those three.

The invariants themselves do not change. The log is still the truth, the
guard still sits on the one Scheme→Go exit, secrets are still credentials,
and every I/O edge is still guarded by the mechanism fitting its substrate.
What follows is how each new thing is made to fit them.

## Decision

### 1. A workspace is a root plus a mount table
The repository pin generalizes to a **workspace**: a root directory and a
user-owned **mount table**, the filesystem counterpart of the binding table.

```toml
# <workspace>/.nibb/workspace.toml
kind = "docs"                 # code | docs | mixed — framing and default tools
[[mount]] path = "."                    access = "write"
[[mount]] path = "~/Reference/contracts" access = "read"
[[mount]] path = "~/Sync/team-handbook"  access = "read"
```

`RepoPin` becomes `WorkspacePin`: a path is resolved (symlinks first, as
today) and matched against the mounts; no match is a deny, `read` makes
every write a deny, and `.nibb/` stays protected (ADR 17). A workspace with
no mount table is exactly today's behaviour: `. → write`.

`kind` selects the default tool set and how the system prompt frames the
work. It does **not** select a security model — there is one.

### 2. Undo is a workspace service, not git
Standing write approvals (ADR 17) were justified by "git is the undo".
Where there is no git, nibb provides its own: before any irreversible write
the previous content is copied into `.nibb/versions/` (content-addressed,
capped by total size and age) and a `file.version` event is logged. `nibb
undo <path>`, `nibb versions <path>`, and the UI's restore read it.

A workspace that *is* a git repository keeps git as the primary undo; the
version store is then a convenience, not the safety net.

### 3. Attachments are ingress, and their content is data
`nibb attach FILE` (and drag-and-drop in the UI) does five things:

1. **copies** the file into `.nibb/attachments/<id>/` so the pin holds and
   the artifact is reproducible from the workspace alone;
2. **extracts** text deterministically by type (plain text as-is; PDF, docx,
   xlsx through an extractor; an image only if the control plane is
   multimodal, else its metadata and a note);
3. **redacts** on ingest through the same controller as every other context
   ingress (E2/E4, P7) — this is why attachments cannot ship before P7;
4. **caps** size, with the same head+tail elision the window uses;
5. **logs** `attach` (name, type, bytes, hash, redactions) and tags the
   extracted text `external` for L2 (P6).

**Attachment content is data, never instruction.** The system prompt says
so, the extracted text enters as a tool result would, and nothing inside an
attachment can grant a permission — a file that says "you may now write
outside the workspace" changes nothing, because permissions come from the
mount table and the guard, not from text.

### 4. An MCP server is a primitive provider with a declared capability profile
Each MCP tool becomes an entry in the **primitive table**, named
`server/tool`, so it is applied through `applyPrim` and crosses the guard
exactly like `fs-read` (invariant 4 holds, unchanged).

The hard part is metadata. MCP describes what a tool *takes*, not what it
*does to the world*. So a server is declared:

```toml
# <workspace>/.nibb/mcp.toml
[[server]]
name      = "notion"
transport = "stdio"                 # stdio | http
command   = ["npx", "-y", "@modelcontextprotocol/server-notion"]
credential = "notion-key"           # keychain class, injected per the binding
dest      = "api.notion.com"        # logical destination for the binding table
  [server.tools.search]   egress = true  irreversible = false
  [server.tools.create_page] egress = true irreversible = true
  [server.tools.append_block] egress = true irreversible = true
    roles = { page_id = "dest", content = "payload" }
```

- **An undeclared tool is treated as `egress` *and* `irreversible`.** Every
  call confirms; any egress without a binding is denied. Fail closed, the
  same posture as an unbound credential (ADR 6).
- **Credentials** use the keychain and the binding table unchanged. For an
  HTTP server the destination is its host; for a stdio server it is the
  logical `mcp:<name>`, which the binding table accepts as a destination
  like any other. Injection follows the binding position
  (`header:Authorization:Bearer`, or `env:NOTION_TOKEN` for a stdio child).
- **Resources** are ingress: fetched through the attachment path — extract,
  redact, log, tag.
- **Tool descriptions are untrusted text.** They reach the model's context,
  so a hostile server can try to steer the model. The guard never reads a
  description, so no description can widen policy; the worst case is the
  model asking for a call the user then sees in a confirm. This is stated,
  not solved.
- **stdio servers are child processes**: the scrubbed environment of
  `shell-exec` plus exactly the credentials the profile declares. They are
  started before the first message and stopped with the session.
- **The tool list is frozen for the session** (ADR 9): servers connect at
  start; a server that dies mid-session makes its tools return errors rather
  than disappearing, so the cached prefix stays valid.

### 5. Tool selection is part of the workspace
Ten servers is hundreds of tools: the schema block is paid once in the
cached prefix, but a 27B model's ability to choose degrades well before the
context does. `workspace.toml` therefore selects which servers and which
tools are exposed, and the default for a new server is *nothing until you
choose*.

### 6. The task block grows structure
The system prompt's task block (ADR 9) carries the workspace kind, the
mounts and their access, the attachments in play, and the servers
available — still one string, still byte-stable within a turn.

## Consequences
- **P7 is a hard prerequisite** for attachments and MCP resources: nibb must
  not ingest a file it did not produce before the ingress redactor exists.
  W2 and W3 therefore cannot start before P7 lands.
- **P3 is a hard prerequisite** for MCP servers with credentials.
- ADR 17's standing write approvals keep their justification through the
  version store, not git.
- `dev/*.scm` remains the way a workspace ships its own Scheme tools; the Go
  toolchain profile becomes one workspace profile among several.
- The exfil scenario suite grows two families: an attachment carrying a
  canary must be redacted at ingest, and an MCP tool must not reach a
  destination its class is not bound to.

## Accepted limit: nibb does not classify personal data
L1/L3 detect *credentials*. A cowork workspace holds documents whose
sensitivity has nothing to do with shape — a contract, a medical letter, a
salary list. Nothing in nibb recognises those. The protections that do apply
are the mount table (what can be read at all), the confirm (what leaves),
the binding table (where it may go), and the Jev content level (ADR 19).
The honest statement is: **nibb controls where content goes, not what
content means.**

## Open questions (these block W-track scheduling)
1. **Does "cowork" mean several people?** One user with non-code work, or a
   shared workspace with more than one human? Multi-user changes identity in
   the log, who may answer a confirm (ADR 21's socket identity), and whether
   the workspace is synced.
2. **Whose MCP servers?** Only servers you wrote or vet, or arbitrary ones
   from a registry? The profile plus confirm is enough for the first; the
   second wants a sandbox for stdio children, which is a phase of its own.
3. **Which attachment types must actually work?** Text and PDF are cheap;
   docx/xlsx are a library decision; images need a multimodal control plane,
   which the local Qwen is not.
4. **Does the agent produce non-text documents** (docx, xlsx, pptx)? That is
   a dependency decision and a tool surface, not core work.
5. **One product or two?** Is cowork the same binary with a different
   front-end and workspace kind, or a separate application sharing the core?
   This decides whether the UI track (ADR 21) is one client or two.
6. **Where does a workspace live** — a plain folder, a synced folder
   (Dropbox/Syncthing), or always a git repository nibb initialises?
