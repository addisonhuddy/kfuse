// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"

	"github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// mapErrno translates err into the errno reported to the kernel.
func mapErrno(err error) syscall.Errno {
	var errno syscall.Errno
	switch {
	case err == nil:
		return 0
	case errors.As(err, &errno):
		return errno
	case errors.Is(err, session.ErrLeaseLost), errors.Is(err, session.ErrLogDiverged):
		return syscall.EROFS
	// Upper-state preconditions refused by the session under its commit lock:
	// the FUSE-level check raced with another mutation, so report the POSIX
	// outcome the loser would have seen had it checked second.
	case errors.Is(err, upper.ErrExists):
		return syscall.EEXIST
	case errors.Is(err, upper.ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, upper.ErrIsDir):
		return syscall.EISDIR
	case errors.Is(err, upper.ErrNotDir):
		return syscall.ENOTDIR
	case errors.Is(err, upper.ErrNotFile), errors.Is(err, upper.ErrInvalidPath):
		return syscall.EINVAL
	case errors.Is(err, upper.ErrNoEntry), errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	case errors.Is(err, context.DeadlineExceeded):
		return syscall.ETIMEDOUT
	}
	return syscall.EIO
}

func errnoOf(err error) syscall.Errno {
	mapped := mapErrno(err)
	if mapped == syscall.EIO && strings.Contains(err.Error(), "no such file") {
		return syscall.ENOENT
	}
	return mapped
}

// commitErrno maps a failed commit to the errno the kernel sees. The cause is
// logged first: the FUSE reply carries only an errno, so an unlogged commit
// failure (lease lost, Kafka or S3 outage) is invisible to the operator.
func commitErrno(op, rel string, err error) syscall.Errno {
	if err == nil {
		return 0
	}
	log.Error("commit failed", "op", op, "path", rel, log.Err(err))
	return mapErrno(err)
}
