# ADR 0059: A website on GitHub Pages, built from the docs

- Status: accepted; implemented 2026-10-23. Builds on 57.
- Date: 2026-10-23

## Context

The documentation was a pile of Markdown in the repository: a 3,300-line
manual written for people reading the code, a browser guide, the protocol and
the Scheme reference. That serves a contributor. Someone who wants to know what
the console looks like, how to run nodwyn as an app, or how to set it up on a
server and use it from a phone and a terminal, had nothing to read and nothing
to look at.

## Decision

1. **A site at `site/`, built by one Python script** (`site/build.py`, with
   `markdown` and `pygments`, pinned in `site/requirements.txt`) into
   `site/_out`, and published by `.github/workflows/pages.yml` to GitHub Pages.
   A pull request builds it, and the build fails on a link that goes nowhere
   (a page, an image, an anchor, or a repository file that isn't there). A push
   to `main` publishes it.
2. **A guide, written for the site** in `site/content`: install, the first
   run, a tour of the console, working in a task, approvals, models and keys
   and folders, schedules and workflows, running standalone, running on a
   server, the terminal clients, and how keys are handled. It leans on the
   console, and on the two ways to run it that a person has to choose between.
3. **A reference, which is the repository's docs as they are**: the manual,
   the console's own page, the protocol, the Scheme reference, the
   specification and every ADR are converted at build time, with links between
   them pointing at the pages they became and links to other repository files
   pointing at GitHub. The guide can't drift from them because it doesn't copy
   them.
4. **Screenshots are of synthetic data**, taken by `scripts/screenshots.sh`
   from the browser checks' demo (a scripted model, temporary folders) in both
   themes and at a phone's width, and kept as WebP under `docs/images`. The
   README shows one, and the site shows the pair that fits the reader's system.
   They are made by hand, when the console's look changes, and not on every
   build, so the build needs no browser.
5. **The site wears the console's colours and typefaces**, taken from the
   console's own style sheet, so a screenshot looks at home on the page.

## Consequences

The guide and the screenshots are the parts that go stale: a change to the
console's look wants `scripts/screenshots.sh` run and the images committed, and
a change to a behaviour the guide describes wants the guide changed. The
reference can't go stale. A repository that is private needs a plan with
Pages for private repositories until it is public. The site has no search yet.

## Not built

- A search box over the reference.
- Screenshots of the console linked to a second host (Hosts), which needs two
  daemons; the guide describes it in words.
- Versioned docs: the site is the docs of `main`.
