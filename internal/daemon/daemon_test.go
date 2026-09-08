// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package daemon

import (
	"context"
	"testing"

	"github.com/addisonhuddy/kfuse/internal/testenv"
)

func TestBranchAtMidOffset(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	stack, err := NewStack(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stack.NewSession(ctx, "test-lower")
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.CommitCreate(ctx, "a.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "a.txt", 0, []byte("A"), true, 0)
	_ = parent.CommitCreate(ctx, "b.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "b.txt", 0, []byte("B"), true, 0)

	// Branch at the offset covering A+B (the parent's tail).
	offB, err := stack.Log.TailOffset(ctx, parent.ID())
	if err != nil {
		t.Fatal(err)
	}
	child, err := stack.BranchSession(ctx, parent.ID(), offB-1, "test-lower")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.Upper().Lookup("a.txt"); !ok {
		t.Fatal("child missing a.txt from branch state")
	}
	if _, ok := child.Upper().Lookup("b.txt"); !ok {
		t.Fatal("child missing b.txt from branch state")
	}

	// Parent writes C after the branch; child must not see it.
	_ = parent.CommitCreate(ctx, "c.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "c.txt", 0, []byte("C"), true, 0)

	parentResumed, err := stack.OpenSession(ctx, parent.ID(), "test-lower")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parentResumed.Upper().Lookup("c.txt"); !ok {
		t.Fatal("parent missing c.txt")
	}
	if _, ok := child.Upper().Lookup("c.txt"); ok {
		t.Fatal("child must not see parent writes after the branch point")
	}
}

func TestBranchAtOffsetZero(t *testing.T) {
	ctx := context.Background()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	stack, err := NewStack(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stack.NewSession(ctx, "test-lower")
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.CommitCreate(ctx, "a.txt", 0644, 1000, 1000)
	_ = parent.CommitWrite(ctx, "a.txt", 0, []byte("A"), true, 0)

	child, err := stack.BranchSession(ctx, parent.ID(), 0, "test-lower")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.Upper().Lookup("a.txt"); ok {
		t.Fatal("branch at offset 0 must yield an empty child")
	}
}
