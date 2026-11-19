// Package storagearchive implements dual-format archive packing (.tar.zst and
// .zip), SHA-256 integrity verification, and atomic / merge unpacking with
// strict path confinement and resource-exhaustion guards.
package storagearchive

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/flate"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Supported archive formats.
const (
	FormatTarZst = "tar.zst"
	FormatZip    = "zip"
)

// Supported unpack commit modes.
const (
	ModeAtomic         = "atomic"
	ModeMergeSkip      = "merge_skip"
	ModeMergeOverwrite = "merge_overwrite"
)

// Default limits and safety margins.
const (
	DefaultFreeHeadroomBytes uint64 = 16 << 20   // 16 MiB minimum disk headroom
	DefaultMaxUnpackBytes    int64  = 512 << 30  // 512 GiB decompression bomb ceiling
	DefaultMaxArchiveEntries int    = 2_000_000  // max files/dirs per archive
	keepMarkerFile                  = ".tm-keep" // internal S3 marker ignored during pack
	diskRecheckInterval      int64  = 64 << 20   // recheck free disk space every 64 MiB written
)

// Sentinel errors returned by Pack, VerifyFileSHA256, and Unpack.
var (
	ErrInvalidArchiveInput = errors.New("storagearchive: invalid input")
	ErrUnsafeArchiveEntry  = errors.New("storagearchive: unsafe archive entry")
	ErrChecksumMismatch    = errors.New("storagearchive: sha256 or byte count mismatch")
	ErrTargetExists        = errors.New("storagearchive: target directory already exists")
	ErrArchiveTooLarge     = errors.New("storagearchive: unpacked size exceeds allowed limit")
	ErrInsufficientStorage = errors.New("storagearchive: insufficient disk space")
)

// DiskUsageFunc queries filesystem capacity for path. Defaults to storage.DiskUsage.
type DiskUsageFunc func(path string) (storage.Usage, error)

// PackOptions configures archive creation from a source directory.
type PackOptions struct {
	SourceDir       string
	DestArchivePath string
	Format          string   // FormatTarZst (default if empty) or FormatZip
	Paths           []string // optional relative paths subset; empty packs entire SourceDir
	FreeHeadroom    *uint64  // nil uses DefaultFreeHeadroomBytes
	DiskUsage       DiskUsageFunc
}

// PackResult describes a completed archive file on disk.
type PackResult struct {
	Format       string `json:"format"`
	ArchiveBytes int64  `json:"archive_bytes"`
	SHA256       string `json:"sha256"`
	FilesCount   int    `json:"files_count"`
	RawBytes     int64  `json:"raw_bytes"`
}

// UnpackOptions configures archive verification and extraction into SpaceDir/TargetRel.
type UnpackOptions struct {
	ArchivePath          string
	Format               string // FormatTarZst (default if empty) or FormatZip
	SpaceDir             string // e.g. /data/datasets or /data/exports
	TargetRel            string // slash-separated relative target directory inside SpaceDir
	Mode                 string // ModeAtomic (default if empty), ModeMergeSkip, ModeMergeOverwrite
	ExpectedSHA256       string // when non-empty, verified strictly before opening archive entries
	ExpectedArchiveBytes int64  // when > 0, verified strictly before opening archive entries
	DeclaredRawBytes     *int64 // when non-nil, enforces exact ceiling on total extracted file bytes
	MaxRawBytes          int64  // fallback ceiling when DeclaredRawBytes is nil (defaults to DefaultMaxUnpackBytes)
	MaxEntries           int    // max entries allowed (defaults to DefaultMaxArchiveEntries)
	FreeHeadroom         *uint64
	DiskUsage            DiskUsageFunc
	CollectModTimes      bool   // populate ModTimes map in UnpackResult
	StageName            string // optional .unpack-<id> basename inside SpaceDir
}

// UnpackResult summarizes a completed unpack operation.
type UnpackResult struct {
	FilesCount int                  `json:"files_count"`
	RawBytes   int64                `json:"raw_bytes"`
	ModTimes   map[string]time.Time `json:"mod_times,omitempty"`
}

// NormalizeFormat validates format, defaulting empty strings to FormatTarZst.
func NormalizeFormat(format string) (string, error) {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return FormatTarZst, nil
	}
	if f == FormatTarZst || f == FormatZip {
		return f, nil
	}
	return "", fmt.Errorf("%w: unsupported archive format %q", ErrInvalidArchiveInput, format)
}

// FormatExtension returns the file extension (with leading dot) for a validated format.
func FormatExtension(format string) string {
	if format == FormatZip {
		return ".zip"
	}
	return ".tar.zst"
}

// NormalizeMode validates unpack commit mode, defaulting empty strings to ModeAtomic.
func NormalizeMode(mode string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return ModeAtomic, nil
	}
	switch m {
	case ModeAtomic, ModeMergeSkip, ModeMergeOverwrite:
		return m, nil
	default:
		return "", fmt.Errorf("%w: unsupported unpack mode %q", ErrInvalidArchiveInput, mode)
	}
}

// CleanRelPath validates and normalizes a slash-separated relative path.
// It rejects empty paths, ".", "..", leading or trailing escaping segments,
// backslashes, Windows drive letters, NUL bytes, and invalid UTF-8.
func CleanRelPath(rel string) (string, error) {
	if rel == "" || !utf8.ValidString(rel) || strings.ContainsRune(rel, 0) || strings.ContainsRune(rel, '\\') {
		return "", fmt.Errorf("%w: invalid relative path %q", ErrUnsafeArchiveEntry, rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute path %q not allowed", ErrUnsafeArchiveEntry, rel)
	}
	if len(rel) >= 2 && rel[1] == ':' && ('a' <= (rel[0]|0x20) && (rel[0]|0x20) <= 'z') {
		return "", fmt.Errorf("%w: drive-letter path %q not allowed", ErrUnsafeArchiveEntry, rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." || seg == "." || seg == "" {
			// Empty trailing segments are stripped by callers for directory entries before calling CleanRelPath.
			return "", fmt.Errorf("%w: unsafe path segment in %q", ErrUnsafeArchiveEntry, rel)
		}
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || !filepath.IsLocal(filepath.FromSlash(cleaned)) {
		return "", fmt.Errorf("%w: path escapes root %q", ErrUnsafeArchiveEntry, rel)
	}
	return cleaned, nil
}

// ValidSHA256Hex reports whether s is a 64-character hexadecimal SHA-256 digest.
func ValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'f'
		isUpper := c >= 'A' && c <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

// VerifyFileSHA256 computes the SHA-256 digest and byte size of archivePath on
// disk and verifies them against expectedSHA256 and expectedBytes.
func VerifyFileSHA256(ctx context.Context, archivePath, expectedSHA256 string, expectedBytes int64) (int64, string, error) {
	want := strings.ToLower(strings.TrimSpace(expectedSHA256))
	if want != "" && !ValidSHA256Hex(want) {
		return 0, "", fmt.Errorf("%w: invalid expected sha256 %q", ErrInvalidArchiveInput, expectedSHA256)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if !fi.Mode().IsRegular() {
		return 0, "", fmt.Errorf("%w: archive path is not a regular file", ErrInvalidArchiveInput)
	}
	if expectedBytes > 0 && fi.Size() != expectedBytes {
		return fi.Size(), "", fmt.Errorf("%w: archive size %d != expected %d", ErrChecksumMismatch, fi.Size(), expectedBytes)
	}

	h := sha256.New()
	buf := make([]byte, 256<<10)
	var nTotal int64
	for {
		if err := ctx.Err(); err != nil {
			return nTotal, "", err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			nTotal += int64(n)
			_, _ = h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nTotal, "", rerr
		}
	}
	if expectedBytes > 0 && nTotal != expectedBytes {
		return nTotal, "", fmt.Errorf("%w: archive bytes read %d != expected %d", ErrChecksumMismatch, nTotal, expectedBytes)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		return nTotal, got, fmt.Errorf("%w: sha256 %s != expected %s", ErrChecksumMismatch, got, want)
	}
	return nTotal, got, nil
}

type sourceEntry struct {
	rel     string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func resolveHeadroom(custom *uint64) uint64 {
	if custom != nil {
		return *custom
	}
	return DefaultFreeHeadroomBytes
}

func checkDiskHeadroom(dir string, needed uint64, headroom uint64, usageFn DiskUsageFunc) error {
	if usageFn == nil {
		usageFn = storage.DiskUsage
	}
	u, err := usageFn(dir)
	if errors.Is(err, storage.ErrUsageUnsupported) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.TotalBytes == 0 {
		return nil
	}
	want := needed + headroom
	if u.FreeBytes < want {
		return fmt.Errorf("%w: need %d bytes (including headroom), free %d bytes", ErrInsufficientStorage, want, u.FreeBytes)
	}
	return nil
}

// collectSourceEntries validates SourceDir and collects directories and regular
// files in deterministic lexical order, rejecting any symbolic links or special
// files along the way.
func collectSourceEntries(ctx context.Context, root *os.Root, sourceDir string, subset []string) ([]sourceEntry, int, int64, error) {
	var entries []sourceEntry
	var filesCount int
	var rawBytes int64

	if len(subset) == 0 {
		err := filepath.WalkDir(sourceDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if p == sourceDir {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			relOS, err := filepath.Rel(sourceDir, p)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(relOS)
			if path.Base(rel) == keepMarkerFile {
				return nil
			}
			cleanRel, err := CleanRelPath(rel)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidArchiveInput, err)
			}
			typ := d.Type()
			if typ&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: source contains symbolic link %q", ErrInvalidArchiveInput, cleanRel)
			}
			info, err := root.Lstat(filepath.FromSlash(cleanRel))
			if err != nil {
				return err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: source contains symbolic link %q", ErrInvalidArchiveInput, cleanRel)
			}
			if info.IsDir() {
				entries = append(entries, sourceEntry{
					rel:     cleanRel,
					mode:    0o755,
					modTime: info.ModTime().UTC(),
					isDir:   true,
				})
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: source contains non-regular file %q", ErrInvalidArchiveInput, cleanRel)
			}
			perm := info.Mode().Perm()
			if perm == 0 {
				perm = 0o644
			}
			entries = append(entries, sourceEntry{
				rel:     cleanRel,
				size:    info.Size(),
				mode:    perm,
				modTime: info.ModTime().UTC(),
			})
			filesCount++
			rawBytes += info.Size()
			return nil
		})
		if err != nil {
			return nil, 0, 0, err
		}
		return entries, filesCount, rawBytes, nil
	}

	seen := make(map[string]bool, len(subset))
	addParentDirs := func(cleanRel string) error {
		segs := strings.Split(cleanRel, "/")
		cur := ""
		for i := 0; i < len(segs)-1; i++ {
			if cur == "" {
				cur = segs[i]
			} else {
				cur = cur + "/" + segs[i]
			}
			fi, err := root.Lstat(filepath.FromSlash(cur))
			if err != nil {
				return err
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: source path %q traverses symbolic link %q", ErrInvalidArchiveInput, cleanRel, cur)
			}
			if !fi.IsDir() {
				return fmt.Errorf("%w: parent %q is not a directory", ErrInvalidArchiveInput, cur)
			}
			if !seen[cur] {
				seen[cur] = true
				entries = append(entries, sourceEntry{
					rel:     cur,
					mode:    0o755,
					modTime: fi.ModTime().UTC(),
					isDir:   true,
				})
			}
		}
		return nil
	}

	for _, rawPath := range subset {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		trimmed := strings.TrimSuffix(strings.TrimSpace(rawPath), "/")
		cleanRel, err := CleanRelPath(trimmed)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("%w: %v", ErrInvalidArchiveInput, err)
		}
		if err := addParentDirs(cleanRel); err != nil {
			return nil, 0, 0, err
		}
		fi, err := root.Lstat(filepath.FromSlash(cleanRel))
		if err != nil {
			return nil, 0, 0, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, 0, 0, fmt.Errorf("%w: source contains symbolic link %q", ErrInvalidArchiveInput, cleanRel)
		}
		if fi.IsDir() {
			subAbs := filepath.Join(sourceDir, filepath.FromSlash(cleanRel))
			err := filepath.WalkDir(subAbs, func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				relOS, err := filepath.Rel(sourceDir, p)
				if err != nil {
					return err
				}
				subRel := filepath.ToSlash(relOS)
				if path.Base(subRel) == keepMarkerFile {
					return nil
				}
				cleanSub, err := CleanRelPath(subRel)
				if err != nil {
					return fmt.Errorf("%w: %v", ErrInvalidArchiveInput, err)
				}
				if seen[cleanSub] {
					return nil
				}
				if d.Type()&fs.ModeSymlink != 0 {
					return fmt.Errorf("%w: source contains symbolic link %q", ErrInvalidArchiveInput, cleanSub)
				}
				sfi, err := root.Lstat(filepath.FromSlash(cleanSub))
				if err != nil {
					return err
				}
				if sfi.Mode()&fs.ModeSymlink != 0 {
					return fmt.Errorf("%w: source contains symbolic link %q", ErrInvalidArchiveInput, cleanSub)
				}
				seen[cleanSub] = true
				if sfi.IsDir() {
					entries = append(entries, sourceEntry{
						rel:     cleanSub,
						mode:    0o755,
						modTime: sfi.ModTime().UTC(),
						isDir:   true,
					})
					return nil
				}
				if !sfi.Mode().IsRegular() {
					return fmt.Errorf("%w: source contains non-regular file %q", ErrInvalidArchiveInput, cleanSub)
				}
				perm := sfi.Mode().Perm()
				if perm == 0 {
					perm = 0o644
				}
				entries = append(entries, sourceEntry{
					rel:     cleanSub,
					size:    sfi.Size(),
					mode:    perm,
					modTime: sfi.ModTime().UTC(),
				})
				filesCount++
				rawBytes += sfi.Size()
				return nil
			})
			if err != nil {
				return nil, 0, 0, err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			return nil, 0, 0, fmt.Errorf("%w: source contains non-regular file %q", ErrInvalidArchiveInput, cleanRel)
		}
		if seen[cleanRel] {
			continue
		}
		seen[cleanRel] = true
		perm := fi.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		entries = append(entries, sourceEntry{
			rel:     cleanRel,
			size:    fi.Size(),
			mode:    perm,
			modTime: fi.ModTime().UTC(),
		})
		filesCount++
		rawBytes += fi.Size()
	}

	slices.SortFunc(entries, func(a, b sourceEntry) int {
		return strings.Compare(a.rel, b.rel)
	})
	return entries, filesCount, rawBytes, nil
}

type hashingCountingWriter struct {
	w interface {
		io.Writer
	}
	h sha256Writer
	n int64
}

type sha256Writer interface {
	io.Writer
	Sum(b []byte) []byte
}

func newHashingWriter(w io.Writer) *hashingCountingWriter {
	return &hashingCountingWriter{w: w, h: sha256.New()}
}

func (hw *hashingCountingWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		hw.n += int64(n)
		_, _ = hw.h.Write(p[:n])
	}
	return n, err
}

func (hw *hashingCountingWriter) HexSum() string {
	return hex.EncodeToString(hw.h.Sum(nil))
}

// Pack packs opts.SourceDir (or opts.Paths within it) into opts.DestArchivePath
// in either FormatTarZst or FormatZip format, returning exact byte counts and SHA-256.
func Pack(ctx context.Context, opts PackOptions) (PackResult, error) {
	format, err := NormalizeFormat(opts.Format)
	if err != nil {
		return PackResult{}, err
	}
	srcTrimmed := strings.TrimSpace(opts.SourceDir)
	if srcTrimmed == "" {
		return PackResult{}, fmt.Errorf("%w: empty source directory", ErrInvalidArchiveInput)
	}
	dstTrimmed := strings.TrimSpace(opts.DestArchivePath)
	if dstTrimmed == "" {
		return PackResult{}, fmt.Errorf("%w: empty destination archive path", ErrInvalidArchiveInput)
	}

	srcInfo, err := os.Lstat(srcTrimmed)
	if err != nil {
		return PackResult{}, err
	}
	if srcInfo.Mode()&fs.ModeSymlink != 0 || !srcInfo.IsDir() {
		return PackResult{}, fmt.Errorf("%w: source %q is not a plain directory", ErrInvalidArchiveInput, srcTrimmed)
	}

	srcRoot, err := os.OpenRoot(srcTrimmed)
	if err != nil {
		return PackResult{}, err
	}
	defer srcRoot.Close()

	entries, filesCount, rawBytes, err := collectSourceEntries(ctx, srcRoot, srcTrimmed, opts.Paths)
	if err != nil {
		return PackResult{}, err
	}

	destParent := filepath.Dir(dstTrimmed)
	if err := os.MkdirAll(destParent, 0o755); err != nil {
		return PackResult{}, err
	}
	headroom := resolveHeadroom(opts.FreeHeadroom)
	if err := checkDiskHeadroom(destParent, uint64(max(rawBytes, 0)), headroom, opts.DiskUsage); err != nil {
		return PackResult{}, err
	}

	partPath := dstTrimmed + ".part-" + strings.ToLower(rand.Text()[:12])
	outFile, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return PackResult{}, err
	}
	cleanupPart := true
	defer func() {
		if cleanupPart {
			_ = outFile.Close()
			_ = os.Remove(partPath)
		}
	}()

	hw := newHashingWriter(outFile)
	switch format {
	case FormatTarZst:
		err = writeTarZstEntries(ctx, hw, srcRoot, entries)
	case FormatZip:
		err = writeZipEntries(ctx, hw, srcRoot, entries)
	}
	if err != nil {
		return PackResult{}, mapOSDiskError(err)
	}
	if err := outFile.Sync(); err != nil {
		return PackResult{}, mapOSDiskError(err)
	}
	if err := outFile.Close(); err != nil {
		return PackResult{}, mapOSDiskError(err)
	}
	if err := os.Rename(partPath, dstTrimmed); err != nil {
		return PackResult{}, mapOSDiskError(err)
	}
	cleanupPart = false

	return PackResult{
		Format:       format,
		ArchiveBytes: hw.n,
		SHA256:       hw.HexSum(),
		FilesCount:   filesCount,
		RawBytes:     rawBytes,
	}, nil
}

// Unpack verifies opts.ArchivePath (when ExpectedSHA256 or ExpectedArchiveBytes
// is provided), extracts all entries into a sibling temporary staging directory
// inside opts.SpaceDir under os.OpenRoot confinement, and commits into
// opts.SpaceDir/opts.TargetRel using atomic rename or merge semantics.
func Unpack(ctx context.Context, opts UnpackOptions) (UnpackResult, error) {
	format, err := NormalizeFormat(opts.Format)
	if err != nil {
		return UnpackResult{}, err
	}
	mode, err := NormalizeMode(opts.Mode)
	if err != nil {
		return UnpackResult{}, err
	}
	spaceDir := strings.TrimSpace(opts.SpaceDir)
	if spaceDir == "" {
		return UnpackResult{}, fmt.Errorf("%w: empty space directory", ErrInvalidArchiveInput)
	}
	targetRel, err := CleanRelPath(strings.TrimSuffix(strings.TrimSpace(opts.TargetRel), "/"))
	if err != nil {
		return UnpackResult{}, fmt.Errorf("%w: invalid target directory %q: %v", ErrInvalidArchiveInput, opts.TargetRel, err)
	}
	if strings.TrimSpace(opts.ArchivePath) == "" {
		return UnpackResult{}, fmt.Errorf("%w: empty archive path", ErrInvalidArchiveInput)
	}

	if err := os.MkdirAll(spaceDir, 0o755); err != nil {
		return UnpackResult{}, err
	}
	spaceRoot, err := os.OpenRoot(spaceDir)
	if err != nil {
		return UnpackResult{}, err
	}
	defer spaceRoot.Close()

	targetOSRel := filepath.FromSlash(targetRel)
	targetAbs := filepath.Join(spaceDir, targetOSRel)

	if mode == ModeAtomic {
		if _, err := spaceRoot.Lstat(targetOSRel); err == nil {
			return UnpackResult{}, fmt.Errorf("%w: %s", ErrTargetExists, targetRel)
		} else if !errors.Is(err, fs.ErrNotExist) {
			// Check whether an intermediate segment is a non-directory or symlink.
			if err := ensureParentDirsInRoot(spaceRoot, path.Dir(targetRel)); err != nil {
				return UnpackResult{}, err
			}
		}
	}

	// Verify archive integrity on disk strictly BEFORE creating any target/stage files.
	if opts.ExpectedSHA256 != "" || opts.ExpectedArchiveBytes > 0 {
		if _, _, err := VerifyFileSHA256(ctx, opts.ArchivePath, opts.ExpectedSHA256, opts.ExpectedArchiveBytes); err != nil {
			return UnpackResult{}, err
		}
	}

	maxRaw := opts.MaxRawBytes
	if maxRaw <= 0 {
		maxRaw = DefaultMaxUnpackBytes
	}
	var estNeed uint64
	if opts.DeclaredRawBytes != nil {
		if *opts.DeclaredRawBytes < 0 {
			return UnpackResult{}, fmt.Errorf("%w: negative declared raw bytes", ErrInvalidArchiveInput)
		}
		maxRaw = *opts.DeclaredRawBytes
		estNeed = uint64(*opts.DeclaredRawBytes)
	} else if fi, stErr := os.Stat(opts.ArchivePath); stErr == nil && fi.Size() > 0 {
		estNeed = uint64(fi.Size())
	}

	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxArchiveEntries
	}
	headroom := resolveHeadroom(opts.FreeHeadroom)
	if err := checkDiskHeadroom(spaceDir, estNeed, headroom, opts.DiskUsage); err != nil {
		return UnpackResult{}, err
	}

	stageName := strings.TrimSpace(opts.StageName)
	if stageName == "" || !strings.HasPrefix(stageName, ".unpack-") || strings.ContainsAny(stageName, `/\`) {
		stageName = ".unpack-" + strings.ToLower(rand.Text()[:16])
	}
	stageAbs := filepath.Join(spaceDir, stageName)
	if err := os.Mkdir(stageAbs, 0o755); err != nil {
		return UnpackResult{}, err
	}
	defer func() {
		_ = os.RemoveAll(stageAbs)
	}()

	stageRoot, err := os.OpenRoot(stageAbs)
	if err != nil {
		return UnpackResult{}, err
	}
	defer func() {
		if stageRoot != nil {
			_ = stageRoot.Close()
		}
	}()

	guard := &unpackLimiter{
		spaceDir: spaceDir,
		maxBytes: maxRaw,
		maxFiles: maxEntries,
		headroom: headroom,
		usageFn:  opts.DiskUsage,
		modTimes: nil,
	}
	if opts.CollectModTimes {
		guard.modTimes = make(map[string]time.Time)
	}

	switch format {
	case FormatTarZst:
		err = extractTarZst(ctx, opts.ArchivePath, stageAbs, stageRoot, guard)
	case FormatZip:
		err = extractZip(ctx, opts.ArchivePath, stageAbs, stageRoot, guard)
	}
	if err != nil {
		return UnpackResult{}, mapOSDiskError(err)
	}

	// Restore directory mtimes deepest-first before closing stageRoot.
	slices.SortFunc(guard.dirTimes, func(a, b dirModTime) int {
		return strings.Compare(b.rel, a.rel)
	})
	for _, dt := range guard.dirTimes {
		abs := filepath.Join(stageAbs, filepath.FromSlash(dt.rel))
		_ = os.Chtimes(abs, dt.modTime, dt.modTime)
	}

	if err := stageRoot.Close(); err != nil {
		stageRoot = nil
		return UnpackResult{}, err
	}
	stageRoot = nil

	if err := ctx.Err(); err != nil {
		return UnpackResult{}, err
	}

	switch mode {
	case ModeAtomic:
		parentRel := path.Dir(targetRel)
		if parentRel != "." && parentRel != "" {
			if err := ensureParentDirsInRoot(spaceRoot, parentRel); err != nil {
				return UnpackResult{}, err
			}
		}
		if _, err := spaceRoot.Lstat(targetOSRel); err == nil {
			return UnpackResult{}, fmt.Errorf("%w: %s", ErrTargetExists, targetRel)
		}
		if err := os.Rename(stageAbs, targetAbs); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return UnpackResult{}, fmt.Errorf("%w: %s", ErrTargetExists, targetRel)
			}
			return UnpackResult{}, mapOSDiskError(err)
		}

	case ModeMergeSkip, ModeMergeOverwrite:
		if err := ensureParentDirsInRoot(spaceRoot, targetRel); err != nil {
			return UnpackResult{}, err
		}
		if err := mergeStageIntoTarget(ctx, stageAbs, targetAbs, mode == ModeMergeOverwrite, guard.modTimes); err != nil {
			return UnpackResult{}, mapOSDiskError(err)
		}
	}

	return UnpackResult{
		FilesCount: guard.filesCount,
		RawBytes:   guard.writtenBytes,
		ModTimes:   guard.modTimes,
	}, nil
}

type dirModTime struct {
	rel     string
	modTime time.Time
}

type unpackLimiter struct {
	spaceDir     string
	maxBytes     int64
	maxFiles     int
	headroom     uint64
	usageFn      DiskUsageFunc
	entriesSeen  int
	filesCount   int
	writtenBytes int64
	sinceCheck   int64
	dirTimes     []dirModTime
	modTimes     map[string]time.Time
}

func (u *unpackLimiter) countEntry() error {
	u.entriesSeen++
	if u.entriesSeen > u.maxFiles {
		return fmt.Errorf("%w: entry count exceeds limit %d", ErrArchiveTooLarge, u.maxFiles)
	}
	return nil
}

func (u *unpackLimiter) copyBounded(ctx context.Context, dst io.Writer, src io.Reader, declaredSize int64) (int64, error) {
	if declaredSize < 0 {
		return 0, fmt.Errorf("%w: negative entry size %d", ErrUnsafeArchiveEntry, declaredSize)
	}
	if u.writtenBytes+declaredSize > u.maxBytes {
		return 0, fmt.Errorf("%w: unpacked bytes would exceed %d", ErrArchiveTooLarge, u.maxBytes)
	}
	buf := make([]byte, 128<<10)
	var fileWritten int64
	for {
		if err := ctx.Err(); err != nil {
			return fileWritten, err
		}
		remaining := u.maxBytes - u.writtenBytes
		stepMax := int64(len(buf))
		if remaining+1 < stepMax {
			stepMax = remaining + 1
		}
		if stepMax <= 0 {
			stepMax = 1
		}
		n, rerr := src.Read(buf[:stepMax])
		if n > 0 {
			u.writtenBytes += int64(n)
			fileWritten += int64(n)
			u.sinceCheck += int64(n)
			if u.writtenBytes > u.maxBytes {
				return fileWritten, fmt.Errorf("%w: unpacked bytes exceeded limit %d", ErrArchiveTooLarge, u.maxBytes)
			}
			if declaredSize >= 0 && fileWritten > declaredSize {
				return fileWritten, fmt.Errorf("%w: entry exceeded its header size %d", ErrUnsafeArchiveEntry, declaredSize)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fileWritten, werr
			}
			if u.sinceCheck >= diskRecheckInterval {
				u.sinceCheck = 0
				if err := checkDiskHeadroom(u.spaceDir, 0, u.headroom, u.usageFn); err != nil {
					return fileWritten, err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fileWritten, rerr
		}
	}
	return fileWritten, nil
}

func ensureParentDirsInRoot(root *os.Root, cleanRel string) error {
	if cleanRel == "" || cleanRel == "." {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(cleanRel, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		osCur := filepath.FromSlash(cur)
		fi, err := root.Lstat(osCur)
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := root.Mkdir(osCur, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
				return mkErr
			}
			fi, err = root.Lstat(osCur)
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%w: path component %q is not a plain directory", ErrUnsafeArchiveEntry, cur)
		}
	}
	return nil
}

func mergeStageIntoTarget(ctx context.Context, stageAbs, targetAbs string, overwrite bool, modTimes map[string]time.Time) error {
	dstRoot, err := os.OpenRoot(targetAbs)
	if err != nil {
		return err
	}
	defer dstRoot.Close()

	return filepath.WalkDir(stageAbs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == stageAbs {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relOS, err := filepath.Rel(stageAbs, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if d.IsDir() {
			return ensureParentDirsInRoot(dstRoot, rel)
		}
		if err := ensureParentDirsInRoot(dstRoot, path.Dir(rel)); err != nil {
			return err
		}
		osRel := filepath.FromSlash(rel)
		existing, err := dstRoot.Lstat(osRel)
		if err == nil {
			if existing.Mode()&fs.ModeSymlink != 0 || existing.IsDir() {
				return fmt.Errorf("%w: destination path %q is a symlink or directory", ErrUnsafeArchiveEntry, rel)
			}
			if !overwrite {
				if modTimes != nil {
					modTimes[rel] = existing.ModTime().UTC()
				}
				return nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		dstFileAbs := filepath.Join(targetAbs, osRel)
		if err := os.Rename(p, dstFileAbs); err != nil {
			return err
		}
		if modTimes != nil {
			if st, stErr := dstRoot.Lstat(osRel); stErr == nil {
				modTimes[rel] = st.ModTime().UTC()
			}
		}
		return nil
	})
}

func mapOSDiskError(err error) error {
	if err == nil {
		return nil
	}
	// Check common ENOSPC error text cross-platform without OS-specific syscall constants.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "no space left on device") || strings.Contains(msg, "disk full") || strings.Contains(msg, "not enough space") {
		return fmt.Errorf("%w: %v", ErrInsufficientStorage, err)
	}
	return err
}

// Pooled fast flate writers for .zip creation (avoids ~800 KiB allocation per file).
var flateWriterPool sync.Pool

func registerFastZipCompressor(zw *zip.Writer) {
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		fw, ok := flateWriterPool.Get().(*flate.Writer)
		if ok {
			fw.Reset(out)
		} else {
			var err error
			fw, err = flate.NewWriter(out, flate.BestSpeed)
			if err != nil {
				return nil, err
			}
		}
		return &pooledFlateWriter{fw: fw}, nil
	})
}

type pooledFlateWriter struct {
	fw *flate.Writer
}

func (p *pooledFlateWriter) Write(b []byte) (int, error) {
	return p.fw.Write(b)
}

func (p *pooledFlateWriter) Close() error {
	if p.fw == nil {
		return nil
	}
	err := p.fw.Close()
	flateWriterPool.Put(p.fw)
	p.fw = nil
	return err
}

// ZipMethodForName returns zip.Store for already-compressed image formats and
// zip.Deflate for text/XML/YAML/JSON/BMP/TIFF files.
func ZipMethodForName(name string) uint16 {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif":
		return zip.Store
	default:
		return zip.Deflate
	}
}
