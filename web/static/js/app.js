// 后台框架：侧边导航、hash 路由、弹窗。各页面实现 { mount(ctx), unmount?() }。

import { overviewPage } from './pages/overview.js';
import { devicesPage } from './pages/devices.js';
import { policiesPage } from './pages/policies.js';
import { settingsPage } from './pages/settings.js';

const ICONS = {
  home: '<path d="M3 9.5 10 4l7 5.5V16a1 1 0 0 1-1 1h-4v-5H8v5H4a1 1 0 0 1-1-1z"/>',
  devices: '<rect x="2.5" y="4" width="11" height="8" rx="1.5"/><path d="M5.5 15h5M8 12v3"/><rect x="14.5" y="7" width="3.5" height="8" rx="1"/>',
  shield: '<path d="M10 2.8 16 5v4.6c0 3.7-2.5 6.6-6 7.8-3.5-1.2-6-4.1-6-7.8V5z"/><path d="m7.3 10 1.9 1.9L13 8.2"/>',
  gear: '<circle cx="10" cy="10" r="2.6"/><path d="M10 2.5v2M10 15.5v2M2.5 10h2M15.5 10h2M4.7 4.7l1.4 1.4M13.9 13.9l1.4 1.4M4.7 15.3l1.4-1.4M13.9 6.1l1.4-1.4"/>',
};

const ROUTES = [
  { id: 'overview', title: '每日概览', icon: 'home', page: overviewPage },
  { id: 'policies', title: '管控策略', icon: 'shield', page: policiesPage },
  { id: 'devices', title: '设备管理', icon: 'devices', page: devicesPage },
  { id: 'system', title: '系统设置', icon: 'gear', page: settingsPage },
];

const $ = (id) => document.getElementById(id);
const ui = {
  nav: $('nav'),
  sidebar: $('sidebar'),
  scrim: $('scrim'),
  title: $('pageTitle'),
  sub: $('pageSub'),
  actions: $('pageActions'),
  content: $('content'),
  modal: $('modal'),
  modalTitle: $('modalTitle'),
  modalBody: $('modalBody'),
};

let current = null;
let modalClose = null;

const ctx = {
  content: ui.content,
  actions: ui.actions,
  setSub(text) {
    ui.sub.textContent = text;
  },
  // openModal 打开弹窗并返回内容容器；titleHTML / bodyHTML 由调用方负责转义。
  openModal(titleHTML, bodyHTML, onClose) {
    ui.modalTitle.innerHTML = titleHTML;
    ui.modalBody.innerHTML = bodyHTML;
    ui.modalBody.scrollTop = 0;
    ui.modal.hidden = false;
    document.body.style.overflow = 'hidden';
    modalClose = onClose || null;
    return ui.modalBody;
  },
  closeModal,
};

function closeModal() {
  if (ui.modal.hidden) return;
  ui.modal.hidden = true;
  ui.modalBody.innerHTML = '';
  document.body.style.overflow = '';
  const fn = modalClose;
  modalClose = null;
  fn?.();
}

function renderNav(active) {
  ui.nav.innerHTML = ROUTES.map(
    (r) => `<a href="#/${r.id}" class="${r.id === active ? 'active' : ''}">
      <svg width="20" height="20" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">${ICONS[r.icon]}</svg>
      <span>${r.title}</span>
    </a>`,
  ).join('');
}

function route() {
  const id = location.hash.replace(/^#\/?/, '') || 'overview';
  const r = ROUTES.find((x) => x.id === id) || ROUTES[0];
  closeModal();
  current?.unmount?.();
  renderNav(r.id);
  ui.title.textContent = r.title;
  ui.sub.textContent = '';
  ui.actions.innerHTML = '';
  ui.content.innerHTML = '';
  document.title = `${r.title} · LeoSentry`;
  current = r.page;
  current.mount(ctx);
  setMenu(false);
  window.scrollTo(0, 0);
}

function setMenu(open) {
  ui.sidebar.classList.toggle('open', open);
  ui.scrim.hidden = !open;
}

$('menuBtn').addEventListener('click', () => setMenu(!ui.sidebar.classList.contains('open')));
ui.scrim.addEventListener('click', () => setMenu(false));
ui.modal.addEventListener('click', (e) => {
  if (e.target.closest('[data-close]')) closeModal();
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeModal();
    setMenu(false);
  }
});
let sessionOK = false;

async function ensureSession() {
  const res = await fetch('/api/v1/session', { cache: 'no-cache' });
  if (res.status === 404) return true;
  return res.ok;
}

function showLogin() {
  sessionOK = false;
  current?.unmount?.();
  current = null;
  ui.title.textContent = '登录';
  ui.sub.textContent = '';
  ui.actions.innerHTML = '';
  ui.content.innerHTML = `<form class="card login-card" id="loginForm">
    <h2>LeoSentry</h2>
    <p class="muted">请输入登录密码</p>
    <label>密码<input class="search" style="width:100%" type="password" name="password" autocomplete="current-password" required></label>
    <button class="btn primary" type="submit">登录</button>
    <div class="form-err" id="loginErr"></div>
  </form>`;
  document.getElementById('loginForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const password = new FormData(e.target).get('password');
    const res = await fetch('/api/v1/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ password }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      document.getElementById('loginErr').textContent = body.error || '登录失败';
      return;
    }
    sessionOK = true;
    route();
  });
}

async function enter() {
  sessionOK = await ensureSession();
  if (sessionOK) route();
  else showLogin();
}

window.addEventListener('hashchange', () => {
  if (sessionOK) route();
  else showLogin();
});
window.addEventListener('leosentry-unauthorized', showLogin);
enter();
