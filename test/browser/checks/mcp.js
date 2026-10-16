// Connections adds, reviews and approves an MCP server; an undeclared tool
// asks every call, without a standing answer; a read tool is gathered
// silently.
const assert = require("assert");
const { out, line, requests, shows, step, ready, desk, check } = require("../lib");

check(async () => {
  const { demo, url, page } = await desk();
  const [mcp] = await line(demo, /MCP (\S+)/);
  const sent = requests(page);
  await page.goto(url);
  await ready(page);
  await page.evaluate(() => (location.hash = "#/connections"));
  await page.click("button:has-text('Add a server')");
  await page.fill("#mcp-name", "chat");
  await page.fill("#mcp-url", mcp);
  await page.click("form.card button.chip:has-text('receipts')");
  await page.click("button:has-text('Add and review its tools')");
  await page.waitForSelector(".server-detail table", { timeout: 10000 });
  const [added] = sent.of("mcp.save");
  const receipts = added?.server.workspaces[0];
  assert.match(receipts, /\/work\/receipts$/);
  assert.deepStrictEqual(added, { server: { name: "chat", workspaces: [receipts], tools: {}, url: mcp } }, "the server added");
  await shows(page, ".server-detail table", "search_messages", "list_channels", "post_message");
  step("server added; tools listed for review");

  await page.selectOption("select[aria-label='What search_messages does']", "read");
  await page.check("input[aria-label='Offer search_messages']");
  await page.check("input[aria-label='Offer post_message']");
  await page.screenshot({ path: `${out}/connections.png` });
  await page.click("button:has-text('Approve this tool list')");
  await page.waitForSelector(".server-row .dot.ok", { timeout: 10000 });
  // The tools turned on, search_messages declared as reading, and the list
  // pinned by its digest.
  const saved = sent.of("mcp.save");
  assert.strictEqual(saved.length, 2, "saved again before approving");
  assert.deepStrictEqual(saved[1].server.tools, { search_messages: { effect: "read", on: true }, post_message: { on: true } });
  const approved = sent.of("mcp.approve");
  assert.strictEqual(approved.length, 1);
  assert.strictEqual(approved[0].name, "chat");
  assert.match(approved[0].manifest, /^[0-9a-f]{64}$/);
  step("approved and pinned");

  await page.evaluate(() => (location.hash = "#/"));
  await page.click("#work-in button:has-text('receipts')");
  await page.fill("#task", "Draft this week's status update and post it to #eng-status");
  await page.click("button[aria-label='Start the task']");
  await page.waitForSelector(".ask-card", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.open"), [{ repo: receipts }], "the task opened in receipts");
  const [{ id }] = sent.of("session.send");
  assert.deepStrictEqual(sent.of("session.send"), [{ id, text: "Draft this week's status update and post it to #eng-status" }]);
  await shows(page, ".ask-card", "chat/post_message", "undeclared tool", "#eng-status", "No standing answer is offered for this call");
  const buttons = await page.$$eval(".ask-card button", (bs) => bs.map((b) => b.textContent.replace(/\s+/g, " ").trim()));
  assert.deepStrictEqual(buttons, ["Allow onceY", "DenyN"], "only once or no");
  await page.screenshot({ path: `${out}/mcp-task.png` });

  await page.click(".ask-card button[data-key='Y']");
  await page.waitForSelector(".prose h2", { timeout: 20000 });
  assert.deepStrictEqual(sent.of("session.answer").map((a) => [a.id, a.reply]), [[id, "y"]]);
  await shows(page, ".transcript", "Posted to #eng-status.");
  step("posted and answered");
  const gathered = (await page.textContent(".activity summary")).replace(/\s+/g, " ").trim();
  assert.strictEqual(gathered, "Gathered1 server · searched oncechat/search_messages", "the read is gathered without asking");

  // Disconnecting drops the approved tool list and the folders the server is
  // offered in, so it asks first as every removal does: Cancel keeps it, and
  // only the confirm sends mcp.remove.
  await page.evaluate(() => (location.hash = "#/connections"));
  await page.waitForSelector(".server-row", { timeout: 10000 });
  // The server's page stays open between visits to Connections.
  if (!(await page.locator(".server-detail").count())) await page.click(".server-row:has-text('chat')");
  await page.waitForSelector(".server-detail button:text-is('Disconnect')", { timeout: 10000 });
  await page.click(".server-detail button:text-is('Disconnect')");
  await shows(page, ".server-detail", "Its approved tool list and the folders it is offered in are dropped");
  assert.strictEqual(sent.of("mcp.remove").length, 0, "Disconnect removed the server before it was confirmed");
  await page.click(".server-detail button:text-is('Cancel')");
  await page.click(".server-detail button:text-is('Disconnect')");
  await page.click(".server-detail button:text-is('Disconnect')");
  await page.waitForSelector(".server-row", { state: "detached", timeout: 10000 });
  assert.deepStrictEqual(sent.of("mcp.remove"), [{ name: "chat" }]);
  // The tabs of Connections are the two it manages; the other two went to the
  // pages that manage models and folders, and their addresses follow.
  assert.deepStrictEqual(await page.$$eval("[id^='connections-tab-']", (tabs) => tabs.map((t) => t.textContent)), ["Tools", "Credentials"]);
  await page.evaluate(() => (location.hash = "#/connections/models"));
  await page.waitForSelector("#section-models");
  assert.strictEqual(await page.evaluate(() => location.hash), "#/settings/models");
  await page.evaluate(() => (location.hash = "#/connections/workspaces"));
  await page.waitForSelector("h1:text-is('Workspaces')");
  assert.strictEqual(await page.evaluate(() => location.hash), "#/workspaces");
  step("Disconnect asks first; the tabs that duplicated other pages went to them");
});
