package compute

import (
	"slices"
	"sync"
)

// MaxHistoryPoints is how many metrics samples are kept per node: ten
// minutes at the default heartbeat interval.
const MaxHistoryPoints = 60

// History keeps the most recent metrics samples of each node in memory.
// It is safe for concurrent use. Points are never modified once appended,
// so the slices it returns may share them.
type History struct {
	mu    sync.Mutex
	nodes map[int64]*ring
}

// ring holds the last MaxHistoryPoints samples of one node; points[start]
// is the oldest of the n stored.
type ring struct {
	points   [MaxHistoryPoints]HistoryPoint
	start, n int
}

// NewHistory returns an empty History.
func NewHistory() *History {
	return &History{nodes: make(map[int64]*ring)}
}

// Append adds a sample of a node, dropping its oldest one when full.
func (h *History) Append(nodeID int64, p HistoryPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.nodes[nodeID]
	if r == nil {
		r = &ring{}
		h.nodes[nodeID] = r
	}
	if r.n < MaxHistoryPoints {
		r.points[(r.start+r.n)%MaxHistoryPoints] = p
		r.n++
		return
	}
	r.points[r.start] = p
	r.start = (r.start + 1) % MaxHistoryPoints
}

// Points returns the samples of a node, oldest first; never nil.
func (h *History) Points(nodeID int64) []HistoryPoint {
	h.mu.Lock()
	r := h.nodes[nodeID]
	if r == nil {
		h.mu.Unlock()
		return []HistoryPoint{}
	}
	out := make([]HistoryPoint, r.n)
	for i := range out {
		out[i] = r.points[(r.start+i)%MaxHistoryPoints]
	}
	h.mu.Unlock()
	// Two heartbeats of one node handled concurrently may append out of
	// order; the contract is oldest first.
	slices.SortStableFunc(out, func(a, b HistoryPoint) int { return a.T.Compare(b.T) })
	return out
}

// Remove forgets the samples of a node.
func (h *History) Remove(nodeID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.nodes, nodeID)
}
