import { $, $$, app, api, h, ago, kb, overview, syncState, pill, toast, fail, ask, fmtTime, shortDigest, invalidate } from '../core.js';

const bySkillName = (ov, name) => ov.resources.find(r => r.kind === 'skill' && r.name === name);

export async function skillsView() {
  const ov = await overview();
  const ks = ov.resources.filter(r => r.kind === 'skill');
  app.innerHTML = `<div class="row between"><h1>Skill 库</h1>
   <div class="row"><button class="ghost" id="upload">上传 tar.gz</button><button id="create">+ 新建 skill</button></div></div>
   <div class="card"><table><tr><th>名称</th><th>描述</th><th>版本</th><th>大小</th><th>分发</th><th>更新</th></tr>
   ${ks.map(k => { const ms = ov.machines.filter(m => ov.assignedOn[m.id]?.[k.id]); const bad = ms.filter(m => syncState(ov, m, k)?.cls !== 'ok').length;
     return `<tr class="click" onclick="location.hash='#/skills/${h(k.name)}'"><td class="mono">${h(k.name)}</td><td class="muted small">${h(k.meta?.description || '')}</td><td>v${k.current_version}</td><td class="muted small">${kb(k.current_size)}</td><td class="small">${ms.map(m => `<span class="chip ${syncState(ov, m, k)?.cls !== 'ok' ? 'ov' : ''}">${h(m.name)}</span>`).join(' ') || '<span class="muted">—</span>'}${bad ? ` <span class="pill warn">${bad} 待同步</span>` : ''}</td><td class="muted small">${ago(k.updated_at)}</td></tr>`; }).join('') || '<tr><td class="muted" colspan="6">还没有 skill。新建一个，或在机器上 <code>agentdeck push ~/.agents/skills/xxx</code>。</td></tr>'}</table></div>
   <input type="file" id="file" accept=".tgz,.tar.gz,application/gzip" hidden>`;
  $('#create').onclick = () => { location.hash = '#/skills/_new'; };
  $('#upload').onclick = () => $('#file').click();
  $('#file').onchange = async e => {
    const f = e.target.files[0]; if (!f) return;
    const name = prompt('skill 名称（小写字母/数字/-）', f.name.replace(/(-v\d+)?\.(tgz|tar\.gz)$/, ''));
    if (!name) return;
    try { const r = await api('POST', `/api/admin/skills/${name}?note=upload`, f, true); toast(r.created ? `已发布 ${name} v${r.version.version}` : '内容未变化'); location.hash = '#/skills/' + name; } catch (err) { fail(err); }
  };
}

export async function skillDetail(name) {
  const ov = await overview();
  const k = bySkillName(ov, name);
  if (!k) { app.innerHTML = `<a href="#/skills" class="muted small">← Skill 库</a><p class="muted">没有叫 ${h(name)} 的 skill</p>`; return; }
  const det = await api('GET', `/api/admin/resources/${k.id}`);
  const versions = det.versions || [];
  const cur = versions.find(v => v.id === k.current_version_id) || versions[0];
  const files = cur ? await api('GET', `/api/admin/versions/${cur.id}/files`) : { files: [] };

  app.innerHTML = `<a href="#/skills" class="muted small">← Skill 库</a>
  <div class="row between"><h1 class="mono">${h(k.name)} <span class="muted small">v${k.current_version}</span></h1>
   <div class="row"><button id="edit">编辑并发布新版本</button><button class="ghost" id="dlBtn">下载</button><button class="danger small" id="del">删除</button></div></div>
  <p class="muted">${h(k.meta?.description) || '<i>无描述</i>'} <button class="ghost small" id="editDesc">改</button></p>

  <h2>分发到机器</h2><div class="card"><table><tr><th style="width:24px"></th><th>机器</th><th>状态</th><th>已应用</th></tr>
   ${ov.machines.map(m => { const st = syncState(ov, m, k);
     return `<tr><td><input type="checkbox" data-m="${m.id}" ${st ? 'checked' : ''}></td><td>${h(m.name)}</td><td>${pill(st) || '<span class="muted small">未分发</span>'}</td><td class="muted small">${st?.ap ? `${shortDigest(st.ap.digest)} · ${ago(st.ap.updated_at)}` : ''}</td></tr>`; }).join('') || '<tr><td class="muted" colspan="4">还没有机器</td></tr>'}</table></div>

  <h2>文件 <span class="muted">(v${cur?.version ?? '-'}, ${files.files.length} 个, ${kb(k.current_size)})</span></h2>
  <div class="card filetree"><ul id="ft">${files.files.map((f, i) => `<li data-i="${i}" class="${i === 0 ? 'active' : ''}">${h(f.path)} <span class="muted">${kb(f.size)}</span></li>`).join('')}</ul>
   <pre id="fv" style="flex:1;margin:0;max-height:520px">${h(files.files[0]?.text ?? (files.files[0]?.binary ? '(binary)' : ''))}</pre></div>

  <h2>版本历史</h2><div class="card"><table><tr><th>版本</th><th>digest</th><th>大小</th><th>备注</th><th>来源</th><th>时间</th><th></th></tr>
  ${versions.map(v => `<tr><td>v${v.version}${v.id === k.current_version_id ? ' <span class="pill ok">当前</span>' : ''}</td><td class="mono small muted">${shortDigest(v.digest)}</td><td class="muted small">${kb(v.size)}</td><td class="small">${h(v.note)}</td><td class="muted small">${h(v.created_by)}</td><td class="muted small">${fmtTime(v.created_at)}</td>
   <td class="right">${v.id !== k.current_version_id ? `<button class="ghost small" data-rb="${v.id}">设为当前</button>` : ''}</td></tr>`).join('')}</table></div>`;

  $('#ft').onclick = e => { const li = e.target.closest('li'); if (!li) return; $('#ft .active')?.classList.remove('active'); li.classList.add('active'); const f = files.files[+li.dataset.i]; $('#fv').textContent = f.binary ? '(binary)' : f.text; };
  $('#edit').onclick = () => { location.hash = `#/skills/${name}/edit`; };
  $('#dlBtn').onclick = () => cur && dl(`/api/admin/versions/${cur.id}/archive`, `${name}-v${cur.version}.tar.gz`);
  $('#editDesc').onclick = async () => { const d = prompt('描述', k.meta?.description || ''); if (d !== null) { await api('PATCH', `/api/admin/resources/${k.id}`, { meta: { description: d } }); invalidate(); reroute(); } };
  $('#del').onclick = async () => { if (await ask('删除 skill', `删除 <b>${h(name)}</b> 及全部版本？已分发机器下次 sync 会移除本地副本。`, '删除', true)) { await api('DELETE', `/api/admin/resources/${k.id}`); location.hash = '#/skills'; } };
  $$('[data-m]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, resource_id: k.id, assigned: c.checked }); toast(c.checked ? '已分发，机器下次 sync 安装' : '已取消'); invalidate(); reroute(); } catch (e) { c.checked = !c.checked; fail(e); } });
  $$('[data-rb]').forEach(b => b.onclick = async () => { await api('POST', `/api/admin/resources/${k.id}/rollback`, { version_id: +b.dataset.rb }); toast('已切换当前版本'); invalidate(); reroute(); });
}

export async function skillEditor(name) {
  let k = null, existing = null;
  if (name) {
    const ov = await overview(); k = bySkillName(ov, name);
    if (!k) return skillsView();
    existing = (await api('GET', `/api/admin/versions/${k.current_version_id}/files`)).files;
  }
  const files = existing ? existing.filter(f => !f.binary).map(f => ({ path: f.path, text: f.text, exec: (f.mode & 0o111) !== 0 })) : [{ path: 'SKILL.md', text: `---\nname: my-skill\ndescription: 一句话说明这个 skill 什么时候该被用到\n---\n\n# my-skill\n\n在这里写指令。\n`, exec: false }];
  let cur = 0;
  const render = () => {
    app.innerHTML = `<a href="${k ? '#/skills/' + h(k.name) : '#/skills'}" class="muted small">← 取消</a>
    <div class="row between"><h1>${k ? '编辑 ' + h(k.name) : '新建 skill'}</h1>
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
      const nm = k ? k.name : $('#name').value.trim();
      if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(nm)) return fail(new Error('名称只能是小写字母、数字、. _ -'));
      try { const r = await api('PUT', `/api/admin/skills/${nm}/files`, { files, note: $('#note').value }); toast(r.created ? `已发布 v${r.version.version}` : '内容没有变化，未产生新版本'); invalidate(); location.hash = '#/skills/' + nm; } catch (e) { fail(e); }
    };
  };
  render();
}
