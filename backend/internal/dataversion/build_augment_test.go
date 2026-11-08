package dataversion

// Builds with the real augment package: augmented copies and the EXIF
// orientation fix.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/augment"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// coreAugmentFiles is a dataset split by its train and val folders:
// train/p00 and train/p00-aug1 (a name an augmented copy of p00 would
// take), train/p01 (a JPEG), a negative and val/v00.
func coreAugmentFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	coreLabeled(t, files, "train/p00.png", 1, "scratch")
	coreLabeled(t, files, "train/p00-aug1.png", 2, "dent")
	files["train/p01.jpg"] = coreJPEG(t, 32, 24, nil)
	files["train/p01.xml"] = coreVOC("p01.jpg", 32, 24, coreBox{"scratch", 2, 2, 12, 12})
	coreLabeled(t, files, "train/neg.png", 3)
	coreLabeled(t, files, "val/v00.png", 4, "scratch")
	return files
}

// coreJPEG is a JPEG image; exif, when set, is the payload of an APP1
// segment written right after SOI.
func coreJPEG(t *testing.T, w, h int, exif []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 10), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	if exif == nil {
		return data
	}
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(2+len(exif)))
	return slices.Concat(data[:2], seg, exif, data[2:])
}

// coreExifOrientation is an Exif payload with one IFD0 entry: the
// orientation tag (little-endian TIFF).
func coreExifOrientation(value uint16) []byte {
	b := []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00")
	b = binary.LittleEndian.AppendUint16(b, 1)      // entries
	b = binary.LittleEndian.AppendUint16(b, 0x0112) // orientation
	b = binary.LittleEndian.AppendUint16(b, 3)      // SHORT
	b = binary.LittleEndian.AppendUint32(b, 1)      // count
	b = binary.LittleEndian.AppendUint16(b, value)
	b = binary.LittleEndian.AppendUint16(b, 0)
	return binary.LittleEndian.AppendUint32(b, 0) // no next IFD
}

// corePNGHeader is a PNG file with only a header (w x h, 8-bit RGBA) and
// no image data: its size can be read, it cannot be decoded.
func corePNGHeader(w, h int) []byte {
	chunk := func(typ string, data []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		out = append(out, typ...)
		out = append(out, data...)
		return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	return slices.Concat([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", ihdr), chunk("IEND", nil))
}

func TestCoreBuildAugment(t *testing.T) {
	e := coreNewEnv(t)
	ctx := context.Background()
	d := e.coreDataset(t, "ds", coreAugmentFiles(t))
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	in.Augmentation = &Augmentation{Methods: []string{"vflip", "hflip"}, Copies: 2, OnlyWithObjects: true}
	pre, err := e.svc.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.augmentWorkers = 3
	v := e.coreCreateBuilt(t, in)
	// Train originals with objects: p00, p00-aug1, p01; two copies each.
	if pre.AugmentedCount != 6 || v.AugmentedCount != 6 || v.ImageCount != 11 || v.TrainCount != 10 || v.ValCount != 1 ||
		v.Stats.Storage.Generated != 6 || v.Stats.Storage.GeneratedBytes <= 0 || v.Stats.Skipped.AugmentSkipped != 0 ||
		v.ObjectCount != 4+6 || v.Stats.Labels[0].Train.Objects != 2 || v.Stats.Labels[1].Train.Objects != 1 { // labels: originals only
		t.Errorf("version = %+v", v)
	}
	rows := e.coreRows(t, v.ID)
	byPath := map[string]coreImageRow{}
	byID := map[int64]coreImageRow{}
	for _, r := range rows {
		byPath[r.Path] = r
		byID[r.ID] = r
	}
	dir := e.coreDir(t, v)
	wantCopies := map[string][]string{
		"images/ds/train/p00.png":      {"images/ds/train/p00-aug1~2.png", "images/ds/train/p00-aug2.png"},
		"images/ds/train/p00-aug1.png": {"images/ds/train/p00-aug1-aug1.png", "images/ds/train/p00-aug1-aug2.png"},
		"images/ds/train/p01.jpg":      {"images/ds/train/p01-aug1.jpg", "images/ds/train/p01-aug2.jpg"},
	}
	var trainList []string
	for _, r := range rows {
		if r.Split == SplitTrain {
			trainList = append(trainList, "./"+r.Path)
		}
	}
	if got := strings.TrimSpace(coreReadFile(t, dir, "train.txt")); got != strings.Join(trainList, "\n") {
		t.Errorf("train.txt = %q, want %q", got, trainList)
	}
	for orig, copies := range wantCopies {
		o, ok := byPath[orig]
		if !ok {
			t.Fatalf("no original %s", orig)
		}
		var methods []string
		for k, c := range copies {
			r, ok := byPath[c]
			if !ok {
				t.Errorf("no copy %s", c)
				continue
			}
			if r.Split != SplitTrain || r.AugmentedFrom == nil || *r.AugmentedFrom != o.ID || r.Stored != StoredGenerated ||
				r.Position != o.Position+k+1 || r.SourcePath != o.SourcePath || r.Width != 32 || r.Height != 24 ||
				len(r.Augment) == 0 || r.ObjectCount != o.ObjectCount {
				t.Errorf("copy %s = %+v (original %+v)", c, r, o)
			}
			methods = append(methods, strings.Join(r.Augment, "+"))
			// The boxes follow the flips.
			want := o.Objects[0]
			if slices.Contains(r.Augment, "hflip") {
				want.Xmin, want.Xmax = 32-want.Xmax, 32-want.Xmin
			}
			if slices.Contains(r.Augment, "vflip") {
				want.Ymin, want.Ymax = 24-want.Ymax, 24-want.Ymin
			}
			if r.Objects[0] != want {
				t.Errorf("%s (%v): box %+v, want %+v", c, r.Augment, r.Objects[0], want)
			}
			label := coreReadFile(t, dir, dataset.UltralyticsLabelPath(r.Path))
			if strings.Count(label, "\n") != 1 {
				t.Errorf("label of %s = %q", c, label)
			}
			f, err := os.Open(filepath.Join(dir, filepath.FromSlash(r.Path)))
			if err != nil {
				t.Fatal(err)
			}
			cfg, format, err := image.DecodeConfig(f)
			f.Close()
			if wantFormat := map[string]string{".png": "png", ".jpg": "jpeg"}[filepath.Ext(c)]; err != nil ||
				cfg.Width != 32 || cfg.Height != 24 || format != wantFormat {
				t.Errorf("%s: %+v %s %v", c, cfg, format, err)
			}
		}
		if len(methods) == 2 && methods[0] == methods[1] {
			t.Errorf("copies of %s are alike: %v", orig, methods)
		}
	}
	// No copies of the negative (only_with_objects) nor of val.
	for _, r := range rows {
		if r.AugmentedFrom != nil && (byID[*r.AugmentedFrom].ObjectCount == 0 || byID[*r.AugmentedFrom].Split != SplitTrain) {
			t.Errorf("copy %s of %+v", r.Path, byID[*r.AugmentedFrom])
		}
	}

	// The same input gives the same copies, with any number of workers;
	// with only_with_objects off the negative gets copies too.
	e.svc.augmentWorkers = 1
	v2 := e.coreCreateBuilt(t, in)
	rows2 := e.coreRows(t, v2.ID)
	if len(rows2) != len(rows) {
		t.Fatalf("%d rows, then %d", len(rows), len(rows2))
	}
	for i := range rows {
		a, b := rows[i], rows2[i]
		if a.Path != b.Path || a.Position != b.Position || fmt.Sprint(a.Augment) != fmt.Sprint(b.Augment) ||
			a.SizeBytes != b.SizeBytes || fmt.Sprint(a.Objects) != fmt.Sprint(b.Objects) {
			t.Errorf("row %d: %+v, then %+v", i, a, b)
		}
	}
	in.Augmentation.OnlyWithObjects = false
	in.Augmentation.Copies = 1
	v3 := e.coreCreateBuilt(t, in)
	if v3.AugmentedCount != 4 {
		t.Errorf("augmented = %d", v3.AugmentedCount)
	}
	for _, r := range e.coreRows(t, v3.ID) {
		if r.Path == "images/ds/train/neg-aug1.png" && (r.ObjectCount != 0 ||
			coreReadFile(t, e.coreDir(t, v3), "labels/ds/train/neg-aug1.txt") != "") {
			t.Errorf("negative copy = %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(e.coreDir(t, v3), "labels/ds/train/neg-aug1.txt")); err != nil {
		t.Errorf("label of the negative copy: %v", err)
	}
}

func TestCoreBuildAugmentSkips(t *testing.T) {
	e := coreNewEnv(t)
	files := coreAugmentFiles(t)
	// Not decodable; too large to augment (its header says 20000 x 20000);
	// a box too small to blur.
	files["train/broken.png"] = corePNGHeader(32, 24)
	files["train/broken.xml"] = coreVOC("broken.png", 32, 24, coreBox{"scratch", 2, 2, 12, 12})
	files["train/huge.png"] = corePNGHeader(20000, 20000)
	files["train/huge.xml"] = coreVOC("huge.png", 20000, 20000, coreBox{"scratch", 2, 2, 12, 12})
	files["train/tiny.png"] = corePNG(t, 32, 24, 9)
	files["train/tiny.xml"] = coreVOC("tiny.png", 32, 24, coreBox{"scratch", 2, 2, 6, 6})
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	in.Augmentation = &Augmentation{Methods: []string{"blur"}, Copies: 2, OnlyWithObjects: true}
	v := e.coreCreateBuilt(t, in)
	// Copies of p00, p00-aug1 and p01 only.
	if v.Stats.Skipped.AugmentSkipped != 3 || v.AugmentedCount != 6 || v.Stats.Storage.Generated != 6 {
		t.Errorf("version = %+v", v)
	}
	for _, r := range e.coreRows(t, v.ID) {
		if r.AugmentedFrom != nil && fmt.Sprint(r.Augment) != "[blur]" {
			t.Errorf("copy %s: %v", r.Path, r.Augment)
		}
		for _, stem := range []string{"broken-aug", "huge-aug", "tiny-aug"} {
			if strings.Contains(r.Path, stem) {
				t.Errorf("copy %s", r.Path)
			}
		}
	}
}

func TestCoreBuildOrientationFix(t *testing.T) {
	e := coreNewEnv(t)
	files := coreAugmentFiles(t)
	files["train/rot.jpg"] = coreJPEG(t, 32, 24, coreExifOrientation(6))
	files["train/rot.xml"] = coreVOC("rot.jpg", 32, 24, coreBox{"dent", 2, 2, 12, 12})
	files["train/up.jpg"] = coreJPEG(t, 32, 24, coreExifOrientation(1))
	files["train/up.xml"] = coreVOC("up.jpg", 32, 24, coreBox{"dent", 2, 2, 12, 12})
	d := e.coreDataset(t, "ds", files)
	p := e.coreProject(t, "p", []string{"scratch", "dent"}, d.ID)
	in := coreInput(p.ID, e.coreClassVersion(t, p.ID), coreBase(d.ID)...)
	in.Augmentation = &Augmentation{Methods: []string{"hflip"}, Copies: 1, OnlyWithObjects: true}
	v := e.coreCreateBuilt(t, in)
	if v.Stats.Storage.OrientationFixed != 1 || v.Stats.Storage.Copied != 1 || v.Stats.Storage.Linked != 6 ||
		v.Stats.Storage.CopiedBytes != int64(len(files["train/rot.jpg"])) {
		t.Errorf("storage = %+v", v.Stats.Storage)
	}
	dir := e.coreDir(t, v)
	for _, r := range e.coreRows(t, v.ID) {
		switch r.Path {
		case "images/ds/train/rot.jpg":
			if r.Stored != StoredCopy || r.Width != 32 || r.Height != 24 || r.SizeBytes != int64(len(files["train/rot.jpg"])) {
				t.Errorf("rot = %+v", r)
			}
			f, err := os.Open(filepath.Join(dir, "images/ds/train/rot.jpg"))
			if err != nil {
				t.Fatal(err)
			}
			o, err := augment.FindOrientation(f, r.SizeBytes)
			f.Close()
			if err != nil || o.Value != 1 {
				t.Errorf("stored orientation = %+v, %v", o, err)
			}
			// The dataset's file is untouched.
			src, _ := os.ReadFile(filepath.Join(d.Path, "train/rot.jpg"))
			if !bytes.Equal(src, files["train/rot.jpg"]) {
				t.Error("the dataset's file changed")
			}
		case "images/ds/train/rot-aug1.jpg":
			if r.Stored != StoredGenerated || r.Width != 32 || r.Height != 24 || r.Objects[0].Xmin != 20 {
				t.Errorf("rot copy = %+v", r)
			}
		case "images/ds/train/up.jpg":
			if r.Stored != StoredLink {
				t.Errorf("up = %+v", r)
			}
		}
	}
}
