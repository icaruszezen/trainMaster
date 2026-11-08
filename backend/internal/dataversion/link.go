package dataversion

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/augment"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Storing the originals: a hard link to the dataset file when possible
// (no extra space; imports replace dataset files by rename, so the link
// keeps the content it was made with), else a copy. A JPEG or TIFF whose
// orientation tag is not 1 is always copied, with the tag reset: its
// boxes are in the stored pixel grid, but training tools apply the tag.

// Free space guard of the files a build writes (copies and augmented
// images): the free space is read before the first one and again every
// spaceRecheck bytes; spaceReserve is always left.
const (
	spaceReserve = 1 << 30
	spaceRecheck = 256 << 20
)

// spaceGuard stops a build before it fills the disk.
type spaceGuard struct {
	usage       func() (storage.Usage, error)
	checked     bool
	unsupported bool
	since       int64 // bytes written since the last check
	warn        func(err error)
}

// before checks that need more bytes fit, when a check is due.
func (g *spaceGuard) before(need int64) error {
	if g.unsupported || (g.checked && g.since < spaceRecheck) {
		return nil
	}
	u, err := g.usage()
	if errors.Is(err, storage.ErrUsageUnsupported) {
		g.unsupported = true
		return nil
	}
	if err != nil {
		if g.warn != nil {
			g.warn(err)
		}
		return nil
	}
	g.checked, g.since = true, 0
	if want := uint64(max(need, 0)) + spaceReserve; u.FreeBytes < want {
		return newError(ErrInsufficientStorage, "磁盘空间不足：生成数据版本还需要约 %s，可用 %s",
			formatSize(want), formatSize(u.FreeBytes))
	}
	return nil
}

// force makes the next before check the free space.
func (g *spaceGuard) force() { g.checked = false }

func (g *spaceGuard) wrote(n int64) { g.since += n }

// formatSize renders a byte count for messages, e.g. "1.5 GB".
func formatSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, i := float64(n)/unit, 0
	for value >= unit && i < 4 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"KB", "MB", "GB", "TB", "PB"}[i])
}

// errSourceChanged and errSourceMissing: the dataset file of an original
// changed or disappeared after it was prepared.
var (
	errSourceChanged = errors.New("dataset file changed")
	errSourceMissing = errors.New("dataset file missing")
)

// sameFileTime reports whether a file's modification time is t as the
// dataset index stores it (microseconds).
func sameFileTime(mod, t time.Time) bool { return time.UnixMicro(mod.UnixMicro()).Equal(t) }

// dbTime is t as PostgreSQL stores it (microseconds, UTC).
func dbTime(t time.Time) time.Time { return time.UnixMicro(t.UnixMicro()).UTC() }

// hasOrientationTag reports whether files with the extension of rel can
// carry an EXIF / TIFF orientation tag.
func hasOrientationTag(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".tif", ".tiff":
		return true
	}
	return false
}

// storedFile is how an original was stored.
type storedFile struct {
	stored           string // StoredLink or StoredCopy
	orientationFixed bool
	copied           int64 // bytes copied
	size             int64 // the stored file
	modTime          time.Time
}

// storeOriginal stores the original img at its version path: opened
// through the dataset's root, checked against what was prepared, then
// linked (verified to be the same file) or copied. errSourceChanged or
// errSourceMissing: the file is no longer what was prepared.
func (b *build) storeOriginal(img *buildImage) (storedFile, error) {
	var out storedFile
	root := b.snaps[img.source].Root
	src, err := root.Open(filepath.FromSlash(img.rel))
	if err != nil {
		return out, errSourceMissing
	}
	defer src.Close()
	fi, err := src.Stat()
	switch {
	case err != nil || !fi.Mode().IsRegular():
		return out, errSourceMissing
	case fi.Size() != img.sourceSize || !sameFileTime(fi.ModTime(), img.sourceMod):
		return out, errSourceChanged
	}
	dst := filepath.Join(b.dir, filepath.FromSlash(img.path))
	if err := b.mkdirFor(dst); err != nil {
		return out, err
	}

	var orientation *augment.Orientation
	if hasOrientationTag(img.rel) {
		o, err := b.s.orientation(src, fi.Size())
		if err != nil {
			return out, fmt.Errorf("read the orientation of %s: %w", img.rel, err)
		}
		if o.Value > 1 {
			orientation = &o
		}
	}
	if orientation == nil {
		linkErr := b.s.link(filepath.Join(root.Name(), filepath.FromSlash(img.rel)), dst)
		if linkErr == nil {
			if li, err := os.Lstat(dst); err == nil && os.SameFile(fi, li) {
				out.stored, out.size, out.modTime = StoredLink, li.Size(), li.ModTime()
				return out, nil
			}
			linkErr = errors.New("the link is not the dataset file")
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return out, fmt.Errorf("remove failed link: %w", err)
		}
		b.linkFailed(img, linkErr)
	}

	need := fi.Size()
	if b.crossDevice[img.source] {
		need = b.remainingBytes(img)
	}
	if err := b.space.before(need); err != nil {
		return out, err
	}
	n, err := copyFileFrom(src, fi.Size(), dst, orientation)
	if err != nil {
		return out, err
	}
	b.space.wrote(n)
	st, err := os.Lstat(dst)
	if err != nil {
		return out, err
	}
	out = storedFile{stored: StoredCopy, orientationFixed: orientation != nil, copied: n, size: st.Size(),
		modTime: st.ModTime()}
	return out, nil
}

// linkFailed records why a link failed (logged once per source).
func (b *build) linkFailed(img *buildImage, err error) {
	if errors.Is(err, syscall.EXDEV) {
		b.crossDevice[img.source] = true
	}
	if !b.linkWarned[img.source] {
		b.linkWarned[img.source] = true
		b.log.Info("data version: cannot hard link the dataset files, copying them", "dataset_id",
			b.job.Sources[img.source].DatasetID, "path", img.rel, "err", err)
	}
}

// remainingBytes is the size of the originals of img's source from img on
// (a dataset on another file system: all of them are copied).
func (b *build) remainingBytes(img *buildImage) int64 {
	var n int64
	for _, o := range b.images[img.index:] {
		if o.source == img.source {
			n += o.sourceSize
		}
	}
	return n
}

// copyFileFrom copies size bytes of src to a temporary file next to dst,
// resets the orientation tag in it (when o is set) and renames it to dst.
func copyFileFrom(src *os.File, size int64, dst string, o *augment.Orientation) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return 0, fmt.Errorf("copy image: %w", err)
	}
	name := tmp.Name()
	// CreateTemp makes the file 0600; the version folder is read by other
	// users too (training nodes over a shared mount, backups).
	err = tmp.Chmod(0o644)
	var n int64
	if err == nil {
		n, err = io.Copy(tmp, io.NewSectionReader(src, 0, size))
	}
	if err == nil && n != size {
		err = errSourceChanged
	}
	if err == nil && o != nil {
		err = augment.NeutralizeOrientation(tmp, *o)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, dst)
	}
	if err != nil {
		_ = os.Remove(name)
		if errors.Is(err, errSourceChanged) {
			return n, err
		}
		return n, fmt.Errorf("copy image: %w", err)
	}
	return n, nil
}

// mkdirFor creates the folder of the file at p (once per folder).
func (b *build) mkdirFor(p string) error {
	dir := filepath.Dir(p)
	if b.dirs[dir] {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create folder in the data version: %w", err)
	}
	b.dirs[dir] = true
	return nil
}
