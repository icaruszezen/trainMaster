package dataversion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

func TestCoreCreateInputChecks(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 10))
	d2 := e.coreDataset(t, "ds2", coreLineFiles(t, 2))
	rv2 := e.coreReviewVersion(t, d2.ID, coreVersionImage{"OK/p00.png", "needs_sample", []string{"dent"}})
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	cv := e.coreClassVersion(t, p.ID)
	cases := []struct {
		name       string
		edit       func(in *CreateInput)
		createOnly bool
		kind       error
		msg        string
	}{
		{"no project", func(in *CreateInput) { in.ProjectID = 0 }, false, ErrInvalid, "项目不存在（可能已被删除）"},
		{"unknown project", func(in *CreateInput) { in.ProjectID = p.ID + 1000 }, false, ErrInvalid, "项目不存在（可能已被删除）"},
		{"no sources", func(in *CreateInput) { in.Sources = nil }, false, ErrInvalid, "请至少选择一个数据集"},
		{"dataset twice", func(in *CreateInput) { in.Sources = append(in.Sources, in.Sources[0]) }, false, ErrInvalid,
			"同一个数据集只能选择一次"},
		{"bad dataset id", func(in *CreateInput) { in.Sources[0].DatasetID = 0 }, false, ErrInvalid, "数据集 ID 无效：0"},
		{"bad version id", func(in *CreateInput) { in.Sources[0].VersionIDs = []int64{-1} }, false, ErrInvalid,
			"审图版本 ID 无效：-1"},
		{"no annotation source", func(in *CreateInput) { in.Sources[0].IncludeBase = false }, false, ErrInvalid,
			"请为每个数据集至少选择一个标注来源（基础标注或审图版本）"},
		{"no train", func(in *CreateInput) { in.Split.Train, in.Split.Val = 0, 100 }, false, ErrInvalid, "训练集比例至少为 1%"},
		{"no val", func(in *CreateInput) { in.Split.Train, in.Split.Val = 100, 0 }, false, ErrInvalid, "验证集比例至少为 1%"},
		{"negative test", func(in *CreateInput) { in.Split.Test = -1 }, false, ErrInvalid, "测试集比例不能小于 0%"},
		{"ratios", func(in *CreateInput) { in.Split.Train = 70 }, false, ErrInvalid, "比例之和必须是 100%（现在是 90%）"},
		{"seed", func(in *CreateInput) { in.Split.Seed = -1 }, false, ErrInvalid, "随机种子必须在 0 到 2147483647 之间"},
		{"group by", func(in *CreateInput) { in.Split.GroupBy = "name" }, false, ErrInvalid, "分组方式必须是"},
		{"empty ignored label", func(in *CreateInput) { in.IgnoreLabels = []string{" "} }, false, ErrInvalid,
			"忽略的标签不能为空"},
		{"long ignored label", func(in *CreateInput) { in.IgnoreLabels = []string{strings.Repeat("长", 101)} }, false,
			ErrInvalid, "超过 100 个字符"},
		{"ignored class", func(in *CreateInput) { in.IgnoreLabels = []string{"dust", " scratch "} }, false, ErrInvalid,
			"忽略的标签“scratch”在类别列表中"},
		{"no copies", func(in *CreateInput) { in.Augmentation = &Augmentation{Methods: []string{"hflip"}} }, false,
			ErrInvalid, "每张图片至少生成 1 份增强副本"},
		{"too many copies", func(in *CreateInput) {
			in.Augmentation = &Augmentation{Methods: []string{"hflip"}, Copies: 2}
		}, false, ErrInvalid, "所选增强方式最多能生成 1 份不同的副本"},
		{"bad method", func(in *CreateInput) { in.Augmentation = &Augmentation{Methods: []string{"zoom"}, Copies: 1} },
			false, ErrInvalid, ""},
		{"no methods", func(in *CreateInput) { in.Augmentation = &Augmentation{Methods: []string{}, Copies: 1} },
			false, ErrInvalid, ""},
		{"dataset not in project", func(in *CreateInput) { in.Sources = coreBase(d.ID, d2.ID) }, false, ErrInvalid,
			"数据集“ds2”不在项目中"},
		{"unknown dataset", func(in *CreateInput) { in.Sources = coreBase(d.ID, d2.ID+1000) }, false, ErrInvalid,
			"数据集不存在（可能已被删除）"},
		{"version of another dataset", func(in *CreateInput) { in.Sources[0].VersionIDs = []int64{rv2.ID} }, false,
			ErrInvalid, "所选审图版本不存在（可能已被删除）"},
		{"unknown version", func(in *CreateInput) { in.Sources[0].VersionIDs = []int64{rv2.ID + 1000} }, false,
			ErrInvalid, "所选审图版本不存在（可能已被删除）"},
		{"empty note", func(in *CreateInput) { in.Note = " " }, true, ErrInvalid, "请填写版本备注"},
		{"long note", func(in *CreateInput) { in.Note = strings.Repeat("备", 501) }, true, ErrInvalid,
			"版本备注不能超过 500 个字符"},
		{"stale class version", func(in *CreateInput) { in.ClassVersion = "x" }, true, ErrConflict,
			"项目的类别列表已经修改，请检查后重新生成"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := coreInput(p.ID, cv, coreBase(d.ID)...)
			c.edit(&in)
			_, err := e.svc.Create(ctx, in)
			coreWantKind(t, err, c.kind, c.msg)
			_, err = e.svc.Preview(ctx, in)
			if c.createOnly {
				if err != nil {
					t.Errorf("Preview: %v", err)
				}
			} else {
				coreWantKind(t, err, c.kind, c.msg)
			}
		})
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM data_versions").Scan(&n); err != nil || n != 0 {
		t.Errorf("%d versions created (%v)", n, err)
	}
	if err := e.db.QueryRow(ctx, "SELECT data_version_seq FROM projects WHERE id = $1", p.ID).Scan(&n); err != nil ||
		n != 0 {
		t.Errorf("data_version_seq = %d (%v)", n, err)
	}

	// The input is normalized: ignored labels trimmed, de-duplicated and
	// sorted; review version ids de-duplicated; methods in display order.
	rv := e.coreReviewVersion(t, d.ID, coreVersionImage{"OK/p00.png", "approved", []string{"scratch"}})
	in := coreInput(p.ID, cv, SourceInput{DatasetID: d.ID, IncludeBase: true, VersionIDs: []int64{rv.ID, rv.ID}})
	in.IgnoreLabels = []string{" dust", "dirt", "dust "}
	in.Augmentation = &Augmentation{Methods: []string{"vflip", "hflip", "hflip"}, Copies: 3, OnlyWithObjects: true}
	v, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(v.Config.IgnoreLabels, ",") != "dirt,dust" || len(v.Sources[0].Versions) != 1 ||
		v.Sources[0].Versions[0].ID != rv.ID || strings.Join(v.Config.Augmentation.Methods, ",") != "hflip,vflip" ||
		v.Config.Augmentation.Copies != 3 || !v.Config.Augmentation.OnlyWithObjects {
		t.Errorf("version = %+v", v)
	}
}

func TestCoreCreateStates(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 10))
	files := map[string][]byte{}
	for i := range 4 {
		coreLabeled(t, files, fmt.Sprintf("OK/p%02d.png", i), i, "scratch")
	}
	one := e.coreDataset(t, "one", files)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID, one.ID)
	cv := e.coreClassVersion(t, p.ID)

	// A review version still being built.
	var reviewID, mergeID int64
	if err := e.db.QueryRow(ctx, `INSERT INTO reviews (name, model_ref, options, status) VALUES ('审图', '{}', '{}', 'ready')
		RETURNING id`).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, note, status)
		VALUES ($1, $2, '{needs_sample}', 'x', 'running') RETURNING id`, reviewID, []int64{d.ID}).Scan(&mergeID); err != nil {
		t.Fatal(err)
	}
	building, err := e.datasets.CreateReviewVersion(ctx, dataset.ReviewVersionDraft{DatasetID: d.ID, ReviewID: reviewID,
		MergeID: mergeID, Note: "审图"})
	if err != nil {
		t.Fatal(err)
	}
	in := coreInput(p.ID, cv, SourceInput{DatasetID: d.ID, IncludeBase: true, VersionIDs: []int64{building}})
	_, err = e.svc.Create(ctx, in)
	coreWantKind(t, err, ErrConflict, "还没有生成完成")
	_, err = e.svc.Preview(ctx, in)
	coreWantKind(t, err, ErrConflict, "还没有生成完成")
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_versions WHERE id = $1", building); err != nil {
		t.Fatal(err)
	}

	// A review version in the recycle bin.
	rv := e.coreReviewVersion(t, d.ID, coreVersionImage{"OK/p00.png", "approved", []string{"scratch"}})
	if err := e.datasets.DeleteVersion(ctx, d.ID, rv.ID); err != nil {
		t.Fatal(err)
	}
	in.Sources[0].VersionIDs = []int64{rv.ID}
	_, err = e.svc.Create(ctx, in)
	coreWantKind(t, err, ErrInvalid, "所选审图版本不存在（可能已被删除）")

	// An empty class list: the preview works (everything is unlisted),
	// Create refuses.
	empty := e.coreProject(t, "empty", nil, d.ID)
	in = coreInput(empty.ID, e.coreClassVersion(t, empty.ID), coreBase(d.ID)...)
	pre, err := e.svc.Preview(ctx, in)
	if err != nil || len(pre.Classes) != 0 || pre.ImageCount != 1 || pre.Skipped.Unlisted != 12 ||
		len(pre.UnlistedLabels) != 3 {
		t.Errorf("preview without classes = %+v, %v", pre, err)
	}
	_, err = e.svc.Create(ctx, in)
	coreWantKind(t, err, ErrConflict, "项目还没有类别，请先在项目中添加类别")

	// One folder grouped by folder: one split stays empty.
	in = coreInput(p.ID, cv, coreBase(one.ID)...)
	in.Split.GroupBy = GroupByFolder
	if pre, err := e.svc.Preview(ctx, in); err != nil || pre.ImageCount != 4 ||
		(pre.Splits.Val.Images != 0 && pre.Splits.Train.Images != 0) {
		t.Errorf("preview = %+v, %v", pre.Splits, err)
	}
	_, err = e.svc.Create(ctx, in)
	coreWantKind(t, err, ErrConflict, "，请调整划分比例、换一个随机种子，或增加图片")

	// A number whose folder exists is skipped (and left alone).
	foreign := e.svc.versionDir(versionDirName(p.ID, 1))
	coreWriteFiles(t, foreign, map[string][]byte{"keep.txt": []byte("backup")})
	v, err := e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	if err != nil || v.Version != 2 {
		t.Fatalf("Create = %+v, %v", v, err)
	}
	if coreReadFile(t, foreign, "keep.txt") != "backup" {
		t.Error("the existing folder was touched")
	}
	// ... at most 100 times.
	for n := 3; n <= 103; n++ {
		if err := os.MkdirAll(e.svc.versionDir(versionDirName(p.ID, n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err = e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	coreWantKind(t, err, ErrConflict, "等已经存在（可能来自备份），请先移走它们")
	var seq int
	if err := e.db.QueryRow(ctx, "SELECT data_version_seq FROM projects WHERE id = $1", p.ID).Scan(&seq); err != nil ||
		seq != 2 {
		t.Errorf("data_version_seq = %d (%v)", seq, err)
	}

	// The project in the recycle bin.
	if _, err := e.db.Exec(ctx, "UPDATE projects SET deleted_at = now() WHERE id = $1", p.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	coreWantKind(t, err, ErrInvalid, "项目不存在（可能已被删除）")
	_, err = e.svc.Preview(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	coreWantKind(t, err, ErrInvalid, "项目不存在（可能已被删除）")

	// A dataset in the recycle bin.
	q := e.coreProject(t, "q", []string{"scratch", "dent"}, d.ID)
	if err := e.datasets.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Create(ctx, coreInput(q.ID, e.coreClassVersion(t, q.ID), coreBase(d.ID)...))
	coreWantKind(t, err, ErrInvalid, "数据集不存在（可能已被删除）")
}

// coreGIF is a small GIF image.
func coreGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 32, 24), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCorePreviewCounts(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	files := coreLineFiles(t, 10)
	coreLabeled(t, files, "NG/dust.png", 104, "dust", "scratch")
	coreLabeled(t, files, "NG/dust-only.png", 105, "dust")
	files["NG/bad.png"] = corePNG(t, 32, 24, 106)
	files["NG/bad.xml"] = []byte("<annotation><object>")
	files["NG/anim.gif"] = coreGIF(t)
	files["NG/anim.xml"] = coreVOC("anim.gif", 32, 24, coreBox{"scratch", 1, 1, 9, 9})
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	// Another project in use sharing the dataset knows dirt; one in the
	// bin knows dust.
	e.coreProject(t, "other", []string{"dirt"}, d.ID)
	gone := e.coreProject(t, "gone", []string{"dust"}, d.ID)
	if _, err := e.db.Exec(ctx, "UPDATE projects SET deleted_at = now() WHERE id = $1", gone.ID); err != nil {
		t.Fatal(err)
	}
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	in.IgnoreLabels = []string{"dust"}
	in.Augmentation = &Augmentation{Methods: []string{"hflip"}, Copies: 1, OnlyWithObjects: true}
	pre, err := e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Taken: p00..p09, dent, empty, dust (1 object kept) and dust-only
	// (a negative now).
	wantSkipped := Skipped{Unannotated: 1, Invalid: 1, UnsupportedFormat: 1, Unlisted: 1}
	if pre.ImageCount != 14 || pre.ObjectCount != 13 || pre.DroppedObjects != 2 || pre.Skipped != wantSkipped ||
		len(pre.Sources) != 1 || pre.Sources[0].SelectedCount != 18 || pre.Sources[0].ImageCount != 14 ||
		pre.Sources[0].DatasetName != "ds" || pre.Compare != nil || pre.ClassVersion != in.ClassVersion {
		t.Errorf("preview = %+v", pre)
	}
	s := pre.Splits
	if s.Train.Images+s.Val.Images+s.Test.Images != 14 || s.Train.Negatives+s.Val.Negatives != 2 ||
		s.Train.Objects+s.Val.Objects != 13 || s.Val.Images == 0 || s.Test.Images != 0 {
		t.Errorf("splits = %+v", s)
	}
	if pre.AugmentedCount != s.Train.Images-s.Train.Negatives {
		t.Errorf("augmented = %d, splits %+v", pre.AugmentedCount, s)
	}
	if len(pre.Labels) != 2 || pre.Labels[0].Name != "scratch" || pre.Labels[1].Name != "dent" ||
		pre.Labels[0].Train.Objects+pre.Labels[0].Val.Objects != 12 || pre.Labels[1].Train.Objects+pre.Labels[1].Val.Objects != 1 {
		t.Errorf("labels = %+v", pre.Labels)
	}
	// Ignored labels are listed too (not in the class list), by objects.
	want := []UnlistedLabel{{Name: "dust", Objects: 2, Images: 2}, {Name: "dirt", Objects: 1, Images: 1, OtherProject: true}}
	if fmt.Sprint(pre.UnlistedLabels) != fmt.Sprint(want) {
		t.Errorf("unlisted = %+v", pre.UnlistedLabels)
	}

	// Negatives left out; dirt ignored too; augmenting negatives.
	in.IncludeEmpty = false
	in.IgnoreLabels = []string{"dirt", "dust"}
	in.Augmentation.OnlyWithObjects = false
	pre, err = e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	wantSkipped = Skipped{Unannotated: 1, Invalid: 1, UnsupportedFormat: 1, Empty: 3}
	// Dropped objects count in the images taken only (dust-only is out).
	if pre.ImageCount != 12 || pre.ObjectCount != 13 || pre.DroppedObjects != 1 || pre.Skipped != wantSkipped ||
		pre.Splits.Train.Negatives+pre.Splits.Val.Negatives != 0 || pre.AugmentedCount != pre.Splits.Train.Images {
		t.Errorf("preview = %+v", pre)
	}

	// The build agrees with the preview (augmentation: build_augment_test.go).
	in.Augmentation = nil
	if pre, err = e.svc.Preview(ctx, in); err != nil {
		t.Fatal(err)
	}
	v := e.coreCreateBuilt(t, in)
	if v.ImageCount != pre.ImageCount || v.ObjectCount != pre.ObjectCount || v.TrainCount != pre.Splits.Train.Images ||
		v.ValCount != pre.Splits.Val.Images || v.Stats.Skipped != pre.Skipped || v.Stats.DroppedObjects != 1 ||
		fmt.Sprint(v.Stats.Labels) != fmt.Sprint(pre.Labels) {
		t.Errorf("version = %+v\npreview = %+v", v, pre)
	}
}

func TestCoreCreateSourceOrder(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	a := e.coreDataset(t, "a", coreLineFiles(t, 4))
	b := e.coreDataset(t, "b", coreLineFiles(t, 4))
	r1 := e.coreReviewVersion(t, a.ID, coreVersionImage{"OK/p00.png", "approved", []string{"dent"}})
	r2 := e.coreReviewVersion(t, a.ID, coreVersionImage{"OK/p01.png", "approved", []string{"dent"}})
	// b was added to the project first.
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, b.ID, a.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID),
		SourceInput{DatasetID: a.ID, IncludeBase: true, VersionIDs: []int64{r1.ID, r2.ID}},
		SourceInput{DatasetID: b.ID, IncludeBase: true, VersionIDs: []int64{}})
	pre, err := e.svc.Preview(ctx, in)
	if err != nil || len(pre.Sources) != 2 || pre.Sources[0].DatasetID != b.ID || pre.Sources[1].FromVersions != 2 {
		t.Errorf("preview sources = %+v, %v", pre.Sources, err)
	}
	v, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Sources) != 2 || v.Sources[0].DatasetID != b.ID || v.Sources[1].DatasetID != a.ID ||
		len(v.Sources[1].Versions) != 2 || v.Sources[1].Versions[0].ID != r2.ID || v.Sources[1].Versions[0].Name != r2.Name ||
		v.Sources[1].Versions[1].ID != r1.ID {
		t.Errorf("sources = %+v", v.Sources)
	}
	// The second version queues behind the first.
	w, err := e.svc.Create(ctx, in)
	if err != nil || w.Version != 2 || w.QueuePosition == nil || *w.QueuePosition != 2 {
		t.Errorf("second version = %+v, %v", w, err)
	}
	// Notes can be edited while queued; nothing is written.
	note := "第二版"
	if w, err = e.svc.Update(ctx, w.ID, UpdateInput{Note: &note}); err != nil || w.Note != "第二版" {
		t.Errorf("Update = %+v, %v", w, err)
	}
	if _, err := os.Lstat(e.coreDir(t, w)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder of a queued version: %v", err)
	}
	if _, err := e.svc.Update(ctx, w.ID+1000, UpdateInput{Note: &note}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update of an unknown version = %v", err)
	}
	e.coreBuildAll(t)
	for _, id := range []int64{v.ID, w.ID} {
		if got, _ := e.svc.Get(ctx, id); got.Status != StatusReady || got.Sources[0].ImageCount == 0 ||
			got.Sources[1].ImageCount == 0 || got.Sources[0].ImageCount+got.Sources[1].ImageCount != got.ImageCount {
			t.Errorf("version %d = %+v", id, got)
		}
	}
}

// TestCoreCreateRacesDeleteDataset: Create locks the datasets before the
// project, as DeleteDataset does, so the two never deadlock; either
// may win.
func TestCoreCreateRacesDeleteDataset(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	var ids []int64
	for i := range 4 {
		ids = append(ids, e.coreDataset(t, fmt.Sprintf("ds%d", i), coreLineFiles(t, 4)).ID)
	}
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, ids...)
	cv := e.coreClassVersion(t, p.ID)
	for _, id := range ids {
		in := coreInput(p.ID, cv, coreBase(id)...)
		in.Split.Train, in.Split.Val = 50, 50
		var wg sync.WaitGroup
		var createErr, deleteErr error
		var v DataVersion
		wg.Add(2)
		go func() {
			defer wg.Done()
			v, createErr = e.svc.Create(ctx, in)
		}()
		go func() {
			defer wg.Done()
			deleteErr = e.datasets.DeleteDataset(ctx, id, true)
		}()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("Create and DeleteDataset are stuck")
		}
		var pe *pgconn.PgError
		if errors.As(createErr, &pe) || errors.As(deleteErr, &pe) {
			t.Fatalf("database error: create %v, delete %v", createErr, deleteErr)
		}
		if deleteErr != nil {
			t.Fatalf("DeleteDataset: %v", deleteErr)
		}
		var ue *Error
		if createErr != nil && !errors.As(createErr, &ue) {
			t.Fatalf("Create: %v", createErr)
		}
		if createErr == nil {
			// Queued before the delete: its build fails.
			e.coreBuildAll(t)
			if got, _ := e.svc.Get(ctx, v.ID); got.Status != StatusFailed || !strings.Contains(got.Error, "已被删除") {
				t.Errorf("version = %s %q", got.Status, got.Error)
			}
		}
	}
}

func TestCoreDeleteUnbuilt(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	cv := e.coreClassVersion(t, p.ID)
	in := coreInput(p.ID, cv, coreBase(d.ID)...)
	in.Split.Train, in.Split.Val = 50, 50

	// Queued.
	q, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, q.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v", err)
	}
	if err := e.svc.Delete(ctx, q.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
	if built, err := e.svc.buildNext(ctx); built || err != nil {
		t.Errorf("buildNext = %v, %v", built, err)
	}

	// Failed, with a folder left by a crash (owned: .pending) and
	// thumbnails: both go.
	f := coreFailedVersion(t, e, in)
	dir := e.coreDir(t, f)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePending(dir, f.ID); err != nil {
		t.Fatal(err)
	}
	coreWriteFiles(t, e.svc.thumbDir(f.ID), map[string][]byte{"1-sm.jpg": []byte("x")})
	if err := e.svc.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.WaitBackground()
	if _, err := e.svc.Get(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v", err)
	}
	for _, p := range []string{dir, e.svc.thumbDir(f.ID)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left: %v", p, err)
		}
	}

	// DeleteProjectVersions: queued, building (no builder) and failed.
	if _, err = e.svc.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	b, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE data_versions SET status = 'building', started_at = now() WHERE id = $1",
		b.ID); err != nil {
		t.Fatal(err)
	}
	f = coreFailedVersion(t, e, in)
	other := e.coreProject(t, "other", []string{"scratch", "dent"}, d.ID)
	keep, err := e.svc.Create(ctx, coreInput(other.ID, e.coreClassVersion(t, other.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteProjectVersions(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT count(*) FROM data_versions WHERE project_id = $1", p.ID).Scan(&n); err != nil ||
		n != 0 {
		t.Errorf("%d versions left (%v)", n, err)
	}
	if _, err := e.svc.Get(ctx, keep.ID); err != nil {
		t.Errorf("the other project's version: %v", err)
	}
	if err := e.svc.DeleteProjectVersions(ctx, p.ID); err != nil {
		t.Errorf("second DeleteProjectVersions: %v", err)
	}
}

// coreFailedVersion creates a version and marks it failed (as a build
// does).
func coreFailedVersion(t *testing.T, e *coreEnv, in CreateInput) DataVersion {
	t.Helper()
	ctx := context.Background()
	v, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE data_versions SET status = 'failed', error = '生成失败（服务器内部错误）',
		finished_at = now() WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	if v, err = e.svc.Get(ctx, v.ID); err != nil || v.Status != StatusFailed {
		t.Fatalf("failed version = %+v, %v", v, err)
	}
	return v
}
