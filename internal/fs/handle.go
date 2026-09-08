// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"context"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	"github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/perf"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// ---------------------------------------------------------------------------
// Handles

// maxReadSize bounds the buffer one read may allocate. The kernel caps FUSE
// reads orders of magnitude below this, so a bigger window can only come from
// a corrupt size event or a bogus request: refuse it instead of allocating
// for it.
const maxReadSize = 1 << 30 // 1 GiB

// readWindow returns how many bytes a read at off may serve from a file of
// size bytes into a want-byte buffer, or EIO for a window that must not be
// allocated: a negative offset/size, or one larger than maxReadSize. The
// arithmetic is overflow-safe, so an absurd size event cannot turn
// off+want into a negative end that skips the EOF clamp.
func readWindow(off int64, want int, size int64) (int64, syscall.Errno) {
	if off < 0 || want < 0 || size < 0 {
		return 0, syscall.EIO
	}
	if off >= size {
		return 0, 0
	}
	end := off + int64(want)
	if end < off || end > size {
		end = size
	}
	if end-off > maxReadSize {
		return 0, syscall.EIO
	}
	return end - off, 0
}

type overlayHandle struct {
	m    *Mounter
	path func() string
	mu   *sync.Mutex
}

// newHandle opens an overlay handle over the path a node reports (the node
// may be renamed while open, so the path is resolved per call).
func (m *Mounter) newHandle(path func() string) *overlayHandle {
	return &overlayHandle{m: m, path: path, mu: &sync.Mutex{}}
}

func (h *overlayHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	readStart := time.Now()
	rel := h.path()
	// One snapshot serves the whole read: the node can be renamed or truncated
	// by a concurrent commit, and mixing a fresh size with stale extents (or
	// vice versa) would hand back bytes from neither state.
	e, err := h.m.resolve(rel)
	if err != nil {
		log.Error("resolve for read failed", "path", rel, log.Err(err))
		return nil, errnoOf(err)
	}
	u, hasNode, size := e.upper, e.upper != nil, e.size
	if hasNode && u.Kind != upper.KindFile {
		return nil, syscall.ENOENT
	}
	n, errno := readWindow(off, len(dest), size)
	if errno != 0 {
		log.Error("read window refused", "path", rel, "off", off, "want", len(dest), "size", size)
		return nil, errno
	}
	if n == 0 {
		return fuse.ReadResultData(nil), 0
	}
	data := make([]byte, n)
	// Sparse CoW: unwritten ranges fall through to the lower file. This covers
	// both fallthrough nodes (gaps between extents) and pure-lower opens
	// (no node yet). Fallthrough nodes read from their fixed LowerRel so a
	// rename keeps holes on the original lower path.
	if !hasNode || u.Fallthrough {
		if err := h.m.readLowerInto(fallthroughSrc(rel, u), off, data); err != nil {
			log.Error("read lower failed", "path", rel, log.Err(err))
			return nil, errnoOf(err)
		}
	}
	if hasNode {
		get := func(id string) ([]byte, error) { return h.m.Blobs.Get(ctx, id) }
		if err := mergeExtents(u.Extents, off, data, get); err != nil {
			log.Error("read failed", "path", rel, log.Err(err))
			return nil, syscall.EIO
		}
	}
	if perf.Enabled() {
		perf.Emit("fuse_read",
			perf.I64("ns", perf.Since(readStart)),
			perf.I64("bytes", int64(len(data))),
			perf.Bool("overlay", hasNode))
	}
	return fuse.ReadResultData(data), 0
}

func (h *overlayHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	writeStart := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	rel := h.path()
	e, err := h.m.resolve(rel)
	if err != nil {
		log.Error("resolve for write failed", "path", rel, log.Err(err))
		return 0, errnoOf(err)
	}
	sparse := false
	var mode, uid, gid uint32
	if e.upper == nil && e.present() {
		// First write to a lower-backed path: materialize a sparse CoW node,
		// carrying the lower file's attrs so the overlay reports them unchanged.
		sparse = true
		mode = uint32(e.lower.Mode & 0o7777)
		uid = e.lower.Uid
		gid = e.lower.Gid
	}
	eof := off+int64(len(data)) >= e.size
	if err := h.m.Session.CommitSparseWrite(ctx, rel, off, data, eof, mode, uid, gid, sparse); err != nil {
		return 0, commitErrno("write", rel, err)
	}
	if perf.Enabled() {
		perf.Emit("fuse_write", perf.I64("ns", perf.Since(writeStart)), perf.I64("bytes", int64(len(data))))
	}
	return uint32(len(data)), 0
}

func (h *overlayHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0 // blobs were durably acked before the Write returned
}

type lowerHandle struct {
	fd int
}

func (h *lowerHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	readStart := time.Now()
	n, err := unix.Pread(h.fd, dest, off)
	if err != nil && err != io.EOF {
		return nil, errnoOf(err)
	}
	if perf.Enabled() {
		perf.Emit("fuse_read",
			perf.I64("ns", perf.Since(readStart)),
			perf.I64("bytes", int64(n)),
			perf.Bool("overlay", false))
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *lowerHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno { return 0 }

func (h *lowerHandle) Release(ctx context.Context) syscall.Errno {
	if err := unix.Close(h.fd); err != nil {
		return errnoOf(err)
	}
	return 0
}
