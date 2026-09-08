// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// analyze turns the JSONL emitted by loadgen (client-side samples) and the
// kfuse daemon (KF_PERF_LOG) into a machine-readable results.json plus a
// human-readable results.md. Regressions are tracked by diffing results.json
// across runs.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sample is one JSONL line from either producer. Latency lines carry "ns";
// summary lines (throughput, resume, branch, checkpoint, mem) carry their own
// fields and are passed through.
type sample struct {
	Ev      string `json:"ev"`
	Ns      int64  `json:"ns"`
	Bytes   int64  `json:"bytes"`
	Label   string `json:"label"`
	Op      string `json:"op"`
	Overlay *bool  `json:"overlay"`
}

// series is the percentile summary of one (scenario, event, label) group.
type series struct {
	Count  int     `json:"count"`
	MeanUs float64 `json:"mean_us"`
	P50Us  float64 `json:"p50_us"`
	P95Us  float64 `json:"p95_us"`
	P99Us  float64 `json:"p99_us"`
	MinUs  float64 `json:"min_us"`
	MaxUs  float64 `json:"max_us"`
}

// summaryEvents are passed through raw instead of being reduced to
// percentiles: each occurrence is individually meaningful.
var summaryEvents = map[string]bool{
	"throughput": true, "resume": true, "branch": true,
	"checkpoint": true, "mem": true,
}

// phaseFields are the latency sub-phases carried on commit/resume/checkpoint
// events; each becomes its own series named "<ev>.<field>".
var phaseFields = []string{
	"kafka_append_ns", "apply_ns", "image_load_ns", "log_read_ns",
	"marshal_ns", "upload_ns",
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: analyze <jsonl-dir> <out-dir>")
		os.Exit(2)
	}
	inDir, outDir := os.Args[1], os.Args[2]
	files, err := filepath.Glob(filepath.Join(inDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "analyze: no *.jsonl under %s\n", inDir)
		os.Exit(1)
	}
	sort.Strings(files)

	latencies := map[string]map[string][]int64{} // scenario -> series key -> ns samples
	summaries := map[string][]map[string]any{}   // scenario -> raw summary events
	for _, path := range files {
		scenario := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if err := readFile(path, scenario, latencies, summaries); err != nil {
			fmt.Fprintf(os.Stderr, "analyze: %s: %v\n", path, err)
			os.Exit(1)
		}
	}

	type scenarioResult struct {
		Series    map[string]series `json:"series,omitempty"`
		Summaries []map[string]any  `json:"summaries,omitempty"`
	}
	results := map[string]scenarioResult{}
	for scenario, groups := range latencies {
		r := results[scenario]
		r.Series = map[string]series{}
		for key, ns := range groups {
			r.Series[key] = summarize(ns)
		}
		r.Summaries = summaries[scenario]
		results[scenario] = r
	}
	for scenario, sums := range summaries {
		if _, ok := results[scenario]; !ok {
			results[scenario] = scenarioResult{Summaries: sums}
		}
	}

	out := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"unit_note":    "latency series in microseconds; *_ns fields in summaries are nanoseconds",
		"scenarios":    results,
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
	jsonBytes, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), append(jsonBytes, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
	md := renderMarkdown(results)
	if err := os.WriteFile(filepath.Join(outDir, "results.md"), []byte(md), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
	fmt.Printf("analyze: wrote %s and results.md (%d scenarios)\n",
		filepath.Join(outDir, "results.json"), len(results))
}

func readFile(path, scenario string, latencies map[string]map[string][]int64, summaries map[string][]map[string]any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s sample
		if err := json.Unmarshal(line, &s); err != nil {
			continue // tolerate torn last line from a killed daemon
		}
		if summaryEvents[s.Ev] {
			var raw map[string]any
			if err := json.Unmarshal(line, &raw); err != nil {
				continue
			}
			summaries[scenario] = append(summaries[scenario], raw)
			// resume/checkpoint phases also feed latency series so their
			// costs show up in the percentile tables.
			addPhases(latencies, scenario, s.Ev, raw)
			continue
		}
		key := s.Ev
		if s.Op != "" {
			key += "." + s.Op
		}
		if s.Overlay != nil {
			if *s.Overlay {
				key += "[overlay]"
			} else {
				key += "[lower]"
			}
		}
		if s.Label != "" {
			key += "{" + s.Label + "}"
		}
		if s.Ns > 0 {
			addSample(latencies, scenario, key, s.Ns)
		}
		if s.Ev == "commit" {
			var raw map[string]any
			if err := json.Unmarshal(line, &raw); err != nil {
				continue
			}
			addPhases(latencies, scenario, key, raw)
		}
	}
	return sc.Err()
}

func addPhases(latencies map[string]map[string][]int64, scenario, base string, raw map[string]any) {
	for _, field := range phaseFields {
		if v, ok := raw[field].(float64); ok {
			addSample(latencies, scenario, base+"."+strings.TrimSuffix(field, "_ns"), int64(v))
		}
	}
}

func addSample(latencies map[string]map[string][]int64, scenario, key string, ns int64) {
	if latencies[scenario] == nil {
		latencies[scenario] = map[string][]int64{}
	}
	latencies[scenario][key] = append(latencies[scenario][key], ns)
}

func summarize(ns []int64) series {
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	var sum int64
	for _, v := range ns {
		sum += v
	}
	us := func(v int64) float64 { return float64(v) / 1e3 }
	pct := func(p float64) float64 {
		idx := int(p * float64(len(ns)-1))
		return us(ns[idx])
	}
	return series{
		Count:  len(ns),
		MeanUs: us(sum) / float64(len(ns)),
		P50Us:  pct(0.50),
		P95Us:  pct(0.95),
		P99Us:  pct(0.99),
		MinUs:  us(ns[0]),
		MaxUs:  us(ns[len(ns)-1]),
	}
}

func renderMarkdown[T any](results map[string]T) string {
	var b strings.Builder
	b.WriteString("# kfuse benchmark results\n\nGenerated " + time.Now().UTC().Format(time.RFC3339) + ". Latencies in microseconds (µs).\n")
	scenarios := make([]string, 0, len(results))
	for s := range results {
		scenarios = append(scenarios, s)
	}
	sort.Strings(scenarios)
	for _, scenario := range scenarios {
		b.WriteString("\n## " + scenario + "\n")
		raw, _ := json.Marshal(results[scenario])
		var r struct {
			Series    map[string]series `json:"series"`
			Summaries []map[string]any  `json:"summaries"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		if len(r.Series) > 0 {
			b.WriteString("\n| series | count | mean | p50 | p95 | p99 | max |\n|---|---|---|---|---|---|---|\n")
			keys := make([]string, 0, len(r.Series))
			for k := range r.Series {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				s := r.Series[k]
				fmt.Fprintf(&b, "| `%s` | %d | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
					k, s.Count, s.MeanUs, s.P50Us, s.P95Us, s.P99Us, s.MaxUs)
			}
		}
		if len(r.Summaries) > 0 {
			b.WriteString("\n```json\n")
			for _, s := range r.Summaries {
				line, _ := json.Marshal(s)
				b.Write(line)
				b.WriteByte('\n')
			}
			b.WriteString("```\n")
		}
	}
	return b.String()
}
