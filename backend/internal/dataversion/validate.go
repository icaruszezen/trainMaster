package dataversion

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/augment"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Limits of a create input.
const (
	maxSources          = 1000    // datasets per version
	maxSourceVersions   = 100     // review versions per dataset (like a dataset download)
	maxIgnoreLabels     = 1000    // ignored labels
	maxIgnoreLabelRunes = 100     // characters of an ignored label
	maxSeed             = 1<<31 - 1
	maxAugmented        = 100_000 // augmented copies per version
)

// createRequest is a validated CreateInput.
type createRequest struct {
	projectID    int64
	note         string // Create only
	classVersion string // Create only
	sources      []SourceInput
	config       Config
	methods      []augment.Method // the normalized augmentation methods (nil: none)
}

// normalizeInput checks the shape of a create (forCreate) or preview input
// and normalizes it: version ids de-duplicated, ignored labels trimmed,
// de-duplicated and sorted, augmentation methods in display order. It does
// not read the database.
func normalizeInput(in CreateInput, forCreate bool) (createRequest, error) {
	req := createRequest{projectID: in.ProjectID, classVersion: in.ClassVersion}
	if in.ProjectID <= 0 {
		return req, errProjectNotFound
	}
	if forCreate {
		note, err := dataset.NormalizeVersionNote(in.Note)
		if err != nil {
			return req, invalidFrom(err)
		}
		req.note = note
	}
	var err error
	if req.sources, err = normalizeSources(in.Sources); err != nil {
		return req, err
	}
	if err := checkSplit(in.Split); err != nil {
		return req, err
	}
	req.config = Config{Split: in.Split, IncludeEmpty: in.IncludeEmpty}
	if req.config.IgnoreLabels, err = normalizeIgnoreLabels(in.IgnoreLabels); err != nil {
		return req, err
	}
	if in.Augmentation != nil {
		methods, err := augment.NormalizeMethods(in.Augmentation.Methods)
		if err != nil {
			return req, invalidFrom(err)
		}
		limit := min(augment.CopiesLimit, augment.MaxCopies(methods))
		switch {
		case in.Augmentation.Copies < 1:
			return req, &Error{Kind: ErrInvalid, Message: "每张图片至少生成 1 份增强副本"}
		case in.Augmentation.Copies > limit:
			return req, newError(ErrInvalid, "所选增强方式最多能生成 %d 份不同的副本", limit)
		}
		names := make([]string, len(methods))
		for i, m := range methods {
			names[i] = string(m)
		}
		req.methods = methods
		req.config.Augmentation = &Augmentation{Methods: names, Copies: in.Augmentation.Copies,
			OnlyWithObjects: in.Augmentation.OnlyWithObjects}
	}
	return req, nil
}

// invalidFrom turns the user-facing error of another package (a note or
// augmentation methods) into an ErrInvalid with the same message.
func invalidFrom(err error) error {
	var de *dataset.Error
	if errors.As(err, &de) {
		return &Error{Kind: ErrInvalid, Message: de.Message}
	}
	if errors.Is(err, augment.ErrInvalidMethods) {
		return &Error{Kind: ErrInvalid, Message: err.Error()}
	}
	return err
}

func normalizeSources(raw []SourceInput) ([]SourceInput, error) {
	switch {
	case len(raw) == 0:
		return nil, &Error{Kind: ErrInvalid, Message: "请至少选择一个数据集"}
	case len(raw) > maxSources:
		return nil, newError(ErrInvalid, "一个数据版本最多包含 %d 个数据集", maxSources)
	}
	out := make([]SourceInput, 0, len(raw))
	for _, src := range raw {
		switch {
		case src.DatasetID <= 0:
			return nil, newError(ErrInvalid, "数据集 ID 无效：%d", src.DatasetID)
		case slices.ContainsFunc(out, func(o SourceInput) bool { return o.DatasetID == src.DatasetID }):
			return nil, newError(ErrInvalid, "同一个数据集只能选择一次（数据集 ID %d）", src.DatasetID)
		case len(src.VersionIDs) > maxSourceVersions:
			return nil, newError(ErrInvalid, "每个数据集最多选择 %d 个审图版本", maxSourceVersions)
		}
		ids := []int64{}
		for _, id := range src.VersionIDs {
			if id <= 0 {
				return nil, newError(ErrInvalid, "审图版本 ID 无效：%d", id)
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if !src.IncludeBase && len(ids) == 0 {
			return nil, &Error{Kind: ErrInvalid, Message: "请为每个数据集至少选择一个标注来源（基础标注或审图版本）"}
		}
		out = append(out, SourceInput{DatasetID: src.DatasetID, IncludeBase: src.IncludeBase, VersionIDs: ids})
	}
	return out, nil
}

func checkSplit(c SplitConfig) error {
	switch {
	case c.Train < 1:
		return &Error{Kind: ErrInvalid, Message: "训练集比例至少为 1%"}
	case c.Val < 1:
		return &Error{Kind: ErrInvalid, Message: "验证集比例至少为 1%"}
	case c.Test < 0:
		return &Error{Kind: ErrInvalid, Message: "测试集比例不能小于 0%"}
	case c.Train+c.Val+c.Test != 100:
		return newError(ErrInvalid, "训练、验证、测试的比例之和必须是 100%%（现在是 %d%%）", c.Train+c.Val+c.Test)
	case c.Seed < 0 || c.Seed > maxSeed:
		return newError(ErrInvalid, "随机种子必须在 0 到 %d 之间", maxSeed)
	case c.GroupBy != GroupByStem && c.GroupBy != GroupByFolder:
		return &Error{Kind: ErrInvalid, Message: "分组方式必须是 stem（按文件名）或 folder（按文件夹）"}
	}
	return nil
}

func normalizeIgnoreLabels(raw []string) ([]string, error) {
	if len(raw) > maxIgnoreLabels {
		return nil, newError(ErrInvalid, "最多忽略 %d 个标签", maxIgnoreLabels)
	}
	out := []string{}
	for _, l := range raw {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
			return nil, &Error{Kind: ErrInvalid, Message: "忽略的标签不能为空"}
		case !utf8.ValidString(l):
			return nil, &Error{Kind: ErrInvalid, Message: "忽略的标签包含无效字符"}
		case utf8.RuneCountInString(l) > maxIgnoreLabelRunes:
			return nil, newError(ErrInvalid, "忽略的标签“%s”超过 %d 个字符", l, maxIgnoreLabelRunes)
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out, nil
}

// checkIgnoredNotClasses refuses ignored labels that are in the class
// list.
func checkIgnoredNotClasses(ignore []string, classes []Class) error {
	for _, l := range ignore {
		if slices.ContainsFunc(classes, func(c Class) bool { return c.Name == l }) {
			return newError(ErrInvalid, "忽略的标签“%s”在类别列表中", l)
		}
	}
	return nil
}

// ---- sources ----

// reviewVersionRow is a review version as the source checks read it.
type reviewVersionRow struct {
	ID        int64
	DatasetID int64
	Major     *int
	Minor     *int
	Status    string
	Trashed   bool
}

func (v reviewVersionRow) name() string {
	if v.Major == nil || v.Minor == nil {
		return ""
	}
	return dataset.VersionName(*v.Major, *v.Minor)
}

func queryReviewVersions(ctx context.Context, q querier, ids []int64) (map[int64]reviewVersionRow, error) {
	out := map[int64]reviewVersionRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, dataset_id, major, minor, status, deleted_at IS NOT NULL
		FROM dataset_versions WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reviewVersionRow])
	if err != nil {
		return nil, err
	}
	for _, v := range list {
		out[v.ID] = v
	}
	return out, nil
}

// validateNewSources checks the sources of a create or preview body: every
// dataset exists, is not in the recycle bin and belongs to the project;
// every review version exists, is of its dataset, is ready and not in the
// bin. It returns the sources as a version stores them: in the project's
// dataset order (the order they were added), the review versions newest
// first, with their names now.
func validateNewSources(ctx context.Context, q querier, projectID int64, inputs []SourceInput) ([]Source, error) {
	ids := make([]int64, len(inputs))
	var versionIDs []int64
	for i, in := range inputs {
		ids[i] = in.DatasetID
		versionIDs = append(versionIDs, in.VersionIDs...)
	}
	type datasetRow struct {
		ID      int64
		Name    string
		DirName string
		AddedAt *time.Time
	}
	rows, err := q.Query(ctx, `SELECT d.id, d.name, d.dir_name, pd.created_at
		FROM datasets d LEFT JOIN project_datasets pd ON pd.dataset_id = d.id AND pd.project_id = $2
		WHERE d.id = ANY($1) AND d.deleted_at IS NULL`, ids, projectID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[datasetRow])
	if err != nil {
		return nil, err
	}
	byID := map[int64]datasetRow{}
	for _, d := range list {
		byID[d.ID] = d
	}
	versions, err := queryReviewVersions(ctx, q, versionIDs)
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(inputs))
	for _, in := range inputs {
		d, ok := byID[in.DatasetID]
		switch {
		case !ok:
			return nil, errDatasetGone
		case d.AddedAt == nil:
			return nil, newError(ErrInvalid, "数据集“%s”不在项目中", d.Name)
		}
		var picked []reviewVersionRow
		for _, id := range in.VersionIDs {
			v, ok := versions[id]
			switch {
			case !ok || v.DatasetID != d.ID || v.Trashed:
				return nil, errReviewVersionGone
			case v.Status != dataset.VersionReady && v.name() == "":
				return nil, &Error{Kind: ErrConflict, Message: "所选审图版本还没有生成完成"}
			case v.Status != dataset.VersionReady:
				return nil, newError(ErrConflict, "审图版本“%s”还没有生成完成", v.name())
			}
			picked = append(picked, v)
		}
		slices.SortFunc(picked, func(a, b reviewVersionRow) int {
			return cmp.Or(cmp.Compare(*b.Major, *a.Major), cmp.Compare(*b.Minor, *a.Minor), cmp.Compare(b.ID, a.ID))
		})
		src := Source{DatasetID: d.ID, DatasetName: d.Name, DirName: d.DirName, IncludeBase: in.IncludeBase,
			Versions: []SourceVersion{}}
		for _, v := range picked {
			src.Versions = append(src.Versions, SourceVersion{ID: v.ID, Name: v.name()})
		}
		sources = append(sources, src)
	}
	slices.SortStableFunc(sources, func(a, b Source) int {
		da, db := byID[a.DatasetID], byID[b.DatasetID]
		return cmp.Or(da.AddedAt.Compare(*db.AddedAt), cmp.Compare(a.DatasetID, b.DatasetID))
	})
	return sources, nil
}

// sourceCheck is who checks the frozen sources of a version.
type sourceCheck int

const (
	checkForRetry sourceCheck = iota // Retry: a review version in the recycle bin is refused
	checkForBuild                    // the build: it is accepted (its rows exist until purged)
)

// validateFrozenSources checks the sources of a version before it is
// retried or built: each dataset still exists, is not in the recycle bin
// and is still in the project; each review version still exists (and,
// for a retry, is not in the bin). Errors are ErrConflict with the
// message a retry answers and a build fails with.
func validateFrozenSources(ctx context.Context, q querier, projectID int64, sources []Source, check sourceCheck) error {
	ids := make([]int64, len(sources))
	var versionIDs []int64
	for i, src := range sources {
		ids[i] = src.DatasetID
		for _, v := range src.Versions {
			versionIDs = append(versionIDs, v.ID)
		}
	}
	type datasetRow struct {
		ID      int64
		Trashed bool
		Linked  bool
	}
	rows, err := q.Query(ctx, `SELECT d.id, d.deleted_at IS NOT NULL,
			EXISTS (SELECT 1 FROM project_datasets pd WHERE pd.dataset_id = d.id AND pd.project_id = $2)
		FROM datasets d WHERE d.id = ANY($1)`, ids, projectID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[datasetRow])
	if err != nil {
		return err
	}
	byID := map[int64]datasetRow{}
	for _, d := range list {
		byID[d.ID] = d
	}
	versions, err := queryReviewVersions(ctx, q, versionIDs)
	if err != nil {
		return err
	}
	for _, src := range sources {
		d, ok := byID[src.DatasetID]
		switch {
		case !ok:
			return newError(ErrConflict, "数据集“%s”已被永久删除，无法重新生成，请新建数据版本", src.DatasetName)
		case d.Trashed:
			return newError(ErrConflict, "数据集“%s”已被删除（在回收站中），恢复后可以重新生成", src.DatasetName)
		case !d.Linked:
			return newError(ErrConflict, "数据集“%s”已经不在项目中，无法重新生成，请新建数据版本", src.DatasetName)
		}
		for _, sv := range src.Versions {
			v, ok := versions[sv.ID]
			switch {
			case !ok || v.DatasetID != src.DatasetID || v.Status != dataset.VersionReady:
				return newError(ErrConflict, "所选审图版本“%s”已被永久删除，无法重新生成，请新建数据版本", sv.Name)
			case v.Trashed && check == checkForRetry:
				return newError(ErrConflict, "审图版本“%s”已被删除（在回收站中），恢复后可以重新生成", sv.Name)
			}
		}
	}
	return nil
}

// selection is the dataset.VersionSelection of a source.
func (src Source) selection() dataset.VersionSelection {
	ids := make([]int64, len(src.Versions))
	for i, v := range src.Versions {
		ids[i] = v.ID
	}
	return dataset.VersionSelection{IncludeBase: src.IncludeBase, VersionIDs: ids}
}
