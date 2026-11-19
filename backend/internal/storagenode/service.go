package storagenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

const (
	trainerCacheSetting = "storage.trainer_dataset_cache"
	nodeProbeTimeout    = 12 * time.Second
	listUsageTimeout    = 2 * time.Second
)

// Options configures a storage node Service.
type Options struct {
	DB                  *pgxpool.Pool
	Storage             *storage.Storage
	Logger              *slog.Logger
	SecretKey           string
	StorageServiceURL   string
	StorageServiceToken string
}

type cachedVolume struct {
	ref       NodeRef
	vol       Volume
	updatedAt time.Time
}

// Service manages storage nodes, protocol Volume drivers, trainer dataset cache
// policies, and background cross-node migrations.
type Service struct {
	db                  *pgxpool.Pool
	storage             *storage.Storage
	dataRoot            string
	log                 *slog.Logger
	cipher              *cipherBox
	storageServiceURL   string
	storageServiceToken string

	mu      sync.Mutex
	volumes map[int64]cachedVolume

	// Background migration loop state (migrator.go).
	base      context.Context
	stop      context.CancelFunc
	loops     sync.WaitGroup
	wakeMig   chan struct{}
	migMu     sync.Mutex
	activeMig map[MigrationTarget]int64    // target -> migration ID while queued/running
	cancelMig map[int64]context.CancelFunc // running migration ID -> cancel func
}

// NewService constructs a storage node Service. When DB is nil (unit tests),
// ActiveNode and VolumeForNode resolve the built-in local directory on Storage.
func NewService(o Options) *Service {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	var dataRoot string
	if o.Storage != nil {
		dataRoot = o.Storage.Root()
	}
	return &Service{
		db:                  o.DB,
		storage:             o.Storage,
		dataRoot:            dataRoot,
		log:                 log,
		cipher:              newCipherBox(o.SecretKey, dataRoot, o.DB),
		storageServiceURL:   strings.TrimRight(strings.TrimSpace(o.StorageServiceURL), "/"),
		storageServiceToken: o.StorageServiceToken,
		volumes:             make(map[int64]cachedVolume),
		base:                context.Background(),
		wakeMig:             make(chan struct{}, 1),
		activeMig:           make(map[MigrationTarget]int64),
		cancelMig:           make(map[int64]context.CancelFunc),
	}
}

// Start recovers interrupted migrations and starts the background migration worker.
func (s *Service) Start(ctx context.Context) error {
	if s.db != nil {
		if err := s.recoverMigrations(ctx); err != nil {
			return err
		}
	}
	s.base, s.stop = context.WithCancel(ctx)
	if s.db != nil {
		s.loops.Add(1)
		go func() {
			defer s.loops.Done()
			s.migratorLoop(s.base)
		}()
	}
	return nil
}

// Stop stops the background migration worker and closes cached remote volumes.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.loops.Wait()

	s.mu.Lock()
	vols := make([]Volume, 0, len(s.volumes))
	for id, cv := range s.volumes {
		vols = append(vols, cv.vol)
		delete(s.volumes, id)
	}
	s.mu.Unlock()

	for _, v := range vols {
		_ = v.Close()
	}
}

type nodeRow struct {
	id             int64
	name           string
	kind           string
	isBuiltin      bool
	isActive       bool
	rawConfig      []byte
	credentialsEnc []byte
	lastCheckedAt  *time.Time
	lastStatus     string
	lastError      string
	createdAt      time.Time
	updatedAt      time.Time
}

const nodeSelectColumns = `id, name, kind, is_builtin, is_active, config, credentials_enc,
	last_checked_at, last_status, last_error, created_at, updated_at`

func scanNodeRow(row pgx.Row) (nodeRow, error) {
	var r nodeRow
	err := row.Scan(
		&r.id, &r.name, &r.kind, &r.isBuiltin, &r.isActive,
		&r.rawConfig, &r.credentialsEnc,
		&r.lastCheckedAt, &r.lastStatus, &r.lastError,
		&r.createdAt, &r.updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, newError(ErrNotFound, "存储节点不存在")
	}
	if err != nil {
		return r, err
	}
	r.createdAt = r.createdAt.UTC()
	r.updatedAt = r.updatedAt.UTC()
	if r.lastCheckedAt != nil {
		u := r.lastCheckedAt.UTC()
		r.lastCheckedAt = &u
	}
	return r, nil
}

func (s *Service) builtinRef() NodeRef {
	ref := BuiltinRef()
	ref.SupportsArchive = s.NodeSupportsArchive(KindLocal, true)
	return ref
}

func (s *Service) rowRef(r nodeRow) NodeRef {
	return NodeRef{
		ID:              r.id,
		Name:            r.name,
		Kind:            r.kind,
		IsBuiltin:       r.isBuiltin,
		SupportsArchive: s.NodeSupportsArchive(r.kind, r.isBuiltin),
	}
}

func (s *Service) parseRowConfig(r nodeRow) (NodeConfig, string, error) {
	switch r.kind {
	case KindLocal:
		if r.isBuiltin {
			p := s.dataRoot
			return NodeConfig{Local: &LocalConfig{Path: p}}, p, nil
		}
		var lc LocalConfig
		if len(r.rawConfig) > 0 {
			if err := json.Unmarshal(r.rawConfig, &lc); err != nil {
				return NodeConfig{}, "", fmt.Errorf("parse local config: %w", err)
			}
		}
		return NodeConfig{Local: &lc}, lc.Path, nil

	case KindDocker:
		var dc DockerConfig
		if len(r.rawConfig) > 0 {
			if err := json.Unmarshal(r.rawConfig, &dc); err != nil {
				return NodeConfig{}, "", fmt.Errorf("parse docker config: %w", err)
			}
		}
		return NodeConfig{Docker: &dc}, dc.Endpoint, nil

	case KindS3:
		var sc S3Config
		if len(r.rawConfig) > 0 {
			if err := json.Unmarshal(r.rawConfig, &sc); err != nil {
				return NodeConfig{}, "", fmt.Errorf("parse s3 config: %w", err)
			}
		}
		return NodeConfig{S3: &sc}, formatS3Target(sc), nil

	case KindSMB:
		var mc SMBConfig
		if len(r.rawConfig) > 0 {
			if err := json.Unmarshal(r.rawConfig, &mc); err != nil {
				return NodeConfig{}, "", fmt.Errorf("parse smb config: %w", err)
			}
		}
		return NodeConfig{SMB: &mc}, formatSMBTarget(mc), nil

	default:
		return NodeConfig{}, "", fmt.Errorf("unsupported storage node kind %q", r.kind)
	}
}

func formatS3Target(sc S3Config) string {
	pfx := strings.Trim(strings.ReplaceAll(strings.TrimSpace(sc.Prefix), `\`, "/"), "/")
	if pfx != "" {
		return fmt.Sprintf("s3://%s/%s (%s)", sc.Bucket, pfx, sc.Endpoint)
	}
	return fmt.Sprintf("s3://%s (%s)", sc.Bucket, sc.Endpoint)
}

func formatSMBTarget(mc SMBConfig) string {
	hostPort := mc.Host
	if mc.Port != 0 && mc.Port != 445 {
		hostPort = fmt.Sprintf("%s:%d", mc.Host, mc.Port)
	}
	base := strings.Trim(strings.ReplaceAll(strings.TrimSpace(mc.BasePath), "/", `\`), `\`)
	if base != "" {
		return fmt.Sprintf(`\\%s\%s\%s`, hostPort, mc.Share, base)
	}
	return fmt.Sprintf(`\\%s\%s`, hostPort, mc.Share)
}

func (s *Service) buildVolume(nodeID int64, isBuiltin bool, kind string, cfg NodeConfig, creds Credentials) (Volume, NodeConfig, string, error) {
	switch kind {
	case KindLocal:
		var rootPath string
		if isBuiltin {
			rootPath = s.dataRoot
		} else {
			if cfg.Local == nil {
				return nil, NodeConfig{}, "", newError(ErrInvalid, "请填写本机挂载目录路径")
			}
			trimmed := strings.TrimSpace(cfg.Local.Path)
			if trimmed == "" {
				return nil, NodeConfig{}, "", newError(ErrInvalid, "本机存储目录路径不能为空")
			}
			if !filepath.IsAbs(trimmed) {
				return nil, NodeConfig{}, "", newError(ErrInvalid, "本机存储目录请填写绝对路径（例如 /mnt/storage）")
			}
			rootPath = filepath.Clean(trimmed)
			if s.dataRoot != "" && filepath.Clean(s.dataRoot) == rootPath {
				return nil, NodeConfig{}, "", newError(ErrConflict, "该目录已是系统内置的本机默认存储目录")
			}
		}
		vol, err := NewLocalVolume(nodeID, isBuiltin, rootPath)
		if err != nil {
			return nil, NodeConfig{}, "", err
		}
		norm := NodeConfig{Local: &LocalConfig{Path: vol.Root()}}
		return vol, norm, vol.Root(), nil

	case KindDocker:
		if cfg.Docker == nil {
			return nil, NodeConfig{}, "", newError(ErrInvalid, "请填写 trainMaster 存储服务地址")
		}
		normDC, err := validateDockerConfig(*cfg.Docker)
		if err != nil {
			return nil, NodeConfig{}, "", err
		}
		if s.storageServiceURL != "" && strings.EqualFold(normDC.Endpoint, s.storageServiceURL) {
			return nil, NodeConfig{}, "", newError(ErrConflict, "该地址已是平台内置的本机默认存储服务地址")
		}
		vol, err := NewDockerVolume(nodeID, isBuiltin, normDC, creds)
		if err != nil {
			return nil, NodeConfig{}, "", err
		}
		norm := NodeConfig{Docker: &normDC}
		return vol, norm, normDC.Endpoint, nil

	case KindS3:
		if cfg.S3 == nil {
			return nil, NodeConfig{}, "", newError(ErrInvalid, "请填写 S3 存储连接参数")
		}
		vol, err := NewS3Volume(nodeID, *cfg.S3, creds.SecretAccessKey)
		if err != nil {
			return nil, NodeConfig{}, "", err
		}
		norm := NodeConfig{S3: &vol.cfg}
		return vol, norm, formatS3Target(vol.cfg), nil

	case KindSMB:
		if cfg.SMB == nil {
			return nil, NodeConfig{}, "", newError(ErrInvalid, "请填写 SMB 共享文件夹连接参数")
		}
		vol, err := NewSMBVolume(nodeID, *cfg.SMB, creds.Password)
		if err != nil {
			return nil, NodeConfig{}, "", err
		}
		norm := NodeConfig{SMB: &vol.cfg}
		return vol, norm, formatSMBTarget(vol.cfg), nil

	default:
		return nil, NodeConfig{}, "", newError(ErrInvalid, "不支持的存储协议类型 %q（可选：docker、local、s3、smb）", kind)
	}
}

func marshalProtocolConfig(kind string, isBuiltin bool, cfg NodeConfig) ([]byte, error) {
	if isBuiltin {
		return []byte("{}"), nil
	}
	switch kind {
	case KindLocal:
		return json.Marshal(cfg.Local)
	case KindDocker:
		return json.Marshal(cfg.Docker)
	case KindS3:
		return json.Marshal(cfg.S3)
	case KindSMB:
		return json.Marshal(cfg.SMB)
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
}

func (s *Service) volumeFromRow(ctx context.Context, r nodeRow) (NodeRef, Volume, error) {
	ref := s.rowRef(r)

	s.mu.Lock()
	if cv, ok := s.volumes[r.id]; ok && cv.updatedAt.Equal(r.updatedAt) {
		s.mu.Unlock()
		return cv.ref, cv.vol, nil
	}
	s.mu.Unlock()

	cfg, _, err := s.parseRowConfig(r)
	if err != nil {
		return ref, nil, err
	}
	creds, err := s.cipher.decrypt(ctx, r.credentialsEnc)
	if err != nil {
		return ref, nil, wrapError(ErrUnavailable, err, "读取存储节点 %q 凭据失败", r.name)
	}
	vol, _, _, err := s.buildVolume(r.id, r.isBuiltin, r.kind, cfg, creds)
	if err != nil {
		return ref, nil, err
	}

	var stale Volume
	s.mu.Lock()
	if prev, ok := s.volumes[r.id]; ok {
		if prev.updatedAt.After(r.updatedAt) {
			s.mu.Unlock()
			_ = vol.Close()
			return prev.ref, prev.vol, nil
		}
		stale = prev.vol
	}
	s.volumes[r.id] = cachedVolume{ref: ref, vol: vol, updatedAt: r.updatedAt}
	s.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}
	return ref, vol, nil
}

func (s *Service) evictCachedVolume(id int64) {
	s.mu.Lock()
	cv, ok := s.volumes[id]
	if ok {
		delete(s.volumes, id)
	}
	s.mu.Unlock()
	if ok && cv.vol != nil {
		_ = cv.vol.Close()
	}
}

func (s *Service) fallbackBuiltinVolume() (NodeRef, Volume, error) {
	ref := s.builtinRef()
	s.mu.Lock()
	if cv, ok := s.volumes[BuiltinID]; ok {
		s.mu.Unlock()
		return cv.ref, cv.vol, nil
	}
	s.mu.Unlock()

	vol, err := NewLocalVolume(BuiltinID, true, s.dataRoot)
	if err != nil {
		return ref, nil, err
	}
	s.mu.Lock()
	s.volumes[BuiltinID] = cachedVolume{ref: ref, vol: vol}
	s.mu.Unlock()
	return ref, vol, nil
}

// ActiveNode returns the currently active storage node reference and its Volume.
func (s *Service) ActiveNode(ctx context.Context) (NodeRef, Volume, error) {
	if s.db == nil {
		return s.fallbackBuiltinVolume()
	}
	r, err := scanNodeRow(s.db.QueryRow(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE is_active = true LIMIT 1"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Defensive fallback to built-in row if active flag was missing.
			r, err = scanNodeRow(s.db.QueryRow(ctx,
				"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", BuiltinID))
		}
		if err != nil {
			return NodeRef{}, nil, err
		}
	}
	return s.volumeFromRow(ctx, r)
}

// VolumeForNode returns the NodeRef and Volume for a specific storage_node_id.
func (s *Service) VolumeForNode(ctx context.Context, nodeID int64) (NodeRef, Volume, error) {
	if nodeID <= 0 {
		nodeID = BuiltinID
	}
	if s.db == nil {
		if nodeID == BuiltinID {
			return s.fallbackBuiltinVolume()
		}
		return NodeRef{}, nil, newError(ErrNotFound, "存储节点不存在")
	}
	r, err := scanNodeRow(s.db.QueryRow(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", nodeID))
	if err != nil {
		return NodeRef{}, nil, err
	}
	return s.volumeFromRow(ctx, r)
}

// NodeRefs loads compact NodeRef metadata for a batch of storage_node_ids.
func (s *Service) NodeRefs(ctx context.Context, ids []int64) (map[int64]NodeRef, error) {
	out := map[int64]NodeRef{BuiltinID: s.builtinRef()}
	if s.db == nil || len(ids) == 0 {
		return out, nil
	}
	uniq := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			id = BuiltinID
		}
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	rows, err := s.db.Query(ctx,
		"SELECT id, name, kind, is_builtin FROM storage_nodes WHERE id = ANY($1)", uniq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref NodeRef
		if err := rows.Scan(&ref.ID, &ref.Name, &ref.Kind, &ref.IsBuiltin); err != nil {
			return nil, err
		}
		ref.SupportsArchive = s.NodeSupportsArchive(ref.Kind, ref.IsBuiltin)
		out[ref.ID] = ref
	}
	return out, rows.Err()
}

// LocalVolumes returns all local-filesystem Volumes (the built-in node plus
// any configured KindLocal nodes whose directories are reachable), used by
// background trash cleanup.
func (s *Service) LocalVolumes(ctx context.Context) ([]Volume, error) {
	if s.db == nil {
		if s.dataRoot == "" {
			return nil, nil
		}
		_, vol, err := s.fallbackBuiltinVolume()
		if err != nil {
			return nil, err
		}
		return []Volume{vol}, nil
	}
	rows, err := s.db.Query(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE kind = $1 ORDER BY is_builtin DESC, id ASC", KindLocal)
	if err != nil {
		return nil, err
	}
	rawRows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (nodeRow, error) {
		return scanNodeRow(row)
	})
	if err != nil {
		return nil, err
	}
	vols := make([]Volume, 0, len(rawRows))
	for _, r := range rawRows {
		if _, vol, verr := s.volumeFromRow(ctx, r); verr == nil {
			vols = append(vols, vol)
		}
	}
	return vols, nil
}

// ListNodes returns all configured storage nodes with usage and item statistics.
func (s *Service) ListNodes(ctx context.Context) ([]Node, error) {
	if s.db == nil {
		ref, vol, err := s.fallbackBuiltinVolume()
		if err != nil {
			return nil, err
		}
		var usage *UsageInfo
		if u, uerr := vol.Usage(ctx); uerr == nil && u.TotalBytes > 0 {
			usage = toUsageInfo(u)
		}
		return []Node{{
			ID:            ref.ID,
			Name:          ref.Name,
			Kind:          ref.Kind,
			IsBuiltin:     true,
			IsActive:      true,
			DisplayTarget: s.dataRoot,
			Config:        NodeConfig{Local: &LocalConfig{Path: s.dataRoot}},
			Status:        StatusOK,
			Usage:         usage,
		}}, nil
	}

	rows, err := s.db.Query(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes ORDER BY is_builtin DESC, id ASC")
	if err != nil {
		return nil, err
	}
	rawRows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (nodeRow, error) {
		return scanNodeRow(row)
	})
	if err != nil {
		return nil, err
	}

	statsByNode, err := s.queryNodeStats(ctx)
	if err != nil {
		return nil, err
	}

	nodes := make([]Node, 0, len(rawRows))
	for _, r := range rawRows {
		n, err := s.rowToNode(ctx, r, statsByNode[r.id])
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *Service) rowToNode(ctx context.Context, r nodeRow, stats NodeStats) (Node, error) {
	cfg, target, err := s.parseRowConfig(r)
	if err != nil {
		return Node{}, err
	}
	n := Node{
		ID:              r.id,
		Name:            r.name,
		Kind:            r.kind,
		IsBuiltin:       r.isBuiltin,
		IsActive:        r.isActive,
		SupportsArchive: s.NodeSupportsArchive(r.kind, r.isBuiltin),
		HasSecret:       len(r.credentialsEnc) > 0,
		DisplayTarget:   target,
		Config:          cfg,
		Status:          r.lastStatus,
		StatusError:     r.lastError,
		LastCheckedAt:   r.lastCheckedAt,
		Stats:           stats,
		CreatedAt:       r.createdAt,
		UpdatedAt:       r.updatedAt,
	}

	switch r.kind {
	case KindLocal:
		if _, vol, verr := s.volumeFromRow(ctx, r); verr == nil {
			if r.isBuiltin {
				n.Status = StatusOK
				n.StatusError = ""
			} else if st, serr := os.Stat(cfg.Local.Path); serr != nil || !st.IsDir() {
				n.Status = StatusError
				if serr != nil {
					n.StatusError = serr.Error()
				} else {
					n.StatusError = "目标路径不是目录"
				}
			}
			if u, uerr := vol.Usage(ctx); uerr == nil && u.TotalBytes > 0 {
				n.Usage = toUsageInfo(u)
			}
		}
	case KindDocker, KindSMB:
		if r.lastStatus == StatusOK || r.isActive {
			uctx, cancel := context.WithTimeout(ctx, listUsageTimeout)
			if _, vol, verr := s.volumeFromRow(uctx, r); verr == nil {
				if u, uerr := vol.Usage(uctx); uerr == nil && u.TotalBytes > 0 {
					n.Usage = toUsageInfo(u)
				}
			}
			cancel()
		}
	}
	return n, nil
}

func toUsageInfo(u storage.Usage) *UsageInfo {
	used := u.UsedBytes
	if used == 0 && u.TotalBytes > u.FreeBytes {
		used = u.TotalBytes - u.FreeBytes
	}
	return &UsageInfo{
		TotalBytes: u.TotalBytes,
		FreeBytes:  u.FreeBytes,
		UsedBytes:  used,
	}
}

func (s *Service) queryNodeStats(ctx context.Context) (map[int64]NodeStats, error) {
	out := make(map[int64]NodeStats)

	dsRows, err := s.db.Query(ctx, `SELECT storage_node_id,
		COUNT(*) FILTER (WHERE deleted_at IS NULL),
		COUNT(*) FILTER (WHERE deleted_at IS NOT NULL AND delete_files)
		FROM datasets GROUP BY storage_node_id`)
	if err != nil {
		return nil, err
	}
	for dsRows.Next() {
		var id int64
		var live, trashed int
		if err := dsRows.Scan(&id, &live, &trashed); err != nil {
			dsRows.Close()
			return nil, err
		}
		st := out[id]
		st.DatasetCount = live
		st.TrashedDatasetCount = trashed
		out[id] = st
	}
	dsRows.Close()
	if err := dsRows.Err(); err != nil {
		return nil, err
	}

	imgRows, err := s.db.Query(ctx, `SELECT d.storage_node_id, COALESCE(SUM(di.size_bytes), 0)
		FROM dataset_images di
		JOIN datasets d ON d.id = di.dataset_id
		WHERE d.deleted_at IS NULL OR d.delete_files
		GROUP BY d.storage_node_id`)
	if err != nil {
		return nil, err
	}
	for imgRows.Next() {
		var id, bytes int64
		if err := imgRows.Scan(&id, &bytes); err != nil {
			imgRows.Close()
			return nil, err
		}
		st := out[id]
		st.StoredBytes += bytes
		out[id] = st
	}
	imgRows.Close()
	if err := imgRows.Err(); err != nil {
		return nil, err
	}

	dvRows, err := s.db.Query(ctx, `SELECT storage_node_id,
		COUNT(*) FILTER (WHERE deleted_at IS NULL),
		COUNT(*) FILTER (WHERE deleted_at IS NOT NULL),
		COALESCE(SUM(total_bytes), 0)
		FROM data_versions GROUP BY storage_node_id`)
	if err != nil {
		return nil, err
	}
	for dvRows.Next() {
		var id, bytes int64
		var live, trashed int
		if err := dvRows.Scan(&id, &live, &trashed, &bytes); err != nil {
			dvRows.Close()
			return nil, err
		}
		st := out[id]
		st.DataVersionCount = live
		st.TrashedDataVersionCount = trashed
		st.StoredBytes += bytes
		out[id] = st
	}
	dvRows.Close()
	if err := dvRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validateNodeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", newError(ErrInvalid, "存储节点名称不能为空")
	}
	if utf8.RuneCountInString(name) > 80 {
		return "", newError(ErrInvalid, "存储节点名称不能超过 80 个字符")
	}
	return name, nil
}

// TestConnection validates and probes a candidate or existing storage node.
func (s *Service) TestConnection(ctx context.Context, in TestNodeInput) (TestNodeResult, error) {
	kind := strings.TrimSpace(in.Kind)
	isBuiltin := false
	cfg := NodeConfig{Local: in.Local, Docker: in.Docker, S3: in.S3, SMB: in.SMB}
	creds := Credentials{SecretAccessKey: in.SecretAccessKey, Password: in.Password, Token: in.Token}

	if in.NodeID > 0 && s.db != nil {
		r, err := scanNodeRow(s.db.QueryRow(ctx,
			"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", in.NodeID))
		if err != nil {
			return TestNodeResult{}, err
		}
		isBuiltin = r.isBuiltin
		if kind == "" {
			kind = r.kind
		}
		storedCfg, _, err := s.parseRowConfig(r)
		if err != nil {
			return TestNodeResult{}, err
		}
		if kind == KindDocker && cfg.Docker == nil && hasNonNullJSON(in.rawConfig) {
			dc, tok, derr := decodeDockerConfigAlias(in.rawConfig)
			if derr != nil {
				return TestNodeResult{}, newError(ErrInvalid, "存储服务配置格式无效")
			}
			cfg.Docker = dc
			if creds.Token == "" && tok != "" {
				creds.Token = tok
			}
		}
		if cfg.Local == nil && cfg.Docker == nil && cfg.S3 == nil && cfg.SMB == nil {
			cfg = storedCfg
		}
		if creds.SecretAccessKey == "" && creds.Password == "" && creds.Token == "" && len(r.credentialsEnc) > 0 {
			storedCreds, err := s.cipher.decrypt(ctx, r.credentialsEnc)
			if err != nil {
				return TestNodeResult{}, wrapError(ErrUnavailable, err, "解密已保存的凭据失败")
			}
			creds = storedCreds
		}
	} else if in.NodeID == BuiltinID && s.db == nil {
		isBuiltin = true
		kind = KindLocal
	}

	vol, _, target, err := s.buildVolume(in.NodeID, isBuiltin, kind, cfg, creds)
	if err != nil {
		return TestNodeResult{}, err
	}
	defer vol.Close()

	pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	defer cancel()

	start := time.Now()
	probeErr := vol.Probe(pctx)
	latency := float64(time.Since(start).Microseconds()) / 1000.0

	if in.NodeID > 0 && s.db != nil {
		status := StatusOK
		errMsg := ""
		if probeErr != nil {
			status = StatusError
			errMsg = probeErr.Error()
		}
		_, _ = s.db.Exec(ctx, `UPDATE storage_nodes
			SET last_checked_at = now(), last_status = $2, last_error = $3
			WHERE id = $1`, in.NodeID, status, errMsg)
	}

	if probeErr != nil {
		return TestNodeResult{
			OK:            false,
			LatencyMS:     latency,
			DisplayTarget: target,
			Message:       probeErr.Error(),
		}, nil
	}

	var usage *UsageInfo
	if u, uerr := vol.Usage(pctx); uerr == nil && u.TotalBytes > 0 {
		usage = toUsageInfo(u)
	}
	return TestNodeResult{
		OK:            true,
		LatencyMS:     latency,
		DisplayTarget: target,
		Usage:         usage,
		Message:       "连接成功，目录创建与小文件读写校验通过",
	}, nil
}

// GetNode returns a single storage node by ID.
func (s *Service) GetNode(ctx context.Context, id int64) (Node, error) {
	if s.db == nil {
		if id == BuiltinID {
			nodes, err := s.ListNodes(ctx)
			if err != nil || len(nodes) == 0 {
				return Node{}, err
			}
			return nodes[0], nil
		}
		return Node{}, newError(ErrNotFound, "存储节点不存在")
	}
	r, err := scanNodeRow(s.db.QueryRow(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", id))
	if err != nil {
		return Node{}, err
	}
	statsMap, err := s.queryNodeStats(ctx)
	if err != nil {
		return Node{}, err
	}
	return s.rowToNode(ctx, r, statsMap[id])
}

// CreateNode creates a new storage node and optionally activates it.
func (s *Service) CreateNode(ctx context.Context, in CreateNodeInput) (Node, error) {
	if s.db == nil {
		return Node{}, newError(ErrUnavailable, "数据库未初始化")
	}
	name, err := validateNodeName(in.Name)
	if err != nil {
		return Node{}, err
	}
	kind := strings.TrimSpace(in.Kind)
	cfg := NodeConfig{Local: in.Local, Docker: in.Docker, S3: in.S3, SMB: in.SMB}
	creds := Credentials{SecretAccessKey: in.SecretAccessKey, Password: in.Password, Token: in.Token}

	vol, normCfg, _, err := s.buildVolume(0, false, kind, cfg, creds)
	if err != nil {
		return Node{}, err
	}
	defer vol.Close()

	if kind == KindLocal {
		if err := s.checkLocalPathUnique(ctx, normCfg.Local.Path, 0); err != nil {
			return Node{}, err
		}
	}
	if kind == KindDocker {
		if err := s.checkDockerEndpointUnique(ctx, normCfg.Docker.Endpoint, 0); err != nil {
			return Node{}, err
		}
	}

	pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	probeErr := vol.Probe(pctx)
	cancel()
	if probeErr != nil && in.Activate {
		return Node{}, wrapError(ErrConflict, probeErr, "存储节点连接或读写校验失败，无法直接设为当前使用节点")
	}

	status := StatusOK
	statusErr := ""
	if probeErr != nil {
		status = StatusError
		statusErr = probeErr.Error()
	}

	rawCfg, err := marshalProtocolConfig(kind, false, normCfg)
	if err != nil {
		return Node{}, err
	}
	encCreds, err := s.cipher.encrypt(ctx, creds)
	if err != nil {
		return Node{}, err
	}

	var inserted nodeRow
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if in.Activate {
			if _, err := tx.Exec(ctx,
				"UPDATE storage_nodes SET is_active = false, updated_at = now() WHERE is_active = true"); err != nil {
				return err
			}
		}
		var scanErr error
		inserted, scanErr = scanNodeRow(tx.QueryRow(ctx, `INSERT INTO storage_nodes
			(name, kind, is_builtin, is_active, config, credentials_enc, last_checked_at, last_status, last_error)
			VALUES ($1, $2, false, $3, $4, $5, now(), $6, $7)
			RETURNING `+nodeSelectColumns,
			name, kind, in.Activate, rawCfg, encCreds, status, statusErr))
		return scanErr
	})
	if err != nil {
		if isUniqueConstraint(err, "storage_nodes_name_key") {
			return Node{}, newError(ErrConflict, "已存在同名存储节点 %q", name)
		}
		return Node{}, err
	}
	return s.rowToNode(ctx, inserted, NodeStats{})
}

func (s *Service) checkLocalPathUnique(ctx context.Context, cleanPath string, exceptID int64) error {
	rows, err := s.db.Query(ctx,
		"SELECT id, name, config FROM storage_nodes WHERE kind = 'local' AND NOT is_builtin AND id <> $1", exceptID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		var raw []byte
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return err
		}
		var lc LocalConfig
		if json.Unmarshal(raw, &lc) == nil && filepath.Clean(lc.Path) == cleanPath {
			return newError(ErrConflict, "目录 %q 已被存储节点 %q 使用", cleanPath, name)
		}
	}
	return rows.Err()
}

func (s *Service) checkDockerEndpointUnique(ctx context.Context, cleanEndpoint string, exceptID int64) error {
	rows, err := s.db.Query(ctx,
		"SELECT id, name, config FROM storage_nodes WHERE kind = 'docker' AND id <> $1", exceptID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		var raw []byte
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return err
		}
		var dc DockerConfig
		if json.Unmarshal(raw, &dc) == nil {
			if norm, nerr := validateDockerConfig(dc); nerr == nil && strings.EqualFold(norm.Endpoint, cleanEndpoint) {
				return newError(ErrConflict, "存储服务地址 %q 已被存储节点 %q 使用", cleanEndpoint, name)
			}
		}
	}
	return rows.Err()
}

// UpdateNode updates a storage node's name, configuration, or credentials.
func (s *Service) UpdateNode(ctx context.Context, id int64, in UpdateNodeInput) (Node, error) {
	if s.db == nil {
		return Node{}, newError(ErrUnavailable, "数据库未初始化")
	}
	r, err := scanNodeRow(s.db.QueryRow(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", id))
	if err != nil {
		return Node{}, err
	}

	if r.isBuiltin {
		if in.Local != nil || in.Docker != nil || in.S3 != nil || in.SMB != nil || hasNonNullJSON(in.rawConfig) ||
			in.SecretAccessKey != nil || in.Password != nil || in.Token != nil || in.ClearSecret {
			return Node{}, newError(ErrConflict, "本机默认存储目录由系统环境变量 TM_DATA_DIR 决定，不支持修改连接参数")
		}
	}

	if !r.isBuiltin && r.kind == KindDocker && hasNonNullJSON(in.rawConfig) {
		dc, tok, derr := decodeDockerConfigAlias(in.rawConfig)
		if derr != nil {
			return Node{}, newError(ErrInvalid, "存储服务配置格式无效")
		}
		if in.Docker == nil {
			in.Docker = dc
		}
		if in.Token == nil && tok != "" {
			in.Token = &tok
		}
	}

	name := r.name
	if in.Name != nil {
		name, err = validateNodeName(*in.Name)
		if err != nil {
			return Node{}, err
		}
	}

	cfg, _, err := s.parseRowConfig(r)
	if err != nil {
		return Node{}, err
	}
	if !r.isBuiltin {
		switch r.kind {
		case KindLocal:
			if in.Docker != nil || in.S3 != nil || in.SMB != nil {
				return Node{}, newError(ErrInvalid, "不支持修改已创建存储节点的协议类型")
			}
			if in.Local != nil {
				cfg.Local = in.Local
			}
		case KindDocker:
			if in.Local != nil || in.S3 != nil || in.SMB != nil {
				return Node{}, newError(ErrInvalid, "不支持修改已创建存储节点的协议类型")
			}
			if in.Docker != nil {
				cfg.Docker = in.Docker
			}
		case KindS3:
			if in.Local != nil || in.Docker != nil || in.SMB != nil {
				return Node{}, newError(ErrInvalid, "不支持修改已创建存储节点的协议类型")
			}
			if in.S3 != nil {
				cfg.S3 = in.S3
			}
		case KindSMB:
			if in.Local != nil || in.Docker != nil || in.S3 != nil {
				return Node{}, newError(ErrInvalid, "不支持修改已创建存储节点的协议类型")
			}
			if in.SMB != nil {
				cfg.SMB = in.SMB
			}
		}
	}

	creds, err := s.cipher.decrypt(ctx, r.credentialsEnc)
	if err != nil && !in.ClearSecret {
		// Allow replacing broken credentials if caller explicitly sends a new secret.
		hasReplacement := (in.SecretAccessKey != nil && *in.SecretAccessKey != "") ||
			(in.Password != nil && *in.Password != "") ||
			(in.Token != nil && *in.Token != "")
		if !hasReplacement {
			return Node{}, wrapError(ErrUnavailable, err, "读取原存储节点密钥失败，请重新填写密钥保存")
		}
		creds = Credentials{}
	}
	if in.ClearSecret {
		creds = Credentials{}
	}
	if in.SecretAccessKey != nil && *in.SecretAccessKey != "" {
		creds.SecretAccessKey = *in.SecretAccessKey
	}
	if in.Password != nil && *in.Password != "" {
		creds.Password = *in.Password
	}
	if in.Token != nil && *in.Token != "" {
		creds.Token = *in.Token
	}

	vol, normCfg, _, err := s.buildVolume(r.id, r.isBuiltin, r.kind, cfg, creds)
	if err != nil {
		return Node{}, err
	}
	defer vol.Close()

	if r.kind == KindLocal && !r.isBuiltin {
		if err := s.checkLocalPathUnique(ctx, normCfg.Local.Path, r.id); err != nil {
			return Node{}, err
		}
	}
	if r.kind == KindDocker {
		if err := s.checkDockerEndpointUnique(ctx, normCfg.Docker.Endpoint, r.id); err != nil {
			return Node{}, err
		}
	}

	pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	probeErr := vol.Probe(pctx)
	cancel()
	if probeErr != nil && r.isActive {
		return Node{}, wrapError(ErrConflict, probeErr, "当前使用中的存储节点修改后无法连通或写入，已拒绝保存")
	}

	status := StatusOK
	statusErr := ""
	if probeErr != nil {
		status = StatusError
		statusErr = probeErr.Error()
	}

	rawCfg, err := marshalProtocolConfig(r.kind, r.isBuiltin, normCfg)
	if err != nil {
		return Node{}, err
	}
	encCreds, err := s.cipher.encrypt(ctx, creds)
	if err != nil {
		return Node{}, err
	}

	updated, err := scanNodeRow(s.db.QueryRow(ctx, `UPDATE storage_nodes
		SET name = $2, config = $3, credentials_enc = $4,
			last_checked_at = now(), last_status = $5, last_error = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+nodeSelectColumns,
		r.id, name, rawCfg, encCreds, status, statusErr))
	if err != nil {
		if isUniqueConstraint(err, "storage_nodes_name_key") {
			return Node{}, newError(ErrConflict, "已存在同名存储节点 %q", name)
		}
		return Node{}, err
	}

	s.evictCachedVolume(r.id)
	statsMap, err := s.queryNodeStats(ctx)
	if err != nil {
		return Node{}, err
	}
	return s.rowToNode(ctx, updated, statsMap[r.id])
}

// ActivateNode probes id and atomically marks it as the single active storage node.
func (s *Service) ActivateNode(ctx context.Context, id int64) (Node, error) {
	if s.db == nil {
		return Node{}, newError(ErrUnavailable, "数据库未初始化")
	}
	r, err := scanNodeRow(s.db.QueryRow(ctx,
		"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1", id))
	if err != nil {
		return Node{}, err
	}
	_, vol, err := s.volumeFromRow(ctx, r)
	if err != nil {
		return Node{}, err
	}

	pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	probeErr := vol.Probe(pctx)
	cancel()
	if probeErr != nil {
		_, _ = s.db.Exec(ctx, `UPDATE storage_nodes
			SET last_checked_at = now(), last_status = 'error', last_error = $2
			WHERE id = $1`, id, probeErr.Error())
		return Node{}, wrapError(ErrConflict, probeErr, "目标存储节点无法连通或不可写，无法切换为当前节点")
	}

	var updated nodeRow
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			"UPDATE storage_nodes SET is_active = false, updated_at = now() WHERE is_active = true AND id <> $1", id); err != nil {
			return err
		}
		var scanErr error
		updated, scanErr = scanNodeRow(tx.QueryRow(ctx, `UPDATE storage_nodes
			SET is_active = true, last_checked_at = now(), last_status = 'ok', last_error = '', updated_at = now()
			WHERE id = $1
			RETURNING `+nodeSelectColumns, id))
		return scanErr
	})
	if err != nil {
		return Node{}, err
	}

	statsMap, err := s.queryNodeStats(ctx)
	if err != nil {
		return Node{}, err
	}
	return s.rowToNode(ctx, updated, statsMap[id])
}

// DeleteNode deletes an unused, inactive, non-builtin storage node.
func (s *Service) DeleteNode(ctx context.Context, id int64) error {
	if s.db == nil {
		return newError(ErrUnavailable, "数据库未初始化")
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		r, err := scanNodeRow(tx.QueryRow(ctx,
			"SELECT "+nodeSelectColumns+" FROM storage_nodes WHERE id = $1 FOR UPDATE", id))
		if err != nil {
			return err
		}
		if r.isBuiltin {
			return newError(ErrConflict, "系统内置的本机默认存储节点不可删除")
		}
		if r.isActive {
			return newError(ErrConflict, "该存储节点当前正在使用中，请先切换到其他存储节点后再删除")
		}
		var dsCount, dvCount, migCount int
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM datasets WHERE storage_node_id = $1", id).Scan(&dsCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM data_versions WHERE storage_node_id = $1", id).Scan(&dvCount); err != nil {
			return err
		}
		if dsCount > 0 || dvCount > 0 {
			return newError(ErrConflict,
				"该存储节点仍有关联的数据集（%d 个）或数据版本（%d 个，含回收站），请先迁移或彻底清理后再删除",
				dsCount, dvCount)
		}
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM storage_migrations
			WHERE (from_node_id = $1 OR to_node_id = $1) AND status IN ('queued', 'running')`, id).Scan(&migCount); err != nil {
			return err
		}
		if migCount > 0 {
			return newError(ErrConflict, "该存储节点存在正在执行的迁移任务，请等待完成或取消后再删除")
		}
		_, err = tx.Exec(ctx, "DELETE FROM storage_nodes WHERE id = $1", id)
		return err
	})
	if err != nil {
		return err
	}
	s.evictCachedVolume(id)
	return nil
}

// GetTrainerCachePolicy loads the global trainer dataset cache policy.
func (s *Service) GetTrainerCachePolicy(ctx context.Context) (TrainerCachePolicy, error) {
	def := DefaultTrainerCachePolicy()
	if s.db == nil {
		return def, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, "SELECT value FROM app_settings WHERE key = $1", trainerCacheSetting).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	var p TrainerCachePolicy
	if err := json.Unmarshal(raw, &p); err != nil {
		return def, nil
	}
	if p.MaxVersions < 1 {
		p.MaxVersions = def.MaxVersions
	}
	if p.TTLHours < 1 {
		p.TTLHours = def.TTLHours
	}
	return p, nil
}

// UpdateTrainerCachePolicy validates and persists the global trainer dataset cache policy.
func (s *Service) UpdateTrainerCachePolicy(ctx context.Context, in TrainerCachePolicy) (TrainerCachePolicy, error) {
	if in.MaxVersions < 1 || in.MaxVersions > 50 {
		return TrainerCachePolicy{}, newError(ErrInvalid, "最多保留数据版本数需在 1 到 50 之间")
	}
	if in.TTLHours < 1 || in.TTLHours > 720 {
		return TrainerCachePolicy{}, newError(ErrInvalid, "缓存最长保留时间需在 1 到 720 小时之间")
	}
	if s.db == nil {
		return in, nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return TrainerCachePolicy{}, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		trainerCacheSetting, raw)
	if err != nil {
		return TrainerCachePolicy{}, err
	}
	return in, nil
}

func isUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
