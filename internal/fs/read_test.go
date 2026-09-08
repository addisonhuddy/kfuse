// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

func TestReadWindow(t *testing.T) {
	cases := []struct {
		name  string
		off   int64
		want  int
		size  int64
		n     int64
		errno syscall.Errno
	}{
		{name: "whole file", off: 0, want: 4096, size: 10, n: 10},
		{name: "clamped to buffer", off: 0, want: 4, size: 10, n: 4},
		{name: "at eof", off: 10, want: 8, size: 10, n: 0},
		{name: "past eof", off: 99, want: 8, size: 10, n: 0},
		{name: "exactly at the cap", off: 0, want: maxReadSize, size: math.MaxInt64, n: maxReadSize},
		{name: "over the cap", off: 0, want: maxReadSize + 1, size: math.MaxInt64, errno: syscall.EIO},
		// A corrupt size event must not make the end wrap negative and skip
		// the EOF clamp, handing back a window the caller never asked for.
		{name: "offset+want overflows", off: math.MaxInt64 - 8, want: 4096, size: math.MaxInt64, n: 8},
		{name: "negative offset", off: -1, want: 8, size: 10, errno: syscall.EIO},
		{name: "negative size", off: 0, want: 8, size: -5, errno: syscall.EIO},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, errno := readWindow(tc.off, tc.want, tc.size)
			if errno != tc.errno {
				t.Fatalf("errno = %v, want %v", errno, tc.errno)
			}
			if n != tc.n {
				t.Errorf("n = %d, want %d", n, tc.n)
			}
		})
	}
}

// An oversized request must fail the read rather than allocate for it, and a
// normal read through the same path must keep working.
func TestReadRefusesOversizedWindow(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "a.txt"), []byte("abcdefgh"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower)
	h := m.newHandle(func() string { return "a.txt" })

	if _, errno := h.Read(t.Context(), make([]byte, 4), -1); errno != syscall.EIO {
		t.Fatalf("read at a negative offset: errno = %v, want EIO", errno)
	}

	res, errno := h.Read(t.Context(), make([]byte, 4), 2)
	if errno != 0 {
		t.Fatalf("read: errno = %v", errno)
	}
	got, status := res.Bytes(make([]byte, 4))
	if status != 0 {
		t.Fatalf("result bytes: %v", status)
	}
	if string(got) != "cdef" {
		t.Errorf("read = %q, want %q", got, "cdef")
	}
}

// A truncate event carrying an absurd size must not let a read allocate a
// window bigger than the cap.
func TestReadRefusesCorruptSizeEvent(t *testing.T) {
	size := uint64(math.MaxInt64)
	m := newMounter(t, t.TempDir(),
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Setattr{Setattr: &kfusev1.Setattr{
			Path: "f", Size: &size,
		}}},
	)
	h := m.newHandle(func() string { return "f" })
	if _, errno := h.Read(t.Context(), make([]byte, 8), math.MaxInt64-4); errno != 0 {
		t.Fatalf("a small window near the end of a huge file should still read: errno = %v", errno)
	}
	if _, errno := readWindow(0, maxReadSize+1, int64(size)); errno != syscall.EIO {
		t.Fatalf("errno = %v, want EIO", errno)
	}
}
