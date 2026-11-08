package dataversion

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// OWNER: backend-core agent. Validating inputs, previews, creating,
// retrying, editing the note and deleting versions (dispatching to the
// recycle bin for ready versions).

// maxVersionSkips bounds how many numbers Create skips because their
// folder already exists (like model versions).
const maxVersionSkips = 100

// Preview counts what a version created with in would contain, from the
// datasets' index (no files are read). in.Note is ignored.
func (s *Service) Preview(ctx context.Context, in CreateInput) (Preview, error) {
	req, err := normalizeInput(in, false)
	if err != nil {
		return Preview{}, err
	}
	p, _, err := s.preview(ctx, req, false)
	return p, err
}

// Annotation states of an image as the filters see it.
const (
	annotationNone      = "none"
	annotationInvalid   = "invalid"
	annotationEmpty     = "empty"
	annotationAnnotated = "annotated"
)

// indexAnnotation is the effective annotation of a snapshot image by the
// index: its review version's label counts, else the base's.
func indexAnnotation(img dataset.SnapshotImage) (labels map[string]int, state string) {
	if img.VersionID != nil {
		return img.VersionLabels, annotationAnnotated
	}
	switch img.AnnotationState {
	case annotationAnnotated:
		return img.BaseLabels, annotationAnnotated
	case annotationEmpty:
		return map[string]int{}, annotationAnnotated
	case annotationInvalid:
		return nil, annotationInvalid
	}
	return nil, annotationNone
}

// indexSize is the image size the index knows: the dataset's, else the
// review version's (0 when unknown).
func indexSize(img dataset.SnapshotImage) (int, int) {
	if img.Width != nil && img.Height != nil && *img.Width > 0 && *img.Height > 0 {
		return *img.Width, *img.Height
	}
	if img.VersionWidth != nil && img.VersionHeight != nil && *img.VersionWidth > 0 && *img.VersionHeight > 0 {
		return *img.VersionWidth, *img.VersionHeight
	}
	return 0, 0
}

// previewImage is an image a preview would take.
type previewImage struct {
	kept    map[string]int
	objects int
}

// preview validates req against the database (forCreate: also the class
// list version) and counts the version it would make from the index. It
// returns the sources as Create stores them.
func (s *Service) preview(ctx context.Context, req createRequest, forCreate bool) (Preview, []Source, error) {
	var classes []Class
	err := s.db.QueryRow(ctx, "SELECT classes FROM projects WHERE id = $1 AND deleted_at IS NULL", req.projectID).
		Scan(&classes)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preview{}, nil, errProjectNotFound
	}
	if err != nil {
		return Preview{}, nil, err
	}
	if classes == nil {
		classes = []Class{}
	}
	if forCreate {
		if err := checkClasses(classes, req.classVersion); err != nil {
			return Preview{}, nil, err
		}
	}
	if err := checkIgnoredNotClasses(req.config.IgnoreLabels, classes); err != nil {
		return Preview{}, nil, err
	}
	sources, err := validateNewSources(ctx, s.db, req.projectID, req.sources)
	if err != nil {
		return Preview{}, nil, err
	}

	filter := newLabelFilter(classes, req.config)
	p := Preview{Classes: classes, ClassVersion: classVersion(classes), Sources: []PreviewSource{},
		Labels: []LabelStats{}, UnlistedLabels: []UnlistedLabel{}}
	var items []splitItem
	var taken []previewImage
	unlisted := map[string]*UnlistedLabel{}
	for si, src := range sources {
		snap, err := s.datasets.OpenSnapshot(ctx, src.DatasetID, src.selection(), dataset.SnapshotOptions{IndexOnly: true})
		if err != nil {
			return Preview{}, nil, s.newSourceError(ctx, req, err)
		}
		ps := PreviewSource{DatasetID: src.DatasetID, DatasetName: src.DatasetName, SelectedCount: len(snap.Images)}
		for _, img := range snap.Images {
			if img.VersionID != nil {
				ps.FromVersions++
			}
			labels, state := indexAnnotation(img)
			switch state {
			case annotationNone:
				p.Skipped.Unannotated++
				continue
			case annotationInvalid:
				p.Skipped.Invalid++
				continue
			}
			for name, n := range labels {
				if !filter.has(name) && n > 0 {
					ul := unlisted[name]
					if ul == nil {
						ul = &UnlistedLabel{Name: name}
						unlisted[name] = ul
					}
					ul.Objects += n
					ul.Images++
				}
			}
			if !dataset.IsTrainableImage(img.RelPath) {
				p.Skipped.UnsupportedFormat++
				continue
			}
			r := filter.counts(labels)
			if r.unlisted {
				p.Skipped.Unlisted++
				continue
			}
			if w, _ := indexSize(img); w == 0 {
				p.Skipped.UnknownSize++
				continue
			}
			if r.objects == 0 && !filter.includeEmpty {
				p.Skipped.Empty++
				continue
			}
			p.DroppedObjects += r.dropped
			items = append(items, splitItem{Source: si, DatasetID: src.DatasetID, RelPath: img.RelPath, Size: img.SizeBytes})
			taken = append(taken, previewImage{kept: r.kept, objects: r.objects})
			ps.ImageCount++
		}
		p.Sources = append(p.Sources, ps)
	}

	assigned := splitImages(items, req.config.Split)
	labelStats := newLabelStats(classes)
	for i, a := range assigned {
		img := taken[i]
		sp := p.Splits.of(a.Split)
		sp.Images++
		sp.Objects += img.objects
		if img.objects == 0 {
			sp.Negatives++
		}
		if a.By == SplitByFolder {
			sp.ByFolder++
			p.Sources[items[i].Source].ByFolder++
		}
		p.ImageCount++
		p.ObjectCount += img.objects
		for name, n := range img.kept {
			labelStats.add(name, a.Split, n)
		}
		if aug := req.config.Augmentation; aug != nil && a.Split == SplitTrain && (img.objects > 0 || !aug.OnlyWithObjects) {
			p.AugmentedCount += aug.Copies
		}
	}
	p.Labels = labelStats.list
	for _, name := range slices.Sorted(maps.Keys(unlisted)) {
		p.UnlistedLabels = append(p.UnlistedLabels, *unlisted[name])
	}
	slices.SortStableFunc(p.UnlistedLabels, func(a, b UnlistedLabel) int { return cmp.Compare(b.Objects, a.Objects) })
	if err := s.markOtherProjects(ctx, req.projectID, sources, p.UnlistedLabels); err != nil {
		return Preview{}, nil, err
	}
	if p.Compare, err = s.previewCompare(ctx, req.projectID, items, assigned); err != nil {
		return Preview{}, nil, err
	}
	return p, sources, nil
}

// of returns the counts of a split.
func (ps *PreviewSplits) of(split string) *PreviewSplit {
	switch split {
	case SplitVal:
		return &ps.Val
	case SplitTest:
		return &ps.Test
	default:
		return &ps.Train
	}
}

// labelStatsList is the per-class statistics in class list order.
type labelStatsList struct {
	list  []LabelStats
	index map[string]int
}

func newLabelStats(classes []Class) labelStatsList {
	l := labelStatsList{list: make([]LabelStats, len(classes)), index: map[string]int{}}
	for i, c := range classes {
		l.list[i] = LabelStats{Name: c.Name, Color: c.Color}
		l.index[c.Name] = i
	}
	return l
}

// add counts n objects of a class in one image of a split.
func (l labelStatsList) add(name, split string, n int) {
	i, ok := l.index[name]
	if !ok || n == 0 {
		return
	}
	oi := &l.list[i].Train
	switch split {
	case SplitVal:
		oi = &l.list[i].Val
	case SplitTest:
		oi = &l.list[i].Test
	}
	oi.Objects += n
	oi.Images++
}

// checkClasses refuses an empty class list and one that is not the
// version the client showed.
func checkClasses(classes []Class, version string) error {
	if len(classes) == 0 {
		return errNoClasses
	}
	if classVersion(classes) != version {
		return errClassesChanged
	}
	return nil
}

// newSourceError explains an error of OpenSnapshot for a create or
// preview: the sources are checked again for the precise message (a
// dataset or review version deleted meanwhile); otherwise the dataset
// package's message is kept.
func (s *Service) newSourceError(ctx context.Context, req createRequest, err error) error {
	var de *dataset.Error
	if !errors.As(err, &de) {
		return err
	}
	if _, verr := validateNewSources(ctx, s.db, req.projectID, req.sources); verr != nil {
		return verr
	}
	return fromDatasetError(de, ErrInvalid)
}

// fromDatasetError turns a user-facing error of package dataset into ours;
// notFound is the kind its ErrNotFound becomes (a field of a request
// naming something gone is ErrInvalid).
func fromDatasetError(de *dataset.Error, notFound error) *Error {
	kind := ErrConflict
	switch {
	case errors.Is(de, dataset.ErrNotFound):
		kind = notFound
	case errors.Is(de, dataset.ErrInvalid):
		kind = ErrInvalid
	case errors.Is(de, dataset.ErrInsufficientStorage):
		kind = ErrInsufficientStorage
	}
	return &Error{Kind: kind, Message: de.Message}
}

// markOtherProjects sets OtherProject on the unlisted labels that are in
// the class list of another project in use sharing a selected dataset.
func (s *Service) markOtherProjects(ctx context.Context, projectID int64, sources []Source, labels []UnlistedLabel) error {
	if len(labels) == 0 {
		return nil
	}
	ids := make([]int64, len(sources))
	for i, src := range sources {
		ids[i] = src.DatasetID
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT c.name FROM projects p
		JOIN project_datasets pd ON pd.project_id = p.id,
		jsonb_to_recordset(p.classes) AS c (name text)
		WHERE pd.dataset_id = ANY($1) AND p.id <> $2 AND p.deleted_at IS NULL`, ids, projectID)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for i := range labels {
		labels[i].OtherProject = slices.Contains(names, labels[i].Name)
	}
	return nil
}

// Create validates in, numbers the version and queues its build.
func (s *Service) Create(ctx context.Context, in CreateInput) (DataVersion, error) {
	req, err := normalizeInput(in, true)
	if err != nil {
		return DataVersion{}, err
	}
	// The preview refuses versions that cannot be trained (advisory: the
	// build counts again from the files).
	p, _, err := s.preview(ctx, req, true)
	if err != nil {
		return DataVersion{}, err
	}
	if err := splitRefusal(SplitCounts{Train: p.Splits.Train.Images, Val: p.Splits.Val.Images,
		Test: p.Splits.Test.Images}); err != nil {
		return DataVersion{}, err
	}
	if p.AugmentedCount > maxAugmented {
		return DataVersion{}, newError(ErrInvalid, "增强图片最多 %d 张，现在约为 %d 张，请减少副本数",
			maxAugmented, p.AugmentedCount)
	}

	datasetIDs := make([]int64, len(req.sources))
	for i, src := range req.sources {
		datasetIDs[i] = src.DatasetID
	}
	slices.Sort(datasetIDs)
	var id int64
	var version int
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Lock order: the datasets first (KEY SHARE: deleting one waits, or
		// is waited for), then the project. Deleting a dataset locks it and
		// then its projects, so the other order could deadlock.
		rows, err := tx.Query(ctx, `SELECT id FROM datasets WHERE id = ANY($1) AND deleted_at IS NULL
			ORDER BY id FOR KEY SHARE`, datasetIDs)
		if err != nil {
			return err
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var classes []Class
		var dirName string
		if version, classes, dirName, err = s.allocateNumber(ctx, tx, req.projectID); err != nil {
			return err
		}
		if err := checkClasses(classes, req.classVersion); err != nil {
			return err
		}
		if err := checkIgnoredNotClasses(req.config.IgnoreLabels, classes); err != nil {
			return err
		}
		// The project row is locked by the number: the links cannot change.
		sources, err := validateNewSources(ctx, tx, req.projectID, req.sources)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO data_versions (project_id, version, dir_name, note, status, classes,
				config, sources)
			VALUES ($1, $2, $3, $4, 'queued', $5, $6, $7) RETURNING id`,
			req.projectID, version, dirName, req.note, classes, req.config, sources).Scan(&id)
	})
	if err != nil {
		return DataVersion{}, err
	}
	s.builder.notify()
	s.log.Info("data version queued", "version_id", id, "project_id", req.projectID, "version", version,
		"sources", len(req.sources), "images", p.ImageCount)
	return s.Get(ctx, id)
}

// allocateNumber gives the next number of the project, in the caller's
// transaction (the UPDATE locks the project row until it ends). Numbers
// whose folder exists (a database restored from an older backup gives
// them again) are skipped.
func (s *Service) allocateNumber(ctx context.Context, tx pgx.Tx, projectID int64) (int, []Class, string, error) {
	for skips := 0; ; skips++ {
		var version int
		var classes []Class
		err := tx.QueryRow(ctx, `UPDATE projects SET data_version_seq = data_version_seq + 1, updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL RETURNING data_version_seq, classes`, projectID).Scan(&version, &classes)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, "", errProjectNotFound
		}
		if err != nil {
			return 0, nil, "", err
		}
		dirName := versionDirName(projectID, version)
		dir := s.versionDir(dirName)
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			if classes == nil {
				classes = []Class{}
			}
			return version, classes, dirName, nil
		} else if err != nil {
			return 0, nil, "", fmt.Errorf("check data version folder: %w", err)
		}
		s.log.Warn("data version folder already exists; skipping its number", "project_id", projectID,
			"version", version, "dir_name", dirName)
		if skips == maxVersionSkips {
			return 0, nil, "", newError(ErrConflict, "数据版本文件夹 %s 等已经存在（可能来自备份），请先移走它们", dirName)
		}
	}
}

// Retry queues a failed version again, with the same configuration.
func (s *Service) Retry(ctx context.Context, id int64) (DataVersion, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		v, err := queryVersion(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if v.Status != StatusFailed {
			return errNotFailed
		}
		if err := validateFrozenSources(ctx, tx, v.ProjectID, v.Sources, checkForRetry); err != nil {
			return err
		}
		for i := range v.Sources {
			v.Sources[i].ImageCount = 0
		}
		if _, err := tx.Exec(ctx, "DELETE FROM data_version_images WHERE version_id = $1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE data_versions SET status = 'queued', error = '', stage = '',
				progress_done = 0, progress_total = 0, started_at = NULL, finished_at = NULL, queued_at = now(),
				image_count = 0, object_count = 0, train_count = 0, val_count = 0, test_count = 0,
				augmented_count = 0, total_bytes = 0, stats = '{}', sources = $2, updated_at = now()
			WHERE id = $1 AND status = 'failed'`, id, v.Sources)
		return err
	})
	if err != nil {
		return DataVersion{}, err
	}
	s.builder.notify()
	s.log.Info("data version queued again", "version_id", id)
	return s.Get(ctx, id)
}

// Update changes the note of a version (in any state). The version.json
// of a ready version is rewritten while its row is locked, so that it
// cannot interleave with another edit (or the end of the build).
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (DataVersion, error) {
	if in.Note != nil {
		note, err := dataset.NormalizeVersionNote(*in.Note)
		if err != nil {
			return DataVersion{}, invalidFrom(err)
		}
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			v, err := queryVersion(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `UPDATE data_versions SET note = $2, updated_at = now() WHERE id = $1
				RETURNING note, updated_at`, id, note).Scan(&v.Note, &v.UpdatedAt); err != nil {
				return err
			}
			if v.Status != StatusReady {
				return nil
			}
			var dirName string
			if err := tx.QueryRow(ctx, "SELECT dir_name FROM data_versions WHERE id = $1", id).Scan(&dirName); err != nil {
				return err
			}
			return writeManifest(s.versionDir(dirName), newManifest(v))
		})
		if err != nil {
			return DataVersion{}, err
		}
	}
	return s.Get(ctx, id)
}

// Delete moves a ready version to the recycle bin (moveToBin) and removes
// a failed or queued one for good; a building one is canceled first.
func (s *Service) Delete(ctx context.Context, id int64) error {
	for attempt := 0; ; attempt++ {
		// A version not built yet: deleting the row is all it takes. The
		// builder owns the folder of a building one; it stops at its next
		// write (or at once, canceled) and clears the folder.
		var status string
		err := s.db.QueryRow(ctx, `DELETE FROM data_versions
			WHERE id = $1 AND status IN ('queued', 'building') AND deleted_at IS NULL RETURNING status`, id).Scan(&status)
		if err == nil {
			if status == StatusBuilding {
				s.builder.cancelBuild(id)
			}
			s.log.Info("data version deleted before it was built", "version_id", id, "status", status)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var v DataVersion
		var dirName string
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if v, err = queryVersion(ctx, tx, id, true); err != nil {
				return err
			}
			if v.Status != StatusFailed {
				return nil
			}
			return tx.QueryRow(ctx, "DELETE FROM data_versions WHERE id = $1 RETURNING dir_name", id).Scan(&dirName)
		})
		if err != nil {
			return err
		}
		switch v.Status {
		case StatusReady:
			// After the transaction: moveToBin is its own guarded
			// statement. Not found: deleted or moved meanwhile, look again.
			err := s.moveToBin(ctx, id)
			if !errors.Is(err, ErrNotFound) || attempt == 2 {
				return err
			}
			continue
		case StatusFailed:
			s.discardOwned(id, s.versionDir(dirName))
			s.discardThumbnails(id)
			s.log.Info("failed data version deleted", "version_id", id)
			return nil
		}
		// Queued again (a retry) or claimed meanwhile: once more.
		if attempt == 2 {
			return fmt.Errorf("delete data version %d: its state keeps changing", id)
		}
	}
}

// DeleteProjectVersions prepares the deletion of a project: its ready
// versions go to the recycle bin; failed, queued and building ones are
// removed (a build is canceled), each like Delete. It never answers a
// conflict.
func (s *Service) DeleteProjectVersions(ctx context.Context, projectID int64) error {
	rows, err := s.db.Query(ctx, `SELECT id FROM data_versions WHERE project_id = $1 AND deleted_at IS NULL
		ORDER BY id`, projectID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("delete data version %d: %w", id, err)
		}
	}
	return nil
}
