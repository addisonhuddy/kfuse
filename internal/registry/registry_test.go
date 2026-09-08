// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/addisonhuddy/kfuse/internal/testenv"
)

func TestCreateLoadSession(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	r, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%032x", time.Now().UnixNano())
	s := Session{ID: id, LowerID: "lower-1", CreatedAt: time.Now().UTC()}
	if err := r.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.LowerID != "lower-1" {
		t.Fatalf("bad session: %+v", got)
	}
}

func TestLoadMissingSession(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	r, _ := New(ctx, cfg)
	if _, err := r.Load(ctx, "nonexistent-session-0001"); err == nil {
		t.Fatal("missing session must error")
	}
}

func TestLeaseClaimRenewRelease(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	r, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("lease-%032x", time.Now().UnixNano())
	if err := r.ClaimLease(ctx, id, "tok-A", "host-1"); err != nil {
		t.Fatal(err)
	}
	// Same token may renew.
	if err := r.RenewLease(ctx, id, "tok-A", "host-1"); err != nil {
		t.Fatal(err)
	}
	// A different token must fail while the lease is live.
	if err := r.ClaimLease(ctx, id, "tok-B", "host-2"); err == nil {
		t.Fatal("second claim must fail with lock held")
	} else if !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	// Release by the owner frees it.
	if err := r.ReleaseLease(ctx, id, "tok-A"); err != nil {
		t.Fatal(err)
	}
	if err := r.ClaimLease(ctx, id, "tok-B", "host-2"); err != nil {
		t.Fatalf("claim after release must succeed: %v", err)
	}
	_ = r.ReleaseLease(ctx, id, "tok-B")
}

func TestLeaseExpiredAllowsClaim(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	r, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("leaseexp-%032x", time.Now().UnixNano())
	// Write a lease that is already past its TTL, then a fresh claim succeeds.
	key := r.leaseKey(id)
	data := fmt.Sprintf(`{"token":"old","host":"h","acquired_at":"%s","ttl":1}`,
		time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339))
	if _, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.bucket), Key: aws.String(key),
		Body: bytes.NewReader([]byte(data)), ContentLength: aws.Int64(int64(len(data))),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.ClaimLease(ctx, id, "tok-new", "host-3"); err != nil {
		t.Fatalf("claim over expired lease must succeed: %v", err)
	}
	_ = r.ReleaseLease(ctx, id, "tok-new")
}
