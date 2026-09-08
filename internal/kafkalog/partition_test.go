// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package kafkalog

import (
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
)

func testLog(t *testing.T, cfg config.Config) *Log {
	t.Helper()
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestNewConfiguresSASLAndTLS(t *testing.T) {
	l := testLog(t, config.Config{
		KafkaBrokers:      "b1:9092,b2:9092",
		KafkaSASLUsername: "user",
		KafkaSASLPassword: "pass",
		KafkaTLS:          true,
		KafkaTopic:        "t",
		KafkaPartitions:   4,
	})
	if got := l.brokers; len(got) != 2 || got[0] != "b1:9092" || got[1] != "b2:9092" {
		t.Errorf("brokers = %v, want the comma-separated list split", got)
	}
	net := l.saramaCfg.Net
	if !net.SASL.Enable || net.SASL.Mechanism != sarama.SASLTypePlaintext {
		t.Fatalf("SASL enable=%v mechanism=%q, want PLAIN enabled", net.SASL.Enable, net.SASL.Mechanism)
	}
	if net.SASL.User != "user" || net.SASL.Password != "pass" {
		t.Errorf("SASL creds = %q/%q, want user/pass", net.SASL.User, net.SASL.Password)
	}
	if !net.TLS.Enable || net.TLS.Config == nil {
		t.Fatal("KafkaTLS must enable TLS with a tls.Config")
	}
	if net.TLS.Config.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS MinVersion = %#x, want TLS 1.2", net.TLS.Config.MinVersion)
	}
}

func TestNewWithoutSASLLeavesMechanismUnset(t *testing.T) {
	l := testLog(t, config.Config{KafkaBrokers: "b:9092", KafkaPartitions: 1})
	if l.saramaCfg.Net.SASL.Enable {
		t.Error("SASL must stay disabled without a username")
	}
	if l.saramaCfg.Net.TLS.Enable || l.saramaCfg.Net.TLS.Config != nil {
		t.Error("TLS must stay disabled without KafkaTLS")
	}
}

func TestPartitionForIsStableAndInRange(t *testing.T) {
	l := testLog(t, config.Config{KafkaBrokers: "b:9092", KafkaPartitions: 8})
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("session-%d", i)
		p := l.PartitionFor(id)
		if p < 0 || p >= 8 {
			t.Fatalf("PartitionFor(%q) = %d, want [0,8)", id, p)
		}
		if again := l.PartitionFor(id); again != p {
			t.Fatalf("PartitionFor(%q) not stable: %d then %d", id, p, again)
		}
	}
}

// The configured partition count is only a fallback: once the real topic
// metadata is known it decides partition selection.
func TestPartitionCountPrefersRefreshedTopicCount(t *testing.T) {
	l := testLog(t, config.Config{KafkaBrokers: "b:9092", KafkaPartitions: 8})
	if got := l.partitionCount(); got != 8 {
		t.Fatalf("partitionCount before refresh = %d, want configured 8", got)
	}
	l.partitions = 3
	if got := l.partitionCount(); got != 3 {
		t.Fatalf("partitionCount after refresh = %d, want 3", got)
	}
	if p := l.PartitionFor("abc"); p < 0 || p >= 3 {
		t.Fatalf("PartitionFor = %d, want [0,3) after refresh", p)
	}
}

// Producer and consumer must agree: Append sets the partition explicitly to
// what PartitionFor computes, and the manual partitioner must honour it
// verbatim rather than re-hashing the key.
func TestManualPartitionerMatchesPartitionFor(t *testing.T) {
	l := testLog(t, config.Config{KafkaBrokers: "b:9092", KafkaTopic: "t", KafkaPartitions: 8})
	partitioner := l.saramaCfg.Producer.Partitioner("t")
	if !partitioner.RequiresConsistency() {
		t.Fatal("partitioner must be consistent for per-session ordering")
	}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("session-%d", i)
		want := int32(l.PartitionFor(id))
		msg := &sarama.ProducerMessage{Topic: "t", Partition: want, Key: sarama.StringEncoder(id)}
		got, err := partitioner.Partition(msg, 8)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("Partition(%q) = %d, PartitionFor = %d", id, got, want)
		}
	}
}

// The kf-type header values are an on-wire contract shared with perf records.
func TestOpNameCoversEveryOp(t *testing.T) {
	cases := []struct {
		want string
		ev   *kfusev1.EventEnvelope
	}{
		{"session_start", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_SessionStart{SessionStart: &kfusev1.SessionStart{}}}},
		{"create", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{}}}},
		{"write", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{}}}},
		{"unlink", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{}}}},
		{"mkdir", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{}}}},
		{"rmdir", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rmdir{Rmdir: &kfusev1.Rmdir{}}}},
		{"rename", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{}}}},
		{"symlink", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Symlink{Symlink: &kfusev1.Symlink{}}}},
		{"setattr", &kfusev1.EventEnvelope{Op: &kfusev1.EventEnvelope_Setattr{Setattr: &kfusev1.Setattr{}}}},
	}
	for _, c := range cases {
		if got := kfusev1.OpName(c.ev); got != c.want {
			t.Errorf("OpName(%T) = %q, want %q", c.ev.Op, got, c.want)
		}
	}
	if got := kfusev1.OpName(&kfusev1.EventEnvelope{}); got != "unknown" {
		t.Errorf("OpName(no op) = %q, want %q", got, "unknown")
	}
}

// Events travel through Kafka as protobuf; a round trip must preserve the
// envelope so replay reconstructs the same state.
func TestEventEnvelopeRoundTrip(t *testing.T) {
	ev := &kfusev1.EventEnvelope{
		Seq:        7,
		UnixMicros: 1234567,
		SessionId:  "sess",
		LowerId:    "lower",
		Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
			Path: "dir/file.txt", Offset: 4, Length: 3, BlobId: []byte("deadbeef"), Eof: true,
		}},
	}
	raw, err := proto.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	got := &kfusev1.EventEnvelope{}
	if err := proto.Unmarshal(raw, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(ev, got) {
		t.Fatalf("round trip changed the event:\n got %v\nwant %v", got, ev)
	}
}
