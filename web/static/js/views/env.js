import { $, $$, app, api, h, ago, overview, configs, syncState, pill, toast, fail, ask, modal, closeModal, fmtTime, shortDigest, trunc, copy, invalidate, pageHeader, crumb, empty, emptyRow } from '../core.js';

export async function envView(id) {
  if (id) return envDetail(id);
  const [ov, cfg] = await Promise.all([overview(), configs()]);
  const ms = ov.machines;
  const managed = ov.resources.filter(r => r.kind === 'env');
  const byName = Object.fromEntries(managed.map(r => [r.name, r]));
  const collected = {}; // name -> machine_id -> export (rc files, excluding agentdeck's env.sh)
  for (const c of cfg) for (const e of (c.snapshot?.exports || [])) if (e.kind !== 'append' && !/agentdeck/.test(e.file)) (collected[e.name] ||= {})[c.machine_id] = e;
  const names = [...new Set([...managed.map(r => r.name), ...Object.keys(collected)])].sort((a, b) => {
    const ma = !!byName[a], mb = !!byName[b]; if (ma !== mb) return ma ? -1 : 1; return a.localeCompare(b);
  });
  const q = (sessionStorage.getItem('agentdeck_env_q') || '').toLowerCase();
  const rcVal = (ex) => ex.kind === 'secret' ? `🔒 ${h(ex.fingerprint)}` : h(trunc(ex.value, 22));

  const cell = (n, m) => {
    const r = byName[n]; const st = r && syncState(ov, m, r); const ex = collected[n]?.[m.id];
    const rcNote = ex ? `<div class="sub-note mono" title="这台机器 rc 里还有一份：${h(ex.file)}:${ex.line}">rc: ${rcVal(ex)}</div>` : '';
    if (st) return `<td class="c"><label class="cellbox"><input type="checkbox" data-r="${r.id}" data-m="${m.id}" checked>${pill(st)}${st.a.has_override ? '<span class="chip ov" title="这台机器用独立取值">*</span>' : ''}</label>${rcNote}</td>`;
    if (r) return `<td class="c"><label class="cellbox"><input type="checkbox" data-r="${r.id}" data-m="${m.id}"><span class="faint small">未分发</span></label>${rcNote}</td>`;
    if (ex) return `<td class="c"><span class="mono small" title="${h(ex.file)}:${ex.line}">${rcVal(ex)}</span><div><button class="link small" data-import="${h(n)}" data-from="${m.id}">从这台导入</button></div></td>`;
    return '<td class="c faint">—</td>';
  };
  const consistency = (n) => { const per = collected[n] || {}; const vals = new Set(Object.values(per).map(e => e.kind === 'secret' ? e.fingerprint : e.value)); return vals.size > 1 ? ' <span class="pill warn" title="各机器 rc 里的取值不一致">rc 不一致</span>' : ''; };
  const managedN = managed.length, unmanagedN = names.length - managedN;

  app.innerHTML = pageHeader({ title: '环境变量', sub: `${managedN} 个在总表 · ${unmanagedN} 个仅在机器 rc 里`,
    desc: '行 = 变量，列 = 机器。勾选即分发（写入该机 <code>~/.config/agentdeck/env.sh</code>，rc 文件会 source 它）。🔒 是秘密值，点名称进详情可显示/复制。灰色 <b>rc:</b> 表示这台机器 rc 里自己 export 了一份、尚未纳入总表，可「从这台导入」。',
    actions: `<input id="q" placeholder="筛选变量…" value="${h(q)}" style="width:200px"><button id="newE">+ 新增变量</button>` }) + `
  <div class="card flush" style="overflow:auto"><table class="envtable"><thead><tr><th style="min-width:200px">变量</th><th style="min-width:180px">总表值</th>${ms.map(m => `<th class="c">${h(m.name)}</th>`).join('')}<th></th></tr></thead><tbody>
  ${names.filter(n => !q || n.toLowerCase().includes(q)).map(n => { const r = byName[n];
    return `<tr data-row="${h(n)}"><td>${r ? `<a href="#/env/${r.id}" class="mono">${h(n)}</a>` : `<span class="mono">${h(n)}</span>`}${r?.meta?.secret ? ' <span class="faint">🔒</span>' : ''}<div class="sub-note">${r ? h(r.meta?.description || `v${r.current_version}`) : '未纳入总表'}</div></td>
    <td>${r ? (r.meta?.secret ? `<span class="secret faint">••••••••</span> <button class="link small" data-reveal="${r.id}">显示</button>` : `<span class="mono small" id="val-${r.id}">…</span>`) : `<button class="link small" data-create="${h(n)}">手动填值</button>`}${consistency(n)}</td>
    ${ms.map(m => cell(n, m)).join('')}
    <td class="right nowrap">${r ? `<button class="ghost small" data-edit="${r.id}">编辑</button>` : ''}</td></tr>`; }).join('') || emptyRow(ms.length + 3, '还没有变量。')}</tbody></table>
  <p class="help">同一台机器 rc 里那份和总表分发的那份都会生效，后 source 的赢——rc 文件末尾 source env.sh，所以总表的值优先。收编完成后建议把 rc 里的那行删掉，"rc:" 提示就会消失。</p></div>`;

  for (const r of managed) if (!r.meta?.secret) api('GET', `/api/admin/resources/${r.id}/reveal`).then(v => { const el = $(`#val-${r.id}`); if (el) { el.textContent = trunc(v.value, 40); el.title = v.value; } }).catch(() => {});

  $('#q').oninput = () => { sessionStorage.setItem('agentdeck_env_q', $('#q').value); const qq = $('#q').value.toLowerCase(); $$('[data-row]').forEach(tr => tr.hidden = qq && !tr.dataset.row.toLowerCase().includes(qq)); };
  $('#newE').onclick = () => editEnv(null);
  $$('[data-create]').forEach(b => b.onclick = () => editEnv(null, b.dataset.create));
  $$('[data-edit]').forEach(b => b.onclick = () => editEnv(ov.byResource[+b.dataset.edit]));
  $$('[data-reveal]').forEach(b => b.onclick = async () => { const v = await api('GET', `/api/admin/resources/${b.dataset.reveal}/reveal`); const span = b.previousElementSibling; span.textContent = v.value; span.className = 'mono small'; b.textContent = '复制'; b.onclick = () => copy(v.value); });
  $$('input[data-r]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, resource_id: +c.dataset.r, assigned: c.checked }); toast(c.checked ? '已分发，机器下次 sync 写入' : '已取消，下次 sync 移除'); invalidate(); reroute(); } catch (e) { c.checked = !c.checked; fail(e); } });
  $$('[data-import]').forEach(b => b.onclick = async () => {
    const m = ov.byMachine[b.dataset.from];
    if (!await ask('导入到总表', `让 <b>${h(m.name)}</b> 下次 sync 时读取 <code>${h(b.dataset.import)}</code> 的真实值，加密回传到总表，并自动分发给它自己。<br><br>机器每 15 分钟 sync 一次；着急的话在那台机器上跑 <code>agentdeck sync</code>。`, '导入')) return;
    await api('POST', `/api/admin/machines/${m.id}/import-env`, { names: [b.dataset.import] }); toast('已登记，等机器回传');
  });
}

function editEnv(r, presetName) {
  const box = modal(`<h3>${r ? '编辑 ' + h(r.name) : '新增变量'}</h3>
    <div class="field"><label>名称</label><input id="eName" class="mono" value="${h(r?.name || presetName || '')}" ${r ? 'disabled' : ''} placeholder="OPENAI_API_KEY"></div>
    <div class="field"><label>值${r ? ' <span class="faint">（留空 = 不改值，只改描述/标记）</span>' : ''}</label><input id="eVal" class="mono" type="text" autocomplete="off" placeholder="${r ? '••••••••' : ''}"></div>
    <div class="field"><label>描述（可选）</label><input id="eDesc" value="${h(r?.meta?.description || '')}" placeholder="比如：自建代理的 key，proxy.ken-chy129.cn"></div>
    <label class="row small" style="gap:8px"><input type="checkbox" id="eSecret" ${r ? (r.meta?.secret ? 'checked' : '') : 'checked'}> 秘密值（列表里打码，显示需点击并记录审计）</label>
    <div class="row end"><button class="ghost" id="c">取消</button><button id="ok">${r ? '保存' : '创建'}</button></div>`);
  if (!r && !presetName) $('#eName', box).focus(); else $('#eVal', box).focus();
  $('#c', box).onclick = closeModal;
  $('#ok', box).onclick = async () => {
    const name = $('#eName', box).value.trim(), val = $('#eVal', box).value;
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) return fail(new Error('名称需是合法的环境变量名'));
    if (!r && !val) return fail(new Error('值不能为空'));
    const body = { secret: $('#eSecret', box).checked, description: $('#eDesc', box).value };
    if (val) body.value = val;
    try { const res = await api('PUT', `/api/admin/env/${name}`, body); closeModal(); toast(res.created ? (r ? `已发布 v${res.version.version}` : '已创建') : '已保存'); invalidate(); reroute(); } catch (e) { fail(e); }
  };
}

export async function envDetail(id) {
  const ov = await overview();
  const r = ov.byResource[+id];
  if (!r || r.kind !== 'env') { app.innerHTML = crumb('#/env', '环境变量') + empty('变量不存在'); return; }
  const det = await api('GET', `/api/admin/resources/${r.id}`);
  const secret = !!r.meta?.secret;
  let shown = null;
  if (!secret) { try { shown = (await api('GET', `/api/admin/resources/${r.id}/reveal`)).value; } catch {} }

  app.innerHTML = crumb('#/env', '环境变量') + pageHeader({ title: h(r.name) + (secret ? ' <span class="faint">🔒</span>' : ''), mono: true, sub: `v${r.current_version}`, desc: h(r.meta?.description || ''),
    actions: '<button id="edit">改值 / 描述</button><button class="danger small" id="del">删除</button>' }) + `
  <div class="card"><div class="row between"><div class="grow"><div class="faint xs" style="text-transform:uppercase;letter-spacing:.5px">当前值</div><div class="mono" id="curVal" style="margin-top:4px;word-break:break-all">${shown != null ? h(shown) : '<span class="secret faint">••••••••••••</span>'}</div></div>
   <div class="row nowrap">${secret ? '<button class="ghost small" id="reveal">显示</button>' : ''}<button class="ghost small" id="copyV">复制</button></div></div>
   <p class="help">分发后写入机器的 <code>~/.config/agentdeck/env.sh</code>：<code>export ${h(r.name)}=…</code>，新开的 shell 即生效。</p></div>

  <div class="cols">
   <div><h2>分发到机器</h2><div class="card flush"><table><tbody>
   ${ov.machines.map(m => { const st = syncState(ov, m, r); const a = det.assignments.find(x => x.machine_id === m.id);
     return `<tr><td style="width:20px"><input type="checkbox" data-m="${m.id}" ${st ? 'checked' : ''}></td><td>${h(m.name)}${st?.ap?.updated_at ? `<div class="sub-note">${ago(st.ap.updated_at)}</div>` : ''}</td><td>${pill(st) || '<span class="faint small">未分发</span>'}</td>
     <td class="small">${!st ? '' : a?.has_override ? `<span class="chip ov">独立取值</span> <button class="link small" data-revealov="${m.id}">显示</button>` : '<span class="faint">跟随总表</span>'}</td>
     <td class="right nowrap">${st ? `<button class="ghost small" data-ov="${m.id}">${a?.has_override ? '改独立取值' : '设独立取值'}</button>${a?.has_override ? ` <button class="link small" data-ovclear="${m.id}">改回跟随</button>` : ''}` : ''}</td></tr>`; }).join('')}</tbody></table>
   <p class="help">"独立取值"让某台机器用自己的值（比如每台一把不同的 key），其余机器仍跟随总表。</p></div></div>
   <div><h2>版本历史</h2><div class="card flush"><table><tbody>
   ${det.versions.map(v => `<tr><td><b>v${v.version}</b>${v.id === r.current_version_id ? ' <span class="pill ok">当前</span>' : ''}<div class="sub-note">${fmtTime(v.created_at)} · ${h(v.created_by)} · ${shortDigest(v.digest)}</div></td><td class="small muted">${h(v.note)}</td><td class="right">${v.id !== r.current_version_id ? `<button class="ghost small" data-rb="${v.id}">回滚到此</button>` : ''}</td></tr>`).join('')}</tbody></table></div>
   <h2>机器回报</h2><div class="card flush"><table><tbody>${det.applied.map(a => `<tr><td>${h(ov.byMachine[a.machine_id]?.name || a.machine_id)}</td><td><span class="pill ${a.status === 'failed' ? 'bad' : ''}">${h(a.status)}</span></td><td class="mono xs faint">${shortDigest(a.digest)}</td><td class="muted small">${h(a.detail)}</td><td class="muted small right">${ago(a.updated_at)}</td></tr>`).join('') || emptyRow(5, '尚无机器回报')}</tbody></table></div></div>
  </div>`;

  $('#edit').onclick = () => editEnv(r);
  $('#del').onclick = async () => { if (await ask('删除变量', `删除 <b>${h(r.name)}</b> 及全部版本？已分发机器下次 sync 会从 env.sh 移除。`, '删除', true)) { await api('DELETE', `/api/admin/resources/${r.id}`); location.hash = '#/env'; } };
  $('#reveal')?.addEventListener('click', async () => { const v = await api('GET', `/api/admin/resources/${r.id}/reveal`); shown = v.value; $('#curVal').textContent = v.value; $('#reveal').remove(); });
  $('#copyV').onclick = async () => { if (shown == null) shown = (await api('GET', `/api/admin/resources/${r.id}/reveal`)).value; copy(shown); };
  $$('[data-m]').forEach(c => c.onchange = async () => { try { await api('PUT', '/api/admin/assignments', { machine_id: c.dataset.m, resource_id: r.id, assigned: c.checked }); toast(c.checked ? '已分发' : '已取消'); invalidate(); reroute(); } catch (e) { c.checked = !c.checked; fail(e); } });
  $$('[data-rb]').forEach(b => b.onclick = async () => { await api('POST', `/api/admin/resources/${r.id}/rollback`, { version_id: +b.dataset.rb }); toast('已回滚'); invalidate(); reroute(); });
  $$('[data-revealov]').forEach(b => b.onclick = async () => { const v = await api('GET', `/api/admin/resources/${r.id}/reveal?machine=${b.dataset.revealov}`); b.replaceWith(Object.assign(document.createElement('span'), { className: 'mono small', textContent: v.value })); });
  $$('[data-ov]').forEach(b => b.onclick = () => {
    const m = ov.byMachine[b.dataset.ov];
    const box = modal(`<h3>${h(m.name)} 的独立取值</h3><div class="field"><label>${h(r.name)} 在这台机器上的值</label><input id="ovVal" class="mono" autocomplete="off"></div><div class="row end"><button class="ghost" id="c">取消</button><button id="ok">保存</button></div>`);
    $('#c', box).onclick = closeModal;
    $('#ok', box).onclick = async () => { const v = $('#ovVal', box).value; if (!v) return fail(new Error('值不能为空')); await api('PUT', '/api/admin/assignments', { machine_id: m.id, resource_id: r.id, assigned: true, set_override: true, override: v }); closeModal(); toast('已保存'); invalidate(); reroute(); };
  });
  $$('[data-ovclear]').forEach(b => b.onclick = async () => { await api('PUT', '/api/admin/assignments', { machine_id: b.dataset.ovclear, resource_id: r.id, assigned: true, set_override: true, override: '' }); toast('已改回跟随总表'); invalidate(); reroute(); });
}
