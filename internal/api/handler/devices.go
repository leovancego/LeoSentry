package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/leo/leosentry/internal/device"
	"github.com/leo/leosentry/internal/store"
)

const maxNameBody = 4 << 10

// DeviceCatalog 提供设备列表与改名，由 device.Catalog 实现。
type DeviceCatalog interface {
	List(ctx context.Context) ([]device.Entry, error)
	SetName(ctx context.Context, id, name string) error
	TZOffset(t time.Time) int
}

// Devices 处理 GET /api/v1/devices。
func Devices(cat DeviceCatalog, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := cat.List(r.Context())
		if err != nil {
			log.Error("list devices failed", "err", err)
			http.Error(w, "devices unavailable", http.StatusInternalServerError)
			return
		}
		now := time.Now()
		writeJSON(w, http.StatusOK, map[string]any{
			"generatedAt": now.Unix(),
			"tzOffset":    cat.TZOffset(now),
			"devices":     list,
		})
	}
}

// DeviceName 处理 PATCH /api/v1/devices/{id}，body 为 {"name":"..."}，空串表示去掉名字。
func DeviceName(cat DeviceCatalog, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name *string `json:"name"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, maxNameBody))
		if err := dec.Decode(&body); err != nil || body.Name == nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		if err := cat.SetName(r.Context(), id, *body.Name); err != nil {
			switch {
			case errors.Is(err, device.ErrInvalidID):
				http.Error(w, "invalid device id", http.StatusBadRequest)
			case errors.Is(err, store.ErrNameTooLong):
				http.Error(w, "name too long", http.StatusBadRequest)
			default:
				log.Error("rename device failed", "id", id, "err", err)
				http.Error(w, "rename failed", http.StatusInternalServerError)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
