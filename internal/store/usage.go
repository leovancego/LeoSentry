package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leo/leosentry/internal/model"
)

// 同一周期、同一设备、同一目标重复写入时（程序重启后重放同一周期）合并而非报错。
const upsertUsage = `
INSERT INTO usage_records (collected_at, device_mac, device_ip, target_ip, domain, bytes_up, bytes_down)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (collected_at, device_mac, target_ip) DO UPDATE SET
    device_ip  = excluded.device_ip,
    domain     = COALESCE(excluded.domain, usage_records.domain),
    bytes_up   = usage_records.bytes_up + excluded.bytes_up,
    bytes_down = usage_records.bytes_down + excluded.bytes_down`

// Name 实现 collector 的 sink 命名。
func (s *Store) Name() string { return "today-db" }

// Consume 在单个事务内写入一个采集周期的全部记录。
// 批次时间戳跨入新的统计日时，先切换到新的 today.db 再写入。
func (s *Store) Consume(ctx context.Context, batch model.UsageBatch) error {
	if len(batch.Events) == 0 {
		return nil
	}
	start := s.opts.Calendar.DayStart(time.Unix(batch.CollectedAt, 0))

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errClosed
	}
	if start.After(s.dayStart) {
		if err := s.rotateLocked(ctx, start); err != nil {
			return err
		}
	}
	if !s.hasFreeSpaceLocked() {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()
	stmt := tx.StmtContext(ctx, s.insert)
	for i := range batch.Events {
		ev := &batch.Events[i]
		if _, err := stmt.ExecContext(ctx,
			ev.CollectedAt,
			encodeMAC(ev.DeviceMAC),
			encodeIPv4(ev.DeviceIP),
			encodeIPv4(ev.TargetIP),
			sql.NullString{String: ev.Domain, Valid: ev.Domain != ""},
			ev.BytesUp,
			ev.BytesDown,
		); err != nil {
			return fmt.Errorf("store: insert usage record: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	s.version.Add(1)
	return nil
}

// DailyTotals 汇总 today.db，用于程序重启后恢复内存当日计数器。
// devices 按设备汇总，domains 按设备+域名汇总（未知域名为空串）。
func (s *Store) DailyTotals(ctx context.Context) (dayStart time.Time, devices, domains []model.UsageTotal, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return time.Time{}, nil, nil, errClosed
	}

	devices, err = queryTotals(ctx, s.db, `
		SELECT device_mac, '', SUM(bytes_up), SUM(bytes_down), COUNT(DISTINCT collected_at)
		FROM usage_records
		GROUP BY device_mac`)
	if err != nil {
		return time.Time{}, nil, nil, err
	}
	domains, err = queryTotals(ctx, s.db, `
		SELECT device_mac, COALESCE(domain, ''), SUM(bytes_up), SUM(bytes_down), COUNT(DISTINCT collected_at)
		FROM usage_records
		GROUP BY device_mac, COALESCE(domain, '')`)
	if err != nil {
		return time.Time{}, nil, nil, err
	}
	return s.dayStart, devices, domains, nil
}

func queryTotals(ctx context.Context, db *sql.DB, query string) ([]model.UsageTotal, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: query totals: %w", err)
	}
	defer rows.Close()
	var out []model.UsageTotal
	for rows.Next() {
		var t model.UsageTotal
		var mac int64
		if err := rows.Scan(&mac, &t.Domain, &t.BytesUp, &t.BytesDown, &t.ActivePeriods); err != nil {
			return nil, fmt.Errorf("store: scan totals: %w", err)
		}
		t.DeviceMAC = decodeMAC(mac)
		out = append(out, t)
	}
	return out, rows.Err()
}

// hasFreeSpaceLocked 检查数据目录所在分区的剩余空间。低于 MinFreeBytes 时暂停写入明细，
// 避免写满路由器根分区导致 UCI 保存、软件安装等系统功能失效；空间恢复后自动继续写入。
func (s *Store) hasFreeSpaceLocked() bool {
	if s.opts.MinFreeBytes == 0 {
		return true
	}
	free, err := s.opts.freeSpace(s.opts.DataDir)
	if err != nil {
		return true
	}
	low := free < s.opts.MinFreeBytes
	switch {
	case low && !s.diskLow:
		s.log.Warn("free space low, usage records are not written until space is freed",
			"dir", s.opts.DataDir, "free_mb", free>>20, "min_free_mb", s.opts.MinFreeBytes>>20)
	case !low && s.diskLow:
		s.log.Info("free space recovered, usage records writing resumed",
			"dir", s.opts.DataDir, "free_mb", free>>20)
	}
	s.diskLow = low
	return !low
}
