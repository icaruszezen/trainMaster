package evaluation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

const leaseRenewEvery = 30 * time.Second

func leaseSeconds() int { return int(model.LeaseDuration / time.Second) }

// Kind returns "model_evaluation" (implementing model.TaskSource).
func (s *Service) Kind() string { return TaskKind }

// Claim assigns the oldest queued evaluation task to nodeID (implementing
// model.TaskSource). Evaluations whose model or data version disappeared while
// queued fail immediately so the node can claim the next task.
func (s *Service) Claim(ctx context.Context, nodeID int64) (any, error) {
	if err := s.expireLeases(ctx); err != nil {
		s.log.Warn("expire evaluation leases before claim", "err", err)
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
			id            int64
			projectID     int64
			modelID       *int64
			modelRefRaw   []byte
			sourceType    string
			dvID          *int64
			sourceRefRaw  []byte
			optsRaw       []byte
			classesRaw    []byte
			progressTotal int
		)
		err := tx.QueryRow(ctx, `SELECT e.id, e.project_id, e.model_id, e.model_ref,
				e.source_type, e.data_version_id, e.source_ref,
				e.options, e.classes, e.progress_total
			FROM evaluations e
			JOIN projects p ON p.id = e.project_id AND p.deleted_at IS NULL
			WHERE e.status = 'queued'
			ORDER BY e.queued_at, e.id
			FOR UPDATE OF e SKIP LOCKED LIMIT 1`).Scan(
			&id, &projectID, &modelID, &modelRefRaw,
			&sourceType, &dvID, &sourceRefRaw,
			&optsRaw, &classesRaw, &progressTotal,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		// Verify model is still live and its weights exist.
		if modelID == nil {
			failMsg = "关联的模型已被删除，无法执行评估"
		} else if s.models != nil {
			m, merr := s.models.GetModel(ctx, *modelID)
			switch {
			case errors.Is(merr, model.ErrNotFound):
				failMsg = "关联的模型不存在或已移入回收站"
			case merr != nil:
				return merr
			case m.FileMissing:
				failMsg = "关联的模型权重文件缺失，无法执行评估"
			}
		}

		// For DataVersion sources, verify the data version is still ready.
		var dvDirName string
		var dvStorageNodeID, dvTotalBytes int64
		if failMsg == "" && sourceType == SourceDataVersion {
			if dvID == nil {
				failMsg = "关联的数据版本已被删除"
			} else {
				var dvStatus string
				var dvDeleted bool
				qerr := tx.QueryRow(ctx,
					"SELECT status, dir_name, (deleted_at IS NOT NULL), storage_node_id, total_bytes FROM data_versions WHERE id = $1",
					*dvID,
				).Scan(&dvStatus, &dvDirName, &dvDeleted, &dvStorageNodeID, &dvTotalBytes)
				switch {
				case errors.Is(qerr, pgx.ErrNoRows) || dvDeleted:
					failMsg = "关联的数据版本已被删除"
				case qerr != nil:
					return qerr
				case dvStatus != dataversion.StatusReady:
					failMsg = "关联的数据版本未处于就绪状态"
				}
			}
		}

		if failMsg != "" {
			failedID, skipped = id, true
			_, uerr := tx.Exec(ctx, `UPDATE evaluations
				SET status = 'failed', stage = '', node_id = NULL, lease_expires_at = NULL,
					error = $2, error_detail = '', finished_at = now(), updated_at = now()
				WHERE id = $1`, id, failMsg)
			return uerr
		}

		if _, err := tx.Exec(ctx, `UPDATE evaluations
			SET status = 'running', stage = $2, node_id = $3,
				attempts = attempts + 1,
				lease_expires_at = now() + make_interval(secs => $4),
				started_at = coalesce(started_at, now()),
				error = '', error_detail = '', finished_at = NULL, updated_at = now()
			WHERE id = $1`, id, StagePreparing, nodeID, model.LeaseDuration.Seconds()); err != nil {
			return err
		}

		var modelRef ModelRef
		_ = json.Unmarshal(modelRefRaw, &modelRef)
		if modelRef.ClassNames == nil {
			modelRef.ClassNames = []string{}
		}
		var sourceRef SourceRef
		_ = json.Unmarshal(sourceRefRaw, &sourceRef)
		var opts Options
		_ = json.Unmarshal(optsRaw, &opts)
		var classes []ClassInfo
		_ = json.Unmarshal(classesRaw, &classes)
		if classes == nil {
			classes = []ClassInfo{}
		}

		var dvRef *DataVersionSourceRef
		transferMode := ""
		if sourceType == SourceDataVersion && sourceRef.DataVersion != nil {
			dvCopy := *sourceRef.DataVersion
			if dvDirName != "" {
				dvCopy.DirName = dvDirName
			}
			sharedLocal := dvStorageNodeID == 0 || dvStorageNodeID == storagenode.BuiltinID
			dvCopy.SharedLocal = &sharedLocal
			dvRef = &dvCopy
			if sharedLocal {
				transferMode = "shared_local"
			} else {
				transferMode = "direct"
			}
		}

		base := fmt.Sprintf("/api/v1/compute/agent/evaluations/%d/", id)
		task = &Task{
			Kind:           TaskKind,
			ID:             id,
			LeaseSeconds:   leaseSeconds(),
			ProjectID:      projectID,
			Model:          modelRef,
			SourceType:     sourceType,
			DataVersion:    dvRef,
			DVTotalBytes:   dvTotalBytes,
			DVTransferMode: transferMode,
			Options:        opts,
			Classes:        classes,
			TotalImages:    progressTotal,
			ModelURL:       base + "model",
			ImagesURL:      base + "images",
			ProgressURL:    base + "progress",
			ResultsURL:     base + "results",
			CompleteURL:    base + "complete",
			FailURL:        base + "fail",
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("claim model evaluation: %w", err)
	}
	if skipped {
		s.log.Warn("queued evaluation failed on claim", "evaluation_id", failedID, "error", failMsg)
	}
	if task != nil {
		if s.storage != nil {
			if mkerr := os.MkdirAll(s.evalDir(task.ID), 0o755); mkerr != nil {
				s.log.Warn("prepare evaluation dir failed", "evaluation_id", task.ID, "err", mkerr)
			}
		}
		s.touchWorker(nodeID)
		s.log.Info("evaluation claimed", "evaluation_id", task.ID, "node_id", nodeID, "model_id", task.Model.ID, "source_type", task.SourceType)
	}
	return task, skipped, nil
}

// expireLeases re-queues or fails running evaluations whose compute-node lease expired.
func (s *Service) expireLeases(ctx context.Context) error {
	requeued, err := s.db.Exec(ctx, `UPDATE evaluations
		SET status = 'queued', stage = '', node_id = NULL, lease_expires_at = NULL,
			eta_seconds = NULL, updated_at = now()
		WHERE status = 'running' AND lease_expires_at < now() AND attempts < $1`, maxAttempts)
	if err != nil {
		return err
	}
	failed, err := s.db.Exec(ctx, `UPDATE evaluations
		SET status = 'failed', stage = '', node_id = NULL, lease_expires_at = NULL,
			eta_seconds = NULL,
			error = '训练节点超时失联，重试次数已耗尽',
			finished_at = now(), updated_at = now()
		WHERE status = 'running' AND lease_expires_at < now() AND attempts >= $1`, maxAttempts)
	if err != nil {
		return err
	}
	if requeued.RowsAffected() > 0 {
		s.log.Warn("re-queued evaluations after lease expiry", "count", requeued.RowsAffected())
		s.notifyTasks()
	}
	if failed.RowsAffected() > 0 {
		s.log.Warn("failed evaluations after max lease retries", "count", failed.RowsAffected())
	}
	return nil
}

func (s *Service) renewLease(ctx context.Context, evalID, nodeID int64, stage string) error {
	tag, err := s.db.Exec(ctx, `UPDATE evaluations
		SET lease_expires_at = now() + make_interval(secs => $3),
			stage = coalesce(nullif($4, ''), stage),
			updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`,
		evalID, nodeID, model.LeaseDuration.Seconds(), stage)
	if err != nil {
		return err
	}
	s.touchWorker(nodeID)
	if tag.RowsAffected() == 0 {
		return s.taskOwnershipError(ctx, evalID)
	}
	return nil
}

func (s *Service) taskOwnershipError(ctx context.Context, evalID int64) error {
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM evaluations WHERE id = $1)", evalID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errEvalNotFound
	}
	return errTaskNotYours
}

// leaseReader periodically renews the evaluation lease during long file transfers.
type leaseReader struct {
	io.ReadSeeker
	renew func() error
	last  time.Time
}

func (l *leaseReader) Read(p []byte) (int, error) {
	if time.Since(l.last) >= leaseRenewEvery {
		if err := l.renew(); err != nil {
			return 0, err
		}
		l.last = time.Now()
	}
	return l.ReadSeeker.Read(p)
}

// OpenTaskModel streams the .pt weights for an evaluation held by nodeID.
func (s *Service) OpenTaskModel(ctx context.Context, evalID, nodeID int64) (*model.File, error) {
	if err := s.renewLease(ctx, evalID, nodeID, StageDownloading); err != nil {
		return nil, err
	}
	var modelID *int64
	if err := s.db.QueryRow(ctx, "SELECT model_id FROM evaluations WHERE id = $1", evalID).Scan(&modelID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errEvalNotFound
		}
		return nil, err
	}
	if modelID == nil {
		return nil, newError(ErrNotFound, "关联的模型已被删除")
	}
	f, err := s.models.OpenModelFile(ctx, *modelID)
	if err != nil {
		var me *model.Error
		if errors.As(err, &me) && errors.Is(me.Kind, model.ErrNotFound) {
			return nil, newError(ErrNotFound, "模型权重文件不存在")
		}
		return nil, err
	}
	f.Content = &leaseReader{
		ReadSeeker: f.Content,
		renew: func() error {
			return s.renewLease(ctx, evalID, nodeID, "")
		},
		last: time.Now(),
	}
	return f, nil
}

// ListTaskImages returns a cursor-paginated batch of samples (with GT boxes)
// for the compute node executing evalID.
func (s *Service) ListTaskImages(ctx context.Context, evalID, nodeID, afterID int64, limit int) (TaskImagePage, error) {
	if err := s.renewLease(ctx, evalID, nodeID, ""); err != nil {
		return TaskImagePage{}, err
	}
	if limit <= 0 {
		limit = DefaultTaskImgLimit
	}
	if limit > MaxTaskImgLimit {
		limit = MaxTaskImgLimit
	}
	rows, err := s.db.Query(ctx, `SELECT id, position, path, width, height, gt_objects
		FROM evaluation_images
		WHERE evaluation_id = $1 AND id > $2
		ORDER BY id
		LIMIT $3`, evalID, afterID, limit+1)
	if err != nil {
		return TaskImagePage{}, err
	}
	defer rows.Close()

	items := make([]TaskImage, 0, limit)
	hasMore := false
	for rows.Next() {
		var (
			img   TaskImage
			gtRaw []byte
		)
		if err := rows.Scan(&img.ID, &img.Position, &img.Path, &img.Width, &img.Height, &gtRaw); err != nil {
			return TaskImagePage{}, err
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		if len(gtRaw) > 0 {
			_ = json.Unmarshal(gtRaw, &img.GTObjects)
		}
		if img.GTObjects == nil {
			img.GTObjects = []GTBox{}
		}
		img.FileURL = fmt.Sprintf("/api/v1/compute/agent/evaluations/%d/images/%d/file", evalID, img.ID)
		items = append(items, img)
	}
	if err := rows.Err(); err != nil {
		return TaskImagePage{}, err
	}
	var nextID int64
	if len(items) > 0 {
		nextID = items[len(items)-1].ID
	}
	return TaskImagePage{
		Items:   items,
		HasMore: hasMore,
		NextID:  nextID,
	}, nil
}

// OpenTaskImage opens the image file for sampleID so the compute node can fetch it over HTTP.
func (s *Service) OpenTaskImage(ctx context.Context, evalID, nodeID, sampleID int64) (*ImageFile, error) {
	if err := s.renewLease(ctx, evalID, nodeID, ""); err != nil {
		return nil, err
	}
	return s.openSampleImageFile(ctx, evalID, sampleID, false, "")
}

// ReportProgress updates stage, progress counters, ETA, and renews the lease.
func (s *Service) ReportProgress(ctx context.Context, evalID, nodeID int64, in ProgressInput) (int, error) {
	stage := strings.TrimSpace(in.Stage)
	if stage != "" && !slices.Contains([]string{StagePreparing, StageDownloading, StageInferring, StageComputing, StageUploading}, stage) {
		return 0, newError(ErrInvalid, "无效的评估阶段：%s", stage)
	}
	tag, err := s.db.Exec(ctx, `UPDATE evaluations
		SET lease_expires_at = now() + make_interval(secs => $3),
			stage = coalesce(nullif($4, ''), stage),
			progress_done = coalesce($5, progress_done),
			progress_total = coalesce($6, progress_total),
			eta_seconds = $7,
			updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`,
		evalID, nodeID, model.LeaseDuration.Seconds(), stage, in.ProgressDone, in.ProgressTotal, in.ETASeconds)
	if err != nil {
		return 0, err
	}
	s.touchWorker(nodeID)
	if tag.RowsAffected() == 0 {
		return 0, s.taskOwnershipError(ctx, evalID)
	}
	return leaseSeconds(), nil
}

// UploadResults persists curves.json and predictions.json.gz uploaded by the compute node.
func (s *Service) UploadResults(ctx context.Context, evalID, nodeID int64, payload UploadResultsPayload) error {
	if err := s.renewLease(ctx, evalID, nodeID, StageUploading); err != nil {
		return err
	}

	var optsRaw []byte
	if err := s.db.QueryRow(ctx, "SELECT options FROM evaluations WHERE id = $1", evalID).Scan(&optsRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errEvalNotFound
		}
		return err
	}
	var opts Options
	_ = json.Unmarshal(optsRaw, &opts)
	if opts.StoreMinConf <= 0 {
		opts.StoreMinConf = DefaultStoreMinConf
	}

	// Load valid sample IDs for this evaluation so bogus IDs cannot corrupt data.
	rows, err := s.db.Query(ctx, "SELECT id FROM evaluation_images WHERE evaluation_id = $1", evalID)
	if err != nil {
		return err
	}
	validIDs := map[int64]bool{}
	for rows.Next() {
		var sid int64
		if serr := rows.Scan(&sid); serr != nil {
			rows.Close()
			return serr
		}
		validIDs[sid] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// Normalize predictions and collect image metadata updates (width, height, inference_error).
	stored := make([]StoredImagePrediction, 0, len(payload.Predictions))
	var (
		metaIDs    []int64
		metaWidths []int32
		metaHeight []int32
		metaErrs   []string
	)
	for _, item := range payload.Predictions {
		if !validIDs[item.SampleID] {
			continue
		}
		preds := make([]PredBox, 0, len(item.Predictions))
		for _, p := range item.Predictions {
			if p.Conf >= opts.StoreMinConf {
				preds = append(preds, p)
			}
		}
		slices.SortStableFunc(preds, func(a, b PredBox) int {
			return cmp.Or(cmp.Compare(b.Conf, a.Conf), strings.Compare(a.Name, b.Name))
		})
		infErr := strings.TrimSpace(item.InferenceError)
		if len(infErr) > 500 {
			infErr = infErr[:500]
		}
		stored = append(stored, StoredImagePrediction{
			SampleID:       item.SampleID,
			Width:          item.Width,
			Height:         item.Height,
			InferenceError: infErr,
			Predictions:    preds,
		})
		if item.Width > 0 || item.Height > 0 || infErr != "" {
			metaIDs = append(metaIDs, item.SampleID)
			metaWidths = append(metaWidths, int32(item.Width))
			metaHeight = append(metaHeight, int32(item.Height))
			metaErrs = append(metaErrs, infErr)
		}
	}

	// Normalize nil curve slices to empty arrays for clean JSON.
	curves := payload.Curves
	if curves.PR == nil {
		curves.PR = []CurveSeries{}
	}
	if curves.F1 == nil {
		curves.F1 = []CurveSeries{}
	}
	if curves.Precision == nil {
		curves.Precision = []CurveSeries{}
	}
	if curves.Recall == nil {
		curves.Recall = []CurveSeries{}
	}

	curvesJSON, err := json.Marshal(curves)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.curvesPath(evalID), curvesJSON); err != nil {
		return fmt.Errorf("write curves.json: %w", err)
	}
	predFile := StoredPredictionsFile{
		EvaluationID: evalID,
		StoreMinConf: opts.StoreMinConf,
		Items:        stored,
	}
	if err := writeGzipJSONAtomic(s.predictionsPath(evalID), predFile); err != nil {
		return fmt.Errorf("write predictions.json.gz: %w", err)
	}

	if len(metaIDs) > 0 {
		const chunkSize = 1000
		for start := 0; start < len(metaIDs); start += chunkSize {
			end := min(start+chunkSize, len(metaIDs))
			if _, err := s.db.Exec(ctx, `UPDATE evaluation_images ei
				SET width = CASE WHEN u.w > 0 THEN u.w ELSE ei.width END,
					height = CASE WHEN u.h > 0 THEN u.h ELSE ei.height END,
					inference_error = u.inf_err
				FROM unnest($1::bigint[], $2::int[], $3::int[], $4::text[])
					AS u (id, w, h, inf_err)
				WHERE ei.id = u.id AND ei.evaluation_id = $5`,
				metaIDs[start:end], metaWidths[start:end], metaHeight[start:end], metaErrs[start:end], evalID); err != nil {
				return err
			}
		}
	}

	s.cache.delete(evalID)
	return nil
}

// CompleteTask finalizes a running evaluation after results have been uploaded,
// computing the default (conf, iou) summary and per-image counts.
func (s *Service) CompleteTask(ctx context.Context, evalID, nodeID int64, rawResult json.RawMessage) (Evaluation, error) {
	if err := s.renewLease(ctx, evalID, nodeID, StageComputing); err != nil {
		return Evaluation{}, err
	}
	ev, err := s.Get(ctx, evalID)
	if err != nil {
		return Evaluation{}, err
	}
	s.cache.delete(evalID)

	loaded, err := s.loadEvaluationDataFromDisk(ctx, ev)
	if err != nil {
		return Evaluation{}, err
	}
	run := ComputeAnalysis(ev.ID, loaded.classes, loaded.samples, loaded.curves, loaded.storeMinConf, ev.Options.Conf, ev.Options.IoU)
	summaryJSON, err := json.Marshal(run.Analysis.Summary)
	if err != nil {
		return Evaluation{}, err
	}

	var resultParam []byte
	if len(rawResult) > 0 && string(rawResult) != "null" {
		resultParam = rawResult
	}

	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		var holder *int64
		if err := tx.QueryRow(ctx, "SELECT status, node_id FROM evaluations WHERE id = $1 FOR UPDATE", evalID).Scan(&status, &holder); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errEvalNotFound
			}
			return err
		}
		if status != StatusRunning || holder == nil || *holder != nodeID {
			return errTaskNotYours
		}
		if _, err := tx.Exec(ctx, `UPDATE evaluations
			SET status = 'succeeded', stage = '', lease_expires_at = NULL,
				progress_done = progress_total, eta_seconds = NULL,
				summary = $2, result = $3,
				error = '', error_detail = '',
				finished_at = now(), updated_at = now()
			WHERE id = $1`, evalID, summaryJSON, resultParam); err != nil {
			return err
		}
		return updateCachedSampleCounts(ctx, tx, run.Samples)
	})
	if err != nil {
		return Evaluation{}, err
	}

	s.cache.put(evalID, loaded)
	s.log.Info("evaluation succeeded",
		"evaluation_id", evalID, "node_id", nodeID,
		"map50", run.Analysis.Summary.MAP50, "map50_95", run.Analysis.Summary.MAP50_95,
		"f1", run.Analysis.Summary.F1)
	return s.Get(ctx, evalID)
}

// FailTask records a compute-node failure; retryable failures re-queue until maxAttempts.
func (s *Service) FailTask(ctx context.Context, evalID, nodeID int64, in FailInput) (Evaluation, error) {
	errMsg := strings.TrimSpace(in.Error)
	if errMsg == "" {
		errMsg = "评估任务执行失败"
	}
	if runes := []rune(errMsg); len(runes) > 500 {
		errMsg = string(runes[:500])
	}
	detail := in.Detail
	if len(detail) > 16000 {
		detail = detail[:16000]
	}

	requeued := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		var holder *int64
		var attempts int
		if err := tx.QueryRow(ctx, "SELECT status, node_id, attempts FROM evaluations WHERE id = $1 FOR UPDATE", evalID).
			Scan(&status, &holder, &attempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errEvalNotFound
			}
			return err
		}
		if status != StatusRunning || holder == nil || *holder != nodeID {
			return errTaskNotYours
		}
		if in.Retryable && attempts < maxAttempts {
			requeued = true
			_, err := tx.Exec(ctx, `UPDATE evaluations
				SET status = 'queued', stage = '', node_id = NULL, lease_expires_at = NULL,
					eta_seconds = NULL, error = $2, error_detail = $3, updated_at = now()
				WHERE id = $1`, evalID, errMsg, detail)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE evaluations
			SET status = 'failed', stage = '', node_id = NULL, lease_expires_at = NULL,
				eta_seconds = NULL, error = $2, error_detail = $3,
				finished_at = now(), updated_at = now()
			WHERE id = $1`, evalID, errMsg, detail)
		return err
	})
	if err != nil {
		return Evaluation{}, err
	}
	s.touchWorker(nodeID)
	if requeued {
		s.log.Warn("evaluation retryable failure, re-queued", "evaluation_id", evalID, "node_id", nodeID, "error", errMsg)
		s.notifyTasks()
	} else {
		s.log.Warn("evaluation failed", "evaluation_id", evalID, "node_id", nodeID, "error", errMsg)
	}
	return s.Get(ctx, evalID)
}
