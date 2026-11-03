package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The conversion queue. Training nodes long-poll POST
// /api/v1/compute/agent/tasks/claim; a claim takes the oldest queued
// conversion with SELECT ... FOR UPDATE SKIP LOCKED, so two nodes never get
// the same one. The node then holds a lease: every report (progress,
// result upload) renews it, and a conversion whose lease expires (the node
// crashed or lost the network) is queued again, or fails after
// maxAttempts claims. Training jobs will be handed out the same way.

const (
	// LeaseDuration is how long a node may go without reporting on a
	// conversion it holds.
	LeaseDuration = 60 * time.Second
	// leaseRenewEvery: transfers that take long renew the lease this often.
	leaseRenewEvery = 15 * time.Second
	// maxAttempts bounds the claims of one conversion.
	maxAttempts = 3
	// MaxClaimWait is the longest a claim waits for a task.
	MaxClaimWait = 30 * time.Second
	// MaxOutputBytes is the largest conversion result accepted.
	MaxOutputBytes = 4 << 30

	maxErrorRunes    = 500
	maxDetailBytes   = 8 << 10
	maxTaskKinds     = 16
	maxTaskKindRunes = 64
)

var msgLeaseLost = fmt.Sprintf("训练节点在转换过程中失去联系（已尝试 %d 次）", maxAttempts)

func leaseSeconds() int { return int(LeaseDuration / time.Second) }

// ClaimTask hands the oldest queued task of one of kinds to the node, or
// nil if none arrives within wait. Unknown kinds are ignored (newer nodes
// may offer more). The wait ends early, without a task, when the service
// stops or StopLongPolls is called.
func (s *Service) ClaimTask(ctx context.Context, nodeID int64, kinds []string, wait time.Duration) (*Task, error) {
	switch {
	case len(kinds) == 0:
		return nil, &Error{Kind: ErrInvalid, Message: "kinds 不能为空"}
	case len(kinds) > maxTaskKinds:
		return nil, newError(ErrInvalid, "kinds 最多 %d 项", maxTaskKinds)
	case wait < 0 || wait > MaxClaimWait:
		return nil, newError(ErrInvalid, "wait_seconds 必须在 0–%d 之间", int(MaxClaimWait/time.Second))
	}
	for _, k := range kinds {
		if k == "" || len(k) > maxTaskKindRunes {
			return nil, &Error{Kind: ErrInvalid, Message: "kinds 中有无效的任务类型"}
		}
	}
	exports := slices.Contains(kinds, TaskModelExport)
	if exports {
		// Waiting in a claim counts as working on conversions, at its start
		// and at its end.
		s.workers.touch(nodeID, s.clock())
		defer func() { s.workers.touch(nodeID, s.clock()) }()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		woken := s.waiters.wait()
		if exports {
			task, err := s.claimExport(ctx, nodeID)
			if task != nil || err != nil {
				return task, err
			}
		}
		select {
		case <-woken:
		case <-timer.C:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.closing:
			return nil, nil
		case <-s.base.Done():
			return nil, nil
		}
	}
}

// claimExport claims the oldest queued conversion for the node.
func (s *Service) claimExport(ctx context.Context, nodeID int64) (*Task, error) {
	var t Task
	var options, info []byte
	err := s.db.QueryRow(ctx, `WITH claimed AS (
			UPDATE model_exports SET status = 'running', stage = 'downloading', node_id = $1, attempts = attempts + 1,
				lease_expires_at = now() + make_interval(secs => $2), size_bytes = NULL, sha256 = '',
				error = '', error_detail = '', started_at = now(), finished_at = NULL, updated_at = now()
			WHERE id = (SELECT id FROM model_exports WHERE status = 'queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING id, model_id, options)
		SELECT c.id, c.options, m.id, m.project_id, m.version, m.yolo_version, m.task, m.file_name,
			m.size_bytes, m.sha256, m.info
		FROM claimed c JOIN models m ON m.id = c.model_id`, nodeID, LeaseDuration.Seconds()).Scan(
		&t.ID, &options, &t.Model.ID, &t.Model.ProjectID, &t.Model.Version, &t.Model.YOLOVersion, &t.Model.Task,
		&t.Model.FileName, &t.Model.SizeBytes, &t.Model.SHA256, &info)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim model export: %w", err)
	}
	if err := json.Unmarshal(options, &t.Options); err != nil {
		return nil, fmt.Errorf("decode options of model export %d: %w", t.ID, err)
	}
	if t.Model.Info, err = decodeInfo(info); err != nil {
		return nil, fmt.Errorf("decode info of model %d: %w", t.Model.ID, err)
	}
	t.Kind, t.LeaseSeconds = TaskModelExport, leaseSeconds()
	base := fmt.Sprintf("/api/v1/compute/agent/model-exports/%d/", t.ID)
	t.InputURL, t.ProgressURL, t.OutputURL = base+"input", base+"progress", base+"output"
	t.CompleteURL, t.FailURL = base+"complete", base+"fail"
	s.log.Info("model export claimed", "export_id", t.ID, "model_id", t.Model.ID, "node_id", nodeID,
		"options", OptionsKey(t.Options))
	return &t, nil
}

// renewLease extends the lease of a conversion the node holds, optionally
// setting its stage; errTaskGone or errTaskNotYours if it does not hold it
// (any more).
func (s *Service) renewLease(ctx context.Context, exportID, nodeID int64, stage string) error {
	tag, err := s.db.Exec(ctx, `UPDATE model_exports
		SET lease_expires_at = now() + make_interval(secs => $3), stage = coalesce(nullif($4, ''), stage), updated_at = now()
		WHERE id = $1 AND node_id = $2 AND status = 'running'`, exportID, nodeID, LeaseDuration.Seconds(), stage)
	if err != nil {
		return err
	}
	s.workers.touch(nodeID, s.clock())
	if tag.RowsAffected() == 0 {
		return s.taskError(ctx, exportID)
	}
	return nil
}

// taskError explains why a node does not hold a conversion.
func (s *Service) taskError(ctx context.Context, exportID int64) error {
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM model_exports WHERE id = $1)", exportID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errTaskGone
	}
	return errTaskNotYours
}

// ReportProgress records the stage of a conversion the node holds and
// renews its lease. It returns the lease in seconds.
func (s *Service) ReportProgress(ctx context.Context, exportID, nodeID int64, stage, message string) (int, error) {
	switch stage {
	case StageDownloading, StageConverting, StageUploading:
	default:
		return 0, &Error{Kind: ErrInvalid, Message: "stage 必须是 downloading、converting 或 uploading"}
	}
	if err := s.renewLease(ctx, exportID, nodeID, stage); err != nil {
		return 0, err
	}
	if message = cleanLabel(message, maxErrorRunes); message != "" {
		s.log.Debug("model export progress", "export_id", exportID, "node_id", nodeID, "stage", stage, "message", message)
	}
	return leaseSeconds(), nil
}

// OpenTaskInput opens the weights of a conversion the node holds. Reading
// the file renews the lease, so a slow download does not lose it. When
// the weights are gone the conversion fails (it cannot succeed).
func (s *Service) OpenTaskInput(ctx context.Context, exportID, nodeID int64) (*File, error) {
	st, err := lockExportState(ctx, s.db, exportID, false)
	if err != nil {
		return nil, err
	}
	if err := st.heldBy(nodeID); err != nil {
		return nil, err
	}
	f, err := s.openInModel(st.DirName, modelFile, errFileMissing)
	if errors.Is(err, errFileMissing) {
		if _, ferr := s.db.Exec(ctx, `UPDATE model_exports SET status = 'failed', stage = '', lease_expires_at = NULL,
				error = $3, finished_at = now(), updated_at = now()
			WHERE id = $1 AND node_id = $2 AND status = 'running'`,
			exportID, nodeID, "模型文件不存在，无法转换。请重新上传该模型"); ferr != nil {
			return nil, ferr
		}
		s.log.Warn("model export failed: model file missing", "export_id", exportID)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if err := s.renewLease(ctx, exportID, nodeID, ""); err != nil {
		f.Close()
		return nil, err
	}
	f.Name = modelFile
	f.Content = &leaseReader{ReadSeeker: f.Content, renew: func() error {
		return s.renewLease(ctx, exportID, nodeID, "")
	}, last: time.Now()}
	return f, nil
}

// leaseReader renews a lease every leaseRenewEvery while it is read, and
// fails once the node no longer holds the conversion.
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

// leaseWriter is leaseReader for receiving a file.
type leaseWriter struct {
	w     io.Writer
	renew func() error
	last  time.Time
}

func (l *leaseWriter) Write(p []byte) (int, error) {
	if time.Since(l.last) >= leaseRenewEvery {
		if err := l.renew(); err != nil {
			return 0, err
		}
		l.last = time.Now()
	}
	return l.w.Write(p)
}

// PutTaskOutput receives the result of a conversion the node holds: size
// bytes (the Content-Length) with the hex SHA-256 sum. The file is checked
// against both and kept in tmp/ until CompleteTask; a file that does not
// match is discarded. Uploading again replaces it.
func (s *Service) PutTaskOutput(ctx context.Context, exportID, nodeID, size int64, sum string, body io.Reader) (int64, error) {
	sum = strings.ToLower(strings.TrimSpace(sum))
	switch {
	case size < 1:
		return 0, &Error{Kind: ErrInvalid, Message: "转换结果是空文件"}
	case size > MaxOutputBytes:
		return 0, &Error{Kind: ErrTooLarge, Message: "转换结果不能超过 4 GiB"}
	case !isHexSHA256(sum):
		return 0, &Error{Kind: ErrBadRequest, Message: "请求头 X-Content-Sha256 无效，应为 64 位十六进制的 SHA-256"}
	}
	unlock, err := s.outputs.lock(ctx, exportID)
	if err != nil {
		return 0, err
	}
	defer unlock()
	st, err := lockExportState(ctx, s.db, exportID, false)
	if err != nil {
		return 0, err
	}
	if err := st.heldBy(nodeID); err != nil {
		return 0, err
	}
	if err := s.checkFreeSpace(size); err != nil {
		return 0, err
	}

	path := s.outputPath(exportID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("create model export temporary folder: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("create model export result: %w", err)
	}
	h := sha256.New()
	w := &leaseWriter{w: io.MultiWriter(f, h), renew: func() error {
		return s.renewLease(ctx, exportID, nodeID, "")
	}, last: time.Now()}
	n, err := io.Copy(w, bodyReader{io.LimitReader(body, size+1)})
	switch {
	case err != nil:
		err = receiveError(err)
	case n != size:
		err = newError(ErrBadRequest, "上传的数据与 Content-Length 不一致（收到 %d 字节，应为 %d 字节）", n, size)
	case hex.EncodeToString(h.Sum(nil)) != sum:
		err = &Error{Kind: ErrInvalid, Message: "转换结果的 SHA-256 校验失败，文件可能在传输中损坏，请重新上传"}
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err == nil {
		// Record the verified file; it also renews the lease.
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE model_exports SET size_bytes = $3, sha256 = $4, stage = 'uploading',
				lease_expires_at = now() + make_interval(secs => $5), updated_at = now()
			WHERE id = $1 AND node_id = $2 AND status = 'running'`, exportID, nodeID, size, sum, LeaseDuration.Seconds())
		if err == nil && tag.RowsAffected() == 0 {
			err = s.taskError(ctx, exportID)
		}
	}
	if err != nil {
		_ = os.Remove(path)
		return 0, err
	}
	s.workers.touch(nodeID, s.clock())
	return n, nil
}

func isHexSHA256(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// CompleteTask finishes a conversion the node holds: the uploaded result
// moves into the model folder (onnx/<id>.onnx) and the conversion
// succeeds with what the node reported about it.
func (s *Service) CompleteTask(ctx context.Context, exportID, nodeID int64, result json.RawMessage) (Export, error) {
	unlock, err := s.outputs.lock(ctx, exportID) // waits for an upload in flight
	if err != nil {
		return Export{}, err
	}
	defer unlock()
	ctx, cancel := s.detach(ctx)
	defer cancel()
	resultJSON, err := json.Marshal(sanitizeResult(result))
	if err != nil {
		return Export{}, fmt.Errorf("encode model export result: %w", err)
	}

	var modelID int64
	var dst string // the result in the model folder, once moved
	part := s.outputPath(exportID)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		st, err := lockExportState(ctx, tx, exportID, true)
		if err != nil {
			return err
		}
		if err := st.heldBy(nodeID); err != nil {
			return err
		}
		if st.Size == nil {
			return &Error{Kind: ErrConflict, Message: "尚未上传转换结果"}
		}
		if fi, err := os.Stat(part); err != nil || fi.Size() != *st.Size {
			return &Error{Kind: ErrConflict, Message: "服务器上的转换结果不完整，请重新上传"}
		}
		dir, err := s.modelDir(st.DirName)
		if err != nil {
			return err
		}
		rel := exportRelPath(exportID)
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create model export folder: %w", err)
		}
		if err := os.Rename(part, target); err != nil {
			return fmt.Errorf("move model export result: %w", err)
		}
		dst = target
		return tx.QueryRow(ctx, `UPDATE model_exports SET status = 'succeeded', stage = '', file_name = $2, result = $3,
				lease_expires_at = NULL, finished_at = now(), updated_at = now()
			WHERE id = $1 RETURNING model_id`, exportID, rel, resultJSON).Scan(&modelID)
	})
	if err != nil {
		if dst != "" {
			if rerr := os.Rename(dst, part); rerr != nil {
				s.log.Error("move model export result back after failed commit", "path", dst, "err", rerr)
			}
		}
		return Export{}, err
	}
	s.removeExportTemp(exportID)
	s.workers.touch(nodeID, s.clock())
	e, err := s.GetExport(ctx, modelID, exportID)
	if err == nil {
		var size int64
		if e.SizeBytes != nil {
			size = *e.SizeBytes
		}
		s.log.Info("model export succeeded", "export_id", exportID, "model_id", modelID, "node_id", nodeID,
			"size_bytes", size, "attempts", e.Attempts)
	}
	return e, err
}

// FailInput is a node's report that a conversion failed.
type FailInput struct {
	Error     string // in Chinese, shown to users
	Detail    string // the node's error output
	Retryable bool   // e.g. a network error: another attempt may succeed
}

// FailTask ends a conversion the node holds: a retryable failure queues it
// again unless it has been claimed maxAttempts times; anything else fails
// it with the node's message.
func (s *Service) FailTask(ctx context.Context, exportID, nodeID int64, in FailInput) (Export, error) {
	unlock, err := s.outputs.lock(ctx, exportID)
	if err != nil {
		return Export{}, err
	}
	defer unlock()
	msg := cleanText(strings.TrimSpace(in.Error), maxErrorRunes)
	if msg == "" {
		msg = "转换失败（训练节点没有给出原因）"
	}
	detail := truncateBytes(cleanText(in.Detail, maxDetailBytes), maxDetailBytes)

	var modelID int64
	var requeued bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		st, err := lockExportState(ctx, tx, exportID, true)
		if err != nil {
			return err
		}
		if err := st.heldBy(nodeID); err != nil {
			return err
		}
		if requeued = in.Retryable && st.Attempts < maxAttempts; requeued {
			return tx.QueryRow(ctx, `UPDATE model_exports SET status = 'queued', stage = '', node_id = NULL,
					lease_expires_at = NULL, size_bytes = NULL, sha256 = '', started_at = NULL, updated_at = now()
				WHERE id = $1 RETURNING model_id`, exportID).Scan(&modelID)
		}
		return tx.QueryRow(ctx, `UPDATE model_exports SET status = 'failed', stage = '', lease_expires_at = NULL,
				size_bytes = NULL, sha256 = '', error = $2, error_detail = $3, finished_at = now(), updated_at = now()
			WHERE id = $1 RETURNING model_id`, exportID, msg, detail).Scan(&modelID)
	})
	if err != nil {
		return Export{}, err
	}
	s.removeExportTemp(exportID)
	s.workers.touch(nodeID, s.clock())
	if requeued {
		s.waiters.notify()
		s.log.Warn("model export attempt failed; queued again", "export_id", exportID, "node_id", nodeID, "error", msg)
	} else {
		s.log.Warn("model export failed", "export_id", exportID, "node_id", nodeID, "error", msg, "retryable", in.Retryable)
	}
	return s.GetExport(ctx, modelID, exportID)
}

// expireLeases queues the running conversions whose node stopped
// reporting again, or fails them after maxAttempts claims.
func (s *Service) expireLeases(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `UPDATE model_exports SET
			status = CASE WHEN attempts < $1 THEN 'queued' ELSE 'failed' END,
			node_id = CASE WHEN attempts < $1 THEN NULL ELSE node_id END,
			started_at = CASE WHEN attempts < $1 THEN NULL ELSE started_at END,
			finished_at = CASE WHEN attempts < $1 THEN NULL ELSE now() END,
			error = CASE WHEN attempts < $1 THEN '' ELSE $2 END,
			stage = '', lease_expires_at = NULL, size_bytes = NULL, sha256 = '', updated_at = now()
		WHERE status = 'running' AND lease_expires_at < now()
		RETURNING id, status`, maxAttempts, msgLeaseLost)
	if err != nil {
		return err
	}
	var id int64
	var status string
	var requeued, failed int
	if _, err := pgx.ForEachRow(rows, []any{&id, &status}, func() error {
		if status == ExportQueued {
			requeued++
		} else {
			failed++
		}
		return nil
	}); err != nil {
		return err
	}
	if requeued > 0 {
		s.waiters.notify()
	}
	if requeued+failed > 0 {
		s.log.Warn("model exports lost their node", "queued_again", requeued, "failed", failed)
	}
	return nil
}
