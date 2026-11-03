package checkpoint

import (
	"math"
	"strconv"
	"strings"
)

// Values built by the unpickler. Python objects map to Go values:
//
//	None → nil, bool → bool, int → int64 (bigInt when it does not fit),
//	float → float64, str → string, bytes / bytearray → pyBytes,
//	tuple → *pyTuple, list → *pyList, dict / OrderedDict → *pyDict,
//	set / frozenset → *pySet, a class or function → *pyGlobal,
//	an instance of any other class → *pyObject, a tensor or nn.Parameter →
//	*pyTensor, a persistent id (a torch storage) → *pyStorage.
//
// Containers are pointers, so a value referenced twice through the memo is
// the same Go value: walkers rely on this to detect cycles and to count a
// shared nn.Parameter once.
type (
	// bigInt is an int that does not fit in int64. Its value is never
	// needed, so it is not kept.
	bigInt struct{}
	// pyBytes holds the contents of a bytes or bytearray object.
	pyBytes string

	pyTuple struct{ items []any }
	pyList  struct{ items []any }
	pySet   struct {
		items  []any // not deduplicated: nothing reads sets
		frozen bool
	}

	// pyGlobal is a reference to a class or function (GLOBAL and
	// STACK_GLOBAL). Nothing is ever imported or called.
	pyGlobal struct {
		module, name string
		qual         string // module + "." + name
	}

	// pyObject is an instance created by REDUCE, NEWOBJ, OBJ or INST that
	// is not handled structurally.
	pyObject struct {
		class *pyGlobal // nil when the callable was not a class reference
		args  []any     // constructor arguments
		attrs *pyDict   // instance __dict__ from BUILD, nil if none
		state any       // BUILD state that is not a dict
		items []any     // APPEND(S) / ADDITEMS on the object (list or set subclasses)
		dict  *pyDict   // SETITEM(S) on the object (dict subclasses)
	}

	// pyTensor stands in for a torch.Tensor or nn.Parameter: only the shape
	// is known (the data lives in the zip and is never read).
	pyTensor struct {
		shape []int64 // nil with known == false when the shape is unknown
		known bool
		param bool // nn.Parameter
	}

	// pyStorage stands in for a torch storage (a persistent id).
	pyStorage struct{}
)

func newGlobal(module, name string) *pyGlobal {
	// Protocols 0–2 use the Python 2 module names (as Python's own
	// Unpickler maps them with fix_imports).
	switch module {
	case "__builtin__", "exceptions":
		module = "builtins"
	case "copy_reg":
		module = "copyreg"
	}
	return &pyGlobal{module: module, name: name, qual: module + "." + name}
}

// pyDict is an insertion-ordered dict. Keys that Python can hash are
// deduplicated (a later SETITEM replaces the value in place, as in
// Python); unhashable or unusual keys are simply appended.
type pyDict struct {
	keys  []any
	vals  []any
	hkeys []any       // normalized keys (see hashKey), nil when not hashable
	index map[any]int // normalized key → position, built once the dict grows
}

// dictIndexMin is the size from which a dict keeps a map index; smaller
// dicts (most of them: module attributes) are scanned.
const dictIndexMin = 8

func (d *pyDict) find(hk any) int {
	if d.index != nil {
		if i, ok := d.index[hk]; ok {
			return i
		}
		return -1
	}
	for i, k := range d.hkeys {
		if k != nil && k == hk {
			return i
		}
	}
	return -1
}

func (d *pyDict) set(k, v any) {
	hk, ok := hashKey(k)
	if ok {
		if i := d.find(hk); i >= 0 {
			d.vals[i] = v
			return
		}
	} else {
		hk = nil
	}
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
	d.hkeys = append(d.hkeys, hk)
	switch {
	case d.index != nil && hk != nil:
		d.index[hk] = len(d.keys) - 1
	case d.index == nil && len(d.keys) >= dictIndexMin:
		d.index = make(map[any]int, len(d.keys)*2)
		for i, k := range d.hkeys {
			if k != nil {
				if _, dup := d.index[k]; !dup {
					d.index[k] = i
				}
			}
		}
	}
}

// get looks up a string key.
func (d *pyDict) get(key string) (any, bool) {
	if d == nil {
		return nil, false
	}
	if i := d.find(key); i >= 0 {
		return d.vals[i], true
	}
	return nil, false
}

func (d *pyDict) len() int {
	if d == nil {
		return 0
	}
	return len(d.keys)
}

// values returns the values in insertion order (none for a nil dict).
func (d *pyDict) values() []any {
	if d == nil {
		return nil
	}
	return d.vals
}

// Normalized dict keys. They are all comparable, so comparing two of them
// as interfaces never panics.
type (
	noneKey  struct{}
	bytesKey string
	tupleKey string
)

// Bounds for hashing tuple keys: a crafted pickle can nest tuples deeply
// or make them huge, and every SETITEM hashes its key.
const (
	maxTupleKeyDepth = 4
	maxTupleKeyItems = 32
)

// hashKey returns the normalized form of a dict key, equal for keys that
// Python considers equal (1, 1.0 and True; equal strings; equal tuples of
// those), or false when the key is unhashable or not worth hashing.
func hashKey(v any) (any, bool) {
	budget := maxTupleKeyItems
	return hashKeyDepth(v, 0, &budget)
}

func hashKeyDepth(v any, depth int, budget *int) (any, bool) {
	switch x := v.(type) {
	case nil:
		return noneKey{}, true
	case string:
		return x, true
	case int64:
		return x, true
	case bool:
		if x {
			return int64(1), true
		}
		return int64(0), true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<62 {
			return int64(x), true
		}
		if math.IsNaN(x) { // NaN keys are distinct objects in Python
			return nil, false
		}
		return x, true
	case pyBytes:
		return bytesKey(x), true
	case *pyGlobal, *pyObject, *pyTensor: // hashed by identity
		return x, true
	case *pyTuple:
		if depth >= maxTupleKeyDepth {
			return nil, false
		}
		var b strings.Builder
		b.WriteByte('(')
		for _, item := range x.items {
			*budget--
			if *budget < 0 {
				return nil, false
			}
			k, ok := hashKeyDepth(item, depth+1, budget)
			if !ok || !appendKey(&b, k) {
				return nil, false
			}
			b.WriteByte(',')
		}
		b.WriteByte(')')
		return tupleKey(b.String()), true
	}
	return nil, false
}

// appendKey encodes a normalized key into a tuple key. Keys hashed by
// identity cannot be encoded as text, so tuples holding them are not
// indexed.
func appendKey(b *strings.Builder, k any) bool {
	switch x := k.(type) {
	case noneKey:
		b.WriteString("N")
	case string:
		b.WriteString("s")
		b.WriteString(strconv.Quote(x))
	case int64:
		b.WriteString("i")
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString("f")
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case bytesKey:
		b.WriteString("b")
		b.WriteString(strconv.Quote(string(x)))
	case tupleKey:
		b.WriteString("t")
		b.WriteString(string(x))
	default:
		return false
	}
	return true
}

// numel returns the number of elements of the tensor, or -1 when its
// shape is unknown or implausible.
func (t *pyTensor) numel() int64 {
	if !t.known {
		return -1
	}
	n := int64(1)
	for _, d := range t.shape {
		if d < 0 || (d > 0 && n > (1<<53)/d) {
			return -1
		}
		n *= d
	}
	return n
}

// lookup returns a named entry of a dict, or an attribute of an object
// (from its BUILD state): train_args is a dict, model.args a dict or an
// IterableSimpleNamespace.
func lookup(v any, key string) (any, bool) {
	switch x := v.(type) {
	case *pyDict:
		return x.get(key)
	case *pyObject:
		if x == nil {
			return nil, false
		}
		if val, ok := x.attrs.get(key); ok {
			return val, true
		}
		return x.dict.get(key)
	}
	return nil, false
}

// lookupDict returns lookup(v, key) when it is a dict.
func lookupDict(v any, key string) *pyDict {
	val, _ := lookup(v, key)
	d, _ := val.(*pyDict)
	return d
}

// asString accepts str values (pathlib paths are already strings).
func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// asInt accepts ints (including decoded numpy integers), but not bools or
// floats.
func asInt(v any) (int64, bool) {
	n, ok := v.(int64)
	return n, ok
}

// asFloat accepts finite floats and ints (not bools).
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// truthy reports whether v is True or a non-zero int.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	}
	return false
}

// sequence returns the items of a list or tuple.
func sequence(v any) ([]any, bool) {
	switch x := v.(type) {
	case *pyList:
		return x.items, true
	case *pyTuple:
		return x.items, true
	}
	return nil, false
}

// className is the class of an object, nil if unknown.
func className(v any) *pyGlobal {
	if o, ok := v.(*pyObject); ok {
		return o.class
	}
	return nil
}

// typeName describes a value for error messages.
func typeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		return "bool"
	case int64, bigInt:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case pyBytes:
		return "bytes"
	case *pyTuple:
		return "tuple"
	case *pyList:
		return "list"
	case *pyDict:
		return "dict"
	case *pySet:
		if x.frozen {
			return "frozenset"
		}
		return "set"
	case *pyGlobal:
		return cleanText(x.qual, maxShortTextLen)
	case *pyObject:
		if x.class != nil {
			return cleanText(x.class.qual, maxShortTextLen)
		}
		return "object"
	case *pyTensor:
		return "torch.Tensor"
	case *pyStorage:
		return "torch.Storage"
	}
	return "unknown"
}
