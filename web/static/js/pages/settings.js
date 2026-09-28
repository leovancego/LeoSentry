// 系统设置：类别名单、日历、采集与检查、端口与证书、登录密码、重启。

import { esc } from '../format.js';

const POL = '/api/v1/policies';
const SET = '/api/v1/settings';
const MINUS =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 8h10"/></svg>';
const WEEK = ['', '周一', '周二', '周三', '周四', '周五', '周六', '周日'];
const DOW = ['一', '二', '三', '四', '五', '六', '日'];

const TABS = [
  ['rules', '类别名单'],
  ['calendar', '日历'],
  ['timing', '采集与检查'],
  ['tls', '端口与证书'],
  ['password', '登录密码'],
  ['restart', '重启'],
];

let ctx = null;
let settings = null;
let policy = null;
let tab = 'rules';
let cat = 'game';
let query = '';
let month = 0;
let selected = '';
let error = '';
let notice = '';

export const settingsPage = {
  mount(c) {
    ctx = c;
    ctx.setSub('');
    ctx.actions.innerHTML = '';
    ctx.content.addEventListener('click', onClick);
    ctx.content.addEventListener('change', onChange);
    ctx.content.addEventListener('input', onInput);
    ctx.content.addEventListener('submit', onSubmit);
    ctx.content.innerHTML = '<div class="card empty">正在加载设置…</div>';
    load();
  },
  unmount() {
    if (!ctx) return;
    ctx.content.removeEventListener('click', onClick);
    ctx.content.removeEventListener('change', onChange);
    ctx.content.removeEventListener('input', onInput);
    ctx.content.removeEventListener('submit', onSubmit);
    ctx = null;
  },
};

async function load() {
  try {
    const [setRes, polRes] = await Promise.all([
      fetch(SET, { headers: { Accept: 'application/json' } }),
      fetch(POL, { headers: { Accept: 'application/json' } }),
    ]);
    if (setRes.status === 401 || polRes.status === 401) {
      window.dispatchEvent(new Event('leosentry-unauthorized'));
      return;
    }
    settings = await setRes.json();
    policy = await polRes.json();
    if (!setRes.ok) throw new Error(settings.error || '设置加载失败');
    if (!polRes.ok) throw new Error(policy.error || '日历加载失败');
    const cal = policy.calendar || {};
    if (!month) month = Number((policy.civilToday || '2026-01-01').slice(5, 7));
    if (!selected) selected = policy.civilToday || '';
    if (!settings.categories?.some((c) => c.id === cat)) cat = settings.categories?.[0]?.id || 'game';
    error = '';
    render();
  } catch (err) {
    error = err.message;
    if (ctx) ctx.content.innerHTML = `<div class="banner">${esc(error)}</div>`;
  }
}

function render() {
  if (!ctx || !settings) return;
  const active = document.activeElement;
  const focusId = active && ctx.content.contains(active) ? active.id : '';
  const selStart = active && 'selectionStart' in active ? active.selectionStart : null;
  const panels = {
    rules: rulesHTML,
    calendar: () => calendarHTML(policy?.calendar || {}),
    timing: timingHTML,
    tls: tlsHTML,
    password: passwordHTML,
    restart: restartHTML,
  };
  ctx.content.innerHTML = `
    <div class="settings-tabs" role="tablist">
      ${TABS.map(([id, name]) => `<button type="button" role="tab" class="${tab === id ? 'active' : ''}" data-tab="${id}" aria-selected="${tab === id}">${name}</button>`).join('')}
    </div>
    ${error ? `<div class="banner">${esc(error)}</div>` : ''}
    ${(panels[tab] || panels.password)()}`;
  if (focusId) {
    const el = document.getElementById(focusId);
    if (el) {
      el.focus();
      if (selStart != null && 'setSelectionRange' in el) {
        try { el.setSelectionRange(selStart, selStart); } catch { /* 时间框不支持 */ }
      }
    }
  }
}

function passwordHTML() {
  return `<section class="zone">
    <div class="zone-title"><h2>登录密码</h2></div>
    <form id="pwForm" class="settings-form">
      <p class="zone-note">首次使用的密码是 123456。改完之后用新密码登录。</p>
      <label>当前密码<input class="search" type="password" name="old" autocomplete="current-password" required></label>
      <label>新密码<input class="search" type="password" name="next" minlength="4" maxlength="64" autocomplete="new-password" required></label>
      <label>再输入一次<input class="search" type="password" name="again" minlength="4" maxlength="64" autocomplete="new-password" required></label>
      <div class="settings-actions">
        <button class="btn primary" type="submit">保存密码</button>
        <button class="btn" type="button" id="logoutBtn">退出登录</button>
      </div>
      <div class="form-err" id="pwErr"></div>
    </form>
  </section>`;
}

function tlsHTML() {
  const t = settings;
  const savedCert = t.tlsCert || '';
  const savedKey = t.tlsKey || '';
  const runningCert = t.tlsRunningCert || '';
  const runningKey = t.tlsRunningKey || '';
  const pending = !t.tlsError && (
    savedCert !== runningCert || savedKey !== runningKey ||
    t.httpPort !== t.httpRunningPort || t.httpsPort !== t.httpsRunningPort
  );
  let status = `<p class="zone-note">当前 HTTP 端口 ${esc(t.httpRunningPort)}。${t.tlsActive ? `已启用 HTTPS，端口 ${esc(t.httpsRunningPort)}。` : '当前没有启用 HTTPS。'}</p>`;
  if (t.tlsError) status = `<div class="banner">${esc(t.tlsError)}</div>`;
  else if (pending) status = '<p class="zone-note">已保存的端口或证书要重新启动后才生效。</p>';
  return `<section class="zone">
    <div class="zone-title"><h2>端口与证书</h2></div>
    <form id="tlsForm" class="settings-form">
      <p class="zone-note">端口填 0 到 65535 的整数。0 表示关闭这一路，两个不能同时为 0，也不能相同。HTTP 默认 8088，HTTPS 默认 8443。</p>
      <div class="time-grid">
        <label>HTTP 端口<input class="search num-input" name="httpPort" inputmode="numeric" value="${esc(t.httpPort)}" autocomplete="off" spellcheck="false"></label>
        <label>HTTPS 端口<input class="search num-input" name="httpsPort" inputmode="numeric" value="${esc(t.httpsPort)}" autocomplete="off" spellcheck="false"></label>
      </div>
      <p class="zone-note">证书和私钥填绝对路径，留空表示不启用 HTTPS。服务启动时检查证书，通过后才按 HTTPS 端口监听。</p>
      ${status}
      <label>证书<input class="search wide" name="cert" value="${esc(savedCert)}" spellcheck="false" autocomplete="off"></label>
      <label>私钥<input class="search wide" name="key" value="${esc(savedKey)}" spellcheck="false" autocomplete="off"></label>
      <div class="settings-actions"><button class="btn primary" type="submit">保存</button></div>
      <p class="muted" id="tlsMsg"></p>
      <div class="form-err" id="tlsErr"></div>
    </form>
  </section>`;
}

function timingHTML() {
  const t = settings;
  return `<section class="zone">
    <div class="zone-title"><h2>采集与检查</h2></div>
    <form id="timeForm" class="settings-form">
      <p class="zone-note">单位：秒。采集间隔和策略检查保存后立刻生效。</p>
      <div class="time-grid">
        <label>网络行为采集<input class="search num-input" id="collectSec" name="collect" type="number" min="1" max="3600" step="1" required value="${esc(t.collectSeconds)}"></label>
        <label>自动检查策略<input class="search num-input" id="policySec" name="policy" type="number" min="1" max="3600" step="1" required value="${esc(t.policySeconds)}"></label>
        <label>统计日切换<input class="search" id="rotateAt" name="rotate" type="time" required value="${esc(t.rotateAt || '03:00')}"></label>
      </div>
      <p class="zone-note">统计日切换保存后，要到「重启」里重新启动服务才生效。${t.rotatePending ? '当前还在用原来的切换时刻。' : ''}</p>
      <div class="settings-actions"><button class="btn primary" type="submit">保存</button></div>
      <div class="form-err" id="timeErr"></div>
    </form>
  </section>`;
}

function rulesHTML() {
  const groups = settings.categories || [];
  const group = groups.find((c) => c.id === cat) || groups[0];
  const q = query.trim().toLowerCase();
  const entries = (group?.entries || []).filter((e) => !q || e.value.toLowerCase().includes(q) || (e.name || '').toLowerCase().includes(q));
  return `<section class="zone">
    <div class="zone-title"><h2>类别名单</h2><span class="hint">域名包含其子域名，带 * 的是通配，IPv4 按地址匹配</span>
      <span class="spacer"></span>
      <a class="btn small" href="${SET}/categories/export">导出 JSON</a>
      <label class="btn small" style="cursor:pointer">导入 JSON<input type="file" accept="application/json,.json" hidden data-import-rules></label>
    </div>
    <p class="zone-note">导入会检查文件格式、域名和 IPv4，然后用文件里的名单一次换掉全部类别。文件里要带上每一个类别。</p>
    ${notice ? `<p class="muted">${esc(notice)}</p>` : ''}
    <div class="chips" style="margin-bottom:10px">
      ${groups.map((c) => `<button type="button" class="chip ${c.id === group?.id ? 'active' : ''}" data-cat="${esc(c.id)}">${esc(c.name)}</button>`).join('')}
    </div>
    <form id="addRule" class="settings-form add-rule">
      <label>网站或 IP<input class="search" id="ruleValue" name="value" placeholder="example.com、*.example.com 或 1.2.3.4" required></label>
      <button class="btn primary" type="submit">添加</button>
      <div class="form-err" id="ruleErr"></div>
    </form>
    <input class="search" id="ruleQuery" placeholder="在这一类里筛选" value="${esc(query)}" style="margin:8px 0;width:min(280px,100%)">
    <div class="entry-list">
      ${entries.length ? entries.map((e) => `<div class="entry-row">
          <span class="entry-name">${esc(e.name && e.name !== e.value ? e.name : '')}</span>
          <code class="entry-value">${esc(e.value)}</code>
          <button class="icon-btn danger-icon" type="button" data-del-rule="${esc(e.value)}" aria-label="删除">${MINUS}</button>
        </div>`).join('') : '<div class="muted">这一类还没有名单。</div>'}
    </div>
  </section>`;
}

function restartHTML() {
  const pending = settings.rotatePending
    ? '<div class="banner">统计日切换已经保存，重新启动后才会按新时刻计算。</div>'
    : '';
  return `<section class="zone">
    <div class="zone-title"><h2>重启服务</h2></div>
    ${pending}
    <p class="zone-note">类别名单、日历、采集间隔、策略检查和登录密码保存后马上生效。统计日切换要等重新启动。端口和 HTTPS 证书若和当前加载的不一致，在「端口与证书」页确认后会自动重启。</p>
    <p class="zone-note">重启会先把没采集完的流量补写进库，再按平时的方式把服务启动起来。中间几秒打不开这个页面。</p>
    <div class="settings-actions">
      <button class="btn danger" type="button" id="restartBtn">重新启动</button>
    </div>
    <p class="muted" id="restartMsg"></p>
    <div class="form-err" id="restartErr"></div>
  </section>`;
}

function calendarHTML(cal) {
  const year = cal.year || 2026;
  const days = (cal.days || []).filter((d) => Number(d.date.slice(5, 7)) === month);
  const first = days[0];
  const pad = first ? first.weekday - 1 : 0;
  const cells = `${Array.from({ length: pad }, () => '<div></div>').join('')}${days.map(cellHTML).join('')}`;
  const picked = (cal.days || []).find((d) => d.date === selected);
  const summer = cal.summer || {};
  const winter = cal.winter || {};
  return `<section class="zone">
    <div class="zone-title"><h2>${year} 年日历</h2>
      <span class="hint">${cal.loaded ? `${esc(cal.document || cal.source || '')} · 放假 ${cal.offDays} 天 · 调休 ${cal.makeupDays} 天` : '尚未导入国务院数据'}</span>
      <span class="spacer"></span>
      <a class="btn small" href="${POL}/calendar/template${cal.year ? `?year=${cal.year}` : ''}">下载模板</a>
      <label class="btn small" style="cursor:pointer">导入 JSON<input type="file" accept="application/json,.json" hidden data-import></label>
    </div>
    ${cal.loaded === false ? '<div class="banner">还没有导入国务院日历。未导入时，工作日按周一至周五计算，不含调休。</div>' : ''}
    <div class="chips" style="margin-bottom:10px">
      ${Array.from({ length: 12 }, (_, i) => `<button type="button" class="chip ${month === i + 1 ? 'active' : ''}" data-month="${i + 1}">${i + 1}月</button>`).join('')}
    </div>
    <div class="cal-grid">${DOW.map((d) => `<div class="cal-dow">${d}</div>`).join('')}${cells}</div>
    <p class="effect">${picked ? esc(dayLine(picked)) : '点一天查看'}</p>
    <div class="legend" style="margin-bottom:14px">
      <span><i class="sw" style="background:var(--warn-soft)"></i>休息日</span>
      <span><i class="sw" style="background:var(--accent-soft)"></i>调休上班</span>
      <span><i class="sw" style="background:#12b5a5"></i>暑假</span>
      <span><i class="sw" style="background:var(--accent)"></i>寒假</span>
    </div>
    <div class="vac-grid">
      <label>暑假<input class="search" type="date" data-vac="summer-start" value="${esc(summer.start || '')}"></label>
      <label>至<input class="search" type="date" data-vac="summer-end" value="${esc(summer.end || '')}"></label>
      <label>寒假<input class="search" type="date" data-vac="winter-start" value="${esc(winter.start || '')}"></label>
      <label>至<input class="search" type="date" data-vac="winter-end" value="${esc(winter.end || '')}"></label>
    </div>
    <div class="settings-actions"><button class="btn primary" type="button" data-save-vac>保存寒暑假</button>
      <span class="hint">留空表示不使用。已有策略引用时不能清空。</span></div>
  </section>`;
}

function cellHTML(d) {
  const cls = [
    'cal-cell',
    d.kind === 'makeup' ? 'makeup' : !d.workday ? 'rest' : '',
    d.date === policy?.civilToday ? 'today' : '',
    d.date === selected ? 'on' : '',
    d.summer ? 'summer' : '',
    d.winter ? 'winter' : '',
  ].filter(Boolean).join(' ');
  return `<button type="button" class="${cls}" data-day="${d.date}"><div class="n">${Number(d.date.slice(8))}</div><div class="nm">${esc(d.name || '')}</div></button>`;
}

function dayLine(d) {
  const bits = [d.date, WEEK[d.weekday] || '', d.name, d.workday ? '工作日' : '休息日'];
  if (d.summer) bits.push('暑假');
  if (d.winter) bits.push('寒假');
  return bits.filter(Boolean).join(' · ');
}

function onClick(e) {
  notice = '';
  const next = e.target.closest('[data-tab]');
  if (next) {
    tab = next.dataset.tab;
    error = '';
    render();
    return;
  }
  if (e.target.closest('#logoutBtn')) {
    logout();
    return;
  }
  if (e.target.closest('#restartBtn')) {
    askRestart();
    return;
  }
  const pick = e.target.closest('[data-cat]');
  if (pick) {
    cat = pick.dataset.cat;
    query = '';
    render();
    return;
  }
  const del = e.target.closest('[data-del-rule]');
  if (del) {
    removeRule(del.dataset.delRule);
    return;
  }
  const mon = e.target.closest('[data-month]');
  if (mon) {
    month = Number(mon.dataset.month);
    render();
    return;
  }
  const day = e.target.closest('[data-day]');
  if (day) {
    selected = day.dataset.day;
    render();
    return;
  }
  if (e.target.closest('[data-save-vac]')) saveVacations();
}

function onInput(e) {
  if (e.target.id !== 'ruleQuery') return;
  query = e.target.value;
  render();
}

function onChange(e) {
  const rules = e.target.closest('[data-import-rules]');
  if (rules) {
    const file = rules.files?.[0];
    rules.value = '';
    if (file) importRules(file);
    return;
  }
  const input = e.target.closest('[data-import]');
  const file = input?.files?.[0];
  if (!file) return;
  input.value = '';
  file.text().then((text) => post(POL + '/calendar', text, 'text')).catch((err) => {
    error = err.message;
    render();
  });
}

function onSubmit(e) {
  if (e.target.id === 'pwForm') {
    e.preventDefault();
    savePassword(e.target);
  } else if (e.target.id === 'timeForm') {
    e.preventDefault();
    saveTiming(e.target);
  } else if (e.target.id === 'tlsForm') {
    e.preventDefault();
    saveTLS(e.target);
  } else if (e.target.id === 'addRule') {
    e.preventDefault();
    addRule(e.target);
  }
}

async function savePassword(form) {
  const fd = new FormData(form);
  const err = document.getElementById('pwErr');
  const next = String(fd.get('next') || '');
  if (next !== String(fd.get('again') || '')) {
    err.textContent = '两次输入的新密码不一致';
    return;
  }
  err.textContent = '';
  const res = await fetch('/api/v1/settings/password', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ old: fd.get('old'), new: next }),
  });
  const body = await res.json().catch(() => ({}));
  if (res.status === 401) {
    window.dispatchEvent(new Event('leosentry-unauthorized'));
    return;
  }
  if (!res.ok) {
    err.textContent = body.error || '保存失败';
    return;
  }
  form.reset();
  err.insertAdjacentHTML('beforebegin', '<p class="muted">密码已更新</p>');
}

function parsePort(raw) {
  const s = String(raw ?? '').trim();
  if (!/^[0-9]{1,5}$/.test(s)) return null;
  const n = Number(s);
  if (n > 65535) return null;
  return n;
}

async function saveTLS(form) {
  const fd = new FormData(form);
  const cert = String(fd.get('cert') || '').trim();
  const key = String(fd.get('key') || '').trim();
  const httpPort = parsePort(fd.get('httpPort'));
  const httpsPort = parsePort(fd.get('httpsPort'));
  const err = document.getElementById('tlsErr');
  const msg = document.getElementById('tlsMsg');
  if (err) err.textContent = '';
  if (msg) msg.textContent = '';
  if (httpPort == null || httpsPort == null) {
    if (err) err.textContent = '端口要是 0 到 65535 的整数';
    return;
  }
  if (httpPort === 0 && httpsPort === 0) {
    if (err) err.textContent = 'HTTP 和 HTTPS 不能同时关闭';
    return;
  }
  if (httpPort > 0 && httpPort === httpsPort) {
    if (err) err.textContent = 'HTTP 和 HTTPS 端口不能相同';
    return;
  }
  if ((cert === '') !== (key === '')) {
    if (err) err.textContent = '证书和私钥要一起填写，或一起留空';
    return;
  }
  const savedCert = settings.tlsCert || '';
  const savedKey = settings.tlsKey || '';
  const runningCert = settings.tlsRunningCert || '';
  const runningKey = settings.tlsRunningKey || '';
  const needRestart = cert !== runningCert || key !== runningKey || httpPort !== settings.httpRunningPort || httpsPort !== settings.httpsRunningPort;
  const unchanged = cert === savedCert && key === savedKey && httpPort === settings.httpPort && httpsPort === settings.httpsPort;
  if (unchanged && !needRestart) {
    if (msg) msg.textContent = '没有改动';
    return;
  }
  if (needRestart) {
    const parts = [`HTTP 端口 ${httpPort}，HTTPS 端口 ${httpsPort}`];
    if (cert === '' && key === '') parts.push('未填写证书，不会启用 HTTPS');
    else parts.push('启动时会检查证书，通过后才监听 HTTPS');
    if (!confirm(`${parts.join('。')}。确定后会保存并重新启动服务，页面会断开几秒。请用新端口再打开。`)) return;
  }
  try {
    const data = await send(SET + '/tls', 'PUT', { tlsCert: cert, tlsKey: key, httpPort, httpsPort });
    settings = data;
    error = '';
    if (data.restart) {
      await startRestart();
      return;
    }
    render();
    const ok = document.getElementById('tlsMsg');
    if (ok) ok.textContent = '已保存';
  } catch (ex) {
    if (err) err.textContent = ex.message;
  }
}

async function saveTiming(form) {
  const fd = new FormData(form);
  const err = document.getElementById('timeErr');
  err.textContent = '';
  try {
    settings = await send(SET + '/timing', 'PUT', {
      collectSeconds: Number(fd.get('collect')),
      policySeconds: Number(fd.get('policy')),
      rotateAt: fd.get('rotate'),
    });
    error = '';
    render();
  } catch (ex) {
    err.textContent = ex.message;
  }
}

async function addRule(form) {
  const err = document.getElementById('ruleErr');
  err.textContent = '';
  try {
    settings = await send(SET + '/categories', 'POST', { category: cat, value: new FormData(form).get('value') });
    form.reset();
    error = '';
    render();
  } catch (ex) {
    err.textContent = ex.message;
  }
}

async function importRules(file) {
  if (!confirm('导入会替换当前全部类别的名单，确定继续？')) return;
  notice = '';
  try {
    const text = await file.text();
    settings = await send(SET + '/categories/import', 'POST', text, 'raw-json');
    query = '';
    notice = '名单已按文件替换';
    error = '';
    render();
  } catch (ex) {
    error = ex.message;
    render();
  }
}

async function removeRule(value) {
  try {
    settings = await send(SET + '/categories', 'DELETE', { category: cat, value });
    error = '';
    render();
  } catch (ex) {
    error = ex.message;
    render();
  }
}

async function saveVacations() {
  const val = (name) => ctx.content.querySelector(`[data-vac="${name}"]`)?.value || '';
  try {
    policy = await send(POL + '/vacations', 'PUT', {
      summer: { start: val('summer-start'), end: val('summer-end') },
      winter: { start: val('winter-start'), end: val('winter-end') },
    });
    error = '';
    render();
  } catch (ex) {
    error = ex.message;
    render();
  }
}

async function post(url, body, kind) {
  if (kind === 'text') {
    policy = await send(url, 'POST', body, 'text/plain');
  }
  error = '';
  render();
}

async function send(url, method, body, type = 'application/json') {
  const raw = type === 'text/plain' || type === 'raw-json';
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': type === 'raw-json' ? 'application/json' : type, Accept: 'application/json' },
    body: raw ? body : JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    window.dispatchEvent(new Event('leosentry-unauthorized'));
    throw new Error('需要登录');
  }
  if (!res.ok) throw new Error(data.error || '保存失败');
  return data;
}

async function askRestart() {
  if (!confirm('重新启动会先安全停止服务，再按平时的方式启动。页面会断开几秒，确定继续？')) return;
  await startRestart();
}

async function startRestart() {
  const err = document.getElementById('restartErr') || document.getElementById('tlsErr');
  const msg = document.getElementById('restartMsg') || document.getElementById('tlsMsg');
  const btn = document.getElementById('restartBtn') || document.querySelector('#tlsForm button[type="submit"]');
  if (err) err.textContent = '';
  if (msg) msg.textContent = '';
  if (btn) btn.disabled = true;
  try {
    const res = await fetch(SET + '/restart', { method: 'POST', headers: { Accept: 'application/json' } });
    const body = await res.json().catch(() => ({}));
    if (res.status === 401) {
      window.dispatchEvent(new Event('leosentry-unauthorized'));
      if (btn) btn.disabled = false;
      return;
    }
    if (!res.ok) {
      if (err) err.textContent = body.error || '重启失败';
      if (btn) btn.disabled = false;
      return;
    }
    if (msg) msg.textContent = '正在重新启动，请稍等再打开这个页面。';
  } catch {
    if (msg) msg.textContent = '正在重新启动，请稍等再打开这个页面。';
  }
}

async function logout() {
  await fetch('/api/v1/logout', { method: 'POST' });
  window.dispatchEvent(new Event('leosentry-unauthorized'));
}
