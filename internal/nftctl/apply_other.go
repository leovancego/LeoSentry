//go:build !linux

package nftctl

import "errors"

// Apply 在非 Linux 上不可用。
func (c *Controller) Apply(Targets) (bool, error) { return false, errors.ErrUnsupported }
