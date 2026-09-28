package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/leo/leosentry/internal/analyzer/activity"
	"github.com/leo/leosentry/internal/analyzer/category"
	"github.com/leo/leosentry/internal/api"
	"github.com/leo/leosentry/internal/collector"
	"github.com/leo/leosentry/internal/collector/conntrack"
	"github.com/leo/leosentry/internal/collector/dns"
	"github.com/leo/leosentry/internal/collector/nftstats"
	"github.com/leo/leosentry/internal/config"
	"github.com/leo/leosentry/internal/device"
	"github.com/leo/leosentry/internal/fswatch"
	"github.com/leo/leosentry/internal/installer"
	"github.com/leo/leosentry/internal/nftctl"
	"github.com/leo/leosentry/internal/policy"
	"github.com/leo/leosentry/internal/settings"
	"github.com/leo/leosentry/internal/statday"
	"github.com/leo/leosentry/internal/store"
	"github.com/leo/leosentry/internal/sysconf"
	"github.com/leo/leosentry/internal/usage"
)

// Run 按依赖顺序启动采集层，阻塞直到 ctx 取消后按相反顺序清理：
//
//	时区 → 系统设置 → 数据库初始化 → 恢复当日计数 → nft 表 → 设备/DNS/conntrack → Web UI → 采集循环
//
// 数据库在任何采集协程启动之前完成初始化。
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	loc, err := sysconf.DetectLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	time.Local = loc
	pdb, err := store.OpenPolicy(ctx, store.PolicyPath(cfg.DataDir), log)
	if err != nil {
		return err
	}
	defer pdb.Close()
	cfg, err = settings.ApplyStored(ctx, pdb, cfg)
	if err != nil {
		return fmt.Errorf("stored settings: %w", err)
	}
	cal := statday.New(cfg.RotateHour, cfg.RotateMinute, loc)
	log.Info("leosentry starting", "timezone", loc.String(),
		"rotate_at", fmt.Sprintf("%02d:%02d", cfg.RotateHour, cfg.RotateMinute),
		"collect_interval", cfg.CollectInterval, "policy_interval", cfg.PolicyInterval)

	if err := sysconf.Apply(ctx, sysconf.Options{
		ManageDnsmasq:      cfg.ManageDnsmasq,
		DNSLogFile:         cfg.DNSLogFile,
		DHCPConfigFile:     cfg.DHCPConfigFile,
		DisableFlowOffload: cfg.DisableFlowOffload,
		Logger:             log,
	}); err != nil {
		log.Error("system configuration incomplete, continuing in degraded mode", "err", err)
	}

	managed := cfg.ManagedNetworks
	if len(managed) == 0 {
		if managed, err = sysconf.LANPrefixes(cfg.LANDevice); err != nil {
			return fmt.Errorf("detect managed networks: %w", err)
		}
	}
	log.Info("managed networks", "prefixes", managed)

	st, err := store.Open(ctx, store.Options{
		DataDir:         cfg.DataDir,
		ArchiveDir:      cfg.ArchiveDir,
		ArchiveKeepDays: cfg.ArchiveKeepDays,
		Calendar:        cal,
		Logger:          log,
		MinFreeBytes:    cfg.MinFreeSpace,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	if !st.ArchiveStatus().Configured {
		log.Warn("archive storage not configured, closed days will be discarded after rotation")
	}

	daily := usage.NewDaily(cal, cfg.CollectInterval)
	dayStart, devTotals, domTotals, err := st.DailyTotals(ctx)
	if err != nil {
		return err
	}
	daily.Restore(dayStart, devTotals, domTotals)

	nft, err := nftctl.New()
	if err != nil {
		return err
	}
	defer nft.Close()
	if err := nft.Setup(nftctl.DefaultOptions(managed)); err != nil {
		return err
	}
	defer func() {
		if err := nft.Teardown(); err != nil {
			log.Warn("remove nft table", "err", err)
		}
	}()
	traffic, err := nftstats.NewReader()
	if err != nil {
		return err
	}
	defer traffic.Close()

	watcher, err := fswatch.New(log)
	if err != nil {
		log.Warn("inotify unavailable, falling back to polling", "err", err)
	} else {
		defer watcher.Close()
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if watcher != nil {
		wg.Go(func() { watcher.Run(ctx) })
	}

	resolver := device.NewResolver(device.Options{
		LeasesFile:     cfg.LeasesFile,
		DHCPConfigFile: cfg.DHCPConfigFile,
		ARPFile:        cfg.ARPFile,
		Watcher:        watcher,
		Logger:         log,
	})
	resolver.Start(ctx)

	registry, err := store.OpenRegistry(ctx, store.RegistryOptions{
		Path:   store.DevicesPath(cfg.DataDir),
		Logger: log,
	})
	if err != nil {
		return err
	}
	defer registry.Close()
	if err := importDeviceSightings(ctx, st, registry, log); err != nil {
		log.Warn("backfill device registry from today.db", "err", err)
	}

	dnsMap := dns.NewMap(cfg.DNSTTL)
	tailer := dns.NewTailer(dnsMap, dns.TailerOptions{
		Path:     cfg.DNSLogFile,
		MaxSize:  cfg.DNSLogMaxSize,
		Location: loc,
		Watcher:  watcher,
		Logger:   log,
	})
	wg.Go(func() { tailer.Run(ctx) })

	var (
		online  activity.OnlineSource
		tracker *conntrack.Tracker
	)
	if src, err := conntrack.OpenSource(cfg.ConntrackFile); err != nil {
		log.Warn("conntrack disabled, realtime online status unavailable", "err", err)
	} else {
		tracker = conntrack.NewTracker(src, cfg.ConntrackInterval, managed, resolver, log)
		if self, err := sysconf.LANAddrs(cfg.LANDevice); err == nil {
			tracker.Ignore(self...)
		}
		online = tracker
		wg.Go(func() { tracker.Run(ctx) })
	}

	rules, err := settings.LoadRules(ctx, pdb)
	if err != nil {
		return fmt.Errorf("load category rules: %w", err)
	}
	live := category.NewLive(rules)
	if err := policy.Seed(ctx, pdb); err != nil {
		return fmt.Errorf("seed calendar: %w", err)
	}
	usage := &policy.Usage{Scan: st, Rules: rules, Interval: cfg.CollectInterval}
	eng := policy.New(policy.Options{
		DB:        pdb,
		Usage:     usage,
		Addresses: resolver,
		Domains:   dnsMap,
		Live:      live,
		Stat:      cal,
		Firewall:  nft,
		Logger:    log,
		Interval:  cfg.PolicyInterval,
	})
	if _, err := eng.Check(ctx); err != nil {
		log.Error("initial policy check failed", "err", err)
	}
	wg.Go(func() { eng.Run(ctx) })

	var (
		catalog  *device.Catalog
		overview *activity.Builder
	)
	if webUIEnabled(cfg) {
		var liveOnline device.OnlineSource
		if tracker != nil {
			liveOnline = tracker
		}
		catalog = device.NewCatalog(device.CatalogOptions{
			Book:     registry,
			Live:     resolver,
			Online:   liveOnline,
			Location: loc,
		})
		overview = activity.NewBuilder(activity.Options{
			Usage:                 st,
			Devices:               resolver,
			Names:                 registry,
			Online:                online,
			Rules:                 live,
			Calendar:              cal,
			Interval:              cfg.CollectInterval,
			MinFlowBytesPerMinute: cfg.MinFlowBytesPerMinute,
			Logger:                log,
		})
	}

	col := collector.New(collector.Options{
		Interval:     cfg.CollectInterval,
		MinFlowBytes: cfg.MinFlowBytes(),
		Traffic:      traffic,
		Devices:      resolver,
		Domains:      dnsMap,
		Sinks:        []collector.Sink{daily, st, registry},
		Logger:       log,
	})
	pref := settings.Bind(pdb, live, usage, col, eng, fmt.Sprintf("%02d:%02d", cfg.RotateHour, cfg.RotateMinute))
	pref.UseTLS(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.HTTPPort, cfg.HTTPSPort)
	if webUIEnabled(cfg) {
		if err := startWebUI(ctx, &wg, cfg, overview, catalog, eng, pref, pdb, log); err != nil {
			log.Error("web ui disabled", "err", err)
		}
	}
	col.Run(ctx)

	log.Info("leosentry stopping")
	return nil
}

// httpsListeners 在证书路径非空时检查证书，并确认端口能监听。证书留空则不启用 HTTPS。
// 监听地址是 ":端口"，绑在全部网卡上。
func httpsListeners(log *slog.Logger, cfg config.Config) ([]string, string) {
	cert := strings.TrimSpace(cfg.TLSCertFile)
	key := strings.TrimSpace(cfg.TLSKeyFile)
	if cert == "" && key == "" {
		return nil, ""
	}
	if err := settings.CheckTLS(cert, key); err != nil {
		log.Error("https disabled", "err", err)
		return nil, err.Error()
	}
	if cfg.HTTPSPort <= 0 {
		msg := "https_port 为 0，未启动 HTTPS"
		log.Error("https disabled", "err", msg)
		return nil, msg
	}
	addr := fmt.Sprintf(":%d", cfg.HTTPSPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		msg := "HTTPS 端口无法监听：" + err.Error()
		log.Error("https disabled", "addr", addr, "err", err)
		return nil, msg
	}
	ln.Close()
	return []string{addr}, ""
}

func listenAddr(port int) []string {
	if port <= 0 {
		return nil
	}
	return []string{fmt.Sprintf(":%d", port)}
}

func importDeviceSightings(ctx context.Context, st *store.Store, reg *store.Registry, log *slog.Logger) error {
	seen, err := st.DeviceSightings(ctx)
	if err != nil {
		return err
	}
	for _, s := range seen {
		reg.Observe(s.MAC, s.IP, s.First)
		reg.Observe(s.MAC, s.IP, s.Last)
	}
	if err := reg.Flush(ctx); err != nil {
		return err
	}
	if len(seen) > 0 {
		log.Info("device registry backfilled", "sightings", len(seen))
	}
	return nil
}

func webUIEnabled(cfg config.Config) bool {
	return cfg.HTTPPort > 0 || cfg.HTTPSPort > 0
}

// startWebUI 启动 Web UI。HTTP 与 HTTPS 都监听全部网卡，地址形如 ":8088"、":8443"。
func startWebUI(ctx context.Context, wg *sync.WaitGroup, cfg config.Config, overview *activity.Builder, devices *device.Catalog, policies *policy.Engine, pref *settings.Service, passwords api.PasswordStore, log *slog.Logger) error {
	tlsAddrs, tlsErr := httpsListeners(log, cfg)
	if tlsErr == "" && len(tlsAddrs) > 0 {
		pref.NoteTLS(strings.TrimSpace(cfg.TLSCertFile), strings.TrimSpace(cfg.TLSKeyFile), "")
	} else {
		pref.NoteTLS("", "", tlsErr)
	}
	var certFile, keyFile string
	if len(tlsAddrs) > 0 {
		certFile = strings.TrimSpace(cfg.TLSCertFile)
		keyFile = strings.TrimSpace(cfg.TLSKeyFile)
	}
	srv, err := api.New(api.Options{
		Addrs: listenAddr(cfg.HTTPPort), TLSAddrs: tlsAddrs,
		TLSCertFile: certFile, TLSKeyFile: keyFile,
		Overview: overview, Devices: devices, Policies: policies,
		Settings: pref, Passwords: passwords, Restart: func() error { return installer.Restart(log) }, Logger: log,
	})
	if err != nil {
		return err
	}
	wg.Go(func() {
		if err := srv.Run(ctx); err != nil {
			log.Error("web ui stopped", "err", err)
		}
	})
	return nil
}
