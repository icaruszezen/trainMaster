package review

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// localEdits is what a simulated local reviewer changes in a package.
type localEdits struct {
	statuses map[int64]string                     // by review image ID
	objects  map[int64][]dataset.AnnotationObject // new annotations
	raw      map[int64][]byte                     // VOC files written as they are
	manifest func(m *reviewfile.Manifest)         // last changes to review.json
}

// makeResults turns a package into the results the local reviewer would
// export after the edits: review.json of kind results and the VOC files
// of the edits (the local reviewer adds the others too, which change
// nothing), in "<package folder>-审图结果/".
func makeResults(t *testing.T, pkg []byte, edits localEdits) []byte {
	t.Helper()
	files, content := zipEntries(t, pkg)
	top := strings.TrimSuffix(files[0].Name, "/"+reviewfile.ReadmeFile)
	m := packageManifest(t, content, top)
	m.Kind = reviewfile.KindResults
	m.ExportedAt = m.ExportedAt.Add(time.Hour)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	out := top + "-审图结果/"
	add := func(name string, data []byte) {
		w, err := zw.Create(out + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for i := range m.Images {
		img := &m.Images[i]
		if st, ok := edits.statuses[img.ID]; ok {
			img.Status = st
			now := m.ExportedAt
			img.ReviewedAt = &now
		}
		var data []byte
		if objects, ok := edits.objects[img.ID]; ok {
			data = reviewfile.EncodeAnnotation(img.SourcePath, img.Width, img.Height, objects)
		}
		if raw, ok := edits.raw[img.ID]; ok {
			data = raw
		}
		if data != nil {
			add(img.Annotation, data)
			img.HasAnnotation = true
			img.Edited = img.Edited || reviewfile.AnnotationVersion(data) != img.BaseAnnotationVersion
		}
	}
	if edits.manifest != nil {
		edits.manifest(m)
	}
	var mb bytes.Buffer
	if err := reviewfile.WriteManifest(&mb, m); err != nil {
		t.Fatal(err)
	}
	add(reviewfile.ManifestFile, mb.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// rezip zips a package again below the folder outer/, replacing the
// content of some entries (by their name in the package folder) and
// adding those of replace it does not have.
func rezip(t *testing.T, pkg []byte, replace map[string][]byte) []byte {
	t.Helper()
	return rezipNamed(t, pkg, replace, func(name string) zip.FileHeader { return zip.FileHeader{Name: "outer/" + name} })
}

// rezipNamed is rezip with the zip entries' headers made by header from
// the names in the package zip.
func rezipNamed(t *testing.T, pkg []byte, replace map[string][]byte, header func(name string) zip.FileHeader) []byte {
	t.Helper()
	files, content := zipEntries(t, pkg)
	top := strings.TrimSuffix(files[0].Name, "/"+reviewfile.ReadmeFile)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	add := func(name string, data []byte) {
		h := header(name)
		w, err := zw.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		data := content[f.Name]
		rel := strings.TrimPrefix(f.Name, top+"/")
		if r, ok := replace[rel]; ok {
			data = r
			seen[rel] = true
		}
		add(f.Name, data)
	}
	for _, rel := range slices.Sorted(maps.Keys(replace)) {
		if !seen[rel] {
			add(top+"/"+rel, replace[rel])
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (e *testEnv) upload(reviewID int64, data []byte, overwrite bool) (Upload, error) {
	return e.svc.UploadResults(context.Background(), reviewID, ResultsInput{FileName: `C:\Users\张三\Downloads\结果.zip`,
		Overwrite: overwrite, Size: int64(len(data)), Body: bytes.NewReader(data)})
}

func (e *testEnv) mustUpload(t *testing.T, reviewID int64, data []byte, overwrite bool) Upload {
	t.Helper()
	up, err := e.upload(reviewID, data, overwrite)
	if err != nil {
		t.Fatalf("UploadResults: %v", err)
	}
	return up
}

func objs(boxes ...dataset.AnnotationObject) []dataset.AnnotationObject {
	if boxes == nil {
		return []dataset.AnnotationObject{}
	}
	return boxes
}

func obj(name string, x1, y1, x2, y2 int) dataset.AnnotationObject {
	return dataset.AnnotationObject{Name: name, Xmin: x1, Ymin: y1, Xmax: x2, Ymax: y2}
}

func TestIntegrationResultsRoundTrip(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _, _ := e.exchangeReview(t)
	b, a, c, d := images[0], images[1], images[2], images[3]
	pkg, _ := e.downloadPackage(t, r.ID, PackageInput{})

	// Locally: b marked needs_sample with other boxes, a approved as it is,
	// c gets a box, d (inference failed, no VOC file) gets one and is
	// marked needs_sample.
	results := makeResults(t, pkg, localEdits{
		statuses: map[int64]string{b.ID: reviewfile.StatusNeedsSample, a.ID: reviewfile.StatusApproved,
			d.ID: reviewfile.StatusNeedsSample},
		objects: map[int64][]dataset.AnnotationObject{
			b.ID: objs(obj("scratch", 1, 1, 10, 10), obj("划痕", 20, 5, 39, 29)),
			c.ID: objs(obj("dent", 4, 4, 30, 25)),
			d.ID: objs(obj("dent", 0, 0, 100, 100)), // clamped to 40×30
		},
	})
	up := e.mustUpload(t, r.ID, results, false)
	want := UploadSummary{Images: 5, Applied: 4, StatusChanges: 3, AnnotationChanges: 3, Unchanged: 1}
	if up.Summary != want || len(up.Problems) != 0 || up.FileName != "结果.zip" || up.Overwrite || up.ID == 0 ||
		up.ReviewID != r.ID {
		t.Errorf("upload = %+v", up)
	}
	for _, tt := range []struct {
		img      Image
		status   string
		revision int
		objects  []dataset.AnnotationObject
		edited   bool
	}{
		{b, reviewfile.StatusNeedsSample, 2, objs(obj("scratch", 1, 1, 10, 10), obj("划痕", 20, 5, 39, 29)), true},
		{a, reviewfile.StatusApproved, 1, objs(obj("scratch", 3, 3, 20, 20)), false},
		{c, reviewfile.StatusPending, 1, objs(obj("dent", 4, 4, 30, 25)), true},
		{d, reviewfile.StatusNeedsSample, 2, objs(obj("dent", 0, 0, 40, 30)), true},
	} {
		got := e.detail(t, r.ID, tt.img.ID)
		if got.Status != tt.status || got.Revision != tt.revision || !reviewfile.SameObjects(got.Objects, tt.objects) ||
			got.Edited != tt.edited || got.ObjectCount != len(tt.objects) || (tt.status != "pending") != (got.ReviewedAt != nil) {
			t.Errorf("%s after the upload = %+v", tt.img.Path, got)
		}
	}
	if rv := e.review(t, r.ID); rv.LastUpload == nil || rv.LastUpload.ID != up.ID || rv.Counts.NeedsSample != 2 ||
		rv.Counts.Approved != 1 {
		t.Errorf("review after the upload = %+v", rv)
	}
	// The staged zip is gone.
	if entries, _ := os.ReadDir(e.svc.uploadsTempDir()); len(entries) != 0 {
		t.Errorf("left in the upload folder: %v", entries)
	}

	// The same results again change nothing.
	if again := e.mustUpload(t, r.ID, results, false); again.Summary != (UploadSummary{Images: 5, Unchanged: 5}) {
		t.Errorf("repeated upload = %+v", again)
	}
	if got := e.detail(t, r.ID, b.ID); got.Revision != 2 {
		t.Errorf("b after the repeated upload = %+v", got)
	}

	// A new package; meanwhile the platform marks c approved. Results that
	// change c's boxes conflict; with overwrite they are applied (the status
	// stays the platform's).
	pkg2, _ := e.downloadPackage(t, r.ID, PackageInput{})
	if _, err := e.svc.SetStatus(ctx, r.ID, c.ID, reviewfile.StatusApproved); err != nil {
		t.Fatal(err)
	}
	results2 := makeResults(t, pkg2, localEdits{
		statuses: map[int64]string{a.ID: reviewfile.StatusNeedsSample},
		objects:  map[int64][]dataset.AnnotationObject{c.ID: objs()},
	})
	up = e.mustUpload(t, r.ID, results2, false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, StatusChanges: 1, Unchanged: 3, Conflicts: 1}) ||
		len(up.Problems) != 1 || up.Problems[0] != (Problem{Path: "line_a/c.bmp", Kind: ProblemConflict, Message: msgUploadConflict}) {
		t.Errorf("conflicting upload = %+v", up)
	}
	if got := e.detail(t, r.ID, c.ID); got.ObjectCount != 1 || got.Status != reviewfile.StatusApproved {
		t.Errorf("c after the conflict = %+v", got)
	}
	up = e.mustUpload(t, r.ID, results2, true)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, AnnotationChanges: 1, Unchanged: 4}) || !up.Overwrite {
		t.Errorf("overwriting upload = %+v", up)
	}
	if got := e.detail(t, r.ID, c.ID); got.ObjectCount != 0 || got.Status != reviewfile.StatusApproved || got.Revision != 3 {
		t.Errorf("c after overwriting = %+v", got)
	}

	// The package zipped again, one VOC file edited by hand (as LabelImg
	// writes it): only that change counts, the images are ignored.
	pkg3, _ := e.downloadPackage(t, r.ID, PackageInput{})
	labelImg := []byte(`<annotation><folder>line_a</folder><filename>a.png</filename>
		<size><width>40</width><height>30</height><depth>3</depth></size>
		<object><name>scratch</name><difficult>1</difficult><bndbox><xmin>3</xmin><ymin>3</ymin><xmax>20</xmax><ymax>20</ymax></bndbox></object>
		<object><name>dent</name><bndbox><xmin>1.4</xmin><ymin>1</ymin><xmax>9</xmax><ymax>9</ymax></bndbox></object>
		</annotation>`)
	up = e.mustUpload(t, r.ID, rezip(t, pkg3, map[string][]byte{a.AnnotationPath: labelImg, a.PackagePath: []byte("not an image")}), false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, AnnotationChanges: 1, Unchanged: 4}) {
		t.Errorf("re-zipped package upload = %+v", up)
	}
	if got := e.detail(t, r.ID, a.ID); !reviewfile.SameObjects(got.Objects, objs(
		dataset.AnnotationObject{Name: "scratch", Xmin: 3, Ymin: 3, Xmax: 20, Ymax: 20, Difficult: true}, obj("dent", 1, 1, 9, 9))) {
		t.Errorf("a after the re-zipped package = %+v", got.Objects)
	}
	if uploads, total, err := e.svc.ListUploads(ctx, r.ID); err != nil || len(uploads) != 5 || uploads[0].ID != up.ID ||
		total != 5 {
		t.Errorf("ListUploads = %+v, %d, %v", uploads, total, err)
	}
}

func TestIntegrationResultsProblems(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _, _ := e.exchangeReview(t)
	b, a, c := images[0], images[1], images[2]
	pkg, _ := e.downloadPackage(t, r.ID, PackageInput{})

	// An invalid VOC file skips the whole image (its status too); so does a
	// box without area; an image no longer in the review and an
	// image whose IDs do not match are reported.
	unknown := reviewfile.ManifestImage{}
	results := makeResults(t, pkg, localEdits{
		statuses: map[int64]string{b.ID: reviewfile.StatusApproved, a.ID: reviewfile.StatusApproved},
		raw:      map[int64][]byte{b.ID: []byte("<annotation><object>"), c.ID: []byte("<html></html>")},
		objects:  map[int64][]dataset.AnnotationObject{a.ID: objs(obj("scratch", 5, 5, 5, 9))},
		manifest: func(m *reviewfile.Manifest) {
			unknown = m.Images[0]
			unknown.ID, unknown.ImageID = 99999, 88888
			unknown.Path, unknown.Annotation = "line_a/NG/gone.png", "line_a/NG/gone.xml"
			unknown.Status = reviewfile.StatusNeedsSample
			m.Images = append(m.Images, unknown)
			m.Images[3].ImageID++ // d
			m.Images[3].Status = reviewfile.StatusApproved
		},
	})
	up := e.mustUpload(t, r.ID, results, false)
	if up.Summary != (UploadSummary{Images: 6, Invalid: 4, Unchanged: 1, Unknown: 1}) || len(up.Problems) != 5 {
		t.Fatalf("upload = %+v", up)
	}
	for i, want := range []Problem{
		{Path: b.PackagePath, Kind: ProblemInvalid, Message: "标注文件无法解析（XML 格式错误）"},
		{Path: a.PackagePath, Kind: ProblemInvalid, Message: "标注无效：第 1 个目标（scratch）的框在图片范围内的宽或高为 0"},
		{Path: c.PackagePath, Kind: ProblemInvalid, Message: "标注文件不是 Pascal VOC 格式"},
		{Path: images[3].PackagePath, Kind: ProblemInvalid, Message: "与审图任务不一致"},
		{Path: "line_a/NG/gone.png", Kind: ProblemUnknown, Message: "已不在审图任务中"},
	} {
		got := up.Problems[i]
		if got.Path != want.Path || got.Kind != want.Kind || !strings.Contains(got.Message, want.Message) {
			t.Errorf("problem %d = %+v, want %+v", i, got, want)
		}
	}
	for _, img := range images[:4] {
		if got := e.detail(t, r.ID, img.ID); got.Revision != 0 || got.Status != reviewfile.StatusPending {
			t.Errorf("%s changed: %+v", img.Path, got)
		}
	}

	// At most MaxProblems problems are kept; the summary counts them all.
	many := makeResults(t, pkg, localEdits{manifest: func(m *reviewfile.Manifest) {
		base := m.Images[0]
		m.Images = nil
		for i := range MaxProblems + 5 {
			img := base
			img.ID, img.ImageID = int64(100000+i), 1
			img.Path, img.Annotation = fmt.Sprintf("line_a/x%d.png", i), fmt.Sprintf("line_a/x%d.xml", i)
			m.Images = append(m.Images, img)
		}
	}})
	if up := e.mustUpload(t, r.ID, many, false); up.Summary.Unknown != MaxProblems+5 || len(up.Problems) != MaxProblems {
		t.Errorf("many unknown images: %+v, %d problems", up.Summary, len(up.Problems))
	}

	// Uploads that are refused as a whole.
	otherReview := makeResults(t, pkg, localEdits{manifest: func(m *reviewfile.Manifest) { m.Review.ID = 4242 }})
	_, err := e.upload(r.ID, otherReview, false)
	wantKind(t, err, ErrInvalid, "这是审图任务 #4242 的审图结果，不是这个任务的")
	_, err = e.upload(r.ID, rezip(t, pkg, nil), false)
	if err != nil {
		t.Errorf("re-zipped package: %v", err)
	}
	// The folder the local reviewer works in, zipped: the statuses set
	// there are only in its progress file.
	_, err = e.upload(r.ID, rezip(t, pkg, map[string][]byte{reviewfile.ProgressFile: []byte(`{}`)}), false)
	wantKind(t, err, ErrInvalid, "请在本地审图程序中点“导出审图结果”")
	_, contents := zipEntries(t, pkg)
	otherPackage := rezip(t, pkg, map[string][]byte{"review.json": bytes.Replace(
		contents[fmt.Sprintf("审图-%d-外观检测 v1 审图/review.json", r.ID)],
		[]byte(fmt.Sprintf(`"review": {`+"\n"+`    "id": %d,`, r.ID)), []byte(`"review": {`+"\n"+`    "id": 77,`), 1)})
	_, err = e.upload(r.ID, otherPackage, false)
	wantKind(t, err, ErrInvalid, "这是审图任务 #77 的审图包")
	_, err = e.upload(r.ID, []byte("hello, not a zip"), false)
	wantKind(t, err, ErrInvalid, "不是有效的 zip 压缩包")
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	w, _ := zw.Create("a/readme.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	_, err = e.upload(r.ID, plain.Bytes(), false)
	wantKind(t, err, ErrInvalid, "压缩包中没有 review.json")
	badManifest := rezip(t, pkg, map[string][]byte{"review.json": []byte(`{"format": "trainmaster-review", "version": 1, "kind": "x"}`)})
	_, err = e.upload(r.ID, badManifest, false)
	wantKind(t, err, ErrInvalid, "review.json 无效：kind")
	_, err = e.upload(999, results, false)
	wantKind(t, err, ErrNotFound, "审图任务不存在")
	_, err = e.svc.UploadResults(ctx, r.ID, ResultsInput{Size: 10, Body: strings.NewReader("short")})
	wantKind(t, err, ErrBadRequest, "收到 5 字节，应为 10 字节")
	_, err = e.svc.UploadResults(ctx, r.ID, ResultsInput{Size: 0, Body: strings.NewReader("")})
	wantKind(t, err, ErrInvalid, "上传的文件是空的")
	_, err = e.svc.UploadResults(ctx, r.ID, ResultsInput{Size: MaxResultsBytes + 1, Body: strings.NewReader("")})
	wantKind(t, err, ErrTooLarge, "4 GiB")
	e.svc.usage = func() (storage.Usage, error) { return storage.Usage{FreeBytes: 100 << 20}, nil }
	_, err = e.upload(r.ID, results, false)
	wantKind(t, err, ErrInsufficientStorage, "磁盘剩余空间不足")
	if entries, _ := os.ReadDir(e.svc.uploadsTempDir()); len(entries) != 0 {
		t.Errorf("left in the upload folder: %v", entries)
	}

	// Images removed from the review since the download are unknown.
	e.svc.usage = func() (storage.Usage, error) { return storage.Usage{}, storage.ErrUsageUnsupported }
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_images WHERE id = $1", a.ImageID); err != nil {
		t.Fatal(err)
	}
	up = e.mustUpload(t, r.ID, makeResults(t, pkg, localEdits{statuses: map[int64]string{a.ID: reviewfile.StatusApproved,
		b.ID: reviewfile.StatusApproved}}), false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, StatusChanges: 1, Unchanged: 3, Unknown: 1}) {
		t.Errorf("upload after a removal = %+v", up)
	}
	if uploads, total, err := e.svc.ListUploads(ctx, r.ID); err != nil || len(uploads) != 4 || total != 4 {
		t.Errorf("ListUploads = %d, %d, %v", len(uploads), total, err)
	}
	_, _, err = e.svc.ListUploads(ctx, 999)
	wantKind(t, err, ErrNotFound, "审图任务不存在")
}

func TestIntegrationStagedUploadsCleanup(t *testing.T) {
	e := newTestEnv(t)
	dir := e.svc.uploadsTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh, busy := dir+"/old.zip", dir+"/fresh.zip", dir+"/busy.zip"
	for _, p := range []string{old, fresh, busy} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := e.clock.Now().Add(-2 * time.Hour)
	for _, p := range []string{old, busy} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, e.clock.Now(), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.svc.busy.add(busy)
	e.svc.cleanUploadTemp()
	if exists(old) || !exists(fresh) || !exists(busy) {
		t.Errorf("after cleanup: old %v, fresh %v, busy %v", exists(old), exists(fresh), exists(busy))
	}
}

// TestIntegrationResultsContinuation uploads results of one package again
// while the review goes on: the platform takes the local state, changes
// back to the package's included, without reporting its own earlier
// upload as a conflict; changes made on the platform meanwhile still
// conflict.
func TestIntegrationResultsContinuation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, _, _ := e.exchangeReview(t)
	b, a, c, d := images[0], images[1], images[2], images[3]
	pkg, _ := e.downloadPackage(t, r.ID, PackageInput{})
	_, contents := zipEntries(t, pkg)
	top := fmt.Sprintf("审图-%d-外观检测 v1 审图/", r.ID)
	packagedB := contents[top+b.AnnotationPath]
	if packagedB == nil {
		t.Fatalf("b has no VOC file in the package")
	}

	up := e.mustUpload(t, r.ID, makeResults(t, pkg, localEdits{
		statuses: map[int64]string{a.ID: reviewfile.StatusApproved, d.ID: reviewfile.StatusNeedsSample},
		objects:  map[int64][]dataset.AnnotationObject{b.ID: objs(obj("dent", 2, 2, 20, 20))},
	}), false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 3, StatusChanges: 2, AnnotationChanges: 1, Unchanged: 2}) {
		t.Fatalf("first upload = %+v", up)
	}

	// Later results of the package: a back to pending, b with its packaged
	// VOC file again, d approved, c approved.
	later := makeResults(t, pkg, localEdits{
		statuses: map[int64]string{d.ID: reviewfile.StatusApproved, c.ID: reviewfile.StatusApproved},
		raw:      map[int64][]byte{b.ID: packagedB},
	})
	up = e.mustUpload(t, r.ID, later, false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 4, StatusChanges: 3, AnnotationChanges: 1, Unchanged: 1}) ||
		len(up.Problems) != 0 {
		t.Errorf("later upload = %+v", up)
	}
	for _, tt := range []struct {
		img     Image
		status  string
		objects []dataset.AnnotationObject
	}{
		{a, reviewfile.StatusPending, objs(obj("scratch", 3, 3, 20, 20))},
		{b, reviewfile.StatusPending, objs(obj("scratch", 1, 1, 10, 10), obj("dent", 2, 2, 12, 12))},
		{c, reviewfile.StatusApproved, objs()},
		{d, reviewfile.StatusApproved, objs()},
	} {
		got := e.detail(t, r.ID, tt.img.ID)
		if got.Status != tt.status || !reviewfile.SameObjects(got.Objects, tt.objects) ||
			(tt.status != reviewfile.StatusPending) != (got.ReviewedAt != nil) {
			t.Errorf("%s after the later upload = %+v", tt.img.Path, got)
		}
	}
	if got := e.detail(t, r.ID, b.ID); got.Edited {
		t.Errorf("b with the packaged boxes again is edited: %+v", got)
	}
	if again := e.mustUpload(t, r.ID, later, false); again.Summary != (UploadSummary{Images: 5, Unchanged: 5}) {
		t.Errorf("later upload repeated = %+v", again)
	}

	// The package zipped again (LabelImg) continues for its VOC files, but
	// it has no statuses of its own: d keeps the status uploaded before.
	up = e.mustUpload(t, r.ID, rezip(t, pkg, map[string][]byte{
		d.AnnotationPath: reviewfile.EncodeAnnotation(d.Path, d.Width, d.Height, objs(obj("dent", 5, 5, 15, 15))),
	}), false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, AnnotationChanges: 1, Unchanged: 4}) {
		t.Errorf("re-zipped package upload = %+v", up)
	}
	if got := e.detail(t, r.ID, d.ID); got.Status != reviewfile.StatusApproved || got.ObjectCount != 1 {
		t.Errorf("d after the re-zipped package = %+v", got)
	}

	// Changed on the platform meanwhile: a conflict, also for an image the
	// package's results changed before; the others still continue.
	if _, err := e.svc.SetStatus(ctx, r.ID, c.ID, reviewfile.StatusNeedsSample); err != nil {
		t.Fatal(err)
	}
	up = e.mustUpload(t, r.ID, makeResults(t, pkg, localEdits{
		statuses: map[int64]string{c.ID: reviewfile.StatusApproved, d.ID: reviewfile.StatusPending},
	}), false)
	if up.Summary != (UploadSummary{Images: 5, Applied: 1, StatusChanges: 1, Unchanged: 3, Conflicts: 1}) ||
		len(up.Problems) != 1 || up.Problems[0] != (Problem{Path: c.PackagePath, Kind: ProblemConflict, Message: msgUploadConflict}) {
		t.Errorf("upload after a change on the platform = %+v", up)
	}
	if got := e.detail(t, r.ID, c.ID); got.Status != reviewfile.StatusNeedsSample {
		t.Errorf("c after the conflict = %+v", got)
	}
	if got := e.detail(t, r.ID, d.ID); got.Status != reviewfile.StatusPending {
		t.Errorf("d after the upload = %+v", got)
	}

	// Results of another package of the review do not continue from this
	// one's: d changed since that package was made.
	pkg2, _ := e.downloadPackage(t, r.ID, PackageInput{})
	if _, err := e.svc.SetStatus(ctx, r.ID, d.ID, reviewfile.StatusApproved); err != nil {
		t.Fatal(err)
	}
	other := makeResults(t, pkg2, localEdits{statuses: map[int64]string{d.ID: reviewfile.StatusNeedsSample}})
	if up := e.mustUpload(t, r.ID, other, false); up.Summary.Conflicts != 1 || up.Summary.Applied != 0 {
		t.Errorf("upload of another package = %+v", up)
	}
}

// TestIntegrationResultsGBKNames uploads a package zipped again by a zip
// tool of Chinese Windows, which writes the entry names in GBK (and "\"
// between folders): the VOC files under Chinese names are still found.
func TestIntegrationResultsGBKNames(t *testing.T) {
	e := newTestEnv(t)
	m := e.addModel(t, e.project(t, "外观检测"), testModel{})
	ds := e.addDataset(t, "产线", "产线", testImage{rel: "划痕.png"}, testImage{rel: "NG/凹陷.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}})
	node := e.node(t, "node-1")
	e.claim(t, node)
	e.inferAll(t, r.ID, node)
	images := e.images(t, r.ID, ImageQuery{})
	var scratch Image
	for _, img := range images {
		if img.AnnotationPath == "产线/划痕.xml" {
			scratch = img
		}
	}
	if scratch.ID == 0 {
		t.Fatalf("images = %+v", images)
	}
	pkg, _ := e.downloadPackage(t, r.ID, PackageInput{})
	gbk := simplifiedchinese.GBK.NewEncoder()
	zipped := rezipNamed(t, pkg, map[string][]byte{
		scratch.AnnotationPath: reviewfile.EncodeAnnotation(scratch.Path, scratch.Width, scratch.Height,
			objs(obj("划痕", 2, 2, 30, 20))),
	}, func(name string) zip.FileHeader {
		encoded, err := gbk.String(strings.ReplaceAll("压缩/"+name, "/", `\`))
		if err != nil {
			t.Fatal(err)
		}
		return zip.FileHeader{Name: encoded, NonUTF8: true}
	})
	up := e.mustUpload(t, r.ID, zipped, false)
	if up.Summary != (UploadSummary{Images: 2, Applied: 1, AnnotationChanges: 1, Unchanged: 1}) || len(up.Problems) != 0 {
		t.Errorf("upload = %+v", up)
	}
	if got := e.detail(t, r.ID, scratch.ID); !reviewfile.SameObjects(got.Objects, objs(obj("划痕", 2, 2, 30, 20))) {
		t.Errorf("划痕.png after the upload = %+v", got.Objects)
	}
}

// TestReadResultsFolderEntries reads zips whose review.json entry is a
// folder by its mode, without a "/" at the end of its name: it is no
// review.json.
func TestReadResultsFolderEntries(t *testing.T) {
	for name, header := range map[string]func() *zip.FileHeader{
		"unix mode": func() *zip.FileHeader {
			h := &zip.FileHeader{Name: "top/review.json"}
			h.SetMode(fs.ModeDir | 0o755)
			return h
		},
		"MS-DOS attribute": func() *zip.FileHeader {
			return &zip.FileHeader{Name: "top/review.json", CreatorVersion: 0, ExternalAttrs: 0x10}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			zw := zip.NewWriter(&b)
			h := header()
			if _, err := zw.CreateHeader(h); err != nil {
				t.Fatal(err)
			}
			w, _ := zw.Create("top/README.txt")
			_, _ = w.Write([]byte("x"))
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
			if err != nil {
				t.Fatal(err)
			}
			if !zr.File[0].FileInfo().IsDir() {
				t.Fatalf("the entry is no folder: %v", zr.File[0].Mode())
			}
			_, err = (&Service{}).readResults(1, zr)
			wantKind(t, err, ErrInvalid, "压缩包中没有 review.json")
		})
	}
}

func TestIntegrationListedTotals(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, _, _, _ := e.exchangeReview(t)
	if _, err := e.db.Exec(ctx, `INSERT INTO review_uploads (review_id, file_name, overwrite, summary)
		SELECT $1, 'x.zip', false, '{}' FROM generate_series(1, 51)`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, on_existing, status)
		SELECT $1, '{1}', '{approved}', 'skip', 'succeeded' FROM generate_series(1, 52)`, r.ID); err != nil {
		t.Fatal(err)
	}
	if uploads, total, err := e.svc.ListUploads(ctx, r.ID); err != nil || len(uploads) != 50 || total != 51 {
		t.Errorf("ListUploads = %d, %d, %v", len(uploads), total, err)
	}
	if merges, total, err := e.svc.ListMerges(ctx, r.ID); err != nil || len(merges) != 50 || total != 52 {
		t.Errorf("ListMerges = %d, %d, %v", len(merges), total, err)
	}
}
