# Live model checks

`test/live` runs nodwyn, built from this tree, against a real model endpoint
(ADR 63). Everything else in the repository uses fakes; this suite is the one
place a real model answers. It never runs in `make test`, `validate`,
promotion or GitHub: the `live` build tag keeps it out of every ordinary
build, and only `make live` or the Gitea workflow `.gitea/workflows/live.yml`
(nightly, and by hand) runs it.

## The endpoint

The suite reads the endpoint from its environment, never from a file here:

| Variable | What it is |
|---|---|
| `NODWYN_LIVE_PROVIDER` | `ollama`, `openai` (any chat-completions server) or `anthropic` |
| `NODWYN_LIVE_URL` | the address, with its version path (`https://host/v1`) |
| `NODWYN_LIVE_MODEL` | the model the scenarios ask |
| `NODWYN_LIVE_KEY` | its key; unset for a keyless server |
| `NODWYN_LIVE_DIALECT` | optional: a chat-completions dialect, or `responses` |
| `NODWYN_LIVE_NUM_CTX` | optional: the context to ask an Ollama model for, such as `163840`; without it nodwyn may ask for the model's largest, and Ollama loads it again at that size |
| `NODWYN_LIVE_REPORT` | where the report goes (default: a temporary directory) |

In Gitea, the address and the key are repository secrets and the rest
repository variables (Settings › Actions); the workflow does nothing until
`NODWYN_LIVE_PROVIDER` and `NODWYN_LIVE_MODEL` are set.

nodwyn never sees the address: each scenario's nodwyn talks to a proxy on
127.0.0.1 that forwards to the endpoint and keeps every request body, so a
scenario can check what left the machine. The key goes into a keychain made
for the scenario, without a passphrase, in a home directory that is deleted
with it, bound to 127.0.0.1 alone. What the suite prints and keeps (the
report, the logs of failed scenarios) has the address, its host and the key
replaced by `[live url]`, `[live host]` and `[live key]`; Gitea also masks
the secrets in the job's log.

## Running it

```sh
NODWYN_LIVE_PROVIDER=ollama NODWYN_LIVE_URL=http://workshop:11434 \
NODWYN_LIVE_MODEL=qwen3:8b make live
```

One scenario: `go test -tags live -count=1 ./test/live/ -run TestARunFixesAFailingTest`.
The scenarios run one at a time; a model that takes minutes a turn takes the
suite most of an hour.

## What it checks

Each scenario judges what nodwyn did (the files, the session's log, what
went through the proxy), never the model's wording:

| Scenario | It passes when |
|---|---|
| `chat-reads-and-resumes` | line mode asked for a number written in a file answers with it, having read the file, and the task resumed in a new line mode answers it again from the conversation |
| `run-fixes-a-test` | `nodwyn run --yolo --unattended` on a Go module with a failing test leaves `go test ./...` passing, without changing the test, and the run ends completed |
| `declined-command` | with every confirm declined, a file the model is told to remove is still there |
| `credential-stays-local` | a GitHub-token-shaped secret in a file the model is asked to read never appears in a request to the endpoint, in line mode's output or in the session log |
| `run-stops-at-its-limit` | an unattended run with more work than `--max-tool-calls 3` allows makes no more than three calls a turn and ends (skipped when the model makes none) |
| `schedule-message` | a task's schedule, once at a time a minute ahead, runs its message when the daemon's scheduler reaches it, not by hand, and the file it asks for holds the word it names |
| `schedule-program` | a schedule every five minutes runs its Scheme program when due: the program's own write, then a model turn it asks for, whose answer is the program's result; the schedule is removed after one run |
| `workflow-message` | a workflow on a command source records the item the source has when it begins and does not act on it; a new item the command prints at a later poll runs the message in a task, with the event attached, and the file it asks for holds the item's id and the word its body gives |
| `workflow-program` | the same trigger runs a Scheme program instead: its own write, and a model turn that reads the attached event, whose answer the program writes |

The schedule and workflow scenarios go through the daemon's socket as the
console does, and wait for its own scheduler, which ticks every 30 seconds,
rather than running anything by hand; they take a few minutes each.

The report (`report.md`, `report.json`) gives each scenario's result, time,
requests, tokens and how its turns ended, so a model's drift shows from run to
run. A failed scenario's session log is kept beside it, scrubbed.
