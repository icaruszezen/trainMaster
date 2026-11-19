package storagenode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storageagent"
)

func startTestDockerAgent(t *testing.T, token string) (*storageagent.Server, *httptest.Server) {
	t.Helper()
	srv, err := storageagent.NewServer(storageagent.Options{
		DataDir: t.TempDir(),
		Token:   token,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("storageagent.NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return srv, ts
}

func TestDockerVolumeLifecycleRangeReadAndEmptyFile(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	srv, ts := startTestDockerAgent(t, "docker-secret-token")
	ctx := context.Background()

	// Wrong token must fail Probe with 401.
	badVol, err := NewDockerVolume(10, false, DockerConfig{Endpoint: ts.URL}, Credentials{Token: "wrong"})
	if err != nil {
		t.Fatalf("NewDockerVolume: %v", err)
	}
	if err := badVol.Probe(ctx); err == nil {
		t.Fatal("expected Probe with wrong token to fail")
	}

	vol, err := NewDockerVolume(10, false, DockerConfig{Endpoint: ts.URL + "/"}, Credentials{Token: "docker-secret-token"})
	if err != nil {
		t.Fatalf("NewDockerVolume: %v", err)
	}
	defer vol.Close()

	if err := vol.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	u, err := vol.Usage(ctx)
	if err != nil || u.TotalBytes == 0 {
		t.Fatalf("Usage = %+v, %v", u, err)
	}

	if err := vol.MkdirDir(ctx, SpaceDatasets, "gears", true); err != nil {
		t.Fatalf("MkdirDir: %v", err)
	}
	if err := vol.MkdirDir(ctx, SpaceDatasets, "gears", true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate exclusive MkdirDir = %v, want fs.ErrExist", err)
	}
	exists, _, err := vol.DirExists(ctx, SpaceDatasets, "gears")
	if err != nil || !exists {
		t.Fatalf("DirExists = %v, %v", exists, err)
	}

	dir, err := vol.OpenDir(ctx, SpaceDatasets, "gears")
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	defer dir.Close()

	// 1. 0-byte regular file (must not fail with HTTP 416 Range Not Satisfiable).
	if err := dir.PutBytes(ctx, "empty.txt", nil, 0o644); err != nil {
		t.Fatalf("PutBytes empty: %v", err)
	}
	ef, err := dir.Open("empty.txt")
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	ebuf, err := io.ReadAll(ef)
	_ = ef.Close()
	if err != nil || len(ebuf) != 0 {
		t.Fatalf("ReadAll empty = %d bytes, err=%v", len(ebuf), err)
	}

	// 2. Large file (> 64 KiB prefix cache): random ReadAt + full streaming Read.
	large := make([]byte, dockerHeaderCacheBytes+48*1024)
	if _, err := io.ReadFull(rand.Reader, large); err != nil {
		t.Fatal(err)
	}
	fi, err := dir.PutFile(ctx, "cam1/frame_001.jpg", bytes.NewReader(large), int64(len(large)), 0o644)
	if err != nil || fi.Size() != int64(len(large)) {
		t.Fatalf("PutFile large = %v, %v", fi, err)
	}
	if err := dir.PutBytes(ctx, ".skipme/note.txt", []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}

	lf, err := dir.Open("cam1/frame_001.jpg")
	if err != nil {
		t.Fatalf("Open large: %v", err)
	}
	tail := make([]byte, 256)
	tailOff := int64(len(large) - 256)
	if n, err := lf.ReadAt(tail, tailOff); err != nil || n != 256 || !bytes.Equal(tail, large[tailOff:]) {
		t.Fatalf("ReadAt tail n=%d err=%v", n, err)
	}
	if _, err := lf.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	gotAll, err := io.ReadAll(lf)
	_ = lf.Close()
	if err != nil || !bytes.Equal(gotAll, large) {
		t.Fatalf("ReadAll len=%d err=%v match=%v", len(gotAll), err, bytes.Equal(gotAll, large))
	}

	// 3. WalkFiles with filepath.SkipDir.
	var files []string
	err = dir.WalkFiles(ctx, func(rel string, info fs.FileInfo, isDir bool) error {
		if isDir && info.Name() == ".skipme" {
			return filepath.SkipDir
		}
		if !isDir {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil || len(files) != 2 {
		t.Fatalf("WalkFiles = %v, err=%v", files, err)
	}

	// 4. CopyFileWithin creates an in-container hardlink when the underlying
	// filesystem permits link(2), or falls back cleanly to copy on FUSE/VirtioFS.
	probeSrc := filepath.Join(srv.DataDir(), ".link-probe-src")
	probeDst := filepath.Join(srv.DataDir(), ".link-probe-dst")
	_ = os.WriteFile(probeSrc, []byte("1"), 0o644)
	fsSupportsLink := os.Link(probeSrc, probeDst) == nil
	_ = os.Remove(probeSrc)
	_ = os.Remove(probeDst)

	cfi, linked, err := vol.CopyFileWithin(ctx,
		SpaceDatasets, "gears/cam1/frame_001.jpg",
		SpaceExports, "dv1/images/train/frame_001.jpg",
		nil)
	if err != nil || cfi.Size() != int64(len(large)) || (fsSupportsLink && !linked) {
		t.Fatalf("CopyFileWithin = %v, linked=%v (fsSupportsLink=%v), err=%v", cfi, linked, fsSupportsLink, err)
	}
	expDir, err := vol.OpenDir(ctx, SpaceExports, "dv1")
	if err != nil {
		t.Fatalf("OpenDir SpaceExports/dv1: %v", err)
	}
	defer expDir.Close()
	copiedFile, err := expDir.Open("images/train/frame_001.jpg")
	if err != nil {
		t.Fatalf("Open copied export file: %v", err)
	}
	copiedBytes, err := io.ReadAll(copiedFile)
	_ = copiedFile.Close()
	if err != nil || !bytes.Equal(copiedBytes, large) {
		t.Fatalf("copied export file bytes mismatch: len=%d err=%v", len(copiedBytes), err)
	}
}

func TestSupportsArchiveAndCanArchiveBetweenMatrix(t *testing.T) {
	svcNoAgent := NewService(Options{SecretKey: "k"})
	svcWithAgent := NewService(Options{SecretKey: "k", StorageServiceURL: "http://storage:8090"})

	if svcNoAgent.NodeSupportsArchive(KindLocal, true) {
		t.Fatal("builtin local without TM_STORAGE_SERVICE_URL must have supports_archive=false")
	}
	if !svcWithAgent.NodeSupportsArchive(KindLocal, true) {
		t.Fatal("builtin local with TM_STORAGE_SERVICE_URL must have supports_archive=true")
	}
	if svcWithAgent.NodeSupportsArchive(KindLocal, false) {
		t.Fatal("non-builtin local must have supports_archive=false")
	}
	if !svcNoAgent.NodeSupportsArchive(KindDocker, false) {
		t.Fatal("docker node must have supports_archive=true")
	}
	if svcWithAgent.NodeSupportsArchive(KindS3, false) || svcWithAgent.NodeSupportsArchive(KindSMB, false) {
		t.Fatal("s3 and smb must have supports_archive=false")
	}

	builtinRef := NodeRef{ID: 1, Kind: KindLocal, IsBuiltin: true}
	passiveLocal := NodeRef{ID: 2, Kind: KindLocal, IsBuiltin: false}
	dockerA := NodeRef{ID: 3, Kind: KindDocker}
	dockerB := NodeRef{ID: 4, Kind: KindDocker}
	s3Node := NodeRef{ID: 5, Kind: KindS3}
	smbNode := NodeRef{ID: 6, Kind: KindSMB}

	if !svcNoAgent.CanArchiveBetween(dockerA, dockerB) {
		t.Fatal("Docker <-> Docker must use archive mode")
	}
	if !svcNoAgent.CanArchiveBetween(builtinRef, dockerA) || !svcNoAgent.CanArchiveBetween(dockerA, builtinRef) {
		t.Fatal("Builtin <-> Docker must use archive mode")
	}
	for _, pair := range [][2]NodeRef{
		{dockerA, dockerA},
		{builtinRef, passiveLocal},
		{passiveLocal, dockerA},
		{dockerA, s3Node},
		{s3Node, dockerB},
		{dockerA, smbNode},
		{builtinRef, s3Node},
	} {
		if svcWithAgent.CanArchiveBetween(pair[0], pair[1]) {
			t.Fatalf("pair %+v -> %+v must fall back to direct mode", pair[0], pair[1])
		}
	}
}

func TestDockerArchiveMigrationPeerToPeerAndRollback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agentA, tsA := startTestDockerAgent(t, "tok-a")
	agentB, tsB := startTestDockerAgent(t, "tok-b")

	ctx := context.Background()
	svc := NewService(Options{
		DB:        db.Pool,
		Storage:   st,
		SecretKey: "test-docker-migration-secret-key",
	})
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop()

	// Seed a dataset on Builtin Local (#1).
	localDSDir := filepath.Join(st.Dir(storage.Datasets), "bolts")
	if err := os.MkdirAll(localDSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgBytes := bytes.Repeat([]byte("bolt-jpeg-payload-"), 200)
	if err := os.WriteFile(filepath.Join(localDSDir, "001.jpg"), imgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDSDir, "001.xml"), []byte("<annotation/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	var dsID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO datasets
		(name, dir_name, origin, storage_node_id, image_count, total_bytes)
		VALUES ('螺栓样本', 'bolts', 'upload', 1, 1, $1) RETURNING id`,
		int64(len(imgBytes)+len("<annotation/>"))).Scan(&dsID); err != nil {
		t.Fatal(err)
	}

	// Create Docker Node A using JSON {"config": {"endpoint": ..., "token": ...}} shape.
	var createA CreateNodeInput
	rawCreateA := `{"name":"存储节点A","kind":"docker","activate":true,"config":{"endpoint":"` + tsA.URL + `","token":"tok-a"}}`
	if err := json.Unmarshal([]byte(rawCreateA), &createA); err != nil {
		t.Fatalf("Unmarshal CreateNodeInput: %v", err)
	}
	nodeA, err := svc.CreateNode(ctx, createA)
	if err != nil {
		t.Fatalf("CreateNode A: %v", err)
	}
	if !nodeA.SupportsArchive || !nodeA.HasSecret || !nodeA.IsActive {
		t.Fatalf("unexpected nodeA: %+v", nodeA)
	}
	rawNodeJSON, _ := json.Marshal(nodeA)
	if strings.Contains(string(rawNodeJSON), "tok-a") {
		t.Fatalf("node JSON leaked secret token: %s", string(rawNodeJSON))
	}

	// Migrate Builtin Local (#1) -> Docker A (archive mode).
	migs1, err := svc.CreateMigrations(ctx, CreateMigrationsInput{
		Targets: []MigrationTarget{{TargetType: TargetDataset, TargetID: dsID}},
	})
	if err != nil {
		t.Fatalf("CreateMigrations 1->A: %v", err)
	}
	if migs1[0].TransferMode != TransferModeArchive {
		t.Fatalf("expected transfer_mode=archive, got %q", migs1[0].TransferMode)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	if err := svc.WaitMigrationsIdle(waitCtx); err != nil {
		cancel()
		t.Fatalf("WaitMigrationsIdle 1->A: %v", err)
	}
	cancel()

	list1, err := svc.ListMigrations(ctx)
	if err != nil || list1[0].Status != MigrationSucceeded || list1[0].ArchiveBytes <= 0 || list1[0].FilesDone != 2 {
		t.Fatalf("unexpected migration 1->A result: %+v, err=%v", list1, err)
	}
	if _, err := os.Stat(localDSDir); !os.IsNotExist(err) {
		t.Fatalf("expected local source dir removed after migration, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(agentA.DataDir(), "datasets/bolts/001.jpg")); err != nil {
		t.Fatalf("expected migrated file on Docker A: %v", err)
	}

	// Create & activate Docker Node B, then migrate Docker A -> Docker B via peer-to-peer pull-unpack.
	nodeB, err := svc.CreateNode(ctx, CreateNodeInput{
		Name:     "存储节点B",
		Kind:     KindDocker,
		Docker:   &DockerConfig{Endpoint: tsB.URL},
		Token:    "tok-b",
		Activate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode B: %v", err)
	}

	migs2, err := svc.CreateMigrations(ctx, CreateMigrationsInput{
		Targets: []MigrationTarget{{TargetType: TargetDataset, TargetID: dsID}},
	})
	if err != nil || migs2[0].TransferMode != TransferModeArchive {
		t.Fatalf("CreateMigrations A->B = %+v, err=%v", migs2, err)
	}
	waitCtx2, cancel2 := context.WithTimeout(ctx, 20*time.Second)
	if err := svc.WaitMigrationsIdle(waitCtx2); err != nil {
		cancel2()
		t.Fatalf("WaitMigrationsIdle A->B: %v", err)
	}
	cancel2()

	list2, err := svc.ListMigrations(ctx)
	if err != nil || list2[0].Status != MigrationSucceeded || list2[0].ToNode.ID != nodeB.ID || list2[0].ArchiveBytes <= 0 {
		t.Fatalf("unexpected migration A->B result: %+v, err=%v", list2, err)
	}
	if _, err := os.Stat(filepath.Join(agentA.DataDir(), "datasets/bolts")); !os.IsNotExist(err) {
		t.Fatalf("expected Docker A source dir deleted after migration, err=%v", err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(agentB.DataDir(), "datasets/bolts/001.jpg"))
	if err != nil || !bytes.Equal(gotBytes, imgBytes) {
		t.Fatalf("Docker B migrated file mismatch, err=%v", err)
	}

	// Verify failure rollback: create a failing mock Docker node C that passes /info
	// probe but returns 400 checksum_mismatch on unpack — source on Docker B must stay intact!
	failMux := http.NewServeMux()
	failMux.HandleFunc("GET /api/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"trainmaster-storage","version":"0.1.0","ok":true,"writable":true,"supports_archive":true,"archive_formats":["tar.zst","zip"],"spaces":["datasets","exports"],"usage":{"total_bytes":1000000000,"free_bytes":900000000,"used_bytes":100000000}}`))
	})
	failMux.HandleFunc("GET /api/v1/spaces/{space}/dirs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"exists":false}`))
	})
	failMux.HandleFunc("DELETE /api/v1/spaces/{space}/dirs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	failMux.HandleFunc("POST /api/v1/archives/pull-unpack", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"checksum_mismatch","message":"simulated corrupted archive"}}`))
	})
	failTS := httptest.NewServer(failMux)
	defer failTS.Close()

	_, err = svc.CreateNode(ctx, CreateNodeInput{
		Name:     "故障节点C",
		Kind:     KindDocker,
		Docker:   &DockerConfig{Endpoint: failTS.URL},
		Activate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode C: %v", err)
	}
	if _, err := svc.CreateMigrations(ctx, CreateMigrationsInput{
		Targets: []MigrationTarget{{TargetType: TargetDataset, TargetID: dsID}},
	}); err != nil {
		t.Fatalf("CreateMigrations B->C: %v", err)
	}
	waitCtx3, cancel3 := context.WithTimeout(ctx, 20*time.Second)
	_ = svc.WaitMigrationsIdle(waitCtx3)
	cancel3()

	list3, _ := svc.ListMigrations(ctx)
	if list3[0].Status != MigrationFailed || list3[0].Stage != "" {
		t.Fatalf("expected failed migration with empty stage, got %+v", list3[0])
	}
	var curOwner int64
	_ = db.Pool.QueryRow(ctx, "SELECT storage_node_id FROM datasets WHERE id = $1", dsID).Scan(&curOwner)
	if curOwner != nodeB.ID {
		t.Fatalf("expected dataset ownership to remain on Docker B (%d) after failed migration, got %d", nodeB.ID, curOwner)
	}
	if _, err := os.Stat(filepath.Join(agentB.DataDir(), "datasets/bolts/001.jpg")); err != nil {
		t.Fatalf("expected source file on Docker B untouched after failed migration: %v", err)
	}
}
