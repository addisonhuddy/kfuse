// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/addisonhuddy/kfuse/internal/s3util"
)

// putJSON marshals v and writes it at key.
func (r *Registry) putJSON(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.putBytes(ctx, key, data)
}

// putBytes writes data at key.
func (r *Registry) putBytes(ctx context.Context, key string, data []byte) error {
	return s3util.PutBytes(ctx, r.client, r.bucket, key, data)
}

// getJSON decodes the object at key into v. It reports whether the object
// exists; a missing object is not an error.
func (r *Registry) getJSON(ctx context.Context, key string, v any) (bool, error) {
	data, err := r.getBytes(ctx, key)
	if err != nil || data == nil {
		return false, err
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		return false, err
	}
	return true, nil
}

// getBytes reads the object at key, returning nil bytes when it is absent.
func (r *Registry) getBytes(ctx context.Context, key string) ([]byte, error) {
	data, err := s3util.GetBytes(ctx, r.client, r.bucket, key)
	if err != nil {
		if s3util.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: get %s: %w", key, err)
	}
	return data, nil
}
