// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

func mkdirEv(path string) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{Path: path, Mode: 0o750, Uid: 7, Gid: 8}}}
}

func createEv(path string) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: path, Mode: 0o600}}}
}

func unlinkEv(path string) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: path}}}
}

func rmdirEv(path string) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rmdir{Rmdir: &kfusev1.Rmdir{Path: path}}}
}

func renameEv(from, to string, fromLower bool) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{From: from, To: to, FromLower: fromLower}}}
}

func symlinkEv(path, target string) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Symlink{Symlink: &kfusev1.Symlink{Path: path, Target: target}}}
}

func setattrEv(sa *kfusev1.Setattr) *kfusev1.EventEnvelope {
	return &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Setattr{Setattr: sa}}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustResolve(t *testing.T, m *Mounter, rel string) entry {
	t.Helper()
	e, err := m.resolve(rel)
	if err != nil {
		t.Fatalf("resolve(%q): %v", rel, err)
	}
	return e
}

// listNames is the merged listing of rel as the kernel would see it.
func listNames(t *testing.T, m *Mounter, rel string) []string {
	t.Helper()
	ents, err := m.mergedEntries(rel)
	if err != nil {
		t.Fatalf("mergedEntries(%q): %v", rel, err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return names
}

// assertAgree checks that listing dir and resolving each child tell the same
// story: every listed name resolves present, every unlisted lower name
// resolves absent, and listed types match resolved types.
func assertAgree(t *testing.T, m *Mounter, dir string, lowerNames ...string) {
	t.Helper()
	ents, err := m.mergedEntries(dir)
	if err != nil {
		t.Fatalf("mergedEntries(%q): %v", dir, err)
	}
	listed := map[string]uint32{}
	for _, de := range ents {
		listed[de.Name] = de.Mode
		e := mustResolve(t, m, join(dir, de.Name))
		if !e.present() {
			t.Errorf("%s/%s listed but resolves absent", dir, de.Name)
		}
		if de.Mode != 0 && de.Mode != e.kind {
			t.Errorf("%s/%s listed as %o, resolves as %o", dir, de.Name, de.Mode, e.kind)
		}
	}
	for _, name := range lowerNames {
		if _, ok := listed[name]; ok {
			continue
		}
		if e := mustResolve(t, m, join(dir, name)); e.present() {
			t.Errorf("%s/%s not listed but resolves present (%+v)", dir, name, e)
		}
	}
}

// Upper nodes win over lower entries and over whiteouts at the same path; a
// whiteout at the exact path hides the lower entry; a whiteout on an ancestor
// hides everything beneath it, including deeper lower paths that were never
// individually whiteouted.
func TestResolvePrecedenceAndWhiteouts(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "shadowed.txt"), []byte("lower"))
	mustWrite(t, filepath.Join(lower, "gone.txt"), []byte("lower"))
	mustWrite(t, filepath.Join(lower, "recreated.txt"), []byte("lower"))
	mustWrite(t, filepath.Join(lower, "dead", "deep", "leaf.txt"), []byte("x"))
	mustWrite(t, filepath.Join(lower, "kept.txt"), []byte("k"))
	m := newMounter(t, lower,
		createEv("shadowed.txt"),
		writeEvent("shadowed.txt", 0, 3, true, false),
		unlinkEv("gone.txt"),
		unlinkEv("recreated.txt"),
		createEv("recreated.txt"),
		rmdirEv("dead"),
	)

	if e := mustResolve(t, m, "shadowed.txt"); e.upper == nil || e.kind != fuse.S_IFREG || e.size != 3 || e.lower == nil {
		t.Errorf("upper over lower: %+v, want upper file of size 3 with a lower twin", e)
	}
	if e := mustResolve(t, m, "gone.txt"); e.present() {
		t.Errorf("exact whiteout must hide the lower entry: %+v", e)
	}
	if e := mustResolve(t, m, "recreated.txt"); e.upper == nil || e.size != 0 {
		t.Errorf("upper node over a whiteout must win: %+v", e)
	}
	for _, p := range []string{"dead", "dead/deep", "dead/deep/leaf.txt"} {
		if e := mustResolve(t, m, p); e.present() {
			t.Errorf("%s under a whiteouted ancestor must be absent: %+v", p, e)
		}
	}
	if e := mustResolve(t, m, "kept.txt"); e.upper != nil || e.kind != fuse.S_IFREG || e.size != 1 || e.attr.Mode != fuse.S_IFREG|0o644 {
		t.Errorf("pure lower: %+v", e)
	}
	if e := mustResolve(t, m, "never-existed"); e.present() {
		t.Errorf("missing path: %+v", e)
	}
	assertAgree(t, m, "", "shadowed.txt", "gone.txt", "recreated.txt", "dead", "kept.txt")
	if got := listNames(t, m, ""); len(got) != 3 || got[0] != "kept.txt" || got[1] != "recreated.txt" || got[2] != "shadowed.txt" {
		t.Errorf("root listing = %v", got)
	}
}

// A lower dir removed and recreated is opaque: its lower children stay hidden
// even when they were never whiteouted themselves, at every level (resolve,
// listing, emptiness), and an upper child created inside is visible.
func TestResolveRecreatedOpaqueDir(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "d", "old.txt"), []byte("old"))
	mustWrite(t, filepath.Join(lower, "d", "sub", "deep.txt"), []byte("deep"))
	m := newMounter(t, lower,
		rmdirEv("d"),
		mkdirEv("d"),
		createEv("d/new.txt"),
		mkdirEv("d/sub"),
	)

	d := mustResolve(t, m, "d")
	if !d.isDir() || d.upper == nil || d.attr.Mode != fuse.S_IFDIR|0o750 || d.attr.Uid != 7 {
		t.Fatalf("recreated dir = %+v, want the upper dir's attrs", d)
	}
	if d.lower == nil {
		t.Error("recreated dir must still know about its lower twin (rename needs from_lower)")
	}
	if e := mustResolve(t, m, "d/old.txt"); e.present() {
		t.Errorf("lower child of an opaque dir leaked: %+v", e)
	}
	if got := listNames(t, m, "d"); len(got) != 2 || got[0] != "new.txt" || got[1] != "sub" {
		t.Errorf("opaque listing = %v, want [new.txt sub]", got)
	}
	// d/sub is an upper dir under the whiteouted ancestor: its lower twin's
	// children are hidden too.
	if e := mustResolve(t, m, "d/sub/deep.txt"); e.present() {
		t.Errorf("lower grandchild under opaque dir leaked: %+v", e)
	}
	if got := listNames(t, m, "d/sub"); len(got) != 0 {
		t.Errorf("listing under opaque dir = %v, want empty", got)
	}
	if empty, err := m.mergedEmpty("d/sub"); err != nil || !empty {
		t.Errorf("mergedEmpty(d/sub) = (%v, %v), want (true, nil)", empty, err)
	}
	assertAgree(t, m, "d", "old.txt", "sub", "new.txt")
	assertAgree(t, m, "d/sub", "deep.txt")
}

// Renaming a lower-backed file or dir leaves a redirect: the new name shows
// the lower content, the old name is gone, and children of a renamed lower
// dir are reachable at their new location only.
func TestResolveRenamedLowerBackedEntries(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "orig.txt"), []byte("12345"))
	mustWrite(t, filepath.Join(lower, "dir", "a.txt"), []byte("a"))
	mustWrite(t, filepath.Join(lower, "dir", "b.txt"), []byte("b"))
	m := newMounter(t, lower,
		renameEv("orig.txt", "moved.txt", true),
		renameEv("dir", "renamed", true),
		unlinkEv("renamed/b.txt"),
	)

	if e := mustResolve(t, m, "orig.txt"); e.present() {
		t.Errorf("rename source still visible: %+v", e)
	}
	if e := mustResolve(t, m, "moved.txt"); e.upper != nil || e.kind != fuse.S_IFREG || e.size != 5 {
		t.Errorf("rename target = %+v, want lower-backed file of size 5", e)
	}
	if e := mustResolve(t, m, "renamed/a.txt"); !e.present() || e.size != 1 {
		t.Errorf("child of renamed lower dir = %+v", e)
	}
	if e := mustResolve(t, m, "renamed/b.txt"); e.present() {
		t.Errorf("whiteout moved with the rename must still hide b.txt: %+v", e)
	}
	if e := mustResolve(t, m, "dir/a.txt"); e.present() {
		t.Errorf("old location still reachable: %+v", e)
	}
	if got := listNames(t, m, "renamed"); len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("renamed listing = %v", got)
	}
	if got := listNames(t, m, ""); len(got) != 2 || got[0] != "moved.txt" || got[1] != "renamed" {
		t.Errorf("root listing = %v", got)
	}
	// A dir holding only a renamed-in lower file is not empty; once that file
	// is unlinked (whiteout at the redirect target) it is.
	if err := m.Session.Upper().Apply(func() *kfusev1.EventEnvelope {
		ev := mkdirEv("box")
		ev.Seq = 4
		return ev
	}()); err != nil {
		t.Fatal(err)
	}
	if err := m.Session.Upper().Apply(func() *kfusev1.EventEnvelope {
		ev := renameEv("moved.txt", "box/inner.txt", false)
		ev.Seq = 5
		return ev
	}()); err != nil {
		t.Fatal(err)
	}
	if empty, err := m.mergedEmpty("box"); err != nil || empty {
		t.Errorf("mergedEmpty(box) = (%v, %v), want (false, nil): holds a renamed-in lower file", empty, err)
	}
	if got := listNames(t, m, "box"); len(got) != 1 || got[0] != "inner.txt" {
		t.Errorf("box listing = %v", got)
	}
	if e := mustResolve(t, m, "box/inner.txt"); e.size != 5 {
		t.Errorf("twice-renamed lower file = %+v", e)
	}
	if err := m.Session.Upper().Apply(func() *kfusev1.EventEnvelope {
		ev := unlinkEv("box/inner.txt")
		ev.Seq = 6
		return ev
	}()); err != nil {
		t.Fatal(err)
	}
	if empty, err := m.mergedEmpty("box"); err != nil || !empty {
		t.Errorf("mergedEmpty(box) after unlink = (%v, %v), want (true, nil)", empty, err)
	}
	if got := listNames(t, m, "box"); len(got) != 0 {
		t.Errorf("box listing after unlink = %v", got)
	}
	assertAgree(t, m, "", "orig.txt", "dir", "moved.txt", "renamed", "box")
	assertAgree(t, m, "box", "inner.txt")
	assertAgree(t, m, "renamed", "a.txt", "b.txt")
}

// A sparse write materializes over a lower file; a later rename keeps the
// node reading (and sizing) from its original lower path, so the merged size
// follows the lower file, not the extents alone, and truncation then wins.
func TestResolveSparseWriteAfterRenameAndTruncate(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "big.bin"), make([]byte, 100))
	m := newMounter(t, lower,
		writeEvent("big.bin", 0, 8, false, true),
		renameEv("big.bin", "moved.bin", true),
	)
	e := mustResolve(t, m, "moved.bin")
	if e.upper == nil || !e.upper.Fallthrough || e.upper.LowerRel != "big.bin" {
		t.Fatalf("moved sparse node = %+v", e.upper)
	}
	if e.size != 100 {
		t.Errorf("size after rename = %d, want lower size 100", e.size)
	}
	if e.lower != nil {
		t.Errorf("moved.bin has no lower twin of its own, got %+v", e.lower)
	}
	if e := mustResolve(t, m, "big.bin"); e.present() {
		t.Errorf("rename source visible: %+v", e)
	}

	size := uint64(4)
	if err := m.Session.Upper().Apply(&kfusev1.EventEnvelope{Seq: 3, Op: &kfusev1.EventEnvelope_Setattr{Setattr: &kfusev1.Setattr{Path: "moved.bin", Size: &size}}}); err != nil {
		t.Fatal(err)
	}
	if e := mustResolve(t, m, "moved.bin"); e.size != 4 || e.attr.Size != 4 {
		t.Errorf("size after truncate = %d/%d, want 4", e.size, e.attr.Size)
	}
}

// Truncating a pure lower file materializes a fallthrough node with an
// explicit size; getattr and lookup must both report it.
func TestResolveTruncatedLowerFile(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "f.txt"), make([]byte, 50))
	size := uint64(10)
	m := newMounter(t, lower, setattrEv(&kfusev1.Setattr{Path: "f.txt", Size: &size}))
	e := mustResolve(t, m, "f.txt")
	if e.upper == nil || e.size != 10 || e.attr.Size != 10 || e.lower == nil {
		t.Errorf("truncated lower = %+v", e)
	}
}

// chmod/chown/touch on a lower-backed path are overrides: the resolved attrs
// carry them, the lower stat does not, and a later materialization keeps them.
func TestResolveAttributeOverrides(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "f.txt"), []byte("abc"))
	if err := os.Mkdir(filepath.Join(lower, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	mode, uid, gid := uint32(0o600), uint32(1000), uint32(1001)
	mt := int64(42e9)
	m := newMounter(t, lower,
		setattrEv(&kfusev1.Setattr{Path: "f.txt", Mode: &mode, Uid: &uid, Gid: &gid, MtimeNs: &mt}),
		setattrEv(&kfusev1.Setattr{Path: "d", Mode: &mode}),
	)
	f := mustResolve(t, m, "f.txt")
	if f.attr.Mode != fuse.S_IFREG|0o600 || f.attr.Uid != 1000 || f.attr.Gid != 1001 || f.attr.Mtime != 42 || f.attr.Size != 3 {
		t.Errorf("overridden file attrs = %+v", f.attr)
	}
	if f.lower.Mode&0o777 != 0o644 {
		t.Errorf("lower stat must stay raw, got %o", f.lower.Mode&0o777)
	}
	d := mustResolve(t, m, "d")
	if d.attr.Mode != fuse.S_IFDIR|0o600 || d.attr.Size != 0 {
		t.Errorf("overridden dir attrs = %+v", d.attr)
	}
	// Materializing via a sparse write absorbs the override.
	if err := m.Session.Upper().Apply(func() *kfusev1.EventEnvelope {
		ev := writeEvent("f.txt", 0, 1, false, true)
		ev.Seq = 3
		return ev
	}()); err != nil {
		t.Fatal(err)
	}
	f = mustResolve(t, m, "f.txt")
	if f.upper == nil || f.attr.Mode != fuse.S_IFREG|0o600 || f.attr.Uid != 1000 || f.size != 3 {
		t.Errorf("materialized file lost override: %+v (size %d)", f.attr, f.size)
	}
}

func TestResolveSymlinks(t *testing.T) {
	lower := t.TempDir()
	if err := os.Symlink("target", filepath.Join(lower, "ll")); err != nil {
		t.Fatal(err)
	}
	m := newMounter(t, lower, symlinkEv("ul", "somewhere/else"))
	if e := mustResolve(t, m, "ll"); e.kind != fuse.S_IFLNK || e.upper != nil || e.attr.Size != uint64(len("target")) {
		t.Errorf("lower symlink = %+v", e)
	}
	if e := mustResolve(t, m, "ul"); e.kind != fuse.S_IFLNK || e.upper == nil || e.attr.Size != uint64(len("somewhere/else")) {
		t.Errorf("upper symlink = %+v", e)
	}
	assertAgree(t, m, "", "ll", "ul")
}

// A lower tree that cannot be inspected is an error at every entry point,
// never absence: lookup must not say ENOENT, create must not say "free",
// sizing must not say zero, and getattr must not fabricate a directory.
func TestResolveLowerPermissionErrorIsNotAbsence(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "locked", "f.txt"), make([]byte, 20))
	mustWrite(t, filepath.Join(lower, "locked", "sub", "g.txt"), []byte("g"))
	if err := os.Chmod(filepath.Join(lower, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(lower, "locked"), 0o755) })
	m := newMounter(t, lower,
		writeEvent("locked/f.txt", 0, 4, false, true), // fallthrough over an unstat-able lower
		mkdirEv("locked/upperdir"),
	)

	wantEACCES := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, unix.EACCES) {
			t.Errorf("%s: err = %v, want EACCES", what, err)
		}
	}
	_, err := m.resolve("locked/f.txt")
	wantEACCES("resolve fallthrough file", err)
	_, err = m.resolve("locked/sub")
	wantEACCES("resolve lower dir", err)
	_, err = m.resolve("locked/sub/g.txt")
	wantEACCES("resolve deeper lower file", err)
	_, err = m.resolve("locked/missing")
	wantEACCES("resolve unknown name under locked dir", err)
	_, err = m.existsInMerged("locked/anything")
	wantEACCES("existsInMerged", err)
	_, err = m.effectiveSize("locked/f.txt")
	wantEACCES("effectiveSize", err)
	_, err = m.sizeOf("locked/f.txt")
	wantEACCES("sizeOf fallthrough", err)
	_, err = m.mergedEntries("locked")
	wantEACCES("mergedEntries", err)
	_, err = m.mergedEmpty("locked/sub")
	wantEACCES("mergedEmpty", err)
	if errno := errnoOf(err); errno != syscall.EACCES {
		t.Errorf("errnoOf(EACCES) = %v", errno)
	}
	// The locked dir itself is stat-able (its parent is readable): it resolves
	// as a lower dir, and an upper dir inside it resolves too, but the upper
	// dir's lower twin stat fails and is reported rather than guessed.
	if e := mustResolve(t, m, "locked"); !e.isDir() || e.upper != nil {
		t.Errorf("locked = %+v, want lower dir", e)
	}
	_, err = m.resolve("locked/upperdir")
	wantEACCES("resolve upper dir under locked lower", err)

	// A genuinely missing sibling elsewhere is still plain absence.
	if e := mustResolve(t, m, "nope"); e.present() {
		t.Errorf("missing = %+v", e)
	}
}

// resolve runs concurrently with commits (FUSE dispatches in parallel) and
// must always return an internally consistent snapshot: the upper node it
// reports is a copy, and kind/attr/size all derive from that one copy.
func TestResolveConcurrentWithApply(t *testing.T) {
	lower := t.TempDir()
	mustWrite(t, filepath.Join(lower, "f.txt"), make([]byte, 100))
	m := newMounter(t, lower)
	up := m.Session.Upper()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, rel := range []string{"f.txt", "d", "d/x", "d/y", ""} {
					e, err := m.resolve(rel)
					if err != nil {
						t.Errorf("resolve(%q): %v", rel, err)
						return
					}
					if !e.present() {
						continue
					}
					if e.upper != nil && e.kind == fuse.S_IFREG && uint64(e.size) != e.attr.Size {
						t.Errorf("inconsistent snapshot for %q: size %d attr %d", rel, e.size, e.attr.Size)
						return
					}
					if e.isDir() != (e.attr.Mode&unix.S_IFMT == fuse.S_IFDIR) {
						t.Errorf("kind/attr disagree for %q: %+v", rel, e)
						return
					}
				}
				if _, err := m.mergedEntries(""); err != nil {
					t.Errorf("mergedEntries: %v", err)
					return
				}
			}
		}()
	}

	seq := uint64(0)
	apply := func(ev *kfusev1.EventEnvelope) {
		seq++
		ev.Seq = seq
		if err := up.Apply(ev); err != nil {
			t.Fatalf("apply seq %d: %v", seq, err)
		}
	}
	for round := 0; round < 200; round++ {
		apply(writeEvent("f.txt", int64(round), 1, false, true))
		apply(mkdirEv("d"))
		apply(createEv("d/x"))
		apply(renameEv("d/x", "d/y", false))
		apply(unlinkEv("d/y"))
		apply(rmdirEv("d"))
		if round%50 == 0 {
			size := uint64(round)
			apply(setattrEv(&kfusev1.Setattr{Path: "f.txt", Size: &size}))
		}
	}
	close(stop)
	wg.Wait()
}
