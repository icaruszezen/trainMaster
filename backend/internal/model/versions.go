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
	// End2End: the detection head has an end-to-end (one-to-one, NMS-free)
	// branch, and ONNX exports default to it.
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
// head: YOLOv10 and YOLO26 always do; other files say so themselves.
func end2endCapable(yoloVersion string, info Info) bool {
	v, _ := LookupYOLOVersion(yoloVersion)
	return v.End2End || info.End2End
}
