package compute

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Node reports are normalized before they are stored or returned, so the
// frontend can rely on the contract in types.go whatever an agent sends:
// numbers that cannot be right (NaN, infinite, negative) become null,
// strings are trimmed and cut to a sane length, arrays are bounded and
// never null, and unknown enum values fall back to their safest value.
// The functions here are idempotent.

// Limits on reported values.
const (
	maxText    = 256  // most strings
	maxMessage = 500  // messages, hints and detection errors
	maxDetail  = 2000 // raw error details
	maxCode    = 64   // issue codes
	maxItems   = 64   // GPUs, issues, torch_arch_list entries
)

// Limits on registration fields.
const (
	maxNodeNameRunes     = 64
	maxHostnameRunes     = 255
	maxAgentVersionRunes = 64
)

var (
	nodeKeyPattern           = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	computeCapabilityPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)
)

// validateNodeKey checks the node_key of a registration or heartbeat.
func validateNodeKey(key string) error {
	if key == "" {
		return &Error{Kind: ErrInvalid, Message: "node_key 不能为空"}
	}
	if !nodeKeyPattern.MatchString(key) {
		return &Error{Kind: ErrInvalid, Message: "node_key 只能由 1–64 个字母、数字、点、下划线或连字符组成"}
	}
	return nil
}

// normalizeNodeName trims a node name and validates it.
func normalizeNodeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", &Error{Kind: ErrInvalid, Message: "节点名称不能为空"}
	case !utf8.ValidString(name):
		return "", &Error{Kind: ErrInvalid, Message: "节点名称包含无效字符"}
	case utf8.RuneCountInString(name) > maxNodeNameRunes:
		return "", newError(ErrInvalid, "节点名称不能超过 %d 个字符", maxNodeNameRunes)
	case strings.ContainsFunc(name, isControlChar):
		return "", &Error{Kind: ErrInvalid, Message: "节点名称不能包含换行、制表符等控制字符"}
	}
	return name, nil
}

// normalizeLabel trims an optional registration field (what names it in
// messages) and validates it like a name, allowing "".
func normalizeLabel(raw, what string, limit int) (string, error) {
	s := strings.TrimSpace(raw)
	switch {
	case !utf8.ValidString(s) || strings.ContainsFunc(s, isControlChar):
		return "", newError(ErrInvalid, "%s包含无效字符", what)
	case utf8.RuneCountInString(s) > limit:
		return "", newError(ErrInvalid, "%s不能超过 %d 个字符", what, limit)
	}
	return s, nil
}

func isControlChar(r rune) bool { return r < 0x20 || r == 0x7f }

// cleanText makes a reported string safe to store and show: valid UTF-8
// without NUL (PostgreSQL jsonb cannot hold \u0000), trimmed and at most
// limit characters long.
func cleanText(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.TrimSpace(s)
	if len(s) > limit && utf8.RuneCountInString(s) > limit {
		end := 0
		for range limit {
			_, size := utf8.DecodeRuneInString(s[end:])
			end += size
		}
		s = strings.TrimSpace(s[:end])
	}
	return s
}

// optText cleans an optional string; one that ends up empty is unknown.
func optText(n Null[string], limit int) Null[string] {
	if !n.Valid {
		return n
	}
	if s := cleanText(n.V, limit); s != "" {
		return Value(s)
	}
	return Null[string]{}
}

// enum returns s if it is one of allowed, else fallback.
func enum(s, fallback string, allowed ...string) string {
	if slices.Contains(allowed, s) {
		return s
	}
	return fallback
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// nonNegative keeps a finite number >= 0.
func nonNegative(n Null[float64]) Null[float64] {
	if n.Valid && finite(n.V) && n.V >= 0 {
		return n
	}
	return Null[float64]{}
}

// positive keeps a finite number > 0. It is used where agents report 0
// for "unknown" (psutil gives max_mhz 0.0 in many VMs) or "no limit".
func positive(n Null[float64]) Null[float64] {
	if n = nonNegative(n); n.Valid && n.V > 0 {
		return n
	}
	return Null[float64]{}
}

// percent keeps a utilization percentage. Rounding in the agent can
// overshoot 100 a little; the progress bars need 0-100.
func percent(n Null[float64]) Null[float64] {
	if n = nonNegative(n); n.Valid {
		n.V = min(n.V, 100)
	}
	return n
}

func nonNegativeInt(n Null[int64]) Null[int64] {
	if n.Valid && n.V >= 0 {
		return n
	}
	return Null[int64]{}
}

func positiveInt(n Null[int64]) Null[int64] {
	if n.Valid && n.V > 0 {
		return n
	}
	return Null[int64]{}
}

// normalizeHardware returns h in normalized form.
func normalizeHardware(h Hardware) Hardware {
	out := Hardware{
		OS:          optText(h.OS, maxText),
		Kernel:      optText(h.Kernel, maxText),
		Arch:        optText(h.Arch, maxText),
		InContainer: h.InContainer,
		CPU: HardwareCPU{
			Model:         optText(h.CPU.Model, maxText),
			PhysicalCores: positiveInt(h.CPU.PhysicalCores),
			LogicalCores:  positiveInt(h.CPU.LogicalCores),
			MaxMHz:        positive(h.CPU.MaxMHz),
			LimitCores:    positive(h.CPU.LimitCores),
		},
		Memory: HardwareMemory{
			TotalBytes: positiveInt(h.Memory.TotalBytes),
			LimitBytes: positiveInt(h.Memory.LimitBytes),
		},
		ShmBytes: nonNegativeInt(h.ShmBytes),
		GPUs:     []GPU{},
		GPUDetection: GPUDetection{
			Method:       enum(h.GPUDetection.Method, DetectionNone, DetectionNVML, DetectionNvidiaSMI, DetectionNone),
			Error:        optText(h.GPUDetection.Error, maxMessage),
			HostGPUCount: nonNegativeInt(h.GPUDetection.HostGPUCount),
		},
	}
	// A limit that is not below the physical memory limits nothing.
	if m := out.Memory; m.LimitBytes.Valid && m.TotalBytes.Valid && m.LimitBytes.V >= m.TotalBytes.V {
		out.Memory.LimitBytes = Null[int64]{}
	}
	if d := h.GPUDriver; d != nil {
		// Without a version the driver section says nothing.
		if version := cleanText(d.DriverVersion, maxText); version != "" {
			out.GPUDriver = &GPUDriver{
				DriverVersion:     version,
				CUDADriverVersion: optText(d.CUDADriverVersion, maxText),
				NVMLVersion:       optText(d.NVMLVersion, maxText),
			}
		}
	}
	for _, g := range h.GPUs {
		g = GPU{
			Index:             nonNegativeInt(g.Index),
			UUID:              optText(g.UUID, maxText),
			Name:              cleanText(g.Name, maxText),
			MemoryTotalBytes:  positiveInt(g.MemoryTotalBytes),
			ComputeCapability: optText(g.ComputeCapability, maxText),
			PCIBusID:          optText(g.PCIBusID, maxText),
			PowerLimitWatts:   positive(g.PowerLimitWatts),
			MIGEnabled:        g.MIGEnabled,
		}
		if cc := g.ComputeCapability; cc.Valid && !computeCapabilityPattern.MatchString(cc.V) {
			g.ComputeCapability = Null[string]{}
		}
		out.GPUs = append(out.GPUs, g)
	}
	out.GPUs = byIndex(out.GPUs, func(g GPU) Null[int64] { return g.Index })
	return out
}

// byIndex drops the GPUs without a valid index (metrics are matched to
// hardware by index, and the frontend keys its lists by it) and those
// whose index repeats an earlier one, sorts the rest by index and keeps
// at most maxItems.
func byIndex[T any](gpus []T, index func(T) Null[int64]) []T {
	seen := make(map[int64]bool, len(gpus))
	out := make([]T, 0, len(gpus))
	for _, g := range gpus {
		if i := index(g); i.Valid && !seen[i.V] {
			seen[i.V] = true
			out = append(out, g)
		}
	}
	slices.SortStableFunc(out, func(a, b T) int { return cmp.Compare(index(a).V, index(b).V) })
	return out[:min(len(out), maxItems)]
}

// normalizeEnvironment returns e in normalized form, or nil for nil. now
// replaces a probed_at that is not a time.
func normalizeEnvironment(e *Environment, now time.Time) *Environment {
	if e == nil {
		return nil
	}
	out := &Environment{
		Status:             enum(e.Status, EnvError, EnvReady, EnvCPUOnly, EnvError),
		Accelerator:        enum(e.Accelerator, AcceleratorCPU, AcceleratorCUDA, AcceleratorCPU),
		PythonVersion:      optText(e.PythonVersion, maxText),
		TorchVersion:       optText(e.TorchVersion, maxText),
		TorchCUDAVersion:   optText(e.TorchCUDAVersion, maxText),
		CUDNNVersion:       optText(e.CUDNNVersion, maxText),
		TorchArchList:      []string{},
		TorchvisionVersion: optText(e.TorchvisionVersion, maxText),
		UltralyticsVersion: optText(e.UltralyticsVersion, maxText),
		TorchVariant:       optText(e.TorchVariant, maxText),
		RecommendedVariant: cleanText(e.RecommendedVariant, maxText),
		CUDAAvailable:      e.CUDAAvailable,
		CUDADeviceCount:    nonNegativeInt(e.CUDADeviceCount),
		SelfTest: SelfTest{
			Status:     enum(e.SelfTest.Status, SelfTestSkipped, SelfTestPassed, SelfTestFailed, SelfTestSkipped),
			Device:     optText(e.SelfTest.Device, maxText),
			DurationMS: nonNegativeInt(e.SelfTest.DurationMS),
			Message:    optText(e.SelfTest.Message, maxMessage),
			Detail:     optText(e.SelfTest.Detail, maxDetail),
		},
		Issues:   []Issue{},
		ProbedAt: normalizeTime(e.ProbedAt, now),
	}
	if !out.CUDADeviceCount.Valid {
		out.CUDADeviceCount = Value[int64](0)
	}
	for _, arch := range e.TorchArchList {
		if arch = cleanText(arch, maxText); arch != "" && len(out.TorchArchList) < maxItems {
			out.TorchArchList = append(out.TorchArchList, arch)
		}
	}
	for _, is := range e.Issues {
		is = Issue{
			Level:   enum(is.Level, LevelInfo, LevelError, LevelWarning, LevelInfo),
			Code:    cleanText(is.Code, maxCode),
			Message: cleanText(is.Message, maxMessage),
			Hint:    optText(is.Hint, maxMessage),
		}
		if is.Code == "" && is.Message == "" {
			continue // nothing to show
		}
		out.Issues = append(out.Issues, is)
	}
	slices.SortStableFunc(out.Issues, func(a, b Issue) int { return cmp.Compare(levelRank(a.Level), levelRank(b.Level)) })
	out.Issues = out.Issues[:min(len(out.Issues), maxItems)]
	return out
}

func levelRank(level string) int {
	switch level {
	case LevelError:
		return 0
	case LevelWarning:
		return 1
	}
	return 2
}

// localTimeLayout is an RFC 3339 time without a zone, as Python's
// datetime.utcnow().isoformat() writes it; it is taken as UTC.
const localTimeLayout = "2006-01-02T15:04:05.999999999"

// normalizeTime returns s as an RFC 3339 UTC time, or now if s is not a
// time at all.
func normalizeTime(s string, now time.Time) string {
	s = strings.TrimSpace(s)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if t, err = time.Parse(localTimeLayout, s); err != nil {
			t = now
		}
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// normalizeMetrics returns m in normalized form, or nil for nil.
func normalizeMetrics(m *Metrics) *Metrics {
	if m == nil {
		return nil
	}
	out := &Metrics{
		CPUPercent:       percent(m.CPUPercent),
		LoadAverage:      normalizeLoad(m.LoadAverage),
		MemoryUsedBytes:  nonNegativeInt(m.MemoryUsedBytes),
		MemoryTotalBytes: positiveInt(m.MemoryTotalBytes),
		DiskPath:         optText(m.DiskPath, maxText),
		DiskTotalBytes:   positiveInt(m.DiskTotalBytes),
		DiskFreeBytes:    nonNegativeInt(m.DiskFreeBytes),
		GPUs:             []GPUMetrics{},
	}
	for _, g := range m.GPUs {
		out.GPUs = append(out.GPUs, GPUMetrics{
			Index:              nonNegativeInt(g.Index),
			UtilizationPercent: percent(g.UtilizationPercent),
			MemoryUsedBytes:    nonNegativeInt(g.MemoryUsedBytes),
			MemoryTotalBytes:   positiveInt(g.MemoryTotalBytes),
			TemperatureC:       nonNegative(g.TemperatureC),
			PowerWatts:         nonNegative(g.PowerWatts),
			// NVML documents that the fan speed may exceed 100%.
			FanPercent:   nonNegative(g.FanPercent),
			ProcessCount: nonNegativeInt(g.ProcessCount),
		})
	}
	out.GPUs = byIndex(out.GPUs, func(g GPUMetrics) Null[int64] { return g.Index })
	return out
}

// normalizeLoad keeps a load average of exactly three valid numbers; any
// other is unknown (nil).
func normalizeLoad(load []Null[float64]) []Null[float64] {
	if len(load) != 3 {
		return nil
	}
	for _, v := range load {
		if !nonNegative(v).Valid {
			return nil
		}
	}
	return slices.Clone(load)
}

// historyPoint is the history sample of normalized metrics taken at t.
func historyPoint(t time.Time, m *Metrics) HistoryPoint {
	p := HistoryPoint{
		T:               t,
		CPUPercent:      m.CPUPercent,
		MemoryUsedBytes: m.MemoryUsedBytes,
		GPUs:            make([]HistoryGPU, 0, len(m.GPUs)),
	}
	for _, g := range m.GPUs {
		p.GPUs = append(p.GPUs, HistoryGPU{
			Index:              g.Index.V,
			UtilizationPercent: g.UtilizationPercent,
			MemoryUsedBytes:    g.MemoryUsedBytes,
		})
	}
	return p
}
