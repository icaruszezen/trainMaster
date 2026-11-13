package training

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
)

func leaseSeconds() int { return int(model.LeaseDuration / time.Second) }

// Kind returns "training_job" (implementing model.TaskSource).
func (s *Service) Kind() string { return TaskKind }

// Claim assigns the oldest queued training job to nodeID (implementing
// model.TaskSource). Jobs whose data version or base model became unavailable
// while queued fail on the spot so the node can claim the next job.
func (s *Service) Claim(ctx context.Context, nodeID int64) (any, error) {
	if err := s.expireLeases(ctx); err != nil {
		s.log.Warn("expire leases before claim", "err", err)
	}
	for range maxClaimSkips {
		task, skipped, err := s.claimOne(ctx, nodeID)
		switch {
		case err != nil:
			return nil, err
		case task != nil:
			return task, nil
		case !skipped:
			return nil, nil
		}
	}
	return nil, nil
}

func (s *Service) claimOne(ctx context.Context, nodeID int64) (task *Task, skipped bool, err error) {
	var failedID int64
	var failMsg string

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var (
			id          int64
			projectID   int64
			dvID        int64
			dvRefRaw    []byte
			initMode    string
			yoloVersion string
			modelScale  string
			jobTask     string
			baseModelID *int64
			baseRefRaw  []byte
			paramsRaw   []byte
		)
		err := tx.QueryRow(ctx, `SELECT j.id, j.project_id, j.data_version_id, j.data_version_ref,
				j.init_mode, j.yolo_version, j.model_scale, j.task,
				j.base_model_id, j.base_model_ref, j.params
			FROM training_jobs j
			JOIN projects p ON p.id = j.project_id AND p.deleted_at IS NULL
			WHERE j.status = 'queued'
			ORDER BY j.queued_at, j.id
			FOR UPDATE OF j SKIP LOCKED LIMIT 1`).Scan(
			&id, &projectID, &dvID, &dvRefRaw,
			&initMode, &yoloVersion, &modelScale, &jobTask,
			&baseModelID, &baseRefRaw, &paramsRaw,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		// Verify data version is still ready and live.
		var dvStatus, dirName string
		var dvDeleted bool
		err = tx.QueryRow(ctx, "SELECT status, dir_name, (deleted_at IS NOT NULL) FROM data_versions WHERE id = $1", dvID).
			Scan(&dvStatus, &dirName, &dvDeleted)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || dvDeleted:
			failMsg = "关联的数据版本已被删除"
		case err != nil:
			return err
		case dvStatus != dataversion.StatusReady:
			failMsg = "关联的数据版本未处于就绪状态"
		}

		var baseRef *BaseModelRef
		if failMsg == "" && initMode != InitScratch {
			if baseModelID == nil {
				failMsg = "关联的基础模型已被删除"
			} else {
				m, merr := s.models.GetModel(ctx, *baseModelID)
				switch {
				case errors.Is(merr, model.ErrNotFound):
					failMsg = "关联的基础模型已被删除"
				case merr != nil:
					return merr
				case m.FileMissing:
					failMsg = "关联的基础模型权重文件缺失"
				default:
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
			}
		}

		if failMsg != "" {
			failedID, skipped = id, true
			_, err := tx.Exec(ctx, `UPDATE training_jobs
				SET status = 'failed', stage = '', lease_expires_at = NULL,
					error = $2, error_detail = '', finished_at = now(), updated_at = now()
				WHERE id = $1`, id, failMsg)
			return err
		}

		if _, err := tx.Exec(ctx, `UPDATE training_jobs
			SET status = 'running', stage = 'preparing', node_id = $2,
				attempts = attempts + 1,
				lease_expires_at = now() + make_interval(secs => $3),
				started_at = coalesce(started_at, now()),
				error = '', error_detail = '', finished_at = NULL, updated_at = now()
			WHERE id = $1`, id, nodeID, model.LeaseDuration.Seconds()); err != nil {
			return err
		}

		var dvRef DataVersionRef
		_ = json.Unmarshal(dvRefRaw, &dvRef)
		if dirName != "" {
			dvRef.DirName = dirName
		}
		if dvRef.ClassNames == nil {
			dvRef.ClassNames = []string{}
		}

		dec := json.NewDecoder(strings.NewReader(string(paramsRaw)))
		dec.UseNumber()
		var rawMap map[string]any
		_ = dec.Decode(&rawMap)
		params, nerr := NormalizeParams(rawMap, initMode)
		if nerr != nil {
			params = rawMap
		}

		var archYAML string
		if initMode == InitScratch {
			archYAML, _ = ArchYAML(yoloVersion, modelScale)
		}

		base := fmt.Sprintf("/api/v1/compute/agent/training-jobs/%d/", id)
		t := &Task{
			Kind:         TaskKind,
			ID:           id,
			LeaseSeconds: leaseSeconds(),
			ProjectID:    projectID,
			InitMode:     initMode,
			YOLOVersion:  yoloVersion,
			ModelScale:   modelScale,
			ArchYAML:     archYAML,
			Task:         jobTask,
			DataVersion:  dvRef,
			BaseModel:    baseRef,
			Params:       params,
			DatasetURL:   base + "dataset",
			ProgressURL:  base + "progress",
			ArtifactsURL: base + "artifacts",
			CompleteURL:  base + "complete",
			FailURL:      base + "fail",
		}
		if baseRef != nil {
			t.BaseModelURL = base + "base-model"
		}
		task = t
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("claim training job: %w", err)
	}
	if skipped {
		s.log.Warn("queued training job failed on claim", "job_id", failedID, "error", failMsg)
	}
	if task != nil {
		if err := os.MkdirAll(filepath.Join(s.jobDir(task.ID), "weights"), 0o755); err != nil {
			s.log.Warn("prepare job run dir", "job_id", task.ID, "err", err)
		}
		s.models.TouchWorker(nodeID, TaskKind)
		s.log.Info("training job claimed", "job_id", task.ID, "node_id", nodeID, "yolo_version", task.YOLOVersion,
			"init_mode", task.InitMode)
	}
	return task, skipped, nil
}

func (s *Service) renewLease(ctx context.Context, q querier, jobID, nodeID int64, stage string) error {
	if stage != "" && stage != StagePreparing && stage != StageDownloading &&
		stage != StageTraining && stage != StageUploading {
		return newError(ErrInvalid, "无效的训练阶段：%s", stage)
	}
	tag, err := q.Exec(ctx, `UPDATE training_jobs
		SET lease_expires_at = now() + make_interval(secs => $3),
			stage = coalesce(nullif($4, ''), stage),
			updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`,
		jobID, nodeID, model.LeaseDuration.Seconds(), stage)
	if err != nil {
		return err
	}
	s.models.TouchWorker(nodeID, TaskKind)
	if tag.RowsAffected() == 0 {
		return s.taskError(ctx, q, jobID)
	}
	return nil
}

func (s *Service) taskError(ctx context.Context, q querier, jobID int64) error {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM training_jobs WHERE id = $1)", jobID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errTaskGone
	}
	return errTaskNotYours
}

// OpenTaskDataset opens a streaming YOLO zip archive of the job's data version
// for remote compute nodes that do not share the server's /data directory.
func (s *Service) OpenTaskDataset(ctx context.Context, jobID, nodeID int64) (*dataversion.Export, error) {
	if err := s.renewLease(ctx, s.db, jobID, nodeID, StageDownloading); err != nil {
		return nil, err
	}
	var dvID int64
	if err := s.db.QueryRow(ctx, "SELECT data_version_id FROM training_jobs WHERE id = $1", jobID).Scan(&dvID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errTaskGone
		}
		return nil, err
	}
	ticket, err := s.dataVersions.CreateExport(ctx, dvID, dataversion.ExportInput{
		Format:           dataversion.FormatYOLO,
		IncludeAugmented: true,
	})
	if err != nil {
		return nil, err
	}
	return s.dataVersions.OpenExport(ctx, dvID, ticket.Token)
}

// OpenTaskBaseModel opens the base .pt weights file for a finetune or resume
// job held by nodeID.
func (s *Service) OpenTaskBaseModel(ctx context.Context, jobID, nodeID int64) (*model.File, error) {
	if err := s.renewLease(ctx, s.db, jobID, nodeID, StageDownloading); err != nil {
		return nil, err
	}
	var baseModelID *int64
	if err := s.db.QueryRow(ctx, "SELECT base_model_id FROM training_jobs WHERE id = $1", jobID).Scan(&baseModelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errTaskGone
		}
		return nil, err
	}
	if baseModelID == nil {
		return nil, errBaseModelNotFound
	}
	f, err := s.models.OpenModelFile(ctx, *baseModelID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, errBaseModelNotFound
		}
		return nil, err
	}
	return f, nil
}

func cleanMetricPtr(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	val := math.Round(*v*1e6) / 1e6
	return &val
}

func sanitizeEpoch(e EpochMetrics) EpochMetrics {
	e.TrainBoxLoss = cleanMetricPtr(e.TrainBoxLoss)
	e.TrainClsLoss = cleanMetricPtr(e.TrainClsLoss)
	e.TrainDFLLoss = cleanMetricPtr(e.TrainDFLLoss)
	e.ValBoxLoss = cleanMetricPtr(e.ValBoxLoss)
	e.ValClsLoss = cleanMetricPtr(e.ValClsLoss)
	e.ValDFLLoss = cleanMetricPtr(e.ValDFLLoss)
	e.Precision = cleanMetricPtr(e.Precision)
	e.Recall = cleanMetricPtr(e.Recall)
	e.MAP50 = cleanMetricPtr(e.MAP50)
	e.MAP50_95 = cleanMetricPtr(e.MAP50_95)
	e.Fitness = cleanMetricPtr(e.Fitness)
	e.LR = cleanMetricPtr(e.LR)
	e.GPUMemGB = cleanMetricPtr(e.GPUMemGB)
	e.DurationSec = cleanMetricPtr(e.DurationSec)
	return e
}

func mergeSummary(cur SummaryMetrics, e EpochMetrics) SummaryMetrics {
	if e.TrainBoxLoss != nil {
		cur.TrainBoxLoss = e.TrainBoxLoss
	}
	if e.TrainClsLoss != nil {
		cur.TrainClsLoss = e.TrainClsLoss
	}
	if e.TrainDFLLoss != nil {
		cur.TrainDFLLoss = e.TrainDFLLoss
	}
	if e.ValBoxLoss != nil {
		cur.ValBoxLoss = e.ValBoxLoss
	}
	if e.ValClsLoss != nil {
		cur.ValClsLoss = e.ValClsLoss
	}
	if e.ValDFLLoss != nil {
		cur.ValDFLLoss = e.ValDFLLoss
	}
	if e.Precision != nil {
		cur.Precision = e.Precision
	}
	if e.Recall != nil {
		cur.Recall = e.Recall
	}
	if e.MAP50 != nil {
		cur.MAP50 = e.MAP50
		if cur.BestMAP50 == nil || *e.MAP50 >= *cur.BestMAP50 {
			v := *e.MAP50
			cur.BestMAP50 = &v
		}
	}
	if e.MAP50_95 != nil {
		cur.MAP50_95 = e.MAP50_95
		if cur.BestMAP50_95 == nil || *e.MAP50_95 >= *cur.BestMAP50_95 {
			v := *e.MAP50_95
			cur.BestMAP50_95 = &v
		}
	}
	if e.LR != nil {
		cur.LR = e.LR
	}
	if e.GPUMemGB != nil {
		cur.GPUMemGB = e.GPUMemGB
	}

	// Fitness in Ultralytics detection defaults to mAP50-95 (or 0.1*mAP50 + 0.9*mAP50-95).
	fit := e.Fitness
	if fit == nil && e.MAP50_95 != nil {
		v := *e.MAP50_95
		if e.MAP50 != nil {
			v = math.Round((0.1*(*e.MAP50)+0.9*(*e.MAP50_95))*1e6) / 1e6
		}
		fit = &v
	}
	if fit != nil {
		cur.Fitness = fit
		if cur.BestFitness == nil || *fit >= *cur.BestFitness {
			bf := *fit
			ep := e.Epoch
			cur.BestFitness = &bf
			cur.BestEpoch = &ep
		}
	}
	return cur
}

// ReportProgress renews the node's lease and persists stage, epoch/batch
// progress, completed epoch metrics, and log output. If the job was canceled or
// deleted, it returns 409/404 so the compute node terminates training at once.
func (s *Service) ReportProgress(ctx context.Context, jobID, nodeID int64, in ProgressInput) (int, error) {
	if in.Stage != "" && in.Stage != StagePreparing && in.Stage != StageDownloading &&
		in.Stage != StageTraining && in.Stage != StageUploading {
		return 0, newError(ErrInvalid, "无效的训练阶段：%s", in.Stage)
	}

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var (
			status       string
			holder       *int64
			curEpoch     int
			totEpochs    int
			batchDone    int
			batchTotal   int
			etaSeconds   *int
			metricsBytes []byte
		)
		err := tx.QueryRow(ctx, `SELECT status, node_id, current_epoch, total_epochs, batch_done, batch_total, eta_seconds, metrics
			FROM training_jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(
			&status, &holder, &curEpoch, &totEpochs, &batchDone, &batchTotal, &etaSeconds, &metricsBytes,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return errTaskGone
		}
		if err != nil {
			return err
		}
		if status != StatusRunning || holder == nil || *holder != nodeID {
			return errTaskNotYours
		}

		var summary SummaryMetrics
		if len(metricsBytes) > 0 {
			_ = json.Unmarshal(metricsBytes, &summary)
		}

		if in.CurrentEpoch != nil && *in.CurrentEpoch >= 0 {
			curEpoch = *in.CurrentEpoch
		}
		if in.TotalEpochs != nil && *in.TotalEpochs > 0 {
			totEpochs = *in.TotalEpochs
		}
		if in.BatchDone != nil && *in.BatchDone >= 0 {
			batchDone = *in.BatchDone
		}
		if in.BatchTotal != nil && *in.BatchTotal >= 0 {
			batchTotal = *in.BatchTotal
		}
		if in.ETASeconds != nil {
			if *in.ETASeconds >= 0 {
				v := *in.ETASeconds
				etaSeconds = &v
			} else {
				etaSeconds = nil
			}
		}

		for _, rawEp := range in.Epochs {
			if rawEp.Epoch < 1 {
				continue
			}
			ep := sanitizeEpoch(rawEp)
			if ep.TotalEpochs <= 0 {
				ep.TotalEpochs = totEpochs
			}
			if ep.Epoch > curEpoch {
				curEpoch = ep.Epoch
			}
			summary = mergeSummary(summary, ep)
			epJSON, err := json.Marshal(ep)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO training_job_epochs (job_id, epoch, metrics)
				VALUES ($1, $2, $3)
				ON CONFLICT (job_id, epoch) DO UPDATE SET metrics = EXCLUDED.metrics`,
				jobID, ep.Epoch, epJSON); err != nil {
				return err
			}
		}

		summaryJSON, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE training_jobs
			SET lease_expires_at = now() + make_interval(secs => $2),
				stage = coalesce(nullif($3, ''), stage),
				current_epoch = $4,
				total_epochs = $5,
				batch_done = $6,
				batch_total = $7,
				eta_seconds = $8,
				metrics = $9,
				updated_at = now()
			WHERE id = $1`,
			jobID, model.LeaseDuration.Seconds(), in.Stage,
			curEpoch, totEpochs, batchDone, batchTotal, etaSeconds, summaryJSON)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.models.TouchWorker(nodeID, TaskKind)

	if in.LogChunk != "" {
		s.appendLog(jobID, in.LogChunk)
	}
	return leaseSeconds(), nil
}

func (s *Service) appendLog(jobID int64, chunk string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()

	if err := os.MkdirAll(s.jobDir(jobID), 0o755); err != nil {
		s.log.Warn("mkdir for training log", "job_id", jobID, "err", err)
		return
	}
	logPath := s.jobLogPath(jobID)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		s.log.Warn("open training log for append", "job_id", jobID, "err", err)
		return
	}
	defer f.Close()

	if fi, err := f.Stat(); err == nil && fi.Size() >= MaxLogFileBytes {
		return
	} else if err == nil && fi.Size()+int64(len(chunk)) > MaxLogFileBytes {
		chunk = chunk[:MaxLogFileBytes-fi.Size()]
	}
	_, _ = f.WriteString(strings.ToValidUTF8(chunk, "\uFFFD"))
}

// UploadArtifact writes an uploaded weight, plot, CSV, or YAML file atomically
// under runs/job-<id>/<relPath> while keeping the node's lease alive.
func (s *Service) UploadArtifact(ctx context.Context, jobID, nodeID int64, relPath string, body io.Reader) (Artifact, error) {
	cleaned, kind, err := cleanArtifactRel(relPath)
	if err != nil {
		return Artifact{}, err
	}
	if err := s.renewLease(ctx, s.db, jobID, nodeID, StageUploading); err != nil {
		return Artifact{}, err
	}

	maxBytes := int64(MaxPlotArtifactBytes)
	if kind == "weight" {
		maxBytes = model.MaxModelBytes
	}

	dstPath := filepath.Join(s.jobDir(jobID), filepath.FromSlash(cleaned))
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return Artifact{}, fmt.Errorf("create artifact folder: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".upload-*")
	if err != nil {
		return Artifact{}, fmt.Errorf("create temp artifact: %w", err)
	}
	tmpName := tmp.Name()
	_ = tmp.Chmod(0o644)
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	lastRenew := time.Now()
	buf := make([]byte, 64*1024)
	var written int64
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > maxBytes {
				_ = tmp.Close()
				return Artifact{}, newError(ErrInvalid, "训练产物文件 %s 超出大小限制", cleaned)
			}
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				_ = tmp.Close()
				return Artifact{}, fmt.Errorf("write artifact: %w", werr)
			}
			if time.Since(lastRenew) >= 15*time.Second {
				if err := s.renewLease(ctx, s.db, jobID, nodeID, StageUploading); err != nil {
					_ = tmp.Close()
					return Artifact{}, err
				}
				lastRenew = time.Now()
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = tmp.Close()
			return Artifact{}, fmt.Errorf("read uploaded artifact: %w", rerr)
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Artifact{}, fmt.Errorf("sync artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Artifact{}, fmt.Errorf("close artifact: %w", err)
	}

	// Re-check lease right before committing file to runs/job-<id>/.
	if err := s.renewLease(ctx, s.db, jobID, nodeID, StageUploading); err != nil {
		return Artifact{}, err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return Artifact{}, fmt.Errorf("rename artifact: %w", err)
	}
	tmpName = ""

	_ = s.refreshDiskArtifacts(ctx, jobID)
	return Artifact{
		Name:      filepath.Base(dstPath),
		RelPath:   cleaned,
		Kind:      kind,
		SizeBytes: written,
	}, nil
}

// CompleteTask finishes a running training job held by nodeID and auto-registers
// weights/best.pt into the model repository when auto_register is enabled.
func (s *Service) CompleteTask(ctx context.Context, jobID, nodeID int64, result json.RawMessage) (Job, error) {
	artifacts := s.scanJobArtifacts(jobID)
	artJSON, err := json.Marshal(artifacts)
	if err != nil {
		return Job{}, err
	}
	var resVal any
	if len(result) > 0 && json.Valid(result) {
		resVal = []byte(result)
	}

	var autoRegister bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		var holder *int64
		err := tx.QueryRow(ctx, "SELECT status, node_id, auto_register FROM training_jobs WHERE id = $1 FOR UPDATE", jobID).
			Scan(&status, &holder, &autoRegister)
		if errors.Is(err, pgx.ErrNoRows) {
			return errTaskGone
		}
		if err != nil {
			return err
		}
		if status != StatusRunning || holder == nil || *holder != nodeID {
			return errTaskNotYours
		}
		_, err = tx.Exec(ctx, `UPDATE training_jobs
			SET status = 'succeeded', stage = '', lease_expires_at = NULL,
				eta_seconds = NULL, artifacts = $2, result = $3,
				error = '', error_detail = '',
				finished_at = now(), updated_at = now()
			WHERE id = $1`, jobID, artJSON, resVal)
		return err
	})
	if err != nil {
		return Job{}, err
	}
	s.models.TouchWorker(nodeID, TaskKind)

	job, err := s.Get(ctx, jobID)
	if err != nil {
		return Job{}, err
	}
	if autoRegister && job.BestModel == nil {
		bestPath := filepath.Join(s.jobDir(jobID), "weights", "best.pt")
		if fi, serr := os.Stat(bestPath); serr == nil && fi.Mode().IsRegular() {
			desc := fmt.Sprintf("训练任务 #%d（%s）自动注册的最佳权重", job.ID, job.Name)
			if _, rerr := s.registerWeights(ctx, job, VariantBest, desc); rerr != nil {
				s.log.Warn("auto-register best.pt failed", "job_id", jobID, "err", rerr)
			} else {
				job, _ = s.Get(ctx, jobID)
			}
		}
	}
	s.log.Info("training job succeeded", "job_id", jobID, "node_id", nodeID)
	return job, nil
}

// FailTask marks a running training job as failed (or requeues it when
// Retryable is true and attempts < maxAttempts).
func (s *Service) FailTask(ctx context.Context, jobID, nodeID int64, in FailInput) (Job, error) {
	errMsg := strings.TrimSpace(in.Error)
	if errMsg == "" {
		errMsg = "训练节点执行训练失败"
	}
	if len([]rune(errMsg)) > 500 {
		errMsg = string([]rune(errMsg)[:500])
	}
	detail := strings.TrimSpace(in.Detail)
	if len(detail) > 16*1024 {
		detail = detail[len(detail)-16*1024:]
	}
	if detail != "" {
		s.appendLog(jobID, "\n[ERROR] "+detail+"\n")
	}

	artifacts := s.scanJobArtifacts(jobID)
	artJSON, _ := json.Marshal(artifacts)

	requeued := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		var holder *int64
		var attempts int
		err := tx.QueryRow(ctx, "SELECT status, node_id, attempts FROM training_jobs WHERE id = $1 FOR UPDATE", jobID).
			Scan(&status, &holder, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return errTaskGone
		}
		if err != nil {
			return err
		}
		if status != StatusRunning || holder == nil || *holder != nodeID {
			return errTaskNotYours
		}
		if in.Retryable && attempts < maxAttempts {
			requeued = true
			_, err = tx.Exec(ctx, `UPDATE training_jobs
				SET status = 'queued', stage = '', node_id = NULL, lease_expires_at = NULL,
					queued_at = now(), updated_at = now()
				WHERE id = $1`, jobID)
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE training_jobs
			SET status = 'failed', stage = '', lease_expires_at = NULL,
				artifacts = $2, error = $3, error_detail = $4,
				finished_at = now(), updated_at = now()
			WHERE id = $1`, jobID, artJSON, errMsg, detail)
		return err
	})
	if err != nil {
		return Job{}, err
	}
	s.models.TouchWorker(nodeID, TaskKind)
	if requeued {
		s.notifyTasks()
	}
	s.log.Warn("training job failed", "job_id", jobID, "node_id", nodeID, "error", errMsg, "retryable", in.Retryable)
	return s.Get(ctx, jobID)
}
