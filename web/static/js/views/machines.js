import { $, $$, app, api, h, ago, online, kb, semverLt, overview, npmLatest, syncState, pill, upgradeCmd, toast, fail, modal, closeModal, ask, fmtTime, shortDigest, trunc, invalidate } from '../core.js';

const AGENT_TOOLS = ['claude', 'codex', 'gemini', 'hermes', 'opencode', 'cursor-agent', 'gh', 'lark-cli', 'bytedcli'];

export async function machinesView() {
  const ov = await overview();
  const ms = ov.machines;
  const latest = await npmLatest(ms.flatMap(m => (m.inventory?.tools || []).map(t => t.package)));

  app.innerHTML = `<div class="row between"><h1>机器</h1><button id="enroll" class="ghost">+ 添加机器</button></div>
    <div id="enrollBox"></div>
    <div class="grid">${ms.map(m => machineCard(ov, m, latest)).join('') || '<p class="muted">还没有机器。点「添加机器」生成一次性注册 token。</p>'}</div>`;
  $('#enroll').onclick = async () => {
    const r = await api('POST', '/api/admin/enroll-tokens', { note: 'from console' });
    $('#enrollBox').innerHTML = `<div class="card"><b>在新机器上执行</b>（token 一次有效）：
      <pre>agentdeck login ${location.origin} ${r.enroll_token} --name $(hostname -s)
agentdeck sync</pre>
      <details><summary>还没装 agentdeck CLI？</summary><pre>go install github.com/Ken-Chy129/agentdeck/cmd/agentdeck@latest</pre>
      <p class="muted small">仓库是公开的，不需要凭证；需要 Go ≥ 1.25（低版本会自动下载工具链）。</p></details></div>`;
  };
}

function machineCard(ov, m, latest) {
  const inv = m.inventory || {};
  const tools = (inv.tools || []).filter(t => AGENT_TOOLS.includes(t.name));
  const behind = tools.filter(t => t.package && latest[t.package] && semverLt(t.version, latest[t.package]));
  const counts = { skill: 0, config: 0, env: 0 }; let warn = 0;
  for (const r of ov.resources) { const st = syncState(ov, m, r); if (st) { counts[r.kind]++; if (st.cls !== 'ok') warn++; } }
  return `<div class="card">
    <div class="row between">
      <a href="#/machines/${m.id}" style="font-size:16px;font-weight:600"><span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)}</a>
      <span class="muted small">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</span></div>
    <div class="muted small" style="margin:4px 0 10px">最近同步 ${ago(m.last_seen_at)} · ${counts.skill} skill · ${counts.config} 配置 · ${counts.env} 变量${warn ? ` · <span class="behind">${warn} 项待同步</span>` : ''}${behind.length ? ` · <span class="behind">${behind.length} 个 CLI 可升级</span>` : ''}</div>
    <table>${tools.map(t => {
      const lv = t.package ? latest[t.package] : ''; const old = lv && semverLt(t.version, lv);
      return `<tr><td class="mono">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}${old ? ` → ${h(lv)}` : ''}</td><td class="muted small">${h(t.source)}</td></tr>`;
    }).join('') || '<tr><td class="muted">尚无 inventory，等第一次 sync</td></tr>'}</table>
  </div>`;
}

const TABS = [['cli', 'CLI'], ['skills', 'Skill'], ['configs', '配置'], ['env', '环境变量'], ['jobs', '任务'], ['logs', '同步日志']];

export async function machineDetail(id, tab) {
  tab = TABS.some(t => t[0] === tab) ? tab : 'cli';
  const [m, ov, logs, jobs] = await Promise.all([
    api('GET', `/api/admin/machines/${id}`), overview(), api('GET', `/api/admin/machines/${id}/logs`), api('GET', `/api/admin/machines/${id}/jobs`)]);
  const inv = m.inventory || {};

  app.innerHTML = `<a href="#/machines" class="muted small">← 机器</a>
  <div class="row between"><h1><span class="dot ${online(m.last_seen_at)}"></span>${h(m.name)} <span class="muted small mono">${h(m.id)}</span></h1>
   <div class="row"><button class="ghost small" id="rename">重命名</button><button class="danger small" id="del">删除机器</button></div></div>
  <div class="row muted small" style="margin:-8px 0 14px;gap:18px"><span>主机 <code>${h(inv.hostname || m.hostname)}</code></span><span>${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</span><span>最近同步 ${ago(m.last_seen_at)}</span><span>配置采集 ${ago(m.snapshot_at)}</span>${(inv.runtimes || []).map(r => `<span>${h(r.name)} ${h(r.version)}</span>`).join('')}</div>
  <div class="tabs">${TABS.map(([k, l]) => `<button class="${k === tab ? 'active' : ''}" data-tab="${k}">${l}${badge(k, ov, m, jobs)}</button>`).join('')}</div>
  <div id="tabBody"></div>`;

  $$('[data-tab]').forEach(b => b.onclick = () => { location.hash = `#/machines/${id}/${b.dataset.tab}`; });
  $('#rename').onclick = async () => { const n = prompt('新名称', m.name); if (n && n !== m.name) { await api('PATCH', `/api/admin/machines/${id}`, { name: n }); invalidate(); reroute(); } };
  $('#del').onclick = async () => { if (await ask('删除机器', `删除 <b>${h(m.name)}</b>？该机器的 token 立刻失效，分发记录一并删除。`, '删除', true)) { await api('DELETE', `/api/admin/machines/${id}`); location.hash = '#/machines'; } };

  const body = $('#tabBody');
  const render = { cli: tabCli, skills: tabSkills, configs: tabConfigs, env: tabEnv, jobs: tabJobs, logs: tabLogs }[tab];
  await render(body, { m, ov, logs, jobs, id });
}

function badge(k, ov, m, jobs) {
  let n = 0;
  if (k === 'jobs') n = jobs.filter(j => j.status === 'queued').length;
  else if (['skills', 'configs', 'env'].includes(k)) { const kind = { skills: 'skill', configs: 'config', env: 'env' }[k]; for (const r of ov.resources) if (r.kind === kind) { const st = syncState(ov, m, r); if (st && st.cls !== 'ok') n++; } }
  return n ? ` <span class="pill warn" style="margin-left:4px">${n}</span>` : '';
}

// ---- CLI ----
async function tabCli(body, { m, id }) {
  const inv = m.inventory || {};
  const latest = await npmLatest((inv.tools || []).map(t => t.package));
  body.innerHTML = `<div class="card"><table><tr><th>工具</th><th>版本</th><th>最新</th><th>来源</th><th>路径</th><th></th></tr>
  ${(inv.tools || []).map(t => { const lv = t.package ? latest[t.package] : ''; const old = lv && semverLt(t.version, lv); const cmd = upgradeCmd(t);
    return `<tr><td class="mono">${h(t.name)}</td><td class="mono ${old ? 'behind' : ''}">${h(t.version || '?')}</td><td class="mono muted">${h(lv || '')}</td><td class="muted small">${h(t.source)}${t.package ? ' · ' + h(t.package) : ''}</td><td class="mono small muted">${h(t.path || '')}</td>
    <td class="row nowrap" style="justify-content:flex-end;flex-wrap:nowrap">${cmd ? `<button class="ghost small" onclick="copyText(${JSON.stringify(cmd)})">复制命令</button>` : ''}
    ${t.source === 'npm-global' && t.package ? `<button class="small" data-job="npm_upgrade" data-pkg="${h(t.package)}">下次 sync 升级</button>` : ''}
    ${t.source === 'brew' ? `<button class="small" data-job="brew_upgrade" data-formula="${h(t.name)}">下次 sync 升级</button>` : ''}</td></tr>`; }).join('') || '<tr><td class="muted">尚无 inventory，等第一次 sync</td></tr>'}</table>
  <p class="help">「下次 sync 升级」会排一个任务，机器每 15 分钟 sync 时执行；「复制命令」则是自己去机器上跑。</p></div>
  <h2>运行时</h2><div class="card tight"><table>${(inv.runtimes || []).map(r => `<tr><td class="mono">${h(r.name)}</td><td class="mono">${h(r.version)}</td><td class="mono muted small">${h(r.path)}</td></tr>`).join('') || '<tr><td class="muted">无</td></tr>'}</table>
  <details style="margin-top:8px"><summary>npm -g 全部 ${(inv.npm_global || []).length} 个 · brew ${(inv.brew || []).length} 个</summary>
  <div class="row top"><table>${(inv.npm_global || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted">${h(p.version)}</td></tr>`).join('')}</table>
  <table>${(inv.brew || []).map(p => `<tr><td class="mono small">${h(p.name)}</td><td class="mono small muted">${h(p.version)}</td></tr>`).join('')}</table></div></details></div>`;
  $$('[data-job]', body).forEach(b => b.onclick = async () => {
    const payload = b.dataset.job === 'npm_upgrade' ? { package: b.dataset.pkg, version: 'latest' } : { formula: b.dataset.formula };
    await api('POST', `/api/admin/machines/${id}/jobs`, { type: b.dataset.job, payload }); toast('任务已排队，机器下次 sync 执行'); location.hash = `#/machines/${id}/jobs`;
  });
}

// ---- generic assigned-resource table used by skills/configs/env tabs ----
function assignedRows(ov, m, kind, linkOf, extra = () => '') {
  const rows = ov.resources.filter(r => r.kind === kind && ov.assignedOn[m.id]?.[r.id]);
  return rows.map(r => { const st = syncState(ov, m, r);
    return `<tr><td>${linkOf(r)}</td><td class="small">v${r.current_version}${st.a.has_override ? ' <span class="chip ov" title="这台机器有独立取值">override</span>' : ''}</td><td>${pill(st)}${st.ap?.updated_at ? ` <span class="muted small">${ago(st.ap.updated_at)}</span>` : ''}</td>${extra(r, st)}<td class="right"><button class="ghost small" data-unassign="${r.id}">取消分发</button></td></tr>`; }).join('');
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
  body.innerHTML = `<div class="card">
   <div class="row" style="margin-bottom:8px">${assignSelect(ov, m, 'skill', '分发一个 skill 到这台机器…')}</div>
   <table><tr><th>名称</th><th>版本</th><th>状态</th><th></th></tr>${assignedRows(ov, m, 'skill', r => `<a href="#/skills/${h(r.name)}" class="mono">${h(r.name)}</a>`) || '<tr><td class="muted" colspan="4">没有分发任何 skill</td></tr>'}</table>
   ${other.length ? `<details style="margin-top:8px"><summary>本机其他 skill（未托管）${other.length} 个</summary>
     <table>${other.map(l => `<tr><td class="mono small">${h(l.name)}</td><td class="muted small">${kb(l.size)}</td><td class="muted small">${l.managed ? '曾托管，将在下次 sync 移除' : ''}</td></tr>`).join('')}</table>
     <p class="help">想把某个本机 skill 收进库：在这台机器上跑 <code>agentdeck push ~/.agents/skills/&lt;name&gt;</code>。</p></details>` : ''}
  </div>`;
  bindAssign(body, id); bindUnassign(body, id);
}

// ---- 配置 ----
async function tabConfigs(body, { m, ov, id }) {
  const snap = m.snapshot || {}; const files = snap.configs || [];
  body.innerHTML = `<h2 style="margin-top:0">托管的配置 Profile</h2><div class="card">
   <div class="row" style="margin-bottom:8px">${assignSelect(ov, m, 'config', '把一个配置 Profile 应用到这台机器…')}</div>
   <table><tr><th>Profile</th><th>版本</th><th>状态</th><th>目标文件</th><th></th></tr>${assignedRows(ov, m, 'config', r => `<a href="#/configs/${r.id}" class="mono">${h(r.name)}</a> <span class="muted small">${h(r.meta?.tool || '')}</span>`, r => `<td class="mono small muted">${h(r.meta?.path || '')}</td>`) || '<tr><td class="muted" colspan="5">没有应用任何配置 Profile</td></tr>'}</table>
   <p class="help">Profile 里的 key 会合并进目标文件（JSON），文件里工具自己写的其他 key 不动；原文件先备份为 <code>*.agentdeck-bak</code>。</p></div>
  <h2>本机采集到的配置文件 <span class="muted">（只读，凭证已在机器端打码）</span></h2>
  <div class="row top" style="gap:14px"><div class="side-list card tight" id="cfgList">${files.map((f, i) => `<div class="small mono click" data-i="${i}" style="padding:5px 6px;border-radius:4px;cursor:pointer;${i === 0 ? 'background:#222a38' : ''}"><span class="muted">${h(f.tool)}</span> ${h(f.path)}<div class="muted" style="font-size:11px">${h(f.format)} · ${kb(f.size)} · ${ago(f.mod_time)}</div></div>`).join('') || '<span class="muted small">没有采集到配置文件</span>'}</div>
   <div class="card" style="flex:1;min-width:0"><div class="row between" style="margin-bottom:6px"><span class="mono small" id="cfgTitle">${h(files[0]?.path || '')}</span><button class="ghost small" id="mkProfile" ${files.length ? '' : 'disabled'}>以此为模板新建 Profile</button></div><pre id="cfgView" style="margin:0;max-height:60vh">${h(files[0] ? (files[0].truncated ? '(文件过大，未采集)' : files[0].content) : '')}</pre></div></div>`;
  bindAssign(body, id); bindUnassign(body, id);
  let cur = 0;
  $$('[data-i]', body).forEach(d => d.onclick = () => { cur = +d.dataset.i; $$('[data-i]', body).forEach(x => x.style.background = ''); d.style.background = '#222a38'; const f = files[cur]; $('#cfgTitle', body).textContent = f.path; $('#cfgView', body).textContent = f.truncated ? '(文件过大，未采集)' : f.content; });
  $('#mkProfile', body).onclick = () => { const f = files[cur]; sessionStorage.setItem('agentdeck_cfg_template', JSON.stringify({ tool: f.tool, path: f.path, format: f.format, content: f.content, from: m.name })); location.hash = '#/configs/_/new'; };
}

// ---- 环境变量 ----
async function tabEnv(body, { m, ov, id }) {
  const snap = m.snapshot || {}; const exports = (snap.exports || []).filter(e => e.kind !== 'append');
  const managedNames = new Set(ov.resources.filter(r => r.kind === 'env').map(r => r.name));
  const assignedNames = new Set(ov.resources.filter(r => r.kind === 'env' && ov.assignedOn[id]?.[r.id]).map(r => r.name));
  const unmanaged = exports.filter(e => !assignedNames.has(e.name) && !e.file.includes('agentdeck'));
  body.innerHTML = `<h2 style="margin-top:0">已分发到本机的变量 <span class="muted">写入 <code>~/.config/agentdeck/env.sh</code>，由 rc 文件 source</span></h2><div class="card">
   <div class="row" style="margin-bottom:8px">${assignSelect(ov, m, 'env', '把总表里的一个变量分发到这台机器…')}</div>
   <table><tr><th>变量</th><th>版本</th><th>状态</th><th></th></tr>${assignedRows(ov, m, 'env', r => `<a href="#/env/${r.id}" class="mono">${h(r.name)}</a>${r.meta?.secret ? ' 🔒' : ''}`) || '<tr><td class="muted" colspan="4">没有分发任何变量</td></tr>'}</table></div>
  <h2>本机 rc 文件里的 export <span class="muted">（采集自 .zshrc/.bashrc 等；秘密值只显示指纹）</span></h2><div class="card">
   <div class="row between" style="margin-bottom:8px"><span class="muted small">勾选后「导入到总表」：机器下次 sync 时读取真实值加密回传，并自动分发给这台机器。已在总表里的同名变量会成为它的新版本。</span><button class="small" id="importSel" disabled>导入到总表</button></div>
   <table><tr><th style="width:24px"></th><th>变量</th><th>值 / 指纹</th><th>来源</th><th>总表</th></tr>
   ${unmanaged.map((e, i) => `<tr><td><input type="checkbox" data-imp="${h(e.name)}"></td><td class="mono">${h(e.name)}</td><td class="mono small">${e.kind === 'secret' ? `<span class="pill">🔒 ${h(e.fingerprint)}</span>` : h(trunc(e.value, 60))}</td><td class="muted small mono">${h(e.file)}:${e.line}</td><td class="small">${managedNames.has(e.name) ? '<span class="pill acc">已有同名</span>' : '<span class="muted">—</span>'}</td></tr>`).join('') || '<tr><td class="muted" colspan="5">rc 文件里没有额外的 export</td></tr>'}</table></div>`;
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
  body.innerHTML = `<div class="card"><table><tr><th>#</th><th>类型</th><th>参数</th><th>状态</th><th>创建</th><th>结果</th><th></th></tr>
  ${jobs.map(j => `<tr><td class="mono">${j.id}</td><td class="mono">${h(j.type)}</td><td class="mono small">${h(JSON.stringify(j.payload))}</td><td><span class="pill ${j.status === 'done' ? 'ok' : j.status === 'failed' ? 'bad' : j.status === 'queued' ? 'warn' : ''}">${h(j.status)}</span></td><td class="muted small">${ago(j.created_at)}</td>
   <td>${j.result ? `<details><summary class="small">输出</summary><pre>${h(j.result)}</pre></details>` : ''}</td><td>${j.status === 'queued' ? `<button class="ghost small" data-cancel="${j.id}">取消</button>` : ''}</td></tr>`).join('') || '<tr><td class="muted" colspan="7">没有任务。在 CLI 页可以下发升级任务。</td></tr>'}</table></div>`;
  $$('[data-cancel]', body).forEach(b => b.onclick = async () => { await api('DELETE', `/api/admin/jobs/${b.dataset.cancel}`); reroute(); });
}

// ---- 日志 ----
async function tabLogs(body, { logs }) {
  body.innerHTML = `<div class="card">${logs.map(l => { const s = l.summary || {}; const changed = (s.resources || s.skills || []).filter(x => x.action !== 'unchanged');
    return `<details><summary><span class="mono small">${fmtTime(l.at)}</span> · ${changed.length ? changed.map(x => `<span class="${x.action === 'failed' ? 'behind' : ''}">${h(x.kind ? x.kind + '/' : '')}${h(x.name)}:${h(x.action)}</span>`).join(', ') : '无变化'} · ${(s.jobs || []).length} 任务 · ${h(s.duration || '')}${s.error ? ` · <span class="behind">${h(s.error)}</span>` : ''}</summary><pre>${h(JSON.stringify(s, null, 2))}</pre></details>`; }).join('') || '<p class="muted">无</p>'}</div>`;
}
