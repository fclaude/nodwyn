const { requests, ready, desk, check } = require("../lib");

check(async () => {
  const { page, url } = await desk();
  const sent = requests(page);
  await page.goto(url);
  await ready(page);
  await page.click('.nav-link[href="#/workflows"]');
  await page.click("#new-watcher");
  await page.waitForSelector("#wf-name");
  const first = sent.of("credentials.list").length;
  const focusSurvived = await page.evaluate(async () => {
    const name = document.querySelector("#wf-name");
    name.focus();
    await new Promise(resolve => setTimeout(resolve, 150));
    return name === document.activeElement && name.isConnected;
  });
  const extra = sent.of("credentials.list").length - first;
  await page.close();
  if (!focusSurvived || extra > 1) {
    throw new Error(`The workflow builder lost focus and requested credentials ${extra} more times in 150 ms.`);
  }
});
