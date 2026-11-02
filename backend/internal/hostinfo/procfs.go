package hostinfo

import (
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Readers of the Linux /proc and /sys files. They take every path relative
// to the root of fsys and never fail: what cannot be read stays nil. The
// file itself is not Linux-specific so that the parsers build (and could
// be tested) everywhere.

// readLinux fills in what fsys tells about the machine.
func readLinux(fsys fs.FS, s *Server) {
	s.Kernel = readLine(fsys, "proc/sys/kernel/osrelease")
	s.InContainer = exists(fsys, ".dockerenv") || exists(fsys, "run/.containerenv")
	s.UptimeSeconds = readUptime(fsys)

	if data, ok := readFile(fsys, "proc/cpuinfo"); ok {
		info := parseCPUInfo(data)
		if info.logical > 0 {
			s.CPU.LogicalCores = info.logical
		}
		s.CPU.Model, s.CPU.PhysicalCores = info.model, info.physical
	}
	s.Memory = readMemory(fsys)
	s.LoadAverage = readLoadAverage(fsys)

	// A limit that is not below what the host has limits nothing.
	cores, bytes := readCgroupLimits(fsys)
	if cores != nil && *cores < float64(s.CPU.LogicalCores) {
		s.CPU.LimitCores = cores
	}
	if bytes != nil && (s.Memory.TotalBytes == nil || *bytes < *s.Memory.TotalBytes) {
		s.Memory.LimitBytes = bytes
	}

	s.NVIDIA = readNVIDIA(fsys)
}

func readFile(fsys fs.FS, name string) (string, bool) {
	data, err := fs.ReadFile(fsys, name)
	return string(data), err == nil
}

// readLine returns the trimmed content of a one-line file, nil if it
// cannot be read or is empty.
func readLine(fsys fs.FS, name string) *string {
	data, ok := readFile(fsys, name)
	if s := strings.TrimSpace(data); ok && s != "" {
		return &s
	}
	return nil
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil
}

// readUptime reads the seconds since boot from /proc/uptime ("864000.12
// 3400000.00").
func readUptime(fsys fs.FS) *int64 {
	data, _ := readFile(fsys, "proc/uptime")
	fields := strings.Fields(data)
	if len(fields) == 0 {
		return nil
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || f < 0 || f > math.MaxInt64 {
		return nil
	}
	v := int64(f)
	return &v
}

// cpuInfo is what /proc/cpuinfo tells.
type cpuInfo struct {
	model    *string
	logical  int  // processor entries; 0 if none
	physical *int // distinct (physical id, core id) pairs; nil without core ids
}

// modelKeys are the /proc/cpuinfo keys that name the processor, in order
// of preference: x86 (and some ARM kernels), 32-bit ARM, MIPS, RISC-V and
// the SoC name of some boards.
var modelKeys = []string{"model name", "Processor", "cpu model", "uarch", "Hardware"}

// parseCPUInfo parses /proc/cpuinfo: one block of "key : value" lines per
// logical processor, separated by blank lines.
func parseCPUInfo(data string) cpuInfo {
	var info cpuInfo
	models := map[string]string{}
	type core struct{ socket, id string }
	cores := map[core]bool{}
	allCoreIDs := true
	var socket, coreID string
	inProcessor := false
	endBlock := func() {
		if inProcessor {
			if coreID == "" {
				allCoreIDs = false
			} else {
				cores[core{socket, coreID}] = true
			}
		}
		socket, coreID, inProcessor = "", "", false
	}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			if strings.TrimSpace(line) == "" {
				endBlock()
			}
			continue
		}
		key, value = strings.TrimSpace(key), strings.Join(strings.Fields(value), " ")
		switch key {
		case "processor":
			if inProcessor { // no blank line between blocks
				endBlock()
			}
			inProcessor = true
			info.logical++
		case "physical id":
			socket = value
		case "core id":
			coreID = value
		}
		if _, seen := models[key]; !seen && value != "" && slices.Contains(modelKeys, key) {
			models[key] = value
		}
	}
	endBlock()
	for _, key := range modelKeys {
		if m, ok := models[key]; ok {
			info.model = &m
			break
		}
	}
	if info.logical > 0 && allCoreIDs {
		n := len(cores)
		info.physical = &n
	}
	return info
}

// readMemory reads /proc/meminfo, whose values are in KiB.
func readMemory(fsys fs.FS) Memory {
	var m Memory
	data, _ := readFile(fsys, "proc/meminfo")
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kib < 0 || kib > math.MaxInt64/1024 {
			continue
		}
		bytes := kib * 1024
		switch key {
		case "MemTotal":
			m.TotalBytes = &bytes
		case "MemAvailable":
			m.AvailableBytes = &bytes
		}
	}
	if m.TotalBytes != nil && m.AvailableBytes != nil {
		used := max(*m.TotalBytes-*m.AvailableBytes, 0)
		m.UsedBytes = &used
	}
	return m
}

// readLoadAverage reads /proc/loadavg ("0.52 0.61 0.70 1/234 5678").
func readLoadAverage(fsys fs.FS) []float64 {
	data, _ := readFile(fsys, "proc/loadavg")
	fields := strings.Fields(data)
	if len(fields) < 3 {
		return nil
	}
	load := make([]float64, 3)
	for i := range load {
		f, err := strconv.ParseFloat(fields[i], 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return nil
		}
		load[i] = f
	}
	return load
}

const cgroupRoot = "sys/fs/cgroup"

// cgroupV1Unlimited is the smallest limit cgroup v1 reports for "no
// limit" (the maximum page counter, about 8 EiB, rounded to pages).
const cgroupV1Unlimited = 1 << 62

// readCgroupLimits returns the CPU quota in cores and the memory limit of
// the cgroup the process runs in, nil where there is none. With cgroup v2
// a container (private cgroup namespace) sees its own cgroup at the root;
// a process in a nested cgroup (a systemd service, a container sharing
// the host's namespace) finds its own one through /proc/self/cgroup.
func readCgroupLimits(fsys fs.FS) (cores *float64, bytes *int64) {
	if exists(fsys, cgroupRoot+"/cgroup.controllers") {
		dirs := append(cgroupV2Dirs(fsys), cgroupRoot)
		for _, dir := range dirs {
			if data, ok := readFile(fsys, dir+"/cpu.max"); ok {
				cores = parseCPUMax(data)
				break
			}
		}
		for _, dir := range dirs {
			if data, ok := readFile(fsys, dir+"/memory.max"); ok {
				bytes = parseMemoryLimit(data)
				break
			}
		}
		return cores, bytes
	}
	// cgroup v1: a container sees its own cgroup in each hierarchy.
	for _, dir := range []string{cgroupRoot + "/cpu,cpuacct", cgroupRoot + "/cpu"} {
		quota, okQuota := readFile(fsys, dir+"/cpu.cfs_quota_us")
		period, okPeriod := readFile(fsys, dir+"/cpu.cfs_period_us")
		if okQuota && okPeriod {
			cores = parseQuota(strings.TrimSpace(quota), strings.TrimSpace(period))
			break
		}
	}
	if data, ok := readFile(fsys, cgroupRoot+"/memory/memory.limit_in_bytes"); ok {
		bytes = parseMemoryLimit(data)
	}
	return cores, bytes
}

// cgroupV2Dirs returns the directory of the process's own cgroup v2 below
// the root, if /proc/self/cgroup names one ("0::/system.slice/x.service").
func cgroupV2Dirs(fsys fs.FS) []string {
	data, _ := readFile(fsys, "proc/self/cgroup")
	for _, line := range strings.Split(data, "\n") {
		rel, ok := strings.CutPrefix(strings.TrimSpace(line), "0::")
		if !ok {
			continue
		}
		dir := path.Clean(cgroupRoot + "/" + rel)
		// Outside the namespace the path starts with "/.."; ignore it.
		if strings.HasPrefix(dir, cgroupRoot+"/") && fs.ValidPath(dir) {
			return []string{dir}
		}
	}
	return nil
}

// parseCPUMax parses cgroup v2 cpu.max ("200000 100000" or "max 100000").
func parseCPUMax(data string) *float64 {
	fields := strings.Fields(data)
	if len(fields) != 2 {
		return nil
	}
	return parseQuota(fields[0], fields[1])
}

// parseQuota returns quota/period in cores (rounded to 0.01), nil for no
// limit ("max", "-1") or unparsable values.
func parseQuota(quota, period string) *float64 {
	q, err := strconv.ParseInt(quota, 10, 64)
	if err != nil || q <= 0 {
		return nil
	}
	p, err := strconv.ParseInt(period, 10, 64)
	if err != nil || p <= 0 {
		return nil
	}
	cores := math.Round(float64(q)/float64(p)*100) / 100
	return &cores
}

// parseMemoryLimit parses memory.max or memory.limit_in_bytes; "max" and
// the v1 "unlimited" value are nil.
func parseMemoryLimit(data string) *int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	if err != nil || v <= 0 || v >= cgroupV1Unlimited {
		return nil
	}
	return &v
}

const (
	nvidiaVendorID = "0x10de"
	nvidiaProcDir  = "proc/driver/nvidia"
	pciDevicesDir  = "sys/bus/pci/devices"
)

// pciDisplayClasses are the PCI class prefixes of VGA (0x0300) and 3D
// (0x0302) controllers; datacenter GPUs are the latter.
var pciDisplayClasses = []string{"0x0300", "0x0302"}

// driverVersionPattern matches the driver version in /proc/driver/nvidia/
// version, e.g. 550.54.15 in "NVRM version: NVIDIA UNIX x86_64 Kernel
// Module  550.54.15  Tue Mar  5 22:23:56 UTC 2024".
var driverVersionPattern = regexp.MustCompile(`\d{3,}\.\d+(?:\.\d+)?`)

// readNVIDIA lists the NVIDIA GPUs the driver knows about, or, when it is
// not loaded, those on the PCI bus.
func readNVIDIA(fsys fs.FS) *NVIDIA {
	gpus := driverGPUs(fsys)
	if len(gpus) == 0 {
		gpus = pciGPUs(fsys)
	}
	if len(gpus) == 0 {
		return nil
	}
	data, _ := readFile(fsys, nvidiaProcDir+"/version")
	return &NVIDIA{DriverVersion: parseDriverVersion(data), GPUs: gpus}
}

// parseDriverVersion finds the version in the NVRM line of
// /proc/driver/nvidia/version (the next line names the GCC version).
func parseDriverVersion(data string) *string {
	line := data
	for _, l := range strings.Split(data, "\n") {
		if strings.HasPrefix(l, "NVRM version:") {
			line = l
			break
		}
	}
	if v := driverVersionPattern.FindString(line); v != "" {
		return &v
	}
	return nil
}

// driverGPUs reads /proc/driver/nvidia/gpus/<bus id>/information.
func driverGPUs(fsys fs.FS) []NVIDIAGPU {
	entries, err := fs.ReadDir(fsys, nvidiaProcDir+"/gpus")
	if err != nil {
		return nil
	}
	var gpus []NVIDIAGPU
	for _, e := range entries {
		data, _ := readFile(fsys, nvidiaProcDir+"/gpus/"+e.Name()+"/information")
		gpus = append(gpus, parseGPUInformation(data, e.Name()))
	}
	slices.SortFunc(gpus, func(a, b NVIDIAGPU) int { return strings.Compare(a.BusID, b.BusID) })
	return gpus
}

// parseGPUInformation parses a GPU's information file ("Model:  NVIDIA
// GeForce RTX 4090", "GPU UUID:  GPU-…", "Bus Location:  0000:01:00.0");
// dirName (the bus ID) stands in for a missing bus location.
func parseGPUInformation(data, dirName string) NVIDIAGPU {
	gpu := NVIDIAGPU{BusID: dirName}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Model":
			gpu.Name = &value
		case "GPU UUID":
			// Before the GPU is initialized the driver shows a mask
			// ("GPU-????????-…" or "GPU-********-…").
			if !strings.ContainsAny(value, "?*") {
				gpu.UUID = &value
			}
		case "Bus Location":
			gpu.BusID = value
		}
	}
	return gpu
}

// pciGPUs lists the NVIDIA display controllers on the PCI bus.
func pciGPUs(fsys fs.FS) []NVIDIAGPU {
	entries, err := fs.ReadDir(fsys, pciDevicesDir)
	if err != nil {
		return nil
	}
	var gpus []NVIDIAGPU
	for _, e := range entries {
		dir := pciDevicesDir + "/" + e.Name()
		vendor, _ := readFile(fsys, dir+"/vendor")
		class, _ := readFile(fsys, dir+"/class")
		if !strings.EqualFold(strings.TrimSpace(vendor), nvidiaVendorID) {
			continue
		}
		class = strings.ToLower(strings.TrimSpace(class))
		if slices.ContainsFunc(pciDisplayClasses, func(p string) bool { return strings.HasPrefix(class, p) }) {
			gpus = append(gpus, NVIDIAGPU{BusID: e.Name()})
		}
	}
	slices.SortFunc(gpus, func(a, b NVIDIAGPU) int { return strings.Compare(a.BusID, b.BusID) })
	return gpus
}
