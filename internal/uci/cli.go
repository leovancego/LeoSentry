package uci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CLI 通过 uci 命令行读写系统配置。
type CLI struct {
	// Bin 为 uci 可执行文件路径，为空时使用 PATH 中的 uci。
	Bin string
}

// Available 报告当前系统是否提供 uci 命令（即是否运行在 OpenWrt 上）。
func (c CLI) Available() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// Get 读取配置项，例如 "dhcp.@dnsmasq[0].logqueries"。
// 配置项不存在时返回 ok=false 且 err=nil。
func (c CLI) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	out, err := c.run(ctx, "-q", "get", key)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// Set 设置配置项，修改在 Commit 之前只存在于 uci 暂存区。
func (c CLI) Set(ctx context.Context, key, value string) error {
	_, err := c.run(ctx, "set", key+"="+value)
	return err
}

// Delete 删除配置项，配置项不存在时不报错。
func (c CLI) Delete(ctx context.Context, key string) error {
	if _, ok, err := c.Get(ctx, key); err != nil || !ok {
		return err
	}
	_, err := c.run(ctx, "delete", key)
	return err
}

// SectionName 把 "dhcp.@dnsmasq[0]" 这类匿名段引用解析为实际段名（如 "cfg01411c"）。
func (c CLI) SectionName(ctx context.Context, ref string) (string, error) {
	out, err := c.run(ctx, "-q", "show", ref)
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	path, _, ok := strings.Cut(first, "=")
	if !ok {
		return "", fmt.Errorf("uci show %s: unexpected output %q", ref, first)
	}
	_, name, ok := strings.Cut(path, ".")
	if !ok || name == "" {
		return "", fmt.Errorf("uci show %s: unexpected output %q", ref, first)
	}
	return name, nil
}

// Commit 提交指定配置包（如 "dhcp"、"firewall"）的暂存修改。
func (c CLI) Commit(ctx context.Context, pkg string) error {
	_, err := c.run(ctx, "commit", pkg)
	return err
}

func (c CLI) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "uci"
}

func (c CLI) run(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("uci %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("uci %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
