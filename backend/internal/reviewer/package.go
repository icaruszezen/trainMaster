package reviewer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// maxManifestBytes bounds review.json (as the platform does for results).
const maxManifestBytes = 256 << 20

// Package is an opened review package: the folder with review.json, the
// images and their VOC files, and the review state of every image. The
// VOC files are the annotations; the statuses that differ from the
// package are kept in review-progress.json. All methods are safe for
// concurrent use.
type Package struct {
	dir      string // absolute
	zipPath  string // the zip the folder was extracted from, "" when opened as a folder
	resumed  bool   // extracted before, or with progress from an earlier session
	openedAt time.Time
	now      func() time.Time
	manifest *reviewfile.Manifest
	datasets map[int64]reviewfile.ManifestDataset

	mu         sync.RWMutex
	root       *os.Root // nil once closed
	lock       *os.File // the locked lock file of the folder (nil without one), see lockPackage
	images     []*image // in review order
	byID       map[int64]*image
	lastExport *ExportInfo
}

// image is the state of one image of a package.
type image struct {
	m           *reviewfile.ManifestImage
	position    int
	fileName    string
	lowerPath   string // of the path in the dataset, for searches
	datasetName string

	status     string
	reviewedAt *time.Time

	width, height *int // from the manifest, or read from the file when it gives none
	sizeChecked   bool // the file was asked for its size

	imageVersion string // of the image file in the folder, "" when missing

	// The VOC file as last read.
	annotationVersion string // "" without a file
	objectCount       int
	labels            []dataset.LabelCount // by count desc, then name
	edited            bool
	annotationError   string // why the file cannot be used, "" if fine
}

// ExportInfo describes the results last exported from a package.
type ExportInfo struct {
	Path       string    `json:"path"`
	FileName   string    `json:"file_name"`
	SizeBytes  int64     `json:"size_bytes"`
	ExportedAt time.Time `json:"exported_at"`
}

// annotationFile is a VOC file as read now.
type annotationFile struct {
	data    []byte // nil: no file
	version string // "" without a file
	file    dataset.VOCFile
	err     string // why the file cannot be used, "" if fine
}

// readAnnotationIn reads the VOC file rel of a package. A missing file is
// no error: the image has no annotation file. A file that cannot be used
// is reported in err, not as an error (as on the platform).
func readAnnotationIn(root *os.Root, rel string) annotationFile {
	a := annotationFile{file: dataset.VOCFile{Objects: []dataset.AnnotationObject{}}}
	data, err := reviewfile.ReadAnnotation(root, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return a
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			a.err = dataset.AnnotationErrorMessage(err)
		} else {
			a.err = "标注文件无法读取：" + err.Error()
		}
		return a
	}
	a.data, a.version = data, reviewfile.AnnotationVersion(data)
	f, err := reviewfile.DecodeAnnotation(data)
	if err != nil {
		a.err = dataset.AnnotationErrorMessage(err)
		return a
	}
	a.file = f
	return a
}

// setAnnotation records what the VOC file of an image holds now.
func (img *image) setAnnotation(a annotationFile) {
	img.annotationVersion = a.version
	img.annotationError = a.err
	if a.err != "" {
		img.objectCount, img.labels = 0, []dataset.LabelCount{}
		img.edited = img.m.Edited // unknown: as the package says
		return
	}
	img.objectCount = len(a.file.Objects)
	img.labels = sortedLabels(reviewfile.LabelCounts(a.file.Objects))
	img.edited = !reviewfile.SameBoxes(a.file.Objects, img.m.Predictions)
}

func sortedLabels(counts map[string]int) []dataset.LabelCount {
	out := make([]dataset.LabelCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, dataset.LabelCount{Name: name, Count: n})
	}
	slices.SortFunc(out, func(a, b dataset.LabelCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func (img *image) hasLabel(name string) bool {
	for _, l := range img.labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// size returns the image size for normalizing boxes and writing VOC
// files: 0, 0 when unknown.
func (img *image) size() (int, int) {
	if img.width != nil && img.height != nil {
		return *img.width, *img.height
	}
	return 0, 0
}

// revision follows the platform's revision of the image: the revision
// in the package plus one for each kind of local change.
func (img *image) revision() int {
	r := img.m.BaseRevision
	if img.status != img.m.BaseStatus {
		r++
	}
	if img.annotationVersion != img.m.BaseAnnotationVersion {
		r++
	}
	return r
}

// statusChanged reports whether the status (or the time it was set)
// differs from the package: what review-progress.json keeps.
func (img *image) statusChanged() bool {
	return img.status != img.m.BaseStatus || !sameTime(img.reviewedAt, img.m.ReviewedAt)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// loadOptions configure opening a package folder.
type loadOptions struct {
	zipPath string
	resumed bool
	now     func() time.Time
	// progress is called now and then with the images scanned so far.
	progress func(done, total int64)
}

// loadPackage opens the package in the folder dir (holding review.json):
// it reads and checks review.json and the progress file, then every image
// file and VOC file, so that lists and counts are right even after the
// VOC files were edited with another program.
func loadPackage(ctx context.Context, dir string, o loadOptions) (*Package, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, openError(dir, err)
	}
	lock, err := lockPackage(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	p, err := loadFromRoot(ctx, root, dir, o)
	if err != nil {
		root.Close()
		closeLock(lock)
		return nil, err
	}
	p.lock = lock
	return p, nil
}

// lockFileName is the file locked in a package folder while a program has
// the package open, so that a second program (the package opened again,
// say by dropping its zip on the program's icon once more) cannot open it
// too: each program keeps the statuses in memory and rewrites
// review-progress.json from them, so the two would undo each other's
// work. The system releases the lock when the program ends, crashed or
// not; the (empty) file stays. It is not part of the results.
const lockFileName = ".tm-reviewer.lock"

// errLocked is returned by lockFile when another program holds the lock.
var errLocked = errors.New("locked by another program")

var errPackageLocked = &Error{Kind: ErrConflict, Message: "这个审图包已在另一个本地审图程序窗口中打开"}

// lockPackage locks the package folder of root; nil (and no error) when
// the folder cannot have the lock: a folder files cannot be created in
// (nothing in it can be changed then), or a file system that cannot lock
// files (some network drives), which open as before.
func lockPackage(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile(lockFileName, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, nil
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, errLocked) {
			return nil, errPackageLocked
		}
		return nil, nil
	}
	return f, nil
}

// closeLock releases the lock of a package folder (closing the file
// releases it).
func closeLock(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func loadFromRoot(ctx context.Context, root *os.Root, dir string, o loadOptions) (*Package, error) {
	m, err := readManifestFile(root)
	if err != nil {
		return nil, err
	}
	if m.Kind != reviewfile.KindPackage {
		return nil, &Error{Kind: ErrInvalid,
			Message: "这是本地审图程序导出的审图结果，不是审图包：请在平台上这个审图任务的页面点“上传审图结果”上传它"}
	}
	now := o.now
	if now == nil {
		now = time.Now
	}
	p := &Package{
		dir: dir, zipPath: o.zipPath, resumed: o.resumed, openedAt: now(), now: now,
		manifest: m, datasets: make(map[int64]reviewfile.ManifestDataset, len(m.Datasets)),
		root: root, images: make([]*image, len(m.Images)), byID: make(map[int64]*image, len(m.Images)),
	}
	for _, d := range m.Datasets {
		p.datasets[d.ID] = d
	}
	for i := range m.Images {
		mi := &m.Images[i]
		img := &image{
			m: mi, position: i, fileName: path.Base(mi.SourcePath), lowerPath: strings.ToLower(mi.SourcePath),
			datasetName: p.datasets[mi.DatasetID].Name,
			status:      mi.BaseStatus, reviewedAt: mi.ReviewedAt, width: mi.Width, height: mi.Height,
		}
		p.images[i], p.byID[mi.ID] = img, img
	}
	progress, err := readProgress(root)
	if err != nil {
		return nil, err
	}
	if progress != nil && progress.PackageID == m.PackageID {
		for _, e := range progress.Images {
			if img := p.byID[e.ID]; img != nil && reviewfile.ValidStatus(e.Status) {
				img.status, img.reviewedAt = e.Status, utcPtr(e.ReviewedAt)
			}
		}
		p.lastExport = progress.LastExport
		p.resumed = true
	}
	if err := p.scan(ctx, o.progress); err != nil {
		return nil, err
	}
	return p, nil
}

// readManifestFile reads and checks review.json of a package folder.
func readManifestFile(root *os.Root) (*reviewfile.Manifest, error) {
	f, err := root.Open(reviewfile.ManifestFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &Error{Kind: ErrInvalid, Message: "文件夹中没有 review.json，这不是审图包"}
	}
	if err != nil {
		return nil, newError(ErrInvalid, "无法读取 review.json：%v", err)
	}
	defer f.Close()
	m, err := reviewfile.ReadManifest(f, maxManifestBytes)
	if err != nil {
		var me *reviewfile.ManifestError
		if errors.As(err, &me) {
			return nil, &Error{Kind: ErrInvalid, Message: me.Message}
		}
		return nil, newError(ErrInvalid, "无法读取 review.json：%v", err)
	}
	return m, nil
}

// scan reads the image files and VOC files of every image, a few at a
// time.
func (p *Package) scan(ctx context.Context, progress func(done, total int64)) error {
	total := int64(len(p.images))
	var done atomic.Int64
	work := make(chan *image)
	var wg sync.WaitGroup
	for range min(8, max(2, runtime.NumCPU())) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for img := range work {
				img.imageVersion = fileVersion(p.root, img.m.Path)
				img.setAnnotation(readAnnotationIn(p.root, img.m.Annotation))
				done.Add(1)
			}
		}()
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var err error
feed:
	for _, img := range p.images {
		select {
		case work <- img:
		case <-ticker.C:
			if progress != nil {
				progress(done.Load(), total)
			}
			select {
			case work <- img:
			case <-ctx.Done():
				err = ctx.Err()
				break feed
			}
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		}
	}
	close(work)
	wg.Wait()
	if progress != nil {
		progress(done.Load(), total)
	}
	return err
}

// fileVersion is the version of the image file rel (its size and time), ""
// when it is missing or not a regular file.
func fileVersion(root *os.Root, rel string) string {
	fi, err := root.Stat(filepath.FromSlash(rel))
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return dataset.ImageVersion(fi.Size(), fi.ModTime())
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// Close closes the package folder. Requests for the package fail
// afterwards.
func (p *Package) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root == nil {
		return nil
	}
	err := p.root.Close()
	p.root = nil
	closeLock(p.lock)
	p.lock = nil
	return err
}

func (p *Package) openRoot() (*os.Root, error) {
	if p.root == nil {
		return nil, errNoPackage
	}
	return p.root, nil
}

// ---- reading ----

// ImageQuery selects images as the platform's image list does; "" or
// "all" does not filter.
type ImageQuery struct {
	Offset    int
	Limit     int
	Status    string // pending, approved, needs_sample or reviewed
	DatasetID int64
	Label     string // exact label of the annotation
	Search    string // case-insensitive substring of the path in the dataset
	Edited    string // edited or unedited
	Boxes     string // with or without
	Inference string // failed
}

// Image filters.
const (
	filterAll      = "all"
	filterReviewed = "reviewed"
	filterEdited   = "edited"
	filterUnedited = "unedited"
	filterWith     = "with"
	filterWithout  = "without"
	filterFailed   = "failed"
)

// Page sizes of the image list (as on the platform).
const (
	defaultImageLimit = 60
	maxImageLimit     = 200
)

// matcher returns the test of whether an image matches q.
func (q ImageQuery) matcher() func(img *image) bool {
	search := strings.ToLower(q.Search)
	return func(img *image) bool {
		switch q.Status {
		case reviewfile.StatusPending, reviewfile.StatusApproved, reviewfile.StatusNeedsSample:
			if img.status != q.Status {
				return false
			}
		case filterReviewed:
			if img.status == reviewfile.StatusPending {
				return false
			}
		}
		switch {
		case q.DatasetID > 0 && img.m.DatasetID != q.DatasetID,
			q.Label != "" && !img.hasLabel(q.Label),
			search != "" && !strings.Contains(img.lowerPath, search),
			q.Edited == filterEdited && !img.edited,
			q.Edited == filterUnedited && img.edited,
			q.Boxes == filterWith && img.objectCount == 0,
			q.Boxes == filterWithout && img.objectCount > 0,
			q.Inference == filterFailed && img.m.InferenceError == "":
			return false
		}
		return true
	}
}

// toImage returns the API view of an image.
func (p *Package) toImage(img *image) Image {
	m := img.m
	predictions := len(m.Predictions)
	return Image{
		ID: m.ID, ReviewID: p.manifest.Review.ID, DatasetID: m.DatasetID, ImageID: m.ImageID, Position: img.position,
		Path: m.SourcePath, FileName: img.fileName, PackagePath: m.Path, AnnotationPath: m.Annotation,
		Width: img.width, Height: img.height, ImageVersion: img.imageVersion, Inferred: true,
		InferenceError: m.InferenceError, ObjectCount: img.objectCount, Labels: nonNil(img.labels),
		PredictionCount: predictions, Edited: img.edited, Status: img.status, Revision: img.revision(),
		ReviewedAt: img.reviewedAt, AnnotationVersion: img.annotationVersion,
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// count adds an image to counts.
func (c *Counts) count(img *image) {
	c.Total++
	c.Inferred++
	if img.m.InferenceError != "" {
		c.InferenceFailed++
	}
	switch img.status {
	case reviewfile.StatusPending:
		c.Pending++
	case reviewfile.StatusApproved:
		c.Approved++
	case reviewfile.StatusNeedsSample:
		c.NeedsSample++
	}
	if img.edited {
		c.Edited++
	}
}

// Summary returns the review with the counts of its images.
func (p *Package) Summary() Summary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	m := p.manifest
	s := Summary{
		ID: m.Review.ID, Name: m.Review.Name, Description: m.Review.Description, Model: m.Model,
		Options: m.Options, Status: "ready", Datasets: make([]Dataset, len(m.Datasets)), CreatedAt: m.Review.CreatedAt,
	}
	s.Model.ClassNames = nonNil(s.Model.ClassNames)
	index := make(map[int64]int, len(m.Datasets))
	for i, d := range m.Datasets {
		s.Datasets[i] = Dataset{ID: d.ID, Name: d.Name, Folder: d.Folder}
		index[d.ID] = i
	}
	for _, img := range p.images {
		s.Counts.count(img)
		s.Datasets[index[img.m.DatasetID]].Counts.count(img)
	}
	return s
}

// ListImages returns a page of the images matching q, in review order,
// and the number of matching images.
func (p *Package) ListImages(q ImageQuery) ([]Image, int) {
	if q.Limit == 0 {
		q.Limit = defaultImageLimit
	}
	match := q.matcher()
	p.mu.RLock()
	defer p.mu.RUnlock()
	items := []Image{}
	total := 0
	for _, img := range p.images {
		if !match(img) {
			continue
		}
		if total >= q.Offset && len(items) < q.Limit {
			items = append(items, p.toImage(img))
		}
		total++
	}
	return items, total
}

// ImageNeighbors locates an image among the images matching q (offset and
// limit do not count) and returns the matching images before and after
// it.
func (p *Package) ImageNeighbors(id int64, q ImageQuery) (Neighbors, error) {
	match := q.matcher()
	p.mu.RLock()
	defer p.mu.RUnlock()
	cur := p.byID[id]
	if cur == nil {
		return Neighbors{}, errImageMissing
	}
	var n Neighbors
	var prev, next *image
	for _, img := range p.images {
		if !match(img) {
			continue
		}
		n.Total++
		switch {
		case img.position < cur.position:
			n.Position++
			prev = img
		case img == cur:
			n.Matches = true
		case next == nil:
			next = img
		}
	}
	if prev != nil {
		v := p.toImage(prev)
		n.Prev = &v
	}
	if next != nil {
		v := p.toImage(next)
		n.Next = &v
	}
	return n, nil
}

// ListLabels returns the labels of the annotations by object count.
func (p *Package) ListLabels() []Label {
	p.mu.RLock()
	defer p.mu.RUnlock()
	byName := map[string]*Label{}
	for _, img := range p.images {
		for _, l := range img.labels {
			e := byName[l.Name]
			if e == nil {
				e = &Label{Name: l.Name}
				byName[l.Name] = e
			}
			e.ObjectCount += l.Count
			e.ImageCount++
		}
	}
	out := make([]Label, 0, len(byName))
	for _, l := range byName {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b Label) int {
		return cmp.Or(cmp.Compare(b.ObjectCount, a.ObjectCount), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// Classes returns the candidate classes of the package.
func (p *Package) Classes() ClassList {
	items := nonNil(p.manifest.Classes)
	return ClassList{Items: items, Total: len(items), Version: ""}
}

// GetImage returns an image with its boxes: the annotation read from its
// VOC file now (the counts follow when another program changed it), and
// the predictions.
func (p *Package) GetImage(id int64) (ImageDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	root, err := p.openRoot()
	if err != nil {
		return ImageDetail{}, err
	}
	img := p.byID[id]
	if img == nil {
		return ImageDetail{}, errImageMissing
	}
	a := readAnnotationIn(root, img.m.Annotation)
	img.setAnnotation(a)
	p.checkSize(root, img)
	return p.detail(img, a.file.Objects), nil
}

// checkSize reads the size of an image the manifest gives none for (an
// image the model could not read), once.
func (p *Package) checkSize(root *os.Root, img *image) {
	if img.sizeChecked || (img.width != nil && img.height != nil) {
		return
	}
	img.sizeChecked = true
	f, err := root.Open(filepath.FromSlash(img.m.Path))
	if err != nil {
		return
	}
	defer f.Close()
	if w, h := dataset.ImageDimensions(f); w != nil && h != nil {
		img.width, img.height = w, h
	}
}

func (p *Package) detail(img *image, objects []dataset.AnnotationObject) ImageDetail {
	return ImageDetail{
		Image: p.toImage(img), Objects: nonNil(objects), Predictions: nonNil(img.m.Predictions),
		DatasetName: img.datasetName, AnnotationError: img.annotationError,
	}
}

// ---- changing ----

// SaveInput is an annotation to write.
type SaveInput struct {
	Objects []dataset.AnnotationObject
	// BaseVersion is the annotation_version the edit started from ("" when
	// the image had no file); unless Force is set, the save fails with
	// ErrAnnotationChanged when the file is no longer that version.
	BaseVersion string
	Force       bool
}

// SaveAnnotation replaces the annotation of an image: its VOC file is
// written anew (atomically) from the objects, which are checked and
// normalized as on the platform. Saving the boxes the file has already
// changes nothing.
func (p *Package) SaveAnnotation(id int64, in SaveInput) (ImageDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	root, err := p.openRoot()
	if err != nil {
		return ImageDetail{}, err
	}
	img := p.byID[id]
	if img == nil {
		return ImageDetail{}, errImageMissing
	}
	a := readAnnotationIn(root, img.m.Annotation)
	if !in.Force && a.version != in.BaseVersion {
		img.setAnnotation(a)
		return ImageDetail{}, errAnnotationChanged
	}
	p.checkSize(root, img)
	w, h := img.size()
	objects, err := dataset.NormalizeObjects(nonNil(in.Objects), w, h)
	if err != nil {
		var de *dataset.Error
		if errors.As(err, &de) {
			return ImageDetail{}, &Error{Kind: ErrInvalid, Message: de.Message}
		}
		return ImageDetail{}, err
	}
	data := reviewfile.EncodeAnnotation(img.m.SourcePath, img.width, img.height, objects)
	if v := reviewfile.AnnotationVersion(data); v != a.version || a.err != "" {
		if err := reviewfile.WriteFile(root, img.m.Annotation, data); err != nil {
			img.setAnnotation(a)
			return ImageDetail{}, newError(ErrIO, "无法保存标注文件 %s：%v", img.m.Annotation, err)
		}
		a = annotationFile{data: data, version: v, file: dataset.VOCFile{Objects: objects}}
	}
	img.setAnnotation(a)
	return p.detail(img, a.file.Objects), nil
}

// SetStatus sets the review status of an image. Setting the status it has
// changes nothing. The change is in review-progress.json before this
// returns; if that file cannot be written, the status stays as it was.
func (p *Package) SetStatus(id int64, status string) (ImageDetail, error) {
	if !reviewfile.ValidStatus(status) {
		return ImageDetail{}, &Error{Kind: ErrInvalid, Message: "status 必须是 pending、approved 或 needs_sample"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	root, err := p.openRoot()
	if err != nil {
		return ImageDetail{}, err
	}
	img := p.byID[id]
	if img == nil {
		return ImageDetail{}, errImageMissing
	}
	if img.status != status {
		oldStatus, oldTime := img.status, img.reviewedAt
		img.status, img.reviewedAt = status, nil
		if status != reviewfile.StatusPending {
			t := p.now().UTC().Truncate(time.Microsecond) // what the platform keeps
			img.reviewedAt = &t
		}
		if err := p.writeProgress(root); err != nil {
			img.status, img.reviewedAt = oldStatus, oldTime
			return ImageDetail{}, err
		}
	}
	a := readAnnotationIn(root, img.m.Annotation)
	img.setAnnotation(a)
	return p.detail(img, a.file.Objects), nil
}

// ---- progress ----

// Progress file identifiers.
const (
	progressFormat  = "trainmaster-review-progress"
	progressVersion = 1
	// maxProgressBytes bounds review-progress.json (a few hundred bytes per
	// image whose status changed).
	maxProgressBytes = 256 << 20
)

// progressFile is review-progress.json: the review statuses that differ
// from the package, and the last export. The annotations are the VOC
// files themselves.
type progressFile struct {
	Format     string          `json:"format"`
	Version    int             `json:"version"`
	PackageID  string          `json:"package_id"`
	ReviewID   int64           `json:"review_id"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Images     []progressImage `json:"images"` // in review order
	LastExport *ExportInfo     `json:"last_export"`
}

type progressImage struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	ReviewedAt *time.Time `json:"reviewed_at"`
}

// readProgress reads review-progress.json of a package folder; nil when
// there is none. A file that cannot be used fails the opening: it holds
// the user's work, so it must not be overwritten silently.
func readProgress(root *os.Root) (*progressFile, error) {
	f, err := root.Open(reviewfile.ProgressFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, newError(ErrInvalid, "无法读取审图进度（%s）：%v", reviewfile.ProgressFile, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxProgressBytes+1))
	if err != nil {
		return nil, newError(ErrInvalid, "无法读取审图进度（%s）：%v", reviewfile.ProgressFile, err)
	}
	var pf progressFile
	if err := json.Unmarshal(data, &pf); err != nil || pf.Format != progressFormat || len(data) > maxProgressBytes {
		return nil, newError(ErrInvalid, "审图进度文件 %s 已损坏，无法继续上次的进度。请把它移走（或删除，之前的审核状态会丢失）后重新打开",
			reviewfile.ProgressFile)
	}
	if pf.Version > progressVersion {
		return nil, newError(ErrInvalid, "审图进度文件 %s 是更新版本的本地审图程序写的，请升级本地审图程序", reviewfile.ProgressFile)
	}
	return &pf, nil
}

// writeProgress rewrites review-progress.json (atomically) from the
// current state. Called with p.mu held.
func (p *Package) writeProgress(root *os.Root) error {
	pf := progressFile{
		Format: progressFormat, Version: progressVersion, PackageID: p.manifest.PackageID,
		ReviewID: p.manifest.Review.ID, UpdatedAt: p.now().UTC(), Images: []progressImage{}, LastExport: p.lastExport,
	}
	for _, img := range p.images {
		if img.statusChanged() {
			pf.Images = append(pf.Images, progressImage{ID: img.m.ID, Status: img.status, ReviewedAt: img.reviewedAt})
		}
	}
	data, err := json.MarshalIndent(&pf, "", "  ")
	if err != nil {
		return err
	}
	if err := reviewfile.WriteFile(root, reviewfile.ProgressFile, data); err != nil {
		return newError(ErrIO, "无法保存审图进度（%s）：%v", filepath.Join(p.dir, reviewfile.ProgressFile), err)
	}
	return nil
}

// ---- information ----

// PackageInfo describes an opened package (GET /api/session).
type PackageInfo struct {
	Path          string              `json:"path"`         // the folder
	ZipPath       string              `json:"zip_path"`     // "" when opened as a folder
	ResultsPath   string              `json:"results_path"` // where the results are exported to
	PackageID     string              `json:"package_id"`
	Review        PackageReview       `json:"review"`
	Model         reviewfile.ModelRef `json:"model"`
	Datasets      []string            `json:"datasets"` // names
	Platform      string              `json:"platform"`
	DownloadedAt  time.Time           `json:"downloaded_at"`
	Counts        Counts              `json:"counts"`
	MissingImages int                 `json:"missing_images"` // images whose file is not in the folder
	Resumed       bool                `json:"resumed"`
	OpenedAt      time.Time           `json:"opened_at"`
	LastExport    *ExportInfo         `json:"last_export"`
}

// PackageReview names the review of a package.
type PackageReview struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Info describes the package.
func (p *Package) Info() PackageInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	m := p.manifest
	info := PackageInfo{
		Path: p.dir, ZipPath: p.zipPath, ResultsPath: p.ResultsPath(), PackageID: m.PackageID,
		Review: PackageReview{ID: m.Review.ID, Name: m.Review.Name, Description: m.Review.Description},
		Model:  m.Model, Datasets: make([]string, len(m.Datasets)), Platform: m.Platform, DownloadedAt: m.ExportedAt,
		Resumed: p.resumed, OpenedAt: p.openedAt, LastExport: p.lastExport,
	}
	info.Model.ClassNames = nonNil(info.Model.ClassNames)
	for i, d := range m.Datasets {
		info.Datasets[i] = d.Name
	}
	for _, img := range p.images {
		info.Counts.count(img)
		if img.imageVersion == "" {
			info.MissingImages++
		}
	}
	return info
}

// ---- files ----

// imageFile is an image file of the package opened for reading.
type imageFile struct {
	file        *os.File
	name        string
	contentType string
	modTime     time.Time
	version     string
}

// OpenImage opens the image file of an image.
func (p *Package) OpenImage(id int64) (*imageFile, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	root, err := p.openRoot()
	if err != nil {
		return nil, err
	}
	img := p.byID[id]
	if img == nil {
		return nil, errImageMissing
	}
	f, err := root.Open(filepath.FromSlash(img.m.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errFileMissing
		}
		return nil, newError(ErrIO, "无法打开图片文件 %s：%v", img.m.Path, err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errFileMissing
	}
	return &imageFile{
		file: f, name: img.fileName, contentType: dataset.ImageContentType(img.m.Path), modTime: fi.ModTime(),
		version: dataset.ImageVersion(fi.Size(), fi.ModTime()),
	}, nil
}

// openError explains why a package folder cannot be opened.
func openError(dir string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return newError(ErrNotFound, "文件夹不存在：%s", dir)
	}
	if errors.Is(err, fs.ErrPermission) {
		return newError(ErrForbidden, "没有权限打开文件夹 %s", dir)
	}
	return newError(ErrIO, "无法打开文件夹 %s：%v", dir, err)
}
