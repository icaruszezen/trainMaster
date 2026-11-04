package reviewer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The recently opened packages are kept in recent.json in the user's
// configuration folder (%APPDATA%\trainmaster-reviewer on Windows), the
// newest first, at most maxRecent.

const (
	recentFile = "recent.json"
	maxRecent  = 12
)

// RecentPackage is a recently opened package.
type RecentPackage struct {
	Path       string    `json:"path"`     // the package folder
	ZipPath    string    `json:"zip_path"` // the zip it came from, "" if opened as a folder
	PackageID  string    `json:"package_id"`
	ReviewID   int64     `json:"review_id"`
	ReviewName string    `json:"review_name"`
	Counts     Counts    `json:"counts"` // when it was last closed (or opened)
	OpenedAt   time.Time `json:"opened_at"`
	// Exists tells whether the folder is still there (filled in when
	// listed, not stored).
	Exists bool `json:"exists"`
}

type recentStore struct {
	path string // "" keeps the list in memory only
	mu   sync.Mutex
	mem  []RecentPackage
}

func newRecentStore(configDir string) *recentStore {
	s := &recentStore{}
	if configDir != "" {
		s.path = filepath.Join(configDir, recentFile)
	}
	return s
}

type recentDoc struct {
	Items []RecentPackage `json:"items"`
}

// load reads the list (an unreadable file is an empty list).
func (s *recentStore) load() []RecentPackage {
	if s.path == "" {
		return append([]RecentPackage(nil), s.mem...)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var doc recentDoc
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	return doc.Items
}

func (s *recentStore) save(items []RecentPackage) error {
	if len(items) > maxRecent {
		items = items[:maxRecent]
	}
	if s.path == "" {
		s.mem = append([]RecentPackage(nil), items...)
		return nil
	}
	for i := range items {
		items[i].Exists = false
	}
	data, err := json.MarshalIndent(recentDoc{Items: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(s.path, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// List returns the list with Exists filled in.
func (s *recentStore) List() []RecentPackage {
	s.mu.Lock()
	items := s.load()
	s.mu.Unlock()
	out := make([]RecentPackage, 0, len(items))
	for _, it := range items {
		fi, err := os.Stat(it.Path)
		it.Exists = err == nil && fi.IsDir()
		out = append(out, it)
	}
	return out
}

// Put adds or updates the entry of a package folder and moves it to the
// front when front is set.
func (s *recentStore) Put(entry RecentPackage, front bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.load()
	out := make([]RecentPackage, 0, len(items)+1)
	at := -1
	for _, it := range items {
		if samePath(it.Path, entry.Path) {
			at = len(out)
			if !front {
				entry.OpenedAt = it.OpenedAt
			}
		} else {
			out = append(out, it)
		}
	}
	if front || at < 0 {
		out = append([]RecentPackage{entry}, out...)
	} else {
		out = append(out[:at], append([]RecentPackage{entry}, out[at:]...)...)
	}
	return s.save(out)
}

// Remove removes the entry of a folder.
func (s *recentStore) Remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.load()
	out := items[:0]
	for _, it := range items {
		if !samePath(it.Path, path) {
			out = append(out, it)
		}
	}
	return s.save(out)
}

// samePath reports whether two cleaned absolute paths name the same
// folder (ignoring case on Windows and macOS).
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
