// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	"github.com/addisonhuddy/kfuse/internal/upper"
)

// entry is one path resolved in the merged view. It is the single answer to
// "what does the mount show at rel": lookup, getattr, readdir typing, open,
// read/write sizing and every mutation precondition consume an entry rather
// than re-deriving visibility from separate upper queries.
//
// Merged-view policy, in precedence order:
//
//  1. An overlay node at rel wins outright, even over a whiteout at the same
//     path (unlink-then-recreate) or under a whiteouted ancestor (opaque dir
//     recreated over a removed lower dir).
//  2. Otherwise a whiteout at rel or at any ancestor hides the lower tree
//     there: the path is absent and the lower tree is not consulted.
//  3. Otherwise the lower entry at rel's effective lower path (following
//     rename redirects) is shown, with the session's Setattr overrides on top.
//
// A lower entry that does not exist (ENOENT/ENOTDIR) is genuine absence and
// resolves without error. Any other lower failure (EACCES, EIO, ELOOP, ...) is
// returned as an error so callers report it instead of fabricating absence,
// a directory, or a zero size.
//
// Coordination with mutations: an entry is a point-in-time read. FUSE
// mutations check it and then commit; the two are not atomic here. Lower-tree
// preconditions (EEXIST against a live lower file, ENOTEMPTY against lower
// children) stay check-then-commit, which is sound because the lower tree is
// read-only for the mount's lifetime. Upper-state preconditions are
// re-evaluated by the session under its commit lock (upper.Check), so racing
// mutations that both pass the merged-view check are arbitrated there and the
// loser is refused before anything reaches the log.
type entry struct {
	rel string
	// kind is fuse.S_IFDIR, S_IFREG or S_IFLNK; 0 means absent.
	kind uint32
	// upper is the overlay node snapshot (a copy; safe to keep), nil when the
	// entry is lower-only or absent.
	upper *upper.Node
	// lower is the lower twin's stat at rel's effective lower path, nil when
	// there is none. It is populated for upper-backed entries too, hidden or
	// not, because a rename must whiteout a lower twin that an overlay node
	// merely shadows.
	lower *unix.Stat_t
	// attr is what the kernel is told about the entry; size is the merged
	// logical size for regular files (0 otherwise).
	attr fuse.Attr
	size int64
}

func (e entry) present() bool { return e.kind != 0 }
func (e entry) isDir() bool   { return e.kind == fuse.S_IFDIR }

// resolve computes the merged-view entry at rel. An absent path resolves to a
// zero-kind entry with a nil error; see entry for the error contract.
func (m *Mounter) resolve(rel string) (entry, error) {
	up := m.Session.Upper()
	e := entry{rel: rel}
	u, hasUpper := up.Lookup(rel)
	if !hasUpper && up.Hidden(rel) {
		return e, nil
	}
	var st unix.Stat_t
	switch err := m.statLower(rel, &st); {
	case err == nil:
		e.lower = &st
	case isAbsent(err):
	default:
		return entry{}, err
	}
	if hasUpper {
		e.upper = u
		switch u.Kind {
		case upper.KindDir:
			e.kind = fuse.S_IFDIR
			e.attr = upperAttr(fuse.S_IFDIR, u, 0)
		case upper.KindFile:
			size, err := m.sizeOfNode(rel, u)
			if err != nil {
				return entry{}, err
			}
			e.kind, e.size = fuse.S_IFREG, size
			e.attr = upperAttr(fuse.S_IFREG, u, uint64(size))
		case upper.KindSymlink:
			e.kind = fuse.S_IFLNK
			e.attr = upperAttr(fuse.S_IFLNK, u, uint64(len(u.Target)))
		default:
			return entry{}, syscall.ENOTSUP
		}
		return e, nil
	}
	if e.lower == nil {
		return e, nil
	}
	ov := up.Override(rel)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		e.kind = fuse.S_IFDIR
	case unix.S_IFREG:
		e.kind = fuse.S_IFREG
	case unix.S_IFLNK:
		e.kind = fuse.S_IFLNK
	default:
		return entry{}, syscall.ENOTSUP
	}
	e.attr = statAttr(e.kind, &st, ov)
	if e.kind == fuse.S_IFREG {
		e.size = int64(e.attr.Size)
	}
	return e, nil
}

// existsInMerged reports whether rel is visible in the merged view. Callers
// use it for the EEXIST checks of create/mkdir/symlink and RENAME_NOREPLACE,
// where a whiteouted lower entry is fair game but a live one blocks. A lower
// tree that cannot be inspected is an error, not "does not exist".
func (m *Mounter) existsInMerged(rel string) (bool, error) {
	e, err := m.resolve(rel)
	if err != nil {
		return false, err
	}
	return e.present(), nil
}
