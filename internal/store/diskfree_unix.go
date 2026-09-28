//go:build unix

package store

import "golang.org/x/sys/unix"

// freeSpace 返回 dir 所在分区对非特权用户可用的剩余字节数。
func freeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
