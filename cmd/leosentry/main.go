package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	_ "time/tzdata"

	"github.com/leo/leosentry/internal/app"
	"github.com/leo/leosentry/internal/config"
	"github.com/leo/leosentry/internal/installer"
)

var version = "dev"

const usage = `LeoSentry - 家庭上网行为监测与管控

用法:
  leosentry [-config 路径]    以前台方式运行服务（procd 调用此方式）
  leosentry install          安装为系统服务并设置开机自启
  leosentry uninstall        移除系统服务（保留配置与数据）
  leosentry version          显示版本

选项:
`

func main() {
	configPath := flag.String("config", config.DefaultPath, "UCI 配置文件路径")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd := flag.Arg(0); cmd {
	case "", "run":
		err = run(ctx, *configPath)
	case "install":
		err = requireRoot(func() error { return installer.Install(ctx, newLogger(slog.LevelInfo)) })
	case "uninstall":
		err = requireRoot(func() error { return installer.Uninstall(ctx, newLogger(slog.LevelInfo)) })
	case "version":
		fmt.Printf("leosentry %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "leosentry:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return requireRoot(func() error {
		return app.Run(ctx, cfg, newLogger(cfg.LogLevel))
	})
}

func requireRoot(fn func() error) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	return fn()
}

// newLogger 输出到 stdout：procd 把 stdout 记为 daemon.info、stderr 记为 daemon.err。
// 非终端输出时省略时间字段，logread 已自带时间戳。
func newLogger(level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if st, err := os.Stdout.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
