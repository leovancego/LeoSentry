package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CollectInterval != Default().CollectInterval {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leosentry")
	content := `
config leosentry 'main'
	option log_level 'debug'
	option rotate_at '04:30'
	option collect_interval '30'
	option min_flow_kb_per_min '16'
	option min_free_mb '0'
	option dns_log_max_size_mb '16'
	option manage_dnsmasq '0'
	option http_port '0'
	list managed_network '192.168.1.7/24'
	list managed_network '10.0.0.0/16'
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RotateHour != 4 || cfg.RotateMinute != 30 {
		t.Errorf("rotate = %d:%d", cfg.RotateHour, cfg.RotateMinute)
	}
	if cfg.CollectInterval != 30*time.Second || cfg.DNSLogMaxSize != 16<<20 || cfg.ManageDnsmasq {
		t.Errorf("cfg = %+v", cfg)
	}
	// 16 KB/分钟按 30 秒周期折算为 8 KB
	if cfg.MinFlowBytes() != 8<<10 || cfg.MinFreeSpace != 0 {
		t.Errorf("min flow = %d, min free = %d", cfg.MinFlowBytes(), cfg.MinFreeSpace)
	}
	if len(cfg.ManagedNetworks) != 2 || cfg.ManagedNetworks[0].String() != "192.168.1.0/24" {
		t.Errorf("managed = %v", cfg.ManagedNetworks)
	}
	if cfg.HTTPPort != 0 {
		t.Errorf("http port = %d, want disabled", cfg.HTTPPort)
	}
}

func TestValidateHTTP(t *testing.T) {
	for _, tc := range []struct {
		port int
		addr string
		ok   bool
	}{
		{8088, "", true},
		{80, "192.168.1.1", true},
		{70000, "", false},
		{8088, "lan", false},
	} {
		c := Default()
		c.HTTPPort, c.HTTPAddress = tc.port, tc.addr
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("port %d addr %q: err = %v", tc.port, tc.addr, err)
		}
	}
}

func TestDefaultUCIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leosentry")
	if err := os.WriteFile(path, []byte(DefaultUCI()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.DataDir != d.DataDir || cfg.DNSTTL != d.DNSTTL || cfg.RotateHour != d.RotateHour || !cfg.DisableFlowOffload ||
		cfg.CollectInterval != d.CollectInterval || cfg.MinFlowBytesPerMinute != d.MinFlowBytesPerMinute || cfg.MinFreeSpace != d.MinFreeSpace ||
		cfg.HTTPPort != d.HTTPPort || cfg.HTTPAddress != d.HTTPAddress {
		t.Fatalf("round trip mismatch: %+v", cfg)
	}
}
