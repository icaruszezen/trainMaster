package reviewer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

const zipName = "审图-7-外观检测.zip"

// dirNames lists the names in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestOpenZipExtractsNextToIt(t *testing.T) {
	ts := newTestServer(t)
	dir := t.TempDir()
	zipPath := newTestPackage().writeZip(t, dir, zipName)

	s := ts.open(zipPath)
	info := s.Package
	want := filepath.Join(dir, "审图-7-外观检测")
	if info.Path != want || info.ZipPath != zipPath || info.Resumed || info.PackageID != strings.Repeat("ab", 16) ||
		info.ResultsPath != want+"-审图结果.zip" || info.Review.ID != 7 || info.Review.Name != "外观检测" ||
		info.Counts.Total != 5 || info.MissingImages != 0 || len(info.Datasets) != 2 || info.LastExport != nil {
		t.Errorf("package = %+v", info)
	}
	// Only the files of review.json (and the README) are extracted, with
	// review.json at the top of the folder; no temporary folder is left.
	for _, rel := range []string{"review.json", "README.txt", "line_a/NG/a.png", "line_a/NG/a.xml", "line_b/e.tif"} {
		if _, err := os.Stat(filepath.Join(want, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(want, "line_b", "d.xml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("d.xml (no annotation in the package) exists: %v", err)
	}
	if got := dirNames(t, dir); len(got) != 2 {
		t.Errorf("folder holds %v", got)
	}
	if len(s.Recent) != 1 || s.Recent[0].Path != want || s.Recent[0].ZipPath != zipPath || !s.Recent[0].Exists ||
		s.Recent[0].ReviewName != "外观检测" {
		t.Errorf("recent = %+v", s.Recent)
	}

	// Opening the zip again continues in the same folder, with the
	// statuses set.
	if code := ts.do(http.MethodPut, "/api/review/images/101/status", map[string]string{"status": "needs_sample"}, nil); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	if code := ts.do(http.MethodPost, "/api/close", nil, nil); code != http.StatusNoContent {
		t.Fatalf("close: %d", code)
	}
	var none Session
	ts.do(http.MethodGet, "/api/session", nil, &none)
	if none.Package != nil || none.Recent[0].Counts.NeedsSample != 1 {
		t.Errorf("session after closing = %+v", none)
	}
	s = ts.open(zipPath)
	if !s.Package.Resumed || s.Package.Path != want || s.Package.Counts.NeedsSample != 1 {
		t.Errorf("reopened = %+v", s.Package)
	}

	// Another package downloaded under the same name goes next to it.
	other := newTestPackage()
	other.packageID = strings.Repeat("cd", 16)
	other.writeZip(t, dir, zipName)
	s = ts.open(zipPath)
	if s.Package.Path != want+" (2)" || s.Package.Resumed || s.Package.Counts.NeedsSample != 0 {
		t.Errorf("other package = %+v", s.Package)
	}
	if len(s.Recent) != 2 || s.Recent[0].Path != want+" (2)" {
		t.Errorf("recent = %+v", s.Recent)
	}
	// Opening the first folder by its path works as well.
	if s = ts.open(want); s.Package.Path != want || s.Package.ZipPath != "" || s.Package.Counts.NeedsSample != 1 {
		t.Errorf("folder = %+v", s.Package)
	}
}

func TestOpenRefusesUnsafeZips(t *testing.T) {
	for _, tt := range []struct {
		name  string
		extra map[string][]byte
		kind  string
		data  []byte // the zip's bytes, when not a package
		msg   string
	}{
		{name: "parent", extra: map[string][]byte{"../evil.txt": []byte("x")}, msg: "不安全的路径（../evil.txt）"},
		{name: "nested parent", extra: map[string][]byte{"审图-7-外观检测/a/../../../evil.txt": []byte("x")}, msg: "不安全的路径"},
		{name: "absolute", extra: map[string][]byte{"/tmp/evil.txt": []byte("x")}, msg: "不安全的路径"},
		{name: "drive", extra: map[string][]byte{"C:/evil.txt": []byte("x")}, msg: "不安全的路径"},
		{name: "backslash", extra: map[string][]byte{`..\evil.txt`: []byte("x")}, msg: "不安全的路径"},
		{name: "symlink", extra: map[string][]byte{"审图-7-外观检测/link": nil}, msg: "符号链接"},
		{name: "results", kind: reviewfile.KindResults, msg: "这是本地审图程序导出的审图结果，不是审图包"},
		{name: "not a zip", data: []byte("not a zip"), msg: "不是有效的 zip 压缩包"},
		{name: "two manifests", extra: map[string][]byte{"other/review.json": []byte("{}")}, msg: "多个 review.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTestServer(t)
			dir := t.TempDir()
			p := filepath.Join(dir, zipName)
			data := tt.data
			if data == nil {
				tp := newTestPackage()
				tp.extra, tp.kind = tt.extra, tt.kind
				data = tp.zip(t)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				t.Fatal(err)
			}
			var e apiError
			if code := ts.do(http.MethodPost, "/api/open", map[string]string{"path": p}, &e); code != http.StatusAccepted {
				t.Fatalf("open: %d %+v", code, e)
			}
			s := ts.waitOpened()
			if s.Package != nil || s.OpenError == nil || s.OpenError.Path != p || !strings.Contains(s.OpenError.Message, tt.msg) {
				t.Errorf("session = %+v (error %+v), want an error containing %q", s, s.OpenError, tt.msg)
			}
			// Nothing was extracted.
			if got := dirNames(t, dir); len(got) != 1 {
				t.Errorf("folder holds %v", got)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
				t.Error("evil.txt was written")
			}
		})
	}
}

func TestOpenPaths(t *testing.T) {
	ts := newTestServer(t)
	parent := t.TempDir()
	pkgDir := filepath.Join(parent, "全部解压缩", "审图-7-外观检测")
	newTestPackage().writeFolder(t, pkgDir)

	// The folder, its review.json, and the folder Windows' “全部解压缩”
	// makes around it all open the package.
	for _, p := range []string{pkgDir, filepath.Join(pkgDir, "review.json"), filepath.Dir(pkgDir)} {
		if s := ts.open(p); s.Package.Path != pkgDir || s.Package.ZipPath != "" {
			t.Errorf("open %s = %+v", p, s.Package)
		}
	}
	// Opening the open folder again changes nothing.
	var o struct {
		Opening Opening `json:"opening"`
	}
	if code := ts.do(http.MethodPost, "/api/open", map[string]string{"path": pkgDir}, &o); code != http.StatusAccepted ||
		o.Opening.Path != pkgDir {
		t.Errorf("open again: %d %+v", code, o)
	}

	second := filepath.Join(parent, "全部解压缩", "审图-8-另一个")
	newTestPackage().writeFolder(t, second)
	textFile := filepath.Join(parent, "说明.txt")
	if err := os.WriteFile(textFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path   string
		status int
		msg    string
	}{
		{"", http.StatusUnprocessableEntity, "请选择审图包"},
		{"relative/审图包.zip", http.StatusUnprocessableEntity, "请输入完整的路径"},
		{filepath.Join(parent, "missing.zip"), http.StatusNotFound, "文件或文件夹不存在"},
		{textFile, http.StatusUnprocessableEntity, "只能打开审图包"},
		{filepath.Dir(pkgDir), http.StatusUnprocessableEntity, "中有 2 个审图包文件夹"},
		{parent, http.StatusUnprocessableEntity, "不是审图包文件夹"},
	} {
		var e apiError
		if code := ts.do(http.MethodPost, "/api/open", map[string]string{"path": tt.path}, &e); code != tt.status ||
			!strings.Contains(e.Error.Message, tt.msg) {
			t.Errorf("open %q: %d %+v, want %d %q", tt.path, code, e, tt.status, tt.msg)
		}
	}
	// A failed check leaves the open package open.
	if s := ts.waitOpened(); s.Package == nil || s.Package.Path != pkgDir || s.OpenError != nil {
		t.Errorf("session = %+v", s)
	}
}

func TestOpenFolderProblems(t *testing.T) {
	ts := newTestServer(t)
	dir := t.TempDir()

	// Missing image files are counted; their file is not found.
	tp := newTestPackage()
	tp.images[1].missing = true
	tp.writeFolder(t, filepath.Join(dir, "a"))
	s := ts.open(filepath.Join(dir, "a"))
	if s.Package.MissingImages != 1 {
		t.Errorf("package = %+v", s.Package)
	}
	var img ImageDetail
	ts.do(http.MethodGet, "/api/review/images/102", nil, &img)
	if img.ImageVersion != "" || img.ObjectCount != 1 {
		t.Errorf("image of a missing file = %+v", img)
	}
	if resp, _ := ts.get("/api/review/images/102/file"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("file of a missing image: %d", resp.StatusCode)
	}

	// A progress file that cannot be read fails the opening (it is the
	// user's work) and is left as it is.
	b := filepath.Join(dir, "b")
	newTestPackage().writeFolder(t, b)
	if err := os.WriteFile(filepath.Join(b, reviewfile.ProgressFile), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodPost, "/api/open", map[string]string{"path": b}, nil)
	s = ts.waitOpened()
	if s.OpenError == nil || !strings.Contains(s.OpenError.Message, "已损坏") || s.Package == nil ||
		s.Package.Path != filepath.Join(dir, "a") {
		t.Errorf("session = %+v", s)
	}
	if data, _ := os.ReadFile(filepath.Join(b, reviewfile.ProgressFile)); string(data) != "{broken" {
		t.Errorf("progress file = %q", data)
	}

	// The progress of another package (an older one extracted into the
	// same folder) does not count: its statuses are not this package's
	// changes.
	c := filepath.Join(dir, "c")
	newTestPackage().writeFolder(t, c)
	progress := `{"format": "trainmaster-review-progress", "version": 1, "package_id": "` + strings.Repeat("ef", 16) +
		`", "images": [{"id": 101, "status": "approved", "reviewed_at": "2026-11-03T08:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(c, reviewfile.ProgressFile), []byte(progress), 0o644); err != nil {
		t.Fatal(err)
	}
	if s = ts.open(c); s.Package.Resumed || s.Package.Counts.Approved != 1 {
		t.Errorf("package with another package's progress = %+v", s.Package)
	}
	// The same package's progress counts.
	progress = strings.Replace(progress, strings.Repeat("ef", 16), strings.Repeat("ab", 16), 1)
	if err := os.WriteFile(filepath.Join(c, reviewfile.ProgressFile), []byte(progress), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodPost, "/api/close", nil, nil)
	if s = ts.open(c); !s.Package.Resumed || s.Package.Counts.Approved != 2 {
		t.Errorf("package with its progress = %+v", s.Package)
	}
	var a ImageDetail
	ts.do(http.MethodGet, "/api/review/images/101", nil, &a)
	if a.Status != "approved" || a.ReviewedAt == nil || a.Revision != 1 {
		t.Errorf("image of the progress = %+v", a.Image)
	}
}

func TestExtractChecksFreeSpaceAndCancels(t *testing.T) {
	dir := t.TempDir()
	zipPath := newTestPackage().writeZip(t, dir, zipName)

	saved := freeSpace
	t.Cleanup(func() { freeSpace = saved })
	freeSpace = func(string) (int64, bool) { return 1 << 20, true }
	_, _, err := extractPackage(context.Background(), zipPath, nil)
	var re *Error
	if !errors.As(err, &re) || !errors.Is(err, ErrNoSpace) || !strings.Contains(re.Message, "磁盘空间不足") {
		t.Errorf("extract on a full disk: %v", err)
	}
	freeSpace = saved

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := extractPackage(ctx, zipPath, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled extract: %v", err)
	}
	if got := dirNames(t, dir); len(got) != 1 {
		t.Errorf("folder holds %v after the failures", got)
	}

	var reported [2]int64
	got, resumed, err := extractPackage(context.Background(), zipPath, func(done, total int64) { reported = [2]int64{done, total} })
	if err != nil || resumed || got != filepath.Join(dir, "审图-7-外观检测") || reported[0] != reported[1] || reported[1] == 0 {
		t.Errorf("extract = %s, %v, %v; progress %v", got, resumed, err, reported)
	}
}

func TestOpenNoSpaceThroughAPI(t *testing.T) {
	ts := newTestServer(t)
	dir := t.TempDir()
	zipPath := newTestPackage().writeZip(t, dir, zipName)
	saved := freeSpace
	t.Cleanup(func() { freeSpace = saved })
	freeSpace = func(string) (int64, bool) { return 0, true }
	ts.do(http.MethodPost, "/api/open", map[string]string{"path": zipPath}, nil)
	if s := ts.waitOpened(); s.OpenError == nil || !strings.Contains(s.OpenError.Message, "磁盘空间不足") {
		t.Errorf("session = %+v", s)
	}
}

func TestOpenWhileOpening(t *testing.T) {
	ts := newTestServer(t)
	dir := t.TempDir()
	newTestPackage().writeFolder(t, dir)
	job := &openJob{path: "x", cancel: func() {}, done: make(chan struct{})}
	job.setStage(stageExtracting)
	job.progress(5, 10)
	ts.srv.mu.Lock()
	ts.srv.opening = job
	ts.srv.mu.Unlock()

	var s Session
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if s.Opening == nil || *s.Opening != (Opening{Path: "x", Stage: stageExtracting, Done: 5, Total: 10}) {
		t.Errorf("opening = %+v", s.Opening)
	}
	var e apiError
	if code := ts.do(http.MethodPost, "/api/open", map[string]string{"path": dir}, &e); code != http.StatusConflict ||
		e.Error.Code != "conflict" {
		t.Errorf("open while opening: %d %+v", code, e)
	}
	ts.srv.mu.Lock()
	ts.srv.opening = nil
	ts.srv.mu.Unlock()
	close(job.done)
}

func TestNoteOpenError(t *testing.T) {
	ts := newTestServer(t)
	_, err := ts.srv.Open("relative.zip")
	ts.srv.NoteOpenError("relative.zip", err)
	var s Session
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if s.OpenError == nil || !strings.Contains(s.OpenError.Message, "请输入完整的路径") {
		t.Errorf("session = %+v", s)
	}
}

func TestSafeEntryName(t *testing.T) {
	for name, want := range map[string]bool{
		"a/b.png": true, "a/": true, "a/..b/c": true, "...": true,
		"": false, "/a": false, "../a": false, "a/../../b": false, "a/..": false, "C:/a": false, "c:a": false, "a\x00b": false,
	} {
		if got := safeEntryName(name); got != want {
			t.Errorf("safeEntryName(%q) = %v", name, got)
		}
	}
}
