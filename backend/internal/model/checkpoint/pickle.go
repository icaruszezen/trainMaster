package checkpoint

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The pickle interpreter: it executes the opcodes of protocols 0–5 the way
// Python's pickle.Unpickler does, except that it never imports or calls
// anything. GLOBAL yields a *pyGlobal; REDUCE, NEWOBJ, OBJ and INST rebuild
// the few constructors a checkpoint needs (dicts, sets, tensors, numpy
// scalars, paths, ...) structurally and turn every other call into a
// *pyObject placeholder (see reduce.go).

// Pickle opcodes (Lib/pickle.py).
const (
	opMark           = '('
	opStop           = '.'
	opPop            = '0'
	opPopMark        = '1'
	opDup            = '2'
	opFloat          = 'F'
	opInt            = 'I'
	opBinInt         = 'J'
	opBinInt1        = 'K'
	opLong           = 'L'
	opBinInt2        = 'M'
	opNone           = 'N'
	opPersID         = 'P'
	opBinPersID      = 'Q'
	opReduce         = 'R'
	opString         = 'S'
	opBinString      = 'T'
	opShortBinString = 'U'
	opUnicode        = 'V'
	opBinUnicode     = 'X'
	opAppend         = 'a'
	opBuild          = 'b'
	opGlobal         = 'c'
	opDict           = 'd'
	opEmptyDict      = '}'
	opAppends        = 'e'
	opGet            = 'g'
	opBinGet         = 'h'
	opInst           = 'i'
	opLongBinGet     = 'j'
	opList           = 'l'
	opEmptyList      = ']'
	opObj            = 'o'
	opPut            = 'p'
	opBinPut         = 'q'
	opLongBinPut     = 'r'
	opSetItem        = 's'
	opTuple          = 't'
	opEmptyTuple     = ')'
	opSetItems       = 'u'
	opBinFloat       = 'G'

	// Protocol 2.
	opProto    = 0x80
	opNewObj   = 0x81
	opExt1     = 0x82
	opExt2     = 0x83
	opExt4     = 0x84
	opTuple1   = 0x85
	opTuple2   = 0x86
	opTuple3   = 0x87
	opNewTrue  = 0x88
	opNewFalse = 0x89
	opLong1    = 0x8a
	opLong4    = 0x8b

	// Protocol 3.
	opBinBytes      = 'B'
	opShortBinBytes = 'C'

	// Protocol 4.
	opShortBinUnicode = 0x8c
	opBinUnicode8     = 0x8d
	opBinBytes8       = 0x8e
	opEmptySet        = 0x8f
	opAddItems        = 0x90
	opFrozenSet       = 0x91
	opNewObjEx        = 0x92
	opStackGlobal     = 0x93
	opMemoize         = 0x94
	opFrame           = 0x95

	// Protocol 5.
	opByteArray8     = 0x96
	opNextBuffer     = 0x97
	opReadOnlyBuffer = 0x98
)

// highestProtocol is pickle.HIGHEST_PROTOCOL of Python 3.8 and later.
const highestProtocol = 5

// limits bounds the work of the unpickler so that a crafted data.pkl cannot
// exhaust memory or CPU. Every opcode does a bounded amount of work, so
// the run time is linear in ops (about 50 ns each), and memory is at most
// about 35 bytes per value (see pushNew) and 110 per memo entry: 100 MB
// with the defaults. Real checkpoints stay far below them: the largest
// Ultralytics data.pkl (a YOLOv9e-seg training checkpoint) is 0.7 MB, and
// a data.pkl has 0.4–0.65 opcodes, 0.7–1.05 values and 0.1–0.15 memo
// entries per byte, nesting below 100 levels and a few thousand items on
// the stack.
type limits struct {
	ops    int // opcodes executed
	values int // values created plus items added to containers
	stack  int // items on the stack
	marks  int // open MARKs (nesting depth of containers being built)
	memo   int // memo entries
}

var defaultLimits = limits{ops: 1 << 24, values: 1 << 21, stack: 1 << 18, marks: 1 << 12, memo: 1 << 18}

// pickleError is a malformed pickle (tooLarge: one over the limits).
type pickleError struct {
	pos      int // offset of the opcode in data.pkl
	msg      string
	tooLarge bool
}

func (e *pickleError) Error() string { return fmt.Sprintf("data.pkl 第 %d 字节：%s", e.pos, e.msg) }

type unpickler struct {
	data   []byte
	pos    int // next byte to read
	op     int // offset of the opcode being executed
	stack  []any
	marks  []int // stack lengths at each open MARK
	memo   map[int64]any
	lim    limits
	ops    int
	values int
}

// unpickle runs the pickle in data and returns the value it builds.
// Errors are *pickleError.
func unpickle(data []byte, lim limits) (any, error) {
	u := &unpickler{data: data, lim: lim, memo: make(map[int64]any)}
	return u.run()
}

func (u *unpickler) corrupt(format string, args ...any) error {
	return &pickleError{pos: u.op, msg: fmt.Sprintf(format, args...)}
}

func (u *unpickler) over(what string) error {
	return &pickleError{pos: u.op, msg: what + "超过上限", tooLarge: true}
}

// alloc accounts for n new values or container items.
func (u *unpickler) alloc(n int) error {
	u.values += n
	if u.values > u.lim.values {
		return u.over("对象数量")
	}
	return nil
}

func (u *unpickler) read(n int) ([]byte, error) {
	if n < 0 || n > len(u.data)-u.pos {
		return nil, u.corrupt("数据不完整（需要 %d 字节，只剩 %d 字节）", n, len(u.data)-u.pos)
	}
	b := u.data[u.pos : u.pos+n]
	u.pos += n
	return b, nil
}

// readLen reads an unsigned length of size bytes and checks that the data
// it announces is there, before anything is allocated for it.
func (u *unpickler) readLen(size int) (int, error) {
	b, err := u.read(size)
	if err != nil {
		return 0, err
	}
	var n uint64
	switch size {
	case 1:
		n = uint64(b[0])
	case 4:
		n = uint64(binary.LittleEndian.Uint32(b))
	default:
		n = binary.LittleEndian.Uint64(b)
	}
	if n > uint64(len(u.data)-u.pos) {
		return 0, u.corrupt("数据不完整（声明的长度为 %d 字节，只剩 %d 字节）", n, len(u.data)-u.pos)
	}
	return int(n), nil
}

// readSignedLen reads the signed 4-byte length of BINSTRING and LONG4.
func (u *unpickler) readSignedLen() (int, error) {
	b, err := u.read(4)
	if err != nil {
		return 0, err
	}
	n := int32(binary.LittleEndian.Uint32(b))
	if n < 0 {
		return 0, u.corrupt("长度为负数（%d）", n)
	}
	if int(n) > len(u.data)-u.pos {
		return 0, u.corrupt("数据不完整（声明的长度为 %d 字节，只剩 %d 字节）", n, len(u.data)-u.pos)
	}
	return int(n), nil
}

// readLine reads up to the next newline (protocol 0 arguments).
func (u *unpickler) readLine() (string, error) {
	i := bytes.IndexByte(u.data[u.pos:], '\n')
	if i < 0 {
		return "", u.corrupt("数据不完整（缺少换行符）")
	}
	line := string(u.data[u.pos : u.pos+i])
	u.pos += i + 1
	return line, nil
}

// base is the start of the current stack frame: Python keeps the items
// after the last MARK in a separate list, so opcodes cannot reach below it.
func (u *unpickler) base() int {
	if n := len(u.marks); n > 0 {
		return u.marks[n-1]
	}
	return 0
}

func (u *unpickler) push(v any) error {
	if len(u.stack) >= u.lim.stack {
		return u.over("栈深度")
	}
	u.stack = append(u.stack, v)
	return nil
}

// pushNew pushes a value the current opcode created. Containers, objects
// and tensors count as 4 values: they take about four times the memory
// of a scalar or of a container item.
func (u *unpickler) pushNew(v any) error {
	n := 1
	switch v.(type) {
	case *pyDict, *pyList, *pyTuple, *pySet, *pyObject, *pyTensor:
		n = 4
	}
	if err := u.alloc(n); err != nil {
		return err
	}
	return u.push(v)
}

func (u *unpickler) pop() (any, error) {
	n := len(u.stack)
	if n <= u.base() {
		return nil, u.corrupt("栈为空")
	}
	v := u.stack[n-1]
	u.stack[n-1] = nil
	u.stack = u.stack[:n-1]
	return v, nil
}

func (u *unpickler) top() (any, error) {
	n := len(u.stack)
	if n <= u.base() {
		return nil, u.corrupt("栈为空")
	}
	return u.stack[n-1], nil
}

// popMark removes the items after the last MARK, and the MARK.
func (u *unpickler) popMark() ([]any, error) {
	n := len(u.marks)
	if n == 0 {
		return nil, u.corrupt("缺少 MARK")
	}
	m := u.marks[n-1]
	u.marks = u.marks[:n-1]
	items := make([]any, len(u.stack)-m)
	copy(items, u.stack[m:])
	clear(u.stack[m:])
	u.stack = u.stack[:m]
	return items, nil
}

// popTuple pops the arguments of REDUCE, NEWOBJ and NEWOBJ_EX, which must
// be a tuple (Python calls f(*args)).
func (u *unpickler) popTuple(what string) ([]any, error) {
	v, err := u.pop()
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case *pyTuple:
		return x.items, nil
	case *pyList: // any iterable works in Python; lists are the only other one seen in the wild
		return x.items, nil
	}
	return nil, u.corrupt("%s 的参数不是元组（%s）", what, typeName(v))
}

func (u *unpickler) memoPut(i int64, v any) error {
	if i < 0 {
		return u.corrupt("memo 编号为负数（%d）", i)
	}
	if _, ok := u.memo[i]; !ok && len(u.memo) >= u.lim.memo {
		return u.over("memo 项数")
	}
	u.memo[i] = v
	return nil
}

func (u *unpickler) memoGet(i int64) error {
	v, ok := u.memo[i]
	if !ok {
		return u.corrupt("引用了不存在的 memo 项 %d", i)
	}
	return u.push(v)
}

func (u *unpickler) run() (any, error) {
	for {
		if u.pos >= len(u.data) {
			return nil, u.corrupt("数据不完整（缺少 STOP）")
		}
		u.op = u.pos
		op := u.data[u.pos]
		u.pos++
		u.ops++
		if u.ops > u.lim.ops {
			return nil, u.over("操作数")
		}
		if op == opStop {
			return u.pop()
		}
		if err := u.exec(op); err != nil {
			return nil, err
		}
	}
}

// exec runs one opcode other than STOP.
func (u *unpickler) exec(op byte) error {
	switch op {
	case opProto:
		b, err := u.read(1)
		if err != nil {
			return err
		}
		if b[0] > highestProtocol {
			return u.corrupt("不支持的 pickle 协议 %d", b[0])
		}
		return nil
	case opFrame: // frames only group opcodes for buffered reads
		_, err := u.read(8)
		return err
	case opMark:
		if len(u.marks) >= u.lim.marks {
			return u.over("嵌套层数")
		}
		u.marks = append(u.marks, len(u.stack))
		return nil
	case opPop:
		if len(u.stack) > u.base() {
			_, err := u.pop()
			return err
		}
		_, err := u.popMark() // POP on an empty frame discards the MARK, as in Python
		return err
	case opPopMark:
		_, err := u.popMark()
		return err
	case opDup:
		v, err := u.top()
		if err != nil {
			return err
		}
		return u.push(v)

	// Scalars.
	case opNone:
		return u.pushNew(nil)
	case opNewTrue:
		return u.pushNew(true)
	case opNewFalse:
		return u.pushNew(false)
	case opInt:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		switch line {
		case "00":
			return u.pushNew(false)
		case "01":
			return u.pushNew(true)
		}
		v, err := u.parseInt(line)
		if err != nil {
			return err
		}
		return u.pushNew(v)
	case opLong:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		v, err := u.parseInt(strings.TrimSuffix(line, "L"))
		if err != nil {
			return err
		}
		return u.pushNew(v)
	case opBinInt:
		b, err := u.read(4)
		if err != nil {
			return err
		}
		return u.pushNew(int64(int32(binary.LittleEndian.Uint32(b))))
	case opBinInt1:
		b, err := u.read(1)
		if err != nil {
			return err
		}
		return u.pushNew(int64(b[0]))
	case opBinInt2:
		b, err := u.read(2)
		if err != nil {
			return err
		}
		return u.pushNew(int64(binary.LittleEndian.Uint16(b)))
	case opLong1, opLong4:
		var n int
		var err error
		if op == opLong1 {
			n, err = u.readLen(1)
		} else {
			n, err = u.readSignedLen()
		}
		if err != nil {
			return err
		}
		b, err := u.read(n)
		if err != nil {
			return err
		}
		return u.pushNew(decodeLong(b))
	case opFloat:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return u.corrupt("FLOAT 的参数不是数字（%.40q）", line)
		}
		return u.pushNew(f)
	case opBinFloat:
		b, err := u.read(8)
		if err != nil {
			return err
		}
		return u.pushNew(math.Float64frombits(binary.BigEndian.Uint64(b)))

	// Strings and bytes.
	case opString:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		if len(line) < 2 || line[0] != line[len(line)-1] || (line[0] != '\'' && line[0] != '"') {
			return u.corrupt("STRING 的参数缺少引号")
		}
		return u.pushNew(unescapeString(line[1 : len(line)-1]))
	case opBinString, opShortBinString, opBinUnicode, opShortBinUnicode, opBinUnicode8:
		var n int
		var err error
		switch op {
		case opBinString:
			n, err = u.readSignedLen()
		case opShortBinString, opShortBinUnicode:
			n, err = u.readLen(1)
		case opBinUnicode:
			n, err = u.readLen(4)
		default:
			n, err = u.readLen(8)
		}
		if err != nil {
			return err
		}
		b, err := u.read(n)
		if err != nil {
			return err
		}
		return u.pushNew(string(b))
	case opUnicode:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		return u.pushNew(decodeRawUnicodeEscape(line))
	case opBinBytes, opShortBinBytes, opBinBytes8, opByteArray8:
		size := 4
		switch op {
		case opShortBinBytes:
			size = 1
		case opBinBytes8, opByteArray8:
			size = 8
		}
		n, err := u.readLen(size)
		if err != nil {
			return err
		}
		b, err := u.read(n)
		if err != nil {
			return err
		}
		return u.pushNew(pyBytes(b))
	case opNextBuffer:
		return u.corrupt("引用了 pickle 之外的缓冲区（NEXT_BUFFER）")
	case opReadOnlyBuffer:
		_, err := u.top()
		return err

	// Containers.
	case opEmptyTuple:
		return u.pushNew(&pyTuple{})
	case opTuple:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		if err := u.alloc(len(items)); err != nil {
			return err
		}
		return u.pushNew(&pyTuple{items: items})
	case opTuple1, opTuple2, opTuple3:
		n := int(op-opTuple1) + 1
		if len(u.stack)-u.base() < n {
			return u.corrupt("栈中的元素不足 %d 个", n)
		}
		items := make([]any, n)
		copy(items, u.stack[len(u.stack)-n:])
		clear(u.stack[len(u.stack)-n:])
		u.stack = u.stack[:len(u.stack)-n]
		if err := u.alloc(n); err != nil {
			return err
		}
		return u.pushNew(&pyTuple{items: items})
	case opEmptyList:
		return u.pushNew(&pyList{})
	case opList:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		if err := u.alloc(len(items)); err != nil {
			return err
		}
		return u.pushNew(&pyList{items: items})
	case opAppend:
		v, err := u.pop()
		if err != nil {
			return err
		}
		target, err := u.top()
		if err != nil {
			return err
		}
		return u.appendItems(target, []any{v})
	case opAppends:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		target, err := u.top()
		if err != nil {
			return err
		}
		return u.appendItems(target, items)
	case opEmptyDict:
		return u.pushNew(&pyDict{})
	case opDict:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		d := &pyDict{}
		if err := u.setItems(d, items); err != nil {
			return err
		}
		return u.pushNew(d)
	case opSetItem:
		v, err := u.pop()
		if err != nil {
			return err
		}
		k, err := u.pop()
		if err != nil {
			return err
		}
		target, err := u.top()
		if err != nil {
			return err
		}
		return u.setItems(target, []any{k, v})
	case opSetItems:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		target, err := u.top()
		if err != nil {
			return err
		}
		return u.setItems(target, items)
	case opEmptySet:
		return u.pushNew(&pySet{})
	case opAddItems:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		target, err := u.top()
		if err != nil {
			return err
		}
		return u.addItems(target, items)
	case opFrozenSet:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		if err := u.alloc(len(items)); err != nil {
			return err
		}
		return u.pushNew(&pySet{items: items, frozen: true})

	// Classes, calls and objects.
	case opGlobal:
		module, err := u.readLine()
		if err != nil {
			return err
		}
		name, err := u.readLine()
		if err != nil {
			return err
		}
		return u.pushNew(newGlobal(module, name))
	case opStackGlobal:
		name, err := u.pop()
		if err != nil {
			return err
		}
		module, err := u.pop()
		if err != nil {
			return err
		}
		m, ok1 := module.(string)
		n, ok2 := name.(string)
		if !ok1 || !ok2 {
			return u.corrupt("STACK_GLOBAL 的参数不是字符串")
		}
		return u.pushNew(newGlobal(m, n))
	case opExt1, opExt2, opExt4:
		return u.corrupt("使用了扩展注册表（EXT 操作码），无法解析")
	case opReduce:
		args, err := u.popTuple("REDUCE")
		if err != nil {
			return err
		}
		fn, err := u.pop()
		if err != nil {
			return err
		}
		v, err := u.call(fn, args)
		if err != nil {
			return err
		}
		return u.pushNew(v)
	case opNewObj, opNewObjEx:
		if op == opNewObjEx {
			if _, err := u.pop(); err != nil { // keyword arguments: not needed
				return err
			}
		}
		args, err := u.popTuple("NEWOBJ")
		if err != nil {
			return err
		}
		cls, err := u.pop()
		if err != nil {
			return err
		}
		return u.pushNew(newObject(cls, args))
	case opObj:
		items, err := u.popMark()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return u.corrupt("OBJ 缺少类")
		}
		v, err := u.call(items[0], items[1:])
		if err != nil {
			return err
		}
		return u.pushNew(v)
	case opInst:
		module, err := u.readLine()
		if err != nil {
			return err
		}
		name, err := u.readLine()
		if err != nil {
			return err
		}
		items, err := u.popMark()
		if err != nil {
			return err
		}
		v, err := u.call(newGlobal(module, name), items)
		if err != nil {
			return err
		}
		return u.pushNew(v)
	case opBuild:
		state, err := u.pop()
		if err != nil {
			return err
		}
		inst, err := u.top()
		if err != nil {
			return err
		}
		return u.build(inst, state)
	case opPersID:
		if _, err := u.readLine(); err != nil {
			return err
		}
		return u.pushNew(&pyStorage{})
	case opBinPersID:
		if _, err := u.pop(); err != nil {
			return err
		}
		return u.pushNew(&pyStorage{})

	// Memo.
	case opPut:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		i, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil {
			return u.corrupt("PUT 的参数不是整数")
		}
		v, err := u.top()
		if err != nil {
			return err
		}
		return u.memoPut(i, v)
	case opBinPut, opLongBinPut:
		size := 1
		if op == opLongBinPut {
			size = 4
		}
		b, err := u.read(size)
		if err != nil {
			return err
		}
		i := int64(b[0])
		if size == 4 {
			i = int64(binary.LittleEndian.Uint32(b))
		}
		v, err := u.top()
		if err != nil {
			return err
		}
		return u.memoPut(i, v)
	case opMemoize:
		v, err := u.top()
		if err != nil {
			return err
		}
		return u.memoPut(int64(len(u.memo)), v)
	case opGet:
		line, err := u.readLine()
		if err != nil {
			return err
		}
		i, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil {
			return u.corrupt("GET 的参数不是整数")
		}
		return u.memoGet(i)
	case opBinGet:
		b, err := u.read(1)
		if err != nil {
			return err
		}
		return u.memoGet(int64(b[0]))
	case opLongBinGet:
		b, err := u.read(4)
		if err != nil {
			return err
		}
		return u.memoGet(int64(binary.LittleEndian.Uint32(b)))
	}
	return u.corrupt("未知的操作码 0x%02x", op)
}

// parseInt parses the decimal argument of INT and LONG (Python accepts any
// int literal there). Values beyond int64 become bigInt.
func (u *unpickler) parseInt(s string) (any, error) {
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 0, 64)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return bigInt{}, nil
	}
	return nil, u.corrupt("整数格式不正确（%.40q）", s)
}

// decodeLong decodes the little-endian two's complement integer of LONG1
// and LONG4.
func decodeLong(b []byte) any {
	if len(b) > 8 {
		return bigInt{}
	}
	if len(b) == 0 {
		return int64(0)
	}
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	shift := uint(64 - 8*len(b))
	return int64(v<<shift) >> shift
}

// unescapeString decodes the backslash escapes of a protocol 0 STRING
// argument (Python's codecs.escape_decode).
func unescapeString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; e {
		case '\\', '\'', '"':
			b.WriteByte(e)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '\n': // line continuation
		case 'x':
			if i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			if e >= '0' && e <= '7' {
				v, n := 0, 0
				for n < 3 && i+n < len(s) && s[i+n] >= '0' && s[i+n] <= '7' {
					v = v*8 + int(s[i+n]-'0')
					n++
				}
				b.WriteByte(byte(v))
				i += n - 1
				continue
			}
			b.WriteByte('\\')
			b.WriteByte(e)
		}
	}
	return b.String()
}

// decodeRawUnicodeEscape decodes a protocol 0 UNICODE argument: Latin-1
// text with \uXXXX and \UXXXXXXXX escapes.
func decodeRawUnicodeEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == 'u' || s[i+1] == 'U') {
			n := 4
			if s[i+1] == 'U' {
				n = 8
			}
			if i+2+n <= len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+2+n], 16, 32); err == nil {
					r := rune(v)
					if !utf8.ValidRune(r) {
						r = utf8.RuneError
					}
					b.WriteRune(r)
					i += 1 + n
					continue
				}
			}
		}
		b.WriteRune(rune(c))
	}
	return b.String()
}
