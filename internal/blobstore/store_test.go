// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/s3fake"
)

const testBucket = "test-bucket"

func newTestStore(t *testing.T) (*BlobStore, *s3fake.Server) {
	t.Helper()
	fake := s3fake.New(t)
	return &BlobStore{bucket: testBucket, prefix: "kfuse/", client: fake.Client()}, fake
}

func TestNewUsesConfigBucketAndPrefix(t *testing.T) {
	b, err := New(context.Background(), config.Config{
		S3Bucket: "b", S3Prefix: "p/", S3Region: "us-east-1",
		S3AccessKey: "a", S3SecretKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.bucket != "b" || b.prefix != "p/" {
		t.Fatalf("New() bucket/prefix = %q/%q, want b/p/", b.bucket, b.prefix)
	}
}

// The storage key is content-addressed and fans out by the first two byte
// pairs of the digest, so a blob id fully determines its location.
func TestKeyLayout(t *testing.T) {
	b := &BlobStore{prefix: "kfuse/"}
	id := "abcdef0123456789" + strings.Repeat("0", 48)
	got, err := b.key(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := "kfuse/blobs/sha256/ab/cd/" + id; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

func TestPutReturnsSha256AndStoresAtKey(t *testing.T) {
	store, fake := newTestStore(t)
	data := []byte("hello blobs")
	sum := sha256.Sum256(data)
	wantID := hex.EncodeToString(sum[:])

	id, err := store.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if id != wantID {
		t.Fatalf("Put id = %q, want sha256 %q", id, wantID)
	}
	key, err := store.key(id)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := fake.Get(testBucket, key)
	if !ok {
		t.Fatalf("no object at %q; keys = %v", key, fake.Keys(testBucket))
	}
	if !bytes.Equal(stored, data) {
		t.Fatalf("stored bytes = %q, want %q", stored, data)
	}
}

// Identical content dedups: the same bytes always map to the same key.
func TestPutIsContentAddressed(t *testing.T) {
	store, fake := newTestStore(t)
	ctx := context.Background()
	first, err := store.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("ids differ for identical content: %q vs %q", first, second)
	}
	if keys := fake.Keys(testBucket); len(keys) != 1 {
		t.Fatalf("stored %d objects, want 1: %v", len(keys), keys)
	}
}

func TestPutReaderRoundTripsWithContentHashID(t *testing.T) {
	store, _ := newTestStore(t)
	data := []byte("streamed content")
	sum := sha256.Sum256(data)

	id, err := store.PutReader(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if id != hex.EncodeToString(sum[:]) {
		t.Fatalf("PutReader id = %q, want %q", id, hex.EncodeToString(sum[:]))
	}
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
}

func TestPutReportsUploadFailure(t *testing.T) {
	store, fake := newTestStore(t)
	fake.FailPuts(true)

	if _, err := store.Put(context.Background(), []byte("boom")); err == nil {
		t.Fatal("Put must fail when S3 rejects the upload")
	} else if !strings.Contains(err.Error(), "blobstore: put") {
		t.Fatalf("error = %v, want it to name the failing put", err)
	}
}

func TestGetMissingBlobFails(t *testing.T) {
	store, _ := newTestStore(t)
	missing := strings.Repeat("00", 32)

	if _, err := store.Get(context.Background(), missing); err == nil {
		t.Fatal("Get of an absent blob must fail")
	}
}

// Get verifies the digest: silently returning corrupted bytes would let bad
// data into the overlay.
func TestGetRejectsChecksumMismatch(t *testing.T) {
	store, fake := newTestStore(t)
	ctx := context.Background()
	id, err := store.Put(ctx, []byte("trustworthy"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.key(id)
	if err != nil {
		t.Fatal(err)
	}
	fake.Corrupt(testBucket, key, []byte("tampered"))

	if _, err := store.Get(ctx, id); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Get of corrupted blob = %v, want a checksum mismatch error", err)
	}
}

func TestPutEmptyBlob(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	id, err := store.Put(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("Get = %q, want empty", data)
	}
}
