// AgentDeck console entry: login + hash routing. Vanilla ES modules, no build step.
import { $, $$, app, api, h, token, setToken, setUnauthorizedHandler, fail, invalidate } from './core.js';
import { overviewView } from './views/overview.js';
import { machinesView, machineDetail } from './views/machines.js';
import { skillsView, skillDetail, skillEditor } from './views/skills.js';
import { configsView, configDetail } from './views/configs.js';
import { envView } from './views/env.js';
import { auditView } from './views/audit.js';

function showLogin() {
  document.body.classList.add('noauth');
  app.innerHTML = `<div class="card login"><h1>登录 AgentDeck</h1>
    <p class="muted">输入服务端 data/admin_token 里的管理 token。</p>
    <input id="tok" type="password" placeholder="admin token" style="width:100%">
    <div class="row" style="margin-top:12px"><button id="go">进入</button></div></div>`;
  $('#go').onclick = async () => {
    setToken($('#tok').value.trim());
    try { await api('GET', '/api/admin/me'); document.body.classList.remove('noauth'); route(); } catch (e) { fail(new Error('token 不对')); }
  };
  $('#tok').onkeydown = e => { if (e.key === 'Enter') $('#go').click(); };
}
setUnauthorizedHandler(showLogin);
$('#logout').onclick = () => { setToken(''); showLogin(); };

const routes = {
  overview: () => overviewView(),
  machines: (id, sub) => id ? machineDetail(id, sub) : machinesView(),
  skills: (id, sub) => id === '_new' ? skillEditor(null) : id ? (sub === 'edit' ? skillEditor(id) : skillDetail(id)) : skillsView(),
  configs: (id, sub) => id && id !== '_' ? configDetail(id, sub) : configsView(sub),
  env: (id) => envView(id),
  audit: () => auditView(),
};

let seq = 0;
export async function route() {
  if (!token) return showLogin();
  const hash = location.hash || '#/overview';
  const [, tab, id, ...rest] = hash.split('/');
  $$('nav a').forEach(a => a.classList.toggle('active', a.dataset.tab === tab));
  const view = routes[tab] || routes.overview;
  const my = ++seq;
  try {
    invalidate();
    await view(decodeURIComponent(id || ''), rest.map(decodeURIComponent).join('/'));
  } catch (e) { if (my === seq) fail(e); }
}
window.addEventListener('hashchange', route);
window.reroute = route;
route();
