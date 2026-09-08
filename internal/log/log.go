// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package log

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

var (
	level  slog.LevelVar
	logger atomic.Pointer[slog.Logger]
)

func init() {
	level.Set(slog.LevelInfo)
	logger.Store(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &level})))
}

func SetOutput(w io.Writer) {
	logger.Store(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: &level})))
}

func SetLevel(l slog.Level) {
	level.Set(l)
}

func Debug(msg string, args ...any) { logger.Load().Debug(msg, args...) }
func Info(msg string, args ...any)  { logger.Load().Info(msg, args...) }
func Warn(msg string, args ...any)  { logger.Load().Warn(msg, args...) }
func Error(msg string, args ...any) { logger.Load().Error(msg, args...) }

func Err(err error) slog.Attr { return slog.Any("err", err) }
