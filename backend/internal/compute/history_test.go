package compute

import (
	"sync"
	"testing"
	"time"
)

func point(i int) HistoryPoint {
	return HistoryPoint{T: testNow.Add(time.Duration(i) * time.Second), CPUPercent: Value(float64(i)), GPUs: []HistoryGPU{}}
}

func cpuValues(points []HistoryPoint) []float64 {
	out := make([]float64, len(points))
	for i, p := range points {
		out[i] = p.CPUPercent.V
	}
	return out
}

func TestHistoryRing(t *testing.T) {
	h := NewHistory()
	if got := h.Points(1); got == nil || len(got) != 0 {
		t.Fatalf("unknown node: %#v, want empty non-nil", got)
	}
	for i := range 3 {
		h.Append(1, point(i))
	}
	if got := cpuValues(h.Points(1)); len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Errorf("points = %v", got)
	}

	// Past the capacity the oldest points go.
	for i := 3; i < MaxHistoryPoints+25; i++ {
		h.Append(1, point(i))
	}
	got := cpuValues(h.Points(1))
	if len(got) != MaxHistoryPoints || got[0] != 25 || got[MaxHistoryPoints-1] != MaxHistoryPoints+24 {
		t.Fatalf("got %d points from %v to %v", len(got), got[0], got[len(got)-1])
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("points out of order at %d: %v", i, got)
		}
	}

	// Nodes are separate, and Remove forgets one.
	h.Append(2, point(100))
	h.Remove(1)
	if n := len(h.Points(1)); n != 0 {
		t.Errorf("removed node still has %d points", n)
	}
	if got := cpuValues(h.Points(2)); len(got) != 1 || got[0] != 100 {
		t.Errorf("node 2 = %v", got)
	}
	h.Remove(42) // unknown: no-op
}

func TestHistoryPointsIsACopy(t *testing.T) {
	h := NewHistory()
	h.Append(1, point(1))
	got := h.Points(1)
	got[0].CPUPercent = Value(99.0)
	if again := h.Points(1); again[0].CPUPercent != Value(1.0) {
		t.Errorf("caller changed the stored point: %+v", again[0])
	}
}

func TestHistoryOrdersByTime(t *testing.T) {
	h := NewHistory()
	h.Append(1, point(2))
	h.Append(1, point(1)) // a heartbeat handled concurrently finished later
	h.Append(1, point(3))
	if got := cpuValues(h.Points(1)); got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("points = %v, want oldest first", got)
	}
}

// TestHistoryConcurrent is meant for -race: heartbeats, page loads and
// deletions run at the same time.
func TestHistoryConcurrent(t *testing.T) {
	h := NewHistory()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			node := int64(g % 3)
			for i := range 500 {
				h.Append(node, point(i))
				if pts := h.Points(node); len(pts) > MaxHistoryPoints {
					t.Errorf("%d points", len(pts))
					return
				}
				if i%100 == 99 {
					h.Remove(node)
				}
			}
		}()
	}
	wg.Wait()
	for node := range int64(3) {
		if n := len(h.Points(node)); n > MaxHistoryPoints {
			t.Errorf("node %d has %d points", node, n)
		}
	}
}
