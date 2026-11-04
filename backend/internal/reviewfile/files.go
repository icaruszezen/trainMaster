package reviewfile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Files of a review folder (on the platform) or of an opened package (in
// the local reviewer), accessed through an os.Root of that folder so that
// a symbolic link inside cannot lead out of it. Paths are slash-separated
// and relative to the root; callers pass paths that ValidRelPath accepts.
// Files are replaced atomically: the new content goes to a hidden
// temporary file next to the target first, which is then renamed over it,
// so readers and crashes never see half a file.

// ErrNotFolder is returned when a path that should be a folder is a file
// or a symbolic link.
var ErrNotFolder = errors.New("reviewfile: not a regular folder")

// TempPrefix starts the names of the temporary files written here (they
// are hidden, and never part of a package).
const TempPrefix = ".tm-review-"

// IsTempName reports whether a file name is one of these temporary files.
func IsTempName(name string) bool { return strings.HasPrefix(name, TempPrefix) }

// EnsureDirs creates the folders of the slash-separated folder path dir
// inside root, as needed. A file or symbolic link in the way fails with
// ErrNotFolder.
func EnsureDirs(root *os.Root, dir string) error {
	if dir == "." || dir == "" {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(dir, "/") {
		cur = path.Join(cur, seg)
		name := filepath.FromSlash(cur)
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			fi, err = root.Lstat(name)
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() { // symbolic links included: Lstat does not follow them
			return fmt.Errorf("%w: %s", ErrNotFolder, cur)
		}
	}
	return nil
}

// ReadAnnotation reads the VOC file rel: at most one byte more than
// dataset.MaxAnnotationBytes, so that an oversized file fails to parse and
// gets a version of its own. A missing file is an fs.ErrNotExist error.
func ReadAnnotation(root *os.Root, rel string) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", rel)
	}
	return io.ReadAll(io.LimitReader(f, dataset.MaxAnnotationBytes+1))
}

// StageFile writes data to a new hidden temporary file in the folder of
// rel (creating the folder if needed) and returns the file's path; Rename
// then puts it in place.
func StageFile(root *os.Root, rel string, data []byte) (string, error) {
	dir := path.Dir(rel)
	if err := EnsureDirs(root, dir); err != nil {
		return "", err
	}
	tmp := path.Join(dir, TempPrefix+strings.ToLower(rand.Text()[:12])+".tmp")
	f, err := root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(filepath.FromSlash(tmp))
		return "", err
	}
	return tmp, nil
}

// Rename renames the file from to the name to (both inside root),
// replacing it.
func Rename(root *os.Root, from, to string) error {
	// os.Root has no Rename in Go 1.24. Both names are inside root and
	// their folders were checked by EnsureDirs; rename never follows a
	// symbolic link at the target, it replaces the link itself.
	return os.Rename(filepath.Join(root.Name(), filepath.FromSlash(from)), filepath.Join(root.Name(), filepath.FromSlash(to)))
}

// WriteFile replaces the file rel with data atomically.
func WriteFile(root *os.Root, rel string, data []byte) error {
	tmp, err := StageFile(root, rel, data)
	if err != nil {
		return err
	}
	if err := Rename(root, tmp, rel); err != nil {
		_ = root.Remove(filepath.FromSlash(tmp))
		return err
	}
	return nil
}
