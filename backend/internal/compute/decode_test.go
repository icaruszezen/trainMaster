package compute

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNullDecode(t *testing.T) {
	check := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	}
	floats := map[string]Null[float64]{
		`12.5`:  Value(12.5),
		`0`:     Value(0.0),
		`-3`:    Value(-3.0), // range checks are normalization's job
		`null`:  {},
		`"12"`:  {},
		`true`:  {},
		`[1]`:   {},
		`{}`:    {},
		`1e400`: {}, // overflows float64
	}
	for in, want := range floats {
		t.Run("float "+in, func(t *testing.T) {
			var got Null[float64]
			if err := json.Unmarshal([]byte(in), &got); err != nil {
				t.Fatal(err)
			}
			check(t, got, want)
		})
	}
	ints := map[string]Null[int64]{
		`3`:                    Value[int64](3),
		`-7`:                   Value[int64](-7),
		`2.0`:                  Value[int64](2), // Python floats for whole numbers
		`1.5e10`:               Value[int64](15_000_000_000),
		`9223372036854775807`:  Value[int64](9223372036854775807),
		`-9223372036854775808`: Value[int64](-9223372036854775808),
		`9223372036854775808`:  {},
		`9.3e18`:               {},
		`1.5`:                  {},
		`"3"`:                  {},
		`null`:                 {},
		`false`:                {},
	}
	for in, want := range ints {
		t.Run("int "+in, func(t *testing.T) {
			var got Null[int64]
			if err := json.Unmarshal([]byte(in), &got); err != nil {
				t.Fatal(err)
			}
			check(t, got, want)
		})
	}
	var s Null[string]
	_ = json.Unmarshal([]byte(`"a"`), &s)
	check(t, s, Value("a"))
	_ = json.Unmarshal([]byte(`5`), &s)
	check(t, s, Null[string]{})
	var b Null[bool]
	_ = json.Unmarshal([]byte(`true`), &b)
	check(t, b, Value(true))
	_ = json.Unmarshal([]byte(`"true"`), &b)
	check(t, b, Null[bool]{})
}

func TestNullDecodeResetsPreviousValue(t *testing.T) {
	n := Value(1.5)
	if err := json.Unmarshal([]byte(`"x"`), &n); err != nil || n.Valid {
		t.Errorf("n = %+v, %v; want null", n, err)
	}
}

func TestNullEncode(t *testing.T) {
	type obj struct {
		A Null[float64]  `json:"a"`
		B Null[int64]    `json:"b"`
		C Null[string]   `json:"c"`
		D Null[bool]     `json:"d"`
		E []Null[string] `json:"e"`
	}
	data, err := json.Marshal(obj{A: Value(1.5), C: Value("x"), E: []Null[string]{{}, Value("y")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":1.5,"b":null,"c":"x","d":null,"e":[null,"y"]}`; string(data) != want {
		t.Errorf("got %s, want %s", data, want)
	}
}

func TestNonFiniteToNull(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"a": NaN}`, `{"a": null}`},
		{`[NaN,Infinity,-Infinity]`, `[null,null,null]`},
		{"[\n\tNaN\n]", "[\n\tnull\n]"},
		{`{"s": "NaN and -Infinity", "x": NaN}`, `{"s": "NaN and -Infinity", "x": null}`},
		{`{"s": "a\"NaN\" b", "x": 1}`, `{"s": "a\"NaN\" b", "x": 1}`},
		{`{"s": "\\", "x": Infinity}`, `{"s": "\\", "x": null}`},
		{`{"NaN": 1, "Infinity": [2]}`, `{"NaN": 1, "Infinity": [2]}`},
		{`{"a": NaNx}`, `{"a": NaNx}`}, // not a token; stays invalid JSON
		{`-Infinity`, `null`},
	}
	for _, tt := range tests {
		if got := string(nonFiniteToNull([]byte(tt.in))); got != tt.want {
			t.Errorf("nonFiniteToNull(%s) = %s, want %s", tt.in, got, tt.want)
		}
	}
	plain := []byte(`{"a": 1}`)
	if got := nonFiniteToNull(plain); &got[0] != &plain[0] {
		t.Error("input without non-finite tokens should be returned as is")
	}
}

func TestDecodeHeartbeatLenient(t *testing.T) {
	body := `{
		"node_key": "node-1", "future_field": {"x": [1, 2]},
		"metrics": {
			"cpu_percent": NaN, "load_average": [NaN, 1, 2], "memory_used_bytes": "lots",
			"memory_total_bytes": 1024.0, "disk_path": 7, "gpus": [
				{"index": 1, "temperature_c": Infinity, "power_watts": -Infinity, "fan_percent": 30, "new": true},
				5, null, {"index": "0"}
			],
			"unknown_section": {}
		},
		"hardware": "not an object",
		"environment": null
	}`
	in, err := DecodeHeartbeat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if in.NodeKey != "node-1" || in.Hardware != nil || in.Environment != nil || in.Metrics == nil {
		t.Fatalf("input = %+v", in)
	}
	m := in.Metrics
	if m.CPUPercent.Valid || m.MemoryUsedBytes.Valid || m.DiskPath.Valid {
		t.Errorf("invalid values should be null: %+v", m)
	}
	if m.MemoryTotalBytes != Value[int64](1024) {
		t.Errorf("memory_total_bytes = %+v", m.MemoryTotalBytes)
	}
	if want := []Null[float64]{{}, Value(1.0), Value(2.0)}; !reflect.DeepEqual(m.LoadAverage, want) {
		t.Errorf("load_average = %+v, want %+v", m.LoadAverage, want)
	}
	if len(m.GPUs) != 4 || m.GPUs[0].Index != Value[int64](1) || m.GPUs[0].TemperatureC.Valid ||
		m.GPUs[0].PowerWatts.Valid || m.GPUs[0].FanPercent != Value(30.0) {
		t.Errorf("gpus = %+v", m.GPUs)
	}
	// After normalization only the GPU with a valid index is left, and
	// the load average with an unknown value is unknown.
	n := normalizeMetrics(m)
	if len(n.GPUs) != 1 || n.LoadAverage != nil {
		t.Errorf("normalized = %+v", n)
	}
}

func TestDecodeRegister(t *testing.T) {
	in, err := DecodeRegister([]byte(`{"node_key": "k", "name": "gpu-01", "hostname": "h", "agent_version": "0.1.0",
		"hardware": {"os": "Ubuntu", "cpu": 5, "gpus": {"0": {}}, "gpu_driver": "550"},
		"environment": {"status": "ready", "issues": [{"level": "error", "message": "x"}]},
		"metrics": [], "extra": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.NodeKey != "k" || in.Name != "gpu-01" || in.Hostname != "h" || in.AgentVersion != "0.1.0" {
		t.Errorf("fields = %+v", in)
	}
	if in.Hardware == nil || in.Hardware.OS != Value("Ubuntu") || in.Hardware.GPUs != nil {
		t.Errorf("hardware = %+v", in.Hardware)
	}
	// A driver section that is not an object carries no version.
	if h := normalizeHardware(*in.Hardware); h.GPUDriver != nil {
		t.Errorf("gpu_driver = %+v", h.GPUDriver)
	}
	if in.Environment == nil || in.Environment.Status != EnvReady || len(in.Environment.Issues) != 1 {
		t.Errorf("environment = %+v", in.Environment)
	}
	if in.Metrics != nil {
		t.Errorf("metrics that are not an object should be absent: %+v", in.Metrics)
	}

	// Wrong types of the top-level fields are left empty for validation.
	in, err = DecodeRegister([]byte(`{"node_key": 5, "name": ["x"]}`))
	if err != nil || in.NodeKey != "" || in.Name != "" || in.Hardware != nil {
		t.Errorf("DecodeRegister = %+v, %v", in, err)
	}
}

func TestDecodeRejectsNonJSON(t *testing.T) {
	for _, body := range []string{``, `{`, `{"node_key": "k"} x`, `{"a": 1,}`, `null`, `[]`, `"x"`, `5`, ` `} {
		if _, err := DecodeRegister([]byte(body)); err == nil {
			t.Errorf("DecodeRegister(%q) should fail", body)
		}
		if _, err := DecodeHeartbeat([]byte(body)); err == nil {
			t.Errorf("DecodeHeartbeat(%q) should fail", body)
		}
	}
	if _, err := DecodeHeartbeat([]byte(" \n{\"node_key\": \"k\"}\n")); err != nil {
		t.Errorf("whitespace around the object should be fine: %v", err)
	}
}

func TestDecodeSectionKeepsUnicode(t *testing.T) {
	in, err := DecodeRegister([]byte(`{"hardware": {"cpu": {"model": "鲲鹏 920 \u0000"}, "os": "` +
		strings.Repeat("é", 300) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	h := normalizeHardware(*in.Hardware)
	if h.CPU.Model != Value("鲲鹏 920") {
		t.Errorf("model = %+v", h.CPU.Model)
	}
	if got := h.OS.V; got != strings.Repeat("é", maxText) {
		t.Errorf("os = %q (%d bytes)", got, len(got))
	}
}
