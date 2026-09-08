// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"bytes"
	"cmp"
	"fmt"
	"slices"
	"strings"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

// Apply mutates the upper according to one event. It is deterministic: the
// result depends only on the ordered event list, never on wall-clock or host.
// Events are trusted (the FUSE layer enforces merge-view semantics before
// emitting them); Apply only rejects protocol violations (bad seq, bad path,
// op/kind mismatches) so replay is deterministic. Every precondition lives in
// checkLocked (shared with Check); the mutation below assumes they hold.
func (u *Upper) Apply(ev *kfusev1.EventEnvelope) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if ev.Seq <= u.seqLast {
		return fmt.Errorf("%w: got %d after %d", ErrSeqOutOfOrder, ev.Seq, u.seqLast)
	}
	if err := u.checkLocked(ev); err != nil {
		return err
	}
	if ev.LowerId != "" {
		u.lowerID = ev.LowerId
	}
	switch op := ev.Op.(type) {
	case *kfusev1.EventEnvelope_SessionStart:
		// Audit/lineage marker; no state mutation.
	case *kfusev1.EventEnvelope_Create:
		path := mustNormalize(op.Create.Path)
		u.nodes[path] = &Node{
			Kind: KindFile,
			Mode: op.Create.Mode,
			UID:  op.Create.Uid,
			GID:  op.Create.Gid,
			Size: 0,
		}
	case *kfusev1.EventEnvelope_Write:
		path := mustNormalize(op.Write.Path)
		n, ok := u.nodes[path]
		if !ok {
			n = &Node{Kind: KindFile, Mode: op.Write.Mode, Size: 0}
			if op.Write.Fallthrough {
				n.Size = -1 // unknown: derived from extents + lower size
				n.Fallthrough = true
				n.LowerRel = path
				// Sparse CoW materializes over a lower file: carry the lower's
				// mode/owner so the overlay reports the same attrs as before.
				n.UID = op.Write.Uid
				n.GID = op.Write.Gid
				// A prior chmod/touch on the lower file recorded an override;
				// carry it onto the materialized node so it isn't lost.
				u.absorbOverride(path, n)
			}
			u.nodes[path] = n
		}
		if op.Write.Length > 0 {
			n.Extents = insertExtent(n.Extents, Extent{
				FileOffset: op.Write.Offset,
				Length:     op.Write.Length,
				BlobID:     append([]byte(nil), op.Write.BlobId...),
				BlobOffset: op.Write.BlobOffset,
			})
		}
		end := op.Write.Offset + int64(op.Write.Length)
		if op.Write.Eof {
			n.Size = end
			n.Extents = clipExtents(n.Extents, end)
		} else if !n.Fallthrough && n.Size < end {
			// Non-fallthrough files track an explicit size. Fallthrough files
			// keep Size == -1 (derived from extents + lower size) until an
			// explicit truncation.
			n.Size = end
		}
	case *kfusev1.EventEnvelope_Unlink:
		path := mustNormalize(op.Unlink.Path)
		if _, ok := u.nodes[path]; ok {
			delete(u.nodes, path)
		} else {
			u.whiteouts[path] = true
		}
	case *kfusev1.EventEnvelope_Mkdir:
		path := mustNormalize(op.Mkdir.Path)
		u.nodes[path] = &Node{
			Kind: KindDir,
			Mode: op.Mkdir.Mode,
			UID:  op.Mkdir.Uid,
			GID:  op.Mkdir.Gid,
		}
	case *kfusev1.EventEnvelope_Rmdir:
		path := mustNormalize(op.Rmdir.Path)
		if _, ok := u.nodes[path]; ok {
			delete(u.nodes, path)
		} else {
			u.whiteouts[path] = true
		}
	case *kfusev1.EventEnvelope_Symlink:
		path := mustNormalize(op.Symlink.Path)
		u.nodes[path] = &Node{
			Kind:   KindSymlink,
			Mode:   0o777,
			Target: op.Symlink.Target,
			UID:    op.Symlink.Uid,
			GID:    op.Symlink.Gid,
		}
	case *kfusev1.EventEnvelope_Rename:
		u.applyRename(op.Rename)
	case *kfusev1.EventEnvelope_Setattr:
		u.applySetattr(op.Setattr)
	}
	u.seqLast = ev.Seq
	return nil
}

// mustNormalize is NormalizePath for a path checkLocked already accepted.
func mustNormalize(p string) string {
	path, err := NormalizePath(p)
	if err != nil {
		panic("upper: unchecked path reached apply: " + err.Error())
	}
	return path
}

// Replay applies an ordered event list to a fresh upper.
func Replay(events []*kfusev1.EventEnvelope) (*Upper, error) {
	u := New()
	for _, ev := range events {
		if err := u.Apply(ev); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// insertExtent inserts e, replacing any overlapping ranges (new write wins),
// keeping the list sorted and non-overlapping.
func insertExtent(extents []Extent, e Extent) []Extent {
	if e.Length == 0 {
		return extents
	}
	eStart, eEnd := e.FileOffset, e.End()
	out := make([]Extent, 0, len(extents)+2)
	for _, x := range extents {
		if !overlaps(x, eStart, eEnd) {
			out = append(out, x)
			continue
		}
		// Drop the overlapping range, but keep the parts outside e.
		if x.FileOffset < eStart {
			out = append(out, Extent{x.FileOffset, uint64(eStart - x.FileOffset), x.BlobID, x.BlobOffset})
		}
		if x.End() > eEnd {
			keepOff := eEnd - x.FileOffset
			out = append(out, Extent{eEnd, uint64(x.End() - eEnd), x.BlobID, x.BlobOffset + uint64(keepOff)})
		}
	}
	out = append(out, e)
	// The result is non-overlapping, so sorting by start offset is a total
	// order: replay stays deterministic and readers can binary-search / stop
	// at the first extent past their window.
	slices.SortFunc(out, func(a, b Extent) int { return cmp.Compare(a.FileOffset, b.FileOffset) })
	return coalesce(out)
}

// coalesce merges neighbours that describe one contiguous run of a single
// blob. Sequential and re-written workloads otherwise grow the list by one
// entry per write forever: the list is checkpointed and replayed per node, so
// its size is a memory and event-size cost, not just a lookup cost.
//
// Input must be sorted and non-overlapping, as insertExtent guarantees.
func coalesce(extents []Extent) []Extent {
	if len(extents) < 2 {
		return extents
	}
	out := extents[:1]
	for _, x := range extents[1:] {
		last := &out[len(out)-1]
		if adjacent(*last, x) {
			last.Length += x.Length
			continue
		}
		out = append(out, x)
	}
	return out
}

// adjacent reports whether b continues a in both the file and the blob, so
// one extent can describe both.
func adjacent(a, b Extent) bool {
	return a.End() == b.FileOffset &&
		bytes.Equal(a.BlobID, b.BlobID) &&
		a.BlobOffset+a.Length == b.BlobOffset
}

func overlaps(x Extent, s, e int64) bool { return x.End() > s && x.FileOffset < e }

// absorbOverride copies a recorded attr override for path onto n and removes
// it, used when a lower-backed path is materialized into an overlay node.
func (u *Upper) absorbOverride(path string, n *Node) {
	ov := u.overrides[path]
	if ov == nil {
		return
	}
	if ov.Mode != nil {
		n.Mode = *ov.Mode
	}
	if ov.UID != nil {
		n.UID = *ov.UID
	}
	if ov.GID != nil {
		n.GID = *ov.GID
	}
	if ov.AtimeNs != nil {
		n.AtimeNs = *ov.AtimeNs
	}
	if ov.MtimeNs != nil {
		n.MtimeNs = *ov.MtimeNs
	}
	delete(u.overrides, path)
}

// applySetattr mutates an overlay node in place, or records an override when
// the path has no overlay node (lower-backed). When a lower-backed file is
// truncated (sa.Size != nil), it materializes a fallthrough node with explicit
// size so reads beyond the truncated length return EOF while lower bytes
// within size continue to fall through.
func (u *Upper) applySetattr(sa *kfusev1.Setattr) {
	path := mustNormalize(sa.Path)
	if n, ok := u.nodes[path]; ok {
		if sa.Mode != nil {
			n.Mode = *sa.Mode & 0o7777
		}
		if sa.Uid != nil {
			n.UID = *sa.Uid
		}
		if sa.Gid != nil {
			n.GID = *sa.Gid
		}
		if sa.AtimeNs != nil {
			n.AtimeNs = *sa.AtimeNs
		}
		if sa.MtimeNs != nil {
			n.MtimeNs = *sa.MtimeNs
		}
		if sa.Size != nil {
			n.Size = int64(*sa.Size)
			n.Extents = clipExtents(n.Extents, n.Size)
		}
		return
	}

	// Pure lower path: if this is a truncate, materialize a fallthrough node.
	if sa.Size != nil {
		n := &Node{
			Kind:        KindFile,
			Mode:        0o644,
			Size:        int64(*sa.Size),
			Fallthrough: true,
			LowerRel:    path,
		}
		u.absorbOverride(path, n)
		if sa.Mode != nil {
			n.Mode = *sa.Mode & 0o7777
		}
		if sa.Uid != nil {
			n.UID = *sa.Uid
		}
		if sa.Gid != nil {
			n.GID = *sa.Gid
		}
		if sa.AtimeNs != nil {
			n.AtimeNs = *sa.AtimeNs
		}
		if sa.MtimeNs != nil {
			n.MtimeNs = *sa.MtimeNs
		}
		u.nodes[path] = n
		return
	}

	ov := u.overrides[path]
	if ov == nil {
		ov = &AttrOverride{}
		u.overrides[path] = ov
	}
	if sa.Mode != nil {
		m := *sa.Mode & 0o7777
		ov.Mode = &m
	}
	if sa.Uid != nil {
		ov.UID = sa.Uid
	}
	if sa.Gid != nil {
		ov.GID = sa.Gid
	}
	if sa.AtimeNs != nil {
		ov.AtimeNs = sa.AtimeNs
	}
	if sa.MtimeNs != nil {
		ov.MtimeNs = sa.MtimeNs
	}
}

// applyRename moves `from` to `to`. Overlay nodes (and redirects, whiteouts,
// overrides) under `from` are rewritten to live under `to`; a purely
// lower-backed `from` becomes a redirect (to -> from) so its bytes/children
// keep falling through. `from` is whiteouted when it was lower-backed; `to`
// is always cleared first.
func (u *Upper) applyRename(r *kfusev1.Rename) {
	from, to := mustNormalize(r.From), mustNormalize(r.To)
	if from == to {
		return
	}
	// Clear the destination: upper nodes/redirects/whiteouts/overrides at/under
	// `to` are replaced wholesale.
	deleteSubtree(u, to)

	hasUpper := u.subtreePresent(from)
	if hasUpper {
		moveSubtree(u, from, to)
	}
	if r.FromLower {
		u.whiteouts[from] = true
	}
	if !hasUpper {
		// Pure lower rename: redirect `to` to the lower `from`. Overrides and
		// whiteouts under `from` move along so their merged view persists.
		moveWhiteouts(u, from, to)
		moveOverrides(u, from, to)
		u.redirects[to] = from
	}
}

func isDescendant(child, parent string) bool {
	return strings.HasPrefix(child, parent+"/")
}

func prefixMatch(p, dir string) (string, bool) {
	if p == dir {
		return "", true
	}
	if strings.HasPrefix(p, dir+"/") {
		return p[len(dir)+1:], true
	}
	return "", false
}

// subtreePresent reports whether any overlay node or redirect lives at `dir`
// or under it. Overrides do not count: an attr tweak on a lower file is still
// a lower-backed rename (redirect), not an overlay move.
func (u *Upper) subtreePresent(dir string) bool {
	for p := range u.nodes {
		if _, ok := prefixMatch(p, dir); ok {
			return true
		}
	}
	for p := range u.redirects {
		if _, ok := prefixMatch(p, dir); ok {
			return true
		}
	}
	return false
}

// moveSubtree rewrites every overlay node, redirect, whiteout, and override
// at/under `from` to the corresponding path under `to`. The `from` key itself
// is not whiteouted here (callers decide that from FromLower). Keys are
// snapshotted first so map-range mutation is safe.
func moveSubtree(u *Upper, from, to string) {
	moveNodes(u, from, to)
	moveRedirects(u, from, to)
	moveWhiteouts(u, from, to)
	moveOverrides(u, from, to)
}

func moveNodes(u *Upper, from, to string) {
	for _, p := range snapshotNodeKeys(u.nodes) {
		if rest, ok := prefixMatch(p, from); ok {
			u.nodes[toPath(to, rest)] = u.nodes[p]
			delete(u.nodes, p)
		}
	}
}

func moveRedirects(u *Upper, from, to string) {
	for _, p := range snapshotRedirectKeys(u.redirects) {
		if rest, ok := prefixMatch(p, from); ok {
			u.redirects[toPath(to, rest)] = u.redirects[p]
			delete(u.redirects, p)
		}
	}
}

// moveWhiteouts rewrites whiteouts strictly under `from` to under `to`, so a
// whiteouted lower child stays whiteouted after its parent is renamed.
func moveWhiteouts(u *Upper, from, to string) {
	for _, p := range snapshotWhiteoutKeys(u.whiteouts) {
		if rest, ok := prefixMatch(p, from); ok && rest != "" {
			delete(u.whiteouts, p)
			u.whiteouts[toPath(to, rest)] = true
		}
	}
}

// moveOverrides rewrites attr overrides at/under `from` to under `to`, so a
// chmod/touch on a lower file stays attached after a rename.
func moveOverrides(u *Upper, from, to string) {
	for _, p := range snapshotOverrideKeys(u.overrides) {
		if rest, ok := prefixMatch(p, from); ok {
			u.overrides[toPath(to, rest)] = u.overrides[p]
			delete(u.overrides, p)
		}
	}
}

// deleteSubtree removes every overlay node, redirect, whiteout, and override
// at/under `to`, so a rename-over overwrites cleanly.
func deleteSubtree(u *Upper, to string) {
	for _, p := range snapshotNodeKeys(u.nodes) {
		if _, ok := prefixMatch(p, to); ok {
			delete(u.nodes, p)
		}
	}
	for _, p := range snapshotRedirectKeys(u.redirects) {
		if _, ok := prefixMatch(p, to); ok {
			delete(u.redirects, p)
		}
	}
	for _, p := range snapshotWhiteoutKeys(u.whiteouts) {
		if _, ok := prefixMatch(p, to); ok {
			delete(u.whiteouts, p)
		}
	}
	for _, p := range snapshotOverrideKeys(u.overrides) {
		if _, ok := prefixMatch(p, to); ok {
			delete(u.overrides, p)
		}
	}
}

func snapshotNodeKeys(m map[string]*Node) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out
}

func snapshotRedirectKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out
}

func snapshotWhiteoutKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out
}

func snapshotOverrideKeys(m map[string]*AttrOverride) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	return out
}

func toPath(dir, rest string) string {
	if rest == "" {
		return dir
	}
	return dir + "/" + rest
}

// clipExtents truncates extents to end (used on Write.eof / truncate-down).
func clipExtents(extents []Extent, end int64) []Extent {
	var out []Extent
	for _, x := range extents {
		if x.FileOffset >= end {
			continue
		}
		if x.End() > end {
			x.Length = uint64(end - x.FileOffset)
		}
		out = append(out, x)
	}
	return out
}
