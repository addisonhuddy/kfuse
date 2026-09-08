// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package fs

import (
	"fmt"

	"github.com/addisonhuddy/kfuse/internal/upper"
)

// mergeExtents overlays exts onto dst, which holds the bytes of the file range
// [off, off+len(dst)) already filled with lower/zero bytes. get fetches a blob
// by id. Extents are expected sorted by FileOffset and non-overlapping (the
// invariant upper.insertExtent maintains).
func mergeExtents(exts []upper.Extent, off int64, dst []byte, get func(string) ([]byte, error)) error {
	end := off + int64(len(dst))
	for _, ext := range exts {
		if ext.FileOffset >= end {
			break
		}
		if ext.End() <= off {
			continue
		}
		blob, err := get(string(ext.BlobID))
		if err != nil {
			return fmt.Errorf("blob %x: %w", ext.BlobID, err)
		}
		src, err := extentBytes(ext, blob)
		if err != nil {
			return err
		}
		// Clip the extent to the requested window. lo/hi are offsets into the
		// extent, so a blob shared by several extents (mid-file overwrite
		// splits one write into two extents) contributes only its own range.
		lo, hi := int64(0), int64(len(src))
		if off > ext.FileOffset {
			lo = off - ext.FileOffset
		}
		if clipped := end - ext.FileOffset; clipped < hi {
			hi = clipped
		}
		if lo >= hi {
			continue
		}
		copy(dst[ext.FileOffset+lo-off:], src[lo:hi])
	}
	return nil
}

// extentBytes returns the blob bytes an extent covers.
func extentBytes(ext upper.Extent, blob []byte) ([]byte, error) {
	lo, hi := ext.BlobOffset, ext.BlobOffset+ext.Length
	if hi > uint64(len(blob)) {
		return nil, fmt.Errorf("fs: extent [%d,%d) exceeds blob %s of length %d", lo, hi, ext.BlobID, len(blob))
	}
	return blob[lo:hi], nil
}
