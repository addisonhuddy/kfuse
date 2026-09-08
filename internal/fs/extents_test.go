// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"bytes"
	"fmt"
	"testing"

	kfusev1 "github.com/addisonhuddy/kfuse/api/kfusev1"
	"github.com/addisonhuddy/kfuse/internal/upper"
)

// fakeBlobs is an in-memory stand-in for the S3 blob store.
type fakeBlobs struct {
	data map[string][]byte
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{data: map[string][]byte{}} }

func (f *fakeBlobs) put(b []byte) string {
	id := fmt.Sprintf("blob%d", len(f.data))
	f.data[id] = b
	return id
}

func (f *fakeBlobs) get(id string) ([]byte, error) {
	b, ok := f.data[id]
	if !ok {
		return nil, fmt.Errorf("no blob %s", id)
	}
	return b, nil
}

// writeEvent applies a write of data at off through the real upper, so the
// extent bookkeeping under test is the production one.
func applyWrite(t *testing.T, u *upper.Upper, blobs *fakeBlobs, seq uint64, path string, off int64, data []byte, eof bool) {
	t.Helper()
	id := blobs.put(data)
	ev := &kfusev1.EventEnvelope{Seq: seq, Op: &kfusev1.EventEnvelope_Write{Write: &kfusev1.Write{
		Path: path, Offset: off, Length: uint64(len(data)), BlobId: []byte(id), Eof: eof,
	}}}
	if err := u.Apply(ev); err != nil {
		t.Fatalf("apply write seq %d: %v", seq, err)
	}
}

// readAll merges the whole file the way overlayHandle.Read does for a
// non-fallthrough node: a zero-filled window plus the extent overlay.
func readAll(t *testing.T, u *upper.Upper, blobs *fakeBlobs, path string, off, n int64) []byte {
	t.Helper()
	node, ok := u.Lookup(path)
	if !ok {
		t.Fatalf("no node at %s", path)
	}
	size, _ := u.FileSize(path)
	end := off + n
	if end > size {
		end = size
	}
	if end <= off {
		return nil
	}
	dst := make([]byte, end-off)
	if err := mergeExtents(node.Extents, off, dst, blobs.get); err != nil {
		t.Fatalf("mergeExtents: %v", err)
	}
	return dst
}

// A mid-file overwrite splits the original extent in two, and the tail piece
// points into the middle of the original blob (BlobOffset > 0).
func TestMergeExtentsHonorsBlobOffsetAfterSplit(t *testing.T) {
	u, blobs := upper.New(), newFakeBlobs()
	applyWrite(t, u, blobs, 1, "f", 0, []byte("ABCDEFGHIJ"), true)
	applyWrite(t, u, blobs, 2, "f", 4, []byte("xx"), false)

	if got, want := string(readAll(t, u, blobs, "f", 0, 64)), "ABCDxxGHIJ"; got != want {
		t.Fatalf("read = %q, want %q", got, want)
	}
}

// A read whose window ends inside the file must still see every extent that
// overlaps it, and must not copy bytes belonging to later extents.
func TestMergeExtentsPartialWindowAfterSplit(t *testing.T) {
	u, blobs := upper.New(), newFakeBlobs()
	applyWrite(t, u, blobs, 1, "f", 0, []byte("ABCDEFGHIJ"), true)
	applyWrite(t, u, blobs, 2, "f", 4, []byte("xx"), false)

	if got, want := string(readAll(t, u, blobs, "f", 0, 6)), "ABCDxx"; got != want {
		t.Fatalf("read(0,6) = %q, want %q", got, want)
	}
	if got, want := string(readAll(t, u, blobs, "f", 5, 3)), "xGH"; got != want {
		t.Fatalf("read(5,3) = %q, want %q", got, want)
	}
}

// Truncating clips an extent's Length; the bytes past the truncation point are
// gone and must read as holes even though the blob still holds them.
func TestMergeExtentsHonorsClippedLengthAfterTruncate(t *testing.T) {
	u, blobs := upper.New(), newFakeBlobs()
	applyWrite(t, u, blobs, 1, "f", 0, []byte("ABCDEFGHIJ"), true)
	size := uint64(3)
	sa := &kfusev1.Setattr{Path: "f", Size: &size}
	if err := u.Apply(&kfusev1.EventEnvelope{Seq: 2, Op: &kfusev1.EventEnvelope_Setattr{Setattr: sa}}); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	applyWrite(t, u, blobs, 3, "f", 9, []byte("Z"), true)

	got := readAll(t, u, blobs, "f", 0, 64)
	want := append(append([]byte("ABC"), bytes.Repeat([]byte{0}, 6)...), 'Z')
	if !bytes.Equal(got, want) {
		t.Fatalf("read = %q, want %q", got, want)
	}
}

func TestExtentBytesRejectsBlobOverrun(t *testing.T) {
	ext := upper.Extent{FileOffset: 0, Length: 8, BlobID: []byte("b"), BlobOffset: 4}
	if _, err := extentBytes(ext, []byte("short")); err == nil {
		t.Fatal("want error when an extent runs past the end of its blob")
	}
}
