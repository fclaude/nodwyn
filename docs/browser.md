# Browser view

The console is organized around tasks (ADR 21's design canvas): **Home** starts
a task in a folder and lists what needs an answer, what is running and what
finished; a **task** shows its conversation with the approval inline, and
beside it four tabs: **Live** holds this turn's budget, the plan and the
task's totals, everything that moves while the task works; **Changes**
holds what it has written, each file's kept versions and the attachments;
**Workspace** holds where it works, the tools it was offered and the
Details inspectors; **Notes** holds the project notes. The plan shows the
model's progress through
it: the steps it marked done (with what it says shows it), the ones it
skipped, and the next. A step folds to its title, the clause up to its
first dash, and the mark's note waits behind the fold too, so the plan
reads at a glance; clicking a step shows the whole of it, the note with it.
After each reply the conversation shows its tokens,
and when the provider reports them or the model has prices, how much of the
prompt came from the cache and what it cost (the manual, "Choosing models");
over several requests, the largest single prompt too, since each request
sends the whole prompt again. The side panel's Context meter shows how full
the context the model holds is against the window's high water, from the
start of a task: while the explorer, the planner or a sub-agent asks in a
context of its own, the meter follows it and names it (*Context ·
explorer*), and shows the conversation's again once it folds; what the
provider counted and how often it was compacted are in its tooltip, so the
panel does not shift on every reply. "Tokens sent", shown from the turn's
start, is the token budget's
count, every request's prompt, a token read from the provider's cache
counted as a tenth. Beside them the turn's clock is split: "Model wait",
each request to a model from its sending to its reply, and "Tool time",
each tool call from its start to its result, the wait for your approval
left out. Only a figure with a limit behind it is a bar (Time, Tool calls,
Tokens sent and Context, and the total spend when a spend limit is set);
the bars come first and the rest are plain rows under them. A compaction is
one line in the conversation: what set it off, and the context's size
before and after.
**Connections** (tools, credentials, workspaces, models),
**Workspaces** and **Settings** hold the rest; Settings also creates,
unlocks and sets the passphrase of the keychain. The
U3 inspectors described below sit under a task's **Details**, in the
Workspace tab. For the app
window and first-run setup see [app.md](app.md).

## Settings

Settings is what tasks start with when nobody says otherwise, kept in
`config.toml`; every other line of the file, comments included, stays as
you wrote it. Its sections:

- **Approvals** — what a task does without asking you (ADR 35): whether it
  writes files in its folder without asking (the default) or asks first,
  and who answers a command no allow list covers: the judge, then you when
  it refuses or there is none (the default); you; or nobody, and it runs. A
  write to a protected file, content that looks like a credential and a
  command built from what a tool returned always ask. The transcript says
  which calls went ahead, which the judge allowed, and why it refused one.
- **Limits** — each turn's tool calls, tokens sent, actions and time: where
  it stops, and, if you set it lower, where it asks first whether to go
  on (by default it doesn't); the spend limit on a whole
  task, in dollars, off until you set where it stops; and how long a model
  reply and a command may take.
- **Models** (ADR 41) — three things.
  *Default for new tasks* is a model from the list and its default effort,
  saved on its own button, since it is a write to `config.toml` that the bar
  below does not carry. *Providers* lists each model server with whether it can
  be used and, when not, the sentence that says what to fix (needs its key,
  keychain locked, key not allowed, not answering); **Test** sends it one short
  request and shows the reply or the provider's refusal; **Check again** asks
  every server now; **Add a provider** and **Edit** take a kind, an address, a
  dialect and an API key (kept in the keychain, bound for the provider's
  requests and its model list, never in the page again), with presets that only fill the form. *Models you can
  choose* is the list the pickers offer, each with the efforts it takes and
  a line saying its context length and prices; **Add a model** and **Edit**
  take a provider, a model id (suggested from what the provider lists, asked
  with its key), its efforts (*As published*, the provider's, once the id is
  given and `models.published` describes it) and its own settings (ADR 41,
  amended 2026-10-05), each with what the provider publishes as its
  placeholder: its context length, its reply cap, which comes out of it, with
  a line under both saying what the conversation is left with as they are
  typed, its prices, which price a request when the provider reports no cost,
  and whether it takes images (yes unless you say no); under *More settings*
  its temperature and seed, and for a chat-completions model which
  field caps its reply, how its requests name their session and whether they
  ask the provider to cache the prompt, which a Claude model needs (the
  manual, "Keeping the cache warm"), or for Ollama how long it stays loaded.
  The default model's settings are its entry's, so editing it changes the
  model wherever it is chosen. These forms are drawn from state kept for the
  visit, so the readiness checks that arrive while one is being typed into
  redraw the page without losing what was typed, and leaving Settings drops
  it, a key included.
- **Judge** — whether a judge is available, what it may be shown and the
  probability at which it lets a command run. Its model, its prices (with
  what typesafe.ai publishes as their placeholders) and how long it may take
  are under **Models**, after the models' own.
- **Documents** — how uploads to Google Docs are handled (the manual,
  "Documents out").
- **Containers** — podman, docker, or whichever is installed, for contained
  MCP servers.
- **Context window** — the water marks between which a task's context is
  summarized or trimmed (unset, they follow each model's context less its
  reply cap; set, they shrink only for a model that cannot hold them), the
  largest tool result shown whole, and the default reply cap of a model that
  sets none.
- The keychain, paired devices, this browser's appearance, whether it
  shows the model's thinking, and its notifications, and this connection.

A field left empty takes its default, which it shows; clearing one puts the
default back. Changes wait in a bar at the bottom of the page and are saved
together, and the daemon checks them together as it would on loading the
file, so a soft limit can be raised past the old hard one along with it.
They apply to tasks started afterwards; an open task keeps the limits and
model it started with, and its Budget details say which. A task's model and
effort are changed under its own message box (below). The judge's address,
its credentials, the keys of the providers (beyond saving one with its
provider) and the `[policy]` table are set where they are reviewed, or in
`config.toml`.

**The model picker.** Under the message box of the New task window, and under
a conversation's, a **Model** and an **Effort** select. Both list the models
of Settings grouped by provider, each labelled with what is wrong when it
cannot be used, and the efforts that model takes (*Not configurable* when it
takes none). Choosing another model keeps the effort when the new model takes
it, else takes that model's own default, and says so.

- *New task.* It starts on the default of Settings. A choice is kept while
  another page is shown and in this browser across a reload (an unsent choice,
  like the message), dropped when the task starts or the default is picked
  again, and never changes the default. A choice left alone is not sent. A
  model that cannot be used is said so and a task is not started on it; one
  removed from the list is said to be gone, and the page does not pick another.
- *A conversation.* The selects show what the next message is answered with
  and change it (`session.model.set`). After a choice, the lines under them
  say that the change takes effect from the next message; an unchanged running
  response shows no change notice. When a choice is queued, they say it keeps the
  model and effort it started with and the choice waits for it, naming both,
  and, if the choice could not be applied when it ended, why. A conversation
  whose model is no longer offered shows *Choose a model…* and its reply box
  stays shut until one is chosen. A closed task shows the model and effort it
  was left on in its heading. The selection is the daemon's and the log's, so
  it is the same after a reload, a reconnection or reopening the task, from
  any window.
- A paired device sees the selects, disabled, and a line saying the model is
  chosen on the computer nodwyn runs on; Home shows only the default model's
  name.

## Tasks, folders and files

**Starting.** Home's composer starts a task in the folder picked last,
which this browser remembers like the theme; until a folder has been
picked, it starts in the first one on the list. **Add a
folder** names one and says what it holds — documents, code or both — and,
in the app window, opens the system's folder chooser. The kind is a label: every
kind has the same tools and the same guard (see
[ADR 22](adr/0022-cowork-workspaces-attachments-mcp.md), amended). The
Workspaces page lists every named folder: **Add a workspace** names another,
**Edit** changes a folder's name, kind and **mounted folders** (other folders
a task may read, or read and write, by absolute path), and **Remove** takes
it off the list after a confirm, leaving the folder, its files and its tasks
as they were. Home offers the first six folders; the rest are a click away on
Workspaces. The name and the mounted folders are kept in `workspaces.toml`
in the config directory, never in the folder, so a folder someone else
commits cannot mount anything (ADR 22). They apply to tasks started
afterwards.

**In a worktree.** Beside the folder, **In a worktree** starts the task in
a git worktree of its own
([ADR 34](adr/0034-sessions-in-git-worktrees.md)): a checkout of the
repository on a branch of its own, `nodwyn/<id>`, in
`.nodwyn/worktrees/<id>`, so several tasks can work on one repository at
once without touching each other's files or the ones you are editing. The
choice is kept for that folder in this tab. Only a git repository's top
folder with a commit can have one; any other is refused, in a sentence,
when the task starts. Uncommitted changes in the folder are not in the
worktree, and the conversation says how many. The task is listed under
its repository; its heading shows the branch, and its Workspace panel the
branch and where it started. Its changes stay on that branch until it is
merged. Closing the task removes the worktree and its branch only when
nothing in them would be lost: no change, no untracked or ignored file (an
attachment is one), no commit its base lacks. Otherwise both stay, and
resuming the task works in them again; a worktree that was removed is made
again on resume.

**The task list.** The sidebar puts what needs your answer first, across
folders. Every other task is under its folder, newest activity first, and
the folder whose task worked last comes first; the order follows as turns
end and tasks close. A folder shows five tasks and **Show N more**, and
its heading folds it (this browser remembers which). Search matches
titles and folder paths in every folder and in the archive
([ADR 39](adr/0039-named-archived-deleted-tasks.md)).

**Switching.** A task picked from the list puts the focus in its message
box, so the next word goes to its reply, and a word not sent is kept for
it. An open task's page is kept while another is shown: it stays
subscribed, draws what happens meanwhile, and comes back at once as it
was, without reading the conversation again. The eight most recently
shown are kept; a saved task's page, or one past those, is read from its
log when it is opened. A key typed into a request card is cleared when you
switch away.

Streamed text and thinking are joined for the next display frame, without
redrawing the task's heading and detail panels for each fragment. A task
kept off screen buffers those fragments until it is shown or another kind
of event arrives. Replies and questions flush preceding fragments at once,
so their order does not depend on a background tab receiving frames. The
conversation follows new text only while you are already near its end.

A task is read by its first message until you name it: the pencil beside
its heading (or a double click on it) turns the heading into a field;
Enter or clicking away saves, Escape keeps the name, and an empty name
gives back the first message. A closed task has **Archive**, which moves
it under **Archived** at the end of the list (**Unarchive**, or resuming
it, brings it back), and **Delete…**, which asks, then removes its
conversation and log for good and leaves the files it changed as they
are. An open task is closed first. A paired phone renames and archives;
only this computer deletes.

**Attaching.** The paperclip in either composer attaches files to the next
message (at most 20 MB each): text formats, `.docx`, `.xlsx`, `.pptx`, PDF
when `pdftotext` is installed, and images (PNG, JPEG, GIF, WebP; at most
10 MB) when the control model takes them; the TUI and line mode attach
nothing. Each file is copied into the folder's
`.nodwyn/attachments/`, its text redacted and capped, and shown to the
model as data. The conversation shows a chip per file with how many
secret-like strings were hidden.

An image cannot be redacted, so before it is attached the task asks, as a
security request card: it names the file, the model and its host, and
whether that host is this computer. Sending waits for your answer; from
Home the task opens first, so the card is in front of you. **Deny** (or
**Stop**) leaves the image out and keeps your message unsent, in the task's
reply box: send it there, with or without attaching again. Home starts
afresh, so the same image is not asked about twice. An attached image shows as
"image · 1200×800 · ~1,105 tokens": what it costs the context. It goes only
to the host you allowed, never to the judge.

**Undo.** Unless git already has a file committed and unchanged, nodwyn keeps
its previous content before changing it, so a write that went ahead without
asking can be taken back. A task's **Changes** list offers **History** per
file; **Restore** writes a kept version back as a write like any other (it
asks only when Settings › Approvals has writes ask), and keeps the current
content too. From a terminal: `nodwyn versions FILE` and `nodwyn undo FILE`.

**Tools from MCP servers.** Connections › Tools adds a remote server (HTTPS, or
plain HTTP to this computer only; a key goes to the keychain, bound to that host only)
and the folders it is offered in, lists its tools for
review, and approves that exact tool list. For each tool you say what it does
— reads, changes things, or leave it undeclared — and whether it is offered;
nothing is offered until you choose. An undeclared tool asks every call and
offers no standing answer. If the server later changes its tool list, new
tasks withhold its tools until you review again.

A server can also be **contained, on this computer** (ADR 23 W3b): its image
pinned by digest, the hosts it may reach (none, or a list its proxy lets
through), the workspace folders it sees, and, if it needs a key, the
variable it reads it from; the key goes to the keychain, bound for every
listed host. Its page says what it can reach, whether podman or docker is
there, and whether its image is; Pull downloads the image by its digest,
which nothing else in nodwyn does and a task never does. Reviewing its tools
starts it in a container and removes it after. A confirm for one of its
tools says it sends to the contained server and what that can reach; a
connection its proxy refused shows in the task.

## Credentials

**Connections › Credentials** (`#/connections/credentials`, or Settings ›
Keychain › Manage credentials) lists what the keychain holds: each
credential's class, kind (an API key, or an OAuth2 account with its provider
and granted scopes, and when its access token runs out), what is configured
to send it (the main model, another model, a model server, an MCP server,
Google Docs, the judge) and the binding rows that allow it somewhere. A credential allowed
nowhere is marked: nodwyn refuses every request that would carry it. A class
a model or an MCP server sends that the keychain does not hold is named at
the top, with a button to add it; so is a keychain without a passphrase.

- **Add a key**: a class, the value (a password field; nothing shows it
  again), and optionally the host it may go to, its methods and where it goes
  in the request (`Authorization: Bearer`, another header, a query parameter,
  the body, or a contained MCP server's environment variable). The host is
  saved as one exact binding row per method.
- **Allow it for a host** adds binding rows for a credential already stored;
  an OAuth2 credential's rows carry the scopes it was granted, so a broader
  token is refused.
- **Replace the key** gives a static key a new value after it was rotated
  where it was issued; its id and class stay, so its bindings and what uses
  it carry on.
- **Connect an account (OAuth2)**: a provider (google, microsoft, github,
  notion, or custom with its endpoints), your OAuth client's id (and secret
  if the provider insists), scopes (the provider's narrowest useful set by
  default) and a class. nodwyn opens the provider's consent page; when you
  agree, the provider sends the browser back to a listener on this
  computer's loopback, and the tokens go into the keychain. The page waits
  up to five minutes and shows the outcome. **Authorize again** does the same
  for a credential the provider stopped refreshing. It works from a browser
  on the computer nodwyn runs on, since that is where the provider sends you
  back.
- **Remove…** asks first, naming what uses it; an OAuth2 credential can be
  revoked at its provider and removed, or removed here only. Its binding
  rows stay in `bindings.toml`: narrowing or removing a row is an edit of that
  file, or of a task's Details › Policy.

No reply carries a value or a token. A paired device sees the list but
changes nothing.

## Other devices (ADR 30)

```sh
nodwyn ui --listen 100.101.102.103:7777          # a tailnet address
nodwyn ui --listen 100.101.102.103:7777 --name desk.tail1234.ts.net:7777
nodwyn ui --listen 0.0.0.0:7777 --name desk.local:7777 --tls-cert cert.pem --tls-key key.pem
nodwyn devices                                   # list paired devices
nodwyn devices revoke phone
```

A browser on another machine pairs with the code **and a device name**. It
gets a device cookie (HttpOnly, SameSite=Strict, Secure over TLS, 180 days);
`devices.toml` in the config directory keeps only the token's SHA-256, so the
file cannot pair anything. Revoking takes effect without a restart: new
requests are refused at once, and a console the device already has open is
closed before its next request, or within five seconds if it is only
watching. The console answers only to the names it listens on, so a wildcard
`--listen` (`0.0.0.0`, `::`) needs `--name` with the name browsers use. Every answer from a device is recorded as the bridge's
kernel identity followed by `via web:NAME`.

A device can read tasks, start and steer them, and answer approvals — one-time,
for this turn, or for the session, as the core offers; it can also restore a
kept file version, save project notes, read the policy files, see the
credentials, and stop a run. Its tasks ask what Settings › Approvals, chosen
on the computer nodwyn runs on, says they ask. It cannot start or resume a
run that answers its own requests (unattended, judged, or saying yes), turn
on "don't ask", save a credential binding,
write a policy file, unlock or create the keychain or change its passphrase,
add, replace, allow, authorize or remove a credential, change Settings, the
model or how
uploads to Google Docs are handled (it can answer the first upload's
question with "allow by hand" or "deny", never "always allow"), edit
workspaces, add, review, approve, pull or remove MCP servers, evaluate Scheme, or
list and revoke devices: those stay with the computer nodwyn runs on. A request counts as remote when it comes from another address or names
this console by anything but its loopback address (a proxy such as
`tailscale serve` connects from loopback on a phone's behalf). Launch tickets
pair only local browsers. Without `--tls-cert`, traffic is unencrypted unless
the network encrypts it; a tailnet does.

# Local browser view

The U3 session desk supports conversations, prompt replies and session lifecycle
controls. Run a daemon, then attach the browser bridge in another terminal:

```sh
nodwyn daemon
nodwyn ui                         # opens the local browser page
nodwyn ui --read-only             # view only; no replies or lifecycle changes
nodwyn ui --socket /absolute/private/sock --no-open
```

`nodwyn ui` connects to an existing daemon and opens the page with a one-time
launch ticket in the address's fragment, so that browser pairs itself. It
also prints a random pairing code for another browser on this computer;
paste that code into its page. The code is never put in a URL or session log.
Keep it in the terminal rather than sharing it. The browser receives an
HttpOnly, SameSite=Strict session cookie; no credential is written to browser
local/session storage. Without `--listen`, restarting the bridge creates a new
port and code; with it, the port is yours and a device's cookie survives
restarts.

The desk shows saved and open sessions, their repository and last activity,
recorded conversations, streamed text, tool arguments and results, guard
decisions, and model usage. Reads and searches in a row fold into one group.
Writes in a row fold the same way, into one Edited group whose header counts
distinct files, each write keeping its row with its outcome; a failed write
opens its row and the group. A line of narration the model writes between two
writes goes into the group, in order, behind its fold, since the group says
what the line was about; a longer reply, or the words before a command, stay
where they are and end the group. Commands in a row fold the same way, into
one run whose header says how many ran and for how long together, counts up
each second while one runs, says when one waits for your answer, and counts
those that failed; a failed or refused command opens its row and the run. A
read, a command, a mark or a reply of more than a line ends the other groups,
so the transcript keeps its order. The model's marks on its plan join one
line in a row ("Steps 1, 2 of 5 marked done"), their notes behind its fold.
The model's thinking is hidden, as in the TUI without `:thinking`, unless
Settings › This browser › The model's thinking is **Shown** (kept in this
browser); shown, a reply that only thinks and calls tools puts its thinking
in the group or run its calls join, between the calls, under the thought's
first line. It reads a fixed log prefix in pages
before applying live events, deduplicating durable sequence numbers. Reloading
the page reads that prefix again. A reconnect never retries an operation. Browser
reload retains pairing through the session cookie, and the address keeps the
task, so the same task opens again.
Untrusted text is rendered as literal text, never HTML or executable Markdown.

Create a session with an existing absolute repository directory, resume a saved
session, and send messages from the composer. Abort cancels the current turn or
pending prompt. Close ends the session and preserves its transcript; it is disabled
while known busy, but another client can start work concurrently, so closing can
still interrupt work. Drafts remain separately in memory for each session in this
tab and disappear on page reload. A failed reply may follow successful delivery:
inspect the transcript before sending again. No operation is automatically retried.

`--read-only` retains the original view-only mode. The server filters advertised
methods and enforces this restriction independently of the browser controls.
The interactive mode includes a pending-prompt panel with reasons, literal
arguments, egress scope, and the existing write diff (additions/deletions colored
without changing the diff algorithm). Allow once and Deny answer only that live
prompt. Eligible ordinary calls also offer the displayed command/write prefix for
this session, or ordinary writes for this turn. Security and protected-path
confirmations remain one-time only; no grant overrides a denial. Under the
composer, **Don't ask: approve writes and commands** answers every ordinary
write and command in that task without asking, and the question waiting when
it is checked, until it is unchecked or the task closes; security and
protected-path questions still ask, the conversation says when it was switched
and each call it approved says so. It is the daemon's, not Scheme's
(`session.approve_all`, [protocol](protocol.md)), it is never saved, so a
resumed task starts asking again, and only this computer turns it on: a
paired device does not see it. The daemon sends
derived scope labels in optional prompt metadata and validates the actual pending
prompt again when an answer arrives. An older daemon without this metadata gets
only one-time browser buttons.

When an unbound call has a core-approved exact binding proposal, the panel shows
its credential class, host, method and injection position. Saving that row uses
the separate binding method, persists beyond the session, and permits the pending
call. That prompt-bound operation cannot substitute row fields. The separate policy-file
editor below proposes explicit file changes for another review. Resume never reinstates session/turn grants from their audit history.
Scheme evaluation remains unavailable from the browser.

Credential prompts use a password field and the existing shared keychain path.
Input is cleared on submit, session switch, refresh, or disconnect; it is never
saved as a draft or included in the transcript. JavaScript strings cannot be
reliably wiped from browser memory; the UI does not claim a secure memory arena.
Budget checkpoints show usage, what the turn has cost so far when that is
known, and unchanged hard ceilings, with continue, stop, and explicit absolute
soft-limit choices. More budget grants no tool authority.

Read-only mode displays the pending prompt but exposes no reply controls and
rejects replies at the server. Pending state is restored from the daemon on
reconnect; stale/replayed answers are rejected by core. Resolution in another
client clears the panel. Browser replies retain the Unix peer identity of the
bridge in audit receipts; individual tabs are not separate security principals.

Closing a browser tab or stopping `nodwyn ui` does not close sessions in the separately running daemon. Stopping the daemon still closes its
sessions normally. The session list's titles and history metadata are snapshots, read
again when this console starts, resumes or closes a task, or another client opens one;
reload the page to update them at other times. Open/running/needs-answer state updates live across the daemon's
sessions, including sessions opened by another client. These updates do not
reload the selected conversation, replace an inspector or discard a draft.

The session view buttons open fixed snapshots:

- **Plan record** shows the latest user message's recorded gate outcome, score and plan,
  and the steps the model marked since (`plan.step`), in order.
  A newer message without a recorded outcome never inherits an older plan.
- **Budget** separates current configured soft/hard limits from the latest turn's
  recorded checkpoints. Checkpoint usage is not live remaining allowance.
- **Log** filters durable records by kind prefix and shows the last 1–1000 matches.
- **Policy** shows the last validated bindings, current command/write prefixes and
  turn approval. It does not restore authority from recorded receipts. Separate
  buttons open the guarded policy-file editor.
- **Tools** shows the model's current tool catalog and input schemas.
- **Friction** reads `docs/friction.md` through the session interpreter and guard.
  It needs an idle open session in interactive mode. Custom checker prompts take
  priority; after answering, reopen Friction to see the completed result. Refresh
  view starts a new guarded read. Denial or cancellation returns an error, never
  the preceding read's contents. Text passes normal redaction and is rendered
  literally. This fixed-path operation cannot select arbitrary files.

Every inspector names its log watermark. Live transcript events keep arriving but
an open inspector stays fixed until Refresh view reloads history and metadata.
Current session metadata is sampled separately from the log prefix. Saved sessions
retain plan/log views; current limits, policy and tools require an open session.
Pending prompts take priority over inspector navigation. Read-only mode has the
same snapshot inspectors; it cannot start a friction read. It retains its server-side mutation restrictions.

Without `--listen` the bridge binds only `127.0.0.1` on a random port; with it,
the address you give, for paired devices (above). Every request checks its
exact Host; pairing and WebSocket upgrades also require the exact browser
origin. Cross-site fetches and framing are refused. There is no general TCP
RPC transport: the daemon's Unix socket remains private, and kernel peer
identity still identifies the bridge process.

Implementation is deliberately small: embedded HTML/CSS/plain JavaScript,
`internal/frontend/web` for the HTTP/WebSocket boundary, and the existing RPC
client for session data. There is no JavaScript build system, second history store or policy evaluator.
`app.js` is the entry point (pairing, router, shell); `connection.js` is the one
connection to the bridge; `desk.js` holds what every page reads (tasks, what
needs you); `sidebar.js`, `home.js`, `task.js`, `transcript.js` and `pages.js`
draw the views; `prompts.js` renders review choices; `setup.js` the first-run
model and keychain setup; `modelpicker.js` the model and effort picker of the New task window, a conversation and Settings; `models.js` Settings › Models; `credentials.js` Connections › Credentials; `attach.js` attachments; `markdown.js` model replies;
`inspectors.js` fixed snapshots from the same redacted records; `policy.js`
policy drafts and review controls; `dom.js` the helpers that keep every string
text. A pinned WebSocket library avoids a
homegrown frame parser; its license is embedded in `nodwyn licenses`.

## Verification and remaining U3 work

Tests cover pairing, missing/wrong credentials, origin/Host rejection, read-only
method enforcement, filtered capability discovery, browser lifecycle/message
operations, durable history after resume, live daemon events and session survival
after browser detach.
Browser checks use isolated synthetic sessions and local fake services, covering
literal HTML-like text, paging, refresh, collapsed thinking/tool details, reload,
disconnect notices and narrow layouts. These are acceptance fixtures, not claims
of production usage.

Browser acceptance also covers creation, message errors, independent drafts,
close/resume, and aborting a pending confirmation without executing its operation.
Prompt tests also cover attributed one-time approval/denial, stale and forged
answers, canary credential isolation, and checkpoint hard ceilings. Browser checks
exercise pending snapshots, cross-client resolution, masked input clearing,
read-only prompt display, and valid/invalid checkpoint choices.
Scope tests additionally cover temporary grant lifetime, protected/security
confirmations, exact persistent binding and host injection, forged destinations,
and clean authority after resume.
Inspector browser checks cover paged log filters, older-plan exclusion, fixed
snapshot refresh, current/saved session distinctions and prompt priority.
The friction view is covered through core, RPC and the browser WebSocket, with
allow/deny/custom-confirm/cancellation, concurrent-operation rejection and canary
redaction. Browser checks also cover prompt priority, cached result reopening,
explicit guarded refresh and denial replacing the previous result.
The policy editor completes the current U3 scope. Its two targets are shared
`bindings.toml` and repository `.nodwyn/allow.toml`. Reads and writes use fixed host
primitives through the interpreter and guard, with no arbitrary path or browser
Scheme evaluation. The editor is unavailable through read-only bridges.

Each save validates TOML and requires a one-time diff confirmation. Session/turn
approvals cannot waive it. A revision comparison under the shared policy lock
rejects stale edits; a new read and review are required. Publication is atomic
and mode 0600. Cooperative writers use the sibling `.lock`; an unrelated editor
can still ignore that advisory lock. Files are capped at 64 KiB, final symlinks
and non-regular files are refused, and repository approval paths cannot escape
through parent symlinks. Recognized secrets are refused instead of round-tripping
redacted text as editable policy.

Bindings apply before the next egress operation. Repository approval-file changes
apply to new or resumed sessions; existing sessions retain their current grants.
Global `config.toml` settings and transient grants are not edited by this view.
Drafts stay only in the selected tab/session and clear on switching, refresh or
disconnect. After answering a prompt, reopen Policy to see the result. Successful
saves require reloading before another edit; failures retain the draft. A failed
connection never retries a save automatically.

Tests cover per-save confirmation, denial/cancellation, stale revisions,
caller-supplied paths/actors, environment-name hijacking, model-origin rejection,
known-secret containment, bounded reads and filesystem escape checks. Browser
acceptance covers creation, persistence, validation errors, stale saves and draft
retention after a failed write.
Cross-session notifications and runs are described below.
Optional general TCP transport remains separate roadmap work.


## Cross-session attention (U4)

The sidebar marks every open session needing a confirmation, credential reply or
budget answer. Select that session to review its normal pending-prompt panel.
The lightweight `session.watch` feed contains only IDs, state flags and revisions;
it does not subscribe the browser to other sessions' transcripts or prompt text.
Late snapshots cannot replace a newer state. Closed/resolved sessions clear their
attention indicator, and disconnect clears the live-state claim.

Desktop notifications are optional. In Settings › Desktop notifications, **Turn
on for this tab** asks this browser for permission; the choice is held only in
this tab. In the app window the system's notifications are used instead, with
no browser permission to grant. When the page
is hidden, a new needs-answer transition can produce a generic notification.
It never includes task text, prompt arguments, diffs, credential classes or
values. Clicking it opens the corresponding session for review. Resolution
closes the notification where supported; reconnect snapshots do not re-notify old
prompts. Blocked or unsupported browser notifications leave sidebar attention
available. Browser/OS notification delivery still depends on their permission
and focus settings.

RPC/WebSocket tests cover content-free snapshots and live transitions. Browser
acceptance verifies another session's prompt and resolution without disturbing a
draft, and notification mocks verify opt-in, generic text, revision ordering,
deduplication and reconnect behavior. Native OS notification delivery was not
exercised by those mocks.

## Runs (U4)

Every task Home starts is a chat (ADR 35). A request that takes long is a
message like any other: the turn goes on until the model answers or a hard
limit stops it, and asks you only what Settings › Approvals leaves to you.

`nodwyn run` carries a task to its end with no conversation, for scripts
and benchmarks ([runs](runs.md)). The console shows such a run like any
task: its status says "Run" or "Unattended run" (and "judged" when the judge
answers its confirms) with the turn count, the sidebar shows the turns and
the time left, and the plan panel shows each turn's progress. A supervised
run's checkpoint arrives as an ordinary prompt. **Stop** aborts the session
and with it the run; a message afterwards goes on with the task as a chat.
The transcript says when an unattended run went on or stopped by itself,
and which confirms the judge allowed or refused.
