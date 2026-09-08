// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"strings"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// TestCommitRejectsInvalidPathBeforeAppend guards against a bad path becoming
// durable: commit must fail before touching the (nil) log.
func TestCommitRejectsInvalidPathBeforeAppend(t *testing.T) {
	u := upper.New()
	s := NewWithState(registry.Session{ID: "fenced", LowerID: "l"}, u, nil, nil, nil)

	for _, p := range []string{"../x", "a//b", "/abs", "", "a\xffb"} {
		if err := s.CommitCreate(context.Background(), p, 0644, 1000, 1000); err == nil {
			t.Fatalf("CommitCreate(%q) must be rejected before append", p)
		} else if !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("CommitCreate(%q) = %v, want an invalid-path error", p, err)
		}
	}
}
