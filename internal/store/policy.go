package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/policy/calendar"
)

//go:embed policy_migrations/*.sql
var policyMigrationFS embed.FS

const policyFile = "policy.db"

// PolicyPath 返回 DataDir 下策略库的路径。
func PolicyPath(dataDir string) string {
	return filepath.Join(dataDir, policyFile)
}

// PolicyRecord 是一台设备的策略原文。Spec 为 JSON。
type PolicyRecord struct {
	MAC         string
	Enabled     bool
	Paused      bool
	ExtendUntil int64
	Spec        string
	UpdatedAt   int64
}

// PolicyStore 保存年度日历、寒暑假和每台设备的策略。
type PolicyStore struct {
	log *slog.Logger
	mu  sync.Mutex
	db  *sql.DB
}

// OpenPolicy 打开或创建策略库。
func OpenPolicy(ctx context.Context, path string, log *slog.Logger) (*PolicyStore, error) {
	if path == "" {
		return nil, errors.New("store: policy path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("store: open policy: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrateFS(ctx, db, policyMigrationFS, "policy_migrations/*.sql"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate policy: %w", err)
	}
	s := &PolicyStore{log: log, db: db}
	if err := s.EnsureDefaultPassword(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: default password: %w", err)
	}
	if log != nil {
		log.Info("policy store opened", "file", path)
	}
	return s, nil
}

// Close 关闭策略库。
func (s *PolicyStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Year 读取某年的法定日历。
func (s *PolicyStore) Year(ctx context.Context, year int) (calendar.Year, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var y calendar.Year
	err := s.db.QueryRowContext(ctx, `
		SELECT year, region, source, document, published, source_url, imported_at
		FROM calendar_year WHERE year = ?`, year).Scan(
		&y.Meta.Year, &y.Meta.Region, &y.Meta.Source, &y.Meta.Document,
		&y.Meta.Published, &y.Meta.SourceURL, &y.Meta.ImportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return calendar.Year{}, calendar.ErrNotFound
	}
	if err != nil {
		return calendar.Year{}, fmt.Errorf("store: load calendar: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT date, weekday, workday, name, kind FROM calendar_day
		WHERE year = ? ORDER BY date`, year)
	if err != nil {
		return calendar.Year{}, fmt.Errorf("store: load calendar days: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d calendar.Day
		var work int
		if err := rows.Scan(&d.Date, &d.Weekday, &work, &d.Name, &d.Kind); err != nil {
			return calendar.Year{}, fmt.Errorf("store: scan calendar day: %w", err)
		}
		d.Workday = work == 1
		y.Days = append(y.Days, d)
	}
	if err := rows.Err(); err != nil {
		return calendar.Year{}, err
	}
	y.Official = true
	y.Reindex()
	return y, nil
}

// SaveCalendar 覆盖该年的法定日历。vac 非 nil 时同时覆盖寒暑假。
func (s *PolicyStore) SaveCalendar(ctx context.Context, y calendar.Year, vac *calendar.Vacations, now time.Time) error {
	if vac != nil {
		if err := vac.Validate(); err != nil {
			return err
		}
	}
	if y.Meta.Year == 0 || len(y.Days) == 0 {
		return errors.New("store: empty calendar")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_day WHERE year = ?`, y.Meta.Year); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_year WHERE year = ?`, y.Meta.Year); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO calendar_year (year, region, source, document, published, source_url, imported_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		y.Meta.Year, y.Meta.Region, y.Meta.Source, y.Meta.Document, y.Meta.Published, y.Meta.SourceURL, now.Unix()); err != nil {
		return err
	}
	for _, d := range y.Days {
		work := 0
		if d.Workday {
			work = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO calendar_day (date, year, weekday, workday, name, kind)
			VALUES (?, ?, ?, ?, ?, ?)`, d.Date, y.Meta.Year, d.Weekday, work, d.Name, d.Kind); err != nil {
			return err
		}
	}
	if vac != nil {
		if err := writeVacations(ctx, tx, *vac); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Vacations 读取寒暑假，未设置时区间为空。
func (s *PolicyStore) Vacations(ctx context.Context) (calendar.Vacations, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT kind, start_date, end_date FROM vacation`)
	if err != nil {
		return calendar.Vacations{}, err
	}
	defer rows.Close()
	var v calendar.Vacations
	for rows.Next() {
		var kind, start, end string
		if err := rows.Scan(&kind, &start, &end); err != nil {
			return calendar.Vacations{}, err
		}
		switch kind {
		case "summer":
			v.Summer = calendar.Span{Start: start, End: end}
		case "winter":
			v.Winter = calendar.Span{Start: start, End: end}
		}
	}
	return v, rows.Err()
}

// SaveVacations 覆盖寒暑假。
func (s *PolicyStore) SaveVacations(ctx context.Context, v calendar.Vacations) error {
	if err := v.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeVacations(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

func writeVacations(ctx context.Context, tx *sql.Tx, v calendar.Vacations) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vacation`); err != nil {
		return err
	}
	for _, row := range []struct {
		kind string
		span calendar.Span
	}{{"summer", v.Summer}, {"winter", v.Winter}} {
		if row.span.Empty() {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO vacation (kind, start_date, end_date) VALUES (?, ?, ?)`,
			row.kind, row.span.Start, row.span.End); err != nil {
			return err
		}
	}
	return nil
}

// Policies 返回全部设备策略。
func (s *PolicyStore) Policies(ctx context.Context) ([]PolicyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT mac, enabled, paused, extend_until, spec, updated_at FROM device_policy ORDER BY mac`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PolicyRecord
	for rows.Next() {
		var r PolicyRecord
		var enabled, paused int
		if err := rows.Scan(&r.MAC, &enabled, &paused, &r.ExtendUntil, &r.Spec, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled, r.Paused = enabled == 1, paused == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveSpec 写入规则。关闭管控时同时取消暂停和临时延长；开启时保留这两项。
func (s *PolicyStore) SaveSpec(ctx context.Context, mac string, enabled bool, spec string, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_policy (mac, enabled, paused, extend_until, spec, updated_at)
		VALUES (?, ?, 0, 0, ?, ?)
		ON CONFLICT(mac) DO UPDATE SET
			enabled = excluded.enabled,
			paused = CASE WHEN excluded.enabled = 0 THEN 0 ELSE paused END,
			extend_until = CASE WHEN excluded.enabled = 0 THEN 0 ELSE extend_until END,
			spec = excluded.spec,
			updated_at = excluded.updated_at`, mac, en, spec, now)
	return err
}

// SetPause 切换暂停。没有策略行时会新建一条未启用的记录，方便一键暂停。
func (s *PolicyStore) SetPause(ctx context.Context, mac string, paused bool, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := 0
	if paused {
		p = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_policy (mac, enabled, paused, extend_until, spec, updated_at)
		VALUES (?, 0, ?, 0, '{"schedule":{"mode":"off"}}', ?)
		ON CONFLICT(mac) DO UPDATE SET paused = excluded.paused, updated_at = excluded.updated_at`,
		mac, p, now)
	return err
}

// SetExtend 设置临时延长的结束时刻，0 表示取消。设备还没有策略时返回 sql.ErrNoRows。
func (s *PolicyStore) SetExtend(ctx context.Context, mac string, until, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `
		UPDATE device_policy SET extend_until = ?, updated_at = ? WHERE mac = ?`, until, now, mac)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClearExpiredExtends 清掉已经结束的临时延长。
func (s *PolicyStore) ClearExpiredExtends(ctx context.Context, now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		UPDATE device_policy SET extend_until = 0 WHERE extend_until > 0 AND extend_until <= ?`, now)
	return err
}
