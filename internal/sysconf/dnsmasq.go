package sysconf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leo/leosentry/internal/uci"
)

const (
	// dnsmasq 启动时配置出错会立即退出，procd 每 5 秒重试一次；
	// 等待 healthSettle 后采样一次 pid，再隔 healthRecheck 复查，pid 不变才算启动成功。
	healthSettle  = 2 * time.Second
	healthRecheck = 6 * time.Second
	conflictMark  = "  # disabled by leosentry"
)

// conflictingDirectives 是与 UCI logfacility 生成的指令冲突、会导致 dnsmasq 以
// "illegal repeated keyword" 拒绝启动的指令。ImmortalWrt 默认的 /etc/dnsmasq.conf
// 带有 log-facility=/dev/null。
var conflictingDirectives = []string{"log-facility"}

// ensureDnsmasqLogging 等价于对每个 dnsmasq 实例执行：
//
//	sed -i 's/^log-facility=/# &/' /etc/dnsmasq.conf     # 注释掉冲突指令
//	uci set dhcp.@dnsmasq[i].logqueries='1'
//	uci set dhcp.@dnsmasq[i].logfacility='<DNSLogFile>'
//	uci commit dhcp && /etc/init.d/dnsmasq restart
//
// OpenWrt 的 dnsmasq 启动脚本按布尔值读取 logqueries，为真时生成 --log-queries=extra，
// 因此这里必须写 '1'（写 'extra' 会被当作假而关闭日志）。
//
// dnsmasq 同时提供全家的 DNS 与 DHCP，重启后若未能稳定运行，
// 立即撤销本次全部修改并再次重启，宁可放弃域名采集也不能让网络瘫痪。
func ensureDnsmasqLogging(ctx context.Context, cli uci.CLI, opts Options) error {
	f, err := uci.ParseFile(opts.DHCPConfigFile)
	if err != nil {
		return err
	}
	instances := len(f.ByType("dnsmasq"))
	if instances == 0 {
		return errors.New("no dnsmasq section in " + opts.DHCPConfigFile)
	}

	var undo []func(context.Context) error
	rollback := func() error {
		var errs []error
		for i := len(undo) - 1; i >= 0; i-- {
			errs = append(errs, undo[i](ctx))
		}
		return errors.Join(errs...)
	}

	changed := false
	want := [][2]string{{"logqueries", "1"}, {"logfacility", opts.DNSLogFile}}
	for i := range instances {
		ref := fmt.Sprintf("dhcp.@dnsmasq[%d]", i)
		name, err := cli.SectionName(ctx, ref)
		if err != nil {
			return errors.Join(err, rollback())
		}
		conf := dnsmasqConfFile(name)
		original, ok, err := disableConflicts(conf)
		if err != nil {
			return errors.Join(err, rollback())
		}
		if ok {
			opts.Logger.Info("conflicting dnsmasq directive commented out", "file", conf, "directives", conflictingDirectives)
			undo = append(undo, func(context.Context) error { return os.WriteFile(conf, original, 0o644) })
			changed = true
		}
		for _, kv := range want {
			key := ref + "." + kv[0]
			old, existed, updated, err := setIfDifferent(ctx, cli, key, kv[1], opts.Logger)
			if err != nil {
				return errors.Join(err, rollback())
			}
			if updated {
				undo = append(undo, func(ctx context.Context) error {
					if existed {
						return cli.Set(ctx, key, old)
					}
					return cli.Delete(ctx, key)
				})
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}

	if err := cli.Commit(ctx, "dhcp"); err != nil {
		return errors.Join(err, rollback(), cli.Commit(ctx, "dhcp"))
	}
	opts.Logger.Info("dnsmasq query logging enabled, restarting dnsmasq", "log_file", opts.DNSLogFile)
	startErr := restartDnsmasq(ctx)
	if startErr == nil {
		return nil
	}

	opts.Logger.Error("dnsmasq failed to start with query logging, rolling back", "err", startErr)
	if err := errors.Join(rollback(), cli.Commit(ctx, "dhcp")); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", startErr, err)
	}
	if err := restartDnsmasq(ctx); err != nil {
		return fmt.Errorf("%w; dnsmasq still failing after rollback: %v", startErr, err)
	}
	return fmt.Errorf("query logging not enabled, changes rolled back: %w", startErr)
}

// dnsmasqConfFile 与 dnsmasq 启动脚本一致：优先 /etc/dnsmasq.<段名>.conf，不存在时用 /etc/dnsmasq.conf。
func dnsmasqConfFile(section string) string {
	p := "/etc/dnsmasq." + section + ".conf"
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "/etc/dnsmasq.conf"
}

// disableConflicts 注释掉文件中的冲突指令，返回原始内容以便回滚。文件不存在视为无冲突。
func disableConflicts(path string) (original []byte, changed bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if isDirective(line, conflictingDirectives) {
			lines[i] = "# " + line + conflictMark
			changed = true
		}
	}
	if !changed {
		return nil, false, nil
	}
	if err := writeFileAtomic(path, []byte(strings.Join(lines, "\n"))); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func isDirective(line string, names []string) bool {
	line = strings.TrimSpace(line)
	for _, n := range names {
		rest, ok := strings.CutPrefix(line, n)
		if ok && (rest == "" || rest[0] == '=' || rest[0] == ' ' || rest[0] == '\t') {
			return true
		}
	}
	return false
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".leosentry-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// restartDnsmasq 重启 dnsmasq 并确认所有实例稳定运行。
func restartDnsmasq(ctx context.Context) error {
	if err := runCommand(ctx, "/etc/init.d/dnsmasq", "restart"); err != nil {
		return err
	}
	if err := sleepCtx(ctx, healthSettle); err != nil {
		return err
	}
	first, err := dnsmasqPIDs(ctx)
	if err != nil {
		return err
	}
	if err := sleepCtx(ctx, healthRecheck); err != nil {
		return err
	}
	second, err := dnsmasqPIDs(ctx)
	if err != nil {
		return err
	}
	if !maps.Equal(first, second) {
		return errors.New("dnsmasq is restarting repeatedly (crash loop)")
	}
	return nil
}

// dnsmasqPIDs 通过 procd 查询 dnsmasq 各实例的 pid，任一实例未运行即返回错误。
func dnsmasqPIDs(ctx context.Context) (map[string]int, error) {
	out, err := exec.CommandContext(ctx, "ubus", "call", "service", "list", `{"name":"dnsmasq"}`).Output()
	if err != nil {
		return nil, fmt.Errorf("ubus service list: %w", err)
	}
	return parseServicePIDs(out, "dnsmasq")
}

func parseServicePIDs(data []byte, service string) (map[string]int, error) {
	var resp map[string]struct {
		Instances map[string]struct {
			Running  bool `json:"running"`
			PID      int  `json:"pid"`
			ExitCode *int `json:"exit_code"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse ubus output: %w", err)
	}
	svc, ok := resp[service]
	if !ok || len(svc.Instances) == 0 {
		return nil, fmt.Errorf("%s has no instances", service)
	}
	pids := make(map[string]int, len(svc.Instances))
	for name, inst := range svc.Instances {
		if !inst.Running {
			if inst.ExitCode != nil {
				return nil, fmt.Errorf("%s instance %s not running (exit code %d)", service, name, *inst.ExitCode)
			}
			return nil, fmt.Errorf("%s instance %s not running", service, name)
		}
		pids[name] = inst.PID
	}
	return pids, nil
}

func setIfDifferent(ctx context.Context, cli uci.CLI, key, want string, log *slog.Logger) (old string, existed, updated bool, err error) {
	old, existed, err = cli.Get(ctx, key)
	if err != nil || old == want {
		return old, existed, false, err
	}
	if err := cli.Set(ctx, key, want); err != nil {
		return old, existed, false, err
	}
	log.Info("uci option updated", "key", key, "old", old, "new", want)
	return old, existed, true, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
