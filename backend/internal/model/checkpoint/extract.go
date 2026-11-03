package checkpoint

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Extraction of Info from the unpickled checkpoint. An Ultralytics
// checkpoint is a dict:
//
//	{"date", "version", "epoch", "best_fitness", "model", "ema", "updates",
//	 "optimizer", "train_args", "train_metrics", "train_results", "git", ...}
//
// "ema" (training checkpoints) or else "model" is the nn.Module, whose
// attributes (from BUILD) hold "yaml" (the model config plus yaml_file and
// scale), "names", "args" and, through "_modules" / "_parameters", the
// layers and their parameters.

// Bounds on what Info keeps from a possibly crafted file. Strings are
// also made valid UTF-8 without control characters: the service stores
// Info as jsonb, which rejects "\u0000". A string from the file can be a
// memoized string of megabytes referenced thousands of times (as every
// class name, say), so extraction never does more than a bounded amount
// of work on one: it truncates first (cleanText), and skips longer names.
const (
	maxClasses      = 1 << 16
	maxClassNameLen = 256 // bytes
	maxTextLen      = 1024
	maxShortTextLen = 128
	maxMetrics      = 64
	maxModuleDepth  = 128
)

// taskByClass maps the Ultralytics model classes to their task.
var taskByClass = map[string]string{
	"DetectionModel":            "detect",
	"SegmentationModel":         "segment",
	"PoseModel":                 "pose",
	"ClassificationModel":       "classify",
	"OBBModel":                  "obb",
	"WorldModel":                "detect",
	"YOLOEModel":                "detect",
	"YOLOESegModel":             "segment",
	"SemanticSegmentationModel": "semantic",
	"DepthModel":                "depth",
}

const supportedModels = "平台支持用 Ultralytics（pip install ultralytics）训练的 YOLOv8、YOLOv9、YOLOv10、YOLO11、YOLO12、YOLO26 模型"

const uploadHint = "请上传 Ultralytics 训练得到的 .pt 文件（如 runs/detect/train/weights/best.pt）"

func extract(root any) (*Info, error) {
	ckpt, ok := root.(*pyDict)
	if !ok {
		return nil, notCheckpoint(root)
	}
	modelVal, hasModel := ckpt.get("model")
	emaVal, hasEMA := ckpt.get("ema")
	if !hasModel && !hasEMA {
		return nil, noModel(ckpt)
	}
	m := emaVal
	if m == nil {
		m = modelVal
	}
	model, ok := m.(*pyObject)
	if !ok || model.class == nil {
		switch m.(type) {
		case nil:
			return nil, newError(ReasonNotUltralytics, "文件中的模型为空（model 和 ema 都是 None），不是可用的 Ultralytics 模型。"+uploadHint)
		case *pyDict:
			return nil, stateDictError()
		}
		return nil, newError(ReasonNotUltralytics, "文件中的 model 是 "+typeName(m)+
			"，不是 Ultralytics 的模型对象。"+uploadHint)
	}
	if err := checkModelClass(model.class); err != nil {
		return nil, err
	}

	info := &Info{
		ModelClass: cleanText(model.class.name, maxShortTextLen),
		Channels:   3,
		ClassNames: []string{},
		Metrics:    map[string]float64{},
	}
	yaml := lookupDict(model, "yaml")
	trainArgs := lookupDict(ckpt, "train_args")
	args, _ := lookup(model, "args") // a dict, or an IterableSimpleNamespace in training checkpoints

	// Layers.
	head := headModule(model)
	if head != nil && head.class != nil {
		info.Head = cleanText(head.class.name, maxShortTextLen)
	}
	modules, lastLayer := yamlModules(yaml)
	if len(modules) == 0 {
		modules = layerClasses(model)
	}
	if info.Head == "" {
		info.Head = lastLayer
	}
	if v, _ := yaml.get("end2end"); truthy(v) {
		info.End2End = true
	}
	if head != nil {
		if v, ok := lookupDict(head, "_modules").get("one2one_cv2"); ok && v != nil {
			info.End2End = true
		}
	}
	if n, ok := asInt(first(yaml, "channels", "ch")); ok && n > 0 && n <= 1024 {
		info.Channels = int(n)
	}
	info.Parameters = countParameters(model)

	// Task.
	info.Task = taskByClass[model.class.name]
	if info.Task == "" { // another Ultralytics *Model class
		for _, v := range []any{lookupValue(trainArgs, "task"), lookupValue(model, "task"), lookupValue(args, "task")} {
			if s, ok := asString(v); ok && validTask(s) {
				info.Task = s
				break
			}
		}
		if info.Task == "" {
			info.Task = taskFromHead(info.Head)
		}
	}

	// Classes.
	if names, ok := classNames(lookupValue(model, "names")); ok {
		info.ClassNames = names
	} else if names, ok := classNames(lookupValue(yaml, "names")); ok {
		info.ClassNames = names
	} else if n, ok := asInt(first(yaml, "nc")); ok && n > 0 && n <= maxClasses {
		info.ClassNames = make([]string, n)
		for i := range info.ClassNames {
			info.ClassNames[i] = "class" + strconv.Itoa(i)
		}
	}

	// Version and scale: from the yaml file name (or, without one, the
	// name of the model trained from), else from the architecture.
	if s, ok := asString(lookupValue(yaml, "yaml_file")); ok {
		info.YAMLFile = cleanText(baseName(s), maxShortTextLen)
	}
	name := info.YAMLFile
	if name == "" {
		name, _ = asString(lookupValue(trainArgs, "model"))
	}
	version, nameScale := versionFromName(name)
	switch {
	case version != "":
		info.YOLOVersion, info.VersionSource = version, VersionSourceYAMLFile
	default:
		if v := versionFromArchitecture(modules, info.Head, info.End2End); v != "" {
			info.YOLOVersion, info.VersionSource = v, VersionSourceArchitecture
		}
	}
	if s, ok := asString(lookupValue(yaml, "scale")); ok && validScale(s) {
		info.Scale = s
	} else if nameScale != "" {
		info.Scale = nameScale
	} else if scales := lookupDict(yaml, "scales"); scales.len() > 0 {
		// Ultralytics builds a yaml without a scale with its first one.
		if s, ok := asString(scales.keys[0]); ok && validScale(s) {
			info.Scale = s
		}
	}

	// Training.
	info.ImageSize = imageSize(lookupValue(trainArgs, "imgsz"))
	if info.ImageSize == 0 {
		info.ImageSize = imageSize(lookupValue(args, "imgsz"))
	}
	if n, ok := asInt(lookupValue(trainArgs, "epochs")); ok && n > 0 && n <= math.MaxInt32 {
		info.Epochs = int(n)
	}
	if n, ok := asInt(lookupValue(ckpt, "epoch")); ok && n >= -1 && n <= math.MaxInt32 {
		e := int(n)
		info.Epoch = &e
	}
	if f, ok := asFloat(lookupValue(ckpt, "best_fitness")); ok {
		info.BestFitness = &f
	}
	if metrics := lookupDict(ckpt, "train_metrics"); metrics != nil {
		n := 0 // numeric entries read; keys that only differ after maxShortTextLen bytes give one metric
		for i, k := range metrics.keys {
			key, ok := asString(k)
			f, ok2 := asFloat(metrics.vals[i])
			if !ok || !ok2 {
				continue
			}
			if n++; n > maxMetrics {
				break
			}
			info.Metrics[cleanText(key, maxShortTextLen)] = f
		}
	}
	if s, ok := asString(lookupValue(ckpt, "version")); ok {
		info.UltralyticsVersion = cleanText(s, maxShortTextLen)
	}
	if s, ok := asString(lookupValue(ckpt, "date")); ok {
		info.Date = cleanText(s, maxShortTextLen)
	}
	if s, ok := asString(lookupValue(trainArgs, "data")); ok {
		info.TrainData = cleanText(s, maxTextLen)
	}
	if s, ok := asString(lookupValue(trainArgs, "model")); ok {
		info.BaseModel = cleanText(s, maxTextLen)
	}
	if v, ok := ckpt.get("optimizer"); ok && v != nil {
		info.TrainingCheckpoint = true
	}
	return info, nil
}

// lookupValue is lookup without the found flag.
func lookupValue(v any, key string) any {
	val, _ := lookup(v, key)
	return val
}

// first returns the first of the keys present in d.
func first(d *pyDict, keys ...string) any {
	for _, k := range keys {
		if v, ok := d.get(k); ok {
			return v
		}
	}
	return nil
}

// checkModelClass rejects model classes the platform cannot use.
func checkModelClass(g *pyGlobal) error {
	qual := cleanText(g.qual, maxShortTextLen)
	switch {
	case strings.HasPrefix(g.module, "__torch__"):
		return torchScriptError()
	case g.module == "models" || strings.HasPrefix(g.module, "models."):
		return newError(ReasonYOLOv5Repo, "该模型是用原版 YOLOv5 仓库（github.com/ultralytics/yolov5，或 YOLOv7 等基于它的仓库）训练的"+
			"（模型类为 "+qual+"），平台暂不支持。"+supportedModels+"，可以用 Ultralytics 重新训练（如 YOLO11）后上传")
	case g.module == "super_gradients" || strings.HasPrefix(g.module, "super_gradients."):
		return newError(ReasonUnsupportedModel, "YOLO-NAS（super-gradients）模型暂不支持（模型类为 "+qual+"）。"+supportedModels)
	case g.name == "RTDETRDetectionModel":
		return newError(ReasonUnsupportedModel, "RT-DETR 模型暂不支持（模型类为 "+qual+"）。"+supportedModels)
	case g.module == "ultralytics" || strings.HasPrefix(g.module, "ultralytics."):
		if strings.HasSuffix(g.name, "Model") {
			return nil
		}
		return newError(ReasonUnsupportedModel, "不支持的 Ultralytics 模型类型（"+qual+"）。"+supportedModels)
	}
	return newError(ReasonNotUltralytics, "文件中的模型（"+qual+"）不是 Ultralytics 的模型。"+supportedModels)
}

// notCheckpoint explains a file whose top-level object is not a dict.
func notCheckpoint(root any) error {
	if g := className(root); g != nil {
		if strings.HasPrefix(g.module, "__torch__") {
			return torchScriptError()
		}
		if err := checkModelClass(g); err != nil && err.(*Error).Reason != ReasonNotUltralytics {
			return err // e.g. a whole YOLOv5 model saved with torch.save(model)
		}
		return newError(ReasonNotUltralytics, "文件中直接保存了一个对象（"+cleanText(g.qual, maxShortTextLen)+
			"），不是 Ultralytics 的 checkpoint（包含 model、train_args 等的字典）。"+uploadHint)
	}
	return newError(ReasonNotUltralytics, "文件中保存的是 "+typeName(root)+"，不是 Ultralytics 的 checkpoint（包含 model、train_args 等的字典）。"+uploadHint)
}

// noModel explains a dict without "model" and "ema".
func noModel(ckpt *pyDict) error {
	for i, k := range ckpt.keys {
		if _, ok := ckpt.vals[i].(*pyTensor); ok {
			return stateDictError()
		}
		if s, ok := k.(string); ok && (s == "state_dict" || s == "model_state_dict" || s == "net") {
			return stateDictError()
		}
	}
	return newError(ReasonNotUltralytics, "文件中没有模型（缺少 model / ema），不是 Ultralytics 训练保存的 checkpoint。"+uploadHint)
}

func stateDictError() error {
	return newError(ReasonNotUltralytics, "文件只包含模型权重（state_dict），没有模型结构，平台无法使用。"+
		"请上传 Ultralytics 训练得到的完整 .pt 文件（如 best.pt），不要只保存 model.state_dict()")
}

func torchScriptError() error {
	return newError(ReasonNotUltralytics, "文件是 TorchScript 模型（torch.jit.save 或导出为 torchscript 格式得到），"+
		"不是 Ultralytics 的 .pt checkpoint。请上传训练得到的 best.pt 或 last.pt")
}

// headModule returns the last layer of model.model (an nn.Sequential).
func headModule(model *pyObject) *pyObject {
	seq, _ := lookupDict(model, "_modules").get("model")
	layers := lookupDict(seq, "_modules")
	if layers.len() == 0 {
		return nil
	}
	h, _ := layers.vals[len(layers.vals)-1].(*pyObject)
	return h
}

// layerClasses returns the class names of the layers of model.model, for
// models whose yaml lists no modules.
func layerClasses(model *pyObject) map[string]bool {
	seq, _ := lookupDict(model, "_modules").get("model")
	names := map[string]bool{}
	for _, v := range lookupDict(seq, "_modules").values() {
		if g := className(v); g != nil && len(g.name) <= maxShortTextLen {
			names[g.name] = true
		}
	}
	return names
}

// yamlModules returns the module names of the layers in the model yaml
// ([from, repeats, module, args] in "backbone" and "head"; "nn.Upsample"
// counts as "Upsample") and the last of them.
func yamlModules(yaml *pyDict) (map[string]bool, string) {
	names := map[string]bool{}
	last := ""
	for _, section := range []string{"backbone", "head"} {
		layers, _ := sequence(lookupValue(yaml, section))
		for _, layer := range layers {
			l, ok := sequence(layer)
			if !ok || len(l) < 3 {
				continue
			}
			if s, ok := l[2].(string); ok {
				if len(s) > maxShortTextLen { // not a module name
					last = ""
					continue
				}
				s = s[strings.LastIndexByte(s, '.')+1:]
				names[s] = true
				last = cleanText(s, maxShortTextLen)
			}
		}
	}
	return names, last
}

// countParameters adds up the elements of the nn.Parameter tensors of the
// module tree, each parameter and module once (like sum(p.numel() for p in
// model.parameters())). It returns 0 when a shape is unknown.
func countParameters(model *pyObject) int64 {
	seenModules := map[*pyObject]bool{}
	seenParams := map[*pyTensor]bool{}
	var total int64
	var walk func(m *pyObject, depth int) bool
	walk = func(m *pyObject, depth int) bool {
		if depth > maxModuleDepth {
			return false
		}
		if seenModules[m] {
			return true
		}
		seenModules[m] = true
		for _, v := range lookupDict(m, "_parameters").values() {
			switch p := v.(type) {
			case nil: // a parameter registered as None (e.g. bias=False)
			case *pyTensor:
				if seenParams[p] {
					continue
				}
				seenParams[p] = true
				n := p.numel()
				if n < 0 || total > math.MaxInt64-n {
					return false
				}
				total += n
			default:
				return false
			}
		}
		for _, v := range lookupDict(m, "_modules").values() {
			switch c := v.(type) {
			case nil:
			case *pyObject:
				if !walk(c, depth+1) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !walk(model, 0) {
		return 0
	}
	return total
}

// classNames reads model.names: a dict {id: name} (sorted by id, gaps
// filled with "class<N>") or a list.
func classNames(v any) ([]string, bool) {
	if d, ok := v.(*pyDict); ok {
		// Different keys can give the same id (1, "1", "01", ... a crafted
		// file can have a million of them): the last one wins, and each
		// class is labeled once.
		byID := map[int]any{}
		maxID := -1
		for i, k := range d.keys {
			id, ok := classID(k)
			if !ok {
				continue
			}
			byID[id] = d.vals[i]
			maxID = max(maxID, id)
		}
		if maxID < 0 {
			return nil, false
		}
		names := make([]string, maxID+1)
		for i := range names {
			names[i] = classLabel(byID[i], i) // "class<N>" when missing
		}
		return names, true
	}
	items, ok := sequence(v)
	if !ok || len(items) == 0 {
		return nil, false
	}
	items = items[:min(len(items), maxClasses)]
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = classLabel(item, i)
	}
	return names, true
}

// classID accepts the keys of model.names: ints, or digit strings (names
// loaded from JSON).
func classID(k any) (int, bool) {
	var n int64
	switch x := k.(type) {
	case int64:
		n = x
	case string:
		if len(x) > 20 { // ParseInt copies the text into its error
			return 0, false
		}
		v, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, false
		}
		n = v
	default:
		return 0, false
	}
	if n < 0 || n >= maxClasses {
		return 0, false
	}
	return int(n), true
}

// classLabel returns the name of class id for display.
func classLabel(v any, id int) string {
	switch x := v.(type) {
	case string:
		return cleanText(x, maxClassNameLen)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return "class" + strconv.Itoa(id)
}

// imageSize reads imgsz: an int, or [h, w] (the larger side).
func imageSize(v any) int {
	valid := func(n int64) bool { return n > 0 && n <= 1<<16 }
	if n, ok := asInt(v); ok && valid(n) {
		return int(n)
	}
	items, _ := sequence(v)
	size := int64(0)
	for _, item := range items {
		if n, ok := asInt(item); ok && valid(n) {
			size = max(size, n)
		}
	}
	return int(size)
}

// validTask accepts task names such as detect or semantic.
func validTask(s string) bool {
	if s == "" || len(s) > 32 || !isLower(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isLower(s[i]) && s[i] != '_' && (s[i] < '0' || s[i] > '9') {
			return false
		}
	}
	return true
}

// taskFromHead guesses the task from the head class, as Ultralytics'
// guess_model_task does.
func taskFromHead(head string) string {
	h := strings.ToLower(head)
	switch {
	case h == "":
		return ""
	case h == "classify" || h == "classifier":
		return "classify"
	case strings.Contains(h, "detect"):
		return "detect"
	case strings.Contains(h, "semanticsegment"):
		return "semantic"
	case strings.Contains(h, "segment"):
		return "segment"
	case strings.Contains(h, "pose"):
		return "pose"
	case strings.Contains(h, "obb"):
		return "obb"
	case strings.Contains(h, "depth"):
		return "depth"
	}
	return ""
}

// cleanText makes a string from the file safe to store and show: valid
// UTF-8, control characters replaced, at most maxBytes long. It only
// looks at the first maxBytes (plus a character) of s, and returns a
// copy, which never keeps a larger string alive.
func cleanText(s string, maxBytes int) string {
	if len(s) > maxBytes+utf8.UTFMax {
		s = truncate(s, maxBytes+utf8.UTFMax)
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return '�'
			}
			return r
		}, s)
	}
	// A replacement character can be longer than what it replaces.
	return strings.Clone(truncate(s, maxBytes))
}

// truncate cuts s to at most n bytes, at the start of a character when
// one is near (s may not be valid UTF-8).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := n; i >= 0 && i > n-utf8.UTFMax; i-- {
		if i == 0 || utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return s[:n]
}
