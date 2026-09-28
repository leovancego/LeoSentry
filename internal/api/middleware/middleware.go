package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Recover 捕获处理器 panic，记录堆栈并返回 500，避免单个请求拖垮服务。
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("http handler panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// AccessLog 以 debug 级别记录访问日志。
func AccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"remote", r.RemoteAddr, "cost", time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
