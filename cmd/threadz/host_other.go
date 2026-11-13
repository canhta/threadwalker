//go:build !unix

package main

import (
	"context"
	"errors"
	"io"
	"syscall"
)

var errHostUnsupported = errors.New("not supported on this platform")

func freeDiskBytes(string) (uint64, error) { return 0, errHostUnsupported }

func runJournalctl(context.Context, []string) (io.ReadCloser, error) {
	return nil, errHostUnsupported
}

func ownerAttr(string) *syscall.SysProcAttr { return nil }
