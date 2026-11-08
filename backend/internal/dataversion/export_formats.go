package dataversion

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// The three download formats of a version (see export.go).

// ---- YOLO (Ultralytics) ----

// writeYOLO writes images/<split>/<path>, the labels Ultralytics looks for
// next to them (labels/<split>/<path>.txt; an empty file for a negative
// sample), classes.txt and data.yaml.
func (e *Export) writeYOLO(ctx context.Context, zw *zip.Writer) error {
	names := e.classNames()
	if err := e.addFile(zw, "classes.txt", []byte(strings.Join(names, "\n")+"\n")); err != nil {
		return err
	}
	if err := e.addFile(zw, "data.yaml", e.yoloDataYAML(names)); err != nil {
		return err
	}
	ids := e.classIDs()
	return e.eachImage(ctx, "", true, func(img exportImage) error {
		name := splitPath("images", img.split, img.path)
		ok, err := e.copyImage(zw, img, name)
		if err != nil || !ok {
			return err
		}
		return e.addFile(zw, dataset.UltralyticsLabelPath(name), yoloLabels(img, ids))
	})
}

// yoloLabels renders the objects of an image as normalized
// "class cx cy w h" lines (boxes clipped to the image).
func yoloLabels(img exportImage, ids map[string]int) []byte {
	W, H := float64(img.width), float64(img.height)
	var b bytes.Buffer
	for _, o := range img.objects {
		id, ok := ids[o.Name]
		if !ok {
			continue
		}
		x1, y1, x2, y2, ok := clipBox(o, img.width, img.height)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%d %.6f %.6f %.6f %.6f\n", id, float64(x1+x2)/2/W, float64(y1+y2)/2/H,
			float64(x2-x1)/W, float64(y2-y1)/H)
	}
	return b.Bytes()
}

// yoloDataYAML renders data.yaml for Ultralytics, in the style of the
// dataset downloads. Without a "path" key the paths are relative to the
// file.
func (e *Export) yoloDataYAML(names []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# trainMaster 导出的 YOLO 数据集：%s。\n", e.title())
	if note := e.noteLine(); note != "" {
		fmt.Fprintf(&b, "# 备注：%s\n", note)
	}
	if !e.includeAugmented && e.version.AugmentedCount > 0 {
		b.WriteString("# 本次下载不含增强图片。\n")
	}
	b.WriteString("# 路径相对于本文件所在的文件夹；类别编号与 classes.txt 一致。\n")
	b.WriteString("train: images/train\nval: images/val\n")
	if e.splits[SplitTest] > 0 {
		b.WriteString("test: images/test\n")
	}
	fmt.Fprintf(&b, "nc: %d\nnames:\n", len(names))
	for i, name := range names {
		// A JSON string is a valid YAML double-quoted scalar.
		q, _ := json.Marshal(name)
		fmt.Fprintf(&b, "  %d: %s\n", i, q)
	}
	return []byte(b.String())
}

// ---- Pascal VOC ----

// isJPEG reports whether a file is a JPEG by its extension.
func isJPEG(name string) bool { return dataset.ImageContentType(name) == "image/jpeg" }

// writeVOC writes JPEGImages/<path> (JPEG files with the extension .jpg:
// most VOC loaders open <id>.jpg), Annotations/<path>.xml,
// ImageSets/Main/{train,val,test,trainval}.txt (test only when it has
// images), classes.txt and, when it helps, README.txt.
func (e *Export) writeVOC(ctx context.Context, zw *zip.Writer) error {
	lists := map[string]*strings.Builder{SplitTrain: {}, SplitVal: {}, SplitTest: {}}
	otherFormats := map[string]int{} // extension -> images that are not JPEG
	difficult := 0
	err := e.eachImage(ctx, "", true, func(img exportImage) error {
		rel := strings.TrimPrefix(img.path, "images/")
		id := strings.TrimSuffix(rel, path.Ext(rel))
		if isJPEG(rel) {
			rel = id + ".jpg" // cannot collide: the stems are unique in a version
		}
		name := "JPEGImages/" + rel
		ok, err := e.copyImage(zw, img, name)
		if err != nil || !ok {
			return err
		}
		if !isJPEG(rel) {
			otherFormats[strings.ToLower(path.Ext(rel))]++
		}
		for _, o := range img.objects {
			if o.Difficult {
				difficult++
			}
		}
		data, err := dataset.EncodeVOC(path.Base(path.Dir(name)), path.Base(name), img.width, img.height, img.objects)
		if err != nil {
			return err
		}
		if err := e.addFile(zw, "Annotations/"+id+".xml", data); err != nil {
			return err
		}
		lists[img.split].WriteString(id + "\n")
		return nil
	})
	if err != nil {
		return err
	}
	for _, split := range Splits {
		if split == SplitTest && lists[split].Len() == 0 {
			continue
		}
		if err := e.addFile(zw, "ImageSets/Main/"+split+".txt", []byte(lists[split].String())); err != nil {
			return err
		}
	}
	trainval := lists[SplitTrain].String() + lists[SplitVal].String()
	if err := e.addFile(zw, "ImageSets/Main/trainval.txt", []byte(trainval)); err != nil {
		return err
	}
	if err := e.addFile(zw, "classes.txt", []byte(strings.Join(e.classNames(), "\n")+"\n")); err != nil {
		return err
	}
	if len(otherFormats) == 0 && difficult == 0 {
		return nil
	}
	return e.addFile(zw, "README.txt", e.vocReadme(otherFormats, difficult))
}

// vocReadme explains what VOC loaders may trip over (Windows line breaks,
// so that Notepad shows them).
func (e *Export) vocReadme(otherFormats map[string]int, difficult int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "trainMaster 导出的 Pascal VOC 数据集：%s。\n\n", e.title())
	b.WriteString("JPEGImages/ 中是图片，Annotations/ 中是同名的 .xml 标注，ImageSets/Main/ 中是划分" +
		"（每行一个图片编号：相对 JPEGImages/ 的路径，不含扩展名），classes.txt 是类别列表。\n")
	if len(otherFormats) > 0 {
		exts := make([]string, 0, len(otherFormats))
		total := 0
		for ext, n := range otherFormats {
			exts = append(exts, ext)
			total += n
		}
		sort.Strings(exts)
		parts := make([]string, len(exts))
		for i, ext := range exts {
			parts[i] = fmt.Sprintf("%s %d 张", ext, otherFormats[ext])
		}
		fmt.Fprintf(&b, "\n注意：有 %d 张图片不是 JPEG 格式（%s），保留了原来的扩展名。"+
			"torchvision、mmdetection、Detectron2 等工具的 VOC 读取代码按“编号.jpg”查找图片，"+
			"使用前请先转换这些图片的格式或修改读取代码；也可以改为下载 YOLO 或 COCO 格式。\n",
			total, strings.Join(parts, "、"))
	}
	fmt.Fprintf(&b, "\n注意：mmdetection 等工具训练时会忽略标为 difficult 的目标（本数据集中有 %d 个）；"+
		"YOLO 与 COCO 格式的下载会照常使用它们。\n", difficult)
	return []byte(strings.ReplaceAll(b.String(), "\n", "\r\n"))
}

// ---- COCO ----

type cocoInfo struct {
	Description string `json:"description"`
	Version     string `json:"version"`
	DateCreated string `json:"date_created"`
}

type cocoImage struct {
	ID       int    `json:"id"`
	FileName string `json:"file_name"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type cocoAnnotation struct {
	ID           int    `json:"id"`
	ImageID      int    `json:"image_id"`
	CategoryID   int    `json:"category_id"`
	BBox         [4]int `json:"bbox"`
	Area         int    `json:"area"`
	IsCrowd      int    `json:"iscrowd"`
	Segmentation []int  `json:"segmentation"`
}

type cocoCategory struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Supercategory string `json:"supercategory"`
}

// writeCOCO writes, per split with images, images/<split>/<path> and
// annotations/instances_<split>.json. Image and annotation ids start at 1
// in each file; a category's id is its class id + 1.
func (e *Export) writeCOCO(ctx context.Context, zw *zip.Writer) error {
	for _, split := range Splits {
		if e.splits[split] == 0 {
			continue
		}
		// The images first; the file then lists those that were written.
		ids := map[int64]int{}
		err := e.eachImage(ctx, split, false, func(img exportImage) error {
			ok, err := e.copyImage(zw, img, splitPath("images", split, img.path))
			if ok {
				ids[img.id] = len(ids) + 1
			}
			return err
		})
		if err != nil {
			return err
		}
		if err := e.writeCOCOFile(ctx, zw, split, ids); err != nil {
			return err
		}
	}
	return nil
}

// writeCOCOFile streams annotations/instances_<split>.json for the written
// images (by row id: their COCO image id): it reads the rows again for the
// images and for the annotations instead of holding them in memory.
func (e *Export) writeCOCOFile(ctx context.Context, zw *zip.Writer, split string, ids map[int64]int) error {
	h, ok := e.header("annotations/instances_"+split+".json", zip.Deflate, e.modTime())
	if !ok {
		return nil
	}
	out, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 64<<10)
	enc := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	description := "trainMaster 数据版本：" + e.title()
	if note := e.noteLine(); note != "" {
		description += "（" + note + "）"
	}
	w.WriteString(`{"info":`)
	if err := enc(cocoInfo{Description: description, Version: fmt.Sprint(e.version.Version),
		DateCreated: e.modTime().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	w.WriteString(`,"licenses":[],"images":[`)
	first := true
	sep := func() {
		if !first {
			w.WriteByte(',')
		}
		first = false
	}
	err = e.eachImage(ctx, split, false, func(img exportImage) error {
		id, ok := ids[img.id]
		if !ok {
			return nil
		}
		sep()
		return enc(cocoImage{ID: id, FileName: strings.TrimPrefix(img.path, "images/"), Width: img.width,
			Height: img.height})
	})
	if err != nil {
		return err
	}
	w.WriteString(`],"annotations":[`)
	first = true
	classIDs := e.classIDs()
	next := 1
	err = e.eachImage(ctx, split, true, func(img exportImage) error {
		imageID, ok := ids[img.id]
		if !ok {
			return nil
		}
		for _, o := range img.objects {
			class, ok := classIDs[o.Name]
			if !ok {
				continue
			}
			x1, y1, x2, y2, ok := clipBox(o, img.width, img.height)
			if !ok {
				continue
			}
			sep()
			if err := enc(cocoAnnotation{ID: next, ImageID: imageID, CategoryID: class + 1,
				BBox: [4]int{x1, y1, x2 - x1, y2 - y1}, Area: (x2 - x1) * (y2 - y1), Segmentation: []int{}}); err != nil {
				return err
			}
			next++
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.WriteString(`],"categories":`)
	categories := make([]cocoCategory, len(e.version.Classes))
	for i, c := range e.version.Classes {
		categories[i] = cocoCategory{ID: i + 1, Name: c.Name}
	}
	if err := enc(categories); err != nil {
		return err
	}
	w.WriteString("}\n")
	return w.Flush()
}
