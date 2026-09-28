package policy

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/leo/leosentry/assets"
	"github.com/leo/leosentry/internal/policy/calendar"
	"github.com/leo/leosentry/internal/store"
)

// Seed 把内嵌日历写入数据库。某一年已经导入过就跳过，避免覆盖页面上改过的寒暑假。
func Seed(ctx context.Context, db *store.PolicyStore) error {
	entries, err := fs.ReadDir(assets.Calendar, "calendar")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(assets.Calendar, "calendar/"+e.Name())
		if err != nil {
			return err
		}
		y, vac, err := calendar.Parse(b)
		if err != nil {
			return err
		}
		if _, err := db.Year(ctx, y.Meta.Year); err == nil {
			continue
		} else if !errors.Is(err, calendar.ErrNotFound) {
			return err
		}
		var replace *calendar.Vacations
		if vac != nil {
			existing, err := db.Vacations(ctx)
			if err != nil {
				return err
			}
			if existing.Summer.Empty() && existing.Winter.Empty() {
				replace = vac
			}
		}
		if err := db.SaveCalendar(ctx, y, replace, time.Now()); err != nil {
			return err
		}
	}
	return nil
}
