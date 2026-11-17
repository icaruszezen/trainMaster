package storagenode

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

const (
	s3InitialPrefixBytes = 64 << 10 // 64 KiB initial Range GET covers headers + VOC XML + EXIF
	s3MultipartPartSize  = 16 << 20 // 16 MiB part size for Multipart Upload
	s3DeleteBatchSize    = 1000
)

// S3Volume implements Volume over any S3-compatible object storage service
// (MinIO, RustFS, Ceph RGW, AWS S3, Alibaba OSS S3, Tencent COS S3, etc.)
// using pure-Go HTTP + AWS Signature V4.
type S3Volume struct {
	nodeID int64
	cfg    S3Config
	secret string

	baseURL   *url.URL
	region    string
	pathStyle bool
	prefix    string // trimmed, no leading/trailing slash
	client    *http.Client
}

// NewS3Volume validates cfg and constructs an S3Volume.
func NewS3Volume(nodeID int64, cfg S3Config, secretAccessKey string) (*S3Volume, error) {
	rawEndpoint := strings.TrimSpace(cfg.Endpoint)
	if rawEndpoint == "" {
		return nil, newError(ErrInvalid, "S3 Endpoint 地址不能为空")
	}
	if !strings.Contains(rawEndpoint, "://") {
		rawEndpoint = "http://" + rawEndpoint
	}
	u, err := url.Parse(rawEndpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, newError(ErrInvalid, "S3 Endpoint 格式无效（示例：http://192.168.1.50:9000 或 https://s3.amazonaws.com）")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""

	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" || strings.ContainsAny(bucket, `/\ `) {
		return nil, newError(ErrInvalid, "S3 Bucket 名称无效")
	}
	accessKey := strings.TrimSpace(cfg.AccessKeyID)
	if accessKey == "" {
		return nil, newError(ErrInvalid, "S3 Access Key 不能为空")
	}
	if secretAccessKey == "" {
		return nil, newError(ErrInvalid, "S3 Secret Key 不能为空")
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	prefix := strings.Trim(strings.ReplaceAll(strings.TrimSpace(cfg.Prefix), `\`, "/"), "/")

	hostOnly := u.Hostname()
	pathStyle := cfg.ForcePathStyle
	if _, addrErr := netip.ParseAddr(hostOnly); addrErr == nil || strings.EqualFold(hostOnly, "localhost") {
		// IP addresses and localhost cannot use virtual-hosted bucket subdomains.
		pathStyle = true
	}

	tr := &http.Transport{
		Proxy: s3ProxySelector,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if cfg.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-configured self-signed LAN MinIO
	}

	normCfg := S3Config{
		Endpoint:           u.String(),
		Region:             region,
		Bucket:             bucket,
		Prefix:             prefix,
		AccessKeyID:        accessKey,
		ForcePathStyle:     pathStyle,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	return &S3Volume{
		nodeID:    nodeID,
		cfg:       normCfg,
		secret:    secretAccessKey,
		baseURL:   u,
		region:    region,
		pathStyle: pathStyle,
		prefix:    prefix,
		client:    &http.Client{Transport: tr},
	}, nil
}

// s3ProxySelector bypasses HTTP_PROXY/HTTPS_PROXY for loopback and RFC1918
// private LAN endpoints (e.g. local MinIO / RustFS on the intranet) while
// honoring environment proxies for public cloud S3 endpoints.
func s3ProxySelector(req *http.Request) (*url.URL, error) {
	host := req.URL.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return nil, nil
		}
	}
	return http.ProxyFromEnvironment(req)
}

func (v *S3Volume) NodeID() int64               { return v.nodeID }
func (v *S3Volume) Kind() string                { return KindS3 }
func (v *S3Volume) IsBuiltin() bool             { return false }
func (v *S3Volume) SupportsFastDirRename() bool { return false }
func (v *S3Volume) Close() error {
	v.client.CloseIdleConnections()
	return nil
}

func (v *S3Volume) DisplayPath(space, rel string) string {
	key := v.objectKey(space, rel)
	return fmt.Sprintf("s3://%s/%s", v.cfg.Bucket, key)
}

func (v *S3Volume) objectKey(space, rel string) string {
	parts := make([]string, 0, 3)
	if v.prefix != "" {
		parts = append(parts, v.prefix)
	}
	if space != "" {
		parts = append(parts, space)
	}
	if rel != "" {
		parts = append(parts, strings.Trim(rel, "/"))
	}
	return strings.Join(parts, "/")
}

func (v *S3Volume) resolveKey(space, rel string) (string, error) {
	if !validSpace(space) {
		return "", storage.ErrInvalidPath
	}
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return "", err
	}
	return v.objectKey(space, cleaned), nil
}

func (v *S3Volume) Usage(_ context.Context) (storage.Usage, error) {
	// Object stores have no POSIX statfs capacity; returning ErrUsageUnsupported
	// lets dataset import and data-version space guards proceed cleanly.
	return storage.Usage{}, storage.ErrUsageUnsupported
}

func (v *S3Volume) Probe(ctx context.Context) error {
	probeKey := v.objectKey(SpaceDatasets, ".tm-probe-"+rand.Text()[:12])
	payload := []byte("trainmaster-probe")
	if err := v.putSingle(ctx, probeKey, payload, false); err != nil {
		return wrapError(ErrUnavailable, err, "无法向 S3 存储桶 %s 写入测试对象", v.cfg.Bucket)
	}
	rc, _, _, err := v.getObject(ctx, probeKey, "")
	if err != nil {
		_ = v.deleteObject(ctx, probeKey)
		return wrapError(ErrUnavailable, err, "无法从 S3 存储桶 %s 读取测试对象", v.cfg.Bucket)
	}
	got, rerr := io.ReadAll(io.LimitReader(rc, 64))
	_ = rc.Close()
	if rerr != nil || !bytes.Equal(got, payload) {
		_ = v.deleteObject(ctx, probeKey)
		return wrapError(ErrUnavailable, rerr, "S3 测试对象内容校验失败")
	}
	if err := v.deleteObject(ctx, probeKey); err != nil {
		return wrapError(ErrUnavailable, err, "无法清理 S3 存储桶 %s 中的测试对象（请检查删除权限）", v.cfg.Bucket)
	}
	return nil
}

func (v *S3Volume) DirExists(ctx context.Context, space, dirRel string) (bool, time.Time, error) {
	baseKey, err := v.resolveKey(space, dirRel)
	if err != nil {
		return false, time.Time{}, err
	}
	prefix := strings.TrimSuffix(baseKey, "/") + "/"
	res, err := v.listObjects(ctx, prefix, "", "", 1)
	if err != nil {
		return false, time.Time{}, err
	}
	if len(res.Contents) > 0 {
		t, _ := time.Parse(time.RFC3339, res.Contents[0].LastModified)
		return true, t.UTC(), nil
	}
	if len(res.CommonPrefixes) > 0 {
		return true, time.Now().UTC(), nil
	}
	return false, time.Time{}, nil
}

func (v *S3Volume) IsPlainDir(ctx context.Context, space, dirRel string) error {
	exists, _, err := v.DirExists(ctx, space, dirRel)
	if err != nil {
		return err
	}
	if !exists {
		return fs.ErrNotExist
	}
	return nil
}

func (v *S3Volume) MkdirDir(ctx context.Context, space, dirRel string, exclusive bool) error {
	baseKey, err := v.resolveKey(space, dirRel)
	if err != nil {
		return err
	}
	if exclusive {
		exists, _, err := v.DirExists(ctx, space, dirRel)
		if err != nil {
			return err
		}
		if exists {
			return fs.ErrExist
		}
	}
	markerKey := strings.TrimSuffix(baseKey, "/") + "/" + keepMarkerFile
	return v.putSingle(ctx, markerKey, nil, false)
}

func (v *S3Volume) OpenDir(ctx context.Context, space, dirRel string) (Dir, error) {
	baseKey, err := v.resolveKey(space, dirRel)
	if err != nil {
		return nil, err
	}
	exists, _, err := v.DirExists(ctx, space, dirRel)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fs.ErrNotExist
	}
	return &S3Dir{
		vol:     v,
		space:   space,
		dirRel:  strings.Trim(dirRel, "/"),
		baseKey: strings.TrimSuffix(baseKey, "/"),
	}, nil
}

func (v *S3Volume) ListTopDirs(ctx context.Context, space, subPrefix string) ([]TopDir, error) {
	baseKey, err := v.resolveKey(space, subPrefix)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if baseKey != "" {
		prefix = strings.TrimSuffix(baseKey, "/") + "/"
	}
	seen := map[string]time.Time{}
	token := ""
	for {
		res, err := v.listObjects(ctx, prefix, "/", token, 1000)
		if err != nil {
			return nil, err
		}
		for _, cp := range res.CommonPrefixes {
			rest := strings.TrimPrefix(cp.Prefix, prefix)
			name := strings.TrimSuffix(rest, "/")
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			if _, ok := seen[name]; !ok {
				seen[name] = time.Time{}
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}

	out := make([]TopDir, 0, len(seen))
	for name := range seen {
		modTime := time.Now().UTC()
		if sub, err := v.listObjects(ctx, prefix+name+"/", "", "", 1); err == nil && len(sub.Contents) > 0 {
			if t, perr := time.Parse(time.RFC3339, sub.Contents[0].LastModified); perr == nil {
				modTime = t.UTC()
			}
		}
		out = append(out, TopDir{Name: name, ModifiedAt: modTime})
	}
	slices.SortFunc(out, func(a, b TopDir) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (v *S3Volume) RenameDir(ctx context.Context, space, fromRel, toRel string) error {
	fromKey, err := v.resolveKey(space, fromRel)
	if err != nil {
		return err
	}
	toKey, err := v.resolveKey(space, toRel)
	if err != nil {
		return err
	}
	fromPrefix := strings.TrimSuffix(fromKey, "/") + "/"
	toPrefix := strings.TrimSuffix(toKey, "/") + "/"
	if fromPrefix == toPrefix {
		return nil
	}

	var keys []string
	token := ""
	for {
		res, err := v.listObjects(ctx, fromPrefix, "", token, 1000)
		if err != nil {
			return err
		}
		for _, obj := range res.Contents {
			keys = append(keys, obj.Key)
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}
	if len(keys) == 0 {
		return fs.ErrNotExist
	}
	for _, oldKey := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		suffix := strings.TrimPrefix(oldKey, fromPrefix)
		if err := v.copyObject(ctx, oldKey, toPrefix+suffix); err != nil {
			return err
		}
	}
	return v.deleteKeys(ctx, keys)
}

func (v *S3Volume) RemoveDirAll(ctx context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	baseKey, err := v.resolveKey(space, dirRel)
	if err != nil {
		return err
	}
	prefix := strings.TrimSuffix(baseKey, "/") + "/"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := v.listObjects(ctx, prefix, "", "", s3DeleteBatchSize)
		if err != nil {
			return err
		}
		if len(res.Contents) == 0 {
			return nil
		}
		keys := make([]string, len(res.Contents))
		for i, c := range res.Contents {
			keys[i] = c.Key
		}
		if err := v.deleteKeys(ctx, keys); err != nil {
			return err
		}
		if !res.IsTruncated {
			return nil
		}
	}
}

func (v *S3Volume) RemoveEmptyDir(ctx context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	baseKey, err := v.resolveKey(space, dirRel)
	if err != nil {
		return err
	}
	prefix := strings.TrimSuffix(baseKey, "/") + "/"
	res, err := v.listObjects(ctx, prefix, "", "", 2)
	if err != nil || len(res.Contents) == 0 {
		return nil
	}
	if len(res.Contents) == 1 && res.Contents[0].Key == prefix+keepMarkerFile {
		_ = v.deleteObject(ctx, res.Contents[0].Key)
	}
	return nil
}

func (v *S3Volume) CopyFileWithin(ctx context.Context, srcSpace, srcRel, dstSpace, dstRel string, _ func(oldname, newname string) error) (fs.FileInfo, bool, error) {
	srcKey, err := v.resolveKey(srcSpace, srcRel)
	if err != nil {
		return nil, false, err
	}
	dstKey, err := v.resolveKey(dstSpace, dstRel)
	if err != nil {
		return nil, false, err
	}
	if err := v.copyObject(ctx, srcKey, dstKey); err != nil {
		return nil, false, err
	}
	info, err := v.headObject(ctx, dstKey)
	if err != nil {
		return nil, false, err
	}
	return info, false, nil
}

// ---- S3Dir ----

// S3Dir implements Dir over an S3 key prefix.
type S3Dir struct {
	vol     *S3Volume
	space   string
	dirRel  string
	baseKey string // without trailing slash
}

func (d *S3Dir) NodeID() int64       { return d.vol.nodeID }
func (d *S3Dir) DisplayPath() string { return d.vol.DisplayPath(d.space, d.dirRel) }
func (d *S3Dir) LocalDir() string    { return "" }
func (d *S3Dir) Close() error        { return nil }

func (d *S3Dir) keyFor(rel string) (string, string, error) {
	cleaned, err := CleanRelPath(rel)
	if err != nil {
		return "", "", err
	}
	return d.baseKey + "/" + cleaned, cleaned, nil
}

func (d *S3Dir) Open(rel string) (File, error) {
	key, cleaned, err := d.keyFor(rel)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	rc, size, modTime, err := d.vol.getObject(ctx, key, fmt.Sprintf("bytes=0-%d", s3InitialPrefixBytes-1))
	if errors.Is(err, errS3RangeUnsatisfiable) {
		// 0-byte object on S3 answers 416 to Range: bytes=0-65535.
		fi, herr := d.vol.headObject(ctx, key)
		if herr != nil {
			return nil, herr
		}
		return &s3File{
			vol:     d.vol,
			key:     key,
			name:    path.Base(cleaned),
			size:    fi.Size(),
			modTime: fi.ModTime(),
			prefix:  []byte{},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	prefix, rerr := io.ReadAll(io.LimitReader(rc, s3InitialPrefixBytes+1))
	_ = rc.Close()
	if rerr != nil {
		return nil, rerr
	}
	if int64(len(prefix)) > size && size >= 0 {
		size = int64(len(prefix))
	}
	return &s3File{
		vol:     d.vol,
		key:     key,
		name:    path.Base(cleaned),
		size:    size,
		modTime: modTime,
		prefix:  prefix,
	}, nil
}

func (d *S3Dir) Lstat(rel string) (fs.FileInfo, error) {
	key, cleaned, err := d.keyFor(rel)
	if err != nil {
		return nil, err
	}
	if path.Base(cleaned) == keepMarkerFile {
		return nil, fs.ErrNotExist
	}
	ctx := context.Background()
	fi, err := d.vol.headObject(ctx, key)
	if err == nil {
		return fi, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// Check if rel is a virtual directory prefix.
	res, lerr := d.vol.listObjects(ctx, key+"/", "", "", 1)
	if lerr != nil {
		return nil, lerr
	}
	if len(res.Contents) > 0 || len(res.CommonPrefixes) > 0 {
		return memFileInfo{name: path.Base(cleaned), mode: fs.ModeDir | 0o755, modTime: time.Now().UTC()}, nil
	}
	return nil, fs.ErrNotExist
}

func (d *S3Dir) Stat(rel string) (fs.FileInfo, error) {
	return d.Lstat(rel)
}

func (d *S3Dir) ReadDirNames(rel string) ([]string, error) {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	prefix := d.baseKey + "/"
	if cleaned != "" {
		prefix = d.baseKey + "/" + cleaned + "/"
	}
	ctx := context.Background()
	seen := map[string]struct{}{}
	anyRaw := false
	token := ""
	for {
		res, err := d.vol.listObjects(ctx, prefix, "/", token, 1000)
		if err != nil {
			return nil, err
		}
		for _, obj := range res.Contents {
			anyRaw = true
			rest := strings.TrimPrefix(obj.Key, prefix)
			rest = strings.TrimSuffix(rest, "/")
			if rest == "" || rest == keepMarkerFile || strings.Contains(rest, "/") {
				continue
			}
			seen[rest] = struct{}{}
		}
		for _, cp := range res.CommonPrefixes {
			anyRaw = true
			rest := strings.TrimSuffix(strings.TrimPrefix(cp.Prefix, prefix), "/")
			if rest == "" || strings.Contains(rest, "/") {
				continue
			}
			seen[rest] = struct{}{}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}
	if cleaned != "" && !anyRaw {
		return nil, fs.ErrNotExist
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

func (d *S3Dir) WalkFiles(ctx context.Context, visit func(rel string, info fs.FileInfo, isDir bool) error) error {
	prefix := d.baseKey + "/"
	visitedDirs := map[string]bool{}
	var skippedPrefixes []string

	isSkipped := func(rel string) bool {
		for _, sp := range skippedPrefixes {
			if rel == sp || strings.HasPrefix(rel, sp+"/") {
				return true
			}
		}
		return false
	}

	token := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := d.vol.listObjects(ctx, prefix, "", token, 1000)
		if err != nil {
			return err
		}
		for _, obj := range res.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := strings.TrimPrefix(obj.Key, prefix)
			rel = strings.TrimSuffix(rel, "/")
			if rel == "" || path.Base(rel) == keepMarkerFile {
				continue
			}
			if isSkipped(rel) {
				continue
			}
			// Synthesize parent directory visits so SkipDir (e.g. hidden/.git/__MACOSX) works identically to WalkDir.
			if dirPart := path.Dir(rel); dirPart != "." && dirPart != "" {
				cur := ""
				skipped := false
				for _, seg := range strings.Split(dirPart, "/") {
					cur = path.Join(cur, seg)
					if isSkipped(cur) {
						skipped = true
						break
					}
					if visitedDirs[cur] {
						continue
					}
					visitedDirs[cur] = true
					derr := visit(cur, memFileInfo{name: seg, mode: fs.ModeDir | 0o755, modTime: time.Now().UTC()}, true)
					if errors.Is(derr, filepath.SkipDir) {
						skippedPrefixes = append(skippedPrefixes, cur)
						skipped = true
						break
					}
					if derr != nil {
						return derr
					}
				}
				if skipped {
					continue
				}
			}
			// Skip bare S3 folder placeholder keys ending with "/".
			if strings.HasSuffix(obj.Key, "/") {
				continue
			}
			modTime, _ := time.Parse(time.RFC3339, obj.LastModified)
			fi := memFileInfo{
				name:    path.Base(rel),
				size:    obj.Size,
				mode:    0o644,
				modTime: modTime.UTC(),
			}
			if err := visit(rel, fi, false); err != nil {
				return err
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			break
		}
		token = res.NextContinuationToken
	}
	return nil
}

func (d *S3Dir) EnsureDir(rel string) error {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return err
	}
	if cleaned == "" {
		return nil
	}
	// Verify no parent directory segment is already occupied by a regular object.
	ctx := context.Background()
	cur := ""
	for _, seg := range strings.Split(cleaned, "/") {
		cur = path.Join(cur, seg)
		if _, err := d.vol.headObject(ctx, d.baseKey+"/"+cur); err == nil {
			return ErrNotDirectory
		}
	}
	return nil
}

func (d *S3Dir) PutFile(ctx context.Context, rel string, r io.Reader, size int64, _ fs.FileMode) (fs.FileInfo, error) {
	key, cleaned, err := d.keyFor(rel)
	if err != nil {
		return nil, err
	}
	if dir := path.Dir(cleaned); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return nil, err
		}
	}
	written, err := d.vol.putStream(ctx, key, r)
	if err != nil {
		return nil, err
	}
	if fi, herr := d.vol.headObject(ctx, key); herr == nil {
		return fi, nil
	}
	return memFileInfo{name: path.Base(cleaned), size: written, mode: 0o644, modTime: time.Now().UTC()}, nil
}

func (d *S3Dir) PutBytes(ctx context.Context, rel string, data []byte, _ fs.FileMode) error {
	key, cleaned, err := d.keyFor(rel)
	if err != nil {
		return err
	}
	if dir := path.Dir(cleaned); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return err
		}
	}
	return d.vol.putSingle(ctx, key, data, false)
}

func (d *S3Dir) CreateExclusive(ctx context.Context, rel string, data []byte, _ fs.FileMode) error {
	key, cleaned, err := d.keyFor(rel)
	if err != nil {
		return err
	}
	if dir := path.Dir(cleaned); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return err
		}
	}
	if _, err := d.vol.headObject(ctx, key); err == nil {
		return fmt.Errorf("%s: %w", cleaned, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return d.vol.putSingle(ctx, key, data, true)
}

func (d *S3Dir) RenameFile(ctx context.Context, fromRel, toRel string, replace bool) error {
	fromKey, _, err := d.keyFor(fromRel)
	if err != nil {
		return err
	}
	toKey, toClean, err := d.keyFor(toRel)
	if err != nil {
		return err
	}
	if fromKey == toKey {
		return nil
	}
	if !replace {
		if _, err := d.vol.headObject(ctx, toKey); err == nil {
			return fmt.Errorf("%s: %w", toClean, fs.ErrExist)
		}
	}
	if err := d.vol.copyObject(ctx, fromKey, toKey); err != nil {
		return err
	}
	return d.vol.deleteObject(ctx, fromKey)
}

func (d *S3Dir) RemoveFile(ctx context.Context, rel string) error {
	key, _, err := d.keyFor(rel)
	if err != nil {
		return err
	}
	return d.vol.deleteObject(ctx, key)
}

// ---- s3File (Read + Seek + ReadAt + Stat) ----

type s3File struct {
	vol     *S3Volume
	key     string
	name    string
	size    int64
	modTime time.Time
	prefix  []byte

	mu        sync.Mutex
	pos       int64
	stream    io.ReadCloser
	streamPos int64
}

func (f *s3File) Name() string { return f.key }

func (f *s3File) Stat() (fs.FileInfo, error) {
	return memFileInfo{name: f.name, size: f.size, mode: 0o644, modTime: f.modTime}, nil
}

func (f *s3File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stream != nil {
		err := f.stream.Close()
		f.stream = nil
		return err
	}
	return nil
}

func (f *s3File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.pos + offset
	case io.SeekEnd:
		next = f.size + offset
	default:
		return 0, errors.New("s3File.Seek: invalid whence")
	}
	if next < 0 {
		return 0, errors.New("s3File.Seek: negative position")
	}
	if f.stream != nil && next != f.streamPos {
		_ = f.stream.Close()
		f.stream = nil
	}
	f.pos = next
	return next, nil
}

func (f *s3File) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pos >= f.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if f.pos < int64(len(f.prefix)) {
		n := copy(p, f.prefix[f.pos:])
		f.pos += int64(n)
		return n, nil
	}
	if f.stream == nil || f.streamPos != f.pos {
		if f.stream != nil {
			_ = f.stream.Close()
			f.stream = nil
		}
		rc, _, _, err := f.vol.getObject(context.Background(), f.key, fmt.Sprintf("bytes=%d-", f.pos))
		if err != nil {
			return 0, err
		}
		f.stream = rc
		f.streamPos = f.pos
	}
	n, err := f.stream.Read(p)
	f.pos += int64(n)
	f.streamPos += int64(n)
	if err != nil {
		_ = f.stream.Close()
		f.stream = nil
	}
	return n, err
}

func (f *s3File) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("s3File.ReadAt: negative offset")
	}
	if off >= f.size {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	// Serve directly from cached 64 KiB header if fully covered.
	if off+int64(len(p)) <= int64(len(f.prefix)) {
		return copy(p, f.prefix[off:]), nil
	}
	if off < int64(len(f.prefix)) && int64(len(f.prefix)) == f.size {
		n := copy(p, f.prefix[off:])
		return n, io.EOF
	}

	end := min(off+int64(len(p))-1, f.size-1)
	rc, _, _, err := f.vol.getObject(context.Background(), f.key, fmt.Sprintf("bytes=%d-%d", off, end))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	n, rerr := io.ReadFull(rc, p[:end-off+1])
	if errors.Is(rerr, io.ErrUnexpectedEOF) {
		rerr = io.EOF
	}
	if rerr == nil && n < len(p) {
		rerr = io.EOF
	}
	return n, rerr
}

// ---- Low-level S3 REST + AWS SigV4 ----

var errS3RangeUnsatisfiable = errors.New("s3: range unsatisfiable")

type s3ListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

type s3InitiateMultipartResult struct {
	UploadID string `xml:"UploadId"`
}

type s3CompleteMultipartUpload struct {
	XMLName xml.Name          `xml:"CompleteMultipartUpload"`
	Parts   []s3CompletedPart `xml:"Part"`
}

type s3CompletedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type s3DeleteRequest struct {
	XMLName xml.Name         `xml:"Delete"`
	Quiet   bool             `xml:"Quiet"`
	Objects []s3DeleteObject `xml:"Object"`
}

type s3DeleteObject struct {
	Key string `xml:"Key"`
}

type s3DeleteResult struct {
	Errors []struct {
		Key     string `xml:"Key"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

type s3ErrorBody struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (v *S3Volume) buildURL(key string, query url.Values) (reqURL string, canonicalURI string, hostHeader string) {
	u := *v.baseURL
	var rawPath string
	if v.pathStyle {
		u.Host = v.baseURL.Host
		if key == "" {
			rawPath = joinURLPath(v.baseURL.Path, s3EscapePath(v.cfg.Bucket))
		} else {
			rawPath = joinURLPath(v.baseURL.Path, s3EscapePath(v.cfg.Bucket)+"/"+s3EscapePath(key))
		}
	} else {
		u.Host = v.cfg.Bucket + "." + v.baseURL.Host
		if key == "" {
			rawPath = joinURLPath(v.baseURL.Path, "")
		} else {
			rawPath = joinURLPath(v.baseURL.Path, s3EscapePath(key))
		}
	}
	if rawPath == "" {
		rawPath = "/"
	}
	u.RawPath = rawPath
	unescaped, err := url.PathUnescape(rawPath)
	if err != nil {
		unescaped = rawPath
	}
	u.Path = unescaped
	if len(query) > 0 {
		u.RawQuery = s3CanonicalQuery(query)
	}
	return u.String(), rawPath, u.Host
}

func joinURLPath(basePath, suffix string) string {
	b := strings.TrimRight(basePath, "/")
	s := strings.TrimLeft(suffix, "/")
	if s == "" {
		if b == "" {
			return "/"
		}
		return b + "/"
	}
	return b + "/" + s
}

func s3EscapePath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == '/' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func s3EscapeQueryComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func s3CanonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	type pair struct{ k, v string }
	var pairs []pair
	for k, vals := range q {
		ek := s3EscapeQueryComponent(k)
		if len(vals) == 0 {
			pairs = append(pairs, pair{ek, ""})
			continue
		}
		for _, val := range vals {
			pairs = append(pairs, pair{ek, s3EscapeQueryComponent(val)})
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := strings.Compare(a.k, b.k); c != 0 {
			return c
		}
		return strings.Compare(a.v, b.v)
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

func (v *S3Volume) doSigned(ctx context.Context, method, key string, query url.Values, body io.Reader, contentLen int64, extraHeaders map[string]string) (*http.Response, error) {
	reqURL, canonicalURI, hostHeader := v.buildURL(key, query)
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Host = hostHeader
	if contentLen >= 0 {
		req.ContentLength = contentLen
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	const payloadHash = "UNSIGNED-PAYLOAD"

	req.Header.Set("Host", hostHeader)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	for hk, hv := range extraHeaders {
		req.Header.Set(hk, hv)
	}

	// Build canonical headers (host + x-amz-*).
	headerKeys := []string{"host"}
	headerVals := map[string]string{"host": strings.TrimSpace(hostHeader)}
	for hk, vals := range req.Header {
		lk := strings.ToLower(hk)
		if strings.HasPrefix(lk, "x-amz-") {
			headerKeys = append(headerKeys, lk)
			headerVals[lk] = strings.TrimSpace(strings.Join(vals, ","))
		}
	}
	slices.Sort(headerKeys)
	var canonHeaders strings.Builder
	for _, lk := range headerKeys {
		canonHeaders.WriteString(lk)
		canonHeaders.WriteByte(':')
		canonHeaders.WriteString(headerVals[lk])
		canonHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerKeys, ";")

	canonReq := strings.Join([]string{
		method,
		canonicalURI,
		s3CanonicalQuery(query),
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	credScope := dateStamp + "/" + v.region + "/s3/aws4_request"
	reqHash := sha256.Sum256([]byte(canonReq))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credScope,
		hex.EncodeToString(reqHash[:]),
	}, "\n")

	signingKey := hmacSHA256(
		hmacSHA256(
			hmacSHA256(
				hmacSHA256([]byte("AWS4"+v.secret), dateStamp),
				v.region,
			),
			"s3",
		),
		"aws4_request",
	)
	sig := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		v.cfg.AccessKeyID, credScope, signedHeaders, sig)
	req.Header.Set("Authorization", authHeader)

	return v.client.Do(req)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func parseS3HTTPError(op string, resp *http.Response) error {
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fs.ErrNotExist
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fs.ErrExist
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return errS3RangeUnsatisfiable
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var eb s3ErrorBody
	if xml.Unmarshal(raw, &eb) == nil && eb.Code != "" {
		if eb.Code == "NoSuchKey" || eb.Code == "NotFound" {
			return fs.ErrNotExist
		}
		return fmt.Errorf("s3 %s: HTTP %d %s (%s)", op, resp.StatusCode, eb.Code, eb.Message)
	}
	return fmt.Errorf("s3 %s: HTTP %d %s", op, resp.StatusCode, strings.TrimSpace(string(raw)))
}

func (v *S3Volume) headObject(ctx context.Context, key string) (fs.FileInfo, error) {
	resp, err := v.doSigned(ctx, http.MethodHead, key, nil, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, parseS3HTTPError("HEAD", resp)
	}
	resp.Body.Close()
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	modTime := parseHTTPTime(resp.Header.Get("Last-Modified"))
	return memFileInfo{name: path.Base(key), size: size, mode: 0o644, modTime: modTime}, nil
}

func (v *S3Volume) getObject(ctx context.Context, key, rangeHdr string) (io.ReadCloser, int64, time.Time, error) {
	var hdrs map[string]string
	if rangeHdr != "" {
		hdrs = map[string]string{"Range": rangeHdr}
	}
	resp, err := v.doSigned(ctx, http.MethodGet, key, nil, nil, 0, hdrs)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, 0, time.Time{}, parseS3HTTPError("GET", resp)
	}
	totalSize := resp.ContentLength
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if idx := strings.LastIndexByte(cr, '/'); idx >= 0 {
			if parsed, perr := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64); perr == nil {
				totalSize = parsed
			}
		}
	}
	return resp.Body, totalSize, parseHTTPTime(resp.Header.Get("Last-Modified")), nil
}

func (v *S3Volume) putSingle(ctx context.Context, key string, data []byte, ifNoneMatch bool) error {
	var hdrs map[string]string
	if ifNoneMatch {
		hdrs = map[string]string{"If-None-Match": "*"}
	}
	resp, err := v.doSigned(ctx, http.MethodPut, key, nil, bytes.NewReader(data), int64(len(data)), hdrs)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return parseS3HTTPError("PUT", resp)
	}
	resp.Body.Close()
	return nil
}

func (v *S3Volume) putStream(ctx context.Context, key string, r io.Reader) (int64, error) {
	buf := make([]byte, s3MultipartPartSize)
	n, err := io.ReadFull(r, buf)
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		// Fits in a single PUT!
		return int64(n), v.putSingle(ctx, key, buf[:n], false)
	}
	if err != nil {
		return 0, err
	}

	// Multipart Upload for large objects (> 16 MiB).
	initResp, err := v.doSigned(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, 0, nil)
	if err != nil {
		return 0, err
	}
	if initResp.StatusCode >= 300 {
		return 0, parseS3HTTPError("CreateMultipartUpload", initResp)
	}
	var initXML s3InitiateMultipartResult
	decErr := xml.NewDecoder(initResp.Body).Decode(&initXML)
	initResp.Body.Close()
	if decErr != nil || initXML.UploadID == "" {
		return 0, fmt.Errorf("s3 CreateMultipartUpload: invalid response: %w", decErr)
	}
	uploadID := initXML.UploadID
	abort := func() {
		abortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if resp, aerr := v.doSigned(abortCtx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil, 0, nil); aerr == nil {
			resp.Body.Close()
		}
	}

	var parts []s3CompletedPart
	var total int64
	partNum := 1
	chunk := buf[:n]
	for {
		if err := ctx.Err(); err != nil {
			abort()
			return total, err
		}
		q := url.Values{
			"partNumber": {strconv.Itoa(partNum)},
			"uploadId":   {uploadID},
		}
		partResp, err := v.doSigned(ctx, http.MethodPut, key, q, bytes.NewReader(chunk), int64(len(chunk)), nil)
		if err != nil {
			abort()
			return total, err
		}
		if partResp.StatusCode >= 300 {
			err = parseS3HTTPError("UploadPart", partResp)
			abort()
			return total, err
		}
		etag := partResp.Header.Get("ETag")
		partResp.Body.Close()
		parts = append(parts, s3CompletedPart{PartNumber: partNum, ETag: etag})
		total += int64(len(chunk))
		partNum++

		m, rerr := io.ReadFull(r, buf)
		if rerr == io.EOF {
			break
		}
		if errors.Is(rerr, io.ErrUnexpectedEOF) {
			chunk = buf[:m]
			q := url.Values{
				"partNumber": {strconv.Itoa(partNum)},
				"uploadId":   {uploadID},
			}
			partResp, err := v.doSigned(ctx, http.MethodPut, key, q, bytes.NewReader(chunk), int64(len(chunk)), nil)
			if err != nil {
				abort()
				return total, err
			}
			if partResp.StatusCode >= 300 {
				err = parseS3HTTPError("UploadPart", partResp)
				abort()
				return total, err
			}
			etag := partResp.Header.Get("ETag")
			partResp.Body.Close()
			parts = append(parts, s3CompletedPart{PartNumber: partNum, ETag: etag})
			total += int64(len(chunk))
			break
		}
		if rerr != nil {
			abort()
			return total, rerr
		}
		chunk = buf[:m]
	}

	completeBody, err := xml.Marshal(s3CompleteMultipartUpload{Parts: parts})
	if err != nil {
		abort()
		return total, err
	}
	compResp, err := v.doSigned(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, bytes.NewReader(completeBody), int64(len(completeBody)), map[string]string{"Content-Type": "application/xml"})
	if err != nil {
		abort()
		return total, err
	}
	if compResp.StatusCode >= 300 {
		err = parseS3HTTPError("CompleteMultipartUpload", compResp)
		abort()
		return total, err
	}
	compResp.Body.Close()
	return total, nil
}

func (v *S3Volume) copyObject(ctx context.Context, srcKey, dstKey string) error {
	copySource := "/" + s3EscapePath(v.cfg.Bucket) + "/" + s3EscapePath(strings.TrimLeft(srcKey, "/"))
	resp, err := v.doSigned(ctx, http.MethodPut, dstKey, nil, nil, 0, map[string]string{
		"X-Amz-Copy-Source": copySource,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return parseS3HTTPError("CopyObject", resp)
	}
	resp.Body.Close()
	return nil
}

func (v *S3Volume) deleteObject(ctx context.Context, key string) error {
	resp, err := v.doSigned(ctx, http.MethodDelete, key, nil, nil, 0, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return parseS3HTTPError("DELETE", resp)
	}
	resp.Body.Close()
	return nil
}

func (v *S3Volume) deleteKeys(ctx context.Context, keys []string) error {
	for chunk := range slices.Chunk(keys, s3DeleteBatchSize) {
		if err := ctx.Err(); err != nil {
			return err
		}
		reqBody := s3DeleteRequest{Quiet: true, Objects: make([]s3DeleteObject, len(chunk))}
		for i, k := range chunk {
			reqBody.Objects[i] = s3DeleteObject{Key: k}
		}
		payload, err := xml.Marshal(reqBody)
		if err != nil {
			return err
		}
		// S3 Multi-Object Delete requires Content-MD5 or x-amz-checksum-sha256 on some implementations;
		// add x-amz-checksum-sha256 and fall back to individual DELETE if rejected.
		sum := sha256.Sum256(payload)
		resp, err := v.doSigned(ctx, http.MethodPost, "", url.Values{"delete": {""}}, bytes.NewReader(payload), int64(len(payload)), map[string]string{
			"Content-Type":         "application/xml",
			"X-Amz-Content-Sha256": hex.EncodeToString(sum[:]),
		})
		if err != nil {
			return err
		}
		if resp.StatusCode >= 300 {
			resp.Body.Close()
			for _, k := range chunk {
				if derr := v.deleteObject(ctx, k); derr != nil {
					return derr
				}
			}
			continue
		}
		var delRes s3DeleteResult
		_ = xml.NewDecoder(resp.Body).Decode(&delRes)
		resp.Body.Close()
		if len(delRes.Errors) > 0 {
			return fmt.Errorf("s3 batch delete %s: %s (%s)", delRes.Errors[0].Key, delRes.Errors[0].Code, delRes.Errors[0].Message)
		}
	}
	return nil
}

func (v *S3Volume) listObjects(ctx context.Context, prefix, delimiter, continuationToken string, maxKeys int) (s3ListResult, error) {
	q := url.Values{
		"list-type": {"2"},
		"prefix":    {prefix},
		"max-keys":  {strconv.Itoa(maxKeys)},
	}
	if delimiter != "" {
		q.Set("delimiter", delimiter)
	}
	if continuationToken != "" {
		q.Set("continuation-token", continuationToken)
	}
	resp, err := v.doSigned(ctx, http.MethodGet, "", q, nil, 0, nil)
	if err != nil {
		return s3ListResult{}, err
	}
	if resp.StatusCode >= 300 {
		return s3ListResult{}, parseS3HTTPError("ListObjectsV2", resp)
	}
	defer resp.Body.Close()
	var out s3ListResult
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return s3ListResult{}, fmt.Errorf("decode S3 ListObjectsV2 XML: %w", err)
	}
	return out, nil
}

func parseHTTPTime(v string) time.Time {
	if v != "" {
		if t, err := http.ParseTime(v); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
