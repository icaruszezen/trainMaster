package reviewer

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/reviewfile"
)

// A small file browser for the open dialog (GET /api/browse): the places
// to start from (the home folders 下载 / 桌面 / 文档 and the drives on
// Windows, the home folder elsewhere), and in a folder its sub-folders
// (those holding review.json are packages) and its .zip files. Hidden
// files and the program's own temporary folders are left out.

// Entry kinds.
const (
	kindFolder    = "folder"
	kindPackage   = "package" // a folder with review.json
	kindZip       = "zip"
	kindResults   = "results" // a zip of exported results (to upload, not to open)
	kindDrive     = "drive"
	kindHome      = "home"
	kindDownloads = "downloads"
	kindDesktop   = "desktop"
	kindDocuments = "documents"
	kindRoot      = "root"
)

const (
	// maxBrowseEntries bounds the entries of a folder listed.
	maxBrowseEntries = 2000
	// maxPackageChecks bounds the sub-folders checked for review.json.
	maxPackageChecks = 500
)

// Place is a place to start browsing from.
type Place struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"` // home, downloads, desktop, documents, drive or root
}

// BrowseEntry is a folder or zip file in a folder.
type BrowseEntry struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Kind       string     `json:"kind"` // folder, package, zip, results, or a place's kind
	SizeBytes  int64      `json:"size_bytes"`
	ModifiedAt *time.Time `json:"modified_at"`
}

// Browse is a listed folder (or the places when Path is "").
type Browse struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent"` // "" at the top of a drive: back to the places
	Entries   []BrowseEntry `json:"entries"`
	Places    []Place       `json:"places"`
	Truncated bool          `json:"truncated"` // the folder has more than maxBrowseEntries entries
}

// browse lists dir; "" lists the places.
func browse(dir string, places []Place) (Browse, error) {
	b := Browse{Path: dir, Places: places, Entries: []BrowseEntry{}}
	if dir == "" {
		for _, p := range places {
			b.Entries = append(b.Entries, BrowseEntry{Name: p.Name, Path: p.Path, Kind: p.Kind})
		}
		return b, nil
	}
	if !filepath.IsAbs(dir) {
		return b, newError(ErrInvalid, "请输入完整的路径：%s", dir)
	}
	dir = filepath.Clean(dir)
	b.Path = dir
	if parent := filepath.Dir(dir); parent != dir {
		b.Parent = parent
	}
	f, err := os.Open(dir)
	if err != nil {
		return b, browseError(dir, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return b, browseError(dir, err)
	}
	if !fi.IsDir() {
		return b, newError(ErrInvalid, "%s 不是文件夹", dir)
	}
	names, err := f.Readdirnames(-1)
	if err != nil && len(names) == 0 {
		return b, browseError(dir, err)
	}
	var folders, zips []BrowseEntry
	checks := 0
	for _, name := range names {
		if strings.HasPrefix(name, ".") || strings.Contains(name, extractingMarker) || reviewfile.IsTempName(name) {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p) // follows links: a link to a folder is a folder
		if err != nil || isHidden(info) {
			continue
		}
		mod := info.ModTime().UTC()
		switch {
		case info.IsDir():
			e := BrowseEntry{Name: name, Path: p, Kind: kindFolder, ModifiedAt: &mod}
			if checks < maxPackageChecks {
				checks++
				if isPackageDir(p) {
					e.Kind = kindPackage
				}
			}
			folders = append(folders, e)
		case info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(name), ".zip"):
			kind := kindZip
			if strings.HasSuffix(strings.TrimSuffix(name, filepath.Ext(name)), resultsSuffix) {
				kind = kindResults
			}
			zips = append(zips, BrowseEntry{Name: name, Path: p, Kind: kind, SizeBytes: info.Size(), ModifiedAt: &mod})
		}
	}
	// Folders by name; zip files newest first (the package just downloaded
	// comes first in the downloads folder).
	slices.SortFunc(folders, func(a, b BrowseEntry) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
	})
	slices.SortFunc(zips, func(a, b BrowseEntry) int {
		return cmp.Or(b.ModifiedAt.Compare(*a.ModifiedAt), cmp.Compare(a.Name, b.Name))
	})
	b.Entries = append(folders, zips...)
	if b.Entries == nil {
		b.Entries = []BrowseEntry{}
	}
	if len(b.Entries) > maxBrowseEntries {
		b.Entries, b.Truncated = b.Entries[:maxBrowseEntries], true
	}
	return b, nil
}

func browseError(dir string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return newError(ErrNotFound, "文件夹不存在：%s", dir)
	case errors.Is(err, fs.ErrPermission):
		return newError(ErrForbidden, "没有权限打开文件夹 %s", dir)
	}
	return newError(ErrIO, "无法打开文件夹 %s：%v", dir, err)
}

// homePlaces returns the home folder and its 下载 / 桌面 / 文档 folders
// (named as on Windows) that exist.
func homePlaces(home string, known map[string]string) []Place {
	var out []Place
	add := func(name, p, kind string) {
		if p == "" {
			return
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, Place{Name: name, Path: p, Kind: kind})
		}
	}
	add("下载", known[kindDownloads], kindDownloads)
	add("桌面", known[kindDesktop], kindDesktop)
	add("文档", known[kindDocuments], kindDocuments)
	add("主文件夹", home, kindHome)
	return out
}
