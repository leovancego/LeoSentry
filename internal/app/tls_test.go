package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/leo/leosentry/internal/config"
)

func TestListenAddrUsesAllInterfaces(t *testing.T) {
	if got := listenAddr(8088); len(got) != 1 || got[0] != ":8088" {
		t.Fatalf("http addr = %v", got)
	}
	if got := listenAddr(8443); len(got) != 1 || got[0] != ":8443" {
		t.Fatalf("https addr = %v", got)
	}
	if listenAddr(0) != nil {
		t.Fatal("port 0 should not listen")
	}
}

func TestHTTPSListenersSkipsEmptyCert(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	addrs, msg := httpsListeners(log, config.Default())
	if len(addrs) != 0 || msg != "" {
		t.Fatalf("addrs=%v msg=%q", addrs, msg)
	}
}

func TestHTTPSListenersReportsBadCert(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default()
	cfg.TLSCertFile = "/no/such/example.crt"
	cfg.TLSKeyFile = "/no/such/example.key"
	addrs, msg := httpsListeners(log, cfg)
	if len(addrs) != 0 || msg == "" {
		t.Fatalf("addrs=%v msg=%q", addrs, msg)
	}
}
