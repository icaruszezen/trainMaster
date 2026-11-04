package reviewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

func TestExport(t *testing.T) {
	ts := newTestServer(t)
	dir := filepath.Join(t.TempDir(), "审图-7-外观检测")
	newTestPackage().writeFolder(t, dir)
	ts.open(dir)

	// a approved as it is, c gets a box, b (approved in the package) stays.
	ts.do(http.MethodPut, "/api/review/images/101/status", map[string]string{"status": "approved"}, nil)
	var c ImageDetail
	ts.do(http.MethodGet, "/api/review/images/103", nil, &c)
	ts.do(http.MethodPut, "/api/review/images/103/annotation", map[string]any{
		"objects": []dataset.AnnotationObject{obj("dent", 4, 4, 30, 25)}, "base_version": c.AnnotationVersion}, nil)

	var res ExportResult
	if code := ts.do(http.MethodPost, "/api/export", nil, &res); code != http.StatusOK {
		t.Fatalf("export: %d", code)
	}
	resultsPath := dir + "-审图结果.zip"
	if res.Path != resultsPath || res.FileName != "审图-7-外观检测-审图结果.zip" || res.SizeBytes == 0 ||
		!res.ExportedAt.Equal(ts.clock.Now()) || res.Images != 5 || res.StatusChanges != 1 || res.AnnotationChanges != 1 ||
		res.Skipped != 0 || res.Unchanged != 3 || res.Counts.Approved != 2 || res.Counts.Edited != 2 || res.MissingImages != 0 {
		t.Errorf("export = %+v", res)
	}
	files := readZip(t, resultsPath)
	top := "审图-7-外观检测-审图结果/"
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	// Every VOC file, the unchanged ones too (d has none).
	if !slices.Equal(names, []string{top + "README.txt", top + "line_a/NG/a.xml", top + "line_a/b.xml", top + "line_a/c.xml",
		top + "line_b/e.xml", top + "review.json"}) {
		t.Fatalf("results hold %v", names)
	}
	if !bytes.HasPrefix(files[top+"README.txt"], []byte("\xef\xbb\xbf")) || !bytes.Contains(files[top+"README.txt"], []byte("\r\n")) ||
		!strings.Contains(string(files[top+"README.txt"]), "上传审图结果") {
		t.Errorf("README = %q", files[top+"README.txt"])
	}
	m, err := reviewfile.ReadManifest(bytes.NewReader(files[top+"review.json"]), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != reviewfile.KindResults || m.PackageID != strings.Repeat("ab", 16) || len(m.Images) != 5 ||
		!m.ExportedAt.Equal(ts.clock.Now()) {
		t.Errorf("manifest = %+v", m)
	}
	a, b, cm, d := m.Images[0], m.Images[1], m.Images[2], m.Images[3]
	if a.Status != "approved" || a.BaseStatus != "pending" || a.ReviewedAt == nil || !a.HasAnnotation || a.Edited ||
		reviewfile.AnnotationVersion(files[top+"line_a/NG/a.xml"]) != a.BaseAnnotationVersion {
		t.Errorf("a = %+v", a)
	}
	if b.Status != "approved" || b.BaseStatus != "approved" || b.BaseRevision != 2 || !b.HasAnnotation {
		t.Errorf("b = %+v", b)
	}
	if d.HasAnnotation || d.BaseAnnotationVersion != "" {
		t.Errorf("d = %+v", d)
	}
	if cm.Status != "pending" || !cm.HasAnnotation || !cm.Edited || cm.BaseAnnotationVersion == "" ||
		reviewfile.AnnotationVersion(files[top+"line_a/c.xml"]) == cm.BaseAnnotationVersion {
		t.Errorf("c = %+v", cm)
	}
	// The export is noted in the session and in the progress file.
	var s Session
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if s.Package.LastExport == nil || s.Package.LastExport.Path != resultsPath || s.Package.LastExport.SizeBytes != res.SizeBytes {
		t.Errorf("last export = %+v", s.Package.LastExport)
	}
	data, _ := os.ReadFile(filepath.Join(dir, reviewfile.ProgressFile))
	var pf progressFile
	if err := json.Unmarshal(data, &pf); err != nil || pf.LastExport == nil || pf.LastExport.Path != resultsPath {
		t.Errorf("progress = %s", data)
	}

	// Exporting again replaces the file; a VOC file that cannot be read is
	// left out (its status still counts) and reported.
	ts.clock.Advance(time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "line_a", "NG", "a.xml"), []byte("<annotation"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodPut, "/api/review/images/104/status", map[string]string{"status": "needs_sample"}, nil)
	if code := ts.do(http.MethodPost, "/api/export", nil, &res); code != http.StatusOK {
		t.Fatalf("export again: %d", code)
	}
	if res.StatusChanges != 2 || res.AnnotationChanges != 1 || res.Skipped != 1 ||
		!slices.Equal(res.SkippedPaths, []string{"line_a/NG/a.xml"}) || res.Unchanged != 2 || !res.ExportedAt.Equal(ts.clock.Now()) {
		t.Errorf("second export = %+v", res)
	}
	files = readZip(t, resultsPath)
	if _, ok := files[top+"line_a/NG/a.xml"]; ok || len(files) != 5 {
		t.Errorf("second results hold %d files", len(files))
	}
	if m, err := reviewfile.ReadManifest(bytes.NewReader(files[top+"review.json"]), 1<<20); err != nil || m.Images[0].HasAnnotation {
		t.Errorf("a in the second results = %+v, %v", m.Images[0], err)
	}
	if names := dirNames(t, filepath.Dir(dir)); len(names) != 2 {
		t.Errorf("next to the package: %v", names)
	}

	// Reopened, the last export is still known.
	ts.do(http.MethodPost, "/api/close", nil, nil)
	if s := ts.open(dir); s.Package.LastExport == nil || !s.Package.LastExport.ExportedAt.Equal(res.ExportedAt) {
		t.Errorf("reopened = %+v", s.Package)
	}
}

func TestExportUnchanged(t *testing.T) {
	ts := newTestServer(t)
	dir := filepath.Join(t.TempDir(), "pkg")
	newTestPackage().writeFolder(t, dir)
	ts.open(dir)
	var res ExportResult
	if code := ts.do(http.MethodPost, "/api/export", nil, &res); code != http.StatusOK {
		t.Fatalf("export: %d", code)
	}
	if res.Path != dir+"-审图结果.zip" || res.Unchanged != 5 || res.StatusChanges != 0 || res.AnnotationChanges != 0 ||
		res.SkippedPaths == nil {
		t.Errorf("export = %+v", res)
	}
	// The top folder is named after the review, whatever the folder is called.
	for name := range readZip(t, res.Path) {
		if !strings.HasPrefix(name, "审图-7-外观检测-审图结果/") {
			t.Errorf("entry %s", name)
		}
	}
}

func TestExportedResultsCannotBeOpened(t *testing.T) {
	ts := newTestServer(t)
	dir := filepath.Join(t.TempDir(), "pkg")
	newTestPackage().writeFolder(t, dir)
	ts.open(dir)
	var res ExportResult
	ts.do(http.MethodPost, "/api/export", nil, &res)
	ts.do(http.MethodPost, "/api/open", map[string]string{"path": res.Path}, nil)
	if s := ts.waitOpened(); s.OpenError == nil || !strings.Contains(s.OpenError.Message, "上传审图结果") {
		t.Errorf("open results = %+v", s.OpenError)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.zip")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := writeFileAtomic(dest, func(f *os.File) error {
		_, _ = f.WriteString("partial")
		return os.ErrInvalid
	})
	if data, _ := os.ReadFile(dest); failed == nil || string(data) != "old" {
		t.Errorf("failed write: %v, file %q", failed, data)
	}
	if err := writeFileAtomic(dest, func(f *os.File) error { _, err := f.WriteString("new"); return err }); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "new" {
		t.Errorf("file = %q", data)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("left %v", names)
	}
}
