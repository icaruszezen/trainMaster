package reviewfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
)

// review.json describes a review package (downloaded from the platform)
// and the results the local reviewer exports from it: the review, the
// model, the candidate classes, the datasets and every image with its
// files, the model's predictions and its review status. The JSON field
// names are a file format: packages and results of one version must stay
// readable by the platform and the local reviewer of that version.

// Manifest format identifiers.
const (
	ManifestFormat  = "trainmaster-review"
	ManifestVersion = 1

	KindPackage = "package" // downloaded from the platform
	KindResults = "results" // exported by the local reviewer
)

// ModelRef describes the model of a review as it was when the review was
// created (the model may be deleted later).
type ModelRef struct {
	ID          int64    `json:"id"`
	ProjectID   int64    `json:"project_id"`
	ProjectName string   `json:"project_name"`
	Version     int      `json:"version"`
	YOLOVersion string   `json:"yolo_version"`
	Task        string   `json:"task"`
	FileName    string   `json:"file_name"` // original file name, e.g. best.pt
	ClassNames  []string `json:"class_names"`
}

// Options are the inference options of a review.
type Options struct {
	Conf    float64 `json:"conf"`    // confidence threshold, 0.01–1
	IoU     float64 `json:"iou"`     // NMS IoU threshold, 0.1–0.95
	ImgSize [2]int  `json:"imgsz"`   // [height, width]
	MaxDet  int     `json:"max_det"` // boxes per image, 1–1000
}

// Class is a candidate class when editing boxes.
type Class struct {
	Name  string `json:"name"`
	Color string `json:"color"` // "#rrggbb", or "" for the color derived from the name
}

// Manifest is review.json.
type Manifest struct {
	Format     string            `json:"format"`      // ManifestFormat
	Version    int               `json:"version"`     // ManifestVersion
	Kind       string            `json:"kind"`        // KindPackage or KindResults
	PackageID  string            `json:"package_id"`  // random per download; the results keep it
	Platform   string            `json:"platform"`    // e.g. "trainMaster 0.1.0-dev (d62132f)"
	ExportedAt time.Time         `json:"exported_at"` // when the package (or the results) were written
	Review     ManifestReview    `json:"review"`
	Model      ModelRef          `json:"model"`
	Options    Options           `json:"options"`
	Classes    []Class           `json:"classes"`
	Datasets   []ManifestDataset `json:"datasets"`
	Images     []ManifestImage   `json:"images"` // in review order
}

// ManifestReview names the review a package belongs to.
type ManifestReview struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// ManifestDataset is a dataset of the review and its folder in the
// package.
type ManifestDataset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Folder string `json:"folder"`
}

// ManifestImage is an image of a package: where its files are, what the
// model predicted, and its review state when the package was downloaded
// (Base*) and now (Status, ReviewedAt, Edited; the local reviewer updates
// them).
type ManifestImage struct {
	ID                    int64        `json:"id"` // the review image
	DatasetID             int64        `json:"dataset_id"`
	ImageID               int64        `json:"image_id"`    // the dataset image
	Path                  string       `json:"path"`        // the image file in the package
	SourcePath            string       `json:"source_path"` // the image in the dataset folder
	Annotation            string       `json:"annotation"`  // the VOC file in the package
	HasAnnotation         bool         `json:"has_annotation"`
	Width                 *int         `json:"width"`
	Height                *int         `json:"height"`
	Predictions           []Prediction `json:"predictions"`
	InferenceError        string       `json:"inference_error"`
	BaseStatus            string       `json:"base_status"`
	BaseRevision          int          `json:"base_revision"`
	BaseAnnotationVersion string       `json:"base_annotation_version"` // of the packaged VOC file ("" none)
	Status                string       `json:"status"`
	ReviewedAt            *time.Time   `json:"reviewed_at"`
	Edited                bool         `json:"edited"`
}

// ManifestError explains why a review.json cannot be used. Message is in
// Chinese and shown to users.
type ManifestError struct {
	Message string
}

func (e *ManifestError) Error() string { return e.Message }

func manifestError(format string, args ...any) error {
	return &ManifestError{Message: "review.json 无效：" + fmt.Sprintf(format, args...)}
}

// errNotReview is returned for JSON that is not a review manifest at all.
var errNotReview = &ManifestError{Message: "这不是 trainMaster 的审图包或审图结果（review.json 的 format 不是 " + ManifestFormat + "）"}

// ReadManifest reads and validates review.json from r, which may hold at
// most limit bytes. Unknown fields are ignored (newer writers may add
// some). The checks: the format and a version this code understands, the
// kind, positive and unique IDs, every image in one of the datasets, valid
// statuses, and image and VOC paths that are valid (ValidRelPath), inside
// the folder of their dataset and unique ignoring case. A problem is a
// *ManifestError.
func ReadManifest(r io.Reader, limit int64) (*Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &ManifestError{Message: fmt.Sprintf("review.json 过大（超过 %d MiB）", limit>>20)}
	}
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, &ManifestError{Message: "review.json 不是有效的 JSON：" + err.Error()}
	}
	switch {
	case head.Format != ManifestFormat:
		return nil, errNotReview
	case head.Version > ManifestVersion:
		return nil, &ManifestError{Message: fmt.Sprintf("审图包的格式版本为 %d，比这个程序支持的版本（%d）新，请升级平台或本地审图程序",
			head.Version, ManifestVersion)}
	case head.Version < 1:
		return nil, manifestError("格式版本 %d 无效", head.Version)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, &ManifestError{Message: "review.json 的内容无效：" + err.Error()}
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validate checks a decoded manifest (see ReadManifest).
func (m *Manifest) validate() error {
	if m.Kind != KindPackage && m.Kind != KindResults {
		return manifestError("kind 必须是 %s 或 %s", KindPackage, KindResults)
	}
	if m.Review.ID <= 0 {
		return manifestError("缺少审图任务 ID")
	}
	folders := map[int64]string{}
	folderKeys := map[string]bool{}
	for i, d := range m.Datasets {
		switch {
		case d.ID <= 0:
			return manifestError("第 %d 个数据集的 ID 无效", i+1)
		case folders[d.ID] != "":
			return manifestError("数据集 %d 出现了多次", d.ID)
		case strings.Contains(d.Folder, "/") || !ValidRelPath(d.Folder) || reservedTop[FoldKey(d.Folder)]:
			return manifestError("数据集 %d 的文件夹名称无效：%q", d.ID, d.Folder)
		case folderKeys[FoldKey(d.Folder)]:
			return manifestError("数据集文件夹 %q 出现了多次", d.Folder)
		}
		folders[d.ID] = d.Folder
		folderKeys[FoldKey(d.Folder)] = true
	}
	ids := make(map[int64]bool, len(m.Images))
	paths := make(map[string]bool, 2*len(m.Images))
	for i, img := range m.Images {
		n := i + 1
		folder, ok := folders[img.DatasetID]
		switch {
		case img.ID <= 0 || img.ImageID <= 0:
			return manifestError("第 %d 张图片的 ID 无效", n)
		case ids[img.ID]:
			return manifestError("图片 %d 出现了多次", img.ID)
		case !ok:
			return manifestError("第 %d 张图片所属的数据集 %d 不在 datasets 中", n, img.DatasetID)
		case !ValidStatus(img.Status) || !ValidStatus(img.BaseStatus):
			return manifestError("第 %d 张图片（%s）的审核状态无效", n, img.Path)
		case img.BaseRevision < 0:
			return manifestError("第 %d 张图片（%s）的 base_revision 无效", n, img.Path)
		case (img.Width != nil && *img.Width <= 0) || (img.Height != nil && *img.Height <= 0):
			return manifestError("第 %d 张图片（%s）的尺寸无效", n, img.Path)
		case img.SourcePath == "":
			return manifestError("第 %d 张图片缺少 source_path", n)
		}
		for _, p := range []string{img.Path, img.Annotation} {
			key := FoldKey(p)
			switch {
			case !ValidRelPath(p) || !strings.HasPrefix(key, FoldKey(folder)+"/"):
				return manifestError("第 %d 张图片的路径无效：%q", n, p)
			case paths[key]:
				return manifestError("路径 %q 出现了多次", p)
			}
			paths[key] = true
		}
		ids[img.ID] = true
	}
	return nil
}

// WriteManifest writes m as review.json: indented, except that every image
// takes one line (packages can have a few hundred thousand images), and
// streamed to w. Characters such as < and & are not escaped; nil lists are
// written as [].
func WriteManifest(w io.Writer, m *Manifest) error {
	mw, err := NewManifestWriter(w, m)
	if err != nil {
		return err
	}
	for i := range m.Images {
		if err := mw.Add(&m.Images[i]); err != nil {
			return err
		}
	}
	return mw.Close()
}

// ManifestWriter writes review.json as WriteManifest does, one image at a
// time, so that a package of many images can be written without holding
// them all in memory: NewManifestWriter writes everything before the
// images (m.Images is ignored), Add appends an image and Close ends the
// file. Nothing is written to w after an error.
type ManifestWriter struct {
	bw   *bufio.Writer
	buf  bytes.Buffer
	line *json.Encoder // encodes into buf
	n    int           // images written
	err  error
}

// NewManifestWriter starts review.json for m on w.
func NewManifestWriter(w io.Writer, m *Manifest) (*ManifestWriter, error) {
	head := *m
	head.Images = nil
	head.Classes = nonNil(head.Classes)
	head.Datasets = nonNil(head.Datasets)
	head.Model.ClassNames = nonNil(head.Model.ClassNames)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&head); err != nil {
		return nil, fmt.Errorf("encode %s: %w", ManifestFile, err)
	}
	const tail = "\"images\": null\n}\n"
	if !bytes.HasSuffix(b.Bytes(), []byte(tail)) {
		return nil, errors.New("encode " + ManifestFile + ": unexpected layout")
	}
	b.Truncate(b.Len() - len(tail))
	b.WriteString("\"images\": [")

	mw := &ManifestWriter{bw: bufio.NewWriterSize(w, 64<<10)}
	mw.line = json.NewEncoder(&mw.buf)
	mw.line.SetEscapeHTML(false)
	if _, err := mw.bw.Write(b.Bytes()); err != nil {
		return nil, err
	}
	return mw, nil
}

// Add appends an image (nil predictions are written as []).
func (mw *ManifestWriter) Add(img *ManifestImage) error {
	if mw.err != nil {
		return mw.err
	}
	c := *img
	c.Predictions = nonNil(c.Predictions)
	mw.buf.Reset()
	if mw.n > 0 {
		mw.buf.WriteByte(',')
	}
	mw.buf.WriteString("\n    ")
	if err := mw.line.Encode(&c); err != nil {
		mw.err = fmt.Errorf("encode %s: %w", ManifestFile, err)
		return mw.err
	}
	mw.buf.Truncate(mw.buf.Len() - 1) // Encode ends with a newline
	if _, err := mw.bw.Write(mw.buf.Bytes()); err != nil {
		mw.err = err
		return err
	}
	mw.n++
	return nil
}

// Close ends review.json and flushes it to w (which it does not close).
func (mw *ManifestWriter) Close() error {
	if mw.err != nil {
		return mw.err
	}
	end := "]\n}\n"
	if mw.n > 0 {
		end = "\n  " + end
	}
	if _, err := mw.bw.WriteString(end); err != nil {
		mw.err = err
		return err
	}
	mw.err = mw.bw.Flush()
	if mw.err == nil {
		mw.err = errors.New("reviewfile: manifest writer closed")
		return nil
	}
	return mw.err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// maxManifestDepth bounds how deep in folders ManifestPrefix looks for
// review.json.
const maxManifestDepth = 3

// ManifestPrefix finds review.json among the slash-separated entry names
// of a zip archive and returns the folder it is in ("" at the top, else
// ending with "/"); every path of the manifest is relative to that folder.
// A package or results archive has it in its top folder; an archive made
// by zipping such a folder again (or the folder it was extracted to) has
// it one or two folders deeper. The shallowest review.json at most three
// folders deep counts; macOS metadata (__MACOSX/) is ignored. None, or two
// equally shallow ones, is a *ManifestError.
func ManifestPrefix(names []string) (string, error) {
	var found []string
	depth := maxManifestDepth + 1
	for _, name := range names {
		if path.Base(name) != ManifestFile || strings.HasPrefix(name, "__MACOSX/") || strings.HasSuffix(name, "/") {
			continue
		}
		d := strings.Count(name, "/")
		switch {
		case d > maxManifestDepth:
		case d < depth:
			depth, found = d, []string{name}
		case d == depth:
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return "", &ManifestError{Message: "压缩包中没有 review.json，这不是 trainMaster 的审图包或审图结果"}
	case 1:
		return strings.TrimSuffix(found[0], ManifestFile), nil
	}
	slices.Sort(found)
	return "", &ManifestError{Message: fmt.Sprintf("压缩包中有多个 review.json（%s 和 %s），请一次只上传一个审图包或审图结果",
		found[0], found[1])}
}
