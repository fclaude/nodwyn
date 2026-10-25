import { register, assert } from '/passkey.js';
const $ = id => document.getElementById(id);
let selected = '', projects = [], parent = false;
async function call(path, body) {
  const response = await fetch(path, body === undefined ? {credentials:'same-origin'} : {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),credentials:'same-origin'});
  if (!response.ok) { if (response.status === 403) { $('login').hidden=false; $('workspace').hidden=true; } throw new Error((await response.text()).trim()); }
  return response.status === 204 ? {} : response.json();
}
function action(fn) { return async event => { event?.preventDefault(); $('error').textContent=''; try { await fn(); } catch(error) { $('error').textContent=error.message; } }; }
function draw() {
  $('projects').replaceChildren(...projects.map(p => { const b=document.createElement('button');b.textContent=p.name;b.setAttribute('aria-pressed',String(p.id===selected));b.onclick=()=>{selected=p.id;draw();};return b; }));
  const p=projects.find(p=>p.id===selected);
  $('project').hidden=!p;
  $('parent').hidden=!parent;
  if(!p)return;
  $('title').textContent=p.name;
  $('preview').href=p.preview;
  $('public').href=p.public;
  $('status').textContent=p.status;
  $('messages').replaceChildren(...p.messages.map(m=>{const el=document.createElement('p');el.className=m.role;el.textContent=m.text;return el;}));
  $('send').disabled=p.busy; $('stop').disabled=!p.busy; $('rename').disabled=p.busy; $('delete').disabled=p.busy;
}
async function refresh(){
  const state=await call('/kids/api/state');projects=state.projects;parent=state.parent;
  if(!projects.some(p=>p.id===selected))selected=projects[0]?.id||'';
  $('login').hidden=true;$('workspace').hidden=false;draw();
}
$('pair').onsubmit=action(async()=>{await call('/pair',{invite:$('invite').value,device:$('device').value});$('invite').value='';await refresh();});
$('register').onclick=action(async()=>{await register($('invite').value,$('device').value);$('invite').value='';await refresh();});
$('signin').onclick=action(async()=>{await assert($('device').value||'parent-browser');await refresh();});
$('create').onsubmit=action(async()=>{const p=await call('/kids/api/create',{name:$('name').value});selected=p.id;$('name').value='';await refresh();});
$('chat').onsubmit=action(async()=>{await call('/kids/api/chat',{id:selected,text:$('text').value});$('text').value='';await refresh();});
$('stop').onclick=action(async()=>{await call('/kids/api/stop',{id:selected});await refresh();});
$('rename').onclick=action(async()=>{const p=projects.find(p=>p.id===selected);const name=prompt('New project name',p.name);if(!name)return;await call('/kids/api/rename',{id:p.id,version:p.version,name});await refresh();});
$('delete').onclick=action(async()=>{const p=projects.find(p=>p.id===selected);const check=await call('/kids/api/delete-check',{id:p.id,version:p.version});if(!confirm(`Delete ${p.name}? This removes its app and website.`))return;await call('/kids/api/delete',{id:p.id,version:p.version,confirm:check.confirm});await refresh();});
$('touch').onclick=action(async()=>{await assert();$('parent-result').textContent='Confirmed';});
$('invite-child').onclick=action(async()=>{$('parent-result').textContent=JSON.stringify(await call('/kids/admin/invite',{}),null,2);});
$('devices').onclick=action(async()=>{$('parent-result').textContent=JSON.stringify(await call('/kids/admin/devices',{}),null,2);});
$('revoke').onclick=action(async()=>{await call('/kids/admin/revoke',{name:$('revoke-name').value});$('parent-result').textContent='Device revoked';});
await action(refresh)();
setInterval(()=>refresh().catch(()=>{}),1500);
