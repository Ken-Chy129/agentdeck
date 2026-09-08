import { $, $$, app, api, h, ago, online, kb, semverLt, overview, npmLatest, syncState, pill, upgradeCmd, toast, fail, modal, closeModal, ask, fmtTime, trunc, invalidate, pageHeader, crumb, empty, emptyRow, statusPill, runRemote, awaitJob, jobPending } from '../core.js';

const AGENT_TOOLS = ['claude', 'codex', 'gemini', 'hermes', 'opencode', 'cursor-agent', 'gh', 'lark-cli', 'bytedcli'];

export async function machinesView() {
  const ov = await overview();
  const ms = ov.machines;
  const latest = await npmLatest(ms.flatMap(m => (m.inventory?.tools || []).map(t => t.package)));

  app.innerHTML = pageHeader({ title: '机器', desc: '每台机器装了 agentdeck CLI，按计划任务每 15 分钟同步一次；这里看到的是它们最近一次上报的状态。', actions: '<button id="enroll">+ 添加机器</button>' }) + `
    <div id="enrollBox"></div>
    <div class="grid">${ms.map(m => machineCard(ov, m, latest)).join('') || empty('还没有机器。点「添加机器」生成一次性注册 token。')}</div>`;
  // Upgrade straight from the list: no need to open each machine.
  $$('[data-upall]').forEach(b => b.onclick = (e) => {
    e.preventDefault(); e.stopPropagation();
    const m = ms.find(x => x.id === b.dataset.upall);
    const cmds = (m.inventory?.tools || []).filter(t => { const lv = t.package ? latest[t.package] : ''; return lv && semverLt(t.version, lv) && upgradeCmd(t); });
    execModal(m.id, cmds.map(t => upgradeCmd(t)).join(' && '), `升级 ${m.name}：${cmds.map(t => t.name).join(' / ')}`);
  });
  $('#enroll').onclick = async () => {
    const r = await api('POST', '/api/admin/enroll-tokens', { note: 'from console' });
    $('#enrollBox').innerHTML = `<div class="card"><div class="title" style="font-size:14px">在新机器上执行 <span class="muted small">（token 一次有效）</span></div>
      <pre>agentdeck login ${location.origin} ${r.enroll_token} --name $(hostname -s)
agentdeck sync</pre>
      <details><summary>还没装 agentdeck CLI？</summary><pre>go install github.com/Ken-Chy129/agentdeck/cmd/agentdeck@latest</pre>
      <p class="help">仓库是公开的，不需要凭证；需要 Go ≥ 1.25（低版本会自动下载工具链）。</p></details></div>`;
  };
}

function machineCard(ov, m, latest) {
  const inv = m.inventory || {};
  const tools = (inv.tools || []).filter(t => AGENT_TOOLS.includes(t.name));
  const behind = tools.filter(t => t.package && latest[t.package] && semverLt(t.version, latest[t.package]));
  const counts = { skill: 0, config: 0, env: 0 }; let warn = 0;
  for (const r of ov.resources) { const st = syncState(ov, m, r); if (st) { counts[r.kind]++; if (st.cls !== 'ok') warn++; } }
  return `<div class="card">
    <div class="row between nowrap">
      <a href="#/machines/${m.id}" class="title link-plain"><span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)}</a>
      <span class="faint xs mono">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</span></div>
    <div class="row mt8 mb12" style="gap:6px">
      <span class="chip">同步 <b>${ago(m.last_seen_at)}</b></span>
      <span class="chip">${counts.skill} skill</span><span class="chip">${counts.config} 配置</span><span class="chip">${counts.env} 变量</span>
      ${warn ? `<span class="chip ov">${warn} 待同步</span>` : ''}${behind.length ? `<span class="chip ov">${behind.length} CLI 可升级</span><button class="small" data-upall="${m.id}">升级</button>` : ''}
    </div>
    <table>${tools.map(t => {
      const lv = t.package ? latest[t.package] : ''; const old = lv && semverLt(t.version, lv);
      return `<tr><td class="mono" style="padding-left:0">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}${old ? ` <span class="faint">→ ${h(lv)}</span>` : ''}</td><td class="faint xs right" style="padding-right:0">${h(t.source)}</td></tr>`;
    }).join('') || `<tr><td class="muted small" style="padding-left:0">尚无 inventory，等第一次 sync</td></tr>`}</table>
  </div>`;
}

const TABS = [['cli', 'CLI'], ['shell', '终端'], ['skills', 'Skill'], ['configs', '配置'], ['env', '环境变量'], ['jobs', '任务'], ['logs', '同步日志']];

export async function machineDetail(id, tab) {
  tab = TABS.some(t => t[0] === tab) ? tab : 'cli';
  const [m, ov, logs, jobs] = await Promise.all([
    api('GET', `/api/admin/machines/${id}`), overview(), api('GET', `/api/admin/machines/${id}/logs`), api('GET', `/api/admin/machines/${id}/jobs`)]);
  const inv = m.inventory || {};

  app.innerHTML = crumb('#/machines', '机器') + pageHeader({
    title: `<span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)}`, sub: `<span class="mono">${h(m.id)}</span>`,
    actions: '<button class="ghost small" id="rename">重命名</button><button class="danger small" id="del">删除机器</button>' }) + `
  <div class="meta-line"><span>主机 <b class="mono">${h(inv.hostname || m.hostname)}</b></span><span>系统 <b>${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</b></span><span>最近同步 <b>${ago(m.last_seen_at)}</b></span><span>配置采集 <b>${ago(m.snapshot_at)}</b></span>${(inv.runtimes || []).slice(0, 6).map(r => `<span>${h(r.name)} <b>${h(r.version)}</b></span>`).join('')}</div>
  <div class="tabs">${TABS.map(([k, l]) => `<button class="${k === tab ? 'active' : ''}" data-tab="${k}">${l}${badge(k, ov, m, jobs)}</button>`).join('')}</div>
  <div id="tabBody"></div>`;

  $$('[data-tab]').forEach(b => b.onclick = () => { location.hash = `#/machines/${id}/${b.dataset.tab}`; });
  $('#rename').onclick = async () => { const n = prompt('新名称', m.name); if (n && n !== m.name) { await api('PATCH', `/api/admin/machines/${id}`, { name: n }); invalidate(); reroute(); } };
  $('#del').onclick = async () => { if (await ask('删除机器', `删除 <b>${h(m.name)}</b>？该机器的 token 立刻失效，分发记录一并删除。`, '删除', true)) { await api('DELETE', `/api/admin/machines/${id}`); location.hash = '#/machines'; } };

  const body = $('#tabBody');
  const render = { cli: tabCli, shell: tabShell, skills: tabSkills, configs: tabConfigs, env: tabEnv, jobs: tabJobs, logs: tabLogs }[tab];
  await render(body, { m, ov, logs, jobs, id });
}

function badge(k, ov, m, jobs) {
  let n = 0;
  if (k === 'jobs') n = jobs.filter(j => jobPending(j.status)).length;
  else if (['skills', 'configs', 'env'].includes(k)) { const kind = { skills: 'skill', configs: 'config', env: 'env' }[k]; for (const r of ov.resources) if (r.kind === kind) { const st = syncState(ov, m, r); if (st && st.cls !== 'ok') n++; } }
  return n ? `<span class="pill warn count">${n}</span>` : '';
}

// ---- CLI ----
async function tabCli(body, { m, id }) {
  const inv = m.inventory || {};
  const latest = await npmLatest((inv.tools || []).map(t => t.package));
  const behind = (inv.tools || []).filter(t => { const lv = t.package ? latest[t.package] : ''; return lv && semverLt(t.version, lv) && upgradeCmd(t); });
  body.innerHTML = `<div class="card flush"><div class="row between" style="padding:12px 16px 0">
  <div class="muted small">${behind.length ? `${behind.length} 个可升级` : '都是最新'}</div>
  ${behind.length ? `<button class="small" id="upAll">升级全部（${behind.length}）</button>` : ''}</div>
  <table><thead><tr><th>工具</th><th>版本</th><th>最新</th><th>来源</th><th>路径</th><th></th></tr></thead><tbody>
  ${(inv.tools || []).map(t => { const lv = t.package ? latest[t.package] : ''; const old = lv && semverLt(t.version, lv); const cmd = upgradeCmd(t);
    return `<tr><td class="mono">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}</td><td class="mono muted">${h(lv || '')}</td><td class="muted small">${h(t.source)}${t.package ? ` <span class="faint">· ${h(t.package)}</span>` : ''}</td><td class="mono xs faint">${h(t.path || '')}</td>
    <td class="right nowrap">${cmd ? `<button class="ghost small" onclick="copyText(${JSON.stringify(cmd)})">复制</button>
    <button class="small" data-up="${h(cmd)}" data-tool="${h(t.name)}">${old ? '升级' : '重装'}</button>` : ''}</td></tr>`; }).join('') || emptyRow(6, '尚无 inventory，等第一次 sync')}</tbody></table>
  <p class="help">升级命令由机器自己算出来（认得 npm prefix / nvm / native 安装器 / codex standalone），点「升级」直接在那台机器上跑。</p></div>
  <h2>运行时</h2><div class="card flush"><table><tbody>${(inv.runtimes || []).map(r => `<tr><td class="mono">${h(r.name)}</td><td class="mono">${h(r.version)}</td><td class="mono xs faint">${h(r.path)}</td></tr>`).join('') || emptyRow(3, '无')}</tbody></table>
  <div class="help"><details><summary>npm -g 全部 ${(inv.npm_global || []).length} 个 · brew ${(inv.brew || []).length} 个</summary>
  <div class="cols mt8"><table><tbody>${(inv.npm_global || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted right">${h(p.version)}</td></tr>`).join('')}</tbody></table>
  <table><tbody>${(inv.brew || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted right">${h(p.version)}</td></tr>`).join('')}</tbody></table></div></details></div></div>`;
  $$('[data-up]', body).forEach(b => b.onclick = () => execModal(id, b.dataset.up, `升级 ${b.dataset.tool}`));
  if ($('#upAll', body)) $('#upAll', body).onclick = () => {
    const cmds = behind.map(t => upgradeCmd(t));
    execModal(id, cmds.join(' && '), `升级 ${behind.map(t => t.name).join(' / ')}`);
  };
}

// ---- remote exec modal ----
// Runs a command on the machine and streams the result into a modal. Used by
// the upgrade buttons so you never have to SSH in for a version bump.
export async function execModal(id, cmd, title) {
  modal(`<h3>${h(title || '在机器上执行')}</h3>
    <pre class="mono" style="white-space:pre-wrap">$ ${h(cmd)}</pre>
    <div id="execOut" class="term muted small">等机器接单…（在线的话几秒内开始）</div>
    <div class="row right mt12"><button class="ghost" onclick="closeModal()">关闭</button></div>`);
  const out = $('#execOut');
  try {
    let j = await runRemote(id, cmd, { waitSec: 60 });
    if (jobPending(j.status)) {
      out.textContent = j.status === 'running' ? '机器已接单，执行中…' : '命令已排队，但机器还没接单。如果它没在 watch 模式，就要等下一次 sync。';
      j = await awaitJob(j.id) || j;
    }
    if (jobPending(j.status)) return;
    out.classList.remove('muted', 'small');
    out.textContent = j.result || '(无输出)';
    if (j.status === 'done') { toast('执行成功'); invalidate(); } else { toast('执行失败，看输出'); }
  } catch (e) {
    out.textContent = String(e.message || e);
  }
}

// ---- terminal ----
async function tabShell(body, { m, id }) {
  const off = online(m.last_seen_at) !== 'on';
  body.innerHTML = `<div class="card">
    <div class="row between mb8"><div class="title" style="font-size:14px">在 ${h(m.name)} 上执行命令</div>
    <span class="chip">login shell · $HOME</span></div>
    <textarea id="cmd" class="mono" rows="3" placeholder="例如：claude update"></textarea>
    <div class="row mt8" style="gap:8px">
      <button id="run">执行 <span class="faint">⌘↵</span></button>
      <select id="tmo" class="small"><option value="120">2 分钟</option><option value="600" selected>10 分钟</option><option value="1800">30 分钟</option></select>
      <span class="help" style="margin:0">命令跑在登录 shell 里，跟你自己 SSH 进去一样能找到 nvm / brew。</span>
    </div>
    <div class="row mt8" style="gap:6px;flex-wrap:wrap">${SNIPPETS.map(s => `<button class="ghost small" data-snip="${h(s[1])}">${h(s[0])}</button>`).join('')}</div>
    <pre id="out" class="term mt12">${off ? '这台机器最近没上报，命令会排队等它上来。' : '就绪。'}</pre>
  </div>`;
  const runIt = async () => {
    const cmd = $('#cmd', body).value.trim();
    if (!cmd) return;
    const out = $('#out', body);
    out.textContent = `$ ${cmd}\n执行中…`;
    $('#run', body).disabled = true;
    try {
      let j = await runRemote(id, cmd, { timeoutSec: +$('#tmo', body).value, waitSec: 60 });
      if (jobPending(j.status)) { out.textContent = `$ ${cmd}\n${j.status === 'running' ? '执行中…' : '已排队，等机器接单…'}`; j = await awaitJob(j.id) || j; }
      out.textContent = jobPending(j.status) ? `$ ${cmd}\n机器一直没接单：确认它在线，或者装 watch 模式。` : (j.result || '(无输出)');
      if (j.status === 'done') invalidate();
    } catch (e) { out.textContent = String(e.message || e); }
    $('#run', body).disabled = false;
  };
  $('#run', body).onclick = runIt;
  $('#cmd', body).onkeydown = (e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') runIt(); };
  $$('[data-snip]', body).forEach(b => b.onclick = () => { $('#cmd', body).value = b.dataset.snip; $('#cmd', body).focus(); });
}

const SNIPPETS = [
  ['claude 版本', 'claude --version'],
  ['codex 版本', 'codex --version'],
  ['npm -g 列表', 'npm ls -g --depth=0'],
  ['磁盘', 'df -h | head -5'],
  ['agentdeck 状态', 'agentdeck status'],
  ['立即 sync', 'agentdeck sync'],
];

// ---- shared: assigned resources table ----
function assignedRows(ov, m, kind, linkOf, extra = () => '') {
  const rows = ov.resources.filter(r => r.kind === kind && ov.assignedOn[m.id]?.[r.id]);
  return rows.map(r => { const st = syncState(ov, m, r);
    return `<tr><td>${linkOf(r)}</td><td class="small muted">v${r.current_version}${st.a.has_override ? ' <span class="chip ov" title="这台机器有独立取值">override</span>' : ''}</td><td>${pill(st)}${st.ap?.updated_at ? ` <span class="faint xs">${ago(st.ap.updated_at)}</span>` : ''}</td>${extra(r, st)}<td class="right"><button class="ghost small" data-unassign="${r.id}">取消分发</button></td></tr>`; }).join('');
}
function bindUnassign(body, id) {
  $$('[data-unassign]', body).forEach(b => b.onclick = async () => { await api('PUT', '/api/admin/assignments', { machine_id: id, resource_id: +b.dataset.unassign, assigned: false }); toast('已取消，机器下次 sync 移除'); invalidate(); reroute(); });
}
function assignSelect(ov, m, kind, label) {
  const opts = ov.resources.filter(r => r.kind === kind && !ov.assignedOn[m.id]?.[r.id]);
  return `<select id="assignSel"><option value="">${label}</option>${opts.map(r => `<option value="${r.id}">${h(r.name)}</option>`).join('')}</select>`;
}
function bindAssign(body, id) {
  const sel = $('#assignSel', body); if (!sel) return;
  sel.onchange = async () => { if (!sel.value) return; await api('PUT', '/api/admin/assignments', { machine_id: id, resource_id: +sel.value, assigned: true }); toast('已分发，机器下次 sync 生效'); invalidate(); reroute(); };
}

// ---- Skill ----
async function tabSkills(body, { m, ov, id }) {
  const local = m.local_skills || [];
  const assignedNames = new Set(ov.resources.filter(r => r.kind === 'skill' && ov.assignedOn[id]?.[r.id]).map(r => r.name));
  const other = local.filter(l => !assignedNames.has(l.name));
  body.innerHTML = `<div class="card flush">
   <div class="toolbar row between">${assignSelect(ov, m, 'skill', '分发一个 skill 到这台机器…')}<span class="muted small">${assignedNames.size} 个已托管 · 本机共 ${local.length} 个</span></div>
   <table><thead><tr><th>名称</th><th>版本</th><th>状态</th><th></th></tr></thead><tbody>${assignedRows(ov, m, 'skill', r => `<a href="#/skills/${encodeURIComponent(r.name)}" class="mono">${h(r.name)}</a>`) || emptyRow(4, '没有分发任何 skill')}</tbody></table>
   ${other.length ? `<div class="help"><details><summary>本机其他 skill（未托管）${other.length} 个</summary>
     <table class="mt8"><tbody>${other.map(l => `<tr><td class="mono small">${h(l.name)}</td><td class="muted small right">${kb(l.size)}</td><td class="muted small">${l.managed ? '曾托管，将在下次 sync 移除' : ''}</td></tr>`).join('')}</tbody></table>
     <p class="help">想把某个本机 skill 收进库：在这台机器上跑 <code>agentdeck push ~/.agents/skills/&lt;name&gt;</code>。</p></details></div>` : ''}
  </div>`;
  bindAssign(body, id); bindUnassign(body, id);
}

// ---- 配置 ----
async function tabConfigs(body, { m, ov, id }) {
  const snap = m.snapshot || {}; const files = snap.configs || [];
  body.innerHTML = `<h2>托管的配置 Profile</h2><div class="card flush">
   <div class="toolbar row">${assignSelect(ov, m, 'config', '把一个配置 Profile 应用到这台机器…')}</div>
   <table><thead><tr><th>Profile</th><th>版本</th><th>状态</th><th>目标文件</th><th></th></tr></thead><tbody>${assignedRows(ov, m, 'config', r => `<a href="#/configs/${r.id}" class="mono">${h(r.name)}</a> <span class="faint xs">${h(r.meta?.tool || '')}</span>`, r => `<td class="mono xs faint">${h(r.meta?.path || '')}</td>`) || emptyRow(5, '没有应用任何配置 Profile')}</tbody></table>
   <p class="help">Profile 里的 key 会合并进目标文件（JSON），工具自己写的其他 key 不动；原文件先备份为 <code>*.agentdeck-bak</code>。</p></div>
  <h2>本机采集到的配置文件 <span class="hint">只读 · 凭证已在机器端打码</span></h2>
  <div class="cols"><div class="narrow card tight filelist" id="cfgList">${files.map((f, i) => `<div class="item ${i === 0 ? 'active' : ''}" data-i="${i}"><span class="tool">${h(f.tool)}</span>${h(f.path)}<div class="sub">${h(f.format)} · ${kb(f.size)} · 改于 ${ago(f.mod_time)}</div></div>`).join('') || empty('没有采集到配置文件')}</div>
   <div class="card"><div class="row between mb8"><span class="mono small" id="cfgTitle">${h(files[0]?.path || '')}</span><button class="ghost small" id="mkProfile" ${files.length ? '' : 'disabled'}>以此为模板新建 Profile</button></div><pre class="fill" id="cfgView">${h(files[0] ? (files[0].truncated ? '(文件过大，未采集)' : files[0].content) : '')}</pre></div></div>`;
  bindAssign(body, id); bindUnassign(body, id);
  let cur = 0;
  $$('[data-i]', body).forEach(d => d.onclick = () => { cur = +d.dataset.i; $$('[data-i]', body).forEach(x => x.classList.remove('active')); d.classList.add('active'); const f = files[cur]; $('#cfgTitle', body).textContent = f.path; $('#cfgView', body).textContent = f.truncated ? '(文件过大，未采集)' : f.content; });
  $('#mkProfile', body).onclick = () => { const f = files[cur]; sessionStorage.setItem('agentdeck_cfg_template', JSON.stringify({ tool: f.tool, path: f.path, format: f.format, content: f.content, from: m.name })); location.hash = '#/configs/_/new'; };
}

// ---- 环境变量 ----
async function tabEnv(body, { m, ov, id }) {
  const snap = m.snapshot || {}; const exports = (snap.exports || []).filter(e => e.kind !== 'append');
  const managedNames = new Set(ov.resources.filter(r => r.kind === 'env').map(r => r.name));
  const assignedNames = new Set(ov.resources.filter(r => r.kind === 'env' && ov.assignedOn[id]?.[r.id]).map(r => r.name));
  const unmanaged = exports.filter(e => !assignedNames.has(e.name) && !e.file.includes('agentdeck'));
  body.innerHTML = `<h2>已分发到本机的变量 <span class="hint">写入 <code>~/.config/agentdeck/env.sh</code>，由 rc 文件 source</span></h2><div class="card flush">
   <div class="toolbar row">${assignSelect(ov, m, 'env', '把总表里的一个变量分发到这台机器…')}</div>
   <table><thead><tr><th>变量</th><th>版本</th><th>状态</th><th></th></tr></thead><tbody>${assignedRows(ov, m, 'env', r => `<a href="#/env/${r.id}" class="mono">${h(r.name)}</a>${r.meta?.secret ? ' <span class="faint">🔒</span>' : ''}`) || emptyRow(4, '没有分发任何变量')}</tbody></table></div>
  <h2>本机 rc 文件里的 export <span class="hint">采集自 .zshrc / .bashrc 等 · 秘密值只显示指纹</span></h2><div class="card flush">
   <div class="toolbar row between"><span class="muted small">勾选后「导入到总表」：机器下次 sync 读取真实值加密回传，并自动分发给这台机器。总表里已有同名变量的，会成为它的新版本。</span><button class="small" id="importSel" disabled>导入到总表</button></div>
   <table><thead><tr><th style="width:20px"></th><th>变量</th><th>值 / 指纹</th><th>来源</th><th>总表</th></tr></thead><tbody>
   ${unmanaged.map(e => `<tr><td><input type="checkbox" data-imp="${h(e.name)}"></td><td class="mono">${h(e.name)}</td><td class="mono small">${e.kind === 'secret' ? `<span class="pill">🔒 ${h(e.fingerprint)}</span>` : h(trunc(e.value, 60))}</td><td class="faint xs mono">${h(e.file)}:${e.line}</td><td class="small">${managedNames.has(e.name) ? '<span class="pill acc">已有同名</span>' : '<span class="faint">—</span>'}</td></tr>`).join('') || emptyRow(5, 'rc 文件里没有额外的 export')}</tbody></table></div>`;
  bindAssign(body, id); bindUnassign(body, id);
  const btn = $('#importSel', body);
  const upd = () => { btn.disabled = !$$('[data-imp]:checked', body).length; };
  $$('[data-imp]', body).forEach(c => c.onchange = upd);
  btn.onclick = async () => {
    const names = [...new Set($$('[data-imp]:checked', body).map(c => c.dataset.imp))];
    await api('POST', `/api/admin/machines/${id}/import-env`, { names });
    toast(`已登记 ${names.length} 个变量，机器下次 sync 回传（15 分钟内；也可在机器上手动跑 agentdeck sync）`);
  };
}

// ---- 任务 ----
async function tabJobs(body, { jobs, id }) {
  body.innerHTML = `<div class="card flush"><table><thead><tr><th>#</th><th>类型</th><th>参数</th><th>状态</th><th>创建</th><th>结果</th><th></th></tr></thead><tbody>
  ${jobs.map(j => `<tr><td class="mono muted">${j.id}</td><td class="mono">${h(j.type)}</td><td class="mono xs faint">${h(JSON.stringify(j.payload))}</td><td>${statusPill(j.status)}</td><td class="muted small">${ago(j.created_at)}</td>
   <td>${j.result ? `<details><summary class="small">输出</summary><pre>${h(j.result)}</pre></details>` : ''}</td><td class="right">${j.status === 'queued' ? `<button class="ghost small" data-cancel="${j.id}">取消</button>` : ''}</td></tr>`).join('') || emptyRow(7, '没有任务。在 CLI 页可以下发升级任务。')}</tbody></table></div>`;
  $$('[data-cancel]', body).forEach(b => b.onclick = async () => { await api('DELETE', `/api/admin/jobs/${b.dataset.cancel}`); reroute(); });
}

// ---- 日志 ----
async function tabLogs(body, { logs }) {
  body.innerHTML = `<div class="card">${logs.map(l => { const s = l.summary || {}; const changed = (s.resources || s.skills || []).filter(x => x.action !== 'unchanged');
    return `<details class="log-item"><summary><span class="mono small" style="color:var(--fg-1)">${fmtTime(l.at)}</span><span class="faint">·</span> ${changed.length ? changed.map(x => `<span class="chip ${x.action === 'failed' ? 'ov' : ''}">${h(x.kind ? x.kind + '/' : '')}${h(x.name)} <b>${h(x.action)}</b></span>`).join(' ') : '<span class="faint">无变化</span>'} <span class="faint">· ${(s.jobs || []).length} 任务 · ${h(s.duration || '')}</span>${s.error ? ` <span class="bad-text">${h(s.error)}</span>` : ''}</summary><pre>${h(JSON.stringify(s, null, 2))}</pre></details>`; }).join('') || empty('无')}</div>`;
}
