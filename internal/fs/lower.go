// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"errors"
	"io"
	"os"
	"sort"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	"github.com/addisonhuddy/kfuse/internal/upper"
)

// mergedEntries lists a directory's merged view: lower ∪ redirected-in lower
// ∪ upper − whiteouts. A directory that exists only in the upper (mkdir
// inside the mount) has no lower twin; its absence is not an error, the
// listing is just upper-only.
func (m *Mounter) mergedEntries(rel string) ([]fuse.DirEntry, error) {
	var names []string
	// A whiteout at this dir's own path or an ancestor means its lower twin
	// is dead (opaque): only upper children are visible.
	if !m.Session.Upper().Hidden(rel) {
		var err error
		names, err = m.listLower(rel)
		if err != nil {
			if !isAbsent(err) {
				return nil, err
			}
			names = nil
		}
	}
	upperNodes, white := m.Session.Upper().Children(rel)
	seen := map[string]bool{}
	var entries []fuse.DirEntry
	for _, name := range names {
		if white[name] || upperNodes[name] != nil {
			continue
		}
		seen[name] = true
		entries = append(entries, fuse.DirEntry{Name: name, Mode: 0})
	}
	// Lower-backed entries renamed into this dir live at a different lower
	// path, so the lower listing above never mentions them.
	for _, name := range m.Session.Upper().Redirected(rel) {
		if seen[name] || white[name] || upperNodes[name] != nil {
			continue
		}
		seen[name] = true
		entries = append(entries, fuse.DirEntry{Name: name, Mode: 0})
	}
	var upperNames []string
	for name := range upperNodes {
		upperNames = append(upperNames, name)
	}
	sort.Strings(upperNames)
	for _, name := range upperNames {
		if seen[name] {
			continue
		}
		node := upperNodes[name]
		mode := uint32(fuse.S_IFREG)
		switch node.Kind {
		case upper.KindDir:
			mode = fuse.S_IFDIR
		case upper.KindSymlink:
			mode = fuse.S_IFLNK
		}
		entries = append(entries, fuse.DirEntry{Name: name, Mode: mode})
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// Lower helpers (lowerFd trick)

// mergedEmpty reports whether a dir has no visible children in the merged
// view (used by Rmdir's ENOTEMPTY check). Whiteouted lower children count as
// removed. An unreadable lower twin is an error, not an empty directory:
// treating it as empty would let rmdir/rename destroy live lower children.
func (m *Mounter) mergedEmpty(rel string) (bool, error) {
	nodes, white := m.Session.Upper().Children(rel)
	if len(nodes) > 0 {
		return false, nil
	}
	for _, name := range m.Session.Upper().Redirected(rel) {
		if !white[name] {
			return false, nil
		}
	}
	if m.Session.Upper().Hidden(rel) {
		return true, nil // opaque: lower twin is dead, only upper children exist
	}
	names, err := m.listLower(rel)
	if err != nil {
		if isAbsent(err) {
			return true, nil // no lower twin
		}
		return false, err
	}
	for _, name := range names {
		if !white[name] {
			return false, nil
		}
	}
	return true, nil
}

// isAbsent reports whether err means "this path has no lower counterpart",
// as opposed to a lower tree that exists but could not be read.
func isAbsent(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR)
}

func (m *Mounter) sizeOf(rel string) (int64, error) {
	u, ok := m.Session.Upper().Lookup(rel)
	if !ok {
		return 0, nil
	}
	return m.sizeOfNode(rel, u)
}

// sizeOfNode is sizeOf against an already-taken node snapshot, so a caller
// reports a size that matches the node it is otherwise describing. A
// fallthrough node whose lower source cannot be stat'ed is an error: its size
// is unknown, not zero.
func (m *Mounter) sizeOfNode(rel string, u *upper.Node) (int64, error) {
	if u == nil || u.Kind != upper.KindFile {
		return 0, nil
	}
	if u.Size >= 0 {
		return u.Size, nil
	}
	end := extentEnd(u)
	if u.Fallthrough {
		ls, err := m.lowerSize(fallthroughSrc(rel, u))
		if err != nil {
			return 0, err
		}
		if ls > end {
			end = ls
		}
	}
	return end, nil
}

// fallthroughSrc returns the lower path a fallthrough file reads holes from:
// its fixed LowerRel if set, else its overlay path.
func fallthroughSrc(rel string, u *upper.Node) string {
	if u != nil && u.Fallthrough && u.LowerRel != "" {
		return u.LowerRel
	}
	return rel
}

func extentEnd(u *upper.Node) int64 {
	var end int64
	for _, e := range u.Extents {
		if e.End() > end {
			end = e.End()
		}
	}
	return end
}

// statLower stats rel's lower entry (no symlink following).
func (m *Mounter) statLower(rel string, st *unix.Stat_t) error {
	return unix.Fstatat(m.lowerDirFd, m.lowerPath(rel), st, unix.AT_SYMLINK_NOFOLLOW)
}

// lowerSize returns the lower tree's file size at rel: 0 when absent, an
// error when the lower entry exists but cannot be stat'ed.
func (m *Mounter) lowerSize(rel string) (int64, error) {
	var st unix.Stat_t
	if err := m.statLower(rel, &st); err != nil {
		if isAbsent(err) {
			return 0, nil
		}
		return 0, err
	}
	return st.Size, nil
}

// effectiveSize returns the merged logical size at rel: the resolved entry's
// size (0 for absent paths and non-files).
func (m *Mounter) effectiveSize(rel string) (int64, error) {
	e, err := m.resolve(rel)
	return e.size, err
}

// readLowerInto fills buf with lower bytes at rel+off. A missing lower file
// leaves buf as zeros (the range is a genuine hole); any other failure is
// returned so the read fails instead of handing back fabricated zeros.
func (m *Mounter) readLowerInto(rel string, off int64, buf []byte) error {
	fd, err := unix.Openat(m.lowerDirFd, m.lowerPath(rel), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		if isAbsent(err) {
			return nil
		}
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if _, err := unix.Pread(fd, buf, off); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// lowerPath resolves rel's effective location in the lower tree, following
// redirects recorded by renames of lower-backed paths. Returns "." for the
// root directory so *at syscalls operate on lowerDirFd directly.
func (m *Mounter) lowerPath(rel string) string {
	if rel == "" {
		return "."
	}
	if lp, ok := m.Session.Upper().LowerPath(rel); ok {
		return lp
	}
	return rel
}

func (m *Mounter) listLower(rel string) ([]string, error) {
	at := m.lowerPath(rel)
	fd, err := unix.Openat(m.lowerDirFd, at, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range names {
		if name != "." && name != ".." {
			out = append(out, name)
		}
	}
	return out, nil
}
