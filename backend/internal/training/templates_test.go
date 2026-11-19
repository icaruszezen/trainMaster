package training

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBuiltinTemplateSpecsValid(t *testing.T) {
	specs := BuiltinTemplateSpecs()
	if len(specs) != 6 {
		t.Fatalf("builtin template count = %d, want 6", len(specs))
	}

	seenKeys := make(map[string]bool, len(specs))
	seenNames := make(map[string]bool, len(specs))
	for i, sp := range specs {
		if sp.Key == "" {
			t.Fatalf("spec[%d] has empty key", i)
		}
		if seenKeys[sp.Key] {
			t.Fatalf("duplicate builtin key %q", sp.Key)
		}
		seenKeys[sp.Key] = true

		if sp.Order != i+1 {
			t.Fatalf("spec %q order = %d, want %d", sp.Key, sp.Order, i+1)
		}
		if _, err := normalizeTemplateName(sp.Name); err != nil {
			t.Fatalf("spec %q invalid name: %v", sp.Key, err)
		}
		if seenNames[sp.Name] {
			t.Fatalf("duplicate builtin name %q", sp.Name)
		}
		seenNames[sp.Name] = true
		if _, err := normalizeTemplateDescription(sp.Description); err != nil {
			t.Fatalf("spec %q invalid description: %v", sp.Key, err)
		}

		norm, err := NormalizeTemplateParams(sp.Overrides)
		if err != nil {
			t.Fatalf("spec %q NormalizeTemplateParams: %v", sp.Key, err)
		}
		if norm["resume"] != false {
			t.Fatalf("spec %q resume = %v, want false", sp.Key, norm["resume"])
		}
		if norm["pretrained"] != true {
			t.Fatalf("spec %q pretrained = %v, want true", sp.Key, norm["pretrained"])
		}
		if norm["workers"] != 8 || norm["cache"] != false {
			t.Fatalf("spec %q hardware defaults workers=%v cache=%v, want 8 / false", sp.Key, norm["workers"], norm["cache"])
		}
		if norm["imgsz"] == 1280 {
			if norm["batch"] != 8 {
				t.Fatalf("1280px spec %q batch = %v, want 8", sp.Key, norm["batch"])
			}
		} else if norm["batch"] != 16 {
			t.Fatalf("640px spec %q batch = %v, want 16", sp.Key, norm["batch"])
		}
	}
}

func TestBuiltinTemplatesSeededAndReadOnly(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	specs := BuiltinTemplateSpecs()
	list, err := env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != len(specs) {
		t.Fatalf("initial template count = %d, want %d", len(list), len(specs))
	}

	for i, sp := range specs {
		got := list[i]
		if !got.IsBuiltin || got.IsDefault || got.BuiltinKey != sp.Key || got.Name != sp.Name || got.Description != sp.Description {
			t.Fatalf("seeded builtin[%d] = %+v, want key=%q name=%q", i, got, sp.Key, sp.Name)
		}
		wantParams, err := NormalizeTemplateParams(sp.Overrides)
		if err != nil {
			t.Fatalf("normalize want params for %q: %v", sp.Key, err)
		}
		if !reflect.DeepEqual(got.Params, wantParams) {
			t.Fatalf("seeded builtin %q params mismatch:\ngot:  %#v\nwant: %#v", sp.Key, got.Params, wantParams)
		}
	}

	// Built-in templates reject direct name, description, and params mutations.
	builtin := list[1] // 通用工业检测基准
	badName := "试图改名"
	if _, err := env.svc.UpdateTemplate(ctx, builtin.ID, UpdateTemplateInput{Name: &badName}); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename builtin err = %v, want ErrConflict", err)
	}
	badDesc := "试图改说明"
	if _, err := env.svc.UpdateTemplate(ctx, builtin.ID, UpdateTemplateInput{Description: &badDesc}); !errors.Is(err, ErrConflict) {
		t.Fatalf("update builtin desc err = %v, want ErrConflict", err)
	}
	badParams := map[string]any{"epochs": 999}
	if _, err := env.svc.UpdateTemplate(ctx, builtin.ID, UpdateTemplateInput{Params: &badParams}); !errors.Is(err, ErrConflict) {
		t.Fatalf("update builtin params err = %v, want ErrConflict", err)
	}

	// Built-in templates cannot be deleted.
	if err := env.svc.DeleteTemplate(ctx, builtin.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete builtin err = %v, want ErrConflict", err)
	}

	// Toggling is_default on a built-in template is allowed and pins it first.
	wantDefault := true
	updated, err := env.svc.UpdateTemplate(ctx, builtin.ID, UpdateTemplateInput{IsDefault: &wantDefault})
	if err != nil {
		t.Fatalf("set builtin default: %v", err)
	}
	if !updated.IsDefault || !updated.IsBuiltin {
		t.Fatalf("updated builtin = %+v", updated)
	}

	// EnsureBuiltinTemplates is idempotent and preserves is_default.
	if err := env.svc.EnsureBuiltinTemplates(ctx); err != nil {
		t.Fatalf("EnsureBuiltinTemplates: %v", err)
	}
	afterSync, err := env.svc.GetTemplate(ctx, builtin.ID)
	if err != nil {
		t.Fatalf("GetTemplate after sync: %v", err)
	}
	if !afterSync.IsDefault || afterSync.BuiltinKey != builtin.BuiltinKey {
		t.Fatalf("after sync builtin = %+v, want is_default preserved", afterSync)
	}

	reordered, err := env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates after default: %v", err)
	}
	if reordered[0].ID != builtin.ID {
		t.Fatalf("default builtin should be first, got %+v", reordered[0])
	}

	// EnsureBuiltinTemplates gracefully handles a pre-existing custom template
	// sharing the same name without mutating the user's template.
	if _, err := env.db.Exec(ctx, `DELETE FROM training_param_templates WHERE builtin_key = 'fast_debug'`); err != nil {
		t.Fatalf("delete fast_debug row for collision test: %v", err)
	}
	customClash, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:   "快速验证 / 试跑",
		Params: map[string]any{"epochs": 12},
	})
	if err != nil {
		t.Fatalf("create custom colliding template: %v", err)
	}
	if err := env.svc.EnsureBuiltinTemplates(ctx); err != nil {
		t.Fatalf("EnsureBuiltinTemplates with name collision: %v", err)
	}
	var syncedName string
	if err := env.db.QueryRow(ctx, `SELECT name FROM training_param_templates WHERE builtin_key = 'fast_debug'`).Scan(&syncedName); err != nil {
		t.Fatalf("lookup synced fast_debug: %v", err)
	}
	if syncedName != "快速验证 / 试跑（系统内置）" {
		t.Fatalf("colliding builtin name = %q, want suffixed", syncedName)
	}
	customStill, err := env.svc.GetTemplate(ctx, customClash.ID)
	if err != nil || customStill.Name != "快速验证 / 试跑" || customStill.IsBuiltin {
		t.Fatalf("custom template was altered: %+v, err=%v", customStill, err)
	}
}

func TestTemplateLifecycleAndJobSnapshot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	builtinCount := len(BuiltinTemplateSpecs())

	// Initially contains only the system built-in templates.
	list, err := env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != builtinCount {
		t.Fatalf("initial template count = %d, want %d", len(list), builtinCount)
	}

	// Validation: empty name rejected.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{Name: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name err = %v, want ErrInvalid", err)
	}

	// Create first custom template as default.
	tpl1, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:        "工业小目标高精度",
		Description: "1280 分辨率 + 300 轮 + 余弦学习率",
		Params: map[string]any{
			"epochs":    300,
			"imgsz":     1280,
			"batch":     8,
			"cos_lr":    true,
			"optimizer": "AdamW",
			"lr0":       0.001,
			"fliplr":    0.0,
			"resume":    true, // must be forced to false
		},
		IsDefault: true,
	})
	if err != nil {
		t.Fatalf("CreateTemplate tpl1: %v", err)
	}
	if tpl1.IsBuiltin || !tpl1.IsDefault || tpl1.Params["epochs"] != 300 || tpl1.Params["imgsz"] != 1280 || tpl1.Params["resume"] != false {
		t.Fatalf("unexpected tpl1: %+v", tpl1)
	}

	// Duplicate name (case-insensitive) fails with ErrConflict.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:   "  工业小目标高精度 ",
		Params: map[string]any{"epochs": 50},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name err = %v, want ErrConflict", err)
	}

	// Create second custom template as default -> automatically unsets tpl1.IsDefault.
	tpl2, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:        "Fast Debug Custom",
		Description: "30 轮快速调试定制",
		Params:      map[string]any{"epochs": 30, "batch": 32},
		IsDefault:   true,
	})
	if err != nil {
		t.Fatalf("CreateTemplate tpl2: %v", err)
	}
	if !tpl2.IsDefault || tpl2.IsBuiltin {
		t.Fatalf("tpl2 should be custom default")
	}

	// Case-insensitive Latin duplicate check.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{Name: "fast debug custom"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate err = %v, want ErrConflict", err)
	}

	// List order: default first, then custom (updated_at DESC), then built-in (builtin_order ASC).
	list, err = env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != builtinCount+2 {
		t.Fatalf("list len = %d, want %d", len(list), builtinCount+2)
	}
	if list[0].ID != tpl2.ID || !list[0].IsDefault || list[1].ID != tpl1.ID || list[1].IsDefault {
		t.Fatalf("unexpected custom ordering: [0]=%+v [1]=%+v", list[0], list[1])
	}
	if !list[2].IsBuiltin || list[2].BuiltinKey != "fast_debug" {
		t.Fatalf("expected built-in templates after custom templates, got [2]=%+v", list[2])
	}

	// Search by keyword matches name or description.
	found, err := env.svc.ListTemplates(ctx, "快速调试定制")
	if err != nil || len(found) != 1 || found[0].ID != tpl2.ID {
		t.Fatalf("search by desc = %+v, err=%v", found, err)
	}

	// Partial update: switch default back to tpl1 and update params.
	newDesc := "更新后的备注"
	wantDefault := true
	updatedParams := map[string]any{"epochs": 250, "imgsz": 1280, "lrf": 0.05}
	updated, err := env.svc.UpdateTemplate(ctx, tpl1.ID, UpdateTemplateInput{
		Description: &newDesc,
		Params:      &updatedParams,
		IsDefault:   &wantDefault,
	})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if !updated.IsDefault || updated.Description != newDesc || updated.Params["epochs"] != 250 || updated.Params["lrf"] != 0.05 {
		t.Fatalf("unexpected updated tpl1: %+v", updated)
	}
	refetched2, err := env.svc.GetTemplate(ctx, tpl2.ID)
	if err != nil || refetched2.IsDefault {
		t.Fatalf("tpl2 should no longer be default: %+v, err=%v", refetched2, err)
	}

	// Create a training job referencing tpl1, then delete tpl1:
	// job preserves original template_name snapshot and sets template_id to nil on delete.
	proj := env.createProject(t, "焊点检测")
	dvID := env.insertReadyDataVersion(t, proj.ID, 1)
	job, err := env.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitScratch,
		YOLOVersion:   "yolo11",
		ModelScale:    "s",
		TemplateID:    &tpl1.ID,
		Params:        updated.Params,
	})
	if err != nil {
		t.Fatalf("Create job with template: %v", err)
	}
	if job.TemplateID == nil || *job.TemplateID != tpl1.ID || job.TemplateName != "工业小目标高精度" {
		t.Fatalf("job template lineage = id:%v name:%q", job.TemplateID, job.TemplateName)
	}

	// Invalid template ID fails with ErrNotFound.
	badID := int64(999999)
	if _, err := env.svc.Create(ctx, CreateInput{
		ProjectID:     proj.ID,
		DataVersionID: dvID,
		InitMode:      InitScratch,
		YOLOVersion:   "yolo11",
		ModelScale:    "s",
		TemplateID:    &badID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing template err = %v, want ErrNotFound", err)
	}

	// Delete tpl1 -> job keeps template_name snapshot, template_id becomes nil.
	if err := env.svc.DeleteTemplate(ctx, tpl1.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if err := env.svc.DeleteTemplate(ctx, tpl1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteTemplate err = %v, want ErrNotFound", err)
	}
	afterDelJob, err := env.svc.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get job after template delete: %v", err)
	}
	if afterDelJob.TemplateID != nil || afterDelJob.TemplateName != "工业小目标高精度" {
		t.Fatalf("job after template delete = id:%v name:%q, want nil & snapshot", afterDelJob.TemplateID, afterDelJob.TemplateName)
	}
	if afterDelJob.Params["epochs"] != 250 {
		t.Fatalf("job params should remain intact after template delete: epochs=%v", afterDelJob.Params["epochs"])
	}
}
