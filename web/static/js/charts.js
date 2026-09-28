// 纯字符串拼接的轻量 SVG 图表，不依赖第三方库。

import { esc, duration } from './format.js';
import { bucketStart, dominantCat } from './data.js';

// stackBar 渲染按比例分段的横条。segments: [{ color, value, title }]
export function stackBar(segments, cls = '') {
  const total = segments.reduce((s, x) => s + x.value, 0);
  if (!total) return `<div class="stackbar ${cls}"></div>`;
  const parts = segments
    .filter((x) => x.value > 0)
    .map((x) => `<span style="width:${((x.value / total) * 100).toFixed(2)}%;background:${x.color}" title="${esc(x.title)}"></span>`)
    .join('');
  return `<div class="stackbar ${cls}">${parts}</div>`;
}

// columns 把选中的时段组织成图表列；hourly 为 true 时每列合并为 1 小时。
export function columns(m, mask, hourly) {
  const cols = [];
  const per = Math.max(1, Math.round(3600 / m.bucketSeconds));
  let key = -1;
  for (let b = 0; b < m.B; b++) {
    if (!mask[b]) continue;
    const k = hourly ? Math.floor(b / per) : b;
    if (k !== key) {
      cols.push([]);
      key = k;
    }
    cols[cols.length - 1].push(b);
  }
  return cols;
}

// timeline 渲染单个设备的一行时间线：颜色为该列的主要类别，深浅为活跃程度。
export function timeline(m, dev, cols, sel, series, cls) {
  const n = cols.length;
  if (!n) return '';
  const gap = n <= 48 ? 0.14 : 0;
  let cells = '';
  for (let i = 0; i < n; i++) {
    const col = cols[i];
    let p = 0;
    for (const b of col) p += series[b];
    const w = (1 - gap).toFixed(2);
    if (!p) {
      cells += `<rect class="e" x="${i}" y="0" width="${w}" height="1"/>`;
      continue;
    }
    const cap = (col.length * m.bucketSeconds) / 60;
    const level = Math.min(1, (p * m.perMin) / cap);
    const cat = dominantCat(m, dev, col, sel);
    const color = cat ? cat.color : '#98a2b3';
    cells += `<rect x="${i}" y="0" width="${w}" height="1" fill="${color}" fill-opacity="${(0.3 + 0.7 * level).toFixed(2)}"/>`;
  }
  return `<svg class="${cls}" viewBox="0 0 ${n} 1" preserveAspectRatio="none" data-dev="${dev.index}">${cells}</svg>`;
}

// axis 生成时间轴刻度，最多 ticks+1 个标签。
export function axis(m, cols, ticks) {
  if (!cols.length) return '';
  const n = cols.length;
  const k = Math.min(ticks, n);
  const labels = [];
  for (let i = 0; i < k; i++) labels.push(m.clock(bucketStart(m, cols[Math.floor((i * n) / k)][0])));
  const last = cols[n - 1];
  labels.push(m.clock(bucketStart(m, last[last.length - 1] + 1)));
  return `<div class="axis">${labels.map((l) => `<span>${l}</span>`).join('')}</div>`;
}

// hourChart 渲染设备全天每小时的活跃分钟数，柱内按类别占比分段。
export function hourChart(m, dev) {
  const per = Math.max(1, Math.round(3600 / m.bucketSeconds));
  const hours = Math.ceil(m.B / per);
  const W = 480;
  const H = 150;
  const top = 8;
  const bottom = 18;
  const plot = H - top - bottom;
  const bw = W / hours;
  let bars = '';
  for (let h = 0; h < hours; h++) {
    let active = 0;
    const catP = new Float64Array(m.cats.length);
    for (let b = h * per; b < Math.min(m.B, (h + 1) * per); b++) {
      active += dev.active[b];
      for (const c of m.cats) catP[c.index] += dev.cat[c.index][b];
    }
    const minutes = active * m.perMin;
    if (!minutes) continue;
    const title = `${m.clock(bucketStart(m, h * per))} 活跃 ${duration(minutes)}`;
    // 同一分钟内多个类别可能同时活跃，按"前台行为优先"依次填满柱高，背景流量只填剩余部分。
    let left = active;
    let y = top + plot;
    const order = m.cats.filter((c) => catP[c.index] > 0).sort((a, b) => a.background - b.background || catP[b.index] - catP[a.index]);
    for (const c of order) {
      const p = Math.min(left, catP[c.index]);
      if (p <= 0) break;
      left -= p;
      const seg = (Math.min(60, p * m.perMin) / 60) * plot;
      y -= seg;
      bars += `<rect x="${(h * bw + 1).toFixed(1)}" y="${y.toFixed(1)}" width="${(bw - 2).toFixed(1)}" height="${seg.toFixed(1)}" fill="${c.color}"><title>${esc(title)} · ${esc(c.name)} ${duration(catP[c.index] * m.perMin)}</title></rect>`;
    }
  }
  let grid = '';
  for (const v of [30, 60]) {
    const y = top + plot - (v / 60) * plot;
    grid += `<line x1="0" x2="${W}" y1="${y}" y2="${y}" stroke="currentColor" stroke-opacity=".12" stroke-dasharray="2 3"/>`;
    grid += `<text x="${W - 2}" y="${y - 2}" text-anchor="end">${v}m</text>`;
  }
  let labels = '';
  for (let h = 0; h < hours; h += 3) {
    labels += `<text x="${(h * bw + bw / 2).toFixed(1)}" y="${H - 4}" text-anchor="middle">${m.clock(bucketStart(m, h * per)).slice(0, 2)}</text>`;
  }
  return `<svg class="hour-chart" viewBox="0 0 ${W} ${H}">${grid}${bars}${labels}</svg>`;
}

export function legend(cats) {
  return `<div class="legend">${cats
    .map((c) => `<span><i class="sw" style="background:${c.color}"></i>${esc(c.name)}</span>`)
    .join('')}</div>`;
}
