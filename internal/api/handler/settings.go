package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/leo/leosentry/internal/installer"
	"github.com/leo/leosentry/internal/settings"
)

const maxCategoryFile = 256 << 10

// SettingsAPI 是系统设置页的读写。
type SettingsAPI interface {
	View(ctx context.Context) (settings.View, error)
	SaveTiming(ctx context.Context, collect, policySec int, rotate string) (settings.View, error)
	AddRule(ctx context.Context, category, value string) (settings.View, error)
	RemoveRule(ctx context.Context, category, value string) (settings.View, error)
	ExportCategories(ctx context.Context) ([]byte, error)
	ImportCategories(ctx context.Context, data []byte) (settings.View, error)
}

// Settings 注册系统设置路由。
type Settings struct {
	API SettingsAPI
	Log *slog.Logger
	// Restart 安全停止当前进程，再按系统服务的方式重新启动。
	Restart func() error
}

// Register 把路由挂到 mux。
func (h Settings) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/settings", h.get)
	mux.HandleFunc("PUT /api/v1/settings/timing", h.timing)
	mux.HandleFunc("POST /api/v1/settings/categories", h.add)
	mux.HandleFunc("DELETE /api/v1/settings/categories", h.remove)
	mux.HandleFunc("GET /api/v1/settings/categories/export", h.exportCategories)
	mux.HandleFunc("POST /api/v1/settings/categories/import", h.importCategories)
	mux.HandleFunc("POST /api/v1/settings/restart", h.restart)
}

func (h Settings) get(w http.ResponseWriter, r *http.Request) {
	view, err := h.API.View(r.Context())
	h.write(w, view, err)
}

func (h Settings) timing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CollectSeconds int    `json:"collectSeconds"`
		PolicySeconds  int    `json:"policySeconds"`
		RotateAt       string `json:"rotateAt"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSmallBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "设置格式不正确"})
		return
	}
	view, err := h.API.SaveTiming(r.Context(), body.CollectSeconds, body.PolicySeconds, body.RotateAt)
	h.write(w, view, err)
}

func (h Settings) add(w http.ResponseWriter, r *http.Request) {
	h.edit(w, r, true)
}

func (h Settings) remove(w http.ResponseWriter, r *http.Request) {
	h.edit(w, r, false)
}

func (h Settings) edit(w http.ResponseWriter, r *http.Request, add bool) {
	var body struct {
		Category string `json:"category"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSmallBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "名单格式不正确"})
		return
	}
	var (
		view settings.View
		err  error
	)
	if add {
		view, err = h.API.AddRule(r.Context(), body.Category, body.Value)
	} else {
		view, err = h.API.RemoveRule(r.Context(), body.Category, body.Value)
	}
	h.write(w, view, err)
}

func (h Settings) exportCategories(w http.ResponseWriter, r *http.Request) {
	body, err := h.API.ExportCategories(r.Context())
	if err != nil {
		h.write(w, settings.View{}, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"leosentry-categories.json\"")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h Settings) importCategories(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCategoryFile+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "文件格式不正确"})
		return
	}
	if len(data) > maxCategoryFile {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "文件太大"})
		return
	}
	view, err := h.API.ImportCategories(r.Context(), data)
	h.write(w, view, err)
}

func (h Settings) restart(w http.ResponseWriter, r *http.Request) {
	if h.Restart == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": installer.ErrNotService.Error()})
		return
	}
	if err := h.Restart(); err != nil {
		if errors.Is(err, installer.ErrNotService) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if h.Log != nil {
			h.Log.Error("restart service", "err", err)
		}
		http.Error(w, "restart failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

func (h Settings) write(w http.ResponseWriter, view settings.View, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, view)
		return
	}
	var se *settings.Error
	if errors.As(err, &se) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": se.Error()})
		return
	}
	if h.Log != nil {
		h.Log.Error("settings request failed", "err", err)
	}
	http.Error(w, "settings failed", http.StatusInternalServerError)
}
