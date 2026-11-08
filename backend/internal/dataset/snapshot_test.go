package dataset

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// snapshotFixture is a dataset with review versions over its base, like
// TestIntegrationVersionExport's.
type snapshotFixture struct {
	e                       *testEnv
	d                       Dataset
	v10, v11, v12, approved Version
}

func newSnapshotFixture(t *testing.T) snapshotFixture {
	t.Helper()
	e := newTestEnv(t, true)
	ctx := context.Background()
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "ds"})
	if err != nil {
		t.Fatal(err)
	}
	img := pngFile(t, 64, 48)
	writeFiles(t, d.Path, map[string][]byte{
		"a.png": img, "a.xml": vocFile("a.png", "base_a"),
		"b.png": img, "b.xml": vocFile("b.png", "base_b"),
		"c.png": img,
		"d.png": img, "d.xml": vocFile("d.png", "base_d"),
		"e.png": img, "e.xml": vocFile("e.png"), // empty
		"f.png": img, "f.xml": []byte("<annotation><object>"), // invalid
		"JPEGImages/v.png": img, "Annotations/v.xml": vocFile("v.png", "base_v"),
		"z.png": img,
	})
	scan, _ := e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)
	f := snapshotFixture{e: e, d: d}
	f.v10 = e.addVersion(t, d.ID, "1.0",
		testVersionImage{"a.png", "needs_sample", []string{"v10_a"}},
		testVersionImage{"b.png", "approved", []string{"v10_b"}},
		testVersionImage{"c.png", "needs_sample", []string{"v10_c"}},
		testVersionImage{"z.png", "needs_sample", []string{"v10_z"}})
	f.v11 = e.addVersion(t, d.ID, "1.1",
		testVersionImage{"a.png", "approved", []string{"v11_a", "v11_a2"}},
		testVersionImage{"d.png", "needs_sample", nil},
		testVersionImage{"JPEGImages/v.png", "needs_sample", []string{"v11_v"}})
	f.v12 = e.addVersion(t, d.ID, "1.2", testVersionImage{"b.png", "needs_sample", []string{"v12_b"}})
	f.approved = e.addVersion(t, d.ID, "1.3", testVersionImage{"b.png", "approved", nil})
	// Since the merges: z.png is removed, c.png replaced.
	if err := os.Remove(filepath.Join(d.Path, "z.png")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, d.Path, map[string][]byte{"c.png": pngFile(t, 32, 24)})
	scan, _ = e.svc.StartScan(ctx, d.ID)
	e.waitJob(t, d.ID, scan.ID)
	return f
}

// snapshotPreview counts a snapshot like PreviewExport.
func snapshotPreview(snap *Snapshot) ExportPreview {
	var p ExportPreview
	labels := map[string]*Label{}
	for _, img := range snap.Images {
		p.ImageCount++
		counts := img.BaseLabels
		if img.VersionID != nil {
			p.FromVersions++
			counts = img.VersionLabels
			n := 0
			for _, c := range counts {
				n += c
			}
			if n > 0 {
				p.AnnotatedCount++
			} else {
				p.EmptyCount++
			}
			p.ObjectCount += n
		} else {
			switch img.AnnotationState {
			case annotationAnnotated:
				p.AnnotatedCount++
			case annotationEmpty:
				p.EmptyCount++
			case annotationInvalid:
				p.InvalidCount++
			case annotationNone:
				p.UnannotatedCount++
			}
			for _, c := range counts {
				p.ObjectCount += c
			}
		}
		for name, c := range counts {
			l := labels[name]
			if l == nil {
				l = &Label{Name: name}
				labels[name] = l
			}
			l.ObjectCount += c
			l.ImageCount++
		}
	}
	p.Labels = []Label{}
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		p.Labels = append(p.Labels, *labels[name])
	}
	slices.SortStableFunc(p.Labels, func(a, b Label) int { return b.ObjectCount - a.ObjectCount })
	return p
}

func TestIntegrationSnapshotMatchesPreviewExport(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()
	for _, sel := range []VersionSelection{
		{IncludeBase: true},
		{IncludeBase: true, VersionIDs: []int64{f.v10.ID}},
		{IncludeBase: true, VersionIDs: []int64{f.v12.ID, f.v10.ID, f.v11.ID}},
		{VersionIDs: []int64{f.v10.ID, f.v11.ID}},
		{VersionIDs: []int64{f.v12.ID, f.v11.ID, f.v10.ID}},
		{VersionIDs: []int64{f.approved.ID, f.v12.ID}},
		{VersionIDs: []int64{f.approved.ID}},
	} {
		want, err := f.e.svc.PreviewExport(ctx, f.d.ID, sel)
		if err != nil {
			t.Fatal(err)
		}
		want.VersionImageCount, want.MissingCount, want.ChangedCount = 0, 0, 0
		for _, opts := range []SnapshotOptions{{IndexOnly: true}, {}} {
			snap, err := f.e.svc.OpenSnapshot(ctx, f.d.ID, sel, opts)
			if err != nil {
				t.Fatalf("OpenSnapshot(%+v, %+v): %v", sel, opts, err)
			}
			if got := snapshotPreview(snap); !equalPreview(got, want) {
				t.Errorf("snapshot of %+v (%+v) counts %+v\nwant %+v", sel, opts, got, want)
			}
			if (snap.Root == nil) != opts.IndexOnly {
				t.Errorf("root = %v with %+v", snap.Root, opts)
			}
			for _, img := range snap.Images {
				switch {
				case img.VersionID != nil && opts.IndexOnly && img.VersionObjects != nil:
					t.Errorf("%s: objects loaded with IndexOnly", img.RelPath)
				case img.VersionID != nil && !opts.IndexOnly && img.VersionObjects == nil:
					t.Errorf("%s: objects not loaded", img.RelPath)
				case img.VersionID == nil && img.VersionObjects != nil:
					t.Errorf("%s: objects without a version", img.RelPath)
				case img.SizeBytes <= 0 || img.ModTime.IsZero() || img.ID == 0:
					t.Errorf("%s: file = %+v", img.RelPath, img)
				}
			}
			snap.Close()
		}
	}
}

// equalPreview compares two previews (labels with the same object count
// may come in any order).
func equalPreview(a, b ExportPreview) bool {
	al, bl := a.Labels, b.Labels
	a.Labels, b.Labels = nil, nil
	if !reflect.DeepEqual(a, b) || len(al) != len(bl) {
		return false
	}
	sortLabels := func(l []Label) []Label {
		return slices.SortedFunc(slices.Values(l), func(x, y Label) int { return strings.Compare(x.Name, y.Name) })
	}
	return slices.Equal(sortLabels(al), sortLabels(bl))
}

func TestIntegrationSnapshotContent(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()
	snap, err := f.e.svc.OpenSnapshot(ctx, f.d.ID, VersionSelection{IncludeBase: true,
		VersionIDs: []int64{f.v10.ID, f.v11.ID, f.v12.ID}}, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if snap.Dataset.ID != f.d.ID || snap.Dataset.Name != "ds" || len(snap.Versions) != 3 ||
		snap.Versions[0].ID != f.v12.ID || snap.Versions[2].ID != f.v10.ID {
		t.Errorf("snapshot = %+v / %+v", snap.Dataset, snap.Versions)
	}
	var paths []string
	byPath := map[string]SnapshotImage{}
	for _, img := range snap.Images {
		paths = append(paths, img.RelPath)
		byPath[img.RelPath] = img
	}
	// Folders first, then names in Explorer order.
	if want := []string{"JPEGImages/v.png", "a.png", "b.png", "c.png", "d.png", "e.png", "f.png"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	a := byPath["a.png"]
	if a.VersionID == nil || *a.VersionID != f.v11.ID || a.ReviewStatus != "approved" || len(a.VersionObjects) != 2 ||
		a.VersionObjects[0].Name != "v11_a" || a.VersionLabels["v11_a2"] != 1 || a.BaseLabels["base_a"] != 1 ||
		a.AnnotationPath == nil || *a.AnnotationPath != "a.xml" || a.AnnotationState != annotationAnnotated ||
		*a.Width != 64 || a.SizeBytes != int64(len(pngFile(t, 64, 48))) {
		t.Errorf("a.png = %+v", a)
	}
	// c.png was replaced since 1.0: the base applies.
	if c := byPath["c.png"]; c.VersionID != nil || c.AnnotationState != annotationNone || *c.Width != 32 {
		t.Errorf("c.png = %+v", c)
	}
	if d := byPath["d.png"]; d.VersionID == nil || d.VersionObjects == nil || len(d.VersionObjects) != 0 ||
		d.ReviewStatus != "needs_sample" {
		t.Errorf("d.png = %+v", d)
	}
	// Reading the files through the root.
	if fh, err := snap.Root.Open("a.png"); err != nil {
		t.Errorf("open through root: %v", err)
	} else {
		fh.Close()
	}
}

func TestIntegrationSnapshotErrors(t *testing.T) {
	f := newSnapshotFixture(t)
	e, ctx := f.e, context.Background()
	open := func(sel VersionSelection, opts SnapshotOptions) error {
		snap, err := e.svc.OpenSnapshot(ctx, f.d.ID, sel, opts)
		snap.Close()
		return err
	}
	wantKind(t, open(VersionSelection{}, SnapshotOptions{}), ErrInvalid, "请至少选择一个标注来源")
	wantKind(t, open(VersionSelection{VersionIDs: []int64{1 << 40}}, SnapshotOptions{}), ErrNotFound, "所选审图版本不存在（可能已被删除）")

	// A version of another dataset.
	other, _ := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "other"})
	if _, err := e.svc.OpenSnapshot(ctx, other.ID, VersionSelection{VersionIDs: []int64{f.v10.ID}},
		SnapshotOptions{AllowTrashedVersions: true}); err == nil {
		t.Error("a version of another dataset was accepted")
	} else {
		wantKind(t, err, ErrNotFound, "所选审图版本不存在")
	}

	// A version being built.
	mergeID, reviewID := e.addMerge(t, f.d.ID)
	building, err := e.svc.CreateReviewVersion(ctx, ReviewVersionDraft{DatasetID: f.d.ID, ReviewID: reviewID,
		MergeID: mergeID, Note: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := open(VersionSelection{VersionIDs: []int64{building}}, SnapshotOptions{AllowTrashedVersions: true}); err != errVersionBuilding {
		t.Errorf("building version: %v", err)
	}

	// A version in the recycle bin: only with AllowTrashedVersions; its
	// objects are still read.
	if err := e.svc.DeleteVersion(ctx, f.d.ID, f.v11.ID); err != nil {
		t.Fatal(err)
	}
	sel := VersionSelection{IncludeBase: true, VersionIDs: []int64{f.v11.ID}}
	wantKind(t, open(sel, SnapshotOptions{}), ErrNotFound, "所选审图版本不存在（可能已被删除）")
	wantKind(t, open(sel, SnapshotOptions{IndexOnly: true}), ErrNotFound, "所选审图版本不存在（可能已被删除）")
	snap, err := e.svc.OpenSnapshot(ctx, f.d.ID, sel, SnapshotOptions{AllowTrashedVersions: true})
	if err != nil {
		t.Fatalf("trashed version with AllowTrashedVersions: %v", err)
	}
	from := 0
	for _, img := range snap.Images {
		if img.VersionID != nil && img.VersionObjects != nil {
			from++
		}
	}
	snap.Close()
	if from != 3 || len(snap.Versions) != 1 || snap.Versions[0].ID != f.v11.ID {
		t.Errorf("trashed snapshot: %d images from the version, versions %+v", from, snap.Versions)
	}

	// A purged version is gone; purged after the snapshot transaction (while
	// the objects are loaded), it is named.
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_versions WHERE id = $1", f.v10.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, open(VersionSelection{IncludeBase: true, VersionIDs: []int64{f.v10.ID}}, SnapshotOptions{AllowTrashedVersions: true}),
		ErrNotFound, "所选审图版本不存在")
	snap = &Snapshot{}
	if err := pgx.BeginFunc(ctx, e.db.Pool, func(tx pgx.Tx) error {
		snap.Versions, err = snapshotVersions(ctx, tx, f.d.ID, VersionSelection{VersionIDs: []int64{f.v12.ID}}, false)
		if err != nil {
			return err
		}
		snap.Images, err = querySnapshotImages(ctx, tx, f.d.ID, []int64{f.v12.ID}, false)
		return err
	}); err != nil || len(snap.Images) != 1 {
		t.Fatalf("snapshot of 1.2: %+v, %v", snap.Images, err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_versions WHERE id = $1", f.v12.ID); err != nil {
		t.Fatal(err)
	}
	wantKind(t, e.svc.loadSnapshotObjects(ctx, snap), ErrNotFound, "所选审图版本“审图1.2”已被永久删除")

	// The folder is missing: the error names the dataset, not the path.
	moved := f.d.Path + "-moved"
	if err := os.Rename(f.d.Path, moved); err != nil {
		t.Fatal(err)
	}
	err = open(VersionSelection{IncludeBase: true}, SnapshotOptions{})
	wantKind(t, err, ErrConflict, "数据集“ds”的文件夹不存在")
	if err != nil && strings.Contains(err.Error(), e.datasetsDir()) {
		t.Errorf("message leaks the path: %v", err)
	}
	if err := open(VersionSelection{IncludeBase: true}, SnapshotOptions{IndexOnly: true}); err != nil {
		t.Errorf("index only without the folder: %v", err)
	}
	if err := os.Rename(moved, f.d.Path); err != nil {
		t.Fatal(err)
	}

	// A dataset in the recycle bin, or unknown.
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_versions WHERE id = $1", building); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteDataset(ctx, f.d.ID, false); err != nil {
		t.Fatal(err)
	}
	wantKind(t, open(VersionSelection{IncludeBase: true}, SnapshotOptions{IndexOnly: true}), ErrNotFound, "数据集不存在")
	_, err = e.svc.OpenSnapshot(ctx, 1<<40, VersionSelection{IncludeBase: true}, SnapshotOptions{})
	wantKind(t, err, ErrNotFound, "数据集不存在")
}
