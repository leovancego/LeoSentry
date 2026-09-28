package store

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// internalMountPoints 是 OpenWrt 上属于内部闪存或内存盘的挂载点。
var internalMountPoints = map[string]bool{"/": true, "/overlay": true, "/rom": true}

var nonDiskFSTypes = map[string]bool{
	"tmpfs": true, "ramfs": true, "squashfs": true, "overlay": true,
	"proc": true, "sysfs": true, "devtmpfs": true, "debugfs": true,
}

// onExternalStorage 判断 dir 是否位于外部硬盘：取 /proc/mounts 中路径前缀最长的挂载点，
// 该挂载点既不能是根/overlay 等内部分区，也不能是内存文件系统。
// 硬盘未挂载时 /mnt/sda1 只是 overlay 上的普通目录，据此可以识别出来，避免把归档写进闪存。
func onExternalStorage(dir string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	return onExternalStorageIn(data, dir)
}

func onExternalStorageIn(mounts []byte, dir string) bool {
	dir = filepath.Clean(dir)
	var bestPoint, bestType string
	sc := bufio.NewScanner(bytes.NewReader(mounts))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		point := unescapeMount(f[1])
		if hasPathPrefix(dir, point) && len(point) >= len(bestPoint) {
			bestPoint, bestType = point, f[2]
		}
	}
	if bestPoint == "" || internalMountPoints[bestPoint] || nonDiskFSTypes[bestType] {
		return false
	}
	return true
}

func hasPathPrefix(path, prefix string) bool {
	return prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// unescapeMount 还原 /proc/mounts 中的八进制转义（如空格写作 \040）。
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
