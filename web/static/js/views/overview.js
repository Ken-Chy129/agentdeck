import { app, h, ago, online, isOnline, overview, configs, npmLatest, semverLt, syncState, pageHeader, empty } from '../core.js';

const SHOW_TOOLS = ['claude', 'codex', 'gemini', 'hermes', 'gh', 'lark-cli', 'bytedcli', 'opencode'];

export async function overviewView() {
  const [ov, cfg] = await Promise.all([overview(), configs()]);
  const ms = ov.machines;
  const tools = ms.flatMap(m => (m.inventory?.tools || []).map(t => ({ ...t, machine: m })));
  const latest = await npmLatest(tools.map(t => t.package));
  const behind = tools.filter(t => t.package && latest[t.package] && semverLt(t.version, latest[t.package]));

  let pending = 0, failed = 0;
  const problems = [];
  for (const m of ms) for (const r of ov.resources) {
    const st = syncState(ov, m, r);
    if (!st) continue;
    if (st.cls === 'bad') { failed++; problems.push({ m, r, st }); }
    else if (st.cls === 'warn') { pending++; problems.push({ m, r, st }); }
  }
  const byKind = (k) => ov.resources.filter(r => r.kind === k).length;

  const envDrift = [];
  const byName = {};
  for (const c of cfg) for (const e of (c.snapshot?.exports || [])) if (e.kind !== 'append' && !/agentdeck/.test(e.file)) (byName[e.name] ||= {})[c.machine_id] = e.kind === 'secret' ? e.fingerprint : e.value;
  for (const [n, per] of Object.entries(byName)) { const vals = new Set(Object.values(per)); if (vals.size > 1) envDrift.push({ name: n, n: vals.size, machines: Object.keys(per).length }); }

  const recent = [...ms].sort((a, b) => (b.last_seen_at || '').localeCompare(a.last_seen_at || ''));
  const linkOf = (r) => r.kind === 'skill' ? `#/skills/${encodeURIComponent(r.name)}` : r.kind === 'config' ? `#/configs/${r.id}` : `#/env/${r.id}`;
  const onlineN = ms.filter(m => isOnline(m.last_seen_at)).length;

  app.innerHTML = pageHeader({ title: '总览', desc: `${ms.length} 台机器 · 每台每 15 分钟同步一次` }) + `
  <div class="stats">
    <div class="stat ${onlineN < ms.length ? 'warn' : ''}"><b>${onlineN}<small> / ${ms.length}</small></b><span>机器在线</span></div>
    <div class="stat"><b>${byKind('skill')}<small> · </small>${byKind('config')}<small> · </small>${byKind('env')}</b><span>Skill · 配置 · 环境变量</span></div>
    <div class="stat ${pending ? 'warn' : ''}"><b>${pending}</b><span>待同步</span></div>
    <div class="stat ${failed ? 'bad' : ''}"><b>${failed}</b><span>同步失败</span></div>
    <div class="stat ${behind.length ? 'warn' : ''}"><b>${behind.length}</b><span>CLI 可升级</span></div>
    <div class="stat ${envDrift.length ? 'warn' : ''}"><b>${envDrift.length}</b><span>变量各机不一致</span></div>
  </div>

  <div class="cols mt16">
   <div class="wide">
    <h2>机器</h2>
    <div class="card flush"><table><thead><tr><th>机器</th><th>系统</th><th>最近同步</th><th>CLI</th></tr></thead><tbody>
    ${recent.map(m => { const inv = m.inventory || {}; const mine = behind.filter(t => t.machine.id === m.id);
      return `<tr class="click" onclick="location.hash='#/machines/${m.id}'"><td><span class="dot ${online(m.last_seen_at)}"></span><b>${h(m.name)}</b></td><td class="muted small">${h(inv.os || m.os)}/${h(inv.arch || m.arch)}</td><td class="small muted">${ago(m.last_seen_at)}</td>
      <td><div class="row" style="gap:5px">${(inv.tools || []).filter(t => SHOW_TOOLS.includes(t.name)).map(t => `<span class="chip ${mine.some(x => x.name === t.name) ? 'ov' : ''}" title="${mine.some(x => x.name === t.name) ? '可升级到 ' + h(latest[t.package]) : h(t.source)}">${h(t.name)} <b>${h(t.version || '?')}</b></span>`).join('')}</div></td></tr>`; }).join('') || `<tr><td colspan="4">${empty('还没有机器，去「机器」页添加')}</td></tr>`}</tbody></table></div>
   </div>
   <div>
    <h2>需要关注</h2>
    <div class="card flush">
    ${problems.length ? `<table><tbody>${problems.slice(0, 15).map(p => `<tr class="click" onclick="location.hash='${linkOf(p.r)}'"><td><span class="faint xs">${h(p.r.kind)}</span><br><span class="mono small">${h(p.r.name)}</span></td><td class="small">${h(p.m.name)}</td><td class="right"><span class="pill ${p.st.cls}" title="${h(p.st.detail || '')}">${h(p.st.text)}</span></td></tr>`).join('')}${problems.length > 15 ? `<tr><td class="muted small" colspan="3">…还有 ${problems.length - 15} 项</td></tr>` : ''}</tbody></table>` : ''}
    ${behind.length ? `<table><tbody>${behind.map(t => `<tr class="click" onclick="location.hash='#/machines/${t.machine.id}'"><td><span class="faint xs">cli</span><br><span class="mono small">${h(t.name)}</span></td><td class="small">${h(t.machine.name)}</td><td class="right mono small"><span class="behind">${h(t.version)}</span> → ${h(latest[t.package])}</td></tr>`).join('')}</tbody></table>` : ''}
    ${envDrift.length ? `<table><tbody>${envDrift.slice(0, 10).map(d => `<tr class="click" onclick="location.hash='#/env'"><td><span class="faint xs">env</span><br><span class="mono small">${h(d.name)}</span></td><td class="small muted">${d.machines} 台机器</td><td class="right"><span class="pill warn">${d.n} 种取值</span></td></tr>`).join('')}</tbody></table>` : ''}
    ${!problems.length && !behind.length && !envDrift.length ? empty('一切正常 ✓') : ''}
    </div>
   </div>
  </div>`;
}
