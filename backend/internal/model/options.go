package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Bounds of the ONNX export options. The frontend
// (frontend/src/features/models/yolo-versions.ts) applies the same rules.
const (
	minImgSize     = 32
	maxImgSize     = 4096
	imgSizeStride  = 32 // the largest stride of the YOLO heads
	defaultImgSize = 640
	minOpset       = 11
	maxOpset       = 21
)

// ExportRequest asks for a conversion; fields left unset take the defaults
// of the model.
type ExportRequest struct {
	Format      string
	ImgSize     []int // nil: the training size; one value: square; two: [height, width]
	Dynamic     *bool
	Postprocess string // "": the default of the model
	Opset       *int   // nil: chosen by Ultralytics
}

// AllowedPostprocess returns the postprocessing modes a model can be
// exported with, its default first. Classification models only have raw
// outputs; models with an end-to-end head default to it (and can still
// embed NMS on their one-to-many head); NMS needs a task with boxes.
func AllowedPostprocess(m Model) []string {
	if m.Task == taskClassify {
		return []string{PostprocessRaw}
	}
	var modes []string
	if end2endCapable(m.YOLOVersion, m.Info) {
		modes = append(modes, PostprocessEnd2End)
	}
	modes = append(modes, PostprocessRaw)
	if slices.Contains(nmsTasks, m.Task) {
		modes = append(modes, PostprocessNMS)
	}
	return modes
}

// NormalizeExport validates a conversion request for m and fills in the
// defaults.
func NormalizeExport(m Model, req ExportRequest) (ExportOptions, error) {
	invalid := func(format string, args ...any) (ExportOptions, error) {
		return ExportOptions{}, newError(ErrInvalid, format, args...)
	}
	if req.Format != FormatONNX {
		return invalid("不支持的转换格式：%s，目前只支持 onnx", truncateRunes(req.Format, 32))
	}
	var o ExportOptions
	switch len(req.ImgSize) {
	case 0:
		size := defaultImgSize
		if m.Info.ImageSize != nil && *m.Info.ImageSize > 0 {
			size = roundImgSize(*m.Info.ImageSize)
		}
		o.ImgSize = [2]int{size, size}
	case 1:
		o.ImgSize = [2]int{req.ImgSize[0], req.ImgSize[0]}
	case 2:
		o.ImgSize = [2]int{req.ImgSize[0], req.ImgSize[1]}
	default:
		return invalid("输入尺寸应为一个整数或 [高, 宽]")
	}
	for _, v := range o.ImgSize {
		if v < minImgSize || v > maxImgSize || v%imgSizeStride != 0 {
			return invalid("输入尺寸必须在 %d–%d 之间且是 %d 的倍数（收到 %d）", minImgSize, maxImgSize, imgSizeStride, v)
		}
	}
	if req.Dynamic != nil {
		o.Dynamic = *req.Dynamic
	}

	allowed := AllowedPostprocess(m)
	switch o.Postprocess = req.Postprocess; o.Postprocess {
	case "":
		o.Postprocess = allowed[0]
	case PostprocessRaw, PostprocessNMS, PostprocessEnd2End:
		if !slices.Contains(allowed, o.Postprocess) {
			return ExportOptions{}, postprocessNotAllowed(m, o.Postprocess)
		}
	default:
		return invalid("后处理方式必须是 raw、nms 或 end2end")
	}

	if req.Opset != nil {
		if *req.Opset < minOpset || *req.Opset > maxOpset {
			return invalid("opset 必须在 %d–%d 之间，或留空自动选择", minOpset, maxOpset)
		}
		v := *req.Opset
		o.Opset = &v
	}
	return o, nil
}

func postprocessNotAllowed(m Model, mode string) error {
	switch {
	case m.Task == taskClassify:
		return &Error{Kind: ErrInvalid, Message: "分类模型只支持原始输出（raw）"}
	case mode == PostprocessNMS:
		return newError(ErrInvalid, "%s模型没有检测框，不能嵌入 NMS，请选择 raw", taskLabel(m.Task))
	}
	return newError(ErrInvalid, "%s 模型没有端到端（NMS-free）检测头，不能导出为 end2end，请选择 raw 或 nms",
		versionLabel(m.YOLOVersion))
}

// roundImgSize turns a training size into a valid export size: the next
// multiple of the stride (Ultralytics rounds the same way), within bounds.
func roundImgSize(size int) int {
	size = (size + imgSizeStride - 1) / imgSizeStride * imgSizeStride
	return min(max(size, minImgSize), maxImgSize)
}

// OptionsKey is the canonical form of normalized options, e.g.
// "imgsz=640x640;dynamic=0;postprocess=raw;opset=auto". Equal options have
// equal keys; nothing parses them.
func OptionsKey(o ExportOptions) string {
	dynamic, opset := 0, "auto"
	if o.Dynamic {
		dynamic = 1
	}
	if o.Opset != nil {
		opset = strconv.Itoa(*o.Opset)
	}
	return fmt.Sprintf("imgsz=%dx%d;dynamic=%d;postprocess=%s;opset=%s", o.ImgSize[0], o.ImgSize[1], dynamic, o.Postprocess, opset)
}

// fileNameUnsafe are the characters a download file name cannot contain on
// Windows (besides control characters).
const fileNameUnsafe = `\/:*?"<>|`

// safeFileStem makes a project name usable in a download file name.
func safeFileStem(name string) string {
	stem := strings.Map(func(r rune) rune {
		if isControlChar(r) || strings.ContainsRune(fileNameUnsafe, r) {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	if stem == "" {
		return "model"
	}
	return stem
}

// ModelFileName is the download name of a model's weights:
// <project>_v<version>.pt.
func ModelFileName(projectName string, version int) string {
	return fmt.Sprintf("%s_v%d.pt", safeFileStem(projectName), version)
}

// ExportFileName is the download name of a conversion:
// <project>_v<version>_<h>x<w>[_dynamic][_nms|_end2end][_opset<N>].onnx,
// with <size> instead of <h>x<w> for square inputs.
func ExportFileName(projectName string, version int, o ExportOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s_v%d_", safeFileStem(projectName), version)
	if o.ImgSize[0] == o.ImgSize[1] {
		b.WriteString(strconv.Itoa(o.ImgSize[0]))
	} else {
		fmt.Fprintf(&b, "%dx%d", o.ImgSize[0], o.ImgSize[1])
	}
	if o.Dynamic {
		b.WriteString("_dynamic")
	}
	if o.Postprocess != PostprocessRaw && o.Postprocess != "" {
		b.WriteString("_" + o.Postprocess)
	}
	if o.Opset != nil {
		fmt.Fprintf(&b, "_opset%d", *o.Opset)
	}
	b.WriteString(".onnx")
	return b.String()
}
