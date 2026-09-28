// 每日概览：默认看今天，也可以切换到昨天。筛选和图表都在浏览器本地计算。

import { ago, bytes, clock, duration, esc, pct, shortDuration } from '../format.js';
import {
  RANGES,
  THRESHOLDS,
  appTotals,
  appsIn,
  bucketStart,
  bucketEnd,
  catLabel,
  categoryTotals,
  deviceStats,
  dominantCat,
  fetchOverview,
  foreground,
  insights,
  rangeMask,
  series,
  sessions,
  appMatches,
} from '../data.js';
import { axis, columns, hourChart, legend, stackBar, timeline } from '../charts.js';

const REFRESH_ICON =
  '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9"/><path d="M13.5 2.5v3h-3"/></svg>';

const narrow = window.matchMedia('(max-width: 680px)');

const state = {
  day: 'today',
  range: 'all',
  cat: 'all',
  device: 'all',
  sort: 'picked',
  appSort: 'minutes',
  appQuery: '',
  appLimit: 15,
};

let ctx = null;
let model = null;
let error = null;
let notice = '';
let loading = false;
let view = null;
let refreshBtn = null;

export const overviewPage = {
  mount(c) {
    ctx = c;
    ctx.actions.innerHTML = `<div class="chips day-switch">
      <button type="button" class="chip ${state.day === 'today' ? 'active' : ''}" data-day="today">今天</button>
      <button type="button" class="chip ${state.day === 'yesterday' ? 'active' : ''}" data-day="yesterday">昨天</button>
    </div><button class="btn primary" type="button" id="refreshBtn">${REFRESH_ICON}<span>刷新</span></button>`;
    refreshBtn = ctx.actions.querySelector('#refreshBtn');
    refreshBtn.addEventListener('click', load);
    ctx.actions.addEventListener('click', onDay);
    ctx.content.addEventListener('click', onClick);
    ctx.content.addEventListener('change', onChange);
    ctx.content.addEventListener('input', onInput);
    ctx.content.addEventListener('pointermove', onHeat);
    ctx.content.addEventListener('pointerdown', onHeat);
    narrow.addEventListener('change', render);
    if (model) render();
    else ctx.content.innerHTML = '<div class="card empty">正在加载…</div>';
    load();
  },
  unmount() {
    ctx.content.removeEventListener('click', onClick);
    ctx.content.removeEventListener('change', onChange);
    ctx.content.removeEventListener('input', onInput);
    ctx.content.removeEventListener('pointermove', onHeat);
    ctx.content.removeEventListener('pointerdown', onHeat);
    narrow.removeEventListener('change', render);
    ctx.actions.removeEventListener('click', onDay);
    ctx = null;
  },
};

async function load() {
  if (loading) return;
  loading = true;
  refreshBtn?.classList.add('loading');
  refreshBtn?.setAttribute('disabled', '');
  try {
    model = await fetchOverview(state.day);
    error = null;
    notice = '';
    if (state.device !== 'all' && !model.devices.some((d) => d.id === state.device)) state.device = 'all';
  } catch (e) {
    error = e;
  } finally {
    loading = false;
    refreshBtn?.classList.remove('loading');
    refreshBtn?.removeAttribute('disabled');
  }
  if (ctx) render();
}

// ---------- 渲染 ----------

function render() {
  if (!ctx) return;
  if (!model) {
    ctx.content.innerHTML = `<div class="banner">加载失败：${esc(error?.message || '未知错误')}</div>`;
    return;
  }
  const m = model;
  const mask = rangeMask(m, state.range);
  const devs = state.device === 'all' ? m.devices : m.devices.filter((d) => d.id === state.device);
  const stats = devs.map((d) => deviceStats(m, d, mask, state.cat));
  const dayStats = state.range === 'all' && state.cat === 'all' ? stats : devs.map((d) => deviceStats(m, d, m.fullMask, 'all'));
  const cols = columns(m, mask, narrow.matches && mask.reduce((s, v) => s + v, 0) > 48);
  view = { m, mask, stats, cols };

  const dayName = state.day === 'yesterday' ? '昨天' : '今天';
  const stale = state.day === 'today' && m.lastAt && m.generatedAt - m.lastAt > 600;
  ctx.setSub(
    m.lastAt
      ? `${dayName} · 数据截至 ${m.clock(m.lastAt)}${stale ? '（采集可能已停止）' : ''} · 统计日 ${m.raw.day.slice(5)}（${m.clock(m.dayStart)} 起）`
      : `${dayName} · 统计日 ${m.raw.day.slice(5)} · 还没有数据`,
  );
  paintDaySwitch();

  ctx.content.innerHTML = [
    notice ? `<div class="banner">${esc(notice)}</div>` : '',
    error ? `<div class="banner">刷新失败：${esc(error.message)}，下面显示的是上一次的数据</div>` : '',
    filtersHTML(m),
    kpisHTML(m, stats),
    `<div class="twin section">${state.day === 'today' ? liveHTML(m, devs) : ''}${insightsHTML(m, dayStats)}</div>`,
    devicesHTML(m, stats, cols),
    heatHTML(m, stats, cols),
    `<div class="twin wide-right section">${categoriesHTML(m, stats)}${appsSectionHTML()}</div>`,
    footHTML(m),
  ].join('');
  renderApps();
}

function filtersHTML(m) {
  const present = new Set();
  for (const d of m.devices) for (const c of m.cats) if (d.cat[c.index].some((v) => v > 0)) present.add(c.id);
  const catChips = [
    { id: 'all', name: '全部' },
    { id: 'ent', name: '娱乐', color: '#d6409f' },
    ...m.cats.filter((c) => present.has(c.id) || c.id === state.cat).map((c) => ({ id: c.id, name: c.name, color: c.color })),
  ];
  const used = (d) => d.active.some((v) => v > 0);
  const devOpts = [...m.devices]
    .sort((a, b) => Number(used(b)) - Number(used(a)) || a.name.localeCompare(b.name, 'zh'))
    .map((d) => `<option value="${esc(d.id)}" ${d.id === state.device ? 'selected' : ''}>${esc(d.name)}</option>`)
    .join('');
  return `<div class="card filters">
    <div class="filter-row"><span class="filter-label">时段</span><div class="chips chips-wrap">${RANGES.map(
      (r) => `<button type="button" class="chip ${r.id === state.range ? 'active' : ''}" data-range="${r.id}">${r.name}${r.hint ? ` <small>${r.hint}</small>` : ''}</button>`,
    ).join('')}</div></div>
    <div class="filter-row"><span class="filter-label">类别</span><div class="chips chips-wrap">${catChips
      .map(
        (c) => `<button type="button" class="chip ${c.id === state.cat ? 'active' : ''}" data-cat="${c.id}">${c.color ? `<i class="dot" style="background:${c.color}"></i>` : ''}${esc(c.name)}</button>`,
      )
      .join('')}</div></div>
    <div class="filter-row"><span class="filter-label">设备</span>
      <select class="select" data-device><option value="all">全部设备（${m.devices.length}）</option>${devOpts}</select>
    </div>
  </div>`;
}

function kpiDuration(min) {
  const v = Math.round(min);
  if (v < 60) return `${v}<small>分钟</small>`;
  const h = Math.floor(v / 60);
  const r = v % 60;
  return `${h}<small>小时</small>${r ? `${r}<small>分</small>` : ''}`;
}

function kpisHTML(m, stats) {
  const hasCat = state.cat !== 'all';
  const byCatSel = hasCat && state.cat !== 'ent';
  const activeDevs = stats.filter((s) => (hasCat ? s.picked : s.active) > 0).length;
  const online = stats.filter((s) => s.dev.online).length;
  const focusLabel = byCatSel ? catLabel(m, state.cat) : '娱乐';
  const focus = stats.reduce((s, x) => s + (byCatSel ? x.picked : x.ent), 0);
  let focusFoot;
  if (byCatSel) {
    const top = [...stats].sort((a, b) => b.picked - a.picked).filter((s) => s.picked > 0).slice(0, 2);
    focusFoot = top.length ? top.map((s) => `${esc(s.dev.name)} ${shortDuration(s.picked)}`).join(' · ') : '所选时段内没有';
  } else {
    const ents = m.cats
      .filter((c) => c.ent)
      .map((c) => ({ c, v: stats.reduce((s, x) => s + x.catMin[c.index], 0) }))
      .filter((x) => x.v > 0)
      .sort((a, b) => b.v - a.v);
    focusFoot = ents.length ? ents.map((x) => `${esc(x.c.name)} ${shortDuration(x.v)}`).join(' · ') : '所选时段内没有娱乐流量';
  }
  const gamer = [...stats].sort((a, b) => b.game - a.game)[0];
  const total = stats.reduce((s, x) => s + (hasCat ? x.pickedBytes : x.bytes), 0);
  const down = stats.reduce((s, x) => s + x.down, 0);
  const up = stats.reduce((s, x) => s + x.up, 0);
  const trafficFoot = hasCat ? '所选类别的流量' : `下行 ${bytes(down)} · 上行 ${bytes(up)}`;
  return `<div class="kpis">
    <div class="card kpi"><div class="kpi-label">活跃设备</div><div class="kpi-value">${activeDevs}<small>台</small></div>
      <div class="kpi-foot">${state.day === 'today' ? `当前在线 ${online} 台 · ` : ''}共 ${stats.length} 台</div></div>
    <div class="card kpi"><div class="kpi-label">${esc(focusLabel)}时长（各设备累计）</div><div class="kpi-value">${kpiDuration(focus)}</div>
      <div class="kpi-foot">${focusFoot}</div></div>
    <div class="card kpi"><div class="kpi-label">游戏时长最多</div><div class="kpi-value">${gamer && gamer.game > 0 ? kpiDuration(gamer.game) : '—'}</div>
      <div class="kpi-foot">${gamer && gamer.game > 0 ? esc(gamer.dev.name) : '所选时段内没有游戏流量'}</div></div>
    <div class="card kpi"><div class="kpi-label">${hasCat ? '类别流量' : '总流量'}</div><div class="kpi-value">${bytes(total)}</div>
      <div class="kpi-foot">${trafficFoot}</div></div>
  </div>`;
}

function liveHTML(m, devs) {
  const perMin = m.perMin;
  const rows = [];
  let idle = 0;
  for (const d of devs) {
    const recent = [...d.recent].sort((a, b) => b.p - a.p);
    const fg = recent.filter((r) => !r.app.cat.background && appMatches(r.app, state.cat));
    if (!fg.length) {
      if (d.online) idle++;
      continue;
    }
    const ent = fg.some((r) => r.app.cat.ent);
    rows.push({ d, fg, ent, score: (ent ? 1e6 : 0) + fg[0].p });
  }
  rows.sort((a, b) => b.score - a.score);
  const items = rows
    .map(({ d, fg }) => {
      const top = fg[0].app;
      return `<div class="list-item clickable" data-open="${d.index}">
        <span class="dot ${d.online ? 'online' : 'offline'}"></span>
        <div class="grow"><div class="title">${esc(d.name)}</div>
          <div class="sub">${fg.slice(0, 3).map((r) => `${esc(r.app.name)} ${Math.round(r.p * perMin)} 分钟`).join(' · ')}</div></div>
        <span class="badge" style="color:${top.cat.color}"><i class="dot" style="background:${top.cat.color}"></i>${esc(top.cat.name)}</span>
      </div>`;
    })
    .join('');
  const win = Math.round(m.recentSeconds / 60);
  return `<div class="card">
    <div class="section-head"><h2>正在进行</h2><span class="hint">最近 ${win} 分钟</span></div>
    <div class="section-body">
      ${items ? `<div class="list">${items}</div>` : `<div class="empty">最近 ${win} 分钟没有明显的上网行为</div>`}
      ${idle ? `<div class="muted" style="font-size:12px;margin-top:6px">另有 ${idle} 台设备在线，只有后台流量</div>` : ''}
    </div>
  </div>`;
}

function insightsHTML(m, dayStats) {
  const list = insights(m, dayStats);
  const items = list
    .slice(0, 8)
    .map(
      (x) => `<div class="list-item clickable" data-open="${x.dev.index}">
        <span class="alert-ico ${x.level}">${x.icon}</span>
        <div class="grow"><div class="title">${esc(x.title)}</div><div class="sub">${esc(x.sub || '')}</div></div>
      </div>`,
    )
    .join('');
  const t = THRESHOLDS;
  return `<div class="card">
    <div class="section-head"><h2>关注提醒</h2><span class="hint">${state.day === 'yesterday' ? '昨天全天' : '今天全天'}</span></div>
    <div class="section-body">
      ${items ? `<div class="list">${items}</div>` : `<div class="empty">${state.day === 'yesterday' ? '昨天' : '今天'}没有需要关注的情况</div>`}
      <div class="muted" style="font-size:12px;margin-top:6px">提醒条件：游戏 ≥ ${t.gameDaily} 分钟、连续游戏 ≥ ${t.gameStreak} 分钟、短视频 ≥ ${t.shortVideoDaily} 分钟、22 点后娱乐 ≥ ${t.lateNightEnt} 分钟</div>
    </div>
  </div>`;
}

const SORTS = {
  picked: (a, b) => b.picked - a.picked || b.active - a.active,
  ent: (a, b) => b.ent - a.ent || b.active - a.active,
  game: (a, b) => b.game - a.game || b.ent - a.ent,
  bytes: (a, b) => b.bytes - a.bytes,
  active: (a, b) => b.active - a.active,
  recent: (a, b) => (b.dev.lastActiveAt || 0) - (a.dev.lastActiveAt || 0),
  name: (a, b) => a.dev.name.localeCompare(b.dev.name, 'zh'),
};

function sortedStats(stats) {
  const cmp = SORTS[state.sort] || SORTS.picked;
  return [...stats].sort((a, b) => (b.active > 0) - (a.active > 0) || cmp(a, b));
}

function devicesHTML(m, stats, cols) {
  const label = catLabel(m, state.cat);
  const opts = [
    ['picked', state.cat === 'all' ? '按上网时长' : `按${label}时长`],
    ['ent', '按娱乐时长'],
    ['game', '按游戏时长'],
    ['bytes', '按流量'],
    ...(state.cat === 'all' ? [] : [['active', '按上网时长']]),
    ['recent', '按最近活跃'],
    ['name', '按名称'],
  ];
  if (!opts.some(([k]) => k === state.sort)) state.sort = 'picked';
  const sorted = sortedStats(stats);
  const idle = sorted.filter((st) => (state.cat === 'all' ? st.active : st.picked) <= 0);
  const cards = sorted
    .filter((st) => !idle.includes(st))
    .map((st) => deviceCard(m, st, cols))
    .join('');
  const idleHTML = idle.length
    ? `<div class="card idle-devs"><span class="muted">${cards ? '其余' : ''} ${idle.length} 台设备在所选条件下没有记录：</span>${idle
        .map((st) => `<button type="button" class="chip" data-open="${st.dev.index}"><span class="dot ${st.dev.online ? 'online' : 'offline'}"></span>${esc(st.dev.name)}</button>`)
        .join('')}</div>`
    : '';
  const range = RANGES.find((r) => r.id === state.range);
  return `<div class="section">
    <div class="section-head" style="padding:0 2px 10px"><h2>设备</h2>
      <span class="hint">${esc(range.name)}${state.cat === 'all' ? '' : ` · ${esc(label)}`}</span><span class="spacer"></span>
      <select class="select" data-sort>${opts.map(([k, v]) => `<option value="${k}" ${k === state.sort ? 'selected' : ''}>${v}</option>`).join('')}</select>
    </div>
    ${cards ? `<div class="device-grid">${cards}</div>` : ''}
    ${idleHTML}
  </div>`;
}

function nowBadge(m, d) {
  const fg = d.recent.filter((r) => !r.app.cat.background).sort((a, b) => b.p - a.p);
  if (fg.length) {
    const a = fg[0].app;
    return `<span class="badge ${a.cat.ent ? 'danger' : ''}" title="最近 ${Math.round(m.recentSeconds / 60)} 分钟">正在 ${esc(a.name)}</span>`;
  }
  if (d.online) return '<span class="badge">在线</span>';
  if (d.lastActiveAt) return `<span class="badge">${ago(d.lastActiveAt, m.generatedAt)}</span>`;
  return '';
}

function deviceCard(m, st, cols) {
  const d = st.dev;
  const hasSel = state.cat !== 'all';
  const meta = [d.ip, d.hostname ? d.hints[0] : d.mac].filter(Boolean).map(esc).join(' · ');
  const middle = hasSel && state.cat !== 'ent' ? { l: catLabel(m, state.cat), v: st.picked } : { l: '娱乐', v: st.ent };
  const segs = m.cats
    .map((c) => ({ c, color: c.color, value: st.catMin[c.index], title: `${c.name} ${duration(st.catMin[c.index])}` }))
    .filter((x) => x.value > 0)
    .sort((a, b) => a.c.background - b.c.background || b.value - a.value);
  const apps = foreground(st.apps)
    .slice(0, 3)
    .map((a) => `<span class="badge"><i class="dot" style="background:${a.app.cat.color}"></i>${esc(a.app.name)} ${shortDuration(a.minutes)}</span>`)
    .join('');
  return `<div class="card device-card" data-open="${d.index}">
    <div class="dc-head">
      <span class="dot ${d.online ? 'online' : 'offline'}" title="${d.online ? '在线' : '离线'}"></span>
      <div style="min-width:0"><div class="dc-name">${esc(d.name)}</div><div class="dc-meta">${meta}</div></div>
      <div class="dc-now">${nowBadge(m, d)}</div>
    </div>
    <div class="dc-stats">
      <div class="dc-stat"><div class="l">上网</div><div class="v">${shortDuration(st.active)}</div></div>
      <div class="dc-stat"><div class="l">${esc(middle.l)}</div><div class="v ${middle.v > 0 ? 'ent' : ''}">${shortDuration(middle.v)}</div></div>
      <div class="dc-stat"><div class="l">流量</div><div class="v">${bytes(hasSel ? st.pickedBytes : st.bytes)}</div></div>
    </div>
    ${stackBar(segs)}
    ${timeline(m, d, cols, state.cat, series(m, d, state.cat), 'mini-tl')}
    ${axis(m, cols, 4)}
    ${apps ? `<div class="dc-apps">${apps}</div>` : ''}
  </div>`;
}

function heatHTML(m, stats, cols) {
  const rows = sortedStats(stats).filter((st) => (state.cat === 'all' ? st.active : st.picked) > 0);
  const present = m.cats.filter((c) => rows.some((st) => st.catMin[c.index] > 0) && !c.background);
  const body = rows.length
    ? `<div class="heat-wrap"><div class="heat">
        ${rows
          .map(
            (st) => `<div class="heat-name" data-open="${st.dev.index}" title="${esc(st.dev.name)}">${esc(st.dev.name)}</div>
              ${timeline(m, st.dev, cols, state.cat, series(m, st.dev, state.cat), 'heat-row')}`,
          )
          .join('')}
        <div class="heat-axis">${axis(m, cols, narrow.matches ? 4 : 8)}</div>
      </div></div>
      <div class="heat-info" id="heatInfo">${narrow.matches ? '点按色块查看该时段详情' : '鼠标移到色块上查看该时段详情'}</div>`
    : '<div class="empty">所选条件下没有上网记录</div>';
  const unit = cols.length && cols[0].length > 1 ? '每格 1 小时' : `每格 ${Math.round(m.bucketSeconds / 60)} 分钟`;
  return `<div class="card section">
    <div class="section-head"><h2>上网时段</h2><span class="hint">${unit}，颜色为该时段的主要行为，越深越活跃</span></div>
    <div class="section-body">${body}<div style="margin-top:10px">${legend(present)}</div></div>
  </div>`;
}

function categoriesHTML(m, stats) {
  const rows = categoryTotals(m, stats);
  const max = rows.reduce((s, r) => Math.max(s, r.minutes), 0) || 1;
  const body = rows
    .map(
      (r) => `<div class="cat-row ${state.cat === r.cat.id ? 'active' : ''} ${r.cat.background ? 'bg' : ''}" data-cat="${r.cat.id}" title="点击只看这个类别">
        <div class="cat-name"><i class="dot" style="background:${r.cat.color}"></i><span>${esc(r.cat.name)}</span></div>
        <div class="cat-bar"><span style="width:${pct(r.minutes, max)}%;background:${r.cat.color}"></span></div>
        <div class="r">${shortDuration(r.minutes)}</div>
        <div class="r muted bytes">${bytes(r.bytes)}</div>
        <div class="cat-devs">${r.devices
          .slice(0, 3)
          .map((x) => `${esc(x.dev.name)} ${shortDuration(x.minutes)}`)
          .join(' · ')}${r.devices.length > 3 ? ` 等 ${r.devices.length} 台` : ''}</div>
      </div>`,
    )
    .join('');
  return `<div class="card">
    <div class="section-head"><h2>行为类别</h2><span class="hint">各设备时长累计，点击筛选</span></div>
    <div class="section-body">${body ? `<div class="cat-list">${body}</div>` : '<div class="empty">没有数据</div>'}</div>
  </div>`;
}

function appsSectionHTML() {
  return `<div class="card">
    <div class="section-head"><h2>应用与网站</h2><span class="spacer"></span>
      <input class="search" type="search" placeholder="搜索应用" value="${esc(state.appQuery)}" data-app-search>
    </div>
    <div class="section-body" id="appsBody"></div>
  </div>`;
}

function renderApps() {
  const el = ctx?.content.querySelector('#appsBody');
  if (!el || !view) return;
  const q = state.appQuery.trim().toLowerCase();
  let rows = appTotals(view.stats);
  if (q) rows = rows.filter((r) => r.app.name.toLowerCase().includes(q) || r.app.cat.name.includes(q));
  const key = state.appSort;
  rows.sort((a, b) => (a.app.cat.id === 'unknown') - (b.app.cat.id === 'unknown') || b[key] - a[key] || b.minutes - a.minutes);
  if (!rows.length) {
    el.innerHTML = '<div class="empty">没有匹配的应用</div>';
    return;
  }
  const shown = rows.slice(0, state.appLimit);
  const th = (k, label, cls = '') =>
    `<th class="r ${cls} ${key === k ? 'sorted' : ''}" data-app-sort="${k}">${label}${key === k ? ' ↓' : ''}</th>`;
  el.innerHTML = `<div class="table-wrap"><table class="tbl">
    <thead><tr><th>应用</th><th class="hide-sm hide-md">类别</th><th>设备</th>${th('minutes', '时长')}${th('bytes', '流量')}</tr></thead>
    <tbody>${shown
      .map(
        (r) => `<tr class="${r.devices.length === 1 ? 'clickable' : ''}" ${r.devices.length === 1 ? `data-open="${r.devices[0].dev.index}"` : ''}>
          <td><i class="dot" style="background:${r.app.cat.color};margin-right:6px"></i><span class="app-name">${esc(r.app.name)}</span>${siteTag(r.app)}</td>
          <td class="hide-sm hide-md">${esc(r.app.cat.name)}</td>
          <td class="devs" title="${esc(r.devices.map((x) => x.dev.name).join('、'))}">${esc(r.devices[0].dev.name)}${r.devices.length > 1 ? ` 等 ${r.devices.length} 台` : ''}</td>
          <td class="r">${shortDuration(r.minutes)}</td>
          <td class="r">${bytes(r.bytes)}</td>
        </tr>`,
      )
      .join('')}</tbody></table></div>
    ${rows.length > shown.length ? `<div class="more"><button type="button" class="btn small" data-more>显示全部 ${rows.length} 项</button></div>` : ''}`;
}

// siteTag 标出规则库未收录、按主域名归并显示的网站。
function siteTag(app) {
  return app.rule || app.cat.id === 'unknown' ? '' : '<span class="tag">网站</span>';
}

function footHTML(m) {
  const s = m.stats;
  return `<div class="foot-note">
    统计口径：每 ${Math.round(m.perMin)} 分钟采集一次，某分钟内有流量即计为活跃 1 分钟；每分钟不足 ${m.minFlowKB} KB 的连接（心跳、推送）不计入。
    多台设备同时使用时时长会累加。应用识别依据 DNS 域名，部分游戏使用私有 DNS 时可能显示为"未识别流量"。<br>
    本次分析 ${s.rows} 条记录（${s.inferredRows} 条按 IP 补全域名），服务器耗时 ${s.buildMillis} ms，生成于 ${m.clock(m.generatedAt)}。
  </div>`;
}

// ---------- 设备详情 ----------

function openDevice(index) {
  const m = model;
  const d = m.devices[index];
  if (!d) return;
  const st = deviceStats(m, d, m.fullMask, 'all');
  const list = sessions(m, d.active, m.fullMask, 2).reverse();
  const sessHTML = list.length
    ? list
        .map((s) => {
          const set = [];
          for (let b = s.from; b <= s.to; b++) set.push(b);
          const apps = foreground(appsIn(m, d, set)).slice(0, 3);
          let ent = 0;
          for (const b of set) ent += d.ent[b];
          ent *= m.perMin;
          return `<div class="list-item">
            <div class="grow"><div class="title num">${m.clock(s.start)} – ${m.clock(s.end)}</div>
              <div class="sub">${apps.map((a) => `${esc(a.app.name)} ${shortDuration(a.minutes)}`).join(' · ') || '后台流量'}</div></div>
            ${ent >= s.minutes / 2 && ent > 0 ? '<span class="badge danger">娱乐</span>' : ''}
            <span class="badge num">${shortDuration(s.minutes)}</span>
          </div>`;
        })
        .join('')
    : `<div class="empty">${state.day === 'yesterday' ? '昨天' : '今天'}没有上网记录</div>`;
  const cats = m.cats
    .map((c) => ({ c, v: st.catMin[c.index] }))
    .filter((x) => x.v > 0)
    .sort((a, b) => a.c.background - b.c.background || b.v - a.v);
  const max = cats.reduce((s, x) => Math.max(s, x.v), 0) || 1;
  const catHTML = cats
    .map(
      (x) => `<div class="cat-row" data-cat="${x.c.id}" data-close title="在概览中只看这个类别">
        <div class="cat-name"><i class="dot" style="background:${x.c.color}"></i><span>${esc(x.c.name)}</span></div>
        <div class="cat-bar"><span style="width:${pct(x.v, max)}%;background:${x.c.color}"></span></div>
        <div class="r">${shortDuration(x.v)}</div><div class="r muted bytes"></div>
      </div>`,
    )
    .join('');
  const appHTML = st.appsAll
    .slice(0, 15)
    .map(
      (a) => `<tr><td><i class="dot" style="background:${a.app.cat.color};margin-right:6px"></i>${esc(a.app.name)}${siteTag(a.app)}</td>
        <td class="r">${shortDuration(a.minutes)}</td><td class="r">${bytes(a.bytes)}</td></tr>`,
    )
    .join('');
  const present = m.cats.filter((c) => st.catMin[c.index] > 0);
  const title = `<span class="dot ${d.online ? 'online' : 'offline'}" style="margin-right:8px;vertical-align:middle"></span>${esc(d.name)}
    <span class="sub">${[d.ip, d.mac, ...d.hints].filter(Boolean).map(esc).join(' · ')}</span>`;
  const info = [
    ['IP', d.ip],
    ['MAC', d.mac],
    ['名称', d.alias || '（未命名，可在设备管理中修改）'],
    ['主机名', d.hostname || '（未上报）'],
    ['设备推测', d.hints.join('、') || '—'],
    ['状态', d.online ? `在线，${d.flows} 个连接` : '离线'],
    ['最后活跃', d.lastActiveAt ? `${clock(d.lastActiveAt, m.tz)}（${ago(d.lastActiveAt, m.generatedAt)}）` : '—'],
  ];
  ctx.openModal(
    title,
    `<div class="kpis" style="margin-top:0">
      <div class="card kpi"><div class="kpi-label">上网</div><div class="kpi-value">${kpiDuration(st.active)}</div></div>
      <div class="card kpi"><div class="kpi-label">娱乐</div><div class="kpi-value">${kpiDuration(st.ent)}</div><div class="kpi-foot">占 ${pct(st.ent, st.active)}%</div></div>
      <div class="card kpi"><div class="kpi-label">游戏</div><div class="kpi-value">${kpiDuration(st.game)}</div></div>
      <div class="card kpi"><div class="kpi-label">流量</div><div class="kpi-value">${bytes(st.bytes)}</div><div class="kpi-foot">下行 ${bytes(st.down)} · 上行 ${bytes(st.up)}</div></div>
    </div>
    <div class="detail-block" style="margin-top:16px"><h3>每小时活跃分钟</h3>${hourChart(m, d)}<div style="margin-top:6px">${legend(present)}</div></div>
    <div class="detail-grid" style="margin-top:18px">
      <div><div class="detail-block"><h3>上网时段（${list.length} 段，最近的在前）</h3><div class="list">${sessHTML}</div></div></div>
      <div>
        <div class="detail-block"><h3>行为类别</h3><div class="cat-list">${catHTML || '<div class="empty">没有数据</div>'}</div></div>
        <div class="detail-block"><h3>应用与网站</h3><div class="table-wrap"><table class="tbl"><thead><tr><th>应用</th><th class="r">时长</th><th class="r">流量</th></tr></thead><tbody>${appHTML}</tbody></table></div></div>
        <div class="detail-block"><h3>设备信息</h3><dl class="kv">${info.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join('')}</dl></div>
      </div>
    </div>`,
  );
  const body = document.getElementById('modalBody');
  body.onclick = (e) => {
    const row = e.target.closest('[data-cat]');
    if (!row) return;
    state.cat = row.dataset.cat;
    state.device = d.id;
    render();
  };
}

// ---------- 交互 ----------

function onDay(e) {
  const btn = e.target.closest('[data-day]');
  if (!btn || btn.dataset.day === state.day || loading) return;
  switchDay(btn.dataset.day);
}

async function switchDay(next) {
  loading = true;
  refreshBtn?.classList.add('loading');
  refreshBtn?.setAttribute('disabled', '');
  try {
    const nextModel = await fetchOverview(next);
    if (next === 'yesterday' && !nextModel.lastAt) {
      notice = '没有昨天的数据';
    } else {
      state.day = next;
      model = nextModel;
      notice = '';
      error = null;
      if (state.device !== 'all' && !model.devices.some((d) => d.id === state.device)) state.device = 'all';
    }
  } catch {
    notice = '数据切换失败';
  } finally {
    loading = false;
    refreshBtn?.classList.remove('loading');
    refreshBtn?.removeAttribute('disabled');
  }
  if (ctx) render();
}

function paintDaySwitch() {
  if (!ctx) return;
  for (const btn of ctx.actions.querySelectorAll('[data-day]')) {
    btn.classList.toggle('active', btn.dataset.day === state.day);
  }
}

function onClick(e) {
  const t = e.target;
  const range = t.closest('[data-range]');
  if (range) {
    state.range = range.dataset.range;
    return render();
  }
  const cat = t.closest('[data-cat]');
  if (cat) {
    const id = cat.dataset.cat;
    state.cat = state.cat === id && cat.classList.contains('cat-row') ? 'all' : id;
    state.appLimit = 15;
    return render();
  }
  const sort = t.closest('[data-app-sort]');
  if (sort) {
    state.appSort = sort.dataset.appSort;
    return renderApps();
  }
  if (t.closest('[data-more]')) {
    state.appLimit = Infinity;
    return renderApps();
  }
  if (t.closest('.heat-row')) return;
  const open = t.closest('[data-open]');
  if (open) openDevice(Number(open.dataset.open));
}

function onChange(e) {
  const t = e.target;
  if (t.matches('[data-device]')) {
    state.device = t.value;
    render();
  } else if (t.matches('[data-sort]')) {
    state.sort = t.value;
    render();
  }
}

function onInput(e) {
  if (e.target.matches('[data-app-search]')) {
    state.appQuery = e.target.value;
    renderApps();
  }
}

// onHeat 显示热力图中指针所在格子的详情（桌面悬停、手机点按）。
function onHeat(e) {
  const svg = e.target.closest?.('.heat-row');
  const info = ctx?.content.querySelector('#heatInfo');
  if (!svg || !info || !view) return;
  const { m, cols } = view;
  const rect = svg.getBoundingClientRect();
  const i = Math.floor(((e.clientX - rect.left) / rect.width) * cols.length);
  const col = cols[Math.max(0, Math.min(cols.length - 1, i))];
  const d = m.devices[Number(svg.dataset.dev)];
  if (!col || !d) return;
  const s = series(m, d, state.cat);
  let p = 0;
  for (const b of col) p += s[b];
  const range = `${m.clock(bucketStart(m, col[0]))}–${m.clock(bucketEnd(m, col[col.length - 1]))}`;
  if (!p) {
    info.innerHTML = `<b>${esc(d.name)}</b> · ${range} · 无${state.cat === 'all' ? '' : esc(catLabel(m, state.cat))}流量`;
    return;
  }
  const cat = dominantCat(m, d, col, state.cat);
  const apps = foreground(appsIn(m, d, col, state.cat)).slice(0, 3);
  info.innerHTML = `<b>${esc(d.name)}</b> · ${range} · 活跃 ${Math.round(p * m.perMin)} 分钟${cat ? ` · 主要为<span style="color:${cat.color}">${esc(cat.name)}</span>` : ''}${
    apps.length ? `：${apps.map((a) => `${esc(a.app.name)} ${Math.round(a.minutes)} 分钟`).join('、')}` : ''
  }`;
}
