# ADR 42: Gitea distributes binaries and books

Date: 2026-10-05. Status: accepted.

## Context

Gitea already runs the acceptance suite and portable builds. Downloadable
binaries and the books need a home independent of Git history. GitHub billing
is an obstacle to using it for releases, so Gitea is the primary service.

## Decision

1. Keep acceptance and publisher tests in the read-only Gitea check job.
   Publish only after it succeeds for `main`, on push or manual dispatch.
2. Use a separate job with release-write authority through the built-in job
   token. GitHub runs only on manual dispatch and publishes nothing.
3. Package Linux and macOS executables for amd64 and arm64 without cgo, with
   a Linux egress proxy of the matching architecture. Execute Linux amd64
   with the existing fake-model selfcheck; describe other targets as
   cross-compiled, and macOS as unsigned and not notarised.
4. Build both books from the same source commit in a scratch checkout. Attach
   both PDFs, the four archives, source metadata and SHA-256 checksums.
5. Create `build-<short commit>` releases as drafts. Publish only after all
   downloads match their checksums and source commit. Repair only that
   commit's unfinished draft on retry; never replace a published release.
6. Keep local promotion unchanged (ADR 13). Release tags identify distributed
   assets; promotion itself still creates no tag. PDF removal, history
   rewriting and release retention are deferred.

The files controlling the workflow and publisher can be written by repository
committers, including an author of a clone. Their contents must not gain
release authority from an untrusted pull request: the publication job runs
only for the trusted `main` ref after checks. The runtime agent's protected
paths include `.gitea/`, `.github/` and `scripts/` (ADR 17); a normal approved
write in `docs/` cannot change the publisher. The Gitea job token stays out of
source and model inputs. The publisher refuses redirects and other-origin
attachment URLs rather than forwarding that token.

## Consequences

Each successful main commit has a retrievable, commit-identified distribution.
The books and binaries share one provenance record. Building books requires
TeX packages on the release runner and adds time and disk use. A failed job
may leave a draft that a rerun completes. Checksums verify bytes but are not
an independent signature. Existing PDFs and tags still contribute to Git's
size until a separately authorised history cleanup.

## Amendment 2026-10-05: PDFs live in releases, not Git

The user authorised the deferred cleanup after the Wails migration. Stop
tracking generated PDFs and ignore their three historical paths:
`docs/book/nibb.pdf`, `docs/book/nodwyn.pdf` and `docs/expert/expert.pdf`.
Keep the LaTeX sources and generated source catalogue in Git. Local builds
still produce both PDFs, and the release builder still rebuilds and verifies
them from the same source as the binaries before publication.

Rewrite Gitea's branches and tags in an isolated mirror after a release has
proved the source-only build. First retain a complete bundle backup outside
the repository. Preserve all non-PDF file contents, modes, commit metadata
and parent relationships; keep commits that become empty. Verify every
rewritten tree against its original and save the commit map. Update only the
observed refs, atomically and with exact old-object leases, so concurrent work
causes a refusal rather than being overwritten.

Published release attachments and their recorded source IDs remain immutable.
Existing tag names point at their corresponding source with PDFs removed;
the saved map connects old release provenance to rewritten commits. Rewriting
changes object IDs and invalidates the old signatures, which remain available
only in the backup. Fresh clones receive the cleaned history; Gitea garbage
collection reclaims unreachable server objects separately. Other local
worktrees and GitHub are outside this cleanup. Release retention stays deferred.
