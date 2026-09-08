// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package upper

import (
	"encoding/json"
	"sort"
)

// Marshal produces a stable, deterministic JSON encoding of the upper state
// (sorted nodes and whiteouts). Two replays of the same event list produce
// byte-identical output.
func (u *Upper) Marshal() ([]byte, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return json.Marshal(u.view())
}

type nodeEntry struct {
	Path string `json:"path"`
	Node *Node  `json:"node"`
}

type overrideEntry struct {
	Path     string        `json:"path"`
	Override *AttrOverride `json:"override"`
}

type upperView struct {
	LowerID   string          `json:"lower_id"`
	SeqLast   uint64          `json:"seq_last"`
	Nodes     []nodeEntry     `json:"nodes"`
	Whiteouts []string        `json:"whiteouts"`
	Overrides []overrideEntry `json:"overrides,omitempty"`
	Redirects []redirectEntry `json:"redirects,omitempty"`
}

type redirectEntry struct {
	To   string `json:"to"`
	From string `json:"from"`
}

func (u *Upper) view() upperView {
	v := upperView{LowerID: u.lowerID, SeqLast: u.seqLast, Nodes: []nodeEntry{}, Whiteouts: []string{}, Overrides: []overrideEntry{}, Redirects: []redirectEntry{}}
	for p, n := range u.nodes {
		v.Nodes = append(v.Nodes, nodeEntry{p, n})
	}
	for p := range u.whiteouts {
		v.Whiteouts = append(v.Whiteouts, p)
	}
	for p, ov := range u.overrides {
		v.Overrides = append(v.Overrides, overrideEntry{p, ov})
	}
	for to, from := range u.redirects {
		v.Redirects = append(v.Redirects, redirectEntry{to, from})
	}
	sort.Slice(v.Nodes, func(i, j int) bool { return v.Nodes[i].Path < v.Nodes[j].Path })
	sort.Strings(v.Whiteouts)
	sort.Slice(v.Overrides, func(i, j int) bool { return v.Overrides[i].Path < v.Overrides[j].Path })
	sort.Slice(v.Redirects, func(i, j int) bool { return v.Redirects[i].To < v.Redirects[j].To })
	return v
}

// UnmarshalState reconstructs an upper from Marshal output (used by state images).
func UnmarshalState(b []byte) (*Upper, error) {
	var v upperView
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	u := New()
	u.lowerID = v.LowerID
	u.seqLast = v.SeqLast
	u.nodes = map[string]*Node{}
	for _, e := range v.Nodes {
		u.nodes[e.Path] = e.Node
	}
	u.whiteouts = map[string]bool{}
	for _, p := range v.Whiteouts {
		u.whiteouts[p] = true
	}
	u.overrides = map[string]*AttrOverride{}
	for _, e := range v.Overrides {
		u.overrides[e.Path] = e.Override
	}
	u.redirects = map[string]string{}
	for _, e := range v.Redirects {
		u.redirects[e.To] = e.From
	}
	return u, nil
}
