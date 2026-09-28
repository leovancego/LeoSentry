package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ListTemplates 返回可套用的策略。
func (e *Engine) ListTemplates(ctx context.Context) ([]Template, error) {
	rows, err := e.db.Templates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(rows))
	for _, r := range rows {
		spec, err := decodeSpec(r.Spec)
		if err != nil {
			continue
		}
		out = append(out, Template{ID: r.ID, Name: r.Name, DailyLimits: spec.DailyLimits, Windows: spec.Windows})
	}
	return out, nil
}

// SaveTemplate 保存一份可套用的策略。id 为空则新建。
func (e *Engine) SaveTemplate(ctx context.Context, id, name string, in Save) (Template, error) {
	name = trimName(name)
	if name == "" || utf8.RuneCountInString(name) > 20 {
		return Template{}, &InputError{Message: "策略名称要在 1 到 20 个字之间"}
	}
	vac, err := e.db.Vacations(ctx)
	if err != nil {
		return Template{}, err
	}
	in.Enabled = true
	in, err = Normalize(in, vac)
	if err != nil {
		return Template{}, err
	}
	raw, err := json.Marshal(in.SpecOf())
	if err != nil {
		return Template{}, err
	}
	id, err = e.db.SaveTemplate(ctx, id, name, string(raw), e.now().Unix())
	if err != nil {
		return Template{}, err
	}
	return Template{ID: id, Name: name, DailyLimits: in.DailyLimits, Windows: in.Windows}, nil
}

// DeleteTemplate 删除一份策略模板，不影响已经套用到设备上的设置。
func (e *Engine) DeleteTemplate(ctx context.Context, id string) error {
	if err := e.db.DeleteTemplate(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func trimName(s string) string {
	return strings.TrimSpace(s)
}
