package storagearchive

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func writeZipEntries(ctx context.Context, w io.Writer, srcRoot *os.Root, entries []sourceEntry) error {
	zw := zip.NewWriter(w)
	registerFastZipCompressor(zw)

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			_ = zw.Close()
			return err
		}
		if e.isDir {
			fh := &zip.FileHeader{
				Name:     e.rel + "/",
				Method:   zip.Store,
				Modified: e.modTime.UTC(),
			}
			fh.SetMode(fs.ModeDir | 0o755)
			if _, err := zw.CreateHeader(fh); err != nil {
				_ = zw.Close()
				return err
			}
			continue
		}

		f, err := srcRoot.Open(filepath.FromSlash(e.rel))
		if err != nil {
			_ = zw.Close()
			return err
		}
		fi, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			_ = zw.Close()
			return statErr
		}
		if !fi.Mode().IsRegular() {
			_ = f.Close()
			_ = zw.Close()
			return fmt.Errorf("%w: %q is no longer a regular file", ErrInvalidArchiveInput, e.rel)
		}

		fh := &zip.FileHeader{
			Name:     e.rel,
			Method:   ZipMethodForName(e.rel),
			Modified: fi.ModTime().UTC(),
		}
		fh.SetMode(e.mode.Perm())
		ew, err := zw.CreateHeader(fh)
		if err != nil {
			_ = f.Close()
			_ = zw.Close()
			return err
		}
		n, copyErr := io.CopyN(ew, f, fi.Size())
		_ = f.Close()
		if copyErr != nil {
			_ = zw.Close()
			return copyErr
		}
		if n != fi.Size() {
			_ = zw.Close()
			return fmt.Errorf("%w: short read packing %q", ErrInvalidArchiveInput, e.rel)
		}
	}

	return zw.Close()
}

func extractZip(ctx context.Context, archivePath, stageAbs string, stageRoot *os.Root, guard *unpackLimiter) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("%w: invalid zip archive: %v", ErrInvalidArchiveInput, err)
	}
	defer zr.Close()

	for _, zf := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := guard.countEntry(); err != nil {
			return err
		}

		mode := zf.Mode()
		if mode&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link %q not permitted in zip", ErrUnsafeArchiveEntry, zf.Name)
		}
		isDir := mode.IsDir() || strings.HasSuffix(zf.Name, "/")
		if !isDir && !mode.IsRegular() {
			return fmt.Errorf("%w: irregular zip entry %q (mode %v)", ErrUnsafeArchiveEntry, zf.Name, mode)
		}

		rawName := zf.Name
		if isDir {
			trimmed := strings.TrimSuffix(rawName, "/")
			if trimmed == "" || trimmed == "." || rawName == "./" {
				continue
			}
			cleanRel, err := CleanRelPath(trimmed)
			if err != nil {
				return err
			}
			if err := ensureParentDirsInRoot(stageRoot, cleanRel); err != nil {
				return err
			}
			if !zf.Modified.IsZero() {
				guard.dirTimes = append(guard.dirTimes, dirModTime{rel: cleanRel, modTime: zf.Modified.UTC()})
			}
			continue
		}

		cleanRel, err := CleanRelPath(rawName)
		if err != nil {
			return err
		}
		if path.Base(cleanRel) == keepMarkerFile {
			continue
		}
		parentRel := path.Dir(cleanRel)
		if parentRel != "." && parentRel != "" {
			if err := ensureParentDirsInRoot(stageRoot, parentRel); err != nil {
				return err
			}
		}

		if zf.UncompressedSize64 > uint64(guard.maxBytes) {
			return fmt.Errorf("%w: zip entry %q size %d exceeds limit %d", ErrArchiveTooLarge, cleanRel, zf.UncompressedSize64, guard.maxBytes)
		}
		declaredSize := int64(zf.UncompressedSize64)

		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: open zip entry %q: %v", ErrInvalidArchiveInput, cleanRel, err)
		}
		perm := mode.Perm()
		if perm&0o111 != 0 {
			perm = 0o755
		} else {
			perm = 0o644
		}
		osRel := filepath.FromSlash(cleanRel)
		out, err := stageRoot.OpenFile(osRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			_ = rc.Close()
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%w: duplicate zip entry %q", ErrUnsafeArchiveEntry, cleanRel)
			}
			return err
		}
		written, copyErr := guard.copyBounded(ctx, out, rc, declaredSize)
		rcErr := rc.Close()
		outErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if rcErr != nil {
			return fmt.Errorf("%w: corrupt zip entry %q: %v", ErrInvalidArchiveInput, cleanRel, rcErr)
		}
		if outErr != nil {
			return outErr
		}
		if written != declaredSize {
			return fmt.Errorf("%w: zip entry %q size mismatch (%d != %d)", ErrInvalidArchiveInput, cleanRel, written, declaredSize)
		}

		absFile := filepath.Join(stageAbs, osRel)
		if !zf.Modified.IsZero() {
			mt := zf.Modified.UTC()
			_ = os.Chtimes(absFile, mt, mt)
		}
		guard.filesCount++
		if guard.modTimes != nil {
			if st, stErr := stageRoot.Lstat(osRel); stErr == nil {
				guard.modTimes[cleanRel] = st.ModTime().UTC()
			}
		}
	}
	return nil
}
