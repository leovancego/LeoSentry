// 管控策略：按设备设置每日时长和时段控制，并可暂停或临时延长。

import { ago, clock, duration, esc } from '../format.js';

const API = '/api/v1/policies';
const PLUS =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M8 3v10M3 8h10"/></svg>';
const MINUS =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M3 8h10"/></svg>';
const MAX_MINUTES = 24 * 60;
const WEEK = ['', '周一', '周二', '周三', '周四', '周五', '周六', '周日'];
const KINDS = [
  ['all', '每天'],
  ['workday', '法定工作日'],
  ['restday', '法定休息日'],
  ['summer', '暑假'],
  ['winter', '寒假'],
  ['weekdays', '指定星期'],
];

const state = { filter: 'all' };
let ctx = null;
let page = null;
let error = null;
let draft = null;
let draftMAC = '';
let draftMode = 'device';
let draftID = '';
let busy = false;

export const policiesPage = {
  mount(c) {
    ctx = c;
    ctx.actions.innerHTML =
      `<button class="btn" type="button" id="polCheck">立即检查</button>`;
    ctx.actions.querySelector('#polCheck').addEventListener('click', () => run(() => post(API + '/check')));
    ctx.content.addEventListener('click', onContentClick);
    const modal = document.getElementById('modal');
    modal.addEventListener('click', onModalClick);
    modal.addEventListener('change', onModalChange);
    modal.addEventListener('input', onModalInput);
    modal.addEventListener('submit', onSubmit);
    if (page) render();
    else ctx.content.innerHTML = '<div class="card empty">正在加载策略…</div>';
    load();
  },
  unmount() {
    ctx.content.removeEventListener('click', onContentClick);
    const modal = document.getElementById('modal');
    modal.removeEventListener('click', onModalClick);
    modal.removeEventListener('change', onModalChange);
    modal.removeEventListener('input', onModalInput);
    modal.removeEventListener('submit', onSubmit);
    ctx = null;
  },
};

async function load() {
  await run(async () => {
    page = await api(API);
    error = null;
  });
}

async function run(fn) {
  if (busy || !ctx) return;
  busy = true;
  const btn = ctx.actions.querySelector('#polCheck');
  btn?.setAttribute('disabled', '');
  try {
    await fn();
  } catch (e) {
    error = e;
  } finally {
    busy = false;
    btn?.removeAttribute('disabled');
  }
  if (ctx) render();
}

async function post(url, body) {
  page = await api(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: body == null ? '{}' : typeof body === 'string' ? body : JSON.stringify(body),
  });
  error = null;
}

async function api(url, opt) {
  const res = await fetch(url, { cache: 'no-cache', headers: { Accept: 'application/json' }, ...opt });
  const text = await res.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = null;
  }
  if (res.status === 401) {
    window.dispatchEvent(new Event('leosentry-unauthorized'));
    throw new Error('需要登录');
  }
  if (!res.ok) throw new Error(body?.error || `服务器返回 ${res.status}`);
  return body;
}

function label(d) {
  return d.name || d.hostname || d.ip || d.mac;
}

function visible() {
  let list = page.devices || [];
  if (state.filter === 'controlled') list = list.filter((d) => d.controlled);
  if (state.filter === 'paused') list = list.filter((d) => d.paused);
  if (state.filter === 'online') list = list.filter((d) => d.online);
  return list.slice().sort((a, b) => rank(a) - rank(b) || label(a).localeCompare(label(b), 'zh'));
}

function rank(d) {
  if (d.paused) return 0;
  if (d.controlled && d.action && d.action !== 'allow') return 1;
  if (d.reason === 'extend') return 2;
  if (d.controlled) return 3;
  if (d.online) return 4;
  return 5;
}

function render() {
  if (!ctx) return;
  if (!page) {
    ctx.content.innerHTML = `<div class="banner">加载失败：${esc(error?.message || '未知错误')}</div>`;
    return;
  }
  const cal = page.calendar || {};
  const today = cal.today || {};
  ctx.setSub(
    [dayLine(today), page.statDay && page.statDay !== page.civilToday ? `用量计入 ${page.statDay}` : '', page.checkedAt ? `检查于 ${clock(page.checkedAt, page.tzOffset)}（${ago(page.checkedAt, page.generatedAt)}）` : '']
      .filter(Boolean)
      .join(' · '),
  );
  const devs = page.devices || [];
  const controlled = devs.filter((d) => d.controlled).length;
  const paused = devs.filter((d) => d.paused).length;
  const extended = devs.filter((d) => d.reason === 'extend').length;
  const limited = devs.filter((d) => d.controlled && d.action && d.action !== 'allow').length;
  ctx.content.innerHTML = `
    <div id="polBanner"></div>
    <div class="kpis">
      ${kpi('受管控', controlled, '台设备')}
      ${kpi('正在限制', limited, '台')}
      ${kpi('已暂停', paused, '台')}
      ${kpi('临时延长', extended, '台')}
    </div>
    <section class="zone">
      <div class="zone-title"><h2>设备</h2>
        <div class="chips">
          ${['all:全部', 'controlled:已管控', 'paused:已暂停', 'online:在线']
            .map((p) => {
              const [id, name] = p.split(':');
              return `<button type="button" class="chip ${state.filter === id ? 'active' : ''}" data-filter="${id}">${name}</button>`;
            })
            .join('')}
        </div>
      </div>
      <div class="policy-grid">${visible().map(cardHTML).join('') || `<div class="card empty">${devs.length ? '没有符合条件的设备' : '还没有发现设备。接入局域网后可以在这里设置。'}</div>`}</div>
    </section>
    ${templatesHTML()}
    <p class="foot-note">按系统设置里的间隔检查用量；暂停、恢复、延长和保存会马上生效。没有变化时不会重新设置防火墙。游戏和视频限制依据域名和地址识别。暂停按设备的网卡地址生效，IPv4 和 IPv6 都会断开；局域网内部互访不受影响。用量按统计日累计，切换时刻在系统设置里。</p>`;
  const banner = ctx.content.querySelector('#polBanner');
  const notes = [];
  if (error) notes.push(`<div class="banner">${esc(error.message)}</div>`);
  if (page.enforceError) notes.push(`<div class="banner">${esc(page.enforceError)}</div>`);
  if (!page.firewall) notes.push(`<div class="card" style="padding:10px 14px;margin-bottom:12px">当前环境没有防火墙控制权，页面上的判定不会下发到网络。</div>`);
  if (cal.loaded === false) notes.push(`<div class="banner">还没有导入国务院日历。请到系统设置里导入，未导入时工作日按周一至周五计算，不含调休。</div>`);
  banner.innerHTML = notes.join('');
}

function kpi(name, value, unit) {
  return `<div class="card kpi"><div class="kpi-label">${name}</div><div class="kpi-value">${value}<small>${unit}</small></div></div>`;
}

function cardHTML(d) {
  const cls = d.paused || d.action === 'block_all' ? 'block' : d.blockGame || d.blockVideo ? 'limit' : '';
  const canExtend = d.enabled && !d.paused && hasRestriction(d);
  return `<article class="card policy-card ${cls}">
    <div class="dc-head">
      <span class="dot ${d.online ? 'online' : 'offline'}"></span>
      <div style="min-width:0;flex:1">
        <div class="dc-name">${esc(label(d))}</div>
        <div class="dc-meta">${esc([d.ip, d.mac].filter(Boolean).join(' · '))}</div>
      </div>
      ${d.paused ? '<span class="badge danger">已暂停</span>' : d.online ? '<span class="badge">在线</span>' : ''}
    </div>
    <p class="effect">${esc(d.text || '未启用管控')}</p>
    <div class="dc-stats">
      <div class="dc-stat"><div class="l">游戏${d.limitLabel ? ' · ' + esc(d.limitLabel) : ''}</div><div class="v">${quotaText(d.gameMinutes, d.gameLimit)}</div>${meter(d.gameMinutes, d.gameLimit)}</div>
      <div class="dc-stat"><div class="l">视频</div><div class="v">${quotaText(d.videoMinutes, d.videoLimit)}</div>${meter(d.videoMinutes, d.videoLimit)}</div>
      <div class="dc-stat"><div class="l">上网</div><div class="v">${quotaText(d.internetMinutes, d.internetLimit)}</div>${meter(d.internetMinutes, d.internetLimit)}</div>
    </div>
    <div class="dc-meta" style="margin-top:8px">时段控制 · ${esc(windowsText(d.windows))}</div>
    <div class="policy-actions">
      <button class="btn small" type="button" data-edit="${esc(d.mac)}">设置</button>
      <button class="btn small ${d.paused ? 'primary' : 'danger'}" type="button" data-pause="${esc(d.mac)}" data-paused="${d.paused ? '1' : '0'}">${d.paused ? '恢复上网' : '暂停上网'}</button>
      ${canExtend ? [15, 30, 60].map((n) => `<button class="btn small" type="button" data-extend="${esc(d.mac)}" data-minutes="${n}">+${n} 分钟</button>`).join('') : ''}
      ${d.reason === 'extend' ? `<button class="btn small" type="button" data-extend="${esc(d.mac)}" data-minutes="0">取消延长</button>` : ''}
    </div>
  </article>`;
}

function quotaText(used, limit) {
  const u = duration(used || 0);
  if (!limit) return `${u} · 不限`;
  return `${u} / ${duration(limit)}`;
}

function meter(used, limit) {
  if (!limit) return '';
  const p = Math.min(100, Math.round(((used || 0) / limit) * 100));
  const cls = used >= limit ? 'over' : p >= 80 ? 'warn' : '';
  return `<div class="meter ${cls}"><span style="width:${p}%"></span></div>`;
}

function actName(w) {
  if (w.blockInternet) return '禁止上网';
  const parts = [];
  if (w.blockGame) parts.push('禁止游戏');
  if (w.blockVideo) parts.push('禁止视频');
  return parts.length ? parts.join('、') : '不限制';
}

function windowsText(ws) {
  const rows = ws || [];
  if (!rows.length) return '不限时段';
  return rows.map((w) => `${w.start || ''}-${w.end || ''} ${actName(w)}`).join('；');
}

function hasRestriction(d) {
  return (d.dailyLimits || []).length > 0 || (d.windows || []).some((w) => w.blockInternet || w.blockGame || w.blockVideo);
}

function templatesHTML() {
  const list = page.templates || [];
  return `<section class="zone">
    <div class="zone-title"><h2>策略</h2><span class="hint">套用到设备时会覆盖那台设备的全部管控设置</span>
      <span class="spacer"></span>
      <button class="icon-btn" type="button" data-add-template aria-label="新建策略">${PLUS}</button>
    </div>
    <div class="policy-grid">${
      list.length
        ? list
            .map(
              (t) => `<article class="card policy-card">
          <div class="dc-name">${esc(t.name)}</div>
          <div class="dc-meta" style="margin-top:6px">${(t.dailyLimits || []).length} 条每日上限 · ${windowsText(t.windows)}</div>
          <div class="policy-actions">
            <button class="btn small" type="button" data-edit-template="${esc(t.id)}">编辑</button>
            <button class="icon-btn danger-icon" type="button" data-del-template="${esc(t.id)}" aria-label="删除">${MINUS}</button>
          </div>
        </article>`,
            )
            .join('')
        : '<div class="card empty">还没有策略。点右上角加号新建，再在设备里套用。</div>'
    }</div>
  </section>`;
}

function dayLine(d) {
  if (!d || !d.date) return '';
  const bits = [d.date, WEEK[d.weekday] || '', d.name, d.workday ? '工作日' : '休息日'];
  if (d.summer) bits.push('暑假');
  if (d.winter) bits.push('寒假');
  return bits.filter(Boolean).join(' · ');
}

function onContentClick(e) {
  const filter = e.target.closest('[data-filter]');
  if (filter) {
    state.filter = filter.dataset.filter;
    render();
    return;
  }
  const edit = e.target.closest('[data-edit]');
  if (edit) {
    openEditor(edit.dataset.edit);
    return;
  }
  if (e.target.closest('[data-add-template]')) {
    openTemplate('');
    return;
  }
  const editTpl = e.target.closest('[data-edit-template]');
  if (editTpl) {
    openTemplate(editTpl.dataset.editTemplate);
    return;
  }
  const delTpl = e.target.closest('[data-del-template]');
  if (delTpl) {
    if (!confirm('删除这份策略？已经套用到设备上的设置不会变。')) return;
    run(() =>
      api(`${API}/templates/${encodeURIComponent(delTpl.dataset.delTemplate)}`, { method: 'DELETE' }).then((p) => {
        page = p;
        error = null;
      }),
    );
    return;
  }
  const pause = e.target.closest('[data-pause]');
  if (pause) {
    run(() => post(`${API}/${encodeURIComponent(pause.dataset.pause)}/pause`, { paused: pause.dataset.paused !== '1' }));
    return;
  }
  const extend = e.target.closest('[data-extend]');
  if (extend) {
    run(() => post(`${API}/${encodeURIComponent(extend.dataset.extend)}/extend`, { minutes: Number(extend.dataset.minutes) }));
    return;
  }
}

function cloneLimits(list) {
  return (list || []).map((x) => ({ ...x, days: { ...x.days, weekdays: [...(x.days?.weekdays || [])] } }));
}
function cloneWindows(list) {
  return (list || []).map((w) => ({ ...w, days: { ...w.days, weekdays: [...(w.days?.weekdays || [])] } }));
}

function openEditor(mac) {
  const d = (page.devices || []).find((x) => x.mac === mac);
  if (!d) return;
  draftMode = 'device';
  draftMAC = mac;
  draftID = '';
  draft = { enabled: !!d.enabled, name: '', dailyLimits: cloneLimits(d.dailyLimits), windows: cloneWindows(d.windows) };
  paintEditor(d);
}

function openTemplate(id) {
  const t = (page.templates || []).find((x) => x.id === id);
  draftMode = 'template';
  draftMAC = '';
  draftID = id || '';
  draft = { enabled: true, name: t?.name || '', dailyLimits: cloneLimits(t?.dailyLimits), windows: cloneWindows(t?.windows) };
  paintEditor(null);
}

function paintEditor(d) {
  const title = draftMode === 'template'
    ? esc(draft.name || '新建策略')
    : `${esc(label(d))}<span class="sub">${esc(d.mac)}</span>`;
  ctx.openModal(title, editorHTML(), () => {
    draft = null;
    draftMAC = '';
    draftID = '';
  });
}

function editorHTML() {
  const tpls = page.templates || [];
  return `<form class="policy-form" data-policy-form>
    ${draftMode === 'device' ? `<label class="check-row enable-row"><input type="checkbox" data-enabled ${draft.enabled ? 'checked' : ''}>启用管控</label>
      <label class="apply-line">套用策略
        <select class="select" data-apply-template>
          <option value="">选择已有策略</option>
          ${tpls.map((t) => `<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('')}
        </select>
      </label>` : `<label class="apply-line">策略名称<input class="search" style="width:100%" maxlength="20" data-name value="${esc(draft.name || '')}" placeholder="例如上学日"></label>`}
    <section class="form-zone">
      <div class="zone-title"><h3>每日上限</h3>
        <button class="icon-btn" type="button" data-add-limit aria-label="添加一条">${PLUS}</button>
      </div>
      <p class="zone-note">单位：分钟</p>
      <div data-limits>${draft.dailyLimits.map((row, i) => limitHTML(row, i)).join('') || '<div class="muted">还没有每日上限</div>'}</div>
    </section>
    <section class="form-zone">
      <div class="zone-title"><h3>时段控制</h3>
        <button class="icon-btn" type="button" data-add-window aria-label="添加时段">${PLUS}</button>
      </div>
      <p class="zone-note">每一段单独选择。禁止游戏和禁止视频可以同时选；不限制、禁止上网各自独占这一段。</p>
      <div data-windows>${draft.windows.map((row, i) => windowHTML(row, i)).join('') || '<div class="muted">还没有时段。每一段可以单独禁止上网、游戏或视频。</div>'}</div>
    </section>
    <div style="display:flex;justify-content:flex-end;margin-top:16px">
      <button class="btn primary" type="submit">保存</button>
    </div>
    <div class="form-err" data-form-err></div>
  </form>`;
}

function limitHTML(row, i) {
  return `<div class="rule-row" data-limit="${i}">
    ${kindSelect(row.days, i, 'limit')}
    ${minuteField('游戏', 'data-game', row.gameMinutes)}
    ${minuteField('视频', 'data-video', row.videoMinutes)}
    ${minuteField('上网', 'data-net', row.internetMinutes)}
    <button class="icon-btn danger-icon" type="button" data-del-limit="${i}" aria-label="删除">${MINUS}</button>
  </div>`;
}

function minuteField(name, attr, value) {
  return `<label class="min-field"><span class="min-name">${name}</span><input class="search num-input" inputmode="numeric" maxlength="4" placeholder="不限" value="${value || ''}" ${attr}></label>`;
}

function windowHTML(row, i) {
  const none = !row.blockInternet && !row.blockGame && !row.blockVideo;
  const chip = (id, name, on) => `<button type="button" class="chip act-${id} ${on ? 'active' : ''}" data-act="${id}" data-i="${i}">${name}</button>`;
  return `<div class="rule-row" data-window="${i}">
    ${kindSelect(row.days, i, 'window')}
    <label>开始<input class="search num-input" type="time" step="60" value="${esc(row.start || '21:00')}" data-start></label>
    <label>结束<input class="search num-input" type="time" step="60" value="${esc(row.end || '22:00')}" data-end></label>
    <div class="act-picks">
      ${chip('none', '不限制', none)}
      ${chip('internet', '禁止上网', !!row.blockInternet)}
      ${chip('game', '禁止游戏', !!row.blockGame)}
      ${chip('video', '禁止视频', !!row.blockVideo)}
    </div>
    <button class="icon-btn danger-icon" type="button" data-del-window="${i}" aria-label="删除">${MINUS}</button>
  </div>`;
}

function kindSelect(days, i, scope) {
  const kind = days?.kind || 'all';
  const selected = new Set(days?.weekdays || []);
  return `<label>适用<select class="select" data-kind data-scope="${scope}" data-i="${i}">
      ${KINDS.map(([id, name]) => `<option value="${id}" ${id === kind ? 'selected' : ''}>${name}</option>`).join('')}
    </select></label>
    ${kind === 'weekdays' ? `<div class="day-picks">${[1, 2, 3, 4, 5, 6, 7].map((n) => `<button type="button" class="chip ${selected.has(n) ? 'active' : ''}" data-wd="${n}" data-scope="${scope}" data-i="${i}">${WEEK[n].slice(1)}</button>`).join('')}</div>` : ''}`;
}

function onModalClick(e) {
  if (!draft) return;
  if (e.target.closest('[data-add-limit]')) {
    capture();
    draft.dailyLimits.push({ days: { kind: 'all' } });
    repaint();
    return;
  }
  const delL = e.target.closest('[data-del-limit]');
  if (delL) {
    capture();
    draft.dailyLimits.splice(Number(delL.dataset.delLimit), 1);
    repaint();
    return;
  }
  if (e.target.closest('[data-add-window]')) {
    capture();
    draft.windows.push({ days: { kind: 'workday' }, start: '21:00', end: '22:00' });
    repaint();
    return;
  }
  const delW = e.target.closest('[data-del-window]');
  if (delW) {
    capture();
    draft.windows.splice(Number(delW.dataset.delWindow), 1);
    repaint();
    return;
  }
  const act = e.target.closest('[data-act]');
  if (act) {
    capture();
    const w = draft.windows[Number(act.dataset.i)];
    if (!w) return;
    if (act.dataset.act === 'none') {
      w.blockInternet = w.blockGame = w.blockVideo = false;
    } else if (act.dataset.act === 'internet') {
      w.blockInternet = !w.blockInternet;
      if (w.blockInternet) w.blockGame = w.blockVideo = false;
    } else if (act.dataset.act === 'game') {
      w.blockGame = !w.blockGame;
      w.blockInternet = false;
    } else if (act.dataset.act === 'video') {
      w.blockVideo = !w.blockVideo;
      w.blockInternet = false;
    }
    repaint();
    return;
  }
  const wd = e.target.closest('[data-wd]');
  if (wd) {
    capture();
    const list = wd.dataset.scope === 'limit' ? draft.dailyLimits : draft.windows;
    const row = list[Number(wd.dataset.i)];
    const n = Number(wd.dataset.wd);
    const set = new Set(row.days.weekdays || []);
    if (set.has(n)) set.delete(n);
    else set.add(n);
    row.days.weekdays = [...set].sort();
    wd.classList.toggle('active');
  }
}

async function onModalChange(e) {
  if (!draft) return;
  const apply = e.target.closest('[data-apply-template]');
  if (apply && apply.value) {
    const t = (page.templates || []).find((x) => x.id === apply.value);
    if (!t || draftMode !== 'device') return;
    draft.enabled = true;
    draft.dailyLimits = cloneLimits(t.dailyLimits);
    draft.windows = cloneWindows(t.windows);
    const errEl = document.querySelector('[data-form-err]');
    try {
      page = await api(`${API}/${encodeURIComponent(draftMAC)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ enabled: true, dailyLimits: draft.dailyLimits, windows: draft.windows }),
      });
      const d = (page.devices || []).find((x) => x.mac === draftMAC);
      if (d) {
        draft.enabled = !!d.enabled;
        draft.dailyLimits = cloneLimits(d.dailyLimits);
        draft.windows = cloneWindows(d.windows);
      }
      error = null;
      render();
      repaint();
    } catch (err) {
      if (errEl) errEl.textContent = err.message;
    }
    return;
  }
  if (!e.target.matches('[data-kind]')) return;
  capture();
  repaint();
}

function repaint() {
  if (draftMode === 'template') paintEditor(null);
  else {
    const d = (page.devices || []).find((x) => x.mac === draftMAC);
    if (d) paintEditor(d);
  }
}

function capture() {
  const form = document.querySelector('[data-policy-form]');
  if (!form || !draft) return;
  const enabled = form.querySelector('[data-enabled]');
  if (enabled) draft.enabled = enabled.checked;
  const name = form.querySelector('[data-name]');
  if (name) draft.name = name.value;
  draft.dailyLimits = [...form.querySelectorAll('[data-limit]')].map(readLimit);
  draft.windows = [...form.querySelectorAll('[data-window]')].map(readWindow);
}

function readLimit(row) {
  return {
    days: readDays(row),
    gameMinutes: readNum(row.querySelector('[data-game]')),
    videoMinutes: readNum(row.querySelector('[data-video]')),
    internetMinutes: readNum(row.querySelector('[data-net]')),
  };
}

function readWindow(row) {
  const acts = new Set([...row.querySelectorAll('[data-act].active')].map((el) => el.dataset.act));
  return {
    days: readDays(row),
    start: row.querySelector('[data-start]').value.trim(),
    end: row.querySelector('[data-end]').value.trim(),
    blockInternet: acts.has('internet'),
    blockGame: acts.has('game'),
    blockVideo: acts.has('video'),
  };
}

function readDays(row) {
  const kind = row.querySelector('[data-kind]').value;
  if (kind !== 'weekdays') return { kind };
  const weekdays = [...row.querySelectorAll('[data-wd].active')].map((el) => Number(el.dataset.wd));
  return { kind, weekdays };
}

function onModalInput(e) {
  const input = e.target.closest('[data-game], [data-video], [data-net]');
  if (!input) return;
  const digits = input.value.replace(/\D/g, '');
  if (digits !== input.value) input.value = digits;
  if (digits && Number(digits) > MAX_MINUTES) input.value = String(MAX_MINUTES);
}

function readNum(input) {
  const v = input.value.trim();
  if (!v) return 0;
  const n = Number(v);
  return Number.isInteger(n) ? n : NaN;
}

async function onSubmit(e) {
  const form = e.target.closest('[data-policy-form]');
  if (!form || !draft) return;
  if (draftMode === 'device' && !draftMAC) return;
  e.preventDefault();
  capture();
  const errEl = form.querySelector('[data-form-err]');
  for (const row of draft.dailyLimits) {
    if ([row.gameMinutes, row.videoMinutes, row.internetMinutes].some((n) => Number.isNaN(n))) {
      errEl.textContent = '时长请填整数分钟';
      return;
    }
    if ([row.gameMinutes, row.videoMinutes, row.internetMinutes].some((n) => n > MAX_MINUTES)) {
      errEl.textContent = '时长不能超过 24 小时（1440 分钟）';
      return;
    }
    if (!row.gameMinutes && !row.videoMinutes && !row.internetMinutes) {
      errEl.textContent = '每条上限至少填写游戏、视频或上网其中一项';
      return;
    }
  }
  const body = { enabled: draft.enabled, dailyLimits: draft.dailyLimits, windows: draft.windows };
  if (draftMode === 'device' && !body.enabled && (page.devices || []).find((d) => d.mac === draftMAC)?.paused) {
    if (!confirm('关闭管控会同时恢复上网。已用时长仍然保留。')) return;
  }
  errEl.textContent = '';
  try {
    if (draftMode === 'template') {
      const path = draftID ? `${API}/templates/${encodeURIComponent(draftID)}` : `${API}/templates`;
      page = await api(path, {
        method: draftID ? 'PUT' : 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ name: draft.name, dailyLimits: draft.dailyLimits, windows: draft.windows }),
      });
    } else page = await api(`${API}/${encodeURIComponent(draftMAC)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
    });
    error = null;
    ctx.closeModal();
    render();
  } catch (err) {
    errEl.textContent = err.message;
  }
}
