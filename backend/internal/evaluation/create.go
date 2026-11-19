package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Create creates one or multiple (batch multi-model) evaluation tasks against
// the same snapshotted data source and enqueues them for compute nodes.
func (s *Service) Create(ctx context.Context, in CreateInput) ([]Evaluation, error) {
	models, err := s.resolveModels(ctx, in.ModelIDs, true)
	if err != nil {
		return nil, err
	}
	baseName, err := normalizeName(in.Name)
	if err != nil {
		return nil, err
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return nil, err
	}
	opts, err := NormalizeOptions(in.Options)
	if err != nil {
		return nil, err
	}
	src, err := s.resolveSource(ctx, in.Source, true)
	if err != nil {
		return nil, err
	}

	sourceRefJSON, err := json.Marshal(src.SourceRef)
	if err != nil {
		return nil, err
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}

	// Pre-marshal GT objects once for all batch evaluations.
	sampleGTRaw := make([][]byte, len(src.Samples))
	for i, smp := range src.Samples {
		b, merr := json.Marshal(smp.GTObjects)
		if merr != nil {
			return nil, merr
		}
		sampleGTRaw[i] = b
	}

	colorCache := map[int64]map[string]string{}
	createdIDs := make([]int64, 0, len(models))

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, m := range models {
			colors, ok := colorCache[m.ProjectID]
			if !ok {
				colors = s.projectClassColors(ctx, m.ProjectID)
				colorCache[m.ProjectID] = colors
			}
			classes, _, _, _ := buildAlignedClasses(m, src.GTLabelCounts, src.GTLabelImages, colors)
			modelRefJSON, merr := json.Marshal(m)
			if merr != nil {
				return merr
			}
			classesJSON, merr := json.Marshal(classes)
			if merr != nil {
				return merr
			}

			name := baseName
			if name == "" {
				name = defaultEvalName(m, src.SourceRef)
			} else if len(models) > 1 {
				suffix := fmt.Sprintf(" (%s v%d)", m.ProjectName, m.Version)
				runes := []rune(name + suffix)
				if len(runes) > maxNameRunes {
					name = string(runes[:maxNameRunes])
				} else {
					name = string(runes)
				}
			}

			var evalID int64
			err := tx.QueryRow(ctx, `INSERT INTO evaluations (
					project_id, model_id, model_ref, name, description,
					source_type, source_fingerprint, data_version_id, source_ref,
					options, classes, status, stage, progress_done, progress_total
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, '', 0, $13)
				RETURNING id`,
				m.ProjectID, m.ID, modelRefJSON, name, desc,
				src.SourceRef.Type, src.Fingerprint, src.DataVersionID, sourceRefJSON,
				optsJSON, classesJSON, StatusQueued, len(src.Samples),
			).Scan(&evalID)
			if err != nil {
				return fmt.Errorf("insert evaluation: %w", err)
			}

			// Bulk-insert snapshotted samples via CopyFrom for high throughput.
			rows := make([][]any, len(src.Samples))
			for i, smp := range src.Samples {
				rows[i] = []any{
					evalID,
					i + 1,
					smp.SampleKey,
					smp.Split,
					smp.DatasetID,
					smp.DatasetName,
					smp.DatasetImageID,
					smp.DataVersionImageID,
					smp.Path,
					smp.Width,
					smp.Height,
					sampleGTRaw[i],
					len(smp.GTObjects),
				}
			}
			_, err = tx.CopyFrom(
				ctx,
				pgx.Identifier{"evaluation_images"},
				[]string{
					"evaluation_id", "position", "sample_key", "split",
					"dataset_id", "dataset_name", "dataset_image_id", "data_version_image_id",
					"path", "width", "height", "gt_objects", "gt_count",
				},
				pgx.CopyFromRows(rows),
			)
			if err != nil {
				return fmt.Errorf("copy evaluation images: %w", err)
			}
			createdIDs = append(createdIDs, evalID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.notifyTasks()

	out := make([]Evaluation, 0, len(createdIDs))
	for _, id := range createdIDs {
		ev, gerr := s.Get(ctx, id)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, ev)
	}
	return out, nil
}

// ---- Query / List / Get ----

const evalSelectColumns = `e.id, e.project_id, coalesce(p.name, e.model_ref->>'project_name', ''),
	e.model_id, (m.id IS NOT NULL AND m.deleted_at IS NULL), e.model_ref,
	e.name, e.description, e.source_type, e.source_fingerprint,
	e.data_version_id, (dv.id IS NOT NULL AND dv.deleted_at IS NULL), e.source_ref,
	e.options, e.classes, e.status, e.stage,
	e.node_id, cn.name, cn.hostname,
	e.attempts, e.progress_done, e.progress_total, e.eta_seconds,
	e.summary, e.error, e.error_detail, e.result,
	e.queued_at, e.started_at, e.finished_at, e.created_at, e.updated_at`

const evalSelectFrom = ` FROM evaluations e
	LEFT JOIN projects p ON p.id = e.project_id
	LEFT JOIN models m ON m.id = e.model_id
	LEFT JOIN data_versions dv ON dv.id = e.data_version_id
	LEFT JOIN compute_nodes cn ON cn.id = e.node_id`

func scanEvaluation(row pgx.Row, workers int) (Evaluation, error) {
	var (
		ev           Evaluation
		modelRefRaw  []byte
		sourceRefRaw []byte
		optsRaw      []byte
		classesRaw   []byte
		nodeID       *int64
		nodeName     *string
		nodeHost     *string
		summaryRaw   []byte
		resultRaw    []byte
	)
	err := row.Scan(
		&ev.ID, &ev.ProjectID, &ev.ProjectName,
		&ev.ModelID, &ev.ModelExists, &modelRefRaw,
		&ev.Name, &ev.Description, &ev.SourceType, &ev.SourceFingerprint,
		&ev.DataVersionID, &ev.DataVersionLive, &sourceRefRaw,
		&optsRaw, &classesRaw, &ev.Status, &ev.Stage,
		&nodeID, &nodeName, &nodeHost,
		&ev.Attempts, &ev.ProgressDone, &ev.ProgressTotal, &ev.ETASeconds,
		&summaryRaw, &ev.Error, &ev.ErrorDetail, &resultRaw,
		&ev.QueuedAt, &ev.StartedAt, &ev.FinishedAt, &ev.CreatedAt, &ev.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evaluation{}, errEvalNotFound
	}
	if err != nil {
		return Evaluation{}, err
	}
	_ = json.Unmarshal(modelRefRaw, &ev.Model)
	if ev.Model.ClassNames == nil {
		ev.Model.ClassNames = []string{}
	}
	_ = json.Unmarshal(sourceRefRaw, &ev.Source)
	_ = json.Unmarshal(optsRaw, &ev.Options)
	_ = json.Unmarshal(classesRaw, &ev.Classes)
	if ev.Classes == nil {
		ev.Classes = []ClassInfo{}
	}
	if len(summaryRaw) > 0 {
		_ = json.Unmarshal(summaryRaw, &ev.Summary)
	}
	if len(resultRaw) > 0 {
		ev.Result = json.RawMessage(resultRaw)
	}
	if nodeID != nil {
		n := NodeRef{ID: *nodeID}
		if nodeName != nil {
			n.Name = *nodeName
		}
		if nodeHost != nil {
			n.Hostname = *nodeHost
		}
		ev.Node = &n
	}
	ev.WorkersOnline = workers
	ev.QueuedAt = ev.QueuedAt.UTC()
	ev.CreatedAt = ev.CreatedAt.UTC()
	ev.UpdatedAt = ev.UpdatedAt.UTC()
	if ev.StartedAt != nil {
		t := ev.StartedAt.UTC()
		ev.StartedAt = &t
	}
	if ev.FinishedAt != nil {
		t := ev.FinishedAt.UTC()
		ev.FinishedAt = &t
	}
	return ev, nil
}

func (s *Service) queuePositions(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM evaluations WHERE status = 'queued' ORDER BY queued_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	pos := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		pos++
		out[id] = pos
	}
	return out, rows.Err()
}

// List returns evaluations matching filter, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Evaluation, error) {
	if f.Status != "" && !slices.Contains([]string{StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled}, f.Status) {
		return nil, newError(ErrBadRequest, "状态筛选参数无效：%s", f.Status)
	}
	if f.SourceType != "" && f.SourceType != SourceDataVersion && f.SourceType != SourceDatasets {
		return nil, newError(ErrBadRequest, "数据来源筛选参数无效：%s", f.SourceType)
	}
	where := " WHERE p.deleted_at IS NULL"
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args)))
	}
	if f.ProjectID > 0 {
		add("e.project_id = $?", f.ProjectID)
	}
	if f.ModelID > 0 {
		add("e.model_id = $?", f.ModelID)
	}
	if f.DataVersionID > 0 {
		add("e.data_version_id = $?", f.DataVersionID)
	}
	if f.SourceType != "" {
		add("e.source_type = $?", f.SourceType)
	}
	if f.Status != "" {
		add("e.status = $?", f.Status)
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		add(`(strpos(lower(e.name), lower($?)) > 0 OR strpos(lower(e.description), lower($?)) > 0 OR strpos(lower(coalesce(p.name, '')), lower($?)) > 0)`, q)
	}

	rows, err := s.db.Query(ctx, "SELECT "+evalSelectColumns+evalSelectFrom+where+" ORDER BY e.created_at DESC, e.id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workers := s.workersOnline()
	items := []Evaluation{}
	hasQueued := false
	for rows.Next() {
		ev, serr := scanEvaluation(rows, workers)
		if serr != nil {
			return nil, serr
		}
		if ev.Status == StatusQueued {
			hasQueued = true
		}
		items = append(items, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if hasQueued {
		if posMap, err := s.queuePositions(ctx); err == nil {
			for i := range items {
				if p, ok := posMap[items[i].ID]; ok {
					items[i].QueuePosition = &p
				}
			}
		}
	}
	return items, nil
}

// Get returns one evaluation by ID.
func (s *Service) Get(ctx context.Context, id int64) (Evaluation, error) {
	workers := s.workersOnline()
	ev, err := scanEvaluation(s.db.QueryRow(ctx, "SELECT "+evalSelectColumns+evalSelectFrom+" WHERE e.id = $1 AND p.deleted_at IS NULL", id), workers)
	if err != nil {
		return Evaluation{}, err
	}
	if ev.Status == StatusQueued {
		if posMap, perr := s.queuePositions(ctx); perr == nil {
			if p, ok := posMap[ev.ID]; ok {
				ev.QueuePosition = &p
			}
		}
	}
	return ev, nil
}

// Update edits name/description and/or persists a new default (conf, iou) working
// point for a succeeded evaluation (recomputing summary & evaluation_images counts).
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (Evaluation, error) {
	ev, err := s.Get(ctx, id)
	if err != nil {
		return Evaluation{}, err
	}

	name := ev.Name
	if in.Name != nil {
		n, nerr := normalizeName(*in.Name)
		if nerr != nil {
			return Evaluation{}, nerr
		}
		if n == "" {
			return Evaluation{}, newError(ErrInvalid, "评估名称不能为空")
		}
		name = n
	}
	desc := ev.Description
	if in.Description != nil {
		d, derr := normalizeDescription(*in.Description)
		if derr != nil {
			return Evaluation{}, derr
		}
		desc = d
	}

	opts := ev.Options
	recomputeThreshold := in.DefaultConf != nil || in.DefaultIoU != nil
	if recomputeThreshold {
		if ev.Status != StatusSucceeded {
			return Evaluation{}, newError(ErrConflict, "仅已完成的评估任务可以保存默认工作阈值")
		}
		if in.DefaultConf != nil {
			c := *in.DefaultConf
			if math.IsNaN(c) || math.IsInf(c, 0) || c < ev.Options.StoreMinConf || c > 0.99 {
				return Evaluation{}, newError(ErrInvalid, "默认置信度阈值必须在 %.2f–0.99 之间", ev.Options.StoreMinConf)
			}
			opts.Conf = round4(c)
		}
		if in.DefaultIoU != nil {
			v := *in.DefaultIoU
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.05 || v > 0.99 {
				return Evaluation{}, newError(ErrInvalid, "匹配 IoU 阈值必须在 0.05–0.99 之间")
			}
			opts.IoU = round4(v)
		}
	}

	if !recomputeThreshold {
		_, err = s.db.Exec(ctx, `UPDATE evaluations SET name = $2, description = $3, updated_at = now() WHERE id = $1`, id, name, desc)
		if err != nil {
			return Evaluation{}, err
		}
		return s.Get(ctx, id)
	}

	// Recompute summary & per-image cached counts at the new default (conf, iou).
	loaded, err := s.loadEvaluationData(ctx, ev)
	if err != nil {
		return Evaluation{}, err
	}
	run := ComputeAnalysis(ev.ID, loaded.classes, loaded.samples, loaded.curves, loaded.storeMinConf, opts.Conf, opts.IoU)
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return Evaluation{}, err
	}
	summaryJSON, err := json.Marshal(run.Analysis.Summary)
	if err != nil {
		return Evaluation{}, err
	}

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE evaluations
			SET name = $2, description = $3, options = $4, summary = $5, updated_at = now()
			WHERE id = $1`, id, name, desc, optsJSON, summaryJSON); err != nil {
			return err
		}
		return updateCachedSampleCounts(ctx, tx, run.Samples)
	})
	if err != nil {
		return Evaluation{}, err
	}
	return s.Get(ctx, id)
}

func updateCachedSampleCounts(ctx context.Context, tx pgx.Tx, samples []computedSample) error {
	if len(samples) == 0 {
		return nil
	}
	const chunkSize = 1000
	for start := 0; start < len(samples); start += chunkSize {
		end := min(start+chunkSize, len(samples))
		n := end - start
		ids := make([]int64, n)
		tps := make([]int32, n)
		fps := make([]int32, n)
		fns := make([]int32, n)
		mcs := make([]int32, n)
		outs := make([]string, n)
		mfc := make([]float64, n)
		for i, cs := range samples[start:end] {
			ids[i] = cs.Data.ID
			tps[i] = int32(cs.TPCount)
			fps[i] = int32(cs.FPCount)
			fns[i] = int32(cs.FNCount)
			mcs[i] = int32(cs.MisclassifiedCount)
			outs[i] = cs.ImageOutcome
			mfc[i] = cs.MaxFPConf
		}
		if _, err := tx.Exec(ctx, `UPDATE evaluation_images ei
			SET tp_count = u.tp, fp_count = u.fp, fn_count = u.fn,
				misclassified_count = u.mc, image_outcome = u.outcome, max_fp_conf = u.mfc
			FROM unnest($1::bigint[], $2::int[], $3::int[], $4::int[], $5::int[], $6::text[], $7::double precision[])
				AS u (id, tp, fp, fn, mc, outcome, mfc)
			WHERE ei.id = u.id`, ids, tps, fps, fns, mcs, outs, mfc); err != nil {
			return err
		}
	}
	return nil
}

// Cancel stops a queued or running evaluation.
func (s *Service) Cancel(ctx context.Context, id int64) (Evaluation, error) {
	tag, err := s.db.Exec(ctx, `UPDATE evaluations
		SET status = 'canceled', stage = '', node_id = NULL, lease_expires_at = NULL,
			eta_seconds = NULL, finished_at = now(), updated_at = now()
		WHERE id = $1 AND status IN ('queued', 'running')`, id)
	if err != nil {
		return Evaluation{}, err
	}
	if tag.RowsAffected() == 0 {
		ev, gerr := s.Get(ctx, id)
		if gerr != nil {
			return Evaluation{}, gerr
		}
		return Evaluation{}, newError(ErrConflict, "当前状态（%s）不支持停止操作", ev.Status)
	}
	return s.Get(ctx, id)
}

// Retry re-queues a failed or canceled evaluation.
func (s *Service) Retry(ctx context.Context, id int64) (Evaluation, error) {
	ev, err := s.Get(ctx, id)
	if err != nil {
		return Evaluation{}, err
	}
	if ev.Status != StatusFailed && ev.Status != StatusCanceled {
		return Evaluation{}, newError(ErrConflict, "仅失败或已取消的评估任务可以重试")
	}
	if !ev.ModelExists {
		return Evaluation{}, newError(ErrConflict, "关联的模型已被删除，无法重新评估")
	}
	if ev.SourceType == SourceDataVersion && !ev.DataVersionLive {
		return Evaluation{}, newError(ErrConflict, "关联的数据版本已被删除，无法重新评估")
	}

	s.cache.delete(id)
	s.removeEvalDir(id)

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE evaluations
			SET status = 'queued', stage = '', node_id = NULL, attempts = 0,
				lease_expires_at = NULL, progress_done = 0, eta_seconds = NULL,
				summary = '{}', error = '', error_detail = '', result = NULL,
				queued_at = now(), started_at = NULL, finished_at = NULL, updated_at = now()
			WHERE id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE evaluation_images
			SET inference_error = '', tp_count = 0, fp_count = 0, fn_count = 0,
				misclassified_count = 0, image_outcome = '', max_fp_conf = 0
			WHERE evaluation_id = $1`, id)
		return err
	})
	if err != nil {
		return Evaluation{}, err
	}
	s.notifyTasks()
	return s.Get(ctx, id)
}

// Delete directly deletes an evaluation and its runs/evaluations/eval-<id>/ directory.
func (s *Service) Delete(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM evaluations WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errEvalNotFound
	}
	s.cache.delete(id)
	s.removeEvalDir(id)
	return nil
}

// DeleteProjectEvaluations deletes all evaluations belonging to projectID
// (called when deleting a project with ?delete_evaluations=true).
func (s *Service) DeleteProjectEvaluations(ctx context.Context, projectID int64) error {
	rows, err := s.db.Query(ctx, `DELETE FROM evaluations WHERE project_id = $1 RETURNING id`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		s.cache.delete(id)
		s.removeEvalDir(id)
	}
	return rows.Err()
}

var _ = time.Now
