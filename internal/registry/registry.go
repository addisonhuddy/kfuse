// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package registry stores session metadata in S3 under meta/sessions/{id}.json.
package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/s3util"
)

type Session struct {
	ID        string           `json:"id"`
	LowerID   string           `json:"lower_id"`
	Lineage   *kfusev1.Lineage `json:"lineage,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}

type Registry struct {
	bucket string
	prefix string
	client *s3.Client
}

func New(ctx context.Context, cfg config.Config) (*Registry, error) {
	return &Registry{
		bucket: cfg.S3Bucket,
		prefix: cfg.S3Prefix,
		client: s3util.NewClient(cfg),
	}, nil
}

// NewWithClient builds a Registry around an existing S3 client (used by tests
// and tooling that talk to a non-default endpoint).
func NewWithClient(bucket, prefix string, client *s3.Client) *Registry {
	return &Registry{bucket: bucket, prefix: prefix, client: client}
}

func (r *Registry) sessionKey(id string) string {
	return fmt.Sprintf("%smeta/sessions/%s.json", r.prefix, id)
}

func (r *Registry) Create(ctx context.Context, s Session) error {
	if err := validateSessionIDs(s); err != nil {
		return err
	}
	key := r.sessionKey(s.ID)
	if err := r.putJSON(ctx, key, s); err != nil {
		return fmt.Errorf("registry: put %s: %w", key, err)
	}
	return nil
}

func (r *Registry) Load(ctx context.Context, id string) (*Session, error) {
	if err := config.ValidateID("session id", id); err != nil {
		return nil, err
	}
	key := r.sessionKey(id)
	var s Session
	found, err := r.getJSON(ctx, key, &s)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("registry: get %s: no such session", key)
	}
	if s.ID != id {
		return nil, fmt.Errorf("registry: id mismatch: stored %q asked %q", s.ID, id)
	}
	return &s, nil
}

// validateSessionIDs rejects ids that are unsafe in an S3 key or a local path.
func validateSessionIDs(s Session) error {
	if err := config.ValidateID("session id", s.ID); err != nil {
		return err
	}
	return config.ValidateID("lower id", s.LowerID)
}

// PutSession overwrites the session metadata (latest_committed_offset etc.).
func (r *Registry) PutSession(ctx context.Context, s Session) error {
	if err := validateSessionIDs(s); err != nil {
		return err
	}
	return r.putJSON(ctx, r.sessionKey(s.ID), s)
}

// ErrLocked is returned when a session's lease is held by another writer.
var ErrLocked = errors.New("registry: session locked")
