package dataset

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"unicode/utf8"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz/lzma"
	"github.com/xi2/xz"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// archiveFormat is a supported archive container, detected from the first
// bytes of the file (never from the file name).
type archiveFormat string

const (
	formatZip    archiveFormat = "zip"
	formatTar    archiveFormat = "tar"
	formatTarGz  archiveFormat = "tar.gz"
	formatTarBz2 archiveFormat = "tar.bz2"
	formatTarXz  archiveFormat = "tar.xz"
	formatTarZst archiveFormat = "tar.zst"
	format7z     archiveFormat = "7z"
	formatRar    archiveFormat = "rar"
)

// Decompression memory bounds for crafted archives that declare huge
// windows. Real archives use far less (zstd -19: 8 MiB, WinRAR 7 default:
// 32 MiB, xz -9 and 7-Zip "Ultra": 64 MiB).
const (
	maxZstdWindow = 256 << 20
	maxRarDict    = 1 << 30
	maxLzmaDict   = 256 << 20 // xz (LZMA2) and 7z (LZMA, LZMA2)
)

var (
	errArchiveFormat = &Error{Kind: ErrInvalid,
		Message: "无法识别的压缩包格式，支持 zip、tar、tar.gz、tar.bz2、tar.xz、tar.zst、7z、rar"}
	errArchiveEncrypted = &Error{Kind: ErrInvalid, Message: "压缩包已加密，暂不支持"}
	errArchiveVolumes   = &Error{Kind: ErrInvalid, Message: "暂不支持分卷压缩包，请合并为单个压缩包后重新上传"}
	errArchiveDict      = &Error{Kind: ErrInvalid,
		Message: "压缩包使用的字典过大（超过 256 MiB），为避免耗尽内存已拒绝解压，请用常规压缩级别重新打包"}
)

func corruptArchive(err error) error {
	return &Error{Kind: ErrInvalid, Message: "压缩包已损坏或不完整：" + err.Error()}
}

var (
	magicZip      = []byte("PK\x03\x04")
	magicZipEmpty = []byte("PK\x05\x06")
	magicZipSpan  = []byte("PK\x07\x08") // first part of a split/spanned zip
	magicGzip     = []byte{0x1f, 0x8b}
	magicBzip2    = []byte("BZh")
	magicXz       = []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	magicZstd     = []byte{0x28, 0xb5, 0x2f, 0xfd}
	magic7z       = []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}
	magicRar4     = []byte("Rar!\x1a\x07\x00")
	magicRar5     = []byte("Rar!\x1a\x07\x01\x00")
)

// detectFormat identifies the archive type from its first 512 bytes.
func detectFormat(head []byte) (archiveFormat, error) {
	switch {
	case bytes.HasPrefix(head, magicZip), bytes.HasPrefix(head, magicZipEmpty):
		return formatZip, nil
	case bytes.HasPrefix(head, magicZipSpan):
		return "", errArchiveVolumes
	case bytes.HasPrefix(head, magicGzip):
		return formatTarGz, nil
	case bytes.HasPrefix(head, magicBzip2):
		return formatTarBz2, nil
	case bytes.HasPrefix(head, magicXz):
		return formatTarXz, nil
	case bytes.HasPrefix(head, magicZstd):
		return formatTarZst, nil
	case bytes.HasPrefix(head, magic7z):
		return format7z, nil
	case bytes.HasPrefix(head, magicRar4), bytes.HasPrefix(head, magicRar5):
		return formatRar, nil
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return formatTar, nil
	}
	return "", errArchiveFormat
}

type entryType int

const (
	entryFile  entryType = iota // regular file
	entryDir                    // directory
	entryLink                   // symbolic or hard link
	entryOther                  // device, FIFO, socket, ...
)

// archiveEntry is one member of an archive. Data is only valid for
// entryFile and only until the walk callback returns.
type archiveEntry struct {
	Name string // as stored, decoded to UTF-8; not yet validated
	Type entryType
	Data io.Reader
}

// archiveReadError marks errors that come from reading the archive rather
// than from the callback (e.g. writing the extracted file).
type archiveReadError struct{ err error }

func (e *archiveReadError) Error() string { return e.err.Error() }
func (e *archiveReadError) Unwrap() error { return e.err }

type entryReader struct{ r io.Reader }

func (r entryReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		err = &archiveReadError{err}
	}
	return n, err
}

// progressFunc receives extraction progress: done out of total in unit
// ("entries" when the entry count is known up front, else "bytes" of the
// archive consumed).
type progressFunc func(unit string, done, total int64)

// walkArchive detects the format of the archive in f (size bytes) and
// calls fn for every entry in archive order. Errors returned by fn are
// passed through unchanged; problems with the archive itself become
// user-facing *Error values (unknown format, encrypted, multi-volume,
// corrupt).
func walkArchive(ctx context.Context, f *os.File, size int64, fn func(*archiveEntry) error, progress progressFunc) error {
	head := make([]byte, 512)
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read archive header: %w", err)
	}
	format, err := detectFormat(head[:n])
	if err != nil {
		return err
	}

	var walkErr error
	switch format {
	case formatZip:
		walkErr = walkZip(ctx, f, size, fn, progress)
	case format7z:
		walkErr = walk7z(ctx, f, size, fn, progress)
	case formatRar:
		walkErr = walkRar(ctx, f, size, head[:n], fn, progress)
	default:
		walkErr = walkTar(ctx, f, size, format, fn, progress)
	}
	return mapArchiveError(walkErr)
}

// mapArchiveError turns errors raised while reading the archive into
// user-facing messages and leaves all other errors alone.
func mapArchiveError(err error) error {
	var rerr *archiveReadError
	if err == nil || !errors.As(err, &rerr) {
		return err
	}
	var szErr *sevenzip.ReadError
	switch {
	case errors.As(rerr.err, &szErr) && szErr.Encrypted,
		errors.Is(rerr.err, rardecode.ErrArchiveEncrypted),
		errors.Is(rerr.err, rardecode.ErrArchivedFileEncrypted),
		errors.Is(rerr.err, rardecode.ErrBadPassword):
		return errArchiveEncrypted
	case errors.Is(rerr.err, rardecode.ErrMultiVolume), errors.Is(rerr.err, rardecode.ErrBadVolumeNumber):
		return errArchiveVolumes
	case errors.Is(rerr.err, errLzmaDictTooLarge), errors.Is(rerr.err, xz.ErrMemlimit):
		return errArchiveDict
	case errors.Is(rerr.err, zip.ErrAlgorithm):
		return &Error{Kind: ErrInvalid, Message: "压缩包使用了不支持的压缩算法，请改用常规的 zip（Deflate）、7z 或 tar.gz 格式重新打包"}
	}
	return corruptArchive(rerr.err)
}

func readErr(err error) error { return &archiveReadError{err} }

// decodeEntryName converts a legacy-encoded entry name to UTF-8. Archives
// created on Chinese Windows store names in the GBK code page without
// flagging the encoding; GB18030 is a superset of GBK.
func decodeEntryName(name string) string {
	if utf8.ValidString(name) {
		return name
	}
	if s, err := simplifiedchinese.GB18030.NewDecoder().String(name); err == nil {
		return s
	}
	return name
}

func walkZip(ctx context.Context, f *os.File, size int64, fn func(*archiveEntry) error, progress progressFunc) error {
	if zipIsMultiVolume(f, size) {
		return errArchiveVolumes
	}
	zr, err := zip.NewReader(f, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) { // names are validated by the caller
		return readErr(err)
	}
	zr.RegisterDecompressor(12, func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) })
	zr.RegisterDecompressor(93, newZstdReadCloser)
	zr.RegisterDecompressor(95, func(r io.Reader) io.ReadCloser {
		xr, err := xz.NewReader(r, maxLzmaDict)
		if err != nil {
			return errorReadCloser{err}
		}
		return io.NopCloser(xr)
	})
	for _, zf := range zr.File {
		if zf.Flags&0x1 != 0 {
			return errArchiveEncrypted
		}
	}

	total := int64(len(zr.File))
	for i, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Names flagged as UTF-8 (bit 11) are decoded too: the flag is
		// sometimes set on names that are not UTF-8 at all.
		e := &archiveEntry{Name: decodeEntryName(zf.Name), Type: modeType(zf.Mode())}
		if zf.Mode().IsDir() || (len(zf.Name) > 0 && zf.Name[len(zf.Name)-1] == '/') {
			e.Type = entryDir
		}
		if err := callWithReader(e, zf.Open, fn); err != nil {
			return err
		}
		progress("entries", int64(i+1), total)
	}
	return nil
}

// callWithReader opens the entry's data for regular files and calls fn.
func callWithReader(e *archiveEntry, open func() (io.ReadCloser, error), fn func(*archiveEntry) error) error {
	if e.Type != entryFile {
		return fn(e)
	}
	rc, err := open()
	if err != nil {
		return readErr(err)
	}
	e.Data = entryReader{rc}
	err = fn(e)
	// Closing promptly lets 7z reuse its decompressor for the next entry.
	if cerr := rc.Close(); cerr != nil && err == nil {
		err = readErr(cerr)
	}
	return err
}

func modeType(m fs.FileMode) entryType {
	switch {
	case m.IsDir():
		return entryDir
	case m&fs.ModeSymlink != 0:
		return entryLink
	case m.IsRegular():
		return entryFile
	}
	return entryOther
}

// zipIsMultiVolume reports whether the end-of-central-directory record
// belongs to a split archive (it is on a disk other than the first). Such
// a last part (name.zip next to name.z01, ...) may otherwise parse, with
// entries pointing into the missing parts.
func zipIsMultiVolume(f *os.File, size int64) bool {
	const eocdLen, maxComment = 22, 65535
	start := max(0, size-eocdLen-maxComment)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return false
	}
	i := bytes.LastIndex(buf, magicZipEmpty)
	if i < 0 || len(buf)-i < eocdLen {
		return false
	}
	diskNumber := binary.LittleEndian.Uint16(buf[i+4:])
	dirDisk := binary.LittleEndian.Uint16(buf[i+6:])
	if diskNumber == 0xffff || dirDisk == 0xffff {
		// ZIP64: the real values are in the ZIP64 records; the locator
		// right before the record holds the total number of disks.
		const locatorLen = 20
		if i < locatorLen || !bytes.HasPrefix(buf[i-locatorLen:], []byte("PK\x06\x07")) {
			return false
		}
		return binary.LittleEndian.Uint32(buf[i-locatorLen+16:]) > 1
	}
	return diskNumber != 0 || dirDisk != 0
}

func newZstdReadCloser(r io.Reader) io.ReadCloser {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
	if err != nil {
		return errorReadCloser{err}
	}
	return d.IOReadCloser()
}

type errorReadCloser struct{ err error }

func (e errorReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errorReadCloser) Close() error             { return nil }

// countingReader counts the bytes read from the archive file for progress
// reporting of stream formats.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func walkTar(ctx context.Context, f *os.File, size int64, format archiveFormat, fn func(*archiveEntry) error, progress progressFunc) error {
	cr := &countingReader{r: io.NewSectionReader(f, 0, size)}
	src := bufio.NewReaderSize(cr, 1<<20)
	var r io.Reader
	switch format {
	case formatTar:
		r = src
	case formatTarGz:
		gr, err := gzip.NewReader(src)
		if err != nil {
			return readErr(err)
		}
		defer gr.Close()
		r = gr
	case formatTarBz2:
		r = bzip2.NewReader(src)
	case formatTarXz:
		// github.com/xi2/xz refuses dictionaries over the limit before
		// allocating them; ulikunitz/xz allocates whatever the file declares.
		xr, err := xz.NewReader(src, maxLzmaDict)
		if err != nil {
			return readErr(err)
		}
		r = xr
	case formatTarZst:
		zr, err := zstd.NewReader(src, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if err != nil {
			return readErr(err)
		}
		defer zr.Close()
		r = zr
	default:
		return fmt.Errorf("walkTar: unexpected format %q", format)
	}

	tr := tar.NewReader(r)
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			if first && errors.Is(err, tar.ErrHeader) {
				return errArchiveFormat // e.g. a gzip file that does not contain a tar archive
			}
			return readErr(err)
		}
		e := &archiveEntry{Name: decodeEntryName(hdr.Name)}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeGNUSparse:
			e.Type, e.Data = entryFile, entryReader{tr}
		case tar.TypeDir:
			e.Type = entryDir
		case tar.TypeSymlink, tar.TypeLink:
			e.Type = entryLink
		case tar.TypeXGlobalHeader:
			continue // PAX metadata, not a member
		default:
			e.Type = entryOther
		}
		if err := fn(e); err != nil {
			return err
		}
		progress("bytes", cr.n, size)
	}
}

func walk7z(ctx context.Context, f *os.File, size int64, fn func(*archiveEntry) error, progress progressFunc) error {
	if sevenZipTruncated(f, size) {
		return errArchiveVolumes
	}
	zr, err := sevenzip.NewReader(f, size)
	if err != nil {
		return readErr(err)
	}
	total := int64(len(zr.File))
	for i, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := &archiveEntry{Name: decodeEntryName(zf.Name), Type: modeType(zf.Mode())}
		if err := callWithReader(e, zf.Open, fn); err != nil {
			return err
		}
		progress("entries", int64(i+1), total)
	}
	return nil
}

// sevenzip decodes LZMA and LZMA2 coders with ulikunitz/xz/lzma, which
// allocates the dictionary size the coder properties declare (up to 4 GiB)
// before reading any data. These replacements decode the same way but
// check the size first. They also cover the compressed archive header,
// which sevenzip.NewReader decodes.
func init() {
	sevenzip.RegisterDecompressor([]byte{0x03, 0x01, 0x01}, newSevenZipLZMA)
	sevenzip.RegisterDecompressor([]byte{0x21}, newSevenZipLZMA2)
}

var errLzmaDictTooLarge = errors.New("LZMA dictionary exceeds the decompression memory limit")

// newSevenZipLZMA decodes an LZMA coder. Its properties are the first 5
// bytes of an LZMA ("alone") header: lc/lp/pb and the dictionary size.
func newSevenZipLZMA(props []byte, size uint64, readers []io.ReadCloser) (io.ReadCloser, error) {
	if len(readers) != 1 || len(props) != 5 {
		return nil, errors.New("7z LZMA: invalid coder properties")
	}
	if binary.LittleEndian.Uint32(props[1:]) > maxLzmaDict {
		return nil, errLzmaDictTooLarge
	}
	in := &prefixedReader{head: binary.LittleEndian.AppendUint64(bytes.Clone(props), size)}
	if br, ok := readers[0].(byteReader); ok {
		in.r = br
	} else {
		in.r = bufio.NewReader(readers[0])
	}
	lr, err := lzma.NewReader(in)
	if err != nil {
		return nil, err
	}
	return coderReadCloser{lr, readers[0]}, nil
}

type byteReader interface {
	io.Reader
	io.ByteReader
}

// prefixedReader yields head, then the bytes of r. Being an io.ByteReader
// matters: the LZMA decoder reads its input byte by byte.
type prefixedReader struct {
	head []byte
	r    byteReader
}

func (p *prefixedReader) Read(b []byte) (int, error) {
	if len(p.head) == 0 {
		return p.r.Read(b)
	}
	n := copy(b, p.head)
	p.head = p.head[n:]
	return n, nil
}

func (p *prefixedReader) ReadByte() (byte, error) {
	if len(p.head) == 0 {
		return p.r.ReadByte()
	}
	c := p.head[0]
	p.head = p.head[1:]
	return c, nil
}

// newSevenZipLZMA2 decodes an LZMA2 coder. Its property byte encodes the
// dictionary size like the LZMA2 filter of xz.
func newSevenZipLZMA2(props []byte, _ uint64, readers []io.ReadCloser) (io.ReadCloser, error) {
	if len(readers) != 1 || len(props) != 1 || props[0] > 40 {
		return nil, errors.New("7z LZMA2: invalid coder properties")
	}
	dict := int64(math.MaxUint32) // 40 means 4 GiB - 1
	if p := props[0]; p < 40 {
		dict = int64(2|p&1) << (p/2 + 11)
	}
	if dict > maxLzmaDict {
		return nil, errLzmaDictTooLarge
	}
	lr, err := lzma.Reader2Config{DictCap: int(dict)}.NewReader2(readers[0])
	if err != nil {
		return nil, err
	}
	return coderReadCloser{lr, readers[0]}, nil
}

// coderReadCloser reads decoded data and closes the coder's input.
type coderReadCloser struct {
	io.Reader
	in io.Closer
}

func (c coderReadCloser) Close() error { return c.in.Close() }

// sevenZipTruncated reports whether the header database lies beyond the
// end of the file, which is what the first volume of a split archive
// (name.7z.001) and an incomplete download look like.
func sevenZipTruncated(f *os.File, size int64) bool {
	var sh [32]byte // signature header: magic, version, CRC, next header offset and size, CRC
	if _, err := f.ReadAt(sh[:], 0); err != nil {
		return false
	}
	offset := binary.LittleEndian.Uint64(sh[12:])
	length := binary.LittleEndian.Uint64(sh[20:])
	end := uint64(len(sh)) + offset + length
	return end < offset || end > uint64(size) // also catches overflow
}

func walkRar(ctx context.Context, f *os.File, size int64, head []byte, fn func(*archiveEntry) error, progress progressFunc) error {
	switch volume, encrypted := rarMainHeaderFlags(head); {
	case volume:
		return errArchiveVolumes
	case encrypted:
		return errArchiveEncrypted
	}
	cr := &countingReader{r: io.NewSectionReader(f, 0, size)}
	rr, err := rardecode.NewReader(cr, rardecode.MaxDictionarySize(maxRarDict))
	if err != nil {
		return readErr(err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := rr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return readErr(err)
		}
		if hdr.Encrypted {
			return errArchiveEncrypted
		}
		e := &archiveEntry{Name: decodeEntryName(hdr.Name), Type: modeType(hdr.Mode())}
		switch {
		case hdr.IsDir:
			e.Type = entryDir
		case hdr.LinkType != 0:
			e.Type = entryLink // RAR5 symlinks, hard links and junctions
		}
		if e.Type == entryFile {
			e.Data = entryReader{rr}
		}
		if err := fn(e); err != nil {
			return err
		}
		progress("bytes", cr.n, size)
	}
}

// rarMainHeaderFlags reads the "volume" and "encrypted headers" flags of
// the main archive header that follows the RAR signature. It returns false
// for anything it cannot parse; rardecode reports those problems itself.
func rarMainHeaderFlags(head []byte) (volume, encrypted bool) {
	if bytes.HasPrefix(head, magicRar4) {
		// Marker block (7 bytes), then HEAD_CRC(2) HEAD_TYPE(1) HEAD_FLAGS(2).
		b := head[len(magicRar4):]
		if len(b) < 5 || b[2] != 0x73 {
			return false, false
		}
		flags := binary.LittleEndian.Uint16(b[3:])
		return flags&0x0001 != 0, flags&0x0080 != 0
	}
	if !bytes.HasPrefix(head, magicRar5) || len(head) < len(magicRar5)+4 {
		return false, false
	}
	// CRC32(4), then vints: header size, header type, header flags,
	// [extra area size], [data size], and for the main header the archive
	// flags. Header type 4 is the archive encryption header.
	b := head[len(magicRar5)+4:]
	next := func() (uint64, bool) {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return 0, false
		}
		b = b[n:]
		return v, true
	}
	_, ok1 := next()
	typ, ok2 := next()
	hflags, ok3 := next()
	if !ok1 || !ok2 || !ok3 {
		return false, false
	}
	if typ == 4 {
		return false, true
	}
	if typ != 1 {
		return false, false
	}
	if hflags&0x0001 != 0 {
		if _, ok := next(); !ok {
			return false, false
		}
	}
	if hflags&0x0002 != 0 {
		if _, ok := next(); !ok {
			return false, false
		}
	}
	aflags, ok := next()
	return ok && aflags&0x0001 != 0, false
}
