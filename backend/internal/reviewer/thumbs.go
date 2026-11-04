package reviewer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Thumbnails: the image list shows small ones (sm, 320 px), generated
// with the platform's code and kept in the user's cache folder
// (thumbs/<package id>/<image id>-sm-<file version>.jpg), so a package
// opened again shows them at once. The canvas asks for large ones (lg)
// while the original loads: the original itself for the formats browsers
// show (it is on the local disk anyway), a 1600 px JPEG for the others
// (TIFF), cached the same way.

// thumbCacheAge is how long the thumbnails of a package not opened again
// are kept.
const thumbCacheAge = 30 * 24 * time.Hour

// thumbMemory bounds the memory of all thumbnail generations running at
// the same time (as estimated by dataset.WriteThumbnail).
const thumbMemory = 1 << 30

// browserNative are the image formats the canvas shows directly (as in
// frontend/src/features/datasets/files.ts).
var browserNative = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true, ".bmp": true,
}

type thumbnailer struct {
	dir    string // "" disables the disk cache (thumbnails are generated in a temporary folder)
	slots  chan struct{}
	memory *semaphore.Weighted
	group  singleflight.Group
}

func newThumbnailer(cacheDir string) *thumbnailer {
	t := &thumbnailer{slots: make(chan struct{}, max(2, runtime.NumCPU()/2)), memory: semaphore.NewWeighted(thumbMemory)}
	if cacheDir != "" {
		t.dir = filepath.Join(cacheDir, "thumbs")
	}
	return t
}

// packageDir returns the cache folder of a package (its ID is hex).
func (t *thumbnailer) packageDir(packageID string) string {
	if t.dir == "" {
		return filepath.Join(os.TempDir(), "trainmaster-reviewer-thumbs", packageID)
	}
	return filepath.Join(t.dir, packageID)
}

// touch marks the cache of a package as used (cleanup keeps it).
func (t *thumbnailer) touch(packageID string) {
	now := time.Now()
	_ = os.Chtimes(t.packageDir(packageID), now, now)
}

// cleanup removes the caches of packages not opened for thumbCacheAge.
func (t *thumbnailer) cleanup() {
	if t.dir == "" {
		return
	}
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && e.IsDir() && time.Since(fi.ModTime()) > thumbCacheAge {
			_ = os.RemoveAll(filepath.Join(t.dir, e.Name()))
		}
	}
}

// open returns the thumbnail of an image of size sm or lg, generating and
// caching it on first use. lg of a browser-native image is the image
// itself.
func (t *thumbnailer) open(ctx context.Context, p *Package, id int64, size string) (*imageFile, error) {
	side, ok := dataset.ThumbnailSide(size)
	if !ok {
		return nil, &Error{Kind: ErrBadRequest, Message: "size 参数只能是 sm 或 lg"}
	}
	src, err := p.OpenImage(id)
	if err != nil {
		return nil, err
	}
	if size == dataset.ThumbLarge && browserNative[strings.ToLower(filepath.Ext(src.name))] {
		return src, nil
	}
	defer src.file.Close()

	prefix := fmt.Sprintf("%d-%s-", id, size)
	cachePath := filepath.Join(t.packageDir(p.manifest.PackageID), prefix+src.version+".jpg")
	if f, err := openThumb(cachePath, src); err == nil {
		return f, nil
	}
	// Shared by concurrent requests, and finished when this one gives up,
	// so the result lands in the cache either way.
	ch := t.group.DoChan(cachePath, func() (any, error) {
		err := t.generate(p, id, side, cachePath)
		if err == nil {
			removeOtherVersions(filepath.Dir(cachePath), prefix, filepath.Base(cachePath))
		}
		return nil, err
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return openThumb(cachePath, src)
}

// generate renders the thumbnail of an image into dst.
func (t *thumbnailer) generate(p *Package, id int64, side int, dst string) error {
	t.slots <- struct{}{}
	defer func() { <-t.slots }()
	src, err := p.OpenImage(id)
	if err != nil {
		return err
	}
	defer src.file.Close()
	f := src.file
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return newError(ErrIO, "无法创建缩略图文件夹：%v", err)
	}
	return writeFileAtomic(dst, func(out *os.File) error {
		return dataset.WriteThumbnail(out, f, side, func(cost int64) (func(), error) {
			if cost > thumbMemory {
				cost = thumbMemory
			}
			if err := t.memory.Acquire(context.Background(), cost); err != nil {
				return nil, err
			}
			return func() { t.memory.Release(cost) }, nil
		})
	})
}

func openThumb(p string, src *imageFile) (*imageFile, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	name := strings.TrimSuffix(src.name, filepath.Ext(src.name)) + ".jpg"
	return &imageFile{file: f, name: name, contentType: "image/jpeg", modTime: fi.ModTime(), version: src.version}, nil
}

// removeOtherVersions deletes cached thumbnails that start with prefix
// except keep.
func removeOtherVersions(dir, prefix, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); n != keep && strings.HasPrefix(n, prefix) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}
