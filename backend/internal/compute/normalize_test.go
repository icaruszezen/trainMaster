package compute

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 2, 7, 30, 0, 0, time.UTC)

func TestValidateNodeKey(t *testing.T) {
	for _, key := range []string{"a", "6f1c8a0e-1234-4abc-9def-0123456789ab", "node_1.local", strings.Repeat("x", 64)} {
		if err := validateNodeKey(key); err != nil {
			t.Errorf("validateNodeKey(%q) = %v", key, err)
		}
	}
	for key, msg := range map[string]string{
		"":                      "node_key 不能为空",
		strings.Repeat("x", 65): "1–64 个字母",
		"a b":                   "1–64 个字母",
		"a/b":                   "1–64 个字母",
		"节点":                    "1–64 个字母",
		"a\n":                   "1–64 个字母",
	} {
		err := validateNodeKey(key)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrInvalid || !strings.Contains(e.Message, msg) {
			t.Errorf("validateNodeKey(%q) = %v, want ErrInvalid containing %q", key, err, msg)
		}
	}
}

func TestNormalizeNodeName(t *testing.T) {
	for in, want := range map[string]string{
		" gpu-server-01 ":       "gpu-server-01",
		"训练机 A":                 "训练机 A",
		strings.Repeat("名", 64): strings.Repeat("名", 64),
	} {
		if got, err := normalizeNodeName(in); err != nil || got != want {
			t.Errorf("normalizeNodeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, msg := range map[string]string{
		"":                      "不能为空",
		"   ":                   "不能为空",
		strings.Repeat("名", 65): "不能超过 64 个字符",
		"a\tb":                  "控制字符",
		"a\x7fb":                "控制字符",
		"a\xffb":                "无效字符",
	} {
		_, err := normalizeNodeName(in)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrInvalid || !strings.Contains(e.Message, msg) {
			t.Errorf("normalizeNodeName(%q) = %v, want ErrInvalid containing %q", in, err, msg)
		}
	}
}

func TestNormalizeLabel(t *testing.T) {
	if got, err := normalizeLabel("  host-1 ", "主机名", 255); err != nil || got != "host-1" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := normalizeLabel("", "主机名", 255); err != nil || got != "" {
		t.Errorf("empty is allowed: %q, %v", got, err)
	}
	if _, err := normalizeLabel(strings.Repeat("h", 256), "主机名", 255); err == nil || !strings.Contains(err.Error(), "主机名不能超过 255 个字符") {
		t.Errorf("too long: %v", err)
	}
	if _, err := normalizeLabel("a\nb", "节点版本号", 64); err == nil || !strings.Contains(err.Error(), "节点版本号包含无效字符") {
		t.Errorf("control character: %v", err)
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		in    string
		limit int
		want  string
	}{
		{"  abc  ", 10, "abc"},
		{"a\x00b", 10, "ab"},
		{"\x00 a", 10, "a"},
		{"abcdef", 3, "abc"},
		{"ab def", 3, "ab"}, // cut before a space, then trimmed
		{"显卡显卡", 3, "显卡显"},
		{"a\xffb", 10, "a�b"},
		{"line1\nline2", 20, "line1\nline2"},
	}
	for _, tt := range tests {
		if got := cleanText(tt.in, tt.limit); got != tt.want {
			t.Errorf("cleanText(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
		}
	}
	if got := optText(Value("  "), 10); got.Valid {
		t.Errorf("a blank string is unknown: %+v", got)
	}
}

func TestNumberRules(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	for _, tt := range []struct {
		name string
		f    func(Null[float64]) Null[float64]
		in   Null[float64]
		want Null[float64]
	}{
		{"nonNegative ok", nonNegative, Value(1.5), Value(1.5)},
		{"nonNegative zero", nonNegative, Value(0.0), Value(0.0)},
		{"nonNegative negative", nonNegative, Value(-1.0), Null[float64]{}},
		{"nonNegative NaN", nonNegative, Value(nan), Null[float64]{}},
		{"nonNegative Inf", nonNegative, Value(inf), Null[float64]{}},
		{"nonNegative -Inf", nonNegative, Value(-inf), Null[float64]{}},
		{"nonNegative null", nonNegative, Null[float64]{}, Null[float64]{}},
		{"positive zero", positive, Value(0.0), Null[float64]{}},
		{"positive ok", positive, Value(0.5), Value(0.5)},
		{"percent over", percent, Value(100.4), Value(100.0)},
		{"percent ok", percent, Value(87.5), Value(87.5)},
		{"percent negative", percent, Value(-0.1), Null[float64]{}},
	} {
		if got := tt.f(tt.in); got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
	if nonNegativeInt(Value[int64](-1)).Valid || !nonNegativeInt(Value[int64](0)).Valid ||
		positiveInt(Value[int64](0)).Valid || !positiveInt(Value[int64](1)).Valid {
		t.Error("integer rules")
	}
}

// messyHardware has a problem in almost every field.
func messyHardware() Hardware {
	return Hardware{
		OS:          Value("  Ubuntu 22.04.4 LTS \n"),
		Kernel:      Value(""),
		Arch:        Value("x86_64"),
		Platform:    Value(" Linux\n"),
		InContainer: true,
		CPU: HardwareCPU{
			Model:         Value("Intel(R) Xeon(R) Gold 6330"),
			PhysicalCores: Value[int64](-28),
			LogicalCores:  Value[int64](56),
			MaxMHz:        Value(0.0), // psutil's "unknown"
			LimitCores:    Value(math.NaN()),
		},
		Memory:    HardwareMemory{TotalBytes: Value[int64](1 << 30), LimitBytes: Value[int64](2 << 30)},
		ShmBytes:  Value[int64](-1),
		GPUDriver: &GPUDriver{DriverVersion: " 550.54.15 ", CUDADriverVersion: Value("12.4"), NVMLVersion: Value("")},
		GPUs: []GPU{
			{Index: Value[int64](1), Name: "NVIDIA GeForce RTX 4090", ComputeCapability: Value("8.9"),
				MemoryTotalBytes: Value[int64](25757220864), PowerLimitWatts: Value(math.Inf(1)), MIGEnabled: Value(false)},
			{Index: Value[int64](0), Name: "NVIDIA A100", ComputeCapability: Value("sm_80"), UUID: Value("GPU-1")},
			{Index: Value[int64](1), Name: "duplicate index"},
			{Index: Value[int64](-1), Name: "negative index"},
			{Name: "no index"},
		},
		GPUDetection: GPUDetection{Method: "magic", Error: Value(strings.Repeat("e", 600)), HostGPUCount: Value[int64](2)},
	}
}

func TestNormalizeHardware(t *testing.T) {
	h := normalizeHardware(messyHardware())
	if h.OS != Value("Ubuntu 22.04.4 LTS") || h.Kernel.Valid || h.Arch != Value("x86_64") || h.Platform != Value("linux") || !h.InContainer {
		t.Errorf("os/kernel/arch/platform = %+v %+v %+v %+v", h.OS, h.Kernel, h.Arch, h.Platform)
	}
	if c := h.CPU; c.PhysicalCores.Valid || c.LogicalCores != Value[int64](56) || c.MaxMHz.Valid || c.LimitCores.Valid {
		t.Errorf("cpu = %+v", c)
	}
	// A limit above the physical memory is no limit.
	if h.Memory.TotalBytes != Value[int64](1<<30) || h.Memory.LimitBytes.Valid || h.ShmBytes.Valid {
		t.Errorf("memory = %+v, shm = %+v", h.Memory, h.ShmBytes)
	}
	if d := h.GPUDriver; d == nil || d.DriverVersion != "550.54.15" || d.CUDADriverVersion != Value("12.4") || d.NVMLVersion.Valid {
		t.Errorf("driver = %+v", d)
	}
	if len(h.GPUs) != 2 || h.GPUs[0].Name != "NVIDIA A100" || h.GPUs[1].Name != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("gpus = %+v", h.GPUs)
	}
	if h.GPUs[0].ComputeCapability.Valid || h.GPUs[1].ComputeCapability != Value("8.9") {
		t.Errorf("compute capabilities = %+v, %+v", h.GPUs[0].ComputeCapability, h.GPUs[1].ComputeCapability)
	}
	if h.GPUs[1].PowerLimitWatts.Valid || h.GPUs[1].MIGEnabled != Value(false) {
		t.Errorf("gpu 1 = %+v", h.GPUs[1])
	}
	if d := h.GPUDetection; d.Method != DetectionNone || len([]rune(d.Error.V)) != maxMessage || d.HostGPUCount != Value[int64](2) {
		t.Errorf("detection = %+v", d)
	}

	// A memory limit below the total stays.
	in := messyHardware()
	in.Memory.LimitBytes = Value[int64](1 << 29)
	if got := normalizeHardware(in).Memory.LimitBytes; got != Value[int64](1<<29) {
		t.Errorf("limit = %+v", got)
	}
	// A driver without a version is no driver.
	in.GPUDriver = &GPUDriver{DriverVersion: "  ", CUDADriverVersion: Value("12.4")}
	if got := normalizeHardware(in).GPUDriver; got != nil {
		t.Errorf("driver = %+v", got)
	}
}

func TestNormalizePlatform(t *testing.T) {
	for _, tt := range []struct {
		in   Null[string]
		want Null[string]
	}{
		{Value("windows"), Value("windows")},
		{Value("linux"), Value("linux")},
		{Value(" Windows "), Value("windows")}, // platform.system() is "Windows"
		{Value("LINUX\n"), Value("linux")},
		{Value("darwin"), Value("darwin")},
		{Value("freebsd"), Value("freebsd")},
		{Value("cygwin_nt-10"), Value("cygwin_nt-10")},
		{Value("a" + strings.Repeat("b", 31)), Value("a" + strings.Repeat("b", 31))},
		// Older agents send none: they run on Linux, which the frontend
		// assumes for null.
		{Null[string]{}, Null[string]{}},
		{Value(""), Null[string]{}},
		{Value("   "), Null[string]{}},
		{Value("a" + strings.Repeat("b", 32)), Null[string]{}}, // too long
		{Value("cygwin_nt-10.0"), Null[string]{}},
		{Value("9front"), Null[string]{}},
		{Value("_linux"), Null[string]{}},
		{Value("win dows"), Null[string]{}},
		{Value("windows\x00"), Null[string]{}},
		{Value("linux\nwindows"), Null[string]{}},
		{Value("视窗"), Null[string]{}},
		{Value("ｗindows"), Null[string]{}}, // full-width w
	} {
		if got := normalizePlatform(tt.in); got != tt.want {
			t.Errorf("normalizePlatform(%+v) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeHardwareLimitsGPUs(t *testing.T) {
	var h Hardware
	for i := range 100 {
		h.GPUs = append(h.GPUs, GPU{Index: Value(int64(99 - i)), Name: "gpu"})
	}
	gpus := normalizeHardware(h).GPUs
	if len(gpus) != maxItems || gpus[0].Index.V != 0 || gpus[maxItems-1].Index.V != maxItems-1 {
		t.Errorf("got %d GPUs, first %d", len(gpus), gpus[0].Index.V)
	}
}

func TestNormalizeEmptyArraysAreArrays(t *testing.T) {
	data, err := json.Marshal(normalizeHardware(Hardware{}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"os":null,"kernel":null,"arch":null,"platform":null,"in_container":false,` +
		`"cpu":{"model":null,"physical_cores":null,"logical_cores":null,"max_mhz":null,"limit_cores":null},` +
		`"memory":{"total_bytes":null,"limit_bytes":null},"shm_bytes":null,"gpu_driver":null,"gpus":[],` +
		`"gpu_detection":{"method":"none","error":null,"host_gpu_count":null}}`
	if string(data) != want {
		t.Errorf("empty hardware\n got %s\nwant %s", data, want)
	}
	data, _ = json.Marshal(normalizeEnvironment(&Environment{}, testNow))
	for _, part := range []string{`"torch_arch_list":[]`, `"issues":[]`, `"status":"error"`, `"accelerator":"cpu"`,
		`"cuda_device_count":0`, `"self_test":{"status":"skipped"`, `"probed_at":"2026-11-02T07:30:00Z"`, `"recommended_variant":""`} {
		if !strings.Contains(string(data), part) {
			t.Errorf("empty environment %s lacks %s", data, part)
		}
	}
	data, _ = json.Marshal(normalizeMetrics(&Metrics{}))
	if want := `{"cpu_percent":null,"load_average":null,"memory_used_bytes":null,"memory_total_bytes":null,` +
		`"disk_path":null,"disk_total_bytes":null,"disk_free_bytes":null,"gpus":[]}`; string(data) != want {
		t.Errorf("empty metrics\n got %s\nwant %s", data, want)
	}
	if normalizeEnvironment(nil, testNow) != nil || normalizeMetrics(nil) != nil {
		t.Error("nil sections stay nil")
	}
}

func messyEnvironment() *Environment {
	e := &Environment{
		Status:             "great",
		Accelerator:        "tpu",
		PythonVersion:      Value("3.12.7"),
		TorchVersion:       Value("2.9.1+cu126"),
		TorchCUDAVersion:   Value("12.6"),
		TorchArchList:      []string{"sm_50", "", "  sm_90  "},
		TorchVariant:       Value("cu126"),
		RecommendedVariant: "cu130",
		CUDAAvailable:      true,
		CUDADeviceCount:    Value[int64](-2),
		SelfTest: SelfTest{Status: "maybe", Device: Value("cuda:0"), DurationMS: Value[int64](1830),
			Message: Value("YOLO11n 前向推理正常"), Detail: Value(strings.Repeat("d", 3000))},
		Issues: []Issue{
			{Level: LevelInfo, Code: "variant_upgrade_available", Message: "可以升级"},
			{Level: "fatal", Code: "x", Message: "未知级别"},
			{Level: LevelWarning, Code: "shm_small", Message: "共享内存太小", Hint: Value("")},
			{Level: LevelError, Code: "driver_too_old", Message: "驱动过旧", Hint: Value("升级驱动")},
			{Level: LevelError},
		},
		ProbedAt: "2026-11-02T15:30:00+08:00",
	}
	for range 70 {
		e.TorchArchList = append(e.TorchArchList, "sm_86")
	}
	return e
}

func TestNormalizeEnvironment(t *testing.T) {
	e := normalizeEnvironment(messyEnvironment(), testNow)
	if e.Status != EnvError || e.Accelerator != AcceleratorCPU || e.CUDADeviceCount != Value[int64](0) || !e.CUDAAvailable {
		t.Errorf("status = %q, accelerator = %q, count = %+v", e.Status, e.Accelerator, e.CUDADeviceCount)
	}
	if len(e.TorchArchList) != maxItems || e.TorchArchList[0] != "sm_50" || e.TorchArchList[1] != "sm_90" {
		t.Errorf("arch list = %d entries starting %v", len(e.TorchArchList), e.TorchArchList[:2])
	}
	if st := e.SelfTest; st.Status != SelfTestSkipped || len(st.Detail.V) != maxDetail || st.DurationMS != Value[int64](1830) {
		t.Errorf("self test = %+v", st)
	}
	var levels, codes []string
	for _, is := range e.Issues {
		levels, codes = append(levels, is.Level), append(codes, is.Code)
	}
	if want := []string{"error", "warning", "info", "info"}; !reflect.DeepEqual(levels, want) {
		t.Errorf("levels = %v, want %v", levels, want)
	}
	if want := []string{"driver_too_old", "shm_small", "variant_upgrade_available", "x"}; !reflect.DeepEqual(codes, want) {
		t.Errorf("codes = %v, want %v (sorted by severity, stable)", codes, want)
	}
	if e.Issues[1].Hint.Valid || e.Issues[0].Hint != Value("升级驱动") {
		t.Errorf("hints = %+v, %+v", e.Issues[0].Hint, e.Issues[1].Hint)
	}
	if e.ProbedAt != "2026-11-02T07:30:00Z" {
		t.Errorf("probed_at = %q", e.ProbedAt)
	}
}

func TestNormalizeTime(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-02T07:30:00Z":             "2026-11-02T07:30:00Z",
		"2026-11-02T07:30:00.123456+00:00": "2026-11-02T07:30:00.123456Z",
		"2026-11-02T15:30:00+08:00":        "2026-11-02T07:30:00Z",
		"2026-11-02T07:31:00.5":            "2026-11-02T07:31:00.5Z", // no zone: UTC
		"yesterday":                        "2026-11-02T07:30:00Z",
		"":                                 "2026-11-02T07:30:00Z",
	} {
		if got := normalizeTime(in, testNow); got != want {
			t.Errorf("normalizeTime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeMetrics(t *testing.T) {
	m := normalizeMetrics(&Metrics{
		CPUPercent:       Value(130.0),
		LoadAverage:      []Null[float64]{Value(0.52), Value(0.61), Value(0.7)},
		MemoryUsedBytes:  Value[int64](-5),
		MemoryTotalBytes: Value[int64](0),
		DiskPath:         Value("/data"),
		DiskTotalBytes:   Value[int64](4000787030016),
		DiskFreeBytes:    Value[int64](0),
		GPUs: []GPUMetrics{
			{Index: Value[int64](1), UtilizationPercent: Value(87.0), TemperatureC: Value(-3.0), FanPercent: Value(120.0),
				PowerWatts: Value(math.NaN()), ProcessCount: Value[int64](1)},
			{Index: Value[int64](0), MemoryUsedBytes: Value[int64](20401094656), MemoryTotalBytes: Value[int64](25757220864)},
			{UtilizationPercent: Value(10.0)},
		},
	})
	if m.CPUPercent != Value(100.0) || m.MemoryUsedBytes.Valid || m.MemoryTotalBytes.Valid ||
		m.DiskFreeBytes != Value[int64](0) || m.DiskTotalBytes != Value[int64](4000787030016) {
		t.Errorf("metrics = %+v", m)
	}
	if want := []Null[float64]{Value(0.52), Value(0.61), Value(0.7)}; !reflect.DeepEqual(m.LoadAverage, want) {
		t.Errorf("load = %+v", m.LoadAverage)
	}
	if len(m.GPUs) != 2 || m.GPUs[0].Index.V != 0 || m.GPUs[1].Index.V != 1 {
		t.Fatalf("gpus = %+v", m.GPUs)
	}
	if g := m.GPUs[1]; g.TemperatureC.Valid || g.PowerWatts.Valid || g.FanPercent != Value(120.0) || g.UtilizationPercent != Value(87.0) {
		t.Errorf("gpu 1 = %+v", g)
	}
	for _, load := range [][]Null[float64]{
		nil, {Value(1.0), Value(2.0)}, {Value(1.0), Value(2.0), Value(3.0), Value(4.0)},
		{Value(1.0), {}, Value(3.0)}, {Value(1.0), Value(-2.0), Value(3.0)}, {Value(math.Inf(1)), Value(2.0), Value(3.0)},
	} {
		if got := normalizeLoad(load); got != nil {
			t.Errorf("normalizeLoad(%+v) = %+v, want nil", load, got)
		}
	}
}

// TestNormalizeIdempotent: stored values are normalized again when read,
// which must not change them, also after a JSON round trip (as through
// the jsonb column).
func TestNormalizeIdempotent(t *testing.T) {
	h := normalizeHardware(messyHardware())
	e := normalizeEnvironment(messyEnvironment(), testNow)
	m := normalizeMetrics(&Metrics{CPUPercent: Value(12.5), LoadAverage: []Null[float64]{Value(1.0), Value(2.0), Value(3.0)},
		GPUs: []GPUMetrics{{Index: Value[int64](0), FanPercent: Value(55.0)}}})
	later := testNow.Add(time.Hour)

	if again := normalizeHardware(h); !reflect.DeepEqual(again, h) {
		t.Errorf("hardware changed:\n%+v\n%+v", h, again)
	}
	if again := normalizeEnvironment(e, later); !reflect.DeepEqual(again, e) {
		t.Errorf("environment changed:\n%+v\n%+v", e, again)
	}
	if again := normalizeMetrics(m); !reflect.DeepEqual(again, m) {
		t.Errorf("metrics changed:\n%+v\n%+v", m, again)
	}

	roundTrip := func(v any, out any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := decodeLenient(data, out); err != nil {
			t.Fatal(err)
		}
	}
	var h2 Hardware
	roundTrip(h, &h2)
	if again := normalizeHardware(h2); !reflect.DeepEqual(again, h) {
		t.Errorf("hardware changed after a round trip:\n%+v\n%+v", h, again)
	}
	var e2 Environment
	roundTrip(e, &e2)
	if again := normalizeEnvironment(&e2, later); !reflect.DeepEqual(again, e) {
		t.Errorf("environment changed after a round trip:\n%+v\n%+v", e, again)
	}
	var m2 Metrics
	roundTrip(m, &m2)
	if again := normalizeMetrics(&m2); !reflect.DeepEqual(again, m) {
		t.Errorf("metrics changed after a round trip:\n%+v\n%+v", m, again)
	}
}

func TestHistoryPoint(t *testing.T) {
	m := normalizeMetrics(&Metrics{CPUPercent: Value(12.5), MemoryUsedBytes: Value[int64](100), GPUs: []GPUMetrics{
		{Index: Value[int64](0), UtilizationPercent: Value(87.0), MemoryUsedBytes: Value[int64](5), TemperatureC: Value(70.0)},
	}})
	p := historyPoint(testNow, m)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"t":"2026-11-02T07:30:00Z","cpu_percent":12.5,"memory_used_bytes":100,` +
		`"gpus":[{"index":0,"utilization_percent":87,"memory_used_bytes":5}]}`; string(data) != want {
		t.Errorf("point\n got %s\nwant %s", data, want)
	}
	if p := historyPoint(testNow, normalizeMetrics(&Metrics{})); p.GPUs == nil {
		t.Error("gpus must be an array")
	}
}

func TestIsOnline(t *testing.T) {
	interval := 10 * time.Second
	if got := OfflineAfter(interval); got != 35*time.Second {
		t.Fatalf("OfflineAfter(10s) = %v, want 35s", got)
	}
	if got := OfflineAfter(2 * time.Second); got != 11*time.Second {
		t.Fatalf("OfflineAfter(2s) = %v, want 11s", got)
	}
	seen := testNow
	for _, tt := range []struct {
		after time.Duration
		want  bool
	}{
		{-time.Second, true}, // clock moved back
		{0, true},
		{34 * time.Second, true},
		{35 * time.Second, true}, // exactly 3 x interval + 5s: still online
		{35*time.Second + time.Microsecond, false},
		{time.Hour, false},
	} {
		if got := isOnline(seen.Add(tt.after), seen, OfflineAfter(interval)); got != tt.want {
			t.Errorf("online %v after the last heartbeat = %v, want %v", tt.after, got, tt.want)
		}
	}
}

func TestCompareNodes(t *testing.T) {
	nodes := []Node{
		{ID: 5, Name: "beta", Status: StatusOffline},
		{ID: 4, Name: "Alpha", Status: StatusOffline},
		{ID: 3, Name: "gamma", Status: StatusOnline},
		{ID: 2, Name: "alpha", Status: StatusOnline},
		{ID: 1, Name: "ALPHA", Status: StatusOnline},
	}
	sortNodes := append([]Node(nil), nodes...)
	slices.SortFunc(sortNodes, compareNodes)
	var ids []int64
	for _, n := range sortNodes {
		ids = append(ids, n.ID)
	}
	if want := []int64{1, 2, 3, 4, 5}; !reflect.DeepEqual(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
}
