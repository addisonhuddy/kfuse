// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// newMounter builds a Mounter over a real lower directory (opened as a dirfd,
// exactly as Mount does) with an upper state built from events, so the lower
// helpers exercise the *at syscalls they use in production.
func newMounter(t *testing.T, lowerDir string, events ...*kfusev1.EventEnvelope) *Mounter {
	t.Helper()
	u := upper.New()
	for i, ev := range events {
		ev.Seq = uint64(i + 1)
		if err := u.Apply(ev); err != nil {
			t.Fatalf("apply event %d: %v", i+1, err)
		}
	}
	fd, err := unix.Open(lowerDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open lower: %v", err)
	}
	m := &Mounter{
		LowerPath:  lowerDir,
		Session:    session.NewWithState(registry.Session{ID: "sess", LowerID: "l"}, u, nil, nil, nil),
		lowerDirFd: fd,
		lowerFdSet: true,
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func writeEvent(path string, off int64, length uint64, eof, fall bool) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
		Path: path, Offset: off, Length: length, Eof: eof, Fallthrough: fall,
		BlobId: []byte("blob"),
	}}}
}

func TestInoForIsStableAndNeverZero(t *testing.T) {
	a := inoFor("a/b")
	b := inoFor("a/b")
	if a != b {
		t.Fatal("inoFor must be stable for the same path")
	}
	if inoFor("a/b") == inoFor("a/c") {
		t.Fatal("distinct paths should not collide for this input")
	}
	// The root ("") must still get a usable inode number; 0 is reserved.
	if inoFor("") == 0 {
		t.Fatal("inoFor must never return 0")
	}
}

func TestJoin(t *testing.T) {
	if got := join("", "a"); got != "a" {
		t.Errorf(`join("", "a") = %q, want "a"`, got)
	}
	if got := join("a", "b"); got != "a/b" {
		t.Errorf(`join("a", "b") = %q, want "a/b"`, got)
	}
}

func TestIsSubpath(t *testing.T) {
	cases := []struct {
		child, parent string
		want          bool
	}{
		{"a/b", "a", true},
		{"a/b/c", "a", true},
		{"a", "a", false},
		{"ab/c", "a", false}, // prefix-of-name is not a subpath
		{"b/a", "a", false},
	}
	for _, c := range cases {
		if got := isSubpath(c.child, c.parent); got != c.want {
			t.Errorf("isSubpath(%q, %q) = %v, want %v", c.child, c.parent, got, c.want)
		}
	}
}

func TestNsecToFuse(t *testing.T) {
	sec, nsec := nsecToFuse(1_500_000_123)
	if sec != 1 || nsec != 500_000_123 {
		t.Errorf("nsecToFuse(1.5e9) = (%d, %d), want (1, 500000123)", sec, nsec)
	}
	if sec, nsec := nsecToFuse(-1); sec != 0 || nsec != 0 {
		t.Errorf("nsecToFuse(-1) = (%d, %d), want (0, 0)", sec, nsec)
	}
}

func TestErrnoOf(t *testing.T) {
	if got := errnoOf(nil); got != 0 {
		t.Errorf("errnoOf(nil) = %v, want 0", got)
	}
	if got := errnoOf(syscall.EPERM); got != syscall.EPERM {
		t.Errorf("errnoOf(EPERM) = %v, want EPERM", got)
	}
	if got := errnoOf(errors.New("open x: no such file or directory")); got != syscall.ENOENT {
		t.Errorf("errnoOf(no such file) = %v, want ENOENT", got)
	}
	if got := errnoOf(errors.New("kafka unavailable")); got != syscall.EIO {
		t.Errorf("errnoOf(other) = %v, want EIO", got)
	}
}

func TestSetattrToProtoOnlySetsValidFields(t *testing.T) {
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_MODE | fuse.FATTR_UID | fuse.FATTR_GID | fuse.FATTR_SIZE
	in.Mode = 0o17755 // file-type bits must be stripped
	in.Uid = 1000
	in.Gid = 1001
	in.Size = 4096

	sa := setattrToProto(in)
	if sa.Mode == nil || *sa.Mode != 0o7755 {
		t.Errorf("Mode = %v, want 0o7755 (type bits masked off)", sa.Mode)
	}
	if sa.Uid == nil || *sa.Uid != 1000 || sa.Gid == nil || *sa.Gid != 1001 {
		t.Errorf("Uid/Gid = %v/%v, want 1000/1001", sa.Uid, sa.Gid)
	}
	if sa.Size == nil || *sa.Size != 4096 {
		t.Errorf("Size = %v, want 4096", sa.Size)
	}
	if sa.AtimeNs != nil || sa.MtimeNs != nil {
		t.Errorf("times must stay unset when FATTR_ATIME/MTIME are absent, got %v/%v", sa.AtimeNs, sa.MtimeNs)
	}

	empty := setattrToProto(&fuse.SetAttrIn{})
	if empty.Mode != nil || empty.Uid != nil || empty.Gid != nil || empty.Size != nil {
		t.Errorf("no valid bits must produce an all-unset Setattr, got %+v", empty)
	}
}

func TestSetattrToProtoTimes(t *testing.T) {
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_ATIME | fuse.FATTR_MTIME
	in.Atime, in.Atimensec = 5, 7
	in.Mtime, in.Mtimensec = 9, 11

	sa := setattrToProto(in)
	if sa.AtimeNs == nil || *sa.AtimeNs != 5*1e9+7 {
		t.Errorf("AtimeNs = %v, want %d", sa.AtimeNs, int64(5*1e9+7))
	}
	if sa.MtimeNs == nil || *sa.MtimeNs != 9*1e9+11 {
		t.Errorf("MtimeNs = %v, want %d", sa.MtimeNs, int64(9*1e9+11))
	}
}

func TestExtentEndTakesFurthestExtent(t *testing.T) {
	if got := extentEnd(&upper.Node{}); got != 0 {
		t.Errorf("extentEnd(no extents) = %d, want 0", got)
	}
	n := &upper.Node{Extents: []upper.Extent{
		{FileOffset: 100, Length: 10},
		{FileOffset: 0, Length: 5},
	}}
	if got := extentEnd(n); got != 110 {
		t.Errorf("extentEnd = %d, want 110", got)
	}
}

func TestFallthroughSrcPrefersFixedLowerRel(t *testing.T) {
	cases := []struct {
		name string
		node *upper.Node
		want string
	}{
		{"nil node", nil, "new.txt"},
		{"not fallthrough", &upper.Node{}, "new.txt"},
		{"fallthrough without lower rel", &upper.Node{Fallthrough: true}, "new.txt"},
		{"renamed fallthrough", &upper.Node{Fallthrough: true, LowerRel: "old.txt"}, "old.txt"},
	}
	for _, c := range cases {
		if got := fallthroughSrc("new.txt", c.node); got != c.want {
			t.Errorf("%s: fallthroughSrc = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLowerPathFollowsRenameRedirects(t *testing.T) {
	lower := t.TempDir()
	m := newMounter(t, lower,
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{
			From: "olddir", To: "newdir", FromLower: true,
		}}},
	)
	if got := m.lowerPath(""); got != "." {
		t.Errorf(`lowerPath("") = %q, want "." so *at syscalls hit the dirfd`, got)
	}
	if got := m.lowerPath("newdir"); got != "olddir" {
		t.Errorf(`lowerPath("newdir") = %q, want "olddir"`, got)
	}
	if got := m.lowerPath("newdir/f.txt"); got != "olddir/f.txt" {
		t.Errorf(`lowerPath("newdir/f.txt") = %q, want "olddir/f.txt"`, got)
	}
	if got := m.lowerPath("untouched"); got != "untouched" {
		t.Errorf(`lowerPath("untouched") = %q, want "untouched"`, got)
	}
}

func TestLowerSizeAndListLower(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lower, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower)

	if got, err := m.lowerSize("a.txt"); err != nil || got != 5 {
		t.Errorf("lowerSize(a.txt) = %d, want 5 (err %v)", got, err)
	}
	if got, err := m.lowerSize("missing.txt"); err != nil || got != 0 {
		t.Errorf("lowerSize(missing) = %d, want 0 (err %v)", got, err)
	}

	names, err := m.listLower("")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "a.txt" || names[1] != "sub" {
		t.Errorf("listLower(root) = %v, want [a.txt sub]", names)
	}
	if _, err := m.listLower("missing"); err == nil {
		t.Error("listLower on a missing dir must fail")
	}
	if _, err := m.listLower("a.txt"); err == nil {
		t.Error("listLower on a file must fail (ENOTDIR)")
	}
}

// A chmod/chown/touch recorded against a lower-backed path must show through
// Lookup's entry attrs, not just Getattr: the kernel serves stat from the
// entry it just looked up, so a resume that rebuilds the inode from Lookup
// would otherwise report the raw lower attrs until the attr cache expires.
func TestStatAttrAppliesOverride(t *testing.T) {
	st := unix.Stat_t{Mode: unix.S_IFREG | 0o644, Uid: 1, Gid: 2, Size: 7}
	st.Mtim = unix.Timespec{Sec: 10}

	if got := statAttr(fuse.S_IFREG, &st, nil); got.Mode != fuse.S_IFREG|0o644 || got.Uid != 1 || got.Size != 7 {
		t.Fatalf("statAttr(no override) = %+v", got)
	}

	mode, uid, gid := uint32(0o640), uint32(100), uint32(200)
	mt := int64(99e9)
	ov := &upper.AttrOverride{Mode: &mode, UID: &uid, GID: &gid, MtimeNs: &mt}
	got := statAttr(fuse.S_IFREG, &st, ov)
	if got.Mode != fuse.S_IFREG|0o640 {
		t.Errorf("mode = %o, want %o", got.Mode, fuse.S_IFREG|0o640)
	}
	if got.Uid != 100 || got.Gid != 200 {
		t.Errorf("owner = %d:%d, want 100:200", got.Uid, got.Gid)
	}
	if got.Mtime != 99 {
		t.Errorf("mtime = %d, want 99", got.Mtime)
	}
	if got.Size != 7 {
		t.Errorf("size = %d, want 7 (not overridden)", got.Size)
	}

	// Directories never report size/atime in the merged view.
	dst := unix.Stat_t{Mode: unix.S_IFDIR | 0o755, Size: 4096}
	if got := statAttr(fuse.S_IFDIR, &dst, ov); got.Size != 0 || got.Mode != fuse.S_IFDIR|0o640 {
		t.Errorf("dir statAttr = %+v, want size 0 and overridden mode", got)
	}
}

func TestMergedEntriesUpperOnlyDir(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "base.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower,
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{Path: "newdir", Mode: 0o755}}},
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "newdir/a.txt", Mode: 0o644}}},
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{Path: "newdir/sub", Mode: 0o755}}},
	)

	// A dir that exists only in the upper has no lower twin: listing it must
	// return its upper children, not fail (or come back empty).
	entries, err := m.mergedEntries("newdir")
	if err != nil {
		t.Fatalf("mergedEntries(newdir): %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "a.txt" || entries[1].Name != "sub" {
		t.Fatalf("mergedEntries(newdir) = %v, want [a.txt sub]", entries)
	}
	if entries[0].Mode != fuse.S_IFREG || entries[1].Mode != fuse.S_IFDIR {
		t.Fatalf("mergedEntries(newdir) modes = %o %o, want IFREG IFDIR", entries[0].Mode, entries[1].Mode)
	}

	// The merged root still lists lower and upper entries together.
	root, err := m.mergedEntries("")
	if err != nil {
		t.Fatalf("mergedEntries(root): %v", err)
	}
	var names []string
	for _, e := range root {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "base.txt" || names[1] != "newdir" {
		t.Fatalf("mergedEntries(root) = %v, want [base.txt newdir]", names)
	}
}

func TestReadLowerIntoZeroFillsOnError(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "a.txt"), []byte("abcdefgh"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower)

	buf := make([]byte, 4)
	if err := m.readLowerInto("a.txt", 2, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "cdef" {
		t.Errorf("readLowerInto = %q, want %q", buf, "cdef")
	}

	// A missing lower file must leave the buffer untouched: reads degrade to
	// zero-filled holes rather than failing.
	buf = []byte{1, 2, 3, 4}
	if err := m.readLowerInto("missing.txt", 0, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "\x01\x02\x03\x04" {
		t.Errorf("readLowerInto(missing) modified the buffer: %v", buf)
	}
}

func TestSizeOfAndEffectiveSize(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "big.txt"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower,
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{Path: "d", Mode: 0o755}}},
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "new.txt", Mode: 0o644}}},
		writeEvent("new.txt", 0, 10, false, false),
		// Sparse CoW over the 100-byte lower file: only 8 bytes written, so
		// the merged size must still come from the lower file.
		writeEvent("big.txt", 0, 8, false, true),
	)

	if got, err := m.sizeOf("new.txt"); err != nil || got != 10 {
		t.Errorf("sizeOf(new.txt) = %d, want 10 (err %v)", got, err)
	}
	if got, err := m.sizeOf("big.txt"); err != nil || got != 100 {
		t.Errorf("sizeOf(fallthrough big.txt) = %d, want the lower size 100 (err %v)", got, err)
	}
	if got, err := m.sizeOf("d"); err != nil || got != 0 {
		t.Errorf("sizeOf(dir) = %d, want 0 (err %v)", got, err)
	}
	if got, err := m.sizeOf("absent"); err != nil || got != 0 {
		t.Errorf("sizeOf(absent) = %d, want 0 (err %v)", got, err)
	}

	if got, err := m.effectiveSize("new.txt"); err != nil || got != 10 {
		t.Errorf("effectiveSize(overlay) = %d, want 10 (err %v)", got, err)
	}
	if got, err := m.effectiveSize("big.txt"); err != nil || got != 100 {
		t.Errorf("effectiveSize(fallthrough) = %d, want 100 (err %v)", got, err)
	}

	// Pure lower path: size comes straight from the lower tree.
	if err := os.WriteFile(filepath.Join(lower, "pure.txt"), []byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := m.effectiveSize("pure.txt"); err != nil || got != 3 {
		t.Errorf("effectiveSize(pure lower) = %d, want 3 (err %v)", got, err)
	}
}

func TestSizeOfHonorsExplicitTruncation(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "big.txt"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	size := uint64(4)
	m := newMounter(t, lower,
		writeEvent("big.txt", 0, 8, false, true),
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Setattr{Setattr: &kfusev1.Setattr{
			Path: "big.txt", Size: &size,
		}}},
	)
	if got, err := m.sizeOf("big.txt"); err != nil || got != 4 {
		t.Errorf("sizeOf after truncate = %d, want 4 (explicit size wins over lower size) (err %v)", got, err)
	}
}

func TestMergedEmpty(t *testing.T) {
	lower := t.TempDir()
	for _, dir := range []string{"emptydir", "withfile", "allwhite"} {
		if err := os.Mkdir(filepath.Join(lower, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lower, "withfile", "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lower, "allwhite", "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower,
		// A whiteout on the only lower child makes allwhite logically empty.
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: "allwhite/f.txt"}}},
		// An upper child makes emptydir non-empty in the merged view.
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{
			Path: "emptydir/new.txt", Mode: 0o644,
		}}},
		// Rmdir of a lower dir whiteouts it: the lower twin is dead (opaque).
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rmdir{Rmdir: &kfusev1.Rmdir{Path: "withfile"}}},
	)

	cases := []struct {
		rel  string
		want bool
	}{
		{"emptydir", false},   // has an upper child
		{"allwhite", true},    // sole lower child is whiteouted
		{"withfile", true},    // whiteouted lower dir: opaque
		{"nonexistent", true}, // no lower twin
	}
	for _, c := range cases {
		got, err := m.mergedEmpty(c.rel)
		if err != nil {
			t.Errorf("mergedEmpty(%q) = %v", c.rel, err)
			continue
		}
		if got != c.want {
			t.Errorf("mergedEmpty(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// RENAME_NOREPLACE gates on the merged view: an upper node or a live lower
// entry at the target blocks the rename; a whiteouted lower entry does not.
func TestExistsInMergedForRenameNoreplace(t *testing.T) {
	lower := t.TempDir()
	if err := os.WriteFile(filepath.Join(lower, "live.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lower, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower,
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "upper.txt", Mode: 0o644}}},
		&kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: "gone.txt"}}},
	)

	for _, tc := range []struct {
		path string
		want bool
	}{
		{"upper.txt", true}, // overlay node
		{"live.txt", true},  // live lower entry
		{"gone.txt", false}, // whiteouted lower entry is fair game
		{"missing.txt", false},
	} {
		if got, err := m.existsInMerged(tc.path); err != nil || got != tc.want {
			t.Errorf("existsInMerged(%s) = (%v, %v), want %v", tc.path, got, err, tc.want)
		}
	}
}

func TestMountRefusesRoot(t *testing.T) {
	m := &Mounter{LowerPath: "/"}
	if _, err := m.Mount(); err == nil {
		t.Fatal("mounting over / must be refused")
	}
}

func TestMountFailsOnMissingLower(t *testing.T) {
	m := &Mounter{LowerPath: filepath.Join(t.TempDir(), "missing")}
	if _, err := m.Mount(); err == nil {
		t.Fatal("mounting over a missing lower dir must fail")
	}
}

func TestCloseReleasesLowerFdAndIsIdempotent(t *testing.T) {
	m := newMounter(t, t.TempDir())
	fd := m.lowerDirFd
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); !errors.Is(err, unix.EBADF) {
		t.Fatalf("lower fd still open after Close: err = %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCloseBeforeMountDoesNotCloseStdin(t *testing.T) {
	m := &Mounter{LowerPath: t.TempDir()}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(0, &st); err != nil {
		t.Fatalf("fd 0 was closed by Close before Mount: %v", err)
	}
}
