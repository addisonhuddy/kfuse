// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package log

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestSetOutputAndLevel(t *testing.T) {
	var out bytes.Buffer
	SetOutput(&out)
	SetLevel(slog.LevelInfo)
	t.Cleanup(func() {
		SetLevel(slog.LevelInfo)
		SetOutput(os.Stderr)
	})

	Debug("debug message")
	Warn("warning message", "key", "value")

	got := out.String()
	if strings.Contains(got, "debug message") {
		t.Fatalf("debug message at info level: %s", got)
	}
	if !strings.Contains(got, "warning message") {
		t.Fatalf("warning message missing: %s", got)
	}
	if !strings.Contains(got, "key=value") {
		t.Fatalf("structured field missing: %s", got)
	}
}
