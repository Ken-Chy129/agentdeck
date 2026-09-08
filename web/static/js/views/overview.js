import { app, h, ago, online, isOnline, overview, configs, npmLatest, semverLt, syncState, fmtTime } from '../core.js';

export async function overviewView() {
  const [ov, cfg] = await Promise.all([overview(), configs()]);
  const ms = ov.machines;
  const tools = ms.flatMap(m => (m.inventory?.tools || []).map(t => ({ ...t, machine: m })));
  const latest = await npmLatest(tools.map(t => t.package));
  const behind = tools.filter(t => t.package && latest[t.package] && semverLt(t.version, latest[t.package]));

  // resource sync health
  let pending = 0, failed = 0;
  const problems = [];
  for (const m of ms) for (const r of ov.resources) {
    const st = syncState(ov, m, r);
    if (!st) continue;
    if (st.cls === 'bad') { failed++; problems.push({ m, r, st }); }
    else if (st.cls === 'warn') { pending++; problems.push({ m, r, st }); }
  }
  const byKind = (k) => ov.resources.filter(r => r.kind === k).length;

  // env drift: same variable, different fingerprints across machines
  const envDrift = [];
  const byName = {};
  for (const c of cfg) for (const e of (c.snapshot?.exports || [])) if (e.kind !== 'append') (byName[e.name] ||= {})[c.machine_id] = e.kind === 'secret' ? e.fingerprint : e.value;
  for (const [n, per] of Object.entries(byName)) { const vals = new Set(Object.values(per)); if (vals.size > 1) envDrift.push({ name: n, n: vals.size }); }

  const recent = ms.map(m => ({ m })).sort((a, b) => (b.m.last_seen_at || '').localeCompare(a.m.last_seen_at || ''));

  app.innerHTML = `<h1>总览</h1>
  <div class="stats">
    <div class="stat"><b>${ms.filter(m => isOnline(m.last_seen_at)).length}<span class="muted" style="font-size:14px"> / ${ms.length}</span></b><span>机器在线（30 分钟内同步过）</span></div>
    <div class="stat"><b>${byKind('skill')} · ${byKind('config')} · ${byKind('env')}</b><span>Skill · 配置 · 环境变量</span></div>
    <div class="stat"><b class="${pending ? 'behind' : ''}">${pending}</b><span>待同步 / 待更新</span></div>
    <div class="stat"><b style="${failed ? 'color:var(--bad)' : ''}">${failed}</b><span>同步失败</span></div>
    <div class="stat"><b class="${behind.length ? 'behind' : ''}">${behind.length}</b><span>CLI 可升级</span></div>
    <div class="stat"><b class="${envDrift.length ? 'behind' : ''}">${envDrift.length}</b><span>环境变量各机不一致</span></div>
  </div>

  <div class="row top" style="gap:16px;margin-top:16px">
   <div style="flex:1;min-width:320px">
    <h2>机器</h2>
    <div class="card tight"><table><tr><th>机器</th><th>系统</th><th>最近同步</th><th>CLI</th></tr>
    ${recent.map(({ m }) => { const inv = m.inventory || {}; const mine = behind.filter(t => t.machine.id === m.id);
      return `<tr class="click" onclick="location.hash='#/machines/${m.id}'"><td><span class="dot ${online(m.last_seen_at)}"></span><b>${h(m.name)}</b></td><td class="muted small">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</td><td class="small">${ago(m.last_seen_at)}</td>
      <td class="small">${(inv.tools || []).filter(t => ['claude', 'codex', 'gemini', 'hermes', 'gh', 'lark-cli', 'bytedcli', 'opencode'].includes(t.name)).map(t => `<span class="chip ${mine.some(x => x.name === t.name) ? 'ov' : ''}" title="${h(t.version)}">${h(t.name)} ${h(t.version || '?')}</span>`).join(' ')}</td></tr>`; }).join('') || '<tr><td class="muted">还没有机器，去「机器」页添加</td></tr>'}</table></div>
   </div>
   <div style="flex:1;min-width:320px">
    <h2>需要关注</h2>
    <div class="card tight">
    ${problems.length ? `<table>${problems.slice(0, 20).map(p => `<tr class="click" onclick="location.hash='${p.r.kind === 'skill' ? '#/skills/' + h(p.r.name) : p.r.kind === 'config' ? '#/configs/' + p.r.id : '#/env/' + p.r.id}'"><td class="mono small">${h(p.r.kind)}/${h(p.r.name)}</td><td class="small">${h(p.m.name)}</td><td><span class="pill ${p.st.cls}">${h(p.st.text)}</span> ${p.st.detail ? `<span class="muted small">${h(p.st.detail)}</span>` : ''}</td></tr>`).join('')}${problems.length > 20 ? `<tr><td class="muted small" colspan="3">…还有 ${problems.length - 20} 项</td></tr>` : ''}</table>` : ''}
    ${behind.length ? `<table style="margin-top:${problems.length ? 8 : 0}px">${behind.map(t => `<tr class="click" onclick="location.hash='#/machines/${t.machine.id}'"><td class="mono small">${h(t.name)}</td><td class="small">${h(t.machine.name)}</td><td class="mono small"><span class="behind">${h(t.version)}</span> → ${h(latest[t.package])}</td></tr>`).join('')}</table>` : ''}
    ${envDrift.length ? `<table style="margin-top:8px">${envDrift.slice(0, 10).map(d => `<tr class="click" onclick="location.hash='#/env'"><td class="mono small">${h(d.name)}</td><td class="small muted" colspan="2">${d.n} 种不同取值</td></tr>`).join('')}</table>` : ''}
    ${!problems.length && !behind.length && !envDrift.length ? '<p class="muted" style="margin:6px 0">一切正常。</p>' : ''}
    </div>
   </div>
  </div>`;
}
