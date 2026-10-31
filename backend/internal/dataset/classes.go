package dataset

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The class list of a dataset (datasets.classes): the labels the annotation
// workspace offers, in order, with optional colors. It is metadata only;
// annotation files are never rewritten when it changes.

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

// ListClasses returns the class list of a dataset.
func (s *Service) ListClasses(ctx context.Context, datasetID int64) ([]Class, error) {
	var classes []Class
	err := s.db.QueryRow(ctx, "SELECT classes FROM datasets WHERE id = $1", datasetID).Scan(&classes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errDatasetNotFound
	}
	if classes == nil {
		classes = []Class{}
	}
	return classes, err
}

// SetClasses replaces the class list of a dataset and returns the stored
// list.
func (s *Service) SetClasses(ctx context.Context, datasetID int64, classes []Class) ([]Class, error) {
	classes, err := NormalizeClassList(classes)
	if err != nil {
		return nil, err
	}
	tag, err := s.db.Exec(ctx, "UPDATE datasets SET classes = $2, updated_at = now() WHERE id = $1", datasetID, classes)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errDatasetNotFound
	}
	return classes, nil
}
