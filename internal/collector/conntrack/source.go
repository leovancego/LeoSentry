package conntrack

import (
	"errors"
	"fmt"
)

// OpenSource 优先使用 netlink，失败时回退到 procfs。
func OpenSource(procfsPath string) (Source, error) {
	src, nlErr := NewNetlinkSource()
	if nlErr == nil {
		return src, nil
	}
	src, pfErr := NewProcfsSource(procfsPath)
	if pfErr == nil {
		return src, nil
	}
	return nil, fmt.Errorf("conntrack unavailable: %w", errors.Join(nlErr, pfErr))
}
