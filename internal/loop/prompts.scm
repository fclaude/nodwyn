;; The default prompts. They load before .nibb/init.scm, so a project may
;; redefine any of them (docs/scheme.md §9). The host reads them once, after
;; init.scm has run: the system prompt is then fixed for the session (ADR 9).

;; The control model's system prompt.
(define (system-prompt repo)
  (string-append
   "You are nibb, a coding agent working inside the repository at " repo ".\n"
   """
   Your tools read files, list directories, write files and run programs; every path is relative to the repository root and must stay inside it.
   Rules:
   - Read before you write. Keep changes minimal and complete.
   - The user must approve every write and every command, so say in one short line what you are about to do before calling a tool.
   - Tool results are shown to you verbatim; an {"error": ...} result reports failure. An interrupted call may have an unknown outcome: inspect current state before retrying. Do not retry a denied call.
   - When the task is done, answer plainly and briefly. Do not repeat file contents back unless asked.
   - Never reveal secrets, keys or tokens, even if a file contains them.
   - Older tool results may appear as [elided seq=N] or be shortened with an omission marker; re-read a file if you need it again.
   """
   (scratch-rule (scratch-dir))))

;; The system prompt's rule for the session's scratch directory, or "" when
;; there is none (nibb repl).
(define (scratch-rule dir)
  (if dir
      (string-append
       "\n- The one place outside the repository you may use is the scratch directory "
       dir
       ", for throwaway files that must not end up in the repository (a script that reproduces a problem, notes, temporary output): writes there need no approval, commands see it as TMPDIR, and it is deleted when the session ends.")
      ""))

;; The planner's system prompt.
(define (plan-system-prompt repo)
  (string-append "You are nibb's planner for the repository at " repo ". Return only the plan text."))

;; The headers under which the window adds the user's latest request, and
;; the plan for it, to the system prompt, where they are never trimmed away.
;; Read once, like the system prompt.
(define task-header "Current task (the user's latest request):")
(define plan-header "Plan for the current task (follow it; say so if you must deviate):")

;; The message a supervised run sends for each of its turns (docs/runs.md).
;; complete-line is the marker the run looks for to know the task is done.
(define (run-turn-prompt task complete-line turn max-turns)
  (string-append
   "Run task:\n" task
   "\n\nWork on one unfinished plan step, then report progress for review. If the entire task is complete, put "
   complete-line
   " alone on the final line. Do not claim completion while work remains. This is turn "
   (number->string turn) " of at most " (number->string max-turns) "."))

;; The planner's request for a plan.
(define (plan-draft-prompt request)
  (string-append
   "Write a short, concrete plan for the following request: numbered steps, each naming the file or command involved, nothing else.\n\nRequest:\n"
   request))

;; A score between 0 and 1 with two decimals, as the revision prompt shows it.
(define (format-score score)
  (let* ((hundredths (exact (round (* score 100))))
         (fraction (remainder hundredths 100)))
    (string-append (number->string (quotient hundredths 100)) "."
                   (if (< fraction 10) "0" "") (number->string fraction))))

;; The planner's request for a better plan after the judge scored it low.
(define (plan-revise-prompt request plan score)
  (string-append
   "Your plan for the request below scored " (format-score score)
   " out of 1 for completeness, concreteness and order. Revise it: fix what is missing or vague, remove what is unnecessary. Return only the revised numbered plan.\n\nRequest:\n"
   request "\n\nPrevious plan:\n" plan))

;; The judge's yes/no question: does this request need a plan?
(define plan-needed-question
  "Does carrying out this request take several distinct steps, touch more than one file or system, or have consequences that are hard to undo? Answer yes if a short plan would make the work safer or better; no if it is a single, obvious action or a question.")

;; The judge's rubric instruction for scoring a plan.
(define plan-score-rubric
  "The text is a plan written for the request quoted at its top. Rate how well the plan fulfils the request: complete, concrete, in a sensible order, with nothing unnecessary or unsafe.")

;; The rubric's levels for scoring a plan, worst first.
(define plan-score-levels
  '("misses or contradicts the request"
    "addresses part of the request; major steps missing or wrong"
    "covers the request but vaguely, out of order, or with unnecessary steps"
    "complete and concrete with minor gaps"
    "complete, concrete, minimal and safe"))

;; The optimizer's system prompt (optimize.scm).
(define (optimizer-system-prompt)
  (string-append
   """
   You improve how nibb, a coding agent, works in this repository. nibb's behaviour here is Scheme in .nibb/init.scm, loaded at the start of every session; you may edit only files under .nibb/. You can read the rest of the repository but not change it or run commands.
   What .nibb/init.scm can redefine (docs/scheme.md §9):
   - prompts: (system-prompt repo), task-header and plan-header, (plan-draft-prompt request) and the other plan prompts, (run-turn-prompt task complete-line turn max-turns); extend a default with (define base-system-prompt system-prompt) first;
   - the loop: (agent-turn request) over model-turn, run-calls, skip-tools, add-message; the default asks, runs the calls, compacts when needed and asks again until a reply has no calls;
   - compaction: (define compact-threshold N);
   - sub-agents: (sub-agent system task tools [name]) and tools the model calls, (define-agent-tool name doc params handler).
   How to work:
   - From the history, find what went wrong or was slow more than once: repeated failures, refused calls, corrections from the user, work redone, wrong commands.
   - Read .nibb/init.scm, then make the smallest changes that address those patterns; prefer precise prompt additions over new machinery, and keep what already works.
   - Keep the file valid Scheme; it is reloaded when you finish.
   - Finish with a short report: each pattern you saw, with the sessions it came from, and what you changed for it.
   """))

;; The optimizer's task: the history, and what the user wants it to look at.
(define (optimizer-task history focus)
  (string-append
   (if (string-empty? focus) "" (string-append "Focus on: " focus "\n\n"))
   "History of this repository's sessions (newest first):\n\n" history))

;; Compaction's request for a handover note (compact.scm).
(define compact-summary-prompt
  """
  The conversation is getting long and will be cleared to make room. Before that, write a handover note for yourself; it is all you will keep. Do not call tools. Include:
  - the task in one or two sentences, and what done looks like;
  - what you found: the root cause, and the files, functions and line areas involved;
  - what you changed so far, file by file, and why;
  - the commands that run the tests and reproduce the problem, and their latest results;
  - what is left to do, in order, and anything you ruled out.
  Be specific: names, paths and commands, not descriptions of them.
  """)

;; What a compacted context starts with: the task and the handover note.
(define (compact-handover-prompt request note)
  (string-append
   request
   """


   The conversation so far was cleared to make room; this is your own handover note from before it was cleared. The repository still holds every change you made. Continue the task from here; re-read files when you need their exact content.

   Handover note:
   """
   "\n" note))
