package dataset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

const (
	// MaxChunkBytes is the largest archive chunk accepted per request.
	MaxChunkBytes = 64 << 20
	// MaxFolderRequestBytes is the largest folder upload request.
	MaxFolderRequestBytes = 256 << 20
	// reserveBytes is the free disk space uploads and imports always leave.
	reserveBytes = 512 << 20
	// maxDeclaredSize rejects absurd sizes before any arithmetic on them.
	maxDeclaredSize = 1 << 50
	// cancelWait is how long Cancel waits for a running job to stop.
	cancelWait = 3 * time.Second
)

const (
	uploadFileName = "upload.bin" // the archive being uploaded
	stagedFilesDir = "files"      // extracted / uploaded entries
	stagedPartsDir = "parts"      // folder upload parts being received
)

// CreateImportInput describes an upload the client is about to send.
type CreateImportInput struct {
	Source     string // SourceArchive or SourceFolder
	FileName   string // archive file name or folder name
	FileSize   int64  // archive size, or total size of the folder's files
	FileCount  int    // number of files (folder)
	OnConflict string // ConflictSkip (default) or ConflictOverwrite
}

// CreateImport creates an import job in state uploading and prepares its
// staging folder.
func (s *Service) CreateImport(ctx context.Context, datasetID int64, in CreateImportInput) (Import, error) {
	n, err := validateImportInput(datasetID, in)
	if err != nil {
		return Import{}, err
	}
	d, err := queryDataset(ctx, s.db, datasetID, false)
	if err != nil {
		return Import{}, err
	}
	if !s.dirExists(d.DirName) {
		return Import{}, errDatasetDirMissing(s.datasetDir(d.DirName))
	}
	// An archive needs room for itself and its extracted content; a folder
	// is staged once and then moved.
	need := n.FileSize
	if n.Source == SourceArchive {
		need *= 2
	}
	if err := s.checkFreeSpace(need); err != nil {
		return Import{}, err
	}
	job, err := insertImport(ctx, s.db, n)
	if err != nil {
		return Import{}, err
	}
	if err := os.MkdirAll(s.stagingDir(job.ID), 0o755); err != nil {
		_, _ = s.db.Exec(context.WithoutCancel(ctx), "DELETE FROM dataset_imports WHERE id = $1", job.ID)
		return Import{}, fmt.Errorf("create import staging folder: %w", err)
	}
	s.log.Info("dataset upload started", "dataset_id", datasetID, "import_id", job.ID, "source", job.Source,
		"file_size", job.FileSize, "file_count", job.FileCount)
	return job, nil
}

func validateImportInput(datasetID int64, in CreateImportInput) (newImport, error) {
	invalid := func(msg string) (newImport, error) { return newImport{}, &Error{Kind: ErrInvalid, Message: msg} }
	n := newImport{DatasetID: datasetID, Source: in.Source, Status: StatusUploading, FileSize: in.FileSize,
		FileCount: in.FileCount, OnConflict: in.OnConflict, FileName: strings.TrimSpace(in.FileName)}
	switch {
	case n.Source != SourceArchive && n.Source != SourceFolder:
		return invalid("导入类型必须是 archive（压缩包）或 folder（文件夹）")
	case n.Source == SourceArchive && n.FileName == "":
		return invalid("请提供压缩包文件名")
	case !utf8.ValidString(n.FileName) || hasControlChar(n.FileName):
		return invalid("文件名包含无效字符")
	case utf8.RuneCountInString(n.FileName) > maxFileNameRunes:
		return invalid("文件名不能超过 255 个字符")
	case n.Source == SourceArchive && n.FileSize < 1:
		return invalid("压缩包大小无效")
	case n.FileSize < 0 || n.FileCount < 0:
		return invalid("文件大小或数量无效")
	case n.FileSize > maxDeclaredSize:
		return invalid("文件过大")
	case n.FileCount > math.MaxInt32: // file_count is an integer column
		return invalid("文件数量过多")
	}
	switch n.OnConflict {
	case "":
		n.OnConflict = ConflictSkip
	case ConflictSkip, ConflictOverwrite:
	default:
		return invalid("同名文件处理方式必须是 skip（跳过）或 overwrite（覆盖）")
	}
	return n, nil
}

// checkFreeSpace fails with ErrInsufficientStorage unless need bytes plus
// the reserve are available. Platforms without disk usage support skip it.
func (s *Service) checkFreeSpace(need int64) error {
	u, err := s.storage.Usage()
	if err != nil {
		if !errors.Is(err, storage.ErrUsageUnsupported) {
			s.log.Warn("check free disk space", "err", err)
		}
		return nil
	}
	want := uint64(need) + reserveBytes
	if u.FreeBytes < want {
		return newError(ErrInsufficientStorage, "磁盘剩余空间不足：可用 %s，至少需要 %s",
			formatBytes(u.FreeBytes), formatBytes(want))
	}
	return nil
}

// freeBytes returns the free disk space of the data directory.
func (s *Service) freeBytes() (uint64, error) {
	u, err := s.storage.Usage()
	return u.FreeBytes, err
}

// extractionBudget is how many bytes an archive may expand to.
func (s *Service) extractionBudget() int64 {
	if n := s.maxExtract.Load(); n > 0 {
		return n
	}
	u, err := s.storage.Usage()
	if err != nil {
		return maxDeclaredSize
	}
	if u.FreeBytes <= reserveBytes {
		return 0
	}
	return int64(min(u.FreeBytes-reserveBytes, maxDeclaredSize))
}

// formatBytes renders a byte count for messages, e.g. "1.5 GB".
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, i := float64(n)/unit, 0
	for value >= unit && i < 4 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"KB", "MB", "GB", "TB", "PB"}[i])
}

// GetImport returns one job of a dataset.
func (s *Service) GetImport(ctx context.Context, datasetID, importID int64) (Import, error) {
	return s.getImport(ctx, s.db, datasetID, importID, false)
}

// getImport loads a job and tells "no such dataset" from "no such job".
func (s *Service) getImport(ctx context.Context, q querier, datasetID, importID int64, forUpdate bool) (Import, error) {
	job, err := queryImport(ctx, q, datasetID, importID, forUpdate)
	if errors.Is(err, errImportNotFound) {
		if _, derr := queryDataset(ctx, q, datasetID, false); derr != nil {
			return job, derr
		}
	}
	return job, err
}

// ListImports returns the newest jobs of a dataset and the number of all
// its jobs.
func (s *Service) ListImports(ctx context.Context, datasetID int64, limit int) ([]Import, int, error) {
	if _, err := queryDataset(ctx, s.db, datasetID, false); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM dataset_imports WHERE dataset_id = $1", datasetID).Scan(&total); err != nil {
		return nil, 0, err
	}
	jobs, err := collectImports(s.db.Query(ctx, "SELECT "+importColumns+` FROM dataset_imports
		WHERE dataset_id = $1 ORDER BY id DESC LIMIT $2`, datasetID, limit))
	return jobs, total, err
}

// notUploading explains why a job no longer accepts uploads.
func notUploading(job Import) error {
	switch job.Status {
	case StatusCanceled:
		return &Error{Kind: ErrConflict, Message: "导入任务已取消"}
	case StatusFailed:
		return &Error{Kind: ErrConflict, Message: "导入任务已失败，请重新导入"}
	}
	return &Error{Kind: ErrConflict, Message: "导入任务已提交，不能继续上传"}
}

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
// *Error of kind ErrTooLarge, which is passed through.
func receiveError(err error) error {
	var be *bodyError
	if !errors.As(err, &be) {
		return fmt.Errorf("store upload: %w", err)
	}
	var ue *Error
	if errors.As(be.err, &ue) {
		return ue
	}
	return &Error{Kind: ErrBadRequest, Message: "上传数据接收不完整，请重试"}
}

// WriteArchiveChunk appends a chunk of the archive at offset, which must
// equal the bytes received so far. A failed chunk leaves no partial data,
// so the client can resume from received_bytes.
func (s *Service) WriteArchiveChunk(ctx context.Context, datasetID, importID, offset int64, body io.Reader) (Import, error) {
	unlock, err := s.uploads.lock(ctx, importID)
	if err != nil {
		return Import{}, err
	}
	defer unlock()

	job, err := s.GetImport(ctx, datasetID, importID)
	if err != nil {
		return job, err
	}
	switch {
	case job.Source != SourceArchive:
		return job, &Error{Kind: ErrBadRequest, Message: "该导入任务不是压缩包上传"}
	case job.Status != StatusUploading:
		return job, notUploading(job)
	case offset != job.ReceivedBytes:
		return job, newError(ErrOffsetMismatch, "上传位置不一致：服务器已接收 %d 字节，请从该位置继续上传", job.ReceivedBytes)
	}
	remaining := job.FileSize - offset
	if err := s.checkFreeSpace(min(remaining, MaxChunkBytes)); err != nil {
		return job, err
	}

	f, err := os.OpenFile(filepath.Join(s.stagingDir(importID), uploadFileName), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // staging removed: canceled meanwhile
			if cur, gerr := s.GetImport(ctx, datasetID, importID); gerr == nil && cur.Status != StatusUploading {
				return cur, notUploading(cur)
			}
		}
		return job, fmt.Errorf("open upload file: %w", err)
	}
	defer f.Close()
	// Drop whatever an earlier, failed chunk may have left behind.
	if err := f.Truncate(offset); err != nil {
		return job, fmt.Errorf("truncate upload file: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return job, fmt.Errorf("seek upload file: %w", err)
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
		return job, err
	}

	updated, err := scanImport(s.db.QueryRow(ctx, `UPDATE dataset_imports
		SET received_bytes = received_bytes + $3, updated_at = now()
		WHERE id = $1 AND dataset_id = $2 AND status = 'uploading' AND received_bytes = $4
		RETURNING `+importColumns, importID, datasetID, n, offset))
	if errors.Is(err, errImportNotFound) {
		_ = f.Truncate(offset)
		if cur, gerr := s.GetImport(ctx, datasetID, importID); gerr == nil {
			return cur, notUploading(cur)
		}
	}
	return updated, err
}

// FileSource yields the files of a folder upload request: the relative
// path inside the uploaded folder and the content. It returns io.EOF after
// the last file.
type FileSource func() (rel string, content io.Reader, err error)

// AddFolderFiles stores uploaded files of a folder import in its staging
// folder. Files that are neither images nor XML are skipped. Uploading the
// same path again replaces the earlier copy without counting it twice, so
// clients can retry a failed request as a whole.
func (s *Service) AddFolderFiles(ctx context.Context, datasetID, importID int64, next FileSource) (Import, error) {
	job, err := s.GetImport(ctx, datasetID, importID)
	if err != nil {
		return job, err
	}
	switch {
	case job.Source != SourceFolder:
		return job, &Error{Kind: ErrBadRequest, Message: "该导入任务不是文件夹上传"}
	case job.Status != StatusUploading:
		return job, notUploading(job)
	}
	if err := s.checkFreeSpace(0); err != nil {
		return job, err
	}
	staging := s.stagingDir(importID)
	partsDir := filepath.Join(staging, stagedPartsDir)
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		return job, fmt.Errorf("create upload parts folder: %w", err)
	}

	var addFiles int
	var addBytes int64
	storeErr := func() error {
		for {
			rel, content, err := next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return receiveError(&bodyError{err})
			}
			clean, ok := cleanEntryPath(rel)
			if !ok {
				return newError(ErrBadRequest, "文件路径无效：%s", rel)
			}
			if classify(clean) == kindIgnored {
				continue
			}
			files, bytes, err := s.storeFolderFile(ctx, importID, partsDir, filepath.Join(staging, stagedFilesDir), clean, content)
			if err != nil {
				return err
			}
			addFiles += files
			addBytes += bytes
		}
	}()

	// Count what was stored even if the request failed midway.
	if addFiles != 0 || addBytes != 0 {
		updated, err := scanImport(s.db.QueryRow(context.WithoutCancel(ctx), `UPDATE dataset_imports
			SET received_files = received_files + $3, received_bytes = received_bytes + $4, updated_at = now()
			WHERE id = $1 AND dataset_id = $2 AND status = 'uploading'
			RETURNING `+importColumns, importID, datasetID, addFiles, addBytes))
		switch {
		case errors.Is(err, errImportNotFound):
			if cur, gerr := s.GetImport(ctx, datasetID, importID); gerr == nil {
				return cur, notUploading(cur)
			}
			return job, err
		case err != nil:
			return job, err
		}
		job = updated
	}
	return job, storeErr
}

// storeFolderFile receives one file into the parts folder and then moves
// it to its staging path. It returns how the received counters change.
func (s *Service) storeFolderFile(ctx context.Context, importID int64, partsDir, filesDir, rel string, content io.Reader) (files int, bytes int64, err error) {
	tmp, err := os.CreateTemp(partsDir, "part-*")
	if err != nil {
		return 0, 0, fmt.Errorf("create upload part: %w", err)
	}
	// CreateTemp uses 0600; the file ends up in the dataset folder, which
	// other tools (and users on the host) read.
	_ = tmp.Chmod(0o644)
	n, err := io.Copy(tmp, bodyReader{content})
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return 0, 0, receiveError(err)
	}

	// Concurrent requests (and retries) may deliver the same path; the
	// check and the rename must not interleave or a file counts twice.
	unlock, err := s.uploads.lock(ctx, importID)
	if err != nil {
		_ = os.Remove(tmp.Name())
		return 0, 0, err
	}
	defer unlock()
	dst := filepath.Join(filesDir, filepath.FromSlash(rel))
	prev, statErr := os.Lstat(dst)
	err = os.MkdirAll(filepath.Dir(dst), 0o755)
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		if isPathConflict(err) {
			return 0, 0, newError(ErrBadRequest, "文件路径冲突：%s", rel)
		}
		return 0, 0, fmt.Errorf("stage uploaded file: %w", err)
	}
	if statErr == nil && prev.Mode().IsRegular() {
		return 0, n - prev.Size(), nil
	}
	return 1, n, nil
}

// CommitImport finishes the upload phase and queues the job. Committing a
// job that is already queued, running or done returns it unchanged, so a
// retried commit is harmless.
func (s *Service) CommitImport(ctx context.Context, datasetID, importID int64) (Import, error) {
	unlock, err := s.uploads.lock(ctx, importID) // waits for a chunk in flight
	if err != nil {
		return Import{}, err
	}
	defer unlock()

	var job Import
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// Lock the dataset first, like DeleteDataset does.
		if _, err := queryDataset(ctx, tx, datasetID, true); err != nil {
			return err
		}
		var err error
		if job, err = queryImport(ctx, tx, datasetID, importID, true); err != nil {
			return err
		}
		switch job.Status {
		case StatusQueued, StatusRunning, StatusSucceeded:
			return nil
		case StatusUploading:
		default:
			return notUploading(job)
		}
		if job.Source == SourceArchive {
			if job.ReceivedBytes != job.FileSize {
				return newError(ErrConflict, "压缩包尚未上传完成（已上传 %s / %s）",
					formatBytes(uint64(job.ReceivedBytes)), formatBytes(uint64(job.FileSize)))
			}
			fi, err := os.Stat(filepath.Join(s.stagingDir(importID), uploadFileName))
			if err != nil || fi.Size() != job.FileSize {
				return &Error{Kind: ErrConflict, Message: "服务器上的压缩包不完整，请取消后重新上传"}
			}
		}
		job, err = scanImport(tx.QueryRow(ctx, `UPDATE dataset_imports SET status = 'queued', updated_at = now()
			WHERE id = $1 RETURNING `+importColumns, importID))
		return err
	})
	if err != nil {
		return job, err
	}
	if job.Status == StatusQueued {
		s.runner.notify()
	}
	return job, nil
}

// CancelImport cancels a job. Uploading and queued jobs end at once;
// a running job is asked to stop and ends as canceled at the next file
// (files already moved into the dataset stay and are indexed). Canceling a
// finished job returns it unchanged.
func (s *Service) CancelImport(ctx context.Context, datasetID, importID int64) (Import, error) {
	job, err := scanImport(s.db.QueryRow(ctx, `UPDATE dataset_imports
		SET status = 'canceled', finished_at = now(), updated_at = now()
		WHERE id = $1 AND dataset_id = $2 AND status IN ('uploading', 'queued')
		RETURNING `+importColumns, importID, datasetID))
	if err == nil {
		s.removeStaging(importID)
		s.log.Info("dataset job canceled", "import_id", importID, "dataset_id", datasetID)
		return job, nil
	}
	if !errors.Is(err, errImportNotFound) {
		return job, err
	}
	if job, err = s.GetImport(ctx, datasetID, importID); err != nil || job.Status != StatusRunning {
		return job, err
	}
	s.runner.cancel(importID)
	s.log.Info("dataset job cancel requested", "import_id", importID, "dataset_id", datasetID)
	deadline := time.Now().Add(cancelWait)
	for job.Status == StatusRunning && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if job, err = s.GetImport(ctx, datasetID, importID); err != nil {
			return job, err
		}
	}
	if job.Status != StatusRunning {
		s.runner.forget(importID)
	}
	return job, nil
}

// StartScan queues a scan job that re-synchronizes the index with the
// dataset folder.
func (s *Service) StartScan(ctx context.Context, datasetID int64) (Import, error) {
	var job Import
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		d, err := queryDataset(ctx, tx, datasetID, true)
		if err != nil {
			return err
		}
		busy, err := hasProcessingJob(ctx, tx, datasetID)
		if err != nil {
			return err
		}
		if busy {
			return &Error{Kind: ErrConflict, Message: "数据集正在导入或扫描，请等待完成后再试"}
		}
		job, err = insertImport(ctx, tx, newImport{DatasetID: datasetID, Source: SourceScan, Status: StatusQueued,
			OnConflict: ConflictSkip, FileName: d.DirName})
		return err
	})
	if err != nil {
		return job, err
	}
	s.runner.notify()
	return job, nil
}

// keyedMutex is a set of mutexes identified by import ID whose Lock can be
// abandoned when the request ends.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[int64]*keyedLock
}

type keyedLock struct {
	token chan struct{} // holds a value while locked
	refs  int
}

func (k *keyedMutex) lock(ctx context.Context, key int64) (unlock func(), err error) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[int64]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{token: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	select {
	case l.token <- struct{}{}:
		return func() { <-l.token; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
