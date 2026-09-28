package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/uci"
)

// DefaultPath 是默认的 UCI 配置文件路径。
const DefaultPath = "/etc/config/leosentry"

// Config 是 LeoSentry 的运行配置。
type Config struct {
	LogLevel slog.Level

	// DataDir 存放当天数据库 today.db，必须位于闪存（OpenWrt 的 /var、/tmp 是内存盘）。
	DataDir string
	// ArchiveDir 是外部硬盘上的归档目录，为空表示不归档。
	ArchiveDir      string
	ArchiveKeepDays int
	// RotateHour、RotateMinute 是每日切换时刻，同时也是"统计日"的分界点。
	RotateHour   int
	RotateMinute int
	// Timezone 为 IANA 时区名，为空时自动读取系统设置。
	Timezone string

	// CollectInterval 同时是流量记录的时间粒度：每周期、每个 (设备, 目标) 组合最多一行。
	CollectInterval time.Duration
	// PolicyInterval 是自动检查管控策略的间隔。
	PolicyInterval    time.Duration
	ConntrackInterval time.Duration
	// MinFlowBytesPerMinute 是记录一个 (设备, 目标) 组合所需的最小流量速率（字节/分钟），
	// 用于过滤心跳等后台流量；按采集周期折算后生效，0 表示不过滤。
	MinFlowBytesPerMinute int64
	// MinFreeSpace 是 DataDir 所在分区需保留的剩余空间（字节），低于该值暂停写入明细，0 表示不检查。
	MinFreeSpace uint64

	// ManagedNetworks 为受管网段，为空时自动使用 LANDevice 上的 IPv4 网段。
	ManagedNetworks []netip.Prefix
	LANDevice       string

	DNSLogFile    string
	DNSLogMaxSize int64
	DNSTTL        time.Duration

	LeasesFile     string
	DHCPConfigFile string
	ARPFile        string
	ConntrackFile  string

	// HTTPPort 为 Web UI 端口，0 表示不启动 Web UI。
	HTTPPort int
	// HTTPAddress 为 Web UI 监听的 IP，为空时只监听 LANDevice 上的 IPv4 地址（不暴露到 WAN）。
	HTTPAddress string

	// ManageDnsmasq 为 true 时自动配置并重启 dnsmasq 以开启查询日志。
	ManageDnsmasq bool
	// DisableFlowOffload 为 true 时自动关闭 fw4 的流量卸载，
	// 否则已卸载的连接绕过 forward 链，nft 计数会严重偏少。
	DisableFlowOffload bool
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		LogLevel:              slog.LevelInfo,
		DataDir:               "/etc/leosentry/data",
		ArchiveKeepDays:       30,
		RotateHour:            3,
		CollectInterval:       time.Minute,
		PolicyInterval:        5 * time.Minute,
		ConntrackInterval:     10 * time.Second,
		MinFlowBytesPerMinute: 8 << 10,
		MinFreeSpace:          20 << 20,
		LANDevice:             "br-lan",
		DNSLogFile:            "/tmp/dnsmasq_query.log",
		DNSLogMaxSize:         8 << 20,
		DNSTTL:                30 * time.Minute,
		LeasesFile:            "/tmp/dhcp.leases",
		DHCPConfigFile:        "/etc/config/dhcp",
		ARPFile:               "/proc/net/arp",
		ConntrackFile:         "/proc/net/nf_conntrack",
		HTTPPort:              8088,
		ManageDnsmasq:         true,
		DisableFlowOffload:    true,
	}
}

// MinFlowBytes 返回按采集周期折算后的单周期最小记录字节数。
func (c Config) MinFlowBytes() int64 {
	return c.MinFlowBytesPerMinute * int64(c.CollectInterval) / int64(time.Minute)
}

// Load 从 UCI 配置文件加载配置；文件不存在时返回默认配置。
func Load(path string) (Config, error) {
	cfg := Default()
	f, err := uci.ParseFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	sections := f.ByType("leosentry")
	if len(sections) == 0 {
		return cfg, nil
	}
	if err := cfg.apply(sections[0]); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

func (c *Config) apply(s *uci.Section) error {
	var errs []error
	str := func(key string, dst *string) {
		if v, ok := s.Lookup(key); ok {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		if v, ok := s.Lookup(key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
				return
			}
			*dst = n
		}
	}
	seconds := func(key string, dst *time.Duration) {
		n := int(*dst / time.Second)
		num(key, &n)
		*dst = time.Duration(n) * time.Second
	}
	boolean := func(key string, dst *bool) {
		if v, ok := s.Lookup(key); ok {
			*dst = v == "1" || v == "true" || v == "yes" || v == "on"
		}
	}

	if v, ok := s.Lookup("log_level"); ok {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("log_level: %w", err))
		}
	}
	str("data_dir", &c.DataDir)
	str("archive_dir", &c.ArchiveDir)
	num("archive_keep_days", &c.ArchiveKeepDays)
	if v, ok := s.Lookup("rotate_at"); ok {
		h, m, err := parseClock(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("rotate_at: %w", err))
		}
		c.RotateHour, c.RotateMinute = h, m
	}
	str("timezone", &c.Timezone)
	seconds("collect_interval", &c.CollectInterval)
	seconds("policy_interval", &c.PolicyInterval)
	seconds("conntrack_interval", &c.ConntrackInterval)
	if v, ok := s.Lookup("min_flow_kb_per_min"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("min_flow_kb_per_min: invalid value %q", v))
		}
		c.MinFlowBytesPerMinute = n << 10
	}
	if v, ok := s.Lookup("min_free_mb"); ok {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("min_free_mb: %w", err))
		}
		c.MinFreeSpace = n << 20
	}
	for _, v := range s.List("managed_network") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("managed_network: %w", err))
			continue
		}
		c.ManagedNetworks = append(c.ManagedNetworks, p.Masked())
	}
	str("lan_device", &c.LANDevice)
	str("dns_log_file", &c.DNSLogFile)
	if v, ok := s.Lookup("dns_log_max_size_mb"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("dns_log_max_size_mb: %w", err))
		}
		c.DNSLogMaxSize = int64(n) << 20
	}
	seconds("dns_ttl", &c.DNSTTL)
	str("leases_file", &c.LeasesFile)
	str("dhcp_config_file", &c.DHCPConfigFile)
	num("http_port", &c.HTTPPort)
	str("http_address", &c.HTTPAddress)
	boolean("manage_dnsmasq", &c.ManageDnsmasq)
	boolean("disable_flow_offload", &c.DisableFlowOffload)
	return errors.Join(errs...)
}

// Validate 校验配置取值范围。
func (c Config) Validate() error {
	var errs []error
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir is required"))
	}
	if c.ArchiveKeepDays < 1 {
		errs = append(errs, errors.New("archive_keep_days must be >= 1"))
	}
	if c.CollectInterval < time.Second {
		errs = append(errs, errors.New("collect_interval must be >= 1s"))
	}
	if c.PolicyInterval < time.Second {
		errs = append(errs, errors.New("policy_interval must be >= 1s"))
	}
	if c.MinFlowBytesPerMinute < 0 {
		errs = append(errs, errors.New("min_flow_kb_per_min must be >= 0"))
	}
	if c.ConntrackInterval < time.Second {
		errs = append(errs, errors.New("conntrack_interval must be >= 1s"))
	}
	if c.DNSLogMaxSize < 1<<20 {
		errs = append(errs, errors.New("dns_log_max_size_mb must be >= 1"))
	}
	if c.DNSTTL < time.Minute {
		errs = append(errs, errors.New("dns_ttl must be >= 60s"))
	}
	if c.HTTPPort < 0 || c.HTTPPort > 65535 {
		errs = append(errs, errors.New("http_port must be within 0-65535"))
	}
	if c.HTTPAddress != "" {
		if _, err := netip.ParseAddr(c.HTTPAddress); err != nil {
			errs = append(errs, fmt.Errorf("http_address: %w", err))
		}
	}
	for _, p := range c.ManagedNetworks {
		if !p.Addr().Is4() {
			errs = append(errs, fmt.Errorf("managed_network %s: only IPv4 is supported", p))
		}
	}
	return errors.Join(errs...)
}

// DefaultUCI 以 UCI 格式渲染默认配置，供安装时生成 /etc/config/leosentry。
func DefaultUCI() string {
	d := Default()
	var b strings.Builder
	w := func(key, val string) { fmt.Fprintf(&b, "\toption %s '%s'\n", key, val) }
	b.WriteString("config leosentry 'main'\n")
	w("log_level", strings.ToLower(d.LogLevel.String()))
	w("data_dir", d.DataDir)
	b.WriteString("\t# 外部硬盘归档目录，例如 /mnt/sda1/leosentry_archive；留空表示不归档\n")
	w("archive_dir", d.ArchiveDir)
	w("archive_keep_days", strconv.Itoa(d.ArchiveKeepDays))
	w("rotate_at", fmt.Sprintf("%02d:%02d", d.RotateHour, d.RotateMinute))
	b.WriteString("\t# 留空则读取 system.@system[0].zonename\n")
	w("timezone", d.Timezone)
	b.WriteString("\t# 采集周期（秒），同时是流量记录的时间粒度\n")
	w("collect_interval", strconv.Itoa(int(d.CollectInterval/time.Second)))
	b.WriteString("\t# 自动检查管控策略的间隔（秒）\n")
	w("policy_interval", strconv.Itoa(int(d.PolicyInterval/time.Second)))
	w("conntrack_interval", strconv.Itoa(int(d.ConntrackInterval/time.Second)))
	b.WriteString("\t# 每分钟流量低于该值（KB）的 设备→目标 组合不记录，用于过滤心跳等后台流量；0 表示全部记录\n")
	w("min_flow_kb_per_min", strconv.FormatInt(d.MinFlowBytesPerMinute>>10, 10))
	b.WriteString("\t# data_dir 所在分区剩余空间低于该值（MB）时暂停写入明细；0 表示不检查\n")
	w("min_free_mb", strconv.FormatUint(d.MinFreeSpace>>20, 10))
	b.WriteString("\t# 受管网段，不配置时自动取 lan_device 上的 IPv4 网段\n")
	b.WriteString("\t# list managed_network '192.168.1.0/24'\n")
	w("lan_device", d.LANDevice)
	w("dns_log_file", d.DNSLogFile)
	w("dns_log_max_size_mb", strconv.FormatInt(d.DNSLogMaxSize>>20, 10))
	w("dns_ttl", strconv.Itoa(int(d.DNSTTL/time.Second)))
	w("leases_file", d.LeasesFile)
	w("dhcp_config_file", d.DHCPConfigFile)
	b.WriteString("\t# Web 管理页面端口，0 表示关闭；http_address 留空则只监听 lan_device 上的地址\n")
	w("http_port", strconv.Itoa(d.HTTPPort))
	w("http_address", d.HTTPAddress)
	w("manage_dnsmasq", "1")
	w("disable_flow_offload", "1")
	return b.String()
}

func parseClock(v string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}
