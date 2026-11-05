package dataset

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// testVersionImage is an image of a review version made in a test.
type testVersionImage struct {
	path, status string
	labels       []string // one box per label
}

// addVersion builds a review version of a dataset as a merge does and
// numbers it (status ready). It returns the version.
func (e *testEnv) addVersion(t *testing.T, datasetID int64, note string, images ...testVersionImage) Version {
	t.Helper()
	ctx := context.Background()
	mergeID, reviewID := e.addMerge(t, datasetID)
	id, err := e.svc.CreateReviewVersion(ctx, ReviewVersionDraft{DatasetID: datasetID, ReviewID: reviewID, MergeID: mergeID,
		Note: note, Source: VersionSource{ReviewID: reviewID, ReviewName: "审图", MergeID: mergeID,
			Statuses: []string{"needs_sample"}}})
	if err != nil {
		t.Fatalf("CreateReviewVersion: %v", err)
	}
	for _, img := range images {
		var objects []AnnotationObject
		for i, l := range img.labels {
			objects = append(objects, AnnotationObject{Name: l, Xmin: 2 + i, Ymin: 2, Xmax: 12 + i, Ymax: 20})
		}
		if err := e.svc.AddVersionImage(ctx, id, VersionImage{Path: img.path, ReviewStatus: img.status, Objects: objects}); err != nil {
			t.Fatalf("AddVersionImage %s: %v", img.path, err)
		}
	}
	var refs []VersionRef
	if err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error {
		refs, err = e.svc.FinishReviewVersions(ctx, tx, mergeID)
		return err
	}); err != nil || len(refs) != 1 || refs[0].ID != id {
		t.Fatalf("FinishReviewVersions = %+v, %v", refs, err)
	}
	v, err := e.svc.GetVersion(ctx, datasetID, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// addMerge inserts a review and a running merge of it (what versions come
// from).
func (e *testEnv) addMerge(t *testing.T, datasetID int64) (mergeID, reviewID int64) {
	t.Helper()
	ctx := context.Background()
	if err := e.db.QueryRow(ctx, `INSERT INTO reviews (name, model_ref, options, status) VALUES ('审图', '{}', '{}', 'ready')
		RETURNING id`).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO review_merges (review_id, dataset_ids, statuses, note, status)
		VALUES ($1, $2, '{needs_sample}', 'x', 'running') RETURNING id`, reviewID, []int64{datasetID}).Scan(&mergeID); err != nil {
		t.Fatal(err)
	}
	return mergeID, reviewID
}

// selection selects versions for a download.
func selection(base bool, versions ...Version) *VersionSelection {
	sel := &VersionSelection{IncludeBase: base, VersionIDs: []int64{}}
	for _, v := range versions {
		sel.VersionIDs = append(sel.VersionIDs, v.ID)
	}
	return sel
}

// vocLabels returns the labels of the boxes of a VOC file.
func vocLabels(t *testing.T, data string) []string {
	t.Helper()
	a, err := parseVOC(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse VOC: %v\n%s", err, data)
	}
	labels := []string{}
	for _, o := range a.Objects {
		labels = append(labels, o.Name)
	}
	return labels
}

func TestIntegrationVersions(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	img := pngFile(t, 64, 48)
	writeFiles(t, d.Path, map[string][]byte{"a.png": img, "b.png": img})
	scan, _ := e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)
	other, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})

	if vs, err := e.svc.ListVersions(ctx, d.ID); err != nil || vs == nil || len(vs) != 0 {
		t.Errorf("ListVersions before any = %+v, %v", vs, err)
	}
	v0 := e.addVersion(t, d.ID, "第一轮", testVersionImage{"a.png", "needs_sample", []string{"scratch", "scratch"}},
		testVersionImage{"b.png", "approved", nil})
	v1 := e.addVersion(t, d.ID, "第二轮", testVersionImage{"b.png", "needs_sample", []string{"dent"}})
	if v0.Name != "审图1.0" || v1.Name != "审图1.1" || v0.ImageCount != 2 || v0.NeedsSampleCount != 1 || v0.ApprovedCount != 1 ||
		v0.ObjectCount != 2 || v0.Status != VersionReady || v0.Note != "第一轮" || v0.FinishedAt == nil || v0.Review == nil ||
		v0.Review.Name != "审图" || v0.MergeID == nil {
		t.Errorf("versions = %+v / %+v", v0, v1)
	}
	if vs, err := e.svc.ListVersions(ctx, d.ID); err != nil || len(vs) != 2 || vs[0].ID != v1.ID || vs[1].ID != v0.ID {
		t.Errorf("ListVersions = %+v, %v", vs, err)
	}
	if got, err := e.svc.GetDataset(ctx, d.ID); err != nil || got.VersionCount != 2 {
		t.Errorf("dataset = %+v, %v", got, err)
	}
	if list, err := e.svc.ListDatasets(ctx, DatasetQuery{}); err != nil || len(list) != 2 {
		t.Errorf("ListDatasets = %+v, %v", list, err)
	} else {
		for _, ds := range list {
			if want := map[int64]int{d.ID: 2, other.ID: 0}[ds.ID]; ds.VersionCount != want {
				t.Errorf("%s version_count = %d", ds.Name, ds.VersionCount)
			}
		}
	}

	// The note: required, trimmed, at most 500 characters.
	note := "  改过的备注\n第二行 "
	if v, err := e.svc.UpdateVersion(ctx, d.ID, v0.ID, UpdateVersionInput{Note: &note}); err != nil || v.Note != "改过的备注\n第二行" ||
		!v.UpdatedAt.After(v0.UpdatedAt) {
		t.Errorf("UpdateVersion = %+v, %v", v, err)
	}
	for _, bad := range []struct{ note, msg string }{{" ", "请填写版本备注"}, {"a\x01", "无效字符"}, {strings.Repeat("长", 501), "500"},
		{"\xff", "无效字符"}} {
		_, err := e.svc.UpdateVersion(ctx, d.ID, v0.ID, UpdateVersionInput{Note: &bad.note})
		wantKind(t, err, ErrInvalid, bad.msg)
	}
	if v, err := e.svc.UpdateVersion(ctx, d.ID, v0.ID, UpdateVersionInput{}); err != nil || v.Note != "改过的备注\n第二行" {
		t.Errorf("UpdateVersion without changes = %+v, %v", v, err)
	}
	_, err := e.svc.UpdateVersion(ctx, other.ID, v0.ID, UpdateVersionInput{Note: &note})
	wantKind(t, err, ErrNotFound, "版本不存在")
	_, err = e.svc.UpdateVersion(ctx, 9999, v0.ID, UpdateVersionInput{Note: &note})
	wantKind(t, err, ErrNotFound, "数据集不存在")
	_, err = e.svc.GetVersion(ctx, d.ID, 9999)
	wantKind(t, err, ErrNotFound, "版本不存在")
	_, err = e.svc.ListVersions(ctx, 9999)
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// A version being built cannot be changed, and is listed first.
	mergeID, reviewID := e.addMerge(t, d.ID)
	building, err := e.svc.CreateReviewVersion(ctx, ReviewVersionDraft{DatasetID: d.ID, ReviewID: reviewID, MergeID: mergeID,
		Note: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if vs, _ := e.svc.ListVersions(ctx, d.ID); len(vs) != 3 || vs[0].ID != building || vs[0].Name != "" || vs[0].Major != nil {
		t.Errorf("ListVersions with one building = %+v", vs)
	}
	if got, _ := e.svc.GetDataset(ctx, d.ID); got.VersionCount != 2 {
		t.Errorf("version_count counts a version being built: %d", got.VersionCount)
	}
	_, err = e.svc.UpdateVersion(ctx, d.ID, building, UpdateVersionInput{Note: &note})
	wantKind(t, err, ErrConflict, "正在生成")
	wantKind(t, e.svc.DeleteVersion(ctx, d.ID, building), ErrConflict, "正在生成")
	_, err = e.svc.PreviewExport(ctx, d.ID, VersionSelection{VersionIDs: []int64{building}})
	wantKind(t, err, ErrConflict, "正在生成")
	// An image no longer in the dataset is not added.
	wantKind(t, e.svc.AddVersionImage(ctx, building, VersionImage{Path: "gone.png", ReviewStatus: "approved"}),
		ErrNotFound, "图片已不在数据集中")
	if err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error { return e.svc.DiscardReviewVersions(ctx, tx, mergeID) }); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.GetVersion(ctx, d.ID, building)
	wantKind(t, err, ErrNotFound, "版本不存在")

	// Deleting a version; its number is not given again.
	if err := e.svc.DeleteVersion(ctx, d.ID, v1.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, e.svc.DeleteVersion(ctx, d.ID, v1.ID), ErrNotFound, "版本不存在")
	wantKind(t, e.svc.DeleteVersion(ctx, other.ID, v0.ID), ErrNotFound, "版本不存在")
	if v2 := e.addVersion(t, d.ID, "第三轮", testVersionImage{"a.png", "approved", nil}); v2.Name != "审图1.2" {
		t.Errorf("version after a delete = %s", v2.Name)
	}
	// A version of a deleted dataset cannot be built; deleting a dataset
	// deletes its versions.
	mergeID, reviewID = e.addMerge(t, other.ID)
	if _, err := e.svc.DeleteDataset(ctx, other.ID, true); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateReviewVersion(ctx, ReviewVersionDraft{DatasetID: other.ID, ReviewID: reviewID, MergeID: mergeID, Note: "x"})
	wantKind(t, err, ErrNotFound, "数据集已被删除")
	if _, err := e.svc.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.db.QueryRow(ctx, "SELECT (SELECT count(*) FROM dataset_versions) + (SELECT count(*) FROM dataset_version_images)").
		Scan(&n); err != nil || n != 0 {
		t.Errorf("versions left after deleting the dataset: %d, %v", n, err)
	}
}

func TestIntegrationVersionExport(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	img := pngFile(t, 64, 48)
	jpg := encodeImage(t, "jpeg", gradient(64, 48))
	writeFiles(t, d.Path, map[string][]byte{
		"a.png": img, "a.xml": vocFile("a.png", "base_a"),
		"b.png": img, "b.xml": vocFile("b.png", "base_b"),
		"c.png": img, // no annotation of its own
		"d.png": img, "d.xml": vocFile("d.png", "base_d"),
		// e.xml belongs to e.png (its <filename>); e.jpg has none.
		"e.jpg": jpg, "e.png": img, "e.xml": vocFile("e.png", "base_e"),
		"JPEGImages/v.png": img, "Annotations/v.xml": vocFile("v.png", "base_v"),
		"JPEGImages/w.png": img,
		"z.png":            img,
	})
	scan, _ := e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)

	v10 := e.addVersion(t, d.ID, "1.0",
		testVersionImage{"a.png", "needs_sample", []string{"v10_a"}},
		testVersionImage{"b.png", "approved", []string{"v10_b"}},
		testVersionImage{"c.png", "needs_sample", []string{"v10_c"}},
		testVersionImage{"z.png", "needs_sample", []string{"v10_z"}})
	v11 := e.addVersion(t, d.ID, "1.1",
		testVersionImage{"a.png", "approved", []string{"v11_a", "v11_a2"}},
		testVersionImage{"d.png", "needs_sample", nil}, // a negative sample
		testVersionImage{"e.jpg", "needs_sample", []string{"v11_e"}},
		testVersionImage{"JPEGImages/v.png", "needs_sample", []string{"v11_v"}},
		testVersionImage{"JPEGImages/w.png", "needs_sample", []string{"v11_w"}})
	v12 := e.addVersion(t, d.ID, "1.2", testVersionImage{"b.png", "needs_sample", []string{"v12_b"}})
	onlyApproved := e.addVersion(t, d.ID, "1.3", testVersionImage{"b.png", "approved", nil})

	// Since the merges: z.png is removed, c.png is replaced by another
	// picture (the boxes of 1.0 are not for it).
	if err := os.Remove(filepath.Join(d.Path, "z.png")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, d.Path, map[string][]byte{"c.png": pngFile(t, 32, 24)})
	scan, _ = e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)
	keys := func(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

	// The base alone, as before versions existed.
	name, files := e.export(t, d.ID, ExportInput{Format: FormatVOC})
	if name != "ds-voc.zip" || files["ds/a.xml"] != string(vocFile("a.png", "base_a")) || len(files) != 13 {
		t.Errorf("base export %s = %v", name, keys(files))
	}
	if n, f := e.export(t, d.ID, ExportInput{Format: FormatVOC, Versions: selection(true)}); n != name || !maps.Equal(f, files) {
		t.Errorf("base export with an explicit selection = %s %v", n, keys(f))
	}

	// One version without the base: its needs_sample images whose file is
	// still the one merged.
	name, files = e.export(t, d.ID, ExportInput{Format: FormatVOC, Versions: selection(false, v10)})
	if name != "ds-审图1.0-voc.zip" || !slices.Equal(keys(files), []string{"ds-审图1.0/a.png", "ds-审图1.0/a.xml"}) {
		t.Errorf("1.0 export %s = %v", name, keys(files))
	}
	if got := vocLabels(t, files["ds-审图1.0/a.xml"]); !slices.Equal(got, []string{"v10_a"}) {
		t.Errorf("a.xml of 1.0 = %v", got)
	}
	if !strings.Contains(files["ds-审图1.0/a.xml"], "<filename>a.png</filename>") ||
		!strings.Contains(files["ds-审图1.0/a.xml"], "<width>64</width>") {
		t.Errorf("a.xml of 1.0:\n%s", files["ds-审图1.0/a.xml"])
	}

	// Two versions without the base: the newer one decides, a is approved
	// there, so it is left out (although 1.0 has it as needs_sample).
	name, files = e.export(t, d.ID, ExportInput{Format: FormatVOC, Versions: selection(false, v10, v11)})
	top := "ds-审图1.0+审图1.1"
	want := []string{top + "/Annotations/v.xml", top + "/Annotations/w.xml", top + "/JPEGImages/v.png", top + "/JPEGImages/w.png",
		top + "/d.png", top + "/d.xml", top + "/e.jpg", top + "/e.xml"}
	if name != top+"-voc.zip" || !slices.Equal(keys(files), want) {
		t.Errorf("1.0+1.1 export %s = %v\nwant %v", name, keys(files), want)
	}
	if got := vocLabels(t, files[top+"/d.xml"]); len(got) != 0 {
		t.Errorf("d.xml (a negative sample) = %v", got)
	}
	if got := vocLabels(t, files[top+"/e.xml"]); !slices.Equal(got, []string{"v11_e"}) ||
		!strings.Contains(files[top+"/e.xml"], "<filename>e.jpg</filename>") {
		t.Errorf("e.xml = %v", got)
	}
	if got := vocLabels(t, files[top+"/Annotations/w.xml"]); !slices.Equal(got, []string{"v11_w"}) {
		t.Errorf("w.xml = %v", got)
	}

	// All versions over the base, in any order: each image takes the newest
	// version that has it, else the base.
	name, files = e.export(t, d.ID, ExportInput{Format: FormatVOC, Versions: selection(true, v12, v10, v11, v10)})
	top = "ds-基础+审图1.0+审图1.1+审图1.2"
	if name != top+"-voc.zip" || len(files) != 14 {
		t.Errorf("overlay export %s = %v", name, keys(files))
	}
	for rel, labels := range map[string][]string{
		"a.xml": {"v11_a", "v11_a2"}, "b.xml": {"v12_b"}, "d.xml": {}, "e.xml": {"base_e"},
		"Annotations/v.xml": {"v11_v"}, "Annotations/w.xml": {"v11_w"},
	} {
		if got := vocLabels(t, files[top+"/"+rel]); !slices.Equal(got, labels) {
			t.Errorf("overlay %s = %v, want %v", rel, got, labels)
		}
	}
	// c.png was replaced since 1.0: it keeps its base (no annotation); the
	// image e.jpg of 1.1 does not take e.png's e.xml.
	if _, ok := files[top+"/c.xml"]; ok || files[top+"/e.xml"] != string(vocFile("e.png", "base_e")) {
		t.Errorf("overlay c.xml present %v, e.xml %q", ok, files[top+"/e.xml"])
	}

	// The same in X-AnyLabeling and YOLO.
	_, files = e.export(t, d.ID, ExportInput{Format: FormatXAnyLabeling, Versions: selection(true, v10, v11, v12)})
	if f, err := parseXAnyLabeling([]byte(files[top+"/a.json"])); err != nil || len(f.Shapes) != 2 ||
		!strings.Contains(files[top+"/a.json"], `"label": "v11_a"`) {
		t.Errorf("overlay a.json = %+v, %v", f, err)
	}
	if f, err := parseXAnyLabeling([]byte(files[top+"/e.json"])); err != nil || f.ImagePath != "e.png" ||
		!strings.Contains(files[top+"/e.json"], `"label": "base_e"`) {
		t.Errorf("overlay e.json = %+v, %v", f, err)
	}
	_, files = e.export(t, d.ID, ExportInput{Format: FormatYOLO, Classes: []string{"base_e", "v11_a", "v12_b"},
		Versions: selection(true, v10, v11, v12)})
	if files[top+"/labels/a.txt"] != "1 0.109375 0.229167 0.156250 0.375000\n" || files[top+"/labels/b.txt"] == "" ||
		!strings.HasPrefix(files[top+"/labels/e.txt"], "0 ") {
		t.Errorf("overlay YOLO labels a %q b %q e %q", files[top+"/labels/a.txt"], files[top+"/labels/b.txt"], files[top+"/labels/e.txt"])
	}
	// A subset keeps the dataset's layout (images/ added: not all the
	// dataset's images are in images/).
	_, files = e.export(t, d.ID, ExportInput{Format: FormatYOLO, Classes: []string{"v12_b"}, Versions: selection(false, v12)})
	if top := "ds-审图1.2"; files[top+"/labels/b.txt"] == "" || files[top+"/images/b.png"] == "" ||
		!strings.Contains(files[top+"/data.yaml"], "本次导出只含数据集的部分图片") {
		t.Errorf("1.2 YOLO export = %v", keys(files))
	}

	// The preview counts the same.
	p, err := e.svc.PreviewExport(ctx, d.ID, VersionSelection{IncludeBase: true, VersionIDs: []int64{v10.ID, v11.ID, v12.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// 8 images: a b c d e.jpg e.png v w; a b d e.jpg v w from versions; c
	// and e.png from the base (c without annotation, e.png annotated). Of
	// the version images, z is gone and c was replaced.
	wantPreview := ExportPreview{ImageCount: 8, FromVersions: 6, AnnotatedCount: 6, EmptyCount: 1, UnannotatedCount: 1,
		ObjectCount: 7, VersionImageCount: 8, MissingCount: 1, ChangedCount: 1}
	labels := p.Labels
	p.Labels = nil
	if !reflect.DeepEqual(p, wantPreview) {
		t.Errorf("overlay preview = %+v\nwant %+v", p, wantPreview)
	}
	if len(labels) != 7 || labels[0] != (Label{Name: "base_e", ObjectCount: 1, ImageCount: 1}) {
		t.Errorf("overlay preview labels = %+v", labels)
	}
	p, err = e.svc.PreviewExport(ctx, d.ID, VersionSelection{VersionIDs: []int64{v10.ID, v11.ID}})
	if err != nil || p.ImageCount != 4 || p.FromVersions != 4 || p.EmptyCount != 1 || p.AnnotatedCount != 3 ||
		p.VersionImageCount != 6 || p.MissingCount != 1 || p.ChangedCount != 1 || len(p.Labels) != 3 {
		t.Errorf("1.0+1.1 preview = %+v, %v", p, err)
	}
	if p, err := e.svc.PreviewExport(ctx, d.ID, VersionSelection{IncludeBase: true}); err != nil || p.ImageCount != 8 ||
		p.FromVersions != 0 || p.AnnotatedCount != 5 || p.VersionImageCount != 0 || p.Labels[0].Name != "base_a" {
		t.Errorf("base preview = %+v, %v", p, err)
	}

	// Selections that cannot be downloaded.
	other, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})
	for _, tt := range []struct {
		sel  *VersionSelection
		kind error
		msg  string
	}{
		{selection(false), ErrInvalid, "请至少选择一个"},
		{&VersionSelection{VersionIDs: []int64{9999}}, ErrNotFound, "所选的版本不存在"},
		{selection(false, onlyApproved), ErrConflict, "所选版本中没有可以导出的图片"},
	} {
		_, err := e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC, Versions: tt.sel})
		wantKind(t, err, tt.kind, tt.msg)
	}
	_, err = e.svc.CreateExport(ctx, other.ID, ExportInput{Format: FormatVOC, Versions: selection(true, v10)})
	wantKind(t, err, ErrNotFound, "所选的版本不存在")
	many := &VersionSelection{IncludeBase: true}
	for i := range maxSelectedVersions + 1 {
		many.VersionIDs = append(many.VersionIDs, int64(i+1))
	}
	_, err = e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC, Versions: many})
	wantKind(t, err, ErrInvalid, "最多选择")
	_, err = e.svc.PreviewExport(ctx, 9999, VersionSelection{IncludeBase: true})
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// A version deleted after the download was prepared fails it.
	ticket, err := e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC, Versions: selection(true, v12)})
	if err != nil {
		t.Fatal(err)
	}
	ticket2, err := e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC, Versions: selection(true, v11)})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := e.svc.OpenExport(ctx, d.ID, ticket2.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if err := e.svc.DeleteVersion(ctx, d.ID, v12.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.OpenExport(ctx, d.ID, ticket.Token)
	wantKind(t, err, ErrNotFound, "所选的版本已被删除")
	// ... also while it is written.
	if err := e.svc.DeleteVersion(ctx, d.ID, v11.ID); err != nil {
		t.Fatal(err)
	}
	if err := ex.Write(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "deleted during the download") {
		t.Errorf("Write after deleting its version: %v", err)
	}
}

func TestExportTop(t *testing.T) {
	versions := func(n int) []Version {
		var out []Version
		for i := n - 1; i >= 0; i-- {
			out = append(out, Version{Name: VersionName(1, i)})
		}
		return out
	}
	for _, tt := range []struct {
		dir  string
		sv   selectedVersions
		want string
	}{
		{"ds", selectedVersions{includeBase: true, versions: versions(2)}, "ds-基础+审图1.0+审图1.1"},
		{"ds", selectedVersions{versions: versions(1)}, "ds-审图1.0"},
		{"ds", selectedVersions{versions: versions(4)}, "ds-审图1.0+审图1.1+审图1.2+审图1.3"},
		{"ds", selectedVersions{versions: versions(5)}, "ds-审图1.0…审图1.4（5个版本）"},
		{"ds", selectedVersions{includeBase: true, versions: versions(12)}, "ds-基础+审图1.0…审图1.11（12个版本）"},
		{strings.Repeat("长", 55), selectedVersions{includeBase: true, versions: versions(3)},
			strings.Repeat("长", 55) + "-基础+3个审图版本"},
	} {
		if got := exportTop(tt.dir, tt.sv); got != tt.want {
			t.Errorf("exportTop(%q, %d versions) = %q, want %q", tt.dir, len(tt.sv.versions), got, tt.want)
		}
	}
	// A long folder name is cut at a character boundary.
	got := exportTop(strings.Repeat("长", 66)+"x", selectedVersions{includeBase: true, versions: versions(3)})
	if len(got) > maxExportTopBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "长-基础+3个审图版本") {
		t.Errorf("long folder name: %q (%d bytes)", got, len(got))
	}
}
