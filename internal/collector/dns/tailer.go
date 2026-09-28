package dns

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/leo/leosentry/internal/fswatch"
)

const (
	pollInterval  = time.Second
	sweepInterval = time.Minute
	readChunk     = 64 << 10
)

// TailerOptions 配置 dnsmasq 查询日志读取器。
type TailerOptions struct {
	Path string
	// MaxSize 是日志文件的大小上限，读完后超过此值即截断。
	MaxSize  int64
	Location *time.Location
	Watcher  *fswatch.Watcher
	Logger   *slog.Logger
}

// Tailer 增量读取 dnsmasq 查询日志并写入 Map。
//
// 启动时从文件开头读取全部已有内容用于预热（行内时间戳早于 TTL 的条目会被正常淘汰），
// 之后只读新增部分。日志位于内存盘 /tmp，LeoSentry 是它唯一的消费者，
// 因此文件超过 MaxSize 时直接截断而不是轮转：dnsmasq 以 O_APPEND 写入，
// 且该文件被 bind mount 进 dnsmasq 的 ujail，改名或删除都会使 dnsmasq 继续写旧 inode。
type Tailer struct {
	opts TailerOptions
	m    *Map
	log  *slog.Logger

	f       *os.File
	offset  int64
	partial []byte
	buf     []byte
	lines   []logLine
}

// NewTailer 创建日志读取器。
func NewTailer(m *Map, opts TailerOptions) *Tailer {
	if opts.Location == nil {
		opts.Location = time.Local
	}
	return &Tailer{opts: opts, m: m, log: opts.Logger, buf: make([]byte, readChunk)}
}

// Run 持续读取日志直到 ctx 取消。
func (t *Tailer) Run(ctx context.Context) {
	defer t.close()

	var changed <-chan struct{}
	if t.opts.Watcher != nil {
		ch, err := t.opts.Watcher.Subscribe(t.opts.Path)
		if err != nil {
			t.log.Warn("watch dns log failed, polling only", "file", t.opts.Path, "err", err)
		} else {
			changed = ch
		}
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	sweep := time.NewTicker(sweepInterval)
	defer sweep.Stop()

	t.poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			t.poll()
		case <-poll.C:
			t.poll()
		case now := <-sweep.C:
			t.m.Sweep(now)
		}
	}
}

func (t *Tailer) poll() {
	if err := t.readNew(); err != nil {
		t.log.Warn("read dns log failed", "file", t.opts.Path, "err", err)
		t.close()
	}
}

func (t *Tailer) readNew() error {
	if t.f == nil {
		f, err := os.OpenFile(t.opts.Path, os.O_RDWR, 0)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		t.f, t.offset, t.partial = f, 0, t.partial[:0]
		t.log.Info("following dns log", "file", t.opts.Path)
	}

	if replaced, err := t.fileReplaced(); err != nil || replaced {
		t.close()
		return err
	}

	st, err := t.f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < t.offset {
		t.offset, t.partial = 0, t.partial[:0]
	}

	for t.offset < st.Size() {
		n, err := t.f.ReadAt(t.buf, t.offset)
		if n > 0 {
			t.offset += int64(n)
			t.consume(t.buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	t.flush()

	if t.offset >= t.opts.MaxSize {
		if err := t.f.Truncate(0); err != nil {
			return err
		}
		t.offset, t.partial = 0, t.partial[:0]
		t.log.Debug("dns log truncated", "file", t.opts.Path)
	}
	return nil
}

// consume 将读到的数据切分成完整行，不完整的行尾保留到下次拼接。
func (t *Tailer) consume(data []byte) {
	now := time.Now()
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.partial = append(t.partial, data...)
			return
		}
		line := data[:i]
		if len(t.partial) > 0 {
			t.partial = append(t.partial, line...)
			line = t.partial
		}
		if l, ok := parseLine(string(line), t.opts.Location, now); ok {
			t.lines = append(t.lines, l)
		}
		t.partial = t.partial[:0]
		data = data[i+1:]
	}
}

func (t *Tailer) flush() {
	if len(t.lines) == 0 {
		return
	}
	t.m.ingest(t.lines, time.Now())
	clear(t.lines)
	t.lines = t.lines[:0]
}

func (t *Tailer) fileReplaced() (bool, error) {
	cur, err := os.Stat(t.opts.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	open, err := t.f.Stat()
	if err != nil {
		return false, err
	}
	return !os.SameFile(cur, open), nil
}

func (t *Tailer) close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}
