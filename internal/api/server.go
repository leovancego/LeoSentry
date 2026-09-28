package api

import (
	"context"
	"crypto/tls"
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

// Options 配置 Web 服务。
type Options struct {
	// Addrs 为 HTTP 监听地址，如 "192.168.1.1:8088"。
	Addrs []string
	// TLSAddrs 为 HTTPS 监听地址。证书或端口不可用时跳过 HTTPS，已有的 HTTP 继续服务。
	TLSAddrs    []string
	TLSCertFile string
	TLSKeyFile  string
	Overview    handler.OverviewSource
	Devices     handler.DeviceCatalog
	Policies    handler.PolicyAPI
	Settings    handler.SettingsAPI
	Passwords   PasswordStore
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

// Run 在 HTTP 与 HTTPS 地址上监听，直到 ctx 取消后优雅关闭。
// HTTP 任一地址监听失败即返回错误。HTTPS 证书或端口不可用时只记录错误，已监听的 HTTP 继续服务。
func (s *Server) Run(ctx context.Context) error {
	httpLns, err := listenAll(s.opts.Addrs)
	if err != nil {
		return err
	}
	tlsLns, tlsErr := s.tlsListeners()
	if len(httpLns) == 0 && len(tlsLns) == 0 {
		if tlsErr != nil {
			return tlsErr
		}
		return errors.New("web ui: no listen address")
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
	if len(tlsLns) > 0 {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	var wg sync.WaitGroup
	errc := make(chan error, len(httpLns)+len(tlsLns))
	for _, ln := range httpLns {
		s.opts.Logger.Info("web ui listening", "url", "http://"+ln.Addr().String()+"/")
		wg.Go(func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		})
	}
	for _, ln := range tlsLns {
		s.opts.Logger.Info("web ui listening", "url", "https://"+ln.Addr().String()+"/")
		wg.Go(func() {
			if err := srv.ServeTLS(ln, s.opts.TLSCertFile, s.opts.TLSKeyFile); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		})
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	wg.Wait()
	return serveErr
}

// tlsListeners 加载证书并监听 HTTPS。未配置 TLS 地址时返回 nil。失败时记录日志并返回错误，不关闭已有的 HTTP 监听。
func (s *Server) tlsListeners() ([]net.Listener, error) {
	if len(s.opts.TLSAddrs) == 0 {
		return nil, nil
	}
	if s.opts.TLSCertFile == "" || s.opts.TLSKeyFile == "" {
		err := errors.New("tls_cert and tls_key are required")
		s.opts.Logger.Error("https disabled", "err", err)
		return nil, err
	}
	if _, err := tls.LoadX509KeyPair(s.opts.TLSCertFile, s.opts.TLSKeyFile); err != nil {
		s.opts.Logger.Error("https disabled", "cert", s.opts.TLSCertFile, "err", err)
		return nil, err
	}
	lns, err := listenAll(s.opts.TLSAddrs)
	if err != nil {
		s.opts.Logger.Error("https disabled", "err", err)
		return nil, err
	}
	return lns, nil
}

func listenAll(addrs []string) ([]net.Listener, error) {
	var listeners []net.Listener
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, err
		}
		listeners = append(listeners, ln)
	}
	return listeners, nil
}
