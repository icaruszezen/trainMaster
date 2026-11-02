package compute

import (
	"slices"
	"sync"
	"time"
)

// MaxHistoryPoints is how many metrics samples are kept per node: ten
// minutes at the default heartbeat interval.
const MaxHistoryPoints = 60

// History keeps the most recent metrics samples of each node in memory.
// It is safe for concurrent use. Points are never modified once appended,
// so the slices it returns may share them.
type History struct {
	mu     sync.Mutex
	maxGap time.Duration
	nodes  map[int64]*ring
}

// ring holds the last MaxHistoryPoints samples of one node; points[start]
// is the oldest of the n stored, and newest is the latest time among them.
type ring struct {
	points   [MaxHistoryPoints]HistoryPoint
	start, n int
	newest   time.Time
}

// NewHistory returns an empty History. A sample that comes more than
// maxGap after the newest stored one of its node (the node was offline in
// between) starts the history of that node afresh, so samples from before
// and after an outage are never shown as one continuous curve.
func NewHistory(maxGap time.Duration) *History {
	return &History{maxGap: maxGap, nodes: make(map[int64]*ring)}
}

// Append adds a sample of a node, dropping its oldest one when full, or
// all of them after a gap longer than maxGap.
func (h *History) Append(nodeID int64, p HistoryPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.nodes[nodeID]
	if r == nil {
		r = &ring{}
		h.nodes[nodeID] = r
	}
	if r.n > 0 && p.T.Sub(r.newest) > h.maxGap {
		r.start, r.n = 0, 0
	}
	if r.n == 0 || p.T.After(r.newest) {
		r.newest = p.T
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
