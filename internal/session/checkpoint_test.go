// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/s3fake"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

const ckptBucket = "test-bucket"

// newCheckpointSession builds a real session over the in-memory log, blob
// store and S3-fake-backed registry. SessionStart lands at offset 0 with seq 1,
// and every later commit through this single-session log advances both by one,
// so a state image's SeqLast is always covered offset + 1: that lets each test
// match an image's content against the offset it is tagged with.
func newCheckpointSession(t *testing.T) (*Session, *s3fake.Server, *fakeLog) {
	t.Helper()
	fake := s3fake.New(t)
	reg := registry.NewWithClient(ckptBucket, "kfuse/", fake.Client())
	log := newFakeLog()
	s, err := NewSession(context.Background(), "l", &fakeBlobs{}, log, reg)
	if err != nil {
		t.Fatal(err)
	}
	return s, fake, log
}

var advanceN atomic.Uint64

// advance commits one Create through the production pipeline.
func advance(t *testing.T, s *Session) {
	t.Helper()
	n := advanceN.Add(1)
	if err := s.CommitCreate(context.Background(), fmt.Sprintf("f%06d.txt", n), 0644, 0, 0); err != nil {
		t.Errorf("commit: %v", err)
	}
}

// advanceTo commits until the session's newest offset is off.
func advanceTo(t *testing.T, s *Session, off int64) {
	t.Helper()
	for s.coveredOffset() < off {
		advance(t, s)
	}
}

func (s *Session) coveredOffset() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastOffset
}

func imageSeq(t *testing.T, s *Session, off int64) uint64 {
	t.Helper()
	covers, data, err := s.reg.LatestStateImage(context.Background(), s.ID(), off)
	if err != nil {
		t.Fatal(err)
	}
	if covers != off {
		t.Fatalf("newest image <= %d covers %d", off, covers)
	}
	restored, err := upper.UnmarshalState(data)
	if err != nil {
		t.Fatal(err)
	}
	return restored.SeqLast()
}

func imageOffsets(t *testing.T, fake *s3fake.Server) []int64 {
	t.Helper()
	var offsets []int64
	for _, key := range fake.Keys(ckptBucket) {
		base := path.Base(key)
		if !strings.HasPrefix(base, "image-") {
			continue
		}
		off, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(base, "image-"), ".json"), 10, 64)
		if err != nil {
			t.Fatalf("unparsable image key %q: %v", key, err)
		}
		offsets = append(offsets, off)
	}
	return offsets
}

func TestCheckpointTagsImageWithItsOwnState(t *testing.T) {
	ctx := context.Background()
	s, fake, log := newCheckpointSession(t)
	advanceTo(t, s, 7)

	off, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if off != 7 {
		t.Fatalf("Checkpoint offset = %d, want 7", off)
	}
	if off != log.tailOffset()-1 {
		t.Fatalf("Checkpoint offset = %d, log tail = %d", off, log.tailOffset())
	}
	if got := imageSeq(t, s, 7); got != 8 {
		t.Fatalf("image state SeqLast = %d, want 8 (tag and content must match)", got)
	}
	if got := imageOffsets(t, fake); len(got) != 1 {
		t.Fatalf("image offsets = %v, want exactly one image", got)
	}
}

// A checkpoint must never claim to cover a record that is not in its snapshot:
// with nothing durable there is no image to write, since an image tagged 0
// would make resume skip the session's first record.
func TestCheckpointWithNothingDurableWritesNoImage(t *testing.T) {
	fake := s3fake.New(t)
	reg := registry.NewWithClient(ckptBucket, "kfuse/", fake.Client())
	s := NewWithState(registry.Session{ID: "sess", LowerID: "l"}, upper.New(), nil, nil, reg)
	// The unreachable broker and cancelled context assert that Checkpoint must
	// not consult Kafka at all when this session has no known offset.
	log, err := kafkalog.New(config.Config{
		KafkaBrokers:    "127.0.0.1:1",
		KafkaPartitions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.log = log
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	off, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if off != -1 {
		t.Fatalf("Checkpoint offset = %d, want -1", off)
	}
	if keys := fake.Keys(ckptBucket); len(keys) != 0 {
		t.Fatalf("keys = %v, want no state image", keys)
	}
}

func TestCheckpointSkipsRedundantUpload(t *testing.T) {
	ctx := context.Background()
	s, fake, _ := newCheckpointSession(t)
	advanceTo(t, s, 3)
	if _, err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	fake.FailPuts(true)
	off, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("second checkpoint at the same offset must not re-upload: %v", err)
	}
	if off != 3 {
		t.Fatalf("Checkpoint offset = %d, want 3", off)
	}
	fake.FailPuts(false)
	advance(t, s)
	if off, err = s.Checkpoint(ctx); err != nil || off != 4 {
		t.Fatalf("Checkpoint after new record = (%d, %v), want (4, nil)", off, err)
	}
	if got := imageOffsets(t, fake); len(got) != 2 {
		t.Fatalf("image offsets = %v, want one image per covered offset", got)
	}
}

// A commit that lands while a checkpoint is uploading belongs to the next
// image, not the one in flight, and a second checkpoint must not upload
// concurrently (out-of-order PUTs would leave older state as the newest image).
func TestCommitDuringUploadStaysOutOfTheUploadingImage(t *testing.T) {
	ctx := context.Background()
	s, fake, _ := newCheckpointSession(t)
	advanceTo(t, s, 2)

	uploading := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fake.BeforePut(func(_, _ string, _ []byte) {
		once.Do(func() { close(uploading) })
		<-release
	})

	first := make(chan int64, 1)
	go func() {
		off, err := s.Checkpoint(ctx)
		if err != nil {
			t.Errorf("first checkpoint: %v", err)
		}
		first <- off
	}()
	<-uploading

	advance(t, s) // a commit lands mid-upload: offset 3
	second := make(chan int64, 1)
	go func() {
		off, err := s.Checkpoint(ctx)
		if err != nil {
			t.Errorf("second checkpoint: %v", err)
		}
		second <- off
	}()
	select {
	case off := <-second:
		t.Fatalf("second checkpoint uploaded offset %d while the first was in flight", off)
	case <-time.After(200 * time.Millisecond):
	}

	fake.BeforePut(nil)
	close(release)
	if off := <-first; off != 2 {
		t.Fatalf("first checkpoint offset = %d, want 2", off)
	}
	if off := <-second; off != 3 {
		t.Fatalf("second checkpoint offset = %d, want 3", off)
	}
	for _, want := range []int64{2, 3} {
		if got := imageSeq(t, s, want); int64(got) != want+1 {
			t.Fatalf("image at offset %d holds state through seq %d", want, got)
		}
	}
}

// Commits landing while a checkpoint uploads must not end up inside an image
// tagged with an earlier offset, and overlapping checkpoints must not leave a
// stale snapshot as the newest image.
func TestConcurrentCommitsAndCheckpointsKeepImagesConsistent(t *testing.T) {
	ctx := context.Background()
	s, fake, _ := newCheckpointSession(t)

	const rounds = 100
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			advance(t, s)
		}()
		go func() {
			defer wg.Done()
			if _, err := s.Checkpoint(ctx); err != nil {
				t.Errorf("Checkpoint: %v", err)
			}
		}()
	}
	wg.Wait()

	offsets := imageOffsets(t, fake)
	if len(offsets) == 0 {
		t.Fatal("no state image written")
	}
	for _, off := range offsets {
		// seq == offset+1 by construction, so a snapshot covering more (or
		// less) state than its tag shows up as a mismatch here.
		if got := imageSeq(t, s, off); int64(got) != off+1 {
			t.Fatalf("image at offset %d holds state through seq %d", off, got)
		}
	}
	newest, _, err := s.reg.LatestStateImage(ctx, s.ID(), -1)
	if err != nil {
		t.Fatal(err)
	}
	if newest != offsets[len(offsets)-1] {
		t.Fatalf("newest image = %d, want the highest written offset %d", newest, offsets[len(offsets)-1])
	}
}
