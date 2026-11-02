package dataset

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
)

// The class list of a dataset (datasets.classes): the labels the annotation
// workspace offers, in order, with optional colors. It is metadata only;
// annotation files are never rewritten when it changes.
//
// Several people may edit the list of one dataset. Every read returns a
// version of the list; a replacement can name the version it started from
// and then fails instead of undoing someone else's change, and adding one
// class (AddClass) never replaces the rest of the list.

var classColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// NormalizeClassList validates a class list: at most maxClasses entries
// whose names follow the rules of NormalizeClasses (an empty list is
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
	names, err := NormalizeClasses(names)
	if err != nil {
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

// ClassList is the class list of a dataset with its version.
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

// queryClasses reads the class list of a dataset, locking the dataset row
// against other changes of the list when forUpdate is set.
func queryClasses(ctx context.Context, q querier, datasetID int64, forUpdate bool) ([]Class, error) {
	sql := "SELECT classes FROM datasets WHERE id = $1"
	if forUpdate {
		// Not FOR UPDATE: images can still be added to the dataset meanwhile.
		sql += " FOR NO KEY UPDATE"
	}
	var classes []Class
	err := q.QueryRow(ctx, sql, datasetID).Scan(&classes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errDatasetNotFound
	}
	return classes, err
}

// ListClasses returns the class list of a dataset.
func (s *Service) ListClasses(ctx context.Context, datasetID int64) (ClassList, error) {
	classes, err := queryClasses(ctx, s.db, datasetID, false)
	if err != nil {
		return ClassList{}, err
	}
	return newClassList(classes), nil
}

// SetClasses replaces the class list of a dataset and returns the stored
// list. When baseVersion is not nil, the list must still be at that
// version, otherwise nothing changes and the error is an ErrConflict.
func (s *Service) SetClasses(ctx context.Context, datasetID int64, classes []Class, baseVersion *string) (ClassList, error) {
	classes, err := NormalizeClassList(classes)
	if err != nil {
		return ClassList{}, err
	}
	return s.changeClasses(ctx, datasetID, func(current ClassList) ([]Class, error) {
		if baseVersion != nil && *baseVersion != current.Version {
			return nil, errClassesChanged
		}
		return classes, nil
	})
}

// AddClass appends a class to the class list of a dataset, unless the list
// has a class of that name already (then the list stays as it is), and
// returns the stored list.
func (s *Service) AddClass(ctx context.Context, datasetID int64, class Class) (ClassList, error) {
	class.Name = strings.TrimSpace(class.Name)
	if problem := classNameProblem(class.Name); problem != "" {
		return ClassList{}, newError(ErrInvalid, "类别%s", problem)
	}
	added, err := NormalizeClassList([]Class{class})
	if err != nil {
		return ClassList{}, err
	}
	return s.changeClasses(ctx, datasetID, func(current ClassList) ([]Class, error) {
		if slices.ContainsFunc(current.Classes, func(c Class) bool { return c.Name == added[0].Name }) {
			return nil, nil
		}
		return NormalizeClassList(append(slices.Clip(current.Classes), added[0]))
	})
}

// changeClasses replaces the class list of a dataset with the one change
// returns for the current list (nil keeps the list), atomically: the
// dataset row stays locked from the read to the write.
func (s *Service) changeClasses(ctx context.Context, datasetID int64, change func(current ClassList) ([]Class, error)) (ClassList, error) {
	var out ClassList
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		classes, err := queryClasses(ctx, tx, datasetID, true)
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
		_, err = tx.Exec(ctx, "UPDATE datasets SET classes = $2, updated_at = now() WHERE id = $1", datasetID, out.Classes)
		return err
	})
	if err != nil {
		return ClassList{}, err
	}
	return out, nil
}
