package checkpoint

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func inspectBytes(data []byte) (*Info, error) {
	return Inspect(bytes.NewReader(data), int64(len(data)))
}

// torchZip builds a file like torch.save writes: data.pkl and version in
// a folder named root (stored, like torch does).
func torchZip(root string, pickle []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct {
		name string
		data []byte
	}{{root + "/data.pkl", pickle}, {root + "/version", []byte("3\n")}} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		if err != nil {
			panic(err)
		}
		_, _ = w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// numbered returns the class names of a model built from a yaml without
// a dataset: "0", "1", ...
func numbered(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = strconv.Itoa(i)
	}
	return names
}

func intp(n int) *int               { return &n }
func floatp(f float64) *float64     { return &f }
func noMetrics() map[string]float64 { return map[string]float64{} }

// trainedNames are the classes of the tiny dataset the trained fixtures
// were trained on.
var trainedNames = []string{"scratch", "划痕-凹坑"}

// untrained is the Info of a model built from an Ultralytics yaml and
// saved without training (testdata/README.md).
func untrained(class, task, version, yaml, head, date string, params int64) Info {
	return Info{
		ModelClass: class, Task: task, YOLOVersion: version, VersionSource: VersionSourceYAMLFile, YAMLFile: yaml,
		Scale: "n", ClassNames: numbered(80), ImageSize: 640, Channels: 3, Parameters: params, Head: head,
		UltralyticsVersion: "8.4.198", Date: date, Metrics: noMetrics(),
	}
}

var yolo11Metrics = map[string]float64{
	"metrics/precision(B)": 0, "metrics/recall(B)": 0, "metrics/mAP50(B)": 0, "metrics/mAP50-95(B)": 0,
	"val/box_loss": 2.43489, "val/cls_loss": 3.74407, "val/dfl_loss": 1.42897, "fitness": 0,
}

func yolo11Best() Info {
	return Info{
		ModelClass: "DetectionModel", Task: "detect", YOLOVersion: "yolo11", VersionSource: VersionSourceYAMLFile,
		YAMLFile: "yolo11n.yaml", Scale: "n", ClassNames: trainedNames, ImageSize: 96, Channels: 3, Parameters: 2590230,
		Head: "Detect", UltralyticsVersion: "8.4.198", Date: "2026-11-03T01:50:30.249939+00:00", Epochs: 2, Epoch: intp(-1),
		Metrics: yolo11Metrics, TrainData: "/var/tmp/work-checkpoint/gen/tinyds/data.yaml", BaseModel: "yolo11n.yaml",
	}
}

func yolo26Best() Info {
	return Info{
		ModelClass: "DetectionModel", Task: "detect", YOLOVersion: "yolo26", VersionSource: VersionSourceYAMLFile,
		YAMLFile: "yolo26n.yaml", Scale: "n", ClassNames: trainedNames, ImageSize: 96, Channels: 3, Parameters: 2504580,
		End2End: true, Head: "Detect", UltralyticsVersion: "8.4.198", Date: "2026-11-03T01:50:32.672166+00:00", Epochs: 2,
		Epoch: intp(-1), TrainData: "/var/tmp/work-checkpoint/gen/tinyds/data.yaml", BaseModel: "yolo26n.yaml",
		Metrics: map[string]float64{
			"metrics/precision(B)": 0, "metrics/recall(B)": 0, "metrics/mAP50(B)": 0, "metrics/mAP50-95(B)": 0,
			"val/box_loss": 3.58054, "val/cls_loss": 4.55223, "val/l1_loss": 0.05179, "fitness": 0,
		},
	}
}

// TestInspectFixtures checks every field against what PyTorch and
// Ultralytics report for the original files (parameters: sum(p.numel()
// for p in model.parameters())).
func TestInspectFixtures(t *testing.T) {
	last := yolo11Best()
	last.Date, last.Epoch, last.BestFitness, last.TrainingCheckpoint = "2026-11-03T01:50:29.482917+00:00", intp(0), floatp(0), true
	variants := yolo11Best()
	variants.ImageSize, variants.Epoch, variants.BestFitness = 128, intp(41), floatp(0.375)
	variants.Metrics = map[string]float64{"metrics/mAP50(B)": 0.91, "metrics/mAP50-95(B)": 0.625, "val/box_loss": 1}
	variants.TrainData = "/data/exports/line-a/data.yaml"
	cls := untrained("ClassificationModel", "classify", "yolo11", "yolo11n-cls.yaml", "Classify", "2026-11-03T01:50:23.971515+00:00", 2812104)
	cls.ClassNames = numbered(1000)
	v9 := untrained("DetectionModel", "detect", "yolov9", "yolov9t.yaml", "Detect", "2026-11-03T01:50:22.989212+00:00", 2128720)
	v9.Scale = "t" // yolov9t.yaml has no scales: from the file name
	custom26 := untrained("DetectionModel", "detect", "yolo26", "line-a-detector.yaml", "Detect", "2026-11-03T01:50:25.547922+00:00", 2572280)
	custom26.VersionSource, custom26.End2End = VersionSourceArchitecture, true // scale: the first in the yaml, as Ultralytics builds it
	customV8 := untrained("DetectionModel", "detect", "yolov8", "my-model.yaml", "Detect", "2026-11-03T01:50:25.710053+00:00", 3157200)
	customV8.VersionSource = VersionSourceArchitecture
	v10 := untrained("DetectionModel", "detect", "yolov10", "yolov10n.yaml", "v10Detect", "2026-11-03T01:50:23.201358+00:00", 2775520)
	v10.End2End = true
	y26 := untrained("DetectionModel", "detect", "yolo26", "yolo26n.yaml", "Detect", "2026-11-03T01:50:23.684984+00:00", 2572280)
	y26.End2End = true
	y26seg := untrained("SegmentationModel", "segment", "yolo26", "yolo26n-seg.yaml", "Segment26", "2026-11-03T01:50:24.459063+00:00", 3126280)
	y26seg.End2End = true

	tests := map[string]Info{
		"yolov8n.pt":          untrained("DetectionModel", "detect", "yolov8", "yolov8n.yaml", "Detect", "2026-11-03T01:50:22.743539+00:00", 3157200),
		"yolov9t.pt":          v9,
		"yolov10n.pt":         v10,
		"yolo11n.pt":          untrained("DetectionModel", "detect", "yolo11", "yolo11n.yaml", "Detect", "2026-11-03T01:50:23.348514+00:00", 2624080),
		"yolo12n.pt":          untrained("DetectionModel", "detect", "yolo12", "yolo12n.yaml", "Detect", "2026-11-03T01:50:23.515412+00:00", 2602288),
		"yolo26n.pt":          y26,
		"yolo11n-seg.pt":      untrained("SegmentationModel", "segment", "yolo11", "yolo11n-seg.yaml", "Segment", "2026-11-03T01:50:23.845480+00:00", 2876848),
		"yolo11n-cls.pt":      cls,
		"yolo11n-pose.pt":     untrained("PoseModel", "pose", "yolo11", "yolo11n-pose.yaml", "Pose", "2026-11-03T01:50:24.099521+00:00", 2908507),
		"yolo11n-obb.pt":      untrained("OBBModel", "obb", "yolo11", "yolo11n-obb.yaml", "OBB", "2026-11-03T01:50:24.240999+00:00", 2695747),
		"yolo26n-seg.pt":      y26seg,
		"yoloe-11n-seg.pt":    untrained("YOLOESegModel", "segment", "yolo11", "yoloe-11n-seg.yaml", "YOLOESegment", "2026-11-03T01:50:24.781234+00:00", 5258054),
		"yolov8n-worldv2.pt":  untrained("WorldModel", "detect", "yolov8", "yolov8n-worldv2.yaml", "WorldDetect", "2026-11-03T01:50:24.923449+00:00", 3695183),
		"line-a-detector.pt":  custom26,
		"my-model.pt":         customV8,
		"yolo11n-best.pt":     yolo11Best(),
		"yolo11n-last.pt":     last,
		"yolo11n-variants.pt": variants,
		"yolo26n-best.pt":     yolo26Best(),
		// The same checkpoint pickled with the other protocols (protocol 0
		// cannot even be loaded by torch: its persistent ids are strings).
		"yolo26n-best-protocol0.pt": yolo26Best(),
		"yolo26n-best-protocol1.pt": yolo26Best(),
		"yolo26n-best-protocol4.pt": yolo26Best(),
		"yolo26n-best-protocol5.pt": yolo26Best(),
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := inspectBytes(readFixture(t, name))
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if !reflect.DeepEqual(*got, want) {
				g, w := reflect.ValueOf(*got), reflect.ValueOf(want)
				for i := 0; i < g.NumField(); i++ {
					if !reflect.DeepEqual(g.Field(i).Interface(), w.Field(i).Interface()) {
						t.Errorf("%s = %#v, want %#v", g.Type().Field(i).Name, g.Field(i).Interface(), w.Field(i).Interface())
					}
				}
			}
		})
	}
}

func TestInspectRejected(t *testing.T) {
	legacy0 := []byte("L119547037146038801333356L\n.I1001\n.")
	legacy4 := []byte("\x80\x04\x95\x0d\x00\x00\x00\x00\x00\x00\x00\x8a\x0al\xfc\x9cF\xf9 j\xa8P\x19.\x80\x04")
	var noPickle bytes.Buffer
	zw := zip.NewWriter(&noPickle)
	w, _ := zw.Create("dataset/images/a.jpg")
	_, _ = w.Write([]byte("jpeg"))
	_ = zw.Close()

	tests := []struct {
		name   string
		data   []byte
		reason string
		want   string // part of the message
	}{
		{"legacy-format.pt", nil, ReasonLegacyFormat, "PyTorch 1.6 之前的旧格式"},
		{"legacy protocol 0", legacy0, ReasonLegacyFormat, "旧格式"},
		{"legacy protocol 4", legacy4, ReasonLegacyFormat, "旧格式"},
		{"yolov5-repo.pt", nil, ReasonYOLOv5Repo, "原版 YOLOv5 仓库（github.com/ultralytics/yolov5，或 YOLOv7 等基于它的仓库）训练的（模型类为 models.yolo.DetectionModel）"},
		{"rtdetr-l.pt", nil, ReasonUnsupportedModel, "RT-DETR 模型暂不支持"},
		{"state-dict.pt", nil, ReasonNotUltralytics, "只包含模型权重（state_dict）"},
		{"state-dict-wrapped.pt", nil, ReasonNotUltralytics, "只包含模型权重（state_dict）"},
		{"torchscript.pt", nil, ReasonNotUltralytics, "TorchScript"},
		{"empty", []byte{}, ReasonNotTorch, "文件为空"},
		{"text", []byte("hello"), ReasonNotTorch, "不是 PyTorch 模型文件"},
		{"plain pickle", []byte("\x80\x04\x95\x05\x00\x00\x00\x00\x00\x00\x00}\x94."), ReasonNotTorch, "普通的 pickle 文件"},
		{"safetensors", []byte("\x10\x00\x00\x00\x00\x00\x00\x00{\"w\":{\"dtype\":1}}"), ReasonNotTorch, "safetensors"},
		{"onnx", []byte("\x08\x08\x12\x07pytorch"), ReasonNotTorch, "ONNX"},
		{"zip without data.pkl", noPickle.Bytes(), ReasonNotTorch, "没有 data.pkl"},
		{"zip start only", []byte("PK\x03\x04"), ReasonCorrupt, "zip 结构无法读取"},
		{"broken pickle", torchZip("best", []byte("\x80\x02}q\x00\xff")), ReasonCorrupt, "data.pkl 第 5 字节：未知的操作码 0xff"},
		{"truncated pickle", torchZip("best", []byte("\x80\x02}q\x00(X\x05\x00\x00\x00model")), ReasonCorrupt, "缺少 STOP"},
		{"deep nesting", torchZip("best", bytes.Repeat([]byte("("), 100000)), ReasonTooLarge, "嵌套层数超过上限"},
		{"top-level list", torchZip("best", []byte("]K\x01a.")), ReasonNotUltralytics, "文件中保存的是 list"},
		{"global with a bad name", torchZip("best", []byte("c\n\xe2\x00\n.")), ReasonNotUltralytics, "文件中保存的是 .\ufffd\ufffd，"},
		{"top-level model", torchZip("best", []byte("cultralytics.nn.tasks\nDetectionModel\n)\x81.")), ReasonNotUltralytics,
			"直接保存了一个对象（ultralytics.nn.tasks.DetectionModel）"},
		{"top-level YOLOv5 model", torchZip("best", []byte("cmodels.yolo\nModel\n)\x81.")), ReasonYOLOv5Repo, "models.yolo.Model"},
		{"TorchScript inside a dict", torchZip("best", []byte("}Vmodel\nc__torch__.ultralytics.nn.tasks\nDetectionModel\n)\x81s.")),
			ReasonNotUltralytics, "TorchScript"},
		{"no model", torchZip("best", []byte("}Vepoch\nK\x01s.")), ReasonNotUltralytics, "缺少 model / ema"},
		{"model and ema None", torchZip("best", []byte("}(Vmodel\nNVema\nNu.")), ReasonNotUltralytics, "model 和 ema 都是 None"},
		{"model is a string", torchZip("best", []byte("}Vmodel\nVbest.pt\ns.")), ReasonNotUltralytics, "model 是 str"},
		{"model of an unknown class", torchZip("best", []byte("}Vmodel\nc__main__\nNet\n)\x81s.")), ReasonNotUltralytics,
			"文件中的模型（__main__.Net）不是 Ultralytics 的模型"},
		{"YOLO-NAS", torchZip("best", []byte("}Vmodel\ncsuper_gradients.training.models\nYoloNAS_S\n)\x81s.")),
			ReasonUnsupportedModel, "YOLO-NAS"},
		{"Ultralytics ensemble", torchZip("best", []byte("}Vmodel\ncultralytics.nn.tasks\nEnsemble\n)\x81s.")),
			ReasonUnsupportedModel, "不支持的 Ultralytics 模型类型（ultralytics.nn.tasks.Ensemble）"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.data
			if data == nil {
				data = readFixture(t, tt.name)
			}
			info, err := inspectBytes(data)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Inspect = %+v, %v; want a *Error", info, err)
			}
			if e.Reason != tt.reason || !strings.Contains(e.Message, tt.want) {
				t.Errorf("Inspect = %s: %s; want %s: …%s…", e.Reason, e.Message, tt.reason, tt.want)
			}
		})
	}
}

// sparseFile is a large virtual file: the bytes written outside holes,
// zeros in the holes.
type sparseFile struct {
	segments []segment
	size     int64
	hole     bool // writes only grow the file
}

type segment struct {
	off  int64
	data []byte
}

func (f *sparseFile) Write(p []byte) (int, error) {
	if !f.hole {
		if n := len(f.segments); n > 0 && f.segments[n-1].off+int64(len(f.segments[n-1].data)) == f.size {
			f.segments[n-1].data = append(f.segments[n-1].data, p...)
		} else {
			f.segments = append(f.segments, segment{off: f.size, data: bytes.Clone(p)})
		}
	}
	f.size += int64(len(p))
	return len(p), nil
}

func (f *sparseFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if off >= f.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), f.size-off))
	clear(p[:n])
	for _, s := range f.segments {
		lo, hi := max(off, s.off), min(off+int64(n), s.off+int64(len(s.data)))
		if lo < hi {
			copy(p[lo-off:hi-off], s.data[lo-s.off:hi-s.off])
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestInspectZip64 reads a checkpoint laid out like a model larger than
// 4 GiB: data.pkl, a 5 GiB tensor, then an entry and the central
// directory beyond 4 GiB, which need the zip64 records.
func TestInspectZip64(t *testing.T) {
	src := readFixture(t, "yolo11n-best.pt")
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	file := &sparseFile{}
	zw := zip.NewWriter(file)
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/version") {
			const tensor = 5 << 30
			w, err := zw.CreateRaw(&zip.FileHeader{Name: "yolo11n-best/data/0", Method: zip.Store,
				CompressedSize64: tensor, UncompressedSize64: tensor})
			if err != nil {
				t.Fatal(err)
			}
			if err := zw.Flush(); err != nil {
				t.Fatal(err)
			}
			file.hole = true
			chunk := make([]byte, 1<<20)
			for i := 0; i < tensor/len(chunk); i++ {
				if _, err := w.Write(chunk); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Flush(); err != nil {
				t.Fatal(err)
			}
			file.hole = false
		}
		if err := zw.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if file.size < 5<<30 || len(file.segments) != 2 {
		t.Fatalf("size %d, %d segments", file.size, len(file.segments))
	}
	tail := file.segments[1].data
	if !bytes.Contains(tail, []byte("PK\x06\x06")) {
		t.Fatal("no zip64 end record")
	}
	got, err := Inspect(file, file.size)
	if err != nil {
		t.Fatal(err)
	}
	if want := yolo11Best(); !reflect.DeepEqual(*got, want) {
		t.Errorf("Inspect = %+v, want %+v", *got, want)
	}

	// A zip64 end record declaring more entries than the limit is
	// rejected before archive/zip allocates for them.
	i := bytes.LastIndex(tail, []byte("PK\x06\x06"))
	binary.LittleEndian.PutUint64(tail[i+24:], 1<<40)
	binary.LittleEndian.PutUint64(tail[i+32:], 1<<40)
	_, err = Inspect(file, file.size)
	var e *Error
	if !errors.As(err, &e) || e.Reason != ReasonTooLarge {
		t.Errorf("1<<40 entries: err = %v, want %s", err, ReasonTooLarge)
	}
}

func TestInspectTruncated(t *testing.T) {
	src := readFixture(t, "yolov8n.pt") // stored, like torch writes it
	for _, n := range []int{4, 31, 100, len(src) / 2, len(src) - 23, len(src) - 1} {
		_, err := inspectBytes(src[:n])
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonCorrupt {
			t.Errorf("first %d bytes: err = %v, want %s", n, err, ReasonCorrupt)
		}
	}
	// A damaged byte in data.pkl fails the CRC check.
	damaged := bytes.Clone(src)
	damaged[1000] ^= 0xff
	_, err := inspectBytes(damaged)
	var e *Error
	if !errors.As(err, &e) || e.Reason != ReasonCorrupt || !strings.Contains(e.Message, "checksum") {
		t.Errorf("damaged: err = %v, want a checksum error", err)
	}
}

func TestInspectZipLimits(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		build func(zw *zip.Writer) error
	}{
		{"central directory too large", ReasonTooLarge, func(zw *zip.Writer) error {
			name := strings.Repeat("n", 60000)
			for i := 0; i < 150; i++ {
				if _, err := zw.CreateHeader(&zip.FileHeader{Name: "best/" + strconv.Itoa(i) + name, Method: zip.Store}); err != nil {
					return err
				}
			}
			return nil
		}},
		{"data.pkl declared too large", ReasonTooLarge, func(zw *zip.Writer) error {
			w, err := zw.CreateRaw(&zip.FileHeader{Name: "best/data.pkl", Method: zip.Store,
				CompressedSize64: 1, UncompressedSize64: maxPickleSize + 1})
			if err == nil {
				_, err = w.Write([]byte("."))
			}
			return err
		}},
		// archive/zip stops reading past the declared size.
		{"data.pkl larger than declared", ReasonCorrupt, func(zw *zip.Writer) error {
			var deflated bytes.Buffer
			fw, _ := flate.NewWriter(&deflated, flate.BestSpeed)
			_, _ = fw.Write(make([]byte, maxPickleSize+1))
			_ = fw.Close()
			w, err := zw.CreateRaw(&zip.FileHeader{Name: "best/data.pkl", Method: zip.Deflate,
				CompressedSize64: uint64(deflated.Len()), UncompressedSize64: 100})
			if err == nil {
				_, err = w.Write(deflated.Bytes())
			}
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			if err := tt.build(zw); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := inspectBytes(buf.Bytes())
			var e *Error
			if !errors.As(err, &e) || e.Reason != tt.want {
				t.Errorf("err = %v, want %s", err, tt.want)
			}
		})
	}
}

// failingReader fails every read that reaches past a byte offset.
type failingReader struct {
	data   []byte
	failAt int64
}

var errDisk = errors.New("disk failure")

func (r failingReader) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > r.failAt {
		return 0, errDisk
	}
	return bytes.NewReader(r.data).ReadAt(p, off)
}

// TestInspectReadError: a failing disk is not a problem with the file, so
// it is not reported as *Error.
func TestInspectReadError(t *testing.T) {
	src := readFixture(t, "yolov8n.pt")
	zr, _ := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	dataStart, _ := zr.File[0].DataOffset()
	for _, failAt := range []int64{0, int64(len(src)) - 100, dataStart + 10} {
		_, err := Inspect(failingReader{data: src, failAt: failAt}, int64(len(src)))
		var e *Error
		if !errors.Is(err, errDisk) || errors.As(err, &e) {
			t.Errorf("failing at %d: err = %v, want the read error", failAt, err)
		}
	}
}

func TestFindPickle(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"archive/data.pkl", "archive/version"}, "archive/data.pkl"},
		{[]string{"best/version", "best/data/0", "best/data.pkl"}, "best/data.pkl"},
		{[]string{"x/notes.txt", "model/data.pkl", "x/data.pkl"}, "x/data.pkl"},
		{[]string{"top.txt", "model/data.pkl"}, "model/data.pkl"},
		{[]string{"data.pkl", "a/b/data.pkl", "/data.pkl"}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		var files []*zip.File
		for _, n := range tt.names {
			files = append(files, &zip.File{FileHeader: zip.FileHeader{Name: n}})
		}
		got := ""
		if f := findPickle(files); f != nil {
			got = f.Name
		}
		if got != tt.want {
			t.Errorf("findPickle(%q) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

// Values for extraction tests.

func dict(kv ...any) *pyDict {
	d := &pyDict{}
	for i := 0; i+1 < len(kv); i += 2 {
		d.set(kv[i], kv[i+1])
	}
	return d
}

func list(items ...any) *pyList   { return &pyList{items: items} }
func tuple(items ...any) *pyTuple { return &pyTuple{items: items} }

func object(qual string, attrs *pyDict) *pyObject {
	i := strings.LastIndexByte(qual, '.')
	return &pyObject{class: newGlobal(qual[:i], qual[i+1:]), attrs: attrs}
}

func param(shape ...int64) *pyTensor { return &pyTensor{shape: shape, known: true, param: true} }

// module builds an nn.Module with parameters and submodules.
func module(qual string, params *pyDict, children *pyDict) *pyObject {
	if params == nil {
		params = dict()
	}
	if children == nil {
		children = dict()
	}
	return object(qual, dict("training", false, "_parameters", params, "_buffers", dict(), "_modules", children))
}

// ultralyticsModel builds a DetectionModel with a Sequential of layers.
func ultralyticsModel(attrs *pyDict, layers ...*pyObject) *pyObject {
	seq := dict()
	for i, l := range layers {
		seq.set(strconv.Itoa(i), l)
	}
	m := module("ultralytics.nn.tasks.DetectionModel", nil, dict("model", module("torch.nn.modules.container.Sequential", nil, seq)))
	for i, k := range attrs.keys {
		m.attrs.set(k, attrs.vals[i])
	}
	return m
}

func TestExtract(t *testing.T) {
	conv := func() *pyObject {
		return module("ultralytics.nn.modules.conv.Conv", nil, dict("conv", module("torch.nn.modules.conv.Conv2d",
			dict("weight", param(16, 3, 3, 3), "bias", nil), nil)))
	}
	detect := module("ultralytics.nn.modules.head.Detect", dict("w", param(2, 5)), nil)
	shared := param(10)
	sharedModule := module("x.Shared", dict("w", shared), nil)
	cyclic := module("x.Cyclic", dict("w", param(3)), nil)
	cyclic.attrs.set("_modules", dict("self", cyclic, "again", cyclic))
	// A module tree that doubles at every level: walking it naively takes
	// 2^60 steps.
	dag := module("x.Leaf", dict("w", param(1)), nil)
	for i := 0; i < 60; i++ {
		dag = module("x.Node", nil, dict("a", dag, "b", dag))
	}
	deep := module("x.Leaf", dict("w", param(1)), nil)
	for i := 0; i < maxModuleDepth+1; i++ {
		deep = module("x.Node", nil, dict("child", deep))
	}
	ckpt := func(model *pyObject, kv ...any) *pyDict { return dict(append([]any{"model", model}, kv...)...) }
	metrics := dict()
	for i := 0; i < 70; i++ {
		metrics.set("m"+strconv.Itoa(i), float64(i))
	}
	metrics.set("nan", 0.0)

	tests := []struct {
		name  string
		root  any
		check func(t *testing.T, info *Info)
	}{
		{"names with gaps, string keys and junk", ckpt(ultralyticsModel(dict("names", dict(int64(0), "a", "2", "c", int64(5), int64(7),
			int64(-1), "neg", int64(maxClasses), "big", "x", "bad", int64(3), list())))),
			func(t *testing.T, info *Info) {
				if want := []string{"a", "class1", "c", "class3", "class4", "7"}; !reflect.DeepEqual(info.ClassNames, want) {
					t.Errorf("ClassNames = %q, want %q", info.ClassNames, want)
				}
			}},
		{"names as a list", ckpt(ultralyticsModel(dict("names", list("a", nil, int64(3), "b\x00\xffc"+strings.Repeat("长", 100))))),
			func(t *testing.T, info *Info) {
				want := []string{"a", "class1", "3", "b\ufffd\ufffdc" + strings.Repeat("长", 82)}
				if !reflect.DeepEqual(info.ClassNames, want) {
					t.Errorf("ClassNames = %q, want %q", info.ClassNames, want)
				}
			}},
		{"names from the yaml, then nc", ckpt(ultralyticsModel(dict("yaml", dict("nc", int64(3))))),
			func(t *testing.T, info *Info) {
				if want := []string{"class0", "class1", "class2"}; !reflect.DeepEqual(info.ClassNames, want) {
					t.Errorf("ClassNames = %q, want %q", info.ClassNames, want)
				}
			}},
		{"image size, channels and epochs", ckpt(ultralyticsModel(dict("yaml", dict("ch", int64(1)), "args", dict("imgsz", int64(320)))),
			"train_args", dict("imgsz", list(int64(480), int64(640), true), "epochs", int64(0)), "epoch", int64(-2)),
			func(t *testing.T, info *Info) {
				if info.ImageSize != 640 || info.Channels != 1 || info.Epochs != 0 || info.Epoch != nil {
					t.Errorf("ImageSize %d, Channels %d, Epochs %d, Epoch %v", info.ImageSize, info.Channels, info.Epochs, info.Epoch)
				}
			}},
		{"image size from model.args (an IterableSimpleNamespace)", ckpt(ultralyticsModel(dict("yaml", dict("channels", int64(9999)),
			"args", object("ultralytics.utils.IterableSimpleNamespace", dict("imgsz", int64(320))))), "train_args", dict("imgsz", true)),
			func(t *testing.T, info *Info) {
				if info.ImageSize != 320 || info.Channels != 3 {
					t.Errorf("ImageSize %d, Channels %d", info.ImageSize, info.Channels)
				}
			}},
		{"at most 64 finite metrics", ckpt(ultralyticsModel(dict()), "train_metrics", metrics),
			func(t *testing.T, info *Info) {
				if len(info.Metrics) != maxMetrics || info.Metrics["m63"] != 63 {
					t.Errorf("Metrics = %v", info.Metrics)
				}
			}},
		{"parameters: shared ones and modules once, cycles", ckpt(ultralyticsModel(dict(), conv(), conv(), sharedModule, sharedModule,
			module("x.Also", dict("w", shared, "none", nil), nil), cyclic, dag, detect)),
			func(t *testing.T, info *Info) {
				if want := int64(2*432 + 10 + 3 + 1 + 10); info.Parameters != want {
					t.Errorf("Parameters = %d, want %d", info.Parameters, want)
				}
				if info.Head != "Detect" {
					t.Errorf("Head = %q", info.Head)
				}
			}},
		{"parameters: unknown shape", ckpt(ultralyticsModel(dict(), module("x.M", dict("w", &pyTensor{param: true}), nil))),
			func(t *testing.T, info *Info) {
				if info.Parameters != 0 {
					t.Errorf("Parameters = %d", info.Parameters)
				}
			}},
		{"parameters: not a tensor", ckpt(ultralyticsModel(dict(), module("x.M", dict("w", int64(1)), nil))),
			func(t *testing.T, info *Info) {
				if info.Parameters != 0 {
					t.Errorf("Parameters = %d", info.Parameters)
				}
			}},
		{"parameters: too deep", ckpt(ultralyticsModel(dict(), deep)),
			func(t *testing.T, info *Info) {
				if info.Parameters != 0 {
					t.Errorf("Parameters = %d", info.Parameters)
				}
			}},
		{"ema wins over model", dict("model", ultralyticsModel(dict("names", list("model"))),
			"ema", ultralyticsModel(dict("names", list("ema"))), "optimizer", dict()),
			func(t *testing.T, info *Info) {
				if !reflect.DeepEqual(info.ClassNames, []string{"ema"}) || !info.TrainingCheckpoint {
					t.Errorf("ClassNames %q, TrainingCheckpoint %v", info.ClassNames, info.TrainingCheckpoint)
				}
			}},
		{"stripped optimizer", ckpt(ultralyticsModel(dict()), "ema", nil, "optimizer", nil),
			func(t *testing.T, info *Info) {
				if info.TrainingCheckpoint {
					t.Error("TrainingCheckpoint")
				}
			}},
		{"version from train_args.model", ckpt(ultralyticsModel(dict()), "train_args", dict("model", `C:\models\yolov8m.pt`)),
			func(t *testing.T, info *Info) {
				if info.YOLOVersion != "yolov8" || info.VersionSource != VersionSourceYAMLFile || info.Scale != "m" ||
					info.YAMLFile != "" || info.BaseModel != `C:\models\yolov8m.pt` {
					t.Errorf("%+v", info)
				}
			}},
		{"version from the layers, scale from the yaml", ckpt(ultralyticsModel(dict("yaml", dict("yaml_file", "/x/custom.yaml",
			"scale", "S!", "scales", dict("m", list(), "l", list()))),
			module("ultralytics.nn.modules.block.C2f", nil, nil), module("ultralytics.nn.modules.head.Segment", nil, nil))),
			func(t *testing.T, info *Info) {
				if info.YOLOVersion != "yolov8" || info.VersionSource != VersionSourceArchitecture || info.Scale != "m" ||
					info.YAMLFile != "custom.yaml" || info.Head != "Segment" || info.Task != "detect" {
					t.Errorf("%+v", info)
				}
			}},
		{"version unknown", ckpt(ultralyticsModel(dict("yaml", dict("yaml_file", "custom.yaml", "backbone", list(list(int64(-1), int64(1), "C3", list())),
			"head", list(list(int64(-1), int64(1), "nn.Upsample", list()), list(list(int64(1)), int64(1), "Detect", list())))))),
			func(t *testing.T, info *Info) {
				if info.YOLOVersion != "" || info.VersionSource != "" || info.Head != "Detect" {
					t.Errorf("%+v", info)
				}
			}},
		{"end-to-end head", ckpt(ultralyticsModel(dict("yaml", dict("end2end", int64(0))),
			module("ultralytics.nn.modules.head.Detect", nil, dict("one2one_cv2", module("torch.nn.ModuleList", nil, nil))))),
			func(t *testing.T, info *Info) {
				if !info.End2End {
					t.Error("End2End = false")
				}
			}},
		{"another Ultralytics model: task from train_args", ckpt(object("ultralytics.nn.tasks.FutureModel", dict("task", "pose")),
			"train_args", dict("task", "Detect!")),
			func(t *testing.T, info *Info) {
				if info.Task != "pose" || info.ModelClass != "FutureModel" {
					t.Errorf("Task %q, ModelClass %q", info.Task, info.ModelClass)
				}
			}},
		{"another Ultralytics model: task from the head", ckpt(object("ultralytics.nn.tasks.FutureModel", dict("yaml", dict(
			"head", list(list(int64(-1), int64(1), "SemanticSegment", list()))))),
			"train_args", dict("task", "Detect!")),
			func(t *testing.T, info *Info) {
				if info.Task != "semantic" || info.Head != "SemanticSegment" {
					t.Errorf("Task %q, Head %q", info.Task, info.Head)
				}
			}},
		{"text is cleaned", ckpt(ultralyticsModel(dict()), "version", "8.4\x00", "date", "\xff", "train_args", dict("data", strings.Repeat("d", 2000))),
			func(t *testing.T, info *Info) {
				if info.UltralyticsVersion != "8.4\ufffd" || info.Date != "\ufffd" || len(info.TrainData) != maxTextLen {
					t.Errorf("%q %q %d", info.UltralyticsVersion, info.Date, len(info.TrainData))
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := extract(tt.root)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, info)
		})
	}

	// Errors.
	for _, tt := range []struct {
		name   string
		root   any
		reason string
	}{
		{"no model, tensors", dict("conv.weight", param(1)), ReasonNotUltralytics},
		{"state_dict key", dict("state_dict", dict()), ReasonNotUltralytics},
		{"model is a state dict", ckpt(nil, "model", dict("w", param(1))), ReasonNotUltralytics},
		{"model of no class", dict("model", &pyObject{}), ReasonNotUltralytics},
		{"YOLOv5 common module", dict("model", object("models.common.DetectMultiBackend", dict())), ReasonYOLOv5Repo},
		{"RT-DETR anywhere", dict("ema", object("thirdparty.RTDETRDetectionModel", dict())), ReasonUnsupportedModel},
	} {
		_, err := extract(tt.root)
		var e *Error
		if !errors.As(err, &e) || e.Reason != tt.reason {
			t.Errorf("%s: err = %v, want %s", tt.name, err, tt.reason)
		}
	}
}

func TestCleanText(t *testing.T) {
	for _, tt := range []struct {
		in   string
		max  int
		want string
	}{
		{"scratch", 64, "scratch"},
		{"a\tb\nc", 64, "a\ufffdb\ufffdc"},
		{"\xffa", 64, "\ufffda"},
		{"划痕-凹坑", 4, "划"},
		{"划痕", 5, "划"},
		{"", 4, ""},
	} {
		got := cleanText(tt.in, tt.max)
		if got != tt.want || !utf8.ValidString(got) {
			t.Errorf("cleanText(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
