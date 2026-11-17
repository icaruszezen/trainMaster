package storagenode

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// LocalVolume implements Volume over a directory on the local filesystem.
type LocalVolume struct {
	nodeID    int64
	isBuiltin bool
	root      string
}

// NewLocalVolume creates a LocalVolume rooted at path (which holds datasets/
// and exports/). It does not create directories until Probe or MkdirDir runs.
func NewLocalVolume(nodeID int64, isBuiltin bool, rootPath string) (*LocalVolume, error) {
	trimmed := strings.TrimSpace(rootPath)
	if trimmed == "" {
		return nil, newError(ErrInvalid, "本机存储目录路径不能为空")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, wrapError(ErrInvalid, err, "解析本机存储目录路径失败")
	}
	return &LocalVolume{nodeID: nodeID, isBuiltin: isBuiltin, root: abs}, nil
}

func (v *LocalVolume) NodeID() int64               { return v.nodeID }
func (v *LocalVolume) Kind() string                { return KindLocal }
func (v *LocalVolume) IsBuiltin() bool             { return v.isBuiltin }
func (v *LocalVolume) SupportsFastDirRename() bool { return true }
func (v *LocalVolume) Root() string                { return v.root }
func (v *LocalVolume) Close() error                { return nil }

func (v *LocalVolume) DisplayPath(space, rel string) string {
	base := filepath.Join(v.root, space)
	if rel == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(rel))
}

func (v *LocalVolume) resolve(space, rel string) (string, error) {
	if !validSpace(space) {
		return "", storage.ErrInvalidPath
	}
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return "", err
	}
	base := filepath.Join(v.root, space)
	if cleaned == "" {
		return base, nil
	}
	return filepath.Join(base, filepath.FromSlash(cleaned)), nil
}

func (v *LocalVolume) Probe(_ context.Context) error {
	for _, sp := range []string{SpaceDatasets, SpaceExports} {
		dir := filepath.Join(v.root, sp)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return wrapError(ErrUnavailable, err, "无法创建存储目录 %s", dir)
		}
		f, err := os.CreateTemp(dir, ".tm-probe-*")
		if err != nil {
			return wrapError(ErrUnavailable, err, "存储目录 %s 不可写", dir)
		}
		name := f.Name()
		_, werr := f.WriteString("ok")
		cerr := f.Close()
		rerr := os.Remove(name)
		if err := errors.Join(werr, cerr, rerr); err != nil {
			return wrapError(ErrUnavailable, err, "存储目录 %s 读写探针失败", dir)
		}
	}
	return nil
}

func (v *LocalVolume) Usage(_ context.Context) (storage.Usage, error) {
	if _, err := os.Stat(v.root); errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(v.root, 0o755); mkErr != nil {
			return storage.Usage{}, mkErr
		}
	}
	return storage.DiskUsage(v.root)
}

func (v *LocalVolume) DirExists(_ context.Context, space, dirRel string) (bool, time.Time, error) {
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return false, time.Time{}, err
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	return true, fi.ModTime().UTC(), nil
}

func (v *LocalVolume) IsPlainDir(_ context.Context, space, dirRel string) error {
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return ErrNotDirectory
	}
	return nil
}

func (v *LocalVolume) MkdirDir(_ context.Context, space, dirRel string, exclusive bool) error {
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if exclusive {
		return os.Mkdir(p, 0o755)
	}
	return os.MkdirAll(p, 0o755)
}

func (v *LocalVolume) OpenDir(_ context.Context, space, dirRel string) (Dir, error) {
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return nil, err
	}
	return openLocalDirWithNode(v.nodeID, p)
}

func (v *LocalVolume) ListTopDirs(_ context.Context, space, subPrefix string) ([]TopDir, error) {
	p, err := v.resolve(space, subPrefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if errors.Is(err, fs.ErrNotExist) {
		return []TopDir{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]TopDir, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() { // DirEntry.IsDir is false for symbolic links
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, TopDir{Name: e.Name(), ModifiedAt: info.ModTime().UTC()})
	}
	return out, nil
}

func (v *LocalVolume) RenameDir(_ context.Context, space, fromRel, toRel string) error {
	from, err := v.resolve(space, fromRel)
	if err != nil {
		return err
	}
	to, err := v.resolve(space, toRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}

func (v *LocalVolume) RemoveDirAll(ctx context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return err
	}
	return removeLocalTreeCtx(ctx, p)
}

func (v *LocalVolume) RemoveEmptyDir(_ context.Context, space, dirRel string) error {
	if strings.TrimSpace(dirRel) == "" {
		return storage.ErrInvalidPath
	}
	p, err := v.resolve(space, dirRel)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (v *LocalVolume) CopyFileWithin(_ context.Context, srcSpace, srcRel, dstSpace, dstRel string, linkFn func(oldname, newname string) error) (fs.FileInfo, bool, error) {
	srcPath, err := v.resolve(srcSpace, srcRel)
	if err != nil {
		return nil, false, err
	}
	dstPath, err := v.resolve(dstSpace, dstRel)
	if err != nil {
		return nil, false, err
	}
	srcInfo, err := os.Lstat(srcPath)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return nil, false, err
	}
	if linkFn == nil {
		linkFn = os.Link
	}
	if err := linkFn(srcPath, dstPath); err == nil {
		li, statErr := os.Lstat(dstPath)
		if statErr == nil && os.SameFile(srcInfo, li) {
			return li, true, nil
		}
		_ = os.Remove(dstPath)
		return nil, false, errors.New("the link is not the source file")
	} else {
		_ = os.Remove(dstPath)
		return nil, false, err
	}
}

func removeLocalTreeCtx(ctx context.Context, p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := removeLocalTreeCtx(ctx, filepath.Join(p, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// LocalDir implements Dir using *os.Root on the local filesystem.
type LocalDir struct {
	nodeID int64
	dir    string
	root   *os.Root
}

// OpenLocalDir opens an arbitrary local directory (such as a staging folder)
// as a Dir with NodeID 0.
func OpenLocalDir(dir string) (*LocalDir, error) {
	return openLocalDirWithNode(0, dir)
}

func openLocalDirWithNode(nodeID int64, dir string) (*LocalDir, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, ErrNotDirectory
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &LocalDir{nodeID: nodeID, dir: dir, root: root}, nil
}

func (d *LocalDir) NodeID() int64       { return d.nodeID }
func (d *LocalDir) DisplayPath() string { return d.dir }
func (d *LocalDir) LocalDir() string    { return d.dir }
func (d *LocalDir) Close() error {
	if d == nil || d.root == nil {
		return nil
	}
	return d.root.Close()
}

func localRel(rel string) (string, error) {
	if rel == "" || !utf8.ValidString(rel) || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return "", storage.ErrInvalidPath
	}
	if len(rel) >= 2 && rel[1] == ':' && ('a' <= (rel[0]|0x20) && (rel[0]|0x20) <= 'z') {
		return "", storage.ErrInvalidPath
	}
	osRel := filepath.FromSlash(path.Clean(rel))
	if osRel == "." || !filepath.IsLocal(osRel) {
		return "", storage.ErrInvalidPath
	}
	return osRel, nil
}

func (d *LocalDir) Open(rel string) (File, error) {
	osRel, err := localRel(rel)
	if err != nil {
		return nil, err
	}
	return d.root.Open(osRel)
}

func (d *LocalDir) Lstat(rel string) (fs.FileInfo, error) {
	osRel, err := localRel(rel)
	if err != nil {
		return nil, err
	}
	return d.root.Lstat(osRel)
}

func (d *LocalDir) Stat(rel string) (fs.FileInfo, error) {
	osRel, err := localRel(rel)
	if err != nil {
		return nil, err
	}
	return d.root.Stat(osRel)
}

func (d *LocalDir) ReadDirNames(rel string) ([]string, error) {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return nil, err
	}
	target := "."
	if cleaned != "" {
		target = filepath.FromSlash(cleaned)
	}
	f, err := d.root.Open(target)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		if name == keepMarkerFile {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

func (d *LocalDir) WalkFiles(ctx context.Context, visit func(rel string, info fs.FileInfo, isDir bool) error) error {
	return filepath.WalkDir(d.dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			if p == d.dir {
				return err
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == d.dir {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relOS, err := filepath.Rel(d.dir, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if path.Base(rel) == keepMarkerFile {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			info, ierr := entry.Info()
			if ierr != nil {
				info = memFileInfo{name: entry.Name(), mode: fs.ModeSymlink}
			}
			return visit(rel, info, false)
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		return visit(rel, info, entry.IsDir())
	})
}

func (d *LocalDir) EnsureDir(rel string) error {
	cleaned, err := CleanOptionalRelPath(rel)
	if err != nil {
		return err
	}
	if cleaned == "" {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(cleaned, "/") {
		cur = path.Join(cur, seg)
		name := filepath.FromSlash(cur)
		fi, err := d.root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			if err := d.root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if fi, err = d.root.Lstat(name); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !fi.IsDir() {
			return ErrNotDirectory
		}
	}
	return nil
}

func (d *LocalDir) PutFile(_ context.Context, rel string, r io.Reader, _ int64, perm fs.FileMode) (fs.FileInfo, error) {
	osRel, err := localRel(rel)
	if err != nil {
		return nil, err
	}
	dirOS := filepath.Dir(osRel)
	if dirOS != "." {
		if err := d.EnsureDir(filepath.ToSlash(dirOS)); err != nil {
			return nil, err
		}
	}
	if perm == 0 {
		perm = 0o644
	}
	tmpOS := filepath.Join(dirOS, ".tm-tmp-"+rand.Text()[:12])
	f, err := d.root.OpenFile(tmpOS, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		_ = f.Chmod(perm)
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(filepath.Join(d.dir, tmpOS), filepath.Join(d.dir, osRel))
	}
	if err != nil {
		_ = d.root.Remove(tmpOS)
		return nil, err
	}
	return d.root.Lstat(osRel)
}

func (d *LocalDir) PutBytes(ctx context.Context, rel string, data []byte, perm fs.FileMode) error {
	_, err := d.PutFile(ctx, rel, bytes.NewReader(data), int64(len(data)), perm)
	return err
}

func (d *LocalDir) CreateExclusive(_ context.Context, rel string, data []byte, perm fs.FileMode) error {
	osRel, err := localRel(rel)
	if err != nil {
		return err
	}
	dirOS := filepath.Dir(osRel)
	if dirOS != "." {
		if err := d.EnsureDir(filepath.ToSlash(dirOS)); err != nil {
			return err
		}
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := d.root.OpenFile(osRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		_ = f.Chmod(perm)
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = d.root.Remove(osRel)
	}
	return err
}

func (d *LocalDir) RenameFile(_ context.Context, fromRel, toRel string, replace bool) error {
	fromOS, err := localRel(fromRel)
	if err != nil {
		return err
	}
	toOS, err := localRel(toRel)
	if err != nil {
		return err
	}
	if dirOS := filepath.Dir(toOS); dirOS != "." {
		if err := d.EnsureDir(filepath.ToSlash(dirOS)); err != nil {
			return err
		}
	}
	if !replace {
		if _, err := d.root.Lstat(toOS); err == nil {
			return fmt.Errorf("%s: %w", toRel, fs.ErrExist)
		}
	}
	return os.Rename(filepath.Join(d.dir, fromOS), filepath.Join(d.dir, toOS))
}

func (d *LocalDir) RemoveFile(_ context.Context, rel string) error {
	osRel, err := localRel(rel)
	if err != nil {
		return err
	}
	err = d.root.Remove(osRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
