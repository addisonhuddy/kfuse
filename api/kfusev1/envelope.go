// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package kfusev1

import "time"

// EventOp is the oneof payload of an EventEnvelope (EventEnvelope_Create,
// EventEnvelope_Write, ...), nameable outside this package.
type EventOp = isEventEnvelope_Op

// OpName is the canonical short name of an envelope's operation. It is the
// value of the Kafka `kf-type` record header and the `op` field of commit
// perf records; the names are part of the on-wire contract and must not
// change.
func OpName(ev *EventEnvelope) string {
	switch ev.GetOp().(type) {
	case *EventEnvelope_SessionStart:
		return "session_start"
	case *EventEnvelope_Create:
		return "create"
	case *EventEnvelope_Write:
		return "write"
	case *EventEnvelope_Unlink:
		return "unlink"
	case *EventEnvelope_Mkdir:
		return "mkdir"
	case *EventEnvelope_Rmdir:
		return "rmdir"
	case *EventEnvelope_Rename:
		return "rename"
	case *EventEnvelope_Symlink:
		return "symlink"
	case *EventEnvelope_Setattr:
		return "setattr"
	}
	return "unknown"
}

// NewEnvelope stamps a fresh event envelope for a session. Seq is left at 0
// for the commit path to assign under the session lock; callers that emit an
// event outside that path (SessionStart) set it themselves.
func NewEnvelope(sessionID, lowerID string, op EventOp) *EventEnvelope {
	return &EventEnvelope{
		UnixMicros: time.Now().UnixMicro(),
		SessionId:  sessionID,
		LowerId:    lowerID,
		Op:         op,
	}
}
