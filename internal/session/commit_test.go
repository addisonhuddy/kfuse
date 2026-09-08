// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"strings"
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

func TestNewIDIsRandom32Hex(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 32 {
			t.Fatalf("NewID() = %q, want 32 hex chars", id)
		}
		if strings.TrimLeft(id, "0123456789abcdef") != "" {
			t.Fatalf("NewID() = %q, want lowercase hex", id)
		}
		if seen[id] {
			t.Fatalf("NewID() repeated %q", id)
		}
		seen[id] = true
	}
}

func TestNewWithStateAdoptsUpperSeq(t *testing.T) {
	u := upper.New()
	if err := u.Apply(&kfusev1.EventEnvelope{
		Seq: 4,
		Op:  &kfusev1.EventEnvelope_SessionStart{SessionStart: &kfusev1.SessionStart{LowerId: "l"}},
	}); err != nil {
		t.Fatal(err)
	}
	s := NewWithState(registry.Session{ID: "sess", LowerID: "l"}, u, nil, nil, nil)
	if s.ID() != "sess" || s.LowerID() != "l" {
		t.Fatalf("ID/LowerID = %q/%q, want sess/l", s.ID(), s.LowerID())
	}
	if s.Upper() != u {
		t.Fatal("Upper() must return the state it was built from")
	}
	if s.seq != 4 {
		t.Fatalf("seq = %d, want the upper's last seq 4", s.seq)
	}
}

// Path validation runs before the Kafka append so a malformed path can never
// become durable and poison replay.
func TestValidateEventPathsRejectsBadPaths(t *testing.T) {
	bad := []string{"", "/abs", "a//b", "./a", "a/../b", "..", "a\xffb"}
	for _, p := range bad {
		events := map[string]*kfusev1.EventEnvelope{
			"create":  {Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: p}}},
			"write":   {Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{Path: p}}},
			"unlink":  {Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: p}}},
			"rmdir":   {Op: &kfusev1.EventEnvelope_Rmdir{Rmdir: &kfusev1.Rmdir{Path: p}}},
			"mkdir":   {Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{Path: p}}},
			"symlink": {Op: &kfusev1.EventEnvelope_Symlink{Symlink: &kfusev1.Symlink{Path: p}}},
			"setattr": {Op: &kfusev1.EventEnvelope_Setattr{Setattr: &kfusev1.Setattr{Path: p}}},
			"rename from": {Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{
				From: p, To: "ok.txt",
			}}},
			"rename to": {Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{
				From: "ok.txt", To: p,
			}}},
		}
		for name, ev := range events {
			if err := validateEventPaths(ev); err == nil {
				t.Errorf("validateEventPaths(%s, %q) = nil, want an error", name, p)
			}
		}
	}
}

func TestValidateEventPathsAcceptsGoodPaths(t *testing.T) {
	good := []*kfusev1.EventEnvelope{
		{Op: &kfusev1.EventEnvelope_SessionStart{SessionStart: &kfusev1.SessionStart{LowerId: "l"}}},
		{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "a.txt"}}},
		{Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{Path: "dir/a.txt"}}},
		{Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{From: "a.txt", To: "dir/b.txt"}}},
		{Op: &kfusev1.EventEnvelope_Symlink{Symlink: &kfusev1.Symlink{Path: "link", Target: "../outside"}}},
	}
	for _, ev := range good {
		if err := validateEventPaths(ev); err != nil {
			t.Errorf("validateEventPaths(%T) = %v, want nil", ev.Op, err)
		}
	}
}

func TestValidateEventPathsRejectsUnknownOp(t *testing.T) {
	if err := validateEventPaths(&kfusev1.EventEnvelope{}); err == nil {
		t.Fatal("an event with no op must be rejected")
	}
}

// Rejection happens before sequencing, so a refused commit must not consume a
// seq number (which would leave a permanent gap in the log).
func TestRejectedCommitDoesNotConsumeSeq(t *testing.T) {
	s := NewWithState(registry.Session{ID: "sess", LowerID: "l"}, upper.New(), nil, nil, nil)
	before := s.seq
	if err := s.CommitCreate(context.Background(), "../escape", 0o644, 0, 0); err == nil {
		t.Fatal("invalid path must be rejected")
	}
	if s.seq != before {
		t.Fatalf("seq = %d, want it unchanged at %d", s.seq, before)
	}
}
