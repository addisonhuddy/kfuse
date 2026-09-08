// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package kafkalog

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/IBM/sarama"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
)

// mockCluster starts an in-process broker that answers metadata for topic t
// with one partition, so a Log can connect without a real cluster.
func mockCluster(t *testing.T) *sarama.MockBroker {
	t.Helper()
	broker := sarama.NewMockBroker(t, 1)
	broker.SetHandlerByMap(map[string]sarama.MockResponse{
		"MetadataRequest": sarama.NewMockMetadataResponse(t).
			SetBroker(broker.Addr(), broker.BrokerID()).
			SetLeader("t", 0, broker.BrokerID()),
		"ApiVersionsRequest": sarama.NewMockApiVersionsResponse(t),
	})
	t.Cleanup(broker.Close)
	return broker
}

// Append must commit with acks=all and retry transient produce failures
// itself before surfacing an error.
func TestProducerConfigCommitsWithAcksAll(t *testing.T) {
	l := testLog(t, config.Config{KafkaBrokers: "b:9092", KafkaTopic: "t", KafkaPartitions: 4})
	pc := l.saramaCfg.Producer
	if pc.RequiredAcks != sarama.WaitForAll {
		t.Errorf("RequiredAcks = %v, want WaitForAll", pc.RequiredAcks)
	}
	if !pc.Return.Successes {
		t.Error("Return.Successes must be set for SyncProducer offsets")
	}
	if pc.Retry.Max != maxAttempts {
		t.Errorf("Retry.Max = %d, want %d", pc.Retry.Max, maxAttempts)
	}
	if pc.Retry.Backoff != baseBackoff {
		t.Errorf("Retry.Backoff = %v, want %v", pc.Retry.Backoff, baseBackoff)
	}
	if l.saramaCfg.Net.DialTimeout != dialTimeout || l.saramaCfg.Net.KeepAlive != keepAliveTimeout {
		t.Error("Net.DialTimeout/KeepAlive must mirror the dial constants")
	}
}

// A producer owns broker connections, so every Append must share one instance
// instead of dialing (and re-authenticating) per event.
func TestAppendProducerIsReused(t *testing.T) {
	broker := mockCluster(t)
	l := testLog(t, config.Config{KafkaBrokers: broker.Addr(), KafkaTopic: "t", KafkaPartitions: 1})
	defer func() { _ = l.Close() }()
	first, err := l.appendProducer()
	if err != nil {
		t.Fatalf("appendProducer: %v", err)
	}
	second, err := l.appendProducer()
	if err != nil {
		t.Fatalf("appendProducer: %v", err)
	}
	if second != first {
		t.Fatalf("appendProducer returned a new producer: %p then %p", first, second)
	}
	c1, _ := l.getClient()
	c2, _ := l.getClient()
	if c1 != c2 {
		t.Error("getClient must return the shared client")
	}
}

// Sarama calls take no context, so a caller that gives up must not stay
// blocked behind broker timeouts and retries.
func TestAppendHonoursCanceledContext(t *testing.T) {
	// A listener that never speaks Kafka: the dial succeeds, the handshake hangs.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
		}
	}()
	l := testLog(t, config.Config{KafkaBrokers: ln.Addr().String(), KafkaTopic: "t", KafkaPartitions: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = l.Append(ctx, &kfusev1.EventEnvelope{SessionId: "s"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Append err = %v, want context deadline", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Append blocked %v past a 200ms deadline", took)
	}
}

func TestCloseIsIdempotentAndReopens(t *testing.T) {
	broker := mockCluster(t)
	l := testLog(t, config.Config{KafkaBrokers: broker.Addr(), KafkaTopic: "t", KafkaPartitions: 1})
	if err := l.Close(); err != nil {
		t.Fatalf("Close on an idle Log: %v", err)
	}
	p, err := l.appendProducer()
	if err != nil {
		t.Fatalf("appendProducer: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	again, err := l.appendProducer()
	if err != nil {
		t.Fatalf("appendProducer after Close: %v", err)
	}
	if again == p {
		t.Error("appendProducer after Close returned the closed producer")
	}
	_ = l.Close()
}
