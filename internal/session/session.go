// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package session wires the commit pipeline: blob-before-log, then Kafka
// append (acks=all) as the commit point, then Apply to the in-memory upper.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/blobstore"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
	"github.com/addisonhuddy/kfuse/internal/log"
	"github.com/addisonhuddy/kfuse/internal/perf"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// checkpointEveryEvents is how many committed events trigger an async state
// image flush (the materializer also flushes on a 30s timer).
const (
	checkpointEveryEvents = 100
	// checkpointTimeout bounds an asynchronous checkpoint.
	checkpointTimeout = 30 * time.Second
)

// ErrLeaseLost is returned from commit paths once the writer lease has been
// lost to another holder (best-effort lease; see README Limits).
var ErrLeaseLost = errors.New("session: lease lost")

// ErrLogDiverged is returned from commit and checkpoint paths after an append
// failed without a definite outcome. The record may or may not be durable, so
// the in-memory upper can no longer be trusted to match the log: this session
// object refuses further writes and snapshots, and the mount must be resumed
// (OpenSession replays the log and settles what actually landed).
var ErrLogDiverged = errors.New("session: log diverged after failed append; remount to resume")

// EventLog is the ordered durable change log a session commits to. Append is
// the commit point: once it returns nil the record is durable and its offset
// is known. *kafkalog.Log is the production implementation.
type EventLog interface {
	Append(ctx context.Context, ev *kfusev1.EventEnvelope) (kafkalog.KafkaOffset, error)
	ReadSession(ctx context.Context, sessionID string, from int64) ([]*kfusev1.EventEnvelope, kafkalog.KafkaOffset, error)
	ReadSessionTo(ctx context.Context, sessionID string, from, toOffset int64) ([]*kfusev1.EventEnvelope, kafkalog.KafkaOffset, error)
}

// BlobPutter stores file bytes and returns their content id. Writes upload
// through it before the event is appended (blob-before-log).
// *blobstore.BlobStore is the production implementation.
type BlobPutter interface {
	Put(ctx context.Context, data []byte) (string, error)
}

var (
	_ EventLog   = (*kafkalog.Log)(nil)
	_ BlobPutter = (*blobstore.BlobStore)(nil)
)

// Session is one mounted session: registry metadata + in-memory upper +
// sequencing state.
type Session struct {
	Meta registry.Session

	mu    sync.Mutex
	upper *upper.Upper
	seq   uint64
	// lastOffset is the Kafka offset of the newest record this session has
	// appended, i.e. the offset the current upper state covers. -1 means
	// unknown (nothing appended through this object yet), in which case
	// Checkpoint writes no image.
	lastOffset int64
	lowerID    string
	leaseLost  atomic.Bool
	// diverged is set once an append failed with an unknown outcome; see
	// ErrLogDiverged.
	diverged atomic.Bool

	// ckptMu serializes checkpoints so two overlapping S3 uploads cannot land
	// out of order; savedOffset (guarded by ckptMu) is the newest offset
	// already durable in S3.
	ckptMu      sync.Mutex
	savedOffset int64
	// ckptInFlight single-flights the async checkpoints triggered by commit:
	// rapid commits must not queue a goroutine each.
	ckptInFlight atomic.Bool
	// ckptWG tracks those async checkpoints so an owner shutting the session
	// down can drain them (WaitCheckpoints) before releasing what they use.
	ckptWG sync.WaitGroup

	blobs BlobPutter
	log   EventLog
	reg   *registry.Registry
}

// NewSession creates and registers a new session, emitting SessionStart.
func NewSession(ctx context.Context, lowerID string, blobs BlobPutter, log EventLog, reg *registry.Registry) (*Session, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	s := &Session{
		Meta:        registry.Session{ID: id, LowerID: lowerID, CreatedAt: time.Now().UTC()},
		upper:       upper.New(),
		lastOffset:  -1,
		savedOffset: -1,
		lowerID:     lowerID,
		blobs:       blobs,
		log:         log,
		reg:         reg,
	}
	if err := s.reg.Create(ctx, s.Meta); err != nil {
		return nil, fmt.Errorf("session: register: %w", err)
	}
	if err := s.reg.AppendLowerSession(ctx, lowerID, id); err != nil {
		return nil, fmt.Errorf("session: index under lower: %w", err)
	}
	start := kfusev1.NewEnvelope(id, lowerID, &kfusev1.EventEnvelope_SessionStart{
		SessionStart: &kfusev1.SessionStart{LowerId: lowerID},
	})
	start.Seq = 1
	off, err := s.log.Append(ctx, start)
	if err != nil {
		return nil, fmt.Errorf("session: emit SessionStart: %w", err)
	}
	s.seq = 1
	s.lastOffset = off.Offset
	if err := s.upper.Apply(start); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenSession resumes an existing session: load registry metadata, load the
// latest state image if present, and replay the session's Kafka log into the
// upper.
func OpenSession(ctx context.Context, id string, lowerID string, blobs BlobPutter, log EventLog, reg *registry.Registry) (*Session, error) {
	meta, err := reg.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if lowerID != "" && meta.LowerID != lowerID {
		return nil, fmt.Errorf("session: %s belongs to lower %q, not %q", id, meta.LowerID, lowerID)
	}
	r, err := reconstruct(ctx, reg, log, id, -1)
	if err != nil {
		return nil, err
	}
	s := &Session{
		Meta:        *meta,
		upper:       r.upper,
		lastOffset:  r.covered,
		savedOffset: r.fromOffset - 1,
		lowerID:     meta.LowerID,
		blobs:       blobs,
		log:         log,
		reg:         reg,
	}
	s.seq = r.upper.SeqLast()
	if perf.Enabled() {
		perf.Emit("resume",
			perf.Str("session", id),
			perf.I64("image_load_ns", r.restoreNs),
			perf.I64("log_read_ns", r.readNs),
			perf.I64("apply_ns", r.applyNs),
			perf.I64("from_offset", r.fromOffset),
			perf.I64("replayed_events", int64(r.replayed)),
			perf.Bool("had_image", r.fromOffset > 0))
	}
	return s, nil
}

// reconstruction is an upper rebuilt from a session's newest usable state
// image plus a replay of its log, either to the tail (resume) or to a fixed
// offset (branch).
type reconstruction struct {
	upper *upper.Upper
	// fromOffset is the log offset replay started at: 0 without an image,
	// otherwise one past the offset the image covers.
	fromOffset int64
	// covered is the newest log offset the upper reflects: everything the
	// image covered plus every replayed record. -1 when the session's
	// partition held nothing at all for it.
	covered int64
	// tail is the offset after the last replayed record (fromOffset when the
	// replay matched nothing).
	tail     int64
	replayed int

	restoreNs, readNs, applyNs int64
}

// reconstruct rebuilds sessionID's upper as of toOffset (inclusive; < 0 for
// the current tail). It is the single reconstruction path shared by resume
// (OpenSession) and branch (Branch). A state image that exists but cannot be
// loaded is an error, never a silent fallback to full replay: once Kafka
// retention has trimmed the covered records that replay is incomplete.
func reconstruct(ctx context.Context, reg *registry.Registry, log EventLog, sessionID string, toOffset int64) (*reconstruction, error) {
	restoreStart := time.Now()
	u, fromOffset, err := RestoreUpper(ctx, reg, sessionID, toOffset)
	if err != nil {
		return nil, err
	}
	r := &reconstruction{upper: u, fromOffset: fromOffset, restoreNs: perf.Since(restoreStart)}
	readStart := time.Now()
	events, tail, err := log.ReadSessionTo(ctx, sessionID, fromOffset, toOffset)
	if err != nil {
		return nil, fmt.Errorf("session: replay %s: %w", sessionID, err)
	}
	r.readNs = perf.Since(readStart)
	applyStart := time.Now()
	for _, ev := range events {
		if err := u.Apply(ev); err != nil {
			return nil, fmt.Errorf("session: apply seq %d: %w", ev.Seq, err)
		}
	}
	r.applyNs = perf.Since(applyStart)
	r.replayed = len(events)
	r.tail = tail.Offset
	r.covered = fromOffset - 1
	if len(events) > 0 && tail.Offset-1 > r.covered {
		r.covered = tail.Offset - 1
	}
	return r, nil
}

// Branch creates a child session whose initial state is parentID's upper as
// of toOffset (inclusive; < 0 for the parent's current committed tail). The
// child is registered with lineage {parent, branchAt} where branchAt is the
// parent-partition offset the branch reflects, its SessionStart is appended
// to its own partition, and its initial state image is tagged with that
// SessionStart's offset -- the child's offset space, not the parent's -- so
// resuming the child skips nothing it should replay and replays nothing it
// already holds. The returned session is opened through the normal resume
// path so it starts life exactly as a later remount would see it.
func Branch(ctx context.Context, parentID string, toOffset int64, lowerID string, blobs BlobPutter, log EventLog, reg *registry.Registry) (*Session, error) {
	branchStart := time.Now()
	parentMeta, err := reg.Load(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("session: load parent session %s: %w", parentID, err)
	}
	if lowerID != "" && parentMeta.LowerID != lowerID {
		return nil, fmt.Errorf("session: parent belongs to lower %q, not %q", parentMeta.LowerID, lowerID)
	}
	r, err := reconstruct(ctx, reg, log, parentID, toOffset)
	if err != nil {
		return nil, err
	}
	branchAt := toOffset
	if branchAt < 0 {
		branchAt = r.tail - 1
		if branchAt < 0 {
			branchAt = 0
		}
	}
	childID, err := NewID()
	if err != nil {
		return nil, err
	}
	lineage := &kfusev1.Lineage{
		ParentSessionId: parentID,
		ParentOffset:    branchAt,
	}
	childMeta := registry.Session{
		ID:        childID,
		LowerID:   parentMeta.LowerID,
		Lineage:   lineage,
		CreatedAt: time.Now().UTC(),
	}
	if err := reg.Create(ctx, childMeta); err != nil {
		return nil, fmt.Errorf("session: register child session: %w", err)
	}
	if err := reg.AppendLowerSession(ctx, parentMeta.LowerID, childID); err != nil {
		return nil, fmt.Errorf("session: append child to lower index: %w", err)
	}
	if err := reg.SaveBranch(ctx, parentID, childID, branchAt); err != nil {
		return nil, fmt.Errorf("session: record branch provenance: %w", err)
	}
	start := kfusev1.NewEnvelope(childID, parentMeta.LowerID, &kfusev1.EventEnvelope_SessionStart{
		SessionStart: &kfusev1.SessionStart{
			LowerId: parentMeta.LowerID,
			Lineage: lineage,
		},
	})
	start.Seq = 1
	childOff, err := log.Append(ctx, start)
	if err != nil {
		return nil, fmt.Errorf("session: emit child SessionStart: %w", err)
	}
	childSnapshot, err := r.upper.Marshal()
	if err != nil {
		return nil, fmt.Errorf("session: snapshot branch state: %w", err)
	}
	if err := reg.SaveStateImage(ctx, childID, childOff.Offset, childSnapshot); err != nil {
		return nil, fmt.Errorf("session: save child initial state: %w", err)
	}
	child, err := OpenSession(ctx, childID, parentMeta.LowerID, blobs, log, reg)
	if err == nil && perf.Enabled() {
		perf.Emit("branch",
			perf.Str("parent", parentID),
			perf.I64("ns", perf.Since(branchStart)),
			perf.I64("replayed_events", int64(r.replayed)),
			perf.I64("branch_at", branchAt),
			perf.I64("image_bytes", int64(len(childSnapshot))))
	}
	return child, err
}

// RestoreUpper loads the newest state image for a session covering at most
// maxOffset (pass -1 for the newest image) and returns the restored upper plus
// the log offset replay must resume from. A missing image yields an empty upper
// and offset 0; an image that exists but cannot be read or decoded is an error,
// since replaying from the beginning silently drops every event the image
// covered once Kafka retention has trimmed them.
func RestoreUpper(ctx context.Context, reg *registry.Registry, sessionID string, maxOffset int64) (*upper.Upper, int64, error) {
	coversOffset, imgData, err := reg.LatestStateImage(ctx, sessionID, maxOffset)
	if err != nil {
		return nil, 0, fmt.Errorf("session: load state image for %s: %w", sessionID, err)
	}
	if coversOffset < 0 || imgData == nil {
		return upper.New(), 0, nil
	}
	restored, err := upper.UnmarshalState(imgData)
	if err != nil {
		return nil, 0, fmt.Errorf("session: decode state image covering offset %d: %w", coversOffset, err)
	}
	return restored, coversOffset + 1, nil
}

// NewWithState builds a session from an existing upper (used by tests and
// tooling that need a mounted view without touching Kafka).
func NewWithState(meta registry.Session, u *upper.Upper, blobs BlobPutter, log EventLog, reg *registry.Registry) *Session {
	s := &Session{
		Meta:        meta,
		upper:       u,
		lastOffset:  -1,
		savedOffset: -1,
		lowerID:     meta.LowerID,
		blobs:       blobs,
		log:         log,
		reg:         reg,
	}
	s.seq = u.SeqLast()
	return s
}

// Checkpoint forces a state image snapshot to S3 and returns the covered Kafka
// offset, or -1 when the session has nothing durable to snapshot yet.
func (s *Session) Checkpoint(ctx context.Context) (int64, error) {
	// One checkpoint at a time: concurrent uploads of different snapshots can
	// complete out of order, leaving an older state as the newest image.
	s.ckptMu.Lock()
	defer s.ckptMu.Unlock()

	if s.diverged.Load() {
		return -1, ErrLogDiverged
	}

	// A session that never appended through this object (tooling, tests) has
	// no offset of its own. Do not fall back to the partition tail: that could
	// tag this upper with records it never applied, causing resume to skip
	// those records permanently.
	s.mu.Lock()
	known := s.lastOffset
	s.mu.Unlock()
	if known < 0 {
		return s.savedOffset, nil
	}

	// Take the covered offset and the serialized state together so the tag
	// always matches the content: a commit that lands during the upload
	// belongs to the next image, not this one.
	s.mu.Lock()
	offset := s.lastOffset
	marshalStart := time.Now()
	snapshot, err := s.upper.Marshal()
	marshalNs := perf.Since(marshalStart)
	s.mu.Unlock()
	if err != nil {
		return -1, fmt.Errorf("session: snapshot upper: %w", err)
	}
	if offset < 0 {
		// Nothing is durable yet: an image tagged 0 would claim to cover the
		// first record and make resume skip it.
		return -1, nil
	}
	if offset <= s.savedOffset {
		// No new records since the last image, so its content is identical.
		return s.savedOffset, nil
	}
	uploadStart := time.Now()
	if err := s.reg.SaveStateImage(ctx, s.ID(), offset, snapshot); err != nil {
		return -1, fmt.Errorf("session: save state image: %w", err)
	}
	if perf.Enabled() {
		perf.Emit("checkpoint",
			perf.Str("session", s.ID()),
			perf.I64("marshal_ns", marshalNs),
			perf.I64("upload_ns", perf.Since(uploadStart)),
			perf.I64("image_bytes", int64(len(snapshot))),
			perf.I64("offset", offset))
	}
	s.savedOffset = offset
	return offset, nil
}

func (s *Session) ID() string          { return s.Meta.ID }
func (s *Session) LowerID() string     { return s.lowerID }
func (s *Session) Upper() *upper.Upper { return s.upper }

// CommitCreate appends a Create event and applies it. Fails if the same path
// already exists in the overlay.
func (s *Session) CommitCreate(ctx context.Context, path string, mode, uid, gid uint32) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{
		Path: path, Mode: mode, Uid: uid, Gid: gid,
	}})
}

// CommitWrite uploads the bytes to S3 first (blob-before-log), then appends
// the Write event, then applies it.
func (s *Session) CommitWrite(ctx context.Context, path string, offset int64, data []byte, eof bool, mode uint32) error {
	if err := s.preflightWrite(path); err != nil {
		return err
	}
	blobID, err := s.blobs.Put(ctx, data)
	if err != nil {
		return fmt.Errorf("session: blob put: %w", err)
	}
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
		Path: path, Offset: offset, Length: uint64(len(data)),
		BlobId: []byte(blobID), Eof: eof, Mode: mode,
	}})
}

// CommitSparseWrite is CommitWrite for a path that may be lower-backed: when
// `fallthrough` is set, a newly-materialized node keeps its lower backing for
// unwritten ranges (sparse CoW). `mode`/`uid`/`gid` are the lower file's attrs,
// carried so the materialized node matches the lower.
func (s *Session) CommitSparseWrite(ctx context.Context, path string, offset int64, data []byte, eof bool, mode, uid, gid uint32, sparse bool) error {
	if err := s.preflightWrite(path); err != nil {
		return err
	}
	blobID, err := s.blobs.Put(ctx, data)
	if err != nil {
		return fmt.Errorf("session: blob put: %w", err)
	}
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
		Path: path, Offset: offset, Length: uint64(len(data)),
		BlobId: []byte(blobID), Eof: eof, Mode: mode, Fallthrough: sparse,
		Uid: uid, Gid: gid,
	}})
}

// CommitUnlink appends an Unlink event (whiteout for lower-backed paths,
// removal for overlay nodes — decided deterministically at replay).
func (s *Session) CommitUnlink(ctx context.Context, path string) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: path}})
}

// CommitMkdir appends a Mkdir event (new directory node).
func (s *Session) CommitMkdir(ctx context.Context, path string, mode, uid, gid uint32) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Mkdir{Mkdir: &kfusev1.Mkdir{
		Path: path, Mode: mode, Uid: uid, Gid: gid,
	}})
}

// CommitRmdir appends an Rmdir event (removal of an empty dir; whiteout for
// lower-backed dirs — decided deterministically at replay).
func (s *Session) CommitRmdir(ctx context.Context, path string) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Rmdir{Rmdir: &kfusev1.Rmdir{Path: path}})
}

// CommitSymlink appends a Symlink event (new symlink node).
func (s *Session) CommitSymlink(ctx context.Context, path, target string, uid, gid uint32) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Symlink{Symlink: &kfusev1.Symlink{Path: path, Target: target, Uid: uid, Gid: gid}})
}

// CommitSetattr appends a Setattr event (attr override or node mutation).
func (s *Session) CommitSetattr(ctx context.Context, sa *kfusev1.Setattr) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Setattr{Setattr: sa})
}

// CommitRename appends a Rename event (overlay tree move, or a redirect when
// `from` is lower-backed).
func (s *Session) CommitRename(ctx context.Context, from, to string, fromLower bool) error {
	return s.commitOp(ctx, &kfusev1.EventEnvelope_Rename{Rename: &kfusev1.Rename{From: from, To: to, FromLower: fromLower}})
}

// commitOp stamps an envelope for op against this session and commits it.
func (s *Session) commitOp(ctx context.Context, op kfusev1.EventOp) error {
	return s.commit(ctx, kfusev1.NewEnvelope(s.Meta.ID, s.lowerID, op))
}

// preflightWrite runs the checks a write can fail before its blob is uploaded
// (fencing and path validity), so a doomed write costs no S3 PUT.
func (s *Session) preflightWrite(path string) error {
	if err := s.refuseIfFenced(); err != nil {
		return err
	}
	if _, err := upper.NormalizePath(path); err != nil {
		return fmt.Errorf("session: invalid event: %w", err)
	}
	return nil
}

// refuseIfFenced reports why this session may no longer append: a lost writer
// lease or a diverged log.
func (s *Session) refuseIfFenced() error {
	if s.leaseLost.Load() {
		return ErrLeaseLost
	}
	if s.diverged.Load() {
		return ErrLogDiverged
	}
	return nil
}

// commit enforces: Kafka ack (commit point) BEFORE Apply. Apply never runs
// unless the event is durable. The seq is assigned under the lock so
// concurrent FUSE commits never race on seq or the upper. Once the writer
// lease is observed lost, no further events are appended; between the loss
// and the next heartbeat a few appends can still land (best-effort lease).
func (s *Session) commit(ctx context.Context, ev *kfusev1.EventEnvelope) error {
	if err := s.refuseIfFenced(); err != nil {
		return err
	}
	// Reject invalid paths BEFORE appending: a bad record is durable and would
	// poison resume if it only failed at Apply time.
	if err := validateEventPaths(ev); err != nil {
		return fmt.Errorf("session: invalid event: %w", err)
	}
	// Detach cancellation: an interrupted FUSE request (e.g. a signal to the
	// client) must not abort the append after the broker persisted it — the
	// kernel restarts the syscall and the retry would duplicate the event,
	// poisoning replay. Once started, a commit runs to completion.
	ctx = context.WithoutCancel(ctx)
	needCheckpoint, err := s.commitLocked(ctx, ev)
	if err != nil {
		return err
	}
	// Launch the async checkpoint after releasing s.mu (it takes the lock
	// itself) and single-flight it so rapid commits cannot pile up goroutines.
	if needCheckpoint && s.ckptInFlight.CompareAndSwap(false, true) {
		s.ckptWG.Add(1)
		go func() {
			defer s.ckptWG.Done()
			defer s.ckptInFlight.Store(false)
			s.checkpointAsync()
		}()
	}
	return nil
}

// WaitCheckpoints blocks until every commit-triggered asynchronous checkpoint
// started so far has finished. Callers must first stop issuing commits, or
// new checkpoints can keep it waiting.
func (s *Session) WaitCheckpoints() { s.ckptWG.Wait() }

// commitLocked appends and applies the event under s.mu, reporting whether the
// commit crossed a checkpoint boundary.
func (s *Session) commitLocked(ctx context.Context, ev *kfusev1.EventEnvelope) (needCheckpoint bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfFenced(); err != nil {
		return false, err
	}
	// Upper-state preconditions are decided here, under the same lock that
	// serializes every commit: two racing creates of one name cannot both
	// pass, and an event Apply would reject never becomes durable.
	if err := s.upper.Check(ev); err != nil {
		return false, fmt.Errorf("session: invalid event: %w", err)
	}
	s.seq++
	ev.Seq = s.seq
	appendStart := time.Now()
	off, err := s.log.Append(ctx, ev)
	if err != nil {
		// The record may still be durable (the broker can persist a produce
		// whose response never arrived). If it did, the log now holds a seq
		// this upper never applied, so neither a later image nor a later
		// event from this object can be trusted: fence it.
		s.diverged.Store(true)
		s.lastOffset = -1
		return false, fmt.Errorf("%w: %w", ErrLogDiverged, err)
	}
	appendNs := perf.Since(appendStart)
	s.lastOffset = off.Offset
	applyStart := time.Now()
	if err = s.upper.Apply(ev); err != nil {
		return false, fmt.Errorf("session: apply after commit (seq %d): %w", ev.Seq, err)
	}
	if perf.Enabled() {
		perf.Emit("commit",
			perf.Str("op", kfusev1.OpName(ev)),
			perf.I64("kafka_append_ns", appendNs),
			perf.I64("apply_ns", perf.Since(applyStart)))
	}
	return s.seq%checkpointEveryEvents == 0, nil
}

// validateEventPaths normalizes every path in the event, rejecting anything
// Apply would refuse (non-UTF-8, empty, '.'/'..', leading '/') before it can
// become durable. Apply re-checks as defense in depth.
func validateEventPaths(ev *kfusev1.EventEnvelope) error {
	norm := func(p string) error {
		_, err := upper.NormalizePath(p)
		return err
	}
	switch op := ev.Op.(type) {
	case *kfusev1.EventEnvelope_SessionStart:
		return nil
	case *kfusev1.EventEnvelope_Create:
		return norm(op.Create.Path)
	case *kfusev1.EventEnvelope_Write:
		return norm(op.Write.Path)
	case *kfusev1.EventEnvelope_Unlink:
		return norm(op.Unlink.Path)
	case *kfusev1.EventEnvelope_Rmdir:
		return norm(op.Rmdir.Path)
	case *kfusev1.EventEnvelope_Mkdir:
		return norm(op.Mkdir.Path)
	case *kfusev1.EventEnvelope_Symlink:
		return norm(op.Symlink.Path)
	case *kfusev1.EventEnvelope_Setattr:
		return norm(op.Setattr.Path)
	case *kfusev1.EventEnvelope_Rename:
		if err := norm(op.Rename.From); err != nil {
			return err
		}
		return norm(op.Rename.To)
	default:
		return fmt.Errorf("unknown op %T", ev.Op)
	}
}

// MarkLeaseLost fences the session: subsequent commits fail with ErrLeaseLost.
func (s *Session) MarkLeaseLost() { s.leaseLost.Store(true) }

// LeaseLost reports whether the session has been fenced by MarkLeaseLost.
func (s *Session) LeaseLost() bool { return s.leaseLost.Load() }

func (s *Session) checkpointAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	if _, err := s.Checkpoint(ctx); err != nil {
		log.Error("checkpoint failed", "session", s.ID(), log.Err(err))
	}
}

// NewID mints a random 32-hex session id.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
