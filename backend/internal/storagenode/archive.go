package storagenode

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storageagent"
	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
)

// ErrArchiveUnsupported indicates that a storage node does not support bulk
// archive pack/unpack operations (e.g. legacy S3, SMB, or passive Local nodes).
var ErrArchiveUnsupported = errors.New("storagenode: bulk archive operations not supported on this node")

// PackRequest configures a bulk archive pack operation on an ArchiveEndpoint.
type PackRequest struct {
	Space  string
	DirRel string
	Format string
	Paths  []string
}

// RemoteArchive describes a packed temporary archive ready to stream or pull.
type RemoteArchive struct {
	ArchiveID    string
	Format       string
	ArchiveBytes int64
	SHA256       string
	FilesCount   int
	RawBytes     int64
	ExpiresAt    time.Time
}

// UnpackStreamRequest configures streaming an archive into an ArchiveEndpoint.
type UnpackStreamRequest struct {
	Space                string
	DirRel               string
	Format               string
	Mode                 string
	ExpectedSHA256       string
	ExpectedArchiveBytes int64
	DeclaredRawBytes     *int64
	IncludeModTimes      bool
}

// PullUnpackParams configures a peer-to-peer HTTP pull-and-unpack between two
// Docker storage containers.
type PullUnpackParams struct {
	SourceURL            string
	SourceToken          string
	InsecureSkipVerify   bool
	ExpectedSHA256       string
	ExpectedArchiveBytes int64
	ExpectedRawBytes     *int64
	Space                string
	DirRel               string
	Format               string
	Mode                 string
	IncludeModTimes      bool
}

// UnpackSummary reports the unpacked file and byte counts plus optional mtimes.
type UnpackSummary struct {
	FilesCount int
	RawBytes   int64
	ModTimes   map[string]time.Time
}

// ArchiveEndpoint abstracts bulk archive pack, stream, delete, and unpack for
// KindDocker storage containers and the Builtin local storage node.
type ArchiveEndpoint interface {
	PackArchive(ctx context.Context, req PackRequest) (RemoteArchive, error)
	OpenArchiveStream(ctx context.Context, archiveID string) (io.ReadCloser, error)
	DeleteArchive(ctx context.Context, archiveID string) error
	UnpackArchiveStream(ctx context.Context, req UnpackStreamRequest, body io.Reader) (UnpackSummary, error)
	// ArchiveDownloadURL returns the direct HTTP(S) URL for archiveID when
	// peer-to-peer pull is supported, or "" for non-HTTP local fallback.
	ArchiveDownloadURL(archiveID string) string
	BearerToken() string
	InsecureSkipVerify() bool
	SupportsPullUnpack() bool
	PullUnpackArchive(ctx context.Context, req PullUnpackParams) (UnpackSummary, error)
}

// --- DockerVolume ArchiveEndpoint implementation ---

func (v *DockerVolume) SupportsPullUnpack() bool { return true }

func (v *DockerVolume) ArchiveDownloadURL(archiveID string) string {
	if archiveID == "" {
		return ""
	}
	return v.endpoint + "/api/v1/archives/" + url.PathEscape(archiveID)
}

func (v *DockerVolume) PackArchive(ctx context.Context, req PackRequest) (RemoteArchive, error) {
	var resp storageagent.PackArchiveResponse
	_, err := v.doJSON(ctx, v.dataClient, http.MethodPost, "/api/v1/archives/pack", storageagent.PackArchiveRequest{
		Space:  req.Space,
		Dir:    req.DirRel,
		Format: req.Format,
		Paths:  req.Paths,
	}, &resp)
	if err != nil {
		return RemoteArchive{}, err
	}
	return RemoteArchive{
		ArchiveID:    resp.ArchiveID,
		Format:       resp.Format,
		ArchiveBytes: resp.ArchiveBytes,
		SHA256:       resp.SHA256,
		FilesCount:   resp.FilesCount,
		RawBytes:     resp.RawBytes,
		ExpiresAt:    resp.ExpiresAt,
	}, nil
}

func (v *DockerVolume) OpenArchiveStream(ctx context.Context, archiveID string) (io.ReadCloser, error) {
	req, err := v.newReq(ctx, http.MethodGet, "/api/v1/archives/"+url.PathEscape(archiveID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.dataClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := decodeAgentError(resp)
		_ = resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func (v *DockerVolume) DeleteArchive(ctx context.Context, archiveID string) error {
	if archiveID == "" {
		return nil
	}
	_, err := v.doJSON(ctx, v.metaClient, http.MethodDelete, "/api/v1/archives/"+url.PathEscape(archiveID), nil, nil)
	return err
}

func (v *DockerVolume) UnpackArchiveStream(ctx context.Context, req UnpackStreamRequest, body io.Reader) (UnpackSummary, error) {
	q := url.Values{
		"space":             {req.Space},
		"dir":               {req.DirRel},
		"format":            {req.Format},
		"mode":              {req.Mode},
		"include_mod_times": {strconv.FormatBool(req.IncludeModTimes)},
	}
	httpReq, err := v.newReq(ctx, http.MethodPost, "/api/v1/archives/unpack?"+q.Encode(), body)
	if err != nil {
		return UnpackSummary{}, err
	}
	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpReq.Header.Set("X-Archive-Sha256", req.ExpectedSHA256)
	if req.DeclaredRawBytes != nil {
		httpReq.Header.Set("X-Archive-Raw-Bytes", strconv.FormatInt(*req.DeclaredRawBytes, 10))
	}
	if req.ExpectedArchiveBytes > 0 {
		httpReq.ContentLength = req.ExpectedArchiveBytes
	}

	resp, err := v.dataClient.Do(httpReq)
	if err != nil {
		return UnpackSummary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return UnpackSummary{}, decodeAgentError(resp)
	}
	var out storageagent.UnpackArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UnpackSummary{}, err
	}
	return UnpackSummary{
		FilesCount: out.FilesCount,
		RawBytes:   out.RawBytes,
		ModTimes:   out.ModTimes,
	}, nil
}

func (v *DockerVolume) PullUnpackArchive(ctx context.Context, req PullUnpackParams) (UnpackSummary, error) {
	var out storageagent.UnpackArchiveResponse
	_, err := v.doJSON(ctx, v.dataClient, http.MethodPost, "/api/v1/archives/pull-unpack", storageagent.PullUnpackRequest{
		SourceURL:            req.SourceURL,
		SourceToken:          req.SourceToken,
		InsecureSkipVerify:   req.InsecureSkipVerify,
		ExpectedSHA256:       req.ExpectedSHA256,
		ExpectedArchiveBytes: req.ExpectedArchiveBytes,
		ExpectedRawBytes:     req.ExpectedRawBytes,
		Space:                req.Space,
		Dir:                  req.DirRel,
		Format:               req.Format,
		Mode:                 req.Mode,
		IncludeModTimes:      req.IncludeModTimes,
	}, &out)
	if err != nil {
		return UnpackSummary{}, err
	}
	return UnpackSummary{
		FilesCount: out.FilesCount,
		RawBytes:   out.RawBytes,
		ModTimes:   out.ModTimes,
	}, nil
}

// --- Builtin local fallback ArchiveEndpoint (when TM_STORAGE_SERVICE_URL == "") ---

type builtinLocalArchiveEndpoint struct {
	dataRoot string
	tmpDir   string
}

func newBuiltinLocalArchiveEndpoint(dataRoot string) *builtinLocalArchiveEndpoint {
	return &builtinLocalArchiveEndpoint{
		dataRoot: dataRoot,
		tmpDir:   filepath.Join(dataRoot, storageagent.TmpArchivesDir),
	}
}

func (b *builtinLocalArchiveEndpoint) ArchiveDownloadURL(string) string { return "" }
func (b *builtinLocalArchiveEndpoint) BearerToken() string              { return "" }
func (b *builtinLocalArchiveEndpoint) InsecureSkipVerify() bool         { return false }
func (b *builtinLocalArchiveEndpoint) SupportsPullUnpack() bool         { return false }

func (b *builtinLocalArchiveEndpoint) PullUnpackArchive(context.Context, PullUnpackParams) (UnpackSummary, error) {
	return UnpackSummary{}, ErrArchiveUnsupported
}

func (b *builtinLocalArchiveEndpoint) PackArchive(ctx context.Context, req PackRequest) (RemoteArchive, error) {
	if !validSpace(req.Space) {
		return RemoteArchive{}, fmt.Errorf("unknown space %q", req.Space)
	}
	cleanDir, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(req.DirRel), "/"))
	if err != nil {
		return RemoteArchive{}, err
	}
	format, err := storagearchive.NormalizeFormat(req.Format)
	if err != nil {
		return RemoteArchive{}, err
	}
	if err := os.MkdirAll(b.tmpDir, 0o755); err != nil {
		return RemoteArchive{}, err
	}
	id := "localpack-" + strings.ToLower(rand.Text()[:20])
	destPath := filepath.Join(b.tmpDir, id+storagearchive.FormatExtension(format))
	zeroHeadroom := uint64(0)
	res, err := storagearchive.Pack(ctx, storagearchive.PackOptions{
		SourceDir:       filepath.Join(b.dataRoot, req.Space, filepath.FromSlash(cleanDir)),
		DestArchivePath: destPath,
		Format:          format,
		Paths:           req.Paths,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		return RemoteArchive{}, err
	}
	return RemoteArchive{
		ArchiveID:    destPath,
		Format:       res.Format,
		ArchiveBytes: res.ArchiveBytes,
		SHA256:       res.SHA256,
		FilesCount:   res.FilesCount,
		RawBytes:     res.RawBytes,
		ExpiresAt:    time.Now().UTC().Add(2 * time.Hour),
	}, nil
}

func (b *builtinLocalArchiveEndpoint) OpenArchiveStream(_ context.Context, archiveID string) (io.ReadCloser, error) {
	return os.Open(archiveID)
}

func (b *builtinLocalArchiveEndpoint) DeleteArchive(_ context.Context, archiveID string) error {
	if archiveID == "" {
		return nil
	}
	if err := os.Remove(archiveID); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *builtinLocalArchiveEndpoint) UnpackArchiveStream(ctx context.Context, req UnpackStreamRequest, body io.Reader) (UnpackSummary, error) {
	if !validSpace(req.Space) {
		return UnpackSummary{}, fmt.Errorf("unknown space %q", req.Space)
	}
	format, err := storagearchive.NormalizeFormat(req.Format)
	if err != nil {
		return UnpackSummary{}, err
	}
	if err := os.MkdirAll(b.tmpDir, 0o755); err != nil {
		return UnpackSummary{}, err
	}
	recvPath := filepath.Join(b.tmpDir, "localrecv-"+strings.ToLower(rand.Text()[:20])+storagearchive.FormatExtension(format))
	defer func() { _ = os.Remove(recvPath) }()

	f, err := os.OpenFile(recvPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return UnpackSummary{}, err
	}
	_, copyErr := io.Copy(f, body)
	if cerr := f.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		return UnpackSummary{}, copyErr
	}
	zeroHeadroom := uint64(0)
	res, err := storagearchive.Unpack(ctx, storagearchive.UnpackOptions{
		ArchivePath:          recvPath,
		Format:               format,
		SpaceDir:             filepath.Join(b.dataRoot, req.Space),
		TargetRel:            req.DirRel,
		Mode:                 req.Mode,
		ExpectedSHA256:       req.ExpectedSHA256,
		ExpectedArchiveBytes: req.ExpectedArchiveBytes,
		DeclaredRawBytes:     req.DeclaredRawBytes,
		FreeHeadroom:         &zeroHeadroom,
		CollectModTimes:      req.IncludeModTimes,
	})
	if err != nil {
		return UnpackSummary{}, err
	}
	return UnpackSummary{
		FilesCount: res.FilesCount,
		RawBytes:   res.RawBytes,
		ModTimes:   res.ModTimes,
	}, nil
}

// --- Compatibility Matrix helpers on *Service ---

// HasBuiltinStorageService reports whether TM_STORAGE_SERVICE_URL is configured.
func (s *Service) HasBuiltinStorageService() bool {
	return s != nil && s.storageServiceURL != ""
}

// NodeSupportsArchive implements the locked supports_archive capability matrix:
//   - kind == "docker"                              -> true
//   - kind == "local" && is_builtin && serviceURL!="" -> true
//   - s3, smb, non-builtin local, or builtin w/o URL -> false
func (s *Service) NodeSupportsArchive(kind string, isBuiltin bool) bool {
	if kind == KindDocker {
		return true
	}
	if kind == KindLocal && isBuiltin && s != nil && s.storageServiceURL != "" {
		return true
	}
	return false
}

// CanArchiveBetween implements the locked cross-node operation mode matrix:
//   - Docker <-> Docker        -> true ("archive")
//   - Builtin Local <-> Docker -> true ("archive")
//   - Any pair involving S3, SMB, or non-builtin Local, or Local <-> Local -> false ("direct")
func (s *Service) CanArchiveBetween(from, to NodeRef) bool {
	if from.ID == to.ID {
		return false
	}
	if from.Kind == KindDocker && to.Kind == KindDocker {
		return true
	}
	if (from.IsBuiltin && to.Kind == KindDocker) || (from.Kind == KindDocker && to.IsBuiltin) {
		return true
	}
	return false
}

// CanArchiveBetweenIDs looks up NodeRefs for fromID and toID and evaluates CanArchiveBetween.
func (s *Service) CanArchiveBetweenIDs(ctx context.Context, fromID, toID int64) bool {
	if s == nil || fromID == toID {
		return false
	}
	refs, err := s.NodeRefs(ctx, []int64{fromID, toID})
	if err != nil {
		return false
	}
	fromRef, ok1 := refs[fromID]
	toRef, ok2 := refs[toID]
	if !ok1 || !ok2 {
		return false
	}
	return s.CanArchiveBetween(fromRef, toRef)
}

// ArchiveEndpointForNode returns an ArchiveEndpoint ONLY for KindDocker nodes
// and the Builtin local storage node (delegating to TM_STORAGE_SERVICE_URL when
// configured, or local storagearchive on /data otherwise). Legacy S3, SMB, and
// passive non-builtin Local nodes strictly return ErrArchiveUnsupported.
func (s *Service) ArchiveEndpointForNode(ctx context.Context, nodeID int64) (NodeRef, ArchiveEndpoint, error) {
	if s == nil {
		return NodeRef{}, nil, ErrArchiveUnsupported
	}
	if nodeID <= 0 || nodeID == BuiltinID {
		ref := s.builtinRef()
		if s.storageServiceURL != "" {
			dv, err := NewDockerVolume(BuiltinID, true, DockerConfig{Endpoint: s.storageServiceURL}, Credentials{Token: s.storageServiceToken})
			if err != nil {
				return ref, nil, err
			}
			return ref, dv, nil
		}
		return ref, newBuiltinLocalArchiveEndpoint(s.dataRoot), nil
	}

	ref, vol, err := s.VolumeForNode(ctx, nodeID)
	if err != nil {
		return NodeRef{}, nil, err
	}
	if ref.Kind != KindDocker {
		return ref, nil, ErrArchiveUnsupported
	}
	archVol, ok := vol.(ArchiveEndpoint)
	if !ok {
		return ref, nil, ErrArchiveUnsupported
	}
	return ref, archVol, nil
}
