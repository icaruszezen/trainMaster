package dataversion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// OWNER: backend-core agent. The builder: one build at a time, oldest
// queued first; restart recovery.
//
// A build owns its version row while it is building: every write it makes
// is guarded by the row's state (WHERE id = $1 AND status = 'building'),
// so a version deleted meanwhile (Delete removes the row and cancels the
// build) stops the build at its next write; the build then clears what it
// wrote. It never holds a transaction while it reads or writes files,
// except in the short finishing transaction. When the service stops, a
// running build is left as it is (building): recoverBuilds fails it at the
// next start.

const (
	// pollInterval is how often the builder looks for queued versions
	// without being woken.
	pollInterval = 10 * time.Second
	// progressInterval bounds how often a build writes its progress.
	progressInterval = 500 * time.Millisecond
	// insertBatch is how many image rows a build inserts at a time.
	insertBatch = 500
	// finishTimeout bounds the writes that record how a build ended.
	finishTimeout = 30 * time.Second
)

var (
	// errBuildDeleted is the cancel cause of a build whose version was
	// deleted.
	errBuildDeleted = errors.New("data version deleted while building")
	// errBuildGone: a guarded write of a build found no building row (the
	// version was deleted).
	errBuildGone = errors.New("data version row gone")
)

// builder holds the state of the build queue in this process.
type builder struct {
	wake chan struct{}

	mu      sync.Mutex
	running int64                   // the version being built, 0 for none
	cancel  context.CancelCauseFunc // cancels it
	// pending are versions deleted while building before the builder
	// registered them (it claimed the row but has not called register).
	pending map[int64]bool
}

func (b *builder) init() {
	b.wake = make(chan struct{}, 1)
	b.pending = map[int64]bool{}
}

// notify wakes the builder (non-blocking).
func (b *builder) notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// register records the running build; a delete that came first cancels it
// at once.
func (b *builder) register(id int64, cancel context.CancelCauseFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running, b.cancel = id, cancel
	if b.pending[id] {
		cancel(errBuildDeleted)
	}
	// Only the version just claimed can be pending: one build at a time.
	clear(b.pending)
}

func (b *builder) unregister(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running == id {
		b.running, b.cancel = 0, nil
	}
}

// cancelBuild cancels the build of a deleted version (or remembers it
// until the build is registered).
func (b *builder) cancelBuild(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running == id && b.cancel != nil {
		b.cancel(errBuildDeleted)
		return
	}
	b.pending[id] = true
}

// recoverBuilds runs at Start, before the builder: versions a previous run
// left building fail as interrupted and their folders are emptied.
func (s *Service) recoverBuilds(ctx context.Context) error {
	type interrupted struct {
		ID      int64
		DirName string
		Sources []Source
	}
	var list []interrupted
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, dir_name, sources FROM data_versions WHERE status = 'building'
			ORDER BY id FOR UPDATE`)
		if err != nil {
			return err
		}
		if list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[interrupted]); err != nil {
			return err
		}
		for _, v := range list {
			if _, err := tx.Exec(ctx, "DELETE FROM data_version_images WHERE version_id = $1", v.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE data_versions SET status = 'failed', error = $2, stage = '',
					progress_done = 0, progress_total = 0, image_count = 0, object_count = 0, train_count = 0,
					val_count = 0, test_count = 0, augmented_count = 0, total_bytes = 0, stats = '{}', sources = $3,
					finished_at = now(), updated_at = now()
				WHERE id = $1`, v.ID, msgInterrupted, resetSourceCounts(v.Sources)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("recover data version builds: %w", err)
	}
	for _, v := range list {
		s.discardOwned(v.ID, s.versionDir(v.DirName))
		s.discardThumbnails(v.ID)
	}
	if len(list) > 0 {
		s.log.Warn("data version builds interrupted by the last shutdown failed", "count", len(list))
	}
	return nil
}

// resetSourceCounts returns the sources with their image counts reset.
func resetSourceCounts(sources []Source) []Source {
	out := slices.Clone(sources)
	if out == nil {
		out = []Source{}
	}
	for i := range out {
		out[i].ImageCount = 0
		out[i].DatasetDeleted = false
	}
	return out
}

// builderLoop builds queued versions one at a time until ctx ends.
func (s *Service) builderLoop(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		for ctx.Err() == nil {
			built, err := s.buildNext(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("data version builder", "err", err)
				}
				break
			}
			if !built {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.builder.wake:
		case <-ticker.C:
		}
	}
}

// buildNext claims the oldest queued version and builds it; built is false
// when none is queued.
func (s *Service) buildNext(ctx context.Context) (built bool, err error) {
	job, err := s.claim(ctx)
	if err != nil || job == nil {
		return false, err
	}
	s.runBuild(ctx, *job)
	return true, nil
}

// buildJob is a claimed version.
type buildJob struct {
	ID        int64
	ProjectID int64
	Version   int
	DirName   string
	Classes   []Class
	Config    Config
	Sources   []Source
	CreatedAt time.Time
}

// claim marks the oldest queued version building and returns it (nil when
// none is queued).
func (s *Service) claim(ctx context.Context) (*buildJob, error) {
	var j buildJob
	err := s.db.QueryRow(ctx, `UPDATE data_versions SET status = 'building', stage = 'preparing',
			progress_done = 0, progress_total = 0, started_at = now(), updated_at = now()
		WHERE id = (SELECT id FROM data_versions WHERE status = 'queued' AND deleted_at IS NULL
			ORDER BY queued_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, project_id, version, dir_name, classes, config, sources, created_at`).
		Scan(&j.ID, &j.ProjectID, &j.Version, &j.DirName, &j.Classes, &j.Config, &j.Sources, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim a queued data version: %w", err)
	}
	return &j, nil
}

// runBuild builds a claimed version and records how it ended.
func (s *Service) runBuild(parent context.Context, job buildJob) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	s.builder.register(job.ID, cancel)
	defer s.builder.unregister(job.ID)

	log := s.log.With("version_id", job.ID, "project_id", job.ProjectID, "version", job.Version)
	log.Info("data version build started")
	start := time.Now()
	b := newBuild(s, job, log)
	err := b.runSafe(ctx)
	switch {
	case err == nil:
		log.Info("data version ready", "images", b.counts.images, "augmented", b.counts.augmented,
			"linked", b.stats.Storage.Linked, "copied", b.stats.Storage.Copied,
			"duration_ms", time.Since(start).Milliseconds())
	case errors.Is(err, errBuildGone) || errors.Is(context.Cause(ctx), errBuildDeleted):
		log.Info("data version deleted while building", "duration_ms", time.Since(start).Milliseconds())
		if parent.Err() == nil {
			b.cleanup()
		}
	case parent.Err() != nil:
		// Shutting down: the row stays building and the folder as it is;
		// recoverBuilds fails it at the next start.
		log.Warn("data version build interrupted by shutdown", "err", err)
	default:
		b.fail(err, time.Since(start))
	}
}

// build is the state of one build.
type build struct {
	s   *Service
	job buildJob
	log *slog.Logger
	ctx context.Context
	dir string // the version folder
	// owned: the folder is this build's (it wrote .pending): it is cleared
	// when the build does not complete.
	owned bool

	stage       string
	done, total int
	saved       time.Time

	snaps   []*dataset.Snapshot // by source
	filter  labelFilter
	images  []*buildImage // the originals taken, in position order
	namer   *pathNamer
	space   spaceGuard
	dirs    map[string]bool // folders created in the version folder
	copies  []*buildCopy    // augmented copies inserted, in position order
	stride  int             // positions per original (1 + copies)
	stats   Stats
	counts  struct{ images, objects, augmented int }
	perSrc  []int // originals taken per source
	labels  labelStatsList
	written int64 // bytes of all stored files

	crossDevice map[int]bool // sources on another file system (links fail with EXDEV)
	linkWarned  map[int]bool
}

// buildImage is an original taken by a build.
type buildImage struct {
	index      int // in build.images
	source     int
	rel        string // in the dataset folder
	sourceSize int64  // the dataset file when prepared
	sourceMod  time.Time
	width      int
	height     int
	objects    []dataset.AnnotationObject // kept, clipped
	annotation *int64                     // the review version whose annotation it has
	split      string
	splitBy    string
	position   int
	path       string // in the version folder
	// Set once stored and inserted.
	stored           string
	orientationFixed bool
	size             int64
	modTime          time.Time
	id               int64
	inserted         bool
}

// buildCopy is an augmented copy.
type buildCopy struct {
	orig     *buildImage
	k        int
	path     string
	width    int
	height   int
	objects  []dataset.AnnotationObject
	methods  []string
	size     int64
	modTime  time.Time
	position int
}

func newBuild(s *Service, job buildJob, log *slog.Logger) *build {
	b := &build{s: s, job: job, log: log, dir: s.versionDir(job.DirName), dirs: map[string]bool{},
		crossDevice: map[int]bool{}, linkWarned: map[int]bool{}, perSrc: make([]int, len(job.Sources)),
		filter: newLabelFilter(job.Classes, job.Config), labels: newLabelStats(job.Classes), stride: 1}
	if a := job.Config.Augmentation; a != nil {
		b.stride = 1 + a.Copies
	}
	b.space = spaceGuard{usage: s.usage, warn: func(err error) { log.Warn("data version: check free disk space", "err", err) }}
	b.stats = emptyStats(job.Classes)
	return b
}

// emptyStats is the complete statistics object with zeros.
func emptyStats(classes []Class) Stats {
	return Stats{Labels: newLabelStats(classes).list}
}

// runSafe runs the build; a panic fails it instead of crashing the server.
func (b *build) runSafe(ctx context.Context) (err error) {
	defer func() {
		if v := recover(); v != nil {
			b.log.Error("data version build panicked", "panic", v, "stack", string(debug.Stack()))
			err = fmt.Errorf("data version build panicked: %v", v)
		}
	}()
	defer b.closeSnapshots()
	return b.run(ctx)
}

func (b *build) run(ctx context.Context) error {
	b.ctx = ctx
	// A retry starts from nothing (rows of an interrupted attempt are
	// removed by recoverBuilds; this is the belt to those braces).
	if _, err := b.s.db.Exec(ctx, "DELETE FROM data_version_images WHERE version_id = $1", b.job.ID); err != nil {
		return err
	}
	if err := b.setStage(StagePreparing, 0); err != nil {
		return err
	}
	if err := validateFrozenSources(ctx, b.s.db, b.job.ProjectID, b.job.Sources, checkForBuild); err != nil {
		return err
	}
	if err := b.makeFolder(); err != nil {
		return err
	}
	if err := b.openSnapshots(); err != nil {
		return err
	}
	if err := b.prepare(); err != nil {
		return err
	}
	if err := b.split(); err != nil {
		return err
	}
	if err := b.linkImages(); err != nil {
		return err
	}
	if err := b.augmentImages(); err != nil {
		return err
	}
	return b.finish()
}

// ---- progress and guarded writes ----

// setStage switches to a stage and writes it at once.
func (b *build) setStage(stage string, total int) error {
	b.stage, b.done, b.total = stage, 0, total
	if err := b.writeProgress(); err != nil {
		return err
	}
	if hook := b.s.buildHook; hook != nil {
		hook(b.job.ID, stage)
	}
	return b.ctx.Err()
}

// tick counts n items done; the progress write is throttled.
func (b *build) tick(n int) error {
	b.done += n
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if time.Since(b.saved) >= progressInterval {
		return b.writeProgress()
	}
	return nil
}

func (b *build) writeProgress() error {
	b.saved = time.Now()
	tag, err := b.s.db.Exec(b.ctx, `UPDATE data_versions SET stage = $2, progress_done = $3, progress_total = $4,
		updated_at = now() WHERE id = $1 AND status = 'building'`, b.job.ID, b.stage, b.done, b.total)
	if err != nil {
		return fmt.Errorf("record data version progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errBuildGone
	}
	return nil
}

// isForeignKeyViolation reports a write that refers to a row that is gone.
func isForeignKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// ---- the folder ----

// makeFolder creates the version folder with its .pending marker. A folder
// left by an earlier attempt (with the marker) is cleared first; one
// without the marker is not ours (a finished version restored from a
// backup?) and is never touched.
func (b *build) makeFolder() error {
	if _, err := os.Lstat(b.dir); err == nil {
		if !hasPending(b.dir) {
			return newError(ErrConflict, "数据版本文件夹 %s 已经存在（可能来自备份），请先移走它", "exports/"+b.job.DirName)
		}
		if err := b.s.discard(b.job.ID, b.dir); err != nil {
			return fmt.Errorf("clear the data version folder: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check the data version folder: %w", err)
	}
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return fmt.Errorf("create the data version folder: %w", err)
	}
	if err := writePending(b.dir, b.job.ID); err != nil {
		return err
	}
	b.owned = true
	return nil
}

// cleanup removes what a build of a deleted version wrote.
func (b *build) cleanup() {
	if b.owned {
		if err := b.s.discard(b.job.ID, b.dir); err != nil {
			b.log.Warn("data version: clear the folder of a deleted version", "err", err)
		}
	}
	b.s.discardThumbnails(b.job.ID)
}

// fail records a failed build: the guarded update to failed (with the
// message users see), the image rows removed, then the folder cleared.
func (b *build) fail(err error, took time.Duration) {
	msg := msgInternal
	var ue *Error
	if errors.As(err, &ue) {
		msg = ue.Message
		b.log.Warn("data version build failed", "reason", msg, "duration_ms", took.Milliseconds())
	} else {
		b.log.Error("data version build failed", "err", err, "duration_ms", took.Milliseconds())
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), finishTimeout)
	defer cancel()
	gone := false
	ferr := pgx.BeginFunc(ctx, b.s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE data_versions SET status = 'failed', error = $2, stage = '',
				progress_done = 0, progress_total = 0, image_count = 0, object_count = 0, train_count = 0,
				val_count = 0, test_count = 0, augmented_count = 0, total_bytes = 0, stats = '{}', sources = $3,
				finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'building'`, b.job.ID, msg, resetSourceCounts(b.job.Sources))
		if err != nil {
			return err
		}
		if gone = tag.RowsAffected() == 0; gone {
			return nil
		}
		_, err = tx.Exec(ctx, "DELETE FROM data_version_images WHERE version_id = $1", b.job.ID)
		return err
	})
	if ferr != nil {
		// The row stays building: recoverBuilds fails it at the next start.
		b.log.Error("record failed data version build", "err", ferr)
		return
	}
	b.cleanup()
}

// ---- preparing ----

// openSnapshots reads the composed images of every source.
func (b *build) openSnapshots() error {
	b.snaps = make([]*dataset.Snapshot, len(b.job.Sources))
	for i, src := range b.job.Sources {
		snap, err := b.s.datasets.OpenSnapshot(b.ctx, src.DatasetID, src.selection(),
			dataset.SnapshotOptions{AllowTrashedVersions: true})
		if err != nil {
			var de *dataset.Error
			if !errors.As(err, &de) {
				return err
			}
			// Deleted meanwhile: the precise message.
			if verr := validateFrozenSources(b.ctx, b.s.db, b.job.ProjectID, b.job.Sources, checkForBuild); verr != nil {
				return verr
			}
			if errors.Is(err, dataset.ErrNotFound) && len(src.Versions) > 0 {
				return newError(ErrConflict, "所选审图版本已被永久删除，无法重新生成，请新建数据版本")
			}
			return fromDatasetError(de, ErrConflict)
		}
		b.snaps[i] = snap
	}
	return nil
}

func (b *build) closeSnapshots() {
	for _, snap := range b.snaps {
		if snap != nil {
			snap.Close()
		}
	}
}

// prepare reads the annotation and checks the file of every image of the
// snapshots, and keeps those the filters take.
func (b *build) prepare() error {
	total := 0
	for _, snap := range b.snaps {
		total += len(snap.Images)
	}
	if err := b.setStage(StagePreparing, total); err != nil {
		return err
	}
	for si, snap := range b.snaps {
		for _, img := range snap.Images {
			if err := b.tick(1); err != nil {
				return err
			}
			bi, err := b.prepareImage(si, snap, img)
			if err != nil {
				return err
			}
			if bi != nil {
				bi.index = len(b.images)
				b.images = append(b.images, bi)
			}
		}
	}
	return nil
}

// prepareImage decides whether an image is taken; nil when it is skipped
// (counted).
func (b *build) prepareImage(source int, snap *dataset.Snapshot, img dataset.SnapshotImage) (*buildImage, error) {
	skipped := &b.stats.Skipped
	var objects []dataset.AnnotationObject
	var annW, annH int
	switch {
	case img.VersionID != nil:
		objects = img.VersionObjects
		if img.VersionWidth != nil && img.VersionHeight != nil {
			annW, annH = *img.VersionWidth, *img.VersionHeight
		}
	case img.AnnotationPath == nil:
		skipped.Unannotated++
		return nil, nil
	default:
		voc, err := readVOC(snap.Root, *img.AnnotationPath)
		if err != nil {
			skipped.Invalid++
			return nil, nil
		}
		objects, annW, annH = voc.Objects, voc.Width, voc.Height
	}
	if !dataset.IsTrainableImage(img.RelPath) {
		skipped.UnsupportedFormat++
		return nil, nil
	}
	if b.filter.hasUnlisted(objects) {
		skipped.Unlisted++
		return nil, nil
	}

	f, err := snap.Root.Open(filepath.FromSlash(img.RelPath))
	if err != nil {
		skipped.Missing++
		return nil, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	switch {
	case err != nil || !fi.Mode().IsRegular():
		skipped.Missing++
		return nil, nil
	case fi.Size() != img.SizeBytes || !sameFileTime(fi.ModTime(), img.ModTime):
		skipped.Changed++
		return nil, nil
	}
	w, h := 0, 0
	switch {
	case img.Width != nil && img.Height != nil && *img.Width > 0 && *img.Height > 0:
		w, h = *img.Width, *img.Height
	case annW > 0 && annH > 0:
		w, h = annW, annH
	default:
		if cfg, _, err := dataset.DecodeImageConfig(f); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			w, h = cfg.Width, cfg.Height
		}
	}
	if w <= 0 || h <= 0 {
		skipped.UnknownSize++
		return nil, nil
	}
	r := b.filter.objects(objects, w, h)
	if len(r.kept) == 0 && !b.filter.includeEmpty {
		skipped.Empty++
		return nil, nil
	}
	b.stats.DroppedObjects += r.dropped
	b.stats.DegenerateObjects += r.degenerate
	return &buildImage{source: source, rel: img.RelPath, sourceSize: fi.Size(), sourceMod: dbTime(fi.ModTime()),
		width: w, height: h, objects: r.kept, annotation: img.VersionID}, nil
}

// readVOC reads and parses a base annotation file through the dataset's
// root.
func readVOC(root *os.Root, rel string) (dataset.VOCFile, error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return dataset.VOCFile{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, dataset.MaxAnnotationBytes+1))
	if err != nil {
		return dataset.VOCFile{}, err
	}
	if len(data) > dataset.MaxAnnotationBytes {
		return dataset.VOCFile{}, errors.New("annotation file too large")
	}
	return dataset.DecodeVOC(data)
}

// hasUnlisted reports whether an object has a label neither in the class
// list nor ignored.
func (f labelFilter) hasUnlisted(objs []dataset.AnnotationObject) bool {
	for _, o := range objs {
		if !f.ignore[o.Name] && !f.has(o.Name) {
			return true
		}
	}
	return false
}

// ---- split ----

// split assigns the splits and the positions of the originals.
func (b *build) split() error {
	items := make([]splitItem, len(b.images))
	for i, img := range b.images {
		items[i] = splitItem{Source: img.source, DatasetID: b.job.Sources[img.source].DatasetID, RelPath: img.rel,
			Size: img.sourceSize}
	}
	assigned := splitImages(items, b.job.Config.Split)
	for i, img := range b.images {
		img.split, img.splitBy = assigned[i].Split, assigned[i].By
		img.position = i * b.stride
	}
	return splitRefusal(splitCounts(assigned))
}

// ---- linking ----

// linkImages stores the originals in the version folder with their label
// files and inserts their rows.
func (b *build) linkImages() error {
	if err := b.setStage(StageLinking, len(b.images)); err != nil {
		return err
	}
	b.namer = newPathNamer()
	for _, img := range b.images {
		img.path = b.namer.originalPath(b.job.Sources[img.source].DirName, img.rel)
	}
	ids := b.classIDs()
	var batch []*buildImage
	for _, img := range b.images {
		if err := b.tick(1); err != nil {
			return err
		}
		st, err := b.storeOriginal(img)
		switch {
		case errors.Is(err, errSourceChanged):
			b.stats.Skipped.Changed++
			continue
		case errors.Is(err, errSourceMissing):
			b.stats.Skipped.Missing++
			continue
		case err != nil:
			return err
		}
		img.stored, img.orientationFixed, img.size, img.modTime = st.stored, st.orientationFixed, st.size, dbTime(st.modTime)
		if err := b.writeLabels(img.path, img.objects, img.width, img.height, ids); err != nil {
			return err
		}
		switch st.stored {
		case StoredLink:
			b.stats.Storage.Linked++
		default:
			b.stats.Storage.Copied++
			b.stats.Storage.CopiedBytes += st.copied
		}
		if st.orientationFixed {
			b.stats.Storage.OrientationFixed++
		}
		batch = append(batch, img)
		if len(batch) == insertBatch {
			if err := b.insertOriginals(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := b.insertOriginals(batch); err != nil {
		return err
	}
	var c SplitCounts
	for _, img := range b.images {
		if img.inserted {
			c.add(img.split, 1)
		}
	}
	// Files that changed since they were prepared are left out: the split
	// may have lost its last val (or train) image.
	return splitRefusal(c)
}

// classIDs maps the class names to their YOLO ids.
func (b *build) classIDs() map[string]int { return b.filter.classes }

// writeLabels writes the YOLO label file of the image at the version path
// p (empty for a negative sample).
func (b *build) writeLabels(p string, objects []dataset.AnnotationObject, width, height int, ids map[string]int) error {
	name := filepath.Join(b.dir, filepath.FromSlash(dataset.UltralyticsLabelPath(p)))
	if err := b.mkdirFor(name); err != nil {
		return err
	}
	if err := os.WriteFile(name, labelFileLines(objects, width, height, ids), 0o644); err != nil {
		return fmt.Errorf("write label file: %w", err)
	}
	return nil
}

// imageRow is a row of data_version_images as insertRows sends it.
type imageRow struct {
	Position         int                        `json:"position"`
	Split            string                     `json:"split"`
	SplitBy          string                     `json:"split_by"`
	Path             string                     `json:"path"`
	SizeBytes        int64                      `json:"size_bytes"`
	ModTime          time.Time                  `json:"mod_time"`
	Width            int                        `json:"width"`
	Height           int                        `json:"height"`
	Objects          []dataset.AnnotationObject `json:"objects"`
	ObjectCount      int                        `json:"object_count"`
	Labels           map[string]int             `json:"labels"`
	DatasetID        int64                      `json:"dataset_id"`
	SourcePath       string                     `json:"source_path"`
	SourceSize       int64                      `json:"source_size"`
	SourceModTime    time.Time                  `json:"source_mod_time"`
	AnnotationSource *int64                     `json:"annotation_source"`
	AugmentedFrom    *int64                     `json:"augmented_from"`
	Augment          []string                   `json:"augment"`
	Stored           string                     `json:"stored"`
}

// originalRow is the row of an original.
func (b *build) originalRow(img *buildImage) imageRow {
	return imageRow{Position: img.position, Split: img.split, SplitBy: img.splitBy, Path: img.path,
		SizeBytes: img.size, ModTime: img.modTime, Width: img.width, Height: img.height, Objects: img.objects,
		ObjectCount: len(img.objects), Labels: objectLabels(img.objects),
		DatasetID: b.job.Sources[img.source].DatasetID, SourcePath: img.rel, SourceSize: img.sourceSize,
		SourceModTime: img.sourceMod, AnnotationSource: img.annotation, Augment: []string{}, Stored: img.stored}
}

func (b *build) insertOriginals(batch []*buildImage) error {
	if len(batch) == 0 {
		return nil
	}
	rows := make([]imageRow, len(batch))
	for i, img := range batch {
		rows[i] = b.originalRow(img)
	}
	ids, err := b.insertRows(rows)
	if err != nil {
		return err
	}
	for _, img := range batch {
		img.id, img.inserted = ids[img.position], true
		b.countOriginal(img)
	}
	return nil
}

// insertRows inserts image rows (guarded: the version must still be
// building) and returns their ids by position.
func (b *build) insertRows(rows []imageRow) (map[int]int64, error) {
	r, err := b.s.db.Query(b.ctx, `INSERT INTO data_version_images (version_id, position, split, split_by, path,
			size_bytes, mod_time, width, height, objects, object_count, labels, dataset_id, source_path, source_size,
			source_mod_time, annotation_source, augmented_from, augment, stored)
		SELECT $1, r.position, r.split, r.split_by, r.path, r.size_bytes, r.mod_time, r.width, r.height, r.objects,
			r.object_count, r.labels, r.dataset_id, r.source_path, r.source_size, r.source_mod_time,
			r.annotation_source, r.augmented_from, r.augment, r.stored
		FROM jsonb_to_recordset($2::jsonb) AS r (position integer, split text, split_by text, path text,
			size_bytes bigint, mod_time timestamptz, width integer, height integer, objects jsonb,
			object_count integer, labels jsonb, dataset_id bigint, source_path text, source_size bigint,
			source_mod_time timestamptz, annotation_source bigint, augmented_from bigint, augment jsonb, stored text)
		WHERE EXISTS (SELECT 1 FROM data_versions WHERE id = $1 AND status = 'building')
		RETURNING position, id`, b.job.ID, rows)
	if err != nil {
		return nil, b.insertError(err)
	}
	ids := map[int]int64{}
	var pos int
	var id int64
	if _, err := pgx.ForEachRow(r, []any{&pos, &id}, func() error {
		ids[pos] = id
		return nil
	}); err != nil {
		return nil, b.insertError(err)
	}
	if len(ids) != len(rows) {
		return nil, errBuildGone
	}
	return ids, nil
}

func (b *build) insertError(err error) error {
	if isForeignKeyViolation(err) {
		return errBuildGone
	}
	return fmt.Errorf("insert data version images: %w", err)
}

// countOriginal adds an inserted original to the counts and statistics.
func (b *build) countOriginal(img *buildImage) {
	b.counts.images++
	b.counts.objects += len(img.objects)
	b.perSrc[img.source]++
	b.written += img.size
	if len(img.objects) == 0 {
		b.stats.Negatives.add(img.split, 1)
	}
	if img.splitBy == SplitByFolder {
		b.stats.ByFolder++
	}
	for name, n := range objectLabels(img.objects) {
		b.labels.add(name, img.split, n)
	}
}

// ---- finishing ----

// finish writes the lists and data.yaml, then in one transaction (the row
// locked) computes the comparison, writes version.json and makes the
// version ready.
func (b *build) finish() error {
	if err := b.setStage(StageFinishing, 0); err != nil {
		return err
	}
	lists := map[string][]byte{}
	var counts SplitCounts
	add := func(split, p string) {
		lists[split] = append(lists[split], "./"+p+"\n"...)
		counts.add(split, 1)
	}
	copies := b.copies
	for _, img := range b.images {
		if !img.inserted {
			continue
		}
		add(img.split, img.path)
		for len(copies) > 0 && copies[0].orig == img {
			add(SplitTrain, copies[0].path)
			copies = copies[1:]
		}
	}
	files := map[string][]byte{classesFile: classesText(b.job.Classes),
		dataYAMLFile: nil, listFile(SplitTrain): lists[SplitTrain], listFile(SplitVal): lists[SplitVal]}
	if counts.Test > 0 {
		files[listFile(SplitTest)] = lists[SplitTest]
	}

	b.stats.Labels = b.labels.list
	b.stats.Storage.Generated = len(b.copies)
	for i := range b.job.Sources {
		b.job.Sources[i].ImageCount = b.perSrc[i]
		b.job.Sources[i].DatasetDeleted = false
	}
	ctx, cancel := context.WithTimeout(b.ctx, finishTimeout)
	defer cancel()
	err := pgx.BeginFunc(ctx, b.s.db, func(tx pgx.Tx) error {
		v := DataVersion{ID: b.job.ID, ProjectID: b.job.ProjectID, Version: b.job.Version, Classes: b.job.Classes,
			Config: b.job.Config, Sources: b.job.Sources, ImageCount: b.counts.images + len(b.copies),
			ObjectCount: b.counts.objects, TrainCount: counts.Train, ValCount: counts.Val, TestCount: counts.Test,
			AugmentedCount: len(b.copies), TotalBytes: b.written, CreatedAt: b.job.CreatedAt}
		var finished time.Time
		err := tx.QueryRow(ctx, `SELECT v.note, p.name, now() FROM data_versions v JOIN projects p ON p.id = v.project_id
			WHERE v.id = $1 AND v.status = 'building' FOR UPDATE OF v`, b.job.ID).Scan(&v.Note, &v.ProjectName, &finished)
		if errors.Is(err, pgx.ErrNoRows) {
			return errBuildGone
		}
		if err != nil {
			return err
		}
		finished = finished.UTC()
		v.FinishedAt = &finished
		for _, c := range b.copies {
			v.ObjectCount += len(c.objects)
		}
		if b.stats.Compare, err = compareVersions(ctx, tx, b.job.ID, b.job.ProjectID, b.job.Version); err != nil {
			return err
		}
		v.Stats = b.stats
		files[dataYAMLFile] = dataYAML(v.ProjectName, v.Version, v.Classes, counts.Test > 0)
		for _, name := range []string{classesFile, dataYAMLFile, listFile(SplitTrain), listFile(SplitVal), listFile(SplitTest)} {
			data, ok := files[name]
			if !ok {
				continue
			}
			if err := os.WriteFile(filepath.Join(b.dir, name), data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}
		if err := writeManifest(b.dir, newManifest(v)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE data_versions SET status = 'ready', stage = '', progress_done = 0,
				progress_total = 0, image_count = $2, object_count = $3, train_count = $4, val_count = $5,
				test_count = $6, augmented_count = $7, total_bytes = $8, stats = $9, sources = $10,
				finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'building'`, b.job.ID, v.ImageCount, v.ObjectCount, v.TrainCount, v.ValCount,
			v.TestCount, v.AugmentedCount, v.TotalBytes, v.Stats, v.Sources)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errBuildGone
		}
		b.counts.augmented = len(b.copies)
		return nil
	})
	if err != nil {
		return err
	}
	// Ready: the folder is the version's for good. A marker left behind
	// (the removal failed) is harmless: nothing clears a ready version's
	// folder.
	b.owned = false
	if err := os.Remove(filepath.Join(b.dir, pendingMarker)); err != nil {
		b.log.Warn("data version: remove the .pending marker", "err", err)
	}
	return nil
}
