// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"strings"
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// parentWithFiles commits one create+write per name and returns the parent
// partition offset of each name's Write record (the offset a branch must
// reach to include that file).
func parentWithFiles(t *testing.T, names ...string) (*Session, *fakeLog, *fakeBlobs, *registry.Registry, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	s, log, blobs, reg := newPipeline(t)
	offs := map[string]int64{}
	for _, name := range names {
		if err := s.CommitCreate(ctx, name, 0644, 1000, 1000); err != nil {
			t.Fatal(err)
		}
		if err := s.CommitWrite(ctx, name, 0, []byte(strings.ToUpper(name)), true, 0); err != nil {
			t.Fatal(err)
		}
		offs[name] = s.coveredOffset()
	}
	return s, log, blobs, reg, offs
}

func has(u *upper.Upper, path string) bool {
	_, ok := u.Lookup(path)
	return ok
}

func TestBranchAtMidOffsetReplaysParentToThatPointOnly(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, offs := parentWithFiles(t, "a", "b", "c")

	child, err := Branch(ctx, parent.ID(), offs["b"], "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if !has(child.Upper(), "a") || !has(child.Upper(), "b") || has(child.Upper(), "c") {
		t.Fatalf("child at offset %d sees a=%v b=%v c=%v, want a,b only",
			offs["b"], has(child.Upper(), "a"), has(child.Upper(), "b"), has(child.Upper(), "c"))
	}
	if child.ID() == parent.ID() || child.LowerID() != "l" {
		t.Fatalf("child identity %s/%s", child.ID(), child.LowerID())
	}
	meta, err := reg.Load(ctx, child.ID())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Lineage == nil || meta.Lineage.ParentSessionId != parent.ID() || meta.Lineage.ParentOffset != offs["b"] {
		t.Fatalf("lineage = %+v, want parent %s at %d", meta.Lineage, parent.ID(), offs["b"])
	}

	// Parent writes after the branch stay invisible to the child, and the
	// child's writes stay invisible to the parent, on resume of either.
	if err := parent.CommitCreate(ctx, "d", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := child.CommitCreate(ctx, "x", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	parent2, err := OpenSession(ctx, parent.ID(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	child2, err := OpenSession(ctx, child.ID(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if has(child2.Upper(), "c") || has(child2.Upper(), "d") || !has(child2.Upper(), "x") {
		t.Fatalf("resumed child sees c=%v d=%v x=%v", has(child2.Upper(), "c"), has(child2.Upper(), "d"), has(child2.Upper(), "x"))
	}
	if has(parent2.Upper(), "x") || !has(parent2.Upper(), "d") {
		t.Fatalf("resumed parent sees x=%v d=%v", has(parent2.Upper(), "x"), has(parent2.Upper(), "d"))
	}
}

func TestBranchAtOffsetZeroYieldsEmptyChild(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, _ := parentWithFiles(t, "a")
	child, err := Branch(ctx, parent.ID(), 0, "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if has(child.Upper(), "a") {
		t.Fatal("branch at offset 0 must yield an empty child")
	}
	meta, _ := reg.Load(ctx, child.ID())
	if meta.Lineage.ParentOffset != 0 {
		t.Fatalf("lineage offset = %d, want 0", meta.Lineage.ParentOffset)
	}
}

func TestBranchAtTailUsesParentsCommittedTail(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, offs := parentWithFiles(t, "a", "b")
	child, err := Branch(ctx, parent.ID(), -1, "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if !has(child.Upper(), "a") || !has(child.Upper(), "b") {
		t.Fatal("tail branch must carry the whole parent state")
	}
	meta, _ := reg.Load(ctx, child.ID())
	if meta.Lineage.ParentOffset != offs["b"] {
		t.Fatalf("lineage offset = %d, want the parent's last record %d", meta.Lineage.ParentOffset, offs["b"])
	}
}

// The child's initial image is tagged in the child's own offset space (its
// SessionStart's offset), never the parent's branch offset. With a shared
// partition the two differ, so a wrong tag would make the child's resume
// either skip its own records or replay the parent's.
func TestBranchChildImageIsTaggedInChildOffsetSpace(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, offs := parentWithFiles(t, "a", "b")
	branchAt := offs["a"]
	child, err := Branch(ctx, parent.ID(), branchAt, "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	covers, img, err := reg.LatestStateImage(ctx, child.ID(), -1)
	if err != nil || img == nil {
		t.Fatalf("child image: covers %d, err %v", covers, err)
	}
	startEvents, _, err := log.ReadSession(ctx, child.ID(), 0)
	if err != nil || len(startEvents) != 1 || startEvents[0].GetSessionStart() == nil {
		t.Fatalf("child log = %v (%v), want a single SessionStart", startEvents, err)
	}
	wantCovers := log.tailOffset() - 1
	if covers != wantCovers {
		t.Fatalf("child image covers %d, want its SessionStart offset %d", covers, wantCovers)
	}
	if covers == branchAt {
		t.Fatalf("test setup: child offset %d collides with parent branch offset; cannot distinguish", covers)
	}
	if child.coveredOffset() != covers || child.savedOffset != covers {
		t.Fatalf("child covered/saved = %d/%d, want %d", child.coveredOffset(), child.savedOffset, covers)
	}
	if child.Upper().SeqLast() != parent.Upper().SeqLast()-2 || has(child.Upper(), "b") {
		t.Fatalf("child state = seq %d b=%v, want the parent as of %d", child.Upper().SeqLast(), has(child.Upper(), "b"), branchAt)
	}
	// The child commits from the seq its inherited state ends at; its first
	// checkpoint covers its own newest record.
	if err := child.CommitCreate(ctx, "x", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if off, err := child.Checkpoint(ctx); err != nil || off != log.tailOffset()-1 {
		t.Fatalf("child checkpoint = %d (%v), want %d", off, err, log.tailOffset()-1)
	}
	child2, err := OpenSession(ctx, child.ID(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if !has(child2.Upper(), "a") || has(child2.Upper(), "b") || !has(child2.Upper(), "x") {
		t.Fatalf("resumed child sees a=%v b=%v x=%v", has(child2.Upper(), "a"), has(child2.Upper(), "b"), has(child2.Upper(), "x"))
	}
}

// Branching from a checkpointed parent restores the image covering at most
// the target offset and replays only the remainder; an image newer than the
// target must not be used.
func TestBranchHonorsTargetOffsetAcrossImages(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, offs := parentWithFiles(t, "a")
	if _, err := parent.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := parent.CommitCreate(ctx, "b", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := parent.CommitWrite(ctx, "b", 0, []byte("B"), true, 0); err != nil {
		t.Fatal(err)
	}
	offs["b"] = parent.coveredOffset()
	if _, err := parent.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := parent.CommitCreate(ctx, "c", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}

	// Target between the two images: restore image(a), replay b.
	child, err := Branch(ctx, parent.ID(), offs["b"], "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if !has(child.Upper(), "a") || !has(child.Upper(), "b") || has(child.Upper(), "c") {
		t.Fatalf("child a=%v b=%v c=%v", has(child.Upper(), "a"), has(child.Upper(), "b"), has(child.Upper(), "c"))
	}
	// Target before the first image's coverage: nothing usable, full replay.
	child0, err := Branch(ctx, parent.ID(), offs["a"]-1, "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := child0.Upper().Lookup("a"); n == nil || len(n.Extents) != 0 {
		t.Fatalf("child before a's write: %+v, want created-but-empty a", n)
	}
}

func TestBranchRefusesUnusableParentImage(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, offs := parentWithFiles(t, "a")
	if err := reg.SaveStateImage(ctx, parent.ID(), offs["a"], []byte("not a state image")); err != nil {
		t.Fatal(err)
	}
	if _, err := Branch(ctx, parent.ID(), -1, "l", blobs, log, reg); err == nil || !strings.Contains(err.Error(), "decode state image") {
		t.Fatalf("branch over a corrupt image: err = %v, want decode failure", err)
	}
	if _, err := OpenSession(ctx, parent.ID(), "l", blobs, log, reg); err == nil || !strings.Contains(err.Error(), "decode state image") {
		t.Fatalf("resume over a corrupt image: err = %v, want decode failure", err)
	}
	// A target before the corrupt image never touches it.
	if _, err := Branch(ctx, parent.ID(), offs["a"]-1, "l", blobs, log, reg); err != nil {
		t.Fatalf("branch below the corrupt image: %v", err)
	}
}

func TestBranchRejectsLowerMismatchAndUnknownParent(t *testing.T) {
	ctx := context.Background()
	parent, log, blobs, reg, _ := parentWithFiles(t, "a")
	if _, err := Branch(ctx, parent.ID(), -1, "other-lower", blobs, log, reg); err == nil || !strings.Contains(err.Error(), "belongs to lower") {
		t.Fatalf("lower mismatch: %v", err)
	}
	if _, err := Branch(ctx, "sess_nope", -1, "l", blobs, log, reg); err == nil {
		t.Fatal("unknown parent must fail")
	}
	if n := log.len(); n != 3 {
		t.Fatalf("refused branches appended records: log has %d, want 3", n)
	}
}

// Branch and resume run the same reconstruction: a parent reconstructed to
// its tail by Branch matches a plain resume byte for byte.
func TestBranchAndResumeReconstructIdentically(t *testing.T) {
	ctx := context.Background()
	parent, log, _, reg, _ := parentWithFiles(t, "a", "b")
	if err := parent.CommitSetattr(ctx, &kfusev1.Setattr{Path: "lower-only", Mode: func() *uint32 { m := uint32(0o600); return &m }()}); err != nil {
		t.Fatal(err)
	}
	viaResume, err := reconstruct(ctx, reg, log, parent.ID(), -1)
	if err != nil {
		t.Fatal(err)
	}
	viaBranch, err := reconstruct(ctx, reg, log, parent.ID(), log.tailOffset()-1)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := viaResume.upper.Marshal()
	b, _ := viaBranch.upper.Marshal()
	if string(a) != string(b) || viaResume.covered != viaBranch.covered || viaResume.tail != viaBranch.tail {
		t.Fatalf("resume %+v vs branch %+v reconstructions differ", viaResume, viaBranch)
	}
	if viaResume.covered != parent.coveredOffset() {
		t.Fatalf("reconstructed covered %d, want live session's %d", viaResume.covered, parent.coveredOffset())
	}
}
