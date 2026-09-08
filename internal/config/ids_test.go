// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	valid := []string{"abc", "1f4e0b", "lower_id-1", "a.b", strings.Repeat("a", maxIDLen)}
	for _, id := range valid {
		if err := ValidateID("session id", id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{
		"", ".", "..", "a/b", "../../etc/passwd", "a\\b", "a b", "a\x00b",
		"a\n", "sess*", "sess?", strings.Repeat("a", maxIDLen+1),
	}
	for _, id := range invalid {
		if err := ValidateID("session id", id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestValidateBlobID(t *testing.T) {
	good := strings.Repeat("ab12", 16)
	if err := ValidateBlobID(good); err != nil {
		t.Fatalf("ValidateBlobID(%q) = %v, want nil", good, err)
	}
	bad := []string{"", "blob-1", strings.Repeat("AB12", 16), strings.Repeat("ab12", 15), good + "a", "../" + good[3:]}
	for _, id := range bad {
		if err := ValidateBlobID(id); err == nil {
			t.Errorf("ValidateBlobID(%q) = nil, want error", id)
		}
	}
}
