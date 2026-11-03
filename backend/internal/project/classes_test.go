package project

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeClassList(t *testing.T) {
	got, err := NormalizeClassList([]Class{{Name: " 划痕 ", Color: "#EF4444"}, {Name: "dent"}, {Name: "Dent", Color: " #00ff00 "}})
	want := []Class{{Name: "划痕", Color: "#ef4444"}, {Name: "dent"}, {Name: "Dent", Color: "#00ff00"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeClassList = %+v, %v; want %+v", got, err, want)
	}
	if got, err := NormalizeClassList(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty list = %#v, %v", got, err)
	}
	// The name rules are dataset.NormalizeClasses'; its errors come back as
	// errors of this package, so that the HTTP layer maps them like the
	// others of project.Service.
	for _, tc := range []struct {
		classes []Class
		msg     string
	}{
		{[]Class{{Name: "a"}, {Name: " a"}}, "类别“a”重复"},
		{[]Class{{Name: ""}}, "第 1 个类别名称为空"},
		{[]Class{{Name: "a\tb"}}, "控制字符"},
		{[]Class{{Name: "a", Color: "red"}}, "类别“a”的颜色必须是 #rrggbb 格式"},
		{[]Class{{Name: "a", Color: "#12345"}}, "#rrggbb"},
		{make([]Class, 1001), "类别数量不能超过 1000 个"},
	} {
		_, err := NormalizeClassList(tc.classes)
		wantKind(t, err, ErrInvalid, tc.msg)
	}
}

func TestMergeClassLists(t *testing.T) {
	got := mergeClassLists([][]Class{
		{{Name: "scratch", Color: "#111111"}, {Name: "dent"}},
		{},
		{{Name: "Dent", Color: "#222222"}, {Name: "scratch", Color: "#333333"}, {Name: "crack"}},
	})
	// Names compare case-sensitively, like the class names of YOLO.
	want := []Class{{Name: "scratch", Color: "#111111"}, {Name: "dent"}, {Name: "Dent", Color: "#222222"}, {Name: "crack"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeClassLists = %+v, want %+v", got, want)
	}
	if got := mergeClassLists(nil); got == nil || len(got) != 0 {
		t.Errorf("no lists = %#v, want an empty list (JSON [])", got)
	}
}

// ---- integration ----

// updatedAt returns the updated_at of a project.
func (e *testEnv) updatedAt(t *testing.T, projectID int64) time.Time {
	t.Helper()
	p, err := e.svc.GetProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return p.UpdatedAt
}

func TestIntegrationClasses(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: "classes"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := e.svc.ListClasses(ctx, p.ID)
	if err != nil || empty.Classes == nil || len(empty.Classes) != 0 || len(empty.Version) != 32 {
		t.Errorf("new project classes = %#v, %v", empty, err)
	}
	time.Sleep(2 * time.Millisecond)
	set, err := e.svc.SetClasses(ctx, p.ID, []Class{{Name: " scratch ", Color: "#EF4444"}, {Name: "凹坑"}}, &empty.Version)
	want := []Class{{Name: "scratch", Color: "#ef4444"}, {Name: "凹坑"}}
	if err != nil || !reflect.DeepEqual(set.Classes, want) || set.Version == empty.Version {
		t.Errorf("SetClasses = %+v, %v", set, err)
	}
	if got, _ := e.svc.ListClasses(ctx, p.ID); !reflect.DeepEqual(got, set) {
		t.Errorf("ListClasses = %+v, want %+v", got, set)
	}
	// A change of the list is a change of the project.
	changed := e.updatedAt(t, p.ID)
	if !changed.After(p.UpdatedAt) {
		t.Errorf("updated_at not touched: %v, was %v", changed, p.UpdatedAt)
	}

	// The same list again (as typed: before normalizing) is no change: the
	// version stays, and so does updated_at.
	time.Sleep(2 * time.Millisecond)
	for _, base := range []*string{&set.Version, nil} {
		same, err := e.svc.SetClasses(ctx, p.ID, []Class{{Name: "scratch ", Color: " #ef4444"}, {Name: "凹坑"}}, base)
		if err != nil || !reflect.DeepEqual(same, set) {
			t.Errorf("SetClasses with the same list = %+v, %v; want %+v", same, err, set)
		}
	}
	if got := e.updatedAt(t, p.ID); !got.Equal(changed) {
		t.Errorf("an unchanged list touched the project: %v, was %v", got, changed)
	}

	// A replacement from an older version is refused and changes nothing,
	// even when it is the list as it is now.
	_, err = e.svc.SetClasses(ctx, p.ID, []Class{{Name: "crack"}}, &empty.Version)
	wantKind(t, err, ErrConflict, "类别列表已被其他人修改，请载入最新的列表后再保存")
	_, err = e.svc.SetClasses(ctx, p.ID, want, &empty.Version)
	wantKind(t, err, ErrConflict, "类别列表已被其他人修改")
	_, err = e.svc.SetClasses(ctx, p.ID, []Class{{Name: "a"}, {Name: "a"}}, nil)
	wantKind(t, err, ErrInvalid, "重复")
	if got, _ := e.svc.ListClasses(ctx, p.ID); !reflect.DeepEqual(got, set) {
		t.Errorf("a refused list changed the classes: %+v", got)
	}

	// Adding a class appends it; adding it again changes nothing.
	added, err := e.svc.AddClass(ctx, p.ID, Class{Name: " 孔洞 ", Color: "#00FF00"})
	want = append(want, Class{Name: "孔洞", Color: "#00ff00"})
	if err != nil || !reflect.DeepEqual(added.Classes, want) || added.Version == set.Version {
		t.Errorf("AddClass = %+v, %v", added, err)
	}
	before := e.updatedAt(t, p.ID)
	if !before.After(changed) {
		t.Errorf("AddClass did not touch the project: %v, was %v", before, changed)
	}
	if again, err := e.svc.AddClass(ctx, p.ID, Class{Name: "孔洞", Color: "#123456"}); err != nil || !reflect.DeepEqual(again, added) {
		t.Errorf("AddClass again = %+v, %v", again, err)
	}
	if after := e.updatedAt(t, p.ID); !after.Equal(before) {
		t.Error("adding an existing class touched the project")
	}
	for _, tc := range []struct {
		class Class
		msg   string
	}{
		{Class{Name: " "}, "类别名称为空"},
		{Class{Name: "a\nb"}, "类别名称包含换行或控制字符"},
		{Class{Name: strings.Repeat("长", 256)}, "类别名称不能超过 255 个字符"},
		{Class{Name: "a", Color: "red"}, "类别“a”的颜色必须是 #rrggbb 格式"},
	} {
		_, err := e.svc.AddClass(ctx, p.ID, tc.class)
		wantKind(t, err, ErrInvalid, tc.msg)
	}

	// Concurrent additions all arrive.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.svc.AddClass(ctx, p.ID, Class{Name: fmt.Sprintf("c%d", i)}); err != nil {
				t.Errorf("AddClass c%d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	current, err := e.svc.ListClasses(ctx, p.ID)
	if err != nil || len(current.Classes) != 3+8 {
		t.Fatalf("after concurrent additions: %+v, %v", current.Classes, err)
	}
	// Of concurrent replacements from the same version, one wins and the
	// others are refused.
	var wins, conflicts int
	var mu sync.Mutex
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.SetClasses(ctx, p.ID, []Class{{Name: fmt.Sprintf("r%d", i)}}, &current.Version)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("SetClasses r%d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || conflicts != 3 {
		t.Errorf("concurrent replacements: %d won, %d refused; want 1 and 3", wins, conflicts)
	}

	// At most 1000 classes.
	full := make([]Class, 1000)
	for i := range full {
		full[i] = Class{Name: fmt.Sprintf("k%d", i)}
	}
	if _, err := e.svc.SetClasses(ctx, p.ID, full, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.AddClass(ctx, p.ID, Class{Name: "one more"})
	wantKind(t, err, ErrInvalid, "类别数量不能超过 1000 个")
	if _, err := e.svc.AddClass(ctx, p.ID, Class{Name: "k0"}); err != nil {
		t.Errorf("adding an existing class to a full list: %v", err)
	}

	if got, err := e.svc.SetClasses(ctx, p.ID, []Class{}, nil); err != nil || got.Classes == nil || len(got.Classes) != 0 ||
		got.Version != empty.Version {
		t.Errorf("clear = %#v, %v", got, err)
	}
	_, err = e.svc.ListClasses(ctx, 1<<40)
	wantKind(t, err, ErrNotFound, "项目不存在")
	_, err = e.svc.SetClasses(ctx, 1<<40, nil, nil)
	wantKind(t, err, ErrNotFound, "项目不存在")
	_, err = e.svc.AddClass(ctx, 1<<40, Class{Name: "a"})
	wantKind(t, err, ErrNotFound, "项目不存在")
}

// classProject creates a project with datasets and a class list.
func (e *testEnv) classProject(t *testing.T, name string, classes []Class, datasetIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	p, err := e.svc.CreateProject(ctx, CreateProjectInput{Name: name, DatasetIDs: datasetIDs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetClasses(ctx, p.ID, classes, nil); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestIntegrationDatasetClasses(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	a := e.addDataset(t, "a")
	b := e.addDataset(t, "b")
	lone := e.addDataset(t, "lone")

	// Created first, but listed after Alpha: the projects of a dataset go
	// by name, ignoring case.
	beta := e.classProject(t, "beta", []Class{{Name: "dent", Color: "#222222"}, {Name: "crack"}, {Name: "scratch", Color: "#333333"}}, a, b)
	alpha := e.classProject(t, "Alpha", []Class{{Name: "scratch", Color: "#111111"}, {Name: "dent"}}, a)
	e.classProject(t, "aardvark", nil, a) // an empty list adds nothing

	got, err := e.svc.DatasetClasses(ctx, a)
	want := []Class{{Name: "scratch", Color: "#111111"}, {Name: "dent"}, {Name: "crack"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetClasses(a) = %+v, %v; want %+v", got, err, want)
	}
	got, err = e.svc.DatasetClasses(ctx, b)
	want = []Class{{Name: "dent", Color: "#222222"}, {Name: "crack"}, {Name: "scratch", Color: "#333333"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetClasses(b) = %+v, %v; want %+v", got, err, want)
	}
	if got, err := e.svc.DatasetClasses(ctx, lone); err != nil || got == nil || len(got) != 0 {
		t.Errorf("DatasetClasses(dataset in no project) = %#v, %v; want []", got, err)
	}
	_, err = e.svc.DatasetClasses(ctx, 1<<40)
	wantKind(t, err, ErrNotFound, "数据集不存在")

	// Leaving a project takes its classes away; editing a list shows at once.
	if err := e.svc.RemoveDataset(ctx, alpha, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddClass(ctx, beta, Class{Name: "孔洞"}); err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.DatasetClasses(ctx, a)
	want = []Class{{Name: "dent", Color: "#222222"}, {Name: "crack"}, {Name: "scratch", Color: "#333333"}, {Name: "孔洞"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("DatasetClasses(a) after the changes = %+v, %v; want %+v", got, err, want)
	}
}

func TestIntegrationExportClasses(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	a := e.addDataset(t, "a")
	b := e.addDataset(t, "b")
	p := e.classProject(t, "外观检测", []Class{{Name: "scratch", Color: "#111111"}, {Name: "dent"}, {Name: "孔洞"}}, a)
	unsorted := e.classProject(t, "未整理", nil, a, b)

	// The class names in the order of the project's list (the YOLO ids).
	if got, err := e.svc.ExportClasses(ctx, p, a); err != nil || !slices.Equal(got, []string{"scratch", "dent", "孔洞"}) {
		t.Errorf("ExportClasses = %v, %v", got, err)
	}
	_, err := e.svc.ExportClasses(ctx, unsorted, a)
	wantKind(t, err, ErrConflict, "项目“未整理”还没有类别列表，请先在项目页面的“类别”中添加类别")
	_, err = e.svc.ExportClasses(ctx, p, b)
	wantKind(t, err, ErrInvalid, "数据集不在项目“外观检测”中，请选择数据集所属的项目")
	_, err = e.svc.ExportClasses(ctx, 1<<40, a)
	wantKind(t, err, ErrInvalid, "所选项目不存在或已被删除，请刷新后重试")
	// An unknown dataset is reported first: it is the resource of the request.
	_, err = e.svc.ExportClasses(ctx, p, 1<<40)
	wantKind(t, err, ErrNotFound, "数据集不存在")
	_, err = e.svc.ExportClasses(ctx, 1<<40, 1<<40)
	wantKind(t, err, ErrNotFound, "数据集不存在")
}
