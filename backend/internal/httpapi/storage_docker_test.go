package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storageagent"
	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode/s3test"
	"github.com/icaruszezen/trainmaster/backend/internal/training"
)

func startHTTPTestStorageAgent(t *testing.T, token string) (*storageagent.Server, *httptest.Server) {
	t.Helper()
	srv, err := storageagent.NewServer(storageagent.Options{
		DataDir: t.TempDir(),
		Token:   token,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return srv, ts
}

func waitReadyDataVersion(t *testing.T, e *storageAPIEnv, dvID int64) dataversion.DataVersion {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rec := e.do(http.MethodGet, fmt.Sprintf("/api/v1/data-versions/%d", dvID), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get data version: %d %s", rec.Code, rec.Body.String())
		}
		cur := decode[dataversion.DataVersion](t, rec)
		if cur.Status == dataversion.StatusReady {
			return cur
		}
		if cur.Status == dataversion.StatusFailed {
			t.Fatalf("data version build failed: %s", cur.Error)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for data version %d", dvID)
	return dataversion.DataVersion{}
}

func TestDockerStorageNodeImportBuildMigrateAndTrainerTarZst(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	e := newStorageAPIEnv(t)
	agentA, tsA := startHTTPTestStorageAgent(t, "agent-a-token")
	agentB, tsB := startHTTPTestStorageAgent(t, "agent-b-token")
	s3srv := s3test.NewServer("tm-legacy")
	defer s3srv.Close()

	// 1. Test connection & create active Docker Storage Node A.
	rec := e.do(http.MethodPost, "/api/v1/storage/nodes/test", map[string]any{
		"kind": "docker",
		"config": map[string]any{
			"endpoint": tsA.URL,
			"token":    "agent-a-token",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("test docker node A: %d %s", rec.Code, rec.Body.String())
	}
	probeRes := decode[storagenode.TestNodeResult](t, rec)
	if !probeRes.OK || probeRes.Usage == nil || probeRes.Usage.TotalBytes == 0 {
		t.Fatalf("unexpected docker probe result: %+v", probeRes)
	}

	rec = e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "Docker存储A",
		"kind":     "docker",
		"activate": true,
		"config": map[string]any{
			"endpoint": tsA.URL,
			"token":    "agent-a-token",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create docker node A: %d %s", rec.Code, rec.Body.String())
	}
	nodeA := decode[storagenode.Node](t, rec)
	if !nodeA.IsActive || !nodeA.SupportsArchive || !nodeA.HasSecret {
		t.Fatalf("unexpected nodeA: %+v", nodeA)
	}

	// GET /api/v1/storage/nodes/{id} returns single node details.
	rec = e.do(http.MethodGet, fmt.Sprintf("/api/v1/storage/nodes/%d", nodeA.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get storage node A: %d %s", rec.Code, rec.Body.String())
	}

	// 2. Import ZIP dataset directly onto Docker Node A (exercises moveStagedDockerArchive).
	dsA := importSampleZipDataset(t, e, "Docker远程样本集", 5)
	if dsA.StorageNode.ID != nodeA.ID || dsA.StorageNode.Kind != storagenode.KindDocker || !dsA.StorageNode.SupportsArchive {
		t.Fatalf("unexpected dsA storage_node: %+v", dsA.StorageNode)
	}
	if dsA.ImageCount != 5 {
		t.Fatalf("expected 5 imported images on Docker A, got %d", dsA.ImageCount)
	}
	if _, err := os.Stat(filepath.Join(agentA.DataDir(), "datasets", dsA.DirName, "part_01.jpg")); err != nil {
		t.Fatalf("expected imported image inside Docker A container data dir: %v", err)
	}

	// 3. Create Docker Node B & activate it, then build cross-node DataVersion (Docker A -> Docker B)
	// exercising maybePrefetchSnapshot + flushDockerStage!
	rec = e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "Docker存储B",
		"kind":     "docker",
		"activate": true,
		"docker": map[string]any{
			"endpoint": tsB.URL,
		},
		"token": "agent-b-token",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create docker node B: %d %s", rec.Code, rec.Body.String())
	}
	nodeB := decode[storagenode.Node](t, rec)

	rec = e.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name":        "Docker跨节点检测项目",
		"dataset_ids": []int64{dsA.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	proj := decode[project.Project](t, rec)
	rec = e.do(http.MethodPut, fmt.Sprintf("/api/v1/projects/%d/classes", proj.ID), map[string]any{
		"classes": []map[string]string{{"name": "scratch", "color": "#ef4444"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set classes: %d %s", rec.Code, rec.Body.String())
	}
	classesResp := decode[classListResponse](t, rec)

	rec = e.do(http.MethodPost, "/api/v1/data-versions", map[string]any{
		"project_id":    proj.ID,
		"note":          "docker-v1",
		"class_version": classesResp.Version,
		"sources":       []map[string]any{{"dataset_id": dsA.ID, "include_base": true, "version_ids": []int64{}}},
		"split":         map[string]any{"train": 80, "val": 20, "test": 0, "seed": 42, "use_folders": true, "group_by": "stem"},
		"scope":         "annotated",
		"include_empty": true,
		"ignore_labels": []string{},
		"augmentation":  nil,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create data version: %d %s", rec.Code, rec.Body.String())
	}
	dvCreated := decode[dataversion.DataVersion](t, rec)
	dvReady := waitReadyDataVersion(t, e, dvCreated.ID)
	if dvReady.StorageNode.ID != nodeB.ID || dvReady.ImageCount != 5 {
		t.Fatalf("unexpected ready data version on Docker B: %+v", dvReady)
	}
	if _, err := os.Stat(filepath.Join(agentB.DataDir(), "exports", "data-versions", fmt.Sprintf("project-%d", proj.ID), fmt.Sprintf("v%d", dvReady.Version), "data.yaml")); err != nil {
		t.Fatalf("expected data.yaml inside Docker B exports dir: %v", err)
	}

	// 4. Register compute trainer node, claim job on Docker B DataVersion, and verify
	// task.dataset_archive points to /dataset.tar.zst and streams a valid .tar.zst!
	nodeKey := "aaaa1111-bbbb-2222-cccc-333344445555"
	rec = e.do(http.MethodPost, "/api/v1/compute/agent/register", map[string]any{
		"node_key":      nodeKey,
		"name":          "GPU-Trainer-Docker",
		"hostname":      "gpu-docker",
		"agent_version": "0.6.0",
		"hardware": map[string]any{
			"in_container":  true,
			"cpu":           map[string]any{"logical_cores": 8},
			"memory":        map[string]any{"total_bytes": 16 << 30},
			"gpus":          []any{},
			"gpu_detection": map[string]any{"method": "none"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("register trainer: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dvReady.ID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"model_scale":     "n",
		"params":          map[string]any{"epochs": 1},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create training job: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/compute/agent/tasks/claim", map[string]any{
		"node_key":     nodeKey,
		"kinds":        []string{"training_job"},
		"wait_seconds": 0,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim task: %d %s", rec.Code, rec.Body.String())
	}
	claimed := decode[struct {
		Task *training.Task `json:"task"`
	}](t, rec)
	if claimed.Task == nil || claimed.Task.DatasetArchive == nil {
		t.Fatalf("expected task.dataset_archive to be populated for Docker DataVersion, got %+v", claimed.Task)
	}
	if claimed.Task.DVTransferMode != "archive" || claimed.Task.DVTotalBytes <= 0 {
		t.Fatalf("expected dv_transfer_mode=archive and dv_total_bytes>0, got mode=%q bytes=%d",
			claimed.Task.DVTransferMode, claimed.Task.DVTotalBytes)
	}

	// Verify GET /dataset streams Storage-Container-packed .zip with X-Transfer-Mode=archive + X-Archive-Sha256!
	zipRec := e.do(http.MethodGet, claimed.Task.DatasetURL, nil, "X-Node-Key", nodeKey)
	if zipRec.Code != http.StatusOK {
		t.Fatalf("download dataset zip: %d %s", zipRec.Code, zipRec.Body.String())
	}
	if gotMode := zipRec.Header().Get("X-Transfer-Mode"); gotMode != "archive" {
		t.Fatalf("expected X-Transfer-Mode=archive on GET /dataset, got %q", gotMode)
	}
	zipSHA := zipRec.Header().Get("X-Archive-Sha256")
	zipActualSum := sha256.Sum256(zipRec.Body.Bytes())
	if zipSHA == "" || zipSHA != hex.EncodeToString(zipActualSum[:]) || zipRec.Header().Get("X-Content-Sha256") != zipSHA {
		t.Fatalf("zip SHA mismatch: X-Archive-Sha256=%q X-Content-Sha256=%q actual=%x",
			zipSHA, zipRec.Header().Get("X-Content-Sha256"), zipActualSum)
	}

	// Also verify GET /dataset.tar.zst, SHA-256 header, and unpack YOLO dataset!
	rec = e.do(http.MethodGet, claimed.Task.DatasetArchive.URL, nil, "X-Node-Key", nodeKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("download dataset.tar.zst: %d %s", rec.Code, rec.Body.String())
	}
	hdrSHA := rec.Header().Get("X-Archive-Sha256")
	actualSum := sha256.Sum256(rec.Body.Bytes())
	if hdrSHA == "" || hdrSHA != hex.EncodeToString(actualSum[:]) {
		t.Fatalf("X-Archive-Sha256 = %q, actual = %x", hdrSHA, actualSum)
	}

	unpackTmp := t.TempDir()
	archFile := filepath.Join(unpackTmp, "dataset.tar.zst")
	if err := os.WriteFile(archFile, rec.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	zeroHeadroom := uint64(0)
	unpackRes, err := storagearchive.Unpack(t.Context(), storagearchive.UnpackOptions{
		ArchivePath:    archFile,
		Format:         storagearchive.FormatTarZst,
		SpaceDir:       unpackTmp,
		TargetRel:      "yolo",
		Mode:           storagearchive.ModeAtomic,
		ExpectedSHA256: hdrSHA,
		FreeHeadroom:   &zeroHeadroom,
	})
	if err != nil {
		t.Fatalf("Unpack downloaded dataset.tar.zst: %v", err)
	}
	if unpackRes.FilesCount < 11 { // 5 images + 5 labels + data.yaml + lists
		t.Fatalf("expected unpacked YOLO tree with >= 11 files, got %+v", unpackRes)
	}
	if _, err := os.Stat(filepath.Join(unpackTmp, "yolo/data.yaml")); err != nil {
		t.Fatalf("expected data.yaml in unpacked trainer archive: %v", err)
	}

	// Complete first job so node can claim next job.
	_ = e.do(http.MethodPost, claimed.Task.FailURL, map[string]any{
		"error": "done test", "retryable": false,
	}, "X-Node-Key", nodeKey)

	// 5. Activate legacy S3 node, migrate dvReady to S3 (must use transfer_mode="direct"),
	// and verify training job on S3 DataVersion uses dv_transfer_mode="direct" & X-Transfer-Mode=direct!
	rec = e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "LegacyS3",
		"kind":     "s3",
		"activate": true,
		"s3": map[string]any{
			"endpoint":         s3srv.URL,
			"region":           "us-east-1",
			"bucket":           "tm-legacy",
			"access_key_id":    s3srv.AccessKey,
			"force_path_style": true,
		},
		"secret_access_key": s3srv.SecretKey,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create S3 node: %d %s", rec.Code, rec.Body.String())
	}
	s3Node := decode[storagenode.Node](t, rec)
	if s3Node.SupportsArchive {
		t.Fatalf("S3 node must report supports_archive=false")
	}

	rec = e.do(http.MethodPost, "/api/v1/storage/migrations", map[string]any{
		"targets": []map[string]any{
			{"target_type": "data_version", "target_id": dvReady.ID},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create Docker->S3 migration: %d %s", rec.Code, rec.Body.String())
	}
	createdS3Migs := decode[List[storagenode.Migration]](t, rec)
	if createdS3Migs.Items[0].TransferMode != storagenode.TransferModeDirect {
		t.Fatalf("Docker->S3 migration must use direct mode, got %q", createdS3Migs.Items[0].TransferMode)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rec = e.do(http.MethodGet, "/api/v1/storage/migrations", nil)
		migs := decode[List[storagenode.Migration]](t, rec)
		if migs.Items[0].Status == storagenode.MigrationSucceeded {
			break
		}
		if migs.Items[0].Status == storagenode.MigrationFailed {
			t.Fatalf("Docker->S3 migration failed: %+v", migs.Items[0])
		}
		time.Sleep(30 * time.Millisecond)
	}

	rec = e.do(http.MethodPost, "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dvReady.ID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"model_scale":     "n",
		"params":          map[string]any{"epochs": 1},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create S3 training job: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/compute/agent/tasks/claim", map[string]any{
		"node_key":     nodeKey,
		"kinds":        []string{"training_job"},
		"wait_seconds": 0,
	})
	s3Claimed := decode[struct {
		Task *training.Task `json:"task"`
	}](t, rec)
	if s3Claimed.Task == nil {
		t.Fatalf("expected S3 training task claimed")
	}
	if s3Claimed.Task.DVTransferMode != "direct" || s3Claimed.Task.DatasetArchive != nil {
		t.Fatalf("expected dv_transfer_mode=direct and nil dataset_archive for S3 DataVersion, got %+v", s3Claimed.Task)
	}
	s3ZipRec := e.do(http.MethodGet, s3Claimed.Task.DatasetURL, nil, "X-Node-Key", nodeKey)
	if s3ZipRec.Code != http.StatusOK || s3ZipRec.Header().Get("X-Transfer-Mode") != "direct" {
		t.Fatalf("expected 200 X-Transfer-Mode=direct on S3 GET /dataset, got %d mode=%q",
			s3ZipRec.Code, s3ZipRec.Header().Get("X-Transfer-Mode"))
	}
}

func TestDockerDataVersionWithOfflineAugmentationPreservesLabels(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	e := newStorageAPIEnv(t)
	agent, ts := startHTTPTestStorageAgent(t, "aug-token-123")

	rec := e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "Docker增强测试节点",
		"kind":     "docker",
		"activate": true,
		"config": map[string]any{
			"endpoint": ts.URL,
			"token":    "aug-token-123",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create docker node: %d %s", rec.Code, rec.Body.String())
	}

	ds := importSampleZipDataset(t, e, "Docker离线增强样本集", 5)

	rec = e.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name":        "Docker离线增强检测项目",
		"dataset_ids": []int64{ds.ID},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	proj := decode[project.Project](t, rec)
	rec = e.do(http.MethodPut, fmt.Sprintf("/api/v1/projects/%d/classes", proj.ID), map[string]any{
		"classes": []map[string]string{{"name": "scratch", "color": "#ef4444"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set classes: %d %s", rec.Code, rec.Body.String())
	}
	classesResp := decode[classListResponse](t, rec)

	rec = e.do(http.MethodPost, "/api/v1/data-versions", map[string]any{
		"project_id":    proj.ID,
		"note":          "docker-aug-v1",
		"class_version": classesResp.Version,
		"sources":       []map[string]any{{"dataset_id": ds.ID, "include_base": true, "version_ids": []int64{}}},
		"split":         map[string]any{"train": 80, "val": 20, "test": 0, "seed": 42, "use_folders": true, "group_by": "stem"},
		"scope":         "annotated",
		"include_empty": true,
		"ignore_labels": []string{},
		"augmentation": map[string]any{
			"copies":            2,
			"methods":           []string{"hflip", "vflip"},
			"only_with_objects": true,
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create augmented data version: %d %s", rec.Code, rec.Body.String())
	}
	dvCreated := decode[dataversion.DataVersion](t, rec)
	dvReady := waitReadyDataVersion(t, e, dvCreated.ID)
	if dvReady.AugmentedCount <= 0 {
		t.Fatalf("expected AugmentedCount > 0, got %+v", dvReady)
	}

	dvRoot := filepath.Join(agent.DataDir(), "exports", "data-versions", fmt.Sprintf("project-%d", proj.ID), fmt.Sprintf("v%d", dvReady.Version))
	var augImages []string
	err := filepath.WalkDir(filepath.Join(dvRoot, "images"), func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(dvRoot, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, "-aug") {
			augImages = append(augImages, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk dvRoot images: %v", err)
	}
	if len(augImages) != dvReady.AugmentedCount {
		t.Fatalf("expected %d augmented images on Docker disk, found %d (%v)", dvReady.AugmentedCount, len(augImages), augImages)
	}
	for _, imgRel := range augImages {
		lblRel := dataset.UltralyticsLabelPath(imgRel)
		lblBytes, err := os.ReadFile(filepath.Join(dvRoot, filepath.FromSlash(lblRel)))
		if err != nil {
			t.Fatalf("missing augmented label on Docker disk for %s -> %s: %v", imgRel, lblRel, err)
		}
		if len(strings.TrimSpace(string(lblBytes))) == 0 {
			t.Fatalf("expected non-empty YOLO label lines for %s -> %s", imgRel, lblRel)
		}
	}

	// Verify both trainer /dataset (.zip) and /dataset.tar.zst contain every augmented .txt label.
	nodeKey := "bbbb1111-cccc-2222-dddd-333344446666"
	rec = e.do(http.MethodPost, "/api/v1/compute/agent/register", map[string]any{
		"node_key":      nodeKey,
		"name":          "Aug-Trainer",
		"hostname":      "aug-trainer",
		"agent_version": "0.6.0",
		"hardware": map[string]any{
			"in_container":  true,
			"cpu":           map[string]any{"logical_cores": 4},
			"memory":        map[string]any{"total_bytes": 8 << 30},
			"gpus":          []any{},
			"gpu_detection": map[string]any{"method": "none"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("register trainer: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dvReady.ID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"model_scale":     "n",
		"params":          map[string]any{"epochs": 1},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create training job: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/compute/agent/tasks/claim", map[string]any{
		"node_key":     nodeKey,
		"kinds":        []string{"training_job"},
		"wait_seconds": 0,
	})
	claimed := decode[struct {
		Task *training.Task `json:"task"`
	}](t, rec)
	if claimed.Task == nil || claimed.Task.DatasetArchive == nil {
		t.Fatalf("expected claimed archive task, got %+v", claimed.Task)
	}

	zipRec := e.do(http.MethodGet, claimed.Task.DatasetURL, nil, "X-Node-Key", nodeKey)
	if zipRec.Code != http.StatusOK {
		t.Fatalf("GET /dataset zip: %d %s", zipRec.Code, zipRec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(zipRec.Body.Bytes()), int64(zipRec.Body.Len()))
	if err != nil {
		t.Fatalf("open trainer dataset zip: %v", err)
	}
	zipEntries := map[string]uint64{}
	for _, f := range zr.File {
		zipEntries[f.Name] = f.UncompressedSize64
	}
	for _, imgRel := range augImages {
		lblRel := dataset.UltralyticsLabelPath(imgRel)
		if sz, ok := zipEntries[lblRel]; !ok || sz == 0 {
			t.Fatalf("trainer dataset.zip missing or empty augmented label %s (ok=%v size=%d)", lblRel, ok, sz)
		}
	}

	tarRec := e.do(http.MethodGet, claimed.Task.DatasetArchive.URL, nil, "X-Node-Key", nodeKey)
	if tarRec.Code != http.StatusOK {
		t.Fatalf("GET /dataset.tar.zst: %d %s", tarRec.Code, tarRec.Body.String())
	}
	unpackTmp := t.TempDir()
	archFile := filepath.Join(unpackTmp, "aug.tar.zst")
	if err := os.WriteFile(archFile, tarRec.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	zeroHeadroom := uint64(0)
	if _, err := storagearchive.Unpack(t.Context(), storagearchive.UnpackOptions{
		ArchivePath:    archFile,
		Format:         storagearchive.FormatTarZst,
		SpaceDir:       unpackTmp,
		TargetRel:      "yolo",
		Mode:           storagearchive.ModeAtomic,
		ExpectedSHA256: tarRec.Header().Get("X-Archive-Sha256"),
		FreeHeadroom:   &zeroHeadroom,
	}); err != nil {
		t.Fatalf("unpack dataset.tar.zst: %v", err)
	}
	for _, imgRel := range augImages {
		lblRel := dataset.UltralyticsLabelPath(imgRel)
		lblBytes, err := os.ReadFile(filepath.Join(unpackTmp, "yolo", filepath.FromSlash(lblRel)))
		if err != nil || len(strings.TrimSpace(string(lblBytes))) == 0 {
			t.Fatalf("trainer dataset.tar.zst missing or empty augmented label %s: err=%v", lblRel, err)
		}
	}
}

func TestDockerImportConflictSkipUsesMergeSkipMode(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	e := newStorageAPIEnv(t)
	agent, err := storageagent.NewServer(storageagent.Options{
		DataDir: t.TempDir(),
		Token:   "conflict-token",
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })

	var mu sync.Mutex
	var unpackModes []string
	injectConflictRel := ""
	injectConflictBytes := []byte("CONCURRENT_EXISTING_XML_DO_NOT_OVERWRITE")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/archives/unpack" {
			mu.Lock()
			unpackModes = append(unpackModes, r.URL.Query().Get("mode"))
			rel := injectConflictRel
			injectConflictRel = ""
			mu.Unlock()
			if rel != "" {
				dstFile := filepath.Join(agent.DataDir(), "datasets", r.URL.Query().Get("dir"), filepath.FromSlash(rel))
				_ = os.MkdirAll(filepath.Dir(dstFile), 0o755)
				_ = os.WriteFile(dstFile, injectConflictBytes, 0o644)
			}
		}
		agent.Handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	rec := e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "Docker冲突模式节点",
		"kind":     "docker",
		"activate": true,
		"config": map[string]any{
			"endpoint": ts.URL,
			"token":    "conflict-token",
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create docker node: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/datasets", map[string]any{"name": "并发冲突导入测试集"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dataset: %d %s", rec.Code, rec.Body.String())
	}
	ds := decode[dataset.Dataset](t, rec)

	runImport := func(onConflict string) {
		t.Helper()
		zipData := makeSampleDatasetZip(t, 2)
		rec := e.do(http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/imports", ds.ID), map[string]any{
			"source":            "archive",
			"file_name":         "batch.zip",
			"file_size":         len(zipData),
			"on_conflict":       onConflict,
			"annotation_format": "voc",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create import (%s): %d %s", onConflict, rec.Code, rec.Body.String())
		}
		imp := decode[dataset.Import](t, rec)
		rec = e.do(http.MethodPut, fmt.Sprintf("/api/v1/datasets/%d/imports/%d/archive?offset=0", ds.ID, imp.ID),
			zipData, "Content-Type", "application/octet-stream")
		if rec.Code != http.StatusOK {
			t.Fatalf("upload archive: %d %s", rec.Code, rec.Body.String())
		}
		rec = e.do(http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/imports/%d/commit", ds.ID, imp.ID), nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("commit import: %d %s", rec.Code, rec.Body.String())
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rec = e.do(http.MethodGet, fmt.Sprintf("/api/v1/datasets/%d/imports/%d", ds.ID, imp.ID), nil)
			cur := decode[dataset.Import](t, rec)
			if cur.Status == dataset.StatusSucceeded {
				return
			}
			if cur.Status == dataset.StatusFailed || cur.Status == dataset.StatusCanceled {
				t.Fatalf("import (%s) failed: %s", onConflict, cur.Error)
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("import (%s) timed out", onConflict)
	}

	// 1. on_conflict="skip" with concurrent write of part_01.xml after WalkFiles snapshot:
	mu.Lock()
	injectConflictRel = "part_01.xml"
	mu.Unlock()
	runImport("skip")

	mu.Lock()
	if len(unpackModes) != 1 || unpackModes[0] != storagearchive.ModeMergeSkip {
		t.Fatalf("expected unpack mode %q for on_conflict=skip, got %v", storagearchive.ModeMergeSkip, unpackModes)
	}
	mu.Unlock()

	gotXML, err := os.ReadFile(filepath.Join(agent.DataDir(), "datasets", ds.DirName, "part_01.xml"))
	if err != nil {
		t.Fatalf("read part_01.xml after skip import: %v", err)
	}
	if !bytes.Equal(gotXML, injectConflictBytes) {
		t.Fatalf("expected concurrent part_01.xml preserved under merge_skip, got %q", string(gotXML))
	}

	// 2. on_conflict="overwrite" sends mode="merge_overwrite" and replaces part_01.xml:
	runImport("overwrite")
	mu.Lock()
	if len(unpackModes) != 2 || unpackModes[1] != storagearchive.ModeMergeOverwrite {
		t.Fatalf("expected unpack mode %q for on_conflict=overwrite, got %v", storagearchive.ModeMergeOverwrite, unpackModes)
	}
	mu.Unlock()

	gotOverwritten, err := os.ReadFile(filepath.Join(agent.DataDir(), "datasets", ds.DirName, "part_01.xml"))
	if err != nil {
		t.Fatalf("read part_01.xml after overwrite import: %v", err)
	}
	if bytes.Equal(gotOverwritten, injectConflictBytes) {
		t.Fatalf("expected part_01.xml replaced under merge_overwrite")
	}
}

func TestDockerConfigAliasEmptyEndpointAndS3PatchNoCollision(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	e := newStorageAPIEnv(t)
	_, ts := startHTTPTestStorageAgent(t, "wire-tok")
	s3srv := s3test.NewServer("wire-bucket")
	defer s3srv.Close()

	rec := e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":     "WireDockerNode",
		"kind":     "docker",
		"activate": false,
		"config": map[string]any{
			"endpoint":             ts.URL,
			"insecure_skip_verify": false,
		},
		"token": "wire-tok",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create docker node: %d %s", rec.Code, rec.Body.String())
	}
	dkNode := decode[storagenode.Node](t, rec)

	// 1. POST /api/v1/storage/nodes/test with node_id + blank config.endpoint must return 422 (never fallback to storedCfg!).
	rec = e.do(http.MethodPost, "/api/v1/storage/nodes/test", map[string]any{
		"node_id": dkNode.ID,
		"kind":    "docker",
		"config": map[string]any{
			"endpoint":             "",
			"insecure_skip_verify": false,
		},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on blank config.endpoint /nodes/test, got %d %s", rec.Code, rec.Body.String())
	}

	// 2. PATCH /api/v1/storage/nodes/{id} with blank config.endpoint must return 422 (never silent 200 no-op!).
	rec = e.do(http.MethodPatch, fmt.Sprintf("/api/v1/storage/nodes/%d", dkNode.ID), map[string]any{
		"config": map[string]any{
			"endpoint":             "",
			"insecure_skip_verify": false,
		},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on blank config.endpoint PATCH, got %d %s", rec.Code, rec.Body.String())
	}

	// 3. Create S3 node and PATCH with "config" key must NOT collide with DockerConfig (no protocol-change 422).
	rec = e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name": "WireS3Node",
		"kind": "s3",
		"s3": map[string]any{
			"endpoint":         s3srv.URL,
			"region":           "us-east-1",
			"bucket":           "wire-bucket",
			"access_key_id":    s3srv.AccessKey,
			"force_path_style": true,
		},
		"secret_access_key": s3srv.SecretKey,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create s3 node: %d %s", rec.Code, rec.Body.String())
	}
	s3Node := decode[storagenode.Node](t, rec)

	rec = e.do(http.MethodPatch, fmt.Sprintf("/api/v1/storage/nodes/%d", s3Node.ID), map[string]any{
		"name": "WireS3NodeRenamed",
		"config": map[string]any{
			"endpoint": s3srv.URL,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on S3 PATCH with config field, got %d %s", rec.Code, rec.Body.String())
	}
	updatedS3 := decode[storagenode.Node](t, rec)
	if updatedS3.Name != "WireS3NodeRenamed" {
		t.Fatalf("expected updated S3 name WireS3NodeRenamed, got %q", updatedS3.Name)
	}
}
