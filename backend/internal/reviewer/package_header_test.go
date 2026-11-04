package reviewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An edit sent for a package that is no longer open (the page was still
// showing it when another package was opened) is refused; one for the open
// package, or without the header, is saved.
func TestWritesNameTheirPackage(t *testing.T) {
	ts := newTestServer(t)
	ts.openFolder()
	var sess Session
	ts.do(http.MethodGet, "/api/session", nil, &sess)
	if sess.Package == nil || sess.Package.PackageID == "" {
		t.Fatalf("session = %+v", sess)
	}
	put := func(path string, body any, pkg string) (int, []byte) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, ts.http.URL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if pkg != "" {
			req.Header.Set(PackageHeader, pkg)
		}
		resp, err := ts.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.Bytes()
	}
	var before ImageDetail
	ts.do(http.MethodGet, "/api/review/images/103", nil, &before)
	other := strings.Repeat("f", 32)
	status := map[string]string{"status": "needs_sample"}
	if code, body := put("/api/review/images/103/status", status, other); code != http.StatusConflict ||
		!strings.Contains(string(body), "另一个审图包") {
		t.Errorf("status for another package: %d %s", code, body)
	}
	annotation := map[string]any{"objects": []any{}, "force": true}
	if code, body := put("/api/review/images/103/annotation", annotation, other); code != http.StatusConflict {
		t.Errorf("annotation for another package: %d %s", code, body)
	}
	var after ImageDetail
	ts.do(http.MethodGet, "/api/review/images/103", nil, &after)
	if after.Status != before.Status || after.Revision != before.Revision || after.AnnotationVersion != before.AnnotationVersion {
		t.Errorf("refused writes changed the image: %+v -> %+v", before.Image, after.Image)
	}
	if code, body := put("/api/review/images/103/status", status, sess.Package.PackageID); code != http.StatusOK {
		t.Errorf("status for the open package: %d %s", code, body)
	}
	if code, body := put("/api/review/images/102/status", status, ""); code != http.StatusOK {
		t.Errorf("status without the header: %d %s", code, body)
	}
}
