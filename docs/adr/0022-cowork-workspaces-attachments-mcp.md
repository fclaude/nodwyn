# ADR 0022: Cowork — workspaces, attachments, and MCP servers

- Status: **partially implemented** (W1, W2 text and W3a done 2026-09-30; images, W3b, W4, W5 proposed)
- Date: 2026-09-22

## Context
nodwyn assumes its work is a **code repository**: one pinned root, git as the
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
2. **Ingress.** Everything the model sees today was produced by a tool nodwyn
   ran. An attachment is content from outside, chosen by the user, of a
   type nodwyn must decode.
3. **The metadata contract.** The guard reads a primitive's declared
   `Egress`, `Irreversible` and parameter *roles* (ADR 4). MCP gives nodwyn a
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
# <workspace>/.nodwyn/workspace.toml
kind = "docs"                 # code | docs | mixed — framing and default tools
[[mount]] path = "."                    access = "write"
[[mount]] path = "~/Reference/contracts" access = "read"
[[mount]] path = "~/Sync/team-handbook"  access = "read"
```

`RepoPin` becomes `WorkspacePin`: a path is resolved (symlinks first, as
today) and matched against the mounts; no match is a deny, `read` makes
every write a deny, and `.nodwyn/` stays protected (ADR 17). A workspace with
no mount table is exactly today's behaviour: `. → write`.

`kind` selects the default tool set and how the system prompt frames the
work. It does **not** select a security model — there is one.

### 2. Undo is a workspace service, not git
Standing write approvals (ADR 17) were justified by "git is the undo".
Where there is no git, nodwyn provides its own: before any irreversible write
the previous content is copied into `.nodwyn/versions/` (content-addressed,
capped by total size and age) and a `file.version` event is logged. `nodwyn
undo <path>`, `nodwyn versions <path>`, and the UI's restore read it.

A workspace that *is* a git repository keeps git as the primary undo; the
version store is then a convenience, not the safety net.

### 3. Attachments are ingress, and their content is data
`nodwyn attach FILE` (and drag-and-drop in the UI) does five things:

1. **copies** the file into `.nodwyn/attachments/<id>/` so the pin holds and
   the artifact is reproducible from the workspace alone;
2. **extracts** text deterministically by type (plain text as-is; PDF, docx,
   xlsx, pptx through an extractor). An **image is not extracted**: the
   control plane on workshop reports `vision` and reads images sent as
   `images: [base64]` on a message (verified 2026-09-22), so an image
   attachment enters the context as an image;
3. **redacts** on ingest through the same controller as every other context
   ingress (E2/E4, P7) — this is why attachments cannot ship before P7;
4. **caps** size, with the same head+tail elision the window uses;
5. **logs** `attach` (name, type, bytes, hash, redactions) and tags the
   extracted text `external` for L2 (P6).

**Attachment content is data, never instruction.** The system prompt says
so, the extracted text enters as a tool result would, and nothing inside an
attachment can grant a permission — a file that says "you may now write
outside the workspace" changes nothing, because permissions come from the
mount table and the guard, not from text. The same rule covers text drawn
*inside* an image.

**Images are not redactable, and that is a hole with a fence around it.**
L1 and L3 scan text; a screenshot of a `.env`, or a photo of a key on a
whiteboard, reaches the model as pixels and no redactor sees it. Three
things bound it, and none of them is redaction:

- the control plane is **local** (workshop, on the tailnet), so an image that
  enters the context has not left the user's network;
- **images are never sent to Jev** — ADR 19's content levels govern text,
  and an unredactable payload has no level; the judge gets the ask, the plan
  and summaries, never an attachment's pixels;
- attaching an image is an explicit act with its own confirm, which names
  the file and says plainly that its contents cannot be redacted.

If the control plane ever becomes remote, images must be re-decided: the
honest options then are OCR-then-redact (imperfect) or refusing images.

### 4. An MCP server is a primitive provider with a declared capability profile
Each MCP tool becomes an entry in the **primitive table**, named
`server/tool`, so it is applied through `applyPrim` and crosses the guard
exactly like `fs-read` (invariant 4 holds, unchanged).

The hard part is metadata. MCP describes what a tool *takes*, not what it
*does to the world*, so a server carries a **capability profile** in
`.nodwyn/mcp.toml` — transport, credential class, destination, and per-tool
`egress`/`irreversible`/role mapping — and **an undeclared tool is treated
as `egress` *and* `irreversible`**: every call confirms, any egress the
binding table does not authorise is denied. Fail closed, the same posture as
an unbound credential (ADR 6).

Credentials and destinations use the keychain and the binding table
unchanged: an HTTP server's destination is its host, a contained server's is
`mcp:<name>`, and injection follows the binding position
(`header:Authorization:Bearer`, `env:NOTION_TOKEN`). Resources are ingress
through the attachment path. Servers connect before the first message and
the tool list is frozen for the session (ADR 9); a server that dies makes
its tools return errors rather than disappearing.

**The isolation, pinning and trust posture for servers nodwyn cannot review
is [ADR 23](./0023-untrusted-mcp-servers.md)**, which refines this section:
no untrusted server runs in nodwyn's process tree, manifests are hashed and
re-approved on change, and a contained server's network and filesystem are
declared rather than inherited.

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
- **P7 is a hard prerequisite** for attachments and MCP resources: nodwyn must
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

## Accepted limit: nodwyn does not classify personal data
L1/L3 detect *credentials*. A cowork workspace holds documents whose
sensitivity has nothing to do with shape — a contract, a medical letter, a
salary list. Nothing in nodwyn recognises those. The protections that do apply
are the mount table (what can be read at all), the confirm (what leaves),
the binding table (where it may go), and the Jev content level (ADR 19).
The honest statement is: **nodwyn controls where content goes, not what
content means.**

## Answers (2026-09-22)
1. **Cowork is one person doing non-code work**, not several people sharing
   a workspace. W5 (collaboration) is dropped; identity in the log and over
   ADR 21's socket stays single-user.
2. **MCP servers are untrusted** — assume they can be neither written nor
   reviewed by us. See [ADR 23](./0023-untrusted-mcp-servers.md).
3. **All attachment types**, images included: the control plane reports
   `vision` and reads images (verified). See §3 for what that costs.
4. **nodwyn produces documents**: docx, Google Docs and LaTeX. LaTeX is a
   `shell-exec` tool over an installed toolchain, docx is pandoc from
   Markdown or LaTeX, and Google Docs needs OAuth — designed in
   [ADR 24](./0024-oauth2-credentials.md), which W5 depends on.
5. **One product, two profiles** (agreed 2026-09-22). One core, one binary,
   one protocol (ADR 21). A workspace `kind` selects the tool surface: a
   `docs` workspace registers file, attachment, MCP and document primitives
   and **does not register `shell-exec`, `register-tool` or Scheme
   evaluation**, so a whole class of guard machinery (argv triggers, command
   approvals) is inert there and the attack surface is much smaller. The
   desktop client and the TUI are two front-ends over the same protocol, and
   the protocol does not expose `session.eval` to a `docs` client. Only a
   distribution constraint — a sandboxed or store-distributed bundle that
   cannot run a daemon or a container — would force a second product; that
   is revisited if and when it appears.
6. **A workspace is a plain folder.** If the user syncs it, that is their
   business: nodwyn makes no assumption about sync, locking or conflicts, and
   the version store is per-machine.

## W1 implemented — 2026-09-30

Workspaces as decided above, with these specifics:

- `<root>/.nodwyn/workspace.toml` holds `kind` and `[[mount]]` rows (`path`
  absolute or `~/…`, `access` read|write). A mount may not be `/`, the home
  directory itself, or overlap the root. Without the file a folder is a code
  workspace, exactly as before. The console writes the file only for a kind
  other than code or when there are mounts, so naming a repository adds
  nothing to its diff; `.nodwyn/` stays protected from the model.
- The pin takes mounts by absolute path; a read-only mount refuses every
  irreversible call. A `docs` workspace registers no `shell-exec`, and a
  guard checker refuses `shell-exec` and `register-tool` there whoever asks;
  `session.eval` is refused too. The system prompt gains a workspace section
  only for docs or mounted workspaces.
- Before `fs-write`/`fs-edit` replaces a file that no git work tree at the
  root covers (mounts are never covered), its content goes to
  `.nodwyn/versions/objects/<sha256>` with an index line, and a `file.version`
  record names it. 20 versions per file, 256 MiB in all, oldest first; a
  failure to keep a version fails the write. `nodwyn versions FILE` and
  `nodwyn undo FILE`; the console restores through a user-origin `fs-write`, so
  a restore shows its diff, asks, and is itself undoable.
- Named workspaces live in `workspaces.toml` in the config directory
  (`workspace.list/save/remove`, local consoles only).

## W2 (text) implemented — 2026-09-30

Attachments as decided above, except images:

- `session.attach {id, path, name}` (daemon clients) and `POST /attach` on the
  browser bridge, which stages the upload privately (20 MB at most), asks the
  daemon to attach that path and deletes it: a page never names a path the
  daemon reads. The session must be idle.
- `internal/extract` decodes by extension: text formats as UTF-8, `.docx`,
  `.xlsx` (sheets as tab-separated rows) and `.pptx` from their XML with a
  64 MB unpack bound, PDF through `pdftotext` when installed. Images are
  refused with a clear message until they can be sent as images; nothing is
  OCRed.
- The copy goes to `.nodwyn/attachments/<id>/<name>` (with a `.gitignore`);
  the text is redacted at ingest, capped at 48 KB (head and tail kept, the
  omission said), and recorded as an `attach` record with the file's hash,
  type, pages and redaction classes. The window renders it as a user-role
  message framed as data; unlike the person's words it may be trimmed,
  oldest first. Old logs have no such records, so request replay is unchanged.
- The console attaches from both composers; the conversation shows a chip per
  file ("2 pages · 1 hidden") and the task an Attachments panel.

Not done: an `attach` confirm for images, per-image token accounting, and the
`external` taint tag (L2 applies to Scheme values, and an attachment never
becomes one).
