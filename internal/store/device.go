package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/leo/leosentry/internal/model"
)

const (
	devicesFile            = "devices.db"
	defaultPersistInterval = time.Hour
	maxDeviceNameRunes     = 40
)

// ErrNameTooLong 表示用户起的名字超过允许长度。
var ErrNameTooLong = errors.New("device name too long")

// Registry 维护不随统计日切换的设备身份：别名、首次出现、历史 IP。
// 新设备 / 新地址立即落盘；同一地址的 last_seen 按 PersistEvery 节流，减少闪存写入。
type Registry struct {
	log          *slog.Logger
	persistEvery time.Duration
	now          func() time.Time

	mu     sync.Mutex
	db     *sql.DB
	byMAC  map[int64]*devRec
	closed bool
	ver    atomic.Uint64
}

type devRec struct {
	name      string
	firstSeen int64
	lastSeen  int64
	lastIP    int64
	flushedAt int64
	metaDirty bool
	ips       map[int64]*ipRec
}

type ipRec struct {
	firstSeen int64
	lastSeen  int64
	flushedAt int64
	dirty     bool
}

// RegistryOptions 配置设备身份库。
type RegistryOptions struct {
	Path string
	// PersistEvery 是 last_seen 落盘间隔，默认 1 小时；0 表示每次观察都写。
	PersistEvery time.Duration
	Logger       *slog.Logger

	now func() time.Time
}

// DevicesPath 返回 DataDir 下设备身份库的路径。
func DevicesPath(dataDir string) string {
	return filepath.Join(dataDir, devicesFile)
}

// OpenRegistry 打开（或创建）设备身份库，并载入内存。
func OpenRegistry(ctx context.Context, opts RegistryOptions) (*Registry, error) {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.PersistEvery == 0 {
		opts.PersistEvery = defaultPersistInterval
	}
	if opts.Path == "" {
		return nil, errors.New("store: devices path required")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+opts.Path+dsnParams)
	if err != nil {
		return nil, fmt.Errorf("store: open devices: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrateFS(ctx, db, deviceMigrationFS, "device_migrations/*.sql"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate devices: %w", err)
	}
	r := &Registry{
		log:          opts.Logger,
		persistEvery: opts.PersistEvery,
		now:          opts.now,
		db:           db,
		byMAC:        map[int64]*devRec{},
	}
	if err := r.load(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if r.log != nil {
		r.log.Info("device registry opened", "file", opts.Path, "devices", len(r.byMAC))
	}
	return r, nil
}

func (r *Registry) load(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT mac, name, first_seen, last_seen, last_ip FROM devices`)
	if err != nil {
		return fmt.Errorf("store: load devices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, first, last, ip int64
		var name string
		if err := rows.Scan(&key, &name, &first, &last, &ip); err != nil {
			return fmt.Errorf("store: scan device: %w", err)
		}
		r.byMAC[key] = &devRec{
			name: name, firstSeen: first, lastSeen: last, lastIP: ip,
			flushedAt: last, ips: map[int64]*ipRec{},
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	ipRows, err := r.db.QueryContext(ctx, `SELECT mac, ip, first_seen, last_seen FROM device_ips`)
	if err != nil {
		return fmt.Errorf("store: load device ips: %w", err)
	}
	defer ipRows.Close()
	for ipRows.Next() {
		var mac, ip, first, last int64
		if err := ipRows.Scan(&mac, &ip, &first, &last); err != nil {
			return fmt.Errorf("store: scan device ip: %w", err)
		}
		d := r.byMAC[mac]
		if d == nil {
			continue
		}
		d.ips[ip] = &ipRec{firstSeen: first, lastSeen: last, flushedAt: last}
	}
	return ipRows.Err()
}

// Close 把尚未落盘的观察写入后关闭。
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.db == nil {
		return nil
	}
	for _, d := range r.byMAC {
		if d.lastSeen != d.flushedAt {
			d.metaDirty = true
		}
		for _, p := range d.ips {
			if p.lastSeen != p.flushedAt {
				p.dirty = true
			}
		}
	}
	err := r.flushLocked(context.Background())
	if _, e := r.db.Exec("PRAGMA journal_mode=DELETE"); e != nil {
		err = errors.Join(err, e)
	}
	err = errors.Join(err, r.db.Close())
	r.db, r.closed = nil, true
	return err
}

// Name 实现 collector.Sink。
func (r *Registry) Name() string { return "devices" }

// Consume 从采集批次中观察设备 MAC 与当时的 IP。
func (r *Registry) Consume(ctx context.Context, batch model.UsageBatch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	for i := range batch.Events {
		ev := &batch.Events[i]
		r.noteLocked(ev.DeviceMAC, ev.DeviceIP, ev.CollectedAt)
	}
	return r.flushLocked(ctx)
}

// Observe 记录一次对某台设备的观察。mac 无法解析时忽略。
func (r *Registry) Observe(mac string, ip netip.Addr, at int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.noteLocked(mac, ip, at)
}

// Flush 把脏数据写入磁盘。
func (r *Registry) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	return r.flushLocked(ctx)
}

func (r *Registry) noteLocked(mac string, ip netip.Addr, at int64) {
	key := encodeMAC(mac)
	if key == 0 || at <= 0 {
		return
	}
	ipn := encodeIPv4(ip)
	d := r.byMAC[key]
	if d == nil {
		d = &devRec{firstSeen: at, lastSeen: at, lastIP: ipn, metaDirty: true, ips: map[int64]*ipRec{}}
		r.byMAC[key] = d
	} else {
		if at < d.firstSeen {
			d.firstSeen = at
			d.metaDirty = true
		}
		if at > d.lastSeen {
			d.lastSeen = at
			if r.shouldPersist(d, at) {
				d.metaDirty = true
			}
		}
		if ipn != 0 && ipn != d.lastIP {
			d.lastIP = ipn
			d.metaDirty = true
		}
	}
	if ipn == 0 {
		return
	}
	p := d.ips[ipn]
	if p == nil {
		d.ips[ipn] = &ipRec{firstSeen: at, lastSeen: at, dirty: true}
		return
	}
	if at < p.firstSeen {
		p.firstSeen = at
		p.dirty = true
	}
	if at > p.lastSeen {
		p.lastSeen = at
		if r.shouldPersist(d, at) {
			p.dirty = true
		}
	}
}

func (r *Registry) shouldPersist(d *devRec, at int64) bool {
	if r.persistEvery == 0 {
		return true
	}
	return at-d.flushedAt >= int64(r.persistEvery/time.Second)
}

func (r *Registry) flushLocked(ctx context.Context) error {
	type item struct {
		key int64
		d   *devRec
	}
	var dirty []item
	for key, d := range r.byMAC {
		need := d.metaDirty
		if !need {
			for _, p := range d.ips {
				if p.dirty {
					need = true
					break
				}
			}
		}
		if need {
			dirty = append(dirty, item{key, d})
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin devices: %w", err)
	}
	defer tx.Rollback()
	upDev, err := tx.PrepareContext(ctx, `
		INSERT INTO devices (mac, name, first_seen, last_seen, last_ip)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mac) DO UPDATE SET
			first_seen = excluded.first_seen,
			last_seen  = excluded.last_seen,
			last_ip    = excluded.last_ip`)
	if err != nil {
		return err
	}
	defer upDev.Close()
	upIP, err := tx.PrepareContext(ctx, `
		INSERT INTO device_ips (mac, ip, first_seen, last_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (mac, ip) DO UPDATE SET
			first_seen = excluded.first_seen,
			last_seen  = excluded.last_seen`)
	if err != nil {
		return err
	}
	defer upIP.Close()
	for _, it := range dirty {
		d := it.d
		if d.metaDirty {
			if _, err := upDev.ExecContext(ctx, it.key, d.name, d.firstSeen, d.lastSeen, d.lastIP); err != nil {
				return fmt.Errorf("store: upsert device: %w", err)
			}
			d.metaDirty = false
			d.flushedAt = d.lastSeen
		}
		for ip, p := range d.ips {
			if !p.dirty {
				continue
			}
			if _, err := upIP.ExecContext(ctx, it.key, ip, p.firstSeen, p.lastSeen); err != nil {
				return fmt.Errorf("store: upsert device ip: %w", err)
			}
			p.dirty = false
			p.flushedAt = p.lastSeen
		}
	}
	return tx.Commit()
}

// SetName 设置或清空用户给设备起的名字。mac 无法解析时返回 nil。
func (r *Registry) SetName(ctx context.Context, mac, name string) error {
	name, err := normalizeDeviceName(name)
	if err != nil {
		return err
	}
	key := encodeMAC(mac)
	if key == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	now := r.now().Unix()
	d := r.byMAC[key]
	if d == nil {
		d = &devRec{firstSeen: now, lastSeen: now, ips: map[int64]*ipRec{}, metaDirty: true}
		r.byMAC[key] = d
	}
	if d.name == name {
		return nil
	}
	d.name = name
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO devices (mac, name, first_seen, last_seen, last_ip)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mac) DO UPDATE SET name = excluded.name`,
		key, name, d.firstSeen, d.lastSeen, d.lastIP); err != nil {
		return fmt.Errorf("store: set device name: %w", err)
	}
	r.ver.Add(1)
	return nil
}

// LookupName 返回用户给该 MAC 起的名字，未命名或未知时为空串。
func (r *Registry) LookupName(mac string) string {
	key := encodeMAC(mac)
	if key == 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.byMAC[key]
	if d == nil {
		return ""
	}
	return d.name
}

// Version 在用户改名后递增，供概览缓存判断是否需要重算设备显示名。
func (r *Registry) Version() uint64 { return r.ver.Load() }

// All 返回当前已知的全部设备（含尚未落盘的观察）。
func (r *Registry) All() []model.KnownDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.KnownDevice, 0, len(r.byMAC))
	for key, d := range r.byMAC {
		kd := model.KnownDevice{
			MAC:       decodeMAC(key),
			Name:      d.name,
			FirstSeen: d.firstSeen,
			LastSeen:  d.lastSeen,
		}
		if d.lastIP != 0 {
			kd.LastIP = decodeIPv4(d.lastIP)
		}
		for ip, p := range d.ips {
			kd.IPs = append(kd.IPs, model.KnownIP{
				IP:        decodeIPv4(ip),
				FirstSeen: p.firstSeen,
				LastSeen:  p.lastSeen,
			})
		}
		slices.SortFunc(kd.IPs, func(a, b model.KnownIP) int {
			return cmp.Or(cmp.Compare(b.LastSeen, a.LastSeen), a.IP.Compare(b.IP))
		})
		out = append(out, kd)
	}
	slices.SortFunc(out, func(a, b model.KnownDevice) int {
		return cmp.Or(cmp.Compare(b.LastSeen, a.LastSeen), cmp.Compare(a.MAC, b.MAC))
	})
	return out
}

func normalizeDeviceName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxDeviceNameRunes {
		return "", ErrNameTooLong
	}
	return s, nil
}
