// Package fswatch 基于 inotify 提供"文件变更通知"。
// 监听的是文件所在目录而非文件本身，这样文件被删除重建或以 rename 方式原子替换
// （uci commit、dnsmasq 重写租约文件）后仍能持续收到通知。
package fswatch

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Watcher 将目录事件按文件路径分发给订阅者。
type Watcher struct {
	w   *fsnotify.Watcher
	log *slog.Logger

	mu   sync.Mutex
	dirs map[string]bool
	subs map[string][]chan struct{}
}

// New 创建 Watcher，需调用 Run 开始分发事件。
func New(log *slog.Logger) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		w:    w,
		log:  log,
		dirs: make(map[string]bool),
		subs: make(map[string][]chan struct{}),
	}, nil
}

// Subscribe 订阅指定文件的变更。返回的通道容量为 1，
// 连续多次变更会合并为一次通知，消费者收到后应重新读取文件。
func (w *Watcher) Subscribe(path string) (<-chan struct{}, error) {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.dirs[dir] {
		if err := w.w.Add(dir); err != nil {
			return nil, err
		}
		w.dirs[dir] = true
	}
	ch := make(chan struct{}, 1)
	w.subs[path] = append(w.subs[path], ch)
	return ch, nil
}

// Run 分发事件直到 ctx 取消。
func (w *Watcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			w.notify(filepath.Clean(ev.Name))
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.log.Warn("fswatch error", "err", err)
		}
	}
}

// Close 释放 inotify 资源。
func (w *Watcher) Close() error {
	return w.w.Close()
}

func (w *Watcher) notify(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range w.subs[path] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
