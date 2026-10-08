# ADR 0047: A task's judge is a choice, and Settings sets judges up

- Status: accepted; implemented 2026-10-08
- Date: 2026-10-08
- Amends: 46 (the judge is chosen per task; a plan's sign-in is no judge;
  its model is not typed), 41 (Settings is tabs; providers carry their
  models), 30 (a paired device chooses no judge)

## Context

ADR 46 made the judge a provider's: `[judge] server` names a Jev server or
an OpenAI one, and Settings › Judge chose between them. Using it showed
three faults, each in the logs of the person's own sessions:

- **An OpenAI judge did not work.** Settings offered every OpenAI server at
  `api.openai.com`, a ChatGPT plan's among them. The keychain sends a plan's
  sign-in only to `/v1/models` and `/v1/responses` (keychain `openai.go`),
  so every judge question on it failed with that refusal: three `jev.req`
  records on 2026-10-07 say so. ADR 46 had left that open ("the first answer
  would show"); it showed.
- **Setting up Jev took knowing `jev-latest`.** The judge's model was a free
  text field, and the server's name, `Jev`, typed there sent `"model": "Jev"`
  to typesafe.ai, which answered `400 Bad Request` (session `96faa033`).
- **Its price had to be typed.** nodwyn knows a judge's price by its host
  and model (`catalogue.JudgePrice`), so with the model wrong there was no
  price, and the person typed `0.042` into `[judge]`.

The person also asked for the judge to be chosen beside the model, for each
task as for the default, and for Settings to have a Judge tab of its own,
with the model settings revamped together.

## Decision

1. **A task's judge is a choice.** `core.Options.Judge` opens a task with a
   judge of its own: a `[servers.NAME]` whose provider answers typed
   questions, or none; left out, it is `[judge]`'s, as before.
   `Session.SetJudge` moves it between turns, refused while a turn runs or an
   attachment is added: a judge chosen mid-turn would judge half of it. Each
   choice is a `session.judge` record (server, provider, host, model, actor)
   written before the change, so the questions asked after it come after it
   in the log. Resuming restores the last record's judge; one `config.toml`
   no longer offers leaves the task with none, with a note saying why, never
   with `[judge]`'s, which would send its plans and commands somewhere nobody
   chose for it.
2. **The judge's primitives read the task's judge on every call.** They are
   registered when a judge could be asked at all (a keychain open,
   `judge.content` not `none`, `config.toml` naming a judge provider), since
   the primitive table is fixed for the session (ADR 9) while the judge is
   not. Each call asks the session (`prim.JudgeClient.JudgeOptions`) for the
   judge it has then, and fails, as no answer, when it has none. Their egress
   metadata, the host and key class the guard checks each call against, is
   moved to the new judge while the task is held idle
   (`prim.RetargetJudge`), so the guard never judges one destination while
   bytes go to another. `judge-available?` (hidden) says whether the task has
   a judge whose key is stored, which the plan gate asks. The judge's tools
   are offered to the model only at `judge.content = "full"` and only when
   the task starts with a judge it can ask: the tool list cannot gain them
   later.
3. **A ChatGPT plan's sign-in is no judge.** `core.CheckJudge` refuses it
   when a task opens with it or moves to it, and `settings.write` refuses it
   as `judge.server`, in a sentence that says the Decisions API takes an API
   key. `settings.read`'s new `judges` lists it with state `plan`, so the
   console can say what to add instead.
4. **The judge's model is not typed.** A judge asks its provider's judge
   model (`config.JudgeModels`: `jev-latest`, `gpt-6-luna`), which is all
   either API answers to, so Settings no longer has a field for it.
   `[judge] model` and `[judge] price_*` stay for a hand-written override
   and apply only on the server `[judge]` names, since they were set for it:
   a task that chooses another judge is not asked or priced as that one
   (`config.JudgeOn`). Settings says when `[judge] model` names a model its
   provider does not answer to, and puts it right in one click (clearing
   it). A price is offered only under a fold, as an override.
5. **Settings is tabs.** General (approvals, limits), Models, Judge, Context,
   Documents & containers, Keys & devices and This browser, each at
   `#/settings/<tab>`, so a link opens one and a reload keeps it; only the
   tab shown is drawn, and the save bar keeps edits from every tab.
   - **Judge** says what new tasks ask, chooses the default among the judges
     set up, and sets one up in place: Jev with its key alone, or an OpenAI
     API key, each with what it is and why nothing else is asked. A judge
     added when there is none becomes the default. Each judge is a card with
     its host, model, price and state; a Jev one's key is replaced and the
     provider removed there, since it carries no model.
   - **Models** lists each provider as a card with its models under it, with
     Add a model there, and the default for new tasks as Model · Effort ·
     Judge. A Jev server is no longer a model provider's preset or kind: it
     is a judge, under Judge. A provider form that needs a key while the
     keychain is locked has the keychain's form beside it.
6. **The judge is chosen beside the model.** The model picker has a Judge
   select wherever it is: New task (sent as `session.open`'s `judge` when it
   differs from the default), a conversation (`session.judge.set`, which waits
   for a running response to end) and Settings' default (`judge.server`). A
   judge that cannot be asked does not stop a task, since the person answers
   instead; the picker says why it cannot.
7. **A paired device chooses no judge.** The judge is shown a task's plan
   and commands, so where they go is this computer's choice, as a model's is
   (ADR 30, ADR 40): the bridge lists `session.judge.set` only for this
   computer, and the daemon refuses `judge` in `session.open` and
   `session.judge.set` from a device.
8. **The terminal client chooses too.** `:judge` shows the task's judge and
   the judges set up; `:judge NAME` and `:judge none` move it.

## Consequences

- An OpenAI judge works with an OpenAI API key, which Settings › Judge adds
  in one step; a person with only a ChatGPT plan is told what to add, not
  left with a judge that refuses every question.
- Setting Jev up takes its key, nothing else. A config that already says
  `model = "Jev"` is shown as wrong with its fix.
- Two tasks may ask different judges at once, and the log of each says
  which. `[judge]` is the default, no longer the only judge.
- The judge's primitives exist in more sessions than before (whenever a
  judge could be asked), and fail when the task has none; `(plan-judge?)`
  asks `judge-available?` rather than whether they exist.

## Not built

- Choosing a judge while a turn runs, queued for its end as a model is: it is
  refused, and the console's select waits.
- A judge for `nodwyn run` and line mode other than `[judge]`'s, and a
  `--judge-server` flag.
- Asking OpenAI's Decisions API for real: the tests ask a fake that speaks it
  as its guide documents. A person's first question with an API key is still
  the first evidence that it answers.
