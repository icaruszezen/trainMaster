package checkpoint

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode/utf8"
)

// Structural handling of the callables found in checkpoints. The rule is
// the same everywhere: a known constructor becomes the Go value it would
// build in Python, anything else a *pyObject that only records what would
// have been called. Arguments are inspected, never trusted: a wrong type
// gives a placeholder, not an error.

// maxTensorDims, maxPathParts and maxPathLen bound the work per REDUCE:
// the arguments can be huge tuples or strings reused through the memo.
// What the handlers build still counts against the byte budget, as the
// results can be kept a million times over.
const (
	maxTensorDims = 64
	maxPathParts  = 256
	maxPathLen    = 8 << 10 // bytes of a joined path (PATH_MAX is 4096)
)

// call handles REDUCE, OBJ and INST: fn(*args).
func (u *unpickler) call(fn any, args []any) (any, error) {
	g, ok := fn.(*pyGlobal)
	if !ok {
		return &pyObject{args: args}, nil
	}
	switch g.qual {
	case "collections.OrderedDict", "builtins.dict":
		d := &pyDict{}
		if len(args) > 0 {
			if err := u.fillDict(d, args[0]); err != nil {
				return nil, err
			}
		}
		return d, nil
	case "collections.defaultdict": // (default_factory, [mapping]); OrderedDict-style items follow via SETITEMS
		d := &pyDict{}
		if len(args) > 1 {
			if err := u.fillDict(d, args[1]); err != nil {
				return nil, err
			}
		}
		return d, nil
	case "builtins.set", "builtins.frozenset":
		s := &pySet{frozen: g.name == "frozenset"}
		if len(args) > 0 {
			items := iterate(args[0])
			if err := u.alloc(len(items)); err != nil {
				return nil, err
			}
			s.items = append(s.items, items...)
		}
		return s, nil
	case "builtins.list", "builtins.tuple":
		var items []any
		if len(args) > 0 {
			src := iterate(args[0])
			if err := u.alloc(len(src)); err != nil {
				return nil, err
			}
			items = append(items, src...)
		}
		if g.name == "list" {
			return &pyList{items: items}, nil
		}
		return &pyTuple{items: items}, nil
	case "builtins.bytes", "builtins.bytearray":
		return u.newBytes(args)
	case "_codecs.encode": // how protocols 0–2 pickle bytes: encode(text, "latin1")
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				encoding := "utf-8"
				if len(args) > 1 {
					encoding, _ = args[1].(string)
				}
				return u.encodeString(s, encoding)
			}
		}
	case "copyreg._reconstructor": // (cls, base, state): object.__new__(cls)
		if len(args) > 0 {
			return newObject(args[0], nil), nil
		}
	case "copyreg.__newobj__": // (cls, *args)
		if len(args) > 0 {
			return newObject(args[0], args[1:]), nil
		}
	case "copyreg.__newobj_ex__": // (cls, args, kwargs)
		if len(args) > 1 {
			a, _ := sequence(args[1])
			return newObject(args[0], a), nil
		}
	case "torch._utils._rebuild_parameter", "torch._utils._rebuild_parameter_with_state":
		var t *pyTensor
		if len(args) > 0 {
			t, _ = args[0].(*pyTensor)
		}
		return newParameter(t), nil
	case "torch._tensor._rebuild_from_type_v2": // (func, new_type, args, state): tensor subclasses
		var t *pyTensor
		if len(args) > 2 {
			if f, ok := args[0].(*pyGlobal); ok {
				if a, ok := sequence(args[2]); ok {
					t = rebuildTensor(f, a)
				}
			}
		}
		if t != nil {
			if err := u.allocBytes(8 * len(t.shape)); err != nil {
				return nil, err
			}
		}
		if len(args) > 1 {
			if c, ok := args[1].(*pyGlobal); ok && c.name == "Parameter" {
				return newParameter(t), nil
			}
		}
		if t == nil {
			t = &pyTensor{}
		}
		return t, nil
	case "torch.Size": // a tuple subclass
		if len(args) > 0 {
			if t, ok := args[0].(*pyTuple); ok {
				return t, nil
			}
		}
	case "numpy.core.multiarray.scalar", "numpy._core.multiarray.scalar":
		if v, ok := numpyScalar(args); ok {
			return v, nil
		}
	default:
		if t := rebuildTensor(g, args); t != nil {
			if err := u.allocBytes(8 * len(t.shape)); err != nil {
				return nil, err
			}
			return t, nil
		}
		if (g.module == "pathlib" || g.module == "pathlib._local") && isPathClass(g.name) {
			n, ok := pathLen(args)
			if ok {
				if err := u.allocBytes(n); err != nil {
					return nil, err
				}
				return joinPath(args, strings.Contains(g.name, "Windows")), nil
			}
		}
	}
	return &pyObject{class: g, args: args}, nil
}

// newObject handles NEWOBJ: cls.__new__(cls, *args).
func newObject(cls any, args []any) any {
	g, _ := cls.(*pyGlobal)
	if g != nil {
		switch g.qual {
		case "collections.OrderedDict", "collections.defaultdict", "builtins.dict":
			return &pyDict{}
		case "builtins.set":
			return &pySet{}
		case "builtins.frozenset":
			return &pySet{frozen: true}
		case "builtins.list":
			return &pyList{}
		}
	}
	return &pyObject{class: g, args: args}
}

// build handles BUILD: inst.__setstate__(state), or an update of the
// instance __dict__ (with an optional slot state: (dict, slots)).
func (u *unpickler) build(inst, state any) error {
	o, ok := inst.(*pyObject)
	if !ok {
		return nil // e.g. attributes of a dict subclass: never needed
	}
	if t, ok := state.(*pyTuple); ok && len(t.items) == 2 {
		d, ok1 := t.items[0].(*pyDict)
		s, ok2 := t.items[1].(*pyDict)
		if (ok1 || t.items[0] == nil) && (ok2 || t.items[1] == nil) {
			if err := u.mergeAttrs(o, d); err != nil {
				return err
			}
			return u.mergeAttrs(o, s)
		}
	}
	switch s := state.(type) {
	case *pyDict:
		return u.mergeAttrs(o, s)
	case nil:
		return nil
	}
	o.state = state
	return nil
}

func (u *unpickler) mergeAttrs(o *pyObject, d *pyDict) error {
	switch {
	case d == nil || o.attrs == d:
		return nil
	case o.attrs == nil:
		o.attrs = d // the usual case: no copy
		return nil
	}
	if err := u.alloc(d.len()); err != nil {
		return err
	}
	for i, k := range d.keys {
		if err := u.dictSet(o.attrs, k, d.vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// dictSet is d.set, with the key a tuple key builds counted against the
// byte budget (a str or bytes key is not copied).
func (u *unpickler) dictSet(d *pyDict, k, v any) error {
	hk, ok := hashKey(k)
	if tk, isTuple := hk.(tupleKey); ok && isTuple {
		if err := u.allocBytes(len(tk)); err != nil {
			return err
		}
	}
	d.setHashed(k, hk, ok, v)
	return nil
}

// fillDict copies the items of a mapping or of a sequence of pairs.
func (u *unpickler) fillDict(d *pyDict, src any) error {
	if m, ok := src.(*pyDict); ok {
		if err := u.alloc(m.len()); err != nil {
			return err
		}
		for i, k := range m.keys {
			if err := u.dictSet(d, k, m.vals[i]); err != nil {
				return err
			}
		}
		return nil
	}
	items, _ := sequence(src)
	if err := u.alloc(len(items)); err != nil {
		return err
	}
	for _, item := range items {
		if kv, ok := sequence(item); ok && len(kv) == 2 {
			if err := u.dictSet(d, kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// appendItems handles APPEND and APPENDS.
func (u *unpickler) appendItems(target any, items []any) error {
	if err := u.alloc(len(items)); err != nil {
		return err
	}
	switch t := target.(type) {
	case *pyList:
		t.items = append(t.items, items...)
	case *pyObject: // a list subclass: obj.append / obj.extend
		t.items = append(t.items, items...)
	default:
		return u.corrupt("不能向 %s 追加元素", typeName(target))
	}
	return nil
}

// setItems handles DICT, SETITEM and SETITEMS.
func (u *unpickler) setItems(target any, kv []any) error {
	if len(kv)%2 != 0 {
		return u.corrupt("字典的键和值不成对")
	}
	if err := u.alloc(len(kv) / 2); err != nil {
		return err
	}
	var d *pyDict
	switch t := target.(type) {
	case *pyDict:
		d = t
	case *pyObject: // a dict subclass: obj[key] = value
		if t.dict == nil {
			t.dict = &pyDict{}
		}
		d = t.dict
	default:
		return u.corrupt("不能向 %s 写入键值", typeName(target))
	}
	for i := 0; i < len(kv); i += 2 {
		if err := u.dictSet(d, kv[i], kv[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// addItems handles ADDITEMS.
func (u *unpickler) addItems(target any, items []any) error {
	if err := u.alloc(len(items)); err != nil {
		return err
	}
	switch t := target.(type) {
	case *pySet:
		if t.frozen {
			return u.corrupt("不能向 frozenset 添加元素")
		}
		t.items = append(t.items, items...)
	case *pyObject: // obj.add(item)
		t.items = append(t.items, items...)
	default:
		return u.corrupt("不能向 %s 添加元素", typeName(target))
	}
	return nil
}

// iterate returns the items of a list, tuple or set (the keys of a dict).
func iterate(v any) []any {
	switch x := v.(type) {
	case *pyList:
		return x.items
	case *pyTuple:
		return x.items
	case *pySet:
		return x.items
	case *pyDict:
		return x.keys
	}
	return nil
}

// newBytes handles bytes(...) and bytearray(...): (), (bytes,), (str,
// encoding) or (list of ints,).
func (u *unpickler) newBytes(args []any) (any, error) {
	if len(args) == 0 {
		return pyBytes(""), nil
	}
	switch x := args[0].(type) {
	case pyBytes:
		return x, nil
	case string:
		encoding := "utf-8"
		if len(args) > 1 {
			encoding, _ = args[1].(string)
		}
		return u.encodeString(x, encoding)
	}
	if items, ok := sequence(args[0]); ok && len(items) <= 1<<16 {
		if err := u.allocBytes(len(items)); err != nil {
			return nil, err
		}
		b := make([]byte, 0, len(items))
		for _, item := range items {
			n, ok := asInt(item)
			if !ok || n < 0 || n > 255 {
				return &pyObject{args: args}, nil
			}
			b = append(b, byte(n))
		}
		return pyBytes(b), nil
	}
	return &pyObject{args: args}, nil
}

// encodeString is str.encode for the encodings pickles use. Only Latin-1
// copies the text (UTF-8 is how strings are kept already).
func (u *unpickler) encodeString(s, encoding string) (pyBytes, error) {
	if !isLatin1(encoding) {
		return pyBytes(s), nil
	}
	if err := u.allocBytes(len(s)); err != nil {
		return "", err
	}
	return latin1(s), nil
}

// isLatin1 recognizes the names of the Latin-1 encoding.
func isLatin1(encoding string) bool {
	if len(encoding) > 16 { // a crafted name is not copied below
		return false
	}
	switch strings.ReplaceAll(strings.ToLower(encoding), "_", "-") {
	case "latin1", "latin-1", "iso-8859-1", "iso8859-1", "l1":
		return true
	}
	return false
}

// latin1 encodes s as Latin-1 (characters beyond it become '?').
func latin1(s string) pyBytes {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			r = '?'
		}
		b = append(b, byte(r))
	}
	return pyBytes(b)
}

// rebuildTensor handles the torch._utils functions that rebuild a tensor
// from its storage; it returns nil for any other callable.
func rebuildTensor(g *pyGlobal, args []any) *pyTensor {
	if g.module != "torch._utils" {
		return nil
	}
	sizeArg := -1
	switch g.name {
	case "_rebuild_tensor_v2", "_rebuild_tensor", "_rebuild_qtensor", "_rebuild_wrapper_subclass":
		sizeArg = 2 // (storage | cls, storage_offset | dtype, size, stride, ...)
	case "_rebuild_meta_tensor_no_storage":
		sizeArg = 1 // (dtype, size, stride, requires_grad)
	case "_rebuild_sparse_tensor", "_rebuild_nested_tensor", "_rebuild_device_tensor_from_numpy",
		"_rebuild_device_tensor_from_cpu_tensor", "_rebuild_xla_tensor", "_rebuild_mtia_tensor":
		return &pyTensor{} // a tensor of unknown shape
	default:
		return nil
	}
	t := &pyTensor{}
	if sizeArg >= len(args) {
		return t
	}
	dims, ok := sequence(args[sizeArg])
	if !ok || len(dims) > maxTensorDims {
		return t
	}
	shape := make([]int64, len(dims))
	for i, d := range dims {
		n, ok := asInt(d)
		if !ok || n < 0 {
			return t
		}
		shape[i] = n
	}
	t.shape, t.known = shape, true
	return t
}

// newParameter wraps a tensor as an nn.Parameter (a new object, so a
// parameter referenced twice through the memo is counted once).
func newParameter(t *pyTensor) *pyTensor {
	if t == nil {
		return &pyTensor{param: true}
	}
	return &pyTensor{shape: t.shape, known: t.known, param: true}
}

func isPathClass(name string) bool {
	switch name {
	case "Path", "PosixPath", "WindowsPath", "PurePath", "PurePosixPath", "PureWindowsPath":
		return true
	}
	return false
}

// pathLen checks that args are the parts of a pathlib path, at most
// maxPathParts strings of at most maxPathLen bytes in all, and returns an
// upper bound of the length of the joined path; false gives a placeholder.
func pathLen(args []any) (int, bool) {
	if len(args) > maxPathParts {
		return 0, false
	}
	n := 0
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			return 0, false
		}
		if n += len(s) + 1; n > maxPathLen {
			return 0, false
		}
	}
	return n, true
}

// joinPath rebuilds a pathlib path from its pickled parts (strings, as
// checked by pathLen) as a string.
func joinPath(args []any, windows bool) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		s, _ := a.(string)
		absolute := strings.HasPrefix(s, "/") || (windows && (strings.HasPrefix(s, `\`) || (len(s) >= 2 && s[1] == ':')))
		if absolute {
			parts = parts[:0] // an absolute part replaces what came before
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	sep := "/"
	if windows {
		sep = `\`
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 && !strings.HasSuffix(parts[i-1], "/") && !strings.HasSuffix(parts[i-1], `\`) {
			b.WriteString(sep)
		}
		b.WriteString(p)
	}
	return b.String()
}

// numpyScalar decodes numpy.core.multiarray.scalar(dtype, data): how numpy
// pickles np.float64(0.5) and the like (older Ultralytics versions saved
// metrics this way).
func numpyScalar(args []any) (any, bool) {
	if len(args) < 2 {
		return nil, false
	}
	dt, ok := args[0].(*pyObject)
	if !ok || dt.class == nil || dt.class.qual != "numpy.dtype" || len(dt.args) == 0 {
		return nil, false
	}
	code, ok := dt.args[0].(string)
	if !ok || len(code) > 16 {
		return nil, false
	}
	// The data is at most 8 bytes (8 characters of a str); larger values,
	// possibly memoized strings of megabytes, are not copied.
	var data []byte
	switch x := args[1].(type) {
	case pyBytes:
		if len(x) > 8 {
			return nil, false
		}
		data = []byte(x)
	case string:
		if len(x) > 8*utf8.UTFMax {
			return nil, false
		}
		data = []byte(latin1(x))
	default:
		return nil, false
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if st, ok := dt.state.(*pyTuple); ok && len(st.items) > 1 && st.items[1] == ">" {
		order = binary.BigEndian
	}
	switch numpyKind(strings.TrimLeft(code, "<>=|")) {
	case 'b':
		if len(data) == 1 {
			return data[0] != 0, true
		}
	case 'f':
		switch len(data) {
		case 2:
			return halfToFloat(order.Uint16(data)), true
		case 4:
			return float64(math.Float32frombits(order.Uint32(data))), true
		case 8:
			return math.Float64frombits(order.Uint64(data)), true
		}
	case 'i':
		switch len(data) {
		case 1:
			return int64(int8(data[0])), true
		case 2:
			return int64(int16(order.Uint16(data))), true
		case 4:
			return int64(int32(order.Uint32(data))), true
		case 8:
			return int64(order.Uint64(data)), true
		}
	case 'u':
		switch len(data) {
		case 1:
			return int64(data[0]), true
		case 2:
			return int64(order.Uint16(data)), true
		case 4:
			return int64(order.Uint32(data)), true
		case 8:
			if v := order.Uint64(data); v <= math.MaxInt64 {
				return int64(v), true
			}
			return bigInt{}, true
		}
	}
	return nil, false
}

// numpyKind maps a numpy dtype code (as numpy pickles it: "f8", "i4", "?"
// ..., or a type name) to its kind: 'b' bool, 'f' float, 'i' signed or
// 'u' unsigned integer; 0 for anything else (complex, strings, ...).
func numpyKind(code string) byte {
	switch code {
	case "?", "bool":
		return 'b'
	case "e", "f", "d", "float16", "float32", "float64":
		return 'f'
	case "b", "h", "i", "l", "q", "int8", "int16", "int32", "int64":
		return 'i'
	case "B", "H", "I", "L", "Q", "uint8", "uint16", "uint32", "uint64":
		return 'u'
	}
	if len(code) == 2 && code[1] >= '1' && code[1] <= '8' {
		switch code[0] {
		case 'b', 'f', 'i', 'u':
			return code[0]
		}
	}
	return 0
}

// halfToFloat converts an IEEE 754 half-precision value.
func halfToFloat(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	exp := int(h>>10) & 0x1f
	frac := float64(h & 0x3ff)
	switch exp {
	case 0:
		return sign * math.Ldexp(frac, -24)
	case 0x1f:
		if frac != 0 {
			return math.NaN()
		}
		return math.Inf(int(sign))
	}
	return sign * math.Ldexp(1+frac/1024, exp-15)
}
