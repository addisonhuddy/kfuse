// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/addisonhuddy/kfuse/internal/config"
)

// LowerSession is the per-session marker stored under a lower's index prefix.
type LowerSession struct {
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

// legacyLowerKey is the old mutable JSON-array index. It is still read (and
// merged into ListByLower results) so indexes written before the per-session
// key layout keep resolving.
func (r *Registry) legacyLowerKey(lowerID string) string {
	return fmt.Sprintf("%smeta/lowers/%s.json", r.prefix, lowerID)
}

func (r *Registry) lowerSessionsPrefix(lowerID string) string {
	return fmt.Sprintf("%smeta/lowers/%s/sessions/", r.prefix, lowerID)
}

func (r *Registry) lowerSessionKey(lowerID, sessionID string) string {
	return r.lowerSessionsPrefix(lowerID) + sessionID + ".json"
}

// ListByLower returns the session IDs recorded for a lower id, sorted
// lexically (nil if none). It merges the per-session markers with any legacy
// JSON-array index.
func (r *Registry) ListByLower(ctx context.Context, lowerID string) ([]string, error) {
	if err := config.ValidateID("lower id", lowerID); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	prefix := r.lowerSessionsPrefix(lowerID)
	paginator := s3.NewListObjectsV2Paginator(r.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("registry: list lower %s sessions: %w", lowerID, err)
		}
		for _, obj := range page.Contents {
			base := path.Base(aws.ToString(obj.Key))
			id, ok := strings.CutSuffix(base, ".json")
			if !ok || id == "" {
				continue
			}
			seen[id] = true
		}
	}
	var legacy []string
	if _, err := r.getJSON(ctx, r.legacyLowerKey(lowerID), &legacy); err != nil {
		return nil, err
	}
	for _, id := range legacy {
		seen[id] = true
	}
	if len(seen) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// AppendLowerSession records sessionID under lowerID's index. Each session is
// its own object, so the write is a single idempotent PutObject and concurrent
// calls for the same lower cannot lose each other's updates.
func (r *Registry) AppendLowerSession(ctx context.Context, lowerID, sessionID string) error {
	if err := config.ValidateID("lower id", lowerID); err != nil {
		return err
	}
	if err := config.ValidateID("session id", sessionID); err != nil {
		return err
	}
	key := r.lowerSessionKey(lowerID, sessionID)
	marker := LowerSession{SessionID: sessionID, CreatedAt: time.Now().UTC()}
	if err := r.putJSON(ctx, key, marker); err != nil {
		return fmt.Errorf("registry: put lower %s: %w", lowerID, err)
	}
	return nil
}
