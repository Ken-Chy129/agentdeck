/* AgentDeck console — vanilla JS, no build step. */
const $ = (s, el = document) => el.querySelector(s);
const h = (s) => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const app = $('#app');

// ---------- auth + api ----------
const TOKEN_KEY = 'agentdeck_admin_token';
let token = localStorage.getItem(TOKEN_KEY) || '';
async function api(method, path, body, raw) {
  const opt = { method, headers: { Authorization: 'Bearer ' + token } };
  if (body !== undefined) {
    if (raw) { opt.body = body; }
    else { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  }
  const r = await fetch(path, opt);
  if (r.status === 401) { showLogin(); throw new Error('unauthorized'); }
  const ct = r.headers.get('content-type') || '';
  const data = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}
function toast(msg, bad) {
  const d = document.createElement('div'); d.className = 't' + (bad ? ' bad' : ''); d.textContent = msg;
  $('#toast').appendChild(d); setTimeout(() => d.remove(), bad ? 6000 : 3000);
}
const fail = (e) => toast(e.message || String(e), true);

function showLogin() {
  app.innerHTML = `<div class="card login"><h1>登录 AgentDeck</h1>
    <p class="muted">输入服务端 data/admin_token 里的管理 token。</p>
    <input id="tok" type="password" placeholder="adadmin_…" style="width:100%">
    <div class="row" style="margin-top:12px"><button id="go">进入</button></div></div>`;
  $('#go').onclick = async () => {
    token = $('#tok').value.trim(); localStorage.setItem(TOKEN_KEY, token);
    try { await api('GET', '/api/admin/me'); route(); } catch (e) { fail(new Error('token 不对')); }
  };
  $('#tok').onkeydown = e => { if (e.key === 'Enter') $('#go').click(); };
}
$('#logout').onclick = () => { localStorage.removeItem(TOKEN_KEY); token = ''; showLogin(); };

// ---------- utils ----------
const ago = (iso) => {
  if (!iso) return '从未';
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 60) return Math.floor(s) + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
};
const online = (iso) => { if (!iso) return ''; const s = (Date.now() - new Date(iso)) / 1000; return s < 1800 ? 'on' : s < 86400 ? 'stale' : ''; };
const kb = (n) => n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(1) + ' MB';
const semverLt = (a, b) => {
  if (!a || !b) return false;
  const pa = a.split(/[.-]/).map(x => parseInt(x, 10)), pb = b.split(/[.-]/).map(x => parseInt(x, 10));
  for (let i = 0; i < 3; i++) { const x = pa[i] || 0, y = pb[i] || 0; if (x !== y) return x < y; }
  return false;
};
function copy(text) { navigator.clipboard.writeText(text).then(() => toast('已复制')); }
window.copyText = copy;

// ---------- routing ----------
function route() {
  if (!token) return showLogin();
  const hash = location.hash || '#/machines';
  const [, tab, id] = hash.split('/');
  document.querySelectorAll('nav a').forEach(a => a.classList.toggle('active', a.dataset.tab === tab));
  const view = { machines: id ? machineDetail : machines, skills: id ? skillDetail : skills, matrix, configs, env: envView }[tab] || machines;
  view(decodeURIComponent(id || '')).catch(fail);
}
window.addEventListener('hashchange', route);

// ---------- machines ----------
async function machines() {
  const ov = await api('GET', '/api/admin/overview');
  const ms = ov.machines;
  const pkgs = new Set();
  ms.forEach(m => (m.inventory?.tools || []).forEach(t => t.package && pkgs.add(t.package)));
  let latest = {};
  if (pkgs.size) { try { latest = await api('GET', '/api/admin/npm-latest?pkgs=' + [...pkgs].join(',')); } catch {} }

  app.innerHTML = `<div class="row" style="justify-content:space-between"><h1>机器</h1>
    <button id="enroll" class="ghost">+ 添加机器</button></div>
    <div id="enrollBox"></div>
    <div class="grid">${ms.map(m => machineCard(m, ov.assignments[m.id] || [], latest)).join('') || '<p class="muted">还没有机器。点「添加机器」生成一次性注册 token。</p>'}</div>`;
  $('#enroll').onclick = async () => {
    const r = await api('POST', '/api/admin/enroll-tokens', { note: 'from console' });
    const base = location.origin;
    $('#enrollBox').innerHTML = `<div class="card"><b>在新机器上执行</b>（token 一次有效）：
      <pre>agentdeck login ${base} ${r.enroll_token} --name $(hostname -s)
agentdeck sync</pre>
      <details><summary>还没装 agentdeck CLI？</summary><pre>go install github.com/Ken-Chy129/agentdeck/cmd/agentdeck@latest</pre>
      <p class="muted small">仓库是公开的，不需要凭证；需要 Go ≥ 1.25（低版本会自动下载工具链）。</p></details></div>`;
  };
}
function machineCard(m, assigned, latest) {
  const inv = m.inventory || {};
  const tools = (inv.tools || []).filter(t => ['claude', 'codex', 'gemini', 'cursor-agent', 'opencode'].includes(t.name));
  const behind = tools.filter(t => t.package && latest[t.package] && semverLt(t.version, latest[t.package]));
  return `<div class="card">
    <div class="row" style="justify-content:space-between">
      <a href="#/machines/${m.id}" style="font-size:16px;font-weight:600"><span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)}</a>
      <span class="muted small">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</span></div>
    <div class="muted small" style="margin:4px 0 10px">最近同步 ${ago(m.last_seen_at)} · ${assigned.length} 个 skill 已分发${behind.length ? ` · <span class="behind">${behind.length} 个 CLI 可升级</span>` : ''}</div>
    <table>${tools.map(t => {
      const lv = t.package ? latest[t.package] : '';
      const old = lv && semverLt(t.version, lv);
      return `<tr><td class="mono">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}${old ? ` → ${h(lv)}` : ''}</td><td class="muted small">${h(t.source)}</td></tr>`;
    }).join('') || '<tr><td class="muted">尚无 inventory，等第一次 sync</td></tr>'}</table>
  </div>`;
}

async function machineDetail(id) {
  const [{ machine: m, assigned }, logs, jobs, skillsAll] = await Promise.all([
    api('GET', `/api/admin/machines/${id}`), api('GET', `/api/admin/machines/${id}/logs`),
    api('GET', `/api/admin/machines/${id}/jobs`), api('GET', '/api/admin/skills')]);
  const inv = m.inventory || {};
  const pkgs = (inv.tools || []).filter(t => t.package).map(t => t.package);
  let latest = {};
  if (pkgs.length) { try { latest = await api('GET', '/api/admin/npm-latest?pkgs=' + pkgs.join(',')); } catch {} }
  const local = m.local_skills || [];
  const assignedNames = new Set(assigned.map(a => a.skill_name));
  const upgradeCmd = (t) => t.source === 'npm-global' && t.package ? `npm i -g ${t.package}@latest` : t.source === 'brew' ? `brew upgrade ${t.name}` : t.source === 'native' && t.name === 'claude' ? 'claude update' : '';

  app.innerHTML = `<a href="#/machines" class="muted small">← 机器</a>
  <div class="row" style="justify-content:space-between"><h1><span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)} <span class="muted small mono">${h(m.id)}</span></h1>
   <div class="row"><button class="ghost small" id="rename">重命名</button><button class="danger small" id="del">删除机器</button></div></div>
  <dl class="kv card"><dt>主机名</dt><dd class="mono">${h(inv.hostname || m.hostname)}</dd><dt>系统</dt><dd class="mono">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</dd>
   <dt>最近同步</dt><dd>${ago(m.last_seen_at)} <span class="muted small">${h(m.last_seen_at || '')}</span></dd><dt>Inventory</dt><dd>${ago(m.inventory_at)}</dd></dl>

  <h2>Agent CLI</h2><div class="card"><table><tr><th>工具</th><th>版本</th><th>最新</th><th>来源</th><th>路径</th><th></th></tr>
  ${(inv.tools || []).map(t => { const lv = t.package ? latest[t.package] : ''; const old = lv && semverLt(t.version, lv); const cmd = upgradeCmd(t);
    return `<tr><td class="mono">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}</td><td class="mono muted">${h(lv || '')}</td><td class="muted small">${h(t.source)}${t.package ? ' · ' + h(t.package) : ''}</td><td class="mono small muted">${h(t.path || '')}</td>
    <td class="row" style="justify-content:flex-end;flex-wrap:nowrap">${cmd ? `<button class="ghost small" onclick="copyText(${JSON.stringify(cmd)})">复制命令</button>` : ''}
    ${t.source === 'npm-global' && t.package ? `<button class="small" data-job="npm_upgrade" data-pkg="${h(t.package)}">下次 sync 升级</button>` : ''}
    ${t.source === 'brew' ? `<button class="small" data-job="brew_upgrade" data-formula="${h(t.name)}">下次 sync 升级</button>` : ''}</td></tr>`; }).join('') || '<tr><td class="muted">无</td></tr>'}</table></div>

  <h2>运行时</h2><div class="card"><table>${(inv.runtimes || []).map(r => `<tr><td class="mono">${h(r.name)}</td><td class="mono">${h(r.version)}</td><td class="mono muted small">${h(r.path)}</td></tr>`).join('')}</table>
  <details style="margin-top:8px"><summary>npm -g 全部 ${(inv.npm_global || []).length} 个 · brew ${(inv.brew || []).length} 个</summary>
  <div class="row" style="align-items:flex-start"><table>${(inv.npm_global || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted">${h(p.version)}</td></tr>`).join('')}</table>
  <table>${(inv.brew || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted">${h(p.version)}</td></tr>`).join('')}</table></div></details></div>

  <h2>Skill</h2><div class="card">
   <div class="row" style="margin-bottom:8px"><select id="addSkill"><option value="">分发一个 skill 到这台机器…</option>${skillsAll.filter(s => !assignedNames.has(s.name)).map(s => `<option>${h(s.name)}</option>`).join('')}</select></div>
   <table><tr><th>名称</th><th>目标版本</th><th>本机状态</th><th></th></tr>
   ${assigned.map(a => { const l = local.find(x => x.name === a.skill_name); const st = !l ? '<span class="pill warn">未安装</span>' : l.digest === a.digest ? '<span class="pill ok">已同步</span>' : `<span class="pill warn">${l.managed ? 'v' + l.version + ' → 待更新' : '本地有改动'}</span>`;
     return `<tr><td><a href="#/skills/${h(a.skill_name)}" class="mono">${h(a.skill_name)}</a></td><td>v${a.version}</td><td>${st}</td><td style="text-align:right"><button class="ghost small" data-unassign="${h(a.skill_name)}">取消分发</button></td></tr>`; }).join('') || '<tr><td class="muted">没有分发任何 skill</td></tr>'}</table>
   ${local.filter(l => !assignedNames.has(l.name)).length ? `<details style="margin-top:8px"><summary>本机其他 skill（未托管）${local.filter(l => !assignedNames.has(l.name)).length} 个</summary>
     <table>${local.filter(l => !assignedNames.has(l.name)).map(l => `<tr><td class="mono small">${h(l.name)}</td><td class="muted small">${kb(l.size)}</td><td class="muted small">${l.managed ? '曾托管，将在下次 sync 移除' : ''}</td></tr>`).join('')}</table></details>` : ''}
  </div>

  <h2>任务</h2><div class="card"><table><tr><th>#</th><th>类型</th><th>参数</th><th>状态</th><th>结果</th><th></th></tr>
  ${jobs.map(j => `<tr><td class="mono">${j.id}</td><td class="mono">${h(j.type)}</td><td class="mono small">${h(JSON.stringify(j.payload))}</td><td><span class="pill ${j.status === 'done' ? 'ok' : j.status === 'failed' ? 'bad' : j.status === 'queued' ? 'warn' : ''}">${h(j.status)}</span></td>
   <td>${j.result ? `<details><summary class="small">输出</summary><pre>${h(j.result)}</pre></details>` : ''}</td><td>${j.status === 'queued' ? `<button class="ghost small" data-cancel="${j.id}">取消</button>` : ''}</td></tr>`).join('') || '<tr><td class="muted">无</td></tr>'}</table></div>

  <h2>同步日志</h2><div class="card">${logs.map(l => { const s = l.summary || {}; const changed = (s.skills || []).filter(x => x.action !== 'unchanged');
    return `<details><summary><span class="mono small">${h(l.at)}</span> · ${changed.length ? changed.map(x => `${h(x.name)}:${h(x.action)}`).join(', ') : '无变化'} · ${(s.jobs || []).length} 任务 · ${h(s.duration)}${s.error ? ` · <span class="behind">${h(s.error)}</span>` : ''}</summary><pre>${h(JSON.stringify(s, null, 2))}</pre></details>`; }).join('') || '<p class="muted">无</p>'}</div>`;

  $('#rename').onclick = async () => { const n = prompt('新名称', m.name); if (n && n !== m.name) { await api('PATCH', `/api/admin/machines/${id}`, { name: n }); route(); } };
  $('#del').onclick = async () => { if (confirm(`删除机器 ${m.name}？该机器的 token 立刻失效。`)) { await api('DELETE', `/api/admin/machines/${id}`); location.hash = '#/machines'; } };
  $('#addSkill').onchange = async e => { if (e.target.value) { await api('PUT', '/api/admin/assignments', { machine_id: id, skill: e.target.value, assigned: true }); toast('已分发，机器下次 sync 生效'); route(); } };
  app.querySelectorAll('[data-unassign]').forEach(b => b.onclick = async () => { await api('PUT', '/api/admin/assignments', { machine_id: id, skill: b.dataset.unassign, assigned: false }); route(); });
  app.querySelectorAll('[data-job]').forEach(b => b.onclick = async () => {
    const payload = b.dataset.job === 'npm_upgrade' ? { package: b.dataset.pkg, version: 'latest' } : { formula: b.dataset.formula };
    await api('POST', `/api/admin/machines/${id}/jobs`, { type: b.dataset.job, payload }); toast('任务已排队，机器下次 sync 执行'); route();
  });
  app.querySelectorAll('[data-cancel]').forEach(b => b.onclick = async () => { await api('DELETE', `/api/admin/jobs/${b.dataset.cancel}`); route(); });
}

// ---------- skills ----------
async function skills() {
  const ks = await api('GET', '/api/admin/skills');
  app.innerHTML = `<div class="row" style="justify-content:space-between"><h1>Skill 库</h1>
   <div class="row"><button class="ghost" id="upload">上传 tar.gz</button><button id="create">+ 新建 skill</button></div></div>
   <div class="card"><table><tr><th>名称</th><th>描述</th><th>版本</th><th>大小</th><th>分发到</th><th>更新</th></tr>
   ${ks.map(k => `<tr><td><a href="#/skills/${h(k.name)}" class="mono">${h(k.name)}</a></td><td class="muted small">${h(k.description)}</td><td>v${k.current_version}</td><td class="muted small">${kb(k.current_size)}</td><td>${k.machine_count} 台</td><td class="muted small">${ago(k.updated_at)}</td></tr>`).join('') || '<tr><td class="muted">还没有 skill。新建一个，或在机器上 <code>agentdeck push ~/.agents/skills/xxx</code>。</td></tr>'}</table></div>
   <input type="file" id="file" accept=".tgz,.tar.gz,application/gzip" hidden>`;
  $('#create').onclick = () => editor(null);
  $('#upload').onclick = () => $('#file').click();
  $('#file').onchange = async e => {
    const f = e.target.files[0]; if (!f) return;
    const name = prompt('skill 名称（小写字母/数字/-）', f.name.replace(/(-v\d+)?\.(tgz|tar\.gz)$/, ''));
    if (!name) return;
    try { const r = await api('POST', `/api/admin/skills/${name}?note=upload`, f, true); toast(r.created ? `已发布 ${name} v${r.version.version}` : '内容未变化'); location.hash = '#/skills/' + name; } catch (err) { fail(err); }
  };
}

async function skillDetail(name) {
  const [{ skill: k, versions, machines: ms }, all] = await Promise.all([api('GET', `/api/admin/skills/${name}`), api('GET', '/api/admin/machines')]);
  const assigned = new Set(ms);
  const cur = versions.find(v => v.id === k.current_version_id) || versions[0];
  const files = cur ? await api('GET', `/api/admin/skills/${name}/versions/${cur.id}/files`) : { files: [] };

  app.innerHTML = `<a href="#/skills" class="muted small">← Skill 库</a>
  <div class="row" style="justify-content:space-between"><h1 class="mono">${h(k.name)} <span class="muted small">v${k.current_version}</span></h1>
   <div class="row"><button id="edit">编辑并发布新版本</button><a class="btn ghost" style="padding:6px 12px;border:1px solid var(--line);border-radius:6px" href="/api/admin/skills/${h(name)}/versions/${cur?.id}/archive" onclick="event.preventDefault();dl(this.href,'${h(name)}-v${k.current_version}.tar.gz')">下载</a><button class="danger small" id="del">删除</button></div></div>
  <p class="muted" id="desc">${h(k.description) || '<i>无描述</i>'} <button class="ghost small" id="editDesc">改</button></p>

  <h2>分发到机器</h2><div class="card row">${all.map(m => `<label class="row" style="gap:6px"><input type="checkbox" data-m="${m.id}" ${assigned.has(m.id) ? 'checked' : ''}> ${h(m.name)}</label>`).join('') || '<span class="muted">还没有机器</span>'}</div>

  <h2>文件 <span class="muted">(v${cur?.version ?? '-'}, ${files.files.length} 个, ${kb(k.current_size)})</span></h2>
  <div class="card filetree"><ul id="ft">${files.files.map((f, i) => `<li data-i="${i}" class="${i === 0 ? 'active' : ''}">${h(f.path)} <span class="muted">${kb(f.size)}</span></li>`).join('')}</ul>
   <pre id="fv" style="flex:1;margin:0;max-height:520px">${h(files.files[0]?.text ?? (files.files[0]?.binary ? '(binary)' : ''))}</pre></div>

  <h2>版本历史</h2><div class="card"><table><tr><th>版本</th><th>digest</th><th>大小</th><th>备注</th><th>来源</th><th>时间</th><th></th></tr>
  ${versions.map(v => `<tr><td>v${v.version}${v.id === k.current_version_id ? ' <span class="pill ok">当前</span>' : ''}</td><td class="mono small muted">${h(v.digest.slice(7, 19))}</td><td class="muted small">${kb(v.size)}</td><td class="small">${h(v.note)}</td><td class="muted small">${h(v.created_by)}</td><td class="muted small">${h(v.created_at)}</td>
   <td>${v.id !== k.current_version_id ? `<button class="ghost small" data-rb="${v.id}">设为当前</button>` : ''}</td></tr>`).join('')}</table></div>`;

  $('#ft').onclick = e => { const li = e.target.closest('li'); if (!li) return; $('#ft .active')?.classList.remove('active'); li.classList.add('active'); const f = files.files[+li.dataset.i]; $('#fv').textContent = f.binary ? '(binary)' : f.text; };
  $('#edit').onclick = () => editor(k, files.files);
  $('#editDesc').onclick = async () => { const d = prompt('描述', k.description); if (d !== null) { await api('PATCH', `/api/admin/skills/${name}`, { description: d }); route(); } };
  $('#del').onclick = async () => { if (confirm(`删除 skill ${name} 及全部版本？已分发机器下次 sync 会移除本地副本。`)) { await api('DELETE', `/api/admin/skills/${name}`); location.hash = '#/skills'; } };
  app.querySelectorAll('[data-m]').forEach(c => c.onchange = async () => { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, skill: name, assigned: c.checked }); toast(c.checked ? '已分发' : '已取消'); });
  app.querySelectorAll('[data-rb]').forEach(b => b.onclick = async () => { await api('POST', `/api/admin/skills/${name}/rollback`, { version_id: +b.dataset.rb }); toast('已切换当前版本'); route(); });
}
window.dl = async (href, fn) => { const r = await fetch(href, { headers: { Authorization: 'Bearer ' + token } }); const b = await r.blob(); const a = document.createElement('a'); a.href = URL.createObjectURL(b); a.download = fn; a.click(); };

function editor(k, existing) {
  const files = existing ? existing.filter(f => !f.binary).map(f => ({ path: f.path, text: f.text, exec: (f.mode & 0o111) !== 0 })) : [{ path: 'SKILL.md', text: `---\nname: my-skill\ndescription: 一句话说明这个 skill 什么时候该被用到\n---\n\n# my-skill\n\n在这里写指令。\n`, exec: false }];
  let cur = 0;
  const render = () => {
    app.innerHTML = `<a href="${k ? '#/skills/' + h(k.name) : '#/skills'}" class="muted small">← 取消</a>
    <div class="row" style="justify-content:space-between"><h1>${k ? '编辑 ' + h(k.name) : '新建 skill'}</h1>
     <div class="row">${k ? '' : '<input id="name" placeholder="skill-name" pattern="[a-z0-9][a-z0-9._-]*">'}<input id="note" placeholder="版本备注（可选）" style="width:220px"><button id="save">发布${k ? '新版本' : ''}</button></div></div>
    <div class="card filetree" style="align-items:stretch"><div><ul id="ft">${files.map((f, i) => `<li data-i="${i}" class="${i === cur ? 'active' : ''}">${h(f.path)}</li>`).join('')}</ul>
      <div class="row" style="margin-top:8px"><button class="ghost small" id="addf">+ 文件</button><button class="ghost small" id="renf">改名</button><button class="danger small" id="delf">删除</button></div>
      <label class="row small muted" style="margin-top:8px;gap:6px"><input type="checkbox" id="exec" ${files[cur]?.exec ? 'checked' : ''}> 可执行</label></div>
     <textarea id="ta" style="flex:1;min-height:560px">${h(files[cur]?.text ?? '')}</textarea></div>`;
    const ta = $('#ta'); ta.oninput = () => files[cur].text = ta.value;
    $('#exec').onchange = e => files[cur].exec = e.target.checked;
    $('#ft').onclick = e => { const li = e.target.closest('li'); if (li) { cur = +li.dataset.i; render(); } };
    $('#addf').onclick = () => { const p = prompt('文件路径，如 scripts/run.sh'); if (p) { files.push({ path: p, text: '', exec: false }); cur = files.length - 1; render(); } };
    $('#renf').onclick = () => { const p = prompt('新路径', files[cur].path); if (p) { files[cur].path = p; render(); } };
    $('#delf').onclick = () => { if (files.length > 1 && confirm('删除 ' + files[cur].path + '？')) { files.splice(cur, 1); cur = 0; render(); } };
    $('#save').onclick = async () => {
      const name = k ? k.name : $('#name').value.trim();
      if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) return fail(new Error('名称只能是小写字母、数字、. _ -'));
      try { const r = await api('PUT', `/api/admin/skills/${name}/files`, { files, note: $('#note').value }); toast(r.created ? `已发布 v${r.version.version}` : '内容没有变化，未产生新版本'); location.hash = '#/skills/' + name; if (k) route(); } catch (e) { fail(e); }
    };
  };
  render();
}

// ---------- configs (collected, read-only) ----------
let configsCache = null;
async function loadConfigs() { configsCache = await api('GET', '/api/admin/configs'); return configsCache; }
async function configs(sel) {
  const all = await loadConfigs();
  const tools = [...new Set(all.flatMap(m => (m.snapshot?.configs || []).map(c => c.tool)))].sort();
  const machines = all.filter(m => m.snapshot);
  // selection: tool[:machine[:path]]
  let [tool, mid, pathIdx] = (sel || '').split(':');
  tool = tool || tools[0];
  const rows = machines.map(m => ({ m, files: (m.snapshot.configs || []).filter(c => c.tool === tool) })).filter(x => x.files.length);
  if (!mid && rows[0]) mid = rows[0].m.machine_id;
  const cur = rows.find(x => x.m.machine_id === mid);
  const file = cur ? cur.files[+pathIdx || 0] : null;

  app.innerHTML = `<h1>配置全貌 <span class="muted small">每台机器 sync 时采集，凭证已在机器端打码</span></h1>
  <div class="tabs">${tools.map(t => `<button class="${t === tool ? 'active' : ''}" data-tool="${h(t)}">${h(t)} <span class="muted">${machines.filter(m => (m.snapshot.configs || []).some(c => c.tool === t)).length}</span></button>`).join('')}</div>
  <div class="row" style="align-items:flex-start;gap:16px">
    <div style="min-width:260px">
      ${rows.map(x => `<div class="card" style="padding:10px 12px;margin-bottom:8px;${x.m.machine_id === mid ? 'border-color:var(--accent)' : ''}">
        <div style="font-weight:600"><span class="dot ${online(x.m.snapshot_at)}"></span>${h(x.m.machine_name)}</div>
        ${x.files.map((f, i) => `<div class="small mono" style="padding:3px 0;cursor:pointer;${x.m.machine_id === mid && i === (+pathIdx || 0) ? 'color:var(--accent)' : 'color:var(--muted)'}" data-sel="${h(tool)}:${x.m.machine_id}:${i}">${h(f.path)} <span class="muted">${kb(f.size)}</span></div>`).join('')}
      </div>`).join('') || '<p class="muted">还没有机器上报过这个工具的配置</p>'}
    </div>
    <div class="card" style="flex:1;min-width:0">
      ${file ? `<div class="row" style="justify-content:space-between;margin-bottom:8px"><span class="mono">${h(cur.m.machine_name)} · ${h(file.path)}</span><span class="muted small">${h(file.format)} · 修改于 ${h(file.mod_time)} · 采集 ${ago(cur.m.snapshot_at)}</span></div>
        <pre style="max-height:70vh;margin:0">${h(file.truncated ? '(文件过大，未采集)' : file.content)}</pre>` : '<p class="muted">选择左侧一个文件</p>'}
    </div>
  </div>
  <p class="muted small" style="margin-top:12px">同一工具在不同机器上的差异一眼可见：切左侧机器即可。<code>&lt;redacted:xxxx&gt;</code> 里的 8 位是值的指纹，两台机器指纹相同 = 用的同一个 key。</p>`;
  app.querySelectorAll('[data-tool]').forEach(b => b.onclick = () => { location.hash = '#/configs/' + b.dataset.tool; });
  app.querySelectorAll('[data-sel]').forEach(d => d.onclick = () => { location.hash = '#/configs/' + d.dataset.sel; });
}

// ---------- env (collected exports across machines) ----------
async function envView() {
  const all = await loadConfigs();
  const machines = all.filter(m => m.snapshot);
  const byName = {};
  machines.forEach(m => (m.snapshot.exports || []).forEach(e => {
    if (e.kind === 'append') return; // PATH-style appends are noise here
    (byName[e.name] ||= {})[m.machine_id] = e;
  }));
  const names = Object.keys(byName).sort((a, b) => {
    const ka = byName[a], kb_ = byName[b];
    const sa = Object.values(ka).some(e => e.kind === 'secret'), sb = Object.values(kb_).some(e => e.kind === 'secret');
    if (sa !== sb) return sa ? -1 : 1;
    return a.localeCompare(b);
  });
  const cell = (e) => {
    if (!e) return '<span class="muted">—</span>';
    if (e.kind === 'secret') return `<span class="pill" title="指纹 ${h(e.fingerprint)} · ${h(e.file)}:${e.line}">🔒 ${h(e.fingerprint)}</span>`;
    return `<span class="mono small" title="${h(e.file)}:${e.line}">${h(e.value.length > 40 ? e.value.slice(0, 40) + '…' : e.value)}</span>`;
  };
  const consistent = (n) => { const vals = new Set(Object.values(byName[n]).map(e => e.kind === 'secret' ? e.fingerprint : e.value)); return vals.size === 1 && Object.keys(byName[n]).length === machines.length; };
  app.innerHTML = `<h1>环境变量 <span class="muted small">来自各机器 shell rc 里的 export（PATH 追加已过滤）</span></h1>
  <div class="card" style="overflow:auto"><table><tr><th>变量</th>${machines.map(m => `<th>${h(m.machine_name)}</th>`).join('')}<th></th></tr>
  ${names.map(n => `<tr><td class="mono">${h(n)}</td>${machines.map(m => `<td>${cell(byName[n][m.machine_id])}</td>`).join('')}
    <td>${consistent(n) ? '<span class="pill ok">一致</span>' : Object.keys(byName[n]).length === 1 ? '<span class="pill">仅一台</span>' : '<span class="pill warn">不一致</span>'}</td></tr>`).join('') || '<tr><td class="muted">尚无数据，等机器 sync</td></tr>'}</table></div>
  <p class="muted small">🔒 后面的 8 位是值的指纹：同一行两台机器指纹相同，说明它们用的是同一个 key。下一步这里会变成可维护的总表，支持把变量导入到任意机器。</p>`;
}

// ---------- matrix ----------
async function matrix() {
  const ov = await api('GET', '/api/admin/overview');
  const ms = ov.machines, ks = ov.skills, as = ov.assignments;
  app.innerHTML = `<h1>分发矩阵</h1><p class="muted small">勾选即分发；机器下次 sync 时安装/移除。</p>
  <div class="card" style="overflow:auto"><table class="matrix"><tr><th>skill \\ 机器</th>${ms.map(m => `<th>${h(m.name)}</th>`).join('')}</tr>
  ${ks.map(k => `<tr><td><a href="#/skills/${h(k.name)}" class="mono">${h(k.name)}</a> <span class="muted small">v${k.current_version}</span></td>
    ${ms.map(m => `<td class="c"><input type="checkbox" data-m="${m.id}" data-k="${h(k.name)}" ${(as[m.id] || []).includes(k.name) ? 'checked' : ''}></td>`).join('')}</tr>`).join('') || `<tr><td class="muted" colspan="${ms.length + 1}">还没有 skill</td></tr>`}</table></div>`;
  app.querySelectorAll('input[type=checkbox]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, skill: c.dataset.k, assigned: c.checked }); } catch (e) { c.checked = !c.checked; fail(e); } });
}

route();
