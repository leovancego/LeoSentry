package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo/leosentry/internal/installer"
	"github.com/leo/leosentry/internal/settings"
	"github.com/leo/leosentry/internal/store"
)

func openSettings(t *testing.T) *settings.Service {
	t.Helper()
	db, err := store.OpenPolicy(context.Background(), filepath.Join(t.TempDir(), "policy.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return settings.Bind(db, nil, nil, nil, nil, "03:00")
}

func TestCategoryExportAndImport(t *testing.T) {
	svc := openSettings(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	Settings{API: svc, Log: log}.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/categories/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export code = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "leosentry-categories.json") {
		t.Fatalf("disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	body := rec.Body.Bytes()
	if !strings.Contains(string(body), `"id": "game"`) {
		t.Fatalf("export = %.120s", body)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/categories/import", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "文件格式不正确") {
		t.Fatalf("bad file code = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/categories/import", strings.NewReader(strings.Repeat("a", maxCategoryFile+1)))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "文件太大") {
		t.Fatalf("big file code = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/categories/import", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"game"`) && !strings.Contains(rec.Body.String(), `"id": "game"`) {
		t.Fatalf("import code = %d body = %.200s", rec.Code, rec.Body.String())
	}
}

func TestRestartEndpoint(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	var calls int
	Settings{Log: log, Restart: func() error { calls++; return nil }}.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/restart", nil))
	if rec.Code != http.StatusOK || calls != 1 || !strings.Contains(rec.Body.String(), `"ok":"1"`) && !strings.Contains(rec.Body.String(), `"ok": "1"`) {
		t.Fatalf("code = %d calls = %d body = %s", rec.Code, calls, rec.Body.String())
	}

	mux = http.NewServeMux()
	Settings{Log: log, Restart: func() error { return installer.ErrNotService }}.Register(mux)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/settings/restart", nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "没有安装成系统服务") {
		t.Fatalf("unavailable code = %d body = %s", rec.Code, rec.Body.String())
	}
}
