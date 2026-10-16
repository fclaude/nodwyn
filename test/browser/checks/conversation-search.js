// Search real fixture logs, follow a hit into an old run and page only that
// turn. No model request or session resume is needed to read the history.
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { out, requests, openTaskMenu, until, ready, desk, check } = require("../lib");

check(async () => {
  const { url, repo, page } = await desk();
  const sessions = path.join(repo, "../../data/sessions");
  const sample = fs.readdirSync(sessions).filter((s) => s.endsWith('.jsonl')).flatMap((s) => fs.readFileSync(path.join(sessions,s),'utf8').trim().split('\n').map(JSON.parse)).find((e) => e.kind === 'session.start');
  const spans = {};
  function fixture(id, project, archived = false, worktree = false) {
    const events = [];
    const add = (kind,payload) => { events.push({session:id,seq:events.length+1,ts:new Date(Date.UTC(2026,9,14,9,0,events.length)).toISOString(),kind,payload}); return events.length; };
    add('session.start',{...sample.payload,repo:worktree ? `${project}/.nodwyn/worktrees/search` : project,worktree:worktree ? {repository:project,path:`${project}/.nodwyn/worktrees/search`,branch:'nodwyn/search',base:'main',base_commit:'abc'} : null});
    add('session.title',{title:id === 'search-earlier' ? 'Earlier <script>literal title</script>' : id});
    for (let run = 0; run < (id === 'search-anchor' ? 3 : 1); run++) {
      const first = add('schedule.run',{due:'2026-10-14T09:00:00Z',started:'2026-10-14T09:00:00Z',through:'2026-10-14T09:00:00Z',manual:false,cost:0,spend_before:0});
      const from = add('user.msg',{text:`Request for ${id}, run ${run + 1}`});
      if (run === 0) {
        for(let n=0;n<600;n++) add('guard.decision',{prim:'fs-read',decision:'allow',origin:'model'});
        add('tool.call',{id:'result',name:'fs-read',args:{path:'evidence.txt'}});
        const hit = add('tool.result',{id:'result',name:'fs-read',ok:true,value:'Found starling-lantern <script>literal result</script> sk-ant-abcdefgh1234\nThis was the chosen fix.'});
        for(let n=0;n<150;n++) add('guard.decision',{prim:'fs-read',decision:'allow',origin:'model'});
        if (id === 'search-anchor') spans[id] = {first,from,hit};
      }
      add('llm.resp',{content:run === 0 ? 'starling-lantern was fixed here.' : `Different reply in run ${run + 1}`,usage:{prompt_tokens:10,output_tokens:5,cost:0}});
      const through=add('turn.end',{outcome:'completed'});
      if(id === 'search-anchor' && run === 0) spans[id].through=through;
    }
    if (archived) add('session.archive',{archived:true});
    add('session.end',{reason:'closed'});
    fs.writeFileSync(path.join(sessions,`${id}.jsonl`),events.map(JSON.stringify).join('\n')+'\n',{mode:0o600});
  }
  fixture('search-anchor',repo);
  fixture('search-earlier',repo,true);
  fixture('search-worktree',repo,false,true);
  fixture('search-unrelated',repo.replace(/\/console$/,'/receipts'));
  const sent=requests(page);
  let failSearch=false,failContext=false,holdQuery='',held=null,heldReady=false;
  await page.routeWebSocket('**/socket',async(ws)=>{
    const server=await ws.connectToServer();
    const queries=new Map();
    ws.onMessage((message)=>{
      const frame=JSON.parse(message);
      if(frame.method==='conversation.search') {
        queries.set(frame.id,frame.params.query);
        if(failSearch) {failSearch=false;ws.send(JSON.stringify({jsonrpc:'2.0',id:frame.id,error:{code:-32000,message:'simulated search failure'}}));return;}
      }
      if(frame.method==='log.context' && failContext) {failContext=false;ws.send(JSON.stringify({jsonrpc:'2.0',id:frame.id,error:{code:-32000,message:'simulated context failure'}}));return;}
      server.send(message);
    });
    server.onMessage((message)=>{
      const frame=JSON.parse(message);
      if(holdQuery && queries.get(frame.id)===holdQuery) {held=()=>ws.send(message);heldReady=true;return;}
      ws.send(message);
    });
  });
  const go=(hash)=>page.evaluate((hash)=>location.hash=hash,hash);
  const loaded=()=>page.waitForSelector('.conversation-context[data-loaded]');
  const search=async(query,scope='conversation')=>{
    await page.fill('#conversation-query',query);await page.selectOption('#conversation-scope',scope);
    await page.click('.conversation-search-form button[type=submit]');
  };
  await page.goto(url);await ready(page);
  await go('#/task/search-anchor');await page.waitForSelector('.task[data-loaded]');
  await openTaskMenu(page);await page.click('a[aria-label="Search conversation"]');await page.waitForSelector('#conversation-query');
  await search('starling-lantern');
  await until('task search results',async()=>await page.locator('.search-task').count()===1);
  assert.strictEqual(await page.locator('.search-task').getAttribute('data-task'),'search-anchor');
  assert.ok((await page.locator('.search-results').textContent()).includes('literal result'));
  assert.strictEqual(await page.locator('.search-results script').count(),0,'a snippet became markup');
  assert.ok(!(await page.locator('.search-results').textContent()).includes('sk-ant-'),'legacy credential leaked');
  assert.strictEqual(sent.of('conversation.search').at(-1).scope,'conversation');
  const searches=sent.of('conversation.search').length;
  await page.click('.conversation-search-form button[type=submit]');
  await until('same query refreshed',()=>sent.of('conversation.search').length===searches+1);
  await until('refreshed matches',async()=>await page.locator('.search-match').count()===2);
  await page.screenshot({path:`${out}/search-results.png`});
  const link=page.locator(`.search-match[data-seq="${spans['search-anchor'].hit}"] a`);
  const target=await link.getAttribute('href');
  const reads=sent.of('log.read').length, subscriptions=sent.of('session.subscribe').length;
  await link.click();await loaded();
  assert.strictEqual(sent.of('log.read').length,reads,'initial context loaded unrelated history pages');
  assert.strictEqual(sent.of('session.subscribe').length,subscriptions,'a historical read subscribed to live state');
  assert.deepStrictEqual(sent.of('log.context').at(-1),{id:'search-anchor',seq:spans['search-anchor'].hit});
  assert.ok((await page.locator('.conversation-match').textContent()).includes('starling-lantern'));
  assert.strictEqual(await page.locator('.ask-card, #reply, .task-actions').count(),0,'historical state gained live controls');
  assert.ok(!(await page.locator('.history-transcript').textContent()).includes('Different reply'),'latest run was fetched instead of the hit');
  await page.screenshot({path:`${out}/search-context.png`});
  await page.click('button:text-is("Load earlier in this turn")');
  await until('earlier history loaded',()=>sent.of('log.read').length>reads);
  await page.waitForFunction(()=>!document.querySelector('.conversation-context button').disabled);
  const earlier=sent.of('log.read').at(-1);
  assert.ok(earlier.after>=spans['search-anchor'].from-1 && earlier.through<spans['search-anchor'].hit);
  await page.click('button:text-is("Load later in this turn")');
  await until('later turn loaded',async()=>(await page.locator('.history-transcript').textContent()).includes('was fixed here'));
  assert.ok(sent.of('log.read').at(-1).through<=spans['search-anchor'].through,'paging escaped the turn');
  assert.ok(!(await page.locator('.history-transcript').textContent()).includes('Different reply'));

  await page.click('a:text-is("Back to search results")');
  await until('restored search results',async()=>await page.locator('.search-match').count()===2);
  assert.strictEqual(await page.inputValue('#conversation-query'),'starling-lantern');
  await search('starling-lantern','earlier');
  await until('project results',async()=>await page.locator('.search-task').count()===2);
  assert.deepStrictEqual((await page.locator('.search-task').evaluateAll((nodes)=>nodes.map((n)=>n.dataset.task))).sort(),['search-earlier','search-worktree']);
  assert.ok((await page.locator('.search-results').textContent()).includes('Archived'));
  assert.strictEqual(await page.locator('.search-task script').count(),0,'a task title became markup');
  assert.strictEqual(sent.of('workspace.save').length,0,'human search enabled model access');
  await page.reload();await ready(page);
  await until('project search after reload',async()=>await page.locator('.search-task').count()===2);
  assert.strictEqual(await page.inputValue('#conversation-scope'),'earlier');
  await search('unfindableword');await page.waitForSelector('.search-status:text-is("No matching conversation found.")');
  failSearch=true;await search('failureword');await page.waitForSelector('button:text-is("Retry search")');
  await page.click('button:text-is("Retry search")');await page.waitForSelector('.search-status:text-is("No matching conversation found.")');
  failContext=true;await go(target);await page.waitForSelector('button:text-is("Retry conversation")');
  await page.click('button:text-is("Retry conversation")');await loaded();
  await page.click('a:text-is("Back to search results")');
  holdQuery='heldoldquery';held=null;heldReady=false;
  await search(holdQuery);await until('held old search',()=>heldReady);
  await go(target);await loaded();
  holdQuery='';held();
  await page.waitForTimeout(100);
  assert.strictEqual(await page.locator('.conversation-context[data-loaded]').count(),1,'an old search replaced the matching conversation');
  assert.strictEqual(sent.of('session.resume').length,0,'search resumed a task');
  assert.strictEqual(sent.of('session.send').length,0,'search sent a message');
  await page.setViewportSize({width:390,height:844});
  await page.waitForFunction(()=>document.getAnimations().every((a)=>a.playState==='finished'||a.effect.getTiming().iterations===Infinity));
  assert.strictEqual(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'history overflowed the phone');
  await page.screenshot({path:`${out}/search-context-phone.png`});
  await page.click('a:text-is("Back to search results")');
  await search('starling-lantern','earlier');
  await until('phone project results',async()=>await page.locator('.search-task').count()===2);
  assert.strictEqual(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'search overflowed the phone');
  await page.screenshot({path:`${out}/search-results-phone.png`});
  console.log('search scopes, archived/worktree isolation, snippets, bounded context, paging, reload, retries, stale responses and phone layouts');
});
