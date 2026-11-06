package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/hostinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

var (
	nodeKeys = []string{"id", "node_key", "name", "hostname", "address", "agent_version", "status", "probe_pending",
		"registered_at", "last_seen_at", "created_at", "hardware", "environment", "metrics", "history"}
	hardwareKeys     = []string{"os", "kernel", "arch", "platform", "in_container", "cpu", "memory", "shm_bytes", "gpu_driver", "gpus", "gpu_detection"}
	hardwareCPUKeys  = []string{"model", "physical_cores", "logical_cores", "max_mhz", "limit_cores"}
	hardwareGPUKeys  = []string{"index", "uuid", "name", "memory_total_bytes", "compute_capability", "pci_bus_id", "power_limit_watts", "mig_enabled"}
	gpuDriverKeys    = []string{"driver_version", "cuda_driver_version", "nvml_version"}
	gpuDetectionKeys = []string{"method", "error", "host_gpu_count"}
	environmentKeys  = []string{"status", "accelerator", "python_version", "torch_version", "torch_cuda_version", "cudnn_version",
		"torch_arch_list", "torchvision_version", "ultralytics_version", "torch_variant", "recommended_variant", "cuda_available",
		"cuda_device_count", "self_test", "issues", "probed_at"}
	selfTestKeys      = []string{"status", "device", "duration_ms", "message", "detail"}
	issueKeys         = []string{"level", "code", "message", "hint"}
	metricsKeys       = []string{"cpu_percent", "load_average", "memory_used_bytes", "memory_total_bytes", "disk_path", "disk_total_bytes", "disk_free_bytes", "gpus"}
	gpuMetricsKeys    = []string{"index", "utilization_percent", "memory_used_bytes", "memory_total_bytes", "temperature_c", "power_watts", "fan_percent", "process_count"}
	historyPointKeys  = []string{"t", "cpu_percent", "memory_used_bytes", "gpus"}
	historyGPUKeys    = []string{"index", "utilization_percent", "memory_used_bytes"}
	serverKeys        = []string{"hostname", "os", "arch", "kernel", "in_container", "uptime_seconds", "cpu", "memory", "load_average", "nvidia", "node_registration"}
	serverCPUKeys     = []string{"model", "logical_cores", "physical_cores", "limit_cores", "usage_percent"}
	serverMemoryKeys  = []string{"total_bytes", "available_bytes", "used_bytes", "limit_bytes"}
	registrationKeys  = []string{"token_required", "heartbeat_interval_seconds", "offline_after_seconds", "local_trainer_mode", "windows_installer_available"}
	nodeRegisterKeys  = []string{"node_id", "heartbeat_interval_seconds", "server_time"}
	nodeHeartbeatKeys = []string{"node_id", "heartbeat_interval_seconds", "probe_requested"}
)

// testHardware, testEnvironment and testMetrics are reports as the node
// agent sends them (the examples of the data contract).
const testHardware = `{
	"os": "Ubuntu 22.04.4 LTS", "kernel": "5.15.0-105-generic", "arch": "x86_64", "platform": "linux", "in_container": true,
	"cpu": {"model": "Intel(R) Xeon(R) Gold 6330 CPU @ 2.00GHz", "physical_cores": 28, "logical_cores": 56,
		"max_mhz": 3100, "limit_cores": 8.0},
	"memory": {"total_bytes": 134987546624, "limit_bytes": null},
	"shm_bytes": 8589934592,
	"gpu_driver": {"driver_version": "550.54.15", "cuda_driver_version": "12.4", "nvml_version": "12.550.54.15"},
	"gpus": [{"index": 0, "uuid": "GPU-5c1d", "name": "NVIDIA GeForce RTX 4090", "memory_total_bytes": 25757220864,
		"compute_capability": "8.9", "pci_bus_id": "00000000:01:00.0", "power_limit_watts": 450, "mig_enabled": false}],
	"gpu_detection": {"method": "nvml", "error": null, "host_gpu_count": null}
}`

const testEnvironment = `{
	"status": "ready", "accelerator": "cuda", "python_version": "3.12.7", "torch_version": "2.9.1+cu126",
	"torch_cuda_version": "12.6", "cudnn_version": "9.10.2", "torch_arch_list": ["sm_50", "sm_86", "sm_90"],
	"torchvision_version": "0.24.1+cu126", "ultralytics_version": "8.4.198", "torch_variant": "cu126",
	"recommended_variant": "cu126", "cuda_available": true, "cuda_device_count": 1,
	"self_test": {"status": "passed", "device": "cuda:0", "duration_ms": 1830, "message": "YOLO11n 前向推理正常（1 块 GPU，FP16）", "detail": null},
	"issues": [{"level": "info", "code": "variant_upgrade_available", "message": "可以换用 cu130", "hint": null}],
	"probed_at": "2026-11-02T07:30:00Z"
}`

func testMetrics(cpu float64) string {
	return fmt.Sprintf(`{"cpu_percent": %v, "load_average": [0.52, 0.61, 0.70], "memory_used_bytes": 23456789012,
		"memory_total_bytes": 134987546624, "disk_path": "/data", "disk_total_bytes": 4000787030016,
		"disk_free_bytes": 2000393515008, "gpus": [{"index": 0, "utilization_percent": %v, "memory_used_bytes": 20401094656,
		"memory_total_bytes": 25757220864, "temperature_c": 71, "power_watts": 312.4, "fan_percent": 55, "process_count": 1}]}`, cpu, cpu)
}

func registerBody(key, name, env, metrics string) string {
	return fmt.Sprintf(`{"node_key": %q, "name": %q, "hostname": "gpu-server-01", "agent_version": "0.1.0",
		"hardware": %s, "environment": %s, "metrics": %s}`, key, name, testHardware, env, metrics)
}

// computeClock is a clock tests move by hand.
type computeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *computeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *computeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type computeEnv struct {
	*apiEnv
	clock *computeClock
}

// testHost is a Host reading a small fixture machine.
func testHost() *hostinfo.Host {
	return hostinfo.New(hostinfo.Options{
		FS: fstest.MapFS{
			"proc/sys/kernel/osrelease": {Data: []byte("6.8.0-45-generic\n")},
			"proc/meminfo":              {Data: []byte("MemTotal: 1000 kB\nMemAvailable: 400 kB\n")},
			"proc/loadavg":              {Data: []byte("0.5 0.6 0.7 1/2 3\n")},
		},
		Hostname: func() (string, error) { return "trainmaster-app", nil },
		NumCPU:   func() int { return 4 },
	})
}

// newComputeEnv serves the compute API on a private database (token is
// TM_NODE_TOKEN), with a clock the test controls.
func newComputeEnv(t *testing.T, token string, trustedProxies ...string) *computeEnv {
	t.Helper()
	db := dbtest.New(t)
	return newComputeHandler(t, compute.Options{DB: db.Pool}, Options{NodeToken: token, TrustedProxies: trustedProxies})
}

// newComputeHandler serves the compute API of compute service o with the
// handler settings of api (NodeToken, TrustedProxies, LocalTrainerMode).
func newComputeHandler(t *testing.T, o compute.Options, api Options) *computeEnv {
	t.Helper()
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	clock := &computeClock{t: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	o.Logger, o.HeartbeatInterval, o.Now = log, 10*time.Second, clock.Now
	api.Logger, api.DB, api.Storage, api.StartedAt = log, fakeDB{}, st, time.Now()
	api.Compute, api.Host = compute.NewService(o), testHost()
	h := NewHandler(api)
	return &computeEnv{apiEnv: &apiEnv{h: h, st: st}, clock: clock}
}

// register registers a node and returns its ID.
func (e *computeEnv) register(t *testing.T, key, name string, header ...string) int64 {
	t.Helper()
	res := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody(key, name, "null", testMetrics(10)), header...), 200)
	return res.NodeID
}

func (e *computeEnv) heartbeat(t *testing.T, body string, header ...string) compute.HeartbeatResult {
	t.Helper()
	return want[compute.HeartbeatResult](t, e.do("POST", "/api/v1/compute/agent/heartbeat", body, header...), 200)
}

func nodeURL(id int64, suffix string) string {
	return fmt.Sprintf("/api/v1/compute/nodes/%d%s", id, suffix)
}

func object(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want an object", what, v)
	}
	return m
}

func firstItem(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("%s = %#v, want a non-empty array", what, v)
	}
	return object(t, items[0], what+"[0]")
}

func TestComputeServerInfo(t *testing.T) {
	// No database needed.
	e := newComputeHandler(t, compute.Options{}, Options{})
	raw := want[map[string]any](t, e.do("GET", "/api/v1/compute/server", nil), 200)
	wantKeys(t, "server", raw, serverKeys...)
	wantKeys(t, "server cpu", object(t, raw["cpu"], "cpu"), serverCPUKeys...)
	wantKeys(t, "server memory", object(t, raw["memory"], "memory"), serverMemoryKeys...)
	if raw["hostname"] != "trainmaster-app" || raw["kernel"] != "6.8.0-45-generic" || raw["nvidia"] != nil {
		t.Errorf("server = %v", raw)
	}
	if mem := object(t, raw["memory"], "memory"); mem["total_bytes"] != 1024000.0 || mem["used_bytes"] != 614400.0 || mem["limit_bytes"] != nil {
		t.Errorf("memory = %v", mem)
	}
	reg := object(t, raw["node_registration"], "node_registration")
	wantKeys(t, "node_registration", reg, registrationKeys...)
	// Without TM_TRAINER_MODE (e.g. outside docker compose) the mode of
	// this machine's own training node is not known; without
	// TM_TRAINER_PACKAGE_DIR there is no installer for Windows.
	if reg["token_required"] != false || reg["heartbeat_interval_seconds"] != 10.0 || reg["offline_after_seconds"] != 35.0 ||
		reg["local_trainer_mode"] != nil || reg["windows_installer_available"] != false {
		t.Errorf("node_registration = %v", reg)
	}

	// The browser endpoints never need the node token.
	e = newComputeHandler(t, compute.Options{}, Options{NodeToken: "0123456789abcdef"})
	raw = want[map[string]any](t, e.do("GET", "/api/v1/compute/server", nil), 200)
	if object(t, raw["node_registration"], "node_registration")["token_required"] != true {
		t.Errorf("token_required should be true: %v", raw["node_registration"])
	}

	// The mode make setup chose for this machine's own node (TM_TRAINER_MODE).
	for _, mode := range []string{"auto", "gpu", "cpu", "off"} {
		e = newComputeHandler(t, compute.Options{}, Options{LocalTrainerMode: mode})
		raw = want[map[string]any](t, e.do("GET", "/api/v1/compute/server", nil), 200)
		reg = object(t, raw["node_registration"], "node_registration")
		wantKeys(t, "node_registration", reg, registrationKeys...)
		if reg["local_trainer_mode"] != mode {
			t.Errorf("local_trainer_mode = %#v, want %q", reg["local_trainer_mode"], mode)
		}
	}
}

func TestComputeNodeAPI(t *testing.T) {
	e := newComputeEnv(t, "")

	// Register: the response follows the contract.
	rec := e.do("POST", "/api/v1/compute/agent/register", registerBody("6f1c8a0e-node", " gpu-server-01 ", "null", testMetrics(12.5)))
	reg := want[map[string]any](t, rec, 200)
	wantKeys(t, "register response", reg, nodeRegisterKeys...)
	if reg["heartbeat_interval_seconds"] != 10.0 || reg["server_time"] != "2026-11-02T08:00:00Z" {
		t.Errorf("register response = %v", reg)
	}
	id := int64(reg["node_id"].(float64))

	// The list shows it online, with its hardware and the first metrics;
	// it is still checking its environment.
	list := want[map[string]any](t, e.do("GET", "/api/v1/compute/nodes", nil), 200)
	if list["total"] != 1.0 {
		t.Fatalf("list = %v", list)
	}
	node := firstItem(t, list["items"], "items")
	wantKeys(t, "node", node, nodeKeys...)
	if node["id"] != float64(id) || node["node_key"] != "6f1c8a0e-node" || node["name"] != "gpu-server-01" ||
		node["hostname"] != "gpu-server-01" || node["agent_version"] != "0.1.0" || node["address"] != "192.0.2.1" ||
		node["status"] != "online" || node["probe_pending"] != true || node["environment"] != nil {
		t.Errorf("node = %v", node)
	}
	for _, k := range []string{"registered_at", "last_seen_at", "created_at"} {
		if node[k] != "2026-11-02T08:00:00Z" {
			t.Errorf("%s = %v", k, node[k])
		}
	}
	hw := object(t, node["hardware"], "hardware")
	wantKeys(t, "hardware", hw, hardwareKeys...)
	if hw["platform"] != "linux" || hw["arch"] != "x86_64" {
		t.Errorf("hardware = %v", hw)
	}
	wantKeys(t, "hardware cpu", object(t, hw["cpu"], "cpu"), hardwareCPUKeys...)
	wantKeys(t, "hardware memory", object(t, hw["memory"], "memory"), "total_bytes", "limit_bytes")
	wantKeys(t, "gpu_driver", object(t, hw["gpu_driver"], "gpu_driver"), gpuDriverKeys...)
	wantKeys(t, "gpu_detection", object(t, hw["gpu_detection"], "gpu_detection"), gpuDetectionKeys...)
	gpu := firstItem(t, hw["gpus"], "gpus")
	wantKeys(t, "hardware gpu", gpu, hardwareGPUKeys...)
	if gpu["name"] != "NVIDIA GeForce RTX 4090" || gpu["compute_capability"] != "8.9" || gpu["memory_total_bytes"] != 25757220864.0 {
		t.Errorf("gpu = %v", gpu)
	}
	metrics := object(t, node["metrics"], "metrics")
	wantKeys(t, "metrics", metrics, metricsKeys...)
	wantKeys(t, "gpu metrics", firstItem(t, metrics["gpus"], "metrics gpus"), gpuMetricsKeys...)
	point := firstItem(t, node["history"], "history")
	wantKeys(t, "history point", point, historyPointKeys...)
	wantKeys(t, "history gpu", firstItem(t, point["gpus"], "history gpus"), historyGPUKeys...)
	if point["t"] != "2026-11-02T08:00:00Z" || point["cpu_percent"] != 12.5 {
		t.Errorf("history point = %v", point)
	}

	// Heartbeats: the response follows the contract, metrics add history
	// points and an environment is stored, which completes the check.
	e.clock.Advance(10 * time.Second)
	hb := want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/heartbeat",
		`{"node_key": "6f1c8a0e-node", "metrics": `+testMetrics(50)+`, "environment": null, "hardware": null}`), 200)
	wantKeys(t, "heartbeat response", hb, nodeHeartbeatKeys...)
	if hb["node_id"] != float64(id) || hb["probe_requested"] != true || hb["heartbeat_interval_seconds"] != 10.0 {
		t.Errorf("heartbeat response = %v", hb)
	}
	e.clock.Advance(10 * time.Second)
	if hb := e.heartbeat(t, `{"node_key": "6f1c8a0e-node", "metrics": `+testMetrics(70)+`, "environment": `+testEnvironment+`}`); hb.ProbeRequested {
		t.Errorf("heartbeat with environment = %+v", hb)
	}

	node = want[map[string]any](t, e.do("GET", nodeURL(id, ""), nil), 200)
	wantKeys(t, "node", node, nodeKeys...)
	if node["last_seen_at"] != "2026-11-02T08:00:20Z" || node["registered_at"] != "2026-11-02T08:00:00Z" || node["probe_pending"] != false {
		t.Errorf("times: %v %v, probe_pending %v", node["last_seen_at"], node["registered_at"], node["probe_pending"])
	}
	env := object(t, node["environment"], "environment")
	wantKeys(t, "environment", env, environmentKeys...)
	wantKeys(t, "self_test", object(t, env["self_test"], "self_test"), selfTestKeys...)
	wantKeys(t, "issue", firstItem(t, env["issues"], "issues"), issueKeys...)
	if env["status"] != "ready" || env["torch_variant"] != "cu126" || env["cuda_device_count"] != 1.0 {
		t.Errorf("environment = %v", env)
	}
	history, _ := node["history"].([]any)
	if len(history) != 3 {
		t.Fatalf("history has %d points, want 3", len(history))
	}
	var cpus []any
	for _, p := range history {
		cpus = append(cpus, object(t, p, "point")["cpu_percent"])
	}
	if fmt.Sprint(cpus) != "[12.5 50 70]" {
		t.Errorf("history cpu = %v, want oldest first", cpus)
	}

	// Unknown or invalid node IDs.
	wantError(t, e.do("GET", nodeURL(99999, ""), nil), 404, codeNotFound, "节点不存在")
	for _, bad := range []string{"0", "-1", "abc", "1.5", "99999999999999999999"} {
		wantError(t, e.do("GET", "/api/v1/compute/nodes/"+bad, nil), 404, codeNotFound, "节点不存在")
		wantError(t, e.do("DELETE", "/api/v1/compute/nodes/"+bad, nil), 404, codeNotFound, "节点不存在")
		wantError(t, e.do("POST", "/api/v1/compute/nodes/"+bad+"/probe", nil), 404, codeNotFound, "节点不存在")
	}
	wantError(t, e.do("GET", "/api/v1/compute/agent/register", nil), 405, codeMethodNotAllowed, "")
	wantError(t, e.do("PUT", "/api/v1/compute/nodes", nil), 405, codeMethodNotAllowed, "")
}

func TestComputeUnknownNode(t *testing.T) {
	e := newComputeEnv(t, "")
	wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", `{"node_key": "not-registered", "metrics": `+testMetrics(1)+`}`),
		404, codeUnknownNode, "请重新注册")

	// After a deletion the node's heartbeats are unknown too.
	id := e.register(t, "node-a", "a")
	e.clock.Advance(time.Hour)
	if rec := e.do("DELETE", nodeURL(id, ""), nil); rec.Code != 204 {
		t.Fatalf("delete status = %d", rec.Code)
	}
	wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", `{"node_key": "node-a"}`), 404, codeUnknownNode, "")
}

func TestComputeNodeToken(t *testing.T) {
	const token = "s3cr3t-node-token-0123"
	e := newComputeEnv(t, token)
	body := registerBody("node-a", "a", "null", "null")

	for name, header := range map[string][]string{
		"missing":      nil,
		"wrong":        {"Authorization", "Bearer s3cr3t-node-token-0124"},
		"prefix":       {"Authorization", "Bearer s3cr3t-node-token-012"},
		"longer":       {"Authorization", "Bearer " + token + "x"},
		"other scheme": {"Authorization", "Basic " + token},
		"no scheme":    {"Authorization", token},
		"empty bearer": {"Authorization", "Bearer "},
		"other header": {"X-Node-Token", token},
		"two values":   {"Authorization", "Bearer " + token + " " + token},
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.do("POST", "/api/v1/compute/agent/register", body, header...)
			wantError(t, rec, 401, codeUnauthorized, "节点令牌不正确")
			if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
				t.Errorf("WWW-Authenticate = %q", got)
			}
			wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", `{"node_key": "node-a"}`, header...), 401, codeUnauthorized, "")
		})
	}
	// The token is checked before the body is read.
	wantError(t, e.do("POST", "/api/v1/compute/agent/register", "{"), 401, codeUnauthorized, "")

	id := e.register(t, "node-a", "a", "Authorization", "Bearer "+token)
	e.register(t, "node-a", "a", "Authorization", "bearer  "+token+" ") // scheme case and spaces do not matter
	if hb := e.heartbeat(t, `{"node_key": "node-a"}`, "Authorization", "Bearer "+token); hb.NodeID != id {
		t.Errorf("heartbeat = %+v", hb)
	}
	// The browser endpoints need no token.
	if l := want[List[compute.Node]](t, e.do("GET", "/api/v1/compute/nodes", nil), 200); l.Total != 1 {
		t.Errorf("list = %+v", l)
	}
	want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200)
	want[compute.Node](t, e.do("POST", nodeURL(id, "/probe"), nil), 202)

	// Without TM_NODE_TOKEN any or no Authorization header is fine.
	open := newComputeEnv(t, "")
	open.register(t, "node-b", "b")
	open.register(t, "node-b", "b", "Authorization", "Bearer whatever")
}

func TestComputeReRegisterKeepsEnvironment(t *testing.T) {
	e := newComputeEnv(t, "")
	id := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("node-a", "a", testEnvironment, testMetrics(30))), 200).NodeID
	if n := want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200); n.ProbePending {
		t.Errorf("registered with an environment, but probe_pending")
	}
	e.clock.Advance(time.Minute)
	// The node restarts and registers without environment and metrics:
	// the old environment is kept and shown as being checked again.
	if again := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("node-a", "renamed", "null", "null")), 200).NodeID; again != id {
		t.Fatalf("re-registration got node %d, want %d", again, id)
	}
	n := want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200)
	if n.Name != "renamed" || n.Environment == nil || n.Environment.TorchVariant != compute.Value("cu126") ||
		n.Metrics == nil || n.Metrics.CPUPercent != compute.Value(30.0) || !n.ProbePending {
		t.Errorf("node = %+v", n)
	}
	if !n.RegisteredAt.Equal(e.clock.Now()) || n.CreatedAt.Equal(e.clock.Now()) {
		t.Errorf("registered %v, created %v", n.RegisteredAt, n.CreatedAt)
	}
	if l := want[List[compute.Node]](t, e.do("GET", "/api/v1/compute/nodes", nil), 200); l.Total != 1 {
		t.Errorf("%d nodes", l.Total)
	}
}

func TestComputeProbeFlow(t *testing.T) {
	e := newComputeEnv(t, "")
	id := e.register(t, "node-a", "a")
	// The node finishes the check it started with.
	if hb := e.heartbeat(t, `{"node_key": "node-a", "environment": `+testEnvironment+`}`); hb.ProbeRequested {
		t.Fatalf("heartbeat with environment = %+v", hb)
	}

	rec := e.do("POST", nodeURL(id, "/probe"), nil)
	raw := want[map[string]any](t, rec, 202)
	wantKeys(t, "probed node", raw, nodeKeys...)
	if raw["probe_pending"] != true || raw["id"] != float64(id) {
		t.Errorf("probe response = %v", raw)
	}
	// Heartbeats ask for the probe until one carries an environment.
	if hb := e.heartbeat(t, `{"node_key": "node-a", "metrics": `+testMetrics(5)+`}`); !hb.ProbeRequested {
		t.Error("heartbeat should report probe_requested")
	}
	if hb := e.heartbeat(t, `{"node_key": "node-a"}`); !hb.ProbeRequested {
		t.Error("probe request lost")
	}
	if hb := e.heartbeat(t, `{"node_key": "node-a", "hardware": `+testHardware+`, "environment": `+testEnvironment+`}`); hb.ProbeRequested {
		t.Error("an environment should complete the probe")
	}
	n := want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200)
	if n.ProbePending || n.Environment == nil {
		t.Errorf("node = %+v", n)
	}
	if hb := e.heartbeat(t, `{"node_key": "node-a"}`); hb.ProbeRequested {
		t.Error("probe requested again")
	}
}

func TestComputeDeleteAndOfflineProbe(t *testing.T) {
	e := newComputeEnv(t, "")
	id := e.register(t, "node-a", "a")

	wantError(t, e.do("DELETE", nodeURL(id, ""), nil), 409, codeConflict, "节点在线，先停止该节点上的 trainer 服务再删除")
	e.clock.Advance(35 * time.Second) // 3 x 10s + 5s: still online
	wantError(t, e.do("DELETE", nodeURL(id, ""), nil), 409, codeConflict, "节点在线")
	want[compute.Node](t, e.do("POST", nodeURL(id, "/probe"), nil), 202)

	e.clock.Advance(time.Second)
	n := want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200)
	if n.Status != compute.StatusOffline || len(n.History) != 1 || n.Metrics == nil {
		t.Errorf("offline node = %+v", n)
	}
	wantError(t, e.do("POST", nodeURL(id, "/probe"), nil), 409, codeConflict, "节点离线，无法重新检测")

	rec := e.do("DELETE", nodeURL(id, ""), nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("delete = %d %q", rec.Code, rec.Body)
	}
	wantError(t, e.do("GET", nodeURL(id, ""), nil), 404, codeNotFound, "节点不存在")
	wantError(t, e.do("DELETE", nodeURL(id, ""), nil), 404, codeNotFound, "节点不存在")
	wantError(t, e.do("POST", nodeURL(id, "/probe"), nil), 404, codeNotFound, "节点不存在")
	if l := want[List[compute.Node]](t, e.do("GET", "/api/v1/compute/nodes", nil), 200); l.Total != 0 || l.Items == nil {
		t.Errorf("list = %+v", l)
	}
}

func TestComputeRequestValidation(t *testing.T) {
	e := newComputeEnv(t, "")
	register := func(body string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/compute/agent/register", body)
	}

	// Bodies over 1 MiB.
	big := registerBody("node-a", "a", `{"issues": [{"level": "info", "message": "`+strings.Repeat("x", 1<<20)+`"}]}`, "null")
	wantError(t, register(big), 413, codePayloadTooLarge, "请求内容过大")
	wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", `{"node_key": "k", "padding": "`+strings.Repeat("x", 1<<20)+`"}`),
		413, codePayloadTooLarge, "")
	// Just under the limit is fine.
	padded := `{"padding": "` + strings.Repeat("x", (1<<20)-len(registerBody("node-p", "p", "null", "null"))-20) + `", ` +
		strings.TrimPrefix(registerBody("node-p", "p", "null", "null"), "{")
	if len(padded) >= 1<<20 {
		t.Fatalf("padded body is %d bytes", len(padded))
	}
	want[compute.RegisterResult](t, register(padded), 200)

	for _, body := range []string{"", "{", "null", "[]", `"x"`, `{"node_key": "a"} {}`} {
		wantError(t, register(body), 400, codeBadRequest, "请求格式不正确")
		wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", body), 400, codeBadRequest, "请求格式不正确")
	}
	for body, msg := range map[string]string{
		`{"name": "a", "hardware": {}}`:                                                                  "node_key 不能为空",
		`{"node_key": "a b", "name": "a", "hardware": {}}`:                                               "node_key 只能由",
		`{"node_key": 5, "name": "a", "hardware": {}}`:                                                   "node_key 不能为空",
		`{"node_key": "k", "hardware": {}}`:                                                              "节点名称不能为空",
		`{"node_key": "k", "name": "a\nb", "hardware": {}}`:                                              "控制字符",
		`{"node_key": "k", "name": "a"}`:                                                                 "缺少硬件信息",
		`{"node_key": "k", "name": "a", "hardware": null}`:                                               "缺少硬件信息",
		`{"node_key": "k", "name": "a", "hardware": [1]}`:                                                "缺少硬件信息",
		`{"node_key": "k", "name": "a", "hardware": {}, "hostname": "` + strings.Repeat("h", 256) + `"}`: "主机名不能超过 255 个字符",
	} {
		wantError(t, register(body), 422, codeValidationFailed, msg)
	}
	wantError(t, e.do("POST", "/api/v1/compute/agent/heartbeat", `{"node_key": ""}`), 422, codeValidationFailed, "node_key 不能为空")
}

func TestComputeLenientReports(t *testing.T) {
	e := newComputeEnv(t, "")
	// A newer agent with unknown fields and an older one with odd values
	// both register; bad numbers become null instead of failing.
	id := want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register", `{
		"node_key": "node-a", "name": "a", "future": {"x": 1},
		"hardware": {"os": "Debian", "cpu": {"logical_cores": 8.0, "max_mhz": NaN}, "gpus": [{"index": 0, "name": "T4", "future": 1}],
			"gpu_detection": {"method": "rocm"}, "new_section": {}},
		"metrics": {"cpu_percent": Infinity, "load_average": [1, 2, -Infinity], "memory_used_bytes": -1,
			"gpus": [{"index": 0, "temperature_c": NaN, "power_watts": "high"}]}
	}`), 200).NodeID
	raw := want[map[string]any](t, e.do("GET", nodeURL(id, ""), nil), 200)
	hw := object(t, raw["hardware"], "hardware")
	cpu := object(t, hw["cpu"], "cpu")
	// Agents before 0.4.0 report no platform: null, which means Linux.
	if cpu["logical_cores"] != 8.0 || cpu["max_mhz"] != nil || object(t, hw["gpu_detection"], "detection")["method"] != "none" ||
		hw["platform"] != nil {
		t.Errorf("hardware = %v", hw)
	}
	m := object(t, raw["metrics"], "metrics")
	if m["cpu_percent"] != nil || m["load_average"] != nil || m["memory_used_bytes"] != nil {
		t.Errorf("metrics = %v", m)
	}
	if g := firstItem(t, m["gpus"], "gpus"); g["temperature_c"] != nil || g["power_watts"] != nil || g["index"] != 0.0 {
		t.Errorf("gpu metrics = %v", g)
	}
	// The heartbeat with NaN values succeeds as well.
	e.heartbeat(t, `{"node_key": "node-a", "metrics": {"cpu_percent": NaN, "gpus": []}}`)

	// The platform is normalized, so the frontend can compare it with
	// "windows".
	e.heartbeat(t, `{"node_key": "node-a", "hardware": {"os": "Windows 11 Pro 23H2", "arch": "AMD64", "platform": " Windows "}}`)
	hw = object(t, want[map[string]any](t, e.do("GET", nodeURL(id, ""), nil), 200)["hardware"], "hardware")
	if hw["platform"] != "windows" || hw["os"] != "Windows 11 Pro 23H2" || hw["shm_bytes"] != nil {
		t.Errorf("windows hardware = %v", hw)
	}
}

func TestComputeNodeOrder(t *testing.T) {
	e := newComputeEnv(t, "")
	e.register(t, "k1", "zulu")
	e.register(t, "k2", "Bravo")
	e.clock.Advance(time.Hour) // k1 and k2 go offline
	e.register(t, "k3", "charlie")
	e.register(t, "k4", "alpha")
	e.register(t, "k5", "ALPHA")
	l := want[List[compute.Node]](t, e.do("GET", "/api/v1/compute/nodes", nil), 200)
	var got []string
	for _, n := range l.Items {
		got = append(got, n.NodeKey)
	}
	if strings.Join(got, ",") != "k4,k5,k3,k2,k1" || l.Total != 5 {
		t.Errorf("order = %v (total %d), want online first, then by name ignoring case, then by ID", got, l.Total)
	}
}

func TestComputeNodeAddress(t *testing.T) {
	send := func(e *computeEnv, remote string, header ...string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/compute/agent/register",
			strings.NewReader(registerBody("node-a", "a", "null", "null")))
		req.RemoteAddr = remote
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		id := want[compute.RegisterResult](t, rec, 200).NodeID
		return want[compute.Node](t, e.do("GET", nodeURL(id, ""), nil), 200).Address
	}
	direct := newComputeEnv(t, "")
	if got := send(direct, "10.0.0.1:5000", "X-Forwarded-For", "203.0.113.7"); got != "10.0.0.1" {
		t.Errorf("without trusted proxies the header is ignored: %q", got)
	}
	if got := send(direct, "[::ffff:192.0.2.9]:5000"); got != "192.0.2.9" {
		t.Errorf("v4-mapped address = %q", got)
	}
	if got := send(direct, "[2001:db8::1]:5000"); got != "2001:db8::1" {
		t.Errorf("IPv6 address = %q", got)
	}
	if got := send(direct, "not-an-address"); got != "" {
		t.Errorf("unparsable peer = %q, want \"\"", got)
	}
	proxied := newComputeEnv(t, "", "10.0.0.0/8")
	if got := send(proxied, "10.0.0.1:5000", "X-Forwarded-For", "203.0.113.7"); got != "203.0.113.7" {
		t.Errorf("behind a trusted proxy = %q", got)
	}
}
