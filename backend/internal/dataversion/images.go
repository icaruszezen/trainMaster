package dataversion

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// The images of a ready version: list, detail, files and thumbnails. A
// version never changes once ready, so its files and thumbnails can be
// cached by browsers for good. A version that is not ready has no images
// yet (errNotReady); one in the recycle bin is not found.

// ErrUnsupportedImage: the image cannot be decoded (its thumbnail cannot
// be made).
var ErrUnsupportedImage = errors.New("unsupported image")

// Image list limits.
const (
	defaultImageLimit = 60
	maxImageLimit     = 200
)

// thumbnails generates and caches the thumbnails of version images. The
// zero value is ready to use.
type thumbnails struct {
	group singleflight.Group // one generation per cache file
	once  sync.Once
	slots chan struct{} // bounds the generations running at once (CPU)
}

func (t *thumbnails) acquire() {
	t.once.Do(func() { t.slots = make(chan struct{}, max(2, runtime.NumCPU())) })
	t.slots <- struct{}{}
}

func (t *thumbnails) release() { <-t.slots }

// readyVersion is what the image operations need of a ready version.
type readyVersion struct {
	id      int64
	dirName string
	sources []Source
}

// loadReady reads a version in use that must be ready.
func (s *Service) loadReady(ctx context.Context, id int64) (readyVersion, error) {
	rv := readyVersion{id: id}
	var status string
	err := s.db.QueryRow(ctx, "SELECT v.status, v.dir_name, v.sources"+versionFrom+" WHERE v.id = $1 AND "+versionLive,
		id).Scan(&status, &rv.dirName, &rv.sources)
	if errors.Is(err, pgx.ErrNoRows) {
		return rv, errVersionNotFound
	}
	if err != nil {
		return rv, err
	}
	if status != StatusReady {
		return rv, errNotReady
	}
	return rv, nil
}

// imageColumns are the columns scanImage reads, from data_version_images i.
const imageColumns = `i.id, i.position, i.split, i.split_by, i.path, i.width, i.height, i.size_bytes, i.object_count,
	i.labels, i.dataset_id, i.source_path, i.annotation_source, i.augmented_from, i.augment, i.stored`

func scanImage(row pgx.Row, rv readyVersion, extra ...any) (Image, error) {
	var img Image
	var labels map[string]int
	var annotationSource *int64
	err := row.Scan(append([]any{&img.ID, &img.Position, &img.Split, &img.SplitBy, &img.Path, &img.Width, &img.Height,
		&img.SizeBytes, &img.ObjectCount, &labels, &img.DatasetID, &img.SourcePath, &annotationSource,
		&img.AugmentedFrom, &img.Augment, &img.Stored}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return img, errImageNotFound
	}
	if err != nil {
		return img, err
	}
	img.FileName = path.Base(img.Path)
	img.Labels = labelCounts(labels)
	if img.Augment == nil {
		img.Augment = []string{}
	}
	// The names as when the version was created (the datasets and their
	// review versions may be gone since).
	for _, src := range rv.sources {
		if src.DatasetID != img.DatasetID {
			continue
		}
		img.DatasetName = src.DatasetName
		if annotationSource != nil {
			for _, v := range src.Versions {
				if v.ID == *annotationSource {
					img.AnnotationSource = &SourceVersion{ID: v.ID, Name: v.Name}
				}
			}
		}
	}
	if annotationSource != nil && img.AnnotationSource == nil {
		img.AnnotationSource = &SourceVersion{ID: *annotationSource}
	}
	base := fmt.Sprintf("/api/v1/data-versions/%d/images/%d", rv.id, img.ID)
	img.ThumbnailURL = base + "/thumbnail?size=" + dataset.ThumbSmall
	img.FileURL = base + "/file"
	return img, nil
}

// labelCounts orders label counts by count desc, then name.
func labelCounts(m map[string]int) []dataset.LabelCount {
	out := make([]dataset.LabelCount, 0, len(m))
	for name, n := range m {
		out = append(out, dataset.LabelCount{Name: name, Count: n})
	}
	slices.SortFunc(out, func(a, b dataset.LabelCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Name, b.Name))
	})
	return out
}

// likePattern matches s anywhere in a text with LIKE / ILIKE.
func likePattern(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
}

// ListImages returns a page of the images of a ready version and their
// total.
func (s *Service) ListImages(ctx context.Context, id int64, f ImageFilter) ([]Image, int, error) {
	if f.Limit == 0 {
		f.Limit = defaultImageLimit
	}
	if f.Limit < 1 || f.Limit > maxImageLimit {
		return nil, 0, newError(ErrBadRequest, "参数 limit 无效，取值范围为 1–%d", maxImageLimit)
	}
	if f.Offset < 0 {
		return nil, 0, newError(ErrBadRequest, "参数 offset 无效")
	}
	if f.Split != "" && !slices.Contains(Splits, f.Split) {
		return nil, 0, newError(ErrBadRequest, "参数 split 无效，可选值：%s", strings.Join(Splits, "、"))
	}
	rv, err := s.loadReady(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	where := " FROM data_version_images i WHERE i.version_id = $1"
	args := []any{id}
	add := func(cond string, arg any) {
		args = append(args, arg)
		where += " AND " + strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args)))
	}
	if f.Split != "" {
		add("i.split = $?", f.Split)
	}
	if f.Label != "" {
		add("i.labels ? $?", f.Label)
	}
	if f.DatasetID != 0 {
		add("i.dataset_id = $?", f.DatasetID)
	}
	if f.Augmented != nil {
		if *f.Augmented {
			where += " AND i.augmented_from IS NOT NULL"
		} else {
			where += " AND i.augmented_from IS NULL"
		}
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		add("(i.path ILIKE $? OR i.source_path ILIKE $?)", likePattern(q))
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Offset, f.Limit)
	rows, err := s.db.Query(ctx, "SELECT "+imageColumns+where+" ORDER BY i.position, i.id OFFSET $"+
		strconv.Itoa(len(args)-1)+" LIMIT $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Image, error) { return scanImage(row, rv) })
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []Image{}
	}
	return items, total, nil
}

// GetImage returns an image of a ready version with its objects.
func (s *Service) GetImage(ctx context.Context, id, imageID int64) (ImageDetail, error) {
	rv, err := s.loadReady(ctx, id)
	if err != nil {
		return ImageDetail{}, err
	}
	var d ImageDetail
	d.Image, err = scanImage(s.db.QueryRow(ctx, "SELECT "+imageColumns+", i.objects FROM data_version_images i"+
		" WHERE i.version_id = $1 AND i.id = $2", id, imageID), rv, &d.Objects)
	if err != nil {
		return ImageDetail{}, err
	}
	if d.Objects == nil {
		d.Objects = []dataset.AnnotationObject{}
	}
	return d, nil
}

// ImageFile is an opened image or thumbnail file.
type ImageFile struct {
	File        *os.File
	ContentType string
	Name        string
}

var errImageFileMissing = &Error{Kind: ErrNotFound, Message: "图片文件不存在（数据版本的文件夹可能已被移动或修改）"}

// storedImage is the stored file of an image.
type storedImage struct {
	path    string // in the version folder
	size    int64
	modTime time.Time
}

// queryStoredImage reads the stored file of an image of a ready version.
func (s *Service) queryStoredImage(ctx context.Context, id, imageID int64) (readyVersion, storedImage, error) {
	rv, err := s.loadReady(ctx, id)
	if err != nil {
		return rv, storedImage{}, err
	}
	var img storedImage
	err = s.db.QueryRow(ctx, "SELECT path, size_bytes, mod_time FROM data_version_images WHERE version_id = $1 AND id = $2",
		id, imageID).Scan(&img.path, &img.size, &img.modTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return rv, img, errImageNotFound
	}
	return rv, img, err
}

// openStored opens the stored file of an image in the version folder dir.
// A file whose size or time differ from those recorded when it was stored
// was rewritten in place (through a hard link, see migration 0013): it is
// served as it is, with a warning in the log.
func (s *Service) openStored(dir string, versionID int64, img storedImage) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errImageFileMissing
		}
		return nil, fmt.Errorf("open data version folder: %w", err)
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(img.path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errImageFileMissing
		}
		return nil, fmt.Errorf("open image: %w", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errImageFileMissing
	}
	if storedChanged(fi, img) {
		s.log.Warn("data version: image file changed since the version was built (rewritten in place?)",
			"data_version_id", versionID, "path", img.path, "size", fi.Size(), "recorded_size", img.size,
			"mod_time", fi.ModTime(), "recorded_mod_time", img.modTime)
	}
	return f, nil
}

// storedChanged reports whether a stored file differs from what was
// recorded (times in microseconds, as the database keeps them).
func storedChanged(fi fs.FileInfo, img storedImage) bool {
	return fi.Size() != img.size || fi.ModTime().UnixMicro() != img.modTime.UnixMicro()
}

// OpenImage opens the file of an image of a ready version.
func (s *Service) OpenImage(ctx context.Context, id, imageID int64) (*ImageFile, error) {
	rv, img, err := s.queryStoredImage(ctx, id, imageID)
	if err != nil {
		return nil, err
	}
	f, err := s.openStored(s.versionDir(rv.dirName), id, img)
	if err != nil {
		return nil, err
	}
	return &ImageFile{File: f, ContentType: dataset.ImageContentType(img.path), Name: path.Base(img.path)}, nil
}

// OpenThumbnail opens the thumbnail of an image ("sm" or "lg"), generating
// it when it is not cached yet.
func (s *Service) OpenThumbnail(ctx context.Context, id, imageID int64, size string) (*ImageFile, error) {
	side, ok := dataset.ThumbnailSide(size)
	if !ok {
		return nil, newError(ErrBadRequest, "size 参数只能是 %s 或 %s", dataset.ThumbSmall, dataset.ThumbLarge)
	}
	rv, img, err := s.queryStoredImage(ctx, id, imageID)
	if err != nil {
		return nil, err
	}
	// The version never changes: one cache file per image and size.
	cachePath := filepath.Join(s.thumbDir(id), strconv.FormatInt(imageID, 10)+"-"+size+".jpg")
	name := strings.TrimSuffix(path.Base(img.path), path.Ext(img.path)) + ".jpg"
	if f, err := os.Open(cachePath); err == nil {
		return &ImageFile{File: f, ContentType: "image/jpeg", Name: name}, nil
	}
	// The generation is shared by concurrent requests and continues when
	// this one gives up, so the result lands in the cache either way.
	ch := s.thumbs.group.DoChan(cachePath, func() (any, error) {
		return nil, s.generateThumbnail(s.versionDir(rv.dirName), id, img, side, cachePath)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, fmt.Errorf("open thumbnail: %w", err)
	}
	return &ImageFile{File: f, ContentType: "image/jpeg", Name: name}, nil
}

// generateThumbnail renders the thumbnail of a stored image into dst, with
// the memory budget shared by all image decoding of the process.
func (s *Service) generateThumbnail(dir string, versionID int64, img storedImage, side int, dst string) error {
	s.thumbs.acquire()
	defer s.thumbs.release()
	f, err := s.openStored(dir, versionID, img)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create thumbnail folder: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".thumb-*")
	if err != nil {
		return fmt.Errorf("create thumbnail: %w", err)
	}
	base := s.base
	err = dataset.WriteThumbnail(tmp, f, side, func(cost int64) (func(), error) {
		return dataset.ReserveImageMemory(base, cost)
	})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		var de *dataset.Error
		if errors.As(err, &de) && errors.Is(de, dataset.ErrUnsupportedImage) {
			return &Error{Kind: ErrUnsupportedImage, Message: de.Message}
		}
		return fmt.Errorf("write thumbnail: %w", err)
	}
	return nil
}
