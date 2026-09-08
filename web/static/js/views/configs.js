import { $, $$, app, api, h, ago, kb, overview, configs, syncState, pill, toast, fail, ask, modal, closeModal, fmtTime, shortDigest, online, invalidate } from '../core.js';

const KNOWN_TOOLS = [
  { tool: 'claude', path: '~/.claude/settings.json', format: 'json' },
  { tool: 'codex', path: '~/.codex/config.toml', format: 'toml' },
  { tool: 'codex', path: '~/.codex/hooks.json', format: 'json' },
  { tool: 'hermes', path: '~/.hermes/config.yaml', format: 'yaml' },
  { tool: 'lark-cli', path: '~/.lark-cli/config.json', format: 'json' },
  { tool: 'gh', path: '~/.config/gh/config.yml', format: 'yaml' },
  { tool: 'gemini', path: '~/.gemini/settings.json', format: 'json' },
];

// ---- 配置页：Profile 列表 + 采集全貌 ----
export async function configsView(sub) {
  const [ov, cfg] = await Promise.all([overview(), configs()]);
  if (sub === 'new') return profileEditor(null, ov);
  const profiles = ov.resources.filter(r => r.kind === 'config');
  const machines = cfg.filter(m => m.snapshot);
  const tools = [...new Set(machines.flatMap(m => (m.snapshot.configs || []).map(c => c.tool)))].sort();
  const tool = sub && tools.includes(sub) ? sub : tools[0];

  app.innerHTML = `<div class="row between"><h1>配置</h1><button id="newP">+ 新建 Profile</button></div>
  <h2 style="margin-top:0">托管的 Profile <span class="muted">一份配置片段 → 应用到若干机器；每台机器可叠加自己的 override</span></h2>
  <div class="card"><table><tr><th>名称</th><th>工具</th><th>目标文件</th><th>版本</th><th>应用到</th><th>更新</th></tr>
  ${profiles.map(p => { const ms = ov.machines.filter(m => ov.assignedOn[m.id]?.[p.id]);
    return `<tr class="click" onclick="location.hash='#/configs/${p.id}'"><td class="mono">${h(p.name)}</td><td>${h(p.meta?.tool || '')}</td><td class="mono small muted">${h(p.meta?.path || '')} <span class="pill">${h(p.meta?.format || '')}</span></td><td>v${p.current_version}</td>
    <td class="small">${ms.map(m => { const st = syncState(ov, m, p); return `<span class="chip ${st.cls !== 'ok' ? 'ov' : ''}" title="${h(st.text)}">${h(m.name)}${st.a.has_override ? ' *' : ''}</span>`; }).join(' ') || '<span class="muted">—</span>'}</td><td class="muted small">${ago(p.updated_at)}</td></tr>`; }).join('') || `<tr><td class="muted" colspan="6">还没有 Profile。可以直接新建，或到下面某台机器的配置文件里点「以此为模板新建 Profile」。目前机器端只会合并 <b>JSON</b> 格式；TOML/YAML 的 Profile 会保存但同步时标记失败，等下个版本。</td></tr>`}</table></div>

  <h2>各机器采集到的配置文件 <span class="muted">只读；凭证在机器端已打码为 <code>&lt;redacted:指纹&gt;</code>，指纹相同 = 同一个 key</span></h2>
  ${tools.length ? `<div class="tabs">${tools.map(t => `<button class="${t === tool ? 'active' : ''}" data-tool="${h(t)}">${h(t)} <span class="muted">${machines.filter(m => (m.snapshot.configs || []).some(c => c.tool === t)).length}</span></button>`).join('')}</div>
  <div class="row top" style="gap:14px" id="snapArea"></div>` : '<p class="muted">还没有机器上报配置</p>'}`;

  $('#newP').onclick = () => { location.hash = '#/configs/_/new'; };
  $$('[data-tool]').forEach(b => b.onclick = () => { location.hash = '#/configs/_/' + b.dataset.tool; });
  if (tools.length) renderSnapshots($('#snapArea'), machines, tool);
}

function renderSnapshots(area, machines, tool) {
  const rows = machines.map(m => ({ m, files: (m.snapshot.configs || []).filter(c => c.tool === tool) })).filter(x => x.files.length);
  let sel = rows[0] ? { mid: rows[0].m.machine_id, i: 0 } : null;
  const draw = () => {
    const cur = sel && rows.find(x => x.m.machine_id === sel.mid); const file = cur?.files[sel.i];
    area.innerHTML = `<div class="side-list">${rows.map(x => `<div class="card tight" style="margin-bottom:8px;${x.m.machine_id === sel?.mid ? 'border-color:var(--accent)' : ''}">
        <div style="font-weight:600"><span class="dot ${online(x.m.snapshot_at)}"></span>${h(x.m.machine_name)} <span class="muted small" style="font-weight:400">${ago(x.m.snapshot_at)}</span></div>
        ${x.files.map((f, i) => `<div class="small mono" style="padding:3px 0;cursor:pointer;${x.m.machine_id === sel?.mid && i === sel.i ? 'color:var(--accent)' : 'color:var(--muted)'}" data-sel="${x.m.machine_id}:${i}">${h(f.path)} <span class="muted">${kb(f.size)}</span></div>`).join('')}</div>`).join('')}</div>
      <div class="card" style="flex:1;min-width:0">${file ? `<div class="row between" style="margin-bottom:8px"><span class="mono">${h(cur.m.machine_name)} · ${h(file.path)}</span><div class="row"><span class="muted small">${h(file.format)} · 修改于 ${ago(file.mod_time)}</span><button class="ghost small" id="tpl">以此为模板新建 Profile</button></div></div>
        <pre style="max-height:65vh;margin:0">${h(file.truncated ? '(文件过大，未采集)' : file.content)}</pre>` : '<p class="muted">选择左侧一个文件</p>'}</div>`;
    $$('[data-sel]', area).forEach(d => d.onclick = () => { const [mid, i] = d.dataset.sel.split(':'); sel = { mid, i: +i }; draw(); });
    $('#tpl', area)?.addEventListener('click', () => { sessionStorage.setItem('agentdeck_cfg_template', JSON.stringify({ tool: file.tool, path: file.path, format: file.format, content: file.content, from: cur.m.machine_name })); location.hash = '#/configs/_/new'; });
  };
  draw();
}

// ---- Profile 详情 ----
export async function configDetail(id, sub) {
  const ov = await overview();
  const p = ov.byResource[+id];
  if (!p || p.kind !== 'config') { app.innerHTML = `<a href="#/configs" class="muted small">← 配置</a><p class="muted">Profile 不存在</p>`; return; }
  const det = await api('GET', `/api/admin/resources/${p.id}`);
  if (sub === 'edit') return profileEditor(p, ov, det.content || '');
  const versions = det.versions || [];
  const m = p.meta || {};

  app.innerHTML = `<a href="#/configs" class="muted small">← 配置</a>
  <div class="row between"><h1 class="mono">${h(p.name)} <span class="muted small">v${p.current_version}</span></h1>
   <div class="row"><button id="edit">编辑并发布新版本</button><button class="danger small" id="del">删除</button></div></div>
  <div class="row muted small" style="margin:-8px 0 14px;gap:18px"><span>工具 <b>${h(m.tool || '-')}</b></span><span>目标 <code>${h(m.path || '')}</code></span><span class="pill">${h(m.format || '')}</span>${m.description ? `<span>${h(m.description)}</span>` : ''}${m.format !== 'json' ? '<span class="pill warn">机器端暂只支持 JSON 合并</span>' : ''}</div>

  <div class="row top" style="gap:16px">
   <div style="flex:1;min-width:360px"><h2 style="margin-top:0">内容 <span class="muted">顶层 key 合并进目标文件</span></h2><pre style="margin:0;max-height:60vh">${h(det.content || '')}</pre></div>
   <div style="flex:1;min-width:360px"><h2 style="margin-top:0">应用到机器</h2>
   <div class="card"><table><tr><th style="width:24px"></th><th>机器</th><th>状态</th><th>Override</th></tr>
   ${ov.machines.map(mc => { const st = syncState(ov, mc, p); const a = det.assignments.find(x => x.machine_id === mc.id);
     return `<tr><td><input type="checkbox" data-m="${mc.id}" ${st ? 'checked' : ''}></td><td>${h(mc.name)}</td><td>${pill(st) || '<span class="muted small">未应用</span>'}${st?.detail ? `<div class="small behind">${h(st.detail)}</div>` : ''}</td>
     <td>${st ? `<button class="ghost small" data-ov="${mc.id}">${a?.has_override ? '编辑 override' : '+ override'}</button>${a?.has_override ? ` <button class="link small" data-ovclear="${mc.id}">清除</button>` : ''}` : ''}</td></tr>`; }).join('')}</table>
   <p class="help">Override 是这台机器独有的一小段同格式 JSON，叠加在 Profile 之上（同名 key 以 override 为准）。适合 model、代理地址这类"大体一样，个别机器不同"的字段。</p>
   ${det.assignments.filter(a => a.has_override).map(a => `<details style="margin-top:6px"><summary class="small">${h(ov.byMachine[a.machine_id]?.name || a.machine_id)} 的 override</summary><pre>${h(a.override || '')}</pre></details>`).join('')}
   </div></div></div>

  <h2>版本历史</h2><div class="card"><table><tr><th>版本</th><th>digest</th><th>大小</th><th>备注</th><th>来源</th><th>时间</th><th></th></tr>
  ${versions.map(v => `<tr><td>v${v.version}${v.id === p.current_version_id ? ' <span class="pill ok">当前</span>' : ''}</td><td class="mono small muted">${shortDigest(v.digest)}</td><td class="muted small">${kb(v.size)}</td><td class="small">${h(v.note)}</td><td class="muted small">${h(v.created_by)}</td><td class="muted small">${fmtTime(v.created_at)}</td>
   <td class="right">${v.id !== p.current_version_id ? `<button class="ghost small" data-rb="${v.id}">设为当前</button>` : ''}</td></tr>`).join('')}</table></div>`;

  $('#edit').onclick = () => { location.hash = `#/configs/${p.id}/edit`; };
  $('#del').onclick = async () => { if (await ask('删除 Profile', `删除 <b>${h(p.name)}</b>？已应用的机器下次 sync 会把它写入的 key 从目标文件移除。`, '删除', true)) { await api('DELETE', `/api/admin/resources/${p.id}`); location.hash = '#/configs'; } };
  $$('[data-m]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, resource_id: p.id, assigned: c.checked }); toast(c.checked ? '已应用，机器下次 sync 写入' : '已取消'); invalidate(); reroute(); } catch (e) { c.checked = !c.checked; fail(e); } });
  $$('[data-rb]').forEach(b => b.onclick = async () => { await api('POST', `/api/admin/resources/${p.id}/rollback`, { version_id: +b.dataset.rb }); toast('已切换当前版本'); invalidate(); reroute(); });
  $$('[data-ov]').forEach(b => b.onclick = () => {
    const a = det.assignments.find(x => x.machine_id === b.dataset.ov);
    const box = modal(`<h3>${h(ov.byMachine[b.dataset.ov]?.name)} 的 override</h3><div class="field"><label>${h(m.format)}，只写需要覆盖的顶层 key</label><textarea id="ovText" style="min-height:220px">${h(a?.override || '')}</textarea></div>
      <div class="row" style="justify-content:flex-end"><button class="ghost" id="c">取消</button><button id="ok">保存</button></div>`);
    $('#c', box).onclick = closeModal;
    $('#ok', box).onclick = async () => {
      const text = $('#ovText', box).value;
      if (m.format === 'json' && text.trim()) { try { JSON.parse(text); } catch (e) { return fail(new Error('不是合法 JSON: ' + e.message)); } }
      await api('PUT', '/api/admin/assignments', { machine_id: b.dataset.ov, resource_id: p.id, assigned: true, set_override: true, override: text }); closeModal(); toast('已保存'); invalidate(); reroute();
    };
  });
  $$('[data-ovclear]').forEach(b => b.onclick = async () => { await api('PUT', '/api/admin/assignments', { machine_id: b.dataset.ovclear, resource_id: p.id, assigned: true, set_override: true, override: '' }); toast('已清除'); invalidate(); reroute(); });
}

// ---- Profile 编辑器 ----
function profileEditor(p, ov, content = '') {
  let tpl = null;
  if (!p) { try { tpl = JSON.parse(sessionStorage.getItem('agentdeck_cfg_template') || 'null'); } catch {} sessionStorage.removeItem('agentdeck_cfg_template'); }
  const m = p?.meta || {};
  const init = { name: p?.name || (tpl ? `${tpl.tool}-default` : ''), tool: m.tool || tpl?.tool || 'claude', path: m.path || tpl?.path || '~/.claude/settings.json', format: m.format || tpl?.format || 'json', description: m.description || '', content: p ? content : (tpl ? stripRedacted(tpl.content) : '{\n  \n}') };

  app.innerHTML = `<a href="${p ? '#/configs/' + p.id : '#/configs'}" class="muted small">← 取消</a>
  <div class="row between"><h1>${p ? '编辑 ' + h(p.name) : '新建配置 Profile'}</h1><div class="row"><input id="note" placeholder="版本备注（可选）" style="width:220px"><button id="save">${p ? '发布新版本' : '创建'}</button></div></div>
  ${tpl ? `<p class="muted small">已用 <b>${h(tpl.from)}</b> 的 <code>${h(tpl.path)}</code> 作为起点。打码的凭证行已去掉——凭证请放到「环境变量」总表里，不要写进 Profile。删掉你不想统一的 key，只留要托管的部分。</p>` : ''}
  <div class="row top" style="gap:16px">
   <div class="card" style="min-width:300px">
    <div class="field"><label>名称</label><input id="pName" class="mono" value="${h(init.name)}" ${p ? 'disabled' : ''} placeholder="claude-default"></div>
    <div class="field"><label>工具</label><input id="pTool" list="toolList" value="${h(init.tool)}"><datalist id="toolList">${[...new Set(KNOWN_TOOLS.map(k => k.tool))].map(t => `<option value="${t}">`).join('')}</datalist></div>
    <div class="field"><label>目标文件（~/ 开头）</label><input id="pPath" class="mono" list="pathList" value="${h(init.path)}"><datalist id="pathList">${KNOWN_TOOLS.map(k => `<option value="${k.path}">`).join('')}</datalist></div>
    <div class="field"><label>格式</label><select id="pFmt">${['json', 'toml', 'yaml'].map(f => `<option ${f === init.format ? 'selected' : ''}>${f}</option>`).join('')}</select></div>
    <div class="field"><label>描述（可选）</label><input id="pDesc" value="${h(init.description)}"></div>
    <p class="help">机器端按顶层 key 合并：Profile 里有的 key 覆盖目标文件里的同名 key，其他 key 保留；Profile 后来删掉的 key 也会从目标文件移除。目前只有 JSON 会真正写入。</p>
   </div>
   <div style="flex:1;min-width:0"><textarea id="pContent" style="min-height:560px">${h(init.content)}</textarea></div>
  </div>`;
  $('#pTool').oninput = () => { const k = KNOWN_TOOLS.find(x => x.tool === $('#pTool').value); if (k && !p) { $('#pPath').value = k.path; $('#pFmt').value = k.format; } };
  $('#save').onclick = async () => {
    const name = $('#pName').value.trim(), fmt = $('#pFmt').value, text = $('#pContent').value;
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(name)) return fail(new Error('名称只能是字母、数字、. _ -'));
    if (fmt === 'json') { try { const o = JSON.parse(text); if (typeof o !== 'object' || Array.isArray(o)) throw new Error('顶层必须是对象'); } catch (e) { return fail(new Error('不是合法 JSON: ' + e.message)); } }
    try {
      const r = await api('PUT', `/api/admin/config/${name}`, { content: text, tool: $('#pTool').value.trim(), path: $('#pPath').value.trim(), format: fmt, description: $('#pDesc').value, note: $('#note').value });
      toast(r.created ? `已发布 v${r.version.version}` : '内容没有变化'); invalidate(); location.hash = '#/configs/' + r.resource.id;
    } catch (e) { fail(e); }
  };
}

// drop lines whose value is a redaction marker so a template never carries fake secrets
function stripRedacted(text) {
  const lines = (text || '').split('\n').filter(l => !/<redacted:[0-9a-f]+>/.test(l));
  let out = lines.join('\n');
  try { const o = JSON.parse(out); return JSON.stringify(o, null, 2); } catch {}
  // fix trailing comma before } after removing a line
  out = out.replace(/,(\s*[}\]])/g, '$1');
  try { const o = JSON.parse(out); return JSON.stringify(o, null, 2); } catch {}
  return out;
}
