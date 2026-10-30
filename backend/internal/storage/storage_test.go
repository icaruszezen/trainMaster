package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "nested", "data"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestNewCreatesLayout(t *testing.T) {
	s := newTestStorage(t)
	if !filepath.IsAbs(s.Root()) {
		t.Errorf("Root() = %q, want an absolute path", s.Root())
	}
	for _, k := range Keys {
		fi, err := os.Stat(filepath.Join(s.Root(), string(k)))
		if err != nil || !fi.IsDir() {
			t.Errorf("subdirectory %s missing: %v", k, err)
		}
	}
	dirs := s.Directories()
	if len(dirs) != len(Keys) {
		t.Fatalf("Directories() returned %d entries, want %d", len(dirs), len(Keys))
	}
	for i, d := range dirs {
		if d.Key != Keys[i] || d.Path != s.Dir(d.Key) || !d.Writable {
			t.Errorf("Directories()[%d] = %+v", i, d)
		}
	}
	// Probe files must not be left behind.
	entries, _ := os.ReadDir(s.Dir(Images))
	if len(entries) != 0 {
		t.Errorf("images dir not empty after probing: %v", entries)
	}
	// Opening an existing layout again is fine.
	if _, err := New(s.Root()); err != nil {
		t.Errorf("New on existing layout: %v", err)
	}
}

func TestNewFailsWhenSubdirIsAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, string(Models)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(root)
	if err == nil || !strings.Contains(err.Error(), "PUID/PGID") {
		t.Fatalf("expected an error with a permission hint, got %v", err)
	}
}

func TestNewFailsWhenNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root or on Windows")
	}
	root := t.TempDir()
	ro := filepath.Join(root, string(Runs))
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	_, err := New(root)
	if err == nil || !strings.Contains(err.Error(), ro) || !strings.Contains(err.Error(), "PUID/PGID") {
		t.Fatalf("expected a not-writable error naming %s, got %v", ro, err)
	}
}

func TestResolve(t *testing.T) {
	s := newTestStorage(t)
	base := s.Dir(Images)
	ok := map[string]string{
		"a.jpg":             filepath.Join(base, "a.jpg"),
		"ds1/line-2/b.png":  filepath.Join(base, "ds1", "line-2", "b.png"),
		"ds1/../c.png":      filepath.Join(base, "c.png"),
		".":                 base,
		"名称 with space.bmp": filepath.Join(base, "名称 with space.bmp"),
	}
	for rel, want := range ok {
		got, err := s.Resolve(Images, rel)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", rel, got, err, want)
		}
	}

	bad := []string{"", "..", "../x.jpg", "a/../../x.jpg", "/etc/passwd", "../images-other/x"}
	for _, rel := range bad {
		if got, err := s.Resolve(Images, rel); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Resolve(%q) = %q, %v; want ErrInvalidPath", rel, got, err)
		}
	}

	if _, err := s.Resolve(Key("secrets"), "x"); err == nil {
		t.Error("Resolve with an unknown key should fail")
	}
}

func TestUsage(t *testing.T) {
	s := newTestStorage(t)
	u, err := s.Usage()
	if runtime.GOOS != "linux" {
		if !errors.Is(err, ErrUsageUnsupported) {
			t.Fatalf("Usage() error = %v, want ErrUsageUnsupported", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if u.TotalBytes == 0 || u.FreeBytes > u.TotalBytes || u.UsedBytes != u.TotalBytes-u.FreeBytes {
		t.Errorf("implausible usage: %+v", u)
	}
}
