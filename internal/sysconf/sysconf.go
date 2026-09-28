// Package sysconf 在启动时自动完成 LeoSentry 依赖的系统设置，无需人工执行命令。
// 所有操作都是幂等的：先读当前值，只有与期望不一致时才修改、提交并重启相关服务。
package sysconf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/uci"
)

const commandTimeout = 30 * time.Second

// Options 配置需要自动管理的系统设置。
type Options struct {
	// ManageDnsmasq 为 true 时为所有 dnsmasq 实例开启查询日志并输出到 DNSLogFile。
	ManageDnsmasq  bool
	DNSLogFile     string
	DHCPConfigFile string
	// DisableFlowOffload 为 true 时关闭 fw4 的软件/硬件流量卸载。
	DisableFlowOffload bool
	Logger             *slog.Logger
}

// Apply 应用系统设置。单项失败不影响其他项，错误合并返回。
func Apply(ctx context.Context, opts Options) error {
	cli := uci.CLI{}
	if !cli.Available() {
		opts.Logger.Warn("uci not found, skipping system configuration (not running on OpenWrt?)")
		return nil
	}
	var errs []error
	if opts.ManageDnsmasq {
		if err := ensureDnsmasqLogging(ctx, cli, opts); err != nil {
			errs = append(errs, fmt.Errorf("dnsmasq: %w", err))
		}
	}
	if opts.DisableFlowOffload {
		if err := ensureFlowOffloadDisabled(ctx, cli, opts.Logger); err != nil {
			errs = append(errs, fmt.Errorf("firewall: %w", err))
		}
	}
	return errors.Join(errs...)
}

// ensureFlowOffloadDisabled 等价于：
//
//	uci set firewall.@defaults[0].flow_offloading='0'
//	uci set firewall.@defaults[0].flow_offloading_hw='0'
//	uci commit firewall && /etc/init.d/firewall reload
//
// 流量卸载开启后，已建立连接的数据包在 ingress 阶段走 flowtable 快速路径，
// 不再经过 forward 钩子，LeoSentry 的 nft 计数器只能统计到每条连接最初的几个包。
func ensureFlowOffloadDisabled(ctx context.Context, cli uci.CLI, log *slog.Logger) error {
	changed := false
	for _, opt := range []string{"flow_offloading", "flow_offloading_hw"} {
		key := "firewall.@defaults[0]." + opt
		cur, _, err := cli.Get(ctx, key)
		if err != nil {
			return err
		}
		if cur != "1" {
			continue
		}
		if err := cli.Set(ctx, key, "0"); err != nil {
			return err
		}
		log.Warn("flow offloading disabled so that nft counters see every forwarded packet", "option", key)
		changed = true
	}
	if !changed {
		return nil
	}
	if err := cli.Commit(ctx, "firewall"); err != nil {
		return err
	}
	return runCommand(ctx, "/etc/init.d/firewall", "reload")
}

func runCommand(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
