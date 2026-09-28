package installer

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ErrNotService 表示当前进程不是由 install 装上的系统服务，页面无法再把它拉起来。
var ErrNotService = errors.New("没有安装成系统服务，不能从这里重启")

var (
	restartMu   sync.Mutex
	restartBusy bool
)

// Restart 先把页面的响应送出去，再调用 init 脚本。
// 脚本会让 procd 发 SIGTERM：采集循环把本周期流量补写进库、数据库和 nft 表按平时退出清理，
// 然后用和开机相同的方式重新启动。
func Restart(log *slog.Logger) error {
	return scheduleRestart(initPath, time.Second, log)
}

func scheduleRestart(script string, delay time.Duration, log *slog.Logger) error {
	info, err := os.Stat(script)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ErrNotService
	}
	restartMu.Lock()
	if restartBusy {
		restartMu.Unlock()
		return nil
	}
	restartBusy = true
	restartMu.Unlock()
	if log != nil {
		log.Info("scheduling service restart")
	}
	go func() {
		time.Sleep(delay)
		// 新会话，避免停服务时的信号把这段启动脚本一起带走。
		cmd := exec.Command(script, "restart")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			restartMu.Lock()
			restartBusy = false
			restartMu.Unlock()
			if log != nil {
				log.Error("restart service", "err", err)
			}
			return
		}
		if log != nil {
			log.Info("service restart started")
		}
	}()
	return nil
}
