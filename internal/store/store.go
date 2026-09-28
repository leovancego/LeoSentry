package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/leo/leosentry/internal/statday"
)

const (
	todayFile    = "today.db"
	prevFile     = "prev.db"
	pendingDir   = "pending"
	metaDayStart = "day_start"
)

// sqlite 连接参数：WAL + synchronous=NORMAL 在断电时最多丢失最近一次提交，
// 但每次提交只顺序追加 WAL，显著减少闪存写入。
const dsnParams = "?_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=temp_store(MEMORY)" +
	"&_txlock=immediate"

var errClosed = errors.New("store: closed")

// Options 配置存储层。
type Options struct {
	// DataDir 位于闪存，存放 today.db 与待归档文件。
	DataDir string
	// ArchiveDir 位于外部硬盘，为空表示不归档。
	ArchiveDir      string
	ArchiveKeepDays int
	Calendar        statday.Calendar
	Logger          *slog.Logger
	// MinFreeBytes 为 DataDir 所在分区需保留的最小剩余空间，低于该值时暂停写入明细；0 表示不检查。
	MinFreeBytes uint64

	now        func() time.Time
	isExternal func(dir string) bool
	freeSpace  func(dir string) (uint64, error)
}

// Store 在闪存上保留当前统计日 today.db 和前一个统计日 prev.db。
// 进入新的统计日时，先完整删除更早的那一天，再把刚结束的一天留作昨天。
type Store struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	db       *sql.DB
	insert   *sql.Stmt
	dayStart time.Time
	diskLow  bool
	version  atomic.Uint64

	archiver *archiver
	cancel   context.CancelFunc
	done     chan struct{}
}

// Open 在程序启动时调用一次：创建目录、打开并初始化 today.db 的表结构。
// 若 today.db 属于更早的统计日（例如程序在切换时刻未运行），先完成切换再返回，
// 保证采集开始前数据库已就绪。
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.isExternal == nil {
		opts.isExternal = onExternalStorage
	}
	if opts.freeSpace == nil {
		opts.freeSpace = freeSpace
	}
	for _, dir := range []string{opts.DataDir, filepath.Join(opts.DataDir, pendingDir)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}

	s := &Store{opts: opts, log: opts.Logger}
	s.archiver = newArchiver(opts, filepath.Join(opts.DataDir, pendingDir))

	s.mu.Lock()
	err := s.openTodayLocked(ctx)
	if err == nil {
		err = s.alignDayLocked(ctx)
	}
	s.mu.Unlock()
	if err != nil {
		s.closeLocked()
		return nil, err
	}

	actx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		s.archiver.run(actx)
	}()
	s.archiver.trigger()

	s.log.Info("store opened", "file", s.todayPath(), "day", opts.Calendar.Label(s.dayStart), "schema", schemaVersion())
	return s, nil
}

// Close 停止归档任务并关闭数据库。
func (s *Store) Close() error {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

// DayStart 返回 today.db 所属统计日的起始时刻。
func (s *Store) DayStart() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dayStart
}

// ArchiveStatus 返回归档存储状态，供 Web UI 提示"未配置归档存储"等情况。
func (s *Store) ArchiveStatus() ArchiveStatus {
	return s.archiver.statusSnapshot()
}

func (s *Store) todayPath() string {
	return filepath.Join(s.opts.DataDir, todayFile)
}

func (s *Store) openTodayLocked(ctx context.Context) error {
	db, err := sql.Open("sqlite", "file:"+s.todayPath()+dsnParams)
	if err != nil {
		return fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("store: migrate %s: %w", s.todayPath(), err)
	}
	insert, err := db.PrepareContext(ctx, upsertUsage)
	if err != nil {
		db.Close()
		return fmt.Errorf("store: prepare: %w", err)
	}
	dayStart, err := readDayStart(ctx, db)
	if err != nil {
		insert.Close()
		db.Close()
		return err
	}
	s.db, s.insert, s.dayStart = db, insert, dayStart
	return nil
}

// alignDayLocked 让 today.db 与当前统计日对齐。
func (s *Store) alignDayLocked(ctx context.Context) error {
	cur := s.opts.Calendar.DayStart(s.opts.now())
	switch {
	case s.dayStart.IsZero():
		return s.setDayStartLocked(ctx, cur)
	case s.dayStart.Before(cur):
		return s.rotateLocked(ctx, cur)
	case s.dayStart.After(cur):
		s.log.Warn("today.db belongs to a future day, system clock may have moved backwards",
			"db_day", s.opts.Calendar.Label(s.dayStart), "now_day", s.opts.Calendar.Label(cur))
		return nil
	default:
		return s.prunePrevLocked()
	}
}

func (s *Store) setDayStartLocked(ctx context.Context, dayStart time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO db_meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		metaDayStart, strconv.FormatInt(dayStart.Unix(), 10))
	if err != nil {
		return fmt.Errorf("store: write %s: %w", metaDayStart, err)
	}
	s.dayStart = dayStart
	return nil
}

func readDayStart(ctx context.Context, db *sql.DB) (time.Time, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM db_meta WHERE key = ?`, metaDayStart).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("store: read %s: %w", metaDayStart, err)
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: invalid %s %q: %w", metaDayStart, v, err)
	}
	return time.Unix(sec, 0), nil
}

// closeLocked 关闭数据库。切换为 DELETE 日志模式会触发检查点并删除 -wal/-shm，
// 关闭后磁盘上只剩单个 today.db 文件，可以直接改名或复制。
func (s *Store) closeLocked() error {
	if s.db == nil {
		return nil
	}
	var errs []error
	if s.insert != nil {
		errs = append(errs, s.insert.Close())
	}
	if _, err := s.db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, s.db.Close())
	s.db, s.insert = nil, nil
	return errors.Join(errs...)
}
