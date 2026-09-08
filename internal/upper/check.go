// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"fmt"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

// Check reports whether Apply would accept ev against the current upper
// state, without mutating anything and without consuming a seq. It is the
// upper-state half of every mutation precondition: the session runs it under
// its commit lock before an event becomes durable, so an event Apply would
// reject (create over an existing node, rmdir of a non-empty upper dir, ...)
// never reaches the log. Lower-tree preconditions are the FUSE layer's job.
//
// Apply calls the same checks first, so the two cannot drift.
func (u *Upper) Check(ev *kfusev1.EventEnvelope) error {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.checkLocked(ev)
}

func (u *Upper) checkLocked(ev *kfusev1.EventEnvelope) error {
	if ev.LowerId != "" && u.lowerID != "" && u.lowerID != ev.LowerId {
		return fmt.Errorf("upper: lower_id changed from %q to %q", u.lowerID, ev.LowerId)
	}
	switch op := ev.Op.(type) {
	case *kfusev1.EventEnvelope_SessionStart:
		return nil
	case *kfusev1.EventEnvelope_Create:
		return u.checkFresh(op.Create.Path)
	case *kfusev1.EventEnvelope_Mkdir:
		return u.checkFresh(op.Mkdir.Path)
	case *kfusev1.EventEnvelope_Symlink:
		return u.checkFresh(op.Symlink.Path)
	case *kfusev1.EventEnvelope_Write:
		path, err := NormalizePath(op.Write.Path)
		if err != nil {
			return err
		}
		if n, ok := u.nodes[path]; ok && n.Kind != KindFile {
			return fmt.Errorf("%w: %s", ErrNotFile, path)
		}
		return nil
	case *kfusev1.EventEnvelope_Unlink:
		path, err := NormalizePath(op.Unlink.Path)
		if err != nil {
			return err
		}
		if n, ok := u.nodes[path]; ok && n.Kind == KindDir {
			return fmt.Errorf("%w: %s", ErrIsDir, path)
		}
		return nil
	case *kfusev1.EventEnvelope_Rmdir:
		path, err := NormalizePath(op.Rmdir.Path)
		if err != nil {
			return err
		}
		if n, ok := u.nodes[path]; ok {
			if n.Kind != KindDir {
				return fmt.Errorf("%w: %s", ErrNotDir, path)
			}
			if u.hasChildren(path) {
				// FUSE never allows rmdir on a non-empty dir, so in replay this
				// is a corrupt log: fail loudly instead of deleting a subtree.
				return fmt.Errorf("%w: %s (corrupt log?)", ErrNotEmpty, path)
			}
		}
		return nil
	case *kfusev1.EventEnvelope_Rename:
		return u.checkRename(op.Rename)
	case *kfusev1.EventEnvelope_Setattr:
		path, err := NormalizePath(op.Setattr.Path)
		if err != nil {
			return err
		}
		if n, ok := u.nodes[path]; ok && op.Setattr.Size != nil && n.Kind != KindFile {
			return fmt.Errorf("%w: %s", ErrNotFile, path)
		}
		return nil
	default:
		return fmt.Errorf("upper: unknown op %T", ev.Op)
	}
}

// checkFresh is the precondition shared by create/mkdir/symlink: a valid path
// with no overlay node. A whiteout at the path is fine (unlink then recreate).
func (u *Upper) checkFresh(p string) error {
	path, err := NormalizePath(p)
	if err != nil {
		return err
	}
	if _, exists := u.nodes[path]; exists {
		return fmt.Errorf("%w: %s", ErrExists, path)
	}
	return nil
}

func (u *Upper) checkRename(r *kfusev1.Rename) error {
	from, err := NormalizePath(r.From)
	if err != nil {
		return err
	}
	to, err := NormalizePath(r.To)
	if err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if isDescendant(to, from) {
		return fmt.Errorf("%w: rename dir %q into its own subtree %q", ErrInvalidPath, from, to)
	}
	if !u.subtreePresent(from) && !r.FromLower {
		return fmt.Errorf("%w: rename of %q", ErrNoEntry, from)
	}
	return nil
}
