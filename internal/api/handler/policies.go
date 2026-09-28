package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/leo/leosentry/internal/policy"
	"github.com/leo/leosentry/internal/policy/calendar"
)

const (
	maxCalendarBody = 256 << 10
	maxPolicyBody   = 32 << 10
	maxSmallBody    = 4 << 10
)

// PolicyAPI 是管控策略页面使用的操作，由 policy.Engine 实现。
type PolicyAPI interface {
	Current(ctx context.Context) (policy.Snapshot, error)
	Check(ctx context.Context) (policy.Snapshot, error)
	ImportCalendar(ctx context.Context, data []byte) (policy.Snapshot, error)
	SaveVacations(ctx context.Context, v calendar.Vacations) (policy.Snapshot, error)
	Save(ctx context.Context, mac string, in policy.Save) (policy.Snapshot, error)
	Pause(ctx context.Context, mac string, paused bool) (policy.Snapshot, error)
	Extend(ctx context.Context, mac string, minutes int) (policy.Snapshot, error)
	Template(year int) ([]byte, string, error)
	ListTemplates(ctx context.Context) ([]policy.Template, error)
	SaveTemplate(ctx context.Context, id, name string, in policy.Save) (policy.Template, error)
	DeleteTemplate(ctx context.Context, id string) error
}

// Policies 注册管控策略相关路由。
type Policies struct {
	API     PolicyAPI
	Devices DeviceCatalog
	Log     *slog.Logger
}

// Register 把路由挂到 mux。
func (h Policies) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/policies", h.get)
	mux.HandleFunc("POST /api/v1/policies/check", h.check)
	mux.HandleFunc("GET /api/v1/policies/calendar/template", h.template)
	mux.HandleFunc("POST /api/v1/policies/calendar", h.importCalendar)
	mux.HandleFunc("PUT /api/v1/policies/vacations", h.vacations)
	mux.HandleFunc("PUT /api/v1/policies/{mac}", h.save)
	mux.HandleFunc("POST /api/v1/policies/{mac}/pause", h.pause)
	mux.HandleFunc("POST /api/v1/policies/{mac}/extend", h.extend)
	mux.HandleFunc("POST /api/v1/policies/templates", h.createTemplate)
	mux.HandleFunc("PUT /api/v1/policies/templates/{id}", h.updateTemplate)
	mux.HandleFunc("DELETE /api/v1/policies/templates/{id}", h.deleteTemplate)
}

func (h Policies) get(w http.ResponseWriter, r *http.Request) {
	snap, err := h.API.Current(r.Context())
	h.write(w, r, snap, err)
}

func (h Policies) check(w http.ResponseWriter, r *http.Request) {
	snap, err := h.API.Check(r.Context())
	h.write(w, r, snap, err)
}

func (h Policies) template(w http.ResponseWriter, r *http.Request) {
	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	body, name, err := h.API.Template(year)
	if err != nil {
		if errors.Is(err, calendar.ErrNotFound) {
			http.Error(w, "calendar not found", http.StatusNotFound)
			return
		}
		h.Log.Error("calendar template", "err", err)
		http.Error(w, "template unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Write(body)
}

func (h Policies) importCalendar(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCalendarBody))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	snap, err := h.API.ImportCalendar(r.Context(), data)
	h.write(w, r, snap, err)
}

func (h Policies) vacations(w http.ResponseWriter, r *http.Request) {
	var body calendar.Vacations
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSmallBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "寒暑假格式不正确"})
		return
	}
	snap, err := h.API.SaveVacations(r.Context(), body)
	h.write(w, r, snap, err)
}

func (h Policies) save(w http.ResponseWriter, r *http.Request) {
	var body policy.Save
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPolicyBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "策略格式不正确"})
		return
	}
	snap, err := h.API.Save(r.Context(), r.PathValue("mac"), body)
	h.write(w, r, snap, err)
}

func (h Policies) pause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paused *bool `json:"paused"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSmallBody)).Decode(&body); err != nil || body.Paused == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请说明是暂停还是恢复"})
		return
	}
	snap, err := h.API.Pause(r.Context(), r.PathValue("mac"), *body.Paused)
	h.write(w, r, snap, err)
}

func (h Policies) extend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes *int `json:"minutes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSmallBody)).Decode(&body); err != nil || body.Minutes == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请填写延长的分钟数"})
		return
	}
	snap, err := h.API.Extend(r.Context(), r.PathValue("mac"), *body.Minutes)
	h.write(w, r, snap, err)
}

func (h Policies) write(w http.ResponseWriter, r *http.Request, snap policy.Snapshot, err error) {
	if err != nil {
		h.writeErr(w, err)
		return
	}
	var known []policy.DeviceInfo
	if h.Devices != nil {
		list, err := h.Devices.List(r.Context())
		if err != nil {
			h.Log.Error("list devices for policies", "err", err)
			http.Error(w, "devices unavailable", http.StatusInternalServerError)
			return
		}
		known = make([]policy.DeviceInfo, 0, len(list))
		for _, d := range list {
			known = append(known, policy.DeviceInfo{
				MAC: d.MAC, Name: d.Name, Hostname: d.Hostname, IP: d.IP, Online: d.Online,
			})
		}
	}
	page := policy.Present(snap, known)
	if tpls, err := h.API.ListTemplates(r.Context()); err == nil {
		page.Templates = tpls
	} else {
		h.Log.Error("list policy templates", "err", err)
	}
	writeJSON(w, http.StatusOK, page)
}

func (h Policies) createTemplate(w http.ResponseWriter, r *http.Request) {
	h.saveTemplate(w, r, "")
}

func (h Policies) updateTemplate(w http.ResponseWriter, r *http.Request) {
	h.saveTemplate(w, r, r.PathValue("id"))
}

func (h Policies) saveTemplate(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Name        string              `json:"name"`
		DailyLimits []policy.DailyLimit `json:"dailyLimits"`
		Windows     []policy.Window     `json:"windows"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPolicyBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "策略格式不正确"})
		return
	}
	if _, err := h.API.SaveTemplate(r.Context(), id, body.Name, policy.Save{
		Enabled: true, DailyLimits: body.DailyLimits, Windows: body.Windows,
	}); err != nil {
		h.writeErr(w, err)
		return
	}
	h.get(w, r)
}

func (h Policies) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := h.API.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		h.writeErr(w, err)
		return
	}
	h.get(w, r)
}

func (h Policies) writeErr(w http.ResponseWriter, err error) {
	var ie *policy.InputError
	switch {
	case errors.As(err, &ie):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ie.Error()})
	case errors.Is(err, policy.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "没有这台设备的策略"})
	default:
		h.Log.Error("policy request failed", "err", err)
		http.Error(w, "policy failed", http.StatusInternalServerError)
	}
}
