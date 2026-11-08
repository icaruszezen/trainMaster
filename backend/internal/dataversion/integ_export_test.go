package dataversion

import (
	"bytes"
	"context"
	"encoding/xml"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// integExportVersion is a ready version with every case a download must
// handle: an upper-case .JPG and a .jpeg (VOC renames them), a PNG in an
// images/ folder of its dataset, a BMP, an augmented copy, a negative
// sample, a box to clip, a box with nothing left after clipping, a
// difficult object and an image whose file is gone.
func (e *integEnv) integExportVersion(t *testing.T) integVersion {
	t.Helper()
	pid := e.integProject(t, "外观检测", "scratch", "dent")
	difficult := integObject("dent", 2, 2, 6, 6)
	difficult.Difficult = true
	return e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Note: "第一版\n第二行",
		Images: []integImageSpec{
			{Path: "images/line_a/NG/0001.JPG", Objects: []dataset.AnnotationObject{integObject("scratch", 1, 1, 10, 10),
				difficult}},
			{Path: "images/line_a/NG/0001-aug1.jpg", AugmentedFrom: 1, Augment: []string{"hflip"},
				Objects: []dataset.AnnotationObject{integObject("scratch", 22, 1, 31, 10)}},
			{Path: "images/line_a/OK/neg.jpeg", Split: SplitVal},
			{Path: "images/line_b/images/x.png", Split: SplitVal,
				Objects: []dataset.AnnotationObject{integObject("dent", 20, 10, 40, 30), integObject("dent", 40, 0, 50, 5)}},
			{Path: "images/line_b/t.bmp", Split: SplitTest, Data: []byte("BM not really"),
				Objects: []dataset.AnnotationObject{integObject("scratch", 0, 0, 32, 24)}},
			{Path: "images/line_b/gone.jpg", NoFile: true, Objects: []dataset.AnnotationObject{integObject("scratch", 1, 1, 2, 2)}},
		}})
}

func integKeys(m map[string][]byte) []string { return slices.Sorted(maps.Keys(m)) }

func integWantEntries(t *testing.T, what string, got map[string][]byte, want ...string) {
	t.Helper()
	slices.Sort(want)
	if keys := integKeys(got); !slices.Equal(keys, want) {
		t.Errorf("%s entries:\n got %q\nwant %q", what, keys, want)
	}
}

func TestIntegrationDataVersionExportYOLO(t *testing.T) {
	e := integNewService(t)
	v := e.integExportVersion(t)
	files, name := e.integDownload(t, v.ID, ExportInput{Format: FormatYOLO, IncludeAugmented: true})
	if name != "外观检测-数据版本v1-yolo.zip" {
		t.Errorf("file name = %q", name)
	}
	integWantEntries(t, "yolo", files, "classes.txt", "data.yaml",
		"images/train/line_a/NG/0001.JPG", "labels/train/line_a/NG/0001.txt",
		"images/train/line_a/NG/0001-aug1.jpg", "labels/train/line_a/NG/0001-aug1.txt",
		"images/val/line_a/OK/neg.jpeg", "labels/val/line_a/OK/neg.txt",
		"images/val/line_b/images/x.png", "images/val/line_b/labels/x.txt",
		"images/test/line_b/t.bmp", "labels/test/line_b/t.txt")
	original, _ := os.ReadFile(filepath.Join(v.Dir, "images", "line_a", "NG", "0001.JPG"))
	if !bytes.Equal(files["images/train/line_a/NG/0001.JPG"], original) {
		t.Error("the image in the archive differs from the version's file")
	}
	for name, want := range map[string]string{
		"classes.txt":                     "scratch\ndent\n",
		"labels/train/line_a/NG/0001.txt": "0 0.171875 0.229167 0.281250 0.375000\n1 0.125000 0.166667 0.125000 0.166667\n",
		"labels/val/line_a/OK/neg.txt":    "",                                        // a negative sample
		"images/val/line_b/labels/x.txt":  "1 0.812500 0.708333 0.375000 0.583333\n", // clipped; the empty box left out
		"labels/test/line_b/t.txt":        "0 0.500000 0.500000 1.000000 1.000000\n",
	} {
		if got := string(files[name]); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	yaml := string(files["data.yaml"])
	for _, part := range []string{"# trainMaster 导出的 YOLO 数据集：外观检测 · 数据版本 v1。\n", "# 备注：第一版\n",
		"\ntrain: images/train\nval: images/val\ntest: images/test\nnc: 2\nnames:\n  0: \"scratch\"\n  1: \"dent\"\n"} {
		if !strings.Contains(yaml, part) {
			t.Errorf("data.yaml has no %q:\n%s", part, yaml)
		}
	}
	if strings.Contains(yaml, "path:") || strings.Contains(yaml, "第二行") || strings.Contains(yaml, "不含增强图片") {
		t.Errorf("data.yaml:\n%s", yaml)
	}
	if !strings.Contains(e.logs.String(), "leaving out an image whose file cannot be opened") {
		t.Error("the missing file must be logged")
	}

	// Without the augmented copies.
	files, _ = e.integDownload(t, v.ID, ExportInput{Format: FormatYOLO})
	if _, ok := files["images/train/line_a/NG/0001-aug1.jpg"]; ok || len(files) != 10 {
		t.Errorf("without augmented copies: %q", integKeys(files))
	}
	if yaml := string(files["data.yaml"]); !strings.Contains(yaml, "# 本次下载不含增强图片。\n") {
		t.Errorf("data.yaml without the augmented copies:\n%s", yaml)
	}
}

// integSimpleVersion is a ready version with JPEG images in train and val
// only, without difficult objects.
func (e *integEnv) integSimpleVersion(t *testing.T, projectName string) integVersion {
	t.Helper()
	pid := e.integProject(t, projectName, "scratch")
	return e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Version: 7,
		Classes: []Class{{Name: "scratch", Color: "#ef4444"}},
		Images: []integImageSpec{
			{Path: "images/d/a.jpg", Objects: []dataset.AnnotationObject{integObject("scratch", 1, 1, 3, 3)}},
			{Path: "images/d/b.jpg", Split: SplitVal},
		}})
}

func TestIntegrationDataVersionExportVOC(t *testing.T) {
	e := integNewService(t)
	v := e.integExportVersion(t)
	files, name := e.integDownload(t, v.ID, ExportInput{Format: FormatVOC, IncludeAugmented: true})
	if name != "外观检测-数据版本v1-voc.zip" {
		t.Errorf("file name = %q", name)
	}
	integWantEntries(t, "voc", files, "classes.txt", "README.txt",
		"JPEGImages/line_a/NG/0001.jpg", "Annotations/line_a/NG/0001.xml",
		"JPEGImages/line_a/NG/0001-aug1.jpg", "Annotations/line_a/NG/0001-aug1.xml",
		"JPEGImages/line_a/OK/neg.jpg", "Annotations/line_a/OK/neg.xml",
		"JPEGImages/line_b/images/x.png", "Annotations/line_b/images/x.xml",
		"JPEGImages/line_b/t.bmp", "Annotations/line_b/t.xml",
		"ImageSets/Main/train.txt", "ImageSets/Main/val.txt", "ImageSets/Main/test.txt", "ImageSets/Main/trainval.txt")
	for name, want := range map[string]string{
		"ImageSets/Main/train.txt":    "line_a/NG/0001\nline_a/NG/0001-aug1\n",
		"ImageSets/Main/val.txt":      "line_a/OK/neg\nline_b/images/x\n",
		"ImageSets/Main/test.txt":     "line_b/t\n",
		"ImageSets/Main/trainval.txt": "line_a/NG/0001\nline_a/NG/0001-aug1\nline_a/OK/neg\nline_b/images/x\n",
		"classes.txt":                 "scratch\ndent\n",
	} {
		if got := string(files[name]); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	voc, err := dataset.DecodeVOC(files["Annotations/line_a/NG/0001.xml"])
	if err != nil || voc.Filename != "0001.jpg" || voc.Width != 32 || voc.Height != 24 || len(voc.Objects) != 2 ||
		voc.Objects[0] != integObject("scratch", 1, 1, 10, 10) || !voc.Objects[1].Difficult {
		t.Errorf("annotation = %+v, %v", voc, err)
	}
	var folder struct {
		Folder string `xml:"folder"`
	}
	if err := xml.Unmarshal(files["Annotations/line_a/NG/0001.xml"], &folder); err != nil || folder.Folder != "NG" {
		t.Errorf("folder = %+v, %v", folder, err)
	}
	if voc, err := dataset.DecodeVOC(files["Annotations/line_a/OK/neg.xml"]); err != nil || voc.Filename != "neg.jpg" ||
		len(voc.Objects) != 0 {
		t.Errorf("negative annotation = %+v, %v", voc, err)
	}
	if voc, err := dataset.DecodeVOC(files["Annotations/line_b/images/x.xml"]); err != nil || voc.Filename != "x.png" {
		t.Errorf("PNG annotation = %+v, %v", voc, err)
	}
	readme := string(files["README.txt"])
	for _, part := range []string{"外观检测 · 数据版本 v1", "有 2 张图片不是 JPEG 格式（.bmp 1 张、.png 1 张）",
		"difficult 的目标（本数据集中有 1 个）", "\r\n"} {
		if !strings.Contains(readme, part) {
			t.Errorf("README.txt has no %q:\n%s", part, readme)
		}
	}

	// JPEG only, no difficult objects, no test images: no README, no test.txt.
	simple := e.integSimpleVersion(t, `a/b:c*?"<>|`)
	files, name = e.integDownload(t, simple.ID, ExportInput{Format: FormatVOC})
	if name != "a_b_c______-数据版本v7-voc.zip" {
		t.Errorf("file name = %q", name)
	}
	integWantEntries(t, "simple voc", files, "classes.txt", "JPEGImages/d/a.jpg", "Annotations/d/a.xml",
		"JPEGImages/d/b.jpg", "Annotations/d/b.xml", "ImageSets/Main/train.txt", "ImageSets/Main/val.txt",
		"ImageSets/Main/trainval.txt")
	yolo, _ := e.integDownload(t, simple.ID, ExportInput{Format: FormatYOLO})
	if yaml := string(yolo["data.yaml"]); strings.Contains(yaml, "test:") {
		t.Errorf("data.yaml without test images:\n%s", yaml)
	}
}

type integCOCOFile struct {
	Info struct {
		Description string `json:"description"`
		Version     string `json:"version"`
		DateCreated string `json:"date_created"`
	} `json:"info"`
	Licenses []any `json:"licenses"`
	Images   []struct {
		ID       int    `json:"id"`
		FileName string `json:"file_name"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	} `json:"images"`
	Annotations []struct {
		ID           int   `json:"id"`
		ImageID      int   `json:"image_id"`
		CategoryID   int   `json:"category_id"`
		BBox         []int `json:"bbox"`
		Area         int   `json:"area"`
		IsCrowd      *int  `json:"iscrowd"`
		Segmentation []any `json:"segmentation"`
	} `json:"annotations"`
	Categories []struct {
		ID            int     `json:"id"`
		Name          string  `json:"name"`
		Supercategory *string `json:"supercategory"`
	} `json:"categories"`
}

func TestIntegrationDataVersionExportCOCO(t *testing.T) {
	e := integNewService(t)
	v := e.integExportVersion(t)
	files, name := e.integDownload(t, v.ID, ExportInput{Format: FormatCOCO, IncludeAugmented: true})
	if name != "外观检测-数据版本v1-coco.zip" {
		t.Errorf("file name = %q", name)
	}
	integWantEntries(t, "coco", files,
		"images/train/line_a/NG/0001.JPG", "images/train/line_a/NG/0001-aug1.jpg",
		"images/val/line_a/OK/neg.jpeg", "images/val/line_b/images/x.png", "images/test/line_b/t.bmp",
		"annotations/instances_train.json", "annotations/instances_val.json", "annotations/instances_test.json")

	train := integJSON[integCOCOFile](t, files["annotations/instances_train.json"])
	if train.Info.Description != "trainMaster 数据版本：外观检测 · 数据版本 v1（第一版）" || train.Info.Version != "1" ||
		train.Info.DateCreated == "" || train.Licenses == nil || len(train.Licenses) != 0 {
		t.Errorf("info = %+v, licenses %v", train.Info, train.Licenses)
	}
	if _, err := time.Parse(time.RFC3339, train.Info.DateCreated); err != nil {
		t.Errorf("date_created: %v", err)
	}
	if len(train.Images) != 2 || train.Images[0].ID != 1 || train.Images[0].FileName != "line_a/NG/0001.JPG" ||
		train.Images[0].Width != 32 || train.Images[0].Height != 24 || train.Images[1].ID != 2 ||
		train.Images[1].FileName != "line_a/NG/0001-aug1.jpg" {
		t.Errorf("train images = %+v (the missing file must be left out)", train.Images)
	}
	if len(train.Annotations) != 3 {
		t.Fatalf("train annotations = %+v", train.Annotations)
	}
	for i, want := range []struct{ image, category, area int }{{1, 1, 81}, {1, 2, 16}, {2, 1, 81}} {
		a := train.Annotations[i]
		if a.ID != i+1 || a.ImageID != want.image || a.CategoryID != want.category || a.Area != want.area ||
			a.IsCrowd == nil || *a.IsCrowd != 0 || a.Segmentation == nil || len(a.Segmentation) != 0 {
			t.Errorf("train annotation %d = %+v", i, a)
		}
	}
	if !slices.Equal(train.Annotations[0].BBox, []int{1, 1, 9, 9}) || !slices.Equal(train.Annotations[1].BBox, []int{2, 2, 4, 4}) {
		t.Errorf("boxes = %v, %v", train.Annotations[0].BBox, train.Annotations[1].BBox)
	}
	if len(train.Categories) != 2 || train.Categories[0].ID != 1 || train.Categories[0].Name != "scratch" ||
		train.Categories[1].ID != 2 || train.Categories[1].Name != "dent" || train.Categories[0].Supercategory == nil ||
		*train.Categories[0].Supercategory != "" {
		t.Errorf("categories = %+v", train.Categories)
	}

	// Ids start at 1 in each file; the negative sample is listed; the box
	// is clipped and the empty one left out.
	val := integJSON[integCOCOFile](t, files["annotations/instances_val.json"])
	if len(val.Images) != 2 || val.Images[0].ID != 1 || val.Images[0].FileName != "line_a/OK/neg.jpeg" ||
		val.Images[1].ID != 2 || len(val.Annotations) != 1 || val.Annotations[0].ID != 1 ||
		val.Annotations[0].ImageID != 2 || !slices.Equal(val.Annotations[0].BBox, []int{20, 10, 12, 14}) ||
		val.Annotations[0].Area != 168 {
		t.Errorf("val = %+v", val)
	}
	test := integJSON[integCOCOFile](t, files["annotations/instances_test.json"])
	if len(test.Images) != 1 || len(test.Annotations) != 1 || test.Annotations[0].Area != 32*24 {
		t.Errorf("test = %+v", test)
	}

	// Without the augmented copies, and a version without test images.
	files, _ = e.integDownload(t, v.ID, ExportInput{Format: FormatCOCO})
	train = integJSON[integCOCOFile](t, files["annotations/instances_train.json"])
	if _, ok := files["images/train/line_a/NG/0001-aug1.jpg"]; ok || len(train.Images) != 1 || len(train.Annotations) != 2 {
		t.Errorf("without augmented copies: %q, %+v", integKeys(files), train)
	}
	simple := e.integSimpleVersion(t, "simple")
	files, _ = e.integDownload(t, simple.ID, ExportInput{Format: FormatCOCO})
	integWantEntries(t, "simple coco", files, "images/train/d/a.jpg", "images/val/d/b.jpg",
		"annotations/instances_train.json", "annotations/instances_val.json")
}

func TestIntegrationDataVersionExportTickets(t *testing.T) {
	e := integNewService(t)
	ctx := context.Background()
	v := e.integSimpleVersion(t, "p")

	for _, format := range []string{"", "zip", "YOLO", "xanylabeling"} {
		_, err := e.svc.CreateExport(ctx, v.ID, ExportInput{Format: format})
		integWantKind(t, err, ErrInvalid, "下载格式必须是 yolo（YOLO）、voc（Pascal VOC）或 coco（COCO）")
	}
	_, err := e.svc.CreateExport(ctx, 1<<40, ExportInput{Format: FormatYOLO})
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
	failed := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: e.integProject(t, "f", "x"), Status: StatusFailed})
	_, err = e.svc.CreateExport(ctx, failed.ID, ExportInput{Format: FormatYOLO})
	integWantKind(t, err, ErrConflict, "数据版本还没有生成完成")

	ticket, err := e.svc.CreateExport(ctx, v.ID, ExportInput{Format: FormatVOC})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(ticket.ExpiresAt); d < 59*time.Minute || d > time.Hour || ticket.ExpiresAt.Location() != time.UTC {
		t.Errorf("expires at %v", ticket.ExpiresAt)
	}
	// A ticket opens only the version it was made for.
	_, err = e.svc.OpenExport(ctx, failed.ID, ticket.Token)
	integWantKind(t, err, ErrNotFound, "下载链接已失效")
	_, err = e.svc.OpenExport(ctx, v.ID, "AAAA")
	integWantKind(t, err, ErrNotFound, "下载链接已失效")
	// It can be opened again until it expires.
	for range 2 {
		x, err := e.svc.OpenExport(ctx, v.ID, ticket.Token)
		if err != nil {
			t.Fatal(err)
		}
		x.Close()
	}
	e.svc.exports.mu.Lock()
	r := e.svc.exports.m[ticket.Token]
	r.expires = time.Now().Add(-time.Second)
	e.svc.exports.m[ticket.Token] = r
	e.svc.exports.mu.Unlock()
	_, err = e.svc.OpenExport(ctx, v.ID, ticket.Token)
	integWantKind(t, err, ErrNotFound, "下载链接已失效")

	// Moved to the recycle bin after the ticket: not found.
	ticket, err = e.svc.CreateExport(ctx, v.ID, ExportInput{Format: FormatYOLO})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.moveToBin(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenExport(ctx, v.ID, ticket.Token)
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
	if _, err := e.svc.Restore(ctx, v.ID, RestoreInput{}); err != nil {
		t.Fatal(err)
	}

	// Purged while it is sent: the download fails instead of ending short.
	x, err := e.svc.OpenExport(ctx, v.ID, ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if _, err := e.db.Exec(ctx, "DELETE FROM data_versions WHERE id = $1", v.ID); err != nil {
		t.Fatal(err)
	}
	if err := x.Write(ctx, &bytes.Buffer{}); err == nil {
		t.Error("a download of a version purged meanwhile must fail")
	}

	// The folder is gone.
	other := e.integSimpleVersion(t, "q")
	ticket, err = e.svc.CreateExport(ctx, other.ID, ExportInput{Format: FormatYOLO})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(other.Dir); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenExport(ctx, other.ID, ticket.Token)
	integWantKind(t, err, ErrConflict, "数据版本的文件夹不存在")
}

func TestDataVersionExportTop(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version int
		want    string
	}{
		{"外观检测", 4, "外观检测-数据版本v4"},
		{" .a/b:c. ", 3, "a_b_c-数据版本v3"},
		{"x\ty\u2028", 1, "x_y_-数据版本v1"},
		{"...", 2, "项目-数据版本v2"},
	} {
		if got := exportTop(tt.name, tt.version); got != tt.want {
			t.Errorf("exportTop(%q, %d) = %q, want %q", tt.name, tt.version, got, tt.want)
		}
	}
	long := exportTop(strings.Repeat("检", 100), 12)
	if len(long) > maxExportTopBytes || !utf8.ValidString(long) || !strings.HasSuffix(long, "-数据版本v12") {
		t.Errorf("long name = %q (%d bytes)", long, len(long))
	}
}
