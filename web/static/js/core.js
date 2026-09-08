// Shared helpers: dom, api, toast, modal, formatting.
export const $ = (s, el = document) => el.querySelector(s);
export const $$ = (s, el = document) => [...el.querySelectorAll(s)];
export const h = (s) => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
export const app = $('#app');

const TOKEN_KEY = 'agentdeck_admin_token';
export let token = localStorage.getItem(TOKEN_KEY) || '';
export function setToken(t) { token = t; if (t) localStorage.setItem(TOKEN_KEY, t); else localStorage.removeItem(TOKEN_KEY); }

let onUnauthorized = () => {};
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

export async function api(method, path, body, raw) {
  const opt = { method, headers: { Authorization: 'Bearer ' + token } };
  if (body !== undefined) {
    if (raw) opt.body = body;
    else { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  }
  const r = await fetch(path, opt);
  if (r.status === 401) { onUnauthorized(); throw new Error('unauthorized'); }
  const ct = r.headers.get('content-type') || '';
  const data = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}

export function toast(msg, bad) {
  const d = document.createElement('div'); d.className = 't' + (bad ? ' bad' : ''); d.textContent = msg;
  $('#toast').appendChild(d); setTimeout(() => d.remove(), bad ? 6000 : 3000);
}
export const fail = (e) => { if (e?.message !== 'unauthorized') toast(e.message || String(e), true); };

// ---- modal ----
const modalEl = $('#modal'), modalBox = $('#modalBox');
export function modal(html) {
  modalBox.innerHTML = html; modalEl.hidden = false;
  const first = modalBox.querySelector('input,textarea,select'); first?.focus();
  return modalBox;
}
export function closeModal() { modalEl.hidden = true; modalBox.innerHTML = ''; }
modalEl.addEventListener('click', e => { if (e.target === modalEl) closeModal(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape' && !modalEl.hidden) closeModal(); });

// confirm dialog with custom text; returns Promise<boolean>
export function ask(title, body, okText = '确定', danger = false) {
  return new Promise(res => {
    const box = modal(`<h3>${h(title)}</h3><p class="muted">${body}</p>
      <div class="row" style="justify-content:flex-end;margin-top:16px"><button class="ghost" id="mcancel">取消</button><button class="${danger ? 'danger' : ''}" id="mok">${h(okText)}</button></div>`);
    $('#mcancel', box).onclick = () => { closeModal(); res(false); };
    $('#mok', box).onclick = () => { closeModal(); res(true); };
  });
}

// ---- formatting ----
export const ago = (iso) => {
  if (!iso) return '从未';
  const s = (Date.now() - new Date(iso)) / 1000;
  if (s < 60) return Math.floor(s) + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
};
export const online = (iso) => { if (!iso) return ''; const s = (Date.now() - new Date(iso)) / 1000; return s < 1800 ? 'on' : s < 86400 ? 'stale' : ''; };
export const isOnline = (iso) => online(iso) === 'on';
export const kb = (n) => n == null ? '' : n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(1) + ' MB';
export const semverLt = (a, b) => {
  if (!a || !b) return false;
  const pa = a.split(/[.-]/).map(x => parseInt(x, 10)), pb = b.split(/[.-]/).map(x => parseInt(x, 10));
  for (let i = 0; i < 3; i++) { const x = pa[i] || 0, y = pb[i] || 0; if (x !== y) return x < y; }
  return false;
};
export const shortDigest = (d) => (d || '').replace(/^sha256:/, '').slice(0, 12);
export const fmtTime = (iso) => iso ? new Date(iso).toLocaleString('zh-CN', { hour12: false }) : '';
export const trunc = (s, n = 48) => { s = String(s ?? ''); return s.length > n ? s.slice(0, n) + '…' : s; };

export function copy(text) { navigator.clipboard.writeText(text).then(() => toast('已复制')); }
export async function dl(href, fn) {
  const r = await fetch(href, { headers: { Authorization: 'Bearer ' + token } });
  const b = await r.blob(); const a = document.createElement('a'); a.href = URL.createObjectURL(b); a.download = fn; a.click();
}
window.copyText = copy;
window.dl = dl;

export const meta = (res) => (res && typeof res.meta === 'object' && res.meta) || {};

// ---- shared data loaders ----
let ovCache = null, ovAt = 0;
export async function overview(force) {
  if (!force && ovCache && Date.now() - ovAt < 5000) return ovCache;
  ovCache = await api('GET', '/api/admin/overview'); ovAt = Date.now();
  ovCache.byMachine = Object.fromEntries(ovCache.machines.map(m => [m.id, m]));
  ovCache.byResource = Object.fromEntries(ovCache.resources.map(r => [r.id, r]));
  ovCache.assignedTo = {}; // resource_id -> {machine_id: assignment}
  ovCache.assignedOn = {}; // machine_id -> {resource_id: assignment}
  for (const a of ovCache.assignments) { (ovCache.assignedTo[a.resource_id] ||= {})[a.machine_id] = a; (ovCache.assignedOn[a.machine_id] ||= {})[a.resource_id] = a; }
  ovCache.appliedMap = {}; // machine_id -> resource_id -> applied
  for (const a of ovCache.applied) (ovCache.appliedMap[a.machine_id] ||= {})[a.resource_id] = a;
  return ovCache;
}
export function invalidate() { ovCache = null; cfgCache = null; }

let cfgCache = null;
export async function configs(force) {
  if (!force && cfgCache) return cfgCache;
  cfgCache = await api('GET', '/api/admin/configs');
  return cfgCache;
}

export async function npmLatest(pkgs) {
  const list = [...new Set(pkgs.filter(Boolean))];
  if (!list.length) return {};
  try { return await api('GET', '/api/admin/npm-latest?pkgs=' + list.map(encodeURIComponent).join(',')); } catch { return {}; }
}

// status of a resource on a machine: {cls, text}
export function syncState(ov, m, res) {
  const a = ov.assignedOn[m.id]?.[res.id];
  if (!a) return null;
  const ap = ov.appliedMap[m.id]?.[res.id];
  if (ap?.status === 'failed') return { cls: 'bad', text: '失败', detail: ap.detail, a, ap };
  if (!ap) return { cls: 'warn', text: '待同步', a, ap };
  const ok = a.has_override ? ap.status !== 'failed' : ap.digest === res.current_digest;
  return ok ? { cls: 'ok', text: '已同步', a, ap } : { cls: 'warn', text: '待更新', a, ap };
}

export const pill = (st) => st ? `<span class="pill ${st.cls}" title="${h(st.detail || '')}">${h(st.text)}</span>` : '';
export const upgradeCmd = (t) => t.source === 'npm-global' && t.package ? `npm i -g ${t.package}@latest` : t.source === 'brew' ? `brew upgrade ${t.name}` : t.source === 'native' && t.name === 'claude' ? 'claude update' : '';

// ---- page scaffolding ----
export const crumb = (href, label) => `<a class="crumb" href="${href}">← ${h(label)}</a>`;
export function pageHeader({ title, sub, desc, actions = '', mono = false }) {
  return `<div class="page-h"><div><h1 class="${mono ? 'mono' : ''}">${title}${sub ? ` <span class="sub">${sub}</span>` : ''}</h1>${desc ? `<p class="desc">${desc}</p>` : ''}</div>${actions ? `<div class="actions">${actions}</div>` : ''}</div>`;
}
export const empty = (html) => `<div class="empty">${html}</div>`;
export const emptyRow = (cols, html) => `<tr><td colspan="${cols}"><div class="empty">${html}</div></td></tr>`;
export const statusPill = (s) => `<span class="pill ${s === 'done' ? 'ok' : s === 'failed' ? 'bad' : s === 'queued' ? 'warn' : ''}">${h(s)}</span>`;
