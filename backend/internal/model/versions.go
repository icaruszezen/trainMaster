package model

import (
	"slices"
	"strings"

	"github.com/icaruszezen/trainmaster/backend/internal/model/checkpoint"
)

// YOLOVersion is a YOLO generation trained with the Ultralytics package.
// The frontend (frontend/src/features/models/yolo-versions.ts) has the
// same table.
type YOLOVersion struct {
	Value string // stored in models.yolo_version
	Label string // shown to users
	// End2End: the detection head includes an optional end-to-end (one-to-one,
	// NMS-free) branch alongside the default one-to-many (NMS) branch.
	End2End bool
}

// YOLOVersions are the supported versions, oldest first. Adding one needs
// no migration (the column is validated here).
var YOLOVersions = []YOLOVersion{
	{Value: "yolov8", Label: "YOLOv8"},
	{Value: "yolov9", Label: "YOLOv9"},
	{Value: "yolov10", Label: "YOLOv10", End2End: true},
	{Value: "yolo11", Label: "YOLO11"},
	{Value: "yolo12", Label: "YOLO12"},
	{Value: "yolo26", Label: "YOLO26", End2End: true},
}

// LookupYOLOVersion returns the version with the given value.
func LookupYOLOVersion(value string) (YOLOVersion, bool) {
	i := slices.IndexFunc(YOLOVersions, func(v YOLOVersion) bool { return v.Value == value })
	if i < 0 {
		return YOLOVersion{}, false
	}
	return YOLOVersions[i], true
}

// versionLabel is the display name of a version value; unknown values are
// shown as they are.
func versionLabel(value string) string {
	if v, ok := LookupYOLOVersion(value); ok {
		return v.Label
	}
	return value
}

// normalizeYOLOVersion validates a chosen version.
func normalizeYOLOVersion(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", &Error{Kind: ErrInvalid, Message: "请选择 YOLO 版本"}
	}
	if _, ok := LookupYOLOVersion(v); !ok {
		labels := make([]string, len(YOLOVersions))
		for i, y := range YOLOVersions {
			labels[i] = y.Value
		}
		return "", newError(ErrInvalid, "不支持的 YOLO 版本：%s（可选：%s）", truncateRunes(v, 32), strings.Join(labels, "、"))
	}
	return v, nil
}

// checkYOLOVersion compares the chosen version with the one recognized in
// the file. A file whose version could not be recognized is accepted as
// whatever was chosen.
func checkYOLOVersion(chosen string, info *checkpoint.Info) error {
	detected := info.YOLOVersion
	if detected == "" || detected == chosen ||
		(info.VersionSource != "yaml_file" && info.VersionSource != "architecture") {
		return nil
	}
	var how string
	switch {
	case info.VersionSource == "yaml_file" && info.YAMLFile != "":
		how = "（" + truncateRunes(info.YAMLFile, 64) + "）"
	case info.VersionSource == "architecture" && info.YAMLFile != "":
		how = "（按网络结构识别，" + truncateRunes(info.YAMLFile, 64) + "）"
	case info.VersionSource == "architecture":
		how = "（按网络结构识别）"
	}
	label := versionLabel(detected)
	return &Error{Kind: ErrVersionMismatch, detected: detected, Message: "所选的版本是 " + versionLabel(chosen) +
		"，但文件中的模型是 " + label + how + "。请改选 " + label + " 后再保存"}
}

// Tasks whose models predict boxes; only they have an end-to-end head or
// can embed NMS.
var nmsTasks = []string{"detect", "segment", "pose", "obb"}

func hasBoxes(task string) bool { return slices.Contains(nmsTasks, task) }

const taskClassify = "classify"

// taskLabels name the tasks in messages.
var taskLabels = map[string]string{
	"detect":   "检测",
	"segment":  "分割",
	"classify": "分类",
	"pose":     "姿态",
	"obb":      "旋转框",
	"semantic": "语义分割",
	"depth":    "深度估计",
}

func taskLabel(task string) string {
	if l, ok := taskLabels[task]; ok {
		return l
	}
	if task == "" {
		return "未知任务的"
	}
	return task
}

// end2endCapable reports whether a box model (hasBoxes) has an end-to-end
// (one-to-one, NMS-free) branch. When the checkpoint inspection observed the
// Detect head's modules (info.One2One != nil), that result is authoritative
// (so a fused one-to-many-only YOLO26 checkpoint reports false); otherwise we
// fall back to the YOLO version table and info.End2End.
func end2endCapable(yoloVersion string, info Info) bool {
	if info.One2One != nil {
		return *info.One2One
	}
	v, _ := LookupYOLOVersion(yoloVersion)
	return v.End2End || info.End2End
}

// one2manyCapable reports whether a box model still has its one-to-many (NMS)
// detection branch. Unfused checkpoints and legacy records without branch info
// default to true; only checkpoints fused to one-to-one only have false.
func one2manyCapable(info Info) bool {
	return info.One2Many == nil || *info.One2Many
}

// prefersEnd2End reports whether a box model should default to end2end export:
// true only when it has an end-to-end head and either its one-to-many branch
// was removed by fusion, or it was explicitly trained with nms=false.
// Standard Ultralytics 8.4+ YOLO26/YOLOv10 checkpoints trained with default
// nms=None (or nms=True) default to raw one-to-many output (external NMS).
func prefersEnd2End(yoloVersion string, info Info) bool {
	if !end2endCapable(yoloVersion, info) {
		return false
	}
	if !one2manyCapable(info) {
		return true
	}
	return info.TrainNMS != nil && !*info.TrainNMS
}
