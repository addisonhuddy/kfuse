// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package s3util holds the S3 plumbing shared by the blob store and the
// registry: client construction from config and whole-object put/get.
package s3util

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/addisonhuddy/kfuse/internal/config"
)

const (
	// maxAttempts bounds how often a single S3 request is tried before its
	// error reaches the caller (and, for writes, a user-visible syscall).
	maxAttempts = 5
	// maxBackoff caps the delay between attempts.
	maxBackoff = 5 * time.Second
	// baseBackoff is the first retry delay; it doubles per attempt.
	baseBackoff = 100 * time.Millisecond
)

// NewClient builds an S3-compatible client from static credentials in cfg.
// Requests retry throttling, 5xx and transient network failures with
// exponential backoff: a blip must not surface as a failed write.
func NewClient(cfg config.Config) *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:      cfg.S3Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Retryer: func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = maxAttempts
				o.MaxBackoff = maxBackoff
			})
		},
	}, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		o.UsePathStyle = cfg.S3UsePathStyle
	})
}

// PutBytes writes data as a whole object at key.
func PutBytes(ctx context.Context, client *s3.Client, bucket, key string, data []byte) error {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	return err
}

// GetBytes reads the whole object at key.
func GetBytes(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	// The client retryer covers the request itself, but the body streams
	// after the response headers are in: a connection reset mid-download
	// lands here, not in the retryer, so re-issue the whole GET.
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepBackoff(ctx, attempt); err != nil {
				return nil, err
			}
		}
		out, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(out.Body)
		_ = out.Body.Close()
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("s3util: read %s body after %d attempts: %w", key, maxAttempts, lastErr)
}

// sleepBackoff waits out the delay before the given attempt, or returns the
// context error if the caller gave up first.
func sleepBackoff(ctx context.Context, attempt int) error {
	d := baseBackoff << (attempt - 2)
	if d > maxBackoff {
		d = maxBackoff
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Delete removes the object at key. A missing object is not an error.
func Delete(ctx context.Context, client *s3.Client, bucket, key string) error {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil && !IsNotFound(err) {
		return err
	}
	return nil
}

// IsNotFound reports whether err is an S3 "object absent" error. Only the
// typed API errors and a 404 response count: matching on message text would
// classify unrelated failures (network, permission) as "absent", and an absent
// lease reads as "unlocked".
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		case "NoSuchBucket":
			// Also answered with 404, but a misconfigured bucket is a failure,
			// not an absent object.
			return false
		}
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode() == http.StatusNotFound
	}
	return false
}
