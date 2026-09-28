package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/api/handler"
	"github.com/leo/leosentry/internal/api/middleware"
	"github.com/leo/leosentry/web"
)

// Options 配置 HTTP 服务。
type Options struct {
	// Addrs 为监听地址，如 "192.168.1.1:8088"。
	Addrs     []string
	Overview  handler.OverviewSource
	Devices   handler.DeviceCatalog
	Policies  handler.PolicyAPI
	Settings  handler.SettingsAPI
	Passwords PasswordStore
	// Restart 在设置页请求重新启动已安装的系统服务。
	Restart func() error
	Logger  *slog.Logger
}

// Server 提供 Web UI 与 REST API。
type Server struct {
	opts    Options
	handler http.Handler
}

// New 注册路由。
func New(opts Options) (*Server, error) {
	static, err := web.Handler()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/overview/today", handler.Overview(opts.Overview, opts.Logger))
	if opts.Devices != nil {
		mux.Handle("GET /api/v1/devices", handler.Devices(opts.Devices, opts.Logger))
		mux.Handle("PATCH /api/v1/devices/{id}", handler.DeviceName(opts.Devices, opts.Logger))
	}
	if opts.Policies != nil {
		handler.Policies{API: opts.Policies, Devices: opts.Devices, Log: opts.Logger}.Register(mux)
	}
	if opts.Settings != nil {
		handler.Settings{API: opts.Settings, Log: opts.Logger, Restart: opts.Restart}.Register(mux)
	}
	sess := newSessions()
	if opts.Passwords != nil {
		mux.HandleFunc("POST /api/v1/login", login(opts.Passwords, sess, opts.Logger))
		mux.HandleFunc("POST /api/v1/logout", logout(sess))
		mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write([]byte("{\"ok\":\"1\"}\n"))
		})
		mux.HandleFunc("PUT /api/v1/settings/password", changePassword(opts.Passwords, opts.Logger))
	}
	mux.Handle("GET /", static)

	h := protect(opts.Passwords, sess, mux)
	h = middleware.AccessLog(opts.Logger, h)
	h = middleware.Recover(opts.Logger, h)
	return &Server{opts: opts, handler: h}, nil
}

// Run 在全部地址上监听，直到 ctx 取消后优雅关闭。任一地址监听失败即返回错误。
func (s *Server) Run(ctx context.Context) error {
	var listeners []net.Listener
	for _, addr := range s.opts.Addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return err
		}
		listeners = append(listeners, ln)
	}

	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.opts.Logger.Handler(), slog.LevelDebug),
	}
	var wg sync.WaitGroup
	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		s.opts.Logger.Info("web ui listening", "url", "http://"+ln.Addr().String()+"/")
		wg.Go(func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		})
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	wg.Wait()
	return err
}
