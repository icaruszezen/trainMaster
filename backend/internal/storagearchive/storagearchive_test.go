package storagearchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

func TestPackAndUnpackTarZstRoundTripAndModTimes(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	zeroHeadroom := uint64(0)

	// Nested Unicode/Chinese paths, empty subdir, 0-byte file, binary & text files.
	mustMkdirAll(t, filepath.Join(src, "产线A/子目录_空"))
	mustMkdirAll(t, filepath.Join(src, "images/train"))
	mustMkdirAll(t, filepath.Join(src, "labels/train"))

	fixedMod := time.Date(2026, 5, 12, 8, 30, 45, 123456000, time.UTC)
	chtimesOK := writeFileWithTime(t, filepath.Join(src, "images/train/样品_001.jpg"), []byte("\xff\xd8\xff\xe0sample-jpeg-bytes"), fixedMod)
	writeFileWithTime(t, filepath.Join(src, "labels/train/样品_001.txt"), []byte("0 0.5 0.5 0.2 0.2\n"), fixedMod.Add(2*time.Second))
	writeFileWithTime(t, filepath.Join(src, "产线A/empty.txt"), nil, fixedMod.Add(5*time.Second))
	// Internal .tm-keep marker must be ignored during Pack.
	writeFileWithTime(t, filepath.Join(src, ".tm-keep"), nil, fixedMod)

	archivePath := filepath.Join(t.TempDir(), "bundle.tar.zst")
	packRes, err := Pack(ctx, PackOptions{
		SourceDir:       src,
		DestArchivePath: archivePath,
		Format:          FormatTarZst,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		t.Fatalf("Pack tar.zst failed: %v", err)
	}
	if packRes.FilesCount != 3 {
		t.Fatalf("FilesCount = %d, want 3", packRes.FilesCount)
	}
	wantRaw := int64(len("\xff\xd8\xff\xe0sample-jpeg-bytes") + len("0 0.5 0.5 0.2 0.2\n") + 0)
	if packRes.RawBytes != wantRaw {
		t.Fatalf("RawBytes = %d, want %d", packRes.RawBytes, wantRaw)
	}
	if !ValidSHA256Hex(packRes.SHA256) {
		t.Fatalf("invalid SHA256 %q", packRes.SHA256)
	}

	spaceDir := filepath.Join(t.TempDir(), "datasets")
	unpackRes, err := Unpack(ctx, UnpackOptions{
		ArchivePath:          archivePath,
		Format:               FormatTarZst,
		SpaceDir:             spaceDir,
		TargetRel:            "migrated-ds",
		Mode:                 ModeAtomic,
		ExpectedSHA256:       packRes.SHA256,
		ExpectedArchiveBytes: packRes.ArchiveBytes,
		DeclaredRawBytes:     &packRes.RawBytes,
		FreeHeadroom:         &zeroHeadroom,
		CollectModTimes:      true,
	})
	if err != nil {
		t.Fatalf("Unpack tar.zst failed: %v", err)
	}
	if unpackRes.FilesCount != 3 || unpackRes.RawBytes != wantRaw {
		t.Fatalf("unexpected UnpackResult: %+v", unpackRes)
	}

	dst := filepath.Join(spaceDir, "migrated-ds")
	st, err := os.Stat(filepath.Join(dst, "images/train/样品_001.jpg"))
	if err != nil {
		t.Fatalf("stat unpacked image: %v", err)
	}
	if chtimesOK && st.ModTime().UTC().UnixMicro() != fixedMod.UnixMicro() {
		t.Fatalf("modTime micro = %d, want %d", st.ModTime().UTC().UnixMicro(), fixedMod.UnixMicro())
	}
	if gotMT, ok := unpackRes.ModTimes["images/train/样品_001.jpg"]; !ok || gotMT.UnixMicro() != st.ModTime().UTC().UnixMicro() {
		t.Fatalf("ModTimes map entry = %v, want on-disk %v", gotMT, st.ModTime().UTC())
	}
	if stEmptyDir, err := os.Stat(filepath.Join(dst, "产线A/子目录_空")); err != nil || !stEmptyDir.IsDir() {
		t.Fatalf("expected empty subdirectory to be preserved, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".tm-keep")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".tm-keep should not be packed, stat err=%v", err)
	}
}

func TestPackZipCompressionMethodsAndSubsetPaths(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	zeroHeadroom := uint64(0)

	mustMkdirAll(t, filepath.Join(src, "images"))
	mustMkdirAll(t, filepath.Join(src, "labels"))
	mustWriteFile(t, filepath.Join(src, "images/a.jpg"), bytes.Repeat([]byte{0xab}, 512))
	mustWriteFile(t, filepath.Join(src, "images/b.png"), bytes.Repeat([]byte{0xcd}, 512))
	mustWriteFile(t, filepath.Join(src, "images/unused.webp"), []byte("unused"))
	mustWriteFile(t, filepath.Join(src, "labels/a.txt"), []byte(strings.Repeat("0 0.5 0.5 0.1 0.1\n", 40)))
	mustWriteFile(t, filepath.Join(src, "data.yaml"), []byte("train: train.txt\nval: val.txt\nnc: 1\nnames:\n  0: \"defect\"\n"))

	zipPath := filepath.Join(t.TempDir(), "subset.zip")
	packRes, err := Pack(ctx, PackOptions{
		SourceDir:       src,
		DestArchivePath: zipPath,
		Format:          FormatZip,
		Paths:           []string{"images/a.jpg", "images/b.png", "labels/a.txt", "data.yaml"},
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		t.Fatalf("Pack zip subset failed: %v", err)
	}
	if packRes.FilesCount != 4 {
		t.Fatalf("FilesCount = %d, want 4", packRes.FilesCount)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	methods := map[string]uint16{}
	for _, zf := range zr.File {
		if !zf.Mode().IsDir() {
			methods[zf.Name] = zf.Method
		}
	}
	_ = zr.Close()

	if methods["images/a.jpg"] != zip.Store || methods["images/b.png"] != zip.Store {
		t.Fatalf("expected compressed images to use zip.Store, got %+v", methods)
	}
	if methods["labels/a.txt"] != zip.Deflate || methods["data.yaml"] != zip.Deflate {
		t.Fatalf("expected text/yaml files to use zip.Deflate, got %+v", methods)
	}
	if _, hasUnused := methods["images/unused.webp"]; hasUnused {
		t.Fatalf("unexpected excluded file in subset archive")
	}

	spaceDir := filepath.Join(t.TempDir(), "exports")
	unpackRes, err := Unpack(ctx, UnpackOptions{
		ArchivePath:    zipPath,
		Format:         FormatZip,
		SpaceDir:       spaceDir,
		TargetRel:      "data-versions/project-1/v1",
		Mode:           ModeAtomic,
		ExpectedSHA256: packRes.SHA256,
		FreeHeadroom:   &zeroHeadroom,
	})
	if err != nil {
		t.Fatalf("Unpack zip failed: %v", err)
	}
	if unpackRes.FilesCount != 4 {
		t.Fatalf("unpack FilesCount = %d, want 4", unpackRes.FilesCount)
	}
}

func TestEmptyDirectoryPackAndUnpack(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	zeroHeadroom := uint64(0)

	for _, format := range []string{FormatTarZst, FormatZip} {
		archivePath := filepath.Join(t.TempDir(), "empty"+FormatExtension(format))
		res, err := Pack(ctx, PackOptions{
			SourceDir:       src,
			DestArchivePath: archivePath,
			Format:          format,
			FreeHeadroom:    &zeroHeadroom,
		})
		if err != nil {
			t.Fatalf("Pack empty (%s): %v", format, err)
		}
		if res.FilesCount != 0 || res.RawBytes != 0 {
			t.Fatalf("expected 0 files/bytes for empty dir (%s), got %+v", format, res)
		}

		spaceDir := t.TempDir()
		uRes, err := Unpack(ctx, UnpackOptions{
			ArchivePath:      archivePath,
			Format:           format,
			SpaceDir:         spaceDir,
			TargetRel:        "empty-target",
			Mode:             ModeAtomic,
			ExpectedSHA256:   res.SHA256,
			DeclaredRawBytes: &res.RawBytes,
			FreeHeadroom:     &zeroHeadroom,
		})
		if err != nil {
			t.Fatalf("Unpack empty (%s): %v", format, err)
		}
		if uRes.FilesCount != 0 {
			t.Fatalf("expected 0 unpacked files (%s), got %+v", format, uRes)
		}
		if st, err := os.Stat(filepath.Join(spaceDir, "empty-target")); err != nil || !st.IsDir() {
			t.Fatalf("expected empty target directory to exist (%s): %v", format, err)
		}
	}
}

func TestUnpackModesConflictAndMerge(t *testing.T) {
	ctx := context.Background()
	zeroHeadroom := uint64(0)

	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "shared.txt"), []byte("from-archive"))
	mustWriteFile(t, filepath.Join(src, "sub/new.txt"), []byte("brand-new"))

	arch := filepath.Join(t.TempDir(), "patch.tar.zst")
	packRes, err := Pack(ctx, PackOptions{
		SourceDir:       src,
		DestArchivePath: arch,
		Format:          FormatTarZst,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		t.Fatal(err)
	}

	spaceDir := t.TempDir()
	targetDir := filepath.Join(spaceDir, "ds1")
	mustMkdirAll(t, targetDir)
	mustWriteFile(t, filepath.Join(targetDir, "shared.txt"), []byte("original"))
	mustWriteFile(t, filepath.Join(targetDir, "keep.txt"), []byte("untouched"))

	// 1. ModeAtomic must fail with ErrTargetExists without altering existing files.
	_, err = Unpack(ctx, UnpackOptions{
		ArchivePath:    arch,
		Format:         FormatTarZst,
		SpaceDir:       spaceDir,
		TargetRel:      "ds1",
		Mode:           ModeAtomic,
		ExpectedSHA256: packRes.SHA256,
		FreeHeadroom:   &zeroHeadroom,
	})
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("expected ErrTargetExists, got %v", err)
	}
	if got := mustReadFile(t, filepath.Join(targetDir, "shared.txt")); string(got) != "original" {
		t.Fatalf("atomic conflict modified existing file: %q", got)
	}

	// 2. ModeMergeSkip keeps existing shared.txt and adds sub/new.txt.
	if _, err := Unpack(ctx, UnpackOptions{
		ArchivePath:    arch,
		Format:         FormatTarZst,
		SpaceDir:       spaceDir,
		TargetRel:      "ds1",
		Mode:           ModeMergeSkip,
		ExpectedSHA256: packRes.SHA256,
		FreeHeadroom:   &zeroHeadroom,
	}); err != nil {
		t.Fatalf("ModeMergeSkip failed: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(targetDir, "shared.txt")); string(got) != "original" {
		t.Fatalf("merge_skip should preserve existing file, got %q", got)
	}
	if got := mustReadFile(t, filepath.Join(targetDir, "sub/new.txt")); string(got) != "brand-new" {
		t.Fatalf("merge_skip did not add new file: %q", got)
	}

	// 3. ModeMergeOverwrite replaces shared.txt while keeping keep.txt.
	if _, err := Unpack(ctx, UnpackOptions{
		ArchivePath:    arch,
		Format:         FormatTarZst,
		SpaceDir:       spaceDir,
		TargetRel:      "ds1",
		Mode:           ModeMergeOverwrite,
		ExpectedSHA256: packRes.SHA256,
		FreeHeadroom:   &zeroHeadroom,
	}); err != nil {
		t.Fatalf("ModeMergeOverwrite failed: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(targetDir, "shared.txt")); string(got) != "from-archive" {
		t.Fatalf("merge_overwrite did not replace file, got %q", got)
	}
	if got := mustReadFile(t, filepath.Join(targetDir, "keep.txt")); string(got) != "untouched" {
		t.Fatalf("merge_overwrite disturbed unrelated file, got %q", got)
	}
}

func TestSecurityGuardsSlipSymlinkChecksumBombAndSpace(t *testing.T) {
	ctx := context.Background()
	zeroHeadroom := uint64(0)

	// 1. Source symlink rejected during Pack.
	srcWithLink := t.TempDir()
	mustWriteFile(t, filepath.Join(srcWithLink, "real.txt"), []byte("ok"))
	if err := os.Symlink("real.txt", filepath.Join(srcWithLink, "sym.txt")); err == nil {
		_, packErr := Pack(ctx, PackOptions{
			SourceDir:       srcWithLink,
			DestArchivePath: filepath.Join(t.TempDir(), "bad.tar.zst"),
			Format:          FormatTarZst,
			FreeHeadroom:    &zeroHeadroom,
		})
		if !errors.Is(packErr, ErrInvalidArchiveInput) {
			t.Fatalf("expected symlink in source to fail with ErrInvalidArchiveInput, got %v", packErr)
		}
	}

	// 2. Malicious tar.zst entries (../escape, /abs, symlink, hardlink).
	badTarCases := []struct {
		name string
		hdr  *tar.Header
		body []byte
	}{
		{
			name: "tar-slip-parent",
			hdr:  &tar.Header{Typeflag: tar.TypeReg, Name: "../escape.txt", Size: 4, Mode: 0o644},
			body: []byte("evil"),
		},
		{
			name: "tar-absolute",
			hdr:  &tar.Header{Typeflag: tar.TypeReg, Name: "/etc/passwd", Size: 4, Mode: 0o644},
			body: []byte("evil"),
		},
		{
			name: "tar-symlink",
			hdr:  &tar.Header{Typeflag: tar.TypeSymlink, Name: "link", Linkname: "/etc/hosts", Mode: 0o777},
		},
		{
			name: "tar-hardlink",
			hdr:  &tar.Header{Typeflag: tar.TypeLink, Name: "hlink", Linkname: "other", Mode: 0o644},
		},
	}

	for _, tc := range badTarCases {
		t.Run(tc.name, func(t *testing.T) {
			archPath, sum := writeRawTarZst(t, tc.hdr, tc.body)
			spaceDir := t.TempDir()
			_, err := Unpack(ctx, UnpackOptions{
				ArchivePath:    archPath,
				Format:         FormatTarZst,
				SpaceDir:       spaceDir,
				TargetRel:      "safe-target",
				Mode:           ModeAtomic,
				ExpectedSHA256: sum,
				FreeHeadroom:   &zeroHeadroom,
			})
			if !errors.Is(err, ErrUnsafeArchiveEntry) {
				t.Fatalf("expected ErrUnsafeArchiveEntry for %s, got %v", tc.name, err)
			}
			if _, statErr := os.Stat(filepath.Join(spaceDir, "safe-target")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("target dir must not be committed on unsafe archive")
			}
			entries, _ := os.ReadDir(spaceDir)
			if len(entries) != 0 {
				t.Fatalf("staging dirs must be cleaned up on failure, found %d entries", len(entries))
			}
		})
	}

	// 3. Zip-slip rejected.
	zipSlipPath, zipSlipSum := writeRawZip(t, "../../outside.txt", []byte("evil"), 0o644)
	spaceDir := t.TempDir()
	_, err := Unpack(ctx, UnpackOptions{
		ArchivePath:    zipSlipPath,
		Format:         FormatZip,
		SpaceDir:       spaceDir,
		TargetRel:      "zip-target",
		Mode:           ModeAtomic,
		ExpectedSHA256: zipSlipSum,
		FreeHeadroom:   &zeroHeadroom,
	})
	if !errors.Is(err, ErrUnsafeArchiveEntry) {
		t.Fatalf("expected ErrUnsafeArchiveEntry on zip-slip, got %v", err)
	}

	// 4. SHA-256 mismatch rejected strictly before unpack.
	validSrc := t.TempDir()
	mustWriteFile(t, filepath.Join(validSrc, "a.txt"), []byte("hello"))
	validArch := filepath.Join(t.TempDir(), "ok.tar.zst")
	packRes, err := Pack(ctx, PackOptions{
		SourceDir:       validSrc,
		DestArchivePath: validArch,
		Format:          FormatTarZst,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongSum := strings.Repeat("0", 64)
	_, err = Unpack(ctx, UnpackOptions{
		ArchivePath:    validArch,
		Format:         FormatTarZst,
		SpaceDir:       spaceDir,
		TargetRel:      "checksum-target",
		Mode:           ModeAtomic,
		ExpectedSHA256: wrongSum,
		FreeHeadroom:   &zeroHeadroom,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}

	// 5. Decompression bomb / DeclaredRawBytes ceiling enforced.
	bombSrc := t.TempDir()
	mustWriteFile(t, filepath.Join(bombSrc, "big.bin"), bytes.Repeat([]byte("A"), 4096))
	bombArch := filepath.Join(t.TempDir(), "bomb.tar.zst")
	bombPack, err := Pack(ctx, PackOptions{
		SourceDir:       bombSrc,
		DestArchivePath: bombArch,
		Format:          FormatTarZst,
		FreeHeadroom:    &zeroHeadroom,
	})
	if err != nil {
		t.Fatal(err)
	}
	smallCap := int64(512)
	_, err = Unpack(ctx, UnpackOptions{
		ArchivePath:      bombArch,
		Format:           FormatTarZst,
		SpaceDir:         spaceDir,
		TargetRel:        "bomb-target",
		Mode:             ModeAtomic,
		ExpectedSHA256:   bombPack.SHA256,
		DeclaredRawBytes: &smallCap,
		FreeHeadroom:     &zeroHeadroom,
	})
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("expected ErrArchiveTooLarge, got %v", err)
	}

	// 6. Insufficient disk space returns ErrInsufficientStorage.
	_, err = Unpack(ctx, UnpackOptions{
		ArchivePath:    validArch,
		Format:         FormatTarZst,
		SpaceDir:       spaceDir,
		TargetRel:      "nospace-target",
		Mode:           ModeAtomic,
		ExpectedSHA256: packRes.SHA256,
		DiskUsage: func(string) (storage.Usage, error) {
			return storage.Usage{TotalBytes: 100 << 20, FreeBytes: 1024, UsedBytes: (100 << 20) - 1024}, nil
		},
	})
	if !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("expected ErrInsufficientStorage, got %v", err)
	}
}

func writeRawTarZst(t *testing.T, hdr *tar.Header, body []byte) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "raw.tar.zst")
	mustWriteFile(t, p, buf.Bytes())
	sum := sha256.Sum256(buf.Bytes())
	return p, hex.EncodeToString(sum[:])
}

func writeRawZip(t *testing.T, entryName string, body []byte, mode fs.FileMode) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: entryName, Method: zip.Deflate}
	fh.SetMode(mode)
	ew, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ew.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "raw.zip")
	mustWriteFile(t, p, buf.Bytes())
	sum := sha256.Sum256(buf.Bytes())
	return p, hex.EncodeToString(sum[:])
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFileWithTime(t *testing.T, path string, data []byte, mt time.Time) bool {
	t.Helper()
	mustWriteFile(t, path, data)
	return os.Chtimes(path, mt, mt) == nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
