package dataversion

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func coreSplitConfig(train, val, test int) SplitConfig {
	return SplitConfig{Train: train, Val: val, Test: test, Seed: 42, UseFolders: true, GroupBy: GroupByStem}
}

// coreManyItems is n images of one dataset, each in its own group.
func coreManyItems(n int) []splitItem {
	items := make([]splitItem, n)
	for i := range items {
		items[i] = splitItem{DatasetID: 1, RelPath: fmt.Sprintf("img/part%05d.png", i), Size: int64(1000 + i)}
	}
	return items
}

func TestBucketMath(t *testing.T) {
	for _, tt := range []struct {
		h    uint64
		want int
	}{
		{0, 0},
		{math.MaxUint64, 99},
		{1 << 63, 50},
		{(1 << 63) - 1, 49},
		// The first value of bucket 1: x*100 >= 2^53, x = ceil(2^53/100).
		{((1<<53 + 99) / 100) << 11, 1},
		{(((1<<53 + 99) / 100) - 1) << 11, 0},
		// The low 11 bits are ignored.
		{0x7ff, 0},
	} {
		if got := bucketOf(tt.h); got != tt.want {
			t.Errorf("bucketOf(%#x) = %d, want %d", tt.h, got, tt.want)
		}
	}
	cfg := coreSplitConfig(70, 20, 10)
	for b, want := range map[int]string{0: SplitVal, 19: SplitVal, 20: SplitTest, 29: SplitTest, 30: SplitTrain, 99: SplitTrain} {
		if got := splitOfBucket(cfg, b); got != want {
			t.Errorf("bucket %d = %s, want %s", b, got, want)
		}
	}
	if got := splitOfBucket(coreSplitConfig(80, 20, 0), 20); got != SplitTrain {
		t.Errorf("bucket 20 with test 0 = %s", got)
	}
	// Buckets are spread evenly.
	var hist [100]int
	for i := range 100000 {
		hist[splitBucket(7, fmt.Sprint(i))]++
	}
	for b, n := range hist {
		if n < 800 || n > 1200 {
			t.Errorf("bucket %d has %d of 100000 keys", b, n)
		}
	}
}

func TestSplitDeterministic(t *testing.T) {
	items := coreManyItems(500)
	cfg := coreSplitConfig(70, 20, 10)
	a := splitImages(items, cfg)
	// The same items in another order get the same splits.
	perm := rand.New(rand.NewPCG(1, 2)).Perm(len(items))
	shuffled := make([]splitItem, len(items))
	for i, p := range perm {
		shuffled[i] = items[p]
	}
	b := splitImages(shuffled, cfg)
	for i, p := range perm {
		if b[i] != a[p] {
			t.Fatalf("%s: %v, then %v", items[p].RelPath, a[p], b[i])
		}
	}
	c := splitCounts(a)
	if c.Train < 300 || c.Train > 400 || c.Val < 70 || c.Val > 130 || c.Test < 25 || c.Test > 75 {
		t.Errorf("counts = %+v", c)
	}
	// Another seed moves images.
	cfg.Seed = 43
	moved := 0
	for i, as := range splitImages(items, cfg) {
		if as != a[i] {
			moved++
		}
	}
	if moved < 100 {
		t.Errorf("another seed moved %d images", moved)
	}
}

func TestSplitNestedVal(t *testing.T) {
	items := coreManyItems(2000)
	base := splitImages(items, coreSplitConfig(80, 20, 0))
	same := splitImages(items, coreSplitConfig(70, 20, 10))
	more := splitImages(items, coreSplitConfig(60, 30, 10))
	for i := range items {
		// The same val ratio: the same val set, whatever train and test.
		if (base[i].Split == SplitVal) != (same[i].Split == SplitVal) {
			t.Fatalf("%s: %s at 80/20/0, %s at 70/20/10", items[i].RelPath, base[i].Split, same[i].Split)
		}
		// A larger val ratio contains the smaller one.
		if base[i].Split == SplitVal && more[i].Split != SplitVal {
			t.Fatalf("%s left val at 60/30/10", items[i].RelPath)
		}
		// Changing test never moves an image from val to train or back.
		if base[i].Split == SplitTrain && same[i].Split == SplitVal {
			t.Fatalf("%s moved from train into val", items[i].RelPath)
		}
	}
}

func TestFolderSplit(t *testing.T) {
	for rel, want := range map[string]string{
		"train/a.jpg":                       SplitTrain,
		"val/a.jpg":                         SplitVal,
		"Valid/a.jpg":                       SplitVal,
		"validation/x/a.jpg":                SplitVal,
		"test/a.jpg":                        SplitTest,
		"train2017/a.jpg":                   SplitTrain,
		"VAL2017/a.jpg":                     SplitVal,
		"images/train/a.jpg":                SplitTrain,
		"JPEGImages/val/a.jpg":              SplitVal,
		"batch1/images/val/a.jpg":           SplitVal,
		"train/images/val/a.jpg":            SplitVal, // the last images folder decides
		"images/val/images/train/a.jpg":     SplitTrain,
		"train/images/a.jpg":                SplitTrain, // Roboflow layout: the first folder
		"images/a.jpg":                      "",
		"a.jpg":                             "",
		"train.jpg":                         "",
		"trainer/a.jpg":                     "",
		"OK/train/a.jpg":                    "",
		"images/training/a.jpg":             "",
		"images/test1/a.jpg":                SplitTest,
		"JPEGImages/train/images/val/a.jpg": SplitVal,
	} {
		if got := folderSplit(rel); got != want {
			t.Errorf("folderSplit(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestSplitFolders(t *testing.T) {
	items := []splitItem{
		// Source 0: train and val folders.
		{Source: 0, DatasetID: 1, RelPath: "images/train/a.jpg", Size: 1},
		{Source: 0, DatasetID: 1, RelPath: "images/val/b.jpg", Size: 2},
		{Source: 0, DatasetID: 1, RelPath: "images/test/c.jpg", Size: 3},
		{Source: 0, DatasetID: 1, RelPath: "extra/d.jpg", Size: 4},
		// Source 1: only a train folder: everything on ratio.
		{Source: 1, DatasetID: 2, RelPath: "train/e.jpg", Size: 5},
		{Source: 1, DatasetID: 2, RelPath: "f.jpg", Size: 6},
		// Source 2: COCO-style names.
		{Source: 2, DatasetID: 3, RelPath: "train2017/g.jpg", Size: 7},
		{Source: 2, DatasetID: 3, RelPath: "val2017/h.jpg", Size: 8},
	}
	byFolder := func(as []splitAssignment) map[string]string {
		out := map[string]string{}
		for i, a := range as {
			if a.By == SplitByFolder {
				out[items[i].RelPath] = a.Split
			}
		}
		return out
	}
	// test 0: the test folder goes to the ratio split.
	got := byFolder(splitImages(items, coreSplitConfig(80, 20, 0)))
	want := map[string]string{"images/train/a.jpg": SplitTrain, "images/val/b.jpg": SplitVal,
		"train2017/g.jpg": SplitTrain, "val2017/h.jpg": SplitVal}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("folders with test 0 = %v, want %v", got, want)
	}
	got = byFolder(splitImages(items, coreSplitConfig(70, 20, 10)))
	want["images/test/c.jpg"] = SplitTest
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("folders with test 10 = %v, want %v", got, want)
	}
	cfg := coreSplitConfig(70, 20, 10)
	cfg.UseFolders = false
	if got := byFolder(splitImages(items, cfg)); len(got) != 0 {
		t.Errorf("folders without use_folders = %v", got)
	}
	// A source with a val folder but no train folder stays on ratio.
	only := []splitItem{{Source: 0, DatasetID: 1, RelPath: "val/a.jpg"}, {Source: 0, DatasetID: 1, RelPath: "b.jpg"}}
	for _, a := range splitImages(only, coreSplitConfig(80, 20, 0)) {
		if a.By != SplitByRatio {
			t.Errorf("val folder alone: %+v", a)
		}
	}
}

func TestNormalizeStem(t *testing.T) {
	for stem, want := range map[string]string{
		"a":                 "a",
		"A":                 "a",
		"a - 副本":            "a",
		"a - 副本 (2)":        "a",
		"a - Copy":          "a",
		"a - copy (3)":      "a",
		"a (2)":             "a",
		"a_aug3":            "a",
		"a-flip":            "a",
		"a_v2":              "a",
		"a_aug1_v2 (1)":     "a",
		"part 12_rot90":     "part_12",
		"划痕001":             "划痕001",
		"脏污001":             "脏污001",
		"划痕 001 - 副本":       "划痕_001",
		"(2)":               "(2)",
		"copy":              "copy",
		"__x__":             "x",
		"IMG_0001.JPG.copy": "img_0001_jpg_copy",
		"sn123_cam1":        "sn123_cam1",
	} {
		if got := normalizeStem(stem); got != want {
			t.Errorf("normalizeStem(%q) = %q, want %q", stem, got, want)
		}
	}
}

func TestSplitLeakageGroups(t *testing.T) {
	// Copies of one image share its split, whatever the seed.
	copies := []string{"NG/a.jpg", "NG/a - 副本.jpg", "NG/a (2).jpg", "NG/a_aug3.png", "NG/A - Copy.jpg"}
	var items []splitItem
	for i, rel := range copies {
		items = append(items, splitItem{DatasetID: 1, RelPath: rel, Size: int64(100 + i)})
	}
	// The same file in another dataset and folder (name and size).
	items = append(items, splitItem{Source: 1, DatasetID: 2, RelPath: "x/y/a.jpg", Size: 100})
	// Different names in Chinese with the same number are not merged.
	items = append(items, splitItem{DatasetID: 1, RelPath: "NG/划痕001.jpg", Size: 7},
		splitItem{DatasetID: 1, RelPath: "NG/脏污001.jpg", Size: 7})
	items = append(items, coreManyItems(200)...)
	separated := 0
	for seed := range int64(40) {
		cfg := coreSplitConfig(50, 40, 10)
		cfg.Seed = seed
		as := splitImages(items, cfg)
		for i := 1; i < 6; i++ {
			if as[i] != as[0] {
				t.Fatalf("seed %d: %s is %v, %s is %v", seed, items[i].RelPath, as[i], items[0].RelPath, as[0])
			}
		}
		if as[6].Split != as[7].Split {
			separated++
		}
	}
	if separated == 0 {
		t.Error("划痕001 and 脏污001 always share a split")
	}

	// Same name, different size in another dataset: independent.
	differ := 0
	for seed := range int64(40) {
		cfg := coreSplitConfig(50, 50, 0)
		cfg.Seed = seed
		as := splitImages([]splitItem{{DatasetID: 1, RelPath: "a.jpg", Size: 1}, {DatasetID: 2, RelPath: "a.jpg", Size: 2},
			{DatasetID: 3, RelPath: "b.jpg", Size: 3}, {DatasetID: 4, RelPath: "c.jpg", Size: 4}}, cfg)
		if as[0] != as[1] {
			differ++
		}
	}
	if differ == 0 {
		t.Error("files with the same name and different sizes always share a split")
	}
}

func TestSplitUncompressedSameSize(t *testing.T) {
	// BMP files of one camera all have the same size: Image_0001.bmp of
	// every lot folder is not one image.
	var items []splitItem
	for lot := range 50 {
		for n := range 20 {
			items = append(items, splitItem{DatasetID: 1, RelPath: fmt.Sprintf("lot%02d/Image_%04d.bmp", lot, n+1),
				Size: 921654})
		}
	}
	as := splitImages(items, coreSplitConfig(80, 20, 0))
	counts := map[string]int{}
	for _, a := range as {
		counts[a.Split]++
	}
	if counts[SplitVal] < 100 || counts[SplitVal] > 300 {
		t.Errorf("val = %d of %d, want about 20 %%", counts[SplitVal], len(items))
	}
	// The same JPEG name and size in two lots is still one group.
	jpg := []splitItem{{DatasetID: 1, RelPath: "lot01/a.jpg", Size: 5000}, {DatasetID: 1, RelPath: "lot02/a.jpg", Size: 5000}}
	for seed := range int64(20) {
		cfg := coreSplitConfig(50, 50, 0)
		cfg.Seed = seed
		if as := splitImages(append(jpg, coreManyItems(20)...), cfg); as[0] != as[1] {
			t.Fatalf("seed %d: %v %v", seed, as[0], as[1])
		}
	}
}

func TestSplitGroupByFolder(t *testing.T) {
	var items []splitItem
	for part := range 50 {
		for cam := range 3 {
			items = append(items, splitItem{DatasetID: 1, RelPath: fmt.Sprintf("SN%03d/cam%d.png", part, cam), Size: int64(part*10 + cam)})
		}
	}
	cfg := coreSplitConfig(80, 20, 0)
	cfg.GroupBy = GroupByFolder
	as := splitImages(items, cfg)
	splits := map[string]bool{}
	for i := 0; i < len(items); i += 3 {
		if as[i] != as[i+1] || as[i] != as[i+2] {
			t.Errorf("%s: %v %v %v", items[i].RelPath, as[i], as[i+1], as[i+2])
		}
		splits[as[i].Split] = true
	}
	if !splits[SplitTrain] || !splits[SplitVal] {
		t.Errorf("splits used = %v", splits)
	}
	// By stem, the cameras of one part are split independently.
	cfg.GroupBy = GroupByStem
	as = splitImages(items, cfg)
	mixed := false
	for i := 0; i < len(items); i += 3 {
		mixed = mixed || as[i] != as[i+1] || as[i] != as[i+2]
	}
	if !mixed {
		t.Error("group_by stem kept every part together")
	}
}

func TestSplitFixUp(t *testing.T) {
	// Two groups with 1 % val: almost always both hash into train, then
	// the one closest to val's interval moves there.
	items := []splitItem{{DatasetID: 1, RelPath: "a.png", Size: 1}, {DatasetID: 1, RelPath: "b.png", Size: 2}}
	cfg := coreSplitConfig(99, 1, 0)
	for seed := range int64(20) {
		cfg.Seed = seed
		as := splitImages(items, cfg)
		if c := splitCounts(as); c.Train != 1 || c.Val != 1 {
			t.Fatalf("seed %d: %+v", seed, c)
		}
		// The moved group is the one with the smaller bucket (ties: key).
		ba, bb := splitBucket(seed, "1/./a"), splitBucket(seed, "1/./b")
		wantVal := 0
		if bb < ba {
			wantVal = 1
		}
		if ba >= 1 && bb >= 1 && as[wantVal].Split != SplitVal {
			t.Errorf("seed %d: buckets %d %d, splits %v", seed, ba, bb, as)
		}
	}
	// Three groups, 34/33/33: each split gets one.
	items = append(items, splitItem{DatasetID: 1, RelPath: "c.png", Size: 3})
	cfg = coreSplitConfig(34, 33, 33)
	for seed := range int64(20) {
		cfg.Seed = seed
		if c := splitCounts(splitImages(items, cfg)); c.Train != 1 || c.Val != 1 || c.Test != 1 {
			t.Fatalf("seed %d: %+v", seed, c)
		}
	}
	// A single group cannot be split: no val.
	one := splitImages(items[:1], coreSplitConfig(99, 1, 0))
	if err := splitRefusal(splitCounts(one)); !errors.Is(err, errNoValImages) {
		t.Errorf("one image: %+v, %v", one, err)
	}
	one = splitImages(items[:1], coreSplitConfig(1, 99, 0))
	if err := splitRefusal(splitCounts(one)); !errors.Is(err, errNoTrainImages) {
		t.Errorf("one image at 1/99: %+v, %v", one, err)
	}
	if err := splitRefusal(SplitCounts{}); !errors.Is(err, errNoImages) {
		t.Errorf("no images: %v", err)
	}
	// Copies of one image are one group: still no val.
	copies := []splitItem{{DatasetID: 1, RelPath: "a.png", Size: 1}, {DatasetID: 1, RelPath: "a (2).png", Size: 1}}
	if err := splitRefusal(splitCounts(splitImages(copies, coreSplitConfig(80, 20, 0)))); !errors.Is(err, errNoValImages) {
		t.Errorf("copies: %v", err)
	}
}
