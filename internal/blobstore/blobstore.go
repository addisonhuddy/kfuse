// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package blobstore stores content-addressed file bytes in S3 under
// blobs/sha256/{aa}/{bb}/{hex}. Identical content dedups for free.
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/perf"
	"github.com/addisonhuddy/kfuse/internal/s3util"
)

type BlobStore struct {
	bucket string
	prefix string
	client *s3.Client
}

func New(ctx context.Context, cfg config.Config) (*BlobStore, error) {
	return &BlobStore{
		bucket: cfg.S3Bucket,
		prefix: cfg.S3Prefix,
		client: s3util.NewClient(cfg),
	}, nil
}

// NewForTest builds a store against an explicit bucket/prefix (integration
// tests only).
func NewForTest(ctx context.Context, bucket, prefix string) *BlobStore {
	return &BlobStore{bucket: bucket, prefix: prefix, client: s3util.NewClient(config.Config{})}
}

// Put stores data and returns the hex sha256 blob id (the storage key).
func (b *BlobStore) Put(ctx context.Context, data []byte) (string, error) {
	return b.PutReader(ctx, bytes.NewReader(data), int64(len(data)))
}

// PutReader reads all of r into memory, hashes it, and uploads it as one S3
// object (the key is the content hash, so it cannot be known before the
// whole input is consumed). The returned id is the sha256 of the bytes, so
// identical content always maps to the same key. size is a capacity hint.
func (b *BlobStore) PutReader(ctx context.Context, r io.Reader, size int64) (string, error) {
	h := sha256.New()
	buf := bytes.NewBuffer(make([]byte, 0, size))
	if _, err := io.Copy(io.MultiWriter(h, buf), r); err != nil {
		return "", err
	}
	id := hex.EncodeToString(h.Sum(nil))
	key, err := b.key(id)
	if err != nil {
		return "", err
	}
	putStart := time.Now()
	if err := s3util.PutBytes(ctx, b.client, b.bucket, key, buf.Bytes()); err != nil {
		return "", fmt.Errorf("blobstore: put %s: %w", key, err)
	}
	if perf.Enabled() {
		perf.Emit("blob_put", perf.I64("ns", perf.Since(putStart)), perf.I64("bytes", size))
	}
	return id, nil
}

// Get fetches the full bytes of a blob by hex id.
func (b *BlobStore) Get(ctx context.Context, blobID string) ([]byte, error) {
	key, err := b.key(blobID)
	if err != nil {
		return nil, err
	}
	getStart := time.Now()
	data, err := s3util.GetBytes(ctx, b.client, b.bucket, key)
	if err != nil {
		return nil, fmt.Errorf("blobstore: get %s: %w", blobID, err)
	}
	if perf.Enabled() {
		perf.Emit("blob_get", perf.I64("ns", perf.Since(getStart)), perf.I64("bytes", int64(len(data))))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != blobID {
		return nil, fmt.Errorf("blobstore: checksum mismatch for %s", blobID)
	}
	return data, nil
}

// key maps a blob id to its object key. The id is validated first: it reaches
// here from the change log and from state images, and an unchecked value both
// panics on the slices below and escapes the blobs/ prefix ("../..").
func (b *BlobStore) key(blobID string) (string, error) {
	if err := config.ValidateBlobID(blobID); err != nil {
		return "", err
	}
	return fmt.Sprintf("%sblobs/sha256/%s/%s/%s", b.prefix, blobID[:2], blobID[2:4], blobID), nil
}
