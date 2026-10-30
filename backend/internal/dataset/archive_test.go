package dataset

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// testEntry is a member of an archive built by a test.
type testEntry struct {
	Name    string
	Data    string
	Type    entryType // entryFile, entryDir or entryLink
	NonUTF8 bool      // zip: store the raw name without the UTF-8 flag
}

func writeZip(t *testing.T, entries []testEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, NonUTF8: e.NonUTF8, Modified: time.Unix(1700000000, 0)}
		switch e.Type {
		case entryDir:
			h.SetMode(fs.ModeDir | 0o755)
		case entryLink:
			h.SetMode(fs.ModeSymlink | 0o777)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if e.Type != entryDir {
			_, _ = io.WriteString(w, e.Data)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTar(t *testing.T, w io.Writer, entries []testEntry) {
	t.Helper()
	tw := tar.NewWriter(w)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Mode: 0o644, Size: int64(len(e.Data)), ModTime: time.Unix(1700000000, 0)}
		switch e.Type {
		case entryDir:
			h.Typeflag, h.Size, h.Mode = tar.TypeDir, 0, 0o755
		case entryLink:
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, e.Data
		default:
			h.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = io.WriteString(tw, e.Data)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

// buildArchive returns the bytes of an archive of the given format.
func buildArchive(t *testing.T, format archiveFormat, entries []testEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case formatZip:
		return writeZip(t, entries)
	case formatTar:
		writeTar(t, &buf, entries)
	case formatTarGz:
		gw := gzip.NewWriter(&buf)
		writeTar(t, gw, entries)
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
	case formatTarXz:
		xw, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		writeTar(t, xw, entries)
		if err := xw.Close(); err != nil {
			t.Fatal(err)
		}
	case formatTarZst:
		zw, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		writeTar(t, zw, entries)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("cannot build %s", format)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// walked is what walkArchive reported for one entry.
type walked struct {
	Type entryType
	Data string
}

func walkFile(t *testing.T, p string) (map[string]walked, error) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	got := map[string]walked{}
	var lastDone, lastTotal int64
	err = walkArchive(context.Background(), f, fi.Size(), func(e *archiveEntry) error {
		w := walked{Type: e.Type}
		if e.Type == entryFile {
			b, err := io.ReadAll(e.Data)
			if err != nil {
				return err
			}
			w.Data = string(b)
		}
		got[e.Name] = w
		return nil
	}, func(unit string, done, total int64) {
		if unit != unitBytes && unit != unitEntries {
			t.Errorf("unexpected progress unit %q", unit)
		}
		if done < lastDone || done > total {
			t.Errorf("progress went from %d/%d to %d/%d", lastDone, lastTotal, done, total)
		}
		lastDone, lastTotal = done, total
	})
	return got, err
}

func TestWalkArchiveFormats(t *testing.T) {
	entries := []testEntry{
		{Name: "set/", Type: entryDir},
		{Name: "set/a.jpg", Data: "jpeg bytes"},
		{Name: "set/sub/b.xml", Data: "<annotation/>"},
		{Name: "set/link.jpg", Data: "a.jpg", Type: entryLink},
		{Name: "../evil.jpg", Data: "kept as is; the caller validates names"},
	}
	for _, format := range []archiveFormat{formatZip, formatTar, formatTarGz, formatTarXz, formatTarZst} {
		t.Run(string(format), func(t *testing.T) {
			data := buildArchive(t, format, entries)
			if got, err := detectFormat(data[:min(len(data), 512)]); err != nil || got != format {
				t.Fatalf("detectFormat = %q, %v", got, err)
			}
			got, err := walkFile(t, writeTemp(t, data))
			if err != nil {
				t.Fatalf("walkArchive: %v", err)
			}
			want := map[string]walked{
				"set/":          {Type: entryDir},
				"set/a.jpg":     {Type: entryFile, Data: "jpeg bytes"},
				"set/sub/b.xml": {Type: entryFile, Data: "<annotation/>"},
				"set/link.jpg":  {Type: entryLink},
				"../evil.jpg":   {Type: entryFile, Data: "kept as is; the caller validates names"},
			}
			if len(got) != len(want) {
				t.Errorf("entries = %v", got)
			}
			for name, w := range want {
				if got[name] != w {
					t.Errorf("%s = %+v, want %+v", name, got[name], w)
				}
			}
		})
	}
}

func TestWalkArchiveFixtures(t *testing.T) {
	png, err := os.ReadFile(filepath.Join("testdata", "sample.7z"))
	if err != nil || len(png) == 0 {
		t.Fatalf("fixture missing: %v", err)
	}
	tests := map[string]map[string]entryType{
		"sample.7z":             {"set/sub/a.png": entryFile, "set/sub/a.xml": entryFile, "set/readme.txt": entryFile},
		"sample.tar.bz2":        {"set/a.png": entryFile, "set/a.xml": entryFile},
		"rar3-subdirs.rar":      {"sub/dir1/file1.txt": entryFile, "sub/dir2/file2.txt": entryFile, "sub/with space/long fn.txt": entryFile, "sub/üȵĩöḋè/file.txt": entryFile, "sub/empty": entryDir},
		"rar5-subdirs.rar":      {"sub/dir1/file1.txt": entryFile, "sub/dir2/file2.txt": entryFile, "sub/with space/long fn.txt": entryFile, "sub/üȵĩöḋè/file.txt": entryFile, "sub/empty": entryDir},
		"rar5-solid.rar":        {"stest1.txt": entryFile, "stest2.txt": entryFile},
		"rar5-symlink-unix.rar": {"data.txt": entryFile, "data_link": entryLink, "random_link": entryLink},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := walkFile(t, filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("walkArchive: %v", err)
			}
			for entry, typ := range want {
				if g, ok := got[entry]; !ok || g.Type != typ {
					t.Errorf("%s: got %+v (present %v), want type %d", entry, g, ok, typ)
				}
			}
			for entry, g := range got {
				if g.Type == entryFile && strings.HasSuffix(entry, ".xml") && !strings.Contains(g.Data, "<annotation>") {
					t.Errorf("%s: unexpected content %q", entry, g.Data)
				}
			}
		})
	}
	// File contents survive decompression.
	got, _ := walkFile(t, filepath.Join("testdata", "rar3-subdirs.rar"))
	if d := got["sub/dir1/file1.txt"].Data; d != "file1\n" {
		t.Errorf("rar3 file1.txt = %q", d)
	}
	got, _ = walkFile(t, filepath.Join("testdata", "rar5-solid.rar"))
	if d := got["stest2.txt"].Data; len(d) != 2048 {
		t.Errorf("rar5 solid stest2.txt has %d bytes", len(d))
	}
	got, _ = walkFile(t, filepath.Join("testdata", "sample.7z"))
	if d := got["set/sub/a.png"].Data; !strings.HasPrefix(d, "\x89PNG") {
		t.Errorf("7z a.png = %q", d[:min(len(d), 8)])
	}
}

func TestWalkArchiveRejects(t *testing.T) {
	sample7z, _ := os.ReadFile(filepath.Join("testdata", "sample.7z"))
	zipData := writeZip(t, []testEntry{{Name: "a.jpg", Data: strings.Repeat("x", 1000)}})

	encryptedZip := bytes.Clone(zipData)
	// Set the "encrypted" flag (bit 0) in the central directory entry.
	cd := bytes.LastIndex(encryptedZip, []byte("PK\x01\x02"))
	encryptedZip[cd+8] |= 0x01

	splitZip := bytes.Clone(zipData)
	eocd := bytes.LastIndex(splitZip, magicZipEmpty)
	splitZip[eocd+4] = 2 // number of this disk
	splitZip[eocd+6] = 2 // disk with the central directory

	var gzNotTar bytes.Buffer
	gw := gzip.NewWriter(&gzNotTar)
	_, _ = gw.Write([]byte(strings.Repeat("not a tar archive ", 100)))
	_ = gw.Close()

	tests := map[string]struct {
		data    []byte
		fixture string
		want    error
	}{
		"unknown format":        {data: []byte("\x89PNG\r\n\x1a\n not an archive"), want: errArchiveFormat},
		"empty file":            {data: []byte{}, want: errArchiveFormat},
		"gzip without tar":      {data: gzNotTar.Bytes(), want: errArchiveFormat},
		"encrypted zip":         {data: encryptedZip, want: errArchiveEncrypted},
		"split zip (first)":     {data: append([]byte("PK\x07\x08"), zipData...), want: errArchiveVolumes},
		"split zip (last)":      {data: splitZip, want: errArchiveVolumes},
		"7z first volume":       {data: sample7z[:200], want: errArchiveVolumes},
		"encrypted 7z":          {fixture: "encrypted.7z", want: errArchiveEncrypted},
		"7z encrypted header":   {fixture: "encrypted-header.7z", want: errArchiveEncrypted},
		"encrypted rar5":        {fixture: "rar5-psw.rar", want: errArchiveEncrypted},
		"rar5 encrypted header": {fixture: "rar5-hpsw.rar", want: errArchiveEncrypted},
		"encrypted rar4":        {fixture: "rar3-comment-psw.rar", want: errArchiveEncrypted},
		"rar4 volume":           {fixture: "rar3-vols.part1-head.rar", want: errArchiveVolumes},
		"rar5 volume":           {fixture: "rar5-vols.part1-head.rar", want: errArchiveVolumes},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join("testdata", tt.fixture)
			if tt.fixture == "" {
				p = writeTemp(t, tt.data)
			}
			_, err := walkFile(t, p)
			if err != tt.want {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}

	// A damaged archive is reported as such, with the reason.
	_, err := walkFile(t, writeTemp(t, zipData[:len(zipData)-30]))
	var ue *Error
	if !errors.As(err, &ue) || !errors.Is(err, ErrInvalid) || !strings.HasPrefix(ue.Message, "压缩包已损坏或不完整") {
		t.Errorf("truncated zip: err = %v", err)
	}
	corrupt := bytes.Clone(zipData)
	dataStart := 30 + int(corrupt[26]) + int(corrupt[28]) // local header, name, extra field
	corrupt[dataStart+2] ^= 0xff                          // inside the deflate stream of a.jpg
	_, err = walkFile(t, writeTemp(t, corrupt))
	if !errors.As(err, &ue) || !strings.HasPrefix(ue.Message, "压缩包已损坏或不完整") {
		t.Errorf("corrupt zip data: err = %v", err)
	}
}

func TestWalkArchiveCallbackErrorsPassThrough(t *testing.T) {
	sentinel := errors.New("disk full")
	p := writeTemp(t, writeZip(t, []testEntry{{Name: "a.jpg", Data: "x"}}))
	f, _ := os.Open(p)
	defer f.Close()
	fi, _ := f.Stat()
	err := walkArchive(context.Background(), f, fi.Size(), func(*archiveEntry) error { return sentinel }, func(string, int64, int64) {})
	if err != sentinel {
		t.Errorf("err = %v, want the callback's error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = walkArchive(ctx, f, fi.Size(), func(*archiveEntry) error { return nil }, func(string, int64, int64) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
}

func TestZipLegacyChineseNames(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String("划痕/图片 1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, writeZip(t, []testEntry{
		{Name: gbk, Data: "x", NonUTF8: true},
		{Name: "正常/utf8.jpg", Data: "y"},
	}))
	got, err := walkFile(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got["划痕/图片 1.jpg"].Data != "x" || got["正常/utf8.jpg"].Data != "y" {
		t.Errorf("entries = %v", got)
	}

	// Some tools set the UTF-8 flag (bit 11) on names that are GBK.
	data := writeZip(t, []testEntry{{Name: gbk, Data: "x", NonUTF8: true}})
	data[6] |= 0x08                                             // local file header flags
	data[bytes.LastIndex(data, []byte("PK\x01\x02"))+9] |= 0x08 // central directory flags
	if got, err := walkFile(t, writeTemp(t, data)); err != nil || got["划痕/图片 1.jpg"].Data != "x" {
		t.Errorf("flagged GBK name: entries = %v, %v", got, err)
	}
}

// setXzDict raises the LZMA2 dictionary size declared by the first block
// of the xz stream at data[off:] to the 4 GiB maximum, like a crafted
// archive would, and fixes the block header checksum.
func setXzDict(t *testing.T, data []byte, off int) {
	t.Helper()
	b := data[off+12:] // the block header follows the 12-byte stream header
	size := (int(b[0]) + 1) * 4
	pos := 2
	for _, present := range []bool{b[1]&0x40 != 0, b[1]&0x80 != 0} { // compressed, uncompressed size
		if present {
			_, n := binary.Uvarint(b[pos:])
			pos += n
		}
	}
	if b[pos] != 0x21 || b[pos+1] != 1 { // LZMA2 filter with one property byte
		t.Fatalf("unexpected block header % x", b[:size])
	}
	b[pos+2] = 40
	binary.LittleEndian.PutUint32(b[size-4:], crc32.ChecksumIEEE(b[:size-4]))
}

// sevenZipWithHugeDict returns testdata/sample.7z with the LZMA2
// dictionary of its compressed header raised to 1 GiB, checksums fixed.
func sevenZipWithHugeDict(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sample.7z"))
	if err != nil {
		t.Fatal(err)
	}
	off := 32 + int(binary.LittleEndian.Uint64(data[12:]))
	header := data[off : off+int(binary.LittleEndian.Uint64(data[20:]))]
	// Coder flags (1-byte ID, has properties), LZMA2, 1 property byte.
	i := bytes.Index(header, []byte{0x21, 0x21, 0x01})
	if i < 0 {
		t.Fatalf("no LZMA2 coder in the header % x", header)
	}
	header[i+3] = 36 // 1 GiB
	binary.LittleEndian.PutUint32(data[28:], crc32.ChecksumIEEE(header))
	binary.LittleEndian.PutUint32(data[8:], crc32.ChecksumIEEE(data[12:32]))
	return data
}

func TestWalkArchiveRejectsHugeLzmaDictionaries(t *testing.T) {
	entries := []testEntry{{Name: "a.jpg", Data: "jpeg bytes"}}
	tarXz := buildArchive(t, formatTarXz, entries)
	setXzDict(t, tarXz, 0)

	// A zip member compressed with xz (method 95) is read normally...
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(95, func(w io.Writer) (io.WriteCloser, error) { return &xzOnClose{w: w}, nil })
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "a.jpg", Method: 95})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "jpeg bytes")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipXz := buf.Bytes()
	if got, err := walkFile(t, writeTemp(t, zipXz)); err != nil || got["a.jpg"].Data != "jpeg bytes" {
		t.Fatalf("zip with an xz member: %v, %v", got, err)
	}
	// ... unless it declares a huge dictionary.
	setXzDict(t, zipXz, bytes.Index(zipXz, magicXz))

	for name, data := range map[string][]byte{"tar.xz": tarXz, "zip (xz)": zipXz, "7z": sevenZipWithHugeDict(t)} {
		if _, err := walkFile(t, writeTemp(t, data)); err != errArchiveDict {
			t.Errorf("%s: err = %v, want errArchiveDict", name, err)
		}
	}
}

// xzOnClose compresses what was written with xz when it is closed:
// zip.Writer creates the compressor before it writes the local file header,
// and xz.NewWriter writes the stream header right away.
type xzOnClose struct {
	bytes.Buffer
	w io.Writer
}

func (c *xzOnClose) Close() error {
	xw, err := xz.NewWriter(c.w)
	if err != nil {
		return err
	}
	if _, err := c.WriteTo(xw); err != nil {
		return err
	}
	return xw.Close()
}

func TestSevenZipCoders(t *testing.T) {
	payload := bytes.Repeat([]byte("scratch dent "), 1000)
	input := func(b []byte) []io.ReadCloser { return []io.ReadCloser{io.NopCloser(bytes.NewReader(b))} }

	// LZMA as 7z stores it: the 5 property bytes of the LZMA header, then
	// the data; the size comes from the archive.
	var lz bytes.Buffer
	lw, err := lzma.WriterConfig{SizeInHeader: true, Size: int64(len(payload))}.NewWriter(&lz)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lw.Write(payload)
	if err := lw.Close(); err != nil {
		t.Fatal(err)
	}
	props, data := lz.Bytes()[:5], lz.Bytes()[lzma.HeaderLen:]
	// sevenzip passes buffered inputs (io.ByteReader); others work too.
	buffered := struct {
		*bytes.Reader
		io.Closer
	}{bytes.NewReader(data), io.NopCloser(nil)}
	for _, in := range [][]io.ReadCloser{input(data), {buffered}} {
		rc, err := newSevenZipLZMA(props, uint64(len(payload)), in)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(rc); err != nil || !bytes.Equal(got, payload) {
			t.Errorf("LZMA: %d bytes, %v", len(got), err)
		}
		_ = rc.Close()
	}

	var lz2 bytes.Buffer
	w2, err := lzma.Writer2Config{DictCap: 1 << 20}.NewWriter2(&lz2)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w2.Write(payload)
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	rc, err := newSevenZipLZMA2([]byte{lzma.EncodeDictCap(1 << 20)}, uint64(len(payload)), input(lz2.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(rc); err != nil || !bytes.Equal(got, payload) {
		t.Errorf("LZMA2: %d bytes, %v", len(got), err)
	}
	_ = rc.Close()

	huge := []byte{props[0], 0, 0, 0, 0x40} // 1 GiB
	if _, err := newSevenZipLZMA(huge, 1, input(nil)); err != errLzmaDictTooLarge {
		t.Errorf("LZMA with a 1 GiB dictionary: err = %v", err)
	}
	for _, p := range []byte{36, 40} { // 1 GiB, 4 GiB - 1
		if _, err := newSevenZipLZMA2([]byte{p}, 1, input(nil)); err != errLzmaDictTooLarge {
			t.Errorf("LZMA2 property %d: err = %v", p, err)
		}
	}
	if _, err := newSevenZipLZMA2([]byte{41}, 1, input(nil)); err == nil || err == errLzmaDictTooLarge {
		t.Errorf("LZMA2 property 41: err = %v", err)
	}
}

func TestRarMainHeaderFlags(t *testing.T) {
	for name, want := range map[string][2]bool{
		"rar3-subdirs.rar":         {false, false},
		"rar5-subdirs.rar":         {false, false},
		"rar5-hpsw.rar":            {false, true},
		"rar3-vols.part1-head.rar": {true, false},
		"rar5-vols.part1-head.rar": {true, false},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if v, e := rarMainHeaderFlags(data[:min(len(data), 512)]); v != want[0] || e != want[1] {
			t.Errorf("%s: volume=%v encrypted=%v, want %v", name, v, e, want)
		}
	}
	if v, e := rarMainHeaderFlags(magicRar5); v || e {
		t.Error("a bare signature must not be reported as anything")
	}
}
