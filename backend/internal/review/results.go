package review

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// Review results (审图结果): what the local reviewer exports from a package
// (review.json of kind results plus the VOC files of its images), uploaded
// back as a zip. The original package zipped again is accepted too: its
// images are ignored, and its VOC files count only where their content
// changed. Not the folder the local reviewer worked in, though: the
// statuses set there are only in its progress file.
//
// Every image of the manifest is compared with its state in the package
// (the base_* fields): a status other than base_status, or a VOC file whose
// version is not base_annotation_version, is a local change. Changes are
// applied to an image the platform has not changed since the package was
// made (same revision). An image changed on both sides is a conflict,
// unless the platform has what the upload brings already (uploading the
// same results again changes nothing) or the upload overwrites. Images
// are applied one by one: a problem with one does not stop the others.
//
// The local reviewer keeps the package's base_* fields when it exports
// again, so results of one package can be uploaded more than once while
// the review goes on. An image whose platform state an earlier upload of
// the same package made (and that nobody changed since, see
// imageRow.continues) continues from there: the local state is compared
// with the platform's state now, so a change back to the package's state
// counts too, and it is never a conflict. A package zipped again has no
// statuses of its own; only its VOC files count then.

// MaxResultsBytes bounds an uploaded results zip (4 GiB).
const MaxResultsBytes = 4 << 30

// maxManifestBytes bounds review.json in a results zip.
const maxManifestBytes = 256 << 20

// manifestQueryBatch bounds the image IDs of one query while applying
// results.
const manifestQueryBatch = 5000

// Upload problem kinds.
const (
	ProblemConflict = "conflict"
	ProblemInvalid  = "invalid"
	ProblemUnknown  = "unknown"
	ProblemFailed   = "failed"  // merges
	ProblemSkipped  = "skipped" // merges: the review status changed meanwhile
)

const (
	msgUploadConflict = "平台上的这张图片在下载审图包之后被修改过"
	msgUploadUnknown  = "这张图片已不在审图任务中（可能已随数据集或重新扫描被移除）"
)

// errUploadConflict refuses applying an image that changed on both sides.
var errUploadConflict = &Error{Kind: ErrConflict, Message: msgUploadConflict}

// ResultsInput is an uploaded results zip.
type ResultsInput struct {
	FileName string // as the user chose it, for the upload history
	// Overwrite applies local changes also to images changed on the
	// platform since the package was downloaded.
	Overwrite bool
	Size      int64     // the length of Body (the request's Content-Length)
	Body      io.Reader // the zip
}

// resultsBody marks errors of reading an upload body, so they are not
// taken for disk errors.
type resultsBody struct{ r io.Reader }

type resultsBodyError struct{ err error }

func (e *resultsBodyError) Error() string { return e.err.Error() }
func (e *resultsBodyError) Unwrap() error { return e.err }

func (b resultsBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		var re *Error
		if !errors.As(err, &re) { // the size limit, from the HTTP layer
			err = &resultsBodyError{err}
		}
	}
	return n, err
}

// UploadResults applies results uploaded from the local reviewer to the
// review (see the top of this file) and records the upload with what it
// did. The zip is staged in tmp/review-uploads/ while it is read; once it
// is received, the client going away no longer stops the work.
func (s *Service) UploadResults(ctx context.Context, reviewID int64, in ResultsInput) (Upload, error) {
	name := cleanLabel(path.Base(strings.ReplaceAll(in.FileName, `\`, "/")), maxLabelRunes)
	if name == "" || name == "." || name == "/" {
		name = "审图结果.zip"
	}
	switch {
	case in.Size == 0:
		return Upload{}, &Error{Kind: ErrInvalid, Message: "上传的文件是空的"}
	case in.Size < 0:
		return Upload{}, &Error{Kind: ErrBadRequest, Message: "缺少上传文件的大小（Content-Length）"}
	case in.Size > MaxResultsBytes:
		return Upload{}, errResultsTooLarge
	}
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return Upload{}, err
	}
	if err := s.checkFreeSpace(in.Size); err != nil {
		return Upload{}, err
	}
	staged, err := s.stageResults(in.Size, in.Body)
	if err != nil {
		return Upload{}, err
	}
	defer func() {
		if err := os.Remove(staged); err != nil {
			s.log.Warn("remove staged review results", "path", staged, "err", err)
		}
		s.busy.remove(staged)
	}()

	ctx, cancel := s.detach(ctx)
	defer cancel()
	zr, err := zip.OpenReader(staged)
	if err != nil {
		return Upload{}, &Error{Kind: ErrInvalid, Message: "上传的文件不是有效的 zip 压缩包"}
	}
	defer zr.Close()
	res, err := s.readResults(reviewID, &zr.Reader)
	if err != nil {
		return Upload{}, err
	}
	up := Upload{ReviewID: reviewID, FileName: name, Overwrite: in.Overwrite, Problems: []Problem{}}
	if err := s.applyResults(ctx, reviewID, res, in.Overwrite, &up); err != nil {
		return Upload{}, err
	}
	saved, err := scanUpload(s.db.QueryRow(ctx, `INSERT INTO review_uploads (review_id, file_name, overwrite, summary, problems)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+uploadColumns, reviewID, up.FileName, up.Overwrite, up.Summary, up.Problems))
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" { // deleted meanwhile
		return Upload{}, errReviewNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	sum := saved.Summary
	s.log.Info("review results uploaded", "review_id", reviewID, "upload_id", saved.ID, "kind", res.manifest.Kind,
		"images", sum.Images, "applied", sum.Applied, "status_changes", sum.StatusChanges,
		"annotation_changes", sum.AnnotationChanges, "unchanged", sum.Unchanged, "conflicts", sum.Conflicts,
		"invalid", sum.Invalid, "unknown", sum.Unknown, "overwrite", in.Overwrite)
	return saved, nil
}

var errResultsTooLarge = newError(ErrTooLarge, "审图结果不能超过 %d GiB", MaxResultsBytes>>30)

// stageResults writes an upload body of size bytes to a new file in
// tmp/review-uploads/, marked busy (the caller removes the file and the
// mark).
func (s *Service) stageResults(size int64, body io.Reader) (string, error) {
	dir := s.uploadsTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create review upload folder: %w", err)
	}
	p := filepath.Join(dir, strings.ToLower(rand.Text())+".zip")
	s.busy.add(p)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		s.busy.remove(p)
		return "", fmt.Errorf("stage review results: %w", err)
	}
	n, err := io.Copy(f, resultsBody{io.LimitReader(body, size+1)})
	var be *resultsBodyError
	switch {
	case errors.As(err, &be):
		err = &Error{Kind: ErrBadRequest, Message: "上传数据接收不完整，请重试"}
	case err != nil:
		var re *Error
		if !errors.As(err, &re) {
			err = fmt.Errorf("stage review results: %w", err)
		}
	case n != size:
		err = newError(ErrBadRequest, "上传的数据与 Content-Length 不一致（收到 %d 字节，应为 %d 字节）", n, size)
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("stage review results: %w", cerr)
	}
	if err != nil {
		_ = os.Remove(p)
		s.busy.remove(p)
		return "", err
	}
	return p, nil
}

// results is an opened results (or package) zip.
type results struct {
	manifest *reviewfile.Manifest
	prefix   string               // of review.json, which manifest paths are relative to
	files    map[string]*zip.File // by name, and by reviewfile.FoldKey of the name
}

// entry returns the zip entry of the manifest path rel, nil if none.
func (r *results) entry(rel string) *zip.File {
	if f := r.files[r.prefix+rel]; f != nil {
		return f
	}
	return r.files[reviewfile.FoldKey(r.prefix+rel)]
}

// readResults finds and reads review.json in a zip and checks that it
// belongs to the review.
func (s *Service) readResults(reviewID int64, zr *zip.Reader) (*results, error) {
	r := &results{files: make(map[string]*zip.File, 2*len(zr.File))}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() { // also an entry with a folder's mode and no "/" at its end
			continue
		}
		// Zip tools of Chinese Windows write GBK names; some Windows tools
		// write "\" between folders.
		name := strings.ReplaceAll(dataset.DecodeEntryName(f.Name), `\`, "/")
		names = append(names, name)
		if r.files[name] == nil {
			r.files[name] = f
		}
		if key := reviewfile.FoldKey(name); r.files[key] == nil {
			r.files[key] = f
		}
	}
	prefix, err := reviewfile.ManifestPrefix(names)
	if err != nil {
		return nil, manifestProblem(err)
	}
	r.prefix = prefix
	f := r.files[prefix+reviewfile.ManifestFile]
	if f == nil {
		return nil, &Error{Kind: ErrInvalid, Message: "压缩包中没有 review.json，这不是 trainMaster 的审图包或审图结果"}
	}
	rc, err := f.Open()
	if err != nil {
		return nil, newError(ErrInvalid, "压缩包中的 review.json 无法读取：%v", err)
	}
	defer rc.Close()
	m, err := reviewfile.ReadManifest(rc, maxManifestBytes)
	if err != nil {
		var me *reviewfile.ManifestError
		if errors.As(err, &me) {
			return nil, manifestProblem(err)
		}
		return nil, newError(ErrInvalid, "压缩包中的 review.json 无法读取：%v", err)
	}
	if m.Review.ID != reviewID {
		what := "审图结果"
		if m.Kind == reviewfile.KindPackage {
			what = "审图包"
		}
		return nil, newError(ErrInvalid, "这是审图任务 #%d 的%s，不是这个任务的", m.Review.ID, what)
	}
	r.manifest = m
	if m.Kind == reviewfile.KindPackage && r.entry(reviewfile.ProgressFile) != nil {
		// The folder the local reviewer works in, zipped: the statuses set
		// there are only in its progress file, which is not read here.
		return nil, &Error{Kind: ErrInvalid, Message: "这是在本地审图程序中打开过的审图包文件夹，其中标记的审核状态只保存在本地审图程序中，" +
			"不能这样上传：请在本地审图程序中点“导出审图结果”，再上传导出的“…-审图结果.zip”"}
	}
	return r, nil
}

func manifestProblem(err error) error {
	var me *reviewfile.ManifestError
	if errors.As(err, &me) {
		return &Error{Kind: ErrInvalid, Message: me.Message}
	}
	return err
}

// localChange is what an uploaded image changes, compared with the state
// it started from (see readLocalChange).
type localChange struct {
	status     string                     // the new status; "" unchanged
	objects    []dataset.AnnotationObject // the new annotation, normalized, if setObjects
	setObjects bool
}

// applyResults applies the images of res one by one and fills in the
// summary and problems of up.
func (s *Service) applyResults(ctx context.Context, reviewID int64, res *results, overwrite bool, up *Upload) error {
	images := res.manifest.Images
	packageID := res.manifest.PackageID
	up.Summary.Images = len(images)
	problem := func(img *reviewfile.ManifestImage, kind, msg string) {
		if len(up.Problems) < MaxProblems {
			up.Problems = append(up.Problems, Problem{Path: img.Path, Kind: kind, Message: msg})
		}
	}
	for start := 0; start < len(images); start += manifestQueryBatch {
		batch := images[start:min(start+manifestQueryBatch, len(images))]
		known, err := s.knownImages(ctx, reviewID, packageID, batch)
		if err != nil {
			return err
		}
		for i := range batch {
			img := &batch[i]
			if err := ctx.Err(); err != nil {
				return err
			}
			k, ok := known[img.ID]
			switch {
			case !ok:
				up.Summary.Unknown++
				problem(img, ProblemUnknown, msgUploadUnknown)
				continue
			case k.imageID != img.ImageID || k.annotationPath != img.Annotation:
				up.Summary.Invalid++
				problem(img, ProblemInvalid, "review.json 中这张图片的信息与审图任务不一致（图片 ID 或标注文件路径不同）")
				continue
			}
			// Compared with the package, or for an image continuing from an
			// earlier upload of this package, with the platform now (a
			// package zipped again has the package's statuses).
			baseStatus, baseVersion := img.BaseStatus, img.BaseAnnotationVersion
			if k.continues {
				baseVersion = k.annotationVersion
				if res.manifest.Kind == reviewfile.KindResults {
					baseStatus = k.status
				}
			}
			lc, msg := readLocalChange(res, img, baseStatus, baseVersion)
			if msg != "" {
				up.Summary.Invalid++
				problem(img, ProblemInvalid, msg)
				continue
			}
			if lc.status == "" && !lc.setObjects {
				up.Summary.Unchanged++
				continue
			}
			what, err := s.applyResult(ctx, reviewID, packageID, img, lc, overwrite)
			var re *Error
			switch {
			case errors.Is(err, errUploadConflict):
				up.Summary.Conflicts++
				problem(img, ProblemConflict, msgUploadConflict)
			case errors.Is(err, errImageNotFound): // removed meanwhile
				up.Summary.Unknown++
				problem(img, ProblemUnknown, msgUploadUnknown)
			case errors.Is(err, errReviewNotFound):
				return err
			case errors.As(err, &re) && errors.Is(err, ErrInvalid):
				up.Summary.Invalid++
				problem(img, ProblemInvalid, re.Message)
			case errors.As(err, &re):
				up.Summary.Conflicts++
				problem(img, ProblemConflict, re.Message)
			case err != nil:
				return err
			case !what.any():
				up.Summary.Unchanged++
			default:
				up.Summary.Applied++
				if what.status {
					up.Summary.StatusChanges++
				}
				if what.annotation {
					up.Summary.AnnotationChanges++
				}
			}
		}
	}
	return nil
}

// knownImage is what must match between an uploaded image and the review,
// and the image's state on the platform.
type knownImage struct {
	imageID        int64
	annotationPath string
	// The state now, and whether an earlier upload of the package made it
	// (see imageRow.continues).
	status, annotationVersion string
	continues                 bool
}

// knownImages returns the images of batch that are still in the review,
// by review image ID; packageID is the package of the upload.
func (s *Service) knownImages(ctx context.Context, reviewID int64, packageID string,
	batch []reviewfile.ManifestImage) (map[int64]knownImage, error) {
	ids := make([]int64, len(batch))
	for i, img := range batch {
		ids[i] = img.ID
	}
	rows, err := s.db.Query(ctx, `SELECT id, image_id, annotation_path,
			$3 <> '' AND coalesce(upload_package_id = $3 AND upload_revision = revision, false),
			status, annotation_version
		FROM review_images WHERE review_id = $1 AND id = ANY($2)`, reviewID, ids, packageID)
	if err != nil {
		return nil, err
	}
	known := make(map[int64]knownImage, len(ids))
	var id int64
	var k knownImage
	scans := []any{&id, &k.imageID, &k.annotationPath, &k.continues, &k.status, &k.annotationVersion}
	_, err = pgx.ForEachRow(rows, scans, func() error {
		known[id] = k
		return nil
	})
	return known, err
}

// readLocalChange compares an uploaded image with the state it started
// from: the status baseStatus and the VOC file of version baseVersion
// (those of the package, see applyResults). A VOC file that cannot be used
// makes the whole image invalid (msg says why).
func readLocalChange(res *results, img *reviewfile.ManifestImage, baseStatus, baseVersion string) (lc localChange, msg string) {
	if img.Status != baseStatus {
		lc.status = img.Status
	}
	f := res.entry(img.Annotation)
	if f == nil {
		return lc, ""
	}
	if f.UncompressedSize64 > dataset.MaxAnnotationBytes {
		return lc, fmt.Sprintf("标注文件 %s 超过 %d MiB", img.Annotation, dataset.MaxAnnotationBytes>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return lc, fmt.Sprintf("标注文件 %s 无法读取：%v", img.Annotation, err)
	}
	data, err := io.ReadAll(io.LimitReader(rc, dataset.MaxAnnotationBytes+1))
	rc.Close()
	if err != nil {
		return lc, fmt.Sprintf("标注文件 %s 无法读取（压缩包可能已损坏）：%v", img.Annotation, err)
	}
	if reviewfile.AnnotationVersion(data) == baseVersion {
		return lc, ""
	}
	voc, err := reviewfile.DecodeAnnotation(data)
	if err != nil {
		return lc, dataset.AnnotationErrorMessage(err)
	}
	objects, err := normalizeObjects(voc.Objects, img.Width, img.Height)
	if err != nil {
		var re *Error
		if errors.As(err, &re) {
			return lc, "标注无效：" + re.Message
		}
		return lc, "标注无效：" + err.Error()
	}
	lc.objects, lc.setObjects = objects, true
	return lc, ""
}

// applyResult applies the local change of one image, uploaded from the
// package packageID, under its lock and reports what changed.
// errUploadConflict: the image changed on both sides.
func (s *Service) applyResult(ctx context.Context, reviewID int64, packageID string, img *reviewfile.ManifestImage,
	lc localChange, overwrite bool) (changed, error) {
	_, what, err := s.changeImageRow(ctx, reviewID, img.ID, func(r imageRow, a annotationFile) (imageChange, error) {
		// The revision the change makes is noted with the package, so that a
		// later upload of it continues from there.
		ch := imageChange{uploadPackageID: packageID}
		// Only what differs from the platform now is a change.
		if lc.status != "" && lc.status != r.Status {
			ch.status = lc.status
		}
		if lc.setObjects && (a.err != "" || a.data == nil || !reviewfile.SameObjects(a.file.Objects, lc.objects)) {
			ch.objects, ch.setObjects = lc.objects, true
		}
		if ch.status == "" && !ch.setObjects {
			return ch, nil // the platform has it already
		}
		// Changed on the platform since the download; unless by an earlier
		// upload of this package, which this one continues.
		if r.Revision != img.BaseRevision && !overwrite && !r.continues(packageID) {
			return ch, errUploadConflict
		}
		if ch.setObjects {
			// Normalized for the size the manifest gave; the platform's counts.
			objects, err := normalizeObjects(ch.objects, r.Width, r.Height)
			if err != nil {
				return ch, err
			}
			ch.objects = objects
		}
		return ch, nil
	})
	return what, err
}

// maxListedRecords bounds the uploads and merges ListUploads and
// ListMerges return.
const maxListedRecords = 50

// ListUploads returns the newest results uploads of a review (at most
// maxListedRecords), newest first, and how many the review has.
func (s *Service) ListUploads(ctx context.Context, reviewID int64) ([]Upload, int, error) {
	if err := s.reviewExists(ctx, s.db, reviewID); err != nil {
		return nil, 0, err
	}
	uploads, err := collectUploads(s.db.Query(ctx, "SELECT "+uploadColumns+
		" FROM review_uploads WHERE review_id = $1 ORDER BY id DESC LIMIT $2", reviewID, maxListedRecords))
	if err != nil {
		return nil, 0, err
	}
	if uploads == nil {
		uploads = []Upload{}
	}
	total := len(uploads)
	if total == maxListedRecords {
		if err := s.db.QueryRow(ctx, "SELECT count(*) FROM review_uploads WHERE review_id = $1", reviewID).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return uploads, total, nil
}
