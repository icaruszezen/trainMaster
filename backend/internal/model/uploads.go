package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/jackc/pgx/v5"
)

// MaxChunkBytes is the largest upload chunk accepted per request.
const MaxChunkBytes = 64 << 20

// CreateUploadInput describes a weights file the client is about to
// upload.
type CreateUploadInput struct {
	ProjectID   int64
	YOLOVersion string
	FileName    string
	FileSize    int64
	Description string
}

// CreateUpload starts the upload of a weights file into a project and
// prepares its staging folder.
func (s *Service) CreateUpload(ctx context.Context, in CreateUploadInput) (Upload, error) {
	yolo, err := normalizeYOLOVersion(in.YOLOVersion)
	if err != nil {
		return Upload{}, err
	}
	name, err := normalizeFileName(in.FileName)
	if err != nil {
		return Upload{}, err
	}
	desc, err := NormalizeDescription(in.Description)
	if err != nil {
		return Upload{}, err
	}
	switch {
	case in.FileSize < 1:
		return Upload{}, &Error{Kind: ErrInvalid, Message: "模型文件大小无效"}
	case in.FileSize > MaxModelBytes:
		return Upload{}, &Error{Kind: ErrInvalid, Message: "模型文件不能超过 8 GiB"}
	case in.ProjectID <= 0:
		return Upload{}, &Error{Kind: ErrInvalid, Message: "请选择项目"}
	}
	if err := s.checkFreeSpace(in.FileSize); err != nil {
		return Upload{}, err
	}
	// FOR KEY SHARE waits for the project being deleted (FOR UPDATE) and
	// then reads it again: one moved to the recycle bin takes no uploads.
	up, err := scanUpload(s.db.QueryRow(ctx, `INSERT INTO model_uploads (project_id, yolo_version, description, file_name,
			file_size, status)
		SELECT id, $2, $3, $4, $5, 'uploading' FROM projects WHERE id = $1 AND deleted_at IS NULL FOR KEY SHARE
		RETURNING `+uploadColumns, in.ProjectID, yolo, desc, name, in.FileSize))
	if errors.Is(err, errUploadNotFound) {
		return Upload{}, errProjectGone
	}
	if err != nil {
		return Upload{}, err
	}
	if err := os.MkdirAll(s.uploadDir(up.ID), 0o755); err != nil {
		_, _ = s.db.Exec(context.WithoutCancel(ctx), "DELETE FROM model_uploads WHERE id = $1", up.ID)
		return Upload{}, fmt.Errorf("create model upload folder: %w", err)
	}
	s.log.Info("model upload started", "upload_id", up.ID, "project_id", up.ProjectID, "yolo_version", yolo,
		"file_size", up.FileSize)
	return up, nil
}

// GetUpload returns one upload.
func (s *Service) GetUpload(ctx context.Context, id int64) (Upload, error) {
	return queryUpload(ctx, s.db, id)
}

// notUploading explains why an upload no longer accepts data.
func notUploading(up Upload) error {
	switch up.Status {
	case UploadCanceled:
		return &Error{Kind: ErrConflict, Message: "上传已取消"}
	case UploadFailed:
		return &Error{Kind: ErrConflict, Message: "上传已失败，请重新上传"}
	}
	return &Error{Kind: ErrConflict, Message: "上传已完成，不能继续上传"}
}

// errUploadFileLost: an upload that is still uploading has no staged file
// (any more), e.g. after the database was restored from a backup without
// the temporary files. What it received is gone; it has to start over.
var errUploadFileLost = &Error{Kind: ErrConflict, Message: "服务器上找不到这次上传已接收的数据（临时文件已丢失），请取消后重新上传"}

// bodyError marks errors from reading the request body, as opposed to
// writing to disk.
type bodyError struct{ err error }

func (e *bodyError) Error() string { return e.err.Error() }
func (e *bodyError) Unwrap() error { return e.err }

type bodyReader struct{ r io.Reader }

func (b bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = &bodyError{err}
	}
	return n, err
}

// receiveError classifies an error from copying a request body to disk.
// The HTTP layer wraps the body so that exceeding its size limit yields an
// *Error of kind ErrTooLarge, which is passed through; so are the errors of
// the lease renewal while receiving a conversion result.
func receiveError(err error) error {
	var ue *Error
	if errors.As(err, &ue) {
		return ue
	}
	var be *bodyError
	if !errors.As(err, &be) {
		return fmt.Errorf("store upload: %w", err)
	}
	return &Error{Kind: ErrBadRequest, Message: "上传数据接收不完整，请重试"}
}

// WriteChunk appends a chunk of the file at offset, which must equal the
// bytes received so far. A failed chunk leaves no partial data, so the
// client can resume from received_bytes.
func (s *Service) WriteChunk(ctx context.Context, id, offset int64, body io.Reader) (Upload, error) {
	unlock, err := s.uploads.lock(ctx, id)
	if err != nil {
		return Upload{}, err
	}
	defer unlock()

	up, err := s.GetUpload(ctx, id)
	if err != nil {
		return up, err
	}
	switch {
	case up.Status != UploadUploading:
		return up, notUploading(up)
	case offset != up.ReceivedBytes:
		return up, newError(ErrOffsetMismatch, "上传位置不一致：服务器已接收 %d 字节，请从该位置继续上传", up.ReceivedBytes)
	}
	remaining := up.FileSize - offset
	if err := s.checkFreeSpace(min(remaining, MaxChunkBytes)); err != nil {
		return up, err
	}

	// The first chunk creates the file in the folder CreateUpload made.
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(s.uploadPath(id), flags, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The folder is removed when the upload is canceled meanwhile;
			// otherwise the staged file is lost.
			cur, gerr := s.GetUpload(ctx, id)
			switch {
			case gerr != nil:
				return up, gerr
			case cur.Status != UploadUploading:
				return cur, notUploading(cur)
			}
			return cur, errUploadFileLost
		}
		return up, fmt.Errorf("open model upload file: %w", err)
	}
	defer f.Close()
	// The bytes received so far must all be there (Truncate would fill a
	// shorter file with zeros).
	if fi, err := f.Stat(); err != nil {
		return up, fmt.Errorf("stat model upload file: %w", err)
	} else if fi.Size() < offset {
		return up, errUploadFileLost
	}
	// Drop whatever an earlier, failed chunk may have left behind.
	if err := f.Truncate(offset); err != nil {
		return up, fmt.Errorf("truncate model upload file: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return up, fmt.Errorf("seek model upload file: %w", err)
	}
	n, err := io.Copy(f, bodyReader{io.LimitReader(body, remaining+1)})
	if err == nil && n > remaining {
		err = &Error{Kind: ErrBadRequest, Message: "上传的数据超过了声明的文件大小"}
	} else if err != nil {
		err = receiveError(err)
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		_ = f.Truncate(offset)
		return up, err
	}

	updated, err := scanUpload(s.db.QueryRow(ctx, `UPDATE model_uploads
		SET received_bytes = received_bytes + $2, updated_at = now()
		WHERE id = $1 AND status = 'uploading' AND received_bytes = $3
		RETURNING `+uploadColumns, id, n, offset))
	if errors.Is(err, errUploadNotFound) {
		_ = f.Truncate(offset)
		if cur, gerr := s.GetUpload(ctx, id); gerr == nil {
			return cur, notUploading(cur)
		}
	}
	return updated, err
}

// CommitInput overrides what the upload was created with; nil keeps it.
type CommitInput struct {
	YOLOVersion *string
	Description *string
}

// CommitUpload turns a fully received upload into the next version of its
// project. It reports whether the model was created now: committing an
// upload that is already completed returns its model unchanged, so a
// retried commit is harmless.
//
// A file that is not a usable Ultralytics model fails the upload
// (ErrInvalidModel) and its staged file is removed. A file whose YOLO
// version differs from the chosen one is refused (ErrVersionMismatch); the
// upload stays uploading with detected_yolo_version set, so the client can
// commit again with the right version.
func (s *Service) CommitUpload(ctx context.Context, id int64, in CommitInput) (Model, bool, error) {
	unlock, err := s.uploads.lock(ctx, id) // waits for a chunk in flight
	if err != nil {
		return Model{}, false, err
	}
	defer unlock()
	// Hashing a large file takes a while; a client that gives up must not
	// interrupt the move into the model folder.
	ctx, cancel := s.detach(ctx)
	defer cancel()

	up, err := s.GetUpload(ctx, id)
	if err != nil {
		return Model{}, false, err
	}
	switch up.Status {
	case UploadCompleted:
		if up.ModelID != nil {
			m, err := s.GetModel(ctx, *up.ModelID)
			if !errors.Is(err, ErrNotFound) {
				return m, false, err
			}
		}
		return Model{}, false, &Error{Kind: ErrConflict, Message: "上传已完成，但其模型已被删除"}
	case UploadUploading:
	default:
		return Model{}, false, notUploading(up)
	}
	yolo, desc := up.YOLOVersion, up.Description
	if in.YOLOVersion != nil {
		if yolo, err = normalizeYOLOVersion(*in.YOLOVersion); err != nil {
			return Model{}, false, err
		}
	}
	if in.Description != nil {
		if desc, err = NormalizeDescription(*in.Description); err != nil {
			return Model{}, false, err
		}
	}
	if up.ReceivedBytes != up.FileSize {
		return Model{}, false, newError(ErrConflict, "模型文件尚未上传完成（已上传 %s / %s）",
			formatBytes(uint64(up.ReceivedBytes)), formatBytes(uint64(up.FileSize)))
	}
	path := s.uploadPath(id)
	if fi, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Model{}, false, errUploadFileLost
	} else if err != nil || fi.Size() != up.FileSize {
		return Model{}, false, &Error{Kind: ErrConflict, Message: "服务器上的模型文件不完整，请取消后重新上传"}
	}

	var ended *Upload // the upload was canceled (or deleted) meanwhile
	m, err := s.addModel(ctx, newModel{ProjectID: up.ProjectID, Source: SourceUpload, YOLOVersion: yolo,
		Description: desc, FileName: up.FileName, SrcPath: path, UploadID: id,
		inTx: func(ctx context.Context, tx pgx.Tx, modelID int64) error {
			cur, err := scanUpload(tx.QueryRow(ctx, `UPDATE model_uploads SET status = 'completed', model_id = $2,
					yolo_version = $3, description = $4, detected_yolo_version = NULL, error = '', updated_at = now()
				WHERE id = $1 AND status = 'uploading'
				RETURNING `+uploadColumns, id, modelID, yolo, desc))
			if errors.Is(err, errUploadNotFound) {
				cur, err = queryUpload(ctx, tx, id)
				if err == nil {
					ended = &cur
					return notUploading(cur)
				}
			}
			return err
		}})
	if err == nil {
		s.removeUploadDir(id)
		return m, true, nil
	}
	if ended != nil {
		return Model{}, false, err
	}
	var me *Error
	if !errors.As(err, &me) {
		return Model{}, false, err
	}
	switch me.Kind {
	case ErrVersionMismatch:
		if _, uerr := s.db.Exec(ctx, `UPDATE model_uploads SET detected_yolo_version = $2, yolo_version = $3,
				description = $4, updated_at = now()
			WHERE id = $1 AND status = 'uploading'`, id, me.detected, yolo, desc); uerr != nil {
			return Model{}, false, uerr
		}
		s.log.Info("model upload refused: YOLO version mismatch", "upload_id", id, "chosen", yolo, "detected", me.detected)
	case ErrInvalidModel:
		if _, uerr := s.db.Exec(ctx, `UPDATE model_uploads SET status = 'failed', error = $2, updated_at = now()
			WHERE id = $1 AND status = 'uploading'`, id, me.Message); uerr != nil {
			return Model{}, false, uerr
		}
		s.removeUploadDir(id)
		s.log.Info("model upload failed: not a supported model", "upload_id", id, "reason", me.Message)
	case ErrInvalid:
		if errors.Is(err, errProjectGone) { // deleted with its project
			s.removeUploadDir(id)
		}
	}
	return Model{}, false, err
}

// CancelUpload cancels an upload and removes its staged file. Canceling an
// upload that has ended returns it unchanged.
func (s *Service) CancelUpload(ctx context.Context, id int64) (Upload, error) {
	up, err := scanUpload(s.db.QueryRow(ctx, `UPDATE model_uploads SET status = 'canceled', updated_at = now()
		WHERE id = $1 AND status = 'uploading'
		RETURNING `+uploadColumns, id))
	if err == nil {
		s.removeUploadDir(id)
		s.log.Info("model upload canceled", "upload_id", id)
		return up, nil
	}
	if !errors.Is(err, errUploadNotFound) {
		return up, err
	}
	return s.GetUpload(ctx, id)
}
