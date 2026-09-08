// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package perf is an opt-in, low-overhead JSONL event recorder for the
// benchmark suite (examples/perf). When KF_PERF_LOG names a file, every
// instrumented phase (blob PUT, Kafka append, upper apply, FUSE reads,
// resume replay, checkpoints, branches) appends one JSON object per line.
// When the variable is unset the fast path is a single atomic load.
package perf

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/addisonhuddy/kfuse/internal/log"
)

var (
	enabled atomic.Bool
	initOne sync.Once

	mu sync.Mutex
	w  *bufio.Writer
	f  *os.File
)

// Enabled reports whether perf recording is active. Callers use it to skip
// timestamping entirely on the hot path when recording is off.
func Enabled() bool {
	initOne.Do(setup)
	return enabled.Load()
}

func setup() {
	path := os.Getenv("KF_PERF_LOG")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Error("perf open failed", "path", path, log.Err(err))
		return
	}
	f = file
	w = bufio.NewWriterSize(file, 64<<10)
	enabled.Store(true)
}

// Field is one key/value pair on an event. Values are emitted as JSON
// numbers (int64/float64), strings, or booleans.
type Field struct {
	Key string
	Val any
}

// I64 returns an int64-valued field.
func I64(key string, v int64) Field { return Field{Key: key, Val: v} }

// Str returns a string-valued field.
func Str(key string, v string) Field { return Field{Key: key, Val: v} }

// Bool returns a boolean-valued field.
func Bool(key string, v bool) Field { return Field{Key: key, Val: v} }

// Emit appends one event line: {"ts":<unix_ns>,"ev":<name>,...fields}.
// It is safe for concurrent use and a no-op when recording is off.
func Emit(name string, fields ...Field) {
	if !Enabled() {
		return
	}
	var b strings.Builder
	b.Grow(128)
	b.WriteString(`{"ts":`)
	b.WriteString(strconv.FormatInt(time.Now().UnixNano(), 10))
	b.WriteString(`,"ev":`)
	b.WriteString(strconv.Quote(name))
	for _, fl := range fields {
		b.WriteByte(',')
		b.WriteString(strconv.Quote(fl.Key))
		b.WriteByte(':')
		switch v := fl.Val.(type) {
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		case float64:
			b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		case string:
			b.WriteString(strconv.Quote(v))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		default:
			b.WriteString(strconv.Quote(fmt.Sprint(v)))
		}
	}
	b.WriteString("}\n")
	mu.Lock()
	_, _ = w.WriteString(b.String())
	mu.Unlock()
}

// Flush drains buffered events to disk. Mount teardown calls it so short
// benchmark runs never lose the tail of the log.
func Flush() {
	if !Enabled() {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := w.Flush(); err != nil {
		log.Error("perf flush failed", log.Err(err))
	}
	_ = f.Sync()
}

// Since returns the elapsed nanoseconds from start.
func Since(start time.Time) int64 { return time.Since(start).Nanoseconds() }
