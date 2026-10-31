package dataset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// execute runs a claimed job and returns its summary. A user cancel makes
// it return ctx's error; the caller decides the final status.
func (s *Service) execute(ctx context.Context, job Import, p *progressTracker) (map[string]int64, error) {
	d, err := queryDataset(ctx, s.db, job.DatasetID, false)
	if err != nil {
		return nil, err
	}
	switch job.Source {
	case SourceScan:
		res, err := s.syncIndex(ctx, d, p)
		if aerr := s.finishIndex(d.ID); err == nil {
			err = aerr
		}
		return res.summary(), err
	case SourceArchive, SourceFolder:
		return s.runImport(ctx, d, job, p)
	}
	return nil, fmt.Errorf("unknown import source %q", job.Source)
}

// indexContext is the context for indexing files that were already moved
// into a dataset: a user cancel must not stop it (the files would be in the
// folder but missing from the index), only stopping the service does.
func (s *Service) indexContext() context.Context { return s.base }

// finishIndex recomputes the dataset counters after a job, whatever its
// outcome. Rows indexed before the service was stopped stay in the table,
// so this runs even then (bounded by finishTimeout; Stop returns before
// the database is closed).
func (s *Service) finishIndex(datasetID int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.indexContext()), finishTimeout)
	defer cancel()
	if err := recomputeAggregates(ctx, s.db, datasetID, true); err != nil {
		return fmt.Errorf("update dataset counters: %w", err)
	}
	return nil
}

// importSummary counts the outcome of an archive or folder import.
type importSummary struct {
	Added, Replaced, Skipped int
	Matched, Invalid         int
	Orphaned, Ignored        int
	Unsafe                   int
	NamesInvalid             int // found while indexing the dataset folder afterwards
	archive                  bool
	// Conversion of X-AnyLabeling / YOLO annotations (convert.go).
	converted                bool
	Converted, ConvertFailed int
	ObjectsSkipped           int
}

func (r *importSummary) summary() map[string]int64 {
	m := map[string]int64{
		sumImagesAdded:        int64(r.Added),
		sumImagesReplaced:     int64(r.Replaced),
		sumImagesSkipped:      int64(r.Skipped),
		sumAnnotationsMatched: int64(r.Matched),
		sumAnnotationsInvalid: int64(r.Invalid),
		sumAnnotationsOrphan:  int64(r.Orphaned),
		sumNamesInvalid:       int64(r.NamesInvalid),
	}
	if r.archive {
		// Folder uploads drop such files before they reach the server.
		m[sumFilesIgnored] = int64(r.Ignored)
		m[sumEntriesUnsafe] = int64(r.Unsafe)
	}
	if r.converted {
		m[sumAnnotationsConverted] = int64(r.Converted)
		m[sumAnnotationsFailed] = int64(r.ConvertFailed)
		m[sumObjectsSkipped] = int64(r.ObjectsSkipped)
	}
	return m
}

// runImport extracts (archives) and moves the staged files into the
// dataset folder, then indexes the folder (spec §5.1 / §5.2).
func (s *Service) runImport(ctx context.Context, d Dataset, job Import, p *progressTracker) (map[string]int64, error) {
	sum := &importSummary{archive: job.Source == SourceArchive, converted: job.AnnotationFormat != FormatVOC}
	// Fail early, before extracting a large archive for nothing.
	root, err := s.openDatasetRoot(d)
	if err != nil {
		return sum.summary(), err
	}
	defer root.Close()

	staged := filepath.Join(s.stagingDir(job.ID), stagedFilesDir)
	if job.Source == SourceArchive {
		if err := s.extract(ctx, job, staged, sum, p); err != nil {
			return sum.summary(), err
		}
		staged = singleTopDir(staged)
	}
	var unconverted map[string]bool
	if job.AnnotationFormat != FormatVOC {
		unconverted, err = s.convertStaged(ctx, staged, job, sum, p)
		if err != nil {
			return sum.summary(), diskFull(err)
		}
	}

	moved, err := s.moveStaged(ctx, root, staged, job.OnConflict, unconverted, sum, p)
	err = diskFull(err)
	if moved > 0 || err == nil {
		res, serr := s.syncIndex(s.indexContext(), d, p)
		sum.NamesInvalid = res.NamesInvalid
		if serr != nil && err == nil {
			err = serr
		}
	}
	if aerr := s.finishIndex(d.ID); err == nil {
		err = aerr
	}
	return sum.summary(), err
}

var errExtractTooLarge = &Error{Kind: ErrInsufficientStorage,
	Message: "解压后的文件超出了磁盘剩余空间（为防止压缩炸弹已中止）"}

// errDiskFull replaces the error of a write that failed because the disk
// is full.
var errDiskFull = &Error{Kind: ErrInsufficientStorage, Message: "磁盘空间已满，导入已中止；请清理磁盘空间后重试"}

func diskFull(err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return errDiskFull
	}
	return err
}

// freeSpaceRecheck is how many bytes an extraction writes between two
// reads of the real free disk space.
const freeSpaceRecheck = 64 << 20

// extract unpacks the uploaded archive into dir, keeping only images and
// annotation files of the job's format with safe names (spec §5.1 step 5).
// The archive is deleted afterwards.
func (s *Service) extract(ctx context.Context, job Import, dir string, sum *importSummary, p *progressTracker) error {
	archivePath := filepath.Join(s.stagingDir(job.ID), uploadFileName)
	f, err := os.Open(archivePath)
	if errors.Is(err, fs.ErrNotExist) {
		return &Error{Kind: ErrInvalid, Message: "上传的压缩包已不存在，请重新上传"}
	}
	if err != nil {
		return fmt.Errorf("open uploaded archive: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	guard := &extractGuard{budget: s.extractionBudget(), recheck: freeSpaceRecheck, freeBytes: s.freeBytes}
	p.setStage(ctx, stageExtracting, unitBytes, fi.Size())
	err = walkArchive(ctx, f, fi.Size(), func(e *archiveEntry) error {
		switch e.Type {
		case entryDir:
			return nil
		case entryLink, entryOther:
			sum.Unsafe++
			return nil
		}
		rel, ok := cleanEntryPath(e.Name)
		if !ok {
			sum.Unsafe++
			return nil
		}
		if classifyFor(rel, job.AnnotationFormat) == kindIgnored {
			sum.Ignored++
			return nil
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		out, err := createStagedFile(dst)
		if err != nil {
			if isPathConflict(err) {
				sum.Unsafe++ // e.g. "a.jpg" both as a file and as a folder
				return nil
			}
			return fmt.Errorf("extract %s: %w", rel, err)
		}
		err = guard.copy(out, e.Data)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}, func(unit string, done, total int64) {
		p.unit = unit
		p.set(ctx, done, total)
	})
	if err != nil {
		return diskFull(err)
	}
	f.Close()
	if err := os.Remove(archivePath); err != nil {
		s.log.Warn("remove extracted archive", "import_id", job.ID, "err", err)
	}
	return nil
}

// extractGuard stops an extraction before it fills the disk. The budget
// (free space minus the reserve when the job started) bounds what this job
// writes; since other imports and uploads write at the same time, the
// real free space is also read again every recheck bytes.
type extractGuard struct {
	budget, written int64
	recheck         int64
	sinceCheck      int64
	freeBytes       func() (uint64, error)
}

// copy writes src to dst, failing with errExtractTooLarge when the budget
// is exceeded or the free space falls below reserveBytes.
func (g *extractGuard) copy(dst io.Writer, src io.Reader) error {
	for {
		n, err := io.CopyN(dst, src, min(g.recheck-g.sinceCheck, g.budget-g.written+1))
		g.written += n
		g.sinceCheck += n
		if g.written > g.budget {
			return errExtractTooLarge
		}
		if g.sinceCheck >= g.recheck {
			g.sinceCheck = 0
			if free, ferr := g.freeBytes(); ferr == nil && free < reserveBytes {
				return errExtractTooLarge
			}
		}
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
	}
}

func createStagedFile(dst string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
}

// isPathConflict reports file system errors caused by the shape of a path
// (a file where a folder is needed, a name that is too long, ...), which
// are problems of one entry rather than of the whole import.
func isPathConflict(err error) bool {
	return errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.EISDIR) ||
		errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, fs.ErrExist)
}

// singleTopDir strips one common top-level folder: an archive batch1.zip
// that holds only batch1/... imports the contents of batch1/.
func singleTopDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return dir
	}
	return filepath.Join(dir, entries[0].Name())
}

// placement is what happened to one staged image.
type placement int

const (
	placedAdded placement = iota
	placedReplaced
	placedSkipped
	placedUnsafe
)

// moveStaged moves the images below staged (and the annotation paired
// with each) into the dataset folder (spec §5.1 step 7). It returns how
// many images were moved; on cancel it stops at the next image.
// unconverted are images whose annotation file could not be converted to
// VOC: an image among them that replaces one in the dataset also drops the
// replaced image's annotation, so it ends up unannotated as reported.
func (s *Service) moveStaged(ctx context.Context, dst *os.Root, staged, onConflict string, unconverted map[string]bool,
	sum *importSummary, p *progressTracker) (int, error) {
	set, err := walkFiles(ctx, s.log, staged, nil)
	if errors.Is(err, fs.ErrNotExist) {
		set, err = &fileSet{}, nil // nothing was staged
	}
	if err != nil {
		return 0, fmt.Errorf("list staged files: %w", err)
	}
	src, err := os.OpenRoot(staged)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer src.Close()

	cache := newAnnotationCache(src)
	images := set.imagePaths()
	pairs := pairAnnotations(images, set.xmlPaths(), cache.filename)
	sum.Orphaned += len(set.XMLs) - len(pairs)
	sum.Ignored += set.Ignored

	p.setStage(ctx, stageImporting, unitFiles, int64(len(images)))
	moved := 0
	for i, rel := range images {
		if err := ctx.Err(); err != nil {
			return moved, err
		}
		xmlRel := pairs[rel]
		valid := false
		if xmlRel != "" {
			_, perr := cache.get(xmlRel) // parse before the file is moved
			valid = perr == nil
		}
		res, err := placeImage(dst, staged, rel, xmlRel, onConflict)
		if err == nil && res == placedReplaced && xmlRel == "" && unconverted[rel] {
			err = removeSidecarXML(dst, rel)
		}
		if err != nil {
			return moved, err
		}
		switch res {
		case placedAdded, placedReplaced:
			moved++
			if res == placedAdded {
				sum.Added++
			} else {
				sum.Replaced++
			}
			if xmlRel != "" {
				if valid {
					sum.Matched++
				} else {
					sum.Invalid++
				}
			}
		case placedSkipped:
			sum.Skipped++
		case placedUnsafe:
			sum.Unsafe++
			s.log.Warn("not importing through a symbolic link in the dataset folder", "path", rel)
		}
		p.set(ctx, int64(i+1), int64(len(images)))
	}
	return moved, nil
}

// placeImage moves one staged image and its annotation into the dataset.
// Existing files are kept (skip) or replaced (overwrite); an existing
// annotation is only replaced in overwrite mode. Paths that would pass
// through a symbolic link inside the dataset folder are refused.
func placeImage(dst *os.Root, staged, rel, xmlRel, onConflict string) (placement, error) {
	if err := ensureDirs(dst, path.Dir(rel)); err != nil {
		return placementFor(err)
	}
	exists, err := regularFileExists(dst, rel)
	if err != nil {
		return placementFor(err)
	}
	if exists && onConflict != ConflictOverwrite {
		return placedSkipped, nil
	}
	if err := moveFile(filepath.Join(staged, filepath.FromSlash(rel)), filepath.Join(dst.Name(), filepath.FromSlash(rel))); err != nil {
		return 0, err
	}
	result := placedAdded
	if exists {
		result = placedReplaced
	}
	if xmlRel == "" {
		return result, nil
	}
	if err := ensureDirs(dst, path.Dir(xmlRel)); err != nil {
		if errors.Is(err, errUnsafeTarget) {
			return result, nil // the image is in; its annotation stays out
		}
		return 0, err
	}
	xmlExists, err := regularFileExists(dst, xmlRel)
	switch {
	case errors.Is(err, errUnsafeTarget), errors.Is(err, errOccupied):
		return result, nil
	case err != nil:
		return 0, err
	case xmlExists && onConflict != ConflictOverwrite:
		return result, nil
	}
	if err := moveFile(filepath.Join(staged, filepath.FromSlash(xmlRel)), filepath.Join(dst.Name(), filepath.FromSlash(xmlRel))); err != nil {
		return 0, err
	}
	return result, nil
}

// removeSidecarXML removes <stem>.xml next to the dataset image rel, the
// file a converted annotation would have replaced.
func removeSidecarXML(dst *os.Root, rel string) error {
	xmlRel := sidecarPath(rel, ".xml")
	exists, err := regularFileExists(dst, xmlRel)
	switch {
	case errors.Is(err, errUnsafeTarget), errors.Is(err, errOccupied):
		return nil
	case err != nil:
		return err
	case !exists:
		return nil
	}
	if err := dst.Remove(filepath.FromSlash(xmlRel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove replaced annotation: %w", err)
	}
	return nil
}

// errUnsafeTarget is returned when a target path contains a symbolic link
// or a non-directory where a folder is needed.
var errUnsafeTarget = errors.New("target path is not a plain folder hierarchy")

func placementFor(err error) (placement, error) {
	switch {
	case errors.Is(err, errUnsafeTarget):
		return placedUnsafe, nil
	case errors.Is(err, errOccupied):
		return placedSkipped, nil
	}
	return 0, err
}

// errOccupied is returned when a target file path is taken by a folder or
// another non-regular file that an import must not replace.
var errOccupied = errors.New("target path is occupied")

// regularFileExists reports whether rel is an existing regular file in
// root, without following a symbolic link at rel.
func regularFileExists(root *os.Root, rel string) (bool, error) {
	fi, err := root.Lstat(filepath.FromSlash(rel))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return false, errUnsafeTarget
	case !fi.Mode().IsRegular():
		return false, errOccupied
	}
	return true, nil
}

// ensureDirs creates the folders of the slash-separated path dir inside
// root one level at a time, refusing to pass through anything that is not
// a real directory (in particular symbolic links).
func ensureDirs(root *os.Root, dir string) error {
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
			if fi, err = root.Lstat(name); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !fi.IsDir() { // symbolic links included: Lstat does not follow them
			return errUnsafeTarget
		}
	}
	return nil
}

// moveFile renames src to dst, replacing dst. When they are on different
// file systems (datasets/ mounted separately) it copies, syncs, renames
// the copy into place and removes src.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".import-*")
	if err != nil {
		return err
	}
	_ = tmp.Chmod(0o644) // like the staged files, not CreateTemp's 0600
	_, err = io.Copy(tmp, in)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	in.Close()
	return os.Remove(src)
}
