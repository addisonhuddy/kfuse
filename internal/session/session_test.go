// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/blobstore"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/testenv"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

func newSessionStack(t *testing.T, prefix string) (*blobstore.BlobStore, *kafkalog.Log, *registry.Registry) {
	t.Helper()
	cfg := testenv.Require(t, prefix)
	ctx := context.Background()
	blobs, err := blobstore.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	log, err := kafkalog.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.EnsureTopic(ctx); err != nil {
		t.Fatal(err)
	}
	if err := log.RefreshPartitions(ctx); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return blobs, log, reg
}

func TestCommitPipelineAndResume(t *testing.T) {
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	blobs, log, reg := newSessionStack(t, prefix)

	s, err := NewSession(ctx, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCreate(ctx, "a.txt", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWrite(ctx, "a.txt", 0, []byte("hello world"), true, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitUnlink(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCreate(ctx, "b.txt", 0600, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWrite(ctx, "b.txt", 0, []byte("resume me"), true, 0); err != nil {
		t.Fatal(err)
	}

	// Resume: fresh upper, replay from Kafka, lazy blob fetch on read.
	s2, err := OpenSession(ctx, s.ID(), "", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Upper().Lookup("a.txt"); ok {
		t.Fatal("a.txt should be unlinked")
	}
	n, ok := s2.Upper().Lookup("b.txt")
	if !ok || n.Kind != upper.KindFile {
		t.Fatal("b.txt should exist after replay")
	}
	ext := n.Extents[0]
	data, err := blobs.Get(ctx, string(ext.BlobID))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resume me" {
		t.Fatalf("resumed bytes: %q", data)
	}
}

func TestCommitDirsAndResume(t *testing.T) {
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	blobs, log, reg := newSessionStack(t, prefix)

	s, err := NewSession(ctx, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitMkdir(ctx, "work", 0o755, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCreate(ctx, "work/a.txt", 0644, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWrite(ctx, "work/a.txt", 0, []byte("dir content"), true, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitRmdir(ctx, "lower-gone"); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenSession(ctx, s.ID(), "", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s2.Upper().Lookup("work")
	if !ok || d.Kind != upper.KindDir {
		t.Fatal("dir missing after resume")
	}
	n, ok := s2.Upper().Lookup("work/a.txt")
	if !ok || n.Kind != upper.KindFile {
		t.Fatal("file under dir missing after resume")
	}
	if !s2.Upper().IsWhiteout("lower-gone") {
		t.Fatal("lower-dir whiteout missing after resume")
	}
}

func TestCommitS3BeforeKafka(t *testing.T) {
	// blob-before-log: a failed S3 put must never produce a Kafka record.
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	_, log, reg := newSessionStack(t, prefix)
	// Use a bucket-less store to force Put failure.
	bad := blobstore.NewForTest(ctx, "no-such-bucket-"+fmt.Sprint(time.Now().UnixNano()), "test/x/")
	s, err := NewSession(ctx, "test-lower", bad, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	before, err := log.TailOffset(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	err = s.CommitWrite(ctx, "a.txt", 0, []byte("boom"), true, 0)
	if err == nil {
		t.Fatal("write to missing bucket must fail")
	}
	after, err := log.TailOffset(ctx, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("Kafka advanced despite S3 failure: %d -> %d", before, after)
	}
}

func TestBranchSessionReconstructsState(t *testing.T) {
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	blobs, log, reg := newSessionStack(t, prefix)

	parent, err := NewSession(ctx, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.CommitCreate(ctx, "file-1.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "file-1.txt", 0, []byte("one"), true, 0)
	_ = parent.CommitCreate(ctx, "file-2.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "file-2.txt", 0, []byte("two"), true, 0)

	// Save parent state snapshot to child
	snapshot, err := parent.Upper().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := NewID()
	if err := reg.SaveStateImage(ctx, childID, 0, snapshot); err != nil {
		t.Fatal(err)
	}
	childMeta := registry.Session{
		ID:        childID,
		LowerID:   "test-lower",
		CreatedAt: time.Now().UTC(),
	}
	if err := reg.Create(ctx, childMeta); err != nil {
		t.Fatal(err)
	}

	// Open child: loads snapshot image, sees parent files
	child, err := OpenSession(ctx, childID, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.Upper().Lookup("file-1.txt"); !ok {
		t.Fatal("child missing file-1 from branch state")
	}
	if _, ok := child.Upper().Lookup("file-2.txt"); !ok {
		t.Fatal("child missing file-2 from branch state")
	}

	// Child writes file-3; parent should not have it
	_ = child.CommitCreate(ctx, "file-3.txt", 0644, 1000, 1000)
	_ = child.CommitWrite(ctx, "file-3.txt", 0, []byte("three"), true, 0)

	parentResumed, _ := OpenSession(ctx, parent.ID(), "test-lower", blobs, log, reg)
	if _, ok := parentResumed.Upper().Lookup("file-3.txt"); ok {
		t.Fatal("parent must not see child writes")
	}
}

func TestCheckpointAndResumeFromImage(t *testing.T) {
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	blobs, log, reg := newSessionStack(t, prefix)

	s, err := NewSession(ctx, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.CommitCreate(ctx, "data.txt", 0644, 1000, 1000)
	_ = s.CommitWrite(ctx, "data.txt", 0, []byte("checkpointed data"), true, 0)

	// Explicit checkpoint flushes state image to S3
	offset, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if offset < 0 {
		t.Fatalf("bad checkpoint offset: %d", offset)
	}

	// Write more data after checkpoint
	_ = s.CommitCreate(ctx, "after.txt", 0644, 1000, 1000)
	_ = s.CommitWrite(ctx, "after.txt", 0, []byte("after checkpoint"), true, 0)

	// Resume: should load state image (covering offset), then replay only the tail event
	resumed, err := OpenSession(ctx, s.ID(), "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resumed.Upper().Lookup("data.txt"); !ok {
		t.Fatal("data.txt missing after resume from image")
	}
	if _, ok := resumed.Upper().Lookup("after.txt"); !ok {
		t.Fatal("after.txt missing after tail replay")
	}
}

func TestMaterializerFlushesEveryNEvents(t *testing.T) {
	ctx := context.Background()
	prefix := "test/" + testenv.RunID() + "/"
	blobs, log, reg := newSessionStack(t, prefix)

	s, err := NewSession(ctx, "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	// Cross the checkpointEveryEvents threshold so the async materializer
	// flushes a state image without an explicit Checkpoint call.
	for i := 0; i < 120; i++ {
		if err := s.CommitCreate(ctx, fmt.Sprintf("f%03d.txt", i), 0644, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		off, _, err := reg.LatestStateImage(ctx, s.ID(), -1)
		if err == nil && off >= 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("materializer did not flush a state image within 30s")
		}
		time.Sleep(200 * time.Millisecond)
	}
	resumed, err := OpenSession(ctx, s.ID(), "test-lower", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resumed.Upper().Lookup("f000.txt"); !ok {
		t.Fatal("f000.txt missing after resume from image")
	}
	if _, ok := resumed.Upper().Lookup("f119.txt"); !ok {
		t.Fatal("f119.txt missing after resume")
	}
}
