package training

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
)

const (
	maxJobNameRunes = 120
	maxDescRunes    = 2000
)

func normalizeJobName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", newError(ErrInvalid, "任务名称不能包含换行或控制字符")
		}
	}
	if utf8.RuneCountInString(s) > maxJobNameRunes {
		return "", newError(ErrInvalid, "任务名称不能超过 %d 个字符", maxJobNameRunes)
	}
	return s, nil
}

func normalizeDescription(raw string) (string, error) {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxDescRunes {
		return "", newError(ErrInvalid, "任务备注不能超过 %d 个字符", maxDescRunes)
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", newError(ErrInvalid, "任务备注不能包含控制字符")
		}
	}
	return s, nil
}

func yoloDisplayLabel(yoloVersion, scale string) string {
	labels := map[string]string{
		"yolov8":  "YOLOv8",
		"yolov9":  "YOLOv9",
		"yolov10": "YOLOv10",
		"yolo11":  "YOLO11",
		"yolo12":  "YOLO12",
		"yolo26":  "YOLO26",
	}
	base, ok := labels[yoloVersion]
	if !ok {
		base = yoloVersion
	}
	return base + scale
}

func defaultJobName(projectName string, dvVersion int, initMode, yoloVersion, scale string, baseModel *BaseModelRef) string {
	arch := yoloDisplayLabel(yoloVersion, scale)
	var suffix string
	switch initMode {
	case InitFinetune:
		if baseModel != nil {
			suffix = fmt.Sprintf("微调 %s v%d", arch, baseModel.Version)
		} else {
			suffix = "微调 " + arch
		}
	case InitResume:
		if baseModel != nil {
			suffix = fmt.Sprintf("续训 %s v%d", arch, baseModel.Version)
		} else {
			suffix = "续训 " + arch
		}
	default:
		suffix = arch
	}
	name := fmt.Sprintf("%s · 数据版本 v%d · %s", projectName, dvVersion, suffix)
	runes := []rune(name)
	if len(runes) > maxJobNameRunes {
		name = string(runes[:maxJobNameRunes])
	}
	return name
}

// Create validates in and inserts a new queued training job.
func (s *Service) Create(ctx context.Context, in CreateInput) (Job, error) {
	if in.ProjectID <= 0 {
		return Job{}, newError(ErrInvalid, "请选择所属项目")
	}
	if in.DataVersionID <= 0 {
		return Job{}, newError(ErrInvalid, "请选择用于训练的数据版本")
	}
	name, err := normalizeJobName(in.Name)
	if err != nil {
		return Job{}, err
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return Job{}, err
	}

	proj, err := s.projects.GetProject(ctx, in.ProjectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			return Job{}, errProjectNotFound
		}
		return Job{}, err
	}

	dv, err := s.dataVersions.Get(ctx, in.DataVersionID)
	if err != nil {
		if errors.Is(err, dataversion.ErrNotFound) {
			return Job{}, errDataVersionNotFound
		}
		return Job{}, err
	}
	if dv.ProjectID != in.ProjectID {
		return Job{}, newError(ErrInvalid, "所选数据版本不属于该项目")
	}
	if dv.Status != dataversion.StatusReady {
		return Job{}, newError(ErrConflict, "数据版本尚未生成完成，请等待生成后再发起训练")
	}
	if len(dv.Classes) == 0 {
		return Job{}, newError(ErrInvalid, "数据版本没有类别，无法训练")
	}
	if dv.TrainCount <= 0 || dv.ValCount <= 0 {
		return Job{}, newError(ErrInvalid, "数据版本需要同时包含训练集与验证集图片")
	}

	var dirName string
	if err := s.db.QueryRow(ctx, "SELECT dir_name FROM data_versions WHERE id = $1 AND deleted_at IS NULL", dv.ID).Scan(&dirName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, errDataVersionNotFound
		}
		return Job{}, err
	}

	initMode := strings.TrimSpace(in.InitMode)
	if initMode == "" {
		if in.BaseModelID != nil && *in.BaseModelID > 0 {
			initMode = InitFinetune
		} else {
			initMode = InitScratch
		}
	}
	if initMode != InitScratch && initMode != InitFinetune && initMode != InitResume {
		return Job{}, newError(ErrInvalid, "初始化模式必须是 scratch（从头训练）、finetune（预训练微调）或 resume（断点续训）")
	}

	var (
		yoloVersion string
		modelScale  string
		baseID      *int64
		baseRef     *BaseModelRef
	)
	if initMode == InitScratch {
		if in.BaseModelID != nil && *in.BaseModelID > 0 {
			return Job{}, newError(ErrInvalid, "从头训练模式不能同时指定基础模型")
		}
		yoloVersion = strings.TrimSpace(in.YOLOVersion)
		if _, ok := model.LookupYOLOVersion(yoloVersion); !ok {
			return Job{}, newError(ErrInvalid, "请选择有效的 YOLO 版本")
		}
		modelScale = strings.TrimSpace(in.ModelScale)
		if modelScale == "" {
			modelScale = DefaultScale(yoloVersion)
		}
		if _, err := ArchYAML(yoloVersion, modelScale); err != nil {
			return Job{}, err
		}
	} else {
		if in.BaseModelID == nil || *in.BaseModelID <= 0 {
			return Job{}, newError(ErrInvalid, "请选择用于微调或续训的基础模型")
		}
		m, err := s.models.GetModel(ctx, *in.BaseModelID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return Job{}, errBaseModelNotFound
			}
			return Job{}, err
		}
		if m.FileMissing {
			return Job{}, newError(ErrConflict, "基础模型的权重文件缺失，无法用于训练")
		}
		if m.Task != "" && m.Task != "detect" {
			return Job{}, newError(ErrInvalid, "当前仅支持目标检测（detect）模型训练，所选模型的任务类型为 %s", m.Task)
		}
		if initMode == InitResume && !m.Info.TrainingCheckpoint {
			return Job{}, newError(ErrInvalid, "所选模型不包含优化器检查点状态（非 last.pt），无法断点续训；请改选“预训练微调”")
		}
		if wantYOLO := strings.TrimSpace(in.YOLOVersion); wantYOLO != "" && wantYOLO != m.YOLOVersion {
			return Job{}, newError(ErrInvalid, "所选基础模型的 YOLO 版本为 %s，与指定的 %s 不一致", m.YOLOVersion, wantYOLO)
		}
		yoloVersion = m.YOLOVersion
		modelScale = m.Info.Scale
		idCopy := m.ID
		baseID = &idCopy
		baseRef = &BaseModelRef{
			ID:                 m.ID,
			ProjectID:          m.Project.ID,
			ProjectName:        m.Project.Name,
			Version:            m.Version,
			YOLOVersion:        m.YOLOVersion,
			Task:               m.Task,
			Scale:              m.Info.Scale,
			FileName:           m.FileName,
			SizeBytes:          m.SizeBytes,
			SHA256:             m.SHA256,
			TrainingCheckpoint: m.Info.TrainingCheckpoint,
		}
	}

	params, err := NormalizeParams(in.Params, initMode)
	if err != nil {
		return Job{}, err
	}
	if valOn, _ := params["val"].(bool); valOn {
		if sp, _ := params["split"].(string); sp == "test" && dv.TestCount == 0 {
			return Job{}, newError(ErrInvalid, "所选数据版本没有测试集（test），请将验证集划分（split）改为 val 或 train")
		}
	}
	if clsFilter, ok := params["classes"].([]int); ok {
		for _, cid := range clsFilter {
			if cid < 0 || cid >= len(dv.Classes) {
				return Job{}, newError(ErrInvalid, "参数 classes 中的类别编号 %d 超出数据版本类别范围（0–%d）", cid, len(dv.Classes)-1)
			}
		}
	}

	classNames := make([]string, len(dv.Classes))
	for i, c := range dv.Classes {
		classNames[i] = c.Name
	}
	dvRef := DataVersionRef{
		ID:             dv.ID,
		Version:        dv.Version,
		DirName:        dirName,
		ClassNames:     classNames,
		ImageCount:     dv.ImageCount,
		TrainCount:     dv.TrainCount,
		ValCount:       dv.ValCount,
		TestCount:      dv.TestCount,
		AugmentedCount: dv.AugmentedCount,
	}

	if name == "" {
		name = defaultJobName(proj.Name, dv.Version, initMode, yoloVersion, modelScale, baseRef)
	}
	autoRegister := true
	if in.AutoRegister != nil {
		autoRegister = *in.AutoRegister
	}
	totalEpochs, _ := params["epochs"].(int)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return Job{}, fmt.Errorf("encode training params: %w", err)
	}
	dvRefJSON, err := json.Marshal(dvRef)
	if err != nil {
		return Job{}, fmt.Errorf("encode data version ref: %w", err)
	}
	var baseRefJSON []byte
	if baseRef != nil {
		baseRefJSON, err = json.Marshal(baseRef)
		if err != nil {
			return Job{}, fmt.Errorf("encode base model ref: %w", err)
		}
	}

	var jobID int64
	err = s.db.QueryRow(ctx, `INSERT INTO training_jobs (
			project_id, data_version_id, data_version_ref, name, description,
			init_mode, yolo_version, model_scale, task, base_model_id, base_model_ref,
			params, auto_register, status, total_epochs
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'detect', $9, $10, $11, $12, 'queued', $13)
		RETURNING id`,
		in.ProjectID, dv.ID, dvRefJSON, name, desc,
		initMode, yoloVersion, modelScale, baseID, baseRefJSON,
		paramsJSON, autoRegister, totalEpochs).Scan(&jobID)
	if err != nil {
		return Job{}, fmt.Errorf("insert training job: %w", err)
	}

	s.log.Info("training job created", "job_id", jobID, "project_id", in.ProjectID, "data_version_id", dv.ID,
		"init_mode", initMode, "yolo_version", yoloVersion, "scale", modelScale)
	return s.Get(ctx, jobID)
}

const jobSelectCols = `
	j.id, j.project_id, p.name, j.data_version_id, (dv.deleted_at IS NULL),
	j.data_version_ref, j.name, j.description, j.init_mode, j.yolo_version, j.model_scale, j.task,
	j.base_model_id, j.base_model_ref, j.params, j.auto_register,
	j.status, j.stage, j.attempts, j.current_epoch, j.total_epochs, j.batch_done, j.batch_total, j.eta_seconds,
	j.metrics, j.artifacts, j.error, j.error_detail, j.result,
	j.queued_at, j.started_at, j.finished_at, j.created_at, j.updated_at,
	cn.id, cn.name, cn.hostname,
	bm.id, bm.version, bm.yolo_version,
	lm.id, lm.version, lm.yolo_version`

const jobSelectFrom = `
	FROM training_jobs j
	JOIN projects p ON p.id = j.project_id
	JOIN data_versions dv ON dv.id = j.data_version_id
	LEFT JOIN compute_nodes cn ON cn.id = j.node_id
	LEFT JOIN models bm ON bm.id = j.best_model_id AND bm.deleted_at IS NULL
	LEFT JOIN models lm ON lm.id = j.last_model_id AND lm.deleted_at IS NULL`

func scanJobRow(row pgx.Row) (Job, error) {
	var (
		j          Job
		dvRefRaw   []byte
		baseRefRaw []byte
		paramsRaw  []byte
		metricsRaw []byte
		artRaw     []byte
		resultRaw  []byte
		nodeID     *int64
		nodeName   *string
		nodeHost   *string
		bestID     *int64
		bestVer    *int
		bestYOLO   *string
		lastID     *int64
		lastVer    *int
		lastYOLO   *string
	)
	err := row.Scan(
		&j.ID, &j.ProjectID, &j.ProjectName, &j.DataVersionID, &j.DataVersionLive,
		&dvRefRaw, &j.Name, &j.Description, &j.InitMode, &j.YOLOVersion, &j.ModelScale, &j.Task,
		&j.BaseModelID, &baseRefRaw, &paramsRaw, &j.AutoRegister,
		&j.Status, &j.Stage, &j.Attempts, &j.CurrentEpoch, &j.TotalEpochs, &j.BatchDone, &j.BatchTotal, &j.ETASeconds,
		&metricsRaw, &artRaw, &j.Error, &j.ErrorDetail, &resultRaw,
		&j.QueuedAt, &j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.UpdatedAt,
		&nodeID, &nodeName, &nodeHost,
		&bestID, &bestVer, &bestYOLO,
		&lastID, &lastVer, &lastYOLO,
	)
	if err != nil {
		return Job{}, err
	}
	if len(dvRefRaw) > 0 {
		_ = json.Unmarshal(dvRefRaw, &j.DataVersion)
	}
	if j.DataVersion.ClassNames == nil {
		j.DataVersion.ClassNames = []string{}
	}
	if len(baseRefRaw) > 0 {
		var br BaseModelRef
		if json.Unmarshal(baseRefRaw, &br) == nil {
			j.BaseModel = &br
		}
	}
	j.Params = map[string]any{}
	if len(paramsRaw) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(paramsRaw)))
		dec.UseNumber()
		var rawMap map[string]any
		if dec.Decode(&rawMap) == nil {
			if norm, nerr := NormalizeParams(rawMap, j.InitMode); nerr == nil {
				j.Params = norm
			} else {
				j.Params = rawMap
			}
		}
	}
	if len(metricsRaw) > 0 {
		_ = json.Unmarshal(metricsRaw, &j.Metrics)
	}
	j.Artifacts = []Artifact{}
	if len(artRaw) > 0 {
		_ = json.Unmarshal(artRaw, &j.Artifacts)
		if j.Artifacts == nil {
			j.Artifacts = []Artifact{}
		}
	}
	if len(resultRaw) > 0 {
		j.Result = json.RawMessage(resultRaw)
	}
	if nodeID != nil && nodeName != nil && nodeHost != nil {
		j.Node = &NodeRef{ID: *nodeID, Name: *nodeName, Hostname: *nodeHost}
	}
	if bestID != nil && bestVer != nil && bestYOLO != nil {
		j.BestModel = &RegisteredModelRef{ID: *bestID, Version: *bestVer, YOLOVersion: *bestYOLO}
	}
	if lastID != nil && lastVer != nil && lastYOLO != nil {
		j.LastModel = &RegisteredModelRef{ID: *lastID, Version: *lastVer, YOLOVersion: *lastYOLO}
	}
	j.Epochs = []EpochMetrics{}
	j.QueuedAt = j.QueuedAt.UTC()
	j.CreatedAt = j.CreatedAt.UTC()
	j.UpdatedAt = j.UpdatedAt.UTC()
	if j.StartedAt != nil {
		t := j.StartedAt.UTC()
		j.StartedAt = &t
	}
	if j.FinishedAt != nil {
		t := j.FinishedAt.UTC()
		j.FinishedAt = &t
	}
	return j, nil
}

// Get returns a single training job including its full epoch history.
func (s *Service) Get(ctx context.Context, id int64) (Job, error) {
	j, err := scanJobRow(s.db.QueryRow(ctx, "SELECT "+jobSelectCols+jobSelectFrom+
		" WHERE j.id = $1 AND p.deleted_at IS NULL", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, errJobNotFound
	}
	if err != nil {
		return Job{}, err
	}
	jobs := []Job{j}
	if err := s.fillQueuePositions(ctx, jobs); err != nil {
		return Job{}, err
	}
	j = jobs[0]

	rows, err := s.db.Query(ctx, "SELECT metrics FROM training_job_epochs WHERE job_id = $1 ORDER BY epoch", id)
	if err != nil {
		return Job{}, err
	}
	epochs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EpochMetrics, error) {
		var raw []byte
		if err := row.Scan(&raw); err != nil {
			return EpochMetrics{}, err
		}
		var em EpochMetrics
		err := json.Unmarshal(raw, &em)
		return em, err
	})
	if err != nil {
		return Job{}, err
	}
	if epochs == nil {
		epochs = []EpochMetrics{}
	}
	j.Epochs = epochs
	return j, nil
}

// List returns training jobs matching f, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Job, error) {
	if f.Status != "" && f.Status != StatusQueued && f.Status != StatusRunning &&
		f.Status != StatusSucceeded && f.Status != StatusFailed && f.Status != StatusCanceled {
		return nil, newError(ErrInvalid, "训练任务状态筛选无效")
	}
	if f.YOLOVersion != "" {
		if _, ok := model.LookupYOLOVersion(f.YOLOVersion); !ok {
			return nil, newError(ErrInvalid, "不支持的 YOLO 版本筛选：%s", f.YOLOVersion)
		}
	}
	q := strings.TrimSpace(f.Search)
	rows, err := s.db.Query(ctx, "SELECT "+jobSelectCols+jobSelectFrom+`
		WHERE p.deleted_at IS NULL
			AND ($1::bigint = 0 OR j.project_id = $1::bigint)
			AND ($2::bigint = 0 OR j.data_version_id = $2::bigint)
			AND ($3 = '' OR j.status = $3)
			AND ($4 = '' OR j.yolo_version = $4)
			AND ($5 = '' OR strpos(lower(j.name), lower($5)) > 0
				OR strpos(lower(j.description), lower($5)) > 0
				OR strpos(lower(p.name), lower($5)) > 0)
		ORDER BY j.created_at DESC, j.id DESC`,
		f.ProjectID, f.DataVersionID, f.Status, f.YOLOVersion, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Job{}
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.fillQueuePositions(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) fillQueuePositions(ctx context.Context, jobs []Job) error {
	workers := s.WorkersOnline()
	needQueue := false
	for i := range jobs {
		jobs[i].WorkersOnline = workers
		if jobs[i].Status == StatusQueued {
			needQueue = true
		}
	}
	if !needQueue {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT j.id FROM training_jobs j
		JOIN projects p ON p.id = j.project_id AND p.deleted_at IS NULL
		WHERE j.status = 'queued'
		ORDER BY j.queued_at, j.id`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	posByID := make(map[int64]int, len(ids))
	for i, id := range ids {
		posByID[id] = i + 1
	}
	for i := range jobs {
		if pos, ok := posByID[jobs[i].ID]; ok {
			p := pos
			jobs[i].QueuePosition = &p
		}
	}
	return nil
}

// Update modifies the name and/or description of a training job.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (Job, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	name := cur.Name
	if in.Name != nil {
		n, err := normalizeJobName(*in.Name)
		if err != nil {
			return Job{}, err
		}
		if n == "" {
			return Job{}, newError(ErrInvalid, "任务名称不能为空")
		}
		name = n
	}
	desc := cur.Description
	if in.Description != nil {
		d, err := normalizeDescription(*in.Description)
		if err != nil {
			return Job{}, err
		}
		desc = d
	}
	tag, err := s.db.Exec(ctx, `UPDATE training_jobs SET name = $2, description = $3, updated_at = now()
		WHERE id = $1`, id, name, desc)
	if err != nil {
		return Job{}, err
	}
	if tag.RowsAffected() == 0 {
		return Job{}, errJobNotFound
	}
	return s.Get(ctx, id)
}

// Cancel stops a queued or running training job.
func (s *Service) Cancel(ctx context.Context, id int64) (Job, error) {
	tag, err := s.db.Exec(ctx, `UPDATE training_jobs
		SET status = 'canceled', stage = '', lease_expires_at = NULL, finished_at = now(), updated_at = now()
		WHERE id = $1 AND status IN ('queued', 'running')`, id)
	if err != nil {
		return Job{}, err
	}
	if tag.RowsAffected() == 0 {
		cur, err := s.Get(ctx, id)
		if err != nil {
			return Job{}, err
		}
		return Job{}, newError(ErrConflict, "训练任务当前状态为 %s，无需取消", cur.Status)
	}
	// Refresh artifacts on disk in case partial weights (e.g. last.pt) were uploaded before cancel.
	_ = s.refreshDiskArtifacts(ctx, id)
	s.log.Info("training job canceled", "job_id", id)
	return s.Get(ctx, id)
}

// Retry resets a failed or canceled training job and puts it at the end of the
// queue.
func (s *Service) Retry(ctx context.Context, id int64) (Job, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if cur.Status != StatusFailed && cur.Status != StatusCanceled {
		return Job{}, newError(ErrConflict, "只有失败或已取消的训练任务可以重新训练")
	}
	if !cur.DataVersionLive {
		return Job{}, newError(ErrConflict, "该任务关联的数据版本已被移入回收站，请先恢复数据版本后再重新训练")
	}
	if cur.InitMode != InitScratch {
		if cur.BaseModelID == nil {
			return Job{}, newError(ErrConflict, "基础模型已被删除，无法重新训练")
		}
		m, err := s.models.GetModel(ctx, *cur.BaseModelID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return Job{}, newError(ErrConflict, "基础模型已被删除，无法重新训练")
			}
			return Job{}, err
		}
		if m.FileMissing {
			return Job{}, newError(ErrConflict, "基础模型的权重文件缺失，无法重新训练")
		}
	}

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM training_job_epochs WHERE job_id = $1", id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE training_jobs
			SET status = 'queued', stage = '', node_id = NULL, attempts = 0, lease_expires_at = NULL,
				current_epoch = 0, batch_done = 0, batch_total = 0, eta_seconds = NULL,
				metrics = '{}', artifacts = '[]', error = '', error_detail = '', result = NULL,
				queued_at = now(), started_at = NULL, finished_at = NULL, updated_at = now()
			WHERE id = $1 AND status IN ('failed', 'canceled')`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return newError(ErrConflict, "训练任务状态已变化，请刷新重试")
		}
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	_ = os.RemoveAll(s.jobDir(id))
	s.notifyTasks()
	s.log.Info("training job retried", "job_id", id)
	return s.Get(ctx, id)
}

// Delete removes a training job and its run directory (runs/job-<id>/). Any
// models already registered into the model repository keep their own copies
// under models/.
func (s *Service) Delete(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM training_jobs j
		USING projects p
		WHERE j.id = $1 AND p.id = j.project_id AND p.deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errJobNotFound
	}
	_ = os.RemoveAll(s.jobDir(id))
	s.log.Info("training job deleted", "job_id", id)
	return nil
}

// DeleteProjectJobs deletes all training jobs belonging to projectID (stopping
// any running ones via task abandonment) and removes their run directories.
func (s *Service) DeleteProjectJobs(ctx context.Context, projectID int64) error {
	rows, err := s.db.Query(ctx, "DELETE FROM training_jobs WHERE project_id = $1 RETURNING id", projectID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		_ = os.RemoveAll(s.jobDir(id))
	}
	if len(ids) > 0 {
		s.log.Info("project training jobs deleted", "project_id", projectID, "count", len(ids))
	}
	return nil
}

// RegisterModel adds a completed (or stopped) job's best.pt or last.pt into
// the project's model repository via model.Service.RegisterTrainedModel.
func (s *Service) RegisterModel(ctx context.Context, jobID int64, in RegisterModelInput) (model.Model, error) {
	variant := strings.TrimSpace(in.Variant)
	if variant == "" {
		variant = VariantBest
	}
	if variant != VariantBest && variant != VariantLast {
		return model.Model{}, newError(ErrInvalid, "权重类型必须是 best 或 last")
	}
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return model.Model{}, err
	}
	if job.Status == StatusQueued || job.Status == StatusRunning {
		return model.Model{}, newError(ErrConflict, "训练任务尚未结束，请等待完成或先停止任务后再注册模型")
	}
	if variant == VariantBest && job.BestModel != nil {
		return model.Model{}, newError(ErrConflict, "最佳权重（best.pt）已注册为模型 v%d", job.BestModel.Version)
	}
	if variant == VariantLast && job.LastModel != nil {
		return model.Model{}, newError(ErrConflict, "最终检查点（last.pt）已注册为模型 v%d", job.LastModel.Version)
	}

	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		if variant == VariantBest {
			desc = fmt.Sprintf("训练任务 #%d（%s）产出的最佳权重", job.ID, job.Name)
		} else {
			desc = fmt.Sprintf("训练任务 #%d（%s）产出的检查点权重（可用于续训）", job.ID, job.Name)
		}
	}
	return s.registerWeights(ctx, job, variant, desc)
}

func (s *Service) registerWeights(ctx context.Context, job Job, variant, desc string) (model.Model, error) {
	fileBase := "best.pt"
	col := "best_model_id"
	if variant == VariantLast {
		fileBase = "last.pt"
		col = "last_model_id"
	}
	weightsPath := path.Join(jobDirRel(job.ID), "weights", fileBase)
	arch := strings.ToLower(yoloDisplayLabel(job.YOLOVersion, job.ModelScale))
	displayFileName := fmt.Sprintf("%s-job%d-%s", arch, job.ID, fileBase)

	registered, err := s.models.RegisterTrainedModel(ctx, model.TrainedModelInput{
		ProjectID:     job.ProjectID,
		TrainingJobID: job.ID,
		YOLOVersion:   job.YOLOVersion,
		Description:   desc,
		WeightsPath:   weightsPath,
		FileName:      displayFileName,
	})
	if err != nil {
		var me *model.Error
		if errors.As(err, &me) {
			switch {
			case errors.Is(me, model.ErrNotFound):
				return model.Model{}, newError(ErrNotFound, "%s", me.Message)
			case errors.Is(me, model.ErrConflict):
				return model.Model{}, newError(ErrConflict, "%s", me.Message)
			default:
				return model.Model{}, newError(ErrInvalid, "%s", me.Message)
			}
		}
		return model.Model{}, err
	}

	if _, err := s.db.Exec(ctx, fmt.Sprintf("UPDATE training_jobs SET %s = $2, updated_at = now() WHERE id = $1", col),
		job.ID, registered.ID); err != nil {
		s.log.Warn("record registered model on training job", "job_id", job.ID, "model_id", registered.ID, "err", err)
	}
	return registered, nil
}

// ReadLogs reads up to limit bytes of train.log starting at byte offset. When
// offset < 0, it reads the last limit bytes of the file.
func (s *Service) ReadLogs(ctx context.Context, id int64, offset int64, limit int) (LogSlice, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return LogSlice{}, err
	}
	if limit <= 0 || limit > MaxLogReadBytes {
		limit = DefaultLogReadBytes
	}
	f, err := os.Open(s.jobLogPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LogSlice{Content: "", Offset: 0, NextOffset: 0, TotalBytes: 0}, nil
		}
		return LogSlice{}, fmt.Errorf("open training log: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return LogSlice{}, fmt.Errorf("stat training log: %w", err)
	}
	total := fi.Size()
	truncated := false
	if offset < 0 {
		if total > int64(limit) {
			offset = total - int64(limit)
			truncated = true
		} else {
			offset = 0
		}
	}
	if offset >= total {
		return LogSlice{Content: "", Offset: total, NextOffset: total, TotalBytes: total}, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return LogSlice{}, fmt.Errorf("seek training log: %w", err)
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return LogSlice{}, fmt.Errorf("read training log: %w", err)
	}
	return LogSlice{
		Content:    strings.ToValidUTF8(string(buf[:n]), "\uFFFD"),
		Offset:     offset,
		NextOffset: offset + int64(n),
		TotalBytes: total,
		Truncated:  truncated,
	}, nil
}

// cleanArtifactRel validates a slash-separated artifact path under runs/job-<id>/.
func cleanArtifactRel(rel string) (string, string, error) {
	rel = strings.TrimSpace(strings.TrimPrefix(rel, "/"))
	if rel == "" || strings.Contains(rel, `\`) {
		return "", "", newError(ErrInvalid, "训练产物路径无效")
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned != rel || strings.HasPrefix(cleaned, "..") {
		return "", "", newError(ErrInvalid, "训练产物路径无效")
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) > 2 {
		return "", "", newError(ErrInvalid, "训练产物路径层级无效")
	}
	if len(parts) == 2 && parts[0] != "weights" {
		return "", "", newError(ErrInvalid, "仅允许访问 weights/ 子目录下的权重文件")
	}
	ext := strings.ToLower(path.Ext(cleaned))
	switch ext {
	case ".pt":
		if len(parts) != 2 || parts[0] != "weights" {
			return "", "", newError(ErrInvalid, "权重文件必须位于 weights/ 目录下")
		}
		return cleaned, "weight", nil
	case ".png", ".jpg", ".jpeg":
		return cleaned, "plot", nil
	case ".csv":
		return cleaned, "csv", nil
	case ".yaml", ".yml":
		return cleaned, "yaml", nil
	case ".json", ".txt", ".log":
		return cleaned, "log", nil
	default:
		return "", "", newError(ErrInvalid, "不支持的训练产物文件类型：%s", ext)
	}
}

// OpenArtifact opens a saved run artifact (plot image, CSV, YAML, or weights)
// for HTTP download or inline viewing.
func (s *Service) OpenArtifact(ctx context.Context, id int64, relPath string) (*ArtifactFile, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	cleaned, _, err := cleanArtifactRel(relPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.jobDir(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errArtifactNotFound
		}
		return nil, fmt.Errorf("open job run folder: %w", err)
	}
	f, err := root.Open(filepath.FromSlash(cleaned))
	if err != nil {
		_ = root.Close()
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errArtifactNotFound
		}
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("stat artifact: %w", err)
		}
		return nil, errArtifactNotFound
	}
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(cleaned)))
	if ct == "" {
		switch strings.ToLower(path.Ext(cleaned)) {
		case ".yaml", ".yml":
			ct = "text/yaml; charset=utf-8"
		case ".csv":
			ct = "text/csv; charset=utf-8"
		case ".log", ".txt":
			ct = "text/plain; charset=utf-8"
		default:
			ct = "application/octet-stream"
		}
	}
	return &ArtifactFile{
		Content:     f,
		Name:        path.Base(cleaned),
		ContentType: ct,
		Size:        fi.Size(),
		ModTime:     fi.ModTime(),
		CloseFunc: func() error {
			err1 := f.Close()
			err2 := root.Close()
			return errors.Join(err1, err2)
		},
	}, nil
}

// scanJobArtifacts enumerates valid artifacts currently saved in runs/job-<id>/.
func (s *Service) scanJobArtifacts(jobID int64) []Artifact {
	dir := s.jobDir(jobID)
	var list []Artifact
	addDir := func(sub string) {
		target := dir
		if sub != "" {
			target = filepath.Join(dir, sub)
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return
		}
		for _, ent := range entries {
			if ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
				continue
			}
			rel := ent.Name()
			if sub != "" {
				rel = sub + "/" + ent.Name()
			}
			if rel == "train.log" {
				continue
			}
			cleaned, kind, err := cleanArtifactRel(rel)
			if err != nil {
				continue
			}
			fi, err := ent.Info()
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			list = append(list, Artifact{
				Name:      ent.Name(),
				RelPath:   cleaned,
				Kind:      kind,
				SizeBytes: fi.Size(),
			})
		}
	}
	addDir("weights")
	addDir("")
	slices.SortFunc(list, func(a, b Artifact) int {
		order := func(k string) int {
			switch k {
			case "weight":
				return 0
			case "plot":
				return 1
			case "csv":
				return 2
			case "yaml":
				return 3
			default:
				return 4
			}
		}
		if oa, ob := order(a.Kind), order(b.Kind); oa != ob {
			return oa - ob
		}
		return strings.Compare(a.RelPath, b.RelPath)
	})
	if list == nil {
		list = []Artifact{}
	}
	return list
}

func (s *Service) refreshDiskArtifacts(ctx context.Context, jobID int64) error {
	list := s.scanJobArtifacts(jobID)
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, "UPDATE training_jobs SET artifacts = $2, updated_at = now() WHERE id = $1", jobID, raw)
	return err
}
