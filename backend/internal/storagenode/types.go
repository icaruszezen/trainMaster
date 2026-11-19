package storagenode

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// Storage node protocol kinds.
const (
	KindLocal  = "local"
	KindDocker = "docker"
	KindS3     = "s3"
	KindSMB    = "smb"
)

// Transfer modes and archive migration stages.
const (
	TransferModeDirect  = "direct"
	TransferModeArchive = "archive"

	MigrationStagePacking      = "packing"
	MigrationStageTransferring = "transferring"
	MigrationStageExtracting   = "extracting"
	MigrationStageVerifying    = "verifying"
	MigrationStageCleaning     = "cleaning"
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
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	IsBuiltin       bool   `json:"is_builtin"`
	SupportsArchive bool   `json:"supports_archive"`
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

// DockerConfig holds non-secret configuration for a trainMaster Docker storage node.
type DockerConfig struct {
	Endpoint           string `json:"endpoint"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
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
	Local  *LocalConfig  `json:"local,omitempty"`
	Docker *DockerConfig `json:"docker,omitempty"`
	S3     *S3Config     `json:"s3,omitempty"`
	SMB    *SMBConfig    `json:"smb,omitempty"`
}

// MarshalJSON emits both nested "docker": {...} and flat "endpoint" /
// "insecure_skip_verify" fields when Docker != nil so both nested and flat API
// consumers work seamlessly while leaving Local/S3/SMB untouched.
func (c NodeConfig) MarshalJSON() ([]byte, error) {
	if c.Docker != nil && c.Local == nil && c.S3 == nil && c.SMB == nil {
		return json.Marshal(struct {
			Docker             *DockerConfig `json:"docker"`
			Endpoint           string        `json:"endpoint"`
			InsecureSkipVerify bool          `json:"insecure_skip_verify"`
		}{
			Docker:             c.Docker,
			Endpoint:           c.Docker.Endpoint,
			InsecureSkipVerify: c.Docker.InsecureSkipVerify,
		})
	}
	type plain NodeConfig
	return json.Marshal(plain(c))
}

// Credentials holds secret fields encrypted at rest in credentials_enc and
// never serialized in API responses.
type Credentials struct {
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Password        string `json:"password,omitempty"`
	Token           string `json:"token,omitempty"`
}

// UsageInfo reports filesystem capacity when supported (Local, Docker, and SMB).
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
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	IsBuiltin       bool       `json:"is_builtin"`
	IsActive        bool       `json:"is_active"`
	SupportsArchive bool       `json:"supports_archive"`
	HasSecret       bool       `json:"has_secret"`
	DisplayTarget   string     `json:"display_target"`
	Config          NodeConfig `json:"config"`
	Status          string     `json:"status"`
	StatusError     string     `json:"status_error,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	Usage           *UsageInfo `json:"usage,omitempty"`
	Stats           NodeStats  `json:"stats"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Ref returns the compact NodeRef of n.
func (n Node) Ref() NodeRef {
	return NodeRef{
		ID:              n.ID,
		Name:            n.Name,
		Kind:            n.Kind,
		IsBuiltin:       n.IsBuiltin,
		SupportsArchive: n.SupportsArchive,
	}
}

type rawDockerConfigWithToken struct {
	Endpoint           string        `json:"endpoint"`
	InsecureSkipVerify bool          `json:"insecure_skip_verify"`
	Token              string        `json:"token"`
	Docker             *DockerConfig `json:"docker"`
}

// CreateNodeInput holds parameters for creating a storage node.
type CreateNodeInput struct {
	Name            string        `json:"name"`
	Kind            string        `json:"kind"`
	Local           *LocalConfig  `json:"local,omitempty"`
	Docker          *DockerConfig `json:"docker,omitempty"`
	S3              *S3Config     `json:"s3,omitempty"`
	SMB             *SMBConfig    `json:"smb,omitempty"`
	SecretAccessKey string        `json:"secret_access_key,omitempty"`
	Password        string        `json:"password,omitempty"`
	Token           string        `json:"token,omitempty"`
	Activate        bool          `json:"activate"`
}

// UnmarshalJSON accepts both "docker": {...} and "config": {...} (as well as
// top-level or nested "token") for KindDocker inputs.
func (in *CreateNodeInput) UnmarshalJSON(data []byte) error {
	type alias CreateNodeInput
	var aux struct {
		alias
		ConfigRaw   json.RawMessage           `json:"config"`
		DockerRaw   *rawDockerConfigWithToken `json:"docker"`
		Credentials *Credentials              `json:"credentials"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*in = CreateNodeInput(aux.alias)
	if aux.DockerRaw != nil {
		in.Docker = &DockerConfig{
			Endpoint:           aux.DockerRaw.Endpoint,
			InsecureSkipVerify: aux.DockerRaw.InsecureSkipVerify,
		}
		if in.Token == "" && aux.DockerRaw.Token != "" {
			in.Token = aux.DockerRaw.Token
		}
	}
	if strings.TrimSpace(in.Kind) == KindDocker && hasNonNullJSON(aux.ConfigRaw) {
		dc, tok, err := decodeDockerConfigAlias(aux.ConfigRaw)
		if err != nil {
			return err
		}
		if in.Docker == nil {
			in.Docker = dc
		}
		if in.Token == "" && tok != "" {
			in.Token = tok
		}
	}
	if in.Token == "" && aux.Credentials != nil && aux.Credentials.Token != "" {
		in.Token = aux.Credentials.Token
	}
	return nil
}

// UpdateNodeInput holds parameters for updating an existing storage node.
// Empty SecretAccessKey / Password / Token preserves the previously saved
// secret unless ClearSecret is true.
type UpdateNodeInput struct {
	Name            *string       `json:"name,omitempty"`
	Local           *LocalConfig  `json:"local,omitempty"`
	Docker          *DockerConfig `json:"docker,omitempty"`
	S3              *S3Config     `json:"s3,omitempty"`
	SMB             *SMBConfig    `json:"smb,omitempty"`
	SecretAccessKey *string       `json:"secret_access_key,omitempty"`
	Password        *string       `json:"password,omitempty"`
	Token           *string       `json:"token,omitempty"`
	ClearSecret     bool          `json:"clear_secret,omitempty"`

	rawConfig json.RawMessage
}

// UnmarshalJSON accepts both "docker": {...} and "config": {...} on PATCH,
// deferring "config" alias resolution until UpdateNode knows the target node's kind.
func (in *UpdateNodeInput) UnmarshalJSON(data []byte) error {
	type alias UpdateNodeInput
	var aux struct {
		alias
		ConfigRaw   json.RawMessage           `json:"config"`
		DockerRaw   *rawDockerConfigWithToken `json:"docker"`
		Credentials *Credentials              `json:"credentials"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*in = UpdateNodeInput(aux.alias)
	in.rawConfig = append(json.RawMessage(nil), aux.ConfigRaw...)
	if aux.DockerRaw != nil {
		in.Docker = &DockerConfig{
			Endpoint:           aux.DockerRaw.Endpoint,
			InsecureSkipVerify: aux.DockerRaw.InsecureSkipVerify,
		}
		if in.Token == nil && aux.DockerRaw.Token != "" {
			tok := aux.DockerRaw.Token
			in.Token = &tok
		}
	}
	if in.Token == nil && aux.Credentials != nil && aux.Credentials.Token != "" {
		tok := aux.Credentials.Token
		in.Token = &tok
	}
	return nil
}

// TestNodeInput holds parameters for probing a candidate or existing node.
// When NodeID > 0 and no new secret is sent, the stored encrypted credentials
// of NodeID are reused.
type TestNodeInput struct {
	NodeID          int64         `json:"node_id,omitempty"`
	Kind            string        `json:"kind"`
	Local           *LocalConfig  `json:"local,omitempty"`
	Docker          *DockerConfig `json:"docker,omitempty"`
	S3              *S3Config     `json:"s3,omitempty"`
	SMB             *SMBConfig    `json:"smb,omitempty"`
	SecretAccessKey string        `json:"secret_access_key,omitempty"`
	Password        string        `json:"password,omitempty"`
	Token           string        `json:"token,omitempty"`

	rawConfig json.RawMessage
}

// UnmarshalJSON accepts both "docker": {...} and "config": {...} on test probe.
func (in *TestNodeInput) UnmarshalJSON(data []byte) error {
	type alias TestNodeInput
	var aux struct {
		alias
		ConfigRaw   json.RawMessage           `json:"config"`
		DockerRaw   *rawDockerConfigWithToken `json:"docker"`
		Credentials *Credentials              `json:"credentials"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*in = TestNodeInput(aux.alias)
	in.rawConfig = append(json.RawMessage(nil), aux.ConfigRaw...)
	if aux.DockerRaw != nil {
		in.Docker = &DockerConfig{
			Endpoint:           aux.DockerRaw.Endpoint,
			InsecureSkipVerify: aux.DockerRaw.InsecureSkipVerify,
		}
		if in.Token == "" && aux.DockerRaw.Token != "" {
			in.Token = aux.DockerRaw.Token
		}
	}
	if strings.TrimSpace(in.Kind) == KindDocker && hasNonNullJSON(in.rawConfig) {
		dc, tok, err := decodeDockerConfigAlias(in.rawConfig)
		if err != nil {
			return err
		}
		if in.Docker == nil {
			in.Docker = dc
		}
		if in.Token == "" && tok != "" {
			in.Token = tok
		}
	}
	if in.Token == "" && aux.Credentials != nil && aux.Credentials.Token != "" {
		in.Token = aux.Credentials.Token
	}
	return nil
}

func hasNonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func decodeDockerConfigAlias(raw json.RawMessage) (*DockerConfig, string, error) {
	var cfgRaw rawDockerConfigWithToken
	if err := json.Unmarshal(raw, &cfgRaw); err != nil {
		return nil, "", err
	}
	if cfgRaw.Docker != nil {
		return &DockerConfig{
			Endpoint:           cfgRaw.Docker.Endpoint,
			InsecureSkipVerify: cfgRaw.Docker.InsecureSkipVerify,
		}, cfgRaw.Token, nil
	}
	return &DockerConfig{
		Endpoint:           cfgRaw.Endpoint,
		InsecureSkipVerify: cfgRaw.InsecureSkipVerify,
	}, cfgRaw.Token, nil
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
	TransferMode string     `json:"transfer_mode"`
	Stage        string     `json:"stage"`
	ArchiveBytes int64      `json:"archive_bytes"`
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
