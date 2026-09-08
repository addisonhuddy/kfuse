// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package s3util

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/addisonhuddy/kfuse/internal/s3fake"
)

// A body that dies mid-download happens after the response headers, so the
// SDK retryer never sees it: GetBytes has to re-issue the GET itself.
func TestGetBytesRetriesTruncatedBody(t *testing.T) {
	srv := s3fake.New(t)
	want := bytes.Repeat([]byte("payload"), 512)
	srv.Put("bucket", "blobs/a", want)
	srv.TruncateGets(1)

	got, err := GetBytes(context.Background(), srv.Client(), "bucket", "blobs/a")
	if err != nil {
		t.Fatalf("GetBytes: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("GetBytes returned %d bytes, want %d", len(got), len(want))
	}
}

// Retries are bounded: a body that never completes must surface an error
// rather than looping forever.
func TestGetBytesGivesUpAfterMaxAttempts(t *testing.T) {
	srv := s3fake.New(t)
	srv.Put("bucket", "blobs/a", bytes.Repeat([]byte("payload"), 512))
	srv.TruncateGets(maxAttempts + 1)

	if _, err := GetBytes(context.Background(), srv.Client(), "bucket", "blobs/a"); err == nil {
		t.Fatal("GetBytes succeeded, want an error after exhausting attempts")
	}
}

func TestGetBytesStopsOnContextCancel(t *testing.T) {
	srv := s3fake.New(t)
	srv.Put("bucket", "blobs/a", bytes.Repeat([]byte("payload"), 512))
	srv.TruncateGets(maxAttempts + 1)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := GetBytes(ctx, srv.Client(), "bucket", "blobs/a"); err == nil {
		t.Fatal("GetBytes succeeded, want the context error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("GetBytes kept retrying for %s after the context expired", elapsed)
	}
}
