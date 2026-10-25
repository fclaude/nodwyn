// This drives the actual managed HTTP application with a fake local model.
const {chromium,webkit}=require('playwright');
const {spawn,execFileSync}=require('child_process');
const fs=require('fs');
const path=require('path');
const assert=require('assert/strict');
(async()=>{
 const binary=path.join(process.argv[2],'kids-demo');
 execFileSync('go',['build','-o',binary,'./test/browser/kids-demo'],{cwd:path.resolve(__dirname,'../../..'),stdio:'inherit'});
 const demo=spawn(binary,[],{stdio:['ignore','pipe','inherit']});
 let browser;
 try {
  const fixture=await new Promise((resolve,reject)=>{let text='';demo.stdout.on('data',b=>{text+=b;if(text.includes('\n')){try{resolve(JSON.parse(text.split('\n')[0]));}catch(e){reject(e);}}});demo.once('exit',c=>reject(new Error(`demo exited ${c}`)));});
  browser=await (process.env.BROWSER==='webkit'?webkit:chromium).launch({headless:true});
  const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(fixture.url);
  await page.locator('#invite').fill(fixture.invite);await page.locator('#device').fill('laptop1-child');await page.locator('#pair button[type=submit],#pair button:not([type])').click();
  await page.locator('#workspace').waitFor({state:'visible'});
  await page.locator('#name').fill('space');await page.locator('#create button').click();await page.locator('#title').filter({hasText:'space'}).waitFor();
  await page.locator('#text').fill('Make a space game');await page.locator('#send').click();await page.getByText('I made your space game. Use See app to try it.',{exact:true}).waitFor();
  assert.equal(await page.locator('#preview').getAttribute('href'),'https://child.test:8443/space/');
  assert.equal(await page.locator('#parent').isVisible(),false);
  const roots=fs.readdirSync(fixture.workspace);assert.equal(roots.length,1);assert.match(fs.readFileSync(path.join(fixture.workspace,roots[0],'index.html'),'utf8'),/Space game/);
  const checks=await page.evaluate(async()=>{const result={};for(const path of ['/socket','/attach','/kids/admin/invite','/kids/admin/devices']){const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});result[path]=r.status;}return result;});
  for(const code of Object.values(checks))assert.ok(code===403||code===404||code===405);
  page.once('dialog',d=>d.accept('orbit'));await page.locator('#rename').click();await page.locator('#title').filter({hasText:'orbit'}).waitFor();
  assert.equal(await page.locator('#preview').getAttribute('href'),'https://child.test:8443/orbit/');
  page.once('dialog',d=>d.dismiss());await page.locator('#delete').click();assert.equal(await page.locator('#project').isVisible(),true);
  await page.screenshot({path:path.join(process.argv[2],'projects.png'),fullPage:true});
  page.once('dialog',d=>d.accept());await page.locator('#delete').click();await page.locator('#project').waitFor({state:'hidden'});
  assert.equal(fs.readdirSync(fixture.workspace).length,0);
  assert.deepEqual(errors,[]);
  console.log('Child pairing, create, chat/file mutation, preview links, rename, cancel/delete and parent/RPC refusal passed.');
 }finally{if(browser)await browser.close();demo.kill('SIGTERM');}
})().catch(e=>{console.error(e);process.exit(1);});
