//go:build !linux

package nftstats

import "errors"

// Reader 在非 Linux 平台上不可用，仅用于保证开发机可以编译与测试。
type Reader struct{}

func NewReader() (*Reader, error) { return nil, errors.ErrUnsupported }

func (r *Reader) Read() (map[FlowPair]Traffic, error) { return nil, errors.ErrUnsupported }
func (r *Reader) Close() error                        { return nil }
