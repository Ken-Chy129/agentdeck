import { $, $$, app, api, h, ago, kb, overview, configs, syncState, pill, toast, fail, ask, modal, closeModal, fmtTime, shortDigest, online, invalidate, pageHeader, crumb, empty, emptyRow, editFile, syncNow, awaitJob, jobPending } from '../core.js';

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
  const [ov, cfg] = await Promise.all([overview(), configs('full')]);
  if (sub === 'new') return profileEditor(null, ov);
  const profiles = ov.resources.filter(r => r.kind === 'config');
  const machines = cfg.filter(m => m.snapshot);
  const tools = [...new Set(machines.flatMap(m => (m.snapshot.configs || []).map(c => c.tool)))].sort();
  const tool = sub && tools.includes(sub) ? sub : tools[0];

  app.innerHTML = pageHeader({ title: '配置', desc: '上面是你托管的配置 Profile（一份片段 → 应用到若干机器，每台可叠加 override）；下面是各机器实际文件的只读全貌。', actions: '<button id="newP">+ 新建 Profile</button>' }) + `
  <h2>托管的 Profile</h2>
  <div class="card flush"><table><thead><tr><th>名称</th><th>工具</th><th>目标文件</th><th>版本</th><th>应用到</th><th>更新</th></tr></thead><tbody>
  ${profiles.map(p => { const ms = ov.machines.filter(m => ov.assignedOn[m.id]?.[p.id]);
    return `<tr class="click" onclick="location.hash='#/configs/${p.id}'"><td class="mono">${h(p.name)}</td><td>${h(p.meta?.tool || '')}</td><td class="mono small muted">${h(p.meta?.path || '')} <span class="pill">${h(p.meta?.format || '')}</span></td><td class="muted">v${p.current_version}</td>
    <td><div class="row" style="gap:5px">${ms.map(m => { const st = syncState(ov, m, p); return `<span class="chip ${st.cls !== 'ok' ? 'ov' : ''}" title="${h(st.text)}">${h(m.name)}${st.a.has_override ? ' *' : ''}</span>`; }).join('') || '<span class="faint">—</span>'}</div></td><td class="muted small">${ago(p.updated_at)}</td></tr>`; }).join('') || emptyRow(6, '还没有 Profile。直接新建，或在下面某台机器的配置文件上点「以此为模板新建 Profile」。<br><span class="faint">目前机器端只合并 <b>JSON</b>；TOML/YAML 的 Profile 会保存但同步时标记失败。</span>')}</tbody></table></div>

  <h2>各机器采集到的配置文件 <span class="hint">只读 · 凭证在机器端已打码为 <code>&lt;redacted:指纹&gt;</code>，指纹相同 = 同一个 key</span></h2>
  ${tools.length ? `<div class="tabs">${tools.map(t => `<button class="${t === tool ? 'active' : ''}" data-tool="${h(t)}">${h(t)} <span class="faint">${machines.filter(m => (m.snapshot.configs || []).some(c => c.tool === t)).length}</span></button>`).join('')}</div>
  <div class="cols" id="snapArea"></div>` : empty('还没有机器上报配置')}`;

  $('#newP').onclick = () => { location.hash = '#/configs/_/new'; };
  $$('[data-tool]').forEach(b => b.onclick = () => { location.hash = '#/configs/_/' + b.dataset.tool; });
  if (tools.length) renderSnapshots($('#snapArea'), machines, tool);
}

function renderSnapshots(area, machines, tool) {
  const rows = machines.map(m => ({ m, files: (m.snapshot.configs || []).filter(c => c.tool === tool) })).filter(x => x.files.length);
  let sel = rows[0] ? { mid: rows[0].m.machine_id, i: 0 } : null;
  const draw = () => {
    const cur = sel && rows.find(x => x.m.machine_id === sel.mid); const file = cur?.files[sel.i];
    const editable = file && !file.truncated;
    area.innerHTML = `<div class="narrow card tight filelist">${rows.map(x => `<div class="group"><span class="dot ${online(x.m.snapshot_at)}"></span>${h(x.m.machine_name)} <span class="faint">${ago(x.m.snapshot_at)}</span></div>
        ${x.files.map((f, i) => `<div class="item ${x.m.machine_id === sel?.mid && i === sel.i ? 'active' : ''}" data-sel="${x.m.machine_id}:${i}">${h(f.path)}<div class="sub">${kb(f.size)} · 改于 ${ago(f.mod_time)}</div></div>`).join('')}`).join('')}</div>
      <div class="card">${file ? `<div class="row between mb8"><span class="mono small"><b>${h(cur.m.machine_name)}</b> · ${h(file.path)}</span><div class="row"><span class="pill">${h(file.format)}</span>${editable ? '<button class="ghost small" id="edit">编辑</button>' : ''}<button class="ghost small" id="tpl">以此为模板新建 Profile</button></div></div>
        <pre class="fill" id="view">${h(file.truncated ? '(文件过大，未采集)' : file.content)}</pre>` : empty('选择左侧一个文件')}</div>`;
    $$('[data-sel]', area).forEach(d => d.onclick = () => { const [mid, i] = d.dataset.sel.split(':'); sel = { mid, i: +i }; draw(); });
    $('#tpl', area)?.addEventListener('click', () => { sessionStorage.setItem('agentdeck_cfg_template', JSON.stringify({ tool: file.tool, path: file.path, format: file.format, content: file.content, from: cur.m.machine_name })); location.hash = '#/configs/_/new'; });
    $('#edit', area)?.addEventListener('click', () => editFileInline(area, cur.m, file, draw));
  };
  draw();
}

// Swaps the read-only preview for an editor. The text shown here is the
// redacted copy, so <redacted:fp> placeholders are left in place deliberately:
// the machine swaps them back to the real secret before writing.
function editFileInline(area, machine, file, redraw) {
  const card = $('#view', area).parentElement;
  card.innerHTML = `<div class="row between mb8"><span class="mono small"><b>${h(machine.machine_name)}</b> · ${h(file.path)}</span><span class="pill">${h(file.format)}</span></div>
    <textarea id="fileEdit" style="min-height:52vh">${h(file.content)}</textarea>
    <p class="help">保存会下发给机器改写这个文件，原文件留一份 <code>.agentdeck-bak</code> 备份。<b>带 <code>&lt;redacted:…&gt;</code> 的行照原样留着即可</b>——机器端会换回真实的值；手动改写它反而会把凭证写坏。</p>
    <div class="row end" style="gap:8px"><button class="ghost" id="fileCancel">取消</button><button id="fileSave">保存到机器</button></div>`;
  const ta = $('#fileEdit', card);
  ta.focus();
  $('#fileCancel', card).onclick = redraw;
  $('#fileSave', card).onclick = async () => {
    const text = ta.value;
    if (file.format === 'json') {
      // Placeholders aren't valid JSON on their own, but they sit inside
      // strings, so the text still parses. A real syntax error is worth
      // catching before we make a round trip to the machine.
      try { JSON.parse(text); } catch (e) { return fail(new Error('不是合法 JSON: ' + e.message)); }
    }
    if (text === file.content) return fail(new Error('内容没有变化'));
    const btn = $('#fileSave', card);
    btn.disabled = true; btn.textContent = '下发中…';
    try {
      await saveFile(machine.machine_id, file.path, text);
      toast('已写入机器');
      invalidate();
      window.reroute?.();
    } catch (e) {
      btn.disabled = false; btn.textContent = '保存到机器';
      fail(e);
    }
  };
}

// Writes the file, then makes the machine re-collect so the page doesn't keep
// showing the copy we just replaced.
async function saveFile(machineID, path, content) {
  let j = await editFile(machineID, path, content, { waitSec: 60 });
  if (jobPending(j.status)) j = await awaitJob(j.id, { tries: 60, everyMs: 2000 }) || j;
  if (jobPending(j.status)) throw new Error('机器还没接单。它可能不在线，稍后去「任务」页看结果。');
  if (j.status !== 'done') throw new Error(j.result || '写入失败');
  try { await syncNow(machineID, { waitSec: 60 }); } catch {}
}

// ---- Profile 详情 ----
export async function configDetail(id, sub) {
  const ov = await overview();
  const p = ov.byResource[+id];
  if (!p || p.kind !== 'config') { app.innerHTML = crumb('#/configs', '配置') + empty('Profile 不存在'); return; }
  const det = await api('GET', `/api/admin/resources/${p.id}`);
  if (sub === 'edit') return profileEditor(p, ov, det.content || '');
  const versions = det.versions || [];
  const m = p.meta || {};

  app.innerHTML = crumb('#/configs', '配置') + pageHeader({ title: h(p.name), mono: true, sub: `v${p.current_version}`, desc: h(m.description || ''),
    actions: '<button id="edit">编辑并发布新版本</button><button class="danger small" id="del">删除</button>' }) + `
  <div class="meta-line"><span>工具 <b>${h(m.tool || '-')}</b></span><span>目标 <b class="mono">${h(m.path || '')}</b></span><span><span class="pill">${h(m.format || '')}</span></span>${m.format !== 'json' ? '<span class="pill warn">机器端暂只支持 JSON 合并</span>' : ''}</div>

  <div class="cols">
   <div class="wide"><h2>内容 <span class="hint">顶层 key 合并进目标文件</span></h2><pre class="fill" style="margin:0">${h(det.content || '')}</pre></div>
   <div><h2>应用到机器</h2>
   <div class="card flush"><table><tbody>
   ${ov.machines.map(mc => { const st = syncState(ov, mc, p); const a = det.assignments.find(x => x.machine_id === mc.id);
     return `<tr><td style="width:20px"><input type="checkbox" data-m="${mc.id}" ${st ? 'checked' : ''}></td><td>${h(mc.name)}${st?.detail ? `<div class="sub-note bad-text">${h(st.detail)}</div>` : ''}</td><td>${pill(st) || '<span class="faint small">未应用</span>'}</td>
     <td class="right nowrap">${st ? `<button class="ghost small" data-ov="${mc.id}">${a?.has_override ? '编辑 override' : '+ override'}</button>${a?.has_override ? ` <button class="link small" data-ovclear="${mc.id}">清除</button>` : ''}` : ''}</td></tr>`; }).join('')}</tbody></table>
   <p class="help">Override 是这台机器独有的一小段同格式 JSON，叠加在 Profile 之上（同名 key 以 override 为准）。适合 model、代理地址这类"大体一样、个别机器不同"的字段。</p></div>
   ${det.assignments.filter(a => a.has_override).map(a => `<div class="card"><details open><summary><b>${h(ov.byMachine[a.machine_id]?.name || a.machine_id)}</b> 的 override</summary><pre style="margin-bottom:0">${h(a.override || '')}</pre></details></div>`).join('')}
   <h2>版本历史</h2><div class="card flush"><table><tbody>
   ${versions.map(v => `<tr><td><b>v${v.version}</b>${v.id === p.current_version_id ? ' <span class="pill ok">当前</span>' : ''}<div class="sub-note">${fmtTime(v.created_at)} · ${h(v.created_by)} · ${kb(v.size)} · ${shortDigest(v.digest)}</div></td><td class="small muted">${h(v.note)}</td>
    <td class="right">${v.id !== p.current_version_id ? `<button class="ghost small" data-rb="${v.id}">设为当前</button>` : ''}</td></tr>`).join('')}</tbody></table></div>
   </div></div>`;

  $('#edit').onclick = () => { location.hash = `#/configs/${p.id}/edit`; };
  $('#del').onclick = async () => { if (await ask('删除 Profile', `删除 <b>${h(p.name)}</b>？已应用的机器下次 sync 会把它写入的 key 从目标文件移除。`, '删除', true)) { await api('DELETE', `/api/admin/resources/${p.id}`); location.hash = '#/configs'; } };
  $$('[data-m]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, resource_id: p.id, assigned: c.checked }); toast(c.checked ? '已应用，机器下次 sync 写入' : '已取消'); invalidate(); reroute(); } catch (e) { c.checked = !c.checked; fail(e); } });
  $$('[data-rb]').forEach(b => b.onclick = async () => { await api('POST', `/api/admin/resources/${p.id}/rollback`, { version_id: +b.dataset.rb }); toast('已切换当前版本'); invalidate(); reroute(); });
  $$('[data-ov]').forEach(b => b.onclick = () => {
    const a = det.assignments.find(x => x.machine_id === b.dataset.ov);
    const box = modal(`<h3>${h(ov.byMachine[b.dataset.ov]?.name)} 的 override</h3><div class="field"><label>${h(m.format)} · 只写需要覆盖的顶层 key</label><textarea id="ovText" style="min-height:220px">${h(a?.override || '')}</textarea></div>
      <div class="row end"><button class="ghost" id="c">取消</button><button id="ok">保存</button></div>`);
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

  app.innerHTML = crumb(p ? '#/configs/' + p.id : '#/configs', '取消') + pageHeader({ title: p ? '编辑 ' + h(p.name) : '新建配置 Profile',
    desc: tpl ? `已用 <b>${h(tpl.from)}</b> 的 <code>${h(tpl.path)}</code> 作为起点，打码的凭证行已去掉——凭证请放到「环境变量」总表里。删掉你不想统一的 key，只留要托管的部分。` : '',
    actions: `<input id="note" placeholder="版本备注（可选）" style="width:220px"><button id="save">${p ? '发布新版本' : '创建'}</button>` }) + `
  <div class="cols">
   <div class="narrow card">
    <div class="field"><label>名称</label><input id="pName" class="mono" value="${h(init.name)}" ${p ? 'disabled' : ''} placeholder="claude-default"></div>
    <div class="field"><label>工具</label><input id="pTool" list="toolList" value="${h(init.tool)}"><datalist id="toolList">${[...new Set(KNOWN_TOOLS.map(k => k.tool))].map(t => `<option value="${t}">`).join('')}</datalist></div>
    <div class="field"><label>目标文件（~/ 开头）</label><input id="pPath" class="mono" list="pathList" value="${h(init.path)}"><datalist id="pathList">${KNOWN_TOOLS.map(k => `<option value="${k.path}">`).join('')}</datalist></div>
    <div class="field"><label>格式</label><select id="pFmt">${['json', 'toml', 'yaml'].map(f => `<option ${f === init.format ? 'selected' : ''}>${f}</option>`).join('')}</select></div>
    <div class="field"><label>描述（可选）</label><input id="pDesc" value="${h(init.description)}"></div>
    <p class="help">机器端按顶层 key 合并：Profile 里有的 key 覆盖目标文件里的同名 key，其他 key 保留；Profile 后来删掉的 key 也会从目标文件移除。目前只有 JSON 会真正写入。</p>
   </div>
   <div><textarea id="pContent" style="min-height:600px">${h(init.content)}</textarea></div>
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

function stripRedacted(text) {
  const lines = (text || '').split('\n').filter(l => !/<redacted:[0-9a-f]+>/.test(l));
  let out = lines.join('\n');
  try { const o = JSON.parse(out); return JSON.stringify(o, null, 2); } catch {}
  out = out.replace(/,(\s*[}\]])/g, '$1');
  try { const o = JSON.parse(out); return JSON.stringify(o, null, 2); } catch {}
  return out;
}
