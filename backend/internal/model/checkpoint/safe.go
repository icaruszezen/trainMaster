package checkpoint

import (
	"strings"
)

// The globals a checkpoint may reference. The trainer converts models with
// Ultralytics' restricted loading (ULTRALYTICS_SAFE_LOAD=1: torch.load
// with weights_only=True against the allow-list of _SafeLoad in
// ultralytics/nn/tasks.py), which refuses any other class or function. A
// file that references one could not be converted and is likely crafted
// (os.system and the like run while Python unpickles it), so Inspect
// rejects it at upload with ReasonUnsafe. The rules mirror torch 2.14 and
// Ultralytics 8.4.198 (testdata/README.md, "Safe globals"):
//
//   - torch's own allow-list (torchSafeGlobals);
//   - the data globals and aliases of _SafeLoad._build (ultralyticsGlobals);
//   - the classes of torch.nn.modules, ultralytics.nn.modules, their direct
//     submodules and ultralytics.nn.tasks, and of ultralytics.nn.distill_model,
//     ultralytics.utils.loss and ultralytics.utils.tal (allowedClass);
//   - classification transforms when the file references
//     torchvision.transforms, and CLIP when it references
//     ultralytics.nn.text_model.CLIP (_SafeLoad.register).
//
// Names are compared after torch's mapping of Python 2 module names
// (IMPORT_MAPPING), as torch reads GLOBAL.

// torchSafeGlobals is torch._weights_only_unpickler._get_allowed_globals().
var torchSafeGlobals = setOf(
	"_codecs.encode", "builtins.bytearray", "builtins.complex", "builtins.set", "collections.Counter",
	"collections.OrderedDict", "torch.BFloat16Storage", "torch.BFloat16Tensor", "torch.BoolStorage", "torch.BoolTensor",
	"torch.ByteStorage", "torch.ByteTensor", "torch.CharStorage", "torch.CharTensor", "torch.ComplexDoubleStorage",
	"torch.ComplexFloatStorage", "torch.DoubleStorage", "torch.DoubleTensor", "torch.FloatStorage", "torch.FloatTensor",
	"torch.HalfStorage", "torch.HalfTensor", "torch.IntStorage", "torch.IntTensor", "torch.LongStorage",
	"torch.LongTensor", "torch.QInt32Storage", "torch.QInt8Storage", "torch.QUInt2x4Storage", "torch.QUInt4x2Storage",
	"torch.QUInt8Storage", "torch.ShortStorage", "torch.ShortTensor", "torch.Size", "torch.Tensor",
	"torch._tensor._rebuild_from_type_v2", "torch._utils._rebuild_device_tensor_from_cpu_tensor",
	"torch._utils._rebuild_device_tensor_from_numpy", "torch._utils._rebuild_meta_tensor_no_storage",
	"torch._utils._rebuild_nested_tensor", "torch._utils._rebuild_parameter",
	"torch._utils._rebuild_parameter_with_state", "torch._utils._rebuild_qtensor", "torch._utils._rebuild_sparse_tensor",
	"torch._utils._rebuild_tensor", "torch._utils._rebuild_tensor_v2", "torch._utils._rebuild_tensor_v3",
	"torch._utils._rebuild_wrapper_subclass", "torch.bcomplex32", "torch.bfloat16", "torch.bits16", "torch.bits1x8",
	"torch.bits2x4", "torch.bits4x2", "torch.bits8", "torch.bool", "torch.complex128", "torch.complex32",
	"torch.complex64", "torch.cuda.BFloat16Storage", "torch.cuda.BFloat16Tensor", "torch.cuda.BoolStorage",
	"torch.cuda.BoolTensor", "torch.cuda.ByteStorage", "torch.cuda.ByteTensor", "torch.cuda.CharStorage",
	"torch.cuda.CharTensor", "torch.cuda.ComplexDoubleStorage", "torch.cuda.ComplexFloatStorage",
	"torch.cuda.DoubleStorage", "torch.cuda.DoubleTensor", "torch.cuda.FloatStorage", "torch.cuda.FloatTensor",
	"torch.cuda.HalfStorage", "torch.cuda.HalfTensor", "torch.cuda.IntStorage", "torch.cuda.IntTensor",
	"torch.cuda.LongStorage", "torch.cuda.LongTensor", "torch.cuda.ShortStorage", "torch.cuda.ShortTensor",
	"torch.cuda.sparse.BFloat16Tensor", "torch.cuda.sparse.ByteTensor", "torch.cuda.sparse.CharTensor",
	"torch.cuda.sparse.DoubleTensor", "torch.cuda.sparse.FloatTensor", "torch.cuda.sparse.HalfTensor",
	"torch.cuda.sparse.IntTensor", "torch.cuda.sparse.LongTensor", "torch.cuda.sparse.ShortTensor", "torch.device",
	"torch.float16", "torch.float32", "torch.float4_e2m1fn_x2", "torch.float64", "torch.float8_e4m3fn",
	"torch.float8_e4m3fnuz", "torch.float8_e5m2", "torch.float8_e5m2fnuz", "torch.float8_e8m0fnu", "torch.int1",
	"torch.int16", "torch.int2", "torch.int3", "torch.int32", "torch.int4", "torch.int5", "torch.int6", "torch.int64",
	"torch.int7", "torch.int8", "torch.nn.parameter.Parameter", "torch.per_channel_affine",
	"torch.per_channel_affine_float_qparams", "torch.per_channel_symmetric", "torch.per_tensor_affine",
	"torch.per_tensor_symmetric", "torch.qint32", "torch.qint8", "torch.quint2x4", "torch.quint4x2", "torch.quint8",
	"torch.serialization._get_layout", "torch.sparse.BFloat16Tensor", "torch.sparse.ByteTensor",
	"torch.sparse.CharTensor", "torch.sparse.DoubleTensor", "torch.sparse.FloatTensor", "torch.sparse.HalfTensor",
	"torch.sparse.IntTensor", "torch.sparse.LongTensor", "torch.sparse.ShortTensor", "torch.storage.TypedStorage",
	"torch.storage.UntypedStorage", "torch.uint1", "torch.uint16", "torch.uint2", "torch.uint3", "torch.uint32",
	"torch.uint4", "torch.uint5", "torch.uint6", "torch.uint64", "torch.uint7", "torch.uint8",
)

// ultralyticsGlobals are the entries of _SafeLoad._build().
var ultralyticsGlobals = setOf(
	"numpy.dtype", "numpy.core.multiarray.scalar", "numpy._core.multiarray.scalar",
	"ultralytics.utils.IterableSimpleNamespace", "ultralytics.yolo.utils.IterableSimpleNamespace",
	"ultralytics.nn.modules.block.Silence", "ultralytics.nn.tasks.YOLOv10DetectionModel",
	"ultralytics.utils.loss.v10DetectLoss", "builtins.getattr",
	"pathlib.PosixPath", "pathlib.WindowsPath", "pathlib._local.PosixPath", "pathlib._local.WindowsPath",
)

// The classification transforms (allowed whenever the file references
// torchvision.transforms) and what they bring along.
var (
	transformGlobals = setOf(
		"torchvision.transforms.transforms.Compose", "torchvision.transforms.transforms.Normalize",
		"torchvision.transforms.transforms.Resize", "torchvision.transforms.transforms.CenterCrop",
		"torchvision.transforms.transforms.ToTensor", "torchvision.transforms.transforms.ToPILImage",
		"torchvision.transforms.functional.InterpolationMode",
	)
	transformExtras = setOf("ultralytics.data.augment.ClassifyLetterBox", "numpy.asarray")
)

// The text model of YOLO-World and YOLOE, and the OpenAI CLIP classes it
// brings along.
const clipGlobal = "ultralytics.nn.text_model.CLIP"

var clipExtras = setOf(
	"clip.model.CLIP", "clip.model.LayerNorm", "clip.model.QuickGELU", "clip.model.ResidualAttentionBlock",
	"clip.model.Transformer", "clip.model.VisionTransformer", "clip.clip._convert_image_to_rgb",
)

// The modules whose nn.Module classes (in classModules: classes defined
// there) are allowed, with modulePackages also for their direct submodules.
var (
	modulePackages = setOf("torch.nn.modules", "ultralytics.nn.modules")
	classModules   = setOf("ultralytics.nn.distill_model", "ultralytics.utils.loss", "ultralytics.utils.tal")
)

// torchImportMapping is torch._utils.IMPORT_MAPPING: the Python 2 module
// names torch renames when it reads a GLOBAL. (torch's NAME_MAPPING
// renames a few more globals, none to an allowed one.)
var torchImportMapping = map[string]string{
	"__builtin__": "builtins", "copy_reg": "copyreg", "Queue": "queue", "repr": "reprlib",
	"_abcoll": "collections.abc", "UserDict": "collections", "UserList": "collections",
	"UserString": "collections", "whichdb": "dbm", "StringIO": "io", "cStringIO": "io",
}

// torchModule is the module name torch checks for a GLOBAL. It differs
// from newGlobal's mapping for a few (torch does not map "exceptions",
// and maps more Python 2 names).
func torchModule(module string) string {
	if m, ok := torchImportMapping[module]; ok {
		return m
	}
	return module
}

func setOf(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// allowedClass reports whether name ("module.Class") is a class of the
// modules whose classes _SafeLoad allows. Ultralytics also checks that the
// class exists and is an nn.Module (or, in classModules, is defined
// there); without importing anything the platform can only check that the
// name looks like a class: an identifier with an upper-case letter
// (v10Detect and v8DetectionLoss do not start with one, _ConvNd is an
// nn.Module). The few names this lets through that are not such classes
// (typing names, constants) are refused by the trainer.
func allowedClass(name string) bool {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return false
	}
	module, class := name[:i], name[i+1:]
	parent := module[:max(strings.LastIndexByte(module, '.'), 0)]
	if !modulePackages[module] && !modulePackages[parent] && module != "ultralytics.nn.tasks" && !classModules[module] {
		return false
	}
	return isClassName(class)
}

func isClassName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	upper := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return upper
}

// maxRefusedNames is how many refused globals the message names.
const maxRefusedNames = 3

// globalCheck collects the globals of a pickle that Ultralytics' safe
// loading would refuse: the first few of each kind, in order, for the
// message (one more than it shows, to know there are more).
type globalCheck struct {
	refused       []string // refused in any case
	transforms    []string // transformExtras: refused unless sawTransforms
	clip          []string // clipExtras: refused unless sawCLIP
	sawTransforms bool     // a torchvision.transforms global
	sawCLIP       bool     // clipGlobal
}

// add checks the global of a GLOBAL, STACK_GLOBAL or INST opcode, named
// as torch names it ("module.name" with torchModule).
func (c *globalCheck) add(full string) {
	switch {
	case torchSafeGlobals[full] || ultralyticsGlobals[full] || allowedClass(full):
	case strings.HasPrefix(full, "torchvision.transforms."):
		c.sawTransforms = true
		if !transformGlobals[full] {
			c.refused = note(c.refused, full)
		}
	case full == clipGlobal:
		c.sawCLIP = true
	case transformExtras[full]:
		c.transforms = note(c.transforms, full)
	case clipExtras[full]:
		c.clip = note(c.clip, full)
	default:
		c.refused = note(c.refused, full)
	}
}

// note adds name to names unless it is there or names is full.
func note(names []string, name string) []string {
	if len(names) > maxRefusedNames {
		return names
	}
	name = cleanText(name, maxShortTextLen)
	for _, n := range names {
		if n == name {
			return names
		}
	}
	return append(names, name)
}

// result returns the refused globals to name (at most maxRefusedNames)
// and whether there are more.
func (c *globalCheck) result() ([]string, bool) {
	names := append([]string(nil), c.refused...)
	if !c.sawTransforms {
		names = append(names, c.transforms...)
	}
	if !c.sawCLIP {
		names = append(names, c.clip...)
	}
	if len(names) > maxRefusedNames {
		return names[:maxRefusedNames], true
	}
	return names, false
}

func unsafeError(names []string, more bool) *Error {
	list := strings.Join(names, "、")
	it := "它"
	if len(names) > 1 || more {
		it = "它们"
	}
	if more {
		list += " 等"
	}
	return newError(ReasonUnsafe, "文件中引用了平台不允许加载的对象（"+list+"），为了安全不会使用"+it+
		"。请上传由 Ultralytics 训练保存的模型文件")
}
