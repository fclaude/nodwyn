# ADR 0022: Cowork — workspaces, attachments, and MCP servers

- Status: **implemented** (W1, W2 text and W3a 2026-09-30, W4 the same day in
  ADR 24, W2 images, W3b and W5 2026-10-01)
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

## Amendment 2026-09-30: docs is not a special case

The restriction in answer 5 (no `shell-exec`, no `register-tool`, no Scheme
evaluation in a `docs` workspace) was an elaboration recorded with the
answer to "one product or two?", not part of it. A self-extending agent
defines its own tools wherever it works, so it is dropped: every kind has
the same tools and the same guard, `session.eval` works in every
workspace, and the system prompt no longer describes a docs workspace as
one that runs no programs. The kind stays as a label the console shows.
Mounts, the pin and the version store are unchanged.

## Amendment 2026-10-01: images may go to a remote control model

§3 bounded unredactable images with three fences, the first being that the
control plane is local, and said that a remote control plane must
re-decide images: OCR-then-redact, or refuse them. The control model is now
whatever `[control]` names (ADR 27), often another machine or a provider.
Re-decided:

- **Images may go to a remote control model**, but only through the image's
  own confirm, and that confirm now carries the decision the local
  control plane used to make for the person: it names the file, says its
  pixels cannot be redacted, and names the destination, the control model
  and its host, saying whether that host is this machine (loopback) or
  another machine whose pixels leave this computer as they are.
- **Nothing is OCRed.** Reading text out of an image to redact it would be
  imperfect, and would make the model see something other than what the
  person chose to send.
- **An image goes only to the host it was confirmed for.** The attach
  record keeps that host; a request to any other host, another configured
  model included, carries a line saying the image is not for it instead.
  The confirm is the consent, so it is never stretched to a destination it
  did not name.
- Images still never go to the judge, and the confirm is a security
  confirm: no standing answer covers it, `--yolo` and a run's
  `auto_approve` never answer it yes, and `--no-confirm` declines it.

## W2 (images) implemented — 2026-10-01

Images as decided above:

- **Formats and caps** (`internal/extract/image.go`). PNG, JPEG, GIF and
  WebP, decided by content: a `.png` that is not a PNG is refused, and so is
  any other image format (HEIC, BMP, TIFF...) with a message saying to
  convert it. A file without an extension is an image when its content is
  one. At most 10 MB and 8000 pixels a side. The dimensions are read from
  the header (no pixel is decoded); the bytes that were checked are the
  bytes that are stored.
- **Vision.** An image is attached only when the control model takes
  images: ollama is asked (`/api/show` capabilities, `vision`), once per
  session; a chat-completions model says so in its configuration,
  `vision = true` (`config.Model.Vision`). Otherwise the attachment is
  refused with what to change.
- **The confirm** (`core.attachImage`). A `guard.Prompt` with `Security`,
  `Prim` "attach image" and the file name, asked through
  `Session.ConfirmApproval`, so it reaches every front-end as an ordinary
  `prompt.confirm`. `--no-confirm` declines it and `--yolo` declines it too
  (as `guard.AutoAllow` answers a security confirm); unanswered for ten
  minutes, or cancelled with `session.abort`, it is declined. A declined
  image leaves a `note` and no copy. While an attachment is in progress no
  turn starts, and closing the session cancels it. The daemon makes the
  attachment without its lifecycle lock, so the confirm can be answered
  from another connection.
- **The log holds no pixels.** The copy goes to
  `.nodwyn/attachments/<id>/<name>` as text attachments do; the `attach`
  record (type `image`) holds the media type, bytes, SHA-256, width,
  height, token estimate, the confirmed host (`destination`) and who
  answered the confirm.
- **The window** (`internal/window/image.go`) carries an image as a user
  message, a line framing it as data plus the image named by its digest
  (`llm.Image`, whose bytes are not part of the request's JSON). When a
  request is sent, the loop (`internal/loop/images.go`) reads each image
  from the store and checks its size and digest. An image whose copy is
  missing or changed, one the model asked does not take, or one going to
  another host than it was confirmed for, is replaced by a line saying
  which; the request's `llm.req` records each as `withheld_images`.
  Request replay hashes the digest, not the bytes, and applies the recorded
  reasons, so `nodwyn replay --window` verifies a request with images
  without reading a file (window recipe v12; older logs have no images and
  rebuild unchanged).
- **Token accounting.** The record holds an estimate the window counts
  instead of bytes/3: 85 tokens, plus 170 per 512-pixel tile of the image
  scaled to fit 2048×2048 and then to a shorter side of at most 768. It is
  the chat-completions tiling, an estimate for local models as bytes/3 is
  for text. An image is trimmed with its line, oldest attachment first.
- **Wire formats.** Ollama gets `images: [base64]` on the message; a
  chat-completions server gets the content as parts, the text and then an
  `image_url` data URL.
- **Not to the judge, nor into narrowed contexts.** Jev's primitives take
  strings, and the planner and sub-agents work in narrowed contexts that
  start after the attachment; tests hold both.
- **Front-ends.** Both console composers attach images; the upload waits
  for the confirm, which the task page shows (the home composer opens the
  task first). A chip reads "image · 1200×800 · ~1,105 tokens". The TUI and
  line mode show the confirm like any other.

Deviations from the plan: an image whose header does not give its
dimensions is refused as damaged rather than attached with a fixed
estimate (the fixed estimate, the formula's largest, 1,445, remains for a
record without dimensions), because a server that cannot read an image
fails every later request until it is trimmed. A person who wants an image
out of the conversation deletes its stored copy; later requests then say
it is missing.

## W5 implemented — 2026-10-01

Documents out, as answer 4 decided, with these specifics:

- **`(doc-export source target)`**, a host primitive in every workspace:
  Markdown (`.md`) or LaTeX (`.tex`) to `.docx`, `.odt` or `.html`, the
  target's extension picking the format, with pandoc 2.15 or later from
  `PATH` (missing, the error names what to install). pandoc runs twice with
  fixed arguments, no shell, the scrubbed environment, `TMPDIR` the scratch
  directory and `shell-exec`'s timeout. The first run, `--sandbox
  --from=F --to=json`, reads the source on stdin and nothing else, so an
  `\input`, an include or a remote image in it reads nothing. For a format
  that embeds images, Go then vets the document tree: an image that is a
  file in the workspace, not credential-shaped, is kept by its absolute
  path; any other is replaced by its description. An HTML page keeps its
  images by reference, except links elsewhere, which a browser would load
  when the page is opened. In every format, raw content (RawBlock,
  RawInline: OpenXML, HTML, LaTeX written into the source) becomes literal
  code, so the output cannot carry a field, a frame or a script that
  fetches something when it is opened. The second run,
  `--from=json --to=T --standalone --output=-`, writes the document, and Go
  writes the target.
- **Why the second run has no sandbox.** A pandoc built without its data
  files embedded, as Fedora's and Debian's are, reads them from disk to
  write a `.docx`, and `--sandbox` refuses that (verified with Fedora's
  pandoc 3.7: "Could not find data file"). The writer needs no sandbox: the
  only files it reads for a tree are the images it embeds, which Go chose.
- **A new parameter role, `source`** (ADR 4, amended): a file the primitive
  only reads. Pinned and denylisted like a path, but a read-only mount may
  supply it to an irreversible primitive. `doc-export`'s target is a
  `path`, so it is a write like `fs-write`'s: a confirm with a preview
  (source → target, formats, created or replaced), standing write approvals
  and turn trust, protected paths, and the version store.
- **No PDF.** A TeX engine reads any file and runs programs when a document
  asks, and pandoc's sandbox does not cover PDF output. LaTeX to PDF stays
  `shell-exec` running `latexmk`, with a standing approval if the person
  gives one, as this repository builds its book.
- **`(google-doc-upload path [title])`**, offered only to a session that
  starts with a credential of the class `[docs] google_credential` names
  (default `google`) stored. One multipart upload to
  `[docs] google_upload_url` (default
  `https://www.googleapis.com/upload/drive/v3/files`) asking for
  `application/vnd.google-apps.document`, which works with the narrow
  `drive.file` scope; `.docx`, `.odt`, `.html` or `.txt`, at most 5 MiB.
  Returns the document's id and link. As for an MCP server's key, the
  credential is Go's: `Meta.EgressClass` is the class, the destination the
  configured endpoint's host, `POST` at `header:Authorization:Bearer`, so
  E3 decides it from the binding table (and its scopes) before anything is
  asked. Every call confirms, with no standing answer, naming the file, its
  size and the destination. (Amended 2026-10-02: how an upload is handled
  is the person's setting, `[docs] google_upload`. With `ask` every call is
  a security confirm, which `--yolo`, `--judge` and unattended runs never
  answer yes. With `allow` no call asks, but E3 and the scan below still
  decide. With `deny` the guard refuses the call and later sessions are not
  offered the tool. While it is unset, the first call that would confirm
  asks which of the three, saves the answer in `config.toml` and records
  `setting.change`. `allow` can be chosen only on this computer, not from a
  paired device.) The token is injected through the session's
  sink, so an OAuth2 token is refreshed first and `cred.refresh` logged.
  The configured URL is validated (https, or http on loopback, for tests);
  the session's client refuses redirects.
- **The bytes are scanned in the handler.** They never become a Scheme
  value, so no checker sees them. `extract.AllText` reads every text the
  file carries — a text file as it is; every XML part of a `.docx` or
  `.odt`, as markup and as character data joined by paragraph, so a key
  split across runs is whole — and the session judges it as the guard's L1
  checker judges an egress payload (`guard.TextSecret.Outgoing`): a known
  secret shape whose class is not bound for the host refuses the upload
  before anything is sent. Images and fonts in a package are not read (the
  limit §3 accepts for attachments); a package holding anything else, such
  as an embedded file, is refused.
- **`doc.upload`** (ADR 2, amended) records each upload: path, media type,
  size, sha256, host, class, sent, ok, the document's id and link or the
  error. Never content, never a token.

The tests use fakes only: a fake pandoc written into a temporary directory
(its arguments and input checked), a fake Drive upload endpoint
(`test/fakes/drive`) and the fake OAuth2 provider. A test with the real
pandoc runs when one is installed and skips otherwise.

Not done: Google Docs beyond creating one (updating a document, folders,
sharing), a resumable upload for files over 5 MiB, `.odt` attachments
(`internal/extract` reads an `.odt` only for this scan), and Markdown
straight to Drive (it goes through `doc-export` first).

## Amendment 2026-10-03: no kind, and the mounts are the person's, not the folder's

Since the amendment of 2026-09-30 the kind (`code`, `docs`, `mixed`) changed
nothing, yet the console still asked what a folder holds, and any answer
but Code wrote `.nodwyn/workspace.toml` into the folder. The kind is
dropped: the console asks nothing about it, `workspace.save`,
`workspace.list` and session details carry none, and `(workspace)` reports
only the mounts.

The mount table moves out of the folder. §1 calls it user-owned, but
`.nodwyn/workspace.toml` is written by whoever commits to the folder, a
stranger's clone included, and its mounts went straight into the pin: a
repository could mount a folder outside itself, for reading or writing,
and a session there used it without a confirm. A test reproduced it with
every confirm answered no. Mounts are now kept with the workspace's name
in `workspaces.toml` in the config directory (`[[workspace.mount]]`, `path`
and `access`). Who writes that file: the daemon, on a `workspace.save` from
a console on this computer (the bridge does not offer it to a paired
device), or the person by hand. The config directory is private to the
guard, so neither a model nor a repository can. The mounts are checked
again as a session reads them, so one written by hand is held to the same
rules. A session loads the mounts of the workspace named at its project's
folder (for a worktree, its repository's, ADR 34), and none for a folder
nobody named. A `.nodwyn/workspace.toml` in a folder is not read: mounts
kept there before are added again on Workspaces. Nothing about a workspace
is written into its folder, so naming one adds nothing to its diff.

## Amendment 2026-10-04: the folder the console picked last

The console remembers which folder the person picked last for a new task
(Home's "Work in" chips, a workspace's "New task here"), because the folder
a fresh task opens in starts there. It used to be kept in the browser only,
under the console's origin — and the app's window gets a console on a new
loopback port every launch (ADR 30), so a browser's own storage never
carried the pick across an app restart. The pick is now told to the daemon
too, by `workspace.active` (local consoles only, like `workspace.save`),
which keeps it in `last-workspace.toml` beside `workspaces.toml` in the
config directory. Who writes that file: the daemon, on a `workspace.active`
from a console on this computer, or the person by hand; the config directory
is private to the guard, so neither a model nor a repository can (ADR 22,
amended 2026-10-03). A tab that has picked keeps its own choice; a tab that
has not starts in the folder picked last — this browser's, else the
daemon's — and in the first folder of the list when neither has one. The
file names a folder, never a policy: it only preselects, and a task opened
there goes through the same root checks as any other.

## Amendment 2026-10-05: a chat-completions model takes images unless it says not

An image was refused at attach time on a chat-completions model whose
configuration did not say `vision = true`, and the way out was Settings, which
cost the person the message they had prepared around the image. A model is now
taken to read images unless its entry says `vision = false` (Settings ›
Models › Takes images: No; ADR 41, amended 2026-10-05, makes it the model's
own): a provider that cannot read one refuses that request, which costs a
failed request instead of the message, and the turn's advice says the model
may not take images and what to do (`llm.Hint`). `config.Model.Vision` is
unset, true or false; unset reads as true for a chat-completions model
(`Model.TakesImages`). Ollama is still asked (`/api/show`), and refuses the
attachment when it says the model takes none. Nothing else changes: every
image still asks its security confirm, names the host it goes to, and goes
only there.

## Amendment 2026-10-06: security review and project identity

[ADR 45](0045-project-trust-and-security-boundaries.md) supersedes the earlier
behaviour where it differs: repository startup and approval files need an external
user trust record; executable identity is resolved, credential transport is a user
setting with secure defaults, keychain decoding and persistence are bounded, and
project paths resolve symlinks consistently. Its compatibility rules apply to
existing sessions, workspaces, notes and worktrees.

## Amendment 2026-10-07: forgetting folders without deleting them

Workspaces listed unnamed folders inferred from task history without a remove
action. Removing a named workspace could immediately list it again as an unnamed
folder. **Forget**, with confirmation, now applies to both: it removes the
registered name and mounts and stores the canonical folder in a top-level
`forgotten` list in `workspaces.toml`. Home and Workspaces omit those suggestions,
but files, task history and already-open tasks are untouched. A matching last
pick is cleared in the daemon and in the console; a folder picked in the tab
is kept even if it is forgotten. Using the folder again brings it back: adding
it through `workspace.save`, or a task started or resumed there, clears its
marker. Repeated forgetting is harmless.

Who writes these markers: the daemon on `workspace.remove` from a console on
this computer, or the person editing the private configuration directory.
Neither a model nor a repository can reach that file. Paired devices cannot
forget a folder, but they may choose one by its path, as before. A task
started there from a device clears the marker as a side effect, as a task
started from a terminal does. That is safe because a marker changes only
suggestions: it never grants access or changes a task's project identity, so
clearing one widens nothing. No file inside the repository is consulted to
make this decision. Session start, in core and so in the daemon and the
terminal alike, clears only a marker that names its folder, and writes the
file only then. It and the console's changes hold a sibling
`workspaces.toml.lock` while they reread and rewrite the file, so neither loses
the other's change. A marker that is not an absolute path is skipped, not an
error, since every session start reads this file for its mounts.

The list is its own read-only method, `workspace.forgotten`, so that
`workspace.list` keeps one result type. A console on an older daemon, which
lacks the method, still lists the daemon's workspaces and hides nothing. Browser checks
exercise cancellation, unnamed and named folders, reload, re-adding, retained
files/history, the paired-device restriction, and a folder forgotten on the
computer that a phone chooses by path and brings back by using it.

## Amendment 2026-10-08: attaching from the TUI

`:attach PATH` in the TUI attaches a file the person names, in local mode
through `core.Session.Attach` and in daemon mode through `session.attach`,
which W2 made for daemon clients that name a path. The path is read as a
shell reads it (`~/`, relative to where nodwyn started). It is the person's
choice, as the console's file picker is, and the daemon reads it as the same
user. The model never names it, and the commands of the TUI are typed, never
taken from session content. Extraction, redaction, the cap and an image's
security confirm are those of every attachment.

An image's `session.attach` waits for its confirm, and the daemon runs one
connection's calls in order, so the TUI asks it on a connection of its own
and answers the confirm on its usual one. The attachment runs beside the
keyboard, so the confirm takes the screen like any prompt. Ctrl+C meanwhile
is `session.abort`, which declines the confirm, rather than quitting. The
TUI counted every prompt as a turn waiting, busy until the turn ends. An
image's confirm (`Prim` `attach image`) is asked by no turn, so it no longer
counts, or every message after an image would have been refused. That held
for an image a console attached while the TUI watched, too.
`TestAttachThroughBothBackends` attaches text and an image over both
backends. On one connection the daemon-mode case waits for the confirm until
the test gives up, which was checked by making that change.

Not built: attaching in line mode.

## Amendment 2026-10-15: a folder may let the model search its earlier tasks

A named folder's entry in `workspaces.toml` gains `earlier_tasks`, off
unless the person ticks it in the folder's editor on Workspaces: whether
the model may search the folder's other tasks and recall their events
(ADR 50 §9). It is the person's, as the mounts are: kept on this computer
with the folder's name, written through `workspace.save`, which a paired
device is refused, or the TUI's `:earlier`, and never in the folder, so a
repository cannot turn it on. A save that does not name it keeps it. A session reads it at
every call rather than at its start, so turning it on or off counts at
once.

## Amendment 2026-10-16: the version store loses nothing silently

A write that replaces a file keeps a version first, and the cases that kept
nothing without saying so now say so. A link is followed, as the write is:
the target's content is kept under the target's path, where the link itself was
skipped and the target overwritten with no version. A file over 16 MiB, too
large to keep, refuses the write with that reason where it used to go ahead.
The sessions of one repository share one store, so its index is changed under
a file lock (`versions/lock`): a pruning session used to rewrite the index
from what it had read and lose a version another had appended, leaving its
object behind.

## Amendment 2026-10-16: spreadsheets and decks are read in their own order

A spreadsheet stores neither empty cells nor empty rows, so reading the cells of a
row in turn shifted every value after a blank one to the left, and a sheet's
rows lost their numbers. Each cell is now placed by its reference (`C3`), an
empty one in between is an empty field, and a row is written with its
number (`3: x<TAB><TAB>5`); a row with nothing in it is left out, its number a
gap. Sheets come in the workbook's order under the names it gives them, and
slides in `presentation.xml`'s order and numbered by it, resolved through the
relationships parts; the numbers in the files' names, which are the order
parts were made in, are used only for a package that has no such parts.

## Amendment 2026-10-19: the version store is the repository's own folder

The store lives in the repository's `.nodwyn`, which whoever can commit to it
writes, and git keeps links. Keep followed them: a link committed at
`.nodwyn/versions/objects/<sha256 of a file's content>` made nodwyn create the
file it named, outside the repository (`~/.zshenv`, a LaunchAgent), with
that file's content, when the person let a write to it go ahead; a linked
`.nodwyn`, `versions` or `objects` put the whole store elsewhere, and pruning
rewrote the index through a link at `index.jsonl.tmp`. Opening a folder needs
no trust (ADR 45) and writes in it go ahead (ADR 35), so nothing stood in the
way.

The store is now written only as itself. `.nodwyn`, `versions` and `objects`
must each be a folder, made if missing, and the store is opened as an
`os.Root` on the folder that was looked at, so nothing written lands outside
it. An object, `index.jsonl` and `lock` must be regular files where they
exist; `.gitignore` and a new object are created exclusively, which never
follows a link; pruning removes whatever is at `index.jsonl.tmp` before
writing it. A link anywhere on the way is refused, not skipped: Keep returns
the reason, so the write fails as it does for any version that cannot be kept
(amendment of 2026-10-16), and the person sees which path to remove. Skipping
the store would let a repository turn off the undo §2 promises. The read side
already refused a linked object (`ReadVersion`).

The lock is created exclusively, or else the file already there is opened:
on macOS, `openat` with `O_CREAT` alone, which `os.Root` uses, failed with
`ENOENT` in 687 of 1,200 attempts when six sessions created the same new
file at once, where `open` on a path and `O_EXCL` never did.

Tests: `TestKeepWritesNothingThroughALinkInTheStore`,
`TestPruneWritesNothingThroughALinkedIndex` and
`TestFsWriteFailsWhenTheStoreLinksOutside` (prim) fail on the code before
this change; `TestVersionsOfSeveralSessionsLoseNoneAndLeaveNoOrphan` covers the
lock.

## Amendment 2026-10-27: Office extraction cancellation

Office document extraction honours cancellation and request deadlines before
reading and between XML tokens. A cancelled extraction returns an error rather
than a successful partial document; spreadsheet cell references and the
workbook/deck ordering remain authoritative.

## Amendment 2026-10-28: one connection can attach an image

The daemon ran a connection's calls in order, so a client with one connection received an image's confirm as an event but could not answer it: `session.answer` waited behind the `session.attach` that waited for it. The daemon now serves a single-request `session.attach` apart from its connection's reader, which goes on reading; the reply may follow those of later calls, and a batch stays in order. `TestImageAttachOnOneConnection` attaches and answers on one connection and times out on the code before this change.
