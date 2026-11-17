package storagenode

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudsoda/go-smb2"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

const smbDialTimeout = 8 * time.Second

// SMBVolume implements Volume over a remote SMB2/3 shared folder (Windows /
// Samba / NAS). It pools a single authenticated tree mount and transparently
// reconnects when an idle TCP session drops.
type SMBVolume struct {
	nodeID int64
	cfg    SMBConfig
	pass   string

	mu      sync.Mutex
	session *smb2.Session
	share   *smb2.Share
}

// NewSMBVolume validates cfg and creates an SMBVolume.
func NewSMBVolume(nodeID int64, cfg SMBConfig, password string) (*SMBVolume, error) {
	host := strings.TrimSpace(cfg.Host)
	host = strings.TrimPrefix(host, `\\`)
	host = strings.TrimPrefix(host, `//`)
	if host == "" || strings.ContainsAny(host, `/\ `) {
		return nil, newError(ErrInvalid, "SMB 服务器地址无效（请填写 IP 或主机名，例如 192.168.1.60）")
	}
	port := cfg.Port
	if port == 0 {
		port = 445
	}
	if port < 1 || port > 65535 {
		return nil, newError(ErrInvalid, "SMB 端口无效（范围 1–65535，默认 445）")
	}
	share := strings.Trim(strings.ReplaceAll(strings.TrimSpace(cfg.Share), "/", `\`), `\`)
	if share == "" || strings.ContainsRune(share, '\\') {
		return nil, newError(ErrInvalid, "SMB 共享名无效（请填写一级共享名称，子目录请填在基础路径中）")
	}
	basePath := strings.Trim(strings.ReplaceAll(strings.TrimSpace(cfg.BasePath), `\`, "/"), "/")
	if basePath != "" {
		var err error
		if basePath, err = CleanRelPath(basePath); err != nil {
			return nil, newError(ErrInvalid, "SMB 基础子路径不合法")
		}
	}
	username := strings.TrimSpace(cfg.Username)
	if username == "" {
		return nil, newError(ErrInvalid, "SMB 用户名不能为空")
	}

	normCfg := SMBConfig{
		Host:     host,
		Port:     port,
		Share:    share,
		BasePath: basePath,
		Username: username,
		Domain:   strings.TrimSpace(cfg.Domain),
	}
	return &SMBVolume{
		nodeID: nodeID,
		cfg:    normCfg,
		pass:   password,
	}, nil
}

func (v *SMBVolume) NodeID() int64               { return v.nodeID }
func (v *SMBVolume) Kind() string                { return KindSMB }
func (v *SMBVolume) IsBuiltin() bool             { return false }
func (v *SMBVolume) SupportsFastDirRename() bool { return true }

func (v *SMBVolume) DisplayPath(space, rel string) string {
	parts := []string{`\\` + v.cfg.Host, v.cfg.Share}
	if v.cfg.BasePath != "" {
		parts = append(parts, strings.ReplaceAll(v.cfg.BasePath, "/", `\`))
	}
	if space != "" {
		parts = append(parts, space)
	}
	if rel != "" {
		parts = append(parts, strings.ReplaceAll(strings.Trim(rel, "/"), "/", `\`))
	}
	return strings.Join(parts, `\`)
}

func (v *SMBVolume) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dropLocked()
	return nil
}

func (v *SMBVolume) dropLocked() {
	if v.share != nil {
		_ = v.share.Umount()
		v.share = nil
	}
	if v.session != nil {
		_ = v.session.Logoff()
		v.session = nil
	}
}

func (v *SMBVolume) connect(ctx context.Context) (*smb2.Share, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.share != nil && v.session != nil {
		if err := v.session.WithContext(ctx).Echo(); err == nil {
			return v.share.WithContext(ctx), nil
		}
		v.dropLocked()
	}

	dialCtx, cancel := context.WithTimeout(ctx, smbDialTimeout)
	defer cancel()
	addr := net.JoinHostPort(v.cfg.Host, strconv.Itoa(v.cfg.Port))
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     v.cfg.Username,
			Password: v.pass,
			Domain:   v.cfg.Domain,
		},
	}
	sess, err := d.Dial(dialCtx, addr)
	if err != nil {
		return nil, wrapError(ErrUnavailable, err, "无法连接 SMB 服务器 %s", addr)
	}
	sh, err := sess.WithContext(dialCtx).Mount(v.cfg.Share)
	if err != nil {
		_ = sess.Logoff()
		return nil, wrapError(ErrUnavailable, err, "无法挂载 SMB 共享目录 \\\\%s\\%s", v.cfg.Host, v.cfg.Share)
	}
	v.session = sess
	v.share = sh
	return sh.WithContext(ctx), nil
}

// smbPath formats a slash-separated relative path into go-smb2's backslash
// relative path without a leading backslash.
func (v *SMBVolume) smbPath(space, rel string) (string, error) {
	if !validSpace(space) {
		return "", storage.ErrInvalidPath
	}
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 3)
	if v.cfg.BasePath != "" {
		parts = append(parts, v.cfg.BasePath)
	}
	parts = append(parts, space)
	if cleaned != "" {
		parts = append(parts, cleaned)
	}
	return strings.ReplaceAll(strings.Join(parts, "/"), "/", `\`), nil
}

func (v *SMBVolume) Probe(ctx context.Context) error {
	sh, err := v.connect(ctx)
	if err != nil {
		return err
	}
	for _, sp := range []string{SpaceDatasets, SpaceExports} {
		dir, err := v.smbPath(sp, "")
		if err != nil {
			return err
		}
		if err := sh.MkdirAll(dir, 0o755); err != nil {
			return wrapError(ErrUnavailable, err, "无法在 SMB 共享中创建目录 %s", dir)
		}
		probe := dir + `\.tm-probe-` + rand.Text()[:12]
		if err := sh.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			return wrapError(ErrUnavailable, err, "SMB 共享目录 %s 不可写", dir)
		}
		if err := sh.Remove(probe); err != nil {
			return wrapError(ErrUnavailable, err, "SMB 共享目录 %s 无法删除测试文件", dir)
		}
	}
	return nil
}

func (v *SMBVolume) Usage(ctx context.Context) (storage.Usage, error) {
	sh, err := v.connect(ctx)
	if err != nil {
		return storage.Usage{}, err
	}
	target := "."
	if v.cfg.BasePath != "" {
		target = strings.ReplaceAll(v.cfg.BasePath, "/", `\`)
		_ = sh.MkdirAll(target, 0o755)
	}
	info, err := sh.Statfs(target)
	if err != nil {
		return storage.Usage{}, err
	}
	block := info.BlockSize()
	total := info.TotalBlockCount() * block
	free := info.AvailableBlockCount() * block
	var used uint64
	if total > free {
		used = total - free
	}
	return storage.Usage{TotalBytes: total, FreeBytes: free, UsedBytes: used}, nil
}

func (v *SMBVolume) DirExists(ctx context.Context, space, dirRel string) (bool, time.Time, error) {
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return false, time.Time{}, err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	fi, err := sh.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	return true, fi.ModTime().UTC(), nil
}

func (v *SMBVolume) IsPlainDir(ctx context.Context, space, dirRel string) error {
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return err
	}
	fi, err := sh.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return ErrNotDirectory
	}
	return nil
}

func (v *SMBVolume) MkdirDir(ctx context.Context, space, dirRel string, exclusive bool) error {
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return err
	}
	if idx := strings.LastIndexByte(p, '\\'); idx > 0 {
		if err := sh.MkdirAll(p[:idx], 0o755); err != nil {
			return err
		}
	}
	if exclusive {
		return sh.Mkdir(p, 0o755)
	}
	return sh.MkdirAll(p, 0o755)
}

func (v *SMBVolume) OpenDir(ctx context.Context, space, dirRel string) (Dir, error) {
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return nil, err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return nil, err
	}
	fi, err := sh.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrNotDirectory
	}
	return &SMBDir{
		vol:     v,
		space:   space,
		dirRel:  strings.Trim(dirRel, "/"),
		baseSMB: p,
	}, nil
}

func (v *SMBVolume) ListTopDirs(ctx context.Context, space, subPrefix string) ([]TopDir, error) {
	p, err := v.smbPath(space, subPrefix)
	if err != nil {
		return nil, err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := sh.ReadDir(p)
	if errors.Is(err, fs.ErrNotExist) {
		return []TopDir{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]TopDir, 0, len(entries))
	for _, fi := range entries {
		if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		out = append(out, TopDir{Name: fi.Name(), ModifiedAt: fi.ModTime().UTC()})
	}
	slices.SortFunc(out, func(a, b TopDir) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (v *SMBVolume) RenameDir(ctx context.Context, space, fromRel, toRel string) error {
	from, err := v.smbPath(space, fromRel)
	if err != nil {
		return err
	}
	to, err := v.smbPath(space, toRel)
	if err != nil {
		return err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return err
	}
	if idx := strings.LastIndexByte(to, '\\'); idx > 0 {
		if err := sh.MkdirAll(to[:idx], 0o755); err != nil {
			return err
		}
	}
	return sh.Rename(from, to)
}

func (v *SMBVolume) RemoveDirAll(ctx context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return err
	}
	err = sh.RemoveAll(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (v *SMBVolume) RemoveEmptyDir(ctx context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	p, err := v.smbPath(space, dirRel)
	if err != nil {
		return err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return nil
	}
	err = sh.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (v *SMBVolume) CopyFileWithin(ctx context.Context, srcSpace, srcRel, dstSpace, dstRel string, _ func(oldname, newname string) error) (fs.FileInfo, bool, error) {
	srcPath, err := v.smbPath(srcSpace, srcRel)
	if err != nil {
		return nil, false, err
	}
	dstPath, err := v.smbPath(dstSpace, dstRel)
	if err != nil {
		return nil, false, err
	}
	sh, err := v.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	in, err := sh.Open(srcPath)
	if err != nil {
		return nil, false, err
	}
	defer in.Close()
	if idx := strings.LastIndexByte(dstPath, '\\'); idx > 0 {
		if err := sh.MkdirAll(dstPath[:idx], 0o755); err != nil {
			return nil, false, err
		}
	}
	tmpPath := dstPath + ".tm-tmp-" + rand.Text()[:10]
	out, err := sh.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, false, err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = sh.Remove(dstPath)
		err = sh.Rename(tmpPath, dstPath)
	}
	if err != nil {
		_ = sh.Remove(tmpPath)
		return nil, false, err
	}
	fi, err := sh.Lstat(dstPath)
	return fi, false, err
}

// ---- SMBDir ----

// SMBDir implements Dir over a directory inside an SMBVolume.
type SMBDir struct {
	vol     *SMBVolume
	space   string
	dirRel  string
	baseSMB string
}

func (d *SMBDir) NodeID() int64       { return d.vol.nodeID }
func (d *SMBDir) DisplayPath() string { return d.vol.DisplayPath(d.space, d.dirRel) }
func (d *SMBDir) LocalDir() string    { return "" }
func (d *SMBDir) Close() error        { return nil }

func (d *SMBDir) join(rel string) (string, string, error) {
	cleaned, err := CleanRelPath(rel)
	if err != nil {
		return "", "", err
	}
	return d.baseSMB + `\` + strings.ReplaceAll(cleaned, "/", `\`), cleaned, nil
}

func (d *SMBDir) Open(rel string) (File, error) {
	p, _, err := d.join(rel)
	if err != nil {
		return nil, err
	}
	sh, err := d.vol.connect(context.Background())
	if err != nil {
		return nil, err
	}
	fi, err := sh.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || fi.IsDir() {
		return nil, fs.ErrInvalid
	}
	return sh.Open(p)
}

func (d *SMBDir) Lstat(rel string) (fs.FileInfo, error) {
	p, _, err := d.join(rel)
	if err != nil {
		return nil, err
	}
	sh, err := d.vol.connect(context.Background())
	if err != nil {
		return nil, err
	}
	return sh.Lstat(p)
}

func (d *SMBDir) Stat(rel string) (fs.FileInfo, error) {
	return d.Lstat(rel)
}

func (d *SMBDir) ReadDirNames(rel string) ([]string, error) {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	smbDir := d.baseSMB
	if cleaned != "" {
		smbDir = d.baseSMB + `\` + strings.ReplaceAll(cleaned, "/", `\`)
	}
	sh, err := d.vol.connect(context.Background())
	if err != nil {
		return nil, err
	}
	entries, err := sh.ReadDir(smbDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, fi := range entries {
		if fi.Name() == keepMarkerFile {
			continue
		}
		out = append(out, fi.Name())
	}
	return out, nil
}

func (d *SMBDir) WalkFiles(ctx context.Context, visit func(rel string, info fs.FileInfo, isDir bool) error) error {
	sh, err := d.vol.connect(ctx)
	if err != nil {
		return err
	}
	var walk func(smbDir, relPrefix string) error
	walk = func(smbDir, relPrefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := sh.ReadDir(smbDir)
		if err != nil {
			if relPrefix == "" {
				return err
			}
			return nil
		}
		slices.SortFunc(entries, func(a, b fs.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
		for _, fi := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := fi.Name()
			if name == keepMarkerFile {
				continue
			}
			rel := name
			if relPrefix != "" {
				rel = relPrefix + "/" + name
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				if err := visit(rel, fi, false); err != nil && !errors.Is(err, filepath.SkipDir) {
					return err
				}
				continue
			}
			if fi.IsDir() {
				err := visit(rel, fi, true)
				if errors.Is(err, filepath.SkipDir) {
					continue
				}
				if err != nil {
					return err
				}
				if err := walk(smbDir+`\`+name, rel); err != nil {
					return err
				}
				continue
			}
			if err := visit(rel, fi, false); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(d.baseSMB, "")
}

func (d *SMBDir) EnsureDir(rel string) error {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return err
	}
	if cleaned == "" {
		return nil
	}
	sh, err := d.vol.connect(context.Background())
	if err != nil {
		return err
	}
	cur := d.baseSMB
	for _, seg := range strings.Split(cleaned, "/") {
		cur += `\` + seg
		fi, err := sh.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := sh.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if fi, err = sh.Lstat(cur); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
			return ErrNotDirectory
		}
	}
	return nil
}

func (d *SMBDir) PutFile(ctx context.Context, rel string, r io.Reader, _ int64, perm fs.FileMode) (fs.FileInfo, error) {
	dstSMB, cleaned, err := d.join(rel)
	if err != nil {
		return nil, err
	}
	if dir := path.Dir(cleaned); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return nil, err
		}
	}
	sh, err := d.vol.connect(ctx)
	if err != nil {
		return nil, err
	}
	if perm == 0 {
		perm = 0o644
	}
	tmpSMB := dstSMB + ".tm-tmp-" + rand.Text()[:10]
	f, err := sh.OpenFile(tmpSMB, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// On Windows SMB servers, Rename fails when the target already exists; remove first.
		_ = sh.Remove(dstSMB)
		err = sh.Rename(tmpSMB, dstSMB)
	}
	if err != nil {
		_ = sh.Remove(tmpSMB)
		return nil, err
	}
	return sh.Lstat(dstSMB)
}

func (d *SMBDir) PutBytes(ctx context.Context, rel string, data []byte, perm fs.FileMode) error {
	_, err := d.PutFile(ctx, rel, bytes.NewReader(data), int64(len(data)), perm)
	return err
}

func (d *SMBDir) CreateExclusive(ctx context.Context, rel string, data []byte, perm fs.FileMode) error {
	dstSMB, cleaned, err := d.join(rel)
	if err != nil {
		return err
	}
	if dir := path.Dir(cleaned); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return err
		}
	}
	sh, err := d.vol.connect(ctx)
	if err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := sh.OpenFile(dstSMB, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = sh.Remove(dstSMB)
	}
	return err
}

func (d *SMBDir) RenameFile(ctx context.Context, fromRel, toRel string, replace bool) error {
	fromSMB, _, err := d.join(fromRel)
	if err != nil {
		return err
	}
	toSMB, toClean, err := d.join(toRel)
	if err != nil {
		return err
	}
	if dir := path.Dir(toClean); dir != "." {
		if err := d.EnsureDir(dir); err != nil {
			return err
		}
	}
	sh, err := d.vol.connect(ctx)
	if err != nil {
		return err
	}
	if _, err := sh.Lstat(toSMB); err == nil {
		if !replace {
			return fmt.Errorf("%s: %w", toClean, fs.ErrExist)
		}
		_ = sh.Remove(toSMB)
	}
	return sh.Rename(fromSMB, toSMB)
}

func (d *SMBDir) RemoveFile(ctx context.Context, rel string) error {
	p, _, err := d.join(rel)
	if err != nil {
		return err
	}
	sh, err := d.vol.connect(ctx)
	if err != nil {
		return err
	}
	err = sh.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
