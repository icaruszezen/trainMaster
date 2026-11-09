package dataversion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Tests of the scopes (参与训练的图片): which images of each source a
// version takes.

func TestScopeNormalizeInput(t *testing.T) {
	in := coreInput(1, "v", SourceInput{DatasetID: 3, IncludeBase: true},
		SourceInput{DatasetID: 4, IncludeBase: true, Scope: ScopeBackground},
		SourceInput{DatasetID: 5, IncludeBase: true, Scope: ScopeAnnotated})
	in.Scope = ScopeAll
	req, err := normalizeInput(in, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, src := range req.sources {
		got = append(got, src.Scope)
	}
	if !slices.Equal(got, []string{ScopeAll, ScopeBackground, ScopeAnnotated}) || req.config.Scope != ScopeAll {
		t.Errorf("scopes = %v, default %q", got, req.config.Scope)
	}

	for _, bad := range []string{"", "Annotated", "none", "background "} {
		in := coreInput(1, "v", coreBase(3)...)
		in.Scope = bad
		_, err := normalizeInput(in, false)
		coreWantKind(t, err, ErrInvalid, "参与训练的图片无效")
	}
	in = coreInput(1, "v", SourceInput{DatasetID: 7, IncludeBase: true, Scope: "everything"})
	_, err = normalizeInput(in, false)
	coreWantKind(t, err, ErrInvalid, "数据集 ID 7 参与训练的图片无效")
	// The other checks come first, as before scopes.
	in = coreInput(0, "v", coreBase(3)...)
	in.Scope = ""
	_, err = normalizeInput(in, false)
	coreWantKind(t, err, ErrInvalid, "项目不存在")
}

// scopeFiles is coreLineFiles(10) (p00..p09 and dent with objects, empty
// with an annotation without objects, none without one, dirt with an
// unlisted label) plus two more images without an annotation, an
// unannotated GIF and an annotation that cannot be read.
func scopeFiles(t *testing.T) map[string][]byte {
	files := coreLineFiles(t, 10)
	files["BG/bg0.png"] = corePNG(t, 32, 24, 200)
	files["BG/bg1.png"] = corePNG(t, 32, 24, 201)
	files["BG/anim.gif"] = coreGIF(t)
	files["NG/bad.png"] = corePNG(t, 32, 24, 202)
	files["NG/bad.xml"] = []byte("<annotation><object>")
	return files
}

func TestCoreScopePreviewAndBuild(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "line", scopeFiles(t))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	cv := e.coreClassVersion(t, p.ID)

	cases := []struct {
		name         string
		scope        string
		includeEmpty bool
		images       int
		negatives    int
		skipped      Skipped
		unlisted     int // labels listed as not in the class list
	}{
		// p00..p09, dent, empty.
		{"annotated", ScopeAnnotated, true, 12, 1,
			Skipped{Unannotated: 4, Invalid: 1, Unlisted: 1}, 1},
		{"annotated without negatives", ScopeAnnotated, false, 11, 0,
			Skipped{Unannotated: 4, Invalid: 1, Unlisted: 1, Empty: 1}, 1},
		// empty, none, bg0, bg1; the GIF cannot be trained.
		{"background", ScopeBackground, true, 4, 4,
			Skipped{Invalid: 1, WithObjects: 12, UnsupportedFormat: 1}, 0},
		{"background ignores include_empty", ScopeBackground, false, 4, 4,
			Skipped{Invalid: 1, WithObjects: 12, UnsupportedFormat: 1}, 0},
		{"all", ScopeAll, true, 15, 4,
			Skipped{Invalid: 1, Unlisted: 1, UnsupportedFormat: 1}, 1},
		{"all ignores include_empty", ScopeAll, false, 15, 4,
			Skipped{Invalid: 1, Unlisted: 1, UnsupportedFormat: 1}, 1},
	}
	for _, c := range cases {
		in := coreInput(p.ID, cv, coreBase(d.ID)...)
		in.Scope, in.IncludeEmpty = c.scope, c.includeEmpty
		pre, err := e.svc.Preview(ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		negatives := pre.Splits.Train.Negatives + pre.Splits.Val.Negatives + pre.Splits.Test.Negatives
		if pre.ImageCount != c.images || negatives != c.negatives || pre.Skipped != c.skipped ||
			len(pre.UnlistedLabels) != c.unlisted || len(pre.Sources) != 1 || pre.Sources[0].Scope != c.scope ||
			pre.Sources[0].ImageCount != c.images || pre.Sources[0].Negatives != c.negatives ||
			pre.Sources[0].SelectedCount != 18 {
			t.Errorf("%s: preview = %+v", c.name, pre)
		}

		// The build takes the same images.
		v := e.coreCreateBuilt(t, in)
		st := v.Stats
		if v.ImageCount != c.images || st.Negatives.Train+st.Negatives.Val+st.Negatives.Test != c.negatives ||
			st.Skipped != c.skipped || v.Config.Scope != c.scope || len(v.Sources) != 1 ||
			v.Sources[0].Scope != c.scope || v.Sources[0].ImageCount != c.images {
			t.Errorf("%s: version = %+v", c.name, v)
		}
		dir := e.coreDir(t, v)
		rows := e.coreRows(t, v.ID)
		var paths []string
		for _, r := range rows {
			paths = append(paths, r.SourcePath)
			if c.scope == ScopeBackground && (r.ObjectCount != 0 || len(r.Objects) != 0) {
				t.Errorf("%s: %s has objects in a background source", c.name, r.SourcePath)
			}
		}
		// An image without an annotation file is a negative sample: an
		// empty label file.
		hasNone := slices.Contains(paths, "NG/none.png")
		if hasNone != (c.scope != ScopeAnnotated) {
			t.Errorf("%s: NG/none.png taken = %v (%v)", c.name, hasNone, paths)
		}
		if hasNone {
			if got, ok := coreFileExists(t, dir, "labels/line/NG/none.txt"); !ok || got != "" {
				t.Errorf("%s: label file of NG/none.png = %q, %v", c.name, got, ok)
			}
		}
		var m struct {
			Config  struct{ Scope string }
			Sources []struct{ Scope string }
		}
		if err := json.Unmarshal([]byte(coreReadFile(t, dir, manifestFile)), &m); err != nil ||
			m.Config.Scope != c.scope || len(m.Sources) != 1 || m.Sources[0].Scope != c.scope {
			t.Errorf("%s: version.json = %+v, %v", c.name, m, err)
		}
	}
}

// coreFileExists reads a file of a version folder.
func coreFileExists(t *testing.T, dir, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	return string(data), err == nil
}

func TestCoreScopePerSource(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	a := e.coreDataset(t, "a", coreLineFiles(t, 6))
	ok := map[string][]byte{}
	for i := range 5 {
		ok[fmt.Sprintf("OK/%d.png", i)] = corePNG(t, 32, 24, 300+i)
	}
	b := e.coreDataset(t, "b-ok", ok)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, a.ID, b.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID),
		SourceInput{DatasetID: b.ID, IncludeBase: true, VersionIDs: []int64{}, Scope: ScopeBackground},
		SourceInput{DatasetID: a.ID, IncludeBase: true, VersionIDs: []int64{}})
	pre, err := e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// a: p00..p05, dent, empty; b: its 5 images as negatives.
	if len(pre.Sources) != 2 || pre.Sources[0].DatasetID != a.ID || pre.Sources[0].Scope != ScopeAnnotated ||
		pre.Sources[0].ImageCount != 8 || pre.Sources[0].Negatives != 1 || pre.Sources[1].Scope != ScopeBackground ||
		pre.Sources[1].ImageCount != 5 || pre.Sources[1].Negatives != 5 || pre.ImageCount != 13 ||
		pre.Skipped != (Skipped{Unannotated: 1, Unlisted: 1}) {
		t.Errorf("preview = %+v", pre)
	}
	v := e.coreCreateBuilt(t, in)
	if v.Config.Scope != ScopeAnnotated || len(v.Sources) != 2 || v.Sources[0].Scope != ScopeAnnotated ||
		v.Sources[1].Scope != ScopeBackground || v.Sources[1].ImageCount != 5 || v.ImageCount != 13 {
		t.Errorf("version = %+v", v)
	}

	// A retry keeps the scopes.
	if _, err := e.db.Exec(ctx, "UPDATE data_versions SET status = 'failed', error = 'x' WHERE id = $1", v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM data_version_images WHERE version_id = $1", v.ID); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.Retry(ctx, v.ID)
	if err != nil || r.Sources[1].Scope != ScopeBackground {
		t.Fatalf("Retry = %+v, %v", r, err)
	}
}

func TestCoreScopeReviewVersions(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	files := map[string][]byte{}
	coreLabeled(t, files, "a.png", 1, "scratch")
	files["b.png"] = corePNG(t, 32, 24, 2)
	files["c.png"] = corePNG(t, 32, 24, 3)
	files["d.png"] = corePNG(t, 32, 24, 4)
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch"}, d.ID)
	// The review annotation of b has a box, c's has none.
	rv := e.coreReviewVersion(t, d.ID, coreVersionImage{path: "b.png", status: "needs_sample", labels: []string{"scratch"}},
		coreVersionImage{path: "c.png", status: "needs_sample"})
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID),
		SourceInput{DatasetID: d.ID, IncludeBase: true, VersionIDs: []int64{rv.ID}})
	in.Scope = ScopeBackground
	pre, err := e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Taken: c (empty review annotation) and d (no annotation).
	if pre.ImageCount != 2 || pre.Skipped != (Skipped{WithObjects: 2}) || pre.Sources[0].FromVersions != 2 {
		t.Errorf("preview = %+v", pre)
	}
	v := e.coreCreateBuilt(t, in)
	var taken []string
	for _, r := range e.coreRows(t, v.ID) {
		taken = append(taken, r.SourcePath)
	}
	slices.Sort(taken)
	if !slices.Equal(taken, []string{"c.png", "d.png"}) || v.Stats.Skipped != pre.Skipped {
		t.Errorf("taken = %v, stats = %+v", taken, v.Stats)
	}
}

// TestCoreScopeLegacyVersions: versions stored before scopes (no scope in
// config and sources) read and build as ScopeAnnotated.
func TestCoreScopeLegacyVersions(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "line", coreLineFiles(t, 6))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	in.Scope = ScopeAll
	v, err := e.svc.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE data_versions SET config = config - 'scope',
		sources = (SELECT jsonb_agg(s - 'scope') FROM jsonb_array_elements(sources) s) WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := e.db.QueryRow(ctx, "SELECT config::text || sources::text FROM data_versions WHERE id = $1", v.ID).Scan(&raw); err != nil ||
		strings.Contains(raw, "scope") {
		t.Fatalf("stripped row = %s, %v", raw, err)
	}
	got, err := e.svc.Get(ctx, v.ID)
	if err != nil || got.Config.Scope != ScopeAnnotated || got.Sources[0].Scope != ScopeAnnotated {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	e.coreBuildAll(t)
	if got, err = e.svc.Get(ctx, v.ID); err != nil || got.Status != StatusReady {
		t.Fatalf("built = %+v, %v", got, err)
	}
	// As before scopes: the image without an annotation is skipped.
	if got.Stats.Skipped.Unannotated != 1 || got.ImageCount != 8 {
		t.Errorf("legacy build = %+v", got)
	}
}
