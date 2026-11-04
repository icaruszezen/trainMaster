package reviewer

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// openFolder writes the default package as a folder and opens it.
func (ts *testServer) openFolder() string {
	ts.t.Helper()
	dir := filepath.Join(ts.t.TempDir(), "审图-7-外观检测")
	newTestPackage().writeFolder(ts.t, dir)
	ts.open(dir)
	return dir
}

func ids(images []Image) []int64 {
	out := []int64{}
	for _, img := range images {
		out = append(out, img.ID)
	}
	return out
}

func TestSecurity(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.CookieName = "tm_reviewer_1" })
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(c *http.Client, method, path string, header ...string) (*http.Response, apiError) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.http.URL+path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			if header[i] == "Host" {
				req.Host = header[i+1]
				continue
			}
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var e apiError
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(resp.Body).Decode(&e)
		}
		return resp, e
	}

	// Without the cookie (or with another token) nothing is answered.
	if resp, e := send(anon, http.MethodGet, "/api/session"); resp.StatusCode != http.StatusUnauthorized ||
		e.Error.Code != "unauthorized" || !strings.Contains(e.Error.Message, "链接已失效") {
		t.Errorf("session without the cookie: %d %+v", resp.StatusCode, e)
	}
	if resp, _ := send(anon, http.MethodGet, "/"); resp.StatusCode != http.StatusUnauthorized ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("page without the cookie: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := send(anon, http.MethodGet, "/?token=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	if resp, _ := send(anon, http.MethodGet, "/api/session", "Cookie", "tm_reviewer_1=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong cookie: %d", resp.StatusCode)
	}
	// The token signs in: a cookie of this port and a redirect to the URL
	// without it.
	resp, _ := send(anon, http.MethodGet, "/review?token="+testToken+"&image=3")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/review?image=3" {
		t.Errorf("sign in: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "tm_reviewer_1" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != testToken || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie = %+v", cookie)
	}
	if resp, _ := send(anon, http.MethodGet, "/api/session", "Cookie", "tm_reviewer_1="+testToken); resp.StatusCode != http.StatusOK {
		t.Errorf("with the cookie: %d", resp.StatusCode)
	}

	// Requests of other web pages are refused, even with the cookie.
	for _, h := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"}, // another port of 127.0.0.1
		{"Origin", "http://127.0.0.1:1"},
		{"Origin", "https://" + strings.TrimPrefix(ts.http.URL, "http://")},
		{"Origin", "null"},
	} {
		if resp, e := send(ts.client, http.MethodPost, "/api/quit", h...); resp.StatusCode != http.StatusForbidden ||
			e.Error.Code != "forbidden" {
			t.Errorf("%v: %d %+v", h, resp.StatusCode, e)
		}
	}
	select {
	case <-ts.quits:
		t.Error("a refused request quit")
	default:
	}
	// The page's own requests are answered.
	for _, h := range [][]string{
		{"Sec-Fetch-Site", "same-origin", "Origin", ts.http.URL},
		{"Sec-Fetch-Site", "none"},
	} {
		if resp, _ := send(ts.client, http.MethodGet, "/api/session", h...); resp.StatusCode != http.StatusOK {
			t.Errorf("%v: %d", h, resp.StatusCode)
		}
	}
}

func TestHostCheck(t *testing.T) {
	srv := New(Options{Token: testToken, Hosts: []string{"127.0.0.1:47821", "localhost:47821"}})
	defer srv.Shutdown()
	for host, want := range map[string]int{
		"127.0.0.1:47821": http.StatusSeeOther, "localhost:47821": http.StatusSeeOther,
		"evil.example:47821": http.StatusForbidden, "127.0.0.1:1": http.StatusForbidden, "": http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/?token="+testToken, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("host %q: %d, want %d", host, rec.Code, want)
		}
	}
}

func TestNoPackage(t *testing.T) {
	ts := newTestServer(t)
	var s Session
	if code := ts.do(http.MethodGet, "/api/session", nil, &s); code != http.StatusOK || s.Version != "test" ||
		s.Package != nil || s.Opening != nil || s.OpenError != nil || s.Recent == nil || s.Window {
		t.Errorf("session = %d %+v", code, s)
	}
	for _, p := range []string{"/api/review", "/api/review/images", "/api/review/images/1", "/api/review/labels"} {
		var e apiError
		if code := ts.do(http.MethodGet, p, nil, &e); code != http.StatusNotFound || !strings.Contains(e.Error.Message, "没有打开审图包") {
			t.Errorf("%s: %d %+v", p, code, e)
		}
	}
	var e apiError
	if code := ts.do(http.MethodPost, "/api/export", nil, &e); code != http.StatusNotFound {
		t.Errorf("export: %d %+v", code, e)
	}
	if code := ts.do(http.MethodGet, "/api/nothing", nil, &e); code != http.StatusNotFound || e.Error.Message != "接口不存在" {
		t.Errorf("unknown API: %d %+v", code, e)
	}
	if code := ts.do(http.MethodDelete, "/api/session", nil, &e); code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE session: %d", code)
	}
	ts.srv.SetWindow(true)
	if ts.do(http.MethodGet, "/api/session", nil, &s); !s.Window {
		t.Error("window not reported")
	}
}

func TestSummaryAndListing(t *testing.T) {
	ts := newTestServer(t)
	ts.openFolder()

	var sum Summary
	if code := ts.do(http.MethodGet, "/api/review", nil, &sum); code != http.StatusOK {
		t.Fatalf("summary: %d", code)
	}
	wantCounts := Counts{Total: 5, Inferred: 5, InferenceFailed: 1, Pending: 4, Approved: 1, Edited: 1}
	if sum.ID != 7 || sum.Name != "外观检测" || sum.Status != "ready" || sum.Project != nil || sum.Counts != wantCounts ||
		len(sum.Datasets) != 2 || sum.Datasets[0].Name != "外观 A" || sum.Datasets[0].Counts.Total != 3 ||
		sum.Datasets[1].Counts != (Counts{Total: 2, Inferred: 2, InferenceFailed: 1, Pending: 2, Edited: 1}) ||
		sum.Model.FileName != "best.pt" || sum.Options.Conf != 0.25 {
		t.Errorf("summary = %+v", sum)
	}

	var all list[Image]
	ts.do(http.MethodGet, "/api/review/images", nil, &all)
	if all.Total != 5 || !slices.Equal(ids(all.Items), []int64{101, 102, 103, 104, 105}) {
		t.Fatalf("images = %+v", all)
	}
	a := all.Items[0]
	if a.ReviewID != 7 || a.DatasetID != 11 || a.ImageID != 1101 || a.Position != 0 || a.Path != "NG/a.png" ||
		a.FileName != "a.png" || a.PackagePath != "line_a/NG/a.png" || a.AnnotationPath != "line_a/NG/a.xml" ||
		a.Width == nil || *a.Width != 40 || a.ImageVersion == "" || !a.Inferred || a.ObjectCount != 2 ||
		a.PredictionCount != 2 || a.Edited || a.Status != "pending" || a.Revision != 0 || a.ReviewedAt != nil ||
		a.MergedAt != nil || a.AnnotationVersion == "" ||
		!slices.Equal(a.Labels, []dataset.LabelCount{{Name: "dent", Count: 1}, {Name: "scratch", Count: 1}}) {
		t.Errorf("image a = %+v", a)
	}
	if d := all.Items[3]; d.InferenceError != "无法读取图片" || d.AnnotationVersion != "" || d.ObjectCount != 0 ||
		d.Labels == nil {
		t.Errorf("image d = %+v", d)
	}
	if e := all.Items[4]; !e.Edited || e.ObjectCount != 1 {
		t.Errorf("image e = %+v", e)
	}

	for _, tt := range []struct {
		query string
		want  []int64
		total int
	}{
		{query("status", "approved"), []int64{102}, 1},
		{query("status", "reviewed"), []int64{102}, 1},
		{query("status", "pending"), []int64{101, 103, 104, 105}, 4},
		{query("dataset_id", "12"), []int64{104, 105}, 2},
		{query("label", "dent"), []int64{101, 105}, 2},
		{query("label", "Dent"), []int64{}, 0},
		{query("q", "ng/"), []int64{101}, 1},
		{query("q", ".TIF"), []int64{105}, 1},
		{query("edited", "edited"), []int64{105}, 1},
		{query("edited", "unedited"), []int64{101, 102, 103, 104}, 4},
		{query("boxes", "without"), []int64{103, 104}, 2},
		{query("boxes", "with"), []int64{101, 102, 105}, 3},
		{query("inference", "failed"), []int64{104}, 1},
		{query("status", "pending", "boxes", "with"), []int64{101, 105}, 2},
		{query("limit", "2", "offset", "1"), []int64{102, 103}, 5},
		{query("offset", "9"), []int64{}, 5},
	} {
		var got list[Image]
		if code := ts.do(http.MethodGet, "/api/review/images"+tt.query, nil, &got); code != http.StatusOK ||
			!slices.Equal(ids(got.Items), tt.want) || got.Total != tt.total {
			t.Errorf("images%s = %d %v (%d), want %v (%d)", tt.query, code, ids(got.Items), got.Total, tt.want, tt.total)
		}
	}
	for _, q := range []string{query("status", "done"), query("limit", "0"), query("limit", "201"), query("offset", "-1"),
		query("dataset_id", "x"), query("edited", "yes"), query("boxes", "some"), query("inference", "ok")} {
		var e apiError
		if code := ts.do(http.MethodGet, "/api/review/images"+q, nil, &e); code != http.StatusBadRequest || e.Error.Code != "bad_request" {
			t.Errorf("images%s: %d %+v", q, code, e)
		}
	}

	// Neighbors among the pending images; b is not one of them.
	var n Neighbors
	ts.do(http.MethodGet, "/api/review/images/102/neighbors"+query("status", "pending"), nil, &n)
	if n.Prev == nil || n.Prev.ID != 101 || n.Next == nil || n.Next.ID != 103 || n.Position != 1 || n.Matches || n.Total != 4 {
		t.Errorf("neighbors of b = %+v", n)
	}
	ts.do(http.MethodGet, "/api/review/images/101/neighbors", nil, &n)
	if n.Prev != nil || n.Next == nil || n.Next.ID != 102 || n.Position != 0 || !n.Matches || n.Total != 5 {
		t.Errorf("neighbors of a = %+v", n)
	}
	var e apiError
	if code := ts.do(http.MethodGet, "/api/review/images/999/neighbors", nil, &e); code != http.StatusNotFound {
		t.Errorf("neighbors of a missing image: %d", code)
	}
	if code := ts.do(http.MethodGet, "/api/review/images/abc", nil, &e); code != http.StatusNotFound { // as on the platform
		t.Errorf("image abc: %d", code)
	}

	var labels list[Label]
	ts.do(http.MethodGet, "/api/review/labels", nil, &labels)
	if !slices.Equal(labels.Items, []Label{{Name: "dent", ObjectCount: 2, ImageCount: 2}, {Name: "scratch", ObjectCount: 2, ImageCount: 2}}) ||
		labels.Total != 2 {
		t.Errorf("labels = %+v", labels)
	}
	var classes ClassList
	ts.do(http.MethodGet, "/api/review/classes", nil, &classes)
	if classes.Total != 2 || classes.Items[0] != (reviewfile.Class{Name: "scratch", Color: "#ff0000"}) || classes.Version != "" {
		t.Errorf("classes = %+v", classes)
	}

	var d ImageDetail
	ts.do(http.MethodGet, "/api/review/images/101", nil, &d)
	if len(d.Objects) != 2 || len(d.Predictions) != 2 || d.DatasetName != "外观 A" || d.AnnotationError != "" ||
		d.Objects[0] != obj("scratch", 1, 1, 10, 10) {
		t.Errorf("detail of a = %+v", d)
	}
	ts.do(http.MethodGet, "/api/review/images/104", nil, &d)
	if d.Objects == nil || len(d.Objects) != 0 || d.Predictions == nil || d.AnnotationVersion != "" || d.DatasetName != "外观 B" {
		t.Errorf("detail of d = %+v", d)
	}
}

func TestSaveAnnotation(t *testing.T) {
	ts := newTestServer(t)
	dir := ts.openFolder()
	var c ImageDetail
	ts.do(http.MethodGet, "/api/review/images/103", nil, &c)
	base := c.AnnotationVersion

	save := func(id string, body any, out any) int {
		t.Helper()
		return ts.do(http.MethodPut, "/api/review/images/"+id+"/annotation", body, out)
	}
	var got ImageDetail
	if code := save("103", map[string]any{"objects": []dataset.AnnotationObject{obj(" scratch ", 1, 1, 50, 50)},
		"base_version": base}, &got); code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}
	// Normalized as on the platform (clamped to 40×30, name trimmed).
	if got.ObjectCount != 1 || !got.Edited || got.Objects[0] != obj("scratch", 1, 1, 40, 30) || got.AnnotationVersion == base ||
		got.Revision != 1 || got.Status != "pending" {
		t.Errorf("saved = %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "line_a", "c.xml"))
	if err != nil || reviewfile.AnnotationVersion(data) != got.AnnotationVersion || !bytes.Contains(data, []byte("<name>scratch</name>")) {
		t.Errorf("c.xml = %s, %v", data, err)
	}
	// Leftover temporary files are not left.
	if names := dirNames(t, filepath.Join(dir, "line_a")); len(names) != 5 {
		t.Errorf("line_a holds %v", names)
	}

	// The version the edit started from must still be the file's.
	var e apiError
	if code := save("103", map[string]any{"objects": []any{}, "base_version": base}, &e); code != http.StatusConflict ||
		e.Error.Code != "annotation_conflict" {
		t.Errorf("stale save: %d %+v", code, e)
	}
	if code := save("103", map[string]any{"objects": []any{}, "force": true}, &got); code != http.StatusOK ||
		got.ObjectCount != 0 || got.Edited {
		t.Errorf("forced save: %d %+v", code, got)
	}
	// Saving what the file holds changes nothing.
	before, _ := os.Stat(filepath.Join(dir, "line_a", "c.xml"))
	if code := save("103", map[string]any{"objects": []any{}, "base_version": got.AnnotationVersion}, &got); code != http.StatusOK {
		t.Errorf("same save: %d", code)
	}
	if after, _ := os.Stat(filepath.Join(dir, "line_a", "c.xml")); !after.ModTime().Equal(before.ModTime()) {
		t.Error("the same annotation was written again")
	}

	for _, tt := range []struct {
		body any
		msg  string
	}{
		{map[string]any{"base_version": ""}, "缺少 objects"},
		{map[string]any{"objects": []any{}}, "缺少 base_version"},
		{map[string]any{"objects": []dataset.AnnotationObject{obj("", 1, 1, 5, 5)}, "force": true}, ""},
		{map[string]any{"objects": []dataset.AnnotationObject{obj("a", 5, 5, 1, 1)}, "force": true}, ""},
	} {
		if code := save("103", tt.body, &e); code != http.StatusUnprocessableEntity || e.Error.Code != "validation_failed" ||
			!strings.Contains(e.Error.Message, tt.msg) {
			t.Errorf("save %v: %d %+v", tt.body, code, e)
		}
	}
	if code := save("999", map[string]any{"objects": []any{}, "force": true}, &e); code != http.StatusNotFound {
		t.Errorf("save of a missing image: %d", code)
	}

	// d had no VOC file: one is written.
	if code := save("104", map[string]any{"objects": []dataset.AnnotationObject{obj("dent", 2, 2, 8, 8)}, "base_version": ""},
		&got); code != http.StatusOK || got.ObjectCount != 1 || !got.Edited {
		t.Errorf("save of d: %d %+v", code, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "line_b", "d.xml")); err != nil {
		t.Error(err)
	}

	// A VOC file changed by another program (LabelImg) is read again.
	labelImg := `<annotation><size><width>40</width><height>30</height></size>
		<object><name>pit</name><bndbox><xmin>3</xmin><ymin>3</ymin><xmax>9</xmax><ymax>9</ymax></bndbox></object></annotation>`
	if err := os.WriteFile(filepath.Join(dir, "line_a", "NG", "a.xml"), []byte(labelImg), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodGet, "/api/review/images/101", nil, &got)
	if got.ObjectCount != 1 || got.Objects[0].Name != "pit" || !got.Edited {
		t.Errorf("a after LabelImg = %+v", got)
	}
	var labels list[Label]
	ts.do(http.MethodGet, "/api/review/labels", nil, &labels)
	if labels.Items[0].Name != "dent" || !slices.ContainsFunc(labels.Items, func(l Label) bool { return l.Name == "pit" }) {
		t.Errorf("labels = %+v", labels)
	}
	// A file that cannot be read is reported; only a forced save replaces it.
	if err := os.WriteFile(filepath.Join(dir, "line_a", "NG", "a.xml"), []byte("<annotation"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodGet, "/api/review/images/101", nil, &got)
	if got.AnnotationError == "" || got.ObjectCount != 0 || len(got.Objects) != 0 {
		t.Errorf("a with a broken file = %+v", got)
	}
	if code := save("101", map[string]any{"objects": []dataset.AnnotationObject{obj("dent", 2, 2, 8, 8)}, "force": true},
		&got); code != http.StatusOK || got.AnnotationError != "" {
		t.Errorf("forced save over a broken file: %d %+v", code, got)
	}
}

func TestSetStatus(t *testing.T) {
	ts := newTestServer(t)
	dir := ts.openFolder()
	var got ImageDetail
	if code := ts.do(http.MethodPut, "/api/review/images/103/status", map[string]string{"status": "needs_sample"}, &got); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	if got.Status != "needs_sample" || got.ReviewedAt == nil || !got.ReviewedAt.Equal(ts.clock.Now()) || got.Revision != 1 {
		t.Errorf("c = %+v", got.Image)
	}
	// b back to pending: its review time goes.
	ts.clock.Advance(time.Minute)
	ts.do(http.MethodPut, "/api/review/images/102/status", map[string]string{"status": "pending"}, &got)
	if got.Status != "pending" || got.ReviewedAt != nil || got.Revision != 3 {
		t.Errorf("b = %+v", got.Image)
	}
	var e apiError
	if code := ts.do(http.MethodPut, "/api/review/images/103/status", map[string]string{"status": "merged"}, &e); code != http.StatusUnprocessableEntity ||
		!strings.Contains(e.Error.Message, "status 必须是") {
		t.Errorf("bad status: %d %+v", code, e)
	}
	if code := ts.do(http.MethodPut, "/api/review/images/103/status", "x", &e); code != http.StatusBadRequest {
		t.Errorf("bad body: %d %+v", code, e)
	}

	// The progress file holds the changes only.
	data, err := os.ReadFile(filepath.Join(dir, reviewfile.ProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	var pf progressFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatal(err)
	}
	if pf.Format != progressFormat || pf.PackageID != strings.Repeat("ab", 16) || pf.ReviewID != 7 || len(pf.Images) != 2 ||
		pf.Images[0].ID != 102 || pf.Images[0].Status != "pending" || pf.Images[1].ID != 103 || pf.Images[1].ReviewedAt == nil {
		t.Errorf("progress = %s", data)
	}
	var sum Summary
	ts.do(http.MethodGet, "/api/review", nil, &sum)
	if sum.Counts.Pending != 4 || sum.Counts.Approved != 0 || sum.Counts.NeedsSample != 1 {
		t.Errorf("counts = %+v", sum.Counts)
	}

	// Reopened, the statuses are back.
	ts.do(http.MethodPost, "/api/close", nil, nil)
	s := ts.open(dir)
	if !s.Package.Resumed || s.Package.Counts.NeedsSample != 1 || s.Package.Counts.Approved != 0 {
		t.Errorf("reopened = %+v", s.Package)
	}
	ts.do(http.MethodGet, "/api/review/images/103", nil, &got)
	if got.Status != "needs_sample" || got.ReviewedAt == nil || got.Revision != 1 {
		t.Errorf("c reopened = %+v", got.Image)
	}
	// Back to the package's status: no change is left.
	ts.do(http.MethodPut, "/api/review/images/102/status", map[string]string{"status": "approved"}, &got)
	if got.Revision != 2 {
		t.Errorf("b approved again = %+v", got.Image)
	}
}

func TestImageFiles(t *testing.T) {
	ts := newTestServer(t)
	ts.openFolder()
	var a ImageDetail
	ts.do(http.MethodGet, "/api/review/images/101", nil, &a)

	resp, body := ts.get("/api/review/images/101/file")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("Cache-Control") != "private, no-cache" ||
		resp.Header.Get("Content-Disposition") != `inline; filename="a.png"; filename*=UTF-8''a.png` || len(body) == 0 {
		t.Errorf("file: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = ts.get("/api/review/images/101/file?v=" + a.ImageVersion)
	if resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Errorf("versioned file: %v", resp.Header)
	}
	if resp, _ = ts.get("/api/review/images/105/file"); resp.Header.Get("Content-Type") != "image/tiff" {
		t.Errorf("tiff: %v", resp.Header)
	}

	// Thumbnails: sm is a JPEG cached on disk; lg of a PNG is the PNG, of
	// a TIFF a JPEG.
	resp, body = ts.get("/api/review/images/101/thumbnail?size=sm")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("sm: %d %v", resp.StatusCode, resp.Header)
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("sm is no JPEG: %v", err)
	}
	cached := filepath.Join(ts.cache, "thumbs", strings.Repeat("ab", 16), "101-sm-"+a.ImageVersion+".jpg")
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("not cached: %v", err)
	}
	if resp, _ = ts.get("/api/review/images/101/thumbnail?size=lg"); resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("lg of a PNG: %v", resp.Header)
	}
	if resp, _ = ts.get("/api/review/images/105/thumbnail?size=lg"); resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("lg of a TIFF: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ = ts.get("/api/review/images/101/thumbnail?size=xl"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("size xl: %d", resp.StatusCode)
	}
	if resp, _ = ts.get("/api/review/images/999/thumbnail?size=sm"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("thumbnail of a missing image: %d", resp.StatusCode)
	}
}

func TestBrowse(t *testing.T) {
	ts := newTestServer(t)
	dir := t.TempDir()
	newTestPackage().writeFolder(t, filepath.Join(dir, "审图-7-外观检测"))
	for _, d := range []string{"Beta", "alpha", ".hidden", "x" + extractingMarker + "abc"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(dir, "old.zip")
	for _, f := range []string{"old.zip", "new.ZIP", "notes.txt", "审图-7-外观检测-审图结果.zip"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("zip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Same time for the new zips (so they sort by name), an hour earlier for old.zip.
	now := time.Now().Truncate(time.Second)
	for _, f := range []string{"new.ZIP", "审图-7-外观检测-审图结果.zip"} {
		if err := os.Chtimes(filepath.Join(dir, f), now, now); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	var b Browse
	if code := ts.do(http.MethodGet, "/api/browse"+query("path", dir), nil, &b); code != http.StatusOK {
		t.Fatalf("browse: %d", code)
	}
	var names, kinds []string
	for _, e := range b.Entries {
		names, kinds = append(names, e.Name), append(kinds, e.Kind)
	}
	if !slices.Equal(names, []string{"alpha", "Beta", "审图-7-外观检测", "new.ZIP", "审图-7-外观检测-审图结果.zip", "old.zip"}) ||
		!slices.Equal(kinds, []string{"folder", "folder", "package", "zip", "results", "zip"}) || b.Path != dir ||
		b.Parent != filepath.Dir(dir) || len(b.Places) != 1 || b.Entries[3].SizeBytes != 3 || b.Truncated {
		t.Errorf("browse = %v %v %+v", names, kinds, b)
	}
	ts.do(http.MethodGet, "/api/browse", nil, &b)
	if len(b.Entries) != 1 || b.Entries[0].Kind != kindHome || b.Entries[0].Path != "/home/test" || b.Path != "" {
		t.Errorf("places = %+v", b)
	}
	var e apiError
	if code := ts.do(http.MethodGet, "/api/browse"+query("path", filepath.Join(dir, "missing")), nil, &e); code != http.StatusNotFound {
		t.Errorf("missing folder: %d", code)
	}
	if code := ts.do(http.MethodGet, "/api/browse"+query("path", "relative"), nil, &e); code != http.StatusUnprocessableEntity {
		t.Errorf("relative folder: %d", code)
	}
	if code := ts.do(http.MethodGet, "/api/browse"+query("path", old), nil, &e); code != http.StatusUnprocessableEntity {
		t.Errorf("a file: %d", code)
	}
}

func TestHomePlaces(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Downloads", "Desktop"} {
		if err := os.Mkdir(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	places := homePlaces(home, map[string]string{
		kindDownloads: filepath.Join(home, "Downloads"), kindDesktop: filepath.Join(home, "Desktop"),
		kindDocuments: filepath.Join(home, "Documents"), // missing
	})
	var got []string
	for _, p := range places {
		got = append(got, p.Name+":"+p.Kind)
	}
	if !slices.Equal(got, []string{"下载:downloads", "桌面:desktop", "主文件夹:home"}) {
		t.Errorf("places = %v", got)
	}
}

func TestRecent(t *testing.T) {
	ts := newTestServer(t)
	dir := ts.openFolder()
	var s Session
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if len(s.Recent) != 1 || s.Recent[0].Path != dir || !s.Recent[0].Exists || s.Recent[0].PackageID != strings.Repeat("ab", 16) {
		t.Errorf("recent = %+v", s.Recent)
	}
	// Kept in the configuration folder for the next run.
	again := New(Options{ConfigDir: ts.config, Token: testToken})
	defer again.Shutdown()
	if r := again.Session().Recent; len(r) != 1 || r[0].Path != dir {
		t.Errorf("recent of the next run = %+v", r)
	}
	if code := ts.do(http.MethodPost, "/api/recent/remove", map[string]string{"path": dir}, nil); code != http.StatusNoContent {
		t.Errorf("remove: %d", code)
	}
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if len(s.Recent) != 0 {
		t.Errorf("recent after removing = %+v", s.Recent)
	}
	// A folder that is gone is listed as such.
	ts.do(http.MethodPost, "/api/close", nil, nil)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	ts.do(http.MethodGet, "/api/session", nil, &s)
	if len(s.Recent) != 1 || s.Recent[0].Exists {
		t.Errorf("recent of a removed folder = %+v", s.Recent)
	}
}

func TestRevealQuitClosing(t *testing.T) {
	ts := newTestServer(t)
	dir := ts.openFolder()
	var e apiError
	if code := ts.do(http.MethodPost, "/api/reveal", map[string]string{"target": "results"}, &e); code != http.StatusConflict ||
		e.Error.Message != "还没有导出审图结果" {
		t.Errorf("reveal before exporting: %d %+v", code, e)
	}
	if code := ts.do(http.MethodPost, "/api/reveal", map[string]string{"target": "x"}, &e); code != http.StatusUnprocessableEntity {
		t.Errorf("reveal x: %d", code)
	}
	if code := ts.do(http.MethodPost, "/api/reveal", map[string]string{"target": "folder"}, nil); code != http.StatusNoContent {
		t.Errorf("reveal folder: %d", code)
	}
	var res ExportResult
	ts.do(http.MethodPost, "/api/export", nil, &res)
	if code := ts.do(http.MethodPost, "/api/reveal", map[string]string{"target": "results"}, nil); code != http.StatusNoContent {
		t.Errorf("reveal results: %d", code)
	}
	ts.mu.Lock()
	if !slices.Equal(ts.reveals, []string{dir, dir + "-审图结果.zip"}) {
		t.Errorf("revealed %v", ts.reveals)
	}
	ts.mu.Unlock()

	if code := ts.do(http.MethodPost, "/api/closing", nil, nil); code != http.StatusNoContent {
		t.Errorf("closing: %d", code)
	}
	select {
	case <-ts.closing:
	default:
		t.Error("closing not told")
	}
	if code := ts.do(http.MethodPost, "/api/quit", nil, nil); code != http.StatusNoContent {
		t.Errorf("quit: %d", code)
	}
	select {
	case <-ts.quits:
	case <-time.After(5 * time.Second):
		t.Error("quit not told")
	}
}

func TestWeb(t *testing.T) {
	built := fstest.MapFS{
		"dist/reviewer.html":      {Data: []byte("<!doctype html><title>built</title>")},
		"dist/assets/app-1.js":    {Data: []byte("console.log(1)")},
		"dist/favicon.svg":        {Data: []byte("<svg/>")},
		"placeholder.html":        {Data: []byte("placeholder")},
		"dist/assets/style-2.css": {Data: []byte("body{}")},
	}
	ts := newTestServer(t, func(o *Options) { o.Web = built })
	for _, tt := range []struct {
		path, body, cache string
		status            int
	}{
		{"/", "<!doctype html><title>built</title>", "no-cache", http.StatusOK},
		{"/review", "<!doctype html><title>built</title>", "no-cache", http.StatusOK},
		{"/reviewer.html", "<!doctype html><title>built</title>", "no-cache", http.StatusOK},
		{"/assets/app-1.js", "console.log(1)", "public, max-age=31536000, immutable", http.StatusOK},
		{"/favicon.svg", "<svg/>", "no-cache", http.StatusOK},
		{"/assets/missing.js", "", "", http.StatusNotFound},
		{"/../placeholder.html", "", "", http.StatusNotFound},
	} {
		resp, body := ts.get(tt.path)
		if resp.StatusCode != tt.status || (tt.body != "" && string(body) != tt.body) ||
			(tt.cache != "" && resp.Header.Get("Cache-Control") != tt.cache) {
			t.Errorf("%s: %d %q %v", tt.path, resp.StatusCode, body, resp.Header)
		}
	}
	if code := ts.do(http.MethodPost, "/review", nil, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("POST /review: %d", code)
	}

	// Built without the frontend: the placeholder.
	ts = newTestServer(t, func(o *Options) { o.Web = fstest.MapFS{"placeholder.html": {Data: []byte("placeholder")}} })
	if resp, body := ts.get("/review"); resp.StatusCode != http.StatusOK || string(body) != "placeholder" ||
		resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("placeholder: %d %q", resp.StatusCode, body)
	}
	ts = newTestServer(t)
	if resp, body := ts.get("/"); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "没有构建") {
		t.Errorf("without any page: %d %q", resp.StatusCode, body)
	}
}

func TestContentDisposition(t *testing.T) {
	for name, want := range map[string]string{
		"a.png":     `inline; filename="a.png"; filename*=UTF-8''a.png`,
		"划痕 1.png":  `inline; filename="__ 1.png"; filename*=UTF-8''%E5%88%92%E7%97%95%201.png`,
		`a"b\c.png`: `inline; filename="a_b_c.png"; filename*=UTF-8''a%22b%5Cc.png`,
	} {
		if got := contentDisposition("inline", name); got != want {
			t.Errorf("contentDisposition(%q) = %s", name, got)
		}
	}
}
