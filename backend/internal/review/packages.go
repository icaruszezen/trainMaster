package review

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Review packages (审图包): the images of a review with their review VOC
// files and review.json, as a zip archive for the local reviewer. Like a
// dataset download, a package is prepared first (CreatePackage checks the
// request and returns a token), then fetched with a plain GET that
// browsers save natively; the archive is generated while it is sent, so a
// download needs no disk space and starts at once.
//
// The archive has one top folder, 审图-<id>-<name>/, with README.txt, the
// images at their package paths, each followed by its VOC file, and
// review.json last: it lists exactly the images the archive holds (an
// image whose file is missing in its dataset is left out of both), and
// records for each one its status, revision and annotation version as
// they were when it was packed (the base of the results uploaded later,
// see results.go).

// packageTTL is how long a prepared package can be fetched (repeatedly:
// browsers and download managers may retry; every fetch packs the images
// as they are then).
const packageTTL = time.Hour

// packageBatch is how many images a package reads from the database at a
// time.
const packageBatch = 500

var errPackageExpired = &Error{Kind: ErrNotFound, Message: "下载链接已失效，请重新下载"}

// PackageInput selects the images of a package. Only inferred images are
// packed.
type PackageInput struct {
	Statuses   []string // image statuses; none: all
	DatasetIDs []int64  // datasets of the review; none: all
}

// PackageTicket is a prepared package download.
type PackageTicket struct {
	Token      string
	FileName   string
	ExpiresAt  time.Time
	ImageCount int // the images matching now (missing files are left out when it is fetched)
}

type packageRequest struct {
	reviewID   int64
	statuses   []string // nil: all
	datasetIDs []int64  // explicit
	fileName   string
	expires    time.Time
}

// tickets holds prepared downloads in memory (the platform runs as a
// single instance).
type tickets[T any] struct {
	mu sync.Mutex
	m  map[string]ticket[T]
}

type ticket[T any] struct {
	value   T
	expires time.Time
}

func (t *tickets[T]) put(token string, v T, expires, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]ticket[T]{}
	}
	for k, old := range t.m {
		if now.After(old.expires) {
			delete(t.m, k)
		}
	}
	t.m[token] = ticket[T]{value: v, expires: expires}
}

func (t *tickets[T]) get(token string, now time.Time) (T, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, ok := t.m[token]
	if !ok || now.After(tk.expires) {
		var zero T
		return zero, false
	}
	return tk.value, true
}

// normalizeStatuses checks a list of image statuses and drops repeats,
// keeping the order. field names the list in messages.
func normalizeStatuses(statuses []string, field string) ([]string, error) {
	var out []string
	for _, st := range statuses {
		if !reviewfile.ValidStatus(st) {
			return nil, newError(ErrInvalid, "%s 中的审核状态 %q 无效（可以是 pending、approved 或 needs_sample）", field, st)
		}
		if !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	return out, nil
}

// reviewDatasetIDs returns the datasets of a review in selection order;
// errReviewNotFound if it does not exist.
func reviewDatasetIDs(ctx context.Context, q querier, reviewID int64) ([]int64, error) {
	rows, err := q.Query(ctx, "SELECT dataset_id FROM review_datasets WHERE review_id = $1 ORDER BY position", reviewID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 { // every review has a dataset, until it is deleted
		var exists bool
		if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM reviews WHERE id = $1)", reviewID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, errReviewNotFound
		}
	}
	return ids, nil
}

// selectDatasets checks that ids are datasets of the review (all: the
// review's datasets, in selection order) and drops repeats.
func selectDatasets(ids, all []int64) ([]int64, error) {
	if len(ids) == 0 {
		return all, nil
	}
	var out []int64
	for _, id := range ids {
		if !slices.Contains(all, id) {
			return nil, newError(ErrInvalid, "数据集 #%d 不在这个审图任务中", id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// selectionFilter is the WHERE condition of the inferred images of a
// review in the given datasets with one of the statuses (nil: any).
func selectionFilter(reviewID int64, datasetIDs []int64, statuses []string, args *sqlArgs) string {
	where := "ri.review_id = " + args.add(reviewID) + " AND ri.inferred AND ri.dataset_id = ANY(" + args.add(datasetIDs) + ")"
	if len(statuses) > 0 {
		where += " AND ri.status = ANY(" + args.add(statuses) + ")"
	}
	return where
}

// CreatePackage prepares the download of a package of the review's
// inferred images matching in.
func (s *Service) CreatePackage(ctx context.Context, reviewID int64, in PackageInput) (PackageTicket, error) {
	statuses, err := normalizeStatuses(in.Statuses, "statuses")
	if err != nil {
		return PackageTicket{}, err
	}
	all, err := reviewDatasetIDs(ctx, s.db, reviewID)
	if err != nil {
		return PackageTicket{}, err
	}
	datasetIDs, err := selectDatasets(in.DatasetIDs, all)
	if err != nil {
		return PackageTicket{}, err
	}
	var name string
	var inferred int
	if err := s.db.QueryRow(ctx, `SELECT r.name, (SELECT count(*) FROM review_images ri WHERE ri.review_id = r.id AND ri.inferred)
		FROM reviews r WHERE r.id = $1`, reviewID).Scan(&name, &inferred); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PackageTicket{}, errReviewNotFound
		}
		return PackageTicket{}, err
	}
	if inferred == 0 {
		return PackageTicket{}, &Error{Kind: ErrConflict, Message: "审图任务还没有识别完成的图片，暂时无法下载审图包"}
	}
	var args sqlArgs
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM review_images ri WHERE "+
		selectionFilter(reviewID, datasetIDs, statuses, &args), args...).Scan(&count); err != nil {
		return PackageTicket{}, err
	}
	if count == 0 {
		return PackageTicket{}, &Error{Kind: ErrConflict, Message: "没有符合条件的图片（只有识别完成的图片可以下载）"}
	}
	now := s.clock()
	r := packageRequest{reviewID: reviewID, statuses: statuses, datasetIDs: datasetIDs,
		fileName: reviewfile.PackageFolder(reviewID, name) + ".zip", expires: now.Add(packageTTL)}
	token := rand.Text()
	s.packages.put(token, r, r.expires, now)
	return PackageTicket{Token: token, FileName: r.fileName, ExpiresAt: r.expires.UTC(), ImageCount: count}, nil
}

// Package is an opened package download. The caller writes it with Write
// and then closes it.
type Package struct {
	FileName string

	s        *Service
	req      packageRequest
	top      string // the folder everything is in
	manifest reviewfile.Manifest
	datasets map[int64]*packageDataset
	reviewFS *os.Root // nil: the review has no folder (no VOC files)
	log      *slog.Logger

	// Counts for the log.
	written, missing, annotations int
}

type packageDataset struct {
	name, dirName string
	root          *os.Root // nil: the folder cannot be opened
}

// packedImage is what Write records about an image it packed, for the
// manifest: its state when it was packed.
type packedImage struct {
	revision   int
	status     string
	reviewedAt *time.Time
	edited     bool
	version    string // of the packed VOC file, "" without one
}

// OpenPackage opens a package prepared by CreatePackage for the review.
func (s *Service) OpenPackage(ctx context.Context, reviewID int64, token string) (*Package, error) {
	req, ok := s.packages.get(token, s.clock())
	if !ok || req.reviewID != reviewID {
		return nil, errPackageExpired
	}
	m := reviewfile.Manifest{Format: reviewfile.ManifestFormat, Version: reviewfile.ManifestVersion,
		Kind: reviewfile.KindPackage, Platform: platformName()}
	err := s.db.QueryRow(ctx, "SELECT id, name, description, created_at, model_ref, options FROM reviews WHERE id = $1",
		reviewID).Scan(&m.Review.ID, &m.Review.Name, &m.Review.Description, &m.Review.CreatedAt, &m.Model, &m.Options)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errReviewNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Review.CreatedAt = m.Review.CreatedAt.UTC()
	classes, err := s.ListClasses(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	m.Classes = classes.Items

	p := &Package{FileName: req.fileName, s: s, req: req, top: strings.TrimSuffix(req.fileName, ".zip"),
		datasets: map[int64]*packageDataset{}, log: s.log.With("review_id", reviewID)}
	rows, err := s.db.Query(ctx, `SELECT rd.dataset_id, coalesce(d.name, rd.dataset_name), rd.folder, d.dir_name
		FROM review_datasets rd LEFT JOIN datasets d ON d.id = rd.dataset_id
		WHERE rd.review_id = $1 AND rd.dataset_id = ANY($2) ORDER BY rd.position`, reviewID, req.datasetIDs)
	if err != nil {
		return nil, err
	}
	var md reviewfile.ManifestDataset
	var dirName *string
	if _, err := pgx.ForEachRow(rows, []any{&md.ID, &md.Name, &md.Folder, &dirName}, func() error {
		m.Datasets = append(m.Datasets, md)
		d := &packageDataset{name: md.Name}
		if dirName != nil {
			d.dirName = *dirName
		}
		p.datasets[md.ID] = d
		return nil
	}); err != nil {
		return nil, err
	}
	p.manifest = m

	// Open the folders last, so that nothing is left open on an error
	// above.
	for id, d := range p.datasets {
		if d.dirName == "" {
			continue
		}
		// The layout of the dataset folders is the storage contract:
		// datasets/<dir_name>/<image path>.
		root, err := os.OpenRoot(filepath.Join(s.storage.Dir(storage.Datasets), d.dirName))
		if err != nil {
			p.log.Warn("review package: dataset folder cannot be opened; its images are left out", "dataset_id", id,
				"err", err)
			continue
		}
		d.root = root
	}
	if root, err := os.OpenRoot(s.reviewDir(reviewID)); err == nil {
		p.reviewFS = root
	} else if !errors.Is(err, fs.ErrNotExist) {
		p.Close()
		return nil, fmt.Errorf("open review folder: %w", err)
	}
	return p, nil
}

// platformName names the platform in review.json, e.g.
// "trainMaster 0.1.0-dev (d62132f)".
func platformName() string {
	name := buildinfo.Name + " " + buildinfo.Version
	if c := buildinfo.Commit; c != "" && c != "unknown" {
		name += " (" + c + ")"
	}
	return name
}

// Close releases the folders.
func (p *Package) Close() error {
	for _, d := range p.datasets {
		if d.root != nil {
			d.root.Close()
		}
	}
	if p.reviewFS != nil {
		p.reviewFS.Close()
	}
	return nil
}

// Write sends the zip archive to w. An error means the archive is
// incomplete (the caller aborts the response).
func (p *Package) Write(ctx context.Context, w io.Writer) error {
	start := time.Now()
	exportedAt := p.s.clock().UTC()
	zw := zip.NewWriter(w)
	dataset.RegisterZipCompressor(zw)
	if err := p.addFile(zw, reviewfile.ReadmeFile, packageReadme(&p.manifest, p.req.statuses, exportedAt), exportedAt); err != nil {
		return err
	}
	packed, err := p.writeImages(ctx, zw)
	if err != nil {
		return err
	}
	// A review deleted meanwhile has lost its VOC files on the way.
	if err := p.s.reviewExists(ctx, p.s.db, p.req.reviewID); err != nil {
		return err
	}
	if err := p.writeManifest(ctx, zw, packed, exportedAt); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	p.log.Info("review package downloaded", "images", p.written, "images_missing", p.missing,
		"annotations", p.annotations, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// writeImages packs the selected images and their VOC files in review
// order and returns what it packed by review image ID.
func (p *Package) writeImages(ctx context.Context, zw *zip.Writer) (map[int64]*packedImage, error) {
	packed := map[int64]*packedImage{}
	last := -1
	for {
		var args sqlArgs
		where := selectionFilter(p.req.reviewID, p.req.datasetIDs, p.req.statuses, &args)
		rows, err := p.s.db.Query(ctx, `SELECT ri.id, ri.dataset_id, ri.position, ri.rel_path, ri.package_path,
				ri.annotation_path, ri.revision, ri.status, ri.reviewed_at, ri.edited
			FROM review_images ri WHERE `+where+" AND ri.position > "+args.add(last)+
			" ORDER BY ri.position LIMIT "+args.add(packageBatch), args...)
		if err != nil {
			return nil, err
		}
		type row struct {
			id, datasetID                     int64
			position                          int
			relPath, packagePath, annotations string
			state                             packedImage
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var b row
			err := r.Scan(&b.id, &b.datasetID, &b.position, &b.relPath, &b.packagePath, &b.annotations,
				&b.state.revision, &b.state.status, &b.state.reviewedAt, &b.state.edited)
			b.state.reviewedAt = utcPtr(b.state.reviewedAt)
			return b, err
		})
		if err != nil {
			return nil, err
		}
		for _, b := range batch {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ok, err := p.copyImage(zw, b.datasetID, b.relPath, b.packagePath)
			if err != nil {
				return nil, err
			}
			if !ok {
				p.missing++
				continue
			}
			p.written++
			// The VOC file is read after the row: an edit in between makes the
			// packed file newer than the revision, which at worst reports a
			// conflict for it when results come back, never loses the edit.
			state := b.state
			if state.version, err = p.copyAnnotation(zw, b.annotations); err != nil {
				return nil, err
			}
			packed[b.id] = &state
		}
		if len(batch) < packageBatch {
			return packed, nil
		}
		last = batch[len(batch)-1].position
	}
}

// copyImage adds the dataset image rel as name; ok is false (and nothing
// is written) when the file cannot be opened.
func (p *Package) copyImage(zw *zip.Writer, datasetID int64, rel, name string) (ok bool, err error) {
	d := p.datasets[datasetID]
	if d == nil || d.root == nil {
		return false, nil
	}
	f, err := d.root.Open(filepath.FromSlash(rel))
	if err != nil {
		p.log.Debug("review package: leaving out an image that cannot be opened", "path", rel, "err", err)
		return false, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		p.log.Debug("review package: leaving out an image that is not a regular file", "path", rel, "err", err)
		return false, nil
	}
	out, err := zw.CreateHeader(p.header(name, dataset.ZipMethod(name), fi.ModTime()))
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, f); err != nil {
		return false, err
	}
	return true, nil
}

// copyAnnotation adds the review VOC file rel, if there is one, and
// returns its version ("" without one).
func (p *Package) copyAnnotation(zw *zip.Writer, rel string) (string, error) {
	if p.reviewFS == nil {
		return "", nil
	}
	f, err := p.reviewFS.Open(filepath.FromSlash(rel))
	if err != nil {
		return "", nil // none (inference failed and never edited)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(f, dataset.MaxAnnotationBytes+1))
	if err != nil {
		p.log.Warn("review package: leaving out a VOC file that cannot be read", "path", rel, "err", err)
		return "", nil
	}
	if err := p.addFile(zw, rel, data, fi.ModTime()); err != nil {
		return "", err
	}
	p.annotations++
	return reviewfile.AnnotationVersion(data), nil
}

func (p *Package) addFile(zw *zip.Writer, name string, data []byte, modTime time.Time) error {
	out, err := zw.CreateHeader(p.header(name, zip.Deflate, modTime))
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// header is the header of the entry for name in the top folder. Names are
// unique (see reviewfile.Layout), and valid Windows paths.
func (p *Package) header(name string, method uint16, modTime time.Time) *zip.FileHeader {
	h := &zip.FileHeader{Name: p.top + "/" + name, Method: method, Modified: modTime}
	h.SetMode(0o644)
	return h
}

// writeManifest writes review.json for the packed images, in review order.
// What changes on an image (its status, revision, ...) is taken from
// packed; the rest never changes once an image is inferred.
func (p *Package) writeManifest(ctx context.Context, zw *zip.Writer, packed map[int64]*packedImage, exportedAt time.Time) error {
	m := p.manifest
	m.ExportedAt = exportedAt
	m.PackageID = newPackageID()
	out, err := zw.CreateHeader(p.header(reviewfile.ManifestFile, zip.Deflate, exportedAt))
	if err != nil {
		return err
	}
	mw, err := reviewfile.NewManifestWriter(out, &m)
	if err != nil {
		return err
	}
	last := -1
	for {
		var args sqlArgs
		rows, err := p.s.db.Query(ctx, `SELECT ri.id, ri.dataset_id, ri.image_id, ri.position, ri.package_path, ri.rel_path,
				ri.annotation_path, ri.width, ri.height, ri.predictions, ri.inference_error
			FROM review_images ri WHERE ri.review_id = `+args.add(p.req.reviewID)+" AND ri.inferred AND ri.dataset_id = ANY("+
			args.add(p.req.datasetIDs)+") AND ri.position > "+args.add(last)+
			" ORDER BY ri.position LIMIT "+args.add(packageBatch), args...)
		if err != nil {
			return err
		}
		var n int
		var img reviewfile.ManifestImage
		var position int
		if _, err := pgx.ForEachRow(rows, []any{&img.ID, &img.DatasetID, &img.ImageID, &position, &img.Path,
			&img.SourcePath, &img.Annotation, &img.Width, &img.Height, &img.Predictions, &img.InferenceError}, func() error {
			n++
			last = position
			st := packed[img.ID]
			if st == nil {
				return nil // not packed: missing, or not selected
			}
			img.HasAnnotation = st.version != ""
			img.BaseStatus, img.BaseRevision, img.BaseAnnotationVersion = st.status, st.revision, st.version
			img.Status, img.ReviewedAt, img.Edited = st.status, st.reviewedAt, st.edited
			err := mw.Add(&img)
			img = reviewfile.ManifestImage{}
			return err
		}); err != nil {
			return err
		}
		if n < packageBatch {
			return mw.Close()
		}
	}
}

// newPackageID returns a random 128-bit ID in hex.
func newPackageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails
	return hex.EncodeToString(b[:])
}

// statusNames are the Chinese names of the image statuses.
var statusNames = map[string]string{
	reviewfile.StatusPending:     "待审核",
	reviewfile.StatusApproved:    "已审核",
	reviewfile.StatusNeedsSample: "需加样",
}

// packageReadme renders README.txt: UTF-8 with a byte order mark and CRLF
// line breaks, so that Notepad on any Windows shows it right.
func packageReadme(m *reviewfile.Manifest, statuses []string, exportedAt time.Time) []byte {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\r\n")
	}
	names := make([]string, len(m.Datasets))
	for i, d := range m.Datasets {
		names[i] = d.Name
	}
	selected := "全部"
	if len(statuses) > 0 {
		parts := make([]string, len(statuses))
		for i, st := range statuses {
			parts[i] = statusNames[st]
		}
		selected = strings.Join(parts, "、")
	}
	b.WriteString("\ufeff") // byte order mark
	line("trainMaster 审图包")
	line("==================")
	line("")
	line("审图任务：#%d %s", m.Review.ID, m.Review.Name)
	yolo := m.Model.YOLOVersion
	if v, ok := model.LookupYOLOVersion(yolo); ok {
		yolo = v.Label
	}
	line("模型：%s v%d（%s，%s）", m.Model.ProjectName, m.Model.Version, yolo, model.TaskLabel(m.Model.Task))
	line("数据集：%s", strings.Join(names, "、"))
	line("包含的图片：%s状态的已识别图片", selected)
	line("下载时间：%s（UTC）", exportedAt.Format("2006-01-02 15:04:05"))
	line("")
	line("这个压缩包是从 trainMaster 平台下载的审图包，包含需要审核的图片、模型识别生成的 VOC 标注文件")
	line("（与图片同名的 .xml 文件）和 review.json（审图包的说明，请不要修改或删除）。")
	line("")
	line("在本地审图：")
	line("1. 在平台上这个审图任务的页面下载“本地审图程序”（Windows，trainmaster-reviewer.exe）。")
	line("2. 打开本地审图程序，选择这个压缩包（也可以把压缩包拖到程序图标上）。程序会把它解压到压缩包旁边的")
	line("   同名文件夹中，以后再打开时可以接着上次的进度审图。")
	line("3. 逐张浏览模型的识别结果：识别正确的图片标为“已审核”（快捷键 S），需要参与后续训练的图片标为")
	line("   “需加样”（快捷键 X）；也可以修改、删除或添加目标框。")
	line("4. 审完后在程序中点“导出审图结果”，压缩包旁边会生成“…-审图结果.zip”。")
	line("5. 回到平台上这个审图任务的页面，点“上传审图结果”，选择导出的压缩包。平台读取结果后，才会更新")
	line("   图片的审核状态和标注。")
	line("")
	line("注意：")
	line("- 审图结果只更新这个审图任务自己的标注，不会直接改动数据集；需要时在平台上点“合并到数据集”。")
	line("- 下载审图包之后，如果平台上有人修改了同一张图片，上传时会提示冲突，可以选择用本地结果覆盖。")
	line("- 也可以用 LabelImg 等工具直接修改 .xml 文件，再把整个文件夹重新压缩成 zip 上传；这样只会更新")
	line("  标注，审核状态需要在本地审图程序或平台上标记。")
	return []byte(b.String())
}
