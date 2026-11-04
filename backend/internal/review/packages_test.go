package review

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// exchangeReview creates a review of two datasets whose folders clash
// once made valid on Windows, and infers most of it:
//
//	line_a: NG/b.png (2 boxes), a.png (1 box), c.bmp (no box)
//	line:a (folder line_a~<id>): d.png (inference failed), e.png (not inferred), g.png (1 box)
//
// It returns the review, its images in review order (b, a, c, d, e, g),
// the datasets and the node.
func (e *testEnv) exchangeReview(t *testing.T) (Review, []Image, [2]int64, int64) {
	t.Helper()
	ctx := context.Background()
	m := e.addModel(t, e.project(t, "外观检测"), testModel{})
	ds1 := e.addDataset(t, "line-a", "line_a", testImage{rel: "a.png"}, testImage{rel: "NG/b.png"}, testImage{rel: "c.bmp"})
	ds2 := e.addDataset(t, "line-a 夜班", "line:a", testImage{rel: "d.png"}, testImage{rel: "e.png"}, testImage{rel: "g.png"})
	r := e.create(t, CreateInput{ModelID: m.ID, DatasetIDs: []int64{ds1, ds2}})
	images := e.images(t, r.ID, ImageQuery{})
	if got := paths(images); !slices.Equal(got, []string{"NG/b.png", "a.png", "c.bmp", "d.png", "e.png", "g.png"}) {
		t.Fatalf("images = %v", got)
	}
	if _, err := e.svc.CreatePackage(ctx, r.ID, PackageInput{}); err == nil {
		t.Error("CreatePackage before inference succeeded")
	} else {
		wantKind(t, err, ErrConflict, "还没有识别完成的图片")
	}
	node := e.node(t, "node-1")
	e.claim(t, node)
	if _, err := e.svc.SubmitResults(ctx, r.ID, node, []ImageResult{
		{ID: images[0].ID, OK: true, Boxes: []ResultBox{box("scratch", 0.9, 1, 1, 10, 10), box("dent", 0.8, 2, 2, 12, 12)}},
		{ID: images[1].ID, OK: true, Boxes: []ResultBox{box("scratch", 0.7, 3, 3, 20, 20)}},
		{ID: images[2].ID, OK: true},
		{ID: images[3].ID, OK: false, Error: "无法读取图片"},
		{ID: images[5].ID, OK: true, Boxes: []ResultBox{box("dent", 0.6, 5, 5, 20, 20)}},
	}); err != nil {
		t.Fatal(err)
	}
	return e.review(t, r.ID), e.images(t, r.ID, ImageQuery{}), [2]int64{ds1, ds2}, node
}

// downloadPackage prepares and writes a package and returns its bytes.
func (e *testEnv) downloadPackage(t *testing.T, reviewID int64, in PackageInput) ([]byte, PackageTicket) {
	t.Helper()
	ctx := context.Background()
	ticket, err := e.svc.CreatePackage(ctx, reviewID, in)
	if err != nil {
		t.Fatalf("CreatePackage: %v", err)
	}
	return e.fetchPackage(t, reviewID, ticket.Token), ticket
}

func (e *testEnv) fetchPackage(t *testing.T, reviewID int64, token string) []byte {
	t.Helper()
	p, err := e.svc.OpenPackage(context.Background(), reviewID, token)
	if err != nil {
		t.Fatalf("OpenPackage: %v", err)
	}
	defer p.Close()
	var b bytes.Buffer
	if err := p.Write(context.Background(), &b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return b.Bytes()
}

// zipEntries reads a zip archive: its entries in order and their content.
func zipEntries(t *testing.T, data []byte) ([]*zip.File, map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	content := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		content[f.Name] = b
	}
	return zr.File, content
}

// packageManifest reads review.json of a package.
func packageManifest(t *testing.T, content map[string][]byte, top string) *reviewfile.Manifest {
	t.Helper()
	data, ok := content[top+"/"+reviewfile.ManifestFile]
	if !ok {
		t.Fatalf("no review.json in the package")
	}
	m, err := reviewfile.ReadManifest(bytes.NewReader(data), 1<<20)
	if err != nil {
		t.Fatalf("ReadManifest: %v\n%s", err, data)
	}
	return m
}

func TestIntegrationPackage(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, images, ds, _ := e.exchangeReview(t)
	b, a, c, d := images[0], images[1], images[2], images[3]

	// Validation.
	_, err := e.svc.CreatePackage(ctx, r.ID, PackageInput{Statuses: []string{"done"}})
	wantKind(t, err, ErrInvalid, `"done" 无效`)
	_, err = e.svc.CreatePackage(ctx, r.ID, PackageInput{DatasetIDs: []int64{ds[0], 999}})
	wantKind(t, err, ErrInvalid, "数据集 #999 不在这个审图任务中")
	_, err = e.svc.CreatePackage(ctx, 999, PackageInput{})
	wantKind(t, err, ErrNotFound, "审图任务不存在")
	_, err = e.svc.CreatePackage(ctx, r.ID, PackageInput{Statuses: []string{reviewfile.StatusApproved}})
	wantKind(t, err, ErrConflict, "没有符合条件的图片")

	// Some review work on the platform; g's file disappears from its
	// dataset.
	if _, err := e.svc.SetStatus(ctx, r.ID, a.ID, reviewfile.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveAnnotation(ctx, r.ID, b.ID, SaveInput{BaseVersion: b.AnnotationVersion,
		Objects: []dataset.AnnotationObject{{Name: "dent", Xmin: 2, Ymin: 2, Xmax: 12, Ymax: 12}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.datasetPath("line:a", "g.png")); err != nil {
		t.Fatal(err)
	}

	data, ticket := e.downloadPackage(t, r.ID, PackageInput{})
	top := fmt.Sprintf("审图-%d-外观检测 v1 审图", r.ID)
	if ticket.FileName != top+".zip" || ticket.ImageCount != 5 ||
		!ticket.ExpiresAt.Equal(e.clock.Now().Add(time.Hour)) || ticket.Token == "" {
		t.Errorf("ticket = %+v", ticket)
	}
	folder2 := fmt.Sprintf("line_a~%d", ds[1])
	files, content := zipEntries(t, data)
	var names []string
	methods := map[string]uint16{}
	for _, f := range files {
		names = append(names, strings.TrimPrefix(f.Name, top+"/"))
		methods[f.Name] = f.Method
	}
	wantNames := []string{"README.txt", "line_a/NG/b.png", "line_a/NG/b.xml", "line_a/a.png", "line_a/a.xml",
		"line_a/c.bmp", "line_a/c.xml", folder2 + "/d.png", "review.json"}
	if !slices.Equal(names, wantNames) {
		t.Errorf("entries = %q\nwant %q", names, wantNames)
	}
	for name, want := range map[string]uint16{"line_a/a.png": zip.Store, "line_a/c.bmp": zip.Deflate,
		"line_a/a.xml": zip.Deflate, "review.json": zip.Deflate, "README.txt": zip.Deflate} {
		if got := methods[top+"/"+name]; got != want {
			t.Errorf("method of %s = %d, want %d", name, got, want)
		}
	}
	if !bytes.Equal(content[top+"/line_a/a.png"], readFile(t, e.datasetPath("line_a", "a.png"))) ||
		!bytes.Equal(content[top+"/line_a/NG/b.xml"], readFile(t, e.reviewFile(r.ID, b.AnnotationPath))) {
		t.Error("packed files differ from the originals")
	}
	readme := string(content[top+"/README.txt"])
	if !strings.HasPrefix(readme, "\ufefftrainMaster 审图包\r\n") || !strings.Contains(readme, fmt.Sprintf("审图任务：#%d 外观检测 v1 审图", r.ID)) ||
		!strings.Contains(readme, "模型：外观检测 v1（YOLO11，检测）") || !strings.Contains(readme, "数据集：line-a、line-a 夜班") ||
		!strings.Contains(readme, "包含的图片：全部状态") {
		t.Errorf("README.txt =\n%s", readme)
	}

	m := packageManifest(t, content, top)
	if m.Kind != reviewfile.KindPackage || len(m.PackageID) != 32 || m.Review.ID != r.ID || m.Review.Name != r.Name ||
		!m.ExportedAt.Equal(e.clock.Now()) || m.Model.ID != r.Model.ID || m.Options != r.Options ||
		!strings.HasPrefix(m.Platform, "trainMaster ") {
		t.Errorf("manifest = %+v", m)
	}
	if fmt.Sprint(m.Datasets) != fmt.Sprintf("[{%d line-a line_a} {%d line-a 夜班 %s}]", ds[0], ds[1], folder2) ||
		fmt.Sprint(m.Classes) != "[{scratch } {dent }]" {
		t.Errorf("datasets = %v, classes = %v", m.Datasets, m.Classes)
	}
	if len(m.Images) != 4 {
		t.Fatalf("manifest images = %+v", m.Images)
	}
	got := map[int64]reviewfile.ManifestImage{}
	for _, img := range m.Images {
		got[img.ID] = img
	}
	mb, ma, mc, md := got[b.ID], got[a.ID], got[c.ID], got[d.ID]
	if ma.Path != "line_a/a.png" || ma.SourcePath != "a.png" || ma.Annotation != "line_a/a.xml" || !ma.HasAnnotation ||
		ma.DatasetID != ds[0] || ma.ImageID != a.ImageID || *ma.Width != 40 || *ma.Height != 30 ||
		ma.BaseStatus != reviewfile.StatusApproved || ma.Status != reviewfile.StatusApproved || ma.BaseRevision != 1 ||
		ma.ReviewedAt == nil || ma.Edited || len(ma.Predictions) != 1 ||
		ma.BaseAnnotationVersion != reviewfile.AnnotationVersion(content[top+"/line_a/a.xml"]) {
		t.Errorf("manifest image a = %+v", ma)
	}
	if !mb.Edited || mb.BaseRevision != 1 || mb.BaseStatus != reviewfile.StatusPending || len(mb.Predictions) != 2 ||
		mb.BaseAnnotationVersion != e.detail(t, r.ID, b.ID).AnnotationVersion {
		t.Errorf("manifest image b = %+v", mb)
	}
	if !mc.HasAnnotation || len(mc.Predictions) != 0 || mc.Predictions == nil {
		t.Errorf("manifest image c = %+v", mc)
	}
	if md.HasAnnotation || md.BaseAnnotationVersion != "" || md.InferenceError != "无法读取图片" || md.Path != folder2+"/d.png" {
		t.Errorf("manifest image d = %+v", md)
	}
	if m.Images[0].ID != b.ID || m.Images[3].ID != d.ID {
		t.Errorf("manifest order = %+v", m.Images)
	}
	if _, ok := got[images[5].ID]; ok { // g, whose file is missing
		t.Error("the manifest lists an image the package does not hold")
	}

	// Every fetch is a new package; the ticket lasts an hour.
	again := packageManifest(t, func() map[string][]byte { _, c := zipEntries(t, e.fetchPackage(t, r.ID, ticket.Token)); return c }(), top)
	if again.PackageID == m.PackageID {
		t.Error("two fetches have the same package ID")
	}
	_, err = e.svc.OpenPackage(ctx, r.ID+1, ticket.Token)
	wantKind(t, err, ErrNotFound, "下载链接已失效")
	e.clock.Advance(time.Hour + time.Second)
	_, err = e.svc.OpenPackage(ctx, r.ID, ticket.Token)
	wantKind(t, err, ErrNotFound, "下载链接已失效")

	// Filters.
	data, ticket = e.downloadPackage(t, r.ID, PackageInput{Statuses: []string{reviewfile.StatusApproved, reviewfile.StatusApproved}})
	files, content = zipEntries(t, data)
	if m := packageManifest(t, content, top); len(files) != 4 || ticket.ImageCount != 1 || len(m.Images) != 1 ||
		m.Images[0].ID != a.ID || len(m.Datasets) != 2 || !strings.Contains(string(content[top+"/README.txt"]), "包含的图片：已审核状态") {
		t.Errorf("approved package: %d entries, ticket %+v, manifest %+v", len(files), ticket, m)
	}
	data, ticket = e.downloadPackage(t, r.ID, PackageInput{DatasetIDs: []int64{ds[1]}})
	files, content = zipEntries(t, data)
	if m := packageManifest(t, content, top); len(files) != 3 || ticket.ImageCount != 2 || len(m.Images) != 1 ||
		m.Images[0].ID != d.ID || len(m.Datasets) != 1 || m.Datasets[0].ID != ds[1] {
		t.Errorf("dataset package: %d entries, ticket %+v, manifest %+v", len(files), ticket, m)
	}

	// A review deleted while its package is sent: the archive breaks off.
	ticket, err = e.svc.CreatePackage(ctx, r.ID, PackageInput{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.OpenPackage(ctx, r.ID, ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := e.svc.DeleteReview(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(ctx, io.Discard); err == nil {
		t.Error("Write of a deleted review's package succeeded")
	}
}

func TestIntegrationPackageMissingFolders(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	r, _, _, _ := e.exchangeReview(t)
	// The review folder is gone (by hand): images without VOC files. A
	// dataset folder is gone: its images are left out.
	if err := os.RemoveAll(e.svc.reviewDir(r.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(e.st.Dir(storage.Datasets), "line:a")); err != nil {
		t.Fatal(err)
	}
	data, _ := e.downloadPackage(t, r.ID, PackageInput{})
	files, content := zipEntries(t, data)
	top := strings.TrimSuffix(files[0].Name, "/README.txt")
	m := packageManifest(t, content, top)
	if len(files) != 5 || len(m.Images) != 3 {
		t.Fatalf("package: %d entries, %d images", len(files), len(m.Images))
	}
	for _, img := range m.Images {
		if img.HasAnnotation || img.BaseAnnotationVersion != "" {
			t.Errorf("image without VOC file = %+v", img)
		}
	}
	_ = ctx
}
