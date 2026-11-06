package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// The class list of a project (projects.classes): the classes it trains,
// in order, with optional colors. The order is the YOLO class id when the
// project's datasets are exported for training (the first class is 0).
// When annotating a dataset, the workspace offers the classes of the
// dataset's projects first (DatasetClasses), then the labels already in the
// dataset. The list is metadata only; annotation files are never rewritten
// when it changes.
//
// Several people may edit the list of one project. Every read returns a
// version of the list; a replacement can name the version it started from
// and then fails instead of undoing someone else's change, and adding one
// class (AddClass) never replaces the rest of the list.

var classColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// NormalizeClassList validates a class list: at most 1000 entries whose
// names follow the rules of dataset.NormalizeClasses (an empty list is
// fine), and colors that are "" or "#rrggbb" (stored in lower case).
func NormalizeClassList(raw []Class) ([]Class, error) {
	out := make([]Class, len(raw))
	if len(raw) == 0 {
		return out, nil
	}
	names := make([]string, len(raw))
	for i, c := range raw {
		names[i] = c.Name
	}
	names, err := dataset.NormalizeClasses(names)
	if err != nil {
		var de *dataset.Error
		if errors.As(err, &de) {
			return nil, &Error{Kind: ErrInvalid, Message: de.Message}
		}
		return nil, err
	}
	for i, c := range raw {
		color := strings.TrimSpace(c.Color)
		if color != "" && !classColorPattern.MatchString(color) {
			return nil, newError(ErrInvalid, "类别“%s”的颜色必须是 #rrggbb 格式", names[i])
		}
		out[i] = Class{Name: names[i], Color: strings.ToLower(color)}
	}
	return out, nil
}

// ClassList is the class list of a project with its version.
type ClassList struct {
	Classes []Class // never nil
	// Version identifies the content of the list (an empty list has one
	// too). It is opaque to clients.
	Version string
}

// newClassList returns classes with their version: a hash of their
// canonical JSON encoding, which is the same for the list as stored and as
// read back.
func newClassList(classes []Class) ClassList {
	if classes == nil {
		classes = []Class{}
	}
	data, _ := json.Marshal(classes) // cannot fail for a []Class
	sum := sha256.Sum256(data)
	return ClassList{Classes: classes, Version: hex.EncodeToString(sum[:16])}
}

// errClassesChanged is returned when the class list changed since the
// version a replacement started from.
var errClassesChanged = &Error{Kind: ErrConflict, Message: "类别列表已被其他人修改，请载入最新的列表后再保存"}

// queryClasses reads the class list of a project, locking the project row
// against other changes of the list when forUpdate is set.
func queryClasses(ctx context.Context, q querier, projectID int64, forUpdate bool) ([]Class, error) {
	sql := "SELECT classes FROM projects WHERE id = $1 AND deleted_at IS NULL"
	if forUpdate {
		// FOR NO KEY UPDATE, like the membership changes: the row stays
		// referenceable, so datasets can still be added meanwhile.
		sql += " FOR NO KEY UPDATE"
	}
	var classes []Class
	err := q.QueryRow(ctx, sql, projectID).Scan(&classes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errProjectNotFound
	}
	return classes, err
}

// ListClasses returns the class list of a project.
func (s *Service) ListClasses(ctx context.Context, projectID int64) (ClassList, error) {
	classes, err := queryClasses(ctx, s.db, projectID, false)
	if err != nil {
		return ClassList{}, err
	}
	return newClassList(classes), nil
}

// SetClasses replaces the class list of a project and returns the stored
// list. When baseVersion is not nil, the list must still be at that
// version, otherwise nothing changes and the error is an ErrConflict. The
// same list again changes nothing, not even the project's updated_at.
func (s *Service) SetClasses(ctx context.Context, projectID int64, classes []Class, baseVersion *string) (ClassList, error) {
	classes, err := NormalizeClassList(classes)
	if err != nil {
		return ClassList{}, err
	}
	return s.changeClasses(ctx, projectID, func(current ClassList) ([]Class, error) {
		if baseVersion != nil && *baseVersion != current.Version {
			return nil, errClassesChanged
		}
		if slices.Equal(classes, current.Classes) {
			return nil, nil // unchanged: keep updated_at
		}
		return classes, nil
	})
}

// AddClass appends a class to the class list of a project, unless the list
// has a class of that name already (then the list stays as it is), and
// returns the stored list.
func (s *Service) AddClass(ctx context.Context, projectID int64, class Class) (ClassList, error) {
	class.Name = strings.TrimSpace(class.Name)
	if problem := dataset.ClassNameProblem(class.Name); problem != "" {
		return ClassList{}, newError(ErrInvalid, "类别%s", problem)
	}
	added, err := NormalizeClassList([]Class{class})
	if err != nil {
		return ClassList{}, err
	}
	return s.changeClasses(ctx, projectID, func(current ClassList) ([]Class, error) {
		if slices.ContainsFunc(current.Classes, func(c Class) bool { return c.Name == added[0].Name }) {
			return nil, nil
		}
		return NormalizeClassList(append(slices.Clip(current.Classes), added[0]))
	})
}

// changeClasses replaces the class list of a project with the one change
// returns for the current list (nil keeps the list), atomically: the
// project row stays locked from the read to the write. A change marks the
// project as updated. The project row is the only lock it takes, so it
// cannot deadlock with the membership changes (see the lock order in
// store.go).
func (s *Service) changeClasses(ctx context.Context, projectID int64, change func(current ClassList) ([]Class, error)) (ClassList, error) {
	var out ClassList
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		classes, err := queryClasses(ctx, tx, projectID, true)
		if err != nil {
			return err
		}
		current := newClassList(classes)
		next, err := change(current)
		if err != nil {
			return err
		}
		if next == nil {
			out = current
			return nil
		}
		out = newClassList(next)
		_, err = tx.Exec(ctx, "UPDATE projects SET classes = $2, updated_at = now() WHERE id = $1", projectID, out.Classes)
		return err
	})
	if err != nil {
		return ClassList{}, err
	}
	return out, nil
}

// DatasetClasses returns the classes of the projects that include a
// dataset, which the annotation workspace offers first: the class list of
// each project in the order of the dataset's projects (by name, as
// dataset.Dataset.Projects lists them), each name once, with the color the
// first project gives it.
func (s *Service) DatasetClasses(ctx context.Context, datasetID int64) ([]Class, error) {
	if err := s.datasetExists(ctx, datasetID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT p.classes FROM project_datasets pd
		JOIN projects p ON p.id = pd.project_id AND p.deleted_at IS NULL
		WHERE pd.dataset_id = $1 ORDER BY lower(p.name), p.id`, datasetID)
	if err != nil {
		return nil, err
	}
	lists, err := pgx.CollectRows(rows, pgx.RowTo[[]Class])
	if err != nil {
		return nil, err
	}
	return mergeClassLists(lists), nil
}

// datasetExists fails with a not-found error unless the dataset exists.
func (s *Service) datasetExists(ctx context.Context, datasetID int64) error {
	var exists bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1 AND deleted_at IS NULL)",
		datasetID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errDatasetNotFound
	}
	return nil
}

// mergeClassLists concatenates class lists, keeping the first class of
// each name. The result is never nil.
func mergeClassLists(lists [][]Class) []Class {
	out := []Class{}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, c := range list {
			if !seen[c.Name] {
				seen[c.Name] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// ExportClasses returns the class names of a project in order, for
// exporting one of its datasets as YOLO: the dataset must exist, the
// project must include it and have at least one class.
func (s *Service) ExportClasses(ctx context.Context, projectID, datasetID int64) ([]string, error) {
	if err := s.datasetExists(ctx, datasetID); err != nil {
		return nil, err
	}
	var name string
	var classes []Class
	var member bool
	err := s.db.QueryRow(ctx, `SELECT p.name, p.classes,
			EXISTS (SELECT 1 FROM project_datasets pd WHERE pd.project_id = p.id AND pd.dataset_id = $2)
		FROM projects p WHERE p.id = $1 AND `+projectLive, projectID, datasetID).Scan(&name, &classes, &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &Error{Kind: ErrInvalid, Message: "所选项目不存在或已被删除，请刷新后重试"}
	}
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, newError(ErrInvalid, "数据集不在项目“%s”中，请选择数据集所属的项目", name)
	}
	if len(classes) == 0 {
		return nil, newError(ErrConflict, "项目“%s”还没有类别列表，请先在项目页面的“类别”中添加类别", name)
	}
	names := make([]string, len(classes))
	for i, c := range classes {
		names[i] = c.Name
	}
	return names, nil
}
