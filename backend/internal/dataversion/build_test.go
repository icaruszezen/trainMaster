package dataversion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// coreLineFiles is a dataset of n annotated images (scratch), plus a
// negative, an unannotated image and one with an unlisted label.
func coreLineFiles(t *testing.T, n int) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for i := range n {
		coreLabeled(t, files, fmt.Sprintf("OK/p%02d.png", i), i, "scratch")
	}
	coreLabeled(t, files, "NG/dent.png", 100, "dent", "scratch")
	coreLabeled(t, files, "NG/empty.png", 101)
	files["NG/none.png"] = corePNG(t, 32, 24, 102)
	coreLabeled(t, files, "NG/dirt.png", 103, "dirt")
	return files
}

func TestCoreBuildLayout(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "line_a", coreLineFiles(t, 20))
	p := e.coreProject(t, "外观检测", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	v, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusQueued || v.Version != 1 || v.QueuePosition == nil || *v.QueuePosition != 1 || v.Progress != nil ||
		v.Note != "第一版" || v.ProjectName != "外观检测" || len(v.Sources) != 1 || v.Sources[0].DirName != "line_a" ||
		v.Stats.Labels == nil || v.ClassVersion != in.ClassVersion {
		t.Errorf("created = %+v", v)
	}
	e.coreBuildAll(t)
	v, err = e.svc.Get(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady || v.Error != "" || v.FinishedAt == nil || v.StartedAt == nil || v.Progress != nil ||
		v.QueuePosition != nil {
		t.Fatalf("built = %+v", v)
	}
	// 20 + dent + empty taken; none (unannotated) and dirt (unlisted) skipped.
	if v.ImageCount != 22 || v.ObjectCount != 22 || v.TrainCount+v.ValCount != 22 || v.TestCount != 0 || v.ValCount == 0 ||
		v.AugmentedCount != 0 || v.Sources[0].ImageCount != 22 || v.TotalBytes <= 0 {
		t.Errorf("counts = %+v", v)
	}
	st := v.Stats
	if st.Skipped != (Skipped{Unannotated: 1, Unlisted: 1}) || st.Storage.Linked != 22 || st.Storage.Copied != 0 ||
		st.Negatives.Train+st.Negatives.Val != 1 || st.Compare != nil || len(st.Labels) != 2 ||
		st.Labels[0].Name != "scratch" || st.Labels[1].Name != "dent" ||
		st.Labels[0].Train.Objects+st.Labels[0].Val.Objects != 21 || st.Labels[1].Train.Images+st.Labels[1].Val.Images != 1 {
		t.Errorf("stats = %+v", st)
	}

	dir := e.coreDir(t, v)
	if _, err := os.Lstat(filepath.Join(dir, pendingMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".pending left: %v", err)
	}
	yaml := coreReadFile(t, dir, "data.yaml")
	if !strings.Contains(yaml, "train: train.txt\nval: val.txt\n") || strings.Contains(yaml, "test:") ||
		strings.Contains(yaml, "path:") || !strings.Contains(yaml, "nc: 2\nnames:\n  0: \"scratch\"\n  1: \"dent\"\n") {
		t.Errorf("data.yaml =\n%s", yaml)
	}
	if got := coreReadFile(t, dir, "classes.txt"); got != "scratch\ndent\n" {
		t.Errorf("classes.txt = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "test.txt")); err == nil {
		t.Error("test.txt without test images")
	}
	var lines []string
	for _, split := range []string{"train", "val"} {
		for _, l := range strings.Split(strings.TrimSuffix(coreReadFile(t, dir, split+".txt"), "\n"), "\n") {
			if !strings.HasPrefix(l, "./images/line_a/") {
				t.Errorf("%s.txt line %q", split, l)
			}
			lines = append(lines, l)
		}
	}
	if len(lines) != 22 {
		t.Errorf("list lines = %d", len(lines))
	}

	rows := e.coreRows(t, v.ID)
	if len(rows) != 22 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, r := range rows {
		vf := filepath.Join(dir, filepath.FromSlash(r.Path))
		df := filepath.Join(d.Path, filepath.FromSlash(r.SourcePath))
		fi, err1 := os.Stat(vf)
		di, err2 := os.Stat(df)
		if err1 != nil || err2 != nil || !os.SameFile(fi, di) || r.Stored != StoredLink || r.SizeBytes != di.Size() {
			t.Errorf("%s: not linked to the dataset file (%v %v) %+v", r.Path, err1, err2, r)
		}
		if r.Path != "images/line_a/"+r.SourcePath || r.Width != 32 || r.Height != 24 || r.AnnotationSource != nil ||
			r.AugmentedFrom != nil || r.Augment == nil || len(r.Augment) != 0 || r.SplitBy != SplitByRatio {
			t.Errorf("row = %+v", r)
		}
		label := coreReadFile(t, dir, dataset.UltralyticsLabelPath(r.Path))
		if !strings.HasPrefix(dataset.UltralyticsLabelPath(r.Path), "labels/line_a/") || strings.Count(label, "\n") != r.ObjectCount {
			t.Errorf("%s: label %q", r.Path, label)
		}
		if !slices.Contains(lines, "./"+r.Path) {
			t.Errorf("%s not listed", r.Path)
		}
	}
	// The box of OK/p00 (2,2)-(12,12) in a 32 x 24 image.
	if got := coreReadFile(t, dir, "labels/line_a/OK/p00.txt"); got != "0 0.218750 0.291667 0.312500 0.416667\n" {
		t.Errorf("p00 label = %q", got)
	}
	if got := coreReadFile(t, dir, "labels/line_a/NG/empty.txt"); got != "" {
		t.Errorf("negative label = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "labels/line_a/NG/empty.txt")); err != nil {
		t.Errorf("negative label file: %v", err)
	}

	var m manifest
	if err := json.Unmarshal([]byte(coreReadFile(t, dir, "version.json")), &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != "trainmaster-data-version" || m.FormatVersion != 1 || m.Project.Name != "外观检测" || m.Version != 1 ||
		m.Note != "第一版" || len(m.Classes) != 2 || m.Counts.Images != 22 || m.Sources[0].ImageCount != 22 ||
		m.FinishedAt == nil || !m.FinishedAt.Equal(*v.FinishedAt) || m.Config.Split.Train != 80 {
		t.Errorf("version.json = %+v", m)
	}

	// Editing the note rewrites version.json.
	note := " 新的备注 "
	if v2, err := e.svc.Update(ctx, v.ID, UpdateInput{Note: &note}); err != nil || v2.Note != "新的备注" {
		t.Errorf("Update = %+v, %v", v2, err)
	}
	if err := json.Unmarshal([]byte(coreReadFile(t, dir, "version.json")), &m); err != nil || m.Note != "新的备注" ||
		m.Counts.Images != 22 {
		t.Errorf("version.json after the edit = %+v, %v", m, err)
	}
	empty := " "
	_, err = e.svc.Update(ctx, v.ID, UpdateInput{Note: &empty})
	coreWantKind(t, err, ErrInvalid, "请填写版本备注")

	if list, err := e.svc.List(ctx, ListFilter{ProjectID: p.ID}); err != nil || len(list) != 1 || list[0].ID != v.ID {
		t.Errorf("List = %+v, %v", list, err)
	}
	if list, err := e.svc.List(ctx, ListFilter{ProjectID: p.ID + 1}); err != nil || list == nil || len(list) != 0 {
		t.Errorf("List of another project = %+v, %v", list, err)
	}
}

func TestCoreBuildReviewVersions(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	files := coreLineFiles(t, 10)
	d := e.coreDataset(t, "ds", files)
	// The review version relabels p00 (dent) and marks NG/none (no base
	// annotation) needs_sample with a scratch.
	rv := e.coreReviewVersion(t, d.ID, coreVersionImage{"OK/p00.png", "approved", []string{"dent", "dent"}},
		coreVersionImage{"NG/none.png", "needs_sample", []string{"scratch"}})
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), SourceInput{DatasetID: d.ID, IncludeBase: true, VersionIDs: []int64{rv.ID}})
	v := e.coreCreateBuilt(t, in)
	if v.ImageCount != 13 || v.Sources[0].Versions[0].Name != rv.Name {
		t.Errorf("version = %+v", v)
	}
	byPath := map[string]coreImageRow{}
	for _, r := range e.coreRows(t, v.ID) {
		byPath[r.SourcePath] = r
	}
	if r := byPath["OK/p00.png"]; r.AnnotationSource == nil || *r.AnnotationSource != rv.ID || r.ObjectCount != 2 ||
		r.Objects[0].Name != "dent" {
		t.Errorf("p00 = %+v", r)
	}
	if r := byPath["NG/none.png"]; r.AnnotationSource == nil || r.ObjectCount != 1 {
		t.Errorf("none = %+v", r)
	}
	if r := byPath["OK/p01.png"]; r.AnnotationSource != nil || r.ObjectCount != 1 || r.Objects[0].Name != "scratch" {
		t.Errorf("p01 = %+v", r)
	}

	// Without the base: only the needs_sample images.
	in.Sources[0].IncludeBase = false
	in.Split = SplitConfig{Train: 50, Val: 50, Seed: 1, GroupBy: GroupByStem}
	if _, err := e.svc.Create(ctx, in); err == nil {
		t.Error("a version of one image was accepted")
	} else {
		coreWantKind(t, err, ErrConflict, "没有图片")
	}
}

func TestCoreBuildFileChecks(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 10))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	// After the scan: one file rewritten (another size), one removed.
	coreWriteFiles(t, d.Path, map[string][]byte{"OK/p01.png": corePNG(t, 40, 30, 1)})
	if err := os.Remove(filepath.Join(d.Path, "OK/p02.png")); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	v, _ = e.svc.Get(ctx, v.ID)
	if v.Status != StatusReady || v.Stats.Skipped.Changed != 1 || v.Stats.Skipped.Missing != 1 || v.ImageCount != 10 {
		t.Errorf("version = %s %q, skipped %+v, images %d", v.Status, v.Error, v.Stats.Skipped, v.ImageCount)
	}
}

func TestCoreBuildCopyFallback(t *testing.T) {
	for name, link := range map[string]func(string, string) error{
		"EXDEV": func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} },
		"EPERM": func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} },
		// A link that is not the opened file (swapped meanwhile).
		"other file": func(_, newname string) error { return os.WriteFile(newname, []byte("x"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			e := coreNewEnv(t)
			e.svc.link = link
			d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
			p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
			v := e.coreCreateBuilt(t, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
			if s := v.Stats.Storage; s.Linked != 0 || s.Copied != 8 || s.CopiedBytes <= 0 {
				t.Errorf("storage = %+v", s)
			}
			dir := e.coreDir(t, v)
			for _, r := range e.coreRows(t, v.ID) {
				want, _ := os.ReadFile(filepath.Join(d.Path, filepath.FromSlash(r.SourcePath)))
				vi, _ := os.Stat(filepath.Join(dir, filepath.FromSlash(r.Path)))
				di, _ := os.Stat(filepath.Join(d.Path, filepath.FromSlash(r.SourcePath)))
				if got := coreReadFile(t, dir, r.Path); got != string(want) || r.Stored != StoredCopy || os.SameFile(vi, di) {
					t.Errorf("%s: copy of %d bytes, stored %s", r.Path, len(got), r.Stored)
				}
			}
			if entries, _ := filepath.Glob(filepath.Join(dir, "images/ds/OK/.copy-*")); len(entries) != 0 {
				t.Errorf("temporary files left: %v", entries)
			}
		})
	}
}

func TestCoreBuildUniqueNames(t *testing.T) {
	e := coreNewEnv(t)
	files := coreLineFiles(t, 10)
	// a.bmp and A.png share a stem; images/labels/x and labels/images/x
	// share a label path (Ultralytics replaces the last images folder).
	files["a.bmp"] = coreBMP(32, 24)
	files["a.xml"] = coreVOC("a.bmp", 32, 24, coreBox{"scratch", 1, 1, 5, 5})
	coreLabeled(t, files, "A.png", 200, "scratch")
	coreLabeled(t, files, "images/labels/x.png", 201, "scratch")
	coreLabeled(t, files, "labels/images/x.png", 202, "scratch")
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v := e.coreCreateBuilt(t, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	paths := map[string]string{}
	for _, r := range e.coreRows(t, v.ID) {
		paths[r.SourcePath] = r.Path
	}
	// Image order: folders first, then names (a.bmp before A.png).
	want := map[string]string{
		"images/labels/x.png": "images/ds/images/labels/x.png",
		"labels/images/x.png": "images/ds/labels/images/x~2.png",
		"a.bmp":               "images/ds/a.bmp",
		"A.png":               "images/ds/A~2.png",
	}
	for src, p := range want {
		if paths[src] != p {
			t.Errorf("%s stored at %q, want %q", src, paths[src], p)
		}
	}
	dir := e.coreDir(t, v)
	labels := map[string]bool{}
	for src := range want {
		l := strings.ToLower(dataset.UltralyticsLabelPath(paths[src]))
		if labels[l] {
			t.Errorf("label %s twice", l)
		}
		labels[l] = true
		if coreReadFile(t, dir, dataset.UltralyticsLabelPath(paths[src])) == "" {
			t.Errorf("no label for %s", paths[src])
		}
	}
}

func TestCoreBuildSourceChanges(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 10))
	d2 := e.coreDataset(t, "ds2", coreLineFiles(t, 4))
	rv := e.coreReviewVersion(t, d2.ID, coreVersionImage{"OK/p00.png", "needs_sample", []string{"dent"}})
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID, d2.ID)
	cv := e.coreClassVersion(t, p.ID)
	sources := []SourceInput{{DatasetID: d.ID, IncludeBase: true}, {DatasetID: d2.ID, IncludeBase: true, VersionIDs: []int64{rv.ID}}}

	// The dataset moved to the recycle bin while queued.
	v1, err := e.svc.Create(ctx, coreInput(p.ID, cv, sources...))
	if err != nil {
		t.Fatal(err)
	}
	// The review version moved to the bin while queued: still built.
	v2, err := e.svc.Create(ctx, coreInput(p.ID, cv, sources[1]))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.datasets.DeleteVersion(ctx, d2.ID, rv.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.datasets.DeleteDataset(ctx, d.ID, true); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v1.ID); v.Status != StatusFailed || v.Error != "数据集“ds”已被删除（在回收站中），恢复后可以重新生成" ||
		v.FinishedAt == nil || v.Progress != nil || !v.Sources[0].DatasetDeleted || v.Sources[1].DatasetDeleted {
		t.Errorf("v1 = %+v", v)
	}
	if _, err := os.Lstat(e.svc.versionDir(versionDirName(p.ID, v1.Version))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder of the failed version: %v", err)
	}
	v, _ := e.svc.Get(ctx, v2.ID)
	if v.Status != StatusReady {
		t.Fatalf("v2 = %s %s", v.Status, v.Error)
	}
	found := false
	for _, r := range e.coreRows(t, v.ID) {
		if r.SourcePath == "OK/p00.png" {
			found = true
			if r.AnnotationSource == nil || *r.AnnotationSource != rv.ID || len(r.Objects) != 1 || r.Objects[0].Name != "dent" {
				t.Errorf("OK/p00.png row = %+v", r)
			}
		}
	}
	if !found {
		t.Error("OK/p00.png not in v2")
	}
	// Retry refuses the trashed dataset; the trashed review version too.
	_, err = e.svc.Retry(ctx, v1.ID)
	coreWantKind(t, err, ErrConflict, "数据集“ds”已被删除（在回收站中）")

	// The selected review version purged while queued.
	v3, err := e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d2.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE data_versions SET sources = jsonb_set(sources, '{0,versions}', $2) WHERE id = $1",
		v3.ID, []SourceVersion{{ID: rv.ID, Name: rv.Name}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM dataset_versions WHERE id = $1", rv.ID); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v3.ID); v.Status != StatusFailed ||
		v.Error != "所选审图版本“"+rv.Name+"”已被永久删除，无法重新生成，请新建数据版本" {
		t.Errorf("v3 = %s %q", v.Status, v.Error)
	}

	// The dataset removed from the project while queued.
	v4, err := e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d2.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.projects.RemoveDataset(ctx, p.ID, d2.ID); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v4.ID); v.Status != StatusFailed || v.Error != "数据集“ds2”已经不在项目中，无法重新生成，请新建数据版本" {
		t.Errorf("v4 = %s %q", v.Status, v.Error)
	}
	_, err = e.svc.Retry(ctx, v4.ID)
	coreWantKind(t, err, ErrConflict, "已经不在项目中")
}

func TestCoreBuildMissingFolder(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(d.Path, d.Path+"-moved"); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v.ID); v.Status != StatusFailed || v.Error != "数据集“ds”的文件夹不存在" {
		t.Errorf("version = %s %q", v.Status, v.Error)
	}
	// Back in place: a retry builds it.
	if err := os.Rename(d.Path+"-moved", d.Path); err != nil {
		t.Fatal(err)
	}
	if v, err := e.svc.Retry(ctx, v.ID); err != nil || v.Status != StatusQueued || v.Error != "" || v.FinishedAt != nil ||
		v.StartedAt != nil {
		t.Errorf("Retry = %+v, %v", v, err)
	}
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v.ID); v.Status != StatusReady || v.ImageCount != 8 {
		t.Errorf("retried = %s %q %d", v.Status, v.Error, v.ImageCount)
	}
	_, err = e.svc.Retry(ctx, v.ID)
	coreWantKind(t, err, ErrConflict, "只有生成失败的数据版本可以重新生成")
}

func TestCoreBuildFolderOwnership(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	cv := e.coreClassVersion(t, p.ID)

	// Create skips the numbers whose folder exists.
	for _, n := range []int{1, 2} {
		coreWriteFiles(t, e.svc.versionDir(versionDirName(p.ID, n)), map[string][]byte{"version.json": []byte("{}")})
	}
	v, err := e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	if err != nil || v.Version != 3 {
		t.Fatalf("Create = %+v, %v", v, err)
	}
	// A folder appearing before the build without .pending is not ours.
	dir := e.svc.versionDir(versionDirName(p.ID, 3))
	coreWriteFiles(t, dir, map[string][]byte{"keep.txt": []byte("backup")})
	e.coreBuildAll(t)
	v, _ = e.svc.Get(ctx, v.ID)
	if v.Status != StatusFailed || !strings.Contains(v.Error, "数据版本文件夹 exports/data-versions/project-") ||
		!strings.Contains(v.Error, "已经存在（可能来自备份），请先移走它") {
		t.Errorf("version = %s %q", v.Status, v.Error)
	}
	if coreReadFile(t, dir, "keep.txt") != "backup" {
		t.Error("the foreign folder was touched")
	}
	// Deleting the failed version leaves it alone too.
	if err := e.svc.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.WaitBackground()
	if coreReadFile(t, dir, "keep.txt") != "backup" {
		t.Error("deleting the failed version removed the foreign folder")
	}
	if _, err := e.svc.Get(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete: %v", err)
	}

	// A folder with .pending (an interrupted build) is cleared.
	v, err = e.svc.Create(ctx, coreInput(p.ID, cv, coreBase(d.ID)...))
	if err != nil || v.Version != 4 {
		t.Fatalf("Create = %+v, %v", v, err)
	}
	dir = e.svc.versionDir(versionDirName(p.ID, 4))
	coreWriteFiles(t, dir, map[string][]byte{pendingMarker: nil, "images/ds/OK/p00.png": []byte("old")})
	e.coreBuildAll(t)
	if v, _ := e.svc.Get(ctx, v.ID); v.Status != StatusReady {
		t.Fatalf("version = %s %q", v.Status, v.Error)
	}
	if got := coreReadFile(t, dir, "images/ds/OK/p00.png"); got == "old" || got == "" {
		t.Errorf("old file kept: %q", got)
	}
}

func TestCoreBuildZeroVal(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	files := map[string][]byte{}
	coreLabeled(t, files, "a.png", 1, "scratch")
	coreLabeled(t, files, "b.png", 2, "scratch")
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	// One of the two files changes after the preview: the build has one
	// image left, all in one split.
	coreWriteFiles(t, d.Path, map[string][]byte{"b.png": corePNG(t, 40, 30, 9)})
	e.coreBuildAll(t)
	v, _ = e.svc.Get(ctx, v.ID)
	if v.Status != StatusFailed || (!strings.HasPrefix(v.Error, "划分后验证集没有图片") && !strings.HasPrefix(v.Error, "划分后训练集没有图片")) {
		t.Errorf("version = %s %q", v.Status, v.Error)
	}
	if rows := e.coreRows(t, v.ID); len(rows) != 0 {
		t.Errorf("rows of a failed version: %d", len(rows))
	}
	if _, err := os.Lstat(e.coreDir(t, v)); !errors.Is(err, os.ErrNotExist) {
		e.svc.WaitBackground()
		if _, err := os.Lstat(e.coreDir(t, v)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("folder of the failed version: %v", err)
		}
	}
}

func TestCoreBuildDiskFull(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	e.svc.link = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }
	e.svc.usage = func() (storage.Usage, error) { return storage.Usage{TotalBytes: 10 << 30, FreeBytes: 512 << 20}, nil }
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	b := e.coreBuildOne(t)
	if b.err == nil || !errors.Is(b.err, ErrInsufficientStorage) {
		t.Errorf("build error = %v", b.err)
	}
	v, _ = e.svc.Get(ctx, v.ID)
	if v.Status != StatusFailed || !strings.HasPrefix(v.Error, "磁盘空间不足：生成数据版本还需要约") {
		t.Errorf("version = %s %q", v.Status, v.Error)
	}
	// Unsupported: no check.
	e.svc.usage = func() (storage.Usage, error) { return storage.Usage{}, storage.ErrUsageUnsupported }
	if _, err := e.svc.Retry(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if v, _ = e.svc.Get(ctx, v.ID); v.Status != StatusReady || v.Stats.Storage.Copied != 8 {
		t.Errorf("without usage = %s %q %+v", v.Status, v.Error, v.Stats.Storage)
	}
}

// coreBuilt is the outcome of one build run by coreBuildOne.
type coreBuilt struct {
	id  int64
	err error
}

// coreBuildOne claims and builds one version, returning the build error.
func (e *coreEnv) coreBuildOne(t *testing.T) coreBuilt {
	t.Helper()
	ctx := context.Background()
	job, err := e.svc.claim(ctx)
	if err != nil || job == nil {
		t.Fatalf("claim = %v, %v", job, err)
	}
	b := newBuild(e.svc, *job, e.svc.log)
	berr := b.runSafe(ctx)
	if berr != nil {
		b.fail(berr, 0)
	}
	return coreBuilt{id: job.ID, err: berr}
}

func TestCoreDeleteWhileBuilding(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.svc.buildHook = func(id int64, stage string) {
		if stage == StageLinking {
			once.Do(func() { close(reached) })
			<-release
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := e.svc.buildNext(ctx); err != nil {
			t.Errorf("buildNext: %v", err)
		}
	}()
	<-reached
	if got, _ := e.svc.Get(ctx, v.ID); got.Status != StatusBuilding || got.Progress == nil || got.Progress.Stage != StageLinking {
		t.Errorf("building = %+v", got)
	}
	dir := e.coreDir(t, v)
	if !hasPending(dir) {
		t.Error("no .pending while building")
	}
	if err := e.svc.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	e.svc.WaitBackground()
	if _, err := e.svc.Get(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder left: %v", err)
	}
	if entries, _ := os.ReadDir(e.svc.rootDir()); len(entries) != 1 || entries[0].Name() != "project-"+fmt.Sprint(p.ID) {
		t.Errorf("data-versions = %v", entries)
	}
	var n int
	e.db.QueryRow(ctx, "SELECT count(*) FROM data_version_images").Scan(&n)
	if n != 0 {
		t.Errorf("%d image rows left", n)
	}
	if e.svc.builder.running != 0 || len(e.svc.builder.pending) != 0 {
		t.Errorf("builder running %d, pending %v", e.svc.builder.running, e.svc.builder.pending)
	}
}

func TestCoreDeleteBetweenClaimAndRegistration(t *testing.T) {
	for _, forget := range []bool{false, true} {
		t.Run(fmt.Sprintf("forget pending %v", forget), func(t *testing.T) {
			e := coreNewEnv(t)
			ctx := context.Background()
			d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
			p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
			v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
			if err != nil {
				t.Fatal(err)
			}
			job, err := e.svc.claim(ctx)
			if err != nil || job == nil || job.ID != v.ID {
				t.Fatalf("claim = %+v, %v", job, err)
			}
			if err := e.svc.Delete(ctx, v.ID); err != nil {
				t.Fatal(err)
			}
			if forget {
				// Without the pending cancel, the first guarded write stops it.
				clear(e.svc.builder.pending)
			}
			e.svc.runBuild(ctx, *job)
			e.svc.WaitBackground()
			if _, err := os.Lstat(e.coreDir(t, v)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("folder: %v", err)
			}
			var n int
			e.db.QueryRow(ctx, "SELECT count(*) FROM data_versions").Scan(&n)
			if n != 0 {
				t.Errorf("%d versions", n)
			}
		})
	}
}

func TestCoreRecoverBuilds(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	// A build stopped by a shutdown after it inserted rows (the hook stops
	// the service context at the augmenting... finishing stage).
	bctx, stop := context.WithCancel(ctx)
	e.svc.buildHook = func(id int64, stage string) {
		if stage == StageFinishing {
			stop()
		}
	}
	if built, err := e.svc.buildNext(bctx); err != nil || !built {
		t.Fatalf("buildNext = %v, %v", built, err)
	}
	e.svc.buildHook = nil
	got, _ := e.svc.Get(ctx, v.ID)
	if got.Status != StatusBuilding || len(e.coreRows(t, v.ID)) != 8 || !hasPending(e.coreDir(t, v)) {
		t.Fatalf("after shutdown = %s, %d rows", got.Status, len(e.coreRows(t, v.ID)))
	}
	if err := e.svc.recoverBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.WaitBackground()
	got, _ = e.svc.Get(ctx, v.ID)
	if got.Status != StatusFailed || got.Error != msgInterrupted || got.FinishedAt == nil || len(e.coreRows(t, v.ID)) != 0 {
		t.Errorf("recovered = %+v", got)
	}
	if _, err := os.Lstat(e.coreDir(t, v)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder: %v", err)
	}
	if _, err := e.svc.Retry(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.coreBuildAll(t)
	if got, _ = e.svc.Get(ctx, v.ID); got.Status != StatusReady || got.ImageCount != 8 {
		t.Errorf("retried = %s %q", got.Status, got.Error)
	}
}

func TestCoreBuildPanic(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	v, err := e.svc.Create(ctx, coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...))
	if err != nil {
		t.Fatal(err)
	}
	e.svc.buildHook = func(id int64, stage string) {
		if stage == StageLinking {
			panic("boom")
		}
	}
	e.coreBuildAll(t)
	if got, _ := e.svc.Get(ctx, v.ID); got.Status != StatusFailed || got.Error != msgInternal {
		t.Errorf("version = %s %q", got.Status, got.Error)
	}
}

func TestCoreCompareVersions(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreLineFiles(t, 30))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	cv := e.coreClassVersion(t, p.ID)
	in := coreInput(p.ID, cv, coreBase(d.ID)...)
	v1 := e.coreCreateBuilt(t, in)

	// v2: two images removed, one added, one relabeled, another split.
	if err := os.Remove(filepath.Join(d.Path, "OK/p00.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d.Path, "OK/p01.png")); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	coreLabeled(t, files, "OK/new.png", 300, "scratch")
	files["OK/p02.xml"] = coreVOC("p02.png", 32, 24, coreBox{"dent", 3, 3, 9, 9})
	coreWriteFiles(t, d.Path, files)
	e.coreScan(t, d.ID)

	pre, err := e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Compare == nil || pre.Compare.ID != v1.ID || pre.Compare.Version != 1 || pre.Compare.Added != 1 ||
		pre.Compare.Removed != 2 || pre.Compare.SplitChanged != 0 || pre.Compare.AnnotationChanged != 0 {
		t.Errorf("preview compare = %+v", pre.Compare)
	}
	in.Split.Val, in.Split.Train = 40, 60 // val grows: some train images move
	pre, err = e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	v2 := e.coreCreateBuilt(t, in)
	c := v2.Stats.Compare
	if c == nil || c.ID != v1.ID || c.Version != 1 || c.Added != 1 || c.Removed != 2 || c.AnnotationChanged != 1 ||
		c.SplitChanged == 0 || c.SplitChanged != pre.Compare.SplitChanged {
		t.Errorf("compare = %+v (preview %+v)", c, pre.Compare)
	}
	// The val set only grows with the same seed.
	split1 := map[string]string{}
	for _, r := range e.coreRows(t, v1.ID) {
		split1[r.SourcePath] = r.Split
	}
	for _, r := range e.coreRows(t, v2.ID) {
		if s, ok := split1[r.SourcePath]; ok && s == SplitVal && r.Split != SplitVal {
			t.Errorf("%s left val", r.SourcePath)
		}
	}
	if v1.Stats.Compare != nil {
		t.Errorf("v1 compare = %+v", v1.Stats.Compare)
	}
}

// coreBMP is a small uncompressed 24-bit BMP of w x h pixels.
func coreBMP(w, h int) []byte {
	row := (w*3 + 3) &^ 3
	size := 54 + row*h
	b := make([]byte, size)
	copy(b, "BM")
	le32 := func(off, v int) {
		b[off], b[off+1], b[off+2], b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	le32(2, size)
	le32(10, 54)
	le32(14, 40)
	le32(18, w)
	le32(22, h)
	b[26], b[28] = 1, 24
	le32(34, row*h)
	for i := 54; i < size; i++ {
		b[i] = byte(i)
	}
	return b
}

var _ = time.Second
