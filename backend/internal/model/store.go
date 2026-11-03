package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is implemented by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---- models ----

// modelSelect reads models with their project's name and the number of
// succeeded conversions; append WHERE and ORDER BY.
const modelSelect = `SELECT m.id, m.project_id, p.name, m.version, m.description, m.source, m.training_job_id,
		m.yolo_version, m.task, m.file_name, m.dir_name, m.size_bytes, m.sha256, m.info, m.created_at, m.updated_at,
		(SELECT count(*) FROM model_exports e WHERE e.model_id = m.id AND e.status = 'succeeded')
	FROM models m JOIN projects p ON p.id = m.project_id`

// modelRow is a model as stored, with the folder the API does not show.
type modelRow struct {
	Model
	dirName string
}

func scanModel(row pgx.Row) (modelRow, error) {
	var r modelRow
	var info []byte
	err := row.Scan(&r.ID, &r.Project.ID, &r.Project.Name, &r.Version, &r.Description, &r.Source, &r.TrainingJobID,
		&r.YOLOVersion, &r.Task, &r.FileName, &r.dirName, &r.SizeBytes, &r.SHA256, &info, &r.CreatedAt, &r.UpdatedAt,
		&r.ExportCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errModelNotFound
	}
	if err != nil {
		return r, err
	}
	if r.Info, err = decodeInfo(info); err != nil {
		return r, fmt.Errorf("decode info of model %d: %w", r.ID, err)
	}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	r.DownloadURL = fmt.Sprintf("/api/v1/models/%d/file", r.ID)
	return r, nil
}

func queryModel(ctx context.Context, q querier, id int64) (modelRow, error) {
	return scanModel(q.QueryRow(ctx, modelSelect+" WHERE m.id = $1", id))
}

func collectModels(rows pgx.Rows, err error) ([]modelRow, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (modelRow, error) { return scanModel(row) })
}

// ---- uploads ----

const uploadColumns = `id, project_id, yolo_version, description, file_name, file_size, received_bytes, status,
	detected_yolo_version, error, model_id, created_at, updated_at`

func scanUpload(row pgx.Row) (Upload, error) {
	var u Upload
	err := row.Scan(&u.ID, &u.ProjectID, &u.YOLOVersion, &u.Description, &u.FileName, &u.FileSize, &u.ReceivedBytes,
		&u.Status, &u.DetectedYOLOVersion, &u.Error, &u.ModelID, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, errUploadNotFound
	}
	u.CreatedAt, u.UpdatedAt = u.CreatedAt.UTC(), u.UpdatedAt.UTC()
	return u, err
}

func queryUpload(ctx context.Context, q querier, id int64) (Upload, error) {
	return scanUpload(q.QueryRow(ctx, "SELECT "+uploadColumns+" FROM model_uploads WHERE id = $1", id))
}

// ---- exports ----

// exportSelect reads conversions with what their API form needs from the
// model, its project and the node; append WHERE and ORDER BY. A queued
// conversion's position counts the queued ones claimed before it (claims
// go by ID).
const exportSelect = `SELECT e.id, e.model_id, e.format, e.options, e.status, e.stage, e.attempts, e.node_id, n.name,
		CASE WHEN e.status = 'queued' THEN
			(SELECT count(*) FROM model_exports q WHERE q.status = 'queued' AND q.id <= e.id) END,
		e.file_name, e.size_bytes, e.sha256, e.result, e.error, e.error_detail,
		e.created_at, e.updated_at, e.started_at, e.finished_at,
		p.name, m.version, m.dir_name
	FROM model_exports e
	JOIN models m ON m.id = e.model_id
	JOIN projects p ON p.id = m.project_id
	LEFT JOIN compute_nodes n ON n.id = e.node_id`

// exportRow is a conversion as stored.
type exportRow struct {
	Export
	relPath     string // file_name: the result, relative to the model folder
	projectName string
	version     int
	dirName     string // of the model
}

func scanExport(row pgx.Row) (exportRow, error) {
	var r exportRow
	var options, result []byte
	var nodeID *int64
	var nodeName *string
	var position *int64
	err := row.Scan(&r.ID, &r.ModelID, &r.Format, &options, &r.Status, &r.Stage, &r.Attempts, &nodeID, &nodeName,
		&position, &r.relPath, &r.SizeBytes, &r.SHA256, &result, &r.Error, &r.ErrorDetail,
		&r.CreatedAt, &r.UpdatedAt, &r.StartedAt, &r.FinishedAt, &r.projectName, &r.version, &r.dirName)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errExportNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(options, &r.Options); err != nil {
		return r, fmt.Errorf("decode options of model export %d: %w", r.ID, err)
	}
	if nodeID != nil && nodeName != nil {
		r.Node = &NodeRef{ID: *nodeID, Name: *nodeName}
	}
	if position != nil {
		p := int(*position)
		r.QueuePosition = &p
	}
	r.Result = json.RawMessage("null")
	if r.Status == ExportSucceeded {
		if len(result) > 0 {
			r.Result = result
		}
		url := fmt.Sprintf("/api/v1/models/%d/exports/%d/file", r.ModelID, r.ID)
		r.DownloadURL = &url
	} else {
		// Until then they describe a result that is still being checked.
		r.SizeBytes, r.SHA256 = nil, ""
	}
	r.FileName = ExportFileName(r.projectName, r.version, r.Options)
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	r.StartedAt, r.FinishedAt = utcPtr(r.StartedAt), utcPtr(r.FinishedAt)
	return r, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func queryExport(ctx context.Context, q querier, modelID, exportID int64) (exportRow, error) {
	return scanExport(q.QueryRow(ctx, exportSelect+" WHERE e.id = $1 AND e.model_id = $2", exportID, modelID))
}

func collectExports(rows pgx.Rows, err error) ([]exportRow, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (exportRow, error) { return scanExport(row) })
}

// exportState is what the agent calls check before touching a conversion.
type exportState struct {
	Status   string
	NodeID   *int64
	Attempts int
	Size     *int64 // of the uploaded result
	SHA256   string
	DirName  string // of the model
}

// lockExportState reads (and with forUpdate, locks) the state of a
// conversion; errTaskGone if it does not exist.
func lockExportState(ctx context.Context, q querier, id int64, forUpdate bool) (exportState, error) {
	sql := `SELECT e.status, e.node_id, e.attempts, e.size_bytes, e.sha256, m.dir_name
		FROM model_exports e JOIN models m ON m.id = e.model_id WHERE e.id = $1`
	if forUpdate {
		sql += " FOR UPDATE OF e"
	}
	var st exportState
	err := q.QueryRow(ctx, sql, id).Scan(&st.Status, &st.NodeID, &st.Attempts, &st.Size, &st.SHA256, &st.DirName)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, errTaskGone
	}
	return st, err
}

// heldBy fails unless the conversion is running on nodeID.
func (st exportState) heldBy(nodeID int64) error {
	if st.Status != ExportRunning || st.NodeID == nil || *st.NodeID != nodeID {
		return errTaskNotYours
	}
	return nil
}
