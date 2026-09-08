// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"sync"
	"testing"
)

// TestConcurrentApplyAndReaders exercises the upper the way the FUSE layer
// does: many reader goroutines while a writer applies events. Run with -race.
func TestConcurrentApplyAndReaders(t *testing.T) {
	u := New()
	if err := u.Apply(ev(1, create("f.txt", 0644))); err != nil {
		t.Fatal(err)
	}

	const events = 200
	var wg sync.WaitGroup
	done := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if n, ok := u.Lookup("f.txt"); ok {
					for _, e := range n.Extents {
						_ = e.End()
					}
					// Mutating the returned copy must not corrupt the upper.
					n.Size = -42
					n.Extents = nil
				}
				u.Children("")
				u.Hidden("f.txt")
				u.IsWhiteout("f.txt")
				u.HasNode("f.txt")
				u.LowerPath("f.txt")
				u.Override("f.txt")
				u.FileSize("f.txt")
				u.SeqLast()
				u.LowerID()
				if _, err := u.Marshal(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}

	for seq := uint64(2); seq <= events; seq++ {
		if err := u.Apply(ev(seq, write("f.txt", int64(seq)*8, 8, "blob"))); err != nil {
			t.Fatalf("apply seq %d: %v", seq, err)
		}
	}
	close(done)
	wg.Wait()

	if size, ok := u.FileSize("f.txt"); !ok || size != events*8+8 {
		t.Fatalf("FileSize = %d, %v; want %d", size, ok, events*8+8)
	}
}
