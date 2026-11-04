package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
	"github.com/icaruszezen/trainmaster/backend/internal/review"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// TestReviewAgentImageFileMissing: an image the platform cannot read from
// its dataset answers 404 not_found, also when the dataset folder is gone
// (409 would make the node give up the whole review), and the node still
// holds the review.
func TestReviewAgentImageFileMissing(t *testing.T) {
	e := newReviewEnv(t, "")
	p := e.project(t, "p")
	m, _ := e.uploadModel(t, p.ID, "detect")
	ds := e.dataset(t, "ds", "a.png", "b.png")
	rv := want[review.Review](t, e.do("POST", "/api/v1/reviews", map[string]any{"model_id": m.ID,
		"dataset_ids": []int64{ds.ID}}), 201)
	want[compute.RegisterResult](t, e.do("POST", "/api/v1/compute/agent/register",
		registerBody("n1", "n1", "null", "null")), 200)
	node := []string{"X-Node-Key", "n1"}
	want[map[string]any](t, e.do("POST", "/api/v1/compute/agent/tasks/claim", `{"kinds": ["review_inference"]}`, node...), 200)
	agent := fmt.Sprintf("/api/v1/compute/agent/review-inferences/%d", rv.ID)
	page := want[map[string]any](t, e.do("GET", agent+"/images", nil, node...), 200)
	items := page["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("images = %v", page)
	}
	urlA := object(t, items[0], "image")["url"].(string)
	urlB := object(t, items[1], "image")["url"].(string)

	dir := filepath.Join(e.st.Dir(storage.Datasets), "ds")
	if err := os.Remove(filepath.Join(dir, "b.png")); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", urlB, nil, node...), 404, codeNotFound, "图片文件不存在")
	if err := os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", urlA, nil, node...), 404, codeNotFound, "数据集文件夹不存在")
	if err := os.Rename(dir+"-moved", dir); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", urlA, nil, node...); rec.Code != 200 {
		t.Errorf("image file after the folder is back = %d %s", rec.Code, rec.Body.String())
	}
	want[map[string]any](t, e.do("POST", agent+"/progress", map[string]any{"stage": "inferring"}, node...), 200)
}
