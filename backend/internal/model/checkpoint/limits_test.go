package checkpoint

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pk builds pickles opcode by opcode for the crafted inputs below.
type pk struct{ bytes.Buffer }

func (p *pk) op(s string) *pk { p.WriteString(s); return p }

// str writes a BINUNICODE.
func (p *pk) str(s string) *pk {
	p.WriteByte(opBinUnicode)
	_ = binary.Write(p, binary.LittleEndian, uint32(len(s)))
	p.WriteString(s)
	return p
}

func (p *pk) global(module, name string) *pk { return p.op("c" + module + "\n" + name + "\n") }

func (p *pk) put(i int) *pk {
	p.WriteByte(opLongBinPut)
	_ = binary.Write(p, binary.LittleEndian, uint32(i))
	return p
}

func (p *pk) get(i int) *pk {
	p.WriteByte(opLongBinGet)
	_ = binary.Write(p, binary.LittleEndian, uint32(i))
	return p
}

func (p *pk) int(n int) *pk {
	p.WriteByte(opBinInt)
	_ = binary.Write(p, binary.LittleEndian, int32(n))
	return p
}

// repeat writes what f writes n times.
func (p *pk) repeat(n int, f func(*pk)) *pk {
	var one pk
	f(&one)
	for i := 0; i < n; i++ {
		p.Write(one.Bytes())
	}
	return p
}

// refs writes MARK, n memo references to i and the opcode closing the
// mark (TUPLE, APPENDS ...).
func (p *pk) refs(i, n int, closing string) *pk {
	p.op("(")
	return p.repeat(n, func(q *pk) { q.get(i) }).op(closing)
}

// allocated returns the bytes f allocates, and those still in use after
// it (by what it returns).
func allocated(f func()) (total, retained uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	total = after.TotalAlloc - before.TotalAlloc
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	return total, retained
}

// craftedModel wraps the state of a DetectionModel (a pickle fragment
// leaving a dict on the stack) in a checkpoint: {"model": model}.
func craftedModel(state func(p *pk)) []byte {
	var p pk
	p.op("\x80\x02}").str("model").global("ultralytics.nn.tasks", "DetectionModel").op(")\x81")
	state(&p)
	p.op("bs.")
	return p.Bytes()
}

// TestCraftedAmplification runs pickles of a few kilobytes that reference
// one large string many times through the memo, each in a way that used to
// copy or scan the whole string every time: every one must be rejected or
// parsed without allocating more than a small multiple of its size.
func TestCraftedAmplification(t *testing.T) {
	const mb = 1 << 20
	big := strings.Repeat("a", 4*mb)
	ctl := "\x01" + strings.Repeat("c", mb)
	digits := strings.Repeat("7", mb)
	tests := []struct {
		name   string
		pickle []byte
		reason string // "" when Inspect succeeds
		want   string // part of the message
	}{
		// #1: built strings.
		{"STACK_GLOBAL of memoized strings", new(pk).op("\x80\x04").str(big).put(0).op("]").
			repeat(100, func(p *pk) { p.get(0).get(0).op("\x93a") }).op(".").Bytes(),
			ReasonTooLarge, "字符串总长度超过上限"},
		{"pathlib path of 256 memoized parts", new(pk).str(strings.Repeat("p", mb)).put(0).op("0]").
			global("pathlib", "PosixPath").put(1).op("0").refs(0, 256, "t").put(2).op("0").
			repeat(200, func(p *pk) { p.get(1).get(2).op("Ra") }).op(".").Bytes(),
			ReasonNotUltralytics, "list"},
		{"pathlib paths at the length limit", new(pk).str(strings.Repeat("p", 4000)).put(0).op("0]").
			global("pathlib", "PosixPath").put(1).op("0").refs(0, 2, "t").put(2).op("0").
			repeat(5000, func(p *pk) { p.get(1).get(2).op("Ra") }).op(".").Bytes(),
			ReasonTooLarge, "字符串总长度超过上限"},
		{"Latin-1 encoding of a memoized string", new(pk).str(big).put(0).op("0]").global("_codecs", "encode").put(1).
			op("0").get(0).str("latin1").op("\x86").put(2).op("0").
			repeat(200, func(p *pk) { p.get(1).get(2).op("Ra") }).op(".").Bytes(),
			ReasonTooLarge, "字符串总长度超过上限"},
		{"encoding name of megabytes", new(pk).str("x").put(0).op("0").str(big).put(1).op("0]").global("_codecs", "encode").
			put(2).op("0").get(0).get(1).op("\x86").put(3).op("0").
			repeat(2000, func(p *pk) { p.get(2).get(3).op("Ra") }).op(".").Bytes(),
			ReasonNotUltralytics, "list"},
		{"bytes of a list of 65536 ints", new(pk).op("(").repeat(1<<16, func(p *pk) { p.op("K\x07") }).op("l").put(0).
			op("\x85").put(1).op("0]").global("builtins", "bytes").put(2).op("0").
			repeat(1000, func(p *pk) { p.get(2).get(1).op("Ra") }).op(".").Bytes(),
			ReasonTooLarge, "字符串总长度超过上限"},
		{"numpy scalar of megabytes", new(pk).global("numpy", "dtype").str("f8").op("\x85R").put(0).op("0").str(big).put(1).
			op("0]").global("numpy.core.multiarray", "scalar").put(2).op("0").get(0).get(1).op("\x86").put(3).op("0").
			repeat(2000, func(p *pk) { p.get(2).get(3).op("Ra") }).op(".").Bytes(),
			ReasonNotUltralytics, "list"},
		{"tensor shapes of 64 dims", new(pk).op("(").repeat(64, func(p *pk) { p.op("K\x01") }).op("t").put(0).op("0").
			op("(").op("N").int(0).get(0).op("N").op("t").put(1).op("0]").global("torch._utils", "_rebuild_tensor_v2").
			put(2).op("0").repeat(100000, func(p *pk) { p.get(2).get(1).op("Ra") }).op(".").Bytes(),
			ReasonTooLarge, "字符串总长度超过上限"},

		// #2: dict keys.
		{"tuple key of memoized strings set again and again", new(pk).str(strings.Repeat("\x01", mb)).put(0).op("0").
			refs(0, 32, "t").put(1).op("0}").repeat(20000, func(p *pk) { p.get(1).op("Ns") }).op(".").Bytes(),
			ReasonNotUltralytics, "缺少 model / ema"},
		{"distinct tuple keys of memoized strings", func() []byte {
			p := new(pk).str(strings.Repeat("\x01", mb)).put(0).op("0}")
			for i := 0; i < 2000; i++ {
				p.op("(").int(i).repeat(31, func(q *pk) { q.get(0) }).op("tNs")
			}
			return p.op(".").Bytes()
		}(), ReasonNotUltralytics, "缺少 model / ema"},
		{"distinct tuple keys at the size limit", func() []byte {
			p := new(pk).str(strings.Repeat("k", 1000)).put(0).op("0}")
			for i := 0; i < 100000; i++ {
				p.int(i).get(0).op("\x86Ns")
			}
			return p.op(".").Bytes()
		}(), ReasonTooLarge, "字符串总长度超过上限"},
		{"string key of megabytes in a large dict", func() []byte {
			p := new(pk).str(big).put(0).op("0}")
			for i := 0; i < dictIndexMin; i++ {
				p.str("k" + string(rune('0'+i))).op("Ns")
			}
			return p.repeat(20000, func(q *pk) { q.get(0).op("Ns") }).op(".").Bytes()
		}(), ReasonNotUltralytics, "缺少 model / ema"},

		// #3: extraction.
		{"65536 class names referencing one string", craftedModel(func(p *pk) {
			p.op("}").str("names").str(ctl).put(0).op("0]").repeat(64, func(q *pk) { q.refs(0, 1024, "e") }).op("s")
		}), "", ""},
		{"class names keyed by a long digit string", craftedModel(func(p *pk) {
			p.op("}").str("names").str(digits).put(0).op("0}").repeat(20000, func(q *pk) { q.get(0).op("Ns") }).op("s")
		}), "", ""},
		{"metrics keyed by one long string", new(pk).op("}").str("model").global("ultralytics.nn.tasks", "DetectionModel").op(")\x81s").
			str("train_metrics").str(ctl).put(0).op("0}").repeat(20000, func(p *pk) { p.get(0).op("G\x3f\xf0\x00\x00\x00\x00\x00\x00s") }).
			op("s.").Bytes(), "", ""},
		{"yaml layers with a module name of megabytes", craftedModel(func(p *pk) {
			p.op("}").str("yaml").op("}").str("backbone").op("(").int(-1).int(1).str(ctl).op("]tq\x00").op("0]").
				repeat(64, func(q *pk) { q.op("(").repeat(1000, func(r *pk) { r.op("h\x00") }).op("e") }).op("ss")
		}), "", ""},
		{"layers of a class named with megabytes", craftedModel(func(p *pk) {
			p.op("}").str("_modules").op("}").str("model").global("torch.nn.modules.container", "Sequential").op(")\x81").
				op("}").str("_modules").op("}").str("ultralytics.nn.modules.block").str(digits).op("\x93").put(0).op("0")
			for i := 0; i < 5000; i++ {
				p.str(strings.Repeat("i", 1+i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))).
					get(0).op(")\x81s")
			}
			p.op("sbss")
		}), ReasonUnsafe, "ultralytics.nn.modules.block.777"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := torchZip("best", tt.pickle)
			var info *Info
			var err error
			start := time.Now()
			n, kept := allocated(func() { info, err = inspectBytes(file) })
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
			} else {
				var e *Error
				if !errors.As(err, &e) || e.Reason != tt.reason || !strings.Contains(e.Message, tt.want) {
					t.Fatalf("Inspect = %v, want %s: …%s…", err, tt.reason, tt.want)
				}
			}
			// The file is read into memory twice (the zip, data.pkl); the
			// byte budget adds 16 MiB plus its size.
			if limit := uint64(6*len(file) + 64<<20); n > limit {
				t.Errorf("allocated %d MB, want at most %d MB", n>>20, limit>>20)
			}
			// Info keeps at most 65536 class names of 256 bytes, not the
			// strings they were cut from.
			if kept > 24<<20 {
				t.Errorf("Info keeps %d MB", kept>>20)
			}
			t.Logf("%d KB pickle: %d MB allocated, %d MB kept, %v", len(tt.pickle)>>10, n>>20, kept>>20,
				time.Since(start).Round(time.Millisecond))
			if info != nil {
				checkInfo(t, info)
			}
			runtime.KeepAlive(info)
		})
	}
}

// TestClassNamesLabeledOnce: keys that give the same class id (1, "01",
// "001" ...) do not make extraction clean the name again for each.
func TestClassNamesLabeledOnce(t *testing.T) {
	ctl := "\x01" + strings.Repeat("c", 1<<20)
	d := &pyDict{}
	for pad := 0; pad < 19; pad++ {
		for id := 0; id < 4096; id++ {
			d.set(strings.Repeat("0", pad)+strconv.Itoa(id), ctl)
		}
	}
	var names []string
	n, kept := allocated(func() { names, _ = classNames(d) })
	if len(names) != 4096 || names[7] != "�"+strings.Repeat("c", maxClassNameLen-3) {
		t.Fatalf("%d names, names[7] = %.20q", len(names), names[7])
	}
	if n > 8<<20 || kept > 2<<20 {
		t.Errorf("allocated %d MB, kept %d MB", n>>20, kept>>20)
	}
	runtime.KeepAlive(names)
}

// TestUnpickleTimeLimit: the run time is checked every timeCheckOps
// opcodes.
func TestUnpickleTimeLimit(t *testing.T) {
	lim := defaultLimits
	lim.time = time.Nanosecond
	data := strings.Repeat("N0", timeCheckOps) + "N."
	_, err := unpickle([]byte(data), lim)
	var pe *pickleError
	if !errors.As(err, &pe) || !pe.tooLarge || !strings.Contains(err.Error(), "解析时间超过上限") {
		t.Fatalf("err = %v, want the time limit", err)
	}
	if _, err := unpickle([]byte(strings.Repeat("N0", timeCheckOps/2-2)+"N."), lim); err != nil {
		t.Errorf("before the first check: %v", err)
	}
	lim.time = 0 // no limit
	if _, err := unpickle([]byte(data), lim); err != nil {
		t.Errorf("without a time limit: %v", err)
	}
}

func TestByteBudget(t *testing.T) {
	// GLOBAL counts the qualified name it builds against lim.bytes plus
	// the size of data.pkl.
	data := "cm\nn\n" + strings.Repeat("0cm\nn\n", 9) + "."
	lim := limits{ops: 100, values: 100, stack: 10, marks: 3, memo: 2, bytes: 0}
	if _, err := unpickle([]byte(data), lim); err != nil {
		t.Errorf("within the budget: %v", err)
	}
	data = "\x80\x04X\x14\x00\x00\x00" + strings.Repeat("m", 20) + "q\x00" + strings.Repeat("h\x00h\x00\x930", 20) + "N."
	_, err := unpickle([]byte(data), lim)
	var pe *pickleError
	if !errors.As(err, &pe) || !pe.tooLarge || !strings.Contains(err.Error(), "字符串总长度超过上限") {
		t.Fatalf("err = %v, want the byte budget", err)
	}
	lim.bytes = 1000
	if _, err := unpickle([]byte(data), lim); err != nil {
		t.Errorf("with a larger budget: %v", err)
	}

	// A module torch renames differently ("exceptions" is not renamed by
	// torch) builds a second name, which counts too.
	stackGlobals := func(module string) string {
		return "\x80\x04\x8c" + string(rune(len(module))) + module + "q\x00X\x14\x00\x00\x00" + strings.Repeat("m", 20) + "q\x01" +
			strings.Repeat("h\x00h\x01\x930", 4) + "N."
	}
	lim.bytes = 100
	if _, err := unpickle([]byte(stackGlobals("collections")), lim); err != nil {
		t.Errorf("collections: %v", err)
	}
	if _, err := unpickle([]byte(stackGlobals("exceptions")), lim); err == nil || !strings.Contains(err.Error(), "字符串总长度超过上限") {
		t.Errorf("exceptions: err = %v, want the byte budget", err)
	}
}
