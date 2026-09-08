// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"google.golang.org/protobuf/proto"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
)

var errFakeAppend = errors.New("fakelog: append failed")

type fakeRecord struct {
	offset int64
	value  []byte
}

// fakeLog is an in-memory EventLog: one partition, offsets dense from 0,
// records stored as marshalled protobuf so replay decodes fresh envelopes
// exactly like the Kafka adapter. Test hooks let a caller hold an append open
// (to observe ordering) or fail it (definite or ambiguous outcome).
type fakeLog struct {
	mu      sync.Mutex
	records []fakeRecord
	appends int

	// gate, when non-nil, is called with the record before it is durable; it
	// may block. Returning false makes the append fail without persisting.
	// Returning true then failAfter makes it persist but report an error
	// (the ambiguous outcome a lost produce response gives).
	gate      func(ev *kfusev1.EventEnvelope) bool
	failAfter bool
}

func newFakeLog() *fakeLog { return &fakeLog{} }

func (f *fakeLog) Append(ctx context.Context, ev *kfusev1.EventEnvelope) (kafkalog.KafkaOffset, error) {
	f.mu.Lock()
	gate, failAfter := f.gate, f.failAfter
	f.appends++
	f.mu.Unlock()
	if gate != nil && !gate(ev) {
		return kafkalog.KafkaOffset{}, errFakeAppend
	}
	value, err := proto.Marshal(ev)
	if err != nil {
		return kafkalog.KafkaOffset{}, err
	}
	f.mu.Lock()
	off := int64(len(f.records))
	f.records = append(f.records, fakeRecord{offset: off, value: value})
	f.mu.Unlock()
	if failAfter {
		return kafkalog.KafkaOffset{}, errFakeAppend
	}
	return kafkalog.KafkaOffset{Topic: "fake", Offset: off}, nil
}

func (f *fakeLog) ReadSession(ctx context.Context, sessionID string, from int64) ([]*kfusev1.EventEnvelope, kafkalog.KafkaOffset, error) {
	return f.ReadSessionTo(ctx, sessionID, from, -1)
}

func (f *fakeLog) ReadSessionTo(ctx context.Context, sessionID string, from, toOffset int64) ([]*kfusev1.EventEnvelope, kafkalog.KafkaOffset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	last := kafkalog.KafkaOffset{Topic: "fake", Offset: from}
	var out []*kfusev1.EventEnvelope
	for _, r := range f.records {
		if r.offset < from || (toOffset >= 0 && r.offset > toOffset) {
			continue
		}
		ev := &kfusev1.EventEnvelope{}
		if err := proto.Unmarshal(r.value, ev); err != nil {
			return nil, last, err
		}
		if ev.SessionId != sessionID {
			continue
		}
		out = append(out, ev)
		last.Offset = r.offset + 1
	}
	return out, last, nil
}

// setGate installs (or clears, with nil) the append hook.
func (f *fakeLog) setGate(g func(ev *kfusev1.EventEnvelope) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = g
}

// setFailAfter makes appends persist but report failure.
func (f *fakeLog) setFailAfter(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAfter = v
}

func (f *fakeLog) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *fakeLog) appendCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appends
}

// tailOffset mirrors kafkalog.Log.TailOffset: the offset after the last record.
func (f *fakeLog) tailOffset() int64 { return int64(f.len()) }

// fakeBlobs is an in-memory BlobPutter with a failure switch.
type fakeBlobs struct {
	mu   sync.Mutex
	puts int
	fail bool
}

var errFakePut = errors.New("fakeblobs: put failed")

func (b *fakeBlobs) Put(ctx context.Context, data []byte) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	if b.fail {
		return "", errFakePut
	}
	return blobIDFor(data), nil
}

func blobIDFor(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (b *fakeBlobs) setFail(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = v
}

func (b *fakeBlobs) putCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.puts
}
