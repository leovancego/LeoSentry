package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// rotateLocked 进入 newStart 这一统计日。
// 刚结束的一天留作昨天；比昨天更早的那一整天一次性删除。
func (s *Store) rotateLocked(ctx context.Context, newStart time.Time) error {
	yesterday := newStart.AddDate(0, 0, -1)
	closed := s.opts.Calendar.Label(s.dayStart)
	keepClosed := s.dayStart.Equal(yesterday)

	if err := s.closeLocked(); err != nil {
		s.log.Warn("close today.db before rotation", "err", err)
	}
	if err := removeDB(s.prevPath()); err != nil {
		return fmt.Errorf("store: drop previous day: %w", err)
	}
	if keepClosed {
		if err := os.Rename(s.todayPath(), s.prevPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if reopenErr := s.openTodayLocked(ctx); reopenErr != nil {
				return errors.Join(fmt.Errorf("store: keep yesterday: %w", err), reopenErr)
			}
			return fmt.Errorf("store: keep yesterday: %w", err)
		}
		s.log.Info("stat day closed", "kept", closed, "new_day", s.opts.Calendar.Label(newStart))
	} else {
		if err := removeDB(s.todayPath()); err != nil {
			return fmt.Errorf("store: drop stale day: %w", err)
		}
		s.log.Info("stat day dropped", "day", closed, "new_day", s.opts.Calendar.Label(newStart))
	}
	if err := s.openTodayLocked(ctx); err != nil {
		return err
	}
	if err := s.setDayStartLocked(ctx, newStart); err != nil {
		return err
	}
	s.version.Add(1)
	return nil
}

// prunePrevLocked 只保留紧挨着当前统计日的前一天。
func (s *Store) prunePrevLocked() error {
	yesterday := s.dayStart.AddDate(0, 0, -1)
	db, err := sqlOpenRO(s.prevPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	start, readErr := readDayStart(context.Background(), db)
	db.Close()
	if readErr != nil || !start.Equal(yesterday) {
		if err := removeDB(s.prevPath()); err != nil {
			return fmt.Errorf("store: drop previous day: %w", err)
		}
		s.log.Info("previous day dropped", "day", s.opts.Calendar.Label(start))
	}
	return nil
}

func (s *Store) prevPath() string {
	return filepath.Join(s.opts.DataDir, prevFile)
}

func removeDB(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sqlOpenRO(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// uniquePath 在文件已存在时追加 -1、-2 等后缀。
func uniquePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := path[:len(path)-len(ext)]
	for i := 1; ; i++ {
		p := base + "-" + strconv.Itoa(i) + ext
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			return p
		}
	}
}
