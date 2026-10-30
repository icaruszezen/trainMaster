package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// testEnv is a Service on a private database and data directory.
type testEnv struct {
	svc *Service
	db  *database.DB
	st  *storage.Storage
}

// newTestEnv creates the environment; start runs the job workers.
func newTestEnv(t *testing.T, start bool) *testEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{DB: db.Pool, Storage: st, Logger: slog.New(slog.DiscardHandler)})
	if start {
		ctx, cancel := context.WithCancel(context.Background())
		if err := svc.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { cancel(); svc.Stop() }) // runs before the database is dropped
	}
	return &testEnv{svc: svc, db: db, st: st}
}

func (e *testEnv) datasetsDir() string { return e.st.Dir(storage.Datasets) }

// writeFiles creates files below dir (slash-separated relative paths).
func writeFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// vocFile returns a VOC annotation with one object per label.
func vocFile(filename string, labels ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "<annotation><filename>%s</filename><size><width>64</width><height>48</height></size>", filename)
	for i, l := range labels {
		fmt.Fprintf(&b, "<object><name>%s</name><bndbox><xmin>%d</xmin><ymin>1</ymin><xmax>%d</xmax><ymax>9</ymax></bndbox></object>", l, i, i+5)
	}
	b.WriteString("</annotation>")
	return []byte(b.String())
}

func pngFile(t *testing.T, w, h int) []byte { return encodeImage(t, "png", gradient(w, h)) }

// waitJob polls a job until it leaves uploading/queued/running.
func (e *testEnv) waitJob(t *testing.T, datasetID, importID int64) Import {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		job, err := e.svc.GetImport(context.Background(), datasetID, importID)
		if err != nil {
			t.Fatalf("GetImport: %v", err)
		}
		switch job.Status {
		case StatusSucceeded, StatusFailed, StatusCanceled:
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d still %s after 15s", importID, job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func wantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	var ue *Error
	if !errors.As(err, &ue) || !errors.Is(err, kind) || !strings.Contains(ue.Message, msgPart) {
		t.Errorf("err = %v, want kind %v containing %q", err, kind, msgPart)
	}
}

func wantSummary(t *testing.T, got map[string]int64, want map[string]int64) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("summary[%s] = %d, want %d (summary %v)", k, got[k], v, got)
		}
	}
}

func TestIntegrationCreateAndUpdateDataset(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()

	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "  划痕-A ", Description: "产线 1"})
	if err != nil {
		t.Fatalf("CreateDataset: %v", err)
	}
	if d.Name != "划痕-A" || d.DirName != "划痕-A" || d.Origin != OriginUpload || d.Description != "产线 1" ||
		d.Path != filepath.Join(e.datasetsDir(), "划痕-A") || d.DirectoryMissing || d.ActiveImport != nil || d.Covers == nil {
		t.Errorf("dataset = %+v", d)
	}
	if fi, err := os.Stat(d.Path); err != nil || !fi.IsDir() {
		t.Errorf("folder not created: %v", err)
	}

	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "划痕-a"})
	wantKind(t, err, ErrConflict, "已存在同名数据集")
	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "a/b"})
	wantKind(t, err, ErrInvalid, "名称不能包含")

	// A folder that already exists on disk (any case) is not reused.
	if err := os.Mkdir(filepath.Join(e.datasetsDir(), "Existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "existing"})
	wantKind(t, err, ErrConflict, "数据目录中已存在同名文件夹“Existing”")

	other, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	newName, desc := "新名字", "  更新后的描述 "
	upd, err := e.svc.UpdateDataset(ctx, d.ID, UpdateDatasetInput{Name: &newName, Description: &desc})
	if err != nil || upd.Name != "新名字" || upd.Description != "更新后的描述" || upd.DirName != "划痕-A" {
		t.Fatalf("UpdateDataset = %+v, %v", upd, err)
	}
	if !upd.UpdatedAt.After(d.UpdatedAt) {
		t.Errorf("updated_at not bumped: %v -> %v", d.UpdatedAt, upd.UpdatedAt)
	}
	clash := "OTHER"
	_, err = e.svc.UpdateDataset(ctx, d.ID, UpdateDatasetInput{Name: &clash})
	wantKind(t, err, ErrConflict, "已存在同名数据集")
	same := "新名字"
	if _, err := e.svc.UpdateDataset(ctx, d.ID, UpdateDatasetInput{Name: &same}); err != nil {
		t.Errorf("keeping the own name: %v", err)
	}
	long := strings.Repeat("字", 501)
	_, err = e.svc.UpdateDataset(ctx, d.ID, UpdateDatasetInput{Description: &long})
	wantKind(t, err, ErrInvalid, "描述不能超过 500 个字符")
	_, err = e.svc.UpdateDataset(ctx, 999999, UpdateDatasetInput{Name: &newName})
	wantKind(t, err, ErrNotFound, "数据集不存在")

	list, err := e.svc.ListDatasets(ctx, "")
	if err != nil || len(list) != 2 || list[0].ID != d.ID || list[1].ID != other.ID {
		t.Errorf("ListDatasets = %+v, %v", list, err)
	}
	for q, want := range map[string]int{"新": 1, "更新后": 1, "OTH": 1, "zzz": 0, " ": 2} {
		if list, err := e.svc.ListDatasets(ctx, q); err != nil || len(list) != want {
			t.Errorf("ListDatasets(%q) returned %d datasets, %v; want %d", q, len(list), err, want)
		}
	}

	// A folder deleted behind the platform's back is reported.
	if err := os.Remove(filepath.Join(e.datasetsDir(), "other")); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.GetDataset(ctx, other.ID); !got.DirectoryMissing {
		t.Error("directory_missing should be true")
	}
}

func TestIntegrationRegisterAndScan(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	dir := filepath.Join(e.datasetsDir(), "line1")
	writeFiles(t, dir, map[string][]byte{
		"OK/a.jpg":        encodeImage(t, "jpeg", gradient(64, 48)),
		"OK/a.xml":        vocFile("a.jpg", "scratch", "scratch", "dent"),
		"NG/b.png":        pngFile(t, 20, 10),
		"NG/b.xml":        vocFile("b.png"),
		"c.png":           pngFile(t, 8, 8),
		"c.xml":           []byte("<annotation><object>"),
		"d.avif":          []byte("not decodable"),
		"orphan.xml":      vocFile("gone.jpg", "x"),
		"Thumbs.db":       []byte("junk"),
		"notes.txt":       []byte("junk"),
		".hidden/e.jpg":   []byte("hidden"),
		"__MACOSX/._a.jp": []byte("junk"),
	})
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	writeFiles(t, filepath.Dir(outside), map[string][]byte{"secret.jpg": pngFile(t, 4, 4)})
	if err := os.Symlink(outside, filepath.Join(dir, "link.jpg")); err != nil {
		t.Fatal(err)
	}

	dirName := "line1"
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "产线1", Directory: &dirName})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if d.Origin != OriginDirectory || d.DirName != "line1" || d.ActiveImport == nil || d.ActiveImport.Source != SourceScan {
		t.Fatalf("dataset = %+v", d)
	}
	job := e.waitJob(t, d.ID, d.ActiveImport.ID)
	if job.Status != StatusSucceeded || job.FinishedAt == nil || job.StartedAt == nil {
		t.Fatalf("scan job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesTotal: 4, sumImagesAdded: 4, sumImagesUpdated: 0, sumImagesRemoved: 0, sumImagesUnchanged: 0,
		sumAnnotationsMatched: 2, sumAnnotationsInvalid: 1, sumAnnotationsOrphan: 1, sumFilesIgnored: 2, sumNamesInvalid: 0,
	})

	d, _ = e.svc.GetDataset(ctx, d.ID)
	if d.ImageCount != 4 || d.AnnotatedCount != 1 || d.EmptyCount != 1 || d.InvalidCount != 1 || d.ObjectCount != 3 || d.LabelCount != 2 ||
		d.ScannedAt == nil || d.ActiveImport != nil || len(d.Covers) != 4 || d.TotalBytes == 0 {
		t.Errorf("dataset after scan = %+v", d)
	}

	list := func(q ImageQuery) []Image {
		t.Helper()
		if q.Limit == 0 {
			q.Limit = 60
		}
		items, total, err := e.svc.ListImages(ctx, d.ID, q)
		if err != nil || total != len(items) && q.Offset == 0 && q.Limit >= total {
			t.Fatalf("ListImages(%+v) = %d items, total %d, %v", q, len(items), total, err)
		}
		return items
	}
	paths := func(items []Image) string {
		var p []string
		for _, i := range items {
			p = append(p, i.Path)
		}
		return strings.Join(p, ",")
	}
	if got := paths(list(ImageQuery{})); got != "NG/b.png,OK/a.jpg,c.png,d.avif" {
		t.Errorf("all = %s", got)
	}
	for status, want := range map[string]string{
		FilterAnnotated: "OK/a.jpg", FilterEmpty: "NG/b.png", FilterInvalid: "c.png", FilterUnannotated: "d.avif",
	} {
		if got := paths(list(ImageQuery{Status: status})); got != want {
			t.Errorf("status %s = %s, want %s", status, got, want)
		}
	}
	if got := paths(list(ImageQuery{Label: "dent"})); got != "OK/a.jpg" {
		t.Errorf("label dent = %s", got)
	}
	if got := paths(list(ImageQuery{Search: "ok/"})); got != "OK/a.jpg" {
		t.Errorf("search = %s", got)
	}
	if got := paths(list(ImageQuery{Sort: SortObjects, Limit: 1})); got != "OK/a.jpg" {
		t.Errorf("sort -objects = %s", got)
	}
	if items, total, _ := e.svc.ListImages(ctx, d.ID, ImageQuery{Offset: 3, Limit: 2}); total != 4 || len(items) != 1 {
		t.Errorf("paging: %d items, total %d", len(items), total)
	}

	imgs := list(ImageQuery{})
	a := imgs[1]
	if a.Width == nil || *a.Width != 64 || a.Annotation == nil || a.Annotation.Path != "OK/a.xml" || a.Annotation.ObjectCount != 3 ||
		len(a.Annotation.Labels) != 2 || a.Annotation.Labels[0] != (LabelCount{"scratch", 2}) || a.FileName != "a.jpg" || a.Version == "" ||
		a.AnnotationState != annotationAnnotated || a.AnnotationPath == nil || *a.AnnotationPath != "OK/a.xml" {
		t.Errorf("image a = %+v", a)
	}
	if b := imgs[0]; b.AnnotationState != annotationEmpty || b.Annotation == nil || b.Annotation.ObjectCount != 0 {
		t.Errorf("image b = %+v", b)
	}
	// An unparsable VOC file is reported with its path but no summary.
	if c := imgs[2]; c.AnnotationState != annotationInvalid || c.AnnotationPath == nil || *c.AnnotationPath != "c.xml" || c.Annotation != nil {
		t.Errorf("image c = %+v", c)
	}
	if avif := imgs[3]; avif.AnnotationState != annotationNone || avif.AnnotationPath != nil || avif.Annotation != nil {
		t.Errorf("image d = %+v", avif)
	}
	if avif := imgs[3]; avif.Width == nil || *avif.Width != 0 && avif.Annotation != nil {
		// d.avif has no annotation and no decodable header.
		if avif.Width != nil {
			t.Errorf("avif dimensions = %v", *avif.Width)
		}
	}
	detail, err := e.svc.GetImage(ctx, d.ID, a.ID)
	if err != nil || len(detail.Objects) != 3 || detail.Objects[2].Name != "dent" || detail.AnnotationError != "" ||
		detail.AnnotationState != annotationAnnotated {
		t.Errorf("detail = %+v, %v", detail, err)
	}
	invalid, err := e.svc.GetImage(ctx, d.ID, imgs[2].ID)
	if err != nil || invalid.Annotation != nil || len(invalid.Objects) != 0 || invalid.AnnotationState != annotationInvalid ||
		invalid.AnnotationPath == nil || *invalid.AnnotationPath != "c.xml" ||
		invalid.AnnotationError != "标注文件无法解析（XML 格式错误）：XML syntax error on line 1: unexpected EOF" {
		t.Errorf("invalid detail = %+v, %v", invalid, err)
	}
	_, err = e.svc.GetImage(ctx, d.ID, 999999)
	wantKind(t, err, ErrNotFound, "图片不存在")

	labels, err := e.svc.ListLabels(ctx, d.ID)
	if err != nil || len(labels) != 2 || labels[0] != (Label{"scratch", 2, 1}) || labels[1] != (Label{"dent", 1, 1}) {
		t.Errorf("labels = %+v, %v", labels, err)
	}

	// The original file is served through the dataset folder; thumbnails
	// are generated once and cached.
	f, err := e.svc.OpenImage(ctx, d.ID, a.ID)
	if err != nil || f.ContentType != "image/jpeg" || f.Version != a.Version {
		t.Fatalf("OpenImage = %+v, %v", f, err)
	}
	f.File.Close()
	th, err := e.svc.OpenThumbnail(ctx, d.ID, a.ID, ThumbSmall)
	if err != nil || th.ContentType != "image/jpeg" || !strings.HasPrefix(th.File.Name(), e.st.Dir(storage.Cache)) {
		t.Fatalf("OpenThumbnail = %+v, %v", th, err)
	}
	th.File.Close()
	_, err = e.svc.OpenThumbnail(ctx, d.ID, imgs[3].ID, ThumbSmall)
	wantKind(t, err, ErrUnsupportedImage, "无法生成")

	// Rescan after changes on disk.
	writeFiles(t, dir, map[string][]byte{
		"OK/a.xml":  vocFile("a.jpg", "scratch"),
		"new/f.png": pngFile(t, 5, 5),
	})
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(filepath.Join(dir, "OK", "a.xml"), future, future)
	if err := os.Remove(filepath.Join(dir, "NG", "b.png")); err != nil {
		t.Fatal(err)
	}
	scan, err := e.svc.StartScan(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	job = e.waitJob(t, d.ID, scan.ID)
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesTotal: 4, sumImagesAdded: 1, sumImagesUpdated: 1, sumImagesRemoved: 1, sumImagesUnchanged: 2,
		sumAnnotationsMatched: 1, sumAnnotationsInvalid: 1, sumAnnotationsOrphan: 2,
	})
	d, _ = e.svc.GetDataset(ctx, d.ID)
	if d.ImageCount != 4 || d.AnnotatedCount != 1 || d.EmptyCount != 0 || d.InvalidCount != 1 || d.ObjectCount != 1 || d.LabelCount != 1 {
		t.Errorf("dataset after rescan = %+v", d)
	}

	// Registering the same folder again, or folders that cannot be used.
	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "again", Directory: &dirName})
	wantKind(t, err, ErrConflict, "已添加为数据集“产线1”")
	if err := os.Symlink(dir, filepath.Join(e.datasetsDir(), "alias")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, e.datasetsDir(), map[string][]byte{"file.txt": nil, ".hidden/a.jpg": nil, "fresh/x.jpg": pngFile(t, 2, 2)})
	for name, msg := range map[string]string{
		"missing": "目录不存在", "alias": "符号链接", "file.txt": "不是文件夹", ".hidden": "目录不存在", "../line1": "目录不存在",
	} {
		_, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "x-" + strings.Trim(name, "./"), Directory: &name})
		wantKind(t, err, ErrNotFound, msg)
	}
	empty := ""
	_, err = e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "x", Directory: &empty})
	wantKind(t, err, ErrInvalid, "请选择")

	dirs, err := e.svc.ListDirectories(ctx)
	if err != nil || len(dirs) != 2 || dirs[0].Name != "fresh" || dirs[0].Registered || dirs[0].ImageCount != 1 ||
		dirs[1].Name != "line1" || !dirs[1].Registered || dirs[1].DatasetID == nil || *dirs[1].DatasetID != d.ID ||
		dirs[1].ImageCount != 4 || dirs[1].AnnotationCount != 4 || dirs[1].Approximate {
		t.Errorf("ListDirectories = %+v, %v", dirs, err)
	}

	// A scan of a folder that vanished fails with a clear message.
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	scan, _ = e.svc.StartScan(ctx, d.ID)
	job = e.waitJob(t, d.ID, scan.ID)
	if job.Status != StatusFailed || !strings.HasPrefix(job.Error, "数据集文件夹不存在：") {
		t.Errorf("scan of missing folder = %+v", job)
	}
}

// uploadArchive runs the archive upload protocol in two chunks.
func (e *testEnv) uploadArchive(t *testing.T, datasetID int64, data []byte, onConflict string) Import {
	t.Helper()
	ctx := context.Background()
	job, err := e.svc.CreateImport(ctx, datasetID, CreateImportInput{
		Source: SourceArchive, FileName: "batch.zip", FileSize: int64(len(data)), OnConflict: onConflict,
	})
	if err != nil {
		t.Fatalf("CreateImport: %v", err)
	}
	half := int64(len(data) / 2)
	if job, err = e.svc.WriteArchiveChunk(ctx, datasetID, job.ID, 0, bytes.NewReader(data[:half])); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	if job, err = e.svc.WriteArchiveChunk(ctx, datasetID, job.ID, half, bytes.NewReader(data[half:])); err != nil {
		t.Fatalf("chunk 2: %v", err)
	}
	if job, err = e.svc.CommitImport(ctx, datasetID, job.ID); err != nil || job.Status != StatusQueued {
		t.Fatalf("CommitImport = %+v, %v", job, err)
	}
	return e.waitJob(t, datasetID, job.ID)
}

func TestIntegrationArchiveConflicts(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	if err != nil {
		t.Fatal(err)
	}
	old := pngFile(t, 10, 10)
	writeFiles(t, d.Path, map[string][]byte{"a.png": old, "a.xml": vocFile("a.png", "old"), "keep.png": old, "keep.xml": vocFile("keep.png", "kept")})

	newer := pngFile(t, 12, 12)
	archive := writeZip(t, []testEntry{
		{Name: "a.png", Data: string(newer)},
		{Name: "a.xml", Data: string(vocFile("a.png", "new"))},
		{Name: "keep.png", Data: string(newer)}, // no annotation in the import
		{Name: "b.png", Data: string(newer)},
	})

	job := e.uploadArchive(t, d.ID, archive, ConflictSkip)
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 1, sumImagesSkipped: 2, sumImagesReplaced: 0, sumAnnotationsMatched: 0})
	if got, _ := os.ReadFile(filepath.Join(d.Path, "a.png")); !bytes.Equal(got, old) {
		t.Error("skip mode replaced a.png")
	}
	if got, _ := os.ReadFile(filepath.Join(d.Path, "a.xml")); !bytes.Contains(got, []byte("old")) {
		t.Error("skip mode replaced a.xml")
	}

	job = e.uploadArchive(t, d.ID, archive, ConflictOverwrite)
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 0, sumImagesReplaced: 3, sumImagesSkipped: 0, sumAnnotationsMatched: 1})
	if got, _ := os.ReadFile(filepath.Join(d.Path, "a.png")); !bytes.Equal(got, newer) {
		t.Error("overwrite mode kept the old a.png")
	}
	if got, _ := os.ReadFile(filepath.Join(d.Path, "a.xml")); !bytes.Contains(got, []byte("new")) {
		t.Error("overwrite mode kept the old a.xml")
	}
	if got, _ := os.ReadFile(filepath.Join(d.Path, "keep.xml")); !bytes.Contains(got, []byte("kept")) {
		t.Error("an annotation the import does not have must be kept")
	}
	d, _ = e.svc.GetDataset(ctx, d.ID)
	if d.ImageCount != 3 || d.AnnotatedCount != 2 || d.LabelCount != 2 {
		t.Errorf("dataset = %+v", d)
	}
	imgs, _, _ := e.svc.ListImages(ctx, d.ID, ImageQuery{Limit: 10})
	if imgs[0].Width == nil || *imgs[0].Width != 12 {
		t.Errorf("index not refreshed after overwrite: %+v", imgs[0])
	}
}

func TestIntegrationArchiveSafety(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(d.Path, "evil")); err != nil {
		t.Fatal(err)
	}
	img := string(pngFile(t, 3, 3))
	job := e.uploadArchive(t, d.ID, writeZip(t, []testEntry{
		{Name: "../escape.png", Data: img},
		{Name: "/abs.png", Data: img},
		{Name: `C:\win.png`, Data: img},
		{Name: "link.png", Data: "/etc/passwd", Type: entryLink},
		{Name: "evil/through-link.png", Data: img}, // the dataset folder has a symlink "evil"
		{Name: "ok.png", Data: img},
	}), ConflictSkip)
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 1, sumEntriesUnsafe: 5})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("files were written outside the dataset: %v", entries)
	}
	for _, p := range []string{filepath.Join(e.st.Root(), "escape.png"), filepath.Join(e.datasetsDir(), "escape.png")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists", p)
		}
	}
	if _, err := os.Stat(e.svc.stagingDir(job.ID)); !os.IsNotExist(err) {
		t.Errorf("staging folder left behind: %v", err)
	}
}

func TestIntegrationArchiveFailures(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "f"})

	job := e.uploadArchive(t, d.ID, []byte("this is not an archive at all"), "")
	if job.Status != StatusFailed || !strings.HasPrefix(job.Error, "无法识别的压缩包格式") {
		t.Errorf("job = %+v", job)
	}
	enc, _ := os.ReadFile(filepath.Join("testdata", "encrypted.7z"))
	if job := e.uploadArchive(t, d.ID, enc, ""); job.Status != StatusFailed || job.Error != errArchiveEncrypted.Message {
		t.Errorf("encrypted job = %+v", job)
	}
	sample, _ := os.ReadFile(filepath.Join("testdata", "sample.7z"))
	job = e.uploadArchive(t, d.ID, sample, "")
	if job.Status != StatusSucceeded {
		t.Fatalf("7z job = %+v", job)
	}
	// set/ is stripped; sub/ stays.
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 1, sumAnnotationsMatched: 1, sumFilesIgnored: 1})
	if _, err := os.Stat(filepath.Join(d.Path, "sub", "a.png")); err != nil {
		t.Errorf("sub/a.png: %v", err)
	}

	// Zip bombs stop at the extraction budget.
	e.svc.maxExtract.Store(1000)
	bomb := writeZip(t, []testEntry{{Name: "big.png", Data: strings.Repeat("\x00", 100_000)}})
	if job := e.uploadArchive(t, d.ID, bomb, ""); job.Status != StatusFailed || job.Error != errExtractTooLarge.Message {
		t.Errorf("bomb job = %+v", job)
	}
}

// fileSource serves a fixed list of files as a folder upload request.
func fileSource(files ...[2]string) FileSource {
	i := 0
	return func() (string, io.Reader, error) {
		if i == len(files) {
			return "", nil, io.EOF
		}
		f := files[i]
		i++
		return f[0], strings.NewReader(f[1]), nil
	}
}

func TestIntegrationFolderImport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "folder"})
	img := string(pngFile(t, 6, 4))
	xml := string(vocFile("a.png", "spot"))

	job, err := e.svc.CreateImport(ctx, d.ID, CreateImportInput{Source: SourceFolder, FileName: "line2", FileCount: 4, FileSize: 100})
	if err != nil || job.Status != StatusUploading || job.OnConflict != ConflictSkip {
		t.Fatalf("CreateImport = %+v, %v", job, err)
	}
	job, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource(
		[2]string{"OK/a.png", img}, [2]string{"OK/a.xml", xml}, [2]string{"notes.txt", "ignored"},
	))
	if err != nil || job.ReceivedFiles != 2 || job.ReceivedBytes != int64(len(img)+len(xml)) {
		t.Fatalf("AddFolderFiles = %+v, %v", job, err)
	}
	// A retried request does not count twice.
	job, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource([2]string{`OK\a.png`, img}, [2]string{"OK/b.png", img}))
	if err != nil || job.ReceivedFiles != 3 || job.ReceivedBytes != int64(2*len(img)+len(xml)) {
		t.Fatalf("retry = %+v, %v", job, err)
	}
	_, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource([2]string{"../x.png", img}))
	wantKind(t, err, ErrBadRequest, "文件路径无效")
	_, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 0, strings.NewReader("x"))
	wantKind(t, err, ErrBadRequest, "不是压缩包上传")

	if job, err = e.svc.CommitImport(ctx, d.ID, job.ID); err != nil || job.Status != StatusQueued {
		t.Fatalf("commit = %+v, %v", job, err)
	}
	// Committing twice is harmless; uploading after the commit is not.
	if again, err := e.svc.CommitImport(ctx, d.ID, job.ID); err != nil || again.ID != job.ID {
		t.Errorf("second commit = %+v, %v", again, err)
	}
	_, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource([2]string{"late.png", img}))
	wantKind(t, err, ErrConflict, "已提交")

	job = e.waitJob(t, d.ID, job.ID)
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 2, sumAnnotationsMatched: 1})
	if _, ok := job.Summary[sumEntriesUnsafe]; ok {
		t.Errorf("folder summary should not report archive-only keys: %v", job.Summary)
	}
	for _, rel := range []string{"OK/a.png", "OK/a.xml", "OK/b.png"} {
		fi, err := os.Stat(filepath.Join(d.Path, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
		} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o644 {
			t.Errorf("%s has mode %v, want 0644", rel, fi.Mode().Perm())
		}
	}
	d, _ = e.svc.GetDataset(ctx, d.ID)
	if d.ImageCount != 2 || d.AnnotatedCount != 1 {
		t.Errorf("dataset = %+v", d)
	}
}

func TestIntegrationUploadProtocol(t *testing.T) {
	e := newTestEnv(t, false) // jobs stay queued
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "proto"})

	for _, in := range []CreateImportInput{
		{Source: "url", FileName: "a.zip", FileSize: 1},
		{Source: SourceArchive, FileName: " ", FileSize: 1},
		{Source: SourceArchive, FileName: "a.zip", FileSize: 0},
		{Source: SourceFolder, FileSize: -1},
		{Source: SourceArchive, FileName: "a.zip", FileSize: 1, OnConflict: "rename"},
		{Source: SourceArchive, FileName: "a\nb.zip", FileSize: 1},
	} {
		_, err := e.svc.CreateImport(ctx, d.ID, in)
		wantKind(t, err, ErrInvalid, "")
	}
	_, err := e.svc.CreateImport(ctx, d.ID, CreateImportInput{Source: SourceArchive, FileName: "a.zip", FileSize: 1 << 49})
	wantKind(t, err, ErrInsufficientStorage, "磁盘剩余空间不足")
	_, err = e.svc.CreateImport(ctx, 424242, CreateImportInput{Source: SourceArchive, FileName: "a.zip", FileSize: 1})
	wantKind(t, err, ErrNotFound, "数据集不存在")

	job, err := e.svc.CreateImport(ctx, d.ID, CreateImportInput{Source: SourceArchive, FileName: "a.zip", FileSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e.svc.stagingDir(job.ID)); err != nil || !fi.IsDir() {
		t.Fatalf("staging folder: %v", err)
	}
	_, err = e.svc.CommitImport(ctx, d.ID, job.ID)
	wantKind(t, err, ErrConflict, "尚未上传完成")
	_, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 3, strings.NewReader("abc"))
	wantKind(t, err, ErrOffsetMismatch, "服务器已接收 0 字节")
	job, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 0, strings.NewReader("01234"))
	if err != nil || job.ReceivedBytes != 5 {
		t.Fatalf("chunk = %+v, %v", job, err)
	}
	_, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 5, strings.NewReader("too long!"))
	wantKind(t, err, ErrBadRequest, "超过了声明的文件大小")
	// A failed chunk leaves nothing behind, so the client resumes at 5.
	if fi, _ := os.Stat(filepath.Join(e.svc.stagingDir(job.ID), uploadFileName)); fi.Size() != 5 {
		t.Errorf("upload.bin has %d bytes after a rejected chunk", fi.Size())
	}
	_, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 5, iotestErrReader{})
	wantKind(t, err, ErrBadRequest, "上传数据接收不完整")
	if job, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 5, strings.NewReader("56789")); err != nil || job.ReceivedBytes != 10 {
		t.Fatalf("last chunk = %+v, %v", job, err)
	}
	_, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource())
	wantKind(t, err, ErrBadRequest, "不是文件夹上传")
	if job, err = e.svc.CommitImport(ctx, d.ID, job.ID); err != nil || job.Status != StatusQueued {
		t.Fatalf("commit = %+v, %v", job, err)
	}
	_, err = e.svc.WriteArchiveChunk(ctx, d.ID, job.ID, 10, strings.NewReader(""))
	wantKind(t, err, ErrConflict, "已提交")

	// Only one scan at a time; queued jobs block deletion.
	_, err = e.svc.StartScan(ctx, d.ID)
	wantKind(t, err, ErrConflict, "正在导入或扫描")
	_, err = e.svc.DeleteDataset(ctx, d.ID, true)
	wantKind(t, err, ErrConflict, "正在导入或扫描")

	// Cancel: queued and uploading jobs end at once; finished ones stay.
	canceled, err := e.svc.CancelImport(ctx, d.ID, job.ID)
	if err != nil || canceled.Status != StatusCanceled || canceled.FinishedAt == nil {
		t.Fatalf("cancel queued = %+v, %v", canceled, err)
	}
	if _, err := os.Stat(e.svc.stagingDir(job.ID)); !os.IsNotExist(err) {
		t.Errorf("staging not removed: %v", err)
	}
	if again, err := e.svc.CancelImport(ctx, d.ID, job.ID); err != nil || again.Status != StatusCanceled {
		t.Errorf("cancel again = %+v, %v", again, err)
	}
	_, err = e.svc.CommitImport(ctx, d.ID, job.ID)
	wantKind(t, err, ErrConflict, "已取消")
	_, err = e.svc.GetImport(ctx, d.ID, 999999)
	wantKind(t, err, ErrNotFound, "导入任务不存在")
	_, err = e.svc.GetImport(ctx, 999999, job.ID)
	wantKind(t, err, ErrNotFound, "数据集不存在")

	jobs, total, err := e.svc.ListImports(ctx, d.ID, 1)
	if err != nil || total != 1 || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Errorf("ListImports = %+v, %d, %v", jobs, total, err)
	}
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestIntegrationDeleteDataset(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()

	keep, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "keep-files"})
	writeFiles(t, keep.Path, map[string][]byte{"a.png": pngFile(t, 2, 2)})
	upload, _ := e.svc.CreateImport(ctx, keep.ID, CreateImportInput{Source: SourceFolder})
	if res, err := e.svc.DeleteDataset(ctx, keep.ID, false); err != nil || res != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keep.Path, "a.png")); err != nil {
		t.Errorf("files must stay without delete_files: %v", err)
	}
	if _, err := os.Stat(e.svc.stagingDir(upload.ID)); !os.IsNotExist(err) {
		t.Errorf("staging of the canceled upload remains: %v", err)
	}
	_, err := e.svc.GetDataset(ctx, keep.ID)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	_, err = e.svc.DeleteDataset(ctx, keep.ID, false)
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// The folder can be registered again afterwards.
	dirName := "keep-files"
	again, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "again", Directory: &dirName})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if busy, _ := e.svc.CancelImport(ctx, again.ID, again.ActiveImport.ID); busy.Status != StatusCanceled {
		t.Fatalf("cancel scan = %+v", busy)
	}

	// delete_files removes the folder, its thumbnails, and never follows a
	// symbolic link inside it.
	outside := t.TempDir()
	writeFiles(t, outside, map[string][]byte{"precious.png": pngFile(t, 2, 2)})
	if err := os.Symlink(outside, filepath.Join(again.Path, "link")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, e.svc.thumbDir(again.ID), map[string][]byte{"1-sm-x.jpg": []byte("thumb")})
	if res, err := e.svc.DeleteDataset(ctx, again.ID, true); err != nil || res != nil {
		t.Fatalf("delete with files: %+v, %v", res, err)
	}
	if _, err := os.Lstat(again.Path); !os.IsNotExist(err) {
		t.Errorf("dataset folder still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.png")); err != nil {
		t.Errorf("deletion followed a symbolic link: %v", err)
	}
	if _, err := os.Stat(e.svc.thumbDir(again.ID)); !os.IsNotExist(err) {
		t.Errorf("thumbnails remain: %v", err)
	}
	if entries, _ := os.ReadDir(e.datasetsDir()); len(entries) != 0 {
		t.Errorf("datasets folder holds %v", entries)
	}

	// A dataset folder replaced by a symbolic link is not deleted through it.
	sym, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "sym"})
	if err := os.Remove(sym.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sym.Path); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.DeleteDataset(ctx, sym.ID, true)
	wantKind(t, err, ErrConflict, "符号链接")
	if _, err := e.svc.GetDataset(ctx, sym.ID); err != nil {
		t.Errorf("a refused delete must keep the dataset: %v", err)
	}
	if _, err := e.svc.DeleteDataset(ctx, sym.ID, false); err != nil {
		t.Errorf("delete without files: %v", err)
	}
}

func TestIntegrationRecoveryAndJanitor(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "rec"})
	insert := func(status string) Import {
		job, err := insertImport(ctx, e.db.Pool, newImport{DatasetID: d.ID, Source: SourceFolder, Status: status, OnConflict: ConflictSkip})
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, e.svc.stagingDir(job.ID), map[string][]byte{"files/a.png": nil})
		return job
	}
	running, uploading, stale := insert(StatusRunning), insert(StatusUploading), insert(StatusUploading)
	writeFiles(t, filepath.Join(e.svc.stagingRoot(), "999999"), map[string][]byte{"upload.bin": nil})
	writeFiles(t, filepath.Join(e.datasetsDir(), deletingPrefix+"77"), map[string][]byte{"a.png": nil})
	if _, err := e.db.Exec(ctx, "UPDATE dataset_imports SET updated_at = now() - interval '25 hours' WHERE id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}

	sctx, cancel := context.WithCancel(ctx)
	if err := e.svc.Start(sctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.svc.Stop() }()

	if job, _ := e.svc.GetImport(ctx, d.ID, running.ID); job.Status != StatusFailed || job.Error != msgInterrupted {
		t.Errorf("interrupted job = %+v", job)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, _ := e.svc.GetImport(ctx, d.ID, stale.ID)
		if job.Status == StatusCanceled {
			if job.Error != msgUploadExpired {
				t.Errorf("expired upload = %+v", job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stale upload not expired: %+v", job)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job, _ := e.svc.GetImport(ctx, d.ID, uploading.ID); job.Status != StatusUploading {
		t.Errorf("active upload = %+v", job)
	}
	for _, p := range []string{e.svc.stagingDir(running.ID), e.svc.stagingDir(stale.ID), filepath.Join(e.svc.stagingRoot(), "999999")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should be removed: %v", p, err)
		}
	}
	if _, err := os.Stat(e.svc.stagingDir(uploading.ID)); err != nil {
		t.Errorf("staging of an active upload was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.datasetsDir(), deletingPrefix+"77")); !os.IsNotExist(err) {
		t.Errorf("leftover of a failed delete remains: %v", err)
	}
}

func TestIntegrationClaimOrder(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	a, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "a"})
	b, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "b"})
	queue := func(d Dataset) int64 {
		job, err := insertImport(ctx, e.db.Pool, newImport{DatasetID: d.ID, Source: SourceScan, Status: StatusQueued, OnConflict: ConflictSkip})
		if err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	a1, a2, b1 := queue(a), queue(a), queue(b)
	claim := func() int64 {
		job, err := e.svc.runner.claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			return 0
		}
		if job.Status != StatusRunning || job.StartedAt == nil {
			t.Errorf("claimed job = %+v", job)
		}
		return job.ID
	}
	if got := claim(); got != a1 {
		t.Fatalf("first claim = %d, want %d", got, a1)
	}
	if got := claim(); got != b1 {
		t.Fatalf("second claim = %d, want %d (dataset a is busy)", got, b1)
	}
	if got := claim(); got != 0 {
		t.Fatalf("third claim = %d, want none", got)
	}
	if _, err := e.db.Exec(ctx, "UPDATE dataset_imports SET status = 'succeeded' WHERE id = $1", a1); err != nil {
		t.Fatal(err)
	}
	if got := claim(); got != a2 {
		t.Fatalf("claim after a1 finished = %d, want %d", got, a2)
	}
}

func TestIntegrationCancelRunningJob(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "c"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2)})
	job, err := e.svc.StartScan(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The cancel request arrives before the worker has registered the job.
	e.svc.runner.cancel(job.ID)
	sctx, cancel := context.WithCancel(ctx)
	if err := e.svc.Start(sctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.svc.Stop() }()
	if done := e.waitJob(t, d.ID, job.ID); done.Status != StatusCanceled || done.Error != "" {
		t.Errorf("job = %+v", done)
	}
}

func TestIntegrationImageDetailReadsAnnotationNow(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	dir := filepath.Join(e.datasetsDir(), "fresh")
	writeFiles(t, dir, map[string][]byte{
		"a.png": pngFile(t, 4, 4), "a.xml": vocFile("a.png", "dent"),
		"b.png": pngFile(t, 4, 4), "b.xml": []byte("<labelme><shape/></labelme>"),
	})
	dirName := "fresh"
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "fresh", Directory: &dirName})
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, d.ID, d.ActiveImport.ID); job.Status != StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	imgs, _, _ := e.svc.ListImages(ctx, d.ID, ImageQuery{Limit: 10})
	a, b := imgs[0], imgs[1]
	detail := func(img Image) ImageDetail {
		t.Helper()
		got, err := e.svc.GetImage(ctx, d.ID, img.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := detail(b); got.AnnotationState != annotationInvalid || got.AnnotationPath == nil || *got.AnnotationPath != "b.xml" ||
		got.AnnotationError != "标注文件不是 Pascal VOC 格式（根元素不是 <annotation>）" || got.Annotation != nil {
		t.Errorf("not VOC = %+v", got)
	}

	// Files edited after the scan: the detail describes them as they are.
	writeFiles(t, dir, map[string][]byte{"a.xml": []byte("<annotation><object>"), "b.xml": vocFile("b.png")})
	if got := detail(a); got.AnnotationState != annotationInvalid || got.Annotation != nil || len(got.Objects) != 0 ||
		!strings.HasPrefix(got.AnnotationError, "标注文件无法解析（XML 格式错误）：") {
		t.Errorf("broken since the scan = %+v", got)
	}
	if got := detail(b); got.AnnotationState != annotationEmpty || got.AnnotationError != "" || got.Annotation == nil ||
		got.Annotation.Path != "b.xml" || got.Annotation.ObjectCount != 0 {
		t.Errorf("fixed since the scan = %+v", got)
	}
	if err := os.Remove(filepath.Join(dir, "a.xml")); err != nil {
		t.Fatal(err)
	}
	if got := detail(a); got.AnnotationState != annotationNone || got.AnnotationPath == nil || *got.AnnotationPath != "a.xml" || got.Annotation != nil ||
		got.AnnotationError != "标注文件已不存在，请重新扫描" {
		t.Errorf("removed since the scan = %+v", got)
	}
	// The index is only updated by a scan.
	if again, _, _ := e.svc.ListImages(ctx, d.ID, ImageQuery{Limit: 10}); again[0].AnnotationState != annotationAnnotated {
		t.Errorf("index changed without a scan: %+v", again[0])
	}
}

func TestIntegrationNonUTF8Names(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	gbk := "\xbb\xae\xba\xdb" // 划痕 in GBK, as `unzip` leaves names from Chinese Windows
	dir := filepath.Join(e.datasetsDir(), "lineA")
	writeFiles(t, dir, map[string][]byte{"ok.png": pngFile(t, 3, 3), "ok.xml": vocFile("ok.png", "dent")})
	if err := os.WriteFile(filepath.Join(dir, gbk+".png"), pngFile(t, 3, 3), 0o644); err != nil {
		t.Skipf("the file system does not accept names that are not UTF-8: %v", err)
	}
	writeFiles(t, dir, map[string][]byte{gbk + "/x.png": pngFile(t, 3, 3), gbk + ".txt": []byte("ignored anyway")})
	writeFiles(t, filepath.Join(e.datasetsDir(), gbk), map[string][]byte{"y.png": pngFile(t, 3, 3)})

	// The folder with a GBK name is not offered; the counts skip such names.
	dirs, err := e.svc.ListDirectories(ctx)
	if err != nil || len(dirs) != 1 || dirs[0].Name != "lineA" || dirs[0].ImageCount != 1 || dirs[0].AnnotationCount != 1 {
		t.Errorf("ListDirectories = %+v, %v", dirs, err)
	}

	dirName := "lineA"
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "lineA", Directory: &dirName})
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, d.ID, d.ActiveImport.ID)
	if job.Status != StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesTotal: 1, sumImagesAdded: 1, sumAnnotationsMatched: 1, sumFilesIgnored: 1, sumNamesInvalid: 2,
	})
	if d, _ = e.svc.GetDataset(ctx, d.ID); d.ImageCount != 1 || d.AnnotatedCount != 1 {
		t.Errorf("dataset = %+v", d)
	}

	// Imports into the folder index their files and report what was skipped.
	job = e.uploadArchive(t, d.ID, writeZip(t, []testEntry{{Name: "new.png", Data: string(pngFile(t, 2, 2))}}), ConflictSkip)
	if job.Status != StatusSucceeded {
		t.Fatalf("import = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 1, sumNamesInvalid: 2})
	if d, _ = e.svc.GetDataset(ctx, d.ID); d.ImageCount != 2 {
		t.Errorf("dataset after import = %+v", d)
	}
}

func TestIntegrationDeleteLeavesUndeletableFiles(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	// Like a folder copied in by another user: some files cannot be removed.
	denied := func(path string) error {
		_ = os.Remove(filepath.Join(path, "a.png"))
		return &fs.PathError{Op: "unlinkat", Path: filepath.Join(path, "root-owned", "b.png"), Err: syscall.EACCES}
	}
	e.svc.removeAll = denied

	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "leftover"})
	writeFiles(t, d.Path, map[string][]byte{"a.png": pngFile(t, 2, 2), "root-owned/b.png": pngFile(t, 2, 2)})
	res, err := e.svc.DeleteDataset(ctx, d.ID, true)
	if err != nil || res == nil || res.FilesRemoved || !strings.Contains(res.Message, "权限不足") || !strings.Contains(res.Message, d.Path) {
		t.Fatalf("DeleteDataset = %+v, %v", res, err)
	}
	_, err = e.svc.GetDataset(ctx, d.ID)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	// What is left is visible under its old name and can be added again.
	if _, err := os.Stat(filepath.Join(d.Path, "root-owned", "b.png")); err != nil {
		t.Errorf("leftover not restored: %v", err)
	}
	if dirs, err := e.svc.ListDirectories(ctx); err != nil || len(dirs) != 1 || dirs[0].Name != "leftover" || dirs[0].Registered {
		t.Errorf("ListDirectories = %+v, %v", dirs, err)
	}
	raw, _ := json.Marshal(res)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || len(body) != 2 || body["files_removed"] != false || body["message"] != res.Message {
		t.Errorf("JSON = %s", raw)
	}

	// A folder with the old name appeared meanwhile: the rest stays hidden.
	taken, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "taken"})
	e.svc.removeAll = func(path string) error {
		if err := os.Mkdir(taken.Path, 0o755); err != nil {
			t.Error(err)
		}
		return denied(path)
	}
	trash := filepath.Join(e.datasetsDir(), deletingPrefix+strconv.FormatInt(taken.ID, 10))
	res, err = e.svc.DeleteDataset(ctx, taken.ID, true)
	if err != nil || res == nil || !strings.Contains(res.Message, trash) {
		t.Fatalf("DeleteDataset = %+v, %v", res, err)
	}
	if fi, err := os.Stat(trash); err != nil || !fi.IsDir() {
		t.Errorf("leftover should stay at %s: %v", trash, err)
	}
}

func TestIntegrationCountersAfterInterruption(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "counters"})
	xml := "b.xml"
	now := dbTime(time.Now())
	if err := upsertImages(ctx, e.db.Pool, d.ID, []imageRecord{{RelPath: "a.png", Size: 10, ModTime: now, State: annotationNone}}); err != nil {
		t.Fatal(err)
	}

	// Stopping the service cancels its context; rows indexed until then
	// are still counted.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	e.svc.base = stopped
	if err := e.svc.finishIndex(d.ID); err != nil {
		t.Fatalf("finishIndex after stop: %v", err)
	}
	if got, _ := e.svc.GetDataset(ctx, d.ID); got.ImageCount != 1 || got.TotalBytes != 10 {
		t.Errorf("counters after stop = %+v", got)
	}

	// After a crash, the next start recounts the datasets of the jobs that
	// were running.
	if err := upsertImages(ctx, e.db.Pool, d.ID, []imageRecord{
		{RelPath: "b.png", Size: 20, ModTime: now, AnnPath: &xml, AnnModTime: &now, AnnSize: new(int64), State: annotationInvalid},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := insertImport(ctx, e.db.Pool, newImport{DatasetID: d.ID, Source: SourceScan, Status: StatusRunning, OnConflict: ConflictSkip}); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	if err := e.svc.Start(sctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.svc.Stop() }()
	if got, _ := e.svc.GetDataset(ctx, d.ID); got.ImageCount != 2 || got.InvalidCount != 1 || got.TotalBytes != 30 {
		t.Errorf("counters after restart = %+v", got)
	}
}
