// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/addisonhuddy/kfuse/internal/config"
)

// BranchProvenance records parent -> child branch lineage.
type BranchProvenance struct {
	ParentID  string    `json:"parent_id"`
	ChildID   string    `json:"child_id"`
	Offset    int64     `json:"offset"`
	CreatedAt time.Time `json:"created_at"`
}

func (r *Registry) imageKey(sessionID string, coversOffset int64) string {
	return fmt.Sprintf("%sstate/sessions/%s/image-%019d.json", r.prefix, sessionID, coversOffset)
}

func (r *Registry) branchKey(parentID, childID string) string {
	return fmt.Sprintf("%smeta/branches/%s/%s.json", r.prefix, parentID, childID)
}

// SaveStateImage writes a serialized upper snapshot covering up to coversOffset.
func (r *Registry) SaveStateImage(ctx context.Context, sessionID string, coversOffset int64, data []byte) error {
	if err := config.ValidateID("session id", sessionID); err != nil {
		return err
	}
	key := r.imageKey(sessionID, coversOffset)
	if err := r.putBytes(ctx, key, data); err != nil {
		return fmt.Errorf("registry: save state image %s: %w", key, err)
	}
	return nil
}

// LatestStateImage finds the newest state image with coversOffset <= maxOffset.
// Pass maxOffset < 0 to return the absolute newest image. Returns coversOffset = -1
// if no matching image exists.
func (r *Registry) LatestStateImage(ctx context.Context, sessionID string, maxOffset int64) (int64, []byte, error) {
	if err := config.ValidateID("session id", sessionID); err != nil {
		return -1, nil, err
	}
	prefix := fmt.Sprintf("%sstate/sessions/%s/image-", r.prefix, sessionID)
	out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return -1, nil, fmt.Errorf("registry: list state images: %w", err)
	}
	var bestKey string
	var bestOffset int64 = -1
	for _, obj := range out.Contents {
		base := filepath.Base(aws.ToString(obj.Key))
		// filename is image-{offset}.json
		trimmed := strings.TrimPrefix(base, "image-")
		trimmed = strings.TrimSuffix(trimmed, ".json")
		off, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			continue
		}
		if maxOffset >= 0 && off > maxOffset {
			continue
		}
		if off > bestOffset {
			bestOffset = off
			bestKey = aws.ToString(obj.Key)
		}
	}
	if bestKey == "" {
		return -1, nil, nil
	}
	data, err := r.getBytes(ctx, bestKey)
	if err != nil {
		return -1, nil, err
	}
	if data == nil {
		return -1, nil, nil
	}
	return bestOffset, data, nil
}

// SaveBranch records branch provenance under meta/branches/{parent}/{child}.json.
func (r *Registry) SaveBranch(ctx context.Context, parentID, childID string, offset int64) error {
	if err := config.ValidateID("parent session id", parentID); err != nil {
		return err
	}
	if err := config.ValidateID("child session id", childID); err != nil {
		return err
	}
	b := BranchProvenance{
		ParentID:  parentID,
		ChildID:   childID,
		Offset:    offset,
		CreatedAt: time.Now().UTC(),
	}
	key := r.branchKey(parentID, childID)
	if err := r.putJSON(ctx, key, b); err != nil {
		return fmt.Errorf("registry: save branch %s: %w", key, err)
	}
	return nil
}
