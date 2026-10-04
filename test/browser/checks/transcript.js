// The transcript's runs of commands, drawn from events written here rather
// than from a scripted model: the order of reads and commands, a run's
// header while a command runs, waits for an answer and fails, and a clock
// that stops when its transcript leaves the page, and the context gauge
// following a conversation moved to another model. The console's own module
// is imported from the demo's console, so this is the code people run. The
// waits of 1.3 s in the page are what is tested: the run's one-second clock.
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

    // Reads and commands that alternate keep their order: each read ends a
    // run of commands and each command a group of reads.
    {
      const { root, t } = fresh();
      read(t, "a.go");
      run(t, "go", 0, 100);
      read(t, "b.go");
      run(t, "go", 0, 100);
      out.order = [...root.querySelectorAll(".turn.agent > .activity")].map((g) => (g.classList.contains("commands") ? "commands" : "reads"));
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
    return out;
  });
  console.log(JSON.stringify(results, null, 1));
  const fail = [];
  const want = (ok, what) => ok || fail.push(what);
  want(results.order.join() === "reads,commands,reads,commands", `order: ${results.order}`);
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
  await page.setContent("");
  if (fail.length) throw new Error(fail.join("\n"));
});
