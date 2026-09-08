// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package kafkalog is the ordered append-only change log. Records are keyed
// by session_id so all events of a session land on one partition
// (hash partitioner), giving per-session total order. Only metadata + blob
// refs travel through Kafka; file bytes live in S3.
package kafkalog

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/config"
)

// KafkaOffset is the canonical "point in time" (spec §4.3).
type KafkaOffset struct {
	Topic     string `json:"topic"`
	Partition int32  `json:"partition"`
	Offset    int64  `json:"offset"`
}

// Retry bounds for the transient failures a broker rolling restart, leader
// election or network blip produces.
const (
	maxAttempts = 5
	baseBackoff = 100 * time.Millisecond
	maxBackoff  = 2 * time.Second
	dialTimeout = 15 * time.Second
	// keepAliveTimeout controls broker connection keep-alives.
	keepAliveTimeout = 30 * time.Second
	// batchTimeout bounds the producer's batching delay.
	batchTimeout = 5 * time.Millisecond
	// fetchMaxWait bounds broker fetch polling.
	fetchMaxWait = 1 * time.Second
	// fetchMaxBytes bounds a single replay fetch.
	fetchMaxBytes = 10e6
	// fetchTimeout bounds each replay fetch.
	fetchTimeout = 30 * time.Second
)

// kafkaVersion is the protocol version negotiated with the brokers; Confluent
// Cloud runs a newer broker and speaks every older protocol.
var kafkaVersion = sarama.V2_8_0_0

type Log struct {
	cfg        config.Config
	topic      string
	partitions int // actual partition count; 0 until RefreshPartitions
	brokers    []string
	saramaCfg  *sarama.Config

	// One client owns the broker connection pool; one producer serves every
	// Append, so a producer per call would reconnect and re-authenticate on
	// every event. Both are created lazily and dropped by Close.
	mu       sync.Mutex
	client   sarama.Client
	producer sarama.SyncProducer
}

func New(cfg config.Config) (*Log, error) {
	sc := sarama.NewConfig()
	sc.Version = kafkaVersion
	sc.ClientID = "kfuse"
	sc.Net.DialTimeout = dialTimeout
	sc.Net.KeepAlive = keepAliveTimeout
	if cfg.KafkaSASLUser != "" {
		sc.Net.SASL.Enable = true
		sc.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		sc.Net.SASL.User = cfg.KafkaSASLUser
		sc.Net.SASL.Password = cfg.KafkaSASLPassword
	}
	if cfg.KafkaTLS {
		sc.Net.TLS.Enable = true
		sc.Net.TLS.Config = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	// A missing topic must surface as "not found", never be auto-created with
	// the broker default partition count.
	sc.Metadata.AllowAutoTopicCreation = false

	sc.Producer.RequiredAcks = sarama.WaitForAll
	sc.Producer.Return.Successes = true
	sc.Producer.Return.Errors = true
	sc.Producer.Retry.Max = maxAttempts
	sc.Producer.Retry.Backoff = baseBackoff
	sc.Producer.Timeout = dialTimeout
	sc.Producer.Flush.Frequency = batchTimeout
	sc.Producer.Partitioner = sarama.NewManualPartitioner

	sc.Consumer.MaxWaitTime = fetchMaxWait
	sc.Consumer.Fetch.Max = fetchMaxBytes
	sc.Consumer.Return.Errors = true

	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("kafkalog: config: %w", err)
	}
	return &Log{
		cfg:        cfg,
		topic:      cfg.KafkaTopic,
		partitions: cfg.KafkaPartitions,
		brokers:    strings.Split(cfg.KafkaBrokers, ","),
		saramaCfg:  sc,
	}, nil
}

// awaitCtx runs a blocking Sarama call and returns early with ctx's error
// if the caller gives up first. Sarama's synchronous APIs take no context;
// the abandoned call finishes in the background against its own timeouts.
func awaitCtx[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := fn()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// getClient returns the shared broker client, creating it on first use.
func (l *Log) getClient() (sarama.Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clientLocked()
}

func (l *Log) clientLocked() (sarama.Client, error) {
	if l.client == nil {
		c, err := sarama.NewClient(l.brokers, l.saramaCfg)
		if err != nil {
			return nil, fmt.Errorf("kafkalog: connect: %w", err)
		}
		l.client = c
	}
	return l.client, nil
}

// RefreshPartitions resolves the topic's actual partition count from broker
// metadata. Producer and consumer MUST agree on partition selection, and the
// topic's real count (not the configured default) is authoritative.
func (l *Log) RefreshPartitions(ctx context.Context) error {
	client, err := awaitCtx(ctx, l.getClient)
	if err != nil {
		return err
	}
	parts, err := awaitCtx(ctx, func() ([]int32, error) {
		if err := client.RefreshMetadata(l.topic); err != nil {
			return nil, err
		}
		return client.Partitions(l.topic)
	})
	if err != nil {
		if errors.Is(err, sarama.ErrUnknownTopicOrPartition) {
			return fmt.Errorf("kafkalog: topic %s not found", l.topic)
		}
		return fmt.Errorf("kafkalog: topic %s: %w", l.topic, err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("kafkalog: topic %s has no visible partitions (ACL check)", l.topic)
	}
	l.partitions = len(parts)
	return nil
}

// PartitionFor maps a session key to its partition. Both the producer
// (Append sets the partition explicitly) and the consumer use this exact
// function, so they always agree.
func (l *Log) PartitionFor(sessionID string) int {
	h := fnv.New32a()
	h.Write([]byte(sessionID))
	return int(h.Sum32() % uint32(l.partitionCount()))
}

// partitionCount returns the actual topic partition count when known, else
// the configured default (topic not yet refreshed).
func (l *Log) partitionCount() int {
	if l.partitions > 0 {
		return l.partitions
	}
	return l.cfg.KafkaPartitions
}

// EnsureTopic creates the topic if missing. Pre-created topics are fine;
// the partition count must never change once chosen. Topics not created here
// (ACL-restricted keys) are not an error.
func (l *Log) EnsureTopic(ctx context.Context) error {
	_, err := awaitCtx(ctx, func() (struct{}, error) {
		admin, err := sarama.NewClusterAdmin(l.brokers, l.saramaCfg)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = admin.Close() }()
		return struct{}{}, admin.CreateTopic(l.topic, &sarama.TopicDetail{
			NumPartitions:     int32(l.partitions),
			ReplicationFactor: -1, // broker default; required on Confluent Cloud
		}, false)
	})
	switch {
	case err == nil,
		errors.Is(err, sarama.ErrTopicAlreadyExists),
		errors.Is(err, sarama.ErrTopicAuthorizationFailed),
		errors.Is(err, sarama.ErrClusterAuthorizationFailed):
		return nil
	}
	return fmt.Errorf("kafkalog: create topic %s: %w", l.topic, err)
}

// Append publishes one event with key=session_id and acks=all, then returns
// the Kafka offset the record landed at.
func (l *Log) Append(ctx context.Context, ev *kfusev1.EventEnvelope) (KafkaOffset, error) {
	value, err := proto.Marshal(ev)
	if err != nil {
		return KafkaOffset{}, err
	}
	part := int32(l.PartitionFor(ev.SessionId))
	msg := &sarama.ProducerMessage{
		Topic:     l.topic,
		Partition: part,
		Key:       sarama.StringEncoder(ev.SessionId),
		Value:     sarama.ByteEncoder(value),
		Headers: []sarama.RecordHeader{
			{Key: []byte("kf-schema"), Value: []byte("1")},
			{Key: []byte("kf-type"), Value: []byte(kfusev1.OpName(ev))},
			{Key: []byte("kf-lower"), Value: []byte(ev.LowerId)},
		},
	}
	producer, err := awaitCtx(ctx, l.appendProducer)
	if err != nil {
		return KafkaOffset{}, err
	}
	off, err := awaitCtx(ctx, func() (KafkaOffset, error) {
		p, o, err := producer.SendMessage(msg)
		return KafkaOffset{Topic: l.topic, Partition: p, Offset: o}, err
	})
	if err != nil {
		return KafkaOffset{}, fmt.Errorf("kafkalog: append: %w", err)
	}
	return off, nil
}

// appendProducer returns the shared producer, creating it on first use.
// SyncProducer is safe for concurrent use and retries a failed produce
// itself (Producer.Retry.Max) before returning an error to the caller.
func (l *Log) appendProducer() (sarama.SyncProducer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.producer == nil {
		client, err := l.clientLocked()
		if err != nil {
			return nil, err
		}
		p, err := sarama.NewSyncProducerFromClient(client)
		if err != nil {
			return nil, fmt.Errorf("kafkalog: producer: %w", err)
		}
		l.producer = p
	}
	return l.producer, nil
}

// Close releases the shared producer and the client's pooled broker
// connections. Further calls open a new client, so Close is safe to call on
// an idle Log and safe to call twice.
func (l *Log) Close() error {
	l.mu.Lock()
	p, c := l.producer, l.client
	l.producer, l.client = nil, nil
	l.mu.Unlock()
	var err error
	if p != nil {
		err = p.Close()
	}
	if c != nil {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

func (l *Log) highWaterMark(ctx context.Context, partition int32) (int64, error) {
	// Append gets its offset from the producer; this is only used to find the
	// replay bound (TailOffset, ReadSessionTo). A leader election or broker
	// restart makes both the connection and the offset read fail transiently,
	// so retry rather than abort a resume over a healthy partition.
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepBackoff(ctx, attempt); err != nil {
				return 0, err
			}
		}
		last, err := awaitCtx(ctx, func() (int64, error) {
			client, err := l.getClient()
			if err != nil {
				return 0, err
			}
			if attempt > 1 {
				_ = client.RefreshMetadata(l.topic)
			}
			return client.GetOffset(l.topic, partition, sarama.OffsetNewest)
		})
		if err == nil {
			return last, nil
		}
		if ctx.Err() != nil {
			return 0, err
		}
		lastErr = fmt.Errorf("kafkalog: high-water mark: %w", err)
	}
	return 0, lastErr
}

// sleepBackoff waits out the exponential delay before the given attempt, or
// returns the context error if the caller gave up first.
func sleepBackoff(ctx context.Context, attempt int) error {
	d := baseBackoff << (attempt - 2)
	if d > maxBackoff {
		d = maxBackoff
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// TailOffset returns the high-water mark (offset after the last record) for a
// session's partition.
func (l *Log) TailOffset(ctx context.Context, sessionID string) (int64, error) {
	return l.highWaterMark(ctx, int32(l.PartitionFor(sessionID)))
}

// ReadSession replays the events for one session, starting at from (inclusive)
// on that session's partition, filtering by key. It reads up to the current
// high-water mark (replay never blocks waiting for new records). Returns the
// events, the offset after the last matching record, and any error.
func (l *Log) ReadSession(ctx context.Context, sessionID string, from int64) ([]*kfusev1.EventEnvelope, KafkaOffset, error) {
	return l.ReadSessionTo(ctx, sessionID, from, -1)
}

// ReadSessionTo replays events for sessionID in [from, toOffset] (inclusive).
// Pass toOffset < 0 to read up to the high-water mark. The returned offset is
// after the last matching record, or from when no matching record is found.
func (l *Log) ReadSessionTo(ctx context.Context, sessionID string, from, toOffset int64) ([]*kfusev1.EventEnvelope, KafkaOffset, error) {
	part := l.PartitionFor(sessionID)
	last := KafkaOffset{Topic: l.topic, Partition: int32(part), Offset: from}
	hw, err := l.highWaterMark(ctx, int32(part))
	if err != nil {
		return nil, last, err
	}
	end := hw - 1
	if toOffset >= 0 && toOffset < end {
		end = toOffset
	}
	if from > end {
		return nil, last, nil
	}
	client, err := l.getClient()
	if err != nil {
		return nil, last, err
	}
	// Retention may have trimmed the partition head; the broker rejects a
	// start offset below it, so begin at the oldest retained record instead.
	start := from
	oldest, err := client.GetOffset(l.topic, int32(part), sarama.OffsetOldest)
	if err != nil {
		return nil, last, fmt.Errorf("kafkalog: oldest offset: %w", err)
	}
	if oldest > start {
		start = oldest
	}
	if start > end {
		return nil, last, nil
	}
	consumer, err := sarama.NewConsumerFromClient(client)
	if err != nil {
		return nil, last, fmt.Errorf("kafkalog: consumer: %w", err)
	}
	defer func() { _ = consumer.Close() }()
	pc, err := consumer.ConsumePartition(l.topic, int32(part), start)
	if err != nil {
		return nil, last, fmt.Errorf("kafkalog: consume partition %d from %d: %w", part, start, err)
	}
	defer func() { _ = pc.Close() }()
	return readSessionFromReader(ctx, l.topic, sessionID, int32(part), from, end, partitionReader{pc: pc})
}

// record is the slice of a Kafka message replay needs.
type record struct {
	Offset int64
	Key    []byte
	Value  []byte
}

type messageReader interface {
	FetchMessage(context.Context) (record, error)
}

// partitionReader adapts a PartitionConsumer's channels to messageReader,
// surfacing consumer errors and the caller's deadline as fetch errors.
type partitionReader struct {
	pc sarama.PartitionConsumer
}

func (r partitionReader) FetchMessage(ctx context.Context) (record, error) {
	select {
	case msg, ok := <-r.pc.Messages():
		if !ok {
			return record{}, errors.New("consumer closed")
		}
		return record{Offset: msg.Offset, Key: msg.Key, Value: msg.Value}, nil
	case cerr, ok := <-r.pc.Errors():
		if !ok {
			return record{}, errors.New("consumer closed")
		}
		return record{}, cerr
	case <-ctx.Done():
		return record{}, ctx.Err()
	}
}

func readSessionFromReader(
	ctx context.Context,
	topic string,
	sessionID string,
	partition int32,
	from, end int64,
	r messageReader,
) ([]*kfusev1.EventEnvelope, KafkaOffset, error) {
	last := KafkaOffset{Topic: topic, Partition: partition, Offset: from}
	if from > end {
		return nil, last, nil
	}
	var out []*kfusev1.EventEnvelope
	scannedThrough := from
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		msg, err := r.FetchMessage(fetchCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return out, last, fmt.Errorf(
					"kafkalog: replay truncated for session %s: reached offset %d of %d: %w",
					sessionID,
					scannedThrough,
					end,
					err,
				)
			}
			return out, last, fmt.Errorf("kafkalog: fetch: %w", err)
		}
		if msg.Offset > end {
			break
		}
		scannedThrough = msg.Offset + 1
		if string(msg.Key) != sessionID {
			if msg.Offset >= end {
				break
			}
			continue
		}
		ev := &kfusev1.EventEnvelope{}
		if err := proto.Unmarshal(msg.Value, ev); err != nil {
			return out, last, fmt.Errorf("kafkalog: decode at %d: %w", msg.Offset, err)
		}
		out = append(out, ev)
		last.Offset = msg.Offset + 1
		if msg.Offset >= end {
			break
		}
	}
	return out, last, nil
}
