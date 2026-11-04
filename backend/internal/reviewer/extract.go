package reviewer

import (
	"archive/zip"
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

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// Opening a package zip extracts it into a folder next to it named like
// the zip (审图-12-外观.zip → 审图-12-外观\), holding review.json at its top.
// Only the files review.json names are extracted (and README.txt and a
// progress file); an archive with any unsafe entry (an absolute path, a
// drive letter, "..", a symbolic link) is refused as a whole. The files go
// to a temporary folder first, which is renamed once everything is there,
// so a folder with the final name is always complete. When that folder
// exists already and holds the same package (same package_id), it is
// opened as it is: the review continues where it stopped. A folder of
// another package (or of other files) keeps its name, and the zip goes to
// "<name> (2)", "<name> (3)", ...

// Extraction limits.
const (
	maxImageBytes = 4 << 30 // one image file
	// extractMargin is free space left over after extracting.
	extractMargin = 64 << 20
	// maxTargets bounds the "<name> (n)" folders tried.
	maxTargets = 100
)

// extractingMarker is in the names of the temporary folders extraction
// writes to.
const extractingMarker = ".tm-extracting-"

// staleExtraction is how old a temporary extraction folder must be to be
// taken for the leftover of a crash and removed.
const staleExtraction = 24 * time.Hour

// extractProgress is told how many bytes of how many are extracted.
type extractProgress func(done, total int64)

// freeSpace returns the bytes free for the user on the volume of dir;
// ok is false when it cannot be told (the check is then skipped). Tests
// replace it.
var freeSpace = diskFree

// extractPackage opens the package zip at zipPath: it returns the folder
// the zip is (or was earlier) extracted to, and whether that folder was
// there already.
func extractPackage(ctx context.Context, zipPath string, progress extractProgress) (dir string, resumed bool, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, newError(ErrNotFound, "文件不存在：%s", zipPath)
		}
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return "", false, newError(ErrIO, "无法打开 %s：%v", zipPath, pe.Err)
		}
		return "", false, newError(ErrInvalid, "%s 不是有效的 zip 压缩包（可能没有下载完整）", filepath.Base(zipPath))
	}
	defer zr.Close()
	plan, err := planExtraction(&zr.Reader)
	if err != nil {
		return "", false, err
	}

	parent := filepath.Dir(zipPath)
	base := strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	if base == "" {
		base = reviewfile.PackageFolder(plan.manifest.Review.ID, plan.manifest.Review.Name)
	}
	target, resumed, err := pickTarget(parent, base, plan.manifest.PackageID)
	if err != nil || resumed {
		return target, resumed, err
	}
	removeStaleExtractions(parent, filepath.Base(target))
	if free, ok := freeSpace(parent); ok && free < plan.total+extractMargin {
		return "", false, newError(ErrNoSpace, "磁盘空间不足：解压审图包需要约 %s，%s 所在的磁盘只剩 %s",
			formatBytes(plan.total+extractMargin), parent, formatBytes(free))
	}
	tmp := target + extractingMarker + strings.ToLower(rand.Text()[:12])
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return "", false, newError(ErrIO, "无法在 %s 中创建文件夹：%v", parent, err)
	}
	if err := plan.extract(ctx, tmp, progress); err != nil {
		if rerr := os.RemoveAll(tmp); rerr != nil {
			err = fmt.Errorf("%w（临时文件夹 %s 没能删除：%v）", err, tmp, rerr)
		}
		return "", false, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.RemoveAll(tmp)
		return "", false, newError(ErrIO, "无法把审图包解压到 %s：%v", target, err)
	}
	return target, false, nil
}

// extraction is what to extract from a package zip.
type extraction struct {
	manifest *reviewfile.Manifest
	files    []extractFile
	total    int64 // bytes
}

type extractFile struct {
	entry *zip.File
	rel   string // in the package folder
}

// planExtraction checks every entry of a package zip, reads review.json and
// lists the files to extract.
func planExtraction(zr *zip.Reader) (*extraction, error) {
	byName := make(map[string]*zip.File, 2*len(zr.File))
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		// Zip tools of Chinese Windows write GBK names; some Windows tools
		// write "\" between folders.
		name := strings.ReplaceAll(dataset.DecodeEntryName(f.Name), `\`, "/")
		if !safeEntryName(name) {
			return nil, newError(ErrInvalid, "压缩包中有不安全的路径（%s），为了安全没有解压。请重新从平台下载审图包", f.Name)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return nil, newError(ErrInvalid, "压缩包中有符号链接（%s），为了安全没有解压。请重新从平台下载审图包", f.Name)
		}
		names = append(names, name)
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		for _, key := range []string{name, reviewfile.FoldKey(name)} {
			if byName[key] == nil {
				byName[key] = f
			}
		}
	}
	prefix, err := reviewfile.ManifestPrefix(names)
	if err != nil {
		var me *reviewfile.ManifestError
		if errors.As(err, &me) {
			return nil, &Error{Kind: ErrInvalid, Message: me.Message}
		}
		return nil, err
	}
	entry := func(rel string) *zip.File {
		if f := byName[prefix+rel]; f != nil {
			return f
		}
		return byName[reviewfile.FoldKey(prefix+rel)]
	}
	mf := entry(reviewfile.ManifestFile)
	if mf == nil { // a folder entry named review.json
		return nil, &Error{Kind: ErrInvalid, Message: "压缩包中没有 review.json，这不是 trainMaster 的审图包"}
	}
	if mf.UncompressedSize64 > maxManifestBytes {
		return nil, newError(ErrInvalid, "review.json 过大（超过 %d MiB）", maxManifestBytes>>20)
	}
	rc, err := mf.Open()
	if err != nil {
		return nil, newError(ErrInvalid, "压缩包中的 review.json 无法读取：%v", err)
	}
	m, err := reviewfile.ReadManifest(rc, maxManifestBytes)
	rc.Close()
	if err != nil {
		var me *reviewfile.ManifestError
		if errors.As(err, &me) {
			return nil, &Error{Kind: ErrInvalid, Message: me.Message}
		}
		return nil, newError(ErrInvalid, "压缩包中的 review.json 无法读取（压缩包可能已损坏）：%v", err)
	}
	if m.Kind != reviewfile.KindPackage {
		return nil, &Error{Kind: ErrInvalid,
			Message: "这是本地审图程序导出的审图结果，不是审图包：请在平台上这个审图任务的页面点“上传审图结果”上传它"}
	}

	plan := &extraction{manifest: m}
	add := func(rel string, limit uint64, what string) error {
		f := entry(rel)
		if f == nil {
			return nil // missing: the image (or VOC file) is shown as missing
		}
		if f.UncompressedSize64 > limit {
			return newError(ErrInvalid, "压缩包中的%s %s 过大（%s）", what, rel, formatBytes(int64(f.UncompressedSize64)))
		}
		plan.files = append(plan.files, extractFile{entry: f, rel: rel})
		plan.total += int64(f.UncompressedSize64)
		return nil
	}
	if err := add(reviewfile.ManifestFile, maxManifestBytes, "文件"); err != nil {
		return nil, err
	}
	for _, rel := range []string{reviewfile.ReadmeFile, reviewfile.ProgressFile} {
		if err := add(rel, maxProgressBytes, "文件"); err != nil {
			return nil, err
		}
	}
	for _, img := range m.Images {
		if err := add(img.Path, maxImageBytes, "图片"); err != nil {
			return nil, err
		}
		if err := add(img.Annotation, dataset.MaxAnnotationBytes, "标注文件"); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// safeEntryName reports whether a zip entry name (with "/" separators)
// stays inside the folder it is extracted to.
func safeEntryName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) ||
		(len(name) >= 2 && name[1] == ':') {
		return false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// extract writes the planned files into the folder dir.
func (x *extraction) extract(ctx context.Context, dir string, progress extractProgress) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return newError(ErrIO, "无法打开文件夹 %s：%v", dir, err)
	}
	defer root.Close()
	var done int64
	report := func() {
		if progress != nil {
			progress(done, x.total)
		}
	}
	report()
	last := time.Now()
	buf := make([]byte, 256<<10)
	for _, f := range x.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := extractOne(ctx, root, f, buf, func(n int64) {
			if time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				if progress != nil {
					progress(done+n, x.total)
				}
			}
		})
		if err != nil {
			return err
		}
		done += n
	}
	report()
	return nil
}

// extractOne writes one entry to its file (which must not exist yet).
func extractOne(ctx context.Context, root *os.Root, f extractFile, buf []byte, progress func(int64)) (int64, error) {
	if err := reviewfile.EnsureDirs(root, path.Dir(f.rel)); err != nil {
		return 0, newError(ErrIO, "无法创建文件夹 %s：%v", path.Dir(f.rel), err)
	}
	rc, err := f.entry.Open()
	if err != nil {
		return 0, newError(ErrInvalid, "压缩包中的 %s 无法读取（压缩包可能已损坏）：%v", f.entry.Name, err)
	}
	defer rc.Close()
	out, err := root.OpenFile(filepath.FromSlash(f.rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) { // names differing in case only: the first counts
			return 0, nil
		}
		return 0, newError(ErrIO, "无法创建文件 %s：%v", f.rel, err)
	}
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return n, err
		}
		m, rerr := rc.Read(buf)
		if m > 0 {
			if _, werr := out.Write(buf[:m]); werr != nil {
				out.Close()
				return n, newError(ErrIO, "无法写入文件 %s：%v", f.rel, werr)
			}
			n += int64(m)
			progress(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return n, newError(ErrInvalid, "压缩包中的 %s 无法读取（压缩包可能已损坏或没有下载完整）：%v", f.entry.Name, rerr)
		}
	}
	if err := out.Close(); err != nil {
		return n, newError(ErrIO, "无法写入文件 %s：%v", f.rel, err)
	}
	return n, nil
}

// pickTarget returns the folder to extract a package to: base in parent,
// or "base (2)", ... resumed is true when that folder holds the package
// (the same package ID) already.
func pickTarget(parent, base, packageID string) (string, bool, error) {
	for n := 1; n <= maxTargets; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		p := filepath.Join(parent, name)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p, false, nil
		}
		if err != nil {
			return "", false, newError(ErrIO, "无法检查文件夹 %s：%v", p, err)
		}
		if fi.IsDir() && folderPackageID(p) == packageID {
			return p, true, nil
		}
	}
	return "", false, newError(ErrConflict, "%s 旁边已经有太多同名的文件夹，请整理后再打开", filepath.Join(parent, base))
}

// folderPackageID returns the package ID of review.json in dir, "" when it
// has none (or one that cannot be read).
func folderPackageID(dir string) string {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ""
	}
	defer root.Close()
	m, err := readManifestFile(root)
	if err != nil {
		return ""
	}
	return m.PackageID
}

// removeStaleExtractions removes temporary extraction folders of name in
// parent that a crash left behind.
func removeStaleExtractions(parent, name string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), name+extractingMarker) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > staleExtraction {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// formatBytes formats a size for messages.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), ""
	for _, s := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}

// ---- what a path opens ----

// openTarget is what a path given to open stands for.
type openTarget struct {
	zipPath string // a package zip to extract (or resume)
	dir     string // a package folder
}

// resolveOpenPath tells what to open for a path: a .zip file, a package
// folder (holding review.json), its review.json, or a folder with exactly
// one package folder in it (such as the folder Windows' “全部解压缩”
// makes). Quotes around the path (Explorer's “复制为路径” adds them; no
// Windows path holds one) are left out.
func resolveOpenPath(p string) (openTarget, error) {
	p = strings.TrimSpace(p)
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		p = strings.TrimSpace(p[1 : len(p)-1])
	}
	if p == "" {
		return openTarget{}, &Error{Kind: ErrInvalid, Message: "请选择审图包（.zip 文件）或解压后的文件夹"}
	}
	if !filepath.IsAbs(p) {
		return openTarget{}, newError(ErrInvalid, "请输入完整的路径（如 C:\\Users\\…\\审图包.zip）：%s", p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return openTarget{}, newError(ErrNotFound, "文件或文件夹不存在：%s", p)
	case errors.Is(err, fs.ErrPermission):
		return openTarget{}, newError(ErrForbidden, "没有权限打开 %s", p)
	case err != nil:
		return openTarget{}, newError(ErrIO, "无法打开 %s：%v", p, err)
	}
	if !fi.IsDir() {
		switch {
		case strings.EqualFold(filepath.Ext(p), ".zip"):
			return openTarget{zipPath: p}, nil
		case strings.EqualFold(filepath.Base(p), reviewfile.ManifestFile):
			return openTarget{dir: filepath.Dir(p)}, nil
		}
		return openTarget{}, newError(ErrInvalid, "只能打开审图包（.zip 文件）或解压后的审图包文件夹：%s", p)
	}
	if isPackageDir(p) {
		return openTarget{dir: p}, nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return openTarget{}, newError(ErrIO, "无法读取文件夹 %s：%v", p, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && isPackageDir(filepath.Join(p, e.Name())) {
			found = append(found, filepath.Join(p, e.Name()))
		}
	}
	if len(found) == 1 {
		return openTarget{dir: found[0]}, nil
	}
	if len(found) > 1 {
		return openTarget{}, newError(ErrInvalid, "%s 中有 %d 个审图包文件夹，请打开其中一个", p, len(found))
	}
	return openTarget{}, newError(ErrInvalid, "%s 中没有 review.json，不是审图包文件夹", p)
}

// isPackageDir reports whether dir has review.json at its top.
func isPackageDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, reviewfile.ManifestFile))
	return err == nil && fi.Mode().IsRegular()
}
