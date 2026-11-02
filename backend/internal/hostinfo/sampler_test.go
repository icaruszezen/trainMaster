package hostinfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestParseCPUTimes(t *testing.T) {
	tests := []struct {
		in   string
		want cpuTimes
		ok   bool
	}{
		// user nice system idle iowait irq softirq steal guest guest_nice;
		// guest time is already part of user and nice.
		{"cpu  100 10 50 800 40 0 0 0 30 0\ncpu0 1 2 3 4\n", cpuTimes{total: 1000, idle: 840}, true},
		{"intr 1\ncpu 10 0 10 80\n", cpuTimes{total: 100, idle: 80}, true}, // old kernel: 4 fields
		{"cpu  1 2 3\n", cpuTimes{}, false},
		{"cpu  1 2 x 4\n", cpuTimes{}, false},
		{"cpu0 1 2 3 4\n", cpuTimes{}, false},
		{"", cpuTimes{}, false},
	}
	for _, tt := range tests {
		got, ok := parseCPUTimes(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseCPUTimes(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestUtilization(t *testing.T) {
	tests := []struct {
		prev, cur cpuTimes
		want      float64
		ok        bool
	}{
		{cpuTimes{1000, 800}, cpuTimes{2000, 1550}, 25, true},
		{cpuTimes{1000, 800}, cpuTimes{1300, 800}, 100, true},
		{cpuTimes{1000, 800}, cpuTimes{1300, 1100}, 0, true},
		{cpuTimes{0, 0}, cpuTimes{3, 2}, 33.3, true},
		{cpuTimes{1000, 800}, cpuTimes{1000, 800}, 0, false}, // no time passed
		{cpuTimes{1000, 800}, cpuTimes{900, 850}, 0, false},  // counters went back
		{cpuTimes{1000, 800}, cpuTimes{1100, 700}, 0, false},
	}
	for _, tt := range tests {
		got, ok := utilization(tt.prev, tt.cur)
		if got != tt.want || ok != tt.ok {
			t.Errorf("utilization(%+v, %+v) = %v, %v; want %v, %v", tt.prev, tt.cur, got, ok, tt.want, tt.ok)
		}
	}
}

// statFS serves a /proc/stat whose busy (user) and idle counters grow by
// busy and idle ticks on every read; failing makes reads fail.
type statFS struct {
	mu         sync.Mutex
	busy, idle uint64
	reads      uint64
	failing    bool
}

func (f *statFS) Open(name string) (fs.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "proc/stat" || f.failing {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	n := f.reads
	f.reads++
	stat := fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\ncpu0 1 1 1 1\n", 1000+n*f.busy, 1000+n*f.idle)
	return fstest.MapFS{"stat": {Data: []byte(stat)}}.Open("stat")
}

func (f *statFS) setFailing(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = v
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSamplerReportsUtilization(t *testing.T) {
	fsys := &statFS{busy: 250, idle: 750} // 25%
	s := NewSampler(fsys, time.Millisecond)
	if s.Usage() != nil {
		t.Fatal("usage before sampling should be nil")
	}
	s.Start(context.Background())
	s.Start(context.Background()) // a second Start does nothing
	waitFor(t, "a usage value", func() bool { return s.Usage() != nil })
	if u := s.Usage(); *u != 25 {
		t.Errorf("usage = %v, want 25", *u)
	}
	*s.Usage() = 99 // a copy
	if u := s.Usage(); *u != 25 {
		t.Errorf("usage changed through the returned pointer: %v", *u)
	}

	// While /proc/stat cannot be read the value is unknown, and it comes
	// back once it can.
	fsys.setFailing(true)
	waitFor(t, "usage to become unknown", func() bool { return s.Usage() == nil })
	fsys.setFailing(false)
	waitFor(t, "usage to come back", func() bool { return s.Usage() != nil })
	if u := s.Usage(); *u != 25 {
		t.Errorf("usage = %v, want 25", *u)
	}

	s.Stop()
	s.Stop() // idempotent
}

func TestSamplerStopsWithContext(t *testing.T) {
	fsys := &statFS{} // counters never move
	s := NewSampler(fsys, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	waitFor(t, "a few readings", func() bool {
		fsys.mu.Lock()
		defer fsys.mu.Unlock()
		return fsys.reads > 3
	})
	cancel()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sampling goroutine did not exit after the context was cancelled")
	}
	// Without elapsed ticks there is no value.
	if s.Usage() != nil {
		t.Errorf("usage = %v, want nil", *s.Usage())
	}
	s.Stop()
}

func TestSamplerWithoutFS(t *testing.T) {
	s := NewSampler(nil, time.Millisecond)
	s.Start(context.Background())
	s.Stop()
	if s.Usage() != nil {
		t.Error("usage without a file system should be nil")
	}
	NewSampler(nil, time.Second).Stop() // Stop without Start
}

func TestHostUsesSampler(t *testing.T) {
	h := New(Options{FS: &statFS{busy: 50, idle: 50}, SampleInterval: time.Millisecond})
	h.Start(context.Background())
	defer h.Stop()
	waitFor(t, "usage in Info", func() bool { return h.Info().CPU.UsagePercent != nil })
	if u := *h.Info().CPU.UsagePercent; u != 50 {
		t.Errorf("usage = %v, want 50", u)
	}
}

func TestStatFSFailure(t *testing.T) {
	fsys := &statFS{failing: true}
	if _, ok := readCPUTimes(fsys); ok {
		t.Error("read should fail")
	}
	if _, err := fs.ReadFile(fsys, "proc/stat"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v", err)
	}
}
