package reviewer

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// The results (审图结果) are a zip next to the package folder,
// "<folder>-审图结果.zip", with one top folder "<package folder>-审图结果/"
// holding review.json of kind results (every image of the package with
// its status now; the base_* fields as in the package) and the VOC files
// whose content differs from the package, at their paths in the package.
// The platform applies them when the zip is uploaded on the review's page
// (internal/review/results.go). Exporting again replaces the file.

// resultsSuffix ends the names of the results zip and its top folder.
const resultsSuffix = "-审图结果"

// ExportResult describes exported results (POST /api/export).
type ExportResult struct {
	ExportInfo
	Images            int `json:"images"`             // in review.json
	StatusChanges     int `json:"status_changes"`     // images whose status differs from the package
	AnnotationChanges int `json:"annotation_changes"` // VOC files in the zip
	// Skipped counts changed VOC files left out because they cannot be read
	// (the platform would refuse the whole image; its status still counts).
	Skipped       int      `json:"skipped"`
	SkippedPaths  []string `json:"skipped_paths"` // the first few
	Counts        Counts   `json:"counts"`
	MissingImages int      `json:"missing_images"`
	Unchanged     int      `json:"unchanged"` // images with neither kind of change
}

// maxSkippedPaths bounds ExportResult.SkippedPaths.
const maxSkippedPaths = 20

// ResultsPath is where the results of the package are exported to.
func (p *Package) ResultsPath() string {
	return filepath.Join(filepath.Dir(p.dir), filepath.Base(p.dir)+resultsSuffix+".zip")
}

// Export writes the results zip (atomically, replacing an earlier one) and
// returns what it holds.
func (p *Package) Export() (ExportResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	root, err := p.openRoot()
	if err != nil {
		return ExportResult{}, err
	}
	dest := p.ResultsPath()
	exportedAt := p.now().UTC().Truncate(time.Second)

	m := *p.manifest
	m.Kind = reviewfile.KindResults
	m.ExportedAt = exportedAt
	m.Images = make([]reviewfile.ManifestImage, len(p.images))
	res := ExportResult{Images: len(p.images), SkippedPaths: []string{}}
	type vocFile struct {
		rel  string
		data []byte
	}
	var vocs []vocFile
	for i, img := range p.images {
		a := readAnnotationIn(root, img.m.Annotation)
		img.setAnnotation(a)
		mi := *img.m
		mi.Status, mi.ReviewedAt, mi.Edited = img.status, img.reviewedAt, img.edited
		mi.HasAnnotation = false
		changedStatus := img.status != img.m.BaseStatus
		changedVOC := a.version != img.m.BaseAnnotationVersion && a.data != nil
		switch {
		case changedVOC && a.err != "":
			res.Skipped++
			if len(res.SkippedPaths) < maxSkippedPaths {
				res.SkippedPaths = append(res.SkippedPaths, img.m.Annotation)
			}
			changedVOC = false
		case changedVOC:
			vocs = append(vocs, vocFile{rel: img.m.Annotation, data: a.data})
			mi.HasAnnotation = true
			res.AnnotationChanges++
		}
		if changedStatus {
			res.StatusChanges++
		}
		if !changedStatus && !changedVOC {
			res.Unchanged++
		}
		res.Counts.count(img)
		if img.imageVersion == "" {
			res.MissingImages++
		}
		m.Images[i] = mi
	}

	top := reviewfile.SafeSegment(reviewfile.PackageFolder(m.Review.ID, m.Review.Name)+resultsSuffix) + "/"
	err = writeFileAtomic(dest, func(f *os.File) error {
		zw := zip.NewWriter(f)
		dataset.RegisterZipCompressor(zw)
		add := func(name string, data []byte) error {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: top + name, Method: zip.Deflate, Modified: exportedAt})
			if err != nil {
				return err
			}
			_, err = w.Write(data)
			return err
		}
		if err := add(reviewfile.ReadmeFile, resultsReadme(&m, res, exportedAt)); err != nil {
			return err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: top + reviewfile.ManifestFile, Method: zip.Deflate, Modified: exportedAt})
		if err != nil {
			return err
		}
		if err := reviewfile.WriteManifest(w, &m); err != nil {
			return err
		}
		for _, v := range vocs {
			if err := add(v.rel, v.data); err != nil {
				return err
			}
		}
		return zw.Close()
	})
	if err != nil {
		return ExportResult{}, newError(ErrIO, "无法写入审图结果 %s：%v", dest, err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return ExportResult{}, newError(ErrIO, "无法读取导出的审图结果 %s：%v", dest, err)
	}
	res.ExportInfo = ExportInfo{Path: dest, FileName: filepath.Base(dest), SizeBytes: fi.Size(), ExportedAt: exportedAt}
	previous := p.lastExport
	p.lastExport = &res.ExportInfo
	if err := p.writeProgress(root); err != nil {
		// The results are written; only the note of it in the progress
		// file is missing.
		p.lastExport = previous
		return res, nil
	}
	return res, nil
}

// resultsReadme renders README.txt of results (UTF-8 with a byte order mark
// and CRLF line breaks, like the package's).
func resultsReadme(m *reviewfile.Manifest, res ExportResult, exportedAt time.Time) []byte {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\r\n")
	}
	b.WriteString("\ufeff") // byte order mark
	line("trainMaster 审图结果")
	line("====================")
	line("")
	line("审图任务：#%d %s", m.Review.ID, m.Review.Name)
	line("导出时间：%s（UTC）", exportedAt.Format("2006-01-02 15:04:05"))
	line("图片：%d 张（待审核 %d、已审核 %d、需加样 %d）", res.Images, res.Counts.Pending, res.Counts.Approved,
		res.Counts.NeedsSample)
	line("本地修改：%d 张图片的审核状态，%d 个标注文件", res.StatusChanges, res.AnnotationChanges)
	line("")
	line("这个压缩包是本地审图程序导出的审图结果。请回到平台上这个审图任务的页面，点“上传审图结果”，")
	line("选择这个压缩包（不用解压）。平台读取后才会更新图片的审核状态和审图标注。")
	line("")
	line("review.json 记录了每张图片的审核状态；只包含在本地修改过的标注文件。请不要修改压缩包中的内容。")
	return []byte(b.String())
}

// writeFileAtomic writes a file through a temporary file in its folder
// that replaces it once write succeeded, so the file is never half
// written.
func writeFileAtomic(dest string, write func(f *os.File) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tm-*.tmp")
	if err != nil {
		return err
	}
	err = write(tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
		if err != nil && errors.Is(err, os.ErrPermission) {
			err = fmt.Errorf("%w（文件可能正被其他程序打开）", err)
		}
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
