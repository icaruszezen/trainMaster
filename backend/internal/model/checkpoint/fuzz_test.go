package checkpoint

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// fixturePickle returns the data.pkl of a fixture.
func fixturePickle(tb testing.TB, data []byte) []byte {
	tb.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		tb.Fatal(err)
	}
	f := findPickle(zr.File)
	if f == nil {
		tb.Fatal("no data.pkl")
	}
	rc, err := f.Open()
	if err != nil {
		tb.Fatal(err)
	}
	defer rc.Close()
	pkl, err := io.ReadAll(rc)
	if err != nil {
		tb.Fatal(err)
	}
	return pkl
}

// fuzzSeeds are small hostile pickles (the large ones are in
// TestUnpickleCraftedDefaults).
var fuzzSeeds = []string{
	strings.Repeat("(", 1000) + ".",
	strings.Repeat("]", 1000) + ".",
	")" + strings.Repeat("\x85", 1000) + ".",
	"]q\x00h\x00a.",                    // a list containing itself
	"}q\x00(X\x01\x00\x00\x00ah\x00u.", // a dict containing itself
	"cultralytics.nn.tasks\nDetectionModel\n)\x81q\x00}q\x01X\x04\x00\x00\x00selfq\x02h\x00sb.", // an object containing itself
	"\x8b\xff\xff\xff\x7f.",
	"X\xff\xff\xff\xffabc.",
	"\x8d\xff\xff\xff\xff\xff\xff\xff\xff.",
	"N" + strings.Repeat("r\x00\x00\x00\x00", 100) + strings.Repeat("h\x00", 100) + ".",
	"\x80\x02}(X\x05\x00\x00\x00modelcultralytics.nn.tasks\nDetectionModel\n)\x81}X\x05\x00\x00\x00names}K\x00X\x01\x00\x00\x00as\x85bu.",
}

func FuzzInspect(f *testing.F) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.pt"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("no fixtures: %v", err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		if bytes.HasPrefix(data, magicZip) {
			if pkl := fixturePickle(f, data); len(pkl) < 64<<10 {
				f.Add(pkl) // the target wraps it in a torch zip
			}
		}
	}
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkInspect(t, data)
		if !bytes.HasPrefix(data, []byte("PK")) {
			checkInspect(t, torchZip("fuzz", data))
		}
	})
}

// checkInspect runs inspect (without the recover of Inspect, so that a
// panic fails) and checks what it returns.
func checkInspect(t *testing.T, data []byte) {
	info, err := inspect(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("error %T %v is not a *Error", err, err)
		}
		switch e.Reason {
		case ReasonNotTorch, ReasonLegacyFormat, ReasonCorrupt, ReasonNotUltralytics,
			ReasonYOLOv5Repo, ReasonUnsupportedModel, ReasonTooLarge:
		default:
			t.Fatalf("unknown reason %q", e.Reason)
		}
		if e.Message == "" || !utf8.ValidString(e.Message) || strings.IndexFunc(e.Message, unicode.IsControl) >= 0 {
			t.Fatalf("bad message %q", e.Message)
		}
		if info != nil {
			t.Fatal("info and error")
		}
		return
	}
	if info == nil {
		t.Fatal("no info and no error")
	}
	checkInfo(t, info)
}

func checkInfo(t *testing.T, info *Info) {
	text := func(field, s string, maxLen int) {
		if len(s) > maxLen || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			t.Fatalf("%s = %q", field, s)
		}
	}
	text("ModelClass", info.ModelClass, maxShortTextLen)
	text("Task", info.Task, maxShortTextLen)
	text("YOLOVersion", info.YOLOVersion, maxShortTextLen)
	text("YAMLFile", info.YAMLFile, maxTextLen)
	text("Scale", info.Scale, maxShortTextLen)
	text("Head", info.Head, maxShortTextLen)
	text("UltralyticsVersion", info.UltralyticsVersion, maxShortTextLen)
	text("Date", info.Date, maxShortTextLen)
	text("TrainData", info.TrainData, maxTextLen)
	text("BaseModel", info.BaseModel, maxTextLen)
	if info.ClassNames == nil || len(info.ClassNames) > maxClasses {
		t.Fatalf("%d class names", len(info.ClassNames))
	}
	for _, n := range info.ClassNames {
		text("class name", n, maxClassNameLen)
	}
	if info.Metrics == nil || len(info.Metrics) > maxMetrics {
		t.Fatalf("%d metrics", len(info.Metrics))
	}
	for k, v := range info.Metrics {
		text("metric", k, maxShortTextLen)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("metric %q = %v", k, v)
		}
	}
	if b := info.BestFitness; b != nil && (math.IsNaN(*b) || math.IsInf(*b, 0)) {
		t.Fatalf("BestFitness = %v", *b)
	}
	switch info.VersionSource {
	case "":
		if info.YOLOVersion != "" {
			t.Fatalf("version %q without a source", info.YOLOVersion)
		}
	case VersionSourceYAMLFile, VersionSourceArchitecture:
		if _, ok := versionByNumber[strings.TrimLeft(info.YOLOVersion, "yolov")]; !ok {
			t.Fatalf("YOLOVersion = %q", info.YOLOVersion)
		}
	default:
		t.Fatalf("VersionSource = %q", info.VersionSource)
	}
	if info.Channels <= 0 || info.Parameters < 0 || info.ImageSize < 0 || info.Epochs < 0 ||
		(info.Epoch != nil && *info.Epoch < -1) {
		t.Fatalf("Info = %+v", *info)
	}
	// The service stores Info as JSON (jsonb rejects \u0000).
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`\u0000`)) {
		t.Fatalf("JSON with NUL: %s", b)
	}
}

// TestInfoSanity applies the checks of FuzzInspect to the fixtures.
func TestInfoSanity(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("testdata", "*.pt"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		checkInspect(t, data)
	}
	for _, s := range fuzzSeeds {
		checkInspect(t, []byte(s))
		checkInspect(t, torchZip("seed", []byte(s)))
	}
}

func BenchmarkInspect(b *testing.B) {
	for _, name := range []string{"yolo11n.pt", "yolo11n-last.pt", "yolov8n.pt"} {
		data := readFixture(b, name)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := Inspect(bytes.NewReader(data), int64(len(data))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
