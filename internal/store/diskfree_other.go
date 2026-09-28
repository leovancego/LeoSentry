//go:build !unix

package store

import "errors"

func freeSpace(string) (uint64, error) { return 0, errors.ErrUnsupported }
