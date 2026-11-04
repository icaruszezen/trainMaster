package review

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

func ptr[T any](v T) *T { return &v }

func TestIntegrationCreateValidation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.project(t, "产线A")
	m := e.addModel(t, p, testModel{})
	cls := e.addModel(t, p, testModel{task: "classify"})
	missing := e.addModel(t, p, testModel{noFile: true})
	ds := e.addDataset(t, "ds", "ds", testImage{rel: "a.png"})
	empty := e.addDataset(t, "empty", "empty")

	for _, tt := range []struct {
		name string
		in   CreateInput
		kind error
		msg  string
	}{
		{"no datasets", CreateInput{ModelID: m.ID}, ErrInvalid, "请至少选择一个数据集"},
		{"bad dataset id", CreateInput{ModelID: m.ID, DatasetIDs: []int64{0}}, ErrInvalid, "数据集 ID 无效"},
		{"too many datasets", CreateInput{ModelID: m.ID, DatasetIDs: func() []int64 {
			var ids []int64
			for i := range 51 {
				ids = append(ids, int64(i+1))
			}
			return ids
		}()}, ErrInvalid, "最多选择 50 个数据集"},
		{"unknown dataset", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds, 999}}, ErrInvalid, "#999 不存在"},
		{"no model", CreateInput{DatasetIDs: []int64{ds}}, ErrInvalid, "请选择模型"},
		{"unknown model", CreateInput{ModelID: 999, DatasetIDs: []int64{ds}}, ErrInvalid, "所选模型不存在"},
		{"model file missing", CreateInput{ModelID: missing.ID, DatasetIDs: []int64{ds}}, ErrConflict, "模型文件不存在"},
		{"classify model", CreateInput{ModelID: cls.ID, DatasetIDs: []int64{ds}}, ErrInvalid, "分类模型没有检测框，不能用于审图"},
		{"no images", CreateInput{ModelID: m.ID, DatasetIDs: []int64{empty}}, ErrInvalid, "所选数据集中没有图片"},
		{"name too long", CreateInput{Name: strings.Repeat("名", 65), ModelID: m.ID, DatasetIDs: []int64{ds}}, ErrInvalid, "不能超过 64"},
		{"name control", CreateInput{Name: "a\tb", ModelID: m.ID, DatasetIDs: []int64{ds}}, ErrInvalid, "控制字符"},
		{"description", CreateInput{Description: strings.Repeat("x", 501), ModelID: m.ID, DatasetIDs: []int64{ds}}, ErrInvalid, "500"},
		{"conf", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}, Options: OptionsInput{Conf: ptr(0.001)}}, ErrInvalid, "conf"},
		{"iou", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}, Options: OptionsInput{IoU: ptr(0.99)}}, ErrInvalid, "iou"},
		{"max_det", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}, Options: OptionsInput{MaxDet: ptr(1001)}}, ErrInvalid, "max_det"},
		{"imgsz", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}, Options: OptionsInput{ImgSize: []int{33}}}, ErrInvalid, "32 的倍数"},
		{"imgsz count", CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds}, Options: OptionsInput{ImgSize: []int{32, 32, 32}}}, ErrInvalid, "[高, 宽]"},
	} {
		_, err := e.svc.CreateReview(ctx, tt.in)
		if err == nil {
			t.Errorf("%s: no error", tt.name)
			continue
		}
		wantKind(t, err, tt.kind, tt.msg)
	}

	// The image cap.
	defer func(n int) { maxImages = n }(maxImages)
	maxImages = 1
	two := e.addDataset(t, "two", "two", testImage{rel: "a.png"}, testImage{rel: "b.png"})
	_, err := e.svc.CreateReview(ctx, CreateInput{ModelID: m.ID, DatasetIDs: []int64{two}})
	wantKind(t, err, ErrInvalid, "共有 2 张图片，一个审图任务最多 1 张")

	// Nothing was created.
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d reviews after failed creations (%v)", n, err)
	}
	if entries, _ := os.ReadDir(e.st.Dir(storage.Reviews)); len(entries) != 0 {
		t.Errorf("review folders after failed creations: %v", entries)
	}
}

func TestIntegrationCreateReview(t *testing.T) {
	e := newTestEnv(t)
	p := e.project(t, "产线A")
	m := e.addModel(t, p, testModel{imageSize: 600, classes: []string{"划痕", "凹坑"}})
	// Dataset folders that become the same folder in the review.
	first := e.addDataset(t, "夜班", "line:1",
		testImage{rel: "b.png", w: 50, h: 40}, testImage{rel: "a.png"}, testImage{rel: "NG/a.png"},
		testImage{rel: "img10.png"}, testImage{rel: "img2.png"}, testImage{rel: "unknown.png", w: -1})
	second := e.addDataset(t, "白班", "line_1", testImage{rel: "a.png"}, testImage{rel: "A.PNG"}, testImage{rel: "a.xml/x.png"})

	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{second, first, second}, Description: " 第一轮 "})
	if r.Name != "产线A v1 审图" || r.Description != "第一轮" || r.Status != StatusQueued || r.Stage != "" ||
		r.Attempts != 0 || r.Node != nil || r.Result != nil || r.QueuePosition == nil || *r.QueuePosition != 1 ||
		!r.ModelExists || r.Project == nil || r.Project.ID != p || r.Project.Name != "产线A" {
		t.Errorf("review = %+v", r)
	}
	if r.Options != (InferenceOptions{Conf: 0.25, IoU: 0.7, ImgSize: [2]int{608, 608}, MaxDet: 300}) {
		t.Errorf("options = %+v", r.Options)
	}
	want := ModelRef{ID: m.ID, ProjectID: p, ProjectName: "产线A", Version: 1, YOLOVersion: "yolo11", Task: "detect",
		FileName: "best.pt", ClassNames: []string{"划痕", "凹坑"}}
	if r.Model.ID != want.ID || r.Model.ProjectName != want.ProjectName || r.Model.Version != 1 ||
		!slices.Equal(r.Model.ClassNames, want.ClassNames) || r.Model.Task != "detect" || r.Model.FileName != "best.pt" {
		t.Errorf("model = %+v", r.Model)
	}
	if r.Counts != (Counts{Total: 9, Pending: 9}) {
		t.Errorf("counts = %+v", r.Counts)
	}
	if len(r.Datasets) != 2 || r.Datasets[0].ID != second || r.Datasets[0].Folder != "line_1" ||
		r.Datasets[0].Name != "白班" || r.Datasets[0].Counts.Total != 3 ||
		r.Datasets[1].ID != first || r.Datasets[1].Folder != "line_1~"+strconv.FormatInt(first, 10) || r.Datasets[1].Counts.Total != 6 {
		t.Errorf("datasets = %+v", r.Datasets)
	}

	images := e.images(t, r.ID, ImageQuery{})
	type pl struct{ path, pkg, ann string }
	var got []pl
	for i, img := range images {
		if img.Position != i || img.ReviewID != r.ID || img.Inferred || img.Status != "pending" || img.Revision != 0 ||
			img.AnnotationVersion != "" || img.ImageVersion == "" || len(img.Labels) != 0 {
			t.Errorf("image %d = %+v", i, img)
		}
		got = append(got, pl{img.Path, img.PackagePath, img.AnnotationPath})
	}
	f := "line_1~" + strconv.FormatInt(first, 10)
	wantPl := []pl{
		// second, in image order (case-insensitive name order, folders first)
		{"a.xml/x.png", "line_1/a.xml/x.png", "line_1/a.xml/x.xml"},
		{"A.PNG", "line_1/A.PNG", "line_1/A.PNG.xml"}, // A.xml is the folder a.xml on Windows
		{"a.png", "line_1/a~2.png", "line_1/a~2.xml"},
		// first
		{"NG/a.png", f + "/NG/a.png", f + "/NG/a.xml"},
		{"a.png", f + "/a.png", f + "/a.xml"},
		{"b.png", f + "/b.png", f + "/b.xml"},
		{"img2.png", f + "/img2.png", f + "/img2.xml"},
		{"img10.png", f + "/img10.png", f + "/img10.xml"},
		{"unknown.png", f + "/unknown.png", f + "/unknown.xml"},
	}
	if !slices.Equal(got, wantPl) {
		t.Errorf("layout =\n%v\nwant\n%v", got, wantPl)
	}
	if images[5].Width == nil || *images[5].Width != 50 || images[8].Width != nil || images[0].FileName != "x.png" {
		t.Errorf("sizes / names: %+v %+v %+v", images[5], images[8], images[0])
	}

	// The folder with its description.
	var td taskDescription
	if err := json.Unmarshal(readFile(t, e.reviewFile(r.ID, reviewfile.ManifestFile)), &td); err != nil {
		t.Fatal(err)
	}
	if td.ReviewID != r.ID || td.Name != r.Name || td.ImageCount != 9 || len(td.Datasets) != 2 ||
		td.Datasets[1].Folder != f || td.Options != r.Options || td.Model.ID != m.ID {
		t.Errorf("review.json = %+v", td)
	}

	// Options and name given; a second review queues behind the first.
	r2 := e.create(t, CreateInput{Name: "  复核  ", ModelID: m.ID, DatasetIDs: []int64{first},
		Options: OptionsInput{Conf: ptr(0.5), IoU: ptr(0.45), ImgSize: []int{480, 640}, MaxDet: ptr(100)}})
	if r2.Name != "复核" || r2.Options != (InferenceOptions{Conf: 0.5, IoU: 0.45, ImgSize: [2]int{480, 640}, MaxDet: 100}) ||
		r2.QueuePosition == nil || *r2.QueuePosition != 2 || r2.Counts.Total != 6 {
		t.Errorf("second review = %+v", r2)
	}
	// A long project name is cut to fit the default name.
	long := e.project(t, strings.Repeat("长", 64))
	lm := e.addModel(t, long, testModel{})
	r3 := e.create(t, CreateInput{ModelID: lm.ID, DatasetIDs: []int64{first}})
	if !strings.HasSuffix(r3.Name, " v1 审图") || len([]rune(r3.Name)) != 64 {
		t.Errorf("default name = %q", r3.Name)
	}
}

func TestDefaultName(t *testing.T) {
	if got := defaultName("产线A", 3); got != "产线A v3 审图" {
		t.Errorf("defaultName = %q", got)
	}
	if got := defaultName("a\tb", 1); got != "a b v1 审图" {
		t.Errorf("defaultName = %q", got)
	}
}
