// 设备管理：起名字、查看历史 IP 与首次出现时间。

import { ago, datetime, esc } from '../format.js';

const API = '/api/v1/devices';
const REFRESH_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9"/><path d="M13.5 2.5v3h-3"/></svg>';

const state = {
  query: '',
  filter: 'all',
  sort: 'recent',
};

let ctx = null;
let data = null;
let error = null;
let loading = false;
let saving = false;

export const devicesPage = {
  mount(c) {
    ctx = c;
    ctx.actions.innerHTML =
      `<button class="btn primary" type="button" id="devRefresh">${REFRESH_ICON}<span>刷新</span></button>`;
    ctx.actions.querySelector('#devRefresh').addEventListener('click', load);
    ctx.content.addEventListener('click', onClick);
    ctx.content.addEventListener('input', onInput);
    ctx.content.addEventListener('change', onChange);
    document.getElementById('modal').addEventListener('submit', onSubmit);
    if (data) render();
    else ctx.content.innerHTML = '<div class="card empty">正在加载设备…</div>';
    load();
  },
  unmount() {
    ctx.content.removeEventListener('click', onClick);
    ctx.content.removeEventListener('input', onInput);
    ctx.content.removeEventListener('change', onChange);
    document.getElementById('modal').removeEventListener('submit', onSubmit);
    ctx = null;
  },
};

async function load() {
  if (loading) return;
  loading = true;
  const btn = ctx?.actions.querySelector('#devRefresh');
  btn?.classList.add('loading');
  btn?.setAttribute('disabled', '');
  try {
    const res = await fetch(API, { cache: 'no-cache', headers: { Accept: 'application/json' } });
    if (!res.ok) throw new Error(`服务器返回 ${res.status}`);
    data = await res.json();
    error = null;
  } catch (e) {
    error = e;
  } finally {
    loading = false;
    btn?.classList.remove('loading');
    btn?.removeAttribute('disabled');
  }
  if (ctx) render();
}

function label(d) {
  return d.name || d.hostname || d.ip || d.mac;
}

function visible() {
  const q = state.query.trim().toLowerCase();
  let list = data.devices || [];
  if (state.filter === 'online') list = list.filter((d) => d.online);
  if (state.filter === 'offline') list = list.filter((d) => !d.online);
  if (q) {
    list = list.filter((d) =>
      [d.name, d.hostname, d.ip, d.mac, ...(d.ips || []).map((x) => x.ip)]
        .filter(Boolean)
        .some((s) => s.toLowerCase().includes(q)),
    );
  }
  const copy = list.slice();
  copy.sort((a, b) => {
    if (state.sort === 'name') return label(a).localeCompare(label(b), 'zh');
    if (state.sort === 'first') return (a.firstSeen || 0) - (b.firstSeen || 0);
    return (b.lastSeen || 0) - (a.lastSeen || 0) || Number(b.online) - Number(a.online);
  });
  return copy;
}

function render() {
  if (!ctx) return;
  if (!data) {
    ctx.content.innerHTML = `<div class="banner">加载失败：${esc(error?.message || '未知错误')}</div>`;
    return;
  }
  const all = data.devices || [];
  const online = all.filter((d) => d.online).length;
  ctx.setSub(`${all.length} 台设备${online ? ` · ${online} 台在线` : ''}`);
  if (!ctx.content.querySelector('[data-dev-list]')) {
    ctx.content.innerHTML = [
      `<div id="devBanner"></div>`,
      `<div class="card filters">
        <div class="filter-row"><span class="filter-label">查找</span>
          <input class="search" type="search" placeholder="名称、主机名、IP、MAC" value="${esc(state.query)}" data-q>
          <div class="chips">
            ${['all:全部', 'online:在线', 'offline:离线']
              .map((p) => {
                const [id, name] = p.split(':');
                return `<button type="button" class="chip ${state.filter === id ? 'active' : ''}" data-filter="${id}">${name}</button>`;
              })
              .join('')}
          </div>
          <span class="spacer"></span>
          <select class="select" data-sort>
            <option value="recent">按最近出现</option>
            <option value="first">按首次出现</option>
            <option value="name">按名称</option>
          </select>
        </div>
      </div>`,
      `<div data-dev-list></div>`,
    ].join('');
    ctx.content.querySelector('[data-sort]').value = state.sort;
  }
  const banner = ctx.content.querySelector('#devBanner');
  if (banner) banner.innerHTML = error ? `<div class="banner">刷新失败：${esc(error.message)}，下面显示的是上一次的数据</div>` : '';
  ctx.content.querySelectorAll('[data-filter]').forEach((el) => el.classList.toggle('active', el.dataset.filter === state.filter));
  const sort = ctx.content.querySelector('[data-sort]');
  if (sort && sort.value !== state.sort) sort.value = state.sort;
  const list = visible();
  const box = ctx.content.querySelector('[data-dev-list]');
  box.innerHTML = list.length
    ? `<div class="card section">
        <div class="table-wrap hide-sm-block"><table class="tbl">
          <thead><tr><th>设备</th><th>状态</th><th>当前 IP</th><th class="hide-sm">主机名</th><th>首次出现</th><th>最近出现</th></tr></thead>
          <tbody>${list.map(rowHTML).join('')}</tbody>
        </table></div>
        <div class="dev-cards">${list.map(cardHTML).join('')}</div>
      </div>`
    : `<div class="card empty">${all.length ? '没有符合条件的设备' : '还没有发现设备。接入局域网后会出现在这里。'}</div>`;
}

function rowHTML(d) {
  return `<tr class="clickable" data-open="${esc(d.id)}">
    <td><div class="dev-cell"><span class="dot ${d.online ? 'online' : 'offline'}"></span><div>
      <span class="app-name">${esc(label(d))}</span>
      <div class="sub muted" style="font-size:12px">${esc(d.mac)}</div></div></div></td>
    <td>${d.online ? '<span class="badge">在线</span>' : '<span class="muted">离线</span>'}</td>
    <td class="num">${esc(d.ip || '—')}</td>
    <td class="hide-sm">${esc(d.hostname || '—')}</td>
    <td class="num">${esc(datetime(d.firstSeen, data.tzOffset, data.generatedAt))}</td>
    <td class="num">${d.lastSeen ? esc(ago(d.lastSeen, data.generatedAt)) : '—'}</td>
  </tr>`;
}

function cardHTML(d) {
  return `<button type="button" class="card dev-card" data-open="${esc(d.id)}">
    <div class="dc-head">
      <span class="dot ${d.online ? 'online' : 'offline'}"></span>
      <div style="min-width:0"><div class="dc-name">${esc(label(d))}</div>
        <div class="dc-meta">${[d.ip, d.hostname || d.mac].filter(Boolean).map(esc).join(' · ')}</div></div>
      ${d.online ? '<span class="badge">在线</span>' : ''}
    </div>
    <div class="dc-meta" style="margin-top:8px">首次出现 ${esc(datetime(d.firstSeen, data.tzOffset, data.generatedAt))}
      · ${d.lastSeen ? esc(ago(d.lastSeen, data.generatedAt)) : '尚无活动'}</div>
  </button>`;
}

function openDevice(id) {
  const d = (data.devices || []).find((x) => x.id === id);
  if (!d) return;
  const lastSeenText = d.lastSeen
    ? `${esc(datetime(d.lastSeen, data.tzOffset, data.generatedAt))}（${esc(ago(d.lastSeen, data.generatedAt))}）`
    : '—';
  const ips = (d.ips || []).slice();
  const ipHTML = ips.length
    ? `<div class="table-wrap"><table class="tbl"><thead><tr><th>地址</th><th>首次使用</th><th>最近使用</th></tr></thead>
        <tbody>${ips
          .map(
            (ip) => `<tr>
              <td class="num">${esc(ip.ip)}${ip.current ? ' <span class="badge">当前</span>' : ''}</td>
              <td class="num">${esc(datetime(ip.firstSeen, data.tzOffset, data.generatedAt))}</td>
              <td class="num">${esc(datetime(ip.lastSeen, data.tzOffset, data.generatedAt))}</td>
            </tr>`,
          )
          .join('')}</tbody></table></div>`
    : '<div class="empty">还没有记录到 IP 地址</div>';
  const title = `<span class="dot ${d.online ? 'online' : 'offline'}" style="margin-right:8px;vertical-align:middle"></span>${esc(label(d))}
    <span class="sub">${esc(d.mac)}</span>`;
  ctx.openModal(
    title,
    `<form class="name-form" data-name-form="${esc(d.id)}">
      <label>
        <span>设备名称</span>
        <input class="search name-input" name="name" maxlength="40" value="${esc(d.name || '')}" placeholder="${esc(d.hostname || '给这台设备起个名字')}" autocomplete="off" data-name-input>
      </label>
      <button class="btn primary" type="submit">保存</button>
    </form>
    <p class="muted" style="margin:8px 0 0;font-size:12px">留空则显示 DHCP 主机名。每日概览里也会用这个名字。</p>
    <div class="detail-block" style="margin-top:20px"><h3>基本信息</h3>
      <dl class="kv">
        <dt>MAC</dt><dd>${esc(d.mac)}</dd>
        <dt>主机名</dt><dd>${esc(d.hostname || '（未上报）')}</dd>
        <dt>当前 IP</dt><dd>${esc(d.ip || '—')}</dd>
        <dt>状态</dt><dd>${d.online ? `在线${d.flows ? `，${d.flows} 个连接` : ''}` : '离线'}</dd>
        <dt>首次出现</dt><dd>${esc(datetime(d.firstSeen, data.tzOffset, data.generatedAt))}</dd>
        <dt>最近出现</dt><dd>${lastSeenText}</dd>
      </dl>
    </div>
    <div class="detail-block"><h3>历史 IP</h3>${ipHTML}</div>`,
  );
  document.querySelector('[data-name-input]')?.focus();
}

async function saveName(id, name) {
  if (saving) return;
  saving = true;
  const form = document.querySelector('[data-name-form]');
  const btn = form?.querySelector('button[type="submit"]');
  btn?.setAttribute('disabled', '');
  try {
    const res = await fetch(`${API}/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    });
    if (!res.ok) throw new Error(res.status === 400 ? '名称无效或过长' : `服务器返回 ${res.status}`);
    const d = (data.devices || []).find((x) => x.id === id);
    if (d) d.name = name.trim();
    ctx.closeModal();
    render();
  } catch (e) {
    if (form) {
      let err = form.parentElement?.querySelector('.form-err');
      if (!err) {
        err = document.createElement('div');
        err.className = 'form-err';
        form.after(err);
      }
      err.textContent = e.message || '保存失败';
    }
  } finally {
    saving = false;
    btn?.removeAttribute('disabled');
  }
}

function onClick(e) {
  const filter = e.target.closest('[data-filter]');
  if (filter) {
    state.filter = filter.dataset.filter;
    return render();
  }
  const open = e.target.closest('[data-open]');
  if (open) openDevice(open.dataset.open);
}

function onInput(e) {
  if (e.target.matches('[data-q]')) {
    state.query = e.target.value;
    render();
  }
}

function onChange(e) {
  if (e.target.matches('[data-sort]')) {
    state.sort = e.target.value;
    render();
  }
}

function onSubmit(e) {
  const form = e.target.closest('[data-name-form]');
  if (!form) return;
  e.preventDefault();
  saveName(form.dataset.nameForm, new FormData(form).get('name') || '');
}
