import { app, api, h, fmtTime } from '../core.js';

export async function auditView() {
  const rows = await api('GET', '/api/admin/audit');
  app.innerHTML = `<h1>审计 <span class="muted">最近 ${rows.length} 条</span></h1>
  <div class="card tight"><table><tr><th>时间</th><th>谁</th><th>动作</th><th>对象</th><th>详情</th></tr>
  ${rows.map(a => `<tr><td class="mono small nowrap">${fmtTime(a.at)}</td><td class="small">${h(a.actor)}</td><td class="mono small ${a.action.includes('reveal') ? 'behind' : ''}">${h(a.action)}</td><td class="mono small">${h(a.target)}</td><td class="muted small">${h(a.detail)}</td></tr>`).join('') || '<tr><td class="muted" colspan="5">还没有记录</td></tr>'}</table></div>`;
}
