package reviewer

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	stdimage "image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/image/tiff"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *testClock { return &testClock{t: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)} }

func intp(n int) *int { return &n }

// testImage is an image of a test package.
type testImage struct {
	id, datasetID int64
	source        string // path in the dataset
	format        string // png (default) or tiff
	predictions   []reviewfile.Prediction
	voc           []dataset.AnnotationObject // the packaged VOC file; nil: the predictions
	noVOC         bool                       // no VOC file (inference failed)
	inferenceErr  string
	status        string // base status, default pending
	revision      int
	noSize        bool // the manifest gives no size
	missing       bool // the image file is not in the zip
}

func pred(name string, conf float64, x1, y1, x2, y2 int) reviewfile.Prediction {
	return reviewfile.Prediction{Name: name, Confidence: conf, Xmin: x1, Ymin: y1, Xmax: x2, Ymax: y2}
}

func obj(name string, x1, y1, x2, y2 int) dataset.AnnotationObject {
	return dataset.AnnotationObject{Name: name, Xmin: x1, Ymin: y1, Xmax: x2, Ymax: y2}
}

// defaultImages is the usual test package:
//
//	line_a (dataset 11): NG/a.png (2 boxes), b.png (1 box, approved), c.png (no box)
//	line_b (dataset 12): d.png (inference failed, no VOC file), e.tif (edited on the platform)
func defaultImages() []testImage {
	return []testImage{
		{id: 101, datasetID: 11, source: "NG/a.png", predictions: []reviewfile.Prediction{
			pred("scratch", 0.9, 1, 1, 10, 10), pred("dent", 0.8, 2, 2, 12, 12)}},
		{id: 102, datasetID: 11, source: "b.png", predictions: []reviewfile.Prediction{pred("scratch", 0.7, 3, 3, 20, 20)},
			status: reviewfile.StatusApproved, revision: 2},
		{id: 103, datasetID: 11, source: "c.png"},
		{id: 104, datasetID: 12, source: "d.png", noVOC: true, inferenceErr: "无法读取图片"},
		{id: 105, datasetID: 12, source: "e.tif", format: "tiff", predictions: []reviewfile.Prediction{pred("dent", 0.6, 5, 5, 20, 20)},
			voc: []dataset.AnnotationObject{obj("dent", 5, 5, 25, 25)}},
	}
}

// testPackage describes a package zip to build.
type testPackage struct {
	reviewID  int64
	name      string
	packageID string
	kind      string // default package
	images    []testImage
	// extra entries added to the zip as they are (name → content); a nil
	// content adds a symbolic link.
	extra map[string][]byte
}

func newTestPackage() testPackage {
	return testPackage{reviewID: 7, name: "外观检测", packageID: strings.Repeat("ab", 16), images: defaultImages()}
}

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func tiffBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var b bytes.Buffer
	if err := tiff.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// manifest builds the package's review.json and its files (by path in
// the package).
func (tp testPackage) build(t testing.TB) (*reviewfile.Manifest, map[string][]byte) {
	t.Helper()
	kind := tp.kind
	if kind == "" {
		kind = reviewfile.KindPackage
	}
	m := &reviewfile.Manifest{
		Format: reviewfile.ManifestFormat, Version: reviewfile.ManifestVersion, Kind: kind, PackageID: tp.packageID,
		Platform: "trainMaster test", ExportedAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC),
		Review: reviewfile.ManifestReview{ID: tp.reviewID, Name: tp.name, Description: "测试",
			CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)},
		Model: reviewfile.ModelRef{ID: 3, ProjectID: 1, ProjectName: "外观", Version: 2, YOLOVersion: "yolo11", Task: "detect",
			FileName: "best.pt", ClassNames: []string{"scratch", "dent"}},
		Options:  reviewfile.Options{Conf: 0.25, IoU: 0.7, ImgSize: [2]int{640, 640}, MaxDet: 300},
		Classes:  []reviewfile.Class{{Name: "scratch", Color: "#ff0000"}, {Name: "dent", Color: ""}},
		Datasets: []reviewfile.ManifestDataset{{ID: 11, Name: "外观 A", Folder: "line_a"}, {ID: 12, Name: "外观 B", Folder: "line_b"}},
	}
	files := map[string][]byte{}
	folders := map[int64]string{11: "line_a", 12: "line_b"}
	for _, ti := range tp.images {
		p := folders[ti.datasetID] + "/" + ti.source
		ann := strings.TrimSuffix(p, filepath.Ext(p)) + ".xml"
		w, h := 40, 30
		if !ti.missing {
			if ti.format == "tiff" {
				files[p] = tiffBytes(t, w, h)
			} else {
				files[p] = pngBytes(t, w, h)
			}
		}
		status := ti.status
		if status == "" {
			status = reviewfile.StatusPending
		}
		mi := reviewfile.ManifestImage{
			ID: ti.id, DatasetID: ti.datasetID, ImageID: ti.id + 1000, Path: p, SourcePath: ti.source, Annotation: ann,
			Width: intp(w), Height: intp(h), Predictions: ti.predictions, InferenceError: ti.inferenceErr,
			BaseStatus: status, BaseRevision: ti.revision, Status: status,
		}
		if ti.noSize {
			mi.Width, mi.Height = nil, nil
		}
		if status != reviewfile.StatusPending {
			at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
			mi.ReviewedAt = &at
		}
		if !ti.noVOC {
			objects := ti.voc
			if objects == nil {
				objects = reviewfile.PredictionObjects(ti.predictions)
			}
			data := reviewfile.EncodeAnnotation(ti.source, mi.Width, mi.Height, objects)
			files[ann] = data
			mi.HasAnnotation = true
			mi.BaseAnnotationVersion = reviewfile.AnnotationVersion(data)
			mi.Edited = !reviewfile.SameBoxes(objects, ti.predictions)
		}
		m.Images = append(m.Images, mi)
	}
	return m, files
}

// zip writes the package zip, with everything in the top folder
// 审图-<id>-<name>/, and returns its bytes.
func (tp testPackage) zip(t testing.TB) []byte {
	t.Helper()
	m, files := tp.build(t)
	top := reviewfile.PackageFolder(tp.reviewID, tp.name) + "/"
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add(top+reviewfile.ReadmeFile, []byte("说明"))
	for _, img := range m.Images {
		if data, ok := files[img.Path]; ok {
			add(top+img.Path, data)
		}
		if data, ok := files[img.Annotation]; ok {
			add(top+img.Annotation, data)
		}
	}
	var mb bytes.Buffer
	if err := reviewfile.WriteManifest(&mb, m); err != nil {
		t.Fatal(err)
	}
	add(top+reviewfile.ManifestFile, mb.Bytes())
	for name, data := range tp.extra {
		if data == nil {
			h := &zip.FileHeader{Name: name}
			h.SetMode(os.ModeSymlink | 0o777)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("/etc/passwd"))
			continue
		}
		add(name, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// writeZip writes the package zip into dir as name and returns its path.
func (tp testPackage) writeZip(t testing.TB, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, tp.zip(t), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeFolder writes the package as an extracted folder dir.
func (tp testPackage) writeFolder(t testing.TB, dir string) {
	t.Helper()
	m, files := tp.build(t)
	var mb bytes.Buffer
	if err := reviewfile.WriteManifest(&mb, m); err != nil {
		t.Fatal(err)
	}
	files[reviewfile.ManifestFile] = mb.Bytes()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- server ----

const testToken = "test-token"

// testServer is a running server with a client that holds its cookie.
type testServer struct {
	t       *testing.T
	srv     *Server
	http    *httptest.Server
	client  *http.Client
	clock   *testClock
	config  string
	cache   string
	quits   chan struct{}
	closing chan struct{}
	reveals []string
	mu      sync.Mutex
}

type serverOption func(*Options)

func newTestServer(t *testing.T, opts ...serverOption) *testServer {
	t.Helper()
	ts := &testServer{t: t, clock: newClock(), config: t.TempDir(), cache: t.TempDir(),
		quits: make(chan struct{}, 1), closing: make(chan struct{}, 1)}
	o := Options{
		Token: testToken, Version: "test", ConfigDir: ts.config, CacheDir: ts.cache, Now: ts.clock.Now,
		Quit:    func() { ts.quits <- struct{}{} },
		Closing: func() { ts.closing <- struct{}{} },
		Reveal: func(p string) error {
			ts.mu.Lock()
			defer ts.mu.Unlock()
			ts.reveals = append(ts.reveals, p)
			return nil
		},
		Places: func() []Place { return []Place{{Name: "主文件夹", Path: "/home/test", Kind: kindHome}} },
	}
	for _, f := range opts {
		f(&o)
	}
	ts.srv = New(o)
	ts.http = httptest.NewServer(ts.srv)
	jar, _ := cookiejar.New(nil)
	ts.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		ts.http.Close()
		ts.srv.Shutdown()
	})
	// Sign in as the program's first URL does.
	resp, err := ts.client.Get(ts.http.URL + "/?token=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-in status = %d", resp.StatusCode)
	}
	return ts
}

// do sends a request and decodes a JSON answer into out (when not nil);
// it returns the status code.
func (ts *testServer) do(method, path string, body any, out any) int {
	ts.t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			ts.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, ts.http.URL+path, r)
	if err != nil {
		ts.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ts.t.Fatal(err)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			ts.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

// get fetches path and returns the response with its body read.
func (ts *testServer) get(path string, header ...string) (*http.Response, []byte) {
	ts.t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.http.URL+path, nil)
	if err != nil {
		ts.t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		ts.t.Fatal(err)
	}
	return resp, data
}

// apiError is the error envelope.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// open opens a package through the API and waits until it is open; it
// fails the test when the opening fails.
func (ts *testServer) open(p string) Session {
	ts.t.Helper()
	var e apiError
	if code := ts.do(http.MethodPost, "/api/open", map[string]string{"path": p}, &e); code != http.StatusAccepted {
		ts.t.Fatalf("open %s: %d %+v", p, code, e)
	}
	s := ts.waitOpened()
	if s.Package == nil {
		msg := ""
		if s.OpenError != nil {
			msg = s.OpenError.Message
		}
		ts.t.Fatalf("open %s failed: %s", p, msg)
	}
	return s
}

// waitOpened waits until no opening is in progress and returns the
// session.
func (ts *testServer) waitOpened() Session {
	ts.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var s Session
		if code := ts.do(http.MethodGet, "/api/session", nil, &s); code != http.StatusOK {
			ts.t.Fatalf("session: %d", code)
		}
		if s.Opening == nil {
			return s
		}
		if time.Now().After(deadline) {
			ts.t.Fatalf("still opening: %+v", s.Opening)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func query(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}

func readZip(t testing.TB, p string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = data
	}
	return out
}
