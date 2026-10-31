package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// uploadArchiveAs uploads an archive with the given import settings and
// waits for the job.
func (e *testEnv) uploadArchiveAs(t *testing.T, datasetID int64, data []byte, in CreateImportInput) Import {
	t.Helper()
	ctx := context.Background()
	in.Source, in.FileName, in.FileSize = SourceArchive, "batch.zip", int64(len(data))
	job, err := e.svc.CreateImport(ctx, datasetID, in)
	if err != nil {
		t.Fatalf("CreateImport: %v", err)
	}
	if job, err = e.svc.WriteArchiveChunk(ctx, datasetID, job.ID, 0, bytes.NewReader(data)); err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if job, err = e.svc.CommitImport(ctx, datasetID, job.ID); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	return e.waitJob(t, datasetID, job.ID)
}

// datasetFiles lists the files below dir (slash-separated relative paths).
func datasetFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// imageObjects returns the objects of every annotated image of a dataset
// by path, as the index and the VOC files on disk describe them.
func (e *testEnv) imageObjects(t *testing.T, datasetID int64) map[string][]AnnotationObject {
	t.Helper()
	ctx := context.Background()
	imgs, _, err := e.svc.ListImages(ctx, datasetID, ImageQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]AnnotationObject{}
	for _, img := range imgs {
		d, err := e.svc.GetImage(ctx, datasetID, img.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.AnnotationState == annotationAnnotated || d.AnnotationState == annotationEmpty {
			out[img.Path] = d.Objects
		}
	}
	return out
}

func yoloLine(id int, cx, cy, w, h float64) string {
	return fmt.Sprintf("%d %g %g %g %g\n", id, cx, cy, w, h)
}

func TestIntegrationYOLOArchiveImport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "yolo"})
	img := string(pngFile(t, 100, 50))
	archive := writeZip(t, []testEntry{
		{Name: "set/classes.txt", Data: "scratch\ndent\n"},
		{Name: "set/data.yaml", Data: "names: [scratch, dent]"},
		{Name: "set/images/train/a.png", Data: img},
		{Name: "set/labels/train/a.txt", Data: yoloLine(0, 0.5, 0.5, 0.2, 0.4) + yoloLine(1, 0.1, 0.1, 0.2, 0.2)},
		{Name: "set/images/train/neg.png", Data: img},
		{Name: "set/labels/train/neg.txt", Data: ""},
		{Name: "set/images/val/b.png", Data: img},
		{Name: "set/images/val/b.txt", Data: "0 0.5 0.5\n"}, // malformed: imported unannotated
		{Name: "set/images/val/b.xml", Data: string(vocFile("b.png", "voc"))},
		{Name: "set/images/val/c.png", Data: img},
		{Name: "set/labels/train/orphan.txt", Data: yoloLine(0, 0.5, 0.5, 0.1, 0.1)},
	})
	job := e.uploadArchiveAs(t, d.ID, archive, CreateImportInput{AnnotationFormat: FormatYOLO, Classes: []string{"scratch", " dent"}})
	if job.Status != StatusSucceeded || job.AnnotationFormat != FormatYOLO || !slices.Equal(job.Classes, []string{"scratch", "dent"}) {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesAdded: 4, sumAnnotationsMatched: 2, sumAnnotationsConverted: 2, sumAnnotationsFailed: 1,
		sumAnnotationsOrphan: 1, sumObjectsSkipped: 0,
		sumFilesIgnored: 3, // classes.txt, data.yaml, b.xml
	})
	want := []string{"images/train/a.png", "images/train/a.xml", "images/train/neg.png", "images/train/neg.xml",
		"images/val/b.png", "images/val/c.png"}
	if got := datasetFiles(t, d.Path); !slices.Equal(got, want) {
		t.Errorf("dataset files = %v\nwant %v", got, want)
	}
	objs := e.imageObjects(t, d.ID)
	wantObjs := map[string][]AnnotationObject{
		"images/train/a.png":   {{Name: "scratch", Xmin: 40, Ymin: 15, Xmax: 60, Ymax: 35}, {Name: "dent", Xmin: 0, Ymin: 0, Xmax: 20, Ymax: 10}},
		"images/train/neg.png": {},
	}
	if !maps.EqualFunc(objs, wantObjs, slices.Equal) {
		t.Errorf("objects = %v\nwant %v", objs, wantObjs)
	}
	xml, _ := os.ReadFile(filepath.Join(d.Path, "images/train/a.xml"))
	for _, part := range []string{"<filename>a.png</filename>", "<width>100</width>", "<height>50</height>", "<folder>train</folder>"} {
		if !bytes.Contains(xml, []byte(part)) {
			t.Errorf("a.xml lacks %s:\n%s", part, xml)
		}
	}
	d, _ = e.svc.GetDataset(ctx, d.ID)
	if d.ImageCount != 4 || d.AnnotatedCount != 1 || d.EmptyCount != 1 || d.LabelCount != 2 {
		t.Errorf("dataset = %+v", d)
	}
}

func TestIntegrationYOLOClassOutOfRange(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "yolo"})
	img := string(pngFile(t, 10, 10))
	archive := writeZip(t, []testEntry{
		{Name: "a.png", Data: img}, {Name: "a.txt", Data: yoloLine(0, 0.5, 0.5, 0.2, 0.2)},
		{Name: "b.png", Data: img}, {Name: "b.txt", Data: yoloLine(0, 0.5, 0.5, 0.2, 0.2) + yoloLine(2, 0.5, 0.5, 0.2, 0.2)},
	})
	job := e.uploadArchiveAs(t, d.ID, archive, CreateImportInput{AnnotationFormat: FormatYOLO, Classes: []string{"a", "b"}})
	want := "标注文件 b.txt 第 2 行的类别编号为 2，但只提供了 2 个类别（编号 0–1），请检查类别顺序后重新导入"
	if job.Status != StatusFailed || job.Error != want {
		t.Errorf("job = %s %q, want failed with %q", job.Status, job.Error, want)
	}
	if got := datasetFiles(t, d.Path); len(got) != 0 {
		t.Errorf("a failed conversion must not import anything: %v", got)
	}
}

func TestIntegrationXAnyLabelingFolderImport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "xal"})
	// An existing image and annotation, kept in skip mode.
	writeFiles(t, d.Path, map[string][]byte{"old.png": pngFile(t, 8, 8), "old.xml": vocFile("old.png", "kept")})
	img := string(pngFile(t, 64, 48))
	shapes := func(imagePath string, shapes ...string) string {
		return fmt.Sprintf(`{"version": "4.2.3", "flags": {}, "imagePath": %q, "imageData": null, "imageWidth": 64, "imageHeight": 48, "shapes": [%s]}`,
			imagePath, strings.Join(shapes, ","))
	}
	rect := `{"label": "划痕", "points": [[1, 2], [11, 2], [11, 12], [1, 12]], "shape_type": "rectangle", "difficult": true}`
	poly := `{"label": "dent", "points": [[20, 5], [40, 30], [25, 40]], "shape_type": "polygon"}`
	point := `{"label": "pt", "points": [[3, 3]], "shape_type": "point"}`

	job, err := e.svc.CreateImport(ctx, d.ID, CreateImportInput{Source: SourceFolder, FileName: "batch", FileCount: 8,
		FileSize: 1000, AnnotationFormat: FormatXAnyLabeling, Classes: []string{"ignored"}})
	if err != nil || job.AnnotationFormat != FormatXAnyLabeling || len(job.Classes) != 0 {
		t.Fatalf("CreateImport = %+v, %v", job, err)
	}
	job, err = e.svc.AddFolderFiles(ctx, d.ID, job.ID, fileSource(
		[2]string{"OK/a.png", img}, [2]string{"OK/a.json", shapes("a.png", rect, poly, point)},
		[2]string{"OK/a.xml", string(vocFile("a.png", "voc"))}, // not uploaded in this format
		// Labels saved to another folder: paired by imagePath.
		[2]string{"NG/b.png", img}, [2]string{"labels/b.json", shapes(`..\NG\b.png`, rect)},
		[2]string{"NG/c.png", img}, [2]string{"NG/c.json", `{"not": "labelme"}`},
		[2]string{"old.png", img}, [2]string{"old.json", shapes("old.png", rect)},
		[2]string{"stray.json", shapes("missing.png")},
	))
	if err != nil || job.ReceivedFiles != 9 {
		t.Fatalf("AddFolderFiles = %+v, %v", job, err)
	}
	if job, err = e.svc.CommitImport(ctx, d.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	job = e.waitJob(t, d.ID, job.ID)
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesAdded: 3, sumImagesSkipped: 1, sumAnnotationsConverted: 3, sumAnnotationsMatched: 2,
		sumAnnotationsFailed: 1, sumAnnotationsOrphan: 1, sumObjectsSkipped: 1,
	})
	want := []string{"NG/b.png", "NG/b.xml", "NG/c.png", "OK/a.png", "OK/a.xml", "old.png", "old.xml"}
	if got := datasetFiles(t, d.Path); !slices.Equal(got, want) {
		t.Errorf("dataset files = %v\nwant %v", got, want)
	}
	box := AnnotationObject{Name: "划痕", Xmin: 1, Ymin: 2, Xmax: 11, Ymax: 12, Difficult: true}
	wantObjs := map[string][]AnnotationObject{
		"OK/a.png": {box, {Name: "dent", Xmin: 20, Ymin: 5, Xmax: 40, Ymax: 40}},
		"NG/b.png": {box},
		"old.png":  {{Name: "kept", Xmin: 0, Ymin: 1, Xmax: 5, Ymax: 9}},
	}
	if objs := e.imageObjects(t, d.ID); !maps.EqualFunc(objs, wantObjs, slices.Equal) {
		t.Errorf("objects = %v\nwant %v", objs, wantObjs)
	}
}

func TestIntegrationImportFormatValidation(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "v"})
	in := CreateImportInput{Source: SourceArchive, FileName: "a.zip", FileSize: 10}
	job, err := e.svc.CreateImport(ctx, d.ID, in)
	if err != nil || job.AnnotationFormat != FormatVOC || job.Classes == nil || len(job.Classes) != 0 {
		t.Fatalf("default = %+v, %v", job, err)
	}
	for _, c := range []struct {
		format  string
		classes []string
		want    string
	}{
		{"coco", nil, "标注格式必须是 voc（Pascal VOC）、xanylabeling（X-AnyLabeling）或 yolo（YOLO）"},
		{FormatYOLO, nil, "YOLO 格式需要提供类别顺序"},
		{FormatYOLO, []string{"a", "a"}, "重复"},
	} {
		in.AnnotationFormat, in.Classes = c.format, c.classes
		_, err := e.svc.CreateImport(ctx, d.ID, in)
		wantKind(t, err, ErrInvalid, c.want)
	}
	// Scans are VOC.
	scan, err := e.svc.StartScan(ctx, d.ID)
	if err != nil || scan.AnnotationFormat != FormatVOC {
		t.Errorf("scan = %+v, %v", scan, err)
	}
}

// readZip returns the files of a zip archive by name.
func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func (e *testEnv) export(t *testing.T, datasetID int64, in ExportInput) (string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	ticket, err := e.svc.CreateExport(ctx, datasetID, in)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	ex, err := e.svc.OpenExport(ctx, datasetID, ticket.Token)
	if err != nil {
		t.Fatalf("OpenExport: %v", err)
	}
	defer ex.Close()
	var b bytes.Buffer
	if err := ex.Write(ctx, &b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ex.FileName != ticket.FileName {
		t.Errorf("file name %q != %q", ex.FileName, ticket.FileName)
	}
	return ticket.FileName, readZip(t, b.Bytes())
}

func TestIntegrationExport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "划痕-A"})
	img := pngFile(t, 64, 48)
	writeFiles(t, d.Path, map[string][]byte{
		"OK/a.png":               img,
		"OK/a.xml":               vocFile("a.png", "scratch", "dent", "other"),
		"OK/neg.png":             img,
		"OK/neg.xml":             vocFile("neg.png"),
		"NG/bad.png":             img,
		"NG/bad.xml":             []byte("<not-voc/>"),
		"plain.png":              img,
		"JPEGImages/v.png":       img,
		"Annotations/v.xml":      vocFile("v.png", "dent"),
		"Annotations/orphan.xml": vocFile("x.png", "dent"),
		"gone.png":               img,
		"notes.txt":              []byte("not exported"),
	})
	scan, _ := e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)
	if err := os.Remove(filepath.Join(d.Path, "gone.png")); err != nil { // deleted since the scan
		t.Fatal(err)
	}
	keys := func(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

	name, files := e.export(t, d.ID, ExportInput{Format: FormatVOC})
	want := []string{"划痕-A/Annotations/v.xml", "划痕-A/JPEGImages/v.png", "划痕-A/NG/bad.png", "划痕-A/NG/bad.xml",
		"划痕-A/OK/a.png", "划痕-A/OK/a.xml", "划痕-A/OK/neg.png", "划痕-A/OK/neg.xml", "划痕-A/plain.png"}
	if name != "划痕-A-voc.zip" || !slices.Equal(keys(files), want) {
		t.Errorf("VOC export %s = %v\nwant %v", name, keys(files), want)
	}
	if files["划痕-A/OK/a.png"] != string(img) || files["划痕-A/OK/a.xml"] != string(vocFile("a.png", "scratch", "dent", "other")) {
		t.Error("VOC export must copy the files as they are")
	}

	name, files = e.export(t, d.ID, ExportInput{Format: FormatXAnyLabeling})
	want = []string{"划痕-A/JPEGImages/v.json", "划痕-A/JPEGImages/v.png", "划痕-A/NG/bad.png", "划痕-A/OK/a.json",
		"划痕-A/OK/a.png", "划痕-A/OK/neg.json", "划痕-A/OK/neg.png", "划痕-A/plain.png"}
	if name != "划痕-A-xanylabeling.zip" || !slices.Equal(keys(files), want) {
		t.Errorf("X-AnyLabeling export %s = %v\nwant %v", name, keys(files), want)
	}
	f, err := parseXAnyLabeling([]byte(files["划痕-A/OK/a.json"]))
	if err != nil || f.ImagePath != "a.png" || f.Width != 64 || len(f.Shapes) != 3 {
		t.Errorf("a.json = %+v, %v", f, err)
	}

	name, files = e.export(t, d.ID, ExportInput{Format: FormatYOLO, Classes: []string{"dent", "scratch", "unused"}})
	want = []string{"划痕-A/classes.txt", "划痕-A/data.yaml",
		"划痕-A/images/JPEGImages/v.png", "划痕-A/images/NG/bad.png", "划痕-A/images/OK/a.png", "划痕-A/images/OK/neg.png",
		"划痕-A/images/plain.png", "划痕-A/labels/JPEGImages/v.txt", "划痕-A/labels/OK/a.txt", "划痕-A/labels/OK/neg.txt"}
	if name != "划痕-A-yolo.zip" || !slices.Equal(keys(files), want) {
		t.Errorf("YOLO export %s = %v\nwant %v", name, keys(files), want)
	}
	// vocFile boxes: object i spans x i..i+5, y 1..9 in a 64×48 image; "other" is not exported.
	if got, want := files["划痕-A/labels/OK/a.txt"], "1 0.039062 0.104167 0.078125 0.166667\n0 0.054688 0.104167 0.078125 0.166667\n"; got != want {
		t.Errorf("a.txt = %q, want %q", got, want)
	}
	if files["划痕-A/labels/OK/neg.txt"] != "" || files["划痕-A/classes.txt"] != "dent\nscratch\nunused\n" {
		t.Errorf("neg.txt %q, classes.txt %q", files["划痕-A/labels/OK/neg.txt"], files["划痕-A/classes.txt"])
	}
	if y := files["划痕-A/data.yaml"]; !strings.Contains(y, "train: images\nval: images\nnc: 3\n") {
		t.Errorf("data.yaml:\n%s", y)
	}

	// Tokens belong to one dataset and expire.
	ticket, _ := e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC})
	other, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})
	_, err = e.svc.OpenExport(ctx, other.ID, ticket.Token)
	wantKind(t, err, ErrNotFound, "下载链接已失效")
	_, err = e.svc.OpenExport(ctx, d.ID, "NOPE")
	wantKind(t, err, ErrNotFound, "下载链接已失效")
	e.svc.exports.mu.Lock()
	r := e.svc.exports.m[ticket.Token]
	r.expires = r.expires.Add(-2 * exportTTL)
	e.svc.exports.m[ticket.Token] = r
	e.svc.exports.mu.Unlock()
	_, err = e.svc.OpenExport(ctx, d.ID, ticket.Token)
	wantKind(t, err, ErrNotFound, "下载链接已失效")

	_, err = e.svc.CreateExport(ctx, other.ID, ExportInput{Format: FormatVOC})
	wantKind(t, err, ErrConflict, "没有图片")
	_, err = e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatYOLO})
	wantKind(t, err, ErrInvalid, "需要提供类别顺序")
	_, err = e.svc.CreateExport(ctx, d.ID, ExportInput{Format: "coco"})
	wantKind(t, err, ErrInvalid, "导出格式必须是")
	_, err = e.svc.CreateExport(ctx, 9999, ExportInput{Format: FormatVOC})
	wantKind(t, err, ErrNotFound, "数据集不存在")
	if err := os.Rename(d.Path, d.Path+"-moved"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC})
	wantKind(t, err, ErrConflict, "数据集文件夹不存在")
}

// A YOLO export imported again (with its classes.txt) gives the same
// boxes, in a layout without images/images/.
func TestIntegrationYOLORoundTrip(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	src, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "src"})
	img := pngFile(t, 64, 48)
	voc := []byte(`<annotation><filename>a.png</filename><object><name>dent</name><bndbox><xmin>3</xmin><ymin>4</ymin><xmax>40</xmax><ymax>30</ymax></bndbox></object>` +
		`<object><name>scratch</name><bndbox><xmin>0</xmin><ymin>0</ymin><xmax>64</xmax><ymax>48</ymax></bndbox></object></annotation>`)
	writeFiles(t, src.Path, map[string][]byte{"train/a.png": img, "train/a.xml": voc, "val/b.png": img, "val/b.xml": vocFile("b.png")})
	scan, _ := e.svc.StartScan(ctx, src.ID)
	e.waitJob(t, src.ID, scan.ID)

	classes := []string{"scratch", "dent"}
	_, files := e.export(t, src.ID, ExportInput{Format: FormatYOLO, Classes: classes})
	if y := files["src/data.yaml"]; !strings.Contains(y, "train: images/train\nval: images/val\n") {
		t.Errorf("data.yaml:\n%s", y)
	}
	var entries []testEntry
	for name, data := range files {
		entries = append(entries, testEntry{Name: name, Data: data})
	}
	slices.SortFunc(entries, func(a, b testEntry) int { return strings.Compare(a.Name, b.Name) })

	dst, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "dst"})
	job := e.uploadArchiveAs(t, dst.ID, writeZip(t, entries), CreateImportInput{AnnotationFormat: FormatYOLO, Classes: classes})
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 2, sumAnnotationsConverted: 2, sumFilesIgnored: 2})
	got, want := e.imageObjects(t, dst.ID), e.imageObjects(t, src.ID)
	want = map[string][]AnnotationObject{"images/train/a.png": want["train/a.png"], "images/val/b.png": want["val/b.png"]}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("round trip = %v\nwant %v", got, want)
	}

	// Exported again, the images stay in images/ (no images/images/).
	_, files = e.export(t, dst.ID, ExportInput{Format: FormatYOLO, Classes: classes})
	if _, ok := files["dst/images/train/a.png"]; !ok {
		t.Errorf("second export = %v", slices.Sorted(maps.Keys(files)))
	}
	if files["dst/labels/train/a.txt"] == "" {
		t.Errorf("labels of the second export = %v", slices.Sorted(maps.Keys(files)))
	}
}
