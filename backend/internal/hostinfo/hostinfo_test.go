package hostinfo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// loadFixture reads testdata/<name>.txtar: text before the first
// "-- path --" line is a comment, and each such line starts a file.
func loadFixture(t *testing.T, name string) fstest.MapFS {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".txtar"))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	var file string
	var content []byte
	flush := func() {
		if file != "" {
			fsys[file] = &fstest.MapFile{Data: content, Mode: 0o444}
		}
	}
	for _, line := range strings.SplitAfter(string(data), "\n") {
		marker := strings.TrimRight(line, "\n")
		if strings.HasPrefix(marker, "-- ") && strings.HasSuffix(marker, " --") && len(marker) > 6 {
			flush()
			file, content = marker[3:len(marker)-3], nil
			continue
		}
		if file != "" {
			content = append(content, line...)
		}
	}
	flush()
	return fsys
}

func newTestHost(fsys fs.FS) *Host {
	return New(Options{
		FS:       fsys,
		Hostname: func() (string, error) { return "trainmaster-app", nil },
		NumCPU:   func() int { return 6 },
	})
}

func ptr[T any](v T) *T { return &v }

func TestInfoCgroupV2WithNVIDIA(t *testing.T) {
	got := newTestHost(loadFixture(t, "cgroup-v2-nvidia")).Info()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	// The full JSON, as the frontend reads it.
	want := `{"hostname":"trainmaster-app","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `",` +
		`"kernel":"5.15.0-105-generic","in_container":true,"uptime_seconds":864000,` +
		`"cpu":{"model":"Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz","logical_cores":4,"physical_cores":3,` +
		`"limit_cores":2,"usage_percent":null},` +
		`"memory":{"total_bytes":16705826816,"available_bytes":8352913408,"used_bytes":8352913408,"limit_bytes":4294967296},` +
		`"load_average":[0.52,0.61,0.7],` +
		`"nvidia":{"driver_version":"550.54.15","gpus":[` +
		`{"name":"NVIDIA GeForce RTX 4090","uuid":"GPU-5c1d9e2f-0a1b-4c2d-8e3f-4a5b6c7d8e9f","bus_id":"0000:01:00.0"},` +
		`{"name":"NVIDIA GeForce RTX 4090","uuid":"GPU-6d0c2a1b-7e3f-4a5b-9c8d-1e2f3a4b5c6d","bus_id":"0000:41:00.0"}]}}`
	if string(data) != want {
		t.Errorf("Info()\n got %s\nwant %s", data, want)
	}
}

func TestInfoCgroupV1(t *testing.T) {
	got := newTestHost(loadFixture(t, "cgroup-v1")).Info()
	want := Server{
		Hostname: "trainmaster-app", OS: runtime.GOOS, Arch: runtime.GOARCH,
		Kernel: ptr("6.8.0-45-generic"), InContainer: true, UptimeSeconds: ptr[int64](12),
		CPU: CPU{Model: ptr("AMD EPYC 7763 64-Core Processor"), LogicalCores: 2, PhysicalCores: ptr(2), LimitCores: ptr(1.5)},
		Memory: Memory{TotalBytes: ptr[int64](67108864000), AvailableBytes: ptr[int64](61440000000),
			UsedBytes: ptr[int64](5668864000), LimitBytes: ptr[int64](2147483648)},
		LoadAverage: []float64{3.1, 2.05, 1},
		// An uninitialized GPU masks its UUID; the information file has no
		// bus location, so the directory name is used.
		NVIDIA: &NVIDIA{DriverVersion: ptr("580.65.06"), GPUs: []NVIDIAGPU{
			{Name: ptr("NVIDIA A100-SXM4-80GB"), BusID: "0000:3b:00.0"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Info()\n got %s\nwant %s", jsonString(got), jsonString(want))
	}
}

func jsonString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestInfoWithoutLimits(t *testing.T) {
	for _, name := range []string{"unlimited-v1", "unlimited-v2", "limits-above-host"} {
		t.Run(name, func(t *testing.T) {
			got := newTestHost(loadFixture(t, name)).Info()
			if got.CPU.LimitCores != nil || got.Memory.LimitBytes != nil {
				t.Errorf("limits = %v cores, %v bytes; want none", got.CPU.LimitCores, got.Memory.LimitBytes)
			}
			if got.NVIDIA != nil {
				t.Errorf("nvidia = %+v, want nil", got.NVIDIA)
			}
			if got.Memory.TotalBytes == nil || *got.Memory.TotalBytes != 8_192_000_000 {
				t.Errorf("total memory = %v", got.Memory.TotalBytes)
			}
		})
	}
	got := newTestHost(loadFixture(t, "unlimited-v1")).Info()
	if !got.InContainer || got.CPU.LogicalCores != 1 || got.CPU.PhysicalCores == nil || *got.CPU.PhysicalCores != 1 {
		t.Errorf("unlimited-v1 = %s", jsonString(got))
	}
	// Without /proc/cpuinfo the runtime's count is used.
	if got := newTestHost(loadFixture(t, "unlimited-v2")).Info(); got.InContainer || got.CPU.LogicalCores != 6 || got.CPU.Model != nil {
		t.Errorf("unlimited-v2 = %s", jsonString(got))
	}
	// Without core ids the physical cores are unknown.
	if got := newTestHost(loadFixture(t, "limits-above-host")).Info(); got.CPU.LogicalCores != 2 || got.CPU.PhysicalCores != nil {
		t.Errorf("limits-above-host = %s", jsonString(got))
	}
}

func TestInfoNestedCgroupV2ARM(t *testing.T) {
	got := newTestHost(loadFixture(t, "nested-v2-arm64")).Info()
	if got.CPU.LimitCores == nil || *got.CPU.LimitCores != 0.5 || got.Memory.LimitBytes == nil || *got.Memory.LimitBytes != 1<<30 {
		t.Errorf("limits = %s", jsonString(got))
	}
	if got.CPU.LogicalCores != 4 || got.CPU.Model != nil || got.CPU.PhysicalCores != nil {
		t.Errorf("cpu = %s", jsonString(got.CPU))
	}
	// MemAvailable is missing: available and used are unknown.
	if got.Memory.AvailableBytes != nil || got.Memory.UsedBytes != nil || *got.Kernel != "6.1.0-rpi7-rpi-v8" {
		t.Errorf("memory = %s, kernel %v", jsonString(got.Memory), got.Kernel)
	}
}

func TestInfoPCIOnly(t *testing.T) {
	got := newTestHost(loadFixture(t, "pci-only")).Info()
	want := &NVIDIA{GPUs: []NVIDIAGPU{{BusID: "0000:17:00.0"}, {BusID: "0000:65:00.0"}}}
	if !reflect.DeepEqual(got.NVIDIA, want) {
		t.Errorf("nvidia = %s, want %s", jsonString(got.NVIDIA), jsonString(want))
	}
	if data := jsonString(got.NVIDIA); data != `{"driver_version":null,"gpus":[{"name":null,"uuid":null,"bus_id":"0000:17:00.0"},{"name":null,"uuid":null,"bus_id":"0000:65:00.0"}]}` {
		t.Errorf("nvidia JSON = %s", data)
	}
}

func TestInfoNothingReadable(t *testing.T) {
	want := `{"hostname":"trainmaster-app","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `",` +
		`"kernel":null,"in_container":false,"uptime_seconds":null,` +
		`"cpu":{"model":null,"logical_cores":6,"physical_cores":null,"limit_cores":null,"usage_percent":null},` +
		`"memory":{"total_bytes":null,"available_bytes":null,"used_bytes":null,"limit_bytes":null},` +
		`"load_average":null,"nvidia":null}`
	// Linux with /proc and /sys unreadable, and platforms without them.
	for name, fsys := range map[string]fs.FS{"empty": loadFixture(t, "empty"), "no FS": nil} {
		if got := jsonString(newTestHost(fsys).Info()); got != want {
			t.Errorf("%s\n got %s\nwant %s", name, got, want)
		}
	}
}

func TestInfoHostnameError(t *testing.T) {
	h := New(Options{Hostname: func() (string, error) { return "", errors.New("no name") }})
	if got := h.Info(); got.Hostname != "" || got.CPU.LogicalCores != runtime.NumCPU() {
		t.Errorf("Info() = %+v", got)
	}
}

func TestDriverLoadedWithoutGPUEntries(t *testing.T) {
	// /proc/driver/nvidia/gpus exists but is empty: the PCI bus still
	// shows the GPU, and the driver version is known.
	fsys := fstest.MapFS{
		"proc/driver/nvidia/version":              {Data: []byte("NVRM version: NVIDIA UNIX x86_64 Kernel Module  535.183.01  Sun May 12 19:39:15 UTC 2024\n")},
		"proc/driver/nvidia/gpus":                 {Mode: fs.ModeDir | 0o555},
		"sys/bus/pci/devices/0000:01:00.0/vendor": {Data: []byte("0x10de\n")},
		"sys/bus/pci/devices/0000:01:00.0/class":  {Data: []byte("0x030200\n")},
	}
	got := readNVIDIA(fsys)
	if got == nil || got.DriverVersion == nil || *got.DriverVersion != "535.183.01" || len(got.GPUs) != 1 || got.GPUs[0].BusID != "0000:01:00.0" {
		t.Errorf("readNVIDIA = %s", jsonString(got))
	}
	// A driver without any GPU is no NVIDIA section.
	delete(fsys, "sys/bus/pci/devices/0000:01:00.0/vendor")
	if got := readNVIDIA(fsys); got != nil {
		t.Errorf("readNVIDIA without GPUs = %s", jsonString(got))
	}
}

func TestParseDriverVersion(t *testing.T) {
	for in, want := range map[string]string{
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.15  Tue Mar  5 22:23:56 UTC 2024\nGCC version:  gcc version 12.2.0 (Debian 12.2.0-14)\n":  "550.54.15",
		"NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  580.65.06  Release Build  (dvs-builder@U16-I3-B03-4-3)  Sun Jul 27 07:09:29 UTC 2025\n": "580.65.06",
		"NVRM version: NVIDIA UNIX Open Kernel Module for aarch64  575.57.08  Release Build\n":                                                            "575.57.08",
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  418.87  Thu Aug  8 15:35:46 CDT 2019\n":                                                          "418.87",
		"NVIDIA UNIX x86_64 Kernel Module  470.256.02\n":                                                                                                  "470.256.02", // no NVRM line: search everything
	} {
		if got := parseDriverVersion(in); got == nil || *got != want {
			t.Errorf("parseDriverVersion(%q) = %v, want %s", in, got, want)
		}
	}
	for _, in := range []string{"", "garbage", "GCC version:  gcc version 12.2.0\n", "NVRM version: unknown\nGCC version: 12.2.0"} {
		if got := parseDriverVersion(in); got != nil {
			t.Errorf("parseDriverVersion(%q) = %q, want nil", in, *got)
		}
	}
}

func TestParseGPUInformation(t *testing.T) {
	got := parseGPUInformation("Model:  NVIDIA H100 80GB HBM3\nGPU UUID:  GPU-********-****-****-****-************\nBus Location:  0000:18:00.0\n", "x")
	if got.Name == nil || *got.Name != "NVIDIA H100 80GB HBM3" || got.UUID != nil || got.BusID != "0000:18:00.0" {
		t.Errorf("got %s", jsonString(got))
	}
	if got := parseGPUInformation("", "0000:01:00.0"); got.Name != nil || got.UUID != nil || got.BusID != "0000:01:00.0" {
		t.Errorf("unreadable information: %s", jsonString(got))
	}
}

func TestParseCPUInfoModels(t *testing.T) {
	arm32 := "Processor\t: ARMv7 Processor rev 4 (v7l)\nprocessor\t: 0\nBogoMIPS\t: 38.40\n\nprocessor\t: 1\n\nHardware\t: BCM2835\n"
	info := parseCPUInfo(arm32)
	if info.model == nil || *info.model != "ARMv7 Processor rev 4 (v7l)" || info.logical != 2 || info.physical != nil {
		t.Errorf("arm32 = %+v", info)
	}
	riscv := "processor\t: 0\nhart\t\t: 0\nisa\t\t: rv64imafdc\nmmu\t\t: sv39\nuarch\t\t: sifive,u74-mc\n"
	if info := parseCPUInfo(riscv); info.model == nil || *info.model != "sifive,u74-mc" {
		t.Errorf("riscv = %+v", info)
	}
	if info := parseCPUInfo(""); info.logical != 0 || info.model != nil || info.physical != nil {
		t.Errorf("empty = %+v", info)
	}
}

func TestCgroupPathOutsideNamespace(t *testing.T) {
	// A process moved out of the namespace's root sees "/../..": its own
	// cgroup is not visible, so the root's files apply.
	fsys := fstest.MapFS{
		"proc/self/cgroup":                   {Data: []byte("0::/../../system.slice/other.service\n")},
		"sys/fs/cgroup/cgroup.controllers":   {Data: []byte("cpu memory\n")},
		"sys/fs/cgroup/cpu.max":              {Data: []byte("100000 100000\n")},
		"sys/fs/cgroup/memory.max":           {Data: []byte("max\n")},
		"sys/system.slice/other.service/x":   {Data: []byte("must not be read")},
		"sys/fs/cgroup/system.slice/cpu.max": {Data: []byte("50000 100000\n")},
	}
	cores, bytes := readCgroupLimits(fsys)
	if cores == nil || *cores != 1 || bytes != nil {
		t.Errorf("limits = %v, %v", cores, bytes)
	}
}

func TestParseLimits(t *testing.T) {
	for in, want := range map[string]*float64{
		"200000 100000": ptr(2.0),
		"150000 100000": ptr(1.5),
		"33333 100000":  ptr(0.33),
		"max 100000":    nil,
		"max":           nil,
		"0 100000":      nil,
		"100 0":         nil,
		"abc 100000":    nil,
		"":              nil,
	} {
		if got := parseCPUMax(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseCPUMax(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]*int64{
		"4294967296\n":         ptr[int64](4294967296),
		"max\n":                nil,
		"9223372036854771712":  nil,
		"-1":                   nil,
		"":                     nil,
		"18446744073709551615": nil,
	} {
		if got := parseMemoryLimit(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseMemoryLimit(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSystemFS(t *testing.T) {
	fsys := SystemFS()
	if runtime.GOOS != "linux" {
		if fsys != nil {
			t.Error("SystemFS should be nil outside Linux")
		}
		return
	}
	// On the real machine at least the basics are readable.
	got := New(Options{FS: fsys}).Info()
	if got.Kernel == nil || got.Memory.TotalBytes == nil || got.CPU.LogicalCores < 1 {
		t.Errorf("Info() on this machine = %s", jsonString(got))
	}
}
