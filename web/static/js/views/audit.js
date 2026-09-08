import { app, api, h, fmtTime, pageHeader, emptyRow } from '../core.js';

export async function auditView() {
  const rows = await api('GET', '/api/admin/audit');
  app.innerHTML = pageHeader({ title: '审计', sub: `最近 ${rows.length} 条`, desc: '谁在什么时候发布、分发、显示了什么。<span class="behind">reveal</span> 表示有人查看了秘密值的明文。' }) + `
  <div class="card flush"><table><thead><tr><th>时间</th><th>谁</th><th>动作</th><th>对象</th><th>详情</th></tr></thead><tbody>
  ${rows.map(a => `<tr><td class="mono small nowrap muted">${fmtTime(a.at)}</td><td class="small">${h(a.actor)}</td><td><span class="pill ${a.action.includes('reveal') ? 'warn' : a.action.includes('delete') ? 'bad' : ''}">${h(a.action)}</span></td><td class="mono small">${h(a.target)}</td><td class="faint xs">${h(a.detail)}</td></tr>`).join('') || emptyRow(5, '还没有记录')}</tbody></table></div>`;
}
