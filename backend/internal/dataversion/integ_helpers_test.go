package dataversion

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/database"
	"github.com/icaruszezen/trainmaster/backend/internal/database/dbtest"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Test helpers of the backend-integration side (recycle bin, images,
// downloads). Their names start with "integ" so that they cannot clash with
// the helpers of the build tests.

// integEnv is a Service (not started: no builder) on a private database.
type integEnv struct {
	svc      *Service
	db       *database.DB
	st       *storage.Storage
	projects *project.Service
	logs     *integLogBuffer
}

// integLogBuffer collects the log output.
type integLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *integLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *integLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func integNewService(t *testing.T) *integEnv {
	t.Helper()
	db := dbtest.New(t)
	st, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := &integLogBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	projects := project.NewService(project.Options{DB: db.Pool, Logger: log})
	svc := NewService(Options{DB: db.Pool, Storage: st, Logger: log, Projects: projects,
		Datasets: dataset.NewService(dataset.Options{DB: db.Pool, Storage: st, Logger: log})})
	t.Cleanup(svc.WaitBackground)
	return &integEnv{svc: svc, db: db, st: st, projects: projects, logs: logs}
}

// integProject inserts a project with a class list.
func (e *integEnv) integProject(t *testing.T, name string, classes ...string) int64 {
	t.Helper()
	list := make([]project.Class, len(classes))
	for i, c := range classes {
		list[i] = project.Class{Name: c, Color: "#ef4444"}
	}
	var id int64
	if err := e.db.QueryRow(context.Background(), "INSERT INTO projects (name, classes) VALUES ($1, $2) RETURNING id",
		name, list).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// integImageSpec is an image of a version inserted by
// integInsertReadyVersion.
type integImageSpec struct {
	Path             string // in the version folder (images/...)
	Split            string // default train
	Width, Height    int    // default 32×24
	Objects          []dataset.AnnotationObject
	DatasetID        int64
	SourcePath       string // default Path without images/<dir>/
	AnnotationSource *int64
	// AugmentedFrom is 1 + the index of its original in the list (0: an
	// original).
	AugmentedFrom int
	Augment       []string
	Data          []byte // the file; nil: a JPEG or PNG (by extension) of the size
	NoFile        bool   // the row only
}

// integVersionSpec is a version inserted by integInsertReadyVersion.
type integVersionSpec struct {
	ProjectID int64
	Version   int // default 1
	Note      string
	Classes   []Class
	Sources   []Source
	Status    string // default ready (other states have no image rows)
	Images    []integImageSpec
}

// integVersion is an inserted version.
type integVersion struct {
	ID       int64
	DirName  string
	Dir      string  // its folder
	ImageIDs []int64 // in the order of the spec
}

// integInsertReadyVersion inserts a version (by default ready) as the
// builder leaves it, with its files under the version folder and its image
// rows (columns of migration 0013).
func (e *integEnv) integInsertReadyVersion(t *testing.T, spec integVersionSpec) integVersion {
	t.Helper()
	ctx := context.Background()
	if spec.Version == 0 {
		spec.Version = 1
	}
	if spec.Status == "" {
		spec.Status = StatusReady
	}
	if spec.Note == "" {
		spec.Note = "备注"
	}
	if spec.Classes == nil {
		spec.Classes = []Class{{Name: "scratch", Color: "#ef4444"}, {Name: "dent", Color: ""}}
	}
	if spec.Sources == nil {
		spec.Sources = []Source{}
	}
	config := Config{Split: SplitConfig{Train: 80, Val: 20, Seed: 42, UseFolders: true, GroupBy: GroupByStem},
		IncludeEmpty: true, IgnoreLabels: []string{}}
	dirName := versionDirName(spec.ProjectID, spec.Version)
	v := integVersion{DirName: dirName, Dir: e.svc.versionDir(dirName)}
	counts := map[string]int{}
	objects, augmented := 0, 0
	for _, img := range spec.Images {
		split := img.Split
		if split == "" {
			split = SplitTrain
		}
		counts[split]++
		objects += len(img.Objects)
		if img.AugmentedFrom != 0 {
			augmented++
		}
	}
	var finished *time.Time
	if spec.Status == StatusReady || spec.Status == StatusFailed {
		now := time.Now()
		finished = &now
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO data_versions (project_id, version, dir_name, note, status, classes,
			config, sources, image_count, object_count, train_count, val_count, test_count, augmented_count, started_at,
			finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15) RETURNING id`,
		spec.ProjectID, spec.Version, dirName, spec.Note, spec.Status, spec.Classes, config, spec.Sources,
		len(spec.Images), objects, counts[SplitTrain], counts[SplitVal], counts[SplitTest], augmented,
		finished).Scan(&v.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(v.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, img := range spec.Images {
		if img.Split == "" {
			img.Split = SplitTrain
		}
		if img.Width == 0 {
			img.Width, img.Height = 32, 24
		}
		if img.Objects == nil {
			img.Objects = []dataset.AnnotationObject{}
		}
		if img.Augment == nil {
			img.Augment = []string{}
		}
		if img.SourcePath == "" {
			parts := strings.SplitN(img.Path, "/", 3)
			img.SourcePath = parts[len(parts)-1]
		}
		file := filepath.Join(v.Dir, filepath.FromSlash(img.Path))
		size, mod := int64(len(img.Data)), time.Now()
		if !img.NoFile {
			data := img.Data
			if data == nil {
				data = integImageData(t, img.Path, img.Width, img.Height)
			}
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0o644); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			size, mod = fi.Size(), fi.ModTime()
		}
		labels := map[string]int{}
		for _, o := range img.Objects {
			labels[o.Name]++
		}
		var from *int64
		stored := StoredLink
		if img.AugmentedFrom != 0 {
			from = &v.ImageIDs[img.AugmentedFrom-1]
			stored = StoredGenerated
		}
		var id int64
		if err := e.db.QueryRow(ctx, `INSERT INTO data_version_images (version_id, position, split, split_by, path,
				size_bytes, mod_time, width, height, objects, object_count, labels, dataset_id, source_path, source_size,
				source_mod_time, annotation_source, augmented_from, augment, stored)
			VALUES ($1, $2, $3, 'ratio', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $5, $6, $14, $15, $16, $17)
			RETURNING id`, v.ID, (i+1)*10, img.Split, img.Path, size, mod, img.Width, img.Height, img.Objects,
			len(img.Objects), labels, img.DatasetID, img.SourcePath, img.AnnotationSource, from, img.Augment,
			stored).Scan(&id); err != nil {
			t.Fatal(err)
		}
		v.ImageIDs = append(v.ImageIDs, id)
	}
	return v
}

// integImageData is a decodable image of the given size, PNG for a .png
// path, else JPEG.
func integImageData(t *testing.T, name string, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 11), 90, 255})
		}
	}
	var b bytes.Buffer
	var err error
	if strings.HasSuffix(strings.ToLower(name), ".png") {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// integObject is a box.
func integObject(name string, xmin, ymin, xmax, ymax int) dataset.AnnotationObject {
	return dataset.AnnotationObject{Name: name, Xmin: xmin, Ymin: ymin, Xmax: xmax, Ymax: ymax}
}

// integDownload prepares and writes a download and returns its entries
// (without the top folder) and the file name.
func (e *integEnv) integDownload(t *testing.T, id int64, in ExportInput) (map[string][]byte, string) {
	t.Helper()
	ctx := context.Background()
	ticket, err := e.svc.CreateExport(ctx, id, in)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	x, err := e.svc.OpenExport(ctx, id, ticket.Token)
	if err != nil {
		t.Fatalf("OpenExport: %v", err)
	}
	defer x.Close()
	if x.FileName != ticket.FileName {
		t.Errorf("file names %q, %q", x.FileName, ticket.FileName)
	}
	var buf bytes.Buffer
	if err := x.Write(ctx, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return integUnzip(t, buf.Bytes(), strings.TrimSuffix(ticket.FileName, "-"+in.Format+".zip")), ticket.FileName
}

// integUnzip reads a zip archive whose entries are all in the folder top.
func integUnzip(t *testing.T, data []byte, top string) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		name, ok := strings.CutPrefix(f.Name, top+"/")
		if !ok {
			t.Errorf("entry %q is not in %q", f.Name, top)
			continue
		}
		if _, dup := out[name]; dup {
			t.Errorf("duplicate entry %q", name)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[name] = content
	}
	return out
}

// integWantKind checks the kind and the message of a dataversion error.
func integWantKind(t *testing.T, err, kind error, msgPart string) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Kind != kind || !strings.Contains(e.Message, msgPart) {
		t.Errorf("error = %#v (%v), want kind %v containing %q", err, err, kind, msgPart)
	}
}

// integJSON decodes JSON data.
func integJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

// integExists reports whether a path exists.
func integExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// integCount counts rows.
func (e *integEnv) integCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(fmt.Errorf("%s: %w", sql, err))
	}
	return n
}
