package hostinfo

import (
	"context"
	"io/fs"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sampler measures the host's CPU utilization: it reads the counters in
// /proc/stat at a fixed interval and computes the share of non-idle time
// between the last two readings. A single reading says nothing about
// utilization, hence the background sampling.
type Sampler struct {
	fsys     fs.FS
	interval time.Duration

	mu      sync.Mutex
	usage   *float64
	started bool
	cancel  context.CancelFunc
	done    chan struct{} // closed when the sampling goroutine has exited
}

// NewSampler creates a Sampler reading proc/stat from fsys every
// interval. With a nil fsys it never reports a value.
func NewSampler(fsys fs.FS, interval time.Duration) *Sampler {
	return &Sampler{fsys: fsys, interval: interval}
}

// Start takes the first reading and keeps sampling in the background until
// ctx is cancelled or Stop is called. Calling it again does nothing.
func (s *Sampler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.fsys == nil {
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
}

// Stop ends the sampling and waits until the goroutine has exited. It may
// be called without Start and more than once.
func (s *Sampler) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Usage returns the utilization in percent over the last interval, or
// nil before two readings succeeded.
func (s *Sampler) Usage() *float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		return nil
	}
	v := *s.usage
	return &v
}

func (s *Sampler) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	prev, havePrev := readCPUTimes(s.fsys)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur, ok := readCPUTimes(s.fsys)
		if !ok {
			s.set(nil) // an old value would be misleading
			havePrev = false
			continue
		}
		if havePrev {
			if u, ok := utilization(prev, cur); ok {
				s.set(&u)
			}
		}
		prev, havePrev = cur, true
	}
}

func (s *Sampler) set(u *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = u
}

// cpuTimes are the aggregate CPU counters of /proc/stat, in clock ticks.
type cpuTimes struct {
	total, idle uint64
}

func readCPUTimes(fsys fs.FS) (cpuTimes, bool) {
	data, ok := readFile(fsys, "proc/stat")
	if !ok {
		return cpuTimes{}, false
	}
	return parseCPUTimes(data)
}

// parseCPUTimes parses the "cpu" line of /proc/stat: user nice system idle
// iowait irq softirq steal guest guest_nice (older kernels list fewer).
// guest and guest_nice are already included in user and nice, so they are
// not added again; idle time includes iowait.
func parseCPUTimes(data string) (cpuTimes, bool) {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		values := fields[1:]
		if len(values) < 4 {
			return cpuTimes{}, false
		}
		var t cpuTimes
		for i, f := range values[:min(len(values), 8)] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			t.total += v
			if i == 3 || i == 4 { // idle, iowait
				t.idle += v
			}
		}
		return t, true
	}
	return cpuTimes{}, false
}

// utilization returns the non-idle share of the time between two
// readings in percent, rounded to 0.1; false if no time passed or the
// counters went backwards (e.g. CPU hotplug).
func utilization(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	total := float64(cur.total - prev.total)
	idle := min(float64(cur.idle-prev.idle), total)
	u := (total - idle) / total * 100
	return math.Round(u*10) / 10, true
}
