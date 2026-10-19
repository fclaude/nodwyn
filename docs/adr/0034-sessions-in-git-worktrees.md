# ADR 0034: Sessions in git worktrees

- Status: **partly implemented** (location and git authority decided
  2026-10-02; a session in a worktree from the console built 2026-10-03,
  see the amendment)
- Date: 2026-10-01
- Amends: ADR 17 (no standing approval for commits or pushes) and the
  original build plan's §4, B3 (the operator commits; see the index), in
  worktree sessions only

## Context
A session is pinned to one directory for its life (`log.SessionStart.Repo`).
Several sessions may be open on one repository — the daemon allows it, and
a long `nodwyn run --yolo` invites it — and today they share one working
tree, one index and one stash. One session's edit shows up in another's test
run; the default habit `git stash; <command>; git stash pop` (prompts.scm)
moves another session's work; a run ties up the checkout the user wants to
keep working in. Nothing detects any of it: `docs/browser.md` only says that
another client can start work concurrently.

Git's linked worktrees give each session its own checkout and branch over
one object store. nodwyn already accepts one as a root: every `.git` check
is an `os.Stat`, which takes the `.git` *file* a linked worktree has, so
`--repo` pointed at a worktree gets the git tools and git as the undo, and
writing that file is a security confirm (`.git/` is protected). What is
missing:

1. **A way to start a session in a fresh worktree.**
2. **The repository a worktree belongs to.** Project notes, MCP servers'
   `workspaces`, the history behind `session-list` and `session-events`,
   `knownFolder` and the console's folder list all key on the exact root
   path, so a worktree is a new, empty project.
3. **A lifecycle.** Closing is not finishing: sessions close on every
   shutdown of the daemon, the app or the TUI, and resume later, and resume
   requires the recorded directory to exist. Nothing removes what a session
   created.
4. **A way for the work to come back.** Only the operator commits (B3), and
   ADR 17 keeps every commit and push a per-call confirm, so a session's
   branch would wait for a person even when the user wants it landed.

## Decision

### 1. One worktree per session, created by the host when the user asks
`--worktree` (plain, `tui`, `run`), the console's "in a worktree" choice and
`worktree.open` start a session in a new worktree of the repository instead
of in the repository. Creating one is user-origin, like choosing the folder:
there is no primitive for it, and the model cannot move its own pin.
Sub-agents share their session's worktree; they run one at a time
(agents.scm), so they need no worktree of their own.

- **Location: inside the repository,** at `<repo>/.nodwyn/worktrees/<id>`,
  where `<id>` is the random suffix of the session ID
  (`20261002T054407Z-1968c13a` → `1968c13a`). A backup of the repository's
  folder then holds its sessions' work in progress too. Restored to another
  path, `git worktree repair` relinks them. nodwyn does not use git's
  `--relative-paths`: that sets a repository format extension, and older
  git and libgit2 then refuse the repository.
  - The directory ignores itself with a `*` `.gitignore`, as
    `.nodwyn/attachments/` does, so the repository's `git status` stays
    clean.
  - It is hidden, so the repository's own sessions do not search it.
  - It is under `.nodwyn/`, so their writes into it are security confirms.
  - It is not under the data directory: the denylist and the task-root
    refusal rule that out, and should.
- **Branch:** `nodwyn/<id>`, from the repository's `HEAD`, or from
  `--worktree-base REF`. The base branch (usually `main`) is recorded; it
  is the `main` of section 4.
  - Uncommitted and untracked changes in the repository are not carried.
    When there are any, the session's start says so.
  - An untracked `.nodwyn/workspace.toml` is copied, because it is the
    folder's settings, not content. Nothing else is copied, and never a
    file the credential denylist names.
- **Creation** runs `git worktree add -b nodwyn/<id> <path> <base>` with the
  scrubbed environment and `GIT_TERMINAL_PROMPT=0`. It runs the user's
  hooks and filters, as their own `git worktree add` would; LFS checkouts
  need them.
- **Refusals:** a repository with no commit, a bare repository, and a root
  below the repository's top level are refused with a sentence saying so.
  - The task-root refusal applies to the repository and to the new path,
    and it moves into core, so the plain CLI and the local TUI get it too.
    Today only RPC calls it.
  - `--worktree` from a session already in a worktree makes a sibling: same
    repository, based on that worktree's `HEAD`.
- **The log:** `worktree.create` records the repository, the path, the
  branch, the base branch and the base commit. `log.SessionStart` gains
  `worktree` (omitempty) with the same fields; `Store`, `session.list` and
  resume read it.

### 2. The checkout is the root; the repository is the project
The pin, every tool, `shell-exec`'s working directory, contained MCP
servers' `/workspace` mounts, `.nodwyn/init.scm`, `allow.toml` and
`workspace.toml` are the worktree's: what the branch has, exactly as if the
user had checked it out. That adds no new trust. "a" answers last the
session (ADR 17). An `allow.toml` edited in the console's policy editor
during a worktree session is the worktree's, and reaches the repository
with the branch.

What concerns the project keys on the **repository** instead. That is the
main worktree, found with `git rev-parse --git-common-dir` (run with the
read-only git flags of `runGit`). The lookup applies to every git root, so a
worktree made by hand groups too. It covers project notes, MCP servers'
`workspaces`, the history behind `session-list`/`session-events` (the
optimizer sees the project's sessions), `knownFolder`, and the console's
folders.

### 3. Close keeps it; removal is explicit and loses nothing committed
- **Close** leaves the worktree, so the session resumes in it. A worktree
  is removed on close, with its branch, when it is clean and its branch has
  no commit the base branch lacks: either nothing happened, or it all
  landed. Nothing is lost.
- **Resume** that finds the directory missing runs `git worktree add` again
  and logs `worktree.restore`. It uses the session's branch if that still
  exists, and otherwise a new one of the same name from the base branch's
  head.
- **`nodwyn worktree list [--repo DIR]`** shows nodwyn's worktrees: session,
  branch, commits ahead of base, dirty or clean, open or closed.
- **`nodwyn worktree remove SESSION`** runs `git worktree remove`, which
  refuses a dirty tree. `--force` discards uncommitted changes, and the
  output names what it discarded. It deletes the branch only if it is
  merged into its base (`git branch -d`); otherwise the branch stays and the
  output names it. It logs `worktree.remove` in the session's log.
- nodwyn removes only worktrees it created: recorded in a session's log and
  under `.nodwyn/worktrees/`.

### 4. The model commits, rebases and pushes a new head for `main`
In a worktree session the model may commit on its branch, rebase it, and
make it the new head of `main`. This comes as three primitives with fixed
arguments, like `git-status` and `git-diff`, offered only in worktree
sessions. It does not come as standing approvals for `shell-exec` prefixes,
so the guard knows exactly what runs:

- **`(git-commit message [paths])`** commits the worktree's changes, or
  only `paths`, on `nodwyn/<id>`. Credential-looking files are left out by
  pathspec, as in `git-status`. It needs no approval.
- **`(git-rebase [action])`** rebases `nodwyn/<id>` onto the newest base.
  When the base branch has an upstream, it fetches that first.
  - A conflict stops it and is reported file by file; the model resolves it
    with `fs-edit`, then calls it with `"continue"` or `"abort"`.
  - Only the session's branch is rewritten. There is no `--exec` and no
    interactive mode.
  - It needs no approval.
- **`(git-push [target])`** has two targets:
  - With `target` omitted, it sends `nodwyn/<id>` to the base's remote,
    with a lease, since rebasing rewrites it.
  - With `"main"`, it makes `nodwyn/<id>` the new head of the base branch,
    by fast-forward only and never forced. It does that on the remote
    `main` tracks, and on the local `main` when that is safe (below). A
    rejected fast-forward says to rebase and try again. With no remote,
    only the local `main` moves.

`shell-exec` is unchanged: `git commit` or `git push` there is still a
per-call confirm. Outside a worktree session ADR 17 and B3 hold as they
are.

What the guard still holds:

- **The model chooses no ref, remote or flag.** Each primitive refuses
  unless `HEAD` is the session's branch in the session's worktree. `main`
  is the base branch recorded at creation.
- **Hooks run only from where the model cannot write.** Commit and rebase
  run the repository's hooks, which are the user's. When `core.hooksPath`
  points inside the worktree (husky's `.husky/`, a tracked `.githooks/`),
  the model could have written the hook. The call then becomes a per-call
  confirm naming the hook.
- **Destinations are the user's.** `.nodwyn/allow.toml` gains
  `push = [URL, …]`, the remote URLs a session may push to; this repository
  lists its two.
  - `git-push` resolves the remote's push URLs
    (`git remote get-url --push --all`) and needs every one listed.
    Otherwise the push is a security confirm. That stops an unattended run
    from pushing to a remote the model re-pointed with `git remote set-url`
    through `shell-exec`.
  - `allow.toml` is protected, so no URL is added without a person.
- **What leaves is scanned.** Before a push, L1 runs over the outgoing diff
  (`git diff <remote head>..HEAD`). A credential shape makes it a security
  confirm, as for any egress (E3).
- **A push is egress and irreversible.** It counts against the turn's
  budget of irreversible actions. It is logged as `git.push` with the
  remote, the ref, and the old and new commit.
- **The local `main` moves only safely.**
  - If no checkout has `main` checked out, its ref moves by compare and
    swap (`git update-ref refs/heads/main NEW OLD`).
  - If a clean checkout has it, `git merge --ff-only` runs there, which
    updates that checkout's files as `git pull` would.
  - If that checkout has local changes, the local `main` is left alone and
    the result says so.

### 5. The model is told
- When the session is in a worktree, the default system prompts add a
  section. It names the branch and the base, says that other sessions may
  be working in other worktrees of the repository, and describes the git
  tools: commit as you go; rebase and push to `main` when the request asks
  for the work to land.
- The stash is shared by every worktree, so in a worktree the habit
  "`git stash; <command>; git stash pop`" becomes a comparison against a
  clean copy: `git archive HEAD | tar -x -C "$TMPDIR/base"`, then the same
  command there. Outside a worktree the habit is unchanged, so ADR 32's
  measured defaults stay what was measured.
- `(worktree)` returns `#f`, or
  `(("repository" . P) ("branch" . B) ("base" . M) ("base-commit" . C))`,
  for strategies.

### 6. Front-ends
- **RPC.** Clients find features in `hello`'s methods, and an unknown
  parameter is refused, so the feature arrives as new methods, not as new
  parameters on old ones:
  - `worktree.open {repo, base?}` returns session details, like
    `session.open`.
  - `worktree.list {repo?}` lists nodwyn's worktrees.
  - `worktree.remove {id, force?}` removes one.

  `run.start` gains `worktree` (omitempty), which a client sends only to a
  daemon that advertises `worktree.open`. `sessionInfo` and
  `sessionDetails` gain `worktree {repository, branch, base, path}`
  (omitempty).
- **Devices.** A paired device may open a worktree session, as it may call
  `session.open`. `worktree.remove` is local only, like other changes to
  setup (ADR 30).
- **Console.**
  - An "in a worktree" toggle beside the folder in the ask bar, shown for
    git folders.
  - The branch, commits ahead of `main` and the last push in the task
    header and Workspace panel, with Remove.
  - Worktree sessions listed under their repository, not as folders of
    their own.
- **TUI.** The chooser gains the row "New session in a new worktree of
  <repo>", and the title bar shows the branch.

## Consequences
- Several sessions, runs included, work on one repository without seeing
  each other's edits. A run's result is a branch, or a new `main` when its
  task says to land it.
- A session that may push `main` can publish a mistake as fast as it
  publishes a fix. The fences are a fast-forward-only push, the user's list
  of destinations, L1 on what leaves, the per-turn budget of irreversible
  actions, and `git.push` in the log. Review comes after the push, not
  before.
- A fresh worktree has none of the repository's untracked or ignored files:
  no build caches, virtualenv or `node_modules`, and no `.env`. The first
  build or install takes time. Tests that need an untracked file fail until
  it is there, and submodules are not initialized. A list of files to copy
  in, with the denylist applied, could come later; it is not part of this
  decision.
- Worktrees with changes pile up when sessions are abandoned. `nodwyn
  worktree list` shows them; nothing removes them on its own.
- A worktree inside the repository is visible to tools that do not skip
  hidden directories. Go and git do skip it, the latter through the
  `.gitignore`. Others that walk the tree may need it excluded.

## Open before WT3
1. Should the default prompt push to `main` only when the request asks for
   it (proposed), or also when the work is done and its checks pass?
2. When the user's own checkout has `main` checked out and is clean, should
   a push to `main` fast-forward it (proposed), or leave the local `main`
   for the user to pull?

## Amendment 2026-10-03: a worktree from the console

The console needed several tasks on one repository without them seeing
each other's edits. This slice builds that end to end, from the ask bar to
git, and leaves the rest of the decision for later. Clause by clause:

**§1, one worktree per session.**
- Built: `core.Options.Worktree` makes
  `<repo>/.nodwyn/worktrees/<id>` on `nodwyn/<id>` from `HEAD` before
  anything of the session loads, with the `*` `.gitignore`, the scrubbed
  environment, `GIT_TERMINAL_PROMPT=0` and the repository's hooks. A
  worktree's sibling comes from that worktree's `HEAD`. A folder below the
  top level, a bare repository and one with no commit are refused in a
  sentence. An untracked `.nodwyn/workspace.toml` is copied, and nothing
  else. `worktree.create` and `SessionStart.worktree` carry the
  repository, path, branch, base and base commit, and `Store`,
  `session.list` and resume read them. The base is the branch the
  repository had checked out, `""` when its `HEAD` was detached.
- Built, beyond the clause: a link on the way (`.nodwyn` or
  `.nodwyn/worktrees` committed as a link) is refused, since `git worktree
  add` would check the tree out wherever it points. So is a `.nodwyn` link
  in the new checkout, when the settings are copied.
- Changed: "the session's start says so" about uncommitted changes is
  `worktree.create`'s `uncommitted` count, which the conversation shows.
- Not built: `--worktree` and `--worktree-base` for the plain CLI, `tui`
  and `run`, and the task-root refusal moving into core. Only RPC makes a
  worktree, after its `rootRefusal` passed the repository; the worktree is
  inside it.

**§2, the repository is the project.**
- Built for the worktrees nodwyn makes: project notes, MCP servers'
  `workspaces`, and `session-list` and `session-events` key on the
  recorded repository. So do the console's folders and its task list.
- Not built: the `git rev-parse --git-common-dir` lookup for every git
  root. A worktree made by hand is still a project of its own.
- Unchanged: `knownFolder`. `versions.list` reads a task's root, the
  worktree, which is a folder a task used.

**§3, close keeps it, removal is explicit.**
- Built: close removes the worktree and its branch when nothing would be
  lost, and records `worktree.remove`. Resume makes a missing one again,
  with `git worktree prune` first, and records `worktree.restore`.
- Changed: "clean" counts ignored and untracked files too. An upload in
  `.nodwyn/attachments/` is ignored, and `git worktree remove` without
  `--force` deletes ignored files, so the first attempt at this slice lost
  them. The one exception is the copied `workspace.toml`, unchanged. A
  commit counts as unlanded when it is on the branch or at the worktree's
  `HEAD` and not on its base.
- Not built: `nodwyn worktree list` and `nodwyn worktree remove`.

**§4, the model commits, rebases and pushes.** Not built (WT3), and its
two questions stay open. In a worktree session, `git commit` through
`shell-exec` asks, as it does everywhere else; the work reaches `main`
when a person merges the branch.

**§5, the model is told.**
- Built: `(worktree)` and `(worktree-section)`, which names the branch,
  its base and the repository, and says others may work beside it and
  that the changes reach the repository only through the branch.
- Changed: the stash habit is overridden in the section rather than
  replaced in `working-rules`, so the prompts measured outside worktrees
  stay as they were (ADR 32, amended 2026-10-03).
- Not built: the section describes no git tools, since §4 is not built.

**§6, front-ends.**
- Built: `worktree.open {repo}` (no `base`), listed in the bridge's
  allowlist for this computer and paired devices alike. `sessionInfo` and
  `sessionDetails` gain `worktree`, with `base_commit` beside the fields
  named here. In the console: **In a worktree** in the ask bar, kept per
  folder for the tab; the branch in the task's heading and Workspace
  panel; worktree tasks listed under their repository; the three records
  in the conversation.
- Changed: the toggle shows for every folder when the daemon offers
  `worktree.open`, since the console cannot tell a git folder before a task
  opens there; the daemon refuses any other folder in a sentence. The choice
  is checked by default for git workspaces; unchecking it opts that workspace
  out for the tab. Other folders remain on the ordinary task path by default.
- Not built: `worktree.list`, `worktree.remove`, `run.start`'s
  `worktree`, commits ahead and the last push, Remove, and the TUI's row and
  title.

The browser check `worktrees` verifies that a new git workspace defaults to
`worktree.open`, that unchecking it starts a task with `session.open`, and
that checked tasks work in separate worktrees. It checks the requests sent,
where each task's file landed, the sidebar's grouping and the branch shown.
It also checks that closing keeps a worktree with work in it. The core tests run
real git in temporary repositories: the removal rules, restore,
refusals, the link check and the shared project.


## Amendment 2026-10-03: nothing is copied into a worktree

A folder's `.nodwyn/workspace.toml` is no longer read (ADR 22, amended
2026-10-03): a worktree's mounted folders are its repository's, from the
workspace list in the config directory. So nothing untracked is copied into
a new worktree, the refusal of a `.nodwyn` link in the new checkout, which
guarded only that copy, goes with it, and close treats every untracked or
ignored file alike: any of them keeps the worktree. The `uncommitted`
count of `worktree.create` counts every line `git status` gives.

## Amendment 2026-10-03: nodwyn's own copies leave the worktree, and git runs beside other methods

A review of the first slice found four ways it failed what §3 promises or
held up everything else.

- **Kept versions are the repository's.** A worktree session kept a file's
  previous content (ADR 22 §2) in the worktree's own `.nodwyn/versions/`,
  which its `.gitignore` ignores. Any file written twice, or a new one
  written again, left such a copy, and an ignored file keeps the worktree
  on close, so almost every task's worktree and branch outlived their
  merge. A worktree session's store is now its repository's
  `.nodwyn/versions/` (§2: the repository is the project). The copies are
  nodwyn's undo, not work left in the worktree. They keep nothing on close,
  outlast the worktree, and Restore finds them after a resume made it
  again. `versions.list` names a task's folder; for a worktree it reads its
  repository's store, which replaces "Unchanged: `knownFolder`" above.
  Uploads in `.nodwyn/attachments/` stay in the worktree and still keep
  it: later requests read them from there.
- **Restore clears only its own registration.** `git worktree prune`
  before making a missing worktree again dropped every registered worktree
  whose folder was away at that moment, a person's on a disk not mounted
  included. Restore now runs `git worktree remove <path>` for its own
  worktree when git still lists it, and nothing else.
- **Close's status lists folders, not files.** `--ignored=traditional
  --untracked-files=normal` gives a folder of untracked or ignored files
  as one line. Any line keeps the worktree as before, but close no longer
  lists every file of a `node_modules`.
- **A checkout does not hold the daemon.** `worktree.open` and
  `session.open` make the session, and `session.resume` resumes it,
  without the RPC server's lifecycle lock, as the model probes run. A
  checkout runs the repository's hooks and filters for up to two minutes,
  and every other method of every client waited on it. Serve closes
  sessions only after every method has returned, so one made meanwhile is
  owned before shutdown. A second resume of the same task waits for the
  first and then describes what it opened. Two worktrees made at once may
  both create `.nodwyn/worktrees/`; an existing folder is accepted, a link
  still refused. Close still holds the lock while the session ends.

Tests: `TestWorktreeGoesOnCloseWithItsVersionsKept`,
`TestWorktreeRestoreLeavesOtherWorktreesRegistered` (core),
`TestVersionsListReadsAWorktreesVersionsFromItsRepository` and
`TestWorktreeCheckoutDoesNotHoldOtherMethods` (rpc), each failing on the
code before this amendment. The last holds a `post-checkout` hook until
`session.list` from another client has been answered.

## Amendment 2026-10-05: reviewing a worktree task's changes

A task in a worktree can be reviewed from the console. Decisions:

- **Worktree tasks only.** The base commit in `worktree.create` is what makes
  "the changes so far" well defined. `worktree.diff` and `worktree.review`
  refuse any other session.
- **`worktree.diff` reads, with the daemon's authority,** the working tree
  against the base commit (committed, staged, unstaged and untracked), through
  fixed git arguments with no external diff or filter program, leaving out
  the files the denylist keeps from the model, and capping each file at the
  size of `versions.diff`. It is read-only, so devices may call it. It runs
  without the lifecycle lock, as a checkout does, and stops after 30 s, so a
  large branch does not hold every other method. The changes it lists take at
  most half a frame as JSON (JSON can make code several times larger than its
  text), and the rest are counted in `omitted`. A file with no lines to show,
  or that git cannot read, is a note in place of its diff and does not fail
  the others.
- **Comments, batched.** `worktree.review` sends the comments of one review as
  a single message (numbered in the order of file and line, grouped by file),
  so one turn answers them all rather than one turn per comment. The message
  is an ordinary user message: the guard and approvals apply to what the agent
  does about it. It carries the comment and the line's number, not the line's
  text: that is file content, and the message has the user's authority. The
  model can read the line. Comments live in the console until sent; the log
  holds the message, not a separate record. A review is refused while the task
  is answering, and a closed task has no worktree to read, so the console
  waits for the first and says so for the second.
- **Centre review, two layouts.** The task's Review button and Home's
  Recently finished worktree Review link open the existing centre inspector,
  replacing conversation/reply until Close review restores them. Unified is
  the default; Side-by-side pairs contiguous deletion/addition blocks in
  order, keeps hunks apart and leaves blanks for unmatched lines. Paths,
  content and explanatory notes are literal text. Long lines wrap within
  the diff, with contained horizontal scrolling for both sides on a phone.
  Old/new columns are display only: line comments always target the current
  file, never an old-only deletion; file comments remain available. Existing
  inline approvals and Changes previews/kept-version comparisons are unchanged.
- **Drafts survive closing.** Saved comments, the unfinished comment, overall
  note, layout, reading position and textarea selection are kept per task in
  tab memory across redraws, mode changes and closing/reopening. A failed
  refresh keeps the preceding diff; stale replies are discarded and late
  replies do not reopen a closed review. Only acceptance clears the submitted
  comments and note; reloading still loses drafts. No log or RPC field changes.
  Home's pending-approval Review links stay on the conversation.
- **Comments only in the first version.** Approving or rejecting a branch is
  not built. A change of direction is not a comment: it is a message the
  user writes to the task.

Tests: `TestChangedFilesSinceBase`, `...StopsAtTheTotalLimit`,
`...NotesAFileItCannotRead`, `...SaysWhatHasNoLines` and `...StopsWhenCancelled`
(prim); `TestWorktreeDiffReturnsChangesSinceBase`,
`...DoesNotHoldOtherMethods` (a held git filter), `...AnswersWithinAFrameForALargeBranch`,
`TestWorktreeReviewSendsOneBatchedMessage`, `...GroupsCommentsByFileAndLine`,
`...DoesNotSendFileContent`, `...RefusesACommentWithoutPathOrText` and
`...WaitsForARunToEnd` (rpc); `TestRemoteDevicesPairByNameAndCannotWidenPolicy`
(web, the allow list); and the browser check `worktrees`, which writes and
removes comments, holds a task on a question to see Send wait and the diff read
again, lists an orphaned comment, marks a changed line, checks the one request
and a closed task's review. The `worktrees` browser check also holds diff
and review replies to check loading, stale reads, failed-refresh retention,
drafts, refusal and acceptance-only clearing. It checks centre placement,
Home entry, closing/reopening, both modes, unequal replacements, multiple
hunks, literal text and current-file-only comments. `screens` exercises both
layouts on desktop and phone in light and dark, long lines, bounded scrolling
and the close action. These centre-layout checks fail on the preceding sidebar
implementation; the response guards and existing `transcript`, `approve` and
`firstrun` preview checks pass on both. Of the tests added with this round, `...StopsAtTheTotalLimit`,
`...NotesAFileItCannotRead` and `...SaysWhatHasNoLines` (prim), and
`...GroupsCommentsByFileAndLine`, `...DoesNotSendFileContent`,
`...DoesNotHoldOtherMethods` and `...AnswersWithinAFrameForALargeBranch` (rpc)
fail on the code before it; `...StopsWhenCancelled`,
`...RefusesACommentWithoutPathOrText` and `...WaitsForARunToEnd` guard what
was already right. `parseDiff` and `diffStats` skip `+++` and `---` only
before the first hunk, so an added line that starts with `++` keeps its number
and its place in the counts; `worktrees.js` checks the numbers.

## Amendment 2026-10-05: git-land, §4 as one step

"Commit and push to main" was 34 of the 145 messages in the 75 local session
logs of 2026-09-30 to 10-05, and each session did it through the shell its
own way: commit messages broke on shell quoting 22 times; signing failed on
the first commit of every glm session, since the shell's environment carries
no agent socket; a rebase hung ten minutes on a passphrase prompt
(20261004T063427Z-97214b45) and another ended in commit-tree and update-ref
by hand (20261004T063641Z-464f8142); models turned signing off
(20261004T024748Z-c7491879) or loaded the person's SSH key into an agent of
their own (20261004T053047Z-1b4c67dc); and a branch was pushed where main
was meant (20261004T035536Z-cd08f00d). §4 is built as one primitive,
`(git-land message)`, offered only in a worktree session, and the worktree
section tells the model to call it, after validate, when the request asks
for the work to land, never to commit, rebase or push through the shell, and
never to turn signing off. Clause by clause:

- `git-commit`: built inside git-land. It commits every change with the
  model's message, the files `fs-read` refuses left out and named. It runs
  git with the session's environment plus the person's agents
  (`SSH_AUTH_SOCK`, `GNUPGHOME`, `GPG_AGENT_INFO`) and every prompt off, so
  a signed commit is signed as the person's terminal would sign it, and a
  passphrase prompt fails at once. A signing failure stops it with that
  said; it never commits unsigned.
- `git-rebase`: built inside git-land. The session's own commits (those
  after the worktree's base commit, or after where the branch last met the
  base once the base has it) go onto the newest base: its upstream, fetched
  first, when it has one, so commits other sessions left on the local main
  are not published. A conflict stops it with the files named, the rebase
  left in progress; the model resolves them with fs-edit and calls git-land
  again, which refuses while conflict markers remain. There is no "abort":
  the person aborts from a terminal.
- `git-push`: built for the `"main"` target only, by fast-forward, never
  forced; pushing the branch itself is not built. The outgoing diff is
  scanned with L1 first, and a credential shape refuses the push (rather
  than asking: nothing in the step can say which text belongs).
- The local main moves only safely: built as decided (its ref by compare and
  swap when no checkout has it, `merge --ff-only` in a clean checkout,
  nothing in one with changes, each said).
- Hooks: a `core.hooksPath` inside the worktree refuses the landing rather
  than asking per call.
- "It needs no approval" is not built: git-land is irreversible and asks,
  as any such call does (the judge first, by default). The `push = [URL, …]`
  list in `.nodwyn/allow.toml` that would let it push unasked is not built,
  nor the `git.push` log record; the guard's own `guard.decision` and the
  tool result say what was pushed where.

## Amendment 2026-10-08: worktrees are the console default

The console's **In a worktree** choice in the ask bar is checked by default
for named workspaces the daemon identifies as git repositories. Unchecking
it starts the next task in that folder itself. The choice remains per-folder
for the current tab, so one folder may opt out without changing another.
Unknown and non-git folders remain on the ordinary task path by default, since
the console cannot safely open those in a worktree. This changes only
new task creation in the console; it does not change how existing tasks
resume or how other front-ends start tasks.

The `worktrees` browser check verifies both paths: a new git workspace starts
with the choice checked and opens through `worktree.open`; after unchecking,
a task opens through `session.open`. The two checked tasks still verify their
work remains isolated in separate worktrees.

## Amendment 2026-10-08: worktrees in the TUI

The TUI clause of the Decision is built, with `--worktree` from §1:

- The chooser's first row, selected at start, is "New session in a new
  worktree of <repo>" when `--repo` has its own `.git` (a folder, or the file
  a worktree has), so Enter twice gives the console's default. The row below
  opens in the folder itself. The `.git` check decides only what is offered:
  whoever can write the folder can make it look like a repository, and then
  the row asks core, which refuses a folder that is not a repository's top
  with a sentence, as `worktree.open` does. Nothing else reads it.
- `nodwyn tui --worktree` opens a new session in a worktree without the
  chooser, in local and daemon mode alike; with `--resume` it is refused,
  since a resumed session keeps its folder.
- The title shows the branch, the chooser lists a stored worktree session
  by its repository and branch, and the conversation shows `worktree.create`,
  `worktree.restore` and `worktree.remove` in the console's words, with the
  count of uncommitted files left out.
- Local mode makes the worktree through `core.Options.Worktree`, without the
  daemon's task-root refusal, as local mode has always opened any `--repo`
  without it. The worktree is inside a repository the local TUI could already
  open as a root, so it reaches nothing new. The refusal moving into core is
  still not built.

The TUI has no diff or review view, and is not to have one: a worktree is
a folder on the person's own computer, and in a terminal `git diff` and an
editor already read it, beside the TUI, at the path the conversation names.
A comment on the changes is a message to the task, which `worktree.review`
only formats. The console's Review exists because a browser, a paired
phone above all, cannot open the folder.

Not built: `--worktree-base`, and `--worktree` for line mode and `nodwyn run`.
`internal/frontend/tui/worktree_test.go` opens through the chooser and the
flag over both backends, against a real git repository.

## Amendment 2026-10-15: a worktree made again has the base it starts from (ADR 49)

A resumed task whose worktree close had removed is made again on a new
branch of the same name from its base branch's head as it is then, but the
log kept the commit the task first started from. Review (`worktree.diff`)
compares with that commit, so every commit `main` gained in between showed
as the task's own change, and close's check fell back to it when the base
branch was gone. A scheduled task, closed and resumed for every run (ADR
49), would meet this every day. `restoreWorktree` now resolves where the new
branch starts and returns that commit; resume records it as the worktree's
`base_commit` in `session.resume` and `worktree.restore`, and the store
reads the latest `worktree.restore`'s base. A worktree kept with its branch
keeps its base. `TestWorktreeMadeAgainHasTheBaseItStartsFrom` fails on the
code before: the base stayed the first commit.

## Amendment 2026-10-16: git-land commits only what it says it commits

`git-land` staged the files it judged by path and then ran `git commit`,
which takes the whole index: a credential file someone had already staged (a
`git add -A` through the shell is common) was committed and pushed, while the
answer named it as left out. It now stages everything, takes the refused
files out of the index with `git reset`, and commits nothing if `git diff
--cached` still names one. Its status read also asked for renames, which
gives a rename's two paths in one record; the second was read as a record of
its own and every staged rename failed. It reads status with `--no-renames`
and only standard output. The regressions are
`TestGitLandLeavesAStagedRefusedFileOut` and `TestGitLandLandsAStagedRename`.

## Amendment 2026-10-18: landing preserves Git's configured SSH command

Raven's SQLite Explorer task committed successfully but `git-land` could not
fetch its base. Its default `GIT_SSH_COMMAND=ssh -o BatchMode=yes` overrode
the checkout's `core.sshCommand`, losing the per-repository deploy key and
`-F` selection of Raven's SSH config. Default SSH then read Fedora's root-owned
system config; inside Raven's user namespace root is unmapped, so OpenSSH
rejected that file's apparent owner even though its host permissions were
correct. The configured Git transport worked in the same namespaces.

Landing now retains an explicit `GIT_SSH_COMMAND`, otherwise reads the
worktree's effective `core.sshCommand` through Git, and uses its noninteractive
default only when neither is set. Git's configuration is the person's own,
including the per-checkout config shared by linked worktrees; no project file
or model argument selects another credential. Existing prompt suppression and
signing behaviour remain. `TestGitLandUsesTheRepositoriesSSHCommand` performs
fetch and push against a temporary local bare repository through a fake SSH
transport, and fails on the old code because it chose the default instead.
