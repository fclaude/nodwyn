# Security

nodwyn exists to keep an agent from leaking your credentials, so I take reports
about it seriously. It's a one-person project, so I can't promise a response
time, but I'll try to answer within a week.

## Reporting a problem

Please don't open a public issue for a vulnerability. Use GitHub's private
reporting instead: on this repository's Security tab, choose **Report a
vulnerability**. Include what you did, what you expected and what happened, and
the version (`nodwyn version`). A failing test is the best report there is, and
`test/exfil` shows how the suite runs a scenario in a real session.

I'll work out a fix with you, and credit you in the release notes if you'd like.
Please give me a reasonable time to ship the fix before you write about it.

## What counts

These are the things nodwyn promises, so a way around any of them is a
vulnerability:

- A secret reaching the model, the session log, a front-end event or any
  destination it isn't bound to.
- The guard allowing a call that its policy, the denylist or a binding
  refuses.
- The keychain exposing a credential value, or its contents without the
  passphrase.
- A repository's own files (`.nodwyn/init.scm`, `allow.toml`) gaining authority
  before you've reviewed them with `nodwyn trust`.
- A paired device or a linked host doing more than it was granted.
- A console page running attacker-controlled script because session content was
  treated as markup.

## What doesn't

[docs/security.md](docs/security.md) lists these in full. The short version is
that nodwyn isn't a sandbox. A command you approve runs with your user's
privileges, and approved builds and tests execute project code. Redaction
recognises the shapes of credentials, so an unknown or encoded secret may not be
caught. Those are limits I document, and a report that only restates one of
them is a feature request.

## Supported versions

Fixes go into `main` and the latest release. There's no long-term support for
older ones.
