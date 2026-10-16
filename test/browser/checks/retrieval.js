// Retrieval (ADR 50) as a person sees it: a task whose model searches the
// repository, the conversation and earlier tasks, recalls an event and
// brings the index up to date. What the transcript says of each, the task's
// Workspace tab, and the Workspaces page's line for the index with its
// Forget, and earlier tasks turned on there showing in the task.
const assert = require("assert");
const { out, line, requests, until, step, ready, desk, check } = require("../lib");

check(async () => {
  const { url, repo, page } = await desk();
  const sent = requests(page);
  await page.goto(url);
  await ready(page);
  await page.click(`button.chip[title="${repo}"]`);
  await page.fill("#task", "Where are the session list's rows drawn, and what did we say about them?");
  await page.press("#task", "Control+Enter");
  await page.waitForFunction(() => location.hash.startsWith("#/task/"));
  const task = await page.evaluate(() => location.hash);
  await page.waitForFunction(() => document.querySelector(".task-head .pill")?.textContent === "Idle", null, { timeout: 20000 });

  // One fold for the looks, each said in words; the index's build under the
  // search that made it, and no read of it marking a call.
  const fold = await page.evaluate(() => {
    document.querySelectorAll("details").forEach((d) => (d.open = true));
    const g = [...document.querySelectorAll(".activity-fold .activity")].pop();
    return {
      what: g.querySelector(".what").textContent,
      rows: [...g.querySelectorAll(".call > summary")].map((s) => `${s.querySelector(".name").textContent} ${s.querySelector(".args").textContent}`),
      marked: [...g.querySelectorAll(".call .stat")].map((s) => s.dataset.decision || "").filter(Boolean),
      index: [...g.querySelectorAll(".call")].map((c) => c.querySelector(".index-update")?.textContent || ""),
    };
  });
  assert.strictEqual(fold.what, "searched 3 times · recalled 1 event · updated the index · 1 failed");
  assert.strictEqual(fold.rows.length, 5, JSON.stringify(fold.rows));
  assert.deepStrictEqual(fold.rows.slice(0, 3), [
    "index-search “session row meta when running” in the repository",
    "index-search “session list rows” in this conversation",
    "index-search “session list rows” in earlier tasks",
  ]);
  assert.match(fold.rows[3], /^recall seq \d+$/);
  assert.strictEqual(fold.rows[4], "index-update the whole repository");
  assert.match(fold.index[0], /^Index updated: read \d+ files · holds \d+ files · \d+\.\d s$/, JSON.stringify(fold.index));
  assert.deepStrictEqual(fold.marked, [], "a read of the index build marked a call");
  step("the transcript says each search, the recall and the index update in words");

  // The task's Workspace tab: the index the search built, earlier tasks off.
  // The tab is read once it shows what is expected, or as it is when
  // waiting for that runs out.
  const workspace = async (expected) => {
    await page.click(".aside-tabs button:text-is('Workspace')");
    const read = () => page.evaluate(() => [...document.querySelectorAll(".panel")].find((p) => p.querySelector("h2")?.textContent === "Workspace")?.innerText || "");
    await until(`the Workspace tab to show ${expected}`, async () => expected.test(await read())).catch(() => {});
    return read();
  };
  let panel = await workspace(/Search index\n\d+ files/);
  assert.match(panel, /Search index\n\d+ files · [\d.]+ kB · just now/, panel);
  assert.match(panel, /Earlier tasks\nnot searched/, panel);
  step("the Workspace tab shows the index and that earlier tasks are not searched");

  // Workspaces: the folder's index line, and Forget, which the next search
  // undoes; then earlier tasks turned on, which the task then shows.
  await page.evaluate(() => (location.hash = "#/workspaces"));
  const row = `.ws-row:has(.mono[title="${repo}"])`;
  await page.waitForSelector(`${row} .ws-index:has-text('Search index:')`);
  assert.match(await page.textContent(`${row} .ws-index`), /^Search index: \d+ files · [\d.]+ kB · updated just now/);
  await page.click(`${row} .ws-index button:text-is('Forget the index')`);
  await page.waitForSelector(`${row} .ws-index:has-text('Search index:')`, { state: "detached" });
  assert.deepStrictEqual(sent.of("index.forget"), [{ repo }]);
  step("the Workspaces page shows the index, and forgets it");

  await page.click(`${row} button:text-is('Name it')`);
  await page.setChecked("#ws-earlier", true);
  await page.click("form.setup-form button:has-text('Add this folder'), form.setup-form button:has-text('Save')");
  await page.waitForSelector(`${row} .ws-earlier`);
  assert.strictEqual(sent.of("workspace.save").at(-1).earlier_tasks, true);
  await page.evaluate((hash) => (location.hash = hash), task);
  await page.waitForSelector(".task-head h1");
  panel = await workspace(/Search index\nbuilt at the first search/);
  assert.match(panel, /Earlier tasks\nsearched by the model/, panel);
  assert.match(panel, /Search index\nbuilt at the first search/, panel);
  await page.screenshot({ path: `${out}/retrieval-workspace.png` });
  step("earlier tasks turned on for the folder show in the task");
});
