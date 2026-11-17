package storagenode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const migrationFreeHeadroom = 64 << 20 // 64 MiB safety margin when target reports disk usage

// IsMigrating reports whether target currently has a queued or running migration.
func (s *Service) IsMigrating(targetType string, targetID int64) bool {
	if s == nil {
		return false
	}
	s.migMu.Lock()
	_, ok := s.activeMig[MigrationTarget{TargetType: targetType, TargetID: targetID}]
	s.migMu.Unlock()
	return ok
}

// CheckNotMigrating returns a 409 ErrConflict error if target is currently being migrated.
func (s *Service) CheckNotMigrating(targetType string, targetID int64) error {
	if s.IsMigrating(targetType, targetID) {
		return newError(ErrConflict, "该数据正在迁移至新的存储节点，请等待迁移完成后再操作")
	}
	return nil
}

func (s *Service) notifyMigrator() {
	select {
	case s.wakeMig <- struct{}{}:
	default:
	}
}

func (s *Service) recoverMigrations(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `UPDATE storage_migrations sm
		SET status = CASE
				WHEN (sm.target_type = 'dataset' AND EXISTS (
					SELECT 1 FROM datasets d WHERE d.id = sm.target_id AND d.storage_node_id = sm.to_node_id
				)) OR (sm.target_type = 'data_version' AND EXISTS (
					SELECT 1 FROM data_versions v WHERE v.id = sm.target_id AND v.storage_node_id = sm.to_node_id
				)) THEN 'succeeded'
				ELSE 'failed'
			END,
			error = CASE
				WHEN (sm.target_type = 'dataset' AND EXISTS (
					SELECT 1 FROM datasets d WHERE d.id = sm.target_id AND d.storage_node_id = sm.to_node_id
				)) OR (sm.target_type = 'data_version' AND EXISTS (
					SELECT 1 FROM data_versions v WHERE v.id = sm.target_id AND v.storage_node_id = sm.to_node_id
				)) THEN ''
				ELSE '服务重启导致迁移中断，原节点文件完好，可重新发起迁移'
			END,
			finished_at = now(),
			updated_at = now()
		WHERE sm.status = 'running'`); err != nil {
		return fmt.Errorf("recover running storage migrations: %w", err)
	}

	rows, err := s.db.Query(ctx,
		"SELECT id, target_type, target_id FROM storage_migrations WHERE status = 'queued'")
	if err != nil {
		return fmt.Errorf("load queued storage migrations: %w", err)
	}
	defer rows.Close()

	s.migMu.Lock()
	clear(s.activeMig)
	count := 0
	for rows.Next() {
		var id, targetID int64
		var targetType string
		if err := rows.Scan(&id, &targetType, &targetID); err != nil {
			s.migMu.Unlock()
			return err
		}
		s.activeMig[MigrationTarget{TargetType: targetType, TargetID: targetID}] = id
		count++
	}
	s.migMu.Unlock()
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 0 {
		s.notifyMigrator()
	}
	return nil
}

// ListMigratableItems returns live datasets and ready data versions that reside
// on a storage node other than the currently active node and are not already migrating.
func (s *Service) ListMigratableItems(ctx context.Context) ([]MigratableItem, error) {
	if s.db == nil {
		return []MigratableItem{}, nil
	}
	activeRef, _, err := s.ActiveNode(ctx)
	if err != nil {
		return nil, err
	}

	var items []MigratableItem
	var nodeIDs []int64

	dsRows, err := s.db.Query(ctx, `SELECT
			d.id,
			d.name,
			COALESCE((
				SELECT string_agg(p.name, '、' ORDER BY p.id)
				FROM project_datasets pd
				JOIN projects p ON p.id = pd.project_id
				WHERE pd.dataset_id = d.id AND p.deleted_at IS NULL
			), '') AS project_name,
			d.dir_name,
			d.image_count,
			d.total_bytes,
			d.storage_node_id,
			d.created_at
		FROM datasets d
		WHERE d.deleted_at IS NULL
			AND d.storage_node_id <> $1
			AND NOT EXISTS (
				SELECT 1 FROM dataset_imports di
				WHERE di.dataset_id = d.id AND di.status IN ('uploading', 'queued', 'running')
			)
			AND NOT EXISTS (
				SELECT 1 FROM storage_migrations sm
				WHERE sm.target_type = 'dataset' AND sm.target_id = d.id AND sm.status IN ('queued', 'running')
			)
		ORDER BY d.updated_at DESC, d.id DESC`, activeRef.ID)
	if err != nil {
		return nil, err
	}
	for dsRows.Next() {
		var it MigratableItem
		var nodeID int64
		if err := dsRows.Scan(&it.TargetID, &it.Name, &it.ProjectName, &it.DirName,
			&it.ImageCount, &it.TotalBytes, &nodeID, &it.CreatedAt); err != nil {
			dsRows.Close()
			return nil, err
		}
		it.TargetType = TargetDataset
		it.StorageNode.ID = nodeID
		it.CreatedAt = it.CreatedAt.UTC()
		nodeIDs = append(nodeIDs, nodeID)
		items = append(items, it)
	}
	dsRows.Close()
	if err := dsRows.Err(); err != nil {
		return nil, err
	}

	dvRows, err := s.db.Query(ctx, `SELECT
			v.id,
			p.name,
			v.version,
			v.note,
			v.dir_name,
			v.image_count,
			v.total_bytes,
			v.storage_node_id,
			v.created_at
		FROM data_versions v
		JOIN projects p ON p.id = v.project_id
		WHERE v.deleted_at IS NULL
			AND p.deleted_at IS NULL
			AND v.status = 'ready'
			AND v.storage_node_id <> $1
			AND NOT EXISTS (
				SELECT 1 FROM storage_migrations sm
				WHERE sm.target_type = 'data_version' AND sm.target_id = v.id AND sm.status IN ('queued', 'running')
			)
		ORDER BY v.created_at DESC, v.id DESC`, activeRef.ID)
	if err != nil {
		return nil, err
	}
	for dvRows.Next() {
		var it MigratableItem
		var ver int
		var note string
		var nodeID int64
		if err := dvRows.Scan(&it.TargetID, &it.ProjectName, &ver, &note, &it.DirName,
			&it.ImageCount, &it.TotalBytes, &nodeID, &it.CreatedAt); err != nil {
			dvRows.Close()
			return nil, err
		}
		it.TargetType = TargetDataVersion
		it.Name = formatVersionLabel(it.ProjectName, ver, note)
		it.StorageNode.ID = nodeID
		it.CreatedAt = it.CreatedAt.UTC()
		nodeIDs = append(nodeIDs, nodeID)
		items = append(items, it)
	}
	dvRows.Close()
	if err := dvRows.Err(); err != nil {
		return nil, err
	}

	if len(items) == 0 {
		return []MigratableItem{}, nil
	}
	refs, err := s.NodeRefs(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if ref, ok := refs[items[i].StorageNode.ID]; ok {
			items[i].StorageNode = ref
		}
	}
	return items, nil
}

func formatVersionLabel(projectName string, version int, note string) string {
	lbl := fmt.Sprintf("%s / v%d", projectName, version)
	if note != "" {
		lbl += "（" + note + "）"
	}
	return lbl
}

type migrationRow struct {
	m          Migration
	fromNodeID int64
	toNodeID   int64
}

const migrationSelectColumns = `id, target_type, target_id, target_name, from_node_id, to_node_id,
	delete_source, status, files_done, files_total, bytes_done, bytes_total, error,
	created_at, started_at, finished_at, updated_at`

func scanMigrationRow(row pgx.Row) (migrationRow, error) {
	var r migrationRow
	err := row.Scan(
		&r.m.ID, &r.m.TargetType, &r.m.TargetID, &r.m.TargetName,
		&r.fromNodeID, &r.toNodeID,
		&r.m.DeleteSource, &r.m.Status,
		&r.m.FilesDone, &r.m.FilesTotal, &r.m.BytesDone, &r.m.BytesTotal,
		&r.m.Error,
		&r.m.CreatedAt, &r.m.StartedAt, &r.m.FinishedAt, &r.m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, newError(ErrNotFound, "迁移任务不存在")
	}
	if err != nil {
		return r, err
	}
	r.m.CreatedAt = r.m.CreatedAt.UTC()
	r.m.UpdatedAt = r.m.UpdatedAt.UTC()
	if r.m.StartedAt != nil {
		u := r.m.StartedAt.UTC()
		r.m.StartedAt = &u
	}
	if r.m.FinishedAt != nil {
		u := r.m.FinishedAt.UTC()
		r.m.FinishedAt = &u
	}
	return r, nil
}

func (s *Service) decorateMigrations(ctx context.Context, raw []migrationRow) ([]Migration, error) {
	if len(raw) == 0 {
		return []Migration{}, nil
	}
	ids := make([]int64, 0, len(raw)*2)
	for _, r := range raw {
		ids = append(ids, r.fromNodeID, r.toNodeID)
	}
	refs, err := s.NodeRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, len(raw))
	for i, r := range raw {
		m := r.m
		m.FromNode = refs[r.fromNodeID]
		if m.FromNode.ID == 0 {
			m.FromNode = NodeRef{ID: r.fromNodeID}
		}
		m.ToNode = refs[r.toNodeID]
		if m.ToNode.ID == 0 {
			m.ToNode = NodeRef{ID: r.toNodeID}
		}
		out[i] = m
	}
	return out, nil
}

// ListMigrations returns the 50 most recent storage migration tasks.
func (s *Service) ListMigrations(ctx context.Context) ([]Migration, error) {
	if s.db == nil {
		return []Migration{}, nil
	}
	rows, err := s.db.Query(ctx,
		"SELECT "+migrationSelectColumns+" FROM storage_migrations ORDER BY created_at DESC, id DESC LIMIT 50")
	if err != nil {
		return nil, err
	}
	raw, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (migrationRow, error) {
		return scanMigrationRow(row)
	})
	if err != nil {
		return nil, err
	}
	return s.decorateMigrations(ctx, raw)
}

// CreateMigrations queues online migrations of datasets and/or ready data
// versions to the currently active storage node.
func (s *Service) CreateMigrations(ctx context.Context, in CreateMigrationsInput) ([]Migration, error) {
	if s.db == nil {
		return nil, newError(ErrUnavailable, "数据库未初始化")
	}
	if len(in.Targets) == 0 {
		return nil, newError(ErrInvalid, "请至少选择一项要迁移的数据集或数据版本")
	}
	if len(in.Targets) > 100 {
		return nil, newError(ErrInvalid, "单次最多提交 100 项迁移任务")
	}
	deleteSource := true
	if in.DeleteSource != nil {
		deleteSource = *in.DeleteSource
	}

	activeRef, activeVol, err := s.ActiveNode(ctx)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	probeErr := activeVol.Probe(pctx)
	cancel()
	if probeErr != nil {
		return nil, wrapError(ErrConflict, probeErr, "当前存储节点 %q 无法连通或不可写，无法启动迁移", activeRef.Name)
	}

	seen := make(map[MigrationTarget]bool, len(in.Targets))
	uniqTargets := make([]MigrationTarget, 0, len(in.Targets))
	for _, t := range in.Targets {
		if t.TargetType != TargetDataset && t.TargetType != TargetDataVersion {
			return nil, newError(ErrInvalid, "不支持的迁移目标类型 %q", t.TargetType)
		}
		if t.TargetID <= 0 {
			return nil, newError(ErrInvalid, "迁移目标 ID 无效")
		}
		if !seen[t] {
			seen[t] = true
			uniqTargets = append(uniqTargets, t)
		}
	}

	var inserted []migrationRow
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, t := range uniqTargets {
			var already bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM storage_migrations
				WHERE target_type = $1 AND target_id = $2 AND status IN ('queued', 'running')
			)`, t.TargetType, t.TargetID).Scan(&already); err != nil {
				return err
			}
			if already {
				return newError(ErrConflict, "所选条目中已有正在排队或执行的迁移任务")
			}

			var targetName string
			var fromNodeID int64
			var totalBytes int64

			switch t.TargetType {
			case TargetDataset:
				err := tx.QueryRow(ctx, `SELECT name, storage_node_id, total_bytes
					FROM datasets WHERE id = $1 AND deleted_at IS NULL FOR SHARE`, t.TargetID).
					Scan(&targetName, &fromNodeID, &totalBytes)
				if errors.Is(err, pgx.ErrNoRows) {
					return newError(ErrNotFound, "数据集 #%d 不存在或已在回收站中", t.TargetID)
				}
				if err != nil {
					return err
				}
				var busy bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (
					SELECT 1 FROM dataset_imports
					WHERE dataset_id = $1 AND status IN ('uploading', 'queued', 'running')
				)`, t.TargetID).Scan(&busy); err != nil {
					return err
				}
				if busy {
					return newError(ErrConflict, "数据集 %q 正在执行导入或扫描任务，请完成后再迁移", targetName)
				}

			case TargetDataVersion:
				var projName, note, status string
				var ver int
				err := tx.QueryRow(ctx, `SELECT p.name, v.version, v.note, v.status, v.storage_node_id, v.total_bytes
					FROM data_versions v
					JOIN projects p ON p.id = v.project_id
					WHERE v.id = $1 AND v.deleted_at IS NULL AND p.deleted_at IS NULL FOR SHARE OF v`, t.TargetID).
					Scan(&projName, &ver, &note, &status, &fromNodeID, &totalBytes)
				if errors.Is(err, pgx.ErrNoRows) {
					return newError(ErrNotFound, "数据版本 #%d 不存在或已在回收站中", t.TargetID)
				}
				if err != nil {
					return err
				}
				targetName = formatVersionLabel(projName, ver, note)
				if status != "ready" {
					return newError(ErrConflict, "数据版本 %q 尚未构建完成，仅支持迁移已就绪的数据版本", targetName)
				}
			}

			if fromNodeID == activeRef.ID {
				return newError(ErrConflict, "%q 已经位于当前激活的存储节点 %q 上", targetName, activeRef.Name)
			}

			mr, err := scanMigrationRow(tx.QueryRow(ctx, `INSERT INTO storage_migrations
				(target_type, target_id, target_name, from_node_id, to_node_id, delete_source, status, bytes_total)
				VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7)
				RETURNING `+migrationSelectColumns,
				t.TargetType, t.TargetID, targetName, fromNodeID, activeRef.ID, deleteSource, totalBytes))
			if err != nil {
				return err
			}
			inserted = append(inserted, mr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.migMu.Lock()
	for _, mr := range inserted {
		s.activeMig[MigrationTarget{TargetType: mr.m.TargetType, TargetID: mr.m.TargetID}] = mr.m.ID
	}
	s.migMu.Unlock()
	s.notifyMigrator()

	return s.decorateMigrations(ctx, inserted)
}

// CancelMigration cancels a queued or running migration task.
func (s *Service) CancelMigration(ctx context.Context, id int64) (Migration, error) {
	if s.db == nil {
		return Migration{}, newError(ErrUnavailable, "数据库未初始化")
	}

	var mr migrationRow
	var cancelFn context.CancelFunc
	var clearedTarget *MigrationTarget

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var scanErr error
		mr, scanErr = scanMigrationRow(tx.QueryRow(ctx,
			"SELECT "+migrationSelectColumns+" FROM storage_migrations WHERE id = $1 FOR UPDATE", id))
		if scanErr != nil {
			return scanErr
		}
		switch mr.m.Status {
		case MigrationQueued:
			mr, scanErr = scanMigrationRow(tx.QueryRow(ctx, `UPDATE storage_migrations
				SET status = 'canceled', error = '用户已取消', finished_at = now(), updated_at = now()
				WHERE id = $1
				RETURNING `+migrationSelectColumns, id))
			if scanErr != nil {
				return scanErr
			}
			t := MigrationTarget{TargetType: mr.m.TargetType, TargetID: mr.m.TargetID}
			clearedTarget = &t
			return nil

		case MigrationRunning:
			var alreadySwitched bool
			switch mr.m.TargetType {
			case TargetDataset:
				_ = tx.QueryRow(ctx,
					"SELECT EXISTS (SELECT 1 FROM datasets WHERE id = $1 AND storage_node_id = $2)",
					mr.m.TargetID, mr.toNodeID).Scan(&alreadySwitched)
			case TargetDataVersion:
				_ = tx.QueryRow(ctx,
					"SELECT EXISTS (SELECT 1 FROM data_versions WHERE id = $1 AND storage_node_id = $2)",
					mr.m.TargetID, mr.toNodeID).Scan(&alreadySwitched)
			}
			if alreadySwitched {
				return newError(ErrConflict, "数据已完成节点切换，正在清理原节点文件，无法取消")
			}
			mr, scanErr = scanMigrationRow(tx.QueryRow(ctx, `UPDATE storage_migrations
				SET status = 'canceled', error = '用户已取消', finished_at = now(), updated_at = now()
				WHERE id = $1
				RETURNING `+migrationSelectColumns, id))
			if scanErr != nil {
				return scanErr
			}
			t := MigrationTarget{TargetType: mr.m.TargetType, TargetID: mr.m.TargetID}
			clearedTarget = &t
			s.migMu.Lock()
			cancelFn = s.cancelMig[id]
			s.migMu.Unlock()
			return nil

		default:
			return newError(ErrConflict, "该迁移任务已处于 %s 状态，无法取消", mr.m.Status)
		}
	})
	if err != nil {
		return Migration{}, err
	}

	if clearedTarget != nil {
		s.migMu.Lock()
		if curID, ok := s.activeMig[*clearedTarget]; ok && curID == id {
			delete(s.activeMig, *clearedTarget)
		}
		s.migMu.Unlock()
	}
	if cancelFn != nil {
		cancelFn()
	}

	list, err := s.decorateMigrations(ctx, []migrationRow{mr})
	if err != nil {
		return Migration{}, err
	}
	return list[0], nil
}

// WaitMigrationsIdle blocks until no migration is queued or running (used in tests).
func (s *Service) WaitMigrationsIdle(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		var active int
		if err := s.db.QueryRow(ctx,
			"SELECT COUNT(*) FROM storage_migrations WHERE status IN ('queued', 'running')").Scan(&active); err != nil {
			return err
		}
		s.migMu.Lock()
		inMem := len(s.activeMig)
		s.migMu.Unlock()
		if active == 0 && inMem == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) migratorLoop(ctx context.Context) {
	for {
		ran, err := s.stepNextMigration(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("storage migration step failed", "err", err)
		}
		if ctx.Err() != nil {
			return
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wakeMig:
		case <-time.After(10 * time.Second):
		}
	}
}

func (s *Service) stepNextMigration(ctx context.Context) (bool, error) {
	var mr migrationRow
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var scanErr error
		mr, scanErr = scanMigrationRow(tx.QueryRow(ctx, `SELECT `+migrationSelectColumns+`
			FROM storage_migrations
			WHERE status = 'queued'
			ORDER BY created_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`))
		if scanErr != nil {
			return scanErr
		}
		mr, scanErr = scanMigrationRow(tx.QueryRow(ctx, `UPDATE storage_migrations
			SET status = 'running', started_at = now(), updated_at = now()
			WHERE id = $1
			RETURNING `+migrationSelectColumns, mr.m.ID))
		return scanErr
	})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	target := MigrationTarget{TargetType: mr.m.TargetType, TargetID: mr.m.TargetID}
	s.migMu.Lock()
	s.activeMig[target] = mr.m.ID
	s.cancelMig[mr.m.ID] = cancel
	s.migMu.Unlock()

	defer func() {
		cancel()
		s.migMu.Lock()
		delete(s.cancelMig, mr.m.ID)
		if curID, ok := s.activeMig[target]; ok && curID == mr.m.ID {
			delete(s.activeMig, target)
		}
		s.migMu.Unlock()
	}()

	s.executeMigration(runCtx, mr)
	return true, nil
}

type migSourceEntry struct {
	rel  string
	size int64
	mode fs.FileMode
}

type countingReader struct {
	r      io.Reader
	onRead func(n int64)
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 && cr.onRead != nil {
		cr.onRead(int64(n))
	}
	return n, err
}

func (s *Service) executeMigration(ctx context.Context, mr migrationRow) {
	var (
		space         string
		srcDirName    string
		dstDirName    string
		dstCreated    bool
		toVol         Volume
		fromVol       Volume
		origUpdatedAt time.Time
	)

	failOrCancel := func(cause error) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if dstCreated && toVol != nil && dstDirName != "" {
			if rerr := toVol.RemoveDirAll(cleanupCtx, space, dstDirName); rerr != nil {
				s.log.Warn("cleanup partial migrated folder failed",
					"migration_id", mr.m.ID, "space", space, "dir", dstDirName, "err", rerr)
			}
		}
		// Preserve 'canceled' if CancelMigration already marked it canceled.
		msg := cause.Error()
		_, _ = s.db.Exec(cleanupCtx, `UPDATE storage_migrations
			SET status = CASE WHEN status = 'canceled' THEN 'canceled' ELSE 'failed' END,
				error = CASE WHEN status = 'canceled' THEN error ELSE $2 END,
				finished_at = COALESCE(finished_at, now()),
				updated_at = now()
			WHERE id = $1 AND status <> 'succeeded'`, mr.m.ID, msg)
	}

	switch mr.m.TargetType {
	case TargetDataset:
		space = SpaceDatasets
		var nodeID int64
		err := s.db.QueryRow(ctx,
			"SELECT dir_name, storage_node_id, updated_at FROM datasets WHERE id = $1 AND deleted_at IS NULL",
			mr.m.TargetID).Scan(&srcDirName, &nodeID, &origUpdatedAt)
		if err != nil {
			failOrCancel(fmt.Errorf("读取源数据集失败：%w", err))
			return
		}
		if nodeID != mr.fromNodeID {
			failOrCancel(errors.New("数据集所在存储节点已发生变化"))
			return
		}

	case TargetDataVersion:
		space = SpaceExports
		var nodeID int64
		var status string
		err := s.db.QueryRow(ctx,
			"SELECT dir_name, storage_node_id, status FROM data_versions WHERE id = $1 AND deleted_at IS NULL",
			mr.m.TargetID).Scan(&srcDirName, &nodeID, &status)
		if err != nil {
			failOrCancel(fmt.Errorf("读取源数据版本失败：%w", err))
			return
		}
		if status != "ready" || nodeID != mr.fromNodeID {
			failOrCancel(errors.New("数据版本状态或存储节点已发生变化"))
			return
		}

	default:
		failOrCancel(fmt.Errorf("未知迁移类型 %q", mr.m.TargetType))
		return
	}

	var err error
	if _, fromVol, err = s.VolumeForNode(ctx, mr.fromNodeID); err != nil {
		failOrCancel(fmt.Errorf("连接源存储节点失败：%w", err))
		return
	}
	if _, toVol, err = s.VolumeForNode(ctx, mr.toNodeID); err != nil {
		failOrCancel(fmt.Errorf("连接目标存储节点失败：%w", err))
		return
	}

	srcDir, err := fromVol.OpenDir(ctx, space, srcDirName)
	if err != nil {
		failOrCancel(fmt.Errorf("打开源目录 %s 失败：%w", fromVol.DisplayPath(space, srcDirName), err))
		return
	}
	defer srcDir.Close()

	var files []migSourceEntry
	var dirs []string
	var totalBytes int64
	err = srcDir.WalkFiles(ctx, func(rel string, info fs.FileInfo, isDir bool) error {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("源目录包含符号链接 %q，出于安全策略拒绝迁移", rel)
		}
		if isDir {
			dirs = append(dirs, rel)
			return nil
		}
		if info.Mode().IsRegular() {
			files = append(files, migSourceEntry{rel: rel, size: info.Size(), mode: info.Mode().Perm()})
			totalBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		failOrCancel(fmt.Errorf("扫描源文件列表失败：%w", err))
		return
	}

	if u, uerr := toVol.Usage(ctx); uerr == nil && u.TotalBytes > 0 {
		need := uint64(totalBytes) + migrationFreeHeadroom
		if u.FreeBytes < need {
			failOrCancel(newError(ErrInsufficientStorage, "目标存储节点剩余空间不足（需要 %.1f MB，剩余 %.1f MB）",
				float64(need)/(1<<20), float64(u.FreeBytes)/(1<<20)))
			return
		}
	}

	if _, err := s.db.Exec(ctx, `UPDATE storage_migrations
		SET files_total = $2, bytes_total = $3, updated_at = now()
		WHERE id = $1`, mr.m.ID, len(files), totalBytes); err != nil {
		failOrCancel(err)
		return
	}

	dstDirName, err = s.pickTargetDirName(ctx, mr.m.TargetType, mr.toNodeID, toVol, space, srcDirName)
	if err != nil {
		failOrCancel(err)
		return
	}

	if err := toVol.MkdirDir(ctx, space, dstDirName, true); err != nil {
		failOrCancel(fmt.Errorf("在目标存储节点创建目录失败：%w", err))
		return
	}
	dstCreated = true

	dstDir, err := toVol.OpenDir(ctx, space, dstDirName)
	if err != nil {
		failOrCancel(fmt.Errorf("打开目标目录失败：%w", err))
		return
	}
	defer dstDir.Close()

	for _, drel := range dirs {
		if err := dstDir.EnsureDir(drel); err != nil {
			failOrCancel(fmt.Errorf("创建子目录 %q 失败：%w", drel, err))
			return
		}
	}

	copiedModTimes := make(map[string]time.Time, len(files))
	var filesDone int
	var bytesDone int64
	lastFlush := time.Now()

	flushProgress := func(force bool) {
		if !force && time.Since(lastFlush) < 250*time.Millisecond {
			return
		}
		lastFlush = time.Now()
		_, _ = s.db.Exec(ctx, `UPDATE storage_migrations
			SET files_done = $2, bytes_done = $3, updated_at = now()
			WHERE id = $1 AND status = 'running'`, mr.m.ID, filesDone, bytesDone)
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			failOrCancel(err)
			return
		}
		sf, err := srcDir.Open(f.rel)
		if err != nil {
			failOrCancel(fmt.Errorf("读取源文件 %q 失败：%w", f.rel, err))
			return
		}
		perm := f.mode
		if perm == 0 {
			perm = 0o644
		}
		var fileCopied int64
		cr := &countingReader{
			r: sf,
			onRead: func(n int64) {
				fileCopied += n
				bytesDone += n
				flushProgress(false)
			},
		}
		dstInfo, putErr := dstDir.PutFile(ctx, f.rel, cr, f.size, perm)
		_ = sf.Close()
		if putErr != nil {
			failOrCancel(fmt.Errorf("写入目标文件 %q 失败：%w", f.rel, putErr))
			return
		}
		if dstInfo.Size() != f.size || fileCopied != f.size {
			failOrCancel(fmt.Errorf("文件 %q 校验不一致（源文件 %d 字节，目标 %d 字节）",
				f.rel, f.size, dstInfo.Size()))
			return
		}
		copiedModTimes[f.rel] = dstInfo.ModTime().UTC()
		filesDone++
		flushProgress(false)
	}
	flushProgress(true)
	_ = dstDir.Close()
	_ = srcDir.Close()

	// Commit storage_node_id switch atomically in PostgreSQL.
	commitErr := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var curStatus string
		if err := tx.QueryRow(ctx,
			"SELECT status FROM storage_migrations WHERE id = $1 FOR UPDATE", mr.m.ID).Scan(&curStatus); err != nil {
			return err
		}
		if curStatus != MigrationRunning {
			return errors.New("迁移任务已被取消")
		}

		switch mr.m.TargetType {
		case TargetDataset:
			var curDir string
			var curNode int64
			var curUpdated time.Time
			if err := tx.QueryRow(ctx, `SELECT dir_name, storage_node_id, updated_at
				FROM datasets WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, mr.m.TargetID).
				Scan(&curDir, &curNode, &curUpdated); err != nil {
				return fmt.Errorf("锁定数据集失败：%w", err)
			}
			if curNode != mr.fromNodeID || curDir != srcDirName || !curUpdated.UTC().Equal(origUpdatedAt.UTC()) {
				return errors.New("数据集在迁移期间发生了变更，已中止切换以保护数据")
			}
			if _, err := tx.Exec(ctx, `UPDATE datasets
				SET storage_node_id = $2, dir_name = $3, recycled_in_place = false, updated_at = now()
				WHERE id = $1`, mr.m.TargetID, mr.toNodeID, dstDirName); err != nil {
				return err
			}
			if err := updateDatasetMigratedModTimes(ctx, tx, mr.m.TargetID, copiedModTimes); err != nil {
				return err
			}

		case TargetDataVersion:
			var curDir, curVerStatus string
			var curNode int64
			if err := tx.QueryRow(ctx, `SELECT dir_name, storage_node_id, status
				FROM data_versions WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, mr.m.TargetID).
				Scan(&curDir, &curNode, &curVerStatus); err != nil {
				return fmt.Errorf("锁定数据版本失败：%w", err)
			}
			if curVerStatus != "ready" || curNode != mr.fromNodeID || curDir != srcDirName {
				return errors.New("数据版本在迁移期间发生了变更，已中止切换")
			}
			if _, err := tx.Exec(ctx, `UPDATE data_versions
				SET storage_node_id = $2, dir_name = $3, updated_at = now()
				WHERE id = $1`, mr.m.TargetID, mr.toNodeID, dstDirName); err != nil {
				return err
			}
			if err := updateDataVersionMigratedModTimes(ctx, tx, mr.m.TargetID, copiedModTimes); err != nil {
				return err
			}
		}

		_, err := tx.Exec(ctx, `UPDATE storage_migrations
			SET files_done = $2, bytes_done = $3, updated_at = now()
			WHERE id = $1`, mr.m.ID, filesDone, bytesDone)
		return err
	})
	if commitErr != nil {
		failOrCancel(commitErr)
		return
	}

	// Database now points to the destination node. Delete source files BEFORE
	// flipping migration status to 'succeeded', so both API callers and UI
	// capacity bars immediately see reclaimed disk space once status='succeeded'.
	if mr.m.DeleteSource {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := fromVol.RemoveDirAll(cleanCtx, space, srcDirName); err != nil {
			s.log.Warn("remove migrated source directory failed",
				"migration_id", mr.m.ID, "from_node_id", mr.fromNodeID,
				"space", space, "dir", srcDirName, "err", err)
		}
		cleanCancel()
	}

	finCtx, finCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer finCancel()
	if _, err := s.db.Exec(finCtx, `UPDATE storage_migrations
		SET status = 'succeeded', files_done = $2, bytes_done = $3, error = '',
			finished_at = now(), updated_at = now()
		WHERE id = $1`, mr.m.ID, filesDone, bytesDone); err != nil {
		s.log.Warn("mark storage migration succeeded failed", "migration_id", mr.m.ID, "err", err)
	}
}

func (s *Service) pickTargetDirName(ctx context.Context, targetType string, toNodeID int64, toVol Volume, space, srcDirName string) (string, error) {
	if targetType == TargetDataVersion {
		exists, _, err := toVol.DirExists(ctx, space, srcDirName)
		if err != nil {
			return "", err
		}
		if exists {
			var owned bool
			if err := s.db.QueryRow(ctx,
				"SELECT EXISTS (SELECT 1 FROM data_versions WHERE storage_node_id = $1 AND dir_name = $2)",
				toNodeID, srcDirName).Scan(&owned); err != nil {
				return "", err
			}
			if owned {
				return "", newError(ErrConflict, "目标存储节点已存在同路径数据版本目录 %q", srcDirName)
			}
			if err := toVol.RemoveDirAll(ctx, space, srcDirName); err != nil {
				return "", fmt.Errorf("清理目标节点残留目录 %q 失败：%w", srcDirName, err)
			}
		}
		return srcDirName, nil
	}

	// Dataset: prefer exact srcDirName, otherwise append -2, -3, ...
	for n := 1; n <= 1000; n++ {
		cand := srcDirName
		if n > 1 {
			cand = srcDirName + "-" + strconv.Itoa(n)
		}
		var takenInDB bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM datasets
			WHERE storage_node_id = $1 AND lower(dir_name) = lower($2)
				AND (deleted_at IS NULL OR NOT delete_files OR recycled_in_place)
		)`, toNodeID, cand).Scan(&takenInDB); err != nil {
			return "", err
		}
		if takenInDB {
			continue
		}
		exists, _, err := toVol.DirExists(ctx, space, cand)
		if err != nil {
			return "", err
		}
		if !exists {
			return cand, nil
		}
	}
	return "", newError(ErrConflict, "目标存储节点无法分配可用的数据集目录名称")
}

func updateDatasetMigratedModTimes(ctx context.Context, tx pgx.Tx, datasetID int64, modTimes map[string]time.Time) error {
	if len(modTimes) == 0 {
		return nil
	}
	type pair struct {
		rel string
		mt  time.Time
	}
	pairs := make([]pair, 0, len(modTimes))
	for rel, mt := range modTimes {
		pairs = append(pairs, pair{rel: rel, mt: mt})
	}
	for chunk := range slices.Chunk(pairs, 500) {
		b := &pgx.Batch{}
		for _, p := range chunk {
			b.Queue("UPDATE dataset_images SET mod_time = $3 WHERE dataset_id = $1 AND rel_path = $2",
				datasetID, p.rel, p.mt)
			b.Queue("UPDATE dataset_images SET annotation_mod_time = $3 WHERE dataset_id = $1 AND annotation_path = $2",
				datasetID, p.rel, p.mt)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return fmt.Errorf("update migrated dataset mod_times: %w", err)
		}
	}
	return nil
}

func updateDataVersionMigratedModTimes(ctx context.Context, tx pgx.Tx, versionID int64, modTimes map[string]time.Time) error {
	if len(modTimes) == 0 {
		return nil
	}
	type pair struct {
		rel string
		mt  time.Time
	}
	pairs := make([]pair, 0, len(modTimes))
	for rel, mt := range modTimes {
		pairs = append(pairs, pair{rel: rel, mt: mt})
	}
	for chunk := range slices.Chunk(pairs, 500) {
		b := &pgx.Batch{}
		for _, p := range chunk {
			b.Queue(`UPDATE data_version_images
				SET mod_time = $3,
					stored = CASE WHEN stored = 'link' THEN 'copy' ELSE stored END
				WHERE version_id = $1 AND path = $2`,
				versionID, p.rel, p.mt)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return fmt.Errorf("update migrated data version mod_times: %w", err)
		}
	}
	return nil
}
