package checkpoint

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// safeGlobalCases are GLOBALs (module and name as pickled) with whether
// Ultralytics' safe loading accepts them; testdata/README.md ("Safe
// globals") checks the same names against Python.
var safeGlobalCases = []struct {
	module, name string
	allowed      bool
}{
	// torch's allow-list.
	{"torch._utils", "_rebuild_tensor_v2", true},
	{"torch._utils", "_rebuild_parameter", true},
	{"torch", "FloatStorage", true},
	{"torch", "HalfStorage", true},
	{"torch", "Size", true},
	{"torch", "float32", true},
	{"collections", "OrderedDict", true},
	{"builtins", "set", true},
	{"__builtin__", "set", true},      // torch maps Python 2 module names
	{"UserDict", "OrderedDict", true}, // UserDict is collections
	{"_codecs", "encode", true},
	// _SafeLoad._build.
	{"builtins", "getattr", true},
	{"__builtin__", "getattr", true},
	{"numpy", "dtype", true},
	{"numpy.core.multiarray", "scalar", true},
	{"numpy._core.multiarray", "scalar", true},
	{"ultralytics.utils", "IterableSimpleNamespace", true},
	{"ultralytics.yolo.utils", "IterableSimpleNamespace", true},
	{"ultralytics.nn.modules.block", "Silence", true},
	{"ultralytics.nn.tasks", "YOLOv10DetectionModel", true},
	{"ultralytics.utils.loss", "v10DetectLoss", true},
	{"pathlib", "PosixPath", true},
	{"pathlib._local", "WindowsPath", true},
	// Classes of the model modules.
	{"torch.nn.modules.conv", "Conv2d", true},
	{"torch.nn.modules.container", "Sequential", true},
	{"torch.nn.modules", "Conv2d", true},
	{"torch.nn.modules.conv", "_ConvNd", true},
	{"ultralytics.nn.modules.block", "C3k2", true},
	{"ultralytics.nn.modules.head", "v10Detect", true},
	{"ultralytics.nn.modules", "Detect", true},
	{"ultralytics.nn.modules", "block.C2f", true}, // the same name as ultralytics.nn.modules.block.C2f
	{"ultralytics.nn.tasks", "DetectionModel", true},
	{"ultralytics.nn.tasks", "RTDETRDetectionModel", true},
	{"ultralytics.nn.distill_model", "DistillationModel", true},
	{"ultralytics.utils.loss", "v8DetectionLoss", true},
	{"ultralytics.utils.tal", "TaskAlignedAssigner", true},
	// Classification transforms, and the CLIP text model.
	{"torchvision.transforms.transforms", "Compose", true},
	{"torchvision.transforms.functional", "InterpolationMode", true},
	{"ultralytics.nn.text_model", "CLIP", true},

	// Refused.
	{"posix", "system", false},
	{"os", "system", false},
	{"nt", "system", false},
	{"subprocess", "Popen", false},
	{"builtins", "eval", false},
	{"builtins", "exec", false},
	{"__builtin__", "eval", false},
	{"builtins", "object", false},
	{"builtins", "dict", false},
	{"exceptions", "set", false}, // torch does not map "exceptions" as a module
	{"copy_reg", "_reconstructor", false},
	{"copyreg", "_reconstructor", false},
	{"collections", "defaultdict", false},
	{"pathlib", "Path", false},
	{"pathlib._local", "PurePosixPath", false},
	{"numpy.core.multiarray", "_reconstruct", false},
	{"numpy", "ndarray", false},
	{"torch.nn", "Conv2d", false},                      // torch.nn is not one of the modules
	{"torch.nn.modules.conv.sub", "Conv2d", false},     // nor a submodule two levels down
	{"torch.nn.modules.conv", "conv2d", false},         // a function
	{"ultralytics.nn.tasks", "torch_safe_load", false}, // a function
	{"ultralytics.nn.tasks", "DetectionModel.forward", false},
	{"ultralytics.nn.tasks", "Detection Model", false},
	{"ultralytics.nn.tasks", "9Model", false},
	{"ultralytics.nn.tasks", "", false},
	{"ultralytics.models.yolo.detect", "DetectionTrainer", false},
	{"ultralytics.utils", "LOGGER", false},
	{"models.yolo", "DetectionModel", false},
	{"__torch__.ultralytics.nn.tasks", "DetectionModel", false},
	{"torchvision.transforms.transforms", "Lambda", false},
	{"torch.serialization", "load", false},
	{"", "eval", false},
}

// refusedGlobals reads the globals (module, name, module, name ...) as
// GLOBAL does and returns those safe loading refuses, as Inspect names them.
func refusedGlobals(t *testing.T, globals ...string) ([]string, bool) {
	t.Helper()
	u := newUnpickler(nil, defaultLimits)
	for i := 0; i+1 < len(globals); i += 2 {
		if _, err := u.global(globals[i], globals[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	return u.globals.result()
}

// split cuts "module.name" at the last dot.
func split(globals []string) []string {
	var parts []string
	for _, g := range globals {
		i := strings.LastIndexByte(g, '.')
		parts = append(parts, g[:i], g[i+1:])
	}
	return parts
}

func TestSafeGlobals(t *testing.T) {
	for _, tt := range safeGlobalCases {
		names, _ := refusedGlobals(t, tt.module, tt.name)
		if (len(names) == 0) != tt.allowed {
			t.Errorf("%s.%s: refused %q, want allowed %v", tt.module, tt.name, names, tt.allowed)
		}
	}

	// Allowed only with the global that brings them along.
	for _, tt := range []struct {
		globals []string
		refused []string
	}{
		{[]string{"numpy.asarray"}, []string{"numpy.asarray"}},
		{[]string{"ultralytics.data.augment.ClassifyLetterBox", "numpy.asarray"},
			[]string{"ultralytics.data.augment.ClassifyLetterBox", "numpy.asarray"}},
		{[]string{"numpy.asarray", "torchvision.transforms.transforms.Resize", "ultralytics.data.augment.ClassifyLetterBox"}, nil},
		{[]string{"numpy.asarray", "torchvision.transforms.transforms.Lambda"}, []string{"torchvision.transforms.transforms.Lambda"}},
		{[]string{"clip.model.CLIP", "clip.clip._convert_image_to_rgb"}, []string{"clip.model.CLIP", "clip.clip._convert_image_to_rgb"}},
		{[]string{"clip.model.Transformer", "ultralytics.nn.text_model.CLIP", "clip.model.LayerNorm"}, nil},
		{[]string{"clip.model.Transformer", "os.system", "numpy.asarray"}, []string{"os.system", "numpy.asarray", "clip.model.Transformer"}},
	} {
		if names, more := refusedGlobals(t, split(tt.globals)...); !reflect.DeepEqual(names, tt.refused) && !(len(names) == 0 && tt.refused == nil) || more {
			t.Errorf("%q: refused %q (more %v), want %q", tt.globals, names, more, tt.refused)
		}
	}

	// The message names three, distinct.
	names, more := refusedGlobals(t, split([]string{"os.system", "os.system", "builtins.eval", "posix.system", "nt.system", "sys.exit"})...)
	if !reflect.DeepEqual(names, []string{"os.system", "builtins.eval", "posix.system"}) || !more {
		t.Errorf("refused %q, more %v", names, more)
	}
	if msg := unsafeError(names, more).Message; msg != "文件中引用了平台不允许加载的对象（os.system、builtins.eval、posix.system 等），"+
		"为了安全不会使用它们。请上传由 Ultralytics 训练保存的模型文件" {
		t.Errorf("message %q", msg)
	}
	if msg := unsafeError([]string{"os.system"}, false).Message; msg != "文件中引用了平台不允许加载的对象（os.system），"+
		"为了安全不会使用它。请上传由 Ultralytics 训练保存的模型文件" {
		t.Errorf("message %q", msg)
	}
}

// TestInspectUnsafe adds calls to a real checkpoint, as a crafted file
// would: Inspect must refuse it although the model in it is fine.
func TestInspectUnsafe(t *testing.T) {
	pkl := fixturePickle(t, readFixture(t, "yolo11n-best.pt"))
	const start = "\x80\x02}q\x00" // the checkpoint dict, before its SETITEMS
	if !bytes.HasPrefix(pkl, []byte(start)) {
		t.Fatalf("data.pkl starts with %q", pkl[:16])
	}
	inject := func(calls ...string) []byte {
		var p pk
		p.op(start)
		for i, c := range calls {
			j := strings.LastIndexByte(c, '.')
			p.str("evil"+string(rune('a'+i))).global(c[:j], c[j+1:]).str("id").op("\x85Rs")
		}
		p.Write(pkl[len(start):])
		return torchZip("best", p.Bytes())
	}
	for _, tt := range []struct {
		calls []string
		want  string
	}{
		{[]string{"posix.system"}, "（posix.system），为了安全不会使用它。"},
		{[]string{"os.system", "builtins.eval", "subprocess.Popen", "builtins.exec"}, "（os.system、builtins.eval、subprocess.Popen 等），为了安全不会使用它们。"},
		{[]string{"__builtin__.eval"}, "（builtins.eval）"},
		{[]string{"ultralytics.nn.tasks.attempt_load_weights"}, "（ultralytics.nn.tasks.attempt_load_weights）"},
	} {
		_, err := inspectBytes(inject(tt.calls...))
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonUnsafe || !strings.Contains(e.Message, tt.want) {
			t.Errorf("%q: Inspect = %v, want %s: …%s…", tt.calls, err, ReasonUnsafe, tt.want)
		}
	}
	// Calls of allowed functions are fine.
	info, err := inspectBytes(inject("collections.OrderedDict", "builtins.set"))
	if err != nil || !reflect.DeepEqual(*info, yolo11Best()) {
		t.Errorf("Inspect = %+v, %v", info, err)
	}

	// A file the platform cannot use for another reason says why.
	for name, reason := range map[string]string{
		"yolov5-repo.pt": ReasonYOLOv5Repo, "torchscript.pt": ReasonNotUltralytics, "rtdetr-l.pt": ReasonUnsupportedModel,
	} {
		_, err := inspectBytes(readFixture(t, name))
		var e *Error
		if !errors.As(err, &e) || e.Reason != reason {
			t.Errorf("%s: Inspect = %v, want %s", name, err, reason)
		}
	}
}

// TestFixtureGlobals: every global of the fixtures that Inspect accepts is
// allowed (TestInspectFixtures also checks this through Inspect).
func TestFixtureGlobals(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("testdata", "*.pt"))
	for _, p := range paths {
		name := filepath.Base(p)
		data := readFixture(t, name)
		if !bytes.HasPrefix(data, magicZip) {
			continue
		}
		u := newUnpickler(fixturePickle(t, data), defaultLimits)
		if _, err := u.run(); err != nil {
			continue
		}
		names, _ := u.globals.result()
		refused := len(names) > 0
		want := map[string]bool{ // files with globals that safe loading refuses
			"yolov5-repo.pt": true, "torchscript.pt": true,
			"yolo26n-best-protocol0.pt": true, "yolo26n-best-protocol1.pt": true,
		}[name]
		if refused != want {
			t.Errorf("%s: refused globals %q", name, names)
		}
	}
}
