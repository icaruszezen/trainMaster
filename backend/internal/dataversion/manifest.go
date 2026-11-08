package dataversion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// The files at the top of a version folder: version.json (the manifest),
// classes.txt, data.yaml and the image lists of Ultralytics.

const (
	manifestFile = "version.json"
	classesFile  = "classes.txt"
	dataYAMLFile = "data.yaml"
)

// listFile is the image list of a split (train.txt, ...).
func listFile(split string) string { return split + ".txt" }

// manifest is version.json: what a version is, readable without the
// database (the folder alone is a complete training set).
type manifest struct {
	Format        string           `json:"format"`
	FormatVersion int              `json:"format_version"`
	Project       manifestProject  `json:"project"`
	Version       int              `json:"version"`
	Note          string           `json:"note"`
	Classes       []Class          `json:"classes"`
	Config        Config           `json:"config"`
	Sources       []manifestSource `json:"sources"`
	Counts        manifestCounts   `json:"counts"`
	CreatedAt     time.Time        `json:"created_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
}

type manifestProject struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// manifestSource is a Source without what is computed when read.
type manifestSource struct {
	DatasetID   int64           `json:"dataset_id"`
	DatasetName string          `json:"dataset_name"`
	DirName     string          `json:"dir_name"`
	IncludeBase bool            `json:"include_base"`
	Versions    []SourceVersion `json:"versions"`
	ImageCount  int             `json:"image_count"`
}

type manifestCounts struct {
	Images    int `json:"images"`
	Objects   int `json:"objects"`
	Train     int `json:"train"`
	Val       int `json:"val"`
	Test      int `json:"test"`
	Augmented int `json:"augmented"`
}

// newManifest is the manifest of a ready version.
func newManifest(v DataVersion) manifest {
	m := manifest{Format: "trainmaster-data-version", FormatVersion: 1,
		Project: manifestProject{ID: v.ProjectID, Name: v.ProjectName}, Version: v.Version, Note: v.Note,
		Classes: v.Classes, Config: v.Config, Sources: []manifestSource{},
		Counts: manifestCounts{Images: v.ImageCount, Objects: v.ObjectCount, Train: v.TrainCount, Val: v.ValCount,
			Test: v.TestCount, Augmented: v.AugmentedCount},
		CreatedAt: v.CreatedAt.UTC(), FinishedAt: utcPtr(v.FinishedAt)}
	for _, src := range v.Sources {
		versions := src.Versions
		if versions == nil {
			versions = []SourceVersion{}
		}
		m.Sources = append(m.Sources, manifestSource{DatasetID: src.DatasetID, DatasetName: src.DatasetName,
			DirName: src.DirName, IncludeBase: src.IncludeBase, Versions: versions, ImageCount: src.ImageCount})
	}
	return m
}

// writeManifest writes version.json into the folder dir atomically (a
// temporary file renamed over it).
func writeManifest(dir string, m manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", manifestFile, err)
	}
	return writeFileAtomic(filepath.Join(dir, manifestFile), append(data, '\n'))
}

// writeFileAtomic writes data to a temporary file next to name and renames
// it over name.
func writeFileAtomic(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(name), err)
	}
	tmp := f.Name()
	err = f.Chmod(0o644) // CreateTemp makes it 0600, like copyFileFrom
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", filepath.Base(name), err)
	}
	return nil
}

// classesText is classes.txt: one class name per line, in YOLO id order.
func classesText(classes []Class) []byte {
	var b strings.Builder
	for _, c := range classes {
		b.WriteString(c.Name)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// commentSafe makes text safe for a YAML comment: a line break (YAML also
// breaks lines at U+2028 and U+2029) must not start a key.
func commentSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
}

// dataYAML is data.yaml for Ultralytics. Without a "path" key the lists
// are relative to the file, and their "./" lines relative to the lists.
func dataYAML(projectName string, version int, classes []Class, hasTest bool) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# trainMaster 数据版本：%s · 数据版本 v%d\n", commentSafe(projectName), version)
	b.WriteString("# 路径相对于本文件所在的文件夹；类别编号与 classes.txt 一致。\n")
	b.WriteString("train: train.txt\nval: val.txt\n")
	if hasTest {
		b.WriteString("test: test.txt\n")
	}
	fmt.Fprintf(&b, "nc: %d\nnames:\n", len(classes))
	for i, c := range classes {
		// A JSON string is a valid YAML double-quoted scalar.
		q, _ := json.Marshal(c.Name)
		fmt.Fprintf(&b, "  %d: %s\n", i, q)
	}
	return []byte(b.String())
}
