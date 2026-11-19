package training

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maxTemplateNameRunes = 120
	maxTemplateDescRunes = 2000
	pgUniqueViolation    = "23505"
)

func normalizeTemplateName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", newError(ErrInvalid, "请输入模板名称")
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return "", newError(ErrInvalid, "模板名称不能包含换行或控制字符")
		}
	}
	if utf8.RuneCountInString(s) > maxTemplateNameRunes {
		return "", newError(ErrInvalid, "模板名称不能超过 %d 个字符", maxTemplateNameRunes)
	}
	return s, nil
}

func normalizeTemplateDescription(raw string) (string, error) {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxTemplateDescRunes {
		return "", newError(ErrInvalid, "模板备注不能超过 %d 个字符", maxTemplateDescRunes)
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return "", newError(ErrInvalid, "模板备注不能包含控制字符")
		}
	}
	return s, nil
}

func isTemplateUniqueNameError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgUniqueViolation {
		return false
	}
	return pgErr.ConstraintName == "training_param_templates_name_key" || strings.Contains(pgErr.Message, "training_param_templates_name_key")
}

const templateSelectCols = `id, name, description, params, is_default, is_builtin, COALESCE(builtin_key, ''), created_at, updated_at`

func scanTemplateRow(row pgx.Row) (ParamTemplate, error) {
	var (
		tpl       ParamTemplate
		paramsRaw []byte
	)
	if err := row.Scan(
		&tpl.ID, &tpl.Name, &tpl.Description, &paramsRaw,
		&tpl.IsDefault, &tpl.IsBuiltin, &tpl.BuiltinKey, &tpl.CreatedAt, &tpl.UpdatedAt,
	); err != nil {
		return ParamTemplate{}, err
	}
	tpl.Params = DefaultParams()
	if len(paramsRaw) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(paramsRaw)))
		dec.UseNumber()
		var rawMap map[string]any
		if dec.Decode(&rawMap) == nil {
			if norm, err := NormalizeTemplateParams(rawMap); err == nil {
				tpl.Params = norm
			} else {
				tpl.Params = rawMap
			}
		}
	}
	tpl.CreatedAt = tpl.CreatedAt.UTC()
	tpl.UpdatedAt = tpl.UpdatedAt.UTC()
	return tpl, nil
}

// ListTemplates returns all global training parameter templates: default first,
// then custom templates (most recently updated), then system built-in presets
// in their curated workflow order. search optionally matches name or description.
func (s *Service) ListTemplates(ctx context.Context, search string) ([]ParamTemplate, error) {
	q := strings.TrimSpace(search)
	rows, err := s.db.Query(ctx, `SELECT `+templateSelectCols+`
		FROM training_param_templates
		WHERE ($1 = ''
			OR strpos(lower(name), lower($1)) > 0
			OR strpos(lower(description), lower($1)) > 0)
		ORDER BY is_default DESC, is_builtin ASC, builtin_order ASC, updated_at DESC, id DESC`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ParamTemplate{}
	for rows.Next() {
		tpl, err := scanTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tpl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetTemplate returns a single training parameter template by ID.
func (s *Service) GetTemplate(ctx context.Context, id int64) (ParamTemplate, error) {
	tpl, err := scanTemplateRow(s.db.QueryRow(ctx,
		`SELECT `+templateSelectCols+` FROM training_param_templates WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ParamTemplate{}, errTemplateNotFound
	}
	if err != nil {
		return ParamTemplate{}, err
	}
	return tpl, nil
}

// CreateTemplate validates and saves a new reusable training parameter template.
func (s *Service) CreateTemplate(ctx context.Context, in CreateTemplateInput) (ParamTemplate, error) {
	name, err := normalizeTemplateName(in.Name)
	if err != nil {
		return ParamTemplate{}, err
	}
	desc, err := normalizeTemplateDescription(in.Description)
	if err != nil {
		return ParamTemplate{}, err
	}
	params, err := NormalizeTemplateParams(in.Params)
	if err != nil {
		return ParamTemplate{}, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return ParamTemplate{}, fmt.Errorf("encode template params: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ParamTemplate{}, err
	}
	defer tx.Rollback(ctx)

	if in.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE training_param_templates SET is_default = false WHERE is_default`); err != nil {
			return ParamTemplate{}, fmt.Errorf("clear default template: %w", err)
		}
	}

	tpl, err := scanTemplateRow(tx.QueryRow(ctx, `INSERT INTO training_param_templates (
			name, description, params, is_default
		) VALUES ($1, $2, $3, $4)
		RETURNING `+templateSelectCols, name, desc, paramsJSON, in.IsDefault))
	if err != nil {
		if isTemplateUniqueNameError(err) {
			return ParamTemplate{}, errTemplateNameExists
		}
		return ParamTemplate{}, fmt.Errorf("insert parameter template: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		if isTemplateUniqueNameError(err) {
			return ParamTemplate{}, errTemplateNameExists
		}
		return ParamTemplate{}, err
	}
	s.log.Info("training parameter template created", "template_id", tpl.ID, "name", tpl.Name, "is_default", tpl.IsDefault)
	return tpl, nil
}

// UpdateTemplate applies partial updates to an existing training parameter template.
// System built-in templates only permit toggling is_default; attempts to modify
// their name, description, or params are rejected with ErrConflict.
func (s *Service) UpdateTemplate(ctx context.Context, id int64, in UpdateTemplateInput) (ParamTemplate, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ParamTemplate{}, err
	}
	defer tx.Rollback(ctx)

	cur, err := scanTemplateRow(tx.QueryRow(ctx,
		`SELECT `+templateSelectCols+` FROM training_param_templates WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ParamTemplate{}, errTemplateNotFound
	}
	if err != nil {
		return ParamTemplate{}, err
	}

	if cur.IsBuiltin && (in.Name != nil || in.Description != nil || in.Params != nil) {
		return ParamTemplate{}, errTemplateBuiltinReadOnly
	}

	name := cur.Name
	desc := cur.Description
	params := cur.Params
	isDefault := cur.IsDefault
	changed := false

	if in.Name != nil {
		n, err := normalizeTemplateName(*in.Name)
		if err != nil {
			return ParamTemplate{}, err
		}
		if n != name {
			name = n
			changed = true
		}
	}
	if in.Description != nil {
		d, err := normalizeTemplateDescription(*in.Description)
		if err != nil {
			return ParamTemplate{}, err
		}
		if d != desc {
			desc = d
			changed = true
		}
	}
	if in.Params != nil {
		norm, err := NormalizeTemplateParams(*in.Params)
		if err != nil {
			return ParamTemplate{}, err
		}
		params = norm
		changed = true
	}
	if in.IsDefault != nil && *in.IsDefault != isDefault {
		isDefault = *in.IsDefault
		changed = true
	}

	if !changed {
		return cur, nil
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return ParamTemplate{}, fmt.Errorf("encode template params: %w", err)
	}

	if isDefault && !cur.IsDefault {
		if _, err := tx.Exec(ctx, `UPDATE training_param_templates SET is_default = false WHERE is_default AND id <> $1`, id); err != nil {
			return ParamTemplate{}, fmt.Errorf("clear default template: %w", err)
		}
	}

	updated, err := scanTemplateRow(tx.QueryRow(ctx, `UPDATE training_param_templates
		SET name = $1, description = $2, params = $3, is_default = $4, updated_at = now()
		WHERE id = $5
		RETURNING `+templateSelectCols, name, desc, paramsJSON, isDefault, id))
	if err != nil {
		if isTemplateUniqueNameError(err) {
			return ParamTemplate{}, errTemplateNameExists
		}
		return ParamTemplate{}, fmt.Errorf("update parameter template: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		if isTemplateUniqueNameError(err) {
			return ParamTemplate{}, errTemplateNameExists
		}
		return ParamTemplate{}, err
	}
	s.log.Info("training parameter template updated", "template_id", updated.ID, "name", updated.Name, "is_default", updated.IsDefault)
	return updated, nil
}

// DeleteTemplate permanently deletes a custom training parameter template.
// System built-in templates cannot be deleted. Any training jobs referencing a
// deleted custom template keep their template_name snapshot while template_id
// is set to NULL by the foreign key constraint.
func (s *Service) DeleteTemplate(ctx context.Context, id int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var isBuiltin bool
	if err := tx.QueryRow(ctx, `SELECT is_builtin FROM training_param_templates WHERE id = $1 FOR UPDATE`, id).Scan(&isBuiltin); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errTemplateNotFound
		}
		return fmt.Errorf("lookup parameter template: %w", err)
	}
	if isBuiltin {
		return errTemplateBuiltinDelete
	}

	if _, err := tx.Exec(ctx, `DELETE FROM training_param_templates WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete parameter template: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.log.Info("training parameter template deleted", "template_id", id)
	return nil
}
