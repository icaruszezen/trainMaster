package storagenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storageagent"
	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
)

const dockerHeaderCacheBytes = 64 << 10 // 64 KiB cached prefix for image header/EXIF checks

var (
	_ Volume          = (*DockerVolume)(nil)
	_ ArchiveEndpoint = (*DockerVolume)(nil)
	_ Dir             = (*dockerDir)(nil)
	_ File            = (*dockerFile)(nil)
)

// DockerVolume implements Volume and ArchiveEndpoint over HTTP(S) against a
// trainmaster-storage container. It uses a proxy-free HTTP transport so local
// and LAN container traffic is never hijacked by HTTP_PROXY/HTTPS_PROXY.
type DockerVolume struct {
	nodeID     int64
	isBuiltin  bool
	endpoint   string
	token      string
	skipVerify bool
	metaClient *http.Client
	dataClient *http.Client
}

func validateDockerConfig(cfg DockerConfig) (DockerConfig, error) {
	ep := strings.TrimSpace(cfg.Endpoint)
	if ep == "" {
		return DockerConfig{}, newError(ErrInvalid, "请填写 trainMaster 存储服务地址（如 http://192.168.1.50:8090）")
	}
	u, err := url.Parse(ep)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return DockerConfig{}, newError(ErrInvalid, "存储服务地址无效，请填写以 http:// 或 https:// 开头的完整地址")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return DockerConfig{}, newError(ErrInvalid, "存储服务地址不应包含查询参数或锚点")
	}
	cleanEP := strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
	return DockerConfig{
		Endpoint:           cleanEP,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}, nil
}

// NewDockerVolume constructs a Volume client for a trainmaster-storage container.
func NewDockerVolume(nodeID int64, isBuiltin bool, cfg DockerConfig, creds Credentials) (*DockerVolume, error) {
	norm, err := validateDockerConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &DockerVolume{
		nodeID:     nodeID,
		isBuiltin:  isBuiltin,
		endpoint:   norm.Endpoint,
		token:      creds.Token,
		skipVerify: norm.InsecureSkipVerify,
		metaClient: storageagent.NewDirectHTTPClient(norm.InsecureSkipVerify, 30*time.Second),
		dataClient: storageagent.NewDirectHTTPClient(norm.InsecureSkipVerify, 0),
	}, nil
}

func (v *DockerVolume) NodeID() int64               { return v.nodeID }
func (v *DockerVolume) Kind() string                { return KindDocker }
func (v *DockerVolume) IsBuiltin() bool             { return v.isBuiltin }
func (v *DockerVolume) SupportsFastDirRename() bool { return true }
func (v *DockerVolume) Endpoint() string            { return v.endpoint }
func (v *DockerVolume) BearerToken() string         { return v.token }
func (v *DockerVolume) InsecureSkipVerify() bool    { return v.skipVerify }
func (v *DockerVolume) Close() error                { return nil }

func (v *DockerVolume) DisplayPath(space, rel string) string {
	clean, err := CleanOptionalRelPath(rel)
	if err != nil || clean == "" {
		return v.endpoint + "/" + space
	}
	return v.endpoint + "/" + space + "/" + clean
}

func (v *DockerVolume) newReq(ctx context.Context, method, pathAndQuery string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, v.endpoint+pathAndQuery, body)
	if err != nil {
		return nil, err
	}
	if v.token != "" {
		req.Header.Set("Authorization", "Bearer "+v.token)
	}
	return req, nil
}

func (v *DockerVolume) doJSON(ctx context.Context, client *http.Client, method, pathAndQuery string, reqBody any, respOut any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := v.newReq(ctx, method, pathAndQuery, bodyReader)
	if err != nil {
		return 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return resp.StatusCode, decodeAgentError(resp)
	}
	if respOut != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(respOut); err != nil {
			return resp.StatusCode, fmt.Errorf("decode storage response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

type agentErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeAgentError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var env agentErrorEnvelope
	_ = json.Unmarshal(raw, &env)
	code := env.Error.Code
	msg := strings.TrimSpace(env.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if msg == "" {
		msg = resp.Status
	}

	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", fs.ErrNotExist, msg)
	case http.StatusConflict:
		if code == "not_directory" || code == "not_regular_file" {
			return fmt.Errorf("%w: %s", ErrNotDirectory, msg)
		}
		return fmt.Errorf("%w: %s", fs.ErrExist, msg)
	case http.StatusInsufficientStorage:
		return fmt.Errorf("%w: %s", storagearchive.ErrInsufficientStorage, msg)
	case http.StatusBadRequest:
		switch code {
		case "checksum_mismatch":
			return fmt.Errorf("%w: %s", storagearchive.ErrChecksumMismatch, msg)
		case "unsafe_archive_entry":
			return fmt.Errorf("%w: %s", storagearchive.ErrUnsafeArchiveEntry, msg)
		case "invalid_input":
			return fmt.Errorf("%w: %s", storage.ErrInvalidPath, msg)
		default:
			return fmt.Errorf("%w: %s", storagearchive.ErrInvalidArchiveInput, msg)
		}
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", storagearchive.ErrArchiveTooLarge, msg)
	case http.StatusUnauthorized:
		return fmt.Errorf("存储服务访问令牌校验失败（HTTP 401）：%s", msg)
	default:
		return fmt.Errorf("存储服务请求失败（HTTP %d）：%s", resp.StatusCode, msg)
	}
}

func (v *DockerVolume) fetchInfo(ctx context.Context) (storageagent.InfoResponse, error) {
	var info storageagent.InfoResponse
	_, err := v.doJSON(ctx, v.metaClient, http.MethodGet, "/api/v1/info", nil, &info)
	if err != nil {
		return storageagent.InfoResponse{}, err
	}
	return info, nil
}

func (v *DockerVolume) Probe(ctx context.Context) error {
	info, err := v.fetchInfo(ctx)
	if err != nil {
		return err
	}
	if !info.OK || !info.SupportsArchive {
		return errors.New("目标地址不是兼容的 trainMaster 存储服务容器")
	}
	return nil
}

func (v *DockerVolume) Usage(ctx context.Context) (storage.Usage, error) {
	info, err := v.fetchInfo(ctx)
	if err != nil {
		return storage.Usage{}, err
	}
	return storage.Usage{
		TotalBytes: info.Usage.TotalBytes,
		FreeBytes:  info.Usage.FreeBytes,
		UsedBytes:  info.Usage.UsedBytes,
	}, nil
}

func (v *DockerVolume) DirExists(ctx context.Context, space, dirRel string) (bool, time.Time, error) {
	if !validSpace(space) {
		return false, time.Time{}, fmt.Errorf("unknown space %q", space)
	}
	clean, err := CleanOptionalRelPath(dirRel)
	if err != nil {
		return false, time.Time{}, err
	}
	q := url.Values{"op": {"exists"}, "dir": {clean}}
	var out struct {
		Exists     bool      `json:"exists"`
		ModifiedAt time.Time `json:"modified_at"`
	}
	if _, err := v.doJSON(ctx, v.metaClient, http.MethodGet, "/api/v1/spaces/"+space+"/dirs?"+q.Encode(), nil, &out); err != nil {
		return false, time.Time{}, err
	}
	return out.Exists, out.ModifiedAt, nil
}

func (v *DockerVolume) IsPlainDir(ctx context.Context, space, dirRel string) error {
	if !validSpace(space) {
		return fmt.Errorf("unknown space %q", space)
	}
	clean, err := CleanOptionalRelPath(dirRel)
	if err != nil {
		return err
	}
	q := url.Values{"op": {"is_plain_dir"}, "dir": {clean}}
	_, err = v.doJSON(ctx, v.metaClient, http.MethodGet, "/api/v1/spaces/"+space+"/dirs?"+q.Encode(), nil, nil)
	return err
}

func (v *DockerVolume) MkdirDir(ctx context.Context, space, dirRel string, exclusive bool) error {
	if !validSpace(space) {
		return fmt.Errorf("unknown space %q", space)
	}
	clean, err := CleanOptionalRelPath(dirRel)
	if err != nil {
		return err
	}
	_, err = v.doJSON(ctx, v.metaClient, http.MethodPost, "/api/v1/spaces/"+space+"/dirs", map[string]any{
		"dir":       clean,
		"exclusive": exclusive,
	}, nil)
	return err
}

func (v *DockerVolume) OpenDir(ctx context.Context, space, dirRel string) (Dir, error) {
	if err := v.IsPlainDir(ctx, space, dirRel); err != nil {
		return nil, err
	}
	clean, err := CleanOptionalRelPath(dirRel)
	if err != nil {
		return nil, err
	}
	return &dockerDir{
		vol:    v,
		space:  space,
		dirRel: clean,
	}, nil
}

func (v *DockerVolume) ListTopDirs(ctx context.Context, space, subPrefix string) ([]TopDir, error) {
	if !validSpace(space) {
		return nil, fmt.Errorf("unknown space %q", space)
	}
	clean, err := CleanOptionalRelPath(subPrefix)
	if err != nil {
		return nil, err
	}
	q := url.Values{"op": {"list_top"}, "prefix": {clean}}
	var resp struct {
		Items []storageagent.TopDirItem `json:"items"`
	}
	if _, err := v.doJSON(ctx, v.metaClient, http.MethodGet, "/api/v1/spaces/"+space+"/dirs?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]TopDir, len(resp.Items))
	for i, it := range resp.Items {
		out[i] = TopDir{Name: it.Name, ModifiedAt: it.ModifiedAt}
	}
	return out, nil
}

func (v *DockerVolume) RenameDir(ctx context.Context, space, fromRel, toRel string) error {
	if !validSpace(space) {
		return fmt.Errorf("unknown space %q", space)
	}
	fromClean, err := CleanRelPath(fromRel)
	if err != nil {
		return err
	}
	toClean, err := CleanRelPath(toRel)
	if err != nil {
		return err
	}
	_, err = v.doJSON(ctx, v.metaClient, http.MethodPatch, "/api/v1/spaces/"+space+"/dirs", map[string]any{
		"from": fromClean,
		"to":   toClean,
	}, nil)
	return err
}

func (v *DockerVolume) RemoveDirAll(ctx context.Context, space, dirRel string) error {
	return v.deleteDir(ctx, space, dirRel, false)
}

func (v *DockerVolume) RemoveEmptyDir(ctx context.Context, space, dirRel string) error {
	return v.deleteDir(ctx, space, dirRel, true)
}

func (v *DockerVolume) deleteDir(ctx context.Context, space, dirRel string, emptyOnly bool) error {
	if !validSpace(space) {
		return fmt.Errorf("unknown space %q", space)
	}
	clean, err := CleanRelPath(dirRel)
	if err != nil {
		return err
	}
	q := url.Values{"dir": {clean}, "empty_only": {strconv.FormatBool(emptyOnly)}}
	_, err = v.doJSON(ctx, v.metaClient, http.MethodDelete, "/api/v1/spaces/"+space+"/dirs?"+q.Encode(), nil, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (v *DockerVolume) CopyFileWithin(ctx context.Context, srcSpace, srcRel, dstSpace, dstRel string, _ func(oldname, newname string) error) (fs.FileInfo, bool, error) {
	srcClean, err := CleanRelPath(srcRel)
	if err != nil {
		return nil, false, err
	}
	dstClean, err := CleanRelPath(dstRel)
	if err != nil {
		return nil, false, err
	}
	var out storageagent.CopyWithinResult
	if _, err := v.doJSON(ctx, v.metaClient, http.MethodPost, "/api/v1/spaces/copy-within", storageagent.CopyWithinItem{
		SrcSpace: srcSpace,
		SrcRel:   srcClean,
		DstSpace: dstSpace,
		DstRel:   dstClean,
	}, &out); err != nil {
		return nil, false, err
	}
	return memFileInfo{
		name:    path.Base(dstClean),
		size:    out.Size,
		mode:    fs.FileMode(out.Mode),
		modTime: time.Unix(0, out.ModTimeNs).UTC(),
	}, out.Linked, nil
}

// BatchCopyFilesWithin hardlinks/copies multiple files inside the same storage container in one RPC.
func (v *DockerVolume) BatchCopyFilesWithin(ctx context.Context, items []storageagent.CopyWithinItem) ([]storageagent.CopyWithinResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var resp struct {
		Items []storageagent.CopyWithinResult `json:"items"`
	}
	if _, err := v.doJSON(ctx, v.dataClient, http.MethodPost, "/api/v1/spaces/copy-within", map[string]any{
		"items": items,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

type dockerDir struct {
	vol    *DockerVolume
	space  string
	dirRel string
}

func (d *dockerDir) NodeID() int64       { return d.vol.nodeID }
func (d *dockerDir) DisplayPath() string { return d.vol.DisplayPath(d.space, d.dirRel) }
func (d *dockerDir) LocalDir() string    { return "" }
func (d *dockerDir) Close() error        { return nil }

func (d *dockerDir) headInfo(ctx context.Context, cleanRel string, lstat bool) (memFileInfo, error) {
	q := url.Values{
		"dir":   {d.dirRel},
		"path":  {cleanRel},
		"lstat": {strconv.FormatBool(lstat)},
	}
	req, err := d.vol.newReq(ctx, http.MethodHead, "/api/v1/spaces/"+d.space+"/files?"+q.Encode(), nil)
	if err != nil {
		return memFileInfo{}, err
	}
	resp, err := d.vol.metaClient.Do(req)
	if err != nil {
		return memFileInfo{}, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return memFileInfo{}, decodeAgentError(resp)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("X-File-Size"), 10, 64)
	permNum, _ := strconv.ParseUint(resp.Header.Get("X-File-Mode"), 10, 32)
	modNs, _ := strconv.ParseInt(resp.Header.Get("X-File-ModTime-Ns"), 10, 64)
	isDir := resp.Header.Get("X-File-Is-Dir") == "true"
	isSym := resp.Header.Get("X-File-Is-Symlink") == "true"
	mode := fs.FileMode(permNum)
	if isSym {
		mode |= fs.ModeSymlink
	} else if isDir {
		mode |= fs.ModeDir
	}
	return memFileInfo{
		name:    path.Base(cleanRel),
		size:    size,
		mode:    mode,
		modTime: time.Unix(0, modNs).UTC(),
	}, nil
}

func (d *dockerDir) Open(rel string) (File, error) {
	clean, err := CleanRelPath(rel)
	if err != nil {
		return nil, err
	}
	fi, err := d.headInfo(context.Background(), clean, false)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrNotDirectory, clean)
	}
	return &dockerFile{
		dir:  d,
		rel:  clean,
		info: fi,
	}, nil
}

func (d *dockerDir) Lstat(rel string) (fs.FileInfo, error) {
	clean, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return memFileInfo{name: path.Base(d.dirRel), mode: fs.ModeDir | 0o755}, nil
	}
	return d.headInfo(context.Background(), clean, true)
}

func (d *dockerDir) Stat(rel string) (fs.FileInfo, error) {
	clean, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return memFileInfo{name: path.Base(d.dirRel), mode: fs.ModeDir | 0o755}, nil
	}
	return d.headInfo(context.Background(), clean, false)
}

func (d *dockerDir) ReadDirNames(rel string) ([]string, error) {
	clean, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	q := url.Values{"op": {"read_names"}, "dir": {d.dirRel}, "rel": {clean}}
	var resp struct {
		Names []string `json:"names"`
	}
	if _, err := d.vol.doJSON(context.Background(), d.vol.metaClient, http.MethodGet, "/api/v1/spaces/"+d.space+"/dirs?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Names == nil {
		return []string{}, nil
	}
	return resp.Names, nil
}

func (d *dockerDir) WalkFiles(ctx context.Context, visit func(rel string, info fs.FileInfo, isDir bool) error) error {
	q := url.Values{"dir": {d.dirRel}}
	req, err := d.vol.newReq(ctx, http.MethodGet, "/api/v1/spaces/"+d.space+"/walk?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := d.vol.dataClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeAgentError(resp)
	}
	var payload struct {
		Items []storageagent.WalkEntry `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode walk response: %w", err)
	}

	var skipPrefix string
	for _, it := range payload.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if skipPrefix != "" && strings.HasPrefix(it.Rel, skipPrefix) {
			continue
		}
		skipPrefix = ""

		mode := fs.FileMode(it.Mode)
		if it.IsSymlink {
			mode |= fs.ModeSymlink
		} else if it.IsDir {
			mode |= fs.ModeDir
		}
		fi := memFileInfo{
			name:    path.Base(it.Rel),
			size:    it.Size,
			mode:    mode,
			modTime: time.Unix(0, it.ModTimeNs).UTC(),
		}
		err := visit(it.Rel, fi, it.IsDir)
		if errors.Is(err, filepath.SkipDir) {
			if it.IsDir {
				skipPrefix = it.Rel + "/"
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *dockerDir) EnsureDir(rel string) error {
	clean, err := CleanOptionalRelPath(rel)
	if err != nil {
		return err
	}
	if clean == "" {
		return nil
	}
	_, err = d.vol.doJSON(context.Background(), d.vol.metaClient, http.MethodPost, "/api/v1/spaces/"+d.space+"/dirs", map[string]any{
		"dir":        d.dirRel,
		"ensure_rel": clean,
	}, nil)
	return err
}

func (d *dockerDir) PutFile(ctx context.Context, rel string, r io.Reader, size int64, perm fs.FileMode) (fs.FileInfo, error) {
	return d.putFile(ctx, rel, r, size, perm, false)
}

func (d *dockerDir) PutBytes(ctx context.Context, rel string, data []byte, perm fs.FileMode) error {
	_, err := d.putFile(ctx, rel, bytes.NewReader(data), int64(len(data)), perm, false)
	return err
}

func (d *dockerDir) CreateExclusive(ctx context.Context, rel string, data []byte, perm fs.FileMode) error {
	_, err := d.putFile(ctx, rel, bytes.NewReader(data), int64(len(data)), perm, true)
	return err
}

func (d *dockerDir) putFile(ctx context.Context, rel string, r io.Reader, size int64, perm fs.FileMode, exclusive bool) (fs.FileInfo, error) {
	clean, err := CleanRelPath(rel)
	if err != nil {
		return nil, err
	}
	if perm == 0 {
		perm = 0o644
	}
	q := url.Values{
		"dir":       {d.dirRel},
		"path":      {clean},
		"exclusive": {strconv.FormatBool(exclusive)},
		"perm":      {strconv.FormatUint(uint64(perm.Perm()), 8)},
	}
	req, err := d.vol.newReq(ctx, http.MethodPut, "/api/v1/spaces/"+d.space+"/files?"+q.Encode(), r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := d.vol.dataClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, decodeAgentError(resp)
	}
	var out storageagent.FileInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return memFileInfo{
		name:    path.Base(clean),
		size:    out.Size,
		mode:    fs.FileMode(out.Mode),
		modTime: time.Unix(0, out.ModTimeNs).UTC(),
	}, nil
}

func (d *dockerDir) RenameFile(ctx context.Context, fromRel, toRel string, replace bool) error {
	fromClean, err := CleanRelPath(fromRel)
	if err != nil {
		return err
	}
	toClean, err := CleanRelPath(toRel)
	if err != nil {
		return err
	}
	_, err = d.vol.doJSON(ctx, d.vol.metaClient, http.MethodPatch, "/api/v1/spaces/"+d.space+"/files", map[string]any{
		"dir":     d.dirRel,
		"from":    fromClean,
		"to":      toClean,
		"replace": replace,
	}, nil)
	return err
}

func (d *dockerDir) RemoveFile(ctx context.Context, rel string) error {
	clean, err := CleanRelPath(rel)
	if err != nil {
		return err
	}
	q := url.Values{"dir": {d.dirRel}, "path": {clean}}
	_, err = d.vol.doJSON(ctx, d.vol.metaClient, http.MethodDelete, "/api/v1/spaces/"+d.space+"/files?"+q.Encode(), nil, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

type dockerFile struct {
	dir     *dockerDir
	rel     string
	info    memFileInfo
	offset  int64
	headBuf []byte
	stream  io.ReadCloser
}

func (f *dockerFile) Name() string               { return f.rel }
func (f *dockerFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *dockerFile) Close() error {
	if f.stream != nil {
		err := f.stream.Close()
		f.stream = nil
		return err
	}
	return nil
}

func (f *dockerFile) fileURL() string {
	q := url.Values{"dir": {f.dir.dirRel}, "path": {f.rel}}
	return "/api/v1/spaces/" + f.dir.space + "/files?" + q.Encode()
}

func (f *dockerFile) ensureHeadBuf() error {
	if f.headBuf != nil || f.info.size == 0 {
		return nil
	}
	end := min(f.info.size, int64(dockerHeaderCacheBytes)) - 1
	buf := make([]byte, end+1)
	n, err := f.fetchRange(0, end, buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	f.headBuf = buf[:n]
	return nil
}

func (f *dockerFile) fetchRange(start, end int64, dst []byte) (int, error) {
	req, err := f.dir.vol.newReq(context.Background(), http.MethodGet, f.fileURL(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := f.dir.vol.dataClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, decodeAgentError(resp)
	}
	return io.ReadFull(resp.Body, dst)
}

func (f *dockerFile) Read(p []byte) (int, error) {
	if f.offset >= f.info.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.stream == nil && f.info.size <= dockerHeaderCacheBytes {
		if err := f.ensureHeadBuf(); err != nil {
			return 0, err
		}
	}
	if f.stream == nil && f.headBuf != nil && f.offset < int64(len(f.headBuf)) {
		n := copy(p, f.headBuf[f.offset:])
		f.offset += int64(n)
		if f.offset >= f.info.size {
			return n, io.EOF
		}
		return n, nil
	}

	if f.stream == nil {
		req, err := f.dir.vol.newReq(context.Background(), http.MethodGet, f.fileURL(), nil)
		if err != nil {
			return 0, err
		}
		if f.offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", f.offset))
		}
		resp, err := f.dir.vol.dataClient.Do(req)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			err := decodeAgentError(resp)
			_ = resp.Body.Close()
			return 0, err
		}
		f.stream = resp.Body
	}

	n, err := f.stream.Read(p)
	f.offset += int64(n)
	if errors.Is(err, io.EOF) {
		_ = f.Close()
	}
	return n, err
}

func (f *dockerFile) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.offset + offset
	case io.SeekEnd:
		next = f.info.size + offset
	default:
		return f.offset, errors.New("invalid seek whence")
	}
	if next < 0 {
		return f.offset, errors.New("negative seek offset")
	}
	if next != f.offset {
		_ = f.Close()
		f.offset = next
	}
	return f.offset, nil
}

func (f *dockerFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative ReadAt offset")
	}
	if off >= f.info.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < dockerHeaderCacheBytes && f.headBuf == nil {
		_ = f.ensureHeadBuf()
	}
	if f.headBuf != nil && off+int64(len(p)) <= int64(len(f.headBuf)) {
		n := copy(p, f.headBuf[off:])
		return n, nil
	}

	avail := f.info.size - off
	want := int64(len(p))
	eof := false
	if want > avail {
		want = avail
		eof = true
	}
	n, err := f.fetchRange(off, off+want-1, p[:want])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, err
	}
	if eof || int64(n) < int64(len(p)) {
		return n, io.EOF
	}
	return n, nil
}
