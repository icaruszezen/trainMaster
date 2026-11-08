package dataversion

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// integImagesVersion is a ready version of two sources with an augmented
// copy, a negative sample and names that need escaping in a search.
func (e *integEnv) integImagesVersion(t *testing.T) (int64, integVersion) {
	t.Helper()
	pid := e.integProject(t, "外观检测", "scratch", "dent")
	review := int64(5)
	v := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid,
		Sources: []Source{
			{DatasetID: 1, DatasetName: "产线A-外观", DirName: "line_a", IncludeBase: true,
				Versions: []SourceVersion{{ID: review, Name: "审图1.2"}}},
			{DatasetID: 2, DatasetName: "产线B", DirName: "line_b", IncludeBase: true, Versions: []SourceVersion{}},
		},
		Images: []integImageSpec{
			{Path: "images/line_a/NG/0001.jpg", DatasetID: 1, AnnotationSource: &review,
				Objects: []dataset.AnnotationObject{integObject("scratch", 1, 1, 10, 10), integObject("dent", 2, 2, 5, 5),
					integObject("scratch", 3, 3, 8, 8)}},
			{Path: "images/line_a/NG/0001-aug1.jpg", DatasetID: 1, AugmentedFrom: 1, Augment: []string{"hflip"},
				Objects: []dataset.AnnotationObject{integObject("scratch", 1, 1, 10, 10)}},
			{Path: "images/line_a/OK/50%_off.jpg", Split: SplitVal, DatasetID: 1},
			{Path: "images/line_b/a0yo.png", Split: SplitTest, DatasetID: 2,
				Objects: []dataset.AnnotationObject{integObject("dent", 0, 0, 4, 4)}},
			{Path: "images/line_b/5000_off.jpg", Split: SplitVal, DatasetID: 2, SourcePath: "raw/5000_off.jpg"},
		},
	})
	return pid, v
}

func TestIntegrationDataVersionListImages(t *testing.T) {
	e := integNewService(t)
	ctx := context.Background()
	_, v := e.integImagesVersion(t)

	all, total, err := e.svc.ListImages(ctx, v.ID, ImageFilter{})
	if err != nil || total != 5 || len(all) != 5 {
		t.Fatalf("ListImages = %d of %d, %v", len(all), total, err)
	}
	first := all[0]
	if first.ID != v.ImageIDs[0] || first.Position != 10 || first.Split != SplitTrain || first.SplitBy != SplitByRatio ||
		first.FileName != "0001.jpg" || first.Width != 32 || first.Height != 24 || first.SizeBytes == 0 ||
		first.ObjectCount != 3 || first.DatasetName != "产线A-外观" || first.SourcePath != "NG/0001.jpg" ||
		first.AnnotationSource == nil || *first.AnnotationSource != (SourceVersion{ID: 5, Name: "审图1.2"}) ||
		first.AugmentedFrom != nil || len(first.Augment) != 0 || first.Augment == nil || first.Stored != StoredLink {
		t.Errorf("first image = %+v", first)
	}
	if !slices.Equal(first.Labels, []dataset.LabelCount{{Name: "scratch", Count: 2}, {Name: "dent", Count: 1}}) {
		t.Errorf("labels = %+v", first.Labels)
	}
	base := "/api/v1/data-versions/" + strconv.FormatInt(v.ID, 10) + "/images/" + strconv.FormatInt(first.ID, 10)
	if first.ThumbnailURL != base+"/thumbnail?size=sm" || first.FileURL != base+"/file" {
		t.Errorf("urls = %q, %q", first.ThumbnailURL, first.FileURL)
	}
	if aug := all[1]; aug.AugmentedFrom == nil || *aug.AugmentedFrom != first.ID || aug.Stored != StoredGenerated ||
		!slices.Equal(aug.Augment, []string{"hflip"}) {
		t.Errorf("augmented copy = %+v", aug)
	}
	if neg := all[2]; neg.Labels == nil || len(neg.Labels) != 0 || neg.AnnotationSource != nil {
		t.Errorf("negative sample = %+v", neg)
	}

	ids := func(f ImageFilter) ([]int64, int) {
		t.Helper()
		items, total, err := e.svc.ListImages(ctx, v.ID, f)
		if err != nil {
			t.Fatalf("ListImages(%+v): %v", f, err)
		}
		out := []int64{}
		for _, img := range items {
			out = append(out, img.ID)
		}
		return out, total
	}
	yes, no := true, false
	img := v.ImageIDs
	for _, tt := range []struct {
		name   string
		filter ImageFilter
		want   []int64
		total  int
	}{
		{"split", ImageFilter{Split: SplitVal}, []int64{img[2], img[4]}, 2},
		{"label", ImageFilter{Label: "dent"}, []int64{img[0], img[3]}, 2},
		{"dataset", ImageFilter{DatasetID: 2}, []int64{img[3], img[4]}, 2},
		{"augmented", ImageFilter{Augmented: &yes}, []int64{img[1]}, 1},
		{"originals", ImageFilter{Augmented: &no}, []int64{img[0], img[2], img[3], img[4]}, 4},
		{"search path", ImageFilter{Search: " LINE_B "}, []int64{img[3], img[4]}, 2},
		{"search", ImageFilter{Search: "ng/0001"}, []int64{img[0], img[1]}, 2},
		{"search source path", ImageFilter{Search: "RAW/"}, []int64{img[4]}, 1},
		{"search escapes %", ImageFilter{Search: "50%"}, []int64{img[2]}, 1},
		{"search escapes _", ImageFilter{Search: "0_o"}, []int64{img[4]}, 1},
		{"search nothing", ImageFilter{Search: `\`}, []int64{}, 0},
		{"combined", ImageFilter{Split: SplitTrain, Label: "scratch", Augmented: &no}, []int64{img[0]}, 1},
		{"page", ImageFilter{Offset: 1, Limit: 2}, []int64{img[1], img[2]}, 5},
		{"page past the end", ImageFilter{Offset: 9}, []int64{}, 5},
	} {
		if got, total := ids(tt.filter); !slices.Equal(got, tt.want) || total != tt.total {
			t.Errorf("%s: %v of %d, want %v of %d", tt.name, got, total, tt.want, tt.total)
		}
	}
	for _, f := range []ImageFilter{{Limit: 201}, {Limit: -1}, {Offset: -1}, {Split: "trainval"}} {
		_, _, err := e.svc.ListImages(ctx, v.ID, f)
		integWantKind(t, err, ErrBadRequest, "参数")
	}

	// Detail: objects always present.
	d, err := e.svc.GetImage(ctx, v.ID, img[0])
	if err != nil || len(d.Objects) != 3 || d.Objects[0] != integObject("scratch", 1, 1, 10, 10) || d.DatasetName != "产线A-外观" {
		t.Errorf("GetImage = %+v, %v", d, err)
	}
	if d, err := e.svc.GetImage(ctx, v.ID, img[2]); err != nil || d.Objects == nil || len(d.Objects) != 0 {
		t.Errorf("GetImage of a negative = %+v, %v", d, err)
	}
	_, err = e.svc.GetImage(ctx, v.ID, 1<<40)
	integWantKind(t, err, ErrNotFound, "图片不存在")

	// Another version's image is not found through this one.
	other := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: e.integProject(t, "q", "scratch"),
		Images: []integImageSpec{{Path: "images/d/a.jpg"}}})
	_, err = e.svc.GetImage(ctx, v.ID, other.ImageIDs[0])
	integWantKind(t, err, ErrNotFound, "图片不存在")
	_, err = e.svc.OpenImage(ctx, v.ID, other.ImageIDs[0])
	integWantKind(t, err, ErrNotFound, "图片不存在")

	// Not ready: 409; missing: 404.
	building := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: e.integProject(t, "r", "scratch"),
		Status: StatusBuilding})
	_, _, err = e.svc.ListImages(ctx, building.ID, ImageFilter{})
	integWantKind(t, err, ErrConflict, "数据版本还没有生成完成")
	_, err = e.svc.GetImage(ctx, building.ID, 1)
	integWantKind(t, err, ErrConflict, "数据版本还没有生成完成")
	_, err = e.svc.OpenThumbnail(ctx, building.ID, 1, dataset.ThumbSmall)
	integWantKind(t, err, ErrConflict, "数据版本还没有生成完成")
	_, _, err = e.svc.ListImages(ctx, 1<<40, ImageFilter{})
	integWantKind(t, err, ErrNotFound, "数据版本不存在")

	// In the recycle bin: not found.
	if err := e.svc.moveToBin(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.ListImages(ctx, v.ID, ImageFilter{})
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
	_, err = e.svc.GetImage(ctx, v.ID, img[0])
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
	_, err = e.svc.OpenImage(ctx, v.ID, img[0])
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
	_, err = e.svc.OpenThumbnail(ctx, v.ID, img[0], dataset.ThumbSmall)
	integWantKind(t, err, ErrNotFound, "数据版本不存在")
}

func TestIntegrationDataVersionImageFiles(t *testing.T) {
	e := integNewService(t)
	ctx := context.Background()
	pid := e.integProject(t, "p", "scratch")
	v := e.integInsertReadyVersion(t, integVersionSpec{ProjectID: pid, Images: []integImageSpec{
		{Path: "images/d/a.jpg", Width: 640, Height: 480},
		{Path: "images/d/b.png"},
		{Path: "images/d/broken.jpg", Data: []byte("not an image")},
		{Path: "images/d/gone.jpg", NoFile: true},
	}})
	a, png, broken, gone := v.ImageIDs[0], v.ImageIDs[1], v.ImageIDs[2], v.ImageIDs[3]

	f, err := e.svc.OpenImage(ctx, v.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f.File)
	f.File.Close()
	want, _ := os.ReadFile(filepath.Join(v.Dir, "images", "d", "a.jpg"))
	if f.ContentType != "image/jpeg" || f.Name != "a.jpg" || !bytes.Equal(data, want) {
		t.Errorf("OpenImage = %q %q, %d bytes", f.ContentType, f.Name, len(data))
	}
	if f, err := e.svc.OpenImage(ctx, v.ID, png); err != nil || f.ContentType != "image/png" {
		t.Errorf("OpenImage of a PNG = %+v, %v", f, err)
	} else {
		f.File.Close()
	}
	_, err = e.svc.OpenImage(ctx, v.ID, gone)
	integWantKind(t, err, ErrNotFound, "图片文件不存在")
	if strings.Contains(e.logs.String(), "changed since the version was built") {
		t.Error("unchanged files must not be reported as changed")
	}

	// A file rewritten in place is served as it is, with a warning.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(v.Dir, "images", "d", "b.png"), later, later); err != nil {
		t.Fatal(err)
	}
	if f, err := e.svc.OpenImage(ctx, v.ID, png); err != nil {
		t.Errorf("OpenImage of a changed file: %v", err)
	} else {
		f.File.Close()
	}
	if !strings.Contains(e.logs.String(), "changed since the version was built") {
		t.Error("a changed file must be logged")
	}

	// Thumbnails: generated once, then cached for good.
	for _, size := range []string{dataset.ThumbSmall, dataset.ThumbLarge} {
		th, err := e.svc.OpenThumbnail(ctx, v.ID, a, size)
		if err != nil {
			t.Fatalf("OpenThumbnail %s: %v", size, err)
		}
		cfg, err := jpeg.DecodeConfig(th.File)
		th.File.Close()
		side, _ := dataset.ThumbnailSide(size)
		if err != nil || th.ContentType != "image/jpeg" || th.Name != "a.jpg" || max(cfg.Width, cfg.Height) != min(side, 640) {
			t.Errorf("thumbnail %s = %+v %+v, %v", size, th, cfg, err)
		}
		cache := filepath.Join(e.svc.thumbDir(v.ID), strconv.FormatInt(a, 10)+"-"+size+".jpg")
		if !integExists(cache) {
			t.Errorf("thumbnail %s not cached at %s", size, cache)
		}
	}
	cache := filepath.Join(e.svc.thumbDir(v.ID), strconv.FormatInt(a, 10)+"-sm.jpg")
	if err := os.WriteFile(cache, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	th, err := e.svc.OpenThumbnail(ctx, v.ID, a, dataset.ThumbSmall)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(th.File)
	th.File.Close()
	if string(data) != "cached" {
		t.Error("a cached thumbnail must not be generated again")
	}
	if th, err := e.svc.OpenThumbnail(ctx, v.ID, png, dataset.ThumbSmall); err != nil || th.Name != "b.jpg" {
		t.Errorf("thumbnail of a PNG = %+v, %v", th, err)
	} else {
		th.File.Close()
	}
	_, err = e.svc.OpenThumbnail(ctx, v.ID, broken, dataset.ThumbSmall)
	integWantKind(t, err, ErrUnsupportedImage, "缩略图")
	if !errors.Is(err, ErrUnsupportedImage) {
		t.Errorf("errors.Is(%v, ErrUnsupportedImage) = false", err)
	}
	_, err = e.svc.OpenThumbnail(ctx, v.ID, gone, dataset.ThumbSmall)
	integWantKind(t, err, ErrNotFound, "图片文件不存在")
	_, err = e.svc.OpenThumbnail(ctx, v.ID, a, "xl")
	integWantKind(t, err, ErrBadRequest, "size")
	_, err = e.svc.OpenThumbnail(ctx, v.ID, 1<<40, dataset.ThumbSmall)
	integWantKind(t, err, ErrNotFound, "图片不存在")
	if entries, _ := os.ReadDir(e.svc.thumbDir(v.ID)); len(entries) != 3 {
		t.Errorf("thumbnail cache = %v (no temporary files may be left)", entries)
	}
}
