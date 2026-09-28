// Package installer 实现 `leosentry install / uninstall`：
// 把程序安装为 procd 服务并设置开机自启，免去手工部署步骤。
package installer

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leo/leosentry/internal/config"
)

const (
	binPath  = "/usr/bin/leosentry"
	initPath = "/etc/init.d/leosentry"
)

//go:embed leosentry.init
var initScript []byte

// Install 安装并启动服务：
//  1. 复制当前可执行文件到 /usr/bin/leosentry；
//  2. 写入 procd 启动脚本 /etc/init.d/leosentry；
//  3. /etc/config/leosentry 不存在时写入默认配置（已存在则保留用户配置）；
//  4. 设置开机自启并（重新）启动服务。
func Install(ctx context.Context, log *slog.Logger) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if self != binPath {
		if err := copyExecutable(self, binPath); err != nil {
			return fmt.Errorf("install binary: %w", err)
		}
		log.Info("binary installed", "path", binPath)
	}

	if err := writeFileAtomic(initPath, initScript, 0o755); err != nil {
		return fmt.Errorf("install init script: %w", err)
	}
	log.Info("init script installed", "path", initPath)

	if _, err := os.Stat(config.DefaultPath); errors.Is(err, fs.ErrNotExist) {
		if err := writeFileAtomic(config.DefaultPath, []byte(config.DefaultUCI()), 0o644); err != nil {
			return fmt.Errorf("write default config: %w", err)
		}
		log.Info("default config written", "path", config.DefaultPath)
	}

	if err := run(ctx, initPath, "enable"); err != nil {
		return err
	}
	if err := run(ctx, initPath, "restart"); err != nil {
		return err
	}
	log.Info("service enabled and started")
	return nil
}

// Uninstall 停止并移除服务，保留配置文件与数据目录。
func Uninstall(ctx context.Context, log *slog.Logger) error {
	if _, err := os.Stat(initPath); err == nil {
		if err := run(ctx, initPath, "stop"); err != nil {
			log.Warn("stop service", "err", err)
		}
		if err := run(ctx, initPath, "disable"); err != nil {
			log.Warn("disable service", "err", err)
		}
	}
	for _, p := range []string{initPath, binPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	log.Info("service removed; config and data kept", "config", config.DefaultPath)
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
