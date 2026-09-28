// 把服务端的紧凑事实表展开成按设备的时段数组，并提供各种组合查询。
// 服务端只负责扫描、分类与去重计数；这里的所有筛选、汇总、排序都在浏览器完成，
// 切换筛选条件不会再请求路由器。

import { clock, duration as fmtMin, localHour } from './format.js';

export const API_OVERVIEW = '/api/v1/overview/today';

const COLORS = {
  game: '#e5484d',
  shortvideo: '#d6409f',
  video: '#f76b15',
  live: '#ffb224',
  music: '#12a594',
  social: '#30a46c',
  edu: '#3e63dd',
  shopping: '#8e4ec6',
  info: '#0090ff',
  system: '#8b8d98',
  iot: '#ad7f58',
  ads: '#b9bbc6',
  other: '#c4c7d0',
  unknown: '#d6d8de',
};

// 背景类流量（系统服务、物联网、广告、未识别）不代表"人在做什么"，
// 在判断时段主要行为、列举主要应用时降低优先级。
const BACKGROUND = new Set(['system', 'iot', 'ads', 'other', 'unknown']);

export const RANGES = [
  { id: 'all', name: '全天' },
  { id: 'morning', name: '上午', hint: '06–12', from: 6, to: 12 },
  { id: 'afternoon', name: '下午', hint: '12–18', from: 12, to: 18 },
  { id: 'evening', name: '晚上', hint: '18–22', from: 18, to: 22 },
  { id: 'night', name: '深夜', hint: '22–06', from: 22, to: 6 },
  { id: 'hour', name: '最近 1 小时' },
];

export async function fetchOverview(day = 'today') {
  const q = day === 'yesterday' ? '?day=yesterday' : '';
  const res = await fetch(API_OVERVIEW + q, { cache: 'no-cache', headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`服务器返回 ${res.status}`);
  return buildModel(await res.json());
}

export function displayName(d) {
  if (d.name) return d.name;
  if (d.hostname) return d.hostname;
  const tail = d.ip ? d.ip.split('.').pop() : '';
  if (d.hints && d.hints.length) return `${d.hints[0]} .${tail}`;
  return d.ip || d.mac || d.id;
}

export function buildModel(raw) {
  const B = raw.buckets || 0;
  const cats = (raw.categories || []).map((c, i) => ({
    id: c.id,
    name: c.name,
    ent: !!c.ent,
    index: i,
    color: COLORS[c.id] || '#98a2b3',
    background: BACKGROUND.has(c.id),
  }));
  const apps = (raw.apps || []).map((a, i) => ({ name: a.name, rule: !!a.rule, index: i, cat: cats[a.cat] }));
  const devices = (raw.devices || []).map((d, i) => ({
    ...d,
    hints: d.hints || [],
    index: i,
    alias: d.name || '',
    name: displayName(d),
    active: new Float32Array(B),
    ent: new Float32Array(B),
    up: new Float64Array(B),
    down: new Float64Array(B),
    cat: cats.map(() => new Float32Array(B)),
    appRows: [],
    recent: [],
  }));

  for (const [d, b, p, up, down] of raw.deviceBuckets || []) {
    const dev = devices[d];
    dev.active[b] = p;
    dev.up[b] = up;
    dev.down[b] = down;
  }
  for (const [d, b, c, p] of raw.categoryBuckets || []) devices[d].cat[c][b] = p;
  for (const [d, b, p] of raw.entBuckets || []) devices[d].ent[b] = p;
  for (const [d, b, a, p, up, down] of raw.appBuckets || []) devices[d].appRows.push({ b, a, p, bytes: up + down });
  for (const [d, a, p] of raw.recent || []) devices[d].recent.push({ app: apps[a], p });

  return {
    raw,
    B,
    perMin: raw.intervalSeconds / 60,
    bucketSeconds: raw.bucketSeconds,
    dayStart: raw.dayStart,
    dayEnd: raw.dayEnd,
    tz: raw.tzOffset,
    lastAt: raw.lastCollectedAt,
    generatedAt: raw.generatedAt,
    recentSeconds: raw.recentSeconds,
    minFlowKB: Math.round(raw.minFlowBytesPerMinute / 1024),
    cats,
    apps,
    devices,
    catById: Object.fromEntries(cats.map((c) => [c.id, c])),
    stats: raw.stats,
    fullMask: new Uint8Array(B).fill(1),
    clock: (t) => clock(t, raw.tzOffset),
  };
}

export function bucketStart(m, b) {
  return m.dayStart + b * m.bucketSeconds;
}

// bucketEnd 为时段结束时刻，当前时段截止到最新采集时间。
export function bucketEnd(m, b) {
  const end = bucketStart(m, b + 1);
  return m.lastAt && end > m.lastAt && bucketStart(m, b) < m.lastAt ? m.lastAt : end;
}

export function rangeMask(m, id) {
  const mask = new Uint8Array(m.B);
  const r = RANGES.find((x) => x.id === id) || RANGES[0];
  const ref = m.lastAt || m.generatedAt;
  for (let b = 0; b < m.B; b++) {
    const start = bucketStart(m, b);
    if (r.id === 'all') {
      mask[b] = 1;
    } else if (r.id === 'hour') {
      mask[b] = start + m.bucketSeconds > ref - 3600 && start < ref ? 1 : 0;
    } else {
      const h = localHour(start, m.tz);
      mask[b] = (r.from < r.to ? h >= r.from && h < r.to : h >= r.from || h < r.to) ? 1 : 0;
    }
  }
  return mask;
}

// ---------- 类别筛选：'all' | 'ent' | 类别 id ----------

export function catLabel(m, sel) {
  if (sel === 'all') return '全部';
  if (sel === 'ent') return '娱乐';
  return m.catById[sel]?.name || sel;
}

export function series(m, dev, sel) {
  if (sel === 'all') return dev.active;
  if (sel === 'ent') return dev.ent;
  return dev.cat[m.catById[sel].index];
}

export function appMatches(app, sel) {
  if (sel === 'all') return true;
  if (sel === 'ent') return app.cat.ent;
  return app.cat.id === sel;
}

// ---------- 单设备统计 ----------

export function deviceStats(m, dev, mask, sel) {
  const nc = m.cats.length;
  const s = series(m, dev, sel);
  let active = 0;
  let ent = 0;
  let picked = 0;
  let up = 0;
  let down = 0;
  const catP = new Float64Array(nc);
  for (let b = 0; b < m.B; b++) {
    if (!mask[b] || !dev.active[b]) continue;
    active += dev.active[b];
    ent += dev.ent[b];
    picked += s[b];
    up += dev.up[b];
    down += dev.down[b];
    for (let c = 0; c < nc; c++) catP[c] += dev.cat[c][b];
  }

  const byApp = new Map();
  for (const r of dev.appRows) {
    if (!mask[r.b]) continue;
    const x = byApp.get(r.a);
    if (x) {
      x.p += r.p;
      x.bytes += r.bytes;
    } else {
      byApp.set(r.a, { app: m.apps[r.a], p: r.p, bytes: r.bytes });
    }
  }
  const appsAll = [...byApp.values()]
    .map((x) => ({ app: x.app, minutes: x.p * m.perMin, bytes: x.bytes }))
    .sort((a, b) => b.minutes - a.minutes || b.bytes - a.bytes);
  const apps = sel === 'all' ? appsAll : appsAll.filter((a) => appMatches(a.app, sel));
  let pickedBytes = 0;
  for (const a of apps) pickedBytes += a.bytes;

  const catMin = Array.from(catP, (p) => p * m.perMin);
  const game = m.catById.game ? catMin[m.catById.game.index] : 0;
  return {
    dev,
    active: active * m.perMin,
    ent: ent * m.perMin,
    game,
    picked: picked * m.perMin,
    up,
    down,
    bytes: up + down,
    pickedBytes,
    catMin,
    apps,
    appsAll,
  };
}

// foreground 过滤掉背景类应用，若只剩背景流量则原样返回。
export function foreground(apps) {
  const fg = apps.filter((x) => !x.app.cat.background);
  return fg.length ? fg : apps;
}

// dominantCat 返回若干时段内的主要类别：优先非背景类别，再比较活跃周期数。
export function dominantCat(m, dev, buckets, sel) {
  if (sel !== 'all' && sel !== 'ent') return m.catById[sel];
  let best = null;
  let bestScore = 0;
  for (const c of m.cats) {
    if (sel === 'ent' && !c.ent) continue;
    let p = 0;
    for (const b of buckets) p += dev.cat[c.index][b];
    if (!p) continue;
    const score = p + (c.background ? 0 : 1e6);
    if (score > bestScore) {
      bestScore = score;
      best = c;
    }
  }
  return best;
}

// appsIn 统计设备在指定时段集合内的应用，按时长倒序。
export function appsIn(m, dev, buckets, sel = 'all') {
  const set = buckets instanceof Set ? buckets : new Set(buckets);
  const byApp = new Map();
  for (const r of dev.appRows) {
    if (!set.has(r.b)) continue;
    const app = m.apps[r.a];
    if (!appMatches(app, sel)) continue;
    const x = byApp.get(r.a) || { app, p: 0, bytes: 0 };
    x.p += r.p;
    x.bytes += r.bytes;
    byApp.set(r.a, x);
  }
  return [...byApp.values()]
    .map((x) => ({ app: x.app, minutes: x.p * m.perMin, bytes: x.bytes }))
    .sort((a, b) => b.minutes - a.minutes || b.bytes - a.bytes);
}

// sessions 把连续活跃的时段合并为一次"上网时段"；中间空闲 gap 个时段以上才算断开。
export function sessions(m, s, mask, gap) {
  const out = [];
  let cur = null;
  let idle = 0;
  for (let b = 0; b < m.B; b++) {
    if (mask[b] && s[b] > 0) {
      if (!cur) cur = { from: b, to: b, p: 0 };
      cur.to = b;
      cur.p += s[b];
      idle = 0;
    } else if (cur && ++idle >= gap) {
      out.push(cur);
      cur = null;
      idle = 0;
    }
  }
  if (cur) out.push(cur);
  return out.map((x) => ({
    ...x,
    start: bucketStart(m, x.from),
    end: bucketEnd(m, x.to),
    minutes: x.p * m.perMin,
  }));
}

// ---------- 全家汇总 ----------

export function categoryTotals(m, list) {
  const rows = m.cats.map((cat) => ({ cat, minutes: 0, bytes: 0, devices: [] }));
  for (const st of list) {
    st.catMin.forEach((min, c) => {
      if (min > 0) {
        rows[c].minutes += min;
        rows[c].devices.push({ dev: st.dev, minutes: min });
      }
    });
    for (const a of st.appsAll) rows[a.app.cat.index].bytes += a.bytes;
  }
  for (const r of rows) r.devices.sort((a, b) => b.minutes - a.minutes);
  return rows
    .filter((r) => r.minutes > 0 || r.bytes > 0)
    .sort((a, b) => a.cat.background - b.cat.background || b.minutes - a.minutes || b.bytes - a.bytes);
}

export function appTotals(list) {
  const byApp = new Map();
  for (const st of list) {
    for (const a of st.apps) {
      const x = byApp.get(a.app.index) || { app: a.app, minutes: 0, bytes: 0, devices: [] };
      x.minutes += a.minutes;
      x.bytes += a.bytes;
      x.devices.push({ dev: st.dev, minutes: a.minutes });
      byApp.set(a.app.index, x);
    }
  }
  const rows = [...byApp.values()];
  for (const r of rows) r.devices.sort((a, b) => b.minutes - a.minutes);
  return rows;
}

// ---------- 关注提醒 ----------

export const THRESHOLDS = {
  gameDaily: 60,
  gameHeavy: 120,
  gameStreak: 45,
  shortVideoDaily: 60,
  lateNightEnt: 10,
};

export function insights(m, dayStats) {
  const out = [];
  const game = m.catById.game;
  const sv = m.catById.shortvideo;
  const night = rangeMask(m, 'night');
  for (const st of dayStats) {
    const dev = st.dev;
    if (game && st.game >= THRESHOLDS.gameDaily) {
      out.push({
        level: st.game >= THRESHOLDS.gameHeavy ? 'danger' : 'warn',
        icon: '🎮',
        dev,
        score: st.game * 2,
        title: `${dev.name} 今天玩游戏 ${fmtMin(st.game)}`,
        sub: topNames(st.apps.filter((a) => a.app.cat.id === 'game')),
      });
    }
    if (game) {
      const streak = sessions(m, dev.cat[game.index], m.fullMask, 1)
        .map((s) => ({ ...s, span: (s.end - s.start) / 60 }))
        .sort((a, b) => b.span - a.span)[0];
      if (streak && streak.span >= THRESHOLDS.gameStreak) {
        out.push({
          level: 'warn',
          icon: '⏱',
          dev,
          score: streak.span * 1.5,
          title: `${dev.name} 连续玩游戏约 ${fmtMin(streak.span)}`,
          sub: `${m.clock(streak.start)}–${m.clock(streak.end)}，其间有流量 ${fmtMin(streak.minutes)}`,
          session: streak,
        });
      }
    }
    if (sv) {
      const min = st.catMin[sv.index];
      if (min >= THRESHOLDS.shortVideoDaily) {
        out.push({
          level: 'warn',
          icon: '📱',
          dev,
          score: min,
          title: `${dev.name} 今天刷短视频 ${fmtMin(min)}`,
          sub: topNames(st.apps.filter((a) => a.app.cat.id === 'shortvideo')),
        });
      }
    }
    let lateP = 0;
    let lastB = -1;
    for (let b = 0; b < m.B; b++) {
      if (night[b] && dev.ent[b] > 0) {
        lateP += dev.ent[b];
        lastB = b;
      }
    }
    const late = lateP * m.perMin;
    if (late >= THRESHOLDS.lateNightEnt) {
      out.push({
        level: 'danger',
        icon: '🌙',
        dev,
        score: late * 3,
        title: `${dev.name} 深夜娱乐 ${fmtMin(late)}`,
        sub: `22:00–06:00 期间，最晚 ${m.clock(bucketEnd(m, lastB))}`,
      });
    }
  }
  return out.sort((a, b) => b.score - a.score);
}

function topNames(apps) {
  return apps
    .slice(0, 3)
    .map((a) => a.app.name)
    .join('、');
}
