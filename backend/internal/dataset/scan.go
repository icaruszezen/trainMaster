package dataset

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

// diskFile is an image or annotation file found on disk.
type diskFile struct {
	Size    int64
	ModTime time.Time // truncated to microseconds, like the database
}

// fileSet holds the images and annotation candidates below a folder,
// keyed by slash-separated relative path.
type fileSet struct {
	Images       map[string]diskFile
	XMLs         map[string]diskFile
	Ignored      int // junk and unrecognized files
	NamesInvalid int // images, XML files and folders skipped because their name is not valid UTF-8
}

func (set *fileSet) imagePaths() []string { return slices.Sorted(maps.Keys(set.Images)) }
func (set *fileSet) xmlPaths() []string   { return slices.Sorted(maps.Keys(set.XMLs)) }

// walkFiles lists the images and XML files below root. Symbolic links are
// never followed (nor listed), junk entries (§4.3) are skipped, and
// unreadable sub-folders are logged and skipped. Names that are not valid
// UTF-8 (e.g. GBK names from an archive unpacked by a tool that does not
// convert them) are logged and skipped too: the database stores paths as
// UTF-8 text, and the client could not send such a name back. tick is
// called for every visited entry so long walks can report progress and
// stop when ctx ends.
func walkFiles(ctx context.Context, log *slog.Logger, root string, tick func()) (*fileSet, error) {
	set := &fileSet{Images: map[string]diskFile{}, XMLs: map[string]diskFile{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			log.Warn("skipping unreadable path in dataset folder", "path", p, "err", err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if tick != nil {
			tick()
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if isJunkSegment(d.Name()) {
				return filepath.SkipDir
			}
			if !utf8.ValidString(d.Name()) {
				log.Warn("skipping folder with a name that is not valid UTF-8", "path", strconv.Quote(p))
				set.NamesInvalid++
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		kind := classify(rel)
		if kind == kindIgnored {
			set.Ignored++
			return nil
		}
		if !utf8.ValidString(rel) {
			log.Warn("skipping file with a name that is not valid UTF-8", "path", strconv.Quote(p))
			set.NamesInvalid++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		f := diskFile{Size: info.Size(), ModTime: dbTime(info.ModTime())}
		if kind == kindImage {
			set.Images[rel] = f
		} else {
			set.XMLs[rel] = f
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// annotationCache parses each annotation file at most once per job.
type annotationCache struct {
	root   *os.Root
	parsed map[string]annotationResult
}

type annotationResult struct {
	a   *vocAnnotation
	err error
}

func newAnnotationCache(root *os.Root) *annotationCache {
	return &annotationCache{root: root, parsed: map[string]annotationResult{}}
}

func (c *annotationCache) get(rel string) (*vocAnnotation, error) {
	if r, ok := c.parsed[rel]; ok {
		return r.a, r.err
	}
	a, err := c.parse(rel)
	c.parsed[rel] = annotationResult{a, err}
	return a, err
}

func (c *annotationCache) parse(rel string) (*vocAnnotation, error) {
	f, err := c.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseVOC(f)
}

// filename returns the <filename> of an annotation, for pairAnnotations.
func (c *annotationCache) filename(rel string) string {
	if a, err := c.get(rel); err == nil {
		return a.Filename
	}
	return ""
}

// scanResult counts what a scan found and changed.
type scanResult struct {
	Total, Added, Updated, Removed, Unchanged int
	Matched, Invalid, Orphaned, Ignored       int
	NamesInvalid                              int
}

func (r scanResult) summary() map[string]int64 {
	return map[string]int64{
		sumImagesTotal:        int64(r.Total),
		sumImagesAdded:        int64(r.Added),
		sumImagesUpdated:      int64(r.Updated),
		sumImagesRemoved:      int64(r.Removed),
		sumImagesUnchanged:    int64(r.Unchanged),
		sumAnnotationsMatched: int64(r.Matched),
		sumAnnotationsInvalid: int64(r.Invalid),
		sumAnnotationsOrphan:  int64(r.Orphaned),
		sumFilesIgnored:       int64(r.Ignored),
		sumNamesInvalid:       int64(r.NamesInvalid),
	}
}

// errDatasetDirMissing builds the error for a dataset folder that is gone
// (or replaced by something that is not a plain folder).
func errDatasetDirMissing(dir string) error {
	return newError(ErrConflict, "数据集文件夹不存在：%s", dir)
}

// openDatasetRoot opens the folder of d for reading and writing. The
// folder itself must be a real directory, not a symbolic link.
func (s *Service) openDatasetRoot(d Dataset) (*os.Root, error) {
	dir := s.datasetDir(d.DirName)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, errDatasetDirMissing(dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open dataset folder: %w", err)
	}
	return root, nil
}

// syncIndex makes dataset_images match the files in the dataset folder
// (spec §5.3): new and changed images are (re)indexed, unchanged ones are
// left alone, and rows of vanished files are deleted. It reports progress
// under the stage "scanning".
func (s *Service) syncIndex(ctx context.Context, d Dataset, p *progressTracker) (scanResult, error) {
	var res scanResult
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return res, err
	}
	defer root.Close()

	// While walking, the total is unknown (0) and done counts entries.
	p.setStage(ctx, stageScanning, unitFiles, 0)
	var walked int64
	set, err := walkFiles(ctx, s.log, root.Name(), func() {
		walked++
		p.set(ctx, walked, 0)
	})
	if err != nil {
		return res, fmt.Errorf("walk dataset folder: %w", err)
	}
	res.Total, res.Ignored, res.NamesInvalid = len(set.Images), set.Ignored, set.NamesInvalid

	existing, err := queryIndex(ctx, s.db, d.ID)
	if err != nil {
		return res, fmt.Errorf("load index: %w", err)
	}

	cache := newAnnotationCache(root)
	images := set.imagePaths()
	pairs := pairAnnotations(images, set.xmlPaths(), cache.filename)
	res.Orphaned = len(set.XMLs) - len(pairs)

	p.setStage(ctx, stageScanning, unitFiles, int64(len(images)))
	batch := make([]imageRecord, 0, upsertBatchSize)
	flush := func() error {
		if err := upsertImages(ctx, s.db, d.ID, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for i, rel := range images {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		img := set.Images[rel]
		xmlRel := pairs[rel]
		var xml *diskFile
		if xmlRel != "" {
			x := set.XMLs[xmlRel]
			xml = &x
		}
		old, known := existing[rel]
		if known && unchanged(old, img, xmlRel, xml) {
			res.Unchanged++
			switch old.State {
			case annotationAnnotated, annotationEmpty:
				res.Matched++
			case annotationInvalid:
				res.Invalid++
			}
		} else {
			rec := s.indexImage(root, cache, rel, img, xmlRel, xml)
			switch rec.State {
			case annotationAnnotated, annotationEmpty:
				res.Matched++
			case annotationInvalid:
				res.Invalid++
			}
			if known {
				res.Updated++
			} else {
				res.Added++
			}
			batch = append(batch, rec)
			if len(batch) == upsertBatchSize {
				if err := flush(); err != nil {
					return res, err
				}
			}
		}
		p.set(ctx, int64(i+1), int64(len(images)))
	}
	if err := flush(); err != nil {
		return res, err
	}

	var gone []int64
	for rel, old := range existing {
		if _, ok := set.Images[rel]; !ok {
			gone = append(gone, old.ID)
		}
	}
	if err := deleteImages(ctx, s.db, d.ID, gone); err != nil {
		return res, err
	}
	res.Removed = len(gone)
	s.removeThumbnails(d.ID, gone)
	return res, nil
}

// unchanged reports whether an indexed row still describes the files on
// disk, so the image and its annotation need not be parsed again.
func unchanged(old indexedImage, img diskFile, xmlRel string, xml *diskFile) bool {
	if old.Size != img.Size || !old.ModTime.Equal(img.ModTime) {
		return false
	}
	if xml == nil {
		return old.AnnPath == nil
	}
	return old.AnnPath != nil && *old.AnnPath == xmlRel &&
		old.AnnModTime != nil && old.AnnModTime.Equal(xml.ModTime) &&
		old.AnnSize != nil && *old.AnnSize == xml.Size
}

// indexImage reads the dimensions of an image and parses its annotation.
func (s *Service) indexImage(root *os.Root, cache *annotationCache, rel string, img diskFile, xmlRel string, xml *diskFile) imageRecord {
	rec := imageRecord{RelPath: rel, Size: img.Size, ModTime: img.ModTime, State: annotationNone}
	if f, err := root.Open(filepath.FromSlash(rel)); err == nil {
		rec.Width, rec.Height = imageDimensions(f)
		f.Close()
	}
	if xml == nil {
		return rec
	}
	rec.AnnPath, rec.AnnModTime, rec.AnnSize = &xmlRel, &xml.ModTime, &xml.Size
	a, err := cache.get(xmlRel)
	if err != nil {
		rec.State = annotationInvalid
		return rec
	}
	rec.ObjectCount = len(a.Objects)
	rec.Labels = a.labelCounts()
	rec.State = annotationEmpty
	if rec.ObjectCount > 0 {
		rec.State = annotationAnnotated
	}
	if rec.Width == nil && a.Width > 0 && a.Height > 0 {
		w, h := a.Width, a.Height
		rec.Width, rec.Height = &w, &h
	}
	return rec
}
