package checkpoint

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Reading data.pkl from the torch zip format (torch.save since PyTorch
// 1.6): a zip whose entries all sit under one folder named after the file
// ("best/", "archive/", "yolov8n/" ...), with the pickle in
// "<folder>/data.pkl" and the tensor data in "<folder>/data/<key>".
// Only data.pkl is read.

const (
	// maxPickleSize bounds data.pkl (Ultralytics models: 0.1–2 MB).
	maxPickleSize = 64 << 20
	// maxDirectoryRead bounds what zip.NewReader may read: the end
	// records and the central directory, which it keeps in memory (about
	// 100 bytes per entry; a torch checkpoint has one entry per tensor).
	maxDirectoryRead = 8 << 20
	// maxZipEntries bounds the entry count the end record may declare:
	// zip.NewReader allocates a slice of that size before reading any
	// entry (when the count is below a 30th of the file size).
	maxZipEntries = 1 << 18
)

// legacyMagic starts a file written by torch.save before PyTorch 1.6 (or
// with _use_new_zipfile_serialization=False): a raw pickle stream whose
// first pickle is the magic number 0x1950a86a20f9469cfc6c, written with
// LONG1 by protocols 2–5 (after PROTO, and a FRAME from protocol 4 on) and
// with LONG by protocols 0 and 1.
var (
	legacyMagic      = []byte{opLong1, 10, 0x6c, 0xfc, 0x9c, 0x46, 0xf9, 0x20, 0x6a, 0xa8, 0x50, 0x19, opStop}
	legacyMagicText  = []byte("L119547037146038801333356L\n.")
	magicZip         = []byte("PK\x03\x04")
	errDirectorySize = errors.New("zip central directory too large")
)

// isLegacy reports whether head starts like a legacy torch file.
func isLegacy(head []byte) bool {
	if bytes.HasPrefix(head, legacyMagicText) {
		return true
	}
	if len(head) < 2 || head[0] != opProto || head[1] < 2 || head[1] > highestProtocol {
		return false
	}
	rest := head[2:]
	if len(rest) >= 9 && rest[0] == opFrame {
		rest = rest[9:]
	}
	return bytes.HasPrefix(rest, legacyMagic)
}

// countingReaderAt passes reads through, remembering the first I/O error
// (to tell a failing disk from a malformed file) and failing reads once a
// budget of bytes is used up. Reads outside the file, which offsets from
// a malformed file can ask for, return io.EOF without reaching r.
type countingReaderAt struct {
	r      io.ReaderAt
	size   int64
	budget int64 // < 0: unlimited
	ioErr  error
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= c.size {
		return 0, io.EOF
	}
	if c.budget >= 0 {
		if int64(len(p)) > c.budget {
			return 0, errDirectorySize
		}
		c.budget -= int64(len(p))
	}
	n, err := c.r.ReadAt(p, off)
	if err != nil && err != io.EOF && c.ioErr == nil {
		c.ioErr = err
	}
	return n, err
}

// readPickle returns the contents of data.pkl.
func readPickle(r io.ReaderAt, size int64) ([]byte, error) {
	if size <= 0 {
		return nil, newError(ReasonNotTorch, "文件为空，不是 PyTorch 模型文件")
	}
	cr := &countingReaderAt{r: r, size: size, budget: -1}
	head := make([]byte, 32)
	n, err := cr.ReadAt(head, 0)
	if n < len(head) && err != nil && err != io.EOF {
		return nil, fmt.Errorf("read model file: %w", err)
	}
	head = head[:n]
	if isLegacy(head) {
		return nil, newError(ReasonLegacyFormat, "模型文件是 PyTorch 1.6 之前的旧格式，平台无法读取。"+
			"请在 Python 中用新版 PyTorch 加载后重新保存再上传："+
			`torch.save(torch.load("model.pt", weights_only=False), "model-new.pt")`)
	}
	if !bytes.HasPrefix(head, magicZip) {
		return nil, newError(ReasonNotTorch, notTorchMessage(head))
	}

	if n, ok := zipEntryCount(cr, size); ok && n > maxZipEntries {
		return nil, newError(ReasonTooLarge, "模型文件（zip）中的条目过多，不像是 Ultralytics 保存的模型，平台拒绝解析")
	}
	if cr.ioErr != nil {
		return nil, fmt.Errorf("read model file: %w", cr.ioErr)
	}
	cr.budget = maxDirectoryRead
	zr, err := zip.NewReader(cr, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) { // entry names are never used as paths
		switch {
		case cr.ioErr != nil:
			return nil, fmt.Errorf("read model file: %w", cr.ioErr)
		case errors.Is(err, errDirectorySize):
			return nil, newError(ReasonTooLarge, "模型文件（zip）中的条目过多，不像是 Ultralytics 保存的模型，平台拒绝解析")
		}
		return nil, corruptError("zip 结构无法读取：" + err.Error())
	}
	cr.budget = -1

	f := findPickle(zr.File)
	if f == nil {
		return nil, newError(ReasonNotTorch, "文件是 zip 压缩包，但其中没有 data.pkl，不是用 torch.save 保存的 PyTorch 模型文件")
	}
	if f.UncompressedSize64 > maxPickleSize {
		return nil, pickleTooLarge()
	}
	rc, err := f.Open()
	if err != nil {
		if cr.ioErr != nil {
			return nil, fmt.Errorf("read model file: %w", cr.ioErr)
		}
		return nil, corruptError("无法打开 data.pkl：" + err.Error())
	}
	defer rc.Close()
	buf := bytes.NewBuffer(make([]byte, 0, int(f.UncompressedSize64)+bytes.MinRead))
	_, err = buf.ReadFrom(io.LimitReader(rc, maxPickleSize+1))
	data := buf.Bytes()
	switch {
	case cr.ioErr != nil:
		return nil, fmt.Errorf("read model file: %w", cr.ioErr)
	case len(data) > maxPickleSize: // the header understated the size
		return nil, pickleTooLarge()
	case err != nil:
		return nil, corruptError("无法读取 data.pkl：" + err.Error())
	}
	return data, nil
}

// zipEntryCount returns the number of entries the end of central
// directory record (or the zip64 one) declares, found the way
// archive/zip finds them; false when there is none, which zip.NewReader
// reports itself.
func zipEntryCount(r io.ReaderAt, size int64) (uint64, bool) {
	const loc64Len, end64Len = 20, 56
	var buf []byte
	endOffset := int64(-1)
	for _, n := range []int64{1024, 65 * 1024} {
		n = min(n, size)
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return 0, false
		}
		if p := findDirectoryEnd(buf); p >= 0 {
			buf, endOffset = buf[p:], size-n+int64(p)
			break
		}
		if n == size {
			break
		}
	}
	if endOffset < 0 {
		return 0, false
	}
	count := uint64(binary.LittleEndian.Uint16(buf[10:]))
	dirSize := binary.LittleEndian.Uint32(buf[12:])
	dirOffset := binary.LittleEndian.Uint32(buf[16:])
	if count != 0xffff && dirSize != 0xffff && dirOffset != 0xffffffff { // the zip64 conditions of archive/zip
		return count, true
	}
	if endOffset < loc64Len {
		return count, true
	}
	loc := make([]byte, loc64Len)
	if _, err := r.ReadAt(loc, endOffset-loc64Len); err != nil {
		return 0, false
	}
	if !bytes.Equal(loc[:4], []byte("PK\x06\x07")) || binary.LittleEndian.Uint32(loc[4:]) != 0 ||
		binary.LittleEndian.Uint32(loc[16:]) != 1 {
		return count, true // not zip64
	}
	end64 := make([]byte, end64Len)
	if _, err := r.ReadAt(end64, int64(binary.LittleEndian.Uint64(loc[8:]))); err != nil ||
		!bytes.Equal(end64[:4], []byte("PK\x06\x06")) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(end64[32:]), true
}

// findDirectoryEnd is archive/zip's search for the end of central
// directory record in the last bytes of the file.
func findDirectoryEnd(b []byte) int {
	const endLen = 22
	for i := len(b) - endLen; i >= 0; i-- {
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			if n := int(b[i+endLen-2]) | int(b[i+endLen-1])<<8; n+endLen+i > len(b) {
				return -1 // truncated comment
			}
			return i
		}
	}
	return -1
}

// findPickle finds "<root>/data.pkl", where root is the folder of the
// first entry (as torch does), or else any data.pkl one level deep.
func findPickle(files []*zip.File) *zip.File {
	if len(files) == 0 {
		return nil
	}
	if root, _, ok := strings.Cut(files[0].Name, "/"); ok {
		for _, f := range files {
			if f.Name == root+"/data.pkl" {
				return f
			}
		}
	}
	for _, f := range files {
		if dir, rest, ok := strings.Cut(f.Name, "/"); ok && dir != "" && rest == "data.pkl" {
			return f
		}
	}
	return nil
}

// notTorchMessage explains a file that is neither a zip nor a legacy
// torch file, naming the format when it is a common mix-up.
func notTorchMessage(head []byte) string {
	const hint = "请上传 Ultralytics 训练得到的 .pt 文件（如 runs/detect/train/weights/best.pt）"
	switch {
	case len(head) >= 2 && head[0] == opProto && head[1] <= highestProtocol:
		return "文件是普通的 pickle 文件，不是用 torch.save 保存的 PyTorch 模型。" + hint
	case len(head) >= 9 && head[8] == '{' && bytes.Contains(head, []byte(`"`)): // 8-byte header length + JSON
		return "文件是 safetensors 格式，不是 PyTorch 的 .pt 文件。" + hint
	case len(head) >= 2 && head[0] == 0x08 && head[1] < 0x20: // protobuf field 1 (ir_version)
		return "文件可能是 ONNX 模型，不是 PyTorch 的 .pt 文件。" + hint
	}
	return "文件不是 PyTorch 模型文件（.pt）。" + hint
}
