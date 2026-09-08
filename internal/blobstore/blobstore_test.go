// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/testenv"
)

func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	bs, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("hello kfuse blob store")
	id, err := bs.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if id != hex.EncodeToString(want[:]) {
		t.Fatalf("id %s != sha256 %s", id, hex.EncodeToString(want[:]))
	}
	got, err := bs.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("round trip mismatch: %q vs %q", got, data)
	}
}

func TestDedupSameKey(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	bs, _ := New(ctx, cfg)
	data := []byte("identical bytes dedup")
	id1, err := bs.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := bs.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("same content must produce same key: %s vs %s", id1, id2)
	}
	if _, err := bs.Get(ctx, id2); err != nil {
		t.Fatal(err)
	}
}

func TestGetMissingBlobErrors(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	bs, _ := New(ctx, cfg)
	_, err := bs.Get(ctx, fmt.Sprintf("%064x", 0))
	if err == nil {
		t.Fatal("missing blob must error")
	}
}
