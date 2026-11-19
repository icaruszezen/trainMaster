package training

import (
	"context"
	"errors"
	"testing"
)

func TestTemplateLifecycleAndJobSnapshot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Empty initially.
	list, err := env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("initial template count = %d, want 0", len(list))
	}

	// Validation: empty name rejected.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{Name: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name err = %v, want ErrInvalid", err)
	}

	// Create first template as default.
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
	if !tpl1.IsDefault || tpl1.Params["epochs"] != 300 || tpl1.Params["imgsz"] != 1280 || tpl1.Params["resume"] != false {
		t.Fatalf("unexpected tpl1: %+v", tpl1)
	}

	// Duplicate name (case-insensitive) fails with ErrConflict.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:   "  工业小目标高精度 ",
		Params: map[string]any{"epochs": 50},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name err = %v, want ErrConflict", err)
	}

	// Create second template as default -> automatically unsets tpl1.IsDefault.
	tpl2, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{
		Name:        "Fast Debug",
		Description: "30 轮快速试跑",
		Params:      map[string]any{"epochs": 30, "batch": 32},
		IsDefault:   true,
	})
	if err != nil {
		t.Fatalf("CreateTemplate tpl2: %v", err)
	}
	if !tpl2.IsDefault {
		t.Fatalf("tpl2 should be default")
	}

	// Case-insensitive Latin duplicate check.
	if _, err := env.svc.CreateTemplate(ctx, CreateTemplateInput{Name: "fast debug"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate err = %v, want ErrConflict", err)
	}

	// List order: default first, then updated_at DESC.
	list, err = env.svc.ListTemplates(ctx, "")
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != 2 || list[0].ID != tpl2.ID || !list[0].IsDefault || list[1].ID != tpl1.ID || list[1].IsDefault {
		t.Fatalf("unexpected list order/default: %+v", list)
	}

	// Search by keyword matches name or description.
	found, err := env.svc.ListTemplates(ctx, "快速试跑")
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

	// Create a training job referencing tpl1, then rename & delete tpl1:
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
