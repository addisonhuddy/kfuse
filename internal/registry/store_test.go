// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/s3fake"
)

const testBucket = "test-bucket"

func newTestRegistry(t *testing.T) (*Registry, *s3fake.Server) {
	t.Helper()
	fake := s3fake.New(t)
	return &Registry{bucket: testBucket, prefix: "kfuse/", client: fake.Client()}, fake
}

func TestNewUsesConfigBucketAndPrefix(t *testing.T) {
	r, err := New(context.Background(), config.Config{
		S3Bucket: "b", S3Prefix: "p/", S3Region: "us-east-1",
		S3AccessKey: "a", S3SecretKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.bucket != "b" || r.prefix != "p/" {
		t.Fatalf("New() bucket/prefix = %q/%q, want b/p/", r.bucket, r.prefix)
	}
}

func TestCreateAndLoadRoundTrip(t *testing.T) {
	r, fake := newTestRegistry(t)
	ctx := context.Background()
	want := Session{
		ID:        "sess-1",
		LowerID:   "lower-1",
		Lineage:   &kfusev1.Lineage{ParentSessionId: "parent", ParentOffset: 42},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := r.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.Get(testBucket, "kfuse/meta/sessions/sess-1.json"); !ok {
		t.Fatalf("metadata not at the documented key; keys = %v", fake.Keys(testBucket))
	}
	got, err := r.Load(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.LowerID != want.LowerID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	if got.Lineage.GetParentSessionId() != "parent" || got.Lineage.GetParentOffset() != 42 {
		t.Fatalf("lineage = %+v, want parent/42", got.Lineage)
	}
}

func TestCreateRequiresIDAndLowerID(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	for _, s := range []Session{{LowerID: "l"}, {ID: "s"}, {}} {
		if err := r.Create(ctx, s); err == nil {
			t.Fatalf("Create(%+v) must fail", s)
		}
	}
}

func TestLoadMissingSessionFails(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.Load(context.Background(), "nope"); err == nil {
		t.Fatal("Load of an unknown session must fail")
	}
}

// A stored document whose id does not match the requested one means the key
// space is corrupted; Load must refuse it rather than return the wrong session.
func TestLoadRejectsIDMismatch(t *testing.T) {
	r, fake := newTestRegistry(t)
	data, err := json.Marshal(Session{ID: "other", LowerID: "l"})
	if err != nil {
		t.Fatal(err)
	}
	fake.Put(testBucket, "kfuse/meta/sessions/asked.json", data)

	if _, err := r.Load(context.Background(), "asked"); err == nil || !strings.Contains(err.Error(), "id mismatch") {
		t.Fatalf("Load = %v, want an id mismatch error", err)
	}
}

func TestPutSessionOverwrites(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.Create(ctx, Session{ID: "s", LowerID: "l1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.PutSession(ctx, Session{ID: "s", LowerID: "l2"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if got.LowerID != "l2" {
		t.Fatalf("LowerID = %q, want the overwritten l2", got.LowerID)
	}
}

func TestListByLowerUnknownLowerIsEmpty(t *testing.T) {
	r, _ := newTestRegistry(t)
	ids, err := r.ListByLower(context.Background(), "never-seen")
	if err != nil {
		t.Fatalf("ListByLower of an unknown lower must not error: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v, want nil", ids)
	}
}

func TestAppendLowerSessionIsIdempotent(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2", "s1"} {
		if err := r.AppendLowerSession(ctx, "lower", id); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := r.ListByLower(ctx, "lower")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s2" {
		t.Fatalf("ids = %v, want [s1 s2] recorded once each", ids)
	}
}

func TestAppendLowerSessionUsesPerSessionKeys(t *testing.T) {
	r, fake := newTestRegistry(t)
	ctx := context.Background()
	if err := r.AppendLowerSession(ctx, "lower", "sess-1"); err != nil {
		t.Fatal(err)
	}
	raw, ok := fake.Get(testBucket, "kfuse/meta/lowers/lower/sessions/sess-1.json")
	if !ok {
		t.Fatalf("marker not at the per-session key; keys = %v", fake.Keys(testBucket))
	}
	var m LowerSession
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "sess-1" || m.CreatedAt.IsZero() {
		t.Fatalf("marker = %+v, want session id and created_at set", m)
	}
}

func TestAppendLowerSessionValidatesIDs(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.AppendLowerSession(ctx, "", "s"); err == nil {
		t.Fatal("empty lower id must fail")
	}
	if err := r.AppendLowerSession(ctx, "l", ""); err == nil {
		t.Fatal("empty session id must fail")
	}
	if err := r.AppendLowerSession(ctx, "l", "a/b"); err == nil {
		t.Fatal("session id with a separator must fail")
	}
}

// The whole point of the per-session key layout: concurrent appends for the
// same lower must never lose or duplicate entries.
func TestAppendLowerSessionConcurrent(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n*2)
	for i := 0; i < n; i++ {
		wg.Add(2)
		id := fmt.Sprintf("sess-%03d", i)
		// Two goroutines per id also exercise idempotency under contention.
		for j := 0; j < 2; j++ {
			go func() {
				defer wg.Done()
				errs <- r.AppendLowerSession(ctx, "lower", id)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids, err := r.ListByLower(ctx, "lower")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != n {
		t.Fatalf("got %d ids, want %d (no lost or duplicated appends)", len(ids), n)
	}
	for i, id := range ids {
		if want := fmt.Sprintf("sess-%03d", i); id != want {
			t.Fatalf("ids[%d] = %q, want %q", i, id, want)
		}
	}
}

// Indexes written by the old mutable JSON array layout must still resolve,
// merged and deduplicated with per-session markers.
func TestListByLowerMergesLegacyIndex(t *testing.T) {
	r, fake := newTestRegistry(t)
	ctx := context.Background()
	legacy, err := json.Marshal([]string{"old-1", "shared"})
	if err != nil {
		t.Fatal(err)
	}
	fake.Put(testBucket, "kfuse/meta/lowers/lower.json", legacy)
	for _, id := range []string{"new-1", "shared"} {
		if err := r.AppendLowerSession(ctx, "lower", id); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := r.ListByLower(ctx, "lower")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new-1", "old-1", "shared"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

// Session markers of one lower must not leak into another lower whose id is a
// prefix of the first (e.g. "low" vs "lower").
func TestListByLowerScopedToExactLowerID(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.AppendLowerSession(ctx, "lower", "s-lower"); err != nil {
		t.Fatal(err)
	}
	if err := r.AppendLowerSession(ctx, "low", "s-low"); err != nil {
		t.Fatal(err)
	}
	ids, err := r.ListByLower(ctx, "low")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "s-low" {
		t.Fatalf("ids = %v, want only [s-low]", ids)
	}
}

func TestSaveStateImageKeyIsOffsetSortable(t *testing.T) {
	r, fake := newTestRegistry(t)
	ctx := context.Background()
	for _, off := range []int64{2, 10} {
		if err := r.SaveStateImage(ctx, "s", off, []byte(fmt.Sprintf("image-%d", off))); err != nil {
			t.Fatal(err)
		}
	}
	keys := fake.Keys(testBucket)
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want two images", keys)
	}
	// Zero-padded offsets keep lexical order equal to numeric order.
	if !strings.Contains(keys[0], "image-0000000000000000002") || !strings.Contains(keys[1], "image-0000000000000000010") {
		t.Fatalf("keys = %v, want zero-padded offsets sorting numerically", keys)
	}
}

func TestLatestStateImagePicksHighestOffset(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	for _, off := range []int64{0, 5, 120} {
		if err := r.SaveStateImage(ctx, "s", off, []byte(fmt.Sprintf("at-%d", off))); err != nil {
			t.Fatal(err)
		}
	}
	off, data, err := r.LatestStateImage(ctx, "s", -1)
	if err != nil {
		t.Fatal(err)
	}
	if off != 120 || string(data) != "at-120" {
		t.Fatalf("LatestStateImage = (%d, %q), want (120, at-120)", off, data)
	}
}

// Branching reads the newest image no newer than the branch point, so images
// past maxOffset must be ignored.
func TestLatestStateImageHonoursMaxOffset(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	for _, off := range []int64{1, 7, 50} {
		if err := r.SaveStateImage(ctx, "s", off, []byte(fmt.Sprintf("at-%d", off))); err != nil {
			t.Fatal(err)
		}
	}
	off, data, err := r.LatestStateImage(ctx, "s", 20)
	if err != nil {
		t.Fatal(err)
	}
	if off != 7 || string(data) != "at-7" {
		t.Fatalf("LatestStateImage(max 20) = (%d, %q), want (7, at-7)", off, data)
	}
}

func TestLatestStateImageNoneReturnsMinusOne(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	off, data, err := r.LatestStateImage(ctx, "s", -1)
	if err != nil {
		t.Fatal(err)
	}
	if off != -1 || data != nil {
		t.Fatalf("LatestStateImage with no images = (%d, %v), want (-1, nil)", off, data)
	}

	// A non-numeric image name is skipped instead of aborting the scan.
	fake := s3fake.New(t)
	r2 := &Registry{bucket: testBucket, prefix: "kfuse/", client: fake.Client()}
	fake.Put(testBucket, "kfuse/state/sessions/s/image-garbage.json", []byte("x"))
	off, _, err = r2.LatestStateImage(ctx, "s", -1)
	if err != nil {
		t.Fatal(err)
	}
	if off != -1 {
		t.Fatalf("offset = %d, want -1 when only unparsable images exist", off)
	}
}

func TestSaveBranchRecordsProvenance(t *testing.T) {
	r, fake := newTestRegistry(t)
	if err := r.SaveBranch(context.Background(), "parent", "child", 9); err != nil {
		t.Fatal(err)
	}
	raw, ok := fake.Get(testBucket, "kfuse/meta/branches/parent/child.json")
	if !ok {
		t.Fatalf("branch record missing; keys = %v", fake.Keys(testBucket))
	}
	var b BranchProvenance
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.ParentID != "parent" || b.ChildID != "child" || b.Offset != 9 {
		t.Fatalf("provenance = %+v, want parent/child at 9", b)
	}
	if b.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestClaimLeaseRejectsForeignHolder(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.ClaimLease(ctx, "s", "token-a", "host-a"); err != nil {
		t.Fatal(err)
	}
	err := r.ClaimLease(ctx, "s", "token-b", "host-b")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second claim = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), "host-a") {
		t.Errorf("error %q should name the current holder", err)
	}
}

func TestClaimLeaseIsReentrantForSameToken(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.ClaimLease(ctx, "s", "token", "host"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClaimLease(ctx, "s", "token", "host"); err != nil {
		t.Fatalf("re-claiming with the same token must succeed: %v", err)
	}
}

// An expired lease is up for grabs: a crashed writer must not block the
// session forever.
func TestClaimLeaseTakesOverExpiredLease(t *testing.T) {
	r, fake := newTestRegistry(t)
	ctx := context.Background()
	stale := Lease{
		Token:      "old",
		Host:       "dead-host",
		AcquiredAt: time.Now().UTC().Add(-2 * LeaseTTL),
		TTL:        int64(LeaseTTL.Seconds()),
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	fake.Put(testBucket, "kfuse/meta/sessions/s.lock", data)

	if err := r.ClaimLease(ctx, "s", "new", "live-host"); err != nil {
		t.Fatalf("claim over an expired lease = %v, want success", err)
	}
	cur, err := r.loadLease(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Token != "new" || cur.Host != "live-host" {
		t.Fatalf("lease = %+v, want the new holder", cur)
	}
}

func TestRenewLeaseDetectsTakeover(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.ClaimLease(ctx, "s", "token-a", "host-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.RenewLease(ctx, "s", "token-a", "host-a"); err != nil {
		t.Fatalf("renewing own lease = %v, want success", err)
	}
	// Another writer stole it (e.g. after a partition healed).
	if err := r.putLease(ctx, "s", "token-b", "host-b"); err != nil {
		t.Fatal(err)
	}
	if err := r.RenewLease(ctx, "s", "token-a", "host-a"); !errors.Is(err, ErrLocked) {
		t.Fatalf("renew after takeover = %v, want ErrLocked", err)
	}
}

func TestRenewLeaseOnMissingLeaseRecreatesIt(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.RenewLease(ctx, "s", "token", "host"); err != nil {
		t.Fatalf("RenewLease with no lease present = %v, want it to write one", err)
	}
	cur, err := r.loadLease(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.Token != "token" {
		t.Fatalf("lease = %+v, want token", cur)
	}
}

func TestReleaseLeaseOnlyRemovesOwnLease(t *testing.T) {
	r, _ := newTestRegistry(t)
	ctx := context.Background()
	if err := r.ClaimLease(ctx, "s", "token-a", "host-a"); err != nil {
		t.Fatal(err)
	}
	// A different writer's release is a no-op.
	if err := r.ReleaseLease(ctx, "s", "token-b"); err != nil {
		t.Fatal(err)
	}
	cur, err := r.loadLease(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.Token != "token-a" {
		t.Fatalf("lease = %+v, want token-a still held", cur)
	}
	if err := r.ReleaseLease(ctx, "s", "token-a"); err != nil {
		t.Fatal(err)
	}
	cur, err = r.loadLease(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cur != nil {
		t.Fatalf("lease = %+v, want it removed", cur)
	}
}

func TestReleaseLeaseWithoutLeaseSucceeds(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.ReleaseLease(context.Background(), "s", "token"); err != nil {
		t.Fatalf("releasing an absent lease = %v, want nil", err)
	}
}

func TestLeaseExpiry(t *testing.T) {
	fresh := Lease{AcquiredAt: time.Now().UTC(), TTL: int64(LeaseTTL.Seconds())}
	if fresh.expired() {
		t.Error("a just-acquired lease must not be expired")
	}
	old := Lease{AcquiredAt: time.Now().UTC().Add(-LeaseTTL - time.Second), TTL: int64(LeaseTTL.Seconds())}
	if !old.expired() {
		t.Error("a lease past its TTL must be expired")
	}
}
