package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
)

// maxExportRetries bounds how often RequestExport looks the conversion up
// again when it is deleted while being requested.
const maxExportRetries = 3

// RequestExport asks for a conversion of a model. It reports whether the
// result is ready: a conversion with the same options that succeeded and
// whose file is still there is returned as it is (the cache). Otherwise the
// conversion is queued: a new one for new options, the same one again
// (attempts and error reset) if it failed before or its file is gone; one
// already queued or running is returned as it is.
func (s *Service) RequestExport(ctx context.Context, modelID int64, req ExportRequest) (Export, bool, error) {
	r, err := queryModel(ctx, s.db, modelID)
	if err != nil {
		return Export{}, false, err
	}
	m := s.toModel(r)
	opts, err := NormalizeExport(m, req)
	if err != nil {
		return Export{}, false, err
	}
	if m.FileMissing {
		return Export{}, false, &Error{Kind: ErrConflict, Message: "模型文件不存在，无法转换。请重新上传该模型"}
	}
	key := OptionsKey(opts)
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return Export{}, false, fmt.Errorf("encode export options: %w", err)
	}

	for range maxExportRetries {
		var id int64
		var queued, ready bool
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `INSERT INTO model_exports (model_id, format, options, options_key, status)
				VALUES ($1, $2, $3, $4, 'queued')
				ON CONFLICT (model_id, format, options_key) DO NOTHING
				RETURNING id`, modelID, req.Format, optsJSON, key).Scan(&id)
			if err == nil {
				queued = true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var status, relPath string
			var size *int64
			err = tx.QueryRow(ctx, `SELECT id, status, file_name, size_bytes FROM model_exports
				WHERE model_id = $1 AND format = $2 AND options_key = $3 FOR UPDATE`,
				modelID, req.Format, key).Scan(&id, &status, &relPath, &size)
			if err != nil {
				return err // pgx.ErrNoRows: deleted meanwhile, try again
			}
			switch status {
			case ExportQueued, ExportRunning:
				return nil
			case ExportSucceeded:
				if s.resultPresent(r.dirName, relPath, size) {
					ready = true
					return nil
				}
			}
			queued = true
			_, err = tx.Exec(ctx, `UPDATE model_exports SET status = 'queued', stage = '', node_id = NULL, attempts = 0,
					lease_expires_at = NULL, file_name = '', size_bytes = NULL, sha256 = '', result = NULL,
					error = '', error_detail = '', started_at = NULL, finished_at = NULL, updated_at = now()
				WHERE id = $1`, id)
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return Export{}, false, err
		}
		if queued {
			s.waiters.notify()
			s.log.Info("model export queued", "export_id", id, "model_id", modelID, "options", key)
		}
		e, err := s.GetExport(ctx, modelID, id)
		return e, ready, err
	}
	return Export{}, false, fmt.Errorf("request model export: deleted %d times while requested", maxExportRetries)
}

// resultPresent reports whether the result of a succeeded conversion is
// still on disk with its recorded size.
func (s *Service) resultPresent(dirName, relPath string, size *int64) bool {
	if relPath == "" || size == nil {
		return false
	}
	dir, err := s.modelDir(dirName)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(relPath)))
	return err == nil && fi.Mode().IsRegular() && fi.Size() == *size
}

// ListExports returns the conversions of a model, newest first.
func (s *Service) ListExports(ctx context.Context, modelID int64) ([]Export, error) {
	if _, err := queryModel(ctx, s.db, modelID); err != nil {
		return nil, err
	}
	rows, err := collectExports(s.db.Query(ctx, exportSelect+" WHERE e.model_id = $1 ORDER BY e.id DESC", modelID))
	if err != nil {
		return nil, err
	}
	workers := s.workers.count(TaskModelExport, s.clock())
	out := make([]Export, len(rows))
	for i, r := range rows {
		out[i] = r.Export
		out[i].WorkersOnline = workers
	}
	return out, nil
}

// GetExport returns one conversion of a model.
func (s *Service) GetExport(ctx context.Context, modelID, exportID int64) (Export, error) {
	r, err := s.getExport(ctx, modelID, exportID)
	return r.Export, err
}

// getExport loads a conversion and tells "no such model" from "no such
// conversion".
func (s *Service) getExport(ctx context.Context, modelID, exportID int64) (exportRow, error) {
	r, err := queryExport(ctx, s.db, modelID, exportID)
	if errors.Is(err, errExportNotFound) {
		if _, merr := queryModel(ctx, s.db, modelID); merr != nil {
			return r, merr
		}
	}
	r.WorkersOnline = s.workers.count(TaskModelExport, s.clock())
	return r, err
}

// DeleteExport deletes a conversion and its result. A queued or running
// one can be deleted too: its node gets "not found" from then on and
// stops.
func (s *Service) DeleteExport(ctx context.Context, modelID, exportID int64) error {
	var relPath, dirName string
	err := s.db.QueryRow(ctx, `DELETE FROM model_exports e USING models m
		WHERE e.id = $1 AND e.model_id = $2 AND m.id = e.model_id
		RETURNING e.file_name, m.dir_name`, exportID, modelID).Scan(&relPath, &dirName)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, merr := queryModel(ctx, s.db, modelID); merr != nil {
			return merr
		}
		return errExportNotFound
	}
	if err != nil {
		return err
	}
	if relPath != "" {
		if dir, err := s.modelDir(dirName); err == nil {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(relPath))); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.log.Warn("remove model export file", "export_id", exportID, "err", err)
			}
		}
	}
	s.removeExportTemp(exportID)
	s.log.Info("model export deleted", "export_id", exportID, "model_id", modelID)
	return nil
}

var errExportFileMissing = &Error{Kind: ErrNotFound, Message: "转换结果文件不存在，请重新转换"}

// OpenExportFile opens the result of a succeeded conversion for download.
func (s *Service) OpenExportFile(ctx context.Context, modelID, exportID int64) (*File, error) {
	r, err := s.getExport(ctx, modelID, exportID)
	if err != nil {
		return nil, err
	}
	if r.Status != ExportSucceeded {
		return nil, &Error{Kind: ErrConflict, Message: "转换尚未完成"}
	}
	f, err := s.openInModel(r.dirName, r.relPath, errExportFileMissing)
	if err != nil {
		return nil, err
	}
	f.Name = r.FileName
	return f, nil
}
