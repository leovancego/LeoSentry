package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"time"
)

// UsageRow 是 today.db 中的一行明细（已解码为内存表示）。
type UsageRow struct {
	CollectedAt int64
	DeviceMAC   string
	DeviceIP    netip.Addr
	TargetIP    netip.Addr
	Domain      string
	// DomainInferred 为 true 表示本行入库时没有域名，Domain 取自当天同一目标 IP 最近一次有域名的记录。
	// 长连接在 DNS Map 条目过期后仍会持续产生流量，借此补回这部分行的域名。
	DomainInferred bool
	BytesUp        int64
	BytesDown      int64
}

// 按 collected_at 升序返回当天全部明细；没有域名的行用同一 target_ip 最近一次出现的域名补全。
// 排序可使用唯一约束的索引（collected_at 为首列），无需额外排序。
const scanUsage = `
SELECT r.collected_at, r.device_mac, r.device_ip, r.target_ip, r.domain, k.domain, r.bytes_up, r.bytes_down
FROM usage_records r
LEFT JOIN (
    SELECT target_ip, domain FROM usage_records
    WHERE id IN (SELECT MAX(id) FROM usage_records WHERE domain IS NOT NULL GROUP BY target_ip)
) k ON r.domain IS NULL AND k.target_ip = r.target_ip
ORDER BY r.collected_at`

// ScanDay 先以 today.db 所属统计日的起始时刻调用一次 begin，再依次回调当天的全部明细。
// 扫描期间持有存储锁，保证读到的是同一统计日的完整数据。
func (s *Store) ScanDay(ctx context.Context, begin func(dayStart time.Time), fn func(*UsageRow) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errClosed
	}
	begin(s.dayStart)
	return scanDB(ctx, s.db, fn)
}

// ScanYesterday 扫描前一个统计日。没有昨天的库，或库不属于昨天时，只调用 begin 后返回空结果。
func (s *Store) ScanYesterday(ctx context.Context, begin func(dayStart time.Time), fn func(*UsageRow) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	yesterday := s.dayStart.AddDate(0, 0, -1)
	begin(yesterday)
	db, err := sqlOpenRO(s.prevPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("store: open previous day: %w", err)
	}
	defer db.Close()
	start, err := readDayStart(ctx, db)
	if err != nil || !start.Equal(yesterday) {
		return err
	}
	return scanDB(ctx, db, fn)
}

func scanDB(ctx context.Context, db *sql.DB, fn func(*UsageRow) error) error {
	rows, err := db.QueryContext(ctx, scanUsage)
	if err != nil {
		return fmt.Errorf("store: scan usage: %w", err)
	}
	defer rows.Close()

	macs := make(map[int64]string)
	var (
		r                       UsageRow
		mac, deviceIP, targetIP int64
		domain, inferred        sql.NullString
	)
	for rows.Next() {
		if err := rows.Scan(&r.CollectedAt, &mac, &deviceIP, &targetIP, &domain, &inferred, &r.BytesUp, &r.BytesDown); err != nil {
			return fmt.Errorf("store: scan usage: %w", err)
		}
		text, ok := macs[mac]
		if !ok {
			text = decodeMAC(mac)
			macs[mac] = text
		}
		r.DeviceMAC = text
		r.DeviceIP, r.TargetIP = decodeIPv4(deviceIP), decodeIPv4(targetIP)
		r.Domain, r.DomainInferred = domain.String, false
		if !domain.Valid && inferred.Valid {
			r.Domain, r.DomainInferred = inferred.String, true
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: scan usage: %w", err)
	}
	return nil
}

// Version 在每次写入明细或切换统计日后递增，供上层缓存判断数据是否变化。
func (s *Store) Version() uint64 { return s.version.Load() }

// DeviceSighting 是 today.db 中某台设备使用过的一个地址及其时间范围。
type DeviceSighting struct {
	MAC   string
	IP    netip.Addr
	First int64
	Last  int64
}

// DeviceSightings 按 (MAC, IP) 汇总当天最早与最晚出现时间，供设备身份库回填。
func (s *Store) DeviceSightings(ctx context.Context) ([]DeviceSighting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, errClosed
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT device_mac, device_ip, MIN(collected_at), MAX(collected_at)
		FROM usage_records
		WHERE device_mac != 0
		GROUP BY device_mac, device_ip`)
	if err != nil {
		return nil, fmt.Errorf("store: device sightings: %w", err)
	}
	defer rows.Close()
	var out []DeviceSighting
	for rows.Next() {
		var mac, ip, first, last int64
		if err := rows.Scan(&mac, &ip, &first, &last); err != nil {
			return nil, fmt.Errorf("store: scan device sighting: %w", err)
		}
		out = append(out, DeviceSighting{
			MAC:   decodeMAC(mac),
			IP:    decodeIPv4(ip),
			First: first,
			Last:  last,
		})
	}
	return out, rows.Err()
}
