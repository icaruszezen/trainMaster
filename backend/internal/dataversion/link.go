package dataversion

import (
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
	"syscall"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/augment"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

// Storing the originals: a hard link to the dataset file when possible
// (no extra space; imports replace dataset files by rename, so the link
// keeps the content it was made with), else a copy. A JPEG or TIFF whose
// orientation tag is not 1 is always copied, with the tag reset: its
// boxes are in the stored pixel grid, but training tools apply the tag.

// Free space guard of the files a build writes (copies and augmented
// images): the free space is read before the first one and again every
// spaceRecheck bytes; spaceReserve is always left.
const (
	spaceReserve = 1 << 30
	spaceRecheck = 256 << 20
)

// spaceGuard stops a build before it fills the disk.
type spaceGuard struct {
	usage       func() (storage.Usage, error)
	checked     bool
	unsupported bool
	since       int64 // bytes written since the last check
	warn        func(err error)
}

// due reports whether the next before checks the free space.
func (g *spaceGuard) due() bool { return !g.unsupported && (!g.checked || g.since >= spaceRecheck) }

// before checks that need more bytes fit, when a check is due.
func (g *spaceGuard) before(need int64) error {
	if !g.due() {
		return nil
	}
	u, err := g.usage()
	if errors.Is(err, storage.ErrUsageUnsupported) {
		g.unsupported = true
		return nil
	}
	if err != nil {
		if g.warn != nil {
			g.warn(err)
		}
		return nil
	}
	g.checked, g.since = true, 0
	if want := uint64(max(need, 0)) + spaceReserve; u.FreeBytes < want {
		return newError(ErrInsufficientStorage, "磁盘空间不足：生成数据版本还需要约 %s，可用 %s",
			formatSize(want), formatSize(u.FreeBytes))
	}
	return nil
}

// force makes the next before check the free space.
func (g *spaceGuard) force() { g.checked = false }

func (g *spaceGuard) wrote(n int64) { g.since += n }

// formatSize renders a byte count for messages, e.g. "1.5 GB".
func formatSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, i := float64(n)/unit, 0
	for value >= unit && i < 4 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, []string{"KB", "MB", "GB", "TB", "PB"}[i])
}

// errSourceChanged and errSourceMissing: the dataset file of an original
// changed or disappeared after it was prepared.
var (
	errSourceChanged = errors.New("dataset file changed")
	errSourceMissing = errors.New("dataset file missing")
)

// sameFileTime reports whether a file's modification time is t as the
// dataset index stores it (microseconds).
func sameFileTime(mod, t time.Time) bool { return time.UnixMicro(mod.UnixMicro()).Equal(t) }

// dbTime is t as PostgreSQL stores it (microseconds, UTC).
func dbTime(t time.Time) time.Time { return time.UnixMicro(t.UnixMicro()).UTC() }

// hasOrientationTag reports whether files with the extension of rel can
// carry an EXIF / TIFF orientation tag.
func hasOrientationTag(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".tif", ".tiff":
		return true
	}
	return false
}

// storedFile is how an original was stored.
type storedFile struct {
	stored           string // StoredLink or StoredCopy
	orientationFixed bool
	copied           int64 // bytes copied
	size             int64 // the stored file
	modTime          time.Time
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += int64(k)
	return k, err
}

// storeOriginal stores the original img at its version path: opened
// through the dataset's root, checked against what was prepared, then
// linked (verified to be the same file), server-side copied, or streamed.
// errSourceChanged or errSourceMissing: the file is no longer what was prepared.
func (b *build) storeOriginal(img *buildImage) (storedFile, error) {
	var out storedFile
	snap := b.snaps[img.source]
	root := snap.Root
	src, err := root.Open(img.rel)
	if err != nil {
		return out, errSourceMissing
	}
	defer src.Close()
	fi, err := src.Stat()
	switch {
	case err != nil || !fi.Mode().IsRegular():
		return out, errSourceMissing
	case fi.Size() != img.sourceSize || (!b.prefetched[img.source] && !sameFileTime(fi.ModTime(), img.sourceMod)):
		return out, errSourceChanged
	}
	useDockerStage := b.vol != nil && b.vol.Kind() == storagenode.KindDocker && b.s.storageNodes != nil
	if !useDockerStage {
		if err := b.mkdirFor(img.path); err != nil {
			return out, err
		}
	}

	var orientation *augment.Orientation
	if hasOrientationTag(img.rel) {
		o, err := b.s.orientation(src, fi.Size())
		if err != nil {
			return out, fmt.Errorf("read the orientation of %s: %w", img.rel, err)
		}
		if o.Value > 1 {
			orientation = &o
		}
	}

	sameNode := snap.Dataset.StorageNodeID == b.job.StorageNodeID
	if !sameNode {
		b.crossDevice[img.source] = true
	}

	if orientation == nil && sameNode {
		if b.verDir.LocalDir() != "" && root.LocalDir() != "" {
			dst := filepath.Join(b.verDir.LocalDir(), filepath.FromSlash(img.path))
			linkErr := b.s.link(filepath.Join(root.LocalDir(), filepath.FromSlash(img.rel)), dst)
			if linkErr == nil {
				if li, err := os.Lstat(dst); err == nil && os.SameFile(fi, li) {
					out.stored, out.size, out.modTime = StoredLink, li.Size(), li.ModTime()
					return out, nil
				}
				linkErr = errors.New("the link is not the dataset file")
			}
			if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return out, fmt.Errorf("remove failed link: %w", err)
			}
			b.linkFailed(img, linkErr)
		} else {
			if err := b.space.before(fi.Size()); err != nil {
				return out, err
			}
			st, linked, copyErr := b.vol.CopyFileWithin(
				b.ctx,
				storagenode.SpaceDatasets, snap.Dataset.DirName+"/"+img.rel,
				storagenode.SpaceExports, b.job.DirName+"/"+img.path,
				nil,
			)
			if copyErr == nil {
				if linked {
					return storedFile{stored: StoredLink, copied: 0, size: st.Size(), modTime: st.ModTime()}, nil
				}
				b.space.wrote(st.Size())
				return storedFile{stored: StoredCopy, copied: st.Size(), size: st.Size(), modTime: st.ModTime()}, nil
			}
		}
	}

	need := fi.Size()
	if b.crossDevice[img.source] && b.space.due() { // only then: remainingBytes walks the rest
		need = b.remainingBytes(img)
	}
	if err := b.space.before(need); err != nil {
		return out, err
	}

	if b.verDir.LocalDir() != "" {
		dst := filepath.Join(b.verDir.LocalDir(), filepath.FromSlash(img.path))
		n, err := copyFileFrom(src, fi.Size(), dst, orientation)
		if err != nil {
			return out, err
		}
		b.space.wrote(n)
		st, err := os.Lstat(dst)
		if err != nil {
			return out, err
		}
		return storedFile{stored: StoredCopy, orientationFixed: orientation != nil, copied: n, size: st.Size(),
			modTime: st.ModTime()}, nil
	}

	if useDockerStage {
		stageRoot, serr := b.ensureDockerStageDir()
		if serr == nil {
			dst := filepath.Join(stageRoot, filepath.FromSlash(img.path))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return out, fmt.Errorf("copy image: %w", err)
			}
			n, err := copyFileFrom(src, fi.Size(), dst, orientation)
			if err != nil {
				return out, err
			}
			b.space.wrote(n)
			st, err := os.Lstat(dst)
			if err != nil {
				return out, err
			}
			return storedFile{stored: StoredCopy, orientationFixed: orientation != nil, copied: n, size: st.Size(),
				modTime: st.ModTime()}, nil
		}
	}

	if err := b.mkdirFor(img.path); err != nil {
		return out, err
	}

	if orientation == nil {
		cr := &countingReader{r: io.NewSectionReader(src, 0, fi.Size())}
		st, err := b.verDir.PutFile(b.ctx, img.path, cr, fi.Size(), 0o644)
		if err != nil {
			return out, fmt.Errorf("copy image: %w", err)
		}
		if cr.n != fi.Size() {
			_ = b.verDir.RemoveFile(b.ctx, img.path)
			return out, errSourceChanged
		}
		b.space.wrote(cr.n)
		return storedFile{stored: StoredCopy, copied: cr.n, size: st.Size(), modTime: st.ModTime()}, nil
	}

	tmpDir := b.s.storage.Dir(storage.Tmp)
	_ = os.MkdirAll(tmpDir, 0o755)
	tmp, err := os.CreateTemp(tmpDir, ".dv-orient-*")
	if err != nil {
		return out, fmt.Errorf("copy image: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	n, err := io.Copy(tmp, io.NewSectionReader(src, 0, fi.Size()))
	if err == nil && n != fi.Size() {
		err = errSourceChanged
	}
	if err == nil {
		err = augment.NeutralizeOrientation(tmp, *orientation)
	}
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		if errors.Is(err, errSourceChanged) {
			return out, err
		}
		return out, fmt.Errorf("copy image: %w", err)
	}
	tmpInfo, err := tmp.Stat()
	if err != nil {
		return out, fmt.Errorf("copy image: %w", err)
	}
	st, err := b.verDir.PutFile(b.ctx, img.path, tmp, tmpInfo.Size(), 0o644)
	if err != nil {
		return out, fmt.Errorf("copy image: %w", err)
	}
	b.space.wrote(tmpInfo.Size())
	return storedFile{stored: StoredCopy, orientationFixed: true, copied: tmpInfo.Size(), size: st.Size(),
		modTime: st.ModTime()}, nil
}

// linkFailed records why a link failed (logged once per source).
func (b *build) linkFailed(img *buildImage, err error) {
	if errors.Is(err, syscall.EXDEV) {
		b.crossDevice[img.source] = true
	}
	if !b.linkWarned[img.source] {
		b.linkWarned[img.source] = true
		b.log.Info("data version: cannot hard link the dataset files, copying them", "dataset_id",
			b.job.Sources[img.source].DatasetID, "path", img.rel, "err", err)
	}
}

// remainingBytes is the size of the originals of img's source from img on
// (a dataset on another file system: all of them are copied).
func (b *build) remainingBytes(img *buildImage) int64 {
	var n int64
	for _, o := range b.images[img.index:] {
		if o.source == img.source {
			n += o.sourceSize
		}
	}
	return n
}

// copyFileFrom copies size bytes of src to a temporary file next to dst,
// resets the orientation tag in it (when o is set) and renames it to dst.
func copyFileFrom(src io.ReaderAt, size int64, dst string, o *augment.Orientation) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return 0, fmt.Errorf("copy image: %w", err)
	}
	name := tmp.Name()
	// CreateTemp makes the file 0600; the version folder is read by other
	// users too (training nodes over a shared mount, backups). Ensure we ignore
	// EPERM on FUSE scratch mounts that already default to 0777.
	_ = tmp.Chmod(0o644)
	var n int64
	if err == nil {
		n, err = io.Copy(tmp, io.NewSectionReader(src, 0, size))
	}
	if err == nil && n != size {
		err = errSourceChanged
	}
	if err == nil && o != nil {
		err = augment.NeutralizeOrientation(tmp, *o)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, dst)
	}
	if err != nil {
		_ = os.Remove(name)
		if errors.Is(err, errSourceChanged) {
			return n, err
		}
		return n, fmt.Errorf("copy image: %w", err)
	}
	return n, nil
}

// mkdirFor creates the folder of the file at rel (once per folder).
func (b *build) mkdirFor(rel string) error {
	dir := path.Dir(rel)
	if dir == "." || dir == "" || b.dirs[dir] {
		return nil
	}
	if err := b.verDir.EnsureDir(dir); err != nil {
		return fmt.Errorf("create folder in the data version: %w", err)
	}
	b.dirs[dir] = true
	return nil
}

// maybePrefetchSnapshot bulk-fetches a cross-node KindDocker dataset snapshot
// via a single .tar.zst stream so prepare() and linkImages() read locally
// without N separate HTTP round-trips.
func (b *build) maybePrefetchSnapshot(sourceIdx int, snap *dataset.Snapshot) {
	if b.s.storageNodes == nil || snap == nil || snap.Root == nil || snap.Root.LocalDir() != "" || len(snap.Images) == 0 {
		return
	}
	if !b.s.storageNodes.CanArchiveBetweenIDs(b.ctx, snap.Dataset.StorageNodeID, b.job.StorageNodeID) {
		return
	}
	_, srcArch, err := b.s.storageNodes.ArchiveEndpointForNode(b.ctx, snap.Dataset.StorageNodeID)
	if err != nil {
		return
	}

	seen := make(map[string]bool, len(snap.Images)*2)
	paths := make([]string, 0, len(snap.Images)*2)
	for _, img := range snap.Images {
		if img.RelPath != "" && !seen[img.RelPath] {
			seen[img.RelPath] = true
			paths = append(paths, img.RelPath)
		}
		if img.AnnotationPath != nil && *img.AnnotationPath != "" && !seen[*img.AnnotationPath] {
			seen[*img.AnnotationPath] = true
			paths = append(paths, *img.AnnotationPath)
		}
	}
	if len(paths) == 0 {
		return
	}

	packed, err := srcArch.PackArchive(b.ctx, storagenode.PackRequest{
		Space:  storagenode.SpaceDatasets,
		DirRel: snap.Dataset.DirName,
		Format: storagearchive.FormatTarZst,
		Paths:  paths,
	})
	if err != nil {
		b.log.Warn("data version archive prefetch pack fallback to direct",
			"dataset_id", snap.Dataset.ID, "err", err)
		return
	}
	defer func() {
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srcArch.DeleteArchive(delCtx, packed.ArchiveID)
	}()

	tmpBase := b.s.storage.Dir(storage.Tmp)
	_ = os.MkdirAll(tmpBase, 0o755)
	stageParent := filepath.Join(tmpBase, fmt.Sprintf(".dv-prefetch-%d-%d-%s", b.job.ID, snap.Dataset.ID, strings.ToLower(rand.Text()[:8])))
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return
	}

	rc, err := srcArch.OpenArchiveStream(b.ctx, packed.ArchiveID)
	if err != nil {
		_ = os.RemoveAll(stageParent)
		return
	}
	archFile := filepath.Join(stageParent, "src.tar.zst")
	f, err := os.OpenFile(archFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = rc.Close()
		_ = os.RemoveAll(stageParent)
		return
	}
	_, copyErr := io.Copy(f, rc)
	_ = rc.Close()
	if cerr := f.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.RemoveAll(stageParent)
		return
	}

	zeroHeadroom := uint64(0)
	_, err = storagearchive.Unpack(b.ctx, storagearchive.UnpackOptions{
		ArchivePath:          archFile,
		Format:               storagearchive.FormatTarZst,
		SpaceDir:             stageParent,
		TargetRel:            "files",
		Mode:                 storagearchive.ModeAtomic,
		ExpectedSHA256:       packed.SHA256,
		ExpectedArchiveBytes: packed.ArchiveBytes,
		DeclaredRawBytes:     &packed.RawBytes,
		FreeHeadroom:         &zeroHeadroom,
	})
	_ = os.Remove(archFile)
	if err != nil {
		_ = os.RemoveAll(stageParent)
		b.log.Warn("data version archive prefetch unpack fallback to direct",
			"dataset_id", snap.Dataset.ID, "err", err)
		return
	}

	localRoot, err := storagenode.OpenLocalDir(filepath.Join(stageParent, "files"))
	if err != nil {
		_ = os.RemoveAll(stageParent)
		return
	}
	_ = snap.Root.Close()
	snap.Root = localRoot
	b.prefetched[sourceIdx] = true
	b.prefetchDirs = append(b.prefetchDirs, stageParent)
}

func (b *build) cleanupPrefetchDirs() {
	b.dockerStageMu.Lock()
	stageDir := b.dockerStageDir
	b.dockerStageDir = ""
	b.dockerStageMu.Unlock()
	if stageDir != "" {
		_ = os.RemoveAll(stageDir)
	}
	for _, dir := range b.prefetchDirs {
		_ = os.RemoveAll(dir)
	}
	b.prefetchDirs = nil
}

func (b *build) ensureDockerStageDir() (string, error) {
	b.dockerStageMu.Lock()
	defer b.dockerStageMu.Unlock()
	if b.dockerStageDir != "" {
		return b.dockerStageDir, nil
	}
	tmpBase := b.s.storage.Dir(storage.Tmp)
	if err := os.MkdirAll(tmpBase, 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join(tmpBase, fmt.Sprintf(".dv-outstage-%d-%s", b.job.ID, strings.ToLower(rand.Text()[:8])))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b.dockerStageDir = dir
	return dir, nil
}

func (b *build) stageDockerBytes(rel string, data []byte) (bool, error) {
	ok, _, err := b.stageDockerBytesWithModTime(rel, data, false)
	return ok, err
}

func (b *build) stageDockerBytesWithModTime(rel string, data []byte, exclusive bool) (bool, time.Time, error) {
	if b.vol == nil || b.vol.Kind() != storagenode.KindDocker || b.s.storageNodes == nil {
		return false, time.Time{}, nil
	}
	stageRoot, err := b.ensureDockerStageDir()
	if err != nil {
		return false, time.Time{}, nil
	}
	dst := filepath.Join(stageRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, time.Time{}, err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return false, time.Time{}, err
	}
	_, writeErr := f.Write(data)
	if cerr := f.Close(); writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = os.Remove(dst)
		return false, time.Time{}, writeErr
	}
	fi, err := os.Lstat(dst)
	if err != nil {
		return false, time.Time{}, err
	}
	return true, fi.ModTime(), nil
}

func (b *build) flushDockerStage(batch []*buildImage) error {
	summary, err := b.flushDockerStageSummary()
	if err != nil || summary == nil {
		return err
	}
	for _, img := range batch {
		if mt, ok := summary.ModTimes[img.path]; ok {
			img.modTime = dbTime(mt)
		}
	}
	return nil
}

func (b *build) flushDockerStageSummary() (*storagenode.UnpackSummary, error) {
	b.dockerStageMu.Lock()
	stageDir := b.dockerStageDir
	b.dockerStageDir = ""
	b.dockerStageMu.Unlock()
	if stageDir == "" {
		return nil, nil
	}
	defer func() {
		_ = os.RemoveAll(stageDir)
	}()

	_, dstArch, err := b.s.storageNodes.ArchiveEndpointForNode(b.ctx, b.job.StorageNodeID)
	if err != nil {
		return nil, fmt.Errorf("open destination docker archive endpoint: %w", err)
	}

	tmpBase := b.s.storage.Dir(storage.Tmp)
	archPath := filepath.Join(tmpBase, fmt.Sprintf(".dv-outpack-%d-%s.tar.zst", b.job.ID, strings.ToLower(rand.Text()[:8])))
	defer func() { _ = os.Remove(archPath) }()

	zeroHeadroom := uint64(0)
	packed, err := storagearchive.Pack(b.ctx, storagearchive.PackOptions{
		SourceDir:       stageDir,
		DestArchivePath: archPath,
		Format:          storagearchive.FormatTarZst,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		return nil, fmt.Errorf("pack staged data version batch: %w", err)
	}
	if packed.FilesCount == 0 {
		return nil, nil
	}

	f, err := os.Open(archPath)
	if err != nil {
		return nil, err
	}
	summary, unpackErr := dstArch.UnpackArchiveStream(b.ctx, storagenode.UnpackStreamRequest{
		Space:                storagenode.SpaceExports,
		DirRel:               b.job.DirName,
		Format:               storagearchive.FormatTarZst,
		Mode:                 storagearchive.ModeMergeOverwrite,
		ExpectedSHA256:       packed.SHA256,
		ExpectedArchiveBytes: packed.ArchiveBytes,
		DeclaredRawBytes:     &packed.RawBytes,
		IncludeModTimes:      true,
	}, f)
	_ = f.Close()
	if unpackErr != nil {
		if errors.Is(unpackErr, storagearchive.ErrInsufficientStorage) {
			return nil, newError(ErrInsufficientStorage, "目标存储节点磁盘空间不足")
		}
		return nil, fmt.Errorf("upload staged data version batch: %w", unpackErr)
	}
	return &summary, nil
}
