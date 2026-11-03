package checkpoint

import (
	"strings"
)

// YOLO versions: the values of Info.YOLOVersion (and models.yolo_version).
const (
	versionV8  = "yolov8"
	versionV9  = "yolov9"
	versionV10 = "yolov10"
	version11  = "yolo11"
	version12  = "yolo12"
	version26  = "yolo26"
)

// versionByNumber maps the number in a model file name to the version.
var versionByNumber = map[string]string{
	"8": versionV8, "9": versionV9, "10": versionV10, "11": version11, "12": version12, "26": version26,
}

// baseName returns the file name of a path saved on any system.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// versionFromName recognizes the YOLO version and the scale from the name
// of an Ultralytics model file: yolov8s.yaml, yolo11x-seg.yaml,
// yoloe-11s-seg.yaml, yoloe-v8s.yaml, yolov8s-worldv2.yaml, yolov10b.yaml,
// yolo26m-obb.pt, ... ("yolo" or "yolov", or "yoloe-" / "yoloe-v", then
// the version number and an optional one-letter scale). Other names
// (custom yaml files, yolov5su.yaml, rtdetr-l.yaml) give "".
func versionFromName(name string) (version, scale string) {
	base := strings.ToLower(baseName(name))
	for _, ext := range []string{".yaml", ".yml", ".pt", ".pth"} {
		if strings.HasSuffix(base, ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	rest, ok := strings.CutPrefix(base, "yolo")
	if !ok {
		return "", ""
	}
	rest = strings.TrimPrefix(rest, "e-")
	rest = strings.TrimPrefix(rest, "v")
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	version = versionByNumber[rest[:n]]
	if version == "" {
		return "", ""
	}
	rest = rest[n:]
	if len(rest) > 0 && isLower(rest[0]) && (len(rest) == 1 || !isLower(rest[1])) {
		scale = rest[:1]
	}
	return version, scale
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

// detectHeads are the heads of the YOLOv8 family (detection and the tasks
// built on it, YOLO-World, YOLOE, classification).
var detectHeads = map[string]bool{
	"Detect": true, "Segment": true, "Pose": true, "OBB": true, "Classify": true,
	"WorldDetect": true, "YOLOEDetect": true, "YOLOESegment": true,
}

// versionFromArchitecture infers the YOLO version from the modules of the
// model yaml (backbone and head) and the head, for models whose yaml has a
// custom name. The rules use the building blocks each version introduced:
//
//   - A2C2f (area attention): YOLO12;
//   - a head of YOLO26 (Segment26, Pose26, OBB26, YOLOESegment26, and
//     SemanticSegment and Depth, which only YOLO26 has), or an end-to-end
//     head with C3k2: YOLO26;
//   - v10Detect, SCDown, PSA or C2fCIB: YOLOv10;
//   - RepNCSPELAN4, ADown or SPPELAN: YOLOv9;
//   - C3k2 (without an end-to-end head): YOLO11;
//   - C2f (or C3Ghost, of yolov8-ghost) with a head of the Detect family:
//     YOLOv8.
//
// Anything else (YOLOv3/v5u/v6, RT-DETR, ResNet or TorchVision backbones,
// ...) gives "". YOLO26 classification
// models have the very same architecture as YOLO11 ones, so they are
// recognized as YOLO11 unless the yaml name says otherwise.
func versionFromArchitecture(modules map[string]bool, head string, end2end bool) string {
	switch {
	case modules["A2C2f"]:
		return version12
	case strings.HasSuffix(head, "26") || head == "SemanticSegment" || head == "Depth" || (end2end && modules["C3k2"]):
		return version26
	case modules["v10Detect"] || modules["SCDown"] || modules["PSA"] || modules["C2fCIB"]:
		return versionV10
	case modules["RepNCSPELAN4"] || modules["ADown"] || modules["SPPELAN"]:
		return versionV9
	case modules["C3k2"]:
		return version11
	case (modules["C2f"] || modules["C3Ghost"]) && detectHeads[head]:
		return versionV8
	}
	return ""
}

// validScale accepts the scale names Ultralytics uses (n, s, m, l, x, b,
// t, c, e ...).
func validScale(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isLower(s[i]) && (s[i] < '0' || s[i] > '9') {
			return false
		}
	}
	return true
}
