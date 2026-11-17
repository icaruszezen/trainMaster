package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode/s3test"
	"github.com/icaruszezen/trainmaster/backend/internal/training"
)

type storageAPIEnv struct {
	*apiEnv
	storageNodes *storagenode.Service
	datasets     *dataset.Service
	projects     *project.Service
	dataVersions *dataversion.Service
	models       *model.Service
	training     *training.Service
}

func newStorageAPIEnv(t *testing.T) *storageAPIEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())

	sn := storagenode.NewService(storagenode.Options{
		DB:        db.Pool,
		Storage:   st,
		Logger:    log,
		SecretKey: "test-secret-key-for-storage-nodes",
	})
	if err := sn.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	ds := dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, StorageNodes: sn, Logger: log})
	if err := ds.Start(ctx); err != nil {
		sn.Stop()
		cancel()
		t.Fatal(err)
	}
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	dv := dataversion.NewService(dataversion.Options{
		DB: db.Pool, Storage: st, StorageNodes: sn, Logger: log, Datasets: ds, Projects: projects,
	})
	models := model.NewService(model.Options{DB: db.Pool, Storage: st, Logger: log})
	tr := training.NewService(training.Options{
		DB: db.Pool, Storage: st, StorageNodes: sn, Logger: log,
		Projects: projects, DataVersions: dv, Models: models,
	})
	models.Start(ctx)
	if err := dv.Start(ctx); err != nil {
		models.Stop()
		ds.Stop()
		sn.Stop()
		cancel()
		t.Fatal(err)
	}
	if err := tr.Start(ctx); err != nil {
		dv.Stop()
		models.Stop()
		ds.Stop()
		sn.Stop()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		tr.Stop()
		dv.Stop()
		models.Stop()
		ds.Stop()
		sn.Stop()
	})

	h := NewHandler(Options{
		Logger:       log,
		DB:           fakeDB{},
		Storage:      st,
		StorageNodes: sn,
		StartedAt:    time.Now(),
		Datasets:     ds,
		Projects:     projects,
		DataVersions: dv,
		Models:       models,
		Training:     tr,
		Compute: compute.NewService(compute.Options{
			DB: db.Pool, StorageNodes: sn, Logger: log, HeartbeatInterval: 10 * time.Second,
		}),
	})
	return &storageAPIEnv{
		apiEnv:       &apiEnv{h: h, st: st},
		storageNodes: sn,
		datasets:     ds,
		projects:     projects,
		dataVersions: dv,
		models:       models,
		training:     tr,
	}
}

func makeSampleDatasetZip(t *testing.T, count int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 1; i <= count; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 32, 24))
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				img.Set(x, y, color.RGBA{R: uint8(40 + i*20), G: 120, B: 180, A: 255})
			}
		}
		iw, err := zw.Create(fmt.Sprintf("part_%02d.jpg", i))
		if err != nil {
			t.Fatal(err)
		}
		if err := jpeg.Encode(iw, img, &jpeg.Options{Quality: 85}); err != nil {
			t.Fatal(err)
		}
		xw, err := zw.Create(fmt.Sprintf("part_%02d.xml", i))
		if err != nil {
			t.Fatal(err)
		}
		xmlBody := fmt.Sprintf(`<annotation><size><width>32</width><height>24</height><depth>3</depth></size>` +
			`<object><name>scratch</name><bndbox><xmin>2</xmin><ymin>2</ymin><xmax>20</xmax><ymax>18</ymax></bndbox></object></annotation>`)
		if _, err := io.WriteString(xw, xmlBody); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func importSampleZipDataset(t *testing.T, e *storageAPIEnv, name string, count int) dataset.Dataset {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/datasets", map[string]any{"name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dataset %s: %d %s", name, rec.Code, rec.Body.String())
	}
	ds := decode[dataset.Dataset](t, rec)

	zipData := makeSampleDatasetZip(t, count)
	rec = e.do(http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/imports", ds.ID), map[string]any{
		"source":            "archive",
		"file_name":         "samples.zip",
		"file_size":         len(zipData),
		"annotation_format": "voc",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create import: %d %s", rec.Code, rec.Body.String())
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
			break
		}
		if cur.Status == dataset.StatusFailed || cur.Status == dataset.StatusCanceled {
			t.Fatalf("import failed: %s", cur.Error)
		}
		time.Sleep(30 * time.Millisecond)
	}
	rec = e.do(http.MethodGet, fmt.Sprintf("/api/v1/datasets/%d", ds.ID), nil)
	return decode[dataset.Dataset](t, rec)
}

func TestStorageNodeEndToEndS3MigrationAndTrainingStream(t *testing.T) {
	e := newStorageAPIEnv(t)
	s3srv := s3test.NewServer("tm-datasets")
	defer s3srv.Close()

	// 1. Built-in node is listed and active, and cannot be deleted.
	rec := e.do(http.MethodGet, "/api/v1/storage/nodes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list nodes: %d %s", rec.Code, rec.Body.String())
	}
	initialNodes := decode[List[storagenode.Node]](t, rec)
	if len(initialNodes.Items) != 1 || !initialNodes.Items[0].IsBuiltin || !initialNodes.Items[0].IsActive {
		t.Fatalf("unexpected initial nodes: %+v", initialNodes.Items)
	}
	if delRec := e.do(http.MethodDelete, "/api/v1/storage/nodes/1", nil); delRec.Code != http.StatusConflict {
		t.Fatalf("deleting builtin node expected 409, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// 2. Create a local dataset + ready data version on built-in storage first.
	localDS := importSampleZipDataset(t, e, "产线A本地样本", 5)
	if !localDS.StorageNode.IsBuiltin || localDS.ImageCount != 5 {
		t.Fatalf("unexpected local dataset: %+v", localDS)
	}
	localDatasetDir := filepath.Join(e.st.Dir(storage.Datasets), localDS.DirName)
	if _, err := os.Stat(filepath.Join(localDatasetDir, "part_01.jpg")); err != nil {
		t.Fatalf("expected local dataset file on disk: %v", err)
	}

	rec = e.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name":        "表面缺陷检测",
		"dataset_ids": []int64{localDS.ID},
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
		"note":          "本地冻结版本",
		"class_version": classesResp.Version,
		"sources":       []map[string]any{{"dataset_id": localDS.ID, "include_base": true, "version_ids": []int64{}}},
		"split":         map[string]any{"train": 80, "val": 20, "test": 0, "seed": 42, "use_folders": true, "group_by": "stem"},
		"scope":         "annotated",
		"include_empty": true,
		"ignore_labels": []string{},
		"augmentation":  nil,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create data version v1: %d %s", rec.Code, rec.Body.String())
	}
	dv1 := decode[dataversion.DataVersion](t, rec)
	for range 200 {
		rec = e.do(http.MethodGet, fmt.Sprintf("/api/v1/data-versions/%d", dv1.ID), nil)
		dv1 = decode[dataversion.DataVersion](t, rec)
		if dv1.Status == dataversion.StatusReady {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if dv1.Status != dataversion.StatusReady {
		t.Fatalf("dv1 did not become ready: %+v", dv1)
	}
	localDVDir := filepath.Join(e.st.Dir(storage.Exports), "data-versions", fmt.Sprintf("project-%d", proj.ID), fmt.Sprintf("v%d", dv1.Version))
	if _, err := os.Stat(localDVDir); err != nil {
		t.Fatalf("expected local data version dir on disk: %v", err)
	}

	// 3. Test S3 connection, create S3 storage node, and activate it.
	s3Cfg := map[string]any{
		"endpoint":         s3srv.URL,
		"region":           "us-east-1",
		"bucket":           "tm-datasets",
		"prefix":           "prod",
		"access_key_id":    s3srv.AccessKey,
		"force_path_style": true,
	}
	rec = e.do(http.MethodPost, "/api/v1/storage/nodes/test", map[string]any{
		"kind":              "s3",
		"s3":                s3Cfg,
		"secret_access_key": s3srv.SecretKey,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("test S3 node: %d %s", rec.Code, rec.Body.String())
	}
	probeRes := decode[storagenode.TestNodeResult](t, rec)
	if !probeRes.OK {
		t.Fatalf("expected S3 probe OK: %+v", probeRes)
	}

	rec = e.do(http.MethodPost, "/api/v1/storage/nodes", map[string]any{
		"name":              "机房 MinIO 存储服务器",
		"kind":              "s3",
		"s3":                s3Cfg,
		"secret_access_key": s3srv.SecretKey,
		"activate":          true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create S3 node: %d %s", rec.Code, rec.Body.String())
	}
	s3Node := decode[storagenode.Node](t, rec)
	if !s3Node.IsActive || !s3Node.HasSecret {
		t.Fatalf("unexpected created S3 node: %+v", s3Node)
	}

	// 4. Import a new dataset directly onto active S3 node — zero dataset files on local disk!
	remoteDS := importSampleZipDataset(t, e, "产线B远程样本", 4)
	if remoteDS.StorageNode.ID != s3Node.ID || remoteDS.StorageNode.Kind != storagenode.KindS3 {
		t.Fatalf("expected dataset on S3 node, got %+v", remoteDS.StorageNode)
	}
	if _, err := os.Stat(filepath.Join(e.st.Dir(storage.Datasets), remoteDS.DirName)); !os.IsNotExist(err) {
		t.Fatalf("remote dataset must NOT exist in local datasets dir, stat err = %v", err)
	}
	if s3srv.ObjectCount("tm-datasets", "prod/datasets/"+remoteDS.DirName+"/") == 0 {
		t.Fatalf("expected dataset objects in S3 bucket")
	}

	// 5. Migrate historical local dataset + ready data version v1 to S3 and verify local space reclamation.
	rec = e.do(http.MethodGet, "/api/v1/storage/migratable", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list migratable: %d %s", rec.Code, rec.Body.String())
	}
	migratable := decode[List[storagenode.MigratableItem]](t, rec)
	if len(migratable.Items) != 2 {
		t.Fatalf("expected 2 migratable items (localDS + dv1), got %+v", migratable.Items)
	}

	rec = e.do(http.MethodPost, "/api/v1/storage/migrations", map[string]any{
		"targets": []map[string]any{
			{"target_type": "dataset", "target_id": localDS.ID},
			{"target_type": "data_version", "target_id": dv1.ID},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create migrations: %d %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rec = e.do(http.MethodGet, "/api/v1/storage/migrations", nil)
		migs := decode[List[storagenode.Migration]](t, rec)
		allDone := len(migs.Items) == 2
		for _, m := range migs.Items {
			if m.Status == storagenode.MigrationFailed {
				t.Fatalf("migration failed: %+v", m)
			}
			if m.Status != storagenode.MigrationSucceeded {
				allDone = false
			}
		}
		if allDone {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Both local directories should be deleted after verified migration (delete_source defaults to true).
	if _, err := os.Stat(localDatasetDir); !os.IsNotExist(err) {
		t.Fatalf("expected migrated local dataset dir to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(localDVDir); !os.IsNotExist(err) {
		t.Fatalf("expected migrated local data version dir to be removed, stat err = %v", err)
	}

	// 6. Update trainer cache policy, register compute node, create training job on migrated dv1,
	// and verify Claim returns shared_local=false + dataset_cache + working dataset zip stream.
	rec = e.do(http.MethodPut, "/api/v1/storage/cache-policy", map[string]any{
		"enabled":      true,
		"max_versions": 3,
		"ttl_hours":    12,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update cache policy: %d %s", rec.Code, rec.Body.String())
	}

	nodeKey := "11111111-2222-3333-4444-555555555555"
	rec = e.do(http.MethodPost, "/api/v1/compute/agent/register", map[string]any{
		"node_key":      nodeKey,
		"name":          "GPU-Trainer-01",
		"hostname":      "gpu01",
		"agent_version": "0.5.0",
		"hardware": map[string]any{
			"in_container":  false,
			"cpu":           map[string]any{"logical_cores": 8},
			"memory":        map[string]any{"total_bytes": 16 << 30},
			"gpus":          []any{},
			"gpu_detection": map[string]any{"method": "none"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("register compute node: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/compute/agent/heartbeat", map[string]any{"node_key": nodeKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body.String())
	}
	hb := decode[compute.HeartbeatResult](t, rec)
	if hb.DatasetCache == nil || !hb.DatasetCache.Enabled || hb.DatasetCache.MaxVersions != 3 || hb.DatasetCache.TTLSeconds != 12*3600 {
		t.Fatalf("unexpected heartbeat dataset_cache: %+v", hb.DatasetCache)
	}

	rec = e.do(http.MethodPost, "/api/v1/training/jobs", map[string]any{
		"project_id":      proj.ID,
		"data_version_id": dv1.ID,
		"init_mode":       "scratch",
		"yolo_version":    "yolo11",
		"model_scale":     "n",
		"params":          map[string]any{"epochs": 2},
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
	var claimResp struct {
		Task *training.Task `json:"task"`
	}
	claimResp = decode[struct {
		Task *training.Task `json:"task"`
	}](t, rec)
	if claimResp.Task == nil {
		t.Fatalf("expected training task to be claimed")
	}
	if claimResp.Task.DataVersion.SharedLocal == nil || *claimResp.Task.DataVersion.SharedLocal {
		t.Fatalf("expected shared_local=false after migration to S3, got %+v", claimResp.Task.DataVersion.SharedLocal)
	}
	if claimResp.Task.DatasetCache == nil || !claimResp.Task.DatasetCache.Enabled || claimResp.Task.DatasetCache.MaxVersions != 3 {
		t.Fatalf("unexpected task dataset_cache: %+v", claimResp.Task.DatasetCache)
	}

	// Stream YOLO dataset zip from S3 through the app server to the trainer!
	rec = e.do(http.MethodGet, claimResp.Task.DatasetURL, nil, "X-Node-Key", nodeKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("download task dataset zip: %d %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid dataset zip: %v", err)
	}
	hasDataYAML := false
	imgFiles := 0
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) == "data.yaml" {
			hasDataYAML = true
		}
		if filepath.Ext(zf.Name) == ".jpg" {
			imgFiles++
		}
	}
	if !hasDataYAML || imgFiles != 5 {
		t.Fatalf("unexpected dataset zip contents: hasDataYAML=%v imgFiles=%d", hasDataYAML, imgFiles)
	}
}
