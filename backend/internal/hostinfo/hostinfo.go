// Package hostinfo describes the machine the platform server runs on: CPU,
// memory, load, cgroup limits and the NVIDIA GPUs the kernel knows about.
// On Linux it reads /proc and /sys through an fs.FS (so tests use
// fixtures); on other platforms it reports only what the Go runtime knows.
package hostinfo

import (
	"context"
	"io/fs"
	"os"
	"runtime"
	"time"
)

// The JSON field names of the types in this file are a contract with the
// frontend (frontend/src/features/training/); do not rename them. Unknown
// values are null.

// Server describes the machine.
type Server struct {
	Hostname      string    `json:"hostname"` // os.Hostname(); compose gives the app container the host's (TM_HOST_HOSTNAME)
	OS            string    `json:"os"`       // runtime.GOOS
	Arch          string    `json:"arch"`     // runtime.GOARCH
	Kernel        *string   `json:"kernel"`
	InContainer   bool      `json:"in_container"`
	UptimeSeconds *int64    `json:"uptime_seconds"` // since the host booted
	CPU           CPU       `json:"cpu"`
	Memory        Memory    `json:"memory"`
	LoadAverage   []float64 `json:"load_average"` // 1, 5 and 15 minutes; nil (null) when unknown
	// NVIDIA is nil when neither the NVIDIA driver nor the PCI bus shows
	// an NVIDIA GPU.
	NVIDIA *NVIDIA `json:"nvidia"`
}

// CPU describes the processors of the host.
type CPU struct {
	Model         *string  `json:"model"`
	LogicalCores  int      `json:"logical_cores"`
	PhysicalCores *int     `json:"physical_cores"`
	LimitCores    *float64 `json:"limit_cores"`   // cgroup CPU quota, when below logical_cores
	UsagePercent  *float64 `json:"usage_percent"` // whole-host utilization over the last sampling interval
}

// Memory describes the memory of the host.
type Memory struct {
	TotalBytes     *int64 `json:"total_bytes"`
	AvailableBytes *int64 `json:"available_bytes"`
	UsedBytes      *int64 `json:"used_bytes"`  // total - available
	LimitBytes     *int64 `json:"limit_bytes"` // cgroup memory limit, when below total_bytes
}

// NVIDIA lists the NVIDIA GPUs of the host. This is a hint for the
// frontend ("this machine has GPUs but no training node uses them"); the
// training nodes report the details.
type NVIDIA struct {
	// DriverVersion is nil when the driver is not loaded (the GPUs were
	// found on the PCI bus only).
	DriverVersion *string     `json:"driver_version"`
	GPUs          []NVIDIAGPU `json:"gpus"`
}

// NVIDIAGPU is one NVIDIA GPU of the host.
type NVIDIAGPU struct {
	Name  *string `json:"name"` // nil when the driver is not loaded
	UUID  *string `json:"uuid"`
	BusID string  `json:"bus_id"` // PCI address, e.g. 0000:01:00.0
}

// DefaultSampleInterval is how often the CPU utilization is sampled.
const DefaultSampleInterval = 5 * time.Second

// Options configure a Host.
type Options struct {
	// FS holds the host's proc/ and sys/ trees and its root files
	// (.dockerenv); SystemFS() for the real machine. Nil reports only what
	// the Go runtime knows, as on platforms other than Linux.
	FS fs.FS
	// Hostname defaults to os.Hostname.
	Hostname func() (string, error)
	// NumCPU defaults to runtime.NumCPU. It is the logical core count
	// when FS does not tell.
	NumCPU func() int
	// SampleInterval defaults to DefaultSampleInterval.
	SampleInterval time.Duration
}

// Host reports on the machine. Its methods are safe for concurrent use.
type Host struct {
	fsys     fs.FS
	hostname func() (string, error)
	numCPU   func() int
	sampler  *Sampler
}

// New creates a Host. Call Start to begin sampling the CPU utilization.
func New(o Options) *Host {
	h := &Host{fsys: o.FS, hostname: o.Hostname, numCPU: o.NumCPU}
	if h.hostname == nil {
		h.hostname = os.Hostname
	}
	if h.numCPU == nil {
		h.numCPU = runtime.NumCPU
	}
	interval := o.SampleInterval
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	h.sampler = NewSampler(o.FS, interval)
	return h
}

// Start begins sampling the CPU utilization in the background until ctx
// is cancelled or Stop is called.
func (h *Host) Start(ctx context.Context) { h.sampler.Start(ctx) }

// Stop ends the sampling and waits for it to finish.
func (h *Host) Stop() { h.sampler.Stop() }

// Info describes the machine now. Values that cannot be read are nil.
func (h *Host) Info() Server {
	s := Server{OS: runtime.GOOS, Arch: runtime.GOARCH, CPU: CPU{LogicalCores: h.numCPU()}}
	if name, err := h.hostname(); err == nil {
		s.Hostname = name
	}
	if h.fsys == nil {
		return s
	}
	readLinux(h.fsys, &s)
	s.CPU.UsagePercent = h.sampler.Usage()
	return s
}
