package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
)

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type serviceEnv struct {
	svc   *Service
	clock *testClock
	db    *database.DB
}

func newServiceEnv(t *testing.T) *serviceEnv {
	t.Helper()
	db := dbtest.New(t)
	clock := &testClock{t: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	svc := NewService(Options{DB: db.Pool, Logger: slog.New(slog.DiscardHandler), HeartbeatInterval: 10 * time.Second, Now: clock.Now})
	return &serviceEnv{svc: svc, clock: clock, db: db}
}

func sampleHardware(gpus ...string) *Hardware {
	h := &Hardware{
		OS:   Value("Ubuntu 22.04.4 LTS"),
		Arch: Value("x86_64"),
		CPU:  HardwareCPU{Model: Value("Intel(R) Xeon(R) Gold 6330"), LogicalCores: Value[int64](56)},
		GPUs: []GPU{},
	}
	for i, name := range gpus {
		h.GPUs = append(h.GPUs, GPU{Index: Value(int64(i)), Name: name, ComputeCapability: Value("8.9")})
	}
	if len(gpus) > 0 {
		h.GPUDriver = &GPUDriver{DriverVersion: "550.54.15", CUDADriverVersion: Value("12.4")}
		h.GPUDetection.Method = DetectionNVML
	}
	return h
}

func sampleEnvironment(variant string) *Environment {
	return &Environment{
		Status: EnvReady, Accelerator: AcceleratorCUDA, TorchVariant: Value(variant), RecommendedVariant: "cu126",
		CUDAAvailable: true, CUDADeviceCount: Value[int64](1),
		SelfTest: SelfTest{Status: SelfTestPassed, Device: Value("cuda:0")}, ProbedAt: "2026-11-02T07:59:00Z",
	}
}

func sampleMetrics(cpu float64) *Metrics {
	return &Metrics{CPUPercent: Value(cpu), MemoryUsedBytes: Value[int64](1 << 30),
		GPUs: []GPUMetrics{{Index: Value[int64](0), UtilizationPercent: Value(cpu), MemoryUsedBytes: Value[int64](1 << 20)}}}
}

func (e *serviceEnv) register(t *testing.T, key, name string, env *Environment, m *Metrics) RegisterResult {
	t.Helper()
	res, err := e.svc.Register(context.Background(), RegisterInput{NodeKey: key, Name: name, Hostname: name + ".lan",
		AgentVersion: "0.1.0", Hardware: sampleHardware("NVIDIA GeForce RTX 4090"), Environment: env, Metrics: m}, "192.0.2.10")
	if err != nil {
		t.Fatalf("Register(%s): %v", key, err)
	}
	return res
}

func (e *serviceEnv) get(t *testing.T, id int64) Node {
	t.Helper()
	n, err := e.svc.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return n
}

func wantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || !errors.Is(err, kind) || !strings.Contains(e.Message, msgPart) {
		t.Errorf("err = %v, want kind %v containing %q", err, kind, msgPart)
	}
}

func TestServiceRegisterListHeartbeat(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	start := e.clock.Now()

	res := e.register(t, "node-a", " gpu-server-01 ", nil, sampleMetrics(10))
	if res.NodeID <= 0 || res.HeartbeatIntervalSeconds != 10 || !res.ServerTime.Equal(start) {
		t.Fatalf("register result = %+v", res)
	}
	nodes, err := e.svc.List(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("List = %+v, %v", nodes, err)
	}
	n := nodes[0]
	// Registered without an environment: the node is checking it.
	if n.ID != res.NodeID || n.NodeKey != "node-a" || n.Name != "gpu-server-01" || n.Hostname != "gpu-server-01 .lan" ||
		n.Address != "192.0.2.10" || n.AgentVersion != "0.1.0" || n.Status != StatusOnline || !n.ProbePending {
		t.Errorf("node = %+v", n)
	}
	if !n.RegisteredAt.Equal(start) || !n.LastSeenAt.Equal(start) || !n.CreatedAt.Equal(start) {
		t.Errorf("times = %v %v %v, want %v", n.RegisteredAt, n.LastSeenAt, n.CreatedAt, start)
	}
	if n.Environment != nil || n.Metrics == nil || n.Metrics.CPUPercent != Value(10.0) {
		t.Errorf("environment = %+v, metrics = %+v", n.Environment, n.Metrics)
	}
	if len(n.Hardware.GPUs) != 1 || n.Hardware.GPUs[0].Name != "NVIDIA GeForce RTX 4090" || n.Hardware.GPUDriver == nil {
		t.Errorf("hardware = %+v", n.Hardware)
	}
	if len(n.History) != 1 || !n.History[0].T.Equal(start) || n.History[0].CPUPercent != Value(10.0) {
		t.Errorf("history = %+v", n.History)
	}

	// Heartbeats move last_seen_at and add history points; the first
	// environment completes the check.
	e.clock.Advance(10 * time.Second)
	hb, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Metrics: sampleMetrics(50)}, "192.0.2.11")
	if err != nil || hb.NodeID != res.NodeID || hb.HeartbeatIntervalSeconds != 10 || !hb.ProbeRequested {
		t.Fatalf("Heartbeat = %+v, %v", hb, err)
	}
	e.clock.Advance(10 * time.Second)
	hb, err = e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Environment: sampleEnvironment("cu126")}, "192.0.2.11")
	if err != nil || hb.ProbeRequested {
		t.Fatalf("Heartbeat with environment = %+v, %v", hb, err)
	}
	n = e.get(t, res.NodeID)
	if !n.LastSeenAt.Equal(start.Add(20*time.Second)) || !n.RegisteredAt.Equal(start) || n.Address != "192.0.2.11" || n.ProbePending {
		t.Errorf("after heartbeats: last seen %v, registered %v, address %q, probe pending %v",
			n.LastSeenAt, n.RegisteredAt, n.Address, n.ProbePending)
	}
	if n.Environment == nil || n.Environment.TorchVariant != Value("cu126") || n.Environment.Issues == nil {
		t.Errorf("environment = %+v", n.Environment)
	}
	// The heartbeat without metrics kept the last ones and added no point.
	if n.Metrics.CPUPercent != Value(50.0) || len(n.History) != 2 || n.History[1].CPUPercent != Value(50.0) {
		t.Errorf("metrics = %+v, history = %+v", n.Metrics, n.History)
	}
	if g := n.History[1].GPUs; len(g) != 1 || g[0].Index != 0 || g[0].UtilizationPercent != Value(50.0) {
		t.Errorf("history gpus = %+v", g)
	}

	// Hardware in a heartbeat replaces the stored one.
	hw := sampleHardware("NVIDIA A100", "NVIDIA A100")
	if _, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Hardware: hw}, ""); err != nil {
		t.Fatal(err)
	}
	if n = e.get(t, res.NodeID); len(n.Hardware.GPUs) != 2 || n.Address != "" {
		t.Errorf("hardware gpus = %+v, address %q", n.Hardware.GPUs, n.Address)
	}
}

func TestServiceStoresNormalizedReports(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	hw := messyHardware()
	res, err := e.svc.Register(ctx, RegisterInput{NodeKey: "k", Name: "n", Hardware: &hw,
		Environment: messyEnvironment(), Metrics: &Metrics{CPUPercent: Value(-1.0)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	wantHW, _ := json.Marshal(normalizeHardware(hw))
	wantEnv, _ := json.Marshal(normalizeEnvironment(messyEnvironment(), e.clock.Now()))
	wantMetrics, _ := json.Marshal(normalizeMetrics(&Metrics{}))
	var hwOK, envOK, metricsOK bool
	if err := e.db.QueryRow(ctx, `SELECT hardware = $2::jsonb, environment = $3::jsonb, metrics = $4::jsonb
		FROM compute_nodes WHERE id = $1`, res.NodeID, wantHW, wantEnv, wantMetrics).Scan(&hwOK, &envOK, &metricsOK); err != nil {
		t.Fatal(err)
	}
	if !hwOK || !envOK || !metricsOK {
		t.Errorf("stored sections are not the normalized ones: hardware %v, environment %v, metrics %v", hwOK, envOK, metricsOK)
	}
	// And the API returns exactly them.
	n := e.get(t, res.NodeID)
	if got, _ := json.Marshal(n.Hardware); string(got) != string(wantHW) {
		t.Errorf("hardware\n got %s\nwant %s", got, wantHW)
	}
	if got, _ := json.Marshal(n.Environment); string(got) != string(wantEnv) {
		t.Errorf("environment\n got %s\nwant %s", got, wantEnv)
	}
}

func TestServiceRegisterValidation(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	valid := RegisterInput{NodeKey: "k", Name: "n", Hardware: sampleHardware()}
	for _, tt := range []struct {
		change func(*RegisterInput)
		msg    string
	}{
		{func(in *RegisterInput) { in.NodeKey = "" }, "node_key 不能为空"},
		{func(in *RegisterInput) { in.NodeKey = "bad key" }, "node_key 只能由"},
		{func(in *RegisterInput) { in.Name = " " }, "节点名称不能为空"},
		{func(in *RegisterInput) { in.Name = strings.Repeat("n", 65) }, "节点名称不能超过 64 个字符"},
		{func(in *RegisterInput) { in.Hostname = strings.Repeat("h", 256) }, "主机名不能超过 255 个字符"},
		{func(in *RegisterInput) { in.AgentVersion = strings.Repeat("1", 65) }, "节点版本号不能超过 64 个字符"},
		{func(in *RegisterInput) { in.Hardware = nil }, "缺少硬件信息"},
	} {
		in := valid
		tt.change(&in)
		_, err := e.svc.Register(ctx, in, "")
		wantKind(t, err, ErrInvalid, tt.msg)
	}
	if nodes, err := e.svc.List(ctx); err != nil || len(nodes) != 0 {
		t.Errorf("invalid registrations stored nodes: %+v, %v", nodes, err)
	}
	_, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "a/b"}, "")
	wantKind(t, err, ErrInvalid, "node_key")
}

func TestServiceUnknownNode(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	_, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "never-registered", Metrics: sampleMetrics(1)}, "")
	wantKind(t, err, ErrUnknownNode, "请重新注册")

	res := e.register(t, "gone", "gone", nil, sampleMetrics(1))
	e.clock.Advance(time.Hour)
	if err := e.svc.Delete(ctx, res.NodeID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "gone"}, "")
	wantKind(t, err, ErrUnknownNode, "请重新注册")

	// Registering again creates a new node without the old history.
	again := e.register(t, "gone", "gone", nil, nil)
	if again.NodeID == res.NodeID {
		t.Errorf("a deleted node got its old ID back")
	}
	if n := e.get(t, again.NodeID); len(n.History) != 0 || n.Metrics != nil {
		t.Errorf("new node = %+v", n)
	}
}

func TestServiceNodeID(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	a := e.register(t, "node-a", "a", nil, nil)
	b := e.register(t, "node-b", "b", nil, nil)
	for key, want := range map[string]int64{"node-a": a.NodeID, "node-b": b.NodeID} {
		if id, err := e.svc.NodeID(ctx, key); err != nil || id != want {
			t.Errorf("NodeID(%s) = %d, %v, want %d", key, id, err, want)
		}
	}
	_, err := e.svc.NodeID(ctx, "never-registered")
	wantKind(t, err, ErrUnknownNode, "请重新注册")
	_, err = e.svc.NodeID(ctx, "")
	wantKind(t, err, ErrInvalid, "node_key 不能为空")
	_, err = e.svc.NodeID(ctx, "bad key/")
	wantKind(t, err, ErrInvalid, "node_key 只能由")

	// A deleted node is unknown.
	e.clock.Advance(time.Hour)
	if err := e.svc.Delete(ctx, a.NodeID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.NodeID(ctx, "node-a")
	wantKind(t, err, ErrUnknownNode, "请重新注册")
	// Looking a node up does not count as a heartbeat.
	if n := e.get(t, b.NodeID); n.Status != StatusOffline {
		t.Errorf("status = %s", n.Status)
	}
	if _, err := e.svc.NodeID(ctx, "node-b"); err != nil {
		t.Fatal(err)
	}
	if n := e.get(t, b.NodeID); n.Status != StatusOffline || !n.LastSeenAt.Equal(b.ServerTime) {
		t.Errorf("node after NodeID = %+v", n)
	}
}

func TestServiceReRegisterKeepsEnvironment(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	first := e.register(t, "node-a", "old-name", sampleEnvironment("cu126"), sampleMetrics(20))
	if n := e.get(t, first.NodeID); n.ProbePending {
		t.Error("a registration with an environment left a probe pending")
	}

	// The node restarts: it registers its hardware without an environment
	// (that follows later); the stored one stays to avoid flicker, marked
	// as being checked again.
	e.clock.Advance(time.Minute)
	restart := e.clock.Now()
	res, err := e.svc.Register(ctx, RegisterInput{NodeKey: "node-a", Name: "new-name", AgentVersion: "0.2.0",
		Hardware: sampleHardware("NVIDIA A100", "NVIDIA A100")}, "192.0.2.20")
	if err != nil {
		t.Fatal(err)
	}
	if res.NodeID != first.NodeID {
		t.Fatalf("re-registration got ID %d, want %d", res.NodeID, first.NodeID)
	}
	n := e.get(t, res.NodeID)
	if n.Name != "new-name" || n.AgentVersion != "0.2.0" || n.Hostname != "" || n.Address != "192.0.2.20" || len(n.Hardware.GPUs) != 2 {
		t.Errorf("node = %+v", n)
	}
	if !n.RegisteredAt.Equal(restart) || !n.LastSeenAt.Equal(restart) || n.CreatedAt.Equal(restart) {
		t.Errorf("times: registered %v, last seen %v, created %v", n.RegisteredAt, n.LastSeenAt, n.CreatedAt)
	}
	if n.Environment == nil || n.Environment.TorchVariant != Value("cu126") || n.Metrics == nil || n.Metrics.CPUPercent != Value(20.0) {
		t.Errorf("environment = %+v, metrics = %+v", n.Environment, n.Metrics)
	}
	if !n.ProbePending {
		t.Error("a registration without an environment should mark the stored one as being checked again")
	}
	if len(n.History) != 1 {
		t.Errorf("history = %+v", n.History)
	}
	// Heartbeats tell the node, which ignores it while it is detecting.
	e.clock.Advance(10 * time.Second)
	if hb, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Metrics: sampleMetrics(30)}, ""); err != nil || !hb.ProbeRequested {
		t.Fatalf("heartbeat = %+v, %v; want probe_requested", hb, err)
	}
	// The new result completes the check.
	e.clock.Advance(10 * time.Second)
	if hb, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Environment: sampleEnvironment("cu128")}, ""); err != nil || hb.ProbeRequested {
		t.Fatalf("heartbeat with environment = %+v, %v; want the request cleared", hb, err)
	}
	if n = e.get(t, res.NodeID); n.ProbePending || n.Environment.TorchVariant != Value("cu128") {
		t.Errorf("after the check: probe pending %v, environment %+v", n.ProbePending, n.Environment)
	}

	// An environment in the registration replaces the stored one.
	e.register(t, "node-a", "new-name", sampleEnvironment("cu130"), nil)
	if n = e.get(t, res.NodeID); n.Environment.TorchVariant != Value("cu130") || n.ProbePending {
		t.Errorf("environment = %+v, probe pending %v", n.Environment, n.ProbePending)
	}
	if nodes, _ := e.svc.List(ctx); len(nodes) != 1 {
		t.Errorf("%d nodes after re-registering", len(nodes))
	}
}

func TestServiceWarnsAboutSharedNodeKey(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	var buf bytes.Buffer
	e.svc.log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	register := func(hostname, address string) int64 {
		t.Helper()
		res, err := e.svc.Register(ctx, RegisterInput{NodeKey: "cloned", Name: "gpu", Hostname: hostname,
			Hardware: sampleHardware("NVIDIA GeForce RTX 4090")}, address)
		if err != nil {
			t.Fatal(err)
		}
		return res.NodeID
	}
	warnings := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for line := range strings.Lines(buf.String()) {
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("log line %q: %v", line, err)
			}
			out = append(out, m)
		}
		buf.Reset()
		return out
	}

	id := register("gpu-a", "192.0.2.1")
	// The same machine restarts its trainer (compose keeps the hostname).
	e.clock.Advance(5 * time.Second)
	register("GPU-A", "192.0.2.1")
	e.clock.Advance(5 * time.Second)
	register("gpu-a", "192.0.2.1")
	if w := warnings(); len(w) != 0 {
		t.Fatalf("restarts logged %v", w)
	}

	// A second machine with a copy of the node-key starts while the first
	// is online.
	e.clock.Advance(e.svc.OfflineAfter())
	if again := register("gpu-b", "192.0.2.2"); again != id {
		t.Fatalf("got node %d, want %d", again, id)
	}
	w := warnings()
	if len(w) != 1 {
		t.Fatalf("warnings = %v, want one", w)
	}
	if w[0]["level"] != "WARN" || w[0]["node_id"] != float64(id) || w[0]["hostname"] != "gpu-b" || w[0]["address"] != "192.0.2.2" ||
		w[0]["previous_hostname"] != "gpu-a" || w[0]["previous_address"] != "192.0.2.1" ||
		!strings.Contains(w[0]["hint"].(string), "两台机器使用了同一个节点标识") || !strings.Contains(w[0]["hint"].(string), "node-key") {
		t.Errorf("warning = %v", w[0])
	}

	// Not suspected: a node that was offline (moved, or renamed while
	// stopped), or one that does not report its hostname.
	e.clock.Advance(e.svc.OfflineAfter() + time.Microsecond)
	register("gpu-c", "192.0.2.3")
	e.clock.Advance(time.Second)
	register("", "192.0.2.3")
	e.clock.Advance(time.Second)
	register("gpu-c", "192.0.2.3")
	if w := warnings(); len(w) != 0 {
		t.Errorf("logged %v", w)
	}
}

func TestServiceHistoryAfterOutage(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	res := e.register(t, "node-a", "a", nil, sampleMetrics(1))
	e.clock.Advance(10 * time.Second)
	if _, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Metrics: sampleMetrics(2)}, ""); err != nil {
		t.Fatal(err)
	}

	// The machine is shut down overnight; meanwhile its card keeps its
	// last samples.
	e.clock.Advance(12 * time.Hour)
	if n := e.get(t, res.NodeID); n.Status != StatusOffline || len(n.History) != 2 {
		t.Fatalf("offline node: status %s, %d history points", n.Status, len(n.History))
	}
	// In the morning its curve starts afresh instead of joining last
	// night's samples.
	e.register(t, "node-a", "a", nil, sampleMetrics(3))
	n := e.get(t, res.NodeID)
	if len(n.History) != 1 || n.History[0].CPUPercent != Value(3.0) || !n.History[0].T.Equal(e.clock.Now()) {
		t.Errorf("history after the outage = %+v", n.History)
	}
	// A heartbeat late by up to the offline threshold continues it.
	e.clock.Advance(e.svc.OfflineAfter())
	if _, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Metrics: sampleMetrics(4)}, ""); err != nil {
		t.Fatal(err)
	}
	if n = e.get(t, res.NodeID); len(n.History) != 2 || n.History[1].CPUPercent != Value(4.0) {
		t.Errorf("history = %+v", n.History)
	}
}

func TestServiceProbe(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	res := e.register(t, "node-a", "a", sampleEnvironment("cu126"), nil)

	n, err := e.svc.RequestProbe(ctx, res.NodeID)
	if err != nil || !n.ProbePending || n.ID != res.NodeID || n.Status != StatusOnline || n.History == nil {
		t.Fatalf("RequestProbe = %+v, %v", n, err)
	}
	// Heartbeats tell the node until it reports an environment.
	e.clock.Advance(10 * time.Second)
	hb, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Metrics: sampleMetrics(1)}, "")
	if err != nil || !hb.ProbeRequested {
		t.Fatalf("heartbeat = %+v, %v; want probe_requested", hb, err)
	}
	// A registration does not clear the request, even with an environment
	// (the node may have detected it before the request).
	e.register(t, "node-a", "a", sampleEnvironment("cu126"), nil)
	if n = e.get(t, res.NodeID); !n.ProbePending {
		t.Error("registration cleared the probe request")
	}
	hb, err = e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a", Hardware: sampleHardware(), Environment: sampleEnvironment("cu130")}, "")
	if err != nil || hb.ProbeRequested {
		t.Fatalf("heartbeat with environment = %+v, %v; want the request cleared", hb, err)
	}
	if n = e.get(t, res.NodeID); n.ProbePending || n.Environment.TorchVariant != Value("cu130") || len(n.Hardware.GPUs) != 0 {
		t.Errorf("node after probe = %+v", n)
	}
	hb, _ = e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "node-a"}, "")
	if hb.ProbeRequested {
		t.Error("probe still requested")
	}

	// Offline nodes cannot probe; unknown ones do not exist.
	e.clock.Advance(35*time.Second + time.Microsecond)
	_, err = e.svc.RequestProbe(ctx, res.NodeID)
	wantKind(t, err, ErrConflict, "节点离线，无法重新检测")
	if n = e.get(t, res.NodeID); n.ProbePending || n.Status != StatusOffline {
		t.Errorf("offline node = %+v", n)
	}
	_, err = e.svc.RequestProbe(ctx, 999999)
	wantKind(t, err, ErrNotFound, "节点不存在")
}

func TestServiceDelete(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	res := e.register(t, "node-a", "a", nil, sampleMetrics(1))

	wantKind(t, e.svc.Delete(ctx, res.NodeID), ErrConflict, "节点在线")
	// Exactly 3 x interval + 5s after the last heartbeat it is still online.
	e.clock.Advance(35 * time.Second)
	if n := e.get(t, res.NodeID); n.Status != StatusOnline {
		t.Fatalf("status at the boundary = %s", n.Status)
	}
	wantKind(t, e.svc.Delete(ctx, res.NodeID), ErrConflict, "先停止该节点上的 trainer 服务再删除")
	e.clock.Advance(time.Microsecond)
	if n := e.get(t, res.NodeID); n.Status != StatusOffline || len(n.History) != 1 {
		t.Fatalf("node past the boundary = %+v", n)
	}
	if err := e.svc.Delete(ctx, res.NodeID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(e.svc.history.Points(res.NodeID)) != 0 {
		t.Error("history kept after delete")
	}
	_, err := e.svc.Get(ctx, res.NodeID)
	wantKind(t, err, ErrNotFound, "节点不存在")
	wantKind(t, e.svc.Delete(ctx, res.NodeID), ErrNotFound, "节点不存在")
}

func TestServiceListOrder(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	// Offline: registered long ago.
	e.register(t, "k1", "delta", nil, nil)
	e.register(t, "k2", "Bravo", nil, nil)
	e.clock.Advance(time.Hour)
	e.register(t, "k3", "charlie", nil, nil)
	e.register(t, "k4", "alpha", nil, nil)
	e.register(t, "k5", "Alpha", nil, nil) // same name ignoring case: by ID

	nodes, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.NodeKey+":"+n.Status)
	}
	want := []string{"k4:online", "k5:online", "k3:online", "k2:offline", "k1:offline"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v, want %v", got, want)
	}

	// A node that comes back moves up.
	if _, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: "k1"}, ""); err != nil {
		t.Fatal(err)
	}
	nodes, _ = e.svc.List(ctx)
	if nodes[3].NodeKey != "k1" || nodes[3].Status != StatusOnline || nodes[4].NodeKey != "k2" {
		t.Errorf("after k1 came back: %s %s", nodes[3].NodeKey, nodes[4].NodeKey)
	}
}

// TestServiceConcurrentHeartbeats is meant for -race: heartbeats of
// several nodes, page loads and probe requests at the same time.
func TestServiceConcurrentHeartbeats(t *testing.T) {
	e := newServiceEnv(t)
	ctx := context.Background()
	ids := []int64{
		e.register(t, "k1", "a", nil, nil).NodeID,
		e.register(t, "k2", "b", nil, nil).NodeID,
	}
	var wg sync.WaitGroup
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := []string{"k1", "k2"}[g%2]
			for i := range 20 {
				if _, err := e.svc.Heartbeat(ctx, HeartbeatInput{NodeKey: key, Metrics: sampleMetrics(float64(i))}, ""); err != nil {
					t.Error(err)
					return
				}
				if _, err := e.svc.List(ctx); err != nil {
					t.Error(err)
					return
				}
				if _, err := e.svc.RequestProbe(ctx, ids[g%2]); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if n := e.get(t, id); len(n.History) != 60 || !n.ProbePending {
			t.Errorf("node %d: %d history points, probe pending %v", id, len(n.History), n.ProbePending)
		}
	}
}
