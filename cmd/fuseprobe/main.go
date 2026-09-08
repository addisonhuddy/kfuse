// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Command fuseprobe mounts a session over a lower dir and verifies the
// merged view without touching Kafka: lower passthrough, upper blob reads
// (S3), whiteouts, and readdir merge. Used by the container e2e; exits
// non-zero on any mismatch.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/blobstore"
	"github.com/addisonhuddy/kfuse/internal/config"
	"github.com/addisonhuddy/kfuse/internal/fs"
	"github.com/addisonhuddy/kfuse/internal/kafkalog"
	"github.com/addisonhuddy/kfuse/internal/registry"
	"github.com/addisonhuddy/kfuse/internal/session"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fuseprobe:", err)
		os.Exit(1)
	}
	fmt.Println("FUSEPROBE PASS")
}

func run() error {
	ctx := context.Background()
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	lower := os.Getenv("PROBE_LOWER")
	if lower == "" {
		return fmt.Errorf("PROBE_LOWER required")
	}
	blobs, err := blobstore.New(ctx, cfg)
	if err != nil {
		return err
	}
	log, err := kafkalog.New(cfg)
	if err != nil {
		return err
	}
	reg, err := registry.New(ctx, cfg)
	if err != nil {
		return err
	}

	// Upper built purely from events: an overlay file with S3-backed bytes,
	// and a whiteout over a lower file.
	u := upper.New()
	events := []*kfusev1.EventEnvelope{
		{Seq: 1, SessionId: "probe", LowerId: "probe-lower",
			Op: &kfusev1.EventEnvelope_SessionStart{SessionStart: &kfusev1.SessionStart{LowerId: "probe-lower"}}},
		{Seq: 2, SessionId: "probe", LowerId: "probe-lower",
			Op: &kfusev1.EventEnvelope_Create{Create: &kfusev1.Create{Path: "overlay.txt", Mode: 0o644}}},
	}
	if err := u.Apply(events[0]); err != nil {
		return err
	}
	if err := u.Apply(events[1]); err != nil {
		return err
	}
	payload := []byte("overlay bytes via S3")
	blobID, err := blobs.Put(ctx, payload)
	if err != nil {
		return err
	}
	writeEv := &kfusev1.EventEnvelope{Seq: 3, SessionId: "probe", LowerId: "probe-lower",
		Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
			Path: "overlay.txt", Offset: 0, Length: uint64(len(payload)), BlobId: []byte(blobID), Eof: true}}}
	if err := u.Apply(writeEv); err != nil {
		return err
	}
	unlinkEv := &kfusev1.EventEnvelope{Seq: 4, SessionId: "probe", LowerId: "probe-lower",
		Op: &kfusev1.EventEnvelope_Unlink{Unlink: &kfusev1.Unlink{Path: "hidden.txt"}}}
	if err := u.Apply(unlinkEv); err != nil {
		return err
	}

	sess := session.NewWithState(registry.Session{ID: "probe", LowerID: "probe-lower"}, u, blobs, log, reg)
	m := &fs.Mounter{LowerPath: lower, Session: sess, Blobs: blobs}
	server, err := m.Mount()
	if err != nil {
		return fmt.Errorf("mount: %w", err)
	}
	defer func() { _ = m.Close() }()
	defer func() { _ = server.Unmount() }()

	checks := []struct {
		name  string
		check func() error
	}{
		{"lower passthrough read", func() error {
			return readEqual(filepath.Join(lower, "base.txt"), "lower-base-content")
		}},
		{"lower nested read", func() error {
			return readEqual(filepath.Join(lower, "dir", "n.txt"), "nested")
		}},
		{"lower symlink readlink", func() error {
			target, err := os.Readlink(filepath.Join(lower, "link.txt"))
			if err != nil {
				return err
			}
			if target != "base.txt" {
				return fmt.Errorf("link target %q", target)
			}
			return nil
		}},
		{"overlay blob read", func() error {
			return readEqual(filepath.Join(lower, "overlay.txt"), string(payload))
		}},
		{"whiteout hides lower", func() error {
			if _, err := os.Stat(filepath.Join(lower, "hidden.txt")); err == nil {
				return fmt.Errorf("hidden.txt should be whiteouted")
			}
			return nil
		}},
		{"readdir merge", func() error {
			entries, err := os.ReadDir(lower)
			if err != nil {
				return err
			}
			names := map[string]bool{}
			for _, e := range entries {
				names[e.Name()] = true
			}
			for _, want := range []string{"base.txt", "dir", "link.txt", "overlay.txt"} {
				if !names[want] {
					return fmt.Errorf("merged readdir missing %q (got %v)", want, names)
				}
			}
			if names["hidden.txt"] {
				return fmt.Errorf("whiteouted name present in readdir")
			}
			return nil
		}},
	}
	for _, c := range checks {
		if err := c.check(); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		fmt.Println("probe ok:", c.name)
	}
	return nil
}

func readEqual(path, want string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(b) != want {
		return fmt.Errorf("%s: got %q want %q", path, b, want)
	}
	return nil
}
