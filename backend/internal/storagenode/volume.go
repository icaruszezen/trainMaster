package storagenode

import (
	"context"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// keepMarkerFile is a hidden 0-byte marker written on object stores (S3) to
// materialize empty dataset or data-version directories. It is invisible to
// WalkFiles and Lstat.
const keepMarkerFile = ".tm-keep"

// File is an open regular file supporting streaming reads, seeking, random
// range reads (for TIFF/JPEG/WebP header & EXIF inspection), and Stat.
type File interface {
	io.Reader
	io.Seeker
	io.ReaderAt
	io.Closer
	Name() string
	Stat() (fs.FileInfo, error)
}

// TopDir describes an immediate subdirectory under a managed space (such as
// datasets/<name> or exports/data-versions/<name>).
type TopDir struct {
	Name       string
	ModifiedAt time.Time
}

// Dir is a directory-scoped virtual filesystem handle (replacing *os.Root)
// whose relative paths are slash-separated and strictly confined inside the
// opened directory.
type Dir interface {
	// NodeID returns the owning storage node ID (0 for local staging dirs).
	NodeID() int64
	// DisplayPath returns a human-readable location string for logs and errors.
	DisplayPath() string
	// LocalDir returns the absolute local OS directory path when backed by the
	// local filesystem, or "" for remote protocols (S3, SMB).
	LocalDir() string
	Close() error

	Open(rel string) (File, error)
	Lstat(rel string) (fs.FileInfo, error)
	Stat(rel string) (fs.FileInfo, error)
	ReadDirNames(rel string) ([]string, error)

	// WalkFiles traverses entries below Dir in lexical order. Returning
	// filepath.SkipDir on a directory skips its subtree. Symbolic links are
	// reported with ModeSymlink in info.Mode() and are never followed.
	WalkFiles(ctx context.Context, visit func(rel string, info fs.FileInfo, isDir bool) error) error

	EnsureDir(rel string) error
	PutFile(ctx context.Context, rel string, r io.Reader, size int64, perm fs.FileMode) (fs.FileInfo, error)
	PutBytes(ctx context.Context, rel string, data []byte, perm fs.FileMode) error
	CreateExclusive(ctx context.Context, rel string, data []byte, perm fs.FileMode) error
	RenameFile(ctx context.Context, fromRel, toRel string, replace bool) error
	RemoveFile(ctx context.Context, rel string) error
}

// Volume abstracts a storage node's managed spaces (SpaceDatasets and
// SpaceExports) across local directories, S3 buckets, and SMB shares.
type Volume interface {
	NodeID() int64
	Kind() string
	IsBuiltin() bool
	// SupportsFastDirRename reports whether directory renames are O(1) metadata
	// operations (true for local and SMB, false for S3).
	SupportsFastDirRename() bool
	DisplayPath(space, rel string) string

	Probe(ctx context.Context) error
	Usage(ctx context.Context) (storage.Usage, error)

	DirExists(ctx context.Context, space, dirRel string) (bool, time.Time, error)
	// IsPlainDir checks that space/dirRel exists and is a real directory (not a
	// regular file or symlink). Returns fs.ErrNotExist if missing, or
	// ErrNotDirectory if occupied by a symlink or file.
	IsPlainDir(ctx context.Context, space, dirRel string) error
	MkdirDir(ctx context.Context, space, dirRel string, exclusive bool) error
	OpenDir(ctx context.Context, space, dirRel string) (Dir, error)
	ListTopDirs(ctx context.Context, space, subPrefix string) ([]TopDir, error)
	RenameDir(ctx context.Context, space, fromRel, toRel string) error
	RemoveDirAll(ctx context.Context, space, dirRel string) error
	RemoveEmptyDir(ctx context.Context, space, dirRel string) error

	// CopyFileWithin copies a file within the same storage node (e.g. from
	// datasets/<ds>/<imgRel> to exports/<dv>/<dstRel>), returning whether a
	// zero-space hard link was created (local same-FS) or a server-side copy
	// was performed (S3 CopyObject / SMB).
	CopyFileWithin(ctx context.Context, srcSpace, srcRel, dstSpace, dstRel string, linkFn func(oldname, newname string) error) (info fs.FileInfo, linked bool, err error)

	Close() error
}

// CleanRelPath validates and normalizes a slash-separated relative path inside
// a space or Dir. Empty paths, ".", "..", leading slashes, backslashes that
// escape, Windows drive letters, NUL bytes, and invalid UTF-8 are rejected.
func CleanRelPath(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	if rel == "" || !utf8.ValidString(rel) || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return "", storage.ErrInvalidPath
	}
	if len(rel) >= 2 && rel[1] == ':' && ('a' <= (rel[0]|0x20) && (rel[0]|0x20) <= 'z') {
		return "", storage.ErrInvalidPath
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || !filepath.IsLocal(filepath.FromSlash(cleaned)) {
		return "", storage.ErrInvalidPath
	}
	return cleaned, nil
}

// CleanOptionalRelPath allows "" or "." (meaning the root of the space/dir)
// and otherwise delegates to CleanRelPath.
func CleanOptionalRelPath(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, `\`, "/"))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	return CleanRelPath(rel)
}

func validSpace(space string) bool {
	return space == SpaceDatasets || space == SpaceExports
}

// memFileInfo is an in-memory fs.FileInfo implementation used by S3 (and
// synthetic directory entries).
type memFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (m memFileInfo) Name() string       { return m.name }
func (m memFileInfo) Size() int64        { return m.size }
func (m memFileInfo) Mode() fs.FileMode  { return m.mode }
func (m memFileInfo) ModTime() time.Time { return m.modTime }
func (m memFileInfo) IsDir() bool        { return m.mode.IsDir() }
func (m memFileInfo) Sys() any           { return nil }
