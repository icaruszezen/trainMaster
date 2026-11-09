package dataset

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// renameList returns the rename list of a job as "kind original>path
// (same_name_as) annotation".
func (e *testEnv) renameList(t *testing.T, datasetID, importID int64) []string {
	t.Helper()
	items, total, err := e.svc.ListImportRenames(context.Background(), datasetID, importID, RenameQuery{Limit: 100})
	if err != nil || total != len(items) {
		t.Fatalf("ListImportRenames = %d items, total %d, %v", len(items), total, err)
	}
	var out []string
	for _, r := range items {
		s := fmt.Sprintf("%s %s>%s (%s)", r.Kind, r.OriginalPath, r.Path, r.SameNameAs)
		if r.AnnotationPath != nil {
			s += " " + *r.AnnotationPath
		}
		out = append(out, s)
	}
	return out
}

// labelledVOC is a LabelImg-style VOC file for filename in folder with one
// box of label.
func labelledVOC(folder, filename, label string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<annotation verified="yes">
	<folder>%s</folder>
	<filename>%s</filename>
	<path>D:\images\%s\%s</path>
	<size><width>64</width><height>48</height><depth>3</depth></size>
	<object><name>%s</name><bndbox><xmin>1</xmin><ymin>2</ymin><xmax>30</xmax><ymax>40</ymax></bndbox></object>
</annotation>
`, folder, filename, folder, filename, label)
}

func TestIntegrationRenameArchiveImport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "cams"})
	if err != nil {
		t.Fatal(err)
	}
	a, b, c, other := string(pngFile(t, 64, 48)), string(pngFile(t, 64, 47)), string(pngFile(t, 30, 30)), string(pngFile(t, 8, 8))
	archive := writeZip(t, []testEntry{
		{Name: "batch/cam1/0001.png", Data: a},
		{Name: "batch/cam1/0001.xml", Data: labelledVOC("cam1", "0001.png", "a")},
		{Name: "batch/cam1/0002.png", Data: other},
		{Name: "batch/cam2/0001.png", Data: b},
		{Name: "batch/cam2/0001.xml", Data: labelledVOC("cam2", "0001.png", "b")},
		{Name: "batch/cam3/0001.png", Data: a}, // the same picture as cam1's
		{Name: "batch/cam3/0001.xml", Data: labelledVOC("cam3", "0001.png", "c")},
		{Name: "batch/cam4/0001.PNG", Data: c},
	})
	job := e.uploadArchive(t, d.ID, archive, ConflictSkip)
	if job.Status != StatusSucceeded || !job.RenameImages {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{
		sumImagesAdded: 5, sumAnnotationsMatched: 3, sumImagesRenamed: 2, sumImagesIdentical: 1,
	})
	wantFiles := []string{
		"cam1/0001.png", "cam1/0001.xml", "cam1/0002.png",
		"cam2/0001_2.png", "cam2/0001_2.xml",
		"cam3/0001.png", "cam3/0001.xml",
		"cam4/0001_3.PNG",
	}
	if got := datasetFiles(t, d.Path); !slices.Equal(got, wantFiles) {
		t.Errorf("dataset files = %v\nwant %v", got, wantFiles)
	}
	xml, _ := os.ReadFile(filepath.Join(d.Path, "cam2", "0001_2.xml"))
	for _, want := range []string{"<filename>0001_2.png</filename>", `<path>D:\images\cam2\0001_2.png</path>`, `verified="yes"`} {
		if !strings.Contains(string(xml), want) {
			t.Errorf("renamed annotation lacks %q:\n%s", want, xml)
		}
	}
	box := func(label string) []AnnotationObject {
		return []AnnotationObject{{Name: label, Xmin: 1, Ymin: 2, Xmax: 30, Ymax: 40}}
	}
	wantObjs := map[string][]AnnotationObject{"cam1/0001.png": box("a"), "cam2/0001_2.png": box("b"), "cam3/0001.png": box("c")}
	if objs := e.imageObjects(t, d.ID); !maps.EqualFunc(objs, wantObjs, slices.Equal) {
		t.Errorf("objects = %v\nwant %v", objs, wantObjs)
	}
	wantList := []string{
		"renamed cam2/0001.png>cam2/0001_2.png (cam1/0001.png) cam2/0001_2.xml",
		"identical cam3/0001.png>cam3/0001.png (cam1/0001.png)",
		"renamed cam4/0001.PNG>cam4/0001_3.PNG (cam1/0001.png)",
	}
	if got := e.renameList(t, d.ID, job.ID); !slices.Equal(got, wantList) {
		t.Errorf("rename list = %q\nwant %q", got, wantList)
	}
	if items, total, err := e.svc.ListImportRenames(ctx, d.ID, job.ID, RenameQuery{Kind: RenameIdentical, Limit: 1}); err != nil ||
		total != 1 || len(items) != 1 || items[0].Path != "cam3/0001.png" || items[0].AnnotationPath != nil {
		t.Errorf("identical only = %+v, %d, %v", items, total, err)
	}
	if items, total, _ := e.svc.ListImportRenames(ctx, d.ID, job.ID, RenameQuery{Offset: 2, Limit: 5}); total != 3 || len(items) != 1 {
		t.Errorf("second page = %+v, total %d", items, total)
	}

	// The same archive again: every image finds its place and is skipped.
	again := e.uploadArchive(t, d.ID, archive, ConflictSkip)
	wantSummary(t, again.Summary, map[string]int64{sumImagesAdded: 0, sumImagesSkipped: 5, sumImagesRenamed: 0, sumImagesIdentical: 0})
	if got := datasetFiles(t, d.Path); !slices.Equal(got, wantFiles) {
		t.Errorf("dataset files after a second import = %v", got)
	}
	if got := e.renameList(t, d.ID, again.ID); len(got) != 0 {
		t.Errorf("rename list of the second import = %q", got)
	}

	// Overwriting replaces the renamed copies, with their annotations (the
	// one top-level folder of an archive is left out).
	archive2 := writeZip(t, []testEntry{
		{Name: "top/cam2/0001.png", Data: b},
		{Name: "top/cam2/0001.xml", Data: labelledVOC("cam2", "0001.png", "b2")},
	})
	over := e.uploadArchive(t, d.ID, archive2, ConflictOverwrite)
	wantSummary(t, over.Summary, map[string]int64{sumImagesAdded: 0, sumImagesReplaced: 1, sumImagesRenamed: 1})
	if got := datasetFiles(t, d.Path); !slices.Equal(got, wantFiles) {
		t.Errorf("dataset files after overwriting = %v", got)
	}
	if objs := e.imageObjects(t, d.ID); !slices.Equal(objs["cam2/0001_2.png"], box("b2")) {
		t.Errorf("cam2/0001_2.png after overwriting = %v", objs["cam2/0001_2.png"])
	}

	// A new image with a taken name and new content gets the next free name.
	archive3 := writeZip(t, []testEntry{{Name: "top/cam5/0001.png", Data: string(pngFile(t, 9, 9))}})
	next := e.uploadArchive(t, d.ID, archive3, ConflictSkip)
	wantSummary(t, next.Summary, map[string]int64{sumImagesAdded: 1, sumImagesRenamed: 1})
	if got := e.renameList(t, d.ID, next.ID); !slices.Equal(got, []string{"renamed cam5/0001.png>cam5/0001_4.png (cam1/0001.png)"}) {
		t.Errorf("rename list = %q", got)
	}
}

func TestIntegrationRenameConvertedFormats(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()

	// YOLO: the converted VOC file follows the image.
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "yolo"})
	train, val := string(pngFile(t, 100, 50)), string(pngFile(t, 100, 51))
	archive := writeZip(t, []testEntry{
		{Name: "images/train/1.png", Data: train},
		{Name: "images/val/1.png", Data: val},
		{Name: "labels/train/1.txt", Data: yoloLine(0, 0.5, 0.5, 0.2, 0.2)},
		{Name: "labels/val/1.txt", Data: yoloLine(1, 0.25, 0.5, 0.1, 0.2)},
		{Name: "classes.txt", Data: "ok\nng\n"},
	})
	job := e.uploadArchiveAs(t, d.ID, archive, CreateImportInput{AnnotationFormat: FormatYOLO, Classes: []string{"ok", "ng"}})
	if job.Status != StatusSucceeded {
		t.Fatalf("job = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 2, sumAnnotationsConverted: 2, sumImagesRenamed: 1})
	want := []string{"images/train/1.png", "images/train/1.xml", "images/val/1_2.png", "images/val/1_2.xml"}
	if got := datasetFiles(t, d.Path); !slices.Equal(got, want) {
		t.Errorf("dataset files = %v\nwant %v", got, want)
	}
	objs := e.imageObjects(t, d.ID)
	if got := objs["images/val/1_2.png"]; len(got) != 1 || got[0].Name != "ng" {
		t.Errorf("objects of the renamed image = %v", got)
	}
	if xml, _ := os.ReadFile(filepath.Join(d.Path, "images", "val", "1_2.xml")); !strings.Contains(string(xml), "<filename>1_2.png</filename>") {
		t.Errorf("converted annotation:\n%s", xml)
	}

	// X-AnyLabeling, uploaded as a folder: a JSON paired by its imagePath
	// in another folder ends up next to the renamed image.
	d2, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "xal"})
	shapes := func(imagePath, label string) string {
		return fmt.Sprintf(`{"imagePath": %q, "imageWidth": 64, "imageHeight": 48, "shapes": [{"label": %q, "points": [[1, 2], [11, 12]], "shape_type": "rectangle"}]}`,
			imagePath, label)
	}
	job, err := e.svc.CreateImport(ctx, d2.ID, CreateImportInput{Source: SourceFolder, FileName: "batch", FileCount: 4,
		FileSize: 1000, AnnotationFormat: FormatXAnyLabeling})
	if err != nil {
		t.Fatal(err)
	}
	job, err = e.svc.AddFolderFiles(ctx, d2.ID, job.ID, fileSource(
		[2]string{"NG/a.png", string(pngFile(t, 64, 48))}, [2]string{"NG/a.json", shapes("a.png", "ng")},
		[2]string{"OK/a.png", string(pngFile(t, 64, 46))}, [2]string{"labels/a.json", shapes(`..\OK\a.png`, "ok")},
	))
	if err != nil {
		t.Fatal(err)
	}
	if job, err = e.svc.CommitImport(ctx, d2.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	job = e.waitJob(t, d2.ID, job.ID)
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 2, sumAnnotationsConverted: 2, sumImagesRenamed: 1})
	want = []string{"NG/a.png", "NG/a.xml", "OK/a_2.png", "OK/a_2.xml"}
	if got := datasetFiles(t, d2.Path); !slices.Equal(got, want) {
		t.Errorf("dataset files = %v\nwant %v", got, want)
	}
	if got := e.imageObjects(t, d2.ID)["OK/a_2.png"]; len(got) != 1 || got[0].Name != "ok" {
		t.Errorf("objects of the renamed image = %v", got)
	}
	if got := e.renameList(t, d2.ID, job.ID); !slices.Equal(got, []string{"renamed OK/a.png>OK/a_2.png (NG/a.png) OK/a_2.xml"}) {
		t.Errorf("rename list = %q", got)
	}
}

func TestIntegrationRenameVOCLayoutAndDatasetImages(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "voc"})
	// An image of the dataset holds the name; it keeps it.
	writeFiles(t, d.Path, map[string][]byte{"old/7.png": pngFile(t, 5, 5), "old/7.xml": vocFile("7.png", "old")})
	if job, _ := e.svc.StartScan(ctx, d.ID); e.waitJob(t, d.ID, job.ID).Status != StatusSucceeded {
		t.Fatal("scan failed")
	}
	archive := writeZip(t, []testEntry{
		{Name: "set1/JPEGImages/7.png", Data: string(pngFile(t, 6, 6))},
		{Name: "set1/Annotations/7.xml", Data: string(vocFile("7.png", "s1"))},
		{Name: "set2/JPEGImages/7.png", Data: string(pngFile(t, 7, 7))},
		{Name: "set2/Annotations/7.xml", Data: string(vocFile("7.png", "s2"))},
	})
	job := e.uploadArchive(t, d.ID, archive, ConflictSkip)
	wantSummary(t, job.Summary, map[string]int64{sumImagesAdded: 2, sumAnnotationsMatched: 2, sumImagesRenamed: 2})
	want := []string{"old/7.png", "old/7.xml", "set1/Annotations/7_2.xml", "set1/JPEGImages/7_2.png",
		"set2/Annotations/7_3.xml", "set2/JPEGImages/7_3.png"}
	if got := datasetFiles(t, d.Path); !slices.Equal(got, want) {
		t.Errorf("dataset files = %v\nwant %v", got, want)
	}
	objs := e.imageObjects(t, d.ID)
	if len(objs["set1/JPEGImages/7_2.png"]) != 1 || objs["set1/JPEGImages/7_2.png"][0].Name != "s1" ||
		len(objs["set2/JPEGImages/7_3.png"]) != 1 || objs["set2/JPEGImages/7_3.png"][0].Name != "s2" {
		t.Errorf("objects = %v", objs)
	}
}

func TestIntegrationRenameRegisteredDirectory(t *testing.T) {
	e := newTestEnv(t, false)
	ctx := context.Background()
	dir := filepath.Join(e.datasetsDir(), "line2")
	a := pngFile(t, 64, 48)
	writeFiles(t, dir, map[string][]byte{
		"A/1.png": a, "A/1.xml": []byte(labelledVOC("A", "1.png", "a")),
		"B/1.png": pngFile(t, 64, 40), "B/1.xml": []byte(labelledVOC("B", "1.png", "b")),
		"C/1.png": a,
		"B/2.png": pngFile(t, 3, 3),
	})
	dirName := "line2"
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "产线2", Directory: &dirName})
	if err != nil {
		t.Fatal(err)
	}
	if !d.ActiveImport.RenameImages {
		t.Errorf("the first scan of an added folder should rename: %+v", d.ActiveImport)
	}
	e.start(t)
	job := e.waitJob(t, d.ID, d.ActiveImport.ID)
	if job.Status != StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}
	wantSummary(t, job.Summary, map[string]int64{sumImagesTotal: 4, sumImagesAdded: 4, sumAnnotationsMatched: 2,
		sumImagesRenamed: 1, sumImagesIdentical: 1, sumImagesRenameFailed: 0})
	want := []string{"A/1.png", "A/1.xml", "B/1_2.png", "B/1_2.xml", "B/2.png", "C/1.png"}
	if got := datasetFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("folder = %v\nwant %v", got, want)
	}
	if objs := e.imageObjects(t, d.ID); len(objs["B/1_2.png"]) != 1 || objs["B/1_2.png"][0].Name != "b" {
		t.Errorf("objects = %v", objs)
	}
	wantList := []string{"renamed B/1.png>B/1_2.png (A/1.png) B/1_2.xml", "identical C/1.png>C/1.png (A/1.png)"}
	if got := e.renameList(t, d.ID, job.ID); !slices.Equal(got, wantList) {
		t.Errorf("rename list = %q\nwant %q", got, wantList)
	}

	// Later scans never rename: the images are in the dataset now.
	writeFiles(t, dir, map[string][]byte{"D/1.png": pngFile(t, 4, 4)})
	scan, err := e.svc.StartScan(ctx, d.ID)
	if err != nil || scan.RenameImages {
		t.Fatalf("StartScan = %+v, %v", scan, err)
	}
	scan = e.waitJob(t, d.ID, scan.ID)
	if _, ok := scan.Summary[sumImagesRenamed]; ok || scan.Summary[sumImagesAdded] != 1 {
		t.Errorf("rescan summary = %v", scan.Summary)
	}
	if _, err := os.Stat(filepath.Join(dir, "D", "1.png")); err != nil {
		t.Errorf("a rescan renamed D/1.png: %v", err)
	}
}
