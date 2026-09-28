package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/device"
	"github.com/leo/leosentry/internal/store"
)

type fakeCatalog struct {
	list     []device.Entry
	lastID   string
	lastName string
	err      error
}

func (f *fakeCatalog) List(context.Context) ([]device.Entry, error) { return f.list, f.err }
func (f *fakeCatalog) SetName(_ context.Context, id, name string) error {
	f.lastID, f.lastName = id, name
	return f.err
}
func (f *fakeCatalog) TZOffset(time.Time) int { return 8 * 3600 }

func TestDevicesList(t *testing.T) {
	cat := &fakeCatalog{list: []device.Entry{{ID: "aa:bb:cc:00:00:01", MAC: "aa:bb:cc:00:00:01", Name: "平板"}}}
	h := Devices(cat, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		TZ      int            `json:"tzOffset"`
		Devices []device.Entry `json:"devices"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.TZ != 8*3600 || len(body.Devices) != 1 || body.Devices[0].Name != "平板" {
		t.Fatalf("body = %+v %v", body, err)
	}
}

func TestDeviceNamePatch(t *testing.T) {
	cat := &fakeCatalog{}
	h := DeviceName(cat, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/devices/aa:bb:cc:00:00:01", strings.NewReader(`{"name":"电视"}`))
	req.SetPathValue("id", "aa:bb:cc:00:00:01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || cat.lastID != "aa:bb:cc:00:00:01" || cat.lastName != "电视" {
		t.Fatalf("code = %d id = %s name = %s", rec.Code, cat.lastID, cat.lastName)
	}

	cat.err = device.ErrInvalidID
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/devices/x", strings.NewReader(`{"name":"a"}`))
	req.SetPathValue("id", "x")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id code = %d", rec.Code)
	}

	cat.err = store.ErrNameTooLong
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/devices/aa:bb:cc:00:00:01", strings.NewReader(`{"name":"很长"}`))
	req.SetPathValue("id", "aa:bb:cc:00:00:01")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long name code = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/v1/devices/aa:bb:cc:00:00:01", strings.NewReader(`{}`))
	req.SetPathValue("id", "aa:bb:cc:00:00:01")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name code = %d", rec.Code)
	}
}
