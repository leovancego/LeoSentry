//go:build !linux

package conntrack

import "errors"

func NewNetlinkSource() (Source, error) { return nil, errors.ErrUnsupported }
