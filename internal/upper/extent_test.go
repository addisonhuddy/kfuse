// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
)

func ext(off int64, length uint64, blob string, blobOff uint64) Extent {
	return Extent{FileOffset: off, Length: length, BlobID: []byte(blob), BlobOffset: blobOff}
}

func TestInsertExtentCoalescesContiguousSameBlob(t *testing.T) {
	var got []Extent
	got = insertExtent(got, ext(0, 4, "b", 0))
	got = insertExtent(got, ext(4, 6, "b", 4))
	if len(got) != 1 {
		t.Fatalf("want one extent after a contiguous append, got %d: %+v", len(got), got)
	}
	if e := got[0]; e.FileOffset != 0 || e.Length != 10 || e.BlobOffset != 0 || string(e.BlobID) != "b" {
		t.Fatalf("merged extent = %+v, want [0,10) of b at 0", e)
	}
}

// Merging is only sound when both the file range and the blob range are
// contiguous; otherwise the merged extent would read the wrong bytes.
func TestInsertExtentDoesNotCoalesceIncompatibleNeighbours(t *testing.T) {
	cases := []struct {
		name   string
		second Extent
	}{
		{"different blob", ext(4, 6, "other", 4)},
		{"gap in the blob", ext(4, 6, "b", 8)},
		{"blob restarts", ext(4, 6, "b", 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := insertExtent([]Extent{ext(0, 4, "b", 0)}, tc.second)
			if len(got) != 2 {
				t.Fatalf("want the extents kept separate, got %+v", got)
			}
		})
	}
}

// A hole punched into an extent and then refilled from the original blob has
// to collapse back to one extent, otherwise every such round trip leaves
// permanent fragmentation behind.
func TestInsertExtentRecombinesSplitExtent(t *testing.T) {
	got := insertExtent(nil, ext(0, 10, "b", 0))
	got = insertExtent(got, ext(4, 2, "hole", 0))
	if len(got) != 3 {
		t.Fatalf("interior overwrite should split into 3, got %+v", got)
	}
	got = insertExtent(got, ext(4, 2, "b", 4))
	if len(got) != 1 || got[0].Length != 10 {
		t.Fatalf("refilling the hole should collapse back to [0,10), got %+v", got)
	}
}

// Rewriting the same range over and over is the workload that used to grow
// the list without bound; the node must keep a constant number of extents.
func TestApplyKeepsExtentCountBoundedOnRepeatedWrites(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, create("f", 0o644))); err != nil {
		t.Fatal(err)
	}
	seq := uint64(1)
	next := func(w *kfusev1.Write) {
		seq++
		if err := u.Apply(ev(seq, w)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		next(&kfusev1.Write{Path: "f", Offset: 0, Length: 8, BlobId: []byte("b"), Eof: false})
		next(&kfusev1.Write{Path: "f", Offset: 8, Length: 8, BlobId: []byte("b"), BlobOffset: 8, Eof: true})
	}
	n, _ := u.Lookup("f")
	if len(n.Extents) != 1 {
		t.Fatalf("want 1 coalesced extent after repeated writes, got %d", len(n.Extents))
	}
	if n.Size != 16 {
		t.Fatalf("size = %d, want 16", n.Size)
	}
}
