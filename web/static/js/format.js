// 纯展示用的格式化函数。所有时间都按路由器时区（tzOffset）显示，不依赖访问设备的时区。

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

// esc 转义插入 HTML 的文本；主机名来自 DHCP，可被局域网设备任意设置，必须转义。
export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
}

export function duration(minutes) {
  const m = Math.round(minutes);
  if (m <= 0) return '0 分钟';
  if (m < 60) return `${m} 分钟`;
  const h = Math.floor(m / 60);
  const r = m % 60;
  return r ? `${h} 小时 ${r} 分` : `${h} 小时`;
}

// shortDuration 用于空间紧张的位置，例如 "2h05m"、"45m"。
export function shortDuration(minutes) {
  const m = Math.round(minutes);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  const r = m % 60;
  return r ? `${h}h${String(r).padStart(2, '0')}m` : `${h}h`;
}

export function bytes(n) {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

function shifted(unix, tz) {
  return new Date((unix + tz) * 1000);
}

// clock 返回 "HH:MM"。
export function clock(unix, tz) {
  const d = shifted(unix, tz);
  return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
}

// localHour 返回路由器时区下的小时 (0-23)。
export function localHour(unix, tz) {
  return shifted(unix, tz).getUTCHours();
}

export function ago(unix, now) {
  const s = Math.max(0, now - unix);
  if (s < 60) return '刚刚';
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  return `${Math.floor(s / 86400)} 天前`;
}

function pad2(n) {
  return String(n).padStart(2, '0');
}

// datetime 返回路由器时区下的 "YYYY-MM-DD HH:MM"；与 now 同一天时写成 "今天 HH:MM"。
export function datetime(unix, tz, now) {
  if (!unix) return '—';
  const d = shifted(unix, tz);
  const hm = `${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}`;
  if (now) {
    const n = shifted(now, tz);
    if (d.getUTCFullYear() === n.getUTCFullYear() && d.getUTCMonth() === n.getUTCMonth() && d.getUTCDate() === n.getUTCDate()) {
      return `今天 ${hm}`;
    }
  }
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())} ${hm}`;
}

export function pct(part, total) {
  return total > 0 ? Math.round((part / total) * 100) : 0;
}
