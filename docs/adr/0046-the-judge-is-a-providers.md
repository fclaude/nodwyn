# ADR 0046: The judge is a provider's

- Status: accepted; implemented 2026-10-06
- Date: 2026-10-06
- Amends: 19 (what the judge may see is `judge.content`), 20 (one set of
  judge primitives, with no handle, speaking the API of the judge's
  server), 41 (Jev is a provider; the judge's settings)

## Context

The judge was one service. `[jev]` gave it an address of its own
(`https://api.typesafe.ai` by default), and its calls sent whatever credential
of class `jev-key` the keychain held, added by hand or through the
Credentials page's Judge panel. A model's key, by then, was a provider's:
saved with a `[servers.NAME]` from Settings › Models, bound for its host
when it was saved, and shown with the other providers' keys (ADR 27, amended
2026-10-03; ADR 41).

OpenAI published a Decisions API
(developers.openai.com/api/docs/guides/decisions, public beta): `POST
/v1/decisions` with `gpt-6-luna`, asked typed questions about an input and
answering each with probabilities: a `predicate` (the probability of yes),
a `choice` among options, with each option's probability, and a `score`
over ordered levels, a probability-weighted index. These are the three
questions nodwyn asks Jev (`noul`, `choice`, `score`), in another wire
format. It charges $0.10 per million input tokens and nothing for output;
Jev charges $0.042. A person with an OpenAI API key already has the key it
takes.

The person asked for the judge to be one of several, and chose: an OpenAI
judge sends the OpenAI key a server already has, a Jev key is managed as a
model provider's for consistency, and `[jev]` becomes `[judge]`. nodwyn's
own repository is the only one with Scheme of its own, so the Scheme
surface may change where that makes it cleaner.

The Scheme surface had two sets of the same three questions: hidden
`jev-noul`, `jev-score` and `jev-choice`, taking the key's handle, for the
plan gate and user code, and the model's `judge-noul`, `judge-score` and
`judge-choice`, for which Go looked the key up itself (ADR 20, amended
2026-09-30). The handle was never a boundary: `credential` gives it to any
code that is not the model's, and the guard checks every call against the
binding table whoever holds it.

## Decision

1. **`[judge]` names a provider.** `[judge] server` is a `[servers.NAME]`
   whose provider is `jev` or `openai`; the judge's requests go to that
   server's address with its key, which its binding rows must allow there.
   Without a server there is no judge: no plan gate, and commands no
   standing approval covers are put to the person. The server must be
   configured and have a credential, and a server of another provider is
   refused, when the file loads. `model` defaults to the provider's judge
   model (`config.JudgeModels`: `jev-latest`, `gpt-6-luna`); `timeout`,
   `content` (ADR 19's levels), `approve_threshold` and the prices are
   `[jev]`'s, renamed. `[judge]` is in the user's `config.toml` only, as
   `[jev]` was: a repository cannot point the judge anywhere.
2. **`[jev]` is refused, not read.** A `config.toml` with the table does
   not load, and the message names what replaces it (a `[servers.jev]`
   with `credential = "jev-key"`, which keeps a stored key and its binding,
   and `[judge] server = "jev"`). There is one way to configure the judge.
3. **Jev is a provider kind.** `[servers.NAME] provider = "jev"` takes no
   dialect and carries no model: a model on it is refused when the file
   loads, the model forms leave it out, and its row offers no Test. Settings
   › Models' Add a provider has a Jev preset at `https://api.typesafe.ai`;
   its key is stored like any provider's, bound for `POST` alone (the
   judge's questions are its only requests, and Jev lists no models) at the
   bearer position. The Credentials page's Judge panel is gone: the judge's
   key is listed with the other providers' keys ("Provider keys"), used by
   "the judge" as well as by its server.
4. **The wire follows the server's provider.** The primitives return the
   same Scheme values whichever it is. For `jev` a question goes to `/v1/systemone` after the server's address,
   as before; for `openai` to `/decisions` after it (OpenAI's address
   carries `/v1`), as `{"model", "input", "questions": [{"type": "predicate"
   | "choice" | "score", "name": "q", "instructions", "choices" | "levels"}]}`.
   A choice's options are sent as `{value, description}` in name order, a
   rubric's levels as `{label, description}` labelled 1 to n, low to high.
   The answer is read back into System One's shape, which the handlers and
   `jev.req` keep; a `refusal` is an error, which callers already take as
   no answer (plan anyway, ask the person, or no in a judged run).
   Redaction, the binding check and `jev.req` are unchanged and cover
   every string either wire sends.
5. **One set of judge primitives, with no handle.** `judge-yes-no` (System
   One's noul, the Decisions API's predicate), `judge-score` and
   `judge-choice` replace both sets. Go injects the stored key of the
   judge's server and the guard judges each call as carrying its class, as
   the model's tools did. They are registered only when the session starts
   with that key stored, as the judge's server is fixed for the session,
   and they are the model's tools only at `judge.content = "full"`; below
   that they are hidden, for the plan gate, Go's judged approval and the
   user's Scheme. A tool the user registers may call them with the model's
   origin at any level, as it could with a handle its `init.scm` looked
   up: which judge-backed tools exist is the user's to decide (ADR 20 §3).
   The plan gate asks `(plan-judge?)` whether they are there. The terminal
   and the console show a judge request by its kind (`[judge choice: …]`);
   an older log's `jev-*` names show as they are.
6. **Settings › Judge chooses the provider** (`judge.server`, a
   `settings.write` setting, local only as the rest are). It offers Jev
   servers and OpenAI servers at `api.openai.com`, since another
   OpenAI-compatible server has no Decisions API, plus whatever the file
   already names. Choosing among configured providers decides where the
   judge's questions go, as choosing a default model decides where a
   task's go; the addresses and keys stay with `settings.provider.write`.
   A provider the judge is on is not removed. `settings.read` reports the
   judge's state as `judge_key`, `judge_key_detail`, `judge_host`,
   `judge_model` and `judge_published` (the `jev_*` fields are gone).
7. **The judges' prices are nodwyn's own.** `catalogue.JudgePrice` keeps
   the price of each judge's questions by host and model: Jev's as before,
   and `gpt-6-luna`'s at the Decisions API's $0.10 per million input
   tokens and nothing for output. It is not the catalogue's chat entry,
   which models.dev publishes for `gpt-6-luna` with an output price.

## Consequences

- A person with an OpenAI API key has a judge without a second account;
  one with Jev keeps it by adding the server the refusal names.
- The thresholds (`judge.approve_threshold`, the plan gate's
  `plan-needed-threshold` and `plan-accept-score`) were set against Jev's
  probabilities, and ADR 32's benchmark measured Jev. Another judge may put
  the same command higher or lower; nothing here measured `gpt-6-luna`,
  which would take paid requests.
- The `jev.req` log kind stays, with its Go names (`log.KindJevReq`,
  `log.JevReq`): every session log before this one carries it, and every
  reader of a log (the console, the terminal, `:budget`, the spend limit
  on resume, the optimizer's Scheme) would otherwise read both kinds. Its
  `tool` names the new primitives from now on. Jev's own wire keeps
  `noul`, its word.
- Tests ask a fake that speaks both APIs (`test/fakes/jev`, and the core
  tests' own), and the exfiltration suite covers the Decisions wire's
  redaction, binding check and refusal.

## Not built

- A chat model as the judge (an Ollama model, say). Its probabilities would
  come from logprobs, which Anthropic does not expose, or from a number
  the model states, neither calibrated like a typed-question API's.
- Asking several questions in one request, which both APIs take; nodwyn
  asks one at a time, about different text.
- The Decisions API was not called: its response's `usage` and `model`
  fields are not documented, so the judge's spend is counted when it
  reports `input_tokens` and unpriced otherwise, and whether a ChatGPT plan
  credential is accepted there is unknown (Settings offers that server; the
  first answer would show).
