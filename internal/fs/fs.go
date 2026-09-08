// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package fs serves the merged overlay view over FUSE: lower tree (via a
// pre-mount fd + *at syscalls) merged with the session's upper state.
// Whiteouts hide lower entries; overlay nodes win. Mutations commit
// blob→Kafka→Apply before the FUSE call returns.
package fs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"

	"github.com/addisonhuddy/kfuse/internal/blobstore"
	"github.com/addisonhuddy/kfuse/internal/session"
)

// mountAttrTimeout controls FUSE attribute and entry cache duration.
const mountAttrTimeout = 1 * time.Second

// Mounter mounts a session over a lower directory.
type Mounter struct {
	LowerPath string
	Session   *session.Session
	Blobs     *blobstore.BlobStore

	lowerDirFd int
	lowerFdSet bool
	closeOnce  sync.Once
}

// Mount opens the lower fd and mounts FUSE over lowerPath. It returns once
// the mount is live; the caller runs the server (Serve) and can Unmount it.
func (m *Mounter) Mount() (*fuse.Server, error) {
	if m.LowerPath == "/" {
		return nil, fmt.Errorf("fs: refusing to mount over /")
	}
	fd, err := unix.Open(m.LowerPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("fs: open lower %s: %w", m.LowerPath, err)
	}
	m.lowerDirFd = fd
	m.lowerFdSet = true

	root := m.newDir()
	attrTimeout := mountAttrTimeout
	nodeFS := fs.NewNodeFS(root, &fs.Options{
		AttrTimeout:  &attrTimeout,
		EntryTimeout: &attrTimeout,
	})
	server, err := fuse.NewServer(nodeFS, m.LowerPath, &fuse.MountOptions{
		FsName:        "kfuse",
		Name:          "kfuse",
		DisableXAttrs: true,
		MaxBackground: 64,
	})
	if err != nil {
		_ = m.Close()
		return nil, fmt.Errorf("fs: mount over %s: %w", m.LowerPath, err)
	}
	go server.Serve()
	_ = server.WaitMount()
	return server, nil
}

// Close releases the lower directory fd. It must be called once the server has
// stopped serving (after Server.Wait), since the *at syscalls that serve the
// lower tree use this fd. Calling it more than once is a no-op.
func (m *Mounter) Close() error {
	var err error
	m.closeOnce.Do(func() {
		if m.lowerFdSet {
			err = unix.Close(m.lowerDirFd)
			m.lowerDirFd = -1
			m.lowerFdSet = false
		}
	})
	if err != nil {
		return fmt.Errorf("fs: close lower fd: %w", err)
	}
	return nil
}

func callerOf(ctx context.Context) fuse.Caller {
	return ctx.(*fuse.Context).Caller
}
