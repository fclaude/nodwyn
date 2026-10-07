// The transcript's runs of commands, drawn from events written here rather
// than from a scripted model: the order of reads and commands, a run's
// header while a command runs, waits for an answer and fails, and a clock
// that stops when its transcript leaves the page, the context gauge
// following a conversation moved to another model, and the task side's
// tabs keeping their choice through an event's redraw. The console's own
// module is imported from the demo's console, so this is the code people
// run. The waits of 1.3 s in the page are what is tested: the run's
// one-second clock.
const { start, line, launch, watch, check } = require("../lib");

check(async () => {
  const demo = start(process.env.DEMO, ["-pace=0"]);
  const [url] = await line(demo, /URL (\S+)/);
  const base = url.split("/#")[0];
  const browser = await launch();
  const page = watch(await browser.newPage({ viewport: { width: 1280, height: 900 } }));
  // Any file of the console puts the page on its origin, where the module
  // and the ones it imports load as they do in the console.
  await page.goto(`${base}/transcript.js`);
  const results = await page.evaluate(async () => {
    const { Transcript, contextGauge } = await import("/transcript.js");
    // The console's own stylesheet, so the rows below are measured as the
    // console lays them out; this page otherwise holds no styles.
    await new Promise((ok) => {
      const css = document.createElement("link");
      css.rel = "stylesheet";
      css.href = "/style.css";
      css.onload = ok;
      document.head.append(css);
    });
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    const fresh = () => {
      const root = document.createElement("div");
      document.body.replaceChildren(root);
      return { root, t: new Transcript(root) };
    };
    let n = 0;
    const call = (t, name, args) => { const id = `c${++n}`; t.render({ kind: "tool.call", payload: { id, name, args } }); return id; };
    const done = (t, id, name, value, ms, ok = true) => t.render({ kind: "tool.result", payload: ok ? { id, name, ok, value, ms } : { id, name, ok, error: value, ms } });
    const read = (t, path) => done(t, call(t, "fs-read", { path }), "fs-read", "text", 3);
    const run = (t, cmd, status, ms) => done(t, call(t, "shell-exec", { cmd, args: [] }), "shell-exec", JSON.stringify({ status, stdout: "", stderr: "" }), ms);
    const header = (root) => { const g = [...root.querySelectorAll(".activity.commands")].pop(); return g ? `${g.querySelector(".label").textContent} | ${g.querySelector(".what").textContent}` : null; };
    const out = {};

    // The screenshot's routine judge, context, harness and tool activity is
    // one closed row, not a stack of separate disclosures and token lines.
    async function activityCollapsePreservesContent() {
      const { root, t } = fresh();
      root.className = "transcript";
      t.render({ kind: "user.msg", payload: { text: "review all setting options against the documentation, what makes and what doesn't make sense anymore?" } });
      t.render({ kind: "jev.req", payload: { tool: "judge-yes-no", ok: true, model: "fake", answer: "proceed", usage: { input_tokens: 338, output_tokens: 20 } } });
      t.render({ kind: "context.narrow", payload: { system: "Focus on settings." } });
      t.render({ kind: "harness.msg", payload: { text: "Review the settings and their documentation." } });
      for (let i = 0; i < 14; i++) read(t, `docs/settings-${i}.md`);
      for (let i = 0; i < 13; i++) done(t, call(t, "fs-find", { path: "docs", glob: "*.md" }), "fs-find", "found settings", 2);
      for (let i = 0; i < 9; i++) done(t, call(t, "fs-list", { path: "docs" }), "fs-list", "settings.md", 2);
      for (let i = 0; i < 7; i++) t.render({ kind: "llm.resp", payload: { usage: { prompt_tokens: i === 6 ? 13086 : 13000, output_tokens: i === 6 ? 170 : 150, reasoning_tokens: i === 0 ? 32 : 0 } } });
      const fold = root.querySelector(".activity-fold");
      const body = fold?.querySelector(".activity-fold-body");
      const initial = {
        folds: root.querySelectorAll(".activity-fold").length,
        agents: root.querySelectorAll(".turn.agent").length,
        closed: fold ? !fold.open : false,
        hidden: body ? !body.checkVisibility() : false,
        rows: [...root.querySelectorAll(".turn.agent > :not(.who)")].map((n) => n.className),
        kept: ["Asked the judge (yes-no)", "Started a focused context", "nodwyn told the model", "read 14 files", "searched 13 times", "listed 9 folders", "91,086 → 1,070 tokens"].every((text) => body?.textContent.includes(text)),
      };
      fold?.querySelector("summary").click();
      const expanded = !!fold?.open && !!body?.checkVisibility();
      const nested = body?.querySelector(".harness-note");
      nested?.querySelector("summary").click();
      const harnessVisible = !!nested?.open && nested.querySelector("pre").checkVisibility();
      read(t, "live-setting.md");
      const live = fold === root.querySelector(".activity-fold") && !!fold?.open && body?.textContent.includes("live-setting.md") && fold.querySelector("summary").textContent.includes("read 15 files");
      fold?.querySelector("summary").click();
      read(t, "another-setting.md");
      const collapsedLive = !!fold && !fold.open && !body.checkVisibility() && body.textContent.includes("another-setting.md");
      t.render({ kind: "stream.text", payload: { Text: "The answer stays visible." } });
      const stream = root.querySelector(".streaming");
      const streamingVisible = stream?.checkVisibility() && !stream.closest(".activity-fold");
      t.render({ kind: "llm.resp", payload: { content: "The complete answer stays visible.", usage: { prompt_tokens: 20, output_tokens: 5 } } });
      const reply = root.querySelector(".prose");
      const replyVisible = reply?.textContent.trim() === "The complete answer stays visible." && reply.checkVisibility() && !reply.closest(".activity-fold") && !fold?.open;
      read(t, "after-reply.md");
      const separated = root.querySelectorAll(".activity-fold").length === 2 && root.querySelectorAll(".activity-fold")[1].previousElementSibling === reply;
      t.render({ kind: "turn.end", payload: { outcome: "completed" } });
      t.render({ kind: "user.msg", payload: { text: "Next turn" } });
      read(t, "next-turn.md");
      const boundary = root.querySelectorAll(".turn.agent").length === 2 && root.querySelectorAll(".activity-fold").length === 3;
      t.reset();
      read(t, "after-reset.md");
      const reset = root.querySelectorAll(".activity-fold").length === 1 && !root.textContent.includes("next-turn.md");
      t.end();
      return { initial, expanded, harnessVisible, live, collapsedLive, streamingVisible, replyVisible, separated, boundary, reset };
    }
    out.activityCollapsePreservesContent = await activityCollapsePreservesContent();
    // A failure is counted on the closed Activity row, where it is seen, and
    // its run or group inside opens; opening the row for it showed the whole
    // stretch whenever a grep found nothing. A question leaves the row
    // closed, before and after the answer: its card at the end of the
    // conversation asks it. A turn that stopped short, a worktree that left
    // files out and an error stay outside the row.
    {
      const row = (root) => {
        const fold = root.querySelector(".activity-fold");
        const failed = fold?.querySelector(".activity-fold-failed");
        return { open: fold?.open ?? null, failed: failed?.checkVisibility() ? failed.textContent : "", what: fold?.querySelector(".activity-fold-what").textContent };
      };
      const visible = (root, text) => [...root.querySelectorAll("summary")].some((s) => s.textContent.includes(text) && s.checkVisibility());
      const a = fresh();
      for (let i = 0; i < 3; i++) read(a.t, `a${i}.go`);
      run(a.t, "grep", 1, 20);
      const command = { ...row(a.root), run: a.root.querySelector(".activity.commands").open, call: a.root.querySelector(".activity.commands .call").open };
      const b = fresh();
      done(b.t, call(b.t, "fs-read", { path: "missing.md" }), "fs-read", "not found", 2, false);
      const readFailure = row(b.root);
      const c = fresh();
      done(c.t, call(c.t, "fs-edit", { path: "missing.md" }), "fs-edit", "old text not found", 2, false);
      const write = { ...row(c.root), group: c.root.querySelector(".activity.writes").open };
      const d = fresh();
      const asked = call(d.t, "shell-exec", { cmd: "rm", args: ["x"] });
      d.t.waiting(true);
      const asking = row(d.root);
      d.t.waiting(false);
      done(d.t, asked, "shell-exec", JSON.stringify({ status: 0, stdout: "", stderr: "" }), 30);
      const answered = row(d.root);
      const e = fresh();
      read(e.t, "ok.md");
      e.t.render({ kind: "error", payload: { Err: "provider unavailable" } });
      const audit = visible(e.root, "Stopped: provider unavailable");
      const f = fresh();
      done(f.t, call(f.t, "shell-start", { cmd: "false", args: [] }), "shell-start", "refused", 2, false);
      const other = row(f.root);
      const g = fresh();
      read(g.t, "a.go");
      g.t.render({ kind: "turn.end", payload: { outcome: "spend_limit" } });
      const stopped = visible(g.root, "Stopped at the spend limit");
      // Where the work had got to is said beside the stop, in view; a stop
      // that carries none (an older log) draws none. Each is read before the
      // next fresh(), which replaces the page's transcript.
      const where = fresh();
      read(where.t, "a.go");
      where.t.render({ kind: "turn.end", payload: { outcome: "hard_budget", error: "tool_calls budget reached", where: "plan: 1 of 4 steps marked, next is step 2 (Test: x); files written this turn: a.go; last command: go test ./... (exited 1)" } });
      const whereShown = visible(where.root, "Where it stopped: plan: 1 of 4 steps marked, next is step 2 (Test: x); files written this turn: a.go; last command: go test ./... (exited 1)");
      const wherePlain = fresh();
      read(wherePlain.t, "a.go");
      wherePlain.t.render({ kind: "turn.end", payload: { outcome: "hard_budget", error: "tool_calls budget reached" } });
      const whereSaid = { shown: whereShown, absent: !wherePlain.root.textContent.includes("Where it stopped") };
      const w = fresh();
      w.t.render({ kind: "worktree.create", payload: { branch: "nodwyn/a", base: "main", base_commit: "abcdef0", uncommitted: 3 } });
      w.t.render({ kind: "worktree.create", payload: { branch: "nodwyn/b", base: "main", base_commit: "abcdef0" } });
      const worktree = { leftOut: visible(w.root, "3 uncommitted files are in the repository but not here"), clean: visible(w.root, "on nodwyn/b") };
      // A call skipped without running (an explorer out of rounds answers its
      // last calls so) is a note behind the row that says why, not a failure
      // counted or drawn outside it; a result that has no call and did fail
      // still is.
      const sk = fresh();
      read(sk.t, "ok.md");
      const skip = (id, name, why) => sk.t.render({ kind: "tool.result", payload: { id, name, ok: false, error: `Not executed: ${why}`, ms: 0 } });
      skip("s1", "fs-read", "this agent is out of rounds");
      skip("s2", "fs-find", "this agent is out of rounds");
      skip("s3", "fs-edit", "an earlier call in this batch failed");
      const skipped = {
        ...row(sk.root),
        outside: sk.root.querySelectorAll(".turn.agent > .audit").length,
        bad: sk.root.querySelectorAll(".audit.bad").length,
        notes: [...sk.root.querySelectorAll(".activity-fold-body > .audit")].map((n) => `${n.querySelector("summary").textContent} | ${n.querySelector("pre").textContent}`),
      };
      const lost = fresh();
      lost.t.render({ kind: "tool.result", payload: { id: "x", name: "fs-read", ok: false, error: "disk full", ms: 0 } });
      const stray = { outside: lost.root.querySelectorAll(".turn.agent > .audit.bad").length, summary: lost.root.querySelector(".audit.bad summary")?.textContent };
      for (const { t } of [a, b, c, d, e, f, g, w, sk, lost]) t.end();
      out.foldExceptions = { command, readFailure, write, asking, answered, audit, other, stopped, whereSaid, worktree, skipped, stray };
    }

    // Reads and commands that alternate keep their order: each read ends a
    // run of commands and each command a group of reads.
    {
      const { root, t } = fresh();
      read(t, "a.go");
      run(t, "go", 0, 100);
      read(t, "b.go");
      run(t, "go", 0, 100);
      out.order = [...root.querySelectorAll(".turn.agent .activity")].map((g) => (g.classList.contains("commands") ? "commands" : "reads"));
    }
    // Commands in a row are one run, with a row each, and a finished run
    // says how many ran and for how long together.
    {
      const { root, t } = fresh();
      run(t, "gofmt", 0, 400);
      run(t, "go", 0, 1200);
      out.consecutive = { runs: root.querySelectorAll(".activity.commands").length, rows: root.querySelectorAll(".activity.commands .call").length, header: header(root) };
    }
    // A running command's run counts up, and stops when it finishes.
    {
      const { root, t } = fresh();
      const id = call(t, "shell-exec", { cmd: "sleep", args: ["2"] });
      const first = header(root);
      await sleep(1300);
      const later = header(root);
      done(t, id, "shell-exec", JSON.stringify({ status: 0, stdout: "", stderr: "" }), 1500);
      const finished = header(root);
      await sleep(1300);
      out.running = { first, later, finished, still: header(root), clock: t.clock };
    }
    // A command waiting for an answer is not running: its run and its row
    // say it waits, through the clock's ticks, until the answer comes.
    {
      const { root, t } = fresh();
      run(t, "ls", 0, 50);
      call(t, "shell-exec", { cmd: "rm", args: ["x"] });
      t.waiting(true);
      await sleep(1300);
      const waiting = header(root);
      const row = [...root.querySelectorAll(".activity.commands .call .stat")].pop().textContent;
      t.waiting(true);
      t.waiting(false);
      out.waiting = { waiting, row, answered: header(root) };
    }
    // A failed or refused command opens its row and the run around it.
    {
      const { root, t } = fresh();
      run(t, "go", 1, 300);
      const g = root.querySelector(".activity.commands");
      const failed = { header: header(root), run: g.open, row: g.querySelector(".call").open };
      const r2 = fresh();
      done(r2.t, call(r2.t, "shell-exec", { cmd: "rm", args: ["x"] }), "shell-exec", "denied: protected path", 2, false);
      const g2 = r2.root.querySelector(".activity.commands");
      out.failed = { failed, refused: { header: header(r2.root), run: g2.open, row: g2.querySelector(".call").open } };
    }
    // A long command line stays inside its run: the row truncates with an
    // ellipsis and the status beside it holds the card's right edge, instead
    // of the line running past the card and a horizontal scrollbar with it.
    {
      const { root, t } = fresh();
      // The transcript as a task page holds it at this viewport: the main
      // column, the aside open, the console's 48px padding each side.
      root.className = "transcript";
      root.style.width = "936px";
      const command = "bash -c go test ./internal/frontend/rpc/ -run TestSessionModelSet -count=1 2>&1 | tail -n 20";
      const id = call(t, "shell-exec", { cmd: "bash", args: ["-c", command] });
      t.render({ kind: "guard.decision", payload: { origin: "model", decision: "confirm", scope: "all" } });
      done(t, id, "shell-exec", JSON.stringify({ status: 1, stdout: "", stderr: "" }), 1200);
      const card = root.querySelector(".activity.commands");
      const name = card.querySelector(".call .name");
      const stat = card.querySelector(".call .stat");
      const cardBox = card.getBoundingClientRect();
      out.longCommand = {
        status: stat.textContent,
        nameClipped: name.scrollWidth > name.clientWidth,
        nameInside: name.getBoundingClientRect().right <= cardBox.right - 11,
        statInside: stat.getBoundingClientRect().right <= cardBox.right - 11,
        scroll: root.scrollWidth <= root.clientWidth + 1,
      };
    }
    // What a turn's blocks are, in order: a reply's text, a group of reads,
    // writes ("Edited"), commands, the token line and the model's marks.
    // Flatten only the new display fold; the existing groups keep their
    // original bodies and order behind it.
    const blocks = (nodes) => [...nodes].flatMap((n) => n.classList.contains("activity-fold") ? blocks(n.querySelector(".activity-fold-body").children) : [n]);
    const shape = (root) =>
      blocks(root.querySelector(".turn.agent").children).filter((n) => !n.classList.contains("who")).map((n) => {
        if (n.classList.contains("activity")) return n.classList.contains("writes") ? "writes" : n.classList.contains("commands") ? "commands" : "reads";
        if (n.classList.contains("prose")) return `prose:${n.textContent.trim().slice(0, 12).trim()}`;
        return n.classList.contains("usage") ? "usage" : n.classList.contains("audit") ? "marks" : n.className;
      });
    const edit = (t, path, ok = true) => done(t, call(t, "fs-edit", { path, edits: [{}] }), "fs-edit", ok ? "ok" : "old text not found", 14, ok);
    const say = (t, content, usage) => t.render({ kind: "llm.resp", payload: { content, usage } });
    // Writes in a row fold into one Edited group, with a row each, and the
    // header counts distinct files. The model's line of narration between two
    // writes goes into the group, in order; the usage line stays last.
    {
      const { root, t } = fresh();
      say(t, "Making the change.");
      edit(t, "task.js");
      say(t, "Now the CSS:");
      edit(t, "style.css");
      say(t, "And the comment:");
      edit(t, "task.js");
      say(t, "", { prompt_tokens: 100, output_tokens: 10 });
      const g = root.querySelector(".activity.writes");
      out.clubbed = {
        shape: shape(root),
        header: g?.querySelector(".what")?.textContent,
        body: [...(g?.querySelector(".activity-body").children || [])].map((n) => (n.classList.contains("prose") ? `prose:${n.textContent.trim()}` : n.classList.contains("step") ? "step" : "detail")),
        closed: g ? !g.open : null,
      };
    }
    // A reasoning model thinks before each write: its thoughts join the
    // Edited group in order, after a line of narration before them, and no
    // second Activity row opens inside the group.
    {
      const think = (t, text) => t.render({ kind: "llm.resp", payload: { thinking: text, tool_calls: [{ name: "fs-edit" }], usage: { prompt_tokens: 10, output_tokens: 1 } } });
      const { root, t } = fresh();
      think(t, "T1");
      edit(t, "a.go");
      think(t, "T2");
      edit(t, "b.go");
      say(t, "Now the CSS.");
      think(t, "T3");
      edit(t, "c.css");
      const g = root.querySelector(".activity.writes");
      out.thoughts = {
        shape: shape(root),
        body: [...g.querySelector(".activity-body").children]
          .filter((n) => !n.classList.contains("step-detail"))
          .map((n) => (n.classList.contains("thinking") ? `thinking:${n.querySelector("pre").textContent}` : n.classList.contains("prose") ? `prose:${n.textContent.trim()}` : n.classList.contains("step") ? `step:${n.querySelector(".mono").textContent}` : n.className)),
        folds: root.querySelectorAll(".activity-fold").length,
      };
    }
    // What is not a line of narration, or is not between writes, stays where
    // the model put it: a paragraph, the words before a command, and the
    // words after the last write.
    {
      const long = "A paragraph that tells you something.\n\nAnd another one, which is why it is not narration.";
      const a = fresh();
      say(a.t, "First.");
      edit(a.t, "a");
      say(a.t, long);
      edit(a.t, "b");
      const b = fresh();
      edit(b.t, "a");
      say(b.t, "Now the tests:");
      done(b.t, call(b.t, "shell-exec", { cmd: "go", args: ["test"] }), "shell-exec", JSON.stringify({ status: 0, stdout: "", stderr: "" }), 100);
      const c = fresh();
      say(c.t, "Run one.");
      run(c.t, "go", 0, 10);
      say(c.t, "Run two.");
      run(c.t, "go", 0, 10);
      const d = fresh();
      say(d.t, "Look at a.");
      read(d.t, "a");
      say(d.t, "Look at b.");
      read(d.t, "b");
      out.kept = { paragraph: shape(a.root), thenCommand: shape(b.root), commands: shape(c.root), reads: shape(d.root) };
    }
    // Marks in a row are one line, its notes behind the fold; a failed write
    // opens its group and the header says so.
    {
      const { root, t } = fresh();
      t.render({ kind: "plan.step", payload: { step: 1, of: 5, state: "done", note: "wrote files" } });
      t.render({ kind: "plan.step", payload: { step: 2, of: 5, state: "done" } });
      t.render({ kind: "plan.step", payload: { step: 3, of: 5, state: "skipped", note: "not needed" } });
      const m = root.querySelector(".audit");
      const f = fresh();
      edit(f.t, "a");
      edit(f.t, "b", false);
      const w = f.root.querySelector(".activity.writes");
      out.marks = { count: root.querySelectorAll(".audit").length, summary: m.querySelector("summary").textContent, notes: m.querySelector("pre")?.textContent, failed: { open: w.open, header: w.querySelector(".what").textContent } };
    }
    // A transcript taken off the page while a command runs (the person left
    // the task) stops its clock at the next tick.
    {
      const { root, t } = fresh();
      call(t, "shell-exec", { cmd: "sleep", args: ["60"] });
      root.remove();
      await sleep(1300);
      out.left = { clock: t.clock };
    }
    // A conversation moved to another model is measured against the window
    // sized for that model, from the switch on (ADR 40).
    {
      const before = [
        { kind: "session.start", payload: { context: { high_water: 61440 } } },
        { kind: "user.msg", payload: { text: "go" } },
        { kind: "llm.req", payload: { est_tokens: 4100 } },
      ];
      const moved = { kind: "session.model", payload: { model: "long", server: "or", context: { high_water: 86016 } } };
      out.gauge = { before: contextGauge(before).high, after: contextGauge([...before, moved]).high, est: contextGauge([...before, moved]).est };
    }
    // A reply's speed: tok/s from the requests' wall time, summed over the
    // stretch's requests the way the line's tokens and time are; a log
    // without wall_ms shows no speed, as before.
    {
      const { root, t } = fresh();
      t.render({ kind: "llm.resp", payload: { content: "one", usage: { prompt_tokens: 100, output_tokens: 800, wall_ms: 4000 } } });
      t.render({ kind: "llm.resp", payload: { content: "two", usage: { prompt_tokens: 100, output_tokens: 800, wall_ms: 1000 } } });
      const old = fresh();
      old.t.render({ kind: "llm.resp", payload: { content: "x", usage: { prompt_tokens: 10, output_tokens: 2, total_ms: 0 } } });
      out.speed = { line: root.querySelector(".usage")?.textContent, old: old.root.querySelector(".usage")?.textContent };
    }
    // A burst through the task's event listener changes its text once per
    // frame, without replacing unrelated panels. The real TaskView and Desk
    // use a synthetic connection so no model's timing decides this check.
    {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const listeners = new Set();
      const conn = {
        live: true,
        methods: new Set(["session.send", "session.abort", "session.answer"]),
        has(method) { return this.methods.has(method); },
        onEvent(fn) { listeners.add(fn); return () => listeners.delete(fn); },
        onResult() { return () => {}; },
        async call(method) {
          if (method === "session.subscribe") return { page: { events: [{ seq: 1, kind: "user.msg", payload: { text: "Begin" } }], through: 1, more: false }, busy: true };
          if (method === "session.info") return {};
          if (method === "session.unsubscribe") return {};
          throw new Error(`unexpected request: ${method}`);
        },
      };
      const desk = new Desk(conn);
      desk.sessions.set("stream", { id: "stream", repo: "/tmp/stream", open: true, busy: true });
      const task = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(task.root);
      await task.open("stream");
      const emit = (kind, payload, seq = 0) => {
        for (const fn of listeners) fn({ method: "session.event", params: { id: "stream", kind, payload, seq } });
      };
      const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
      const text = () => task.el.transcript.querySelector(".streaming")?.textContent || "";
      const t = task.el.transcript;
      t.style.cssText = "height:240px;max-height:240px;min-height:0;flex:none;overflow:auto";
      emit("stream.text", { Text: "Start: " });
      await frame();
      const head = task.el.head.firstChild, panel = task.el.side.firstChild;
      let mutations = [];
      const observer = new MutationObserver((records) => mutations.push(...records));
      observer.observe(task.root, { childList: true, subtree: true, characterData: true });
      for (let i = 0; i < 40; i++) emit("stream.text", { text: "x" });
      const beforeFrame = text();
      await frame();
      mutations.push(...observer.takeRecords());
      observer.disconnect();
      const updates = mutations.filter((m) => m.target === t.querySelector(".streaming")).length;
      const burst = { beforeFrame, text: text(), updates, sameHead: head === task.el.head.firstChild, samePanel: panel === task.el.side.firstChild };

      // Thinking keeps every fragment with the setting both hidden and
      // shown, and alternating stream kinds retain their own text.
      const thoughts = [];
      for (const setting of ["hidden", "shown"]) {
        document.documentElement.dataset.thinking = setting;
        emit("stream.thinking", { Text: setting + ":" });
        emit("stream.text", { Text: " reply " });
        emit("stream.thinking", { Text: "thought " });
        await frame();
        const thinking = t.querySelector(".thinking");
        thoughts.push({ text: thinking.querySelector("pre").textContent, visible: thinking.getClientRects().length > 0 });
      }
      delete document.documentElement.dataset.thinking;

      // Following the bottom stays at the bottom; reading earlier text
      // keeps its scroll position as the next batch arrives.
      emit("stream.text", { Text: "\nA line of streamed text.".repeat(100) });
      await frame();
      const followed = Math.abs(t.scrollHeight - t.clientHeight - t.scrollTop) <= 1;
      t.scrollTop = 0;
      emit("stream.text", { Text: "\nMore text.".repeat(20) });
      await frame();
      const stayed = t.scrollTop === 0 && t.scrollHeight > t.clientHeight + 120;

      // A final response supersedes queued text and thinking immediately,
      // before a frame, and the cancelled frame cannot append stale text.
      emit("stream.thinking", { Text: "provisional thought" });
      emit("stream.text", { Text: "provisional reply" });
      emit("llm.resp", { content: "Finished reply", thinking: "Finished thought" }, 2);
      const finished = () => ({ prose: [...t.querySelectorAll(".prose")].map((n) => n.textContent.trim()), thinking: t.querySelector(".thinking pre")?.textContent, streams: t.querySelectorAll(".streaming").length });
      const final = finished();
      await frame();
      const afterFinalFrame = finished();

      // Questions keep their place after the text that led to them, even
      // when both arrive before the next display frame.
      emit("stream.text", { Text: "Before the question" });
      emit("prompt.confirm", { ID: "ask", Prompt: { Prim: "shell-exec", Argv: ["ls"], Reasons: ["Confirm the command"] } });
      const promptOrder = text() === "Before the question" && t.lastElementChild.matches(".ask-card") && task.el.send.disabled;
      emit("prompt.closed", { ID: "ask" });

      // Parked tasks collect fragments without mutating their detached
      // transcript; returning catches up without another subscription.
      task.park();
      task.root.remove();
      const parked = text();
      emit("stream.text", { Text: " while away" });
      await frame();
      const unchangedAway = text() === parked;
      document.body.append(task.root);
      task.show(desk.sessions.get("stream"));
      const caughtUp = text() === "Before the question while away";

      // Reloading a view and disposing it each cancel a queued frame, so
      // old fragments cannot leak into a freshly read conversation.
      emit("stream.text", { Text: " discard on reload" });
      await task.open("stream");
      await frame();
      const reloaded = !task.el.transcript.textContent.includes("discard on reload");
      emit("stream.text", { Text: "Last fragment before closing" });
      for (const fn of listeners) fn({ method: "session.closed", params: { id: "stream" } });
      const closed = text() === "Last fragment before closing" && task.el.reply.disabled && task.el.head.querySelector(".pill")?.textContent === "Saved";
      await frame();
      const closedOnce = text() === "Last fragment before closing";
      emit("stream.text", { Text: " discard on disposal" });
      task.dispose();
      await frame();
      const disposed = !task.el.transcript.textContent.includes("discard on disposal");
      out.streaming = { burst, thoughts, followed, stayed, final, afterFinalFrame, promptOrder, unchangedAway, caughtUp, reloaded, closed, closedOnce, disposed };
    }

    // The side's tabs: Live first with the ticking panels, the chosen tab
    // kept through the redraw every event makes, an empty tab saying so,
    // and the arrow keys moving the choice.
    {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const listeners = new Set();
      const conn = {
        live: true,
        methods: new Set(["session.send", "session.abort", "session.answer"]),
        has(method) { return this.methods.has(method); },
        onEvent(fn) { listeners.add(fn); return () => listeners.delete(fn); },
        onResult() { return () => {}; },
        async call(method) {
          if (method === "session.subscribe") return { page: { events: [{ seq: 1, kind: "user.msg", payload: { text: "Begin" } }], through: 1, more: false }, busy: false };
          if (method === "session.info") return {};
          if (method === "session.unsubscribe") return {};
          throw new Error(`unexpected request: ${method}`);
        },
      };
      const desk = new Desk(conn);
      desk.sessions.set("tabs", { id: "tabs", repo: "/tmp/tabs", open: true, busy: false });
      const task = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(task.root);
      await task.open("tabs");
      const emit = (kind, payload, seq = 0) => {
        for (const fn of listeners) fn({ method: "session.event", params: { id: "tabs", kind, payload, seq } });
      };
      const bar = () => task.el.side.querySelector("[role=tablist]");
      const chosen = () => bar().querySelector("[aria-selected=true]")?.textContent;
      const shown = () => {
        const body = task.el.side.querySelector("[role=tabpanel]");
        const panels = [...body.querySelectorAll(".panel h2")];
        return panels.length ? panels.map((h2) => h2.childNodes[0].textContent) : body.firstElementChild.textContent;
      };
      const pick = (label) => [...bar().querySelectorAll("[role=tab]")].find((b) => b.textContent === label).click();
      const tabs = {};
      tabs.names = [...bar().querySelectorAll("[role=tab]")].map((b) => b.textContent);
      tabs.first = chosen();
      tabs.live = shown();
      pick("Changes");
      tabs.changes = { chosen: chosen(), shown: shown() };
      // A recorded event redraws the side; the tab it chose stands.
      emit("llm.resp", { content: "Working" }, 2);
      tabs.afterEvent = { chosen: chosen(), shown: shown() };
      pick("Workspace");
      tabs.workspace = { chosen: chosen(), shown: shown() };
      pick("Notes");
      tabs.notes = { chosen: chosen(), shown: shown() };
      // The arrows move both the choice and the focus, from wherever the
      // reader is, Home jumping to the first tab.
      const press = (key) => {
        const at = [...bar().querySelectorAll("[role=tab]")].find((b) => b.textContent === chosen());
        at.focus();
        at.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
        return { chosen: chosen(), focused: document.activeElement.textContent };
      };
      tabs.left = press("ArrowLeft");
      tabs.leftTwice = press("ArrowLeft");
      tabs.home = press("Home");
      pick("Changes");
      emit("guard.decision", { prim: "fs-write", path: "old.txt", decision: "confirm", answer: "yes" }, 3);
      const toggle = task.el.side.querySelector(".change-toggle");
      toggle.focus();
      toggle.click();
      tabs.inspector = {
        button: toggle.tagName === "BUTTON",
        missing: task.el.side.textContent.includes("Preview unavailable"),
        expanded: task.el.side.querySelector(".change-toggle").getAttribute("aria-expanded") === "true",
        empty: task.changeDiff("--- x\n+++ x\n", "Comparison").textContent.includes("No content differences"),
        large: task.changeDiff("(diff too large to show)", "Comparison").textContent.includes("Diff too large"),
        // The sentinel is a whole line of its own: a file whose text says it
        // is a diff, as task.js does, is still shown as one.
        literal: !!task.changeDiff("--- x\n+++ x\n@@ -1 +1 @@\n-old\n+// diff too large to show\n", "Comparison").querySelector(".diff-line.add"),
      };
      const pending = [];
      const originalCall = conn.call;
      conn.call = (method, args) => method === "versions.diff" ? new Promise((resolve) => pending.push(resolve)) : originalCall(method, args);
      task.histories.set("old.txt", { versions: [] });
      const first = task.compareVersion("old.txt", "hash");
      const second = task.compareVersion("old.txt", "hash");
      pending[1]({ label: "Newest comparison", diff: "--- x\n+++ x\n@@ -1 +1 @@\n-old\n+new\n" });
      await second;
      pending[0]({ label: "Stale comparison", diff: "+stale" });
      await first;
      tabs.inspector.newest = task.comparisons.get(JSON.stringify(["old.txt", "hash"])).label === "Newest comparison";
      const navigated = task.compareVersion("old.txt", "hash");
      task.version++;
      pending[2]({ label: "Wrong task", diff: "+wrong" });
      await navigated;
      tabs.inspector.navigation = !task.comparisons.get(JSON.stringify(["old.txt", "hash"])).label;
      task.dispose();
      out.tabs = tabs;
    }

    // The side's clocks: only pairs the log answers count. A request that
    // failed records no reply and a crash leaves its tool's result to the
    // resume; neither may be counted on to now. The log below is written
    // in the order a resume records: session.resume first, then the
    // recovery result, so the time between the crash and the resume stays
    // nobody's. Without this the stale request counts from its sending to
    // the log's last record, and the recovery result bridges the dead time.
    {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const base = Date.now() - 120000;
      const at = (ms) => new Date(base + ms).toISOString();
      const events = [
        { seq: 1, kind: "user.msg", ts: at(0), payload: { text: "Fix the bug" } },
        { seq: 2, kind: "llm.req", ts: at(1000), payload: {} },
        { seq: 3, kind: "note", ts: at(2000), payload: { text: "model request failed: connection refused" } },
        { seq: 4, kind: "tool.call", ts: at(2500), payload: { id: "orphan", name: "fs-read", args: {} } },
        { seq: 5, kind: "turn.end", ts: at(3000), payload: { outcome: "error" } },
        { seq: 6, kind: "user.msg", ts: at(10000), payload: { text: "Go on" } },
        { seq: 7, kind: "llm.req", ts: at(11000), payload: {} },
        { seq: 8, kind: "llm.resp", ts: at(14000), payload: { content: "Working", usage: { prompt_tokens: 10, output_tokens: 5 } } },
        { seq: 9, kind: "turn.end", ts: at(15000), payload: { outcome: "completed" } },
        { seq: 10, kind: "session.resume", ts: at(16000), payload: {} },
        { seq: 11, kind: "tool.result", ts: at(16000), payload: { id: "orphan", name: "fs-read", ok: false, error: "Interrupted before a durable result was recorded." } },
        { seq: 12, kind: "user.msg", ts: at(20000), payload: { text: "And again" } },
        { seq: 13, kind: "llm.req", ts: at(21000), payload: {} },
        { seq: 14, kind: "llm.resp", ts: at(23000), payload: { content: "Done", usage: { prompt_tokens: 10, output_tokens: 5 } } },
        { seq: 15, kind: "turn.end", ts: at(24000), payload: { outcome: "completed" } },
      ];
      const listeners = new Set();
      const conn = {
        live: true,
        methods: new Set(["session.send", "session.abort", "session.answer"]),
        has(method) { return this.methods.has(method); },
        onEvent(fn) { listeners.add(fn); return () => listeners.delete(fn); },
        onResult() { return () => {}; },
        async call(method) {
          if (method === "session.subscribe") return { page: { events, through: events.length, more: false }, busy: false };
          if (method === "session.info") return {};
          if (method === "session.unsubscribe") return {};
          throw new Error(`unexpected request: ${method}`);
        },
      };
      const desk = new Desk(conn);
      desk.sessions.set("clocks", { id: "clocks", repo: "/tmp/clocks", open: true, busy: false });
      const task = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(task.root);
      await task.open("clocks");
      const totals = task.el.side.querySelector("[data-totals]");
      out.clocks = {
        model: totals?.querySelector("[data-model]")?.textContent,
        tools: totals?.querySelector("[data-tools]")?.textContent,
        running: totals?.querySelector("[data-running]")?.textContent,
        working: totals?.querySelector("[data-working]")?.textContent,
      };
      task.dispose();
    }

    // The Totals panel's model speed: output tokens over the wall time of
    // the requests that reported tokens — wall_ms where the reply carries
    // it, the records' timestamps where it does not (an older log).
    {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const listeners = new Set();
      // The log's first record carries the window's high water, so the
      // Context figure has a limit to count against (a seq of 0 is not a
      // record the task keeps).
      const log = [
        { seq: 1, kind: "session.start", payload: { context: { high_water: 774143 } }, ts: "2026-01-01T00:00:00Z" },
        { seq: 2, kind: "user.msg", payload: { text: "Begin" }, ts: "2026-01-01T00:00:00Z" },
        { seq: 3, kind: "llm.req", payload: { model: "m", est_tokens: 100 }, ts: "2026-01-01T00:00:01Z" },
        { seq: 4, kind: "llm.resp", payload: { content: "one", usage: { prompt_tokens: 100, output_tokens: 800, wall_ms: 4000 } }, ts: "2026-01-01T00:00:06Z" },
        { seq: 5, kind: "llm.req", payload: { model: "m", est_tokens: 100 }, ts: "2026-01-01T00:00:07Z" },
        { seq: 6, kind: "llm.resp", payload: { content: "two", usage: { prompt_tokens: 100, output_tokens: 800 } }, ts: "2026-01-01T00:00:08Z" },
        { seq: 7, kind: "tool.call", payload: { id: "t1", name: "fs-read", args: {} }, ts: "2026-01-01T00:00:09Z" },
        { seq: 8, kind: "tool.result", payload: { id: "t1", name: "fs-read", ok: true, value: "x", ms: 500 }, ts: "2026-01-01T00:00:10Z" },
      ];
      // A task that has asked nothing yet; one whose planner is asking, in a
      // context of its own, before the conversation has; and one whose
      // planner asked and folded after the conversation had.
      const fresh = [log[0], log[1]];
      const planning = [
        log[0],
        log[1],
        { seq: 3, kind: "context.narrow", payload: { agent: "planner" }, ts: "2026-01-01T00:00:01Z" },
        { seq: 4, kind: "llm.req", payload: { model: "m", est_tokens: 40 }, ts: "2026-01-01T00:00:01Z" },
      ];
      const folded = [
        ...log.slice(0, 4),
        { seq: 5, kind: "context.narrow", payload: { agent: "planner" }, ts: "2026-01-01T00:00:07Z" },
        { seq: 6, kind: "llm.req", payload: { model: "m", est_tokens: 40 }, ts: "2026-01-01T00:00:07Z" },
        { seq: 7, kind: "llm.resp", payload: { content: "1. a plan", usage: { prompt_tokens: 30, cached_tokens: 20, output_tokens: 10 } }, ts: "2026-01-01T00:00:08Z" },
        { seq: 8, kind: "context.fold", payload: {}, ts: "2026-01-01T00:00:08Z" },
      ];
      const logs = { speed: log, old: log.filter((e) => e.kind !== "session.start"), fresh, planning, folded };
      const conn = {
        live: true,
        methods: new Set(["session.send", "session.abort", "session.answer"]),
        has(method) { return this.methods.has(method); },
        onEvent(fn) { listeners.add(fn); return () => listeners.delete(fn); },
        onResult() { return () => {}; },
        async call(method, params) {
          if (method === "session.subscribe") return {
            page: { events: logs[params.id], through: logs[params.id].at(-1).seq, more: false },
            busy: false,
          };
          if (method === "session.info") return { budgets: { wall_ns: { soft: 4 * 3600e9, hard: 8 * 3600e9 }, tool_calls: { soft: 5000, hard: 5000 }, tokens: { soft: 2000000, hard: 2000000 } } };
          if (method === "session.unsubscribe") return {};
          throw new Error(`unexpected request: ${method}`);
        },
      };
      const desk = new Desk(conn);
      desk.sessions.set("speed", { id: "speed", repo: "/tmp/speed", open: true, busy: false });
      desk.sessions.set("old", { id: "old", repo: "/tmp/old", open: true, busy: false });
      for (const id of ["fresh", "planning", "folded"]) desk.sessions.set(id, { id, repo: `/tmp/${id}`, open: true, busy: false });
      const task = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(task.root);
      await task.open("speed");
      const panel = [...task.el.side.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.childNodes[0].textContent === "Totals");
      const row = (name) => {
        const m = [...(panel?.querySelectorAll(".meter,.kv") || [])].find((n) => n.firstElementChild?.textContent === name || n.querySelector(".meter-head span")?.textContent === name);
        const head = m?.querySelector(".meter-head");
        if (head) return `${head.children[0].textContent} | ${head.children[1].textContent} | meter`;
        const cells = m ? [...m.children].map((c) => c.textContent.trim()) : [];
        return m ? `${cells[0]} | ${cells[1]} | row` : null;
      };
      out.totals = { speed: row("Model speed"), wait: row("Model wait"), tools: row("Tool time") };
      // Only a figure with a limit is a bar, and the bars come first: the
      // plain rows (Model wait, Tool time) follow them, not take turns with
      // them. The provider's own count moved from a reflowing hint line into
      // the Context bar's tooltip.
      const metersNode = task.el.side.querySelector("[data-meters]");
      const figure = (n) => `${n.className.split(" ")[0]}:${n.firstElementChild.firstElementChild?.textContent || n.firstElementChild.textContent}`;
      out.meters = {
        order: [...(metersNode?.children || [])].map(figure),
        context: [...(metersNode?.querySelectorAll(".meter") || [])].find((m) => m.firstElementChild.firstElementChild.textContent === "Context")?.textContent,
      };
      out.context = {
        hint: task.el.side.querySelector("[data-meters] small") ? true : null,
        title: task.el.side.querySelector("[data-meters] .meter[title]")?.title,
      };
      task.dispose();
      // A log that does not give the high water has nothing to count the
      // context against: a row with its estimate, never "/ ~0".
      const older = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(older.root);
      await older.open("old");
      const unlimited = [...older.el.side.querySelectorAll("[data-meters] > *")].find((n) => n.textContent.startsWith("Context"));
      out.noHighWater = { class: unlimited?.className, text: unlimited?.textContent };
      older.dispose();
      // The window is known from the start: the bar is there, empty, before
      // any request, and Tokens used with it; while the planner asks in a
      // context of its own, the bar follows it, named; once it folds, the
      // bar is the conversation's again.
      const bars = async (id) => {
        const view = new TaskView(conn, desk, () => {});
        document.body.replaceChildren(view.root);
        await view.open(id);
        const found = {};
        for (const m of view.el.side.querySelectorAll("[data-meters] .meter")) {
          const label = m.querySelector(".meter-head span").textContent;
          if (label.startsWith("Context") || label === "Tokens used") found[label.startsWith("Context") ? "context" : "tokens"] = { text: m.textContent, width: m.querySelector(".meter-fill")?.style.width };
        }
        view.dispose();
        return found;
      };
      out.early = { fresh: await bars("fresh"), planning: await bars("planning"), folded: await bars("folded") };
    }
    // The session token cache rate is a raw prompt-token share across
    // turns and models, not a mean of their rates or the discounted budget.
    {
      const { TaskView } = await import("/task.js");
      const { Desk } = await import("/desk.js");
      const listeners = new Set();
      const ev = (seq, kind, payload = {}) => ({ seq, kind, payload, ts: new Date(Date.UTC(2026, 0, 1) + seq * 1000).toISOString() });
      const replies = (...usages) => [ev(1, "user.msg", { text: "Begin" }), ...usages.map((usage, i) => ev(i + 2, "llm.resp", { usage }))];
      const logs = {
        weighted: [
          ev(1, "user.msg", { text: "First turn" }),
          ev(2, "llm.req", { model: "first" }),
          ev(3, "llm.resp", { usage: { prompt_tokens: 100, cached_tokens: 100, output_tokens: 9000 } }),
          ev(4, "turn.end"),
          ev(5, "user.msg", { text: "Second turn" }),
          ev(6, "llm.req", { model: "second" }),
          ev(7, "llm.resp", { usage: { prompt_tokens: 900, cached_tokens: 0, output_tokens: 9000 } }),
        ],
        mixed: replies({ prompt_tokens: 100, cached_tokens: 67 }, { prompt_tokens: 9000 }, undefined, { prompt_tokens: 10000, cached_tokens: 10000, unreported: true }, { prompt_tokens: 10000, cached_tokens: null }),
        zero: replies({ prompt_tokens: 100, cached_tokens: 0, cache_write_tokens: 100 }),
        full: replies({ prompt_tokens: 100, cached_tokens: 100 }),
        unknown: replies({ prompt_tokens: 100 }, { prompt_tokens: 100, cache_write_tokens: 100 }),
        unreported: replies({ prompt_tokens: 100, cached_tokens: 100, unreported: true }),
        noPrompt: replies({ cached_tokens: 80 }, { prompt_tokens: 0, cached_tokens: 0 }),
        missing: replies(undefined),
        fresh: replies(),
        retained: [ev(101, "user.msg", { text: "After truncation" }), ev(102, "llm.resp", { usage: { prompt_tokens: 200, cached_tokens: 100 } })],
      };
      const conn = {
        live: true,
        has() { return false; },
        onEvent(fn) { listeners.add(fn); return () => listeners.delete(fn); },
        onResult() { return () => {}; },
        async call(method, params) {
          if (method === "session.subscribe") return { page: { events: logs[params.id], through: logs[params.id].at(-1).seq, more: false, truncated: params.id === "retained" }, busy: false };
          if (method === "session.info" || method === "session.unsubscribe") return {};
          throw new Error(`unexpected request: ${method}`);
        },
      };
      const desk = new Desk(conn);
      for (const id of Object.keys(logs)) desk.sessions.set(id, { id, repo: `/tmp/${id}`, open: true, busy: false });
      const task = new TaskView(conn, desk, () => {});
      document.body.replaceChildren(task.root);
      const figure = () => {
        const panel = task.el.side.querySelector("[data-totals]");
        const row = [...(panel?.querySelectorAll(".kv") || [])].find((n) => n.firstElementChild.textContent === "Token cache rate");
        return { value: row?.lastElementChild.textContent ?? null, title: row?.title, scope: panel?.querySelector("h2").textContent };
      };
      const rates = {};
      for (const id of Object.keys(logs)) {
        await task.open(id);
        rates[id] = figure();
      }
      await task.open("weighted");
      const response = ev(8, "llm.resp", { usage: { prompt_tokens: 1000, cached_tokens: 1000 } });
      logs.weighted.push(response);
      for (const fn of listeners) fn({ method: "session.event", params: { id: "weighted", ...response } });
      rates.incremental = figure();
      // Reopening reads the recorded usage again, rather than losing the
      // new response or carrying another task's cached aggregation across.
      await task.open("weighted");
      rates.reopened = figure();
      out.sessionCacheRate = rates;
      task.dispose();
    }
    return out;
  });
  console.log(JSON.stringify(results, null, 1));
  const fail = [];
  const want = (ok, what) => ok || fail.push(what);
  const ac = results.activityCollapsePreservesContent;
  want(ac.initial.folds === 1 && ac.initial.agents === 1 && ac.initial.closed && ac.initial.hidden && ac.initial.rows.join() === "activity-fold" && ac.initial.kept, `activityCollapsePreservesContent: screenshot activity folds into one closed row: ${JSON.stringify(ac.initial)}`);
  want(ac.expanded && ac.harnessVisible && ac.live && ac.collapsedLive, `activityCollapsePreservesContent: expansion and live updates retain content and choice: ${JSON.stringify(ac)}`);
  want(ac.streamingVisible && ac.replyVisible && ac.separated && ac.boundary && ac.reset, `activityCollapsePreservesContent: replies, boundaries and reset: ${JSON.stringify(ac)}`);
  const fx = results.foldExceptions;
  want(fx.command.open === false && fx.command.failed === "1 failed" && !fx.command.what.includes("failed") && fx.command.run && fx.command.call, `a failed command is counted on the closed row, its run open inside: ${JSON.stringify(fx.command)}`);
  want(fx.readFailure.open === false && fx.readFailure.failed === "1 failed", `a failed read is counted on the closed row: ${JSON.stringify(fx.readFailure)}`);
  want(fx.write.open === false && fx.write.failed === "1 failed" && fx.write.group, `a failed write is counted on the closed row, its group open inside: ${JSON.stringify(fx.write)}`);
  want(fx.other.open === false && fx.other.failed === "1 failed", `a failed call is counted on the closed row: ${JSON.stringify(fx.other)}`);
  want(fx.asking.open === false && fx.asking.what.includes("Waiting for your answer") && fx.answered.open === false && fx.answered.failed === "", `a question leaves the row closed, before and after the answer: ${JSON.stringify({ asking: fx.asking, answered: fx.answered })}`);
  want(fx.audit && fx.stopped, `an error and a stop at a limit stay outside the row: ${JSON.stringify(fx)}`);
  want(fx.whereSaid.shown && fx.whereSaid.absent, `a stopped turn says where it stopped, in view, and one with no where says nothing: ${JSON.stringify(fx.whereSaid)}`);
  want(fx.skipped.failed === "" && fx.skipped.outside === 0 && fx.skipped.bad === 0 && fx.skipped.notes.join("; ") === "2 calls skipped: this agent is out of rounds | fs-read\nfs-find; fs-edit skipped: an earlier call in this batch failed | fs-edit", `skipped calls are notes behind the row, said with their reason and not a failure, those in a row for one reason as one note: ${JSON.stringify(fx.skipped)}`);
  want(fx.stray.outside === 1 && fx.stray.summary === "fs-read failed", `a result without its call that failed is still said outside the row: ${JSON.stringify(fx.stray)}`);
  want(fx.worktree.leftOut && !fx.worktree.clean, `files left out of a worktree are said outside the row, a clean worktree inside it: ${JSON.stringify(fx.worktree)}`);
  want(results.order.join() === "reads,commands,reads,commands", `order: ${results.order}`);
  const cb = results.clubbed;
  want(cb.shape.join() === "prose:Making the c,writes,usage" && cb.header === "2 files" && cb.closed === true, `writes and their narration fold: ${JSON.stringify(cb)}`);
  want(cb.body.join() === "step,detail,prose:Now the CSS:,step,detail,prose:And the comment:,step,detail", `narration in order inside the group: ${JSON.stringify(cb.body)}`);
  const th = results.thoughts;
  want(th.body.join() === "thinking:T1,step:a.go,thinking:T2,step:b.go,prose:Now the CSS.,thinking:T3,step:c.css" && th.shape.join() === "writes,usage" && th.folds === 1, `thoughts between writes stay in order inside the group: ${JSON.stringify(th)}`);
  const k = results.kept;
  want(k.paragraph.join() === "prose:First.,writes,prose:A paragraph,writes", `a paragraph ends the group: ${JSON.stringify(k.paragraph)}`);
  want(k.thenCommand.join() === "writes,prose:Now the test,commands", `the words before a command stay before it: ${JSON.stringify(k.thenCommand)}`);
  want(k.commands.join() === "prose:Run one.,commands,prose:Run two.,commands", `replies and commands keep their order: ${JSON.stringify(k.commands)}`);
  want(k.reads.join() === "prose:Look at a.,reads,prose:Look at b.,reads", `replies and reads keep their order: ${JSON.stringify(k.reads)}`);
  const mk = results.marks;
  want(mk.count === 1 && mk.summary === "Steps 1, 2 of 5 marked done · Step 3 of 5 marked skipped" && mk.notes === "1. wrote files\n3. not needed", `marks join: ${JSON.stringify(mk)}`);
  want(mk.failed.open === true && mk.failed.header === "2 files · 1 failed", `a failed write opens its group: ${JSON.stringify(mk.failed)}`);
  want(results.consecutive.runs === 1 && results.consecutive.rows === 2 && results.consecutive.header === "Ran 2 commands | in 1.6 s", `consecutive: ${JSON.stringify(results.consecutive)}`);
  const r = results.running;
  want(/^Running a command \| 0\.\d s$/.test(r.first) && /^Running a command \| 1\.\d s$/.test(r.later), `running counts up: ${r.first} then ${r.later}`);
  want(r.finished === "Ran 1 command | in 1.5 s" && r.still === r.finished && r.clock === null, `running stops: ${JSON.stringify(r)}`);
  const w = results.waiting;
  want(w.waiting === "Ran 1 command, waiting for your answer | " && w.row === "waiting for your answer", `waiting: ${JSON.stringify(w)}`);
  want(/^Ran 1 command, running another \| \d\.\d s$/.test(w.answered), `answered: ${w.answered}`);
  const f = results.failed;
  want(f.failed.header === "Ran 1 command | in 0.3 s · 1 failed" && f.failed.run && f.failed.row, `failed: ${JSON.stringify(f.failed)}`);
  want(f.refused.header === "Ran 1 command | in 0.0 s · 1 failed" && f.refused.run && f.refused.row, `refused: ${JSON.stringify(f.refused)}`);
  want(results.left.clock === null, `the clock outlived its transcript: ${JSON.stringify(results.left)}`);
  const l = results.longCommand;
  want(l.status === "exit 1 · 1.2 s · allowed: don't ask", `long command status: ${JSON.stringify(l)}`);
  want(l.nameClipped && l.nameInside && l.statInside && l.scroll, `long command stays inside its card: ${JSON.stringify(l)}`);
  want(results.gauge.before === 61440 && results.gauge.after === 86016 && results.gauge.est === 4100, `gauge across a switch: ${JSON.stringify(results.gauge)}`);
  want(results.speed.line === "200 → 1,600 tokens over 2 requests · largest prompt 100 · 320.0 tok/s" && !results.speed.old.includes("tok/s"), `reply speed: ${JSON.stringify(results.speed)}`);
  const s = results.streaming;
  want(s.burst.beforeFrame === "Start: " && s.burst.text === "Start: " + "x".repeat(40) && s.burst.updates === 1 && s.burst.sameHead && s.burst.samePanel, `stream burst: ${JSON.stringify(s.burst)}`);
  want(s.thoughts[0].text === "hidden:thought " && !s.thoughts[0].visible && s.thoughts[1].text === "hidden:thought shown:thought " && s.thoughts[1].visible, `streamed thinking setting: ${JSON.stringify(s.thoughts)}`);
  want(s.followed && s.stayed, `stream scrolling: followed=${s.followed}, stayed=${s.stayed}`);
  want(s.final.prose.join() === "Finished reply" && s.final.thinking === "Finished thought" && s.final.streams === 0 && JSON.stringify(s.final) === JSON.stringify(s.afterFinalFrame), `final reply after queued fragments: ${JSON.stringify(s.final)}, later ${JSON.stringify(s.afterFinalFrame)}`);
  want(s.promptOrder, "a question overtook its queued text or enabled sending");
  want(s.unchangedAway && s.caughtUp, `parked stream: unchanged=${s.unchangedAway}, caught up=${s.caughtUp}`);
  want(s.reloaded && s.disposed, `queued streams outlived reload or disposal: ${JSON.stringify(s)}`);
  want(s.closed && s.closedOnce, "session closure lost or duplicated its queued text, or left the reply enabled");
  const cache = results.sessionCacheRate;
  want(cache.weighted.value === "10%", `session token cache rate: token-weighted across turns and models, without output or budget discounts: ${JSON.stringify(cache.weighted)}`);
  want(cache.mixed.value === "67%", `session token cache rate: unknown and unreported replies excluded, percentage rounded: ${JSON.stringify(cache.mixed)}`);
  want(cache.zero.value === "0%" && cache.full.value === "100%", `session token cache rate: reported zero (cache writes are not reads) and full cache: ${JSON.stringify({ zero: cache.zero, full: cache.full })}`);
  for (const id of ["unknown", "unreported", "noPrompt", "missing", "fresh"]) want(cache[id].value === "unknown", `session token cache rate: ${id} has no usable denominator: ${JSON.stringify(cache[id])}`);
  want(cache.incremental.value === "55%" && cache.reopened.value === "55%", `session token cache rate: live recorded reply and reopening update the totals: ${JSON.stringify({ incremental: cache.incremental, reopened: cache.reopened })}`);
  want(cache.retained.value === "50%" && cache.retained.scope === "Totalsfrom the log that remains", `session token cache rate: retained history scope: ${JSON.stringify(cache.retained)}`);
  want(/raw prompt tokens/.test(cache.weighted.title) && /not the share of requests/.test(cache.weighted.title) && /unreported usage/.test(cache.weighted.title), `session token cache rate: tooltip explains the denominator and unknown usage: ${JSON.stringify(cache.weighted)}`);
  want(results.totals.speed === "Model speed | 320.0 tok/s · 2 requests | row" && results.totals.wait?.endsWith("| row") && results.totals.tools?.endsWith("| row"), `totals speed: ${JSON.stringify(results.totals)}`);
  want(results.meters.order.join() === "meter:Time,meter:Tool calls,meter:Tokens used,meter:Context,kv:Model wait,kv:Tool time", `this turn: bars first, then rows: ${JSON.stringify(results.meters.order)}`);
  want(results.meters.context === "Context~100 / ~774,143", `the context against the high water: ${results.meters.context}`);
  want(results.context.hint === null && /the provider counted 100 tokens/.test(results.context.title || ""), `context hint: ${JSON.stringify(results.context)}`);
  want(results.noHighWater.class === "kv" && results.noHighWater.text === "Context~100", `context without a high water: ${JSON.stringify(results.noHighWater)}`);
  const early = results.early;
  want(early.fresh.context?.text === "Context— / ~774,143" && early.fresh.context?.width === "0%", `context before any request: ${JSON.stringify(early.fresh)}`);
  want(early.fresh.tokens?.text === "Tokens used0 / 2,000,000", `tokens used before any reply: ${JSON.stringify(early.fresh)}`);
  want(early.planning.context?.text === "Context · planner~40 / ~774,143", `context while the planner asks: ${JSON.stringify(early.planning)}`);
  want(early.folded.context?.text === "Context~100 / ~774,143", `context once the planner folded: ${JSON.stringify(early.folded)}`);
  // Tokens used is every request's prompt and reply, a cached prompt token
  // a tenth: 100 + 800, then 30 - 20 + 20 / 10 + 10.
  want(early.folded.tokens?.text === "Tokens used922 / 2,000,000", `tokens used counts prompts, replies and cached tokens at a tenth: ${JSON.stringify(early.folded)}`);
  const tb = results.tabs;
  want(tb.names.join() === "Live,Changes,Workspace,Notes" && tb.first === "Live", `aside tabs: ${JSON.stringify(tb.names)} first ${tb.first}`);
  want(tb.live.join() === "This turn,Plan,Totals", `Live tab panels: ${JSON.stringify(tb.live)}`);
  want(tb.changes.chosen === "Changes" && tb.changes.shown === "No approved changes yet.", `Changes tab: ${JSON.stringify(tb.changes)}`);
  want(tb.afterEvent.chosen === "Changes" && tb.afterEvent.shown === "No approved changes yet.", `the tab kept through an event's redraw: ${JSON.stringify(tb.afterEvent)}`);
  want(tb.workspace.chosen === "Workspace" && tb.workspace.shown.join() === "Workspace,Details", `Workspace tab: ${JSON.stringify(tb.workspace)}`);
  want(tb.notes.chosen === "Notes" && tb.notes.shown === "Reading the notes…", `Notes tab: ${JSON.stringify(tb.notes)}`);
  want(tb.left.chosen === "Workspace" && tb.left.focused === "Workspace" && tb.leftTwice.chosen === "Changes" && tb.leftTwice.focused === "Changes" && tb.home.chosen === "Live" && tb.home.focused === "Live", `the tabs' arrow keys: ${JSON.stringify({ left: tb.left, leftTwice: tb.leftTwice, home: tb.home })}`);
  want(Object.values(tb.inspector).every(Boolean), `change inspector states and stale replies: ${JSON.stringify(tb.inspector)}`);
  const c = results.clocks;
  want(c.model === "5.0 s" && c.tools === "0.0 s", `the clocks count only answered pairs: ${JSON.stringify(c)}`);
  want(c.running === "12 s" && c.working === "12 s", `running and working stay at the turns' own spans: ${JSON.stringify(c)}`);
  await page.setContent("");
  if (fail.length) throw new Error(fail.join("\n"));
});
