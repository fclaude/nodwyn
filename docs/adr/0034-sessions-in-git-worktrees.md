# ADR 0034: Sessions in git worktrees

- Status: **partly implemented** (location and git authority decided
  2026-10-02; a session in a worktree from the console built 2026-10-03,
  see the amendment)
- Date: 2026-10-01
- Amends: ADR 17 (no standing approval for commits or pushes) and plan §4
  B3 (the operator commits), in worktree sessions only

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
  opens there; the daemon refuses any other folder in a sentence.
- Not built: `worktree.list`, `worktree.remove`, `run.start`'s
  `worktree`, commits ahead and the last push, Remove, and the TUI's row and
  title.

The browser check `worktrees` starts two tasks in worktrees of one
repository from the ask bar. It checks the requests sent, where each
task's file landed, the sidebar's grouping and the branch shown. It also
checks that closing keeps a worktree with work in it. The core tests run
real git in temporary repositories: the removal rules, restore,
refusals, the link check and the shared project.

