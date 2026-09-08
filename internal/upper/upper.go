// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package upper implements the pure logical overlay state: inode table,
// extent maps, and whiteouts. Apply is a pure function of an ordered event
// list; it never performs I/O and never consults the lower tree.
package upper

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type Kind int

const (
	KindFile Kind = iota
	KindDir
	KindSymlink
)

// Extent is a sparse range of a file backed by one S3 blob.
type Extent struct {
	FileOffset int64  `json:"file_offset"`
	Length     uint64 `json:"length"`
	BlobID     []byte `json:"blob_id"` // hex sha256 of the blob object
	BlobOffset uint64 `json:"blob_offset"`
}

func (e Extent) End() int64 { return e.FileOffset + int64(e.Length) }

// Node is one overlay-created (or overlay-modified) filesystem node.
type Node struct {
	Kind    Kind     `json:"kind"`
	Mode    uint32   `json:"mode"`
	UID     uint32   `json:"uid"`
	GID     uint32   `json:"gid"`
	Size    int64    `json:"size"` // files only; -1 = unknown (derive from extents)
	Target  string   `json:"target,omitempty"`
	Extents []Extent `json:"extents,omitempty"`
	AtimeNs int64    `json:"atime_ns,omitempty"`
	MtimeNs int64    `json:"mtime_ns,omitempty"`
	// Fallthrough marks a file materialized over a lower-backed path: gaps in
	// the extent map read through to the lower file (sparse CoW).
	Fallthrough bool `json:"fallthrough,omitempty"`
	// LowerRel is the lower path a fallthrough file reads holes from. It is
	// fixed at materialization so a rename keeps holes reading the original
	// lower location (instead of the new path).
	LowerRel string `json:"lower_rel,omitempty"`
}

// AttrOverride records a Setattr applied to a lower-backed path that has no
// overlay node yet. Getattr merges it over the lower's real stat.
type AttrOverride struct {
	Mode    *uint32 `json:"mode,omitempty"`
	UID     *uint32 `json:"uid,omitempty"`
	GID     *uint32 `json:"gid,omitempty"`
	AtimeNs *int64  `json:"atime_ns,omitempty"`
	MtimeNs *int64  `json:"mtime_ns,omitempty"`
	Size    *int64  `json:"size,omitempty"` // logical truncation of a lower file
}

// Upper is the overlay state. Paths are cleaned relative paths (see
// NormalizePath). It is safe for concurrent use: Apply takes the write lock,
// every reader takes the read lock. Readers get copies of nodes and overrides,
// never the stored pointers, so a later Apply cannot mutate state a caller is
// still inspecting.
type Upper struct {
	mu        sync.RWMutex
	lowerID   string
	nodes     map[string]*Node
	whiteouts map[string]bool
	overrides map[string]*AttrOverride
	redirects map[string]string // to -> lower source (renamed lower-backed paths)
	seqLast   uint64
}

func New() *Upper {
	return &Upper{
		nodes:     map[string]*Node{},
		whiteouts: map[string]bool{},
		overrides: map[string]*AttrOverride{},
		redirects: map[string]string{},
	}
}

var (
	ErrSeqOutOfOrder = errors.New("upper: event seq out of order")
	ErrInvalidPath   = errors.New("upper: invalid path")
	ErrUnimplemented = errors.New("upper: op not implemented")
	ErrNotFile       = errors.New("upper: path is not a regular file")
	ErrIsDir         = errors.New("upper: path is a directory")
	ErrNotDir        = errors.New("upper: path is not a directory")
	ErrExists        = errors.New("upper: path already exists")
	ErrNotEmpty      = errors.New("upper: rmdir of non-empty dir")
	ErrNoEntry       = errors.New("upper: no such path")
)

// NormalizePath validates and canonicalizes an event path: relative to the
// lower root, '/'-separated, no leading '/', no '.'/'..' components, UTF-8.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidPath)
	}
	if !utf8.ValidString(p) {
		return "", fmt.Errorf("%w: non-UTF-8", ErrInvalidPath)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: absolute path %q", ErrInvalidPath, p)
	}
	// Every component must be a real name: a path that passes is already
	// canonical, so it is returned unchanged.
	for _, c := range strings.Split(p, "/") {
		switch c {
		case "", ".":
			return "", fmt.Errorf("%w: path %q contains empty or '.' component", ErrInvalidPath, p)
		case "..":
			return "", fmt.Errorf("%w: path %q contains '..'", ErrInvalidPath, p)
		}
	}
	return p, nil
}

func (u *Upper) LowerID() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.lowerID
}

func (u *Upper) SeqLast() uint64 {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.seqLast
}

// clone returns a deep copy of n (including each extent's BlobID bytes), so
// callers never hold memory Apply or another caller can mutate underneath
// them.
func (n *Node) clone() *Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Extents != nil {
		c.Extents = make([]Extent, len(n.Extents))
		for i, e := range n.Extents {
			if e.BlobID != nil {
				e.BlobID = append([]byte(nil), e.BlobID...)
			}
			c.Extents[i] = e
		}
	}
	return &c
}

// clone returns a deep copy of ov.
func (ov *AttrOverride) clone() *AttrOverride {
	if ov == nil {
		return nil
	}
	c := AttrOverride{}
	copyU32 := func(p *uint32) *uint32 {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	copyI64 := func(p *int64) *int64 {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	c.Mode = copyU32(ov.Mode)
	c.UID = copyU32(ov.UID)
	c.GID = copyU32(ov.GID)
	c.AtimeNs = copyI64(ov.AtimeNs)
	c.MtimeNs = copyI64(ov.MtimeNs)
	c.Size = copyI64(ov.Size)
	return &c
}

// Lookup returns a copy of the overlay node at path. A whiteout hides the
// *lower* entry only; an overlay node at the same path still wins (unlink of a
// lower file followed by create at the same path is legal POSIX).
func (u *Upper) Lookup(path string) (*Node, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.lookupLocked(path)
}

func (u *Upper) lookupLocked(path string) (*Node, bool) {
	if n, ok := u.nodes[path]; ok {
		return n.clone(), true
	}
	return nil, false
}

// HasNode reports whether an overlay node exists at path.
func (u *Upper) HasNode(path string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	_, ok := u.nodes[path]
	return ok
}

// IsWhiteout reports whether path is an explicit whiteout.
func (u *Upper) IsWhiteout(path string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.whiteouts[path]
}

// Override returns a copy of the recorded attr override for a lower-backed
// path, or nil.
func (u *Upper) Override(path string) *AttrOverride {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.overrides[path].clone()
}

// Redirect returns the lower source for an exactly-redirected path (renamed
// lower-backed entry), or ok=false.
func (u *Upper) Redirect(path string) (string, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	src, ok := u.redirects[path]
	return src, ok
}

// LowerPath resolves rel's effective path in the lower tree. A redirect maps
// an entry (and its descendants) to a different lower location. Returns the
// mapped lower path and whether any redirect applied.
func (u *Upper) LowerPath(rel string) (string, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if src, ok := u.redirects[rel]; ok {
		return src, true
	}
	// Walk ancestors looking for the deepest redirect: `to/sub` under a
	// redirect `to -> from` maps to `from/sub`.
	rest := rel
	for {
		i := strings.LastIndexByte(rest, '/')
		if i < 0 {
			return rel, false
		}
		rest = rest[:i]
		if src, ok := u.redirects[rest]; ok {
			return src + rel[i:], true
		}
	}
}

// Hidden reports whether path is covered by a whiteout: at the path itself
// or under any whiteouted ancestor. Once a lower dir is rmdir'd, its whole
// lower subtree is dead — even if an upper dir node later shadows the name
// (opaque semantics). Callers must check for an upper node at path FIRST;
// nodes win over whiteouts.
func (u *Upper) Hidden(path string) bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.whiteouts[path] {
		return true
	}
	rest := path
	for {
		i := strings.LastIndexByte(rest, '/')
		if i < 0 {
			return false
		}
		rest = rest[:i]
		if u.whiteouts[rest] {
			return true
		}
	}
}

// hasChildren reports whether any upper node lives directly under dir.
func (u *Upper) hasChildren(dir string) bool {
	prefix := dir + "/"
	for p := range u.nodes {
		if strings.HasPrefix(p, prefix) && !strings.Contains(p[len(prefix):], "/") {
			return true
		}
	}
	return false
}

// Children returns copies of the overlay children of a directory and the set
// of whiteouted names at that level. Overlay nodes win over whiteouts;
// whiteouts hide only lower entries (FUSE suppresses them from the merged
// readdir).
func (u *Upper) Children(dir string) (map[string]*Node, map[string]bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	prefix := dir
	if prefix != "" {
		prefix += "/"
	}
	nodes := map[string]*Node{}
	white := map[string]bool{}
	for p, n := range u.nodes {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if rest != "" && !strings.Contains(rest, "/") {
				nodes[rest] = n.clone()
			}
		}
	}
	for p := range u.whiteouts {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if rest != "" && !strings.Contains(rest, "/") {
				if _, has := nodes[rest]; !has {
					white[rest] = true
				}
			}
		}
	}
	return nodes, white
}

// Redirected returns the names directly under dir that are lower-backed
// entries renamed into dir (redirect targets). They have no overlay node and
// no lower entry at their own path, so a merged listing must add them
// explicitly. Names also shadowed by an overlay node or a whiteout are the
// caller's to arbitrate, exactly as for Children.
func (u *Upper) Redirected(dir string) []string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	prefix := dir
	if prefix != "" {
		prefix += "/"
	}
	var names []string
	for p := range u.redirects {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if rest != "" && !strings.Contains(rest, "/") {
				names = append(names, rest)
			}
		}
	}
	sort.Strings(names)
	return names
}

// FileSize returns the logical file size: explicit size if set, else the end
// of the last extent.
func (u *Upper) FileSize(path string) (int64, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	n, ok := u.nodes[path]
	if !ok || n.Kind != KindFile {
		return 0, false
	}
	if n.Size >= 0 {
		return n.Size, true
	}
	var end int64
	for _, e := range n.Extents {
		if e.End() > end {
			end = e.End()
		}
	}
	return end, true
}
