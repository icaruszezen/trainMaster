package storagearchive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func writeTarZstEntries(ctx context.Context, w io.Writer, srcRoot *os.Root, entries []sourceEntry) error {
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			_ = tw.Close()
			_ = zw.Close()
			return err
		}
		if e.isDir {
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     e.rel + "/",
				Mode:     0o755,
				ModTime:  e.modTime.UTC(),
				Format:   tar.FormatPAX,
			}
			if err := tw.WriteHeader(hdr); err != nil {
				_ = tw.Close()
				_ = zw.Close()
				return err
			}
			continue
		}

		f, err := srcRoot.Open(filepath.FromSlash(e.rel))
		if err != nil {
			_ = tw.Close()
			_ = zw.Close()
			return err
		}
		fi, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			_ = tw.Close()
			_ = zw.Close()
			return statErr
		}
		if !fi.Mode().IsRegular() {
			_ = f.Close()
			_ = tw.Close()
			_ = zw.Close()
			return fmt.Errorf("%w: %q is no longer a regular file", ErrInvalidArchiveInput, e.rel)
		}

		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     e.rel,
			Size:     fi.Size(),
			Mode:     int64(e.mode.Perm()),
			ModTime:  fi.ModTime().UTC(),
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = f.Close()
			_ = tw.Close()
			_ = zw.Close()
			return err
		}
		n, copyErr := io.CopyN(tw, f, fi.Size())
		_ = f.Close()
		if copyErr != nil {
			_ = tw.Close()
			_ = zw.Close()
			return copyErr
		}
		if n != fi.Size() {
			_ = tw.Close()
			_ = zw.Close()
			return fmt.Errorf("%w: short read packing %q", ErrInvalidArchiveInput, e.rel)
		}
	}

	if err := tw.Close(); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

func extractTarZst(ctx context.Context, archivePath, stageAbs string, stageRoot *os.Root, guard *unpackLimiter) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: invalid zstd stream: %v", ErrInvalidArchiveEntryOrCorrupt(err), err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: corrupt tar stream: %v", ErrInvalidArchiveInput, err)
		}
		if err := guard.countEntry(); err != nil {
			return err
		}

		if hdr.Linkname != "" {
			return fmt.Errorf("%w: link target %q not permitted in archive", ErrUnsafeArchiveEntry, hdr.Linkname)
		}
		if paxPath, ok := hdr.PAXRecords["path"]; ok && paxPath != hdr.Name {
			return fmt.Errorf("%w: inconsistent PAX path header", ErrUnsafeArchiveEntry)
		}
		if _, hasLink := hdr.PAXRecords["linkpath"]; hasLink {
			return fmt.Errorf("%w: PAX linkpath not permitted", ErrUnsafeArchiveEntry)
		}

		rawName := hdr.Name
		switch hdr.Typeflag {
		case tar.TypeDir:
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
			if !hdr.ModTime.IsZero() {
				guard.dirTimes = append(guard.dirTimes, dirModTime{rel: cleanRel, modTime: hdr.ModTime.UTC()})
			}

		case tar.TypeReg, tar.TypeRegA:
			if strings.HasSuffix(rawName, "/") {
				return fmt.Errorf("%w: regular file entry %q ends with slash", ErrUnsafeArchiveEntry, rawName)
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
			perm := fs.FileMode(hdr.Mode) & 0o777
			if perm&0o111 != 0 {
				perm = 0o755
			} else {
				perm = 0o644
			}
			osRel := filepath.FromSlash(cleanRel)
			out, err := stageRoot.OpenFile(osRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				if errors.Is(err, fs.ErrExist) {
					return fmt.Errorf("%w: duplicate archive entry %q", ErrUnsafeArchiveEntry, cleanRel)
				}
				return err
			}
			written, copyErr := guard.copyBounded(ctx, out, tr, hdr.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written != hdr.Size {
				return fmt.Errorf("%w: truncated archive entry %q (%d != %d)", ErrInvalidArchiveInput, cleanRel, written, hdr.Size)
			}
			absFile := filepath.Join(stageAbs, osRel)
			if !hdr.ModTime.IsZero() {
				mt := hdr.ModTime.UTC()
				_ = os.Chtimes(absFile, mt, mt)
			}
			guard.filesCount++
			if guard.modTimes != nil {
				if st, stErr := stageRoot.Lstat(osRel); stErr == nil {
					guard.modTimes[cleanRel] = st.ModTime().UTC()
				}
			}

		default:
			return fmt.Errorf("%w: unsupported tar entry type %q for %q", ErrUnsafeArchiveEntry, hdr.Typeflag, rawName)
		}
	}
	return nil
}

// ErrInvalidArchiveEntryOrCorrupt maps low-level decode failures to ErrInvalidArchiveInput.
func ErrInvalidArchiveEntryOrCorrupt(_ error) error {
	return ErrInvalidArchiveInput
}
