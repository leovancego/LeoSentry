package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/statday"
)

const (
	archiveRetryInterval = time.Hour
	// pendingKeepDays：已配置归档但硬盘暂不可用时，待归档文件在闪存上最多保留的天数。
	pendingKeepDays = 3
)

// ArchiveStatus 描述归档存储的状态。
type ArchiveStatus struct {
	Configured       bool
	Available        bool
	LastArchivedAt   time.Time
	LastArchivedFile string
	LastError        string
}

// archiver 把 pending 目录中已结束统计日的库文件复制到外部硬盘。
type archiver struct {
	dir        string
	pendingDir string
	keepDays   int
	cal        statday.Calendar
	now        func() time.Time
	isExternal func(string) bool
	log        *slog.Logger

	kick      chan struct{}
	processMu sync.Mutex

	statusMu sync.Mutex
	status   ArchiveStatus
}

func newArchiver(opts Options, pendingDir string) *archiver {
	return &archiver{
		dir:        opts.ArchiveDir,
		pendingDir: pendingDir,
		keepDays:   opts.ArchiveKeepDays,
		cal:        opts.Calendar,
		now:        opts.now,
		isExternal: opts.isExternal,
		log:        opts.Logger,
		kick:       make(chan struct{}, 1),
		status:     ArchiveStatus{Configured: opts.ArchiveDir != ""},
	}
}

func (a *archiver) trigger() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *archiver) run(ctx context.Context) {
	ticker := time.NewTicker(archiveRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
		case <-ticker.C:
		}
		a.process()
	}
}

func (a *archiver) statusSnapshot() ArchiveStatus {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.status
}

func (a *archiver) setStatus(fn func(*ArchiveStatus)) {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	fn(&a.status)
}

// process 处理全部待归档文件：
//   - 未配置归档目录：直接删除（当天数据只保留到下一次切换）；
//   - 已配置但硬盘未挂载或复制失败：保留等待重试，超过 pendingKeepDays 后删除；
//   - 复制并校验成功：删除闪存上的文件，并清理超过保留天数的旧归档。
func (a *archiver) process() {
	a.processMu.Lock()
	defer a.processMu.Unlock()

	files, err := filepath.Glob(filepath.Join(a.pendingDir, "usage_*.db"))
	if err != nil {
		a.log.Error("list pending archives failed", "err", err)
		return
	}
	slices.Sort(files)

	available := a.dir != "" && a.isExternal(a.dir)
	if available {
		if err := os.MkdirAll(a.dir, 0o755); err != nil {
			available = false
			a.setStatus(func(s *ArchiveStatus) { s.LastError = err.Error() })
		}
	}
	a.setStatus(func(s *ArchiveStatus) { s.Available = available })

	for _, f := range files {
		switch {
		case a.dir == "":
			a.log.Info("archive not configured, discarding closed day", "file", f)
			os.Remove(f)
		case !available:
			a.log.Warn("archive storage not mounted, keeping file on flash", "file", f, "archive_dir", a.dir)
			a.dropIfStale(f)
		default:
			dst, err := a.archiveOne(f)
			if err != nil {
				a.log.Error("archive failed", "file", f, "err", err)
				a.setStatus(func(s *ArchiveStatus) { s.LastError = err.Error() })
				a.dropIfStale(f)
				continue
			}
			a.log.Info("archived", "file", dst)
			a.setStatus(func(s *ArchiveStatus) {
				s.LastArchivedAt, s.LastArchivedFile, s.LastError = a.now(), dst, ""
			})
		}
	}
	if available {
		a.cleanup()
	}
}

// archiveOne 复制到 <dir>/archive_<日期>.db.tmp，校验大小与 SHA-256 一致后改名，最后删除源文件。
func (a *archiver) archiveOne(src string) (string, error) {
	label := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(src), "usage_"), ".db")
	dst := uniquePath(filepath.Join(a.dir, "archive_"+label+".db"))
	tmp := dst + ".tmp"

	srcSum, srcSize, err := copyFile(src, tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	dstSum, dstSize, err := hashFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if srcSize != dstSize || !bytes.Equal(srcSum, dstSum) {
		os.Remove(tmp)
		return "", fmt.Errorf("verify %s: checksum mismatch", tmp)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	syncDir(a.dir)
	if err := os.Remove(src); err != nil {
		return dst, fmt.Errorf("remove archived source: %w", err)
	}
	return dst, nil
}

// cleanup 删除超过保留天数的归档文件。
func (a *archiver) cleanup() {
	cutoff := a.cal.Label(a.cal.DayStart(a.now()).AddDate(0, 0, -a.keepDays))
	files, _ := filepath.Glob(filepath.Join(a.dir, "archive_*.db"))
	for _, f := range files {
		if day, ok := labelOf(f, "archive_"); ok && day < cutoff {
			if err := os.Remove(f); err == nil {
				a.log.Info("expired archive removed", "file", f)
			}
		}
	}
}

func (a *archiver) dropIfStale(f string) {
	cutoff := a.cal.Label(a.cal.DayStart(a.now()).AddDate(0, 0, -pendingKeepDays))
	if day, ok := labelOf(f, "usage_"); ok && day < cutoff {
		a.log.Warn("pending archive expired, discarding", "file", f)
		os.Remove(f)
	}
}

// labelOf 从文件名中取出 YYYY-MM-DD 日期标签，日期字符串可直接按字典序比较。
func labelOf(path, prefix string) (string, bool) {
	name := strings.TrimPrefix(filepath.Base(path), prefix)
	if len(name) < len(time.DateOnly) {
		return "", false
	}
	day := name[:len(time.DateOnly)]
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return "", false
	}
	return day, true
}

func copyFile(src, dst string) (sum []byte, size int64, err error) {
	in, err := os.Open(src)
	if err != nil {
		return nil, 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, 0, err
	}
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, 0, err
	}
	return h.Sum(nil), size, nil
}

func hashFile(path string) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, 0, err
	}
	return h.Sum(nil), n, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
