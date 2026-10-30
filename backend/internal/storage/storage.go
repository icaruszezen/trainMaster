// Package storage manages the on-disk data directory (TM_DATA_DIR).
//
// Files such as images and model weights are stored as plain files in a
// fixed set of subdirectories, so they can be browsed, backed up and
// restored with ordinary tools. Only metadata goes into PostgreSQL.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Key names one of the managed subdirectories. The subdirectory name on
// disk equals the key.
type Key string

const (
	Images   Key = "images"   // original images, later split per dataset
	Datasets Key = "datasets" // dataset version snapshots and exported training sets (YOLO/VOC/COCO)
	Models   Key = "models"   // model weights
	Runs     Key = "runs"     // training/evaluation run artifacts (logs, curves, checkpoints)
	Exports  Key = "exports"  // packaged exports and reports
	Tmp      Key = "tmp"      // temporary files such as in-progress uploads
)

// Keys lists every managed subdirectory in display order.
var Keys = []Key{Images, Datasets, Models, Runs, Exports, Tmp}

// ErrInvalidPath is returned by Resolve for paths that are absolute or
// would escape the target directory.
var ErrInvalidPath = errors.New("storage: invalid relative path")

// Storage is an initialized data directory.
type Storage struct {
	root string
}

// DirStatus describes one managed subdirectory.
type DirStatus struct {
	Key      Key
	Path     string
	Writable bool
}

// New creates root and all managed subdirectories (if missing) and verifies
// that each of them is writable.
func New(root string) (*Storage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory %q: %w", root, err)
	}
	s := &Storage{root: abs}
	for _, k := range Keys {
		if err := os.MkdirAll(s.Dir(k), 0o755); err != nil {
			return nil, fmt.Errorf("create data directory: %w%s", err, permissionHint)
		}
	}
	if err := s.Check(); err != nil {
		return nil, err
	}
	return s, nil
}

const permissionHint = "（请检查挂载目录权限：Docker 部署时确认宿主机目录属主与 PUID/PGID 一致）"

// Root returns the absolute data root.
func (s *Storage) Root() string { return s.root }

// Dir returns the absolute path of a managed subdirectory.
func (s *Storage) Dir(k Key) string { return filepath.Join(s.root, string(k)) }

// Resolve joins a slash-separated relative path onto the directory for k.
// It rejects empty, absolute and parent-escaping paths ("../x"). The check
// is lexical: it does not follow symlinks, so code that opens user-chosen
// paths should additionally use os.OpenRoot(s.Dir(k)).
func (s *Storage) Resolve(k Key, rel string) (string, error) {
	if !slices.Contains(Keys, k) {
		return "", fmt.Errorf("storage: unknown directory key %q", k)
	}
	rel = filepath.FromSlash(rel)
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	return filepath.Join(s.Dir(k), rel), nil
}

// Check verifies that every managed subdirectory is writable.
func (s *Storage) Check() error {
	var errs []error
	for _, k := range Keys {
		if err := probeWritable(s.Dir(k)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w%s", errors.Join(errs...), permissionHint)
	}
	return nil
}

// Directories reports the path and writability of every managed
// subdirectory.
func (s *Storage) Directories() []DirStatus {
	out := make([]DirStatus, 0, len(Keys))
	for _, k := range Keys {
		out = append(out, DirStatus{Key: k, Path: s.Dir(k), Writable: probeWritable(s.Dir(k)) == nil})
	}
	return out
}

// Usage is the capacity of the filesystem that holds the data root.
type Usage struct {
	TotalBytes uint64
	// FreeBytes is the space available to this (unprivileged) process.
	FreeBytes uint64
	// UsedBytes is TotalBytes - FreeBytes, so it includes blocks reserved
	// for root.
	UsedBytes uint64
}

// Usage returns the capacity of the filesystem that holds the data root.
// It returns ErrUsageUnsupported on platforms without an implementation.
func (s *Storage) Usage() (Usage, error) { return diskUsage(s.root) }

// ErrUsageUnsupported is returned by Usage on platforms where disk usage is
// not implemented.
var ErrUsageUnsupported = errors.New("storage: disk usage not supported on this platform")

// probeWritable creates and removes a temporary file in dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("directory %s is not writable: %w", dir, err)
	}
	if err := errors.Join(f.Close(), os.Remove(f.Name())); err != nil {
		return fmt.Errorf("directory %s: clean up probe file: %w", dir, err)
	}
	return nil
}
