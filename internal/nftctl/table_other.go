//go:build !linux

package nftctl

import "errors"

// Controller 在非 Linux 平台上不可用，仅用于保证开发机可以编译与测试。
type Controller struct{}

func New() (*Controller, error) {
	return nil, errors.ErrUnsupported
}

func (c *Controller) Setup(Options) error { return errors.ErrUnsupported }
func (c *Controller) Teardown() error     { return errors.ErrUnsupported }
func (c *Controller) Close() error        { return nil }
