// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package kafkalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/testenv"
)

func newTestLog(t *testing.T) *Log {
	t.Helper()
	cfg := testenv.Require(t, "test/"+testenv.RunID()+"/")
	l, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.EnsureTopic(ctx); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}
	if err := l.RefreshPartitions(ctx); err != nil {
		t.Fatalf("refresh partitions: %v", err)
	}
	return l
}

func sid() string { return fmt.Sprintf("%032x", time.Now().UnixNano()) }

func event(seq uint64, sessionID string, op any) *kfusev1.EventEnvelope {
	e := &kfusev1.EventEnvelope{Seq: seq, SessionId: sessionID, LowerId: "test-lower"}
	switch o := op.(type) {
	case *kfusev1.SessionStart:
		e.Op = &kfusev1.EventEnvelope_SessionStart{SessionStart: o}
	case *kfusev1.Create:
		e.Op = &kfusev1.EventEnvelope_Create{Create: o}
	case *kfusev1.Write:
		e.Op = &kfusev1.EventEnvelope_Write{Write: o}
	case *kfusev1.Unlink:
		e.Op = &kfusev1.EventEnvelope_Unlink{Unlink: o}
	}
	return e
}

func TestAppendThenReadInOrder(t *testing.T) {
	l := newTestLog(t)
	ctx := context.Background()
	session := sid()
	events := []*kfusev1.EventEnvelope{
		event(1, session, &kfusev1.SessionStart{LowerId: "test-lower"}),
		event(2, session, &kfusev1.Create{Path: "a.txt", Mode: 0644}),
		event(3, session, &kfusev1.Write{Path: "a.txt", Offset: 0, Length: 5, BlobId: []byte("deadbeef"), Eof: true}),
	}
	var lastOff int64
	for _, ev := range events {
		off, err := l.Append(ctx, ev)
		if err != nil {
			t.Fatal(err)
		}
		lastOff = off.Offset
	}
	got, last, err := l.ReadSession(ctx, session, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	for i, ev := range got {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("seq out of order at %d: %d", i, ev.Seq)
		}
		if ev.SessionId != session {
			t.Fatalf("wrong session at %d", i)
		}
	}
	if last.Offset != lastOff+1 {
		t.Fatalf("last offset want %d, got %d", lastOff+1, last.Offset)
	}
}

func TestAppendReturnsOffset(t *testing.T) {
	l := newTestLog(t)
	ctx := context.Background()
	session := sid()
	off, err := l.Append(ctx, event(1, session, &kfusev1.Create{Path: "f", Mode: 0644}))
	if err != nil {
		t.Fatal(err)
	}
	if off.Partition != int32(l.PartitionFor(session)) {
		t.Fatalf("partition %d != expected %d", off.Partition, l.PartitionFor(session))
	}
	if off.Offset < 0 {
		t.Fatalf("bad offset %d", off.Offset)
	}
	got, _, err := l.ReadSession(ctx, session, off.Offset)
	if err != nil || len(got) != 1 {
		t.Fatalf("resume from offset failed: %d events, err %v", len(got), err)
	}
}

func TestAppendReturnsOwnOffsetWhenKeyIsInterleaved(t *testing.T) {
	l := newTestLog(t)
	ctx := context.Background()
	a := sid()
	b := sid()
	for l.PartitionFor(a) != l.PartitionFor(b) {
		b = sid()
	}
	if _, err := l.Append(ctx, event(1, a, &kfusev1.Create{Path: "a-1", Mode: 0644})); err != nil {
		t.Fatal(err)
	}
	bOffset, err := l.Append(ctx, event(1, b, &kfusev1.Create{Path: "b-1", Mode: 0644}))
	if err != nil {
		t.Fatal(err)
	}
	aOffset, err := l.Append(ctx, event(2, a, &kfusev1.Create{Path: "a-2", Mode: 0644}))
	if err != nil {
		t.Fatal(err)
	}
	if aOffset.Partition != bOffset.Partition {
		t.Fatalf("partitions differ: A=%d B=%d", aOffset.Partition, bOffset.Partition)
	}
	if aOffset.Offset != bOffset.Offset+1 {
		t.Fatalf("A offset = %d, B offset = %d; want adjacent records", aOffset.Offset, bOffset.Offset)
	}
}

func TestKeyIsolation(t *testing.T) {
	l := newTestLog(t)
	ctx := context.Background()
	a, b := sid(), sid()
	if _, err := l.Append(ctx, event(1, a, &kfusev1.Create{Path: "a.txt", Mode: 0644})); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, event(1, b, &kfusev1.Create{Path: "b.txt", Mode: 0644})); err != nil {
		t.Fatal(err)
	}
	aLast, err := l.Append(ctx, event(2, a, &kfusev1.Unlink{Path: "a.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	got, last, err := l.ReadSession(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("session A should see only its 2 events, got %d", len(got))
	}
	for _, ev := range got {
		if ev.SessionId != a {
			t.Fatalf("leak: got event for %s while reading %s", ev.SessionId, a)
		}
	}
	if last.Offset != aLast.Offset+1 {
		t.Fatalf("last offset = %d, want %d after the last matching record", last.Offset, aLast.Offset+1)
	}
}

func TestReadSessionFromOffset(t *testing.T) {
	l := newTestLog(t)
	ctx := context.Background()
	session := sid()
	var offsets []int64
	for i := uint64(1); i <= 5; i++ {
		off, err := l.Append(ctx, event(i, session, &kfusev1.Create{Path: fmt.Sprintf("f%d", i), Mode: 0644}))
		if err != nil {
			t.Fatal(err)
		}
		offsets = append(offsets, off.Offset)
	}
	// Resume from the offset of the third event: partition offsets include
	// other sessions' records, so only offsets returned by Append are valid.
	got, _, err := l.ReadSession(ctx, session, offsets[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events after offset %d, got %d", offsets[2], len(got))
	}
	if got[0].Seq != 3 {
		t.Fatalf("first event after offset should be seq 3, got %d", got[0].Seq)
	}
}
