// What every browser check shares: the programs and browsers it starts,
// stopped however it ends; its temporary folders, removed likewise; the
// page errors and console errors that fail it; and the requests a page
// sends, so that a check asserts what an action did rather than that a
// control exists.
const fs = require("fs");
const net = require("net");
const os = require("os");
const path = require("path");
const { spawn } = require("child_process");
const { chromium } = require("playwright");

const kids = [];
const browsers = [];
const dirs = [];
const errors = [];
const warnings = [];
const expected = [];

// out is where the check leaves its screenshots, a folder of its own (run.sh
// passes out/<check>), so that checks running at once never overwrite each
// other's.
const out = process.argv[2];
if (!out) throw new Error("usage: node checks/<check>.js OUT-DIR");
fs.mkdirSync(out, { recursive: true });

// Stopped by a signal, a check still exits through the handler below.
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => process.exit(130));
// Whatever ends the check, nothing it started outlives it and its folders
// go. A check that finishes has already stopped its programs gently, in
// check(); this is for the ways out that skip that.
process.on("exit", () => {
  for (const kid of kids) if (kid.exitCode === null && kid.signalCode === null) kid.kill("SIGKILL");
  for (const dir of dirs) fs.rmSync(dir, { recursive: true, force: true, maxRetries: 3 });
});

// start runs a program the check needs and keeps what it prints, in
// kid.stdoutText and kid.stderrText.
function start(command, args, options = {}) {
  const kid = spawn(command, args, options);
  kid.stdoutText = "";
  kid.stderrText = "";
  kid.stdout.on("data", (d) => (kid.stdoutText += d));
  kid.stderr.on("data", (d) => (kid.stderrText += d));
  // Its exit, not the close of its output, which a program it started may
  // hold open after it is gone.
  kid.exited = new Promise((resolve) => kid.on("exit", resolve));
  kids.push(kid);
  return kid;
}

// line resolves to the groups of the first match of re in what kid printed
// on stream after offset from, and fails if kid stops before printing it.
function line(kid, re, stream = "stdout", from = 0) {
  const text = () => kid[`${stream}Text`].slice(from);
  return new Promise((resolve, reject) => {
    const look = () => {
      const m = re.exec(text());
      if (m) {
        done();
        resolve(m.slice(1));
      }
    };
    const closed = (code, signal) => {
      done();
      reject(new Error(`${path.basename(kid.spawnfile)} stopped (${signal || code}) before printing ${re}:\n${kid.stderrText.slice(-2000)}`));
    };
    const done = () => {
      kid[stream].off("data", look);
      kid.off("close", closed);
    };
    kid[stream].on("data", look);
    kid.on("close", closed);
    look();
  });
}

// tempDir makes a folder under the system's temporary directory: TMPDIR,
// which is the session's scratch directory when nodwyn runs its own checks.
// The scratch lives inside nodwyn's data directory, and a daemon refuses a
// task rooted in its own config or data ("which nodwyn never works in"), so
// a world placed there fails every check that starts a task in it; /tmp
// stands in, as the demo's own root already does.
function tempDir(prefix) {
  let base = os.tmpdir();
  const home = os.homedir();
  for (const sub of [".local/share/nodwyn", ".config/nodwyn"]) {
    const dir = path.join(home, sub);
    if (base === dir || base.startsWith(dir + path.sep)) base = "/tmp";
  }
  const dir = fs.mkdtempSync(path.join(base, prefix));
  dirs.push(dir);
  return dir;
}

// runtimeDir is an XDG_RUNTIME_DIR, where a daemon makes its socket. A Unix
// socket's path has at most 103 bytes on macOS (107 on Linux) and a session's
// scratch directory can be too long for one, so this falls back to /tmp, as
// the Go tests' shortDir does.
function runtimeDir() {
  const base = os.tmpdir().length + "/nodwyn-run-XXXXXX/nodwyn/sock".length <= 103 ? os.tmpdir() : "/tmp";
  const dir = fs.mkdtempSync(path.join(base, "nodwyn-run-"));
  dirs.push(dir);
  return dir;
}

// freePort is a loopback port that nothing listened on a moment ago: the
// system picked it.
function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

async function launch(options) {
  const browser = await chromium.launch(options);
  browsers.push(browser);
  return browser;
}

// watch fails the check on any error a page throws or logs, but for those
// expectError allowed. A warning is printed, and does not fail it.
function watch(page, label = "") {
  const tag = label ? `${label}: ` : "";
  page.on("pageerror", (e) => errors.push(tag + e.message));
  page.on("console", (m) => {
    if (m.type() === "warning") warnings.push(tag + m.text());
    if (m.type() !== "error") return;
    const allowed = expected.findIndex((re) => re.test(m.text()));
    if (allowed < 0) errors.push(tag + m.text());
    else console.log("expected error:", tag + m.text(), expected.splice(allowed, 1)[0]);
  });
  return page;
}

// expectError allows one console error matching re: one the check provokes
// on purpose, such as a request the console must refuse.
function expectError(re) {
  expected.push(re);
}

// requests records the JSON-RPC requests a page sends over its socket, in
// order, as {method, params}.
function requests(page) {
  const sent = [];
  page.on("websocket", (socket) =>
    socket.on("framesent", ({ payload }) => {
      try {
        const frame = JSON.parse(payload);
        if (frame.method) sent.push({ method: frame.method, params: frame.params });
      } catch {
        /* not JSON-RPC */
      }
    }),
  );
  sent.of = (method) => sent.filter((r) => r.method === method).map((r) => r.params);
  return sent;
}

// shows fails unless the element at selector shows each of texts.
async function shows(page, selector, ...texts) {
  const text = await page.innerText(selector);
  for (const want of texts) if (!text.includes(want)) throw new Error(`${selector} does not show ${JSON.stringify(want)}; it shows:\n${text.slice(0, 1500)}`);
}

// until asks ready every 50 ms until it returns something truthy, and
// returns that; after limit ms it fails, saying what never happened.
async function until(what, ready, limit = 10000) {
  for (const end = Date.now() + limit; ; await new Promise((r) => setTimeout(r, 50))) {
    const got = await ready();
    if (got) return got;
    if (Date.now() > end) throw new Error(`no ${what} after ${limit} ms`);
  }
}

// check runs the body of a check. Then, whether it passed or not, it closes
// the browsers, stops the programs (SIGTERM, then SIGKILL after five
// seconds), removes the folders, and exits 1 if the body failed or a page
// reported an error.
function check(body) {
  (async () => {
    let failed = false;
    try {
      await body();
    } catch (e) {
      failed = true;
      console.log("FAILED", e.stack || e);
      // What each page showed when the check failed, as failed-N.png.
      const pages = browsers.flatMap((b) => b.contexts().flatMap((c) => c.pages()));
      for (const [i, page] of pages.entries()) await page.screenshot({ path: `${out}/failed-${i + 1}.png` }).catch(() => {});
    } finally {
      await Promise.all(browsers.map((b) => b.close().catch(() => {})));
      await Promise.all(
        kids.map(async (kid) => {
          if (kid.exitCode !== null || kid.signalCode !== null) return;
          kid.kill("SIGTERM");
          const timer = setTimeout(() => kid.kill("SIGKILL"), 5000);
          await kid.exited;
          clearTimeout(timer);
        }),
      );
      for (const dir of dirs.splice(0)) fs.rmSync(dir, { recursive: true, force: true, maxRetries: 3 });
    }
    for (const w of warnings) console.log("warning:", w);
    if (expected.length) errors.push(...expected.map((re) => `an expected error never came: ${re}`));
    console.log(errors.length ? "PAGE ERRORS\n" + errors.join("\n") : "no page errors");
    process.exit(failed || errors.length ? 1 : 0);
  })();
}

module.exports = { out, start, line, tempDir, runtimeDir, freePort, launch, watch, expectError, requests, shows, until, check };
