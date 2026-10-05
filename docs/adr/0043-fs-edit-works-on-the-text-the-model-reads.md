# ADR 43 — fs-edit works on the text the model reads

Status: accepted, implemented 2026-10-05 (amends 25 and 38)

## Context

Ingress redaction (ADR 25) shows the model a credential-shaped text as
`[REDACTED <class>]` wherever it reads one: a file, a search, a diff, its own
earlier words. The scanner knows a shape, not whether the text is real, so a
fake key in a test fixture is hidden as a real one is.

`fs-edit` matched `old` against the file's own text. A line with a credential
on it therefore could not be edited: `old`, copied from what the model read,
held a placeholder the file did not. A chat on a coding task (session
`08490dd4`, 2026-10-05) spent five minutes and thirty calls failing to change
one fixture line, answering "old text not found" each time and asking the
person to approve a security confirm for each try. The model could not tell
that the file already held the key it was trying to write, because it read the
same placeholder before and after. ADR 18's progress guard now stops such a
turn; this decision removes its cause.

## Decision

1. **`old` is matched in the text the model reads.** `fs-edit` redacts the
   file as ingress does (the default scanner, never an allowlist) and finds
   `old` there; a match is a span of the file, found by the offsets redaction
   reports. A file with no credential in it is its own view, so nothing changes
   for it. `replace_all` and the uniqueness rule count matches in the view,
   so a placeholder the file holds as text and one redaction wrote, which read
   the same, are two places to the model. A match that begins or ends inside a
   placeholder is refused.
2. **A placeholder in `new` keeps what it stands for.** When the matched span
   held a credential, `new` has either the span's placeholders, the same
   classes in the same order, and each keeps its credential, or none, and the
   credentials are dropped. Any other `new` with placeholders is an error, for
   a placeholder says which class and not which credential, and pairing them
   any other way could copy, reorder or half keep one. A span with no
   credential takes `new` as text, as every edit did.
3. **What is not found in the view is looked for in the file, as before.** An
   `old` the model typed that equals the file's own text still matches, so
   nothing that worked stops working, and the lookup tells the model nothing it
   could not try anyway.
4. **The guard is unchanged.** It reads `new`, not the file the edit makes
   (`TextSecret`, payload role). A kept credential is not in `new`, so it asks
   nothing; one the model writes is, and is a security confirm that no standing
   approval waives, as before. The denylist and the repository pin judge the
   path before the file is read, so a placeholder is no way into a credential
   file.
5. **Errors are written from the view.** A not-found error that quotes lines
   (ADR 38's whitespace hint) quotes them as the model reads them, so it carries
   placeholders and never a credential. An `old` that holds credential-shaped
   text is a guess at what the file holds, and the error says to write the
   placeholder in its place.
6. **The tool's description says so.** `fs-edit`'s description tells the model
   that a credential reads as a placeholder, that the placeholder in `old`
   matches whatever the file holds, and that it goes in `new`, in order, to keep
   it. This is a rule in what the model reads, and as ADR 32 asks, it names the
   failure it comes from: the session above.

## Why it leaks nothing

No credential reaches the model, a request, the log or a frontend: the view is
made inside the primitive, and its errors and the guard's diff are redacted as
every other event is. A credential cannot leave its file: `new` can keep it in
place or drop it, never copy it, move it to another file or reorder it
(clause 2). The match cannot be used to read a credential, for the view holds
no characters of it; guessing one by `old` text is the oracle clause 3 keeps,
no cheaper than before. Everything the guard decides, it decides as before.

## Not decided here

- An exact-value list of known-fake credentials (provider-published examples,
  a repository's fixtures) that ingress would leave unredacted would let the
  model read and write fixtures and stop their security confirms. It is safe for
  secrecy, since the only value that passes is one its writer already knows, but
  it relaxes ADR 25's rule that the log holds no credential-shaped text, and it
  needs its own decision. Matching by appearance (`EXAMPLE`, `test`) or by
  length is not safe: a command can make a real key look fake or short.
- A model cannot write the literal text `[REDACTED <class>]` over a credential
  it matched through a placeholder, since that means "keep". `fs-write` of a
  whole file cannot keep a credential at all.

## Consequences

- `internal/prim/editview.go` holds the view; `applyEdit` uses it, and the
  preview and the handler share it, as ADR 38 has them share `applyEdit`.
- Tests: `internal/prim` (the placeholder rules, with each refusal), `internal/core`
  (a model reads, keeps and replaces a credential through the guard and the
  loop, and neither a request nor the log holds it), `test/exfil` (a scenario
  that a denied file stays denied, and a test that an edit and its refusals leak
  a canary nowhere).
