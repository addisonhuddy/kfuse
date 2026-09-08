// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package kafkalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

type scriptedReader struct {
	message record
	delay   time.Duration
	err     error
	calls   int
}

func (r *scriptedReader) FetchMessage(context.Context) (record, error) {
	r.calls++
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	if r.err != nil {
		return record{}, r.err
	}
	return r.message, nil
}

func TestReadSessionSlowFetchDoesNotSilentlyTruncate(t *testing.T) {
	session := "slow-fetch-session"
	event := &kfusev1.EventEnvelope{
		Seq:       1,
		SessionId: session,
		LowerId:   "test-lower",
		Op: &kfusev1.EventEnvelope_Create{
			Create: &kfusev1.Create{Path: "f", Mode: 0644},
		},
	}
	value, err := proto.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	reader := &scriptedReader{
		message: record{
			Key:    []byte(session),
			Value:  value,
			Offset: 0,
		},
		delay: 2100 * time.Millisecond,
	}
	got, last, err := readSessionFromReader(
		context.Background(),
		"topic",
		session,
		0,
		0,
		0,
		reader,
	)
	if err != nil {
		t.Fatalf("slow replay: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("slow replay returned %#v, want one event", got)
	}
	if last.Offset != 1 {
		t.Fatalf("last offset = %d, want 1", last.Offset)
	}
	if reader.calls != 1 {
		t.Fatalf("FetchMessage calls = %d, want 1", reader.calls)
	}

	reader = &scriptedReader{err: context.DeadlineExceeded}
	_, _, err = readSessionFromReader(
		context.Background(),
		"topic",
		session,
		0,
		0,
		0,
		reader,
	)
	if err == nil {
		t.Fatal("premature fetch deadline must return an error")
	}
	if !strings.Contains(err.Error(), "replay truncated") {
		t.Fatalf("error = %q, want replay truncated", err)
	}
}
