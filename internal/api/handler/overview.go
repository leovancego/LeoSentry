package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/leo/leosentry/internal/analyzer/activity"
	"github.com/leo/leosentry/web"
)

// OverviewSource 提供今日概览快照，由 activity.Builder 实现。
type OverviewSource interface {
	Snapshot(ctx context.Context, which string) (*activity.Snapshot, error)
}

// Overview 处理 GET /api/v1/overview/today。
// 快照已预先序列化并压缩，这里只负责协商缓存与编码后原样写出。
func Overview(src OverviewSource, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		which := r.URL.Query().Get("day")
		snap, err := src.Snapshot(r.Context(), which)
		if err != nil {
			log.Error("build overview failed", "err", err)
			http.Error(w, "overview unavailable", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", snap.ETag)
		h.Set("Vary", "Accept-Encoding")
		if r.Header.Get("If-None-Match") == snap.ETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := snap.JSON
		if web.AcceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			body = snap.Gzip
		}
		w.Write(body)
	}
}
