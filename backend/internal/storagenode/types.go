package storagenode

import "time"

// Storage node protocol kinds.
const (
	KindLocal = "local"
	KindS3    = "s3"
	KindSMB   = "smb"
)

// Managed logical namespaces stored on a storage node.
const (
	SpaceDatasets = "datasets"
	SpaceExports  = "exports"
)

// Built-in local storage node constants (seeded by 0017_storage_nodes.sql).
const (
	BuiltinID   int64 = 1
	BuiltinName       = "本机默认存储"
)

// Connectivity check statuses.
const (
	StatusUnknown = "unknown"
	StatusOK      = "ok"
	StatusError   = "error"
)

// Migration target types and statuses.
const (
	TargetDataset     = "dataset"
	TargetDataVersion = "data_version"

	MigrationQueued    = "queued"
	MigrationRunning   = "running"
	MigrationSucceeded = "succeeded"
	MigrationFailed    = "failed"
	MigrationCanceled  = "canceled"
)

// NodeRef is the compact storage node reference embedded in Dataset and
// DataVersion API responses.
type NodeRef struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	IsBuiltin bool   `json:"is_builtin"`
}

// BuiltinRef returns the default NodeRef for the built-in local storage node.
func BuiltinRef() NodeRef {
	return NodeRef{ID: BuiltinID, Name: BuiltinName, Kind: KindLocal, IsBuiltin: true}
}

// LocalConfig holds configuration for a local directory storage node.
// For the built-in node, Path is resolved dynamically from TM_DATA_DIR.
type LocalConfig struct {
	Path string `json:"path"`
}

// S3Config holds non-secret configuration for an S3-compatible storage node.
type S3Config struct {
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	Bucket             string `json:"bucket"`
	Prefix             string `json:"prefix"`
	AccessKeyID        string `json:"access_key_id"`
	ForcePathStyle     bool   `json:"force_path_style"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

// SMBConfig holds non-secret configuration for an SMB/CIFS shared folder node.
type SMBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Share    string `json:"share"`
	BasePath string `json:"base_path"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
}

// NodeConfig holds protocol-specific public parameters.
type NodeConfig struct {
	Local *LocalConfig `json:"local,omitempty"`
	S3    *S3Config    `json:"s3,omitempty"`
	SMB   *SMBConfig   `json:"smb,omitempty"`
}

// Credentials holds secret fields encrypted at rest in credentials_enc and
// never serialized in API responses.
type Credentials struct {
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Password        string `json:"password,omitempty"`
}

// UsageInfo reports filesystem capacity when supported (Local and SMB).
type UsageInfo struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// NodeStats summarizes datasets and data versions stored on a node.
type NodeStats struct {
	DatasetCount            int   `json:"dataset_count"`
	TrashedDatasetCount     int   `json:"trashed_dataset_count"`
	DataVersionCount        int   `json:"data_version_count"`
	TrashedDataVersionCount int   `json:"trashed_data_version_count"`
	StoredBytes             int64 `json:"stored_bytes"`
}

// Node is a storage node returned by the management API.
type Node struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	IsBuiltin     bool       `json:"is_builtin"`
	IsActive      bool       `json:"is_active"`
	HasSecret     bool       `json:"has_secret"`
	DisplayTarget string     `json:"display_target"`
	Config        NodeConfig `json:"config"`
	Status        string     `json:"status"`
	StatusError   string     `json:"status_error,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	Usage         *UsageInfo `json:"usage,omitempty"`
	Stats         NodeStats  `json:"stats"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Ref returns the compact NodeRef of n.
func (n Node) Ref() NodeRef {
	return NodeRef{ID: n.ID, Name: n.Name, Kind: n.Kind, IsBuiltin: n.IsBuiltin}
}

// CreateNodeInput holds parameters for creating a storage node.
type CreateNodeInput struct {
	Name            string       `json:"name"`
	Kind            string       `json:"kind"`
	Local           *LocalConfig `json:"local,omitempty"`
	S3              *S3Config    `json:"s3,omitempty"`
	SMB             *SMBConfig   `json:"smb,omitempty"`
	SecretAccessKey string       `json:"secret_access_key,omitempty"`
	Password        string       `json:"password,omitempty"`
	Activate        bool         `json:"activate"`
}

// UpdateNodeInput holds parameters for updating an existing storage node.
// Empty SecretAccessKey / Password preserves the previously saved secret unless
// ClearSecret is true.
type UpdateNodeInput struct {
	Name            *string      `json:"name,omitempty"`
	Local           *LocalConfig `json:"local,omitempty"`
	S3              *S3Config    `json:"s3,omitempty"`
	SMB             *SMBConfig   `json:"smb,omitempty"`
	SecretAccessKey *string      `json:"secret_access_key,omitempty"`
	Password        *string      `json:"password,omitempty"`
	ClearSecret     bool         `json:"clear_secret,omitempty"`
}

// TestNodeInput holds parameters for probing a candidate or existing node.
// When NodeID > 0 and no new secret is sent, the stored encrypted credentials
// of NodeID are reused.
type TestNodeInput struct {
	NodeID          int64        `json:"node_id,omitempty"`
	Kind            string       `json:"kind"`
	Local           *LocalConfig `json:"local,omitempty"`
	S3              *S3Config    `json:"s3,omitempty"`
	SMB             *SMBConfig   `json:"smb,omitempty"`
	SecretAccessKey string       `json:"secret_access_key,omitempty"`
	Password        string       `json:"password,omitempty"`
}

// TestNodeResult reports the outcome of a connection & write probe.
type TestNodeResult struct {
	OK            bool       `json:"ok"`
	LatencyMS     float64    `json:"latency_ms"`
	DisplayTarget string     `json:"display_target"`
	Usage         *UsageInfo `json:"usage,omitempty"`
	Message       string     `json:"message"`
}

// TrainerCachePolicy configures optional short-term dataset caching on compute
// nodes. When Enabled is false (default), training work directories are deleted
// immediately when each training job ends.
type TrainerCachePolicy struct {
	Enabled     bool `json:"enabled"`
	MaxVersions int  `json:"max_versions"`
	TTLHours    int  `json:"ttl_hours"`
}

// TrainerCacheTaskPayload is the normalized cache policy sent to trainer nodes
// in training job tasks and heartbeat responses.
type TrainerCacheTaskPayload struct {
	Enabled     bool `json:"enabled"`
	MaxVersions int  `json:"max_versions"`
	TTLSeconds  int  `json:"ttl_seconds"`
}

// DefaultTrainerCachePolicy returns the default policy (disabled, immediate cleanup).
func DefaultTrainerCachePolicy() TrainerCachePolicy {
	return TrainerCachePolicy{
		Enabled:     false,
		MaxVersions: 2,
		TTLHours:    24,
	}
}

// TaskPayload converts the policy into the agent wire payload.
func (p TrainerCachePolicy) TaskPayload() TrainerCacheTaskPayload {
	mv := p.MaxVersions
	if mv < 1 {
		mv = 2
	}
	ttl := p.TTLHours
	if ttl < 1 {
		ttl = 24
	}
	return TrainerCacheTaskPayload{
		Enabled:     p.Enabled,
		MaxVersions: mv,
		TTLSeconds:  ttl * 3600,
	}
}

// MigratableItem describes a dataset or ready data version residing on a
// storage node other than the currently active node.
type MigratableItem struct {
	TargetType  string    `json:"target_type"` // "dataset" or "data_version"
	TargetID    int64     `json:"target_id"`
	Name        string    `json:"name"`
	ProjectName string    `json:"project_name,omitempty"`
	DirName     string    `json:"dir_name"`
	ImageCount  int       `json:"image_count"`
	TotalBytes  int64     `json:"total_bytes"`
	StorageNode NodeRef   `json:"storage_node"`
	CreatedAt   time.Time `json:"created_at"`
}

// MigrationTarget identifies one dataset or data version to migrate.
type MigrationTarget struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
}

// CreateMigrationsInput requests migrating one or more items to the active node.
type CreateMigrationsInput struct {
	Targets      []MigrationTarget `json:"targets"`
	DeleteSource *bool             `json:"delete_source,omitempty"` // nil defaults to true
}

// Migration is a background storage migration task record.
type Migration struct {
	ID           int64      `json:"id"`
	TargetType   string     `json:"target_type"`
	TargetID     int64      `json:"target_id"`
	TargetName   string     `json:"target_name"`
	FromNode     NodeRef    `json:"from_node"`
	ToNode       NodeRef    `json:"to_node"`
	DeleteSource bool       `json:"delete_source"`
	Status       string     `json:"status"`
	FilesDone    int        `json:"files_done"`
	FilesTotal   int        `json:"files_total"`
	BytesDone    int64      `json:"bytes_done"`
	BytesTotal   int64      `json:"bytes_total"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
