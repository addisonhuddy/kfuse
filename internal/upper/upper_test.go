// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

func ev(seq uint64, op any) *kfusev1.EventEnvelope {
	e := &kfusev1.EventEnvelope{Seq: seq, SessionId: "s", LowerId: "l"}
	switch o := op.(type) {
	case *kfusev1.SessionStart:
		e.Op = &kfusev1.EventEnvelope_SessionStart{SessionStart: o}
	case *kfusev1.Create:
		e.Op = &kfusev1.EventEnvelope_Create{Create: o}
	case *kfusev1.Write:
		e.Op = &kfusev1.EventEnvelope_Write{Write: o}
	case *kfusev1.Unlink:
		e.Op = &kfusev1.EventEnvelope_Unlink{Unlink: o}
	case *kfusev1.Symlink:
		e.Op = &kfusev1.EventEnvelope_Symlink{Symlink: o}
	case *kfusev1.Rename:
		e.Op = &kfusev1.EventEnvelope_Rename{Rename: o}
	case *kfusev1.Setattr:
		e.Op = &kfusev1.EventEnvelope_Setattr{Setattr: o}
	}
	return e
}

func create(path string, mode uint32) *kfusev1.Create {
	return &kfusev1.Create{Path: path, Mode: mode, Uid: 1000, Gid: 1000}
}

func write(path string, off int64, length uint64, blob string) *kfusev1.Write {
	return &kfusev1.Write{Path: path, Offset: off, Length: length, BlobId: []byte(blob), Eof: true}
}

func sparseWrite(path string, off int64, length uint64, blob string) *kfusev1.Write {
	return &kfusev1.Write{Path: path, Offset: off, Length: length, BlobId: []byte(blob), Fallthrough: true}
}

func unlink(path string) *kfusev1.Unlink {
	return &kfusev1.Unlink{Path: path}
}

func start() *kfusev1.SessionStart {
	return &kfusev1.SessionStart{LowerId: "l"}
}

func TestApplyCreate(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, create("a.txt", 0644))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("a.txt")
	if !ok {
		t.Fatal("inode missing after Create")
	}
	if n.Kind != KindFile || n.Mode != 0644 || n.UID != 1000 || n.GID != 1000 {
		t.Fatalf("bad node: %+v", n)
	}
	if n.Size != 0 {
		t.Fatalf("size should be 0, got %d", n.Size)
	}
}

func TestApplyWriteAddsExtent(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("f", 0644)))
	_ = u.Apply(ev(2, write("f", 0, 10, "blob-1")))
	n, _ := u.Lookup("f")
	if len(n.Extents) != 1 {
		t.Fatalf("want 1 extent, got %d", len(n.Extents))
	}
	e := n.Extents[0]
	if e.FileOffset != 0 || e.Length != 10 || string(e.BlobID) != "blob-1" {
		t.Fatalf("bad extent: %+v", e)
	}
	if sz, _ := u.FileSize("f"); sz != 10 {
		t.Fatalf("size want 10, got %d", sz)
	}
}

func TestApplyWriteCreatesFileWhenMissing(t *testing.T) {
	u := New()
	w := write("new", 0, 4, "b")
	w.Mode = 0600
	_ = u.Apply(ev(1, w))
	n, ok := u.Lookup("new")
	if !ok || n.Kind != KindFile {
		t.Fatal("Write should create the file")
	}
	if n.Mode != 0600 {
		t.Fatalf("mode from Write: %o", n.Mode)
	}
}

func TestApplyUnlinkOverlayFile(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("f", 0644)))
	_ = u.Apply(ev(2, write("f", 0, 5, "b")))
	_ = u.Apply(ev(3, unlink("f")))
	if _, ok := u.Lookup("f"); ok {
		t.Fatal("node should be gone")
	}
	if u.IsWhiteout("f") {
		t.Fatal("overlay unlink must not leave a whiteout")
	}
	if u.whiteouts["f"] {
		t.Fatal("whiteout present after overlay-node unlink")
	}
}

func TestApplyUnlinkLowerFileLeavesWhiteout(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, unlink("lower-file")))
	if !u.IsWhiteout("lower-file") {
		t.Fatal("unlink of non-upper path must record a whiteout")
	}
}

func TestUnlinkThenCreateSamePath(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, unlink("x")))       // whiteout lower x
	_ = u.Apply(ev(2, create("x", 0644))) // create overlay x
	n, ok := u.Lookup("x")
	if !ok {
		t.Fatal("overlay node should win over whiteout")
	}
	_ = n
}

func TestWriteOverlapReplaces(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, write("f", 0, 10, "b1")))
	_ = u.Apply(ev(2, write("f", 5, 10, "b2")))
	n, _ := u.Lookup("f")
	if len(n.Extents) != 2 {
		t.Fatalf("want 2 extents, got %d: %+v", len(n.Extents), n.Extents)
	}
	if n.Extents[0].End() != 5 || n.Extents[1].FileOffset != 5 || string(n.Extents[1].BlobID) != "b2" {
		t.Fatalf("bad extents: %+v", n.Extents)
	}
}

func TestWriteEofTruncatesDown(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, write("f", 0, 100, "b1")))
	w := write("f", 0, 10, "b2")
	w.Eof = true
	_ = u.Apply(ev(2, w))
	n, _ := u.Lookup("f")
	if n.Size != 10 {
		t.Fatalf("size want 10, got %d", n.Size)
	}
	if len(n.Extents) != 1 || n.Extents[0].Length != 10 {
		t.Fatalf("extents should be clipped to 10: %+v", n.Extents)
	}
}

func TestSeqMustBeMonotonic(t *testing.T) {
	u := New()
	_ = u.Apply(ev(2, create("a", 0644)))
	if err := u.Apply(ev(1, create("b", 0644))); err == nil {
		t.Fatal("out-of-order seq must error")
	}
	if err := u.Apply(ev(2, create("c", 0644))); err == nil {
		t.Fatal("duplicate seq must error")
	}
}

func TestInvalidPathsRejected(t *testing.T) {
	cases := []string{"", "/abs", "a/../b", "a/./b", ".", "..", "a//b", "a/", "a\xffb"}
	for _, p := range cases {
		u := New()
		err := u.Apply(ev(1, &kfusev1.Create{Path: p, Mode: 0644}))
		if err == nil {
			t.Fatalf("path %q must be rejected", p)
		}
	}
}

func TestNormalizePathRejectsInvalidUTF8Encodings(t *testing.T) {
	cases := map[string]string{
		"overlong 2-byte":      "a\xC0\xAFb",
		"overlong 3-byte":      "a\xE0\x80\xAFb",
		"overlong 4-byte":      "a\xF0\x80\x80\xAFb",
		"surrogate U+D800":     "a\xED\xA0\x80b",
		"surrogate U+DFFF":     "a\xED\xBF\xBFb",
		"above U+10FFFF":       "a\xF4\x90\x80\x80b",
		"5-byte lead":          "a\xF8\x88\x80\x80\x80b",
		"truncated 2-byte":     "a\xC3",
		"truncated 3-byte":     "a\xE2\x82",
		"truncated 4-byte mid": "a\xF0\x9F\x98b",
		"stray continuation":   "a\x80b",
		"invalid byte":         "a\xffb",
	}
	for name, p := range cases {
		if _, err := NormalizePath(p); err == nil {
			t.Errorf("%s: NormalizePath(%q) accepted invalid UTF-8", name, p)
		} else if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: err = %v, want ErrInvalidPath", name, err)
		}
	}
}

func TestNormalizePathAcceptsValidMultibyte(t *testing.T) {
	cases := []string{
		"caf\u00e9.txt",
		"dir/\u65e5\u672c\u8a9e/file",
		"emoji/\U0001F600.png",
		"\U0010FFFF",
		"plain/ascii",
	}
	for _, p := range cases {
		got, err := NormalizePath(p)
		if err != nil {
			t.Errorf("NormalizePath(%q) = %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("NormalizePath(%q) = %q, want unchanged", p, got)
		}
	}
}

func TestNormalizePathPreservesRelativeRules(t *testing.T) {
	reject := []string{"", "/abs", "a/../b", "a/./b", ".", "..", "a//b", "a/", "/", "./a", "../a"}
	for _, p := range reject {
		if _, err := NormalizePath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("NormalizePath(%q) err = %v, want ErrInvalidPath", p, err)
		}
	}
	accept := []string{"a", "a/b", "a/b/c", "...", "a/...", "a b/c d", "a\\b"}
	for _, p := range accept {
		if got, err := NormalizePath(p); err != nil || got != p {
			t.Errorf("NormalizePath(%q) = %q, %v; want %q, nil", p, got, err, p)
		}
	}
}

func TestLookupSnapshotOwnsBlobIDBytes(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, create("f", 0644))); err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(ev(2, write("f", 0, 4, "blob-one"))); err != nil {
		t.Fatal(err)
	}
	a, _ := u.Lookup("f")
	b, _ := u.Lookup("f")
	a.Extents[0].BlobID[0] = 'X'
	if got := string(b.Extents[0].BlobID); got != "blob-one" {
		t.Fatalf("mutating one snapshot leaked into another: %q", got)
	}
	c, _ := u.Lookup("f")
	if got := string(c.Extents[0].BlobID); got != "blob-one" {
		t.Fatalf("mutating a snapshot leaked into stored state: %q", got)
	}
}

func TestChildrenSnapshotOwnsBlobIDBytes(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, create("d/f", 0644))); err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(ev(2, write("d/f", 0, 4, "blob-one"))); err != nil {
		t.Fatal(err)
	}
	kids, _ := u.Children("d")
	kids["f"].Extents[0].BlobID[0] = 'X'
	n, _ := u.Lookup("d/f")
	if got := string(n.Extents[0].BlobID); got != "blob-one" {
		t.Fatalf("mutating a Children snapshot leaked into stored state: %q", got)
	}
}

func TestWriteOnDirRejected(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("d", 0755)))
	u.nodes["d"].Kind = KindDir
	if err := u.Apply(ev(2, write("d", 0, 1, "b"))); err == nil {
		t.Fatal("Write on dir must error")
	}
}

func symlink(path, target string) *kfusev1.Symlink {
	return &kfusev1.Symlink{Path: path, Target: target}
}

func TestApplySymlinkKeepsOwner(t *testing.T) {
	u := New()
	s := symlink("s", "t")
	s.Uid, s.Gid = 1234, 5678
	if err := u.Apply(ev(1, s)); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("s")
	if !ok {
		t.Fatal("symlink missing after Symlink")
	}
	if n.UID != 1234 || n.GID != 5678 {
		t.Fatalf("symlink owner = %d:%d, want 1234:5678", n.UID, n.GID)
	}
}

func TestApplySymlink(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, symlink("s", "target-file"))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("s")
	if !ok {
		t.Fatal("symlink missing after Symlink")
	}
	if n.Kind != KindSymlink || n.Target != "target-file" {
		t.Fatalf("bad symlink node: %+v", n)
	}
}

func TestApplySymlinkDuplicate(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, symlink("s", "a")))
	if err := u.Apply(ev(2, symlink("s", "b"))); err == nil {
		t.Fatal("duplicate Symlink must error")
	}
}

func TestApplySymlinkOverExistingNodeErrors(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("f", 0644)))
	if err := u.Apply(ev(2, symlink("f", "x"))); err == nil {
		t.Fatal("Symlink over existing node must error")
	}
}

func TestUnlinkSymlinkRemovesNode(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, symlink("s", "t")))
	_ = u.Apply(ev(2, unlink("s")))
	if _, ok := u.Lookup("s"); ok {
		t.Fatal("symlink should be gone")
	}
	if u.IsWhiteout("s") {
		t.Fatal("overlay symlink unlink must not leave a whiteout")
	}
}

func TestSymlinkShadowingWhiteout(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, unlink("s"))) // whiteout lower s
	_ = u.Apply(ev(2, symlink("s", "t")))
	n, ok := u.Lookup("s")
	if !ok || n.Kind != KindSymlink {
		t.Fatal("overlay symlink must shadow the whiteout")
	}
}

func TestReplayDeterminismWithSymlinks(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		ev(1, symlink("s", "a.txt")),
		ev(2, symlink("d/l", "b.txt")),
		ev(3, unlink("s")),
		ev(4, symlink("s", "c.txt")),
	}
	u1, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := u1.Marshal()
	b2, _ := u2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("replays differ:\n%s\n%s", b1, b2)
	}
	n, _ := u1.Lookup("s")
	if n == nil || n.Target != "c.txt" {
		t.Fatal("replay lost final symlink state")
	}
	if _, ok := u1.Lookup("d/l"); !ok {
		t.Fatal("nested symlink lost")
	}
}

func TestMarshalRoundTripSymlink(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, symlink("s", "t")))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := u2.Lookup("s")
	if !ok || n.Kind != KindSymlink || n.Target != "t" {
		t.Fatal("symlink lost in round trip")
	}
}

func u32(v uint32) *uint32 { return &v }
func u64(v uint64) *uint64 { return &v }

func setattr(path string) *kfusev1.Setattr { return &kfusev1.Setattr{Path: path} }

func TestApplySetattrChmodOnOverlayFile(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("f", 0644)))
	sa := setattr("f")
	sa.Mode = u32(0600)
	if err := u.Apply(ev(2, sa)); err != nil {
		t.Fatal(err)
	}
	n, _ := u.Lookup("f")
	if n.Mode != 0600 {
		t.Fatalf("mode want 0600, got %o", n.Mode)
	}
}

func TestApplySetattrTruncateDown(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, write("f", 0, 100, "b1")))
	sa := setattr("f")
	sa.Size = u64(10)
	if err := u.Apply(ev(2, sa)); err != nil {
		t.Fatal(err)
	}
	n, _ := u.Lookup("f")
	if n.Size != 10 {
		t.Fatalf("size want 10, got %d", n.Size)
	}
	if len(n.Extents) != 1 || n.Extents[0].Length != 10 {
		t.Fatalf("extents should be clipped to 10: %+v", n.Extents)
	}
}

func TestApplySetattrTruncateUp(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, write("f", 0, 10, "b1")))
	sa := setattr("f")
	sa.Size = u64(50)
	if err := u.Apply(ev(2, sa)); err != nil {
		t.Fatal(err)
	}
	n, _ := u.Lookup("f")
	if n.Size != 50 {
		t.Fatalf("size want 50, got %d", n.Size)
	}
	if len(n.Extents) != 1 {
		t.Fatalf("truncate-up must keep extents: %+v", n.Extents)
	}
}

func TestApplySetattrOnDirRejectsTruncate(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0755)))
	sa := setattr("d")
	sa.Size = u64(10)
	if err := u.Apply(ev(2, sa)); err == nil {
		t.Fatal("truncate of a dir must error")
	}
}

func TestApplySetattrOverrideOnLowerFile(t *testing.T) {
	u := New()
	sa := setattr("lower-file")
	sa.Mode = u32(0400)
	sa.MtimeNs = int64p(1234)
	if err := u.Apply(ev(1, sa)); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.Lookup("lower-file"); ok {
		t.Fatal("setattr on lower path must not create an overlay node")
	}
	ov := u.Override("lower-file")
	if ov == nil || ov.Mode == nil || *ov.Mode != 0400 {
		t.Fatalf("override missing/wrong: %+v", ov)
	}
	if ov.MtimeNs == nil || *ov.MtimeNs != 1234 {
		t.Fatalf("mtime override missing: %+v", ov)
	}
}

func int64p(v int64) *int64 { return &v }

func TestApplySetattrTruncateLowerFileMaterializesFallthrough(t *testing.T) {
	u := New()
	// First chmod lower file (records override)
	sa1 := setattr("lower-f")
	sa1.Mode = u32(0600)
	_ = u.Apply(ev(1, sa1))

	// Truncate lower file: should materialize fallthrough node with mode 0600 and size 20
	sa2 := setattr("lower-f")
	sa2.Size = u64(20)
	if err := u.Apply(ev(2, sa2)); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("lower-f")
	if !ok || !n.Fallthrough {
		t.Fatal("truncate of lower file must materialize a fallthrough node")
	}
	if n.Size != 20 {
		t.Fatalf("size want 20, got %d", n.Size)
	}
	if n.Mode != 0600 {
		t.Fatalf("mode should inherit earlier override (0600), got %o", n.Mode)
	}
	if u.Override("lower-f") != nil {
		t.Fatal("override should be cleaned up after node materialization")
	}
}

func TestApplySetattrOnSymlink(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, symlink("s", "target")))
	sa := setattr("s")
	sa.Mode = u32(0700)
	sa.Uid = u32(500)
	if err := u.Apply(ev(2, sa)); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("s")
	if !ok || n.Kind != KindSymlink {
		t.Fatal("symlink node missing")
	}
	if n.Mode != 0700 || n.UID != 500 {
		t.Fatalf("bad symlink attrs: %+v", n)
	}
}

func TestApplySetattrOverrideMerge(t *testing.T) {
	u := New()
	sa1 := setattr("x")
	sa1.Mode = u32(0600)
	_ = u.Apply(ev(1, sa1))
	sa2 := setattr("x")
	sa2.Uid = u32(42)
	_ = u.Apply(ev(2, sa2))
	ov := u.Override("x")
	if ov.Mode == nil || *ov.Mode != 0600 || ov.UID == nil || *ov.UID != 42 {
		t.Fatalf("overrides must merge: %+v", ov)
	}
}

func TestSetattrThenCreateWins(t *testing.T) {
	u := New()
	sa := setattr("x")
	sa.Mode = u32(0600)
	_ = u.Apply(ev(1, sa)) // override on (assumed) lower x
	_ = u.Apply(ev(2, create("x", 0644)))
	n, _ := u.Lookup("x")
	if n.Mode != 0644 {
		t.Fatalf("create must win over earlier override, got %o", n.Mode)
	}
}

func TestReplayDeterminismWithSetattr(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		ev(1, create("f", 0644)),
		ev(2, write("f", 0, 10, "b")),
		ev(3, setattr("f")),
		ev(4, setattr("lower")),
		ev(5, symlink("s", "f")),
	}
	events[2].GetSetattr().Mode = u32(0600)
	events[3].GetSetattr().Uid = u32(7)
	u1, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := u1.Marshal()
	b2, _ := u2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("replays differ:\n%s\n%s", b1, b2)
	}
}

func TestMarshalRoundTripOverride(t *testing.T) {
	u := New()
	sa := setattr("lower")
	sa.Mode = u32(0400)
	_ = u.Apply(ev(1, sa))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	ov := u2.Override("lower")
	if ov == nil || ov.Mode == nil || *ov.Mode != 0400 {
		t.Fatal("override lost in round trip")
	}
}

func rename(from, to string, fromLower bool) *kfusev1.Rename {
	return &kfusev1.Rename{From: from, To: to, FromLower: fromLower}
}

func TestApplyRenameOverlayFile(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("a", 0644)))
	_ = u.Apply(ev(2, write("a", 0, 4, "b")))
	if err := u.Apply(ev(3, rename("a", "b", false))); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.Lookup("a"); ok {
		t.Fatal("old path must be gone")
	}
	n, ok := u.Lookup("b")
	if !ok || n.Kind != KindFile || len(n.Extents) != 1 {
		t.Fatalf("file lost after rename: %+v", n)
	}
	if u.IsWhiteout("a") {
		t.Fatal("pure-overlay rename must not whiteout the source")
	}
}

func TestApplyRenameOverlayDirWithChildren(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0755)))
	_ = u.Apply(ev(2, create("d/f", 0644)))
	_ = u.Apply(ev(3, write("d/f", 0, 3, "b")))
	if err := u.Apply(ev(4, rename("d", "e", false))); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.Lookup("d"); ok {
		t.Fatal("old dir must be gone")
	}
	n, ok := u.Lookup("e/f")
	if !ok || len(n.Extents) != 1 {
		t.Fatalf("child lost after dir rename: %+v", n)
	}
}

func TestApplyRenameOverExistingFile(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("a", 0644)))
	_ = u.Apply(ev(2, create("b", 0644)))
	_ = u.Apply(ev(3, write("b", 0, 5, "old")))
	_ = u.Apply(ev(4, write("a", 0, 3, "new")))
	if err := u.Apply(ev(5, rename("a", "b", false))); err != nil {
		t.Fatal(err)
	}
	n, _ := u.Lookup("b")
	if n == nil || len(n.Extents) != 1 || string(n.Extents[0].BlobID) != "new" {
		t.Fatalf("rename over existing must replace content: %+v", n)
	}
}

func TestApplyRenameLowerFileRedirectsAndWhiteouts(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, rename("lower-a", "a", true))); err != nil {
		t.Fatal(err)
	}
	if !u.IsWhiteout("lower-a") {
		t.Fatal("lower-backed rename must whiteout the source")
	}
	if _, ok := u.Lookup("a"); ok {
		t.Fatal("lower rename must not create an overlay node")
	}
	src, ok := u.Redirect("a")
	if !ok || src != "lower-a" {
		t.Fatalf("redirect a -> lower-a missing, got %q %v", src, ok)
	}
}

func TestApplyRenameLowerDirRedirectsDescendants(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, rename("olddir", "newdir", true))); err != nil {
		t.Fatal(err)
	}
	if lp, ok := u.LowerPath("newdir/child.txt"); !ok || lp != "olddir/child.txt" {
		t.Fatalf("descendant redirect: got %q %v", lp, ok)
	}
}

func TestApplyRenameThenRenameRedirectAgain(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, rename("x", "y", true))) // y -> x
	_ = u.Apply(ev(2, rename("y", "z", true))) // z -> x (move redirect)
	if _, ok := u.Redirect("y"); ok {
		t.Fatal("old redirect y must be gone")
	}
	if src, ok := u.Redirect("z"); !ok || src != "x" {
		t.Fatalf("redirect z -> x missing, got %q %v", src, ok)
	}
	if lp, _ := u.LowerPath("z/sub"); lp != "x/sub" {
		t.Fatalf("nested redirect after re-rename: %q", lp)
	}
}

func TestApplyRenameDirIntoOwnSubtreeRejected(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0755)))
	_ = u.Apply(evMkdir(2, mkdir("d/sub", 0755)))
	if err := u.Apply(ev(3, rename("d", "d/sub", false))); err == nil {
		t.Fatal("rename dir into own subtree must error")
	}
}

func TestApplyRenameMovesWhiteouts(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0755)))
	_ = u.Apply(ev(2, unlink("d/lower-x"))) // whiteout under d
	_ = u.Apply(ev(3, rename("d", "e", false)))
	if !u.IsWhiteout("e/lower-x") {
		t.Fatal("whiteout under renamed dir must move to e/lower-x")
	}
	if u.IsWhiteout("d/lower-x") {
		t.Fatal("old whiteout path must be gone")
	}
}

func TestApplyRenameFallthroughFileKeepsLowerRel(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, sparseWrite("lower-f", 100, 50, "b"))) // fallthrough, LowerRel=lower-f
	if err := u.Apply(ev(2, rename("lower-f", "renamed", true))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("renamed")
	if !ok || !n.Fallthrough {
		t.Fatal("fallthrough node must move to renamed")
	}
	if n.LowerRel != "lower-f" {
		t.Fatalf("LowerRel must stay at original lower path, got %q", n.LowerRel)
	}
	if !u.IsWhiteout("lower-f") {
		t.Fatal("from_lower rename must whiteout the lower twin")
	}
}

func TestApplyRenameMovesOverrides(t *testing.T) {
	u := New()
	sa := setattr("lower-x")
	sa.Mode = u32(0400)
	_ = u.Apply(ev(1, sa)) // override on lower file
	if err := u.Apply(ev(2, rename("lower-x", "x", true))); err != nil {
		t.Fatal(err)
	}
	if u.Override("lower-x") != nil {
		t.Fatal("override must move off the old path")
	}
	ov := u.Override("x")
	if ov == nil || ov.Mode == nil || *ov.Mode != 0400 {
		t.Fatalf("override must move to x: %+v", ov)
	}
}

func TestApplyRenameOverridesUnderDir(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0755)))
	sa := setattr("d/lower-f")
	sa.Mode = u32(0600)
	_ = u.Apply(ev(2, sa)) // override under d
	_ = u.Apply(ev(3, rename("d", "e", false)))
	if u.Override("d/lower-f") != nil {
		t.Fatal("override under renamed dir must move")
	}
	if ov := u.Override("e/lower-f"); ov == nil || *ov.Mode != 0600 {
		t.Fatalf("override must move to e/lower-f: %+v", ov)
	}
}

func TestReplayDeterminismWithRename(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		ev(1, create("a", 0644)),
		ev(2, write("a", 0, 3, "b")),
		ev(3, rename("a", "b", false)),
		ev(4, rename("lower-x", "x", true)),
		ev(5, rename("b", "c", false)),
	}
	u1, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := u1.Marshal()
	b2, _ := u2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("replays differ:\n%s\n%s", b1, b2)
	}
}

func TestMarshalRoundTripRedirect(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, rename("lower-a", "a", true)))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	src, ok := u2.Redirect("a")
	if !ok || src != "lower-a" {
		t.Fatal("redirect lost in round trip")
	}
	if !u2.IsWhiteout("lower-a") {
		t.Fatal("whiteout lost in round trip")
	}
}

func TestApplySparseWriteMaterializesFallthrough(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, sparseWrite("lower-file", 100, 50, "b"))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("lower-file")
	if !ok || !n.Fallthrough {
		t.Fatal("sparse write must materialize a fallthrough node")
	}
	if n.Size != -1 {
		t.Fatalf("fallthrough size should stay unknown (-1), got %d", n.Size)
	}
	if len(n.Extents) != 1 || n.Extents[0].FileOffset != 100 || n.Extents[0].Length != 50 {
		t.Fatalf("bad extent: %+v", n.Extents)
	}
}

func TestSparseWriteDoesNotAdvanceExplicitSize(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, sparseWrite("f", 100, 50, "b1")))
	_ = u.Apply(ev(2, sparseWrite("f", 300, 10, "b2")))
	n, _ := u.Lookup("f")
	if n.Size != -1 {
		t.Fatalf("size should stay -1 after sparse writes, got %d", n.Size)
	}
	if len(n.Extents) != 2 {
		t.Fatalf("want 2 extents, got %d", len(n.Extents))
	}
}

func TestSparseWriteEofTruncatesToExplicitSize(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, sparseWrite("f", 100, 50, "b1")))
	w := sparseWrite("f", 0, 10, "b2")
	w.Eof = true
	_ = u.Apply(ev(2, w))
	n, _ := u.Lookup("f")
	if n.Size != 10 {
		t.Fatalf("eof truncate should set explicit size 10, got %d", n.Size)
	}
}

func TestMarshalRoundTripFallthrough(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, sparseWrite("f", 100, 50, "b")))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := u2.Lookup("f")
	if !ok || !n.Fallthrough || n.Size != -1 {
		t.Fatal("fallthrough lost in round trip")
	}
	if n.LowerRel != "f" {
		t.Fatalf("LowerRel lost in round trip, got %q", n.LowerRel)
	}
}

func TestSparseWriteInheritsOverride(t *testing.T) {
	u := New()
	sa := setattr("lower-f")
	sa.Mode = u32(0600)
	_ = u.Apply(ev(1, sa)) // chmod the lower file -> override
	if err := u.Apply(ev(2, sparseWrite("lower-f", 0, 5, "b"))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("lower-f")
	if !ok || !n.Fallthrough {
		t.Fatal("sparse write must materialize a fallthrough node")
	}
	if n.Mode != 0600 {
		t.Fatalf("mode should inherit the override (0600), got %o", n.Mode)
	}
	if u.Override("lower-f") != nil {
		t.Fatal("override must be absorbed, not left dangling")
	}
}

func TestSparseWritePastLowerEOF(t *testing.T) {
	u := New()
	w := sparseWrite("f", 100, 50, "b")
	w.Eof = true // FUSE sets eof when write reaches/passes the effective end
	if err := u.Apply(ev(1, w)); err != nil {
		t.Fatal(err)
	}
	n, _ := u.Lookup("f")
	if n.Size != 150 {
		t.Fatalf("write past EOF should set explicit size 150, got %d", n.Size)
	}
	if len(n.Extents) != 1 || n.Extents[0].FileOffset != 100 {
		t.Fatalf("bad extent: %+v", n.Extents)
	}
}

func TestSparseWriteOverlapReplaces(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, sparseWrite("f", 0, 10, "b1")))
	_ = u.Apply(ev(2, sparseWrite("f", 5, 10, "b2")))
	n, _ := u.Lookup("f")
	if len(n.Extents) != 2 {
		t.Fatalf("want 2 extents after overlapping write, got %d: %+v", len(n.Extents), n.Extents)
	}
	if string(n.Extents[0].BlobID) != "b1" || n.Extents[0].Length != 5 {
		t.Fatalf("left extent should be b1[0,5): %+v", n.Extents)
	}
	if string(n.Extents[1].BlobID) != "b2" || n.Extents[1].FileOffset != 5 {
		t.Fatalf("right extent should be b2[5,15): %+v", n.Extents)
	}
}

// A mid-extent overwrite splits the old extent; the pieces plus the new write
// must come back sorted by file offset (readers stop at the first extent past
// their window, so an unsorted list hides data).
func TestInsertExtentKeepsListSorted(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, write("f", 0, 10, "b1")))
	over := write("f", 4, 2, "b2")
	over.Eof = false // an interior overwrite, so the file keeps its size
	_ = u.Apply(ev(2, over))
	n, _ := u.Lookup("f")
	if len(n.Extents) != 3 {
		t.Fatalf("want 3 extents after a mid-extent overwrite, got %d: %+v", len(n.Extents), n.Extents)
	}
	for i := 1; i < len(n.Extents); i++ {
		if n.Extents[i-1].FileOffset >= n.Extents[i].FileOffset {
			t.Fatalf("extents not sorted by offset: %+v", n.Extents)
		}
	}
	mid := n.Extents[1]
	if mid.FileOffset != 4 || string(mid.BlobID) != "b2" {
		t.Fatalf("new write should sit between the split pieces: %+v", n.Extents)
	}
	tail := n.Extents[2]
	if tail.FileOffset != 6 || tail.BlobOffset != 6 || tail.Length != 4 {
		t.Fatalf("tail piece should point into b1 at offset 6: %+v", tail)
	}
}

func TestTruncateZeroThenWrite(t *testing.T) {
	u := New()
	sa := setattr("f")
	sa.Size = u64(0)
	_ = u.Apply(ev(1, sa)) // O_TRUNC: fallthrough node with Size 0
	n, _ := u.Lookup("f")
	if n.Size != 0 {
		t.Fatalf("truncate to 0 should set Size 0, got %d", n.Size)
	}
	w := sparseWrite("f", 0, 5, "b")
	w.Eof = true
	_ = u.Apply(ev(2, w))
	n, _ = u.Lookup("f")
	if n.Size != 5 {
		t.Fatalf("write after O_TRUNC should grow to 5, got %d", n.Size)
	}
	if len(n.Extents) != 1 || n.Extents[0].Length != 5 {
		t.Fatalf("bad extent after truncate-then-write: %+v", n.Extents)
	}
}

func TestSessionStartNoMutation(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, start()))
	if len(u.nodes) != 0 || len(u.whiteouts) != 0 {
		t.Fatal("SessionStart must not mutate state")
	}
	if u.LowerID() != "l" {
		t.Fatalf("lower id: %q", u.LowerID())
	}
}

func TestReplayDeterminism(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		ev(1, start()),
		ev(2, create("a.txt", 0644)),
		ev(3, write("a.txt", 0, 10, "blob-a")),
		ev(4, create("b.txt", 0600)),
		ev(5, write("b.txt", 0, 3, "blob-b")),
		ev(6, unlink("b.txt")),
	}
	u1, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := u1.Marshal()
	b2, _ := u2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("replays differ:\n%s\n%s", b1, b2)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		ev(1, create("a.txt", 0644)),
		ev(2, write("a.txt", 0, 10, "blob-a")),
		ev(3, unlink("lower-x")),
	}
	u1, _ := Replay(events)
	b, err := u1.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(u1.nodes["a.txt"].Extents[0].BlobID, u2.nodes["a.txt"].Extents[0].BlobID) {
		t.Fatal("blob id lost in round trip")
	}
	if !u2.IsWhiteout("lower-x") {
		t.Fatal("whiteout lost in round trip")
	}
}

func TestChildren(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("d/f1", 0644)))
	_ = u.Apply(ev(2, create("d/f2", 0644)))
	_ = u.Apply(ev(3, unlink("d/lower-x")))
	nodes, white := u.Children("d")
	if len(nodes) != 2 {
		t.Fatalf("want f1+f2: %v", nodes)
	}
	if !white["lower-x"] {
		t.Fatal("lower-x should be whiteouted in children")
	}
}

func mkdir(path string, mode uint32) *kfusev1.Mkdir {
	return &kfusev1.Mkdir{Path: path, Mode: mode, Uid: 1000, Gid: 1000}
}

func rmdir(path string) *kfusev1.Rmdir {
	return &kfusev1.Rmdir{Path: path}
}

func evMkdir(seq uint64, m *kfusev1.Mkdir) *kfusev1.EventEnvelope {
	e := ev(seq, nil)
	e.Op = &kfusev1.EventEnvelope_Mkdir{Mkdir: m}
	return e
}

func evRmdir(seq uint64, r *kfusev1.Rmdir) *kfusev1.EventEnvelope {
	e := ev(seq, nil)
	e.Op = &kfusev1.EventEnvelope_Rmdir{Rmdir: r}
	return e
}

func TestSeqLastAdvancesOnRenameAndSetattr(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("a", 0644)))
	_ = u.Apply(ev(2, rename("a", "b", false)))
	sa := setattr("b")
	sa.Mode = u32(0600)
	_ = u.Apply(ev(3, sa))
	if got := u.SeqLast(); got != 3 {
		t.Fatalf("SeqLast = %d, want 3 (rename/setattr must advance seq)", got)
	}
}

func TestMarshalRoundTripPreservesSeqLast(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("a", 0644)))
	_ = u.Apply(ev(2, write("a", 0, 3, "b")))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := u2.SeqLast(); got != 2 {
		t.Fatalf("SeqLast after round trip = %d, want 2", got)
	}
	// The next event must not be treated as out-of-order.
	if err := u2.Apply(ev(3, create("c", 0644))); err != nil {
		t.Fatalf("apply seq 3 after restore: %v", err)
	}
}

func TestSparseWriteCarriesLowerAttrs(t *testing.T) {
	u := New()
	w := sparseWrite("lower-f", 0, 5, "b")
	w.Mode = 0640
	w.Uid = 1001
	w.Gid = 1002
	if err := u.Apply(ev(1, w)); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("lower-f")
	if !ok || !n.Fallthrough {
		t.Fatal("sparse write must materialize a fallthrough node")
	}
	if n.Mode != 0640 || n.UID != 1001 || n.GID != 1002 {
		t.Fatalf("node must carry lower attrs, got mode=%o uid=%d gid=%d", n.Mode, n.UID, n.GID)
	}
}

func TestApplyMkdir(t *testing.T) {
	u := New()
	if err := u.Apply(evMkdir(1, mkdir("d", 0o755))); err != nil {
		t.Fatal(err)
	}
	n, ok := u.Lookup("d")
	if !ok {
		t.Fatal("dir missing after Mkdir")
	}
	if n.Kind != KindDir || n.Mode != 0o755 || n.UID != 1000 || n.GID != 1000 {
		t.Fatalf("bad dir node: %+v", n)
	}
}

func TestApplyMkdirDuplicate(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0o755)))
	if err := u.Apply(evMkdir(2, mkdir("d", 0o755))); err == nil {
		t.Fatal("duplicate Mkdir must error")
	}
}

func TestApplyRmdirUpperDir(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0o755)))
	_ = u.Apply(evRmdir(2, rmdir("d")))
	if _, ok := u.Lookup("d"); ok {
		t.Fatal("dir should be gone")
	}
	if u.IsWhiteout("d") {
		t.Fatal("upper-dir rmdir must not leave a whiteout")
	}
}

func TestApplyRmdirLowerDirLeavesWhiteout(t *testing.T) {
	u := New()
	_ = u.Apply(evRmdir(1, rmdir("dir")))
	if !u.IsWhiteout("dir") {
		t.Fatal("rmdir of non-upper path must whiteout")
	}
}

func TestApplyRmdirNonEmptyErrors(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0o755)))
	_ = u.Apply(ev(2, create("d/f", 0644)))
	err := u.Apply(evRmdir(3, rmdir("d")))
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("rmdir of non-empty dir must error, got %v", err)
	}
}

func TestApplyRmdirOnFileErrors(t *testing.T) {
	u := New()
	_ = u.Apply(ev(1, create("f", 0644)))
	if err := u.Apply(evRmdir(2, rmdir("f"))); err == nil {
		t.Fatal("Rmdir on a file must error")
	}
}

func TestRmdirThenMkdirShadowsWhiteout(t *testing.T) {
	u := New()
	_ = u.Apply(evRmdir(1, rmdir("dir"))) // whiteout lower dir
	_ = u.Apply(evMkdir(2, mkdir("dir", 0o755)))
	n, ok := u.Lookup("dir")
	if !ok || n.Kind != KindDir {
		t.Fatal("re-mkdir must create an upper dir shadowing the whiteout")
	}
	if !u.Hidden("dir/x") {
		t.Fatal("children under a whiteouted lower dir stay hidden (opaque)")
	}
}

func TestHiddenAncestorWhiteouts(t *testing.T) {
	u := New()
	_ = u.Apply(evRmdir(1, rmdir("a")))
	_ = u.Apply(evRmdir(2, rmdir("b/c")))
	cases := []struct {
		path string
		want bool
	}{
		{"a", true},
		{"a/x", true},
		{"a/x/y", true},
		{"b", false},
		{"b/c", true},
		{"b/c/d", true},
		{"b/other", false},
		{"x", false},
	}
	for _, c := range cases {
		if got := u.Hidden(c.path); got != c.want {
			t.Fatalf("Hidden(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestHiddenShadowedByNode(t *testing.T) {
	u := New()
	_ = u.Apply(evRmdir(1, rmdir("d")))
	_ = u.Apply(evMkdir(2, mkdir("d", 0o755)))
	_ = u.Apply(ev(3, create("d/upper-file", 0644)))
	if _, ok := u.Lookup("d"); !ok {
		t.Fatal("upper dir node must shadow the whiteout at lookup level")
	}
	if _, ok := u.Lookup("d/upper-file"); !ok {
		t.Fatal("upper child must be visible under shadowing dir")
	}
	if !u.Hidden("d/lower-file") {
		t.Fatal("lower child under whiteouted dir must stay hidden (opaque)")
	}
}

func TestReplayDeterminismWithDirs(t *testing.T) {
	events := []*kfusev1.EventEnvelope{
		evMkdir(1, mkdir("d", 0o755)),
		ev(2, create("d/f", 0644)),
		ev(3, write("d/f", 0, 3, "b")),
		evRmdir(4, rmdir("gone-dir")),
		evRmdir(5, rmdir("d2")),
		evMkdir(6, mkdir("d2", 0o700)),
	}
	u1, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	u2, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := u1.Marshal()
	b2, _ := u2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("replays differ:\n%s\n%s", b1, b2)
	}
	n, _ := u1.Lookup("d/f")
	if n == nil || len(n.Extents) != 1 {
		t.Fatal("file under dir lost in replay")
	}
	if !u1.IsWhiteout("gone-dir") {
		t.Fatal("lower-dir whiteout lost in replay")
	}
	if _, ok := u1.Lookup("d2"); !ok {
		t.Fatal("shadowing dir missing")
	}
}

func TestMarshalRoundTripDir(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0o755)))
	_ = u.Apply(ev(2, create("d/f", 0644)))
	b, err := u.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	u2, err := UnmarshalState(b)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := u2.Lookup("d")
	if !ok || n.Kind != KindDir {
		t.Fatal("dir node lost in round trip")
	}
	if _, ok := u2.Lookup("d/f"); !ok {
		t.Fatal("child lost in round trip")
	}
}

// Check mirrors Apply's preconditions without consuming a seq or mutating.
func TestCheckMirrorsApplyPreconditions(t *testing.T) {
	u := New()
	_ = u.Apply(evMkdir(1, mkdir("d", 0o755)))
	_ = u.Apply(ev(2, create("d/f", 0644)))
	before, _ := u.Marshal()

	cases := []struct {
		name string
		ev   *kfusev1.EventEnvelope
		want error
	}{
		{"create over file", ev(0, create("d/f", 0644)), ErrExists},
		{"mkdir over dir", evMkdir(0, mkdir("d", 0o755)), ErrExists},
		{"rmdir non-empty", evRmdir(0, rmdir("d")), ErrNotEmpty},
		{"rmdir file", evRmdir(0, rmdir("d/f")), ErrNotDir},
		{"unlink dir", ev(0, unlink("d")), ErrIsDir},
		{"rename ghost", ev(0, rename("ghost", "x", false)), ErrNoEntry},
		{"rename into self", ev(0, rename("d", "d/inside", false)), ErrInvalidPath},
		{"bad path", ev(0, create("../x", 0644)), ErrInvalidPath},
	}
	for _, c := range cases {
		if err := u.Check(c.ev); !errors.Is(err, c.want) {
			t.Errorf("Check(%s) = %v, want %v", c.name, err, c.want)
		}
		c.ev.Seq = 3
		if err := u.Apply(c.ev); !errors.Is(err, c.want) {
			t.Errorf("Apply(%s) = %v, want %v", c.name, err, c.want)
		}
	}
	// Accepted events pass Check without being applied.
	for _, ok := range []*kfusev1.EventEnvelope{
		ev(0, create("d/g", 0644)),
		ev(0, unlink("d/f")),
		ev(0, unlink("lower-only")),
		ev(0, rename("d/f", "d/h", false)),
		ev(0, rename("lower", "moved", true)),
	} {
		if err := u.Check(ok); err != nil {
			t.Errorf("Check(%v) = %v, want nil", ok.Op, err)
		}
	}
	after, _ := u.Marshal()
	if string(before) != string(after) || u.SeqLast() != 2 {
		t.Fatal("Check or a refused Apply mutated the upper")
	}
}
