// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// loadgen drives file operations against a kfuse mount and emits one JSONL
// sample per operation, measured from the client side (full FUSE round trip).
// The analyze tool turns these samples plus the daemon's KF_PERF_LOG into
// percentile tables.
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := parseFlags(os.Args[2:])
	var err error
	switch os.Args[1] {
	case "write":
		err = runWrite(args)
	case "read":
		err = runRead(args)
	case "meta":
		err = runMeta(args)
	case "throughput":
		err = runThroughput(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: loadgen <write|read|meta|throughput> [--key value ...]
  write:      --dir D --files N --writes K --size BYTES [--concurrency C]
  read:       --dir D --files N --size BYTES [--label L]
  meta:       --dir D [--label L]
  throughput: --dir D --writers N --seconds T --size BYTES`)
	os.Exit(2)
}

func parseFlags(argv []string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(argv); i += 2 {
		if len(argv[i]) > 2 && argv[i][:2] == "--" {
			m[argv[i][2:]] = argv[i+1]
		}
	}
	return m
}

func intArg(m map[string]string, key string, def int) int {
	if v, ok := m[key]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loadgen: bad --%s %q\n", key, v)
			os.Exit(2)
		}
		return n
	}
	return def
}

var emitMu sync.Mutex

// emit writes one JSONL sample to stdout.
func emit(ev string, ns int64, bytes int64, label string) {
	emitMu.Lock()
	defer emitMu.Unlock()
	fmt.Printf(`{"ts":%d,"ev":%q,"ns":%d,"bytes":%d,"label":%q}`+"\n",
		time.Now().UnixNano(), ev, ns, bytes, label)
}

func payload(size int) []byte {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)
	return buf
}

// runWrite creates N files and performs K sized writes to each, C files in
// flight at a time. Every write syscall is one sample.
func runWrite(a map[string]string) error {
	dir := a["dir"]
	files := intArg(a, "files", 32)
	writes := intArg(a, "writes", 16)
	size := intArg(a, "size", 4096)
	conc := intArg(a, "concurrency", 1)
	label := a["label"]
	if label == "" {
		label = fmt.Sprintf("%dB", size)
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < files; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			path := filepath.Join(dir, fmt.Sprintf("w-%s-%d.dat", label, i))
			if err := writeFile(path, writes, size, label); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return firstErr
}

func writeFile(path string, writes, size int, label string) error {
	data := payload(size)
	start := time.Now()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	emit("client_create", time.Since(start).Nanoseconds(), 0, label)
	defer func() { _ = f.Close() }()
	for k := 0; k < writes; k++ {
		off := int64(k) * int64(size)
		t := time.Now()
		if _, err := f.WriteAt(data, off); err != nil {
			return err
		}
		emit("client_write", time.Since(t).Nanoseconds(), int64(size), label)
	}
	return nil
}

// runRead reads N files sequentially in --size chunks; the caller labels the
// pass (e.g. cold vs warm) and controls cache state via remounts.
func runRead(a map[string]string) error {
	dir := a["dir"]
	files := intArg(a, "files", 32)
	size := intArg(a, "size", 4096)
	label := a["label"]
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	n := 0
	buf := make([]byte, size)
	for _, e := range entries {
		if e.IsDir() || n >= files {
			continue
		}
		n++
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		var off int64
		for {
			t := time.Now()
			m, err := f.ReadAt(buf, off)
			if m > 0 {
				emit("client_read", time.Since(t).Nanoseconds(), int64(m), label)
				off += int64(m)
			}
			if err != nil {
				break // io.EOF ends the file
			}
		}
		_ = f.Close()
	}
	return nil
}

// runMeta measures stat of every entry plus a full readdir of --dir.
func runMeta(a map[string]string) error {
	dir := a["dir"]
	label := a["label"]
	t := time.Now()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	emit("client_readdir", time.Since(t).Nanoseconds(), int64(len(entries)), label)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		t := time.Now()
		if _, err := os.Lstat(path); err != nil {
			return err
		}
		emit("client_stat", time.Since(t).Nanoseconds(), 0, label)
	}
	return nil
}

// runThroughput runs N writer goroutines appending --size writes to private
// files for T seconds and reports each write plus a final events/s summary.
func runThroughput(a map[string]string) error {
	dir := a["dir"]
	writers := intArg(a, "writers", 1)
	seconds := intArg(a, "seconds", 10)
	size := intArg(a, "size", 4096)
	label := fmt.Sprintf("writers=%d", writers)
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	var total int64
	var totalMu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	start := time.Now()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			data := payload(size)
			path := filepath.Join(dir, fmt.Sprintf("tp-%d-%d.dat", writers, w))
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			defer func() { _ = f.Close() }()
			var off int64
			var n int64
			for time.Now().Before(deadline) {
				t := time.Now()
				if _, err := f.WriteAt(data, off); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					return
				}
				emit("client_write", time.Since(t).Nanoseconds(), int64(size), label)
				off += int64(size)
				n++
			}
			totalMu.Lock()
			total += n
			totalMu.Unlock()
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	emitMu.Lock()
	fmt.Printf(`{"ts":%d,"ev":"throughput","writers":%d,"events":%d,"elapsed_ns":%d,"events_per_sec":%.2f,"label":%q}`+"\n",
		time.Now().UnixNano(), writers, total, elapsed.Nanoseconds(),
		float64(total)/elapsed.Seconds(), label)
	emitMu.Unlock()
	return firstErr
}
