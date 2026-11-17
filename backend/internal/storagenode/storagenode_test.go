package storagenode

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode/s3test"
)

func TestCleanRelPath(t *testing.T) {
	good := map[string]string{
		"a.jpg":           "a.jpg",
		"sub/dir/img.png": "sub/dir/img.png",
		`sub\dir\img.png`: "sub/dir/img.png",
		"a/b/../c.xml":    "a/c.xml",
	}
	for in, want := range good {
		got, err := CleanRelPath(in)
		if err != nil || got != want {
			t.Errorf("CleanRelPath(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}

	bad := []string{
		"",
		".",
		"..",
		"../secret",
		"a/../../secret",
		"/abs/path.jpg",
		`\abs\path.jpg`,
		"C:/win/path.jpg",
		"d:\\win\\path.jpg",
		"bad\x00name.jpg",
		string([]byte{0xff, 0xfe}),
	}
	for _, in := range bad {
		if got, err := CleanRelPath(in); err == nil {
			t.Errorf("CleanRelPath(%q) = %q, nil; want error", in, got)
		}
	}
}

func TestCipherBoxRoundTrip(t *testing.T) {
	ctx := context.Background()
	box := newCipherBox("unit-test-secret-key-0123456789", "", nil)

	in := Credentials{SecretAccessKey: "sk-abc-123", Password: "smb-password"}
	enc, err := box.encrypt(ctx, in)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("expected non-empty ciphertext")
	}
	out, err := box.decrypt(ctx, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if out != in {
		t.Fatalf("decrypt = %+v, want %+v", out, in)
	}

	// Wrong key must fail authentication.
	other := newCipherBox("different-secret-key-9876543210", "", nil)
	if _, err := other.decrypt(ctx, enc); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestLocalVolumeLifecycle(t *testing.T) {
	ctx := context.Background()
	vol, err := NewLocalVolume(1, true, t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalVolume: %v", err)
	}
	defer vol.Close()

	if err := vol.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	u, err := vol.Usage(ctx)
	if err != nil || u.TotalBytes == 0 {
		t.Fatalf("Usage = %+v, %v", u, err)
	}

	if err := vol.MkdirDir(ctx, SpaceDatasets, "bolts", true); err != nil {
		t.Fatalf("MkdirDir: %v", err)
	}
	if err := vol.MkdirDir(ctx, SpaceDatasets, "bolts", true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second exclusive MkdirDir = %v, want fs.ErrExist", err)
	}

	dir, err := vol.OpenDir(ctx, SpaceDatasets, "bolts")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer dir.Close()

	payload := []byte("hello-local-image-bytes")
	fi, err := dir.PutFile(ctx, "train/001.jpg", bytes.NewReader(payload), int64(len(payload)), 0o644)
	if err != nil || fi.Size() != int64(len(payload)) {
		t.Fatalf("PutFile = %v, %v", fi, err)
	}
	if err := dir.CreateExclusive(ctx, "train/001.xml", []byte("<annotation/>"), 0o644); err != nil {
		t.Fatalf("CreateExclusive: %v", err)
	}
	if err := dir.CreateExclusive(ctx, "train/001.xml", []byte("<annotation/>"), 0o644); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate CreateExclusive = %v, want fs.ErrExist", err)
	}

	// CopyFileWithin should create a hardlink on the same local filesystem.
	li, linked, err := vol.CopyFileWithin(ctx,
		SpaceDatasets, "bolts/train/001.jpg",
		SpaceExports, "data-versions/project-1/v1/images/bolts/001.jpg",
		os.Link)
	if err != nil || !linked || li.Size() != int64(len(payload)) {
		t.Fatalf("CopyFileWithin = %v, linked=%v, err=%v", li, linked, err)
	}
}

func TestS3VolumeLifecycleAndMultipart(t *testing.T) {
	ctx := context.Background()
	srv := s3test.NewServer("tm-datasets")
	defer srv.Close()

	vol, err := NewS3Volume(2, S3Config{
		Endpoint:       srv.URL,
		Region:         "us-east-1",
		Bucket:         "tm-datasets",
		Prefix:         "prod",
		AccessKeyID:    srv.AccessKey,
		ForcePathStyle: true,
	}, srv.SecretKey)
	if err != nil {
		t.Fatalf("NewS3Volume: %v", err)
	}
	defer vol.Close()

	if err := vol.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := vol.MkdirDir(ctx, SpaceDatasets, "pcb", true); err != nil {
		t.Fatalf("MkdirDir: %v", err)
	}
	exists, _, err := vol.DirExists(ctx, SpaceDatasets, "pcb")
	if err != nil || !exists {
		t.Fatalf("DirExists = %v, %v", exists, err)
	}

	dir, err := vol.OpenDir(ctx, SpaceDatasets, "pcb")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer dir.Close()

	// 1. Upload a multi-part object (> 16 MiB) to exercise S3 Multipart Upload,
	// initial 64 KiB prefix cache, streaming Read continuation, and random ReadAt.
	large := make([]byte, s3MultipartPartSize+512*1024)
	if _, err := io.ReadFull(rand.Reader, large); err != nil {
		t.Fatal(err)
	}
	fi, err := dir.PutFile(ctx, "hd/frame_001.bmp", bytes.NewReader(large), int64(len(large)), 0o644)
	if err != nil || fi.Size() != int64(len(large)) {
		t.Fatalf("PutFile large = %v, %v", fi, err)
	}
	if err := dir.PutBytes(ctx, ".git/config", []byte("ignored"), 0o644); err != nil {
		t.Fatalf("PutBytes hidden: %v", err)
	}

	f, err := dir.Open("hd/frame_001.bmp")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Random ReadAt near the end of the 16.5 MiB object (well past 64 KiB cache).
	tail := make([]byte, 128)
	tailOff := int64(len(large) - 128)
	if n, err := f.ReadAt(tail, tailOff); err != nil || n != 128 || !bytes.Equal(tail, large[tailOff:]) {
		t.Fatalf("ReadAt tail n=%d err=%v match=%v", n, err, bytes.Equal(tail, large[tailOff:]))
	}
	// Full streaming Read from offset 0 across the 64 KiB prefix boundary.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	gotAll, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(gotAll, large) {
		t.Fatalf("ReadAll len=%d err=%v match=%v", len(gotAll), err, bytes.Equal(gotAll, large))
	}
	_ = f.Close()

	// WalkFiles should skip .tm-keep and allow filepath.SkipDir on .git.
	var walked []string
	err = dir.WalkFiles(ctx, func(rel string, info fs.FileInfo, isDir bool) error {
		if isDir && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !isDir {
			walked = append(walked, rel)
		}
		return nil
	})
	if err != nil || len(walked) != 1 || walked[0] != "hd/frame_001.bmp" {
		t.Fatalf("WalkFiles = %v, err=%v", walked, err)
	}

	// CopyFileWithin uses server-side CopyObject.
	cfi, linked, err := vol.CopyFileWithin(ctx,
		SpaceDatasets, "pcb/hd/frame_001.bmp",
		SpaceExports, "data-versions/project-1/v1/images/pcb/frame_001.bmp",
		nil)
	if err != nil || linked || cfi.Size() != int64(len(large)) {
		t.Fatalf("CopyFileWithin S3 = %v, linked=%v, err=%v", cfi, linked, err)
	}

	if err := vol.RemoveDirAll(ctx, SpaceDatasets, "pcb"); err != nil {
		t.Fatalf("RemoveDirAll: %v", err)
	}
	exists, _, err = vol.DirExists(ctx, SpaceDatasets, "pcb")
	if err != nil || exists {
		t.Fatalf("after RemoveDirAll DirExists = %v, %v", exists, err)
	}
}

func TestServiceNodesAndMigrationIntegration(t *testing.T) {
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	svc := NewService(Options{
		DB:        db.Pool,
		Storage:   st,
		SecretKey: "integration-test-secret-key-1234",
	})
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop()

	// 1. Seed a dataset on the built-in local node (id=1).
	dsDir := filepath.Join(st.Dir(storage.Datasets), "steel-defects")
	if err := os.MkdirAll(dsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgBytes := []byte("fake-jpeg-payload-for-migration-test")
	if err := os.WriteFile(filepath.Join(dsDir, "001.jpg"), imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var dsID int64
	if err := db.QueryRow(ctx, `INSERT INTO datasets (name, dir_name, origin, image_count, total_bytes, storage_node_id)
		VALUES ('钢材缺陷集', 'steel-defects', 'upload', 1, $1, 1) RETURNING id`, len(imgBytes)).Scan(&dsID); err != nil {
		t.Fatal(err)
	}

	// 2. Create and activate an S3 storage node.
	s3Srv := s3test.NewServer("remote-bucket")
	defer s3Srv.Close()

	s3Node, err := svc.CreateNode(ctx, CreateNodeInput{
		Name: "远程 MinIO 节点",
		Kind: KindS3,
		S3: &S3Config{
			Endpoint:       s3Srv.URL,
			Bucket:         "remote-bucket",
			Prefix:         "tm",
			AccessKeyID:    s3Srv.AccessKey,
			ForcePathStyle: true,
		},
		SecretAccessKey: s3Srv.SecretKey,
		Activate:        true,
	})
	if err != nil {
		t.Fatalf("CreateNode S3: %v", err)
	}
	if !s3Node.IsActive || !s3Node.HasSecret {
		t.Fatalf("unexpected S3 node state: %+v", s3Node)
	}

	// 3. Migratable items should list the local dataset.
	migratable, err := svc.ListMigratableItems(ctx)
	if err != nil || len(migratable) != 1 || migratable[0].TargetID != dsID {
		t.Fatalf("ListMigratableItems = %+v, %v", migratable, err)
	}

	// 4. Queue online migration with default delete_source = true.
	migs, err := svc.CreateMigrations(ctx, CreateMigrationsInput{
		Targets: []MigrationTarget{{TargetType: TargetDataset, TargetID: dsID}},
	})
	if err != nil || len(migs) != 1 {
		t.Fatalf("CreateMigrations = %+v, %v", migs, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := svc.WaitMigrationsIdle(waitCtx); err != nil {
		t.Fatalf("WaitMigrationsIdle: %v", err)
	}

	// 5. Verify DB switched to s3Node.ID, S3 has the file, and local source dir was cleaned!
	var newNodeID int64
	if err := db.QueryRow(ctx, "SELECT storage_node_id FROM datasets WHERE id = $1", dsID).Scan(&newNodeID); err != nil {
		t.Fatal(err)
	}
	if newNodeID != s3Node.ID {
		t.Fatalf("dataset storage_node_id = %d, want %d", newNodeID, s3Node.ID)
	}
	if _, err := os.Stat(dsDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected local source folder %s to be deleted after migration, stat err=%v", dsDir, err)
	}
	if s3Srv.ObjectCount("remote-bucket", "tm/datasets/steel-defects/001.jpg") != 1 {
		t.Fatalf("expected migrated object in S3 bucket")
	}
}
