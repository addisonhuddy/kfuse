// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

func TestMapErrno(t *testing.T) {
	missing, err := os.Open(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		_ = missing.Close()
		t.Fatal("os.Open missing file succeeded")
	}

	tests := []struct {
		name string
		err  error
		want syscall.Errno
	}{
		{name: "nil", err: nil, want: 0},
		{name: "errno", err: syscall.ENOENT, want: syscall.ENOENT},
		{name: "wrapped errno", err: fmt.Errorf("wrap: %w", syscall.EACCES), want: syscall.EACCES},
		{name: "missing file", err: err, want: syscall.ENOENT},
		{name: "wrapped not exist", err: fmt.Errorf("x: %w", os.ErrNotExist), want: syscall.ENOENT},
		{name: "lease lost", err: session.ErrLeaseLost, want: syscall.EROFS},
		{name: "log diverged", err: fmt.Errorf("%w: kafka down", session.ErrLogDiverged), want: syscall.EROFS},
		{name: "invalid path", err: fmt.Errorf("%w: non-UTF-8", upper.ErrInvalidPath), want: syscall.EINVAL},
		{name: "exists", err: upper.ErrExists, want: syscall.EEXIST},
		{name: "canceled", err: context.Canceled, want: syscall.EINTR},
		{name: "deadline", err: context.DeadlineExceeded, want: syscall.ETIMEDOUT},
		{name: "other", err: errors.New("boom"), want: syscall.EIO},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapErrno(tt.err); got != tt.want {
				t.Errorf("mapErrno() = %v, want %v", got, tt.want)
			}
			if got := errnoOf(tt.err); got != tt.want {
				t.Errorf("errnoOf() = %v, want %v", got, tt.want)
			}
			if got := commitErrno("test", "path", tt.err); got != tt.want {
				t.Errorf("commitErrno() = %v, want %v", got, tt.want)
			}
		})
	}
}
