// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// maxIDLen bounds ids so a hostile value cannot blow up S3 keys or paths.
const maxIDLen = 128

// ValidateID checks that id is safe to interpolate into an S3 object key and
// into a local state-dir path: no separators, no traversal, printable ASCII
// only. kind names the field for the error message.
func ValidateID(kind, id string) error {
	if id == "" {
		return fmt.Errorf("config: %s is empty", kind)
	}
	if len(id) > maxIDLen {
		return fmt.Errorf("config: %s is longer than %d bytes", kind, maxIDLen)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("config: %s %q is a path traversal", kind, id)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return fmt.Errorf("config: %s %q contains an illegal character %q", kind, id, string(c))
		}
	}
	return nil
}

// ErrInvalidBlobID is returned for a blob id that is not a lowercase hex
// sha256 digest.
var ErrInvalidBlobID = errors.New("config: invalid blob id")

// ValidateBlobID checks that blobID is exactly a lowercase hex sha256 digest,
// which is what blob object keys are built from.
func ValidateBlobID(blobID string) error {
	if len(blobID) != sha256.Size*2 {
		return fmt.Errorf("%w: want %d hex chars, got %d", ErrInvalidBlobID, sha256.Size*2, len(blobID))
	}
	for i := 0; i < len(blobID); i++ {
		c := blobID[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q is not lowercase hex", ErrInvalidBlobID, blobID)
		}
	}
	return nil
}
