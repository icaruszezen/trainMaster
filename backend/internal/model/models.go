package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// ---- adding models ----

// newModel describes a weights file on disk that is to become a model.
type newModel struct {
	ProjectID     int64
	Source        string
	TrainingJobID *int64
	YOLOVersion   string // validated
	Description   string // normalized
	FileName      string // original file name, normalized
	// SrcPath is the file; it is moved into the model folder, so it must be
	// on the same file system (under the data directory) and nobody else
	// may write to it meanwhile.
	SrcPath string
	// UploadID is the upload being committed (0 for other sources). The
	// model folder names it until the commit, so that the file can go back
	// to the upload after a crash (recoverPending).
	UploadID int64
	// inTx runs in the transaction that inserts the model, after the insert
	// (the upload commit marks its upload as completed).
	inTx func(ctx context.Context, tx pgx.Tx, modelID int64) error
}

// maxVersionSkips bounds how many versions addModel skips because their
// folder already exists.
const maxVersionSkips = 100

// addModel turns a weights file into a model: it hashes and inspects the
// file, checks the chosen YOLO version, allocates the next version of the
// project (under the project's row lock, so concurrent additions get
// distinct versions), moves the file into models/project-<id>/v<N>/ and
// writes the model.json sidecar. Everything on disk happens inside the
// transaction and is undone if it fails, so a model row never exists
// without its folder (unless the folder is removed by hand later). Until
// the commit the folder holds the .pending marker: if the process dies
// before the commit, recoverPending clears the folder later.
func (s *Service) addModel(ctx context.Context, n newModel) (Model, error) {
	f, err := os.Open(n.SrcPath)
	if err != nil {
		return Model{}, fmt.Errorf("open model file: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return Model{}, fmt.Errorf("stat model file: %w", err)
	}
	size := fi.Size()
	if !fi.Mode().IsRegular() || size == 0 {
		f.Close()
		return Model{}, &Error{Kind: ErrInvalidModel, Message: "模型文件是空的"}
	}
	sum, err := hashFile(ctx, f)
	var ci *checkpoint.Info
	if err == nil {
		ci, err = s.inspectFile(ctx, f, size)
	}
	f.Close()
	if err != nil {
		var ce *checkpoint.Error
		if errors.As(err, &ce) {
			return Model{}, &Error{Kind: ErrInvalidModel, Message: ce.Message}
		}
		return Model{}, fmt.Errorf("inspect model file: %w", err)
	}
	if ci == nil {
		return Model{}, errors.New("inspect model file: no result")
	}
	if err := checkYOLOVersion(n.YOLOVersion, ci); err != nil {
		return Model{}, err
	}
	info := newInfo(ci)
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return Model{}, fmt.Errorf("encode model info: %w", err)
	}

	var (
		id      int64
		dir     string // the model folder, once created
		movedTo string // the file in it, once moved
	)
	// The folder is in use (recoverPending leaves it alone) from before it
	// is created until it is complete or removed again.
	var busyDir string
	defer func() {
		if busyDir != "" {
			s.busy.remove(busyDir)
		}
	}()
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var version int
		var projectName, dirName, target string
		for skips := 0; ; skips++ {
			// The update locks the project row until the transaction ends:
			// concurrent additions to the project wait here, and deleting the
			// project waits for the model.
			err := tx.QueryRow(ctx, `UPDATE projects SET model_version_seq = model_version_seq + 1
				WHERE id = $1 RETURNING model_version_seq, name`, n.ProjectID).Scan(&version, &projectName)
			if errors.Is(err, pgx.ErrNoRows) {
				return errProjectGone
			}
			if err != nil {
				return err
			}
			dirName = modelDirName(n.ProjectID, version)
			if target, err = s.modelDir(dirName); err != nil {
				return err
			}
			// A folder can only exist if an earlier attempt crashed before
			// its commit (its version was rolled back), or if files were
			// restored by hand. It belongs to no model. The crash leaves the
			// .pending marker: such a folder is cleared and its version used.
			// Others are kept and their version skipped.
			if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
				break
			} else if err != nil {
				return fmt.Errorf("check model folder: %w", err)
			}
			if gone, err := s.recoverPending(ctx, tx, dirName); gone {
				break
			} else if err != nil {
				s.log.Warn("clear model folder of an interrupted addition", "path", target, "err", err)
			}
			s.log.Warn("model folder already exists; skipping its version", "path", target, "version", version)
			if skips == maxVersionSkips {
				return fmt.Errorf("model folders of %d versions already exist", maxVersionSkips)
			}
		}
		var createdAt time.Time
		err := tx.QueryRow(ctx, `INSERT INTO models (project_id, version, description, source, training_job_id,
				yolo_version, task, file_name, dir_name, size_bytes, sha256, info)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id, created_at`,
			n.ProjectID, version, n.Description, n.Source, n.TrainingJobID, n.YOLOVersion, info.Task, n.FileName,
			dirName, size, sum, infoJSON).Scan(&id, &createdAt)
		if err != nil {
			return err
		}
		if n.inTx != nil {
			if err := n.inTx(ctx, tx, id); err != nil {
				return err
			}
		}

		s.busy.add(target)
		busyDir = target
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("create model folder: %w", err)
		}
		dir = target
		if err := writePendingMarker(dir, pendingMarker{UploadID: n.UploadID}); err != nil {
			return err
		}
		dst := filepath.Join(dir, modelFile)
		if err := os.Rename(n.SrcPath, dst); err != nil {
			_ = os.RemoveAll(dir)
			dir = ""
			return fmt.Errorf("move model file: %w", err)
		}
		movedTo = dst
		return writeSidecar(dir, sidecar{
			ModelID: id, Project: ProjectRef{ID: n.ProjectID, Name: projectName}, Version: version,
			Source: n.Source, TrainingJobID: n.TrainingJobID, YOLOVersion: n.YOLOVersion, FileName: n.FileName,
			SizeBytes: size, SHA256: sum, CreatedAt: createdAt.UTC(), Info: info,
		})
	})
	if err != nil {
		if movedTo != "" {
			if rerr := os.Rename(movedTo, n.SrcPath); rerr != nil {
				s.log.Error("move model file back after failed commit", "path", movedTo, "err", rerr)
				return Model{}, err
			}
		}
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		return Model{}, err
	}
	// Committed: the folder belongs to the model now. A marker that cannot
	// be removed is harmless (recoverPending removes it later).
	if err := os.Remove(filepath.Join(dir, pendingFile)); err != nil {
		s.log.Warn("remove model folder marker", "path", dir, "err", err)
	}
	m, err := s.GetModel(ctx, id)
	if err == nil {
		s.log.Info("model added", "model_id", id, "project_id", n.ProjectID, "version", m.Version, "source", n.Source,
			"yolo_version", n.YOLOVersion, "task", m.Task, "size_bytes", size)
	}
	return m, err
}

// inspectFile reads the metadata of a weights file once one of the
// maxInspections slots is free. It waits until ctx ends: commits detach
// their context from the request, so only the service stopping ends it.
func (s *Service) inspectFile(ctx context.Context, r io.ReaderAt, size int64) (*checkpoint.Info, error) {
	select {
	case s.inspectSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.inspectSlots }()
	return s.inspect(r, size)
}

// hashFile returns the hex SHA-256 of f from its start, stopping early
// when ctx ends.
func hashFile(ctx context.Context, f *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx: ctx, r: io.NewSectionReader(f, 0, 1<<62)}); err != nil {
		return "", fmt.Errorf("hash model file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader fails reads once ctx has ended.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sidecar is model.json: a description of the model next to its file, so
// that a copy or backup of models/ explains itself without the database.
type sidecar struct {
	ModelID       int64      `json:"model_id"`
	Project       ProjectRef `json:"project"`
	Version       int        `json:"version"`
	Source        string     `json:"source"`
	TrainingJobID *int64     `json:"training_job_id"`
	YOLOVersion   string     `json:"yolo_version"`
	FileName      string     `json:"file_name"` // original name of model.pt
	SizeBytes     int64      `json:"size_bytes"`
	SHA256        string     `json:"sha256"`
	CreatedAt     time.Time  `json:"created_at"`
	Info          Info       `json:"info"`
}

// writeSidecar writes model.json into dir through a temporary file, so
// readers never see half of it.
func writeSidecar(dir string, sc sidecar) error {
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", sidecarFile, err)
	}
	tmp, err := os.CreateTemp(dir, "."+sidecarFile+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", sidecarFile, err)
	}
	_ = tmp.Chmod(0o644)
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, sidecarFile))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", sidecarFile, err)
	}
	return nil
}

// pendingMarker is the content of .pending, which marks a model folder
// whose addModel has not committed yet.
type pendingMarker struct {
	UploadID int64 `json:"upload_id,omitempty"` // the upload the file came from, if any
}

// writePendingMarker creates .pending in dir and syncs it, so that it is
// on disk before the weights are moved in.
func writePendingMarker(dir string, pm pendingMarker) error {
	data, err := json.Marshal(pm)
	if err != nil {
		return fmt.Errorf("encode %s: %w", pendingFile, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, pendingFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", pendingFile, err)
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", pendingFile, err)
	}
	return nil
}

// recoverPending clears the model folder dirName if the addModel that
// created it was interrupted before its commit (the process died: the
// folder still has the .pending marker, and no model owns it). The weights
// go back to their upload if it is still waiting for them, so that the
// upload can be committed again; the folder is then moved aside for the
// janitor (cleanUnused). A folder a model owns only loses its stale
// marker. It reports whether the folder is gone; folders without the
// marker, and those in use by this process, are left alone. q sees the
// committed models (the pool, or the transaction of addModel).
func (s *Service) recoverPending(ctx context.Context, q querier, dirName string) (bool, error) {
	dir, err := s.modelDir(dirName)
	if err != nil {
		return false, err
	}
	if !s.busy.tryAdd(dir) {
		return false, nil // an addition in progress, or another recovery
	}
	defer s.busy.remove(dir)
	marker := filepath.Join(dir, pendingFile)
	data, err := os.ReadFile(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", pendingFile, err)
	}
	var owned bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM models WHERE dir_name = $1)", dirName).Scan(&owned); err != nil {
		return false, err
	}
	if owned { // the process died after the commit
		if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("remove %s: %w", pendingFile, err)
		}
		return false, nil
	}
	var pm pendingMarker
	if json.Unmarshal(data, &pm) == nil && pm.UploadID > 0 {
		if err := s.returnToUpload(ctx, pm.UploadID, filepath.Join(dir, modelFile)); err != nil {
			return false, err // the upload may still need the file: try again later
		}
	}
	trash := filepath.Join(s.modelsDir(), deletingPrefix+"pending-"+strings.ReplaceAll(dirName, "/", "-")+"-"+
		strings.ToLower(rand.Text()[:8]))
	if err := os.Rename(dir, trash); err != nil {
		return false, fmt.Errorf("move model folder aside: %w", err)
	}
	s.log.Warn("cleared the model folder of an addition interrupted before its commit", "dir", dirName,
		"upload_id", pm.UploadID)
	return true, nil
}

// returnToUpload moves the weights of an interrupted commit (src) back to
// the staged file of their upload, if the upload is still uploading, has
// received the whole file and has lost its staged file (it was moved
// away). It fails when that cannot be decided or done now; nil means the
// file is back or not needed.
func (s *Service) returnToUpload(ctx context.Context, uploadID int64, src string) error {
	// Waiting long could only mean the upload is busy; try next time.
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	unlock, err := s.uploads.lock(lockCtx, uploadID)
	if err != nil {
		return fmt.Errorf("lock model upload %d: %w", uploadID, err)
	}
	defer unlock()
	up, err := s.GetUpload(ctx, uploadID)
	if errors.Is(err, ErrNotFound) { // deleted with its project
		return nil
	}
	if err != nil {
		return err
	}
	if up.Status != UploadUploading || up.ReceivedBytes != up.FileSize {
		return nil
	}
	if _, err := os.Lstat(s.uploadPath(uploadID)); err == nil {
		return nil // it has its file
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check model upload file: %w", err)
	}
	if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() || fi.Size() != up.FileSize {
		return nil // not moved in yet, or not the upload's file
	}
	err = os.MkdirAll(s.uploadDir(uploadID), 0o755)
	if err == nil {
		err = os.Rename(src, s.uploadPath(uploadID))
	}
	if err != nil {
		return fmt.Errorf("move the file of an interrupted commit back to upload %d: %w", uploadID, err)
	}
	s.log.Info("moved the file of an interrupted commit back to its upload", "upload_id", uploadID)
	return nil
}

// recoverPendingIn runs recoverPending on the model folders of a project
// folder (e.g. project-12) that have the .pending marker.
func (s *Service) recoverPendingIn(ctx context.Context, projectDir string) {
	entries, err := os.ReadDir(filepath.Join(s.modelsDir(), projectDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := projectDir + "/" + e.Name()
		if _, err := os.Lstat(filepath.Join(s.modelsDir(), projectDir, e.Name(), pendingFile)); err != nil {
			continue
		}
		if _, err := s.recoverPending(ctx, s.db, dirName); err != nil && ctx.Err() == nil {
			s.log.Warn("clear model folder of an interrupted addition", "dir", dirName, "err", err)
		}
	}
}

// recoverPendingFolders runs recoverPendingIn on every project folder.
func (s *Service) recoverPendingFolders(ctx context.Context) {
	entries, err := os.ReadDir(s.modelsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "project-") {
			s.recoverPendingIn(ctx, e.Name())
		}
	}
}

// TrainedModelInput describes the weights a training job produced.
type TrainedModelInput struct {
	ProjectID     int64
	TrainingJobID int64 // required
	YOLOVersion   string
	Description   string
	// WeightsPath is the weights file, slash-separated and relative to the
	// storage directory runs/ (e.g. job-7/weights/best.pt). It is copied:
	// the run keeps its files.
	WeightsPath string
	// FileName is shown as the original file name; "" means the base name
	// of WeightsPath.
	FileName string
}

// RegisterTrainedModel adds the weights of a finished training job as the
// next version of its project (source "training"). Training jobs are not
// implemented yet; this is the entry point they will use, with the same
// checks and storage as uploads.
func (s *Service) RegisterTrainedModel(ctx context.Context, in TrainedModelInput) (Model, error) {
	if in.TrainingJobID <= 0 {
		return Model{}, &Error{Kind: ErrInvalid, Message: "缺少训练任务 ID"}
	}
	yolo, err := normalizeYOLOVersion(in.YOLOVersion)
	if err != nil {
		return Model{}, err
	}
	desc, err := NormalizeDescription(in.Description)
	if err != nil {
		return Model{}, err
	}
	src, err := s.storage.Resolve(storage.Runs, in.WeightsPath)
	if err != nil {
		return Model{}, newError(ErrInvalid, "权重文件路径无效：%s", truncateRunes(in.WeightsPath, 256))
	}
	name := in.FileName
	if name == "" {
		name = filepath.Base(src)
	}
	if name, err = normalizeFileName(name); err != nil {
		return Model{}, err
	}
	fi, err := os.Stat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Model{}, newError(ErrNotFound, "权重文件不存在：%s", in.WeightsPath)
	case err != nil:
		return Model{}, fmt.Errorf("stat trained weights: %w", err)
	case !fi.Mode().IsRegular():
		return Model{}, newError(ErrInvalid, "权重文件不是普通文件：%s", in.WeightsPath)
	}
	if err := s.checkFreeSpace(fi.Size()); err != nil {
		return Model{}, err
	}

	// Copy into tmp/ first: the copy is what gets hashed and inspected (the
	// run's file could change meanwhile), and moving it in is atomic.
	staging, err := s.newRegistrationDir()
	if err != nil {
		return Model{}, err
	}
	defer func() {
		_ = os.RemoveAll(staging)
		s.busy.remove(staging)
	}()
	copied := filepath.Join(staging, modelFile)
	if err := copyFile(ctx, src, copied); err != nil {
		return Model{}, err
	}
	jobID := in.TrainingJobID
	return s.addModel(ctx, newModel{ProjectID: in.ProjectID, Source: SourceTraining, TrainingJobID: &jobID,
		YOLOVersion: yolo, Description: desc, FileName: name, SrcPath: copied})
}

// newRegistrationDir creates a private folder under registrationsRoot,
// marked as in use until the caller removes it.
func (s *Service) newRegistrationDir() (string, error) {
	if err := os.MkdirAll(s.registrationsRoot(), 0o755); err != nil {
		return "", fmt.Errorf("create model registration folder: %w", err)
	}
	dir := filepath.Join(s.registrationsRoot(), strings.ToLower(rand.Text()))
	s.busy.add(dir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		s.busy.remove(dir)
		return "", fmt.Errorf("create model registration folder: %w", err)
	}
	return dir, nil
}

// copyFile copies src to the new file dst (mode 0644) and syncs it.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open trained weights: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("copy trained weights: %w", err)
	}
	_, err = io.Copy(out, ctxReader{ctx: ctx, r: in})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("copy trained weights: %w", err)
	}
	return nil
}

// ---- reading ----

// ModelQuery selects the models ListModels returns.
type ModelQuery struct {
	ProjectID   int64  // 0: all projects
	YOLOVersion string // "": all versions
	Source      string // "": all sources
	Search      string // in the description, original file name or project name, ignoring case
}

// ListModels returns the models selected by q, newest first.
func (s *Service) ListModels(ctx context.Context, q ModelQuery) ([]Model, error) {
	rows, err := collectModels(s.db.Query(ctx, modelSelect+`
		WHERE ($1::bigint = 0 OR m.project_id = $1::bigint) AND ($2 = '' OR m.yolo_version = $2) AND ($3 = '' OR m.source = $3)
			AND ($4 = '' OR strpos(lower(m.description), lower($4)) > 0 OR strpos(lower(m.file_name), lower($4)) > 0
				OR strpos(lower(p.name), lower($4)) > 0)
		ORDER BY m.created_at DESC, m.id DESC`, q.ProjectID, q.YOLOVersion, q.Source, strings.TrimSpace(q.Search)))
	if err != nil {
		return nil, err
	}
	return s.toModels(rows), nil
}

// ListProjectModels returns the models of a project, the latest version
// first.
func (s *Service) ListProjectModels(ctx context.Context, projectID int64) ([]Model, error) {
	var found bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)", projectID).Scan(&found); err != nil {
		return nil, err
	}
	if !found {
		return nil, errProjectNotFound
	}
	rows, err := collectModels(s.db.Query(ctx, modelSelect+" WHERE m.project_id = $1 ORDER BY m.version DESC", projectID))
	if err != nil {
		return nil, err
	}
	return s.toModels(rows), nil
}

// GetModel returns one model.
func (s *Service) GetModel(ctx context.Context, id int64) (Model, error) {
	r, err := queryModel(ctx, s.db, id)
	if err != nil {
		return Model{}, err
	}
	return s.toModel(r), nil
}

func (s *Service) toModels(rows []modelRow) []Model {
	out := make([]Model, len(rows))
	for i, r := range rows {
		out[i] = s.toModel(r)
	}
	return out
}

// toModel completes a row with what is on disk.
func (s *Service) toModel(r modelRow) Model {
	m := r.Model
	m.FileMissing = !s.isRegularFile(r.dirName, modelFile)
	return m
}

// isRegularFile reports whether the file rel (inside the folder of a
// model) exists as a regular file.
func (s *Service) isRegularFile(dirName, rel string) bool {
	dir, err := s.modelDir(dirName)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}

// UpdateModelInput holds the fields to change; nil leaves a field alone.
type UpdateModelInput struct {
	Description *string
}

// UpdateModel changes the description of a model.
func (s *Service) UpdateModel(ctx context.Context, id int64, in UpdateModelInput) (Model, error) {
	if in.Description != nil {
		desc, err := NormalizeDescription(*in.Description)
		if err != nil {
			return Model{}, err
		}
		tag, err := s.db.Exec(ctx, "UPDATE models SET description = $2, updated_at = now() WHERE id = $1", id, desc)
		if err != nil {
			return Model{}, err
		}
		if tag.RowsAffected() == 0 {
			return Model{}, errModelNotFound
		}
	}
	return s.GetModel(ctx, id)
}

// File is an open model or conversion file to send.
type File struct {
	Content io.ReadSeeker
	Name    string // download file name
	Size    int64
	ModTime time.Time
	file    *os.File
}

// Close closes the file.
func (f *File) Close() error { return f.file.Close() }

// OpenModelFile opens the weights of a model for download.
func (s *Service) OpenModelFile(ctx context.Context, id int64) (*File, error) {
	r, err := queryModel(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	f, err := s.openInModel(r.dirName, modelFile, errFileMissing)
	if err != nil {
		return nil, err
	}
	f.Name = ModelFileName(r.Project.Name, r.Version)
	return f, nil
}

// openInModel opens the regular file rel inside a model folder; missing
// is returned when there is none. It does not follow symbolic links out
// of models/.
func (s *Service) openInModel(dirName, rel string, missing error) (*File, error) {
	root, err := os.OpenRoot(s.modelsDir())
	if err != nil {
		return nil, fmt.Errorf("open models folder: %w", err)
	}
	defer root.Close()
	f, err := root.Open(filepath.Join(filepath.FromSlash(dirName), filepath.FromSlash(rel)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || isEscape(err) {
			return nil, missing
		}
		return nil, fmt.Errorf("open model file: %w", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("stat model file: %w", err)
		}
		return nil, missing
	}
	return &File{Content: f, Size: fi.Size(), ModTime: fi.ModTime(), file: f}, nil
}

// isEscape reports whether an os.Root refused a path that leaves it
// (through a symbolic link).
func isEscape(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes")
}

// ---- deleting ----

// DeleteModel deletes a model with its conversions and its folder (the
// weights, model.json and the cached conversions). Nodes still working on
// one of its conversions get "not found" from then on and stop. The result
// is nil unless the model was deleted but its folder could not be removed
// completely.
func (s *Service) DeleteModel(ctx context.Context, id int64) (*DeleteResult, error) {
	ctx, cancel := s.detach(ctx)
	defer cancel()
	var dirName, trash string
	var exportIDs []int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT dir_name FROM models WHERE id = $1 FOR UPDATE", id).Scan(&dirName)
		if errors.Is(err, pgx.ErrNoRows) {
			return errModelNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "DELETE FROM model_exports WHERE model_id = $1 RETURNING id", id)
		if err != nil {
			return err
		}
		if exportIDs, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM models WHERE id = $1", id); err != nil {
			return err
		}
		// Move the folder out of the way first: this is atomic and quick, so
		// the transaction can still roll back, and the slow removal below
		// cannot leave a half-deleted model folder behind.
		trash, err = s.moveToTrash(dirName, id)
		return err
	})
	if err != nil {
		if trash != "" {
			s.restoreFromTrash(trash, dirName)
		}
		return nil, err
	}
	for _, eid := range exportIDs {
		s.removeExportTemp(eid)
	}
	s.log.Info("model deleted", "model_id", id, "dir", dirName, "exports", len(exportIDs))
	if trash == "" {
		return nil, nil
	}
	defer s.busy.remove(trash)
	if err := s.removeAll(trash); err != nil {
		return s.restoreLeftover(id, dirName, trash, err), nil
	}
	return nil, nil
}

// moveToTrash renames the folder of a model to a hidden name in models/
// and marks it as in use (the caller removes the mark). A missing folder
// is not an error (nothing to delete). Symbolic links are moved, not
// followed.
func (s *Service) moveToTrash(dirName string, modelID int64) (string, error) {
	dir, err := s.modelDir(dirName)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("stat model folder: %w", err)
	}
	trash := filepath.Join(s.modelsDir(), fmt.Sprintf("%s%d", deletingPrefix, modelID))
	s.busy.add(trash)
	if err := os.Rename(dir, trash); err != nil {
		s.busy.remove(trash)
		return "", fmt.Errorf("move model folder aside: %w", err)
	}
	return trash, nil
}

// restoreFromTrash undoes moveToTrash after the delete failed.
func (s *Service) restoreFromTrash(trash, dirName string) {
	defer s.busy.remove(trash)
	dir, err := s.modelDir(dirName)
	if err == nil {
		err = os.Rename(trash, dir)
	}
	if err != nil {
		s.log.Error("restore model folder after failed delete", "path", trash, "err", err)
	}
}

// restoreLeftover handles a model folder that could not be removed
// completely (e.g. files added by another user): what is left gets its
// name back, so it can be cleaned up by hand. Versions are never reused,
// so no new model takes that name.
func (s *Service) restoreLeftover(modelID int64, dirName, trash string, removeErr error) *DeleteResult {
	reason := "部分文件无法删除"
	if errors.Is(removeErr, fs.ErrPermission) {
		reason = "权限不足"
	}
	if dir, err := s.modelDir(dirName); err == nil {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) && os.MkdirAll(filepath.Dir(dir), 0o755) == nil &&
			os.Rename(trash, dir) == nil {
			s.log.Error("model folder only partly deleted; kept the rest under its name",
				"model_id", modelID, "path", dir, "err", removeErr)
			return &DeleteResult{Message: fmt.Sprintf("模型已删除，但文件夹未能完全删除（%s）：%s。请在服务器上手动删除剩余的文件。",
				reason, dir)}
		}
	}
	s.log.Error("model folder only partly deleted", "model_id", modelID, "path", trash, "err", removeErr)
	return &DeleteResult{Message: fmt.Sprintf("模型已删除，但文件夹未能完全删除（%s）：剩余的文件在 %s，请在服务器上手动删除。",
		reason, trash)}
}

// DeleteProjectModels deletes all models of a project (as DeleteModel
// does), before the project itself is deleted. The result is nil unless
// some folders could not be removed completely.
func (s *Service) DeleteProjectModels(ctx context.Context, projectID int64) (*DeleteResult, error) {
	rows, err := s.db.Query(ctx, "SELECT id FROM models WHERE project_id = $1 ORDER BY id", projectID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	var leftovers []string
	for _, id := range ids {
		res, err := s.DeleteModel(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return nil, err
		}
		if res != nil {
			leftovers = append(leftovers, res.Message)
		}
	}
	// The project's folder is empty now unless something was left behind
	// (a crash leftover is moved aside first).
	projectDir := fmt.Sprintf("project-%d", projectID)
	s.recoverPendingIn(ctx, projectDir)
	if dir, err := s.modelDir(projectDir); err == nil {
		_ = os.Remove(dir)
	}
	if len(ids) > 0 {
		s.log.Info("project models deleted", "project_id", projectID, "models", len(ids))
	}
	switch len(leftovers) {
	case 0:
		return nil, nil
	case 1:
		return &DeleteResult{Message: leftovers[0]}, nil
	}
	return &DeleteResult{Message: fmt.Sprintf("%s（另有 %d 个模型的文件夹也未能完全删除）", leftovers[0], len(leftovers)-1)}, nil
}
