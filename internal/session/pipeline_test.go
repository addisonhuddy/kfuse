// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/s3fake"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// newPipeline builds a real session over controllable log/blob fakes and an
// S3-fake-backed registry: the production commit and checkpoint code runs
// unmodified, only the transports are swapped.
func newPipeline(t *testing.T) (*Session, *fakeLog, *fakeBlobs, *registry.Registry) {
	t.Helper()
	fake := s3fake.New(t)
	reg := registry.NewWithClient(ckptBucket, "kfuse/", fake.Client())
	log, blobs := newFakeLog(), &fakeBlobs{}
	s, err := NewSession(context.Background(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	return s, log, blobs, reg
}

func TestNewSessionEmitsSessionStartFirst(t *testing.T) {
	s, log, _, reg := newPipeline(t)
	events, tail, err := log.ReadSession(context.Background(), s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].GetSessionStart() == nil || events[0].Seq != 1 {
		t.Fatalf("log after NewSession = %+v, want a single SessionStart at seq 1", events)
	}
	if tail.Offset != 1 || s.coveredOffset() != 0 {
		t.Fatalf("tail %d / covered %d, want 1 / 0", tail.Offset, s.coveredOffset())
	}
	if _, err := reg.Load(context.Background(), s.ID()); err != nil {
		t.Fatalf("session not registered: %v", err)
	}
}

// Blob upload failing must leave the log untouched and the upper unchanged:
// nothing may become durable that points at bytes that never landed.
func TestWriteBlobFailurePreventsAppend(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, _ := newPipeline(t)
	if err := s.CommitCreate(ctx, "a.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	before, appends := log.len(), log.appendCalls()
	blobs.setFail(true)

	if err := s.CommitWrite(ctx, "a.txt", 0, []byte("boom"), true, 0); !errors.Is(err, errFakePut) {
		t.Fatalf("CommitWrite = %v, want blob failure", err)
	}
	if err := s.CommitSparseWrite(ctx, "lower.txt", 0, []byte("boom"), false, 0644, 0, 0, true); !errors.Is(err, errFakePut) {
		t.Fatalf("CommitSparseWrite = %v, want blob failure", err)
	}
	if log.len() != before || log.appendCalls() != appends {
		t.Fatalf("log advanced despite blob failure: %d -> %d records, %d -> %d Append calls",
			before, log.len(), appends, log.appendCalls())
	}
	if n, _ := s.Upper().Lookup("a.txt"); len(n.Extents) != 0 {
		t.Fatalf("upper gained extents %+v from a failed write", n.Extents)
	}
	if _, ok := s.Upper().Lookup("lower.txt"); ok {
		t.Fatal("upper materialized lower.txt from a failed write")
	}

	blobs.setFail(false)
	if err := s.CommitWrite(ctx, "a.txt", 0, []byte("ok"), true, 0); err != nil {
		t.Fatalf("write after blob recovery: %v", err)
	}
	if log.len() != before+1 {
		t.Fatalf("log has %d records, want %d", log.len(), before+1)
	}
}

// Invalid operations are refused before any transport is touched: no blob
// upload, no Append call, no seq consumed.
func TestInvalidOperationsRejectedBeforeDurability(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, _ := newPipeline(t)
	seqBefore := s.seq
	cases := map[string]func() error{
		"create":       func() error { return s.CommitCreate(ctx, "../x", 0644, 0, 0) },
		"write":        func() error { return s.CommitWrite(ctx, "/abs", 0, []byte("x"), true, 0) },
		"sparse write": func() error { return s.CommitSparseWrite(ctx, "a//b", 0, []byte("x"), true, 0, 0, 0, true) },
		"unlink":       func() error { return s.CommitUnlink(ctx, "") },
		"mkdir":        func() error { return s.CommitMkdir(ctx, "a\xffb", 0755, 0, 0) },
		"rmdir":        func() error { return s.CommitRmdir(ctx, "./a") },
		"symlink":      func() error { return s.CommitSymlink(ctx, "..", "t", 0, 0) },
		"rename":       func() error { return s.CommitRename(ctx, "ok", "a/../b", false) },
		"setattr":      func() error { return s.CommitSetattr(ctx, &kfusev1.Setattr{Path: "/abs"}) },
	}
	for name, commit := range cases {
		if err := commit(); err == nil {
			t.Errorf("%s with an invalid path was accepted", name)
		}
	}
	if log.appendCalls() != 1 { // SessionStart only
		t.Fatalf("Append called %d times, want 1", log.appendCalls())
	}
	if s.seq != seqBefore {
		t.Fatalf("seq %d -> %d: a rejected commit consumed a seq", seqBefore, s.seq)
	}
	// Writes validate the path before uploading, so a bad path costs no S3 PUT.
	if blobs.putCalls() != 0 {
		t.Fatalf("blob Put called %d times for rejected writes, want 0", blobs.putCalls())
	}
}

// The upper must not change until the log has acknowledged the record. The
// gate holds the append open while the test inspects the upper; if Apply ever
// ran before Append returned, the node would be visible here.
func TestUpperChangesOnlyAfterAppendAck(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)

	inAppend := make(chan struct{})
	release := make(chan struct{})
	log.setGate(func(ev *kfusev1.EventEnvelope) bool {
		close(inAppend)
		<-release
		return true
	})
	done := make(chan error, 1)
	go func() { done <- s.CommitCreate(ctx, "a.txt", 0644, 0, 0) }()
	<-inAppend
	if _, ok := s.Upper().Lookup("a.txt"); ok {
		t.Fatal("a.txt visible in the upper before the log acknowledged it")
	}
	if s.Upper().SeqLast() != 1 {
		t.Fatalf("upper SeqLast = %d before ack, want 1", s.Upper().SeqLast())
	}
	log.setGate(nil)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Upper().Lookup("a.txt"); !ok {
		t.Fatal("a.txt missing from the upper after the ack")
	}
	if s.coveredOffset() != 1 {
		t.Fatalf("covered offset = %d, want the acked record's offset 1", s.coveredOffset())
	}
}

// A definitely failed append (nothing persisted) still fences the session:
// the adapter cannot distinguish "never sent" from "sent, response lost", so
// commit treats both as ambiguous and refuses to keep going on this object.
func TestFailedAppendLeavesUpperUnchangedAndFences(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)
	if err := s.CommitCreate(ctx, "a.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	log.setGate(func(*kfusev1.EventEnvelope) bool { return false })
	err := s.CommitCreate(ctx, "b.txt", 0644, 0, 0)
	if !errors.Is(err, ErrLogDiverged) || !errors.Is(err, errFakeAppend) {
		t.Fatalf("CommitCreate = %v, want ErrLogDiverged wrapping the transport error", err)
	}
	if _, ok := s.Upper().Lookup("b.txt"); ok {
		t.Fatal("b.txt applied although its append failed")
	}
	log.setGate(nil)

	for name, commit := range allCommits(ctx, s) {
		if err := commit(); !errors.Is(err, ErrLogDiverged) {
			t.Errorf("%s after failed append = %v, want ErrLogDiverged", name, err)
		}
	}
	if log.len() != 2 {
		t.Fatalf("log has %d records after fencing, want 2", log.len())
	}
	if _, err := s.Checkpoint(ctx); !errors.Is(err, ErrLogDiverged) {
		t.Fatalf("Checkpoint after failed append = %v, want ErrLogDiverged", err)
	}
}

// An ambiguous append (the record landed but the ack was lost) is the case that
// matters: the log now holds seq N that this upper never applied. A later image
// from this object would claim to cover offsets whose effects it lacks, and a
// later commit would reuse seq N. Both must be refused; a resume from the log
// settles the truth.
func TestAmbiguousAppendIsNotClaimedBySnapshotOrLaterCommit(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, reg := newPipeline(t)
	if err := s.CommitCreate(ctx, "a.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	log.setFailAfter(true)
	if err := s.CommitCreate(ctx, "b.txt", 0644, 0, 0); !errors.Is(err, ErrLogDiverged) {
		t.Fatalf("CommitCreate = %v, want ErrLogDiverged", err)
	}
	log.setFailAfter(false)
	if log.len() != 3 {
		t.Fatalf("log has %d records, want 3 (the ambiguous record persisted)", log.len())
	}
	if _, ok := s.Upper().Lookup("b.txt"); ok {
		t.Fatal("b.txt applied although the append reported failure")
	}

	if err := s.CommitCreate(ctx, "c.txt", 0644, 0, 0); !errors.Is(err, ErrLogDiverged) {
		t.Fatalf("commit after ambiguous append = %v, want ErrLogDiverged", err)
	}
	if off, err := s.Checkpoint(ctx); !errors.Is(err, ErrLogDiverged) {
		t.Fatalf("Checkpoint after ambiguous append = (%d, %v), want ErrLogDiverged", off, err)
	}
	if covers, _, err := reg.LatestStateImage(ctx, s.ID(), -1); err != nil || covers != 1 {
		t.Fatalf("newest image covers %d (err %v), want the pre-failure image at 1", covers, err)
	}

	// Resume reconciles from the log: b.txt is real, seq continues after it.
	resumed, err := OpenSession(ctx, s.ID(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resumed.Upper().Lookup("b.txt"); !ok {
		t.Fatal("resume dropped the record that landed during the ambiguous append")
	}
	if err := resumed.CommitCreate(ctx, "c.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	events, _, err := log.ReadSession(ctx, s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("record %d has seq %d, want dense seqs after resume", i, ev.Seq)
		}
	}
	if off, err := resumed.Checkpoint(ctx); err != nil || off != 3 {
		t.Fatalf("Checkpoint after resume = (%d, %v), want (3, nil)", off, err)
	}
}

func allCommits(ctx context.Context, s *Session) map[string]func() error {
	return map[string]func() error{
		"create":       func() error { return s.CommitCreate(ctx, "n.txt", 0o644, 0, 0) },
		"write":        func() error { return s.CommitWrite(ctx, "a.txt", 0, []byte("x"), true, 0) },
		"sparse write": func() error { return s.CommitSparseWrite(ctx, "lower.txt", 0, []byte("x"), false, 0o644, 0, 0, true) },
		"mkdir":        func() error { return s.CommitMkdir(ctx, "d", 0o755, 0, 0) },
		"unlink":       func() error { return s.CommitUnlink(ctx, "a.txt") },
		"rmdir":        func() error { return s.CommitRmdir(ctx, "d") },
		"symlink":      func() error { return s.CommitSymlink(ctx, "link", "a.txt", 0, 0) },
		"rename":       func() error { return s.CommitRename(ctx, "a.txt", "b.txt", false) },
		"setattr":      func() error { return s.CommitSetattr(ctx, &kfusev1.Setattr{Path: "a.txt"}) },
	}
}

// Every mutation entry point, including regular and sparse writes, is refused
// after lease loss without uploading a blob or touching the log.
func TestEveryMutationRefusedAfterLeaseLoss(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, _ := newPipeline(t)
	s.MarkLeaseLost()
	appends := log.appendCalls()
	for name, commit := range allCommits(ctx, s) {
		if err := commit(); !errors.Is(err, ErrLeaseLost) {
			t.Errorf("%s after lease loss = %v, want ErrLeaseLost", name, err)
		}
	}
	if log.appendCalls() != appends {
		t.Fatalf("Append called after lease loss")
	}
	if blobs.putCalls() != 0 {
		t.Fatalf("blob Put called %d times after lease loss, want 0", blobs.putCalls())
	}
}

// Resume through the dependency interfaces: image + tail replay reproduce the
// writer's upper, and the resumed session continues seq and offsets correctly.
func TestResumeReplaysImageAndTail(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, reg := newPipeline(t)
	if err := s.CommitMkdir(ctx, "d", 0755, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCreate(ctx, "d/a.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitWrite(ctx, "d/a.txt", 0, []byte("hello"), true, 0); err != nil {
		t.Fatal(err)
	}
	if off, err := s.Checkpoint(ctx); err != nil || off != 3 {
		t.Fatalf("Checkpoint = (%d, %v), want (3, nil)", off, err)
	}
	// Past the image: these must come from the log replay.
	if err := s.CommitUnlink(ctx, "gone-lower.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitSparseWrite(ctx, "lower.bin", 4096, []byte("XXXX"), false, 0644, 0, 0, true); err != nil {
		t.Fatal(err)
	}

	r, err := OpenSession(ctx, s.ID(), "l", blobs, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	if r.seq != s.seq || r.coveredOffset() != s.coveredOffset() {
		t.Fatalf("resumed seq/offset = %d/%d, want %d/%d", r.seq, r.coveredOffset(), s.seq, s.coveredOffset())
	}
	if r.savedOffset != 3 {
		t.Fatalf("resumed savedOffset = %d, want the image's 3", r.savedOffset)
	}
	want, _ := s.Upper().Marshal()
	got, _ := r.Upper().Marshal()
	if string(want) != string(got) {
		t.Fatalf("resumed upper differs from writer's upper\n got %s\nwant %s", got, want)
	}
	n, ok := r.Upper().Lookup("d/a.txt")
	if !ok || len(n.Extents) != 1 || string(n.Extents[0].BlobID) != blobIDFor([]byte("hello")) {
		t.Fatalf("d/a.txt after resume = %+v, want one extent pointing at the uploaded blob", n)
	}
	if !r.Upper().IsWhiteout("gone-lower.txt") {
		t.Fatal("whiteout lost across resume")
	}
	if sp, ok := r.Upper().Lookup("lower.bin"); !ok || sp.Size != -1 {
		t.Fatalf("sparse node after resume = %+v, want fallthrough (Size -1)", sp)
	}

	// The resumed writer keeps going where the old one stopped.
	if err := r.CommitCreate(ctx, "after.txt", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	if off, err := r.Checkpoint(ctx); err != nil || off != 6 {
		t.Fatalf("Checkpoint after resume = (%d, %v), want (6, nil)", off, err)
	}
	if got := imageSeq(t, r, 6); got != 7 {
		t.Fatalf("image at 6 has SeqLast %d, want 7", got)
	}
}

func TestOpenSessionRejectsLowerMismatch(t *testing.T) {
	ctx := context.Background()
	s, log, blobs, reg := newPipeline(t)
	if _, err := OpenSession(ctx, s.ID(), "other-lower", blobs, log, reg); err == nil {
		t.Fatal("OpenSession with the wrong lower must fail")
	}
	if _, err := OpenSession(ctx, s.ID(), "", blobs, log, reg); err != nil {
		t.Fatalf("OpenSession without a lower hint: %v", err)
	}
}

// Concurrent commits through the real pipeline must produce dense seqs, one
// record per commit, and an upper whose SeqLast matches the log.
func TestConcurrentCommitsSerializeSeq(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.CommitMkdir(ctx, "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), 0755, 0, 0); err != nil {
				t.Errorf("mkdir %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	events, tail, err := log.ReadSession(ctx, s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n+1 {
		t.Fatalf("%d records, want %d", len(events), n+1)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("record %d has seq %d, want dense", i, ev.Seq)
		}
	}
	if s.Upper().SeqLast() != uint64(n+1) || s.coveredOffset() != tail.Offset-1 {
		t.Fatalf("upper SeqLast %d / covered %d, want %d / %d", s.Upper().SeqLast(), s.coveredOffset(), n+1, tail.Offset-1)
	}
}

// A checkpoint that overlaps an in-flight commit tags its image with exactly
// the offsets whose effects the snapshot contains.
func TestCheckpointDuringInFlightCommitTagsOnlyAppliedState(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)
	advanceTo(t, s, 3)

	inAppend := make(chan struct{})
	release := make(chan struct{})
	log.setGate(func(*kfusev1.EventEnvelope) bool {
		close(inAppend)
		<-release
		return true
	})
	done := make(chan error, 1)
	go func() { done <- s.CommitCreate(ctx, "inflight.txt", 0644, 0, 0) }()
	<-inAppend
	log.setGate(nil)

	// The commit holds s.mu until the append returns, so Checkpoint blocks
	// behind it rather than snapshotting half a commit. Release, then check.
	ckpt := make(chan int64, 1)
	go func() {
		off, err := s.Checkpoint(ctx)
		if err != nil {
			t.Errorf("checkpoint: %v", err)
		}
		ckpt <- off
	}()
	select {
	case off := <-ckpt:
		t.Fatalf("checkpoint returned %d while a commit was in flight", off)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	off := <-ckpt
	if off != 4 {
		t.Fatalf("checkpoint offset = %d, want 4", off)
	}
	if got := imageSeq(t, s, off); got != 5 {
		t.Fatalf("image at %d has SeqLast %d, want 5", off, got)
	}
	restored, fromOffset, err := RestoreUpper(ctx, s.reg, s.ID(), -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.Lookup("inflight.txt"); !ok || fromOffset != 5 {
		t.Fatalf("image (resume from %d) lacks the committed in-flight record", fromOffset)
	}
}

// Racing mutations that pass the FUSE layer's merged-view check together are
// arbitrated by the session: the upper-state precondition is re-evaluated
// under the commit lock, so exactly one create of a name becomes durable and
// the loser is refused before it ever reaches the log. Without this, the
// second record would be appended and then rejected by Apply, leaving a log
// that replay cannot consume.
func TestRacingCreatesCommitExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.CommitCreate(ctx, "same.txt", 0644, 0, 0)
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, upper.ErrExists):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d creates succeeded, want exactly 1", winners)
	}
	if log.len() != 2 { // SessionStart + the one create
		t.Fatalf("log holds %d records, want 2", log.len())
	}
	if _, err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	// Replay must accept everything that was appended.
	if _, err := OpenSession(ctx, s.ID(), "", &fakeBlobs{}, log, s.reg); err != nil {
		t.Fatalf("resume after racing creates: %v", err)
	}
}

// Rejected preconditions also cover rmdir of a non-empty upper dir, unlink of
// a dir, and rename of a path that exists nowhere: none of them append.
func TestUpperPreconditionsRefuseBeforeAppend(t *testing.T) {
	ctx := context.Background()
	s, log, _, _ := newPipeline(t)
	if err := s.CommitMkdir(ctx, "d", 0755, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCreate(ctx, "d/f", 0644, 0, 0); err != nil {
		t.Fatal(err)
	}
	before := log.len()
	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{"rmdir non-empty", func() error { return s.CommitRmdir(ctx, "d") }, upper.ErrNotEmpty},
		{"unlink dir", func() error { return s.CommitUnlink(ctx, "d") }, upper.ErrIsDir},
		{"rmdir file", func() error { return s.CommitRmdir(ctx, "d/f") }, upper.ErrNotDir},
		{"mkdir over file", func() error { return s.CommitMkdir(ctx, "d/f", 0755, 0, 0) }, upper.ErrExists},
		{"rename nothing", func() error { return s.CommitRename(ctx, "ghost", "x", false) }, upper.ErrNoEntry},
	}
	for _, c := range cases {
		err := c.run()
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if log.len() != before {
		t.Fatalf("refused events were appended: %d records, want %d", log.len(), before)
	}
	if s.Upper().SeqLast() != uint64(before) {
		t.Fatalf("SeqLast %d, want %d", s.Upper().SeqLast(), before)
	}
}
