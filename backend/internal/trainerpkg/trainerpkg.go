// Package trainerpkg provides the downloads that install the training node
// on a Windows computer: the install script and the package it installs
// with pip. Both come from the trainer source directory
// TM_TRAINER_PACKAGE_DIR, laid out like the repository's trainer/
// directory (the Docker image bundles the files needed as
// /app/tools/trainer).
//
// The directory is looked at again on every request, so files put in place
// or edited after the start (e.g. during development, where it is the
// repository's trainer/) are picked up. Only regular files are served;
// symbolic links are not followed.
package trainerpkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable is returned when no directory is configured or a file
// that is needed is not there.
var ErrUnavailable = errors.New("training node package not available")

var errNotConfigured = fmt.Errorf("%w: no directory configured", ErrUnavailable)

// Paths in the source directory, which are also the paths in the archive.
const (
	scriptPath       = "scripts/install.ps1"
	pyprojectPath    = "pyproject.toml"
	requirementsPath = "requirements.txt"
	moduleDir        = "trainmaster_trainer"
	moduleInit       = moduleDir + "/__init__.py"
	torchVariants    = "torch_variants.txt" // the variant table in moduleDir, read at run time
)

// utf8BOM starts the install script in the repository, so that Windows
// PowerShell 5.1 reads it as UTF-8 when it is run with -File.
var utf8BOM = []byte("\xef\xbb\xbf")

// File is a download. Its Data is shared: callers must not change it.
type File struct {
	Data    []byte
	ModTime time.Time
	// ETag is a strong entity tag (quoted), or "" when the file has none.
	ETag string
}

// Source provides the downloads of one trainer source directory. It is
// safe for concurrent use.
type Source struct {
	dir string

	mu sync.Mutex
	// archive is the last archive built; fingerprint identifies the files
	// it was built from.
	archive     *File
	fingerprint string
}

// New returns the Source for dir; "" means none is configured.
func New(dir string) *Source {
	return &Source{dir: dir}
}

// Check reports whether both the install script and the package can be
// downloaded. The error says what is missing.
func (s *Source) Check() error {
	if _, err := s.stat(scriptPath); err != nil {
		return err
	}
	_, err := s.scan()
	return err
}

// InstallScript returns scripts/install.ps1 without its UTF-8 BOM. The
// install command downloads it as text (decoded by the charset of the
// response) and runs it as a script block, where the BOM would only be a
// stray first character.
func (s *Source) InstallScript() (*File, error) {
	fi, err := s.stat(scriptPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(scriptPath))
	if err != nil {
		return nil, err
	}
	return &File{Data: bytes.TrimPrefix(data, utf8BOM), ModTime: fi.ModTime()}, nil
}

// Archive returns the package: a zip, without a top folder, of the files
// the install script installs with pip (pyproject.toml, requirements.txt
// and the sources of the module trainmaster_trainer) and of the script
// itself, all as they are in the directory. It is built again only when a
// file is added or removed or its size or modification time changes.
// ModTime is that of the newest file, ETag a hash of what identifies the
// files.
func (s *Source) Archive() (*File, error) {
	files, err := s.scan()
	if err != nil {
		return nil, err
	}
	fp := fingerprint(files)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archive != nil && s.fingerprint == fp {
		return s.archive, nil
	}
	data, err := s.build(files)
	if err != nil {
		return nil, err
	}
	modTime := files[0].modTime
	for _, f := range files[1:] {
		if f.modTime.After(modTime) {
			modTime = f.modTime
		}
	}
	s.archive, s.fingerprint = &File{Data: data, ModTime: modTime, ETag: `"` + fp + `"`}, fp
	return s.archive, nil
}

// entry is a file of the package.
type entry struct {
	name    string // path in the directory and in the archive, with "/"
	size    int64
	modTime time.Time
}

// scan lists the files of the package, sorted by name. pyproject.toml,
// requirements.txt and trainmaster_trainer/__init__.py are required; the
// install script is included when it is there.
func (s *Source) scan() ([]entry, error) {
	var files []entry
	add := func(name string, required bool) error {
		fi, err := s.stat(name)
		if err != nil {
			if !required && errors.Is(err, ErrUnavailable) {
				return nil
			}
			return err
		}
		files = append(files, entry{name: name, size: fi.Size(), modTime: fi.ModTime()})
		return nil
	}
	if err := add(pyprojectPath, true); err != nil {
		return nil, err
	}
	if err := add(requirementsPath, true); err != nil {
		return nil, err
	}
	if err := add(moduleInit, true); err != nil {
		return nil, err
	}
	// The other files of the module: only its own level, so neither
	// __pycache__ nor anything else in subfolders.
	des, err := os.ReadDir(s.path(moduleDir))
	if err != nil {
		return nil, err
	}
	for _, de := range des {
		if name := de.Name(); moduleFile(name) && name != "__init__.py" {
			if err := add(moduleDir+"/"+name, false); err != nil {
				return nil, err
			}
		}
	}
	if err := add(scriptPath, false); err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	return files, nil
}

// moduleFile reports whether a file of trainmaster_trainer/ belongs in the
// package: the sources and the variant table. Like the shell glob *.py,
// the pattern skips hidden files (editor backups and the like).
func moduleFile(name string) bool {
	return !strings.HasPrefix(name, ".") && (strings.HasSuffix(name, ".py") || name == torchVariants)
}

// fingerprint identifies files by path, size and modification time.
func fingerprint(files []entry) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", f.name, f.size, f.modTime.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// build writes the archive of files.
func (s *Source) build(files []entry) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		data, err := os.ReadFile(s.path(f.name))
		if err != nil {
			return nil, err
		}
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: f.modTime}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stat returns the file information of name, which must be a regular file
// (a symbolic link is not followed). A file that is missing or of another
// kind is ErrUnavailable; other errors, such as a permission problem, are
// returned as they are.
func (s *Source) stat(name string) (fs.FileInfo, error) {
	if s.dir == "" {
		return nil, errNotConfigured
	}
	fi, err := os.Lstat(s.path(name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s is missing", ErrUnavailable, name)
	case err != nil:
		return nil, err
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnavailable, name)
	}
	return fi, nil
}

// path is the path of name (with "/") in the directory.
func (s *Source) path(name string) string {
	return filepath.Join(s.dir, filepath.FromSlash(name))
}
