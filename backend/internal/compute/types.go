package compute

import "time"

// The JSON field names of the types in this file are the contract between
// the server, the node agent (trainer/trainmaster_trainer) and the
// frontend (frontend/src/features/training/); do not rename them.
//
// Conventions: times are RFC 3339 UTC, sizes are bytes, percentages are
// 0-100, temperatures °C and power watts. An unknown value is null (a
// Null that is not Valid), never 0 or "". Array fields are always arrays.
// Values reported by nodes pass through the normalize functions
// (normalize.go) before they are stored or returned.

// Node statuses, derived from last_seen_at.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// Node is a registered training node as the API returns it.
type Node struct {
	ID           int64  `json:"id"`
	NodeKey      string `json:"node_key"` // generated and kept by the node itself
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	Address      string `json:"address"` // client IP of the last registration or heartbeat; may be ""
	AgentVersion string `json:"agent_version"`
	Status       string `json:"status"` // StatusOnline or StatusOffline
	// ProbePending: a user asked for a new probe, or the node registered
	// without an environment (it started and is checking it; Environment
	// may still hold the result of its previous run), and the node has not
	// reported the result yet.
	ProbePending bool      `json:"probe_pending"`
	RegisteredAt time.Time `json:"registered_at"` // last (re)start of the node
	LastSeenAt   time.Time `json:"last_seen_at"`  // last registration or heartbeat (server clock)
	CreatedAt    time.Time `json:"created_at"`
	Hardware     Hardware  `json:"hardware"`
	// Environment is nil until the node finished its first check.
	Environment *Environment `json:"environment"`
	Metrics     *Metrics     `json:"metrics"`
	// History holds the recent metrics, oldest first. It lives in memory
	// only and starts empty after a server restart; when an offline node
	// comes back, its first new sample starts it afresh.
	History []HistoryPoint `json:"history"`
}

// Hardware is the static description of a node, reported when it starts
// and after a probe.
type Hardware struct {
	OS          Null[string]   `json:"os"` // PRETTY_NAME of /etc/os-release (inside a container: the image's)
	Kernel      Null[string]   `json:"kernel"`
	Arch        Null[string]   `json:"arch"` // platform.machine(), e.g. x86_64
	InContainer bool           `json:"in_container"`
	CPU         HardwareCPU    `json:"cpu"`
	Memory      HardwareMemory `json:"memory"`
	ShmBytes    Null[int64]    `json:"shm_bytes"` // size of /dev/shm
	// GPUDriver is nil when there is no NVIDIA driver or it cannot be
	// probed.
	GPUDriver    *GPUDriver   `json:"gpu_driver"`
	GPUs         []GPU        `json:"gpus"`
	GPUDetection GPUDetection `json:"gpu_detection"`
}

// HardwareCPU describes the processors of a node.
type HardwareCPU struct {
	Model         Null[string]  `json:"model"`
	PhysicalCores Null[int64]   `json:"physical_cores"`
	LogicalCores  Null[int64]   `json:"logical_cores"`
	MaxMHz        Null[float64] `json:"max_mhz"`
	LimitCores    Null[float64] `json:"limit_cores"` // cgroup CPU quota in cores; null without a quota
}

// HardwareMemory describes the memory of a node.
type HardwareMemory struct {
	TotalBytes Null[int64] `json:"total_bytes"` // physical memory
	LimitBytes Null[int64] `json:"limit_bytes"` // cgroup limit; null without one or when it is not below total_bytes
}

// GPUDriver describes the NVIDIA driver of a node.
type GPUDriver struct {
	DriverVersion     string       `json:"driver_version"`
	CUDADriverVersion Null[string] `json:"cuda_driver_version"` // highest CUDA version the driver supports
	NVMLVersion       Null[string] `json:"nvml_version"`
}

// GPU is one graphics card of a node.
type GPU struct {
	Index             Null[int64]   `json:"index"` // NVML index; always set after normalization
	UUID              Null[string]  `json:"uuid"`
	Name              string        `json:"name"`
	MemoryTotalBytes  Null[int64]   `json:"memory_total_bytes"`
	ComputeCapability Null[string]  `json:"compute_capability"` // "major.minor"
	PCIBusID          Null[string]  `json:"pci_bus_id"`
	PowerLimitWatts   Null[float64] `json:"power_limit_watts"`
	MIGEnabled        Null[bool]    `json:"mig_enabled"`
}

// GPU detection methods.
const (
	DetectionNVML      = "nvml"
	DetectionNvidiaSMI = "nvidia-smi"
	DetectionNone      = "none"
)

// GPUDetection tells how the node looked for GPUs.
type GPUDetection struct {
	Method string       `json:"method"` // DetectionNVML, DetectionNvidiaSMI or DetectionNone
	Error  Null[string] `json:"error"`  // why detection failed (raw English error)
	// HostGPUCount is the number of NVIDIA GPUs of the host that the node
	// sees in /proc/driver/nvidia/gpus, even when none was passed into its
	// container.
	HostGPUCount Null[int64] `json:"host_gpu_count"`
}

// Environment statuses.
const (
	EnvReady   = "ready"    // GPU and CUDA usable, self test passed
	EnvCPUOnly = "cpu_only" // no usable GPU, CPU self test passed
	EnvError   = "error"    // an error-level issue (including a failed self test)
)

// Accelerators.
const (
	AcceleratorCUDA = "cuda"
	AcceleratorCPU  = "cpu"
)

// Self test statuses.
const (
	SelfTestPassed  = "passed"
	SelfTestFailed  = "failed"
	SelfTestSkipped = "skipped"
)

// Issue levels, most severe first.
const (
	LevelError   = "error"
	LevelWarning = "warning"
	LevelInfo    = "info"
)

// Environment is the result of the node's CUDA / PyTorch / YOLO check.
type Environment struct {
	Status             string       `json:"status"`      // EnvReady, EnvCPUOnly or EnvError
	Accelerator        string       `json:"accelerator"` // AcceleratorCUDA or AcceleratorCPU: what training will use
	PythonVersion      Null[string] `json:"python_version"`
	TorchVersion       Null[string] `json:"torch_version"`
	TorchCUDAVersion   Null[string] `json:"torch_cuda_version"` // torch.version.cuda; null for CPU builds
	CUDNNVersion       Null[string] `json:"cudnn_version"`
	TorchArchList      []string     `json:"torch_arch_list"` // torch.cuda.get_arch_list()
	TorchvisionVersion Null[string] `json:"torchvision_version"`
	UltralyticsVersion Null[string] `json:"ultralytics_version"`
	TorchVariant       Null[string] `json:"torch_variant"`       // installed PyTorch variant (cu126, cpu, ...)
	RecommendedVariant string       `json:"recommended_variant"` // variant that fits the node's driver and GPUs
	CUDAAvailable      bool         `json:"cuda_available"`
	CUDADeviceCount    Null[int64]  `json:"cuda_device_count"` // always set after normalization (0 when unavailable)
	SelfTest           SelfTest     `json:"self_test"`
	Issues             []Issue      `json:"issues"`    // most severe first
	ProbedAt           string       `json:"probed_at"` // when the node finished the check (node clock)
}

// SelfTest is the result of the YOLO forward pass the node runs.
type SelfTest struct {
	Status     string       `json:"status"` // SelfTestPassed, SelfTestFailed or SelfTestSkipped
	Device     Null[string] `json:"device"` // "cuda:0" or "cpu"
	DurationMS Null[int64]  `json:"duration_ms"`
	Message    Null[string] `json:"message"` // Chinese summary
	Detail     Null[string] `json:"detail"`  // raw error when it failed
}

// Issue is a problem (or hint) the node found in its environment.
type Issue struct {
	Level   string       `json:"level"` // LevelError, LevelWarning or LevelInfo
	Code    string       `json:"code"`  // e.g. driver_too_old; the frontend only shows message and hint
	Message string       `json:"message"`
	Hint    Null[string] `json:"hint"` // how to fix it
}

// Metrics are the live readings a node sends with every heartbeat.
type Metrics struct {
	CPUPercent Null[float64] `json:"cpu_percent"`
	// LoadAverage is the 1, 5 and 15 minute load average: three valid
	// numbers after normalization, or nil (null) when unknown.
	LoadAverage      []Null[float64] `json:"load_average"`
	MemoryUsedBytes  Null[int64]     `json:"memory_used_bytes"`
	MemoryTotalBytes Null[int64]     `json:"memory_total_bytes"`
	DiskPath         Null[string]    `json:"disk_path"` // the node's data directory
	DiskTotalBytes   Null[int64]     `json:"disk_total_bytes"`
	DiskFreeBytes    Null[int64]     `json:"disk_free_bytes"`
	GPUs             []GPUMetrics    `json:"gpus"`
}

// GPUMetrics are the live readings of one GPU.
type GPUMetrics struct {
	Index              Null[int64]   `json:"index"` // matches Hardware.GPUs[].Index; always set after normalization
	UtilizationPercent Null[float64] `json:"utilization_percent"`
	MemoryUsedBytes    Null[int64]   `json:"memory_used_bytes"`
	MemoryTotalBytes   Null[int64]   `json:"memory_total_bytes"`
	TemperatureC       Null[float64] `json:"temperature_c"`
	PowerWatts         Null[float64] `json:"power_watts"`
	FanPercent         Null[float64] `json:"fan_percent"`
	ProcessCount       Null[int64]   `json:"process_count"` // compute processes on the GPU
}

// HistoryPoint is one sample of a node's recent metrics.
type HistoryPoint struct {
	T               time.Time     `json:"t"` // server clock
	CPUPercent      Null[float64] `json:"cpu_percent"`
	MemoryUsedBytes Null[int64]   `json:"memory_used_bytes"`
	GPUs            []HistoryGPU  `json:"gpus"`
}

// HistoryGPU is one GPU of a HistoryPoint.
type HistoryGPU struct {
	Index              int64         `json:"index"`
	UtilizationPercent Null[float64] `json:"utilization_percent"`
	MemoryUsedBytes    Null[int64]   `json:"memory_used_bytes"`
}

// RegisterInput is a node registration (POST /api/v1/compute/agent/register).
type RegisterInput struct {
	NodeKey      string
	Name         string
	Hostname     string
	AgentVersion string
	Hardware     *Hardware    // required
	Environment  *Environment // nil keeps the stored one and requests a probe (the node is checking it)
	Metrics      *Metrics     // nil keeps the stored one
}

// RegisterResult answers a registration.
type RegisterResult struct {
	NodeID                   int64     `json:"node_id"`
	HeartbeatIntervalSeconds float64   `json:"heartbeat_interval_seconds"`
	ServerTime               time.Time `json:"server_time"`
}

// HeartbeatInput is a heartbeat (POST /api/v1/compute/agent/heartbeat). A
// nil section leaves the stored one unchanged.
type HeartbeatInput struct {
	NodeKey     string
	Hardware    *Hardware
	Environment *Environment // also completes a pending probe
	Metrics     *Metrics     // also adds a history point
}

// HeartbeatResult answers a heartbeat.
type HeartbeatResult struct {
	NodeID                   int64   `json:"node_id"`
	HeartbeatIntervalSeconds float64 `json:"heartbeat_interval_seconds"`
	// ProbeRequested asks the node to probe its hardware and environment
	// again and to report the result with a later heartbeat. It is also
	// true after a registration without an environment, until a heartbeat
	// brings one; a node that is still detecting its environment (or has a
	// result it has not sent yet) ignores it.
	ProbeRequested bool `json:"probe_requested"`
}
