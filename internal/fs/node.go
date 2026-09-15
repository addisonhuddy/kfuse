// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"context"
	"hash/fnv"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/perf"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// baseNode is what every node kind carries: its inode and the mount it
// belongs to. Its path is the node's position in the merged view.
type baseNode struct {
	fs.Inode
	m *Mounter
}

func (n *baseNode) path() string { return n.Path(nil) }

// dirNode is the merged directory view (root, upper dirs, lower dirs).
type dirNode struct {
	baseNode
}

// fileNode is an overlay-created file (blob-backed extents). node is the
// snapshot taken when the inode was created and is never mutated afterwards:
// live attrs come from a fresh Upper lookup, this is only the fallback for a
// path the overlay no longer knows.
type fileNode struct {
	baseNode
	node *upper.Node
}

// lowerNode is a lower tree entry served read-only by passthrough
// (writes to lower files become sparse CoW overlay nodes).
type lowerNode struct {
	baseNode
	st unix.Stat_t
}

// symlinkNode is an overlay-created symlink.
type symlinkNode struct {
	baseNode
	node *upper.Node
}

func (m *Mounter) newDir() *dirNode { return &dirNode{baseNode{m: m}} }

func (m *Mounter) newFile(u *upper.Node) *fileNode {
	return &fileNode{baseNode: baseNode{m: m}, node: u}
}

func (m *Mounter) newSymlink(u *upper.Node) *symlinkNode {
	return &symlinkNode{baseNode: baseNode{m: m}, node: u}
}

func (m *Mounter) newLower(st unix.Stat_t) *lowerNode {
	return &lowerNode{baseNode: baseNode{m: m}, st: st}
}

func inoFor(rel string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(rel))
	v := h.Sum64()
	if v == 0 {
		v = 1
	}
	return v
}

func join(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// ---------------------------------------------------------------------------
// Entry attrs

const entryTimeout = 1 * time.Second

// addChild registers child at rel under this directory and fills the entry
// reply with attr (the same mode carries into the stable attr).
func (n *dirNode) addChild(ctx context.Context, rel string, child fs.InodeEmbedder, attr fuse.Attr, out *fuse.EntryOut) *fs.Inode {
	in := n.NewPersistentInode(ctx, child, fs.StableAttr{Mode: attr.Mode, Ino: inoFor(rel)})
	out.Attr = attr
	out.SetEntryTimeout(entryTimeout)
	out.SetAttrTimeout(entryTimeout)
	return in
}

// upperAttr builds the attrs of an overlay node of the given type.
func upperAttr(typ uint32, u *upper.Node, size uint64) fuse.Attr {
	a := fuse.Attr{
		Mode:  typ | (u.Mode & 0o7777),
		Size:  size,
		Owner: fuse.Owner{Uid: u.UID, Gid: u.GID},
	}
	a.Atime, a.Atimensec = nsecToFuse(u.AtimeNs)
	a.Mtime, a.Mtimensec = nsecToFuse(u.MtimeNs)
	return a
}

// statAttr builds the attrs of a lower entry of the given type, with the
// session's Setattr overrides (chmod/chown/touch on a lower-backed path)
// merged on top. Directories report no size: the merged view's size is not
// the lower dir's.
func statAttr(typ uint32, st *unix.Stat_t, ov *upper.AttrOverride) fuse.Attr {
	la := attrsFromStat(st)
	la.applyOverride(ov)
	a := fuse.Attr{
		Mode:  typ | la.mode,
		Owner: fuse.Owner{Uid: la.uid, Gid: la.gid},
	}
	if typ != fuse.S_IFDIR {
		a.Size = la.size
		a.Atime, a.Atimensec = nsecToFuse(la.atimeNs)
	}
	a.Mtime, a.Mtimensec = nsecToFuse(la.mtimeNs)
	return a
}

// ---------------------------------------------------------------------------
// Lookup

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if perf.Enabled() {
		defer func(start time.Time) { perf.Emit("fuse_lookup", perf.I64("ns", perf.Since(start))) }(time.Now())
	}
	rel := join(n.path(), name)
	e, err := n.m.resolve(rel)
	if err != nil {
		return nil, errnoOf(err)
	}
	if !e.present() {
		return nil, syscall.ENOENT
	}
	var child fs.InodeEmbedder
	switch {
	case e.isDir():
		child = n.m.newDir()
	case e.upper != nil && e.kind == fuse.S_IFREG:
		child = n.m.newFile(e.upper)
	case e.upper != nil:
		child = n.m.newSymlink(e.upper)
	default:
		child = n.m.newLower(*e.lower)
	}
	return n.addChild(ctx, rel, child, e.attr, out), 0
}

// ---------------------------------------------------------------------------
// Getattr

// lowerAttrs are a lower entry's attrs as the merged view reports them: the
// lower stat with the session's Setattr overrides applied on top.
type lowerAttrs struct {
	mode     uint32
	uid, gid uint32
	size     uint64
	atimeNs  int64
	mtimeNs  int64
}

func attrsFromStat(st *unix.Stat_t) lowerAttrs {
	return lowerAttrs{
		mode:    uint32(st.Mode & 0o7777),
		uid:     st.Uid,
		gid:     st.Gid,
		size:    uint64(st.Size),
		atimeNs: st.Atim.Sec*1e9 + st.Atim.Nsec,
		mtimeNs: st.Mtim.Sec*1e9 + st.Mtim.Nsec,
	}
}

// applyOverride overlays the fields a Setattr recorded for the path.
func (a *lowerAttrs) applyOverride(ov *upper.AttrOverride) {
	if ov == nil {
		return
	}
	if ov.Mode != nil {
		a.mode = *ov.Mode
	}
	if ov.UID != nil {
		a.uid = *ov.UID
	}
	if ov.GID != nil {
		a.gid = *ov.GID
	}
	if ov.Size != nil {
		a.size = uint64(*ov.Size)
	}
	if ov.AtimeNs != nil {
		a.atimeNs = *ov.AtimeNs
	}
	if ov.MtimeNs != nil {
		a.mtimeNs = *ov.MtimeNs
	}
}

func (a lowerAttrs) fill(out *fuse.AttrOut, typ uint32) {
	out.Mode = typ | a.mode
	out.Size = a.size
	out.Owner = fuse.Owner{Uid: a.uid, Gid: a.gid}
	out.Atime, out.Atimensec = nsecToFuse(a.atimeNs)
	out.Mtime, out.Mtimensec = nsecToFuse(a.mtimeNs)
	out.SetTimeout(entryTimeout)
}

// fillUpper reports an overlay node's attrs.
func fillUpper(out *fuse.AttrOut, typ uint32, u *upper.Node, size uint64) {
	out.Attr = upperAttr(typ, u, size)
	out.SetTimeout(entryTimeout)
}

func (n *dirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if perf.Enabled() {
		defer func(start time.Time) { perf.Emit("fuse_getattr", perf.I64("ns", perf.Since(start))) }(time.Now())
	}
	rel := n.path()
	e, err := n.m.resolve(rel)
	if err != nil {
		return errnoOf(err)
	}
	if !e.isDir() {
		if rel == "" {
			return syscall.ENOENT
		}
		// The kernel still holds this inode but the merged view no longer
		// shows a directory here (removed or replaced underneath an open
		// handle). Report a placeholder dir until the entry expires.
		out.Mode = fuse.S_IFDIR | 0o755
		out.SetTimeout(entryTimeout)
		return 0
	}
	out.Attr = e.attr
	out.SetTimeout(entryTimeout)
	return 0
}

func (n *fileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	rel := n.path()
	e, err := n.m.resolve(rel)
	if err != nil {
		return errnoOf(err)
	}
	if e.upper != nil && e.kind == fuse.S_IFREG {
		out.Attr = e.attr
		out.SetTimeout(entryTimeout)
		return 0
	}
	// The overlay no longer knows this path (unlinked or renamed away under
	// an open inode): fall back to the snapshot taken at creation.
	if n.node == nil {
		return syscall.ENOENT
	}
	size, err := n.m.sizeOfNode(rel, n.node)
	if err != nil {
		return errnoOf(err)
	}
	fillUpper(out, fuse.S_IFREG, n.node, uint64(size))
	return 0
}

func (n *lowerNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	rel := n.path()
	e, err := n.m.resolve(rel)
	if err != nil {
		return errnoOf(err)
	}
	if e.present() {
		out.Attr = e.attr
		out.SetTimeout(entryTimeout)
		return 0
	}
	// Gone from the merged view under an open inode: report the stat taken at
	// lookup with any overrides still recorded for the path.
	typ := uint32(fuse.S_IFREG)
	switch n.st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		typ = fuse.S_IFDIR
	case unix.S_IFLNK:
		typ = fuse.S_IFLNK
	}
	a := attrsFromStat(&n.st)
	a.applyOverride(n.m.Session.Upper().Override(rel))
	a.fill(out, typ)
	return 0
}

func (n *symlinkNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	rel := n.path()
	target := ""
	mode := uint32(0o777)
	var uid, gid uint32
	if u, ok := n.m.Session.Upper().Lookup(rel); ok {
		target = u.Target
		if u.Mode != 0 {
			mode = u.Mode & 0o7777
		}
		uid = u.UID
		gid = u.GID
	} else if n.node != nil {
		target = n.node.Target
	}
	out.Mode = fuse.S_IFLNK | mode
	out.Size = uint64(len(target))
	out.Owner = fuse.Owner{Uid: uid, Gid: gid}
	out.SetTimeout(entryTimeout)
	return 0
}

func (n *symlinkNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	rel := n.path()
	if u, ok := n.m.Session.Upper().Lookup(rel); ok {
		return []byte(u.Target), 0
	}
	if n.node != nil {
		return []byte(n.node.Target), 0
	}
	return nil, syscall.ENOENT
}

func nsecToFuse(ns int64) (uint64, uint32) {
	if ns < 0 {
		return 0, 0
	}
	return uint64(ns / 1e9), uint32(ns % 1e9)
}

// ---------------------------------------------------------------------------
// Readdir (merged: lower ∪ upper − whiteouts)

func (n *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if perf.Enabled() {
		defer func(start time.Time) { perf.Emit("fuse_readdir", perf.I64("ns", perf.Since(start))) }(time.Now())
	}
	entries, err := n.m.mergedEntries(n.path())
	if err != nil {
		return nil, errnoOf(err)
	}
	return fs.NewListDirStream(entries), 0
}

// ---------------------------------------------------------------------------
// Open / Create / Read / Write

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return n.m.newHandle(n.path), 0, 0
}

func (n *lowerNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, 0, syscall.ENOENT
	}
	rel := n.path()
	if flags&(unix.O_WRONLY|unix.O_RDWR) != 0 || n.m.Session.Upper().HasNode(rel) {
		// Sparse CoW: writes materialize a fallthrough overlay node; reads
		// (including reads after a write) use overlayHandle so extents +
		// lower fallthrough are merged.
		return n.m.newHandle(n.path), 0, 0
	}
	fd, err := unix.Openat(n.m.lowerDirFd, n.m.lowerPath(rel), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, errnoOf(err)
	}
	return &lowerHandle{fd: fd}, 0, 0
}

func (n *lowerNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	buf := make([]byte, 4096)
	l, err := unix.Readlinkat(n.m.lowerDirFd, n.m.lowerPath(n.path()), buf)
	if err != nil {
		return nil, errnoOf(err)
	}
	return buf[:l], 0
}

func (n *dirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	rel := join(n.path(), name)
	if errno := n.mustBeAbsent(rel); errno != 0 {
		return nil, nil, 0, errno
	}
	caller := callerOf(ctx)
	if err := n.m.Session.CommitCreate(ctx, rel, mode&0o7777, caller.Uid, caller.Gid); err != nil {
		return nil, nil, 0, commitErrno("create", rel, err)
	}
	u, _ := n.m.Session.Upper().Lookup(rel)
	child := n.m.newFile(u)
	attr := fuse.Attr{Mode: fuse.S_IFREG | (mode & 0o7777), Size: 0, Owner: fuse.Owner{Uid: caller.Uid, Gid: caller.Gid}}
	in := n.addChild(ctx, rel, child, attr, out)
	return in, n.m.newHandle(child.path), 0, 0
}

func (n *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	rel := join(n.path(), name)
	if err := n.m.Session.CommitUnlink(ctx, rel); err != nil {
		return commitErrno("unlink", rel, err)
	}
	return 0
}

// Symlink creates an upper symlink node. Fails with EEXIST when the merged
// view already shows the name; a whiteouted lower entry is fair game.
func (n *dirNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := join(n.path(), name)
	if errno := n.mustBeAbsent(rel); errno != 0 {
		return nil, errno
	}
	caller := callerOf(ctx)
	if err := n.m.Session.CommitSymlink(ctx, rel, target, caller.Uid, caller.Gid); err != nil {
		return nil, commitErrno("symlink", rel, err)
	}
	u, _ := n.m.Session.Upper().Lookup(rel)
	attr := fuse.Attr{Mode: fuse.S_IFLNK | 0o777, Size: uint64(len(target)), Owner: fuse.Owner{Uid: caller.Uid, Gid: caller.Gid}}
	return n.addChild(ctx, rel, n.m.newSymlink(u), attr, out), 0
}

// Mkdir creates an upper dir node. Fails with EEXIST when the merged view
// already shows the name (upper node, live lower entry); a whiteouted lower
// entry is fine — the new dir is opaque.
func (n *dirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := join(n.path(), name)
	if errno := n.mustBeAbsent(rel); errno != 0 {
		return nil, errno
	}
	caller := callerOf(ctx)
	if err := n.m.Session.CommitMkdir(ctx, rel, mode&0o7777, caller.Uid, caller.Gid); err != nil {
		return nil, commitErrno("mkdir", rel, err)
	}
	attr := fuse.Attr{Mode: fuse.S_IFDIR | (mode & 0o7777), Owner: fuse.Owner{Uid: caller.Uid, Gid: caller.Gid}}
	return n.addChild(ctx, rel, n.m.newDir(), attr, out), 0
}

// Rmdir removes an empty dir from the merged view: an upper dir node is
// deleted, a lower dir is whiteouted (opaque).
func (n *dirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	rel := join(n.path(), name)
	e, err := n.m.resolve(rel)
	if err != nil {
		return errnoOf(err)
	}
	if !e.present() {
		return syscall.ENOENT
	}
	if !e.isDir() {
		return syscall.ENOTDIR
	}
	empty, err := n.m.mergedEmpty(rel)
	if err != nil {
		return errnoOf(err)
	}
	if !empty {
		return syscall.ENOTEMPTY
	}
	if err := n.m.Session.CommitRmdir(ctx, rel); err != nil {
		return commitErrno("rmdir", rel, err)
	}
	return 0
}

// Rename moves a child from this directory to newParent. POSIX constraints
// (EISDIR/ENOTDIR/ENOTEMPTY/EINVAL) are checked against the merged view, then
// a single Rename event commits the move (overlay tree move, or a lower
// redirect + whiteout).
func (n *dirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	nd, ok := newParent.(*dirNode)
	if !ok {
		return syscall.EIO
	}
	from := join(n.path(), name)
	to := join(nd.path(), newName)

	if flags&renameExchange != 0 {
		return syscall.ENOTSUP
	}

	// A path can have BOTH an upper node and a lower twin (fallthrough file,
	// or an opaque dir recreated over a whiteouted lower dir); `from_lower`
	// must then still be set so the lower twin is whiteouted on rename.
	src, err := n.m.resolve(from)
	if err != nil {
		return errnoOf(err)
	}
	if !src.present() {
		return syscall.ENOENT
	}
	dst, err := n.m.resolve(to)
	if err != nil {
		return errnoOf(err)
	}
	if dst.present() {
		if flags&renameNoReplace != 0 {
			return syscall.EEXIST
		}
		// `to` must not be a non-empty directory, and kind must agree.
		if dst.isDir() && !src.isDir() {
			return syscall.EISDIR
		}
		if !dst.isDir() && src.isDir() {
			return syscall.ENOTDIR
		}
		if dst.isDir() {
			empty, err := n.m.mergedEmpty(to)
			if err != nil {
				return errnoOf(err)
			}
			if !empty {
				return syscall.ENOTEMPTY
			}
		}
	}

	if src.isDir() && isSubpath(to, from) {
		return syscall.EINVAL
	}

	if err := n.m.Session.CommitRename(ctx, from, to, src.lower != nil); err != nil {
		return commitErrno("rename", from, err)
	}
	return 0
}

// mustBeAbsent is the shared precondition of create/mkdir/symlink: the name
// must not be visible in the merged view. A lower tree that cannot be
// inspected fails the operation rather than being treated as empty.
func (n *dirNode) mustBeAbsent(rel string) syscall.Errno {
	exists, err := n.m.existsInMerged(rel)
	if err != nil {
		return errnoOf(err)
	}
	if exists {
		return syscall.EEXIST
	}
	return 0
}

func isSubpath(child, parent string) bool {
	return strings.HasPrefix(child, parent+"/")
}

// ---------------------------------------------------------------------------
// Setattr

// setattrToProto maps a FUSE SetAttrIn to a Setattr event, capturing only the
// fields the caller actually set (mode/uid/gid/size/atime/mtime).
func setattrToProto(in *fuse.SetAttrIn) *kfusev1.Setattr {
	sa := &kfusev1.Setattr{}
	if in.Valid&fuse.FATTR_MODE != 0 {
		m := in.Mode & 0o7777
		sa.Mode = &m
	}
	if in.Valid&fuse.FATTR_UID != 0 {
		u := in.Uid
		sa.Uid = &u
	}
	if in.Valid&fuse.FATTR_GID != 0 {
		g := in.Gid
		sa.Gid = &g
	}
	if in.Valid&fuse.FATTR_SIZE != 0 {
		s := in.Size
		sa.Size = &s
	}
	if atime, ok := in.GetATime(); ok {
		a := atime.UnixNano()
		sa.AtimeNs = &a
	}
	if mtime, ok := in.GetMTime(); ok {
		m := mtime.UnixNano()
		sa.MtimeNs = &m
	}
	return sa
}

// commitSetattr commits a Setattr event for rel. sizeErrno, when non-zero,
// rejects a truncate on node kinds that have no size to set.
func (m *Mounter) commitSetattr(ctx context.Context, rel string, in *fuse.SetAttrIn, sizeErrno syscall.Errno) syscall.Errno {
	sa := setattrToProto(in)
	sa.Path = rel
	if sa.Size != nil && sizeErrno != 0 {
		return sizeErrno
	}
	if err := m.Session.CommitSetattr(ctx, sa); err != nil {
		return commitErrno("setattr", rel, err)
	}
	return 0
}

func (n *fileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if errno := n.m.commitSetattr(ctx, n.path(), in, 0); errno != 0 {
		return errno
	}
	return n.Getattr(ctx, f, out)
}

func (n *lowerNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if errno := n.m.commitSetattr(ctx, n.path(), in, 0); errno != 0 {
		return errno
	}
	return n.Getattr(ctx, f, out)
}

func (n *symlinkNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if errno := n.m.commitSetattr(ctx, n.path(), in, syscall.ENOTSUP); errno != 0 {
		return errno
	}
	return n.Getattr(ctx, f, out)
}

func (n *dirNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if errno := n.m.commitSetattr(ctx, n.path(), in, syscall.EISDIR); errno != 0 {
		return errno
	}
	return n.Getattr(ctx, f, out)
}

// Statvfs keeps df working.
func (n *dirNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	out.Blocks = 1 << 40
	out.Bfree = 1 << 40
	out.Bavail = 1 << 40
	out.Bsize = 4096
	out.NameLen = 255
	out.Frsize = 4096
	return 0
}
