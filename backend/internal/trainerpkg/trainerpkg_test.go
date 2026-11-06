package trainerpkg

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testTime is the modification time of the files writePackage writes; the
// install script is an hour newer.
var testTime = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)

const testScript = "\ufeff# trainMaster 训练节点安装脚本\r\nparam([string]$ServerUrl)\r\nWrite-Host '安装'\r\n"

// packageFiles are the files of the package writePackage writes, with
// their content.
var packageFiles = map[string]string{
	"pyproject.toml":                         "[project]\nname = \"trainmaster-trainer\"\n",
	"requirements.txt":                       "psutil>=5.9\n",
	"trainmaster_trainer/__init__.py":        "__version__ = \"0.4.0\"\n",
	"trainmaster_trainer/__main__.py":        "from .agent import main\n",
	"trainmaster_trainer/agent.py":           "def main():\n    pass\n",
	"trainmaster_trainer/torch_variants.txt": "cu130 580\ncu126 525\n",
	"scripts/install.ps1":                    testScript,
}

// otherFiles are in the directory (as in the repository's trainer/), but
// not in the package.
var otherFiles = map[string]string{
	"Dockerfile":                   "FROM python:3.12-slim\n",
	"requirements-dev.txt":         "pytest\n",
	"scripts/install.sh":           "#!/bin/sh\n",
	"scripts/lib/torch-variant.sh": "#!/bin/sh\n",
	"tests/test_agent.py":          "def test(): pass\n",
	".venv/lib/site.py":            "x = 1\n",

	"trainmaster_trainer/notes.md":     "# notes\n",
	"trainmaster_trainer/agent.pyc":    "\x00\x01",
	"trainmaster_trainer/.#agent.py":   "editor lock\n",
	"trainmaster_trainer/sub/extra.py": "x = 1\n",

	"trainmaster_trainer/__pycache__/agent.cpython-312.pyc": "\x00\x01",
}

// writePackage creates a trainer source directory with packageFiles and
// otherFiles, a symbolic link to a source file and a folder named like
// one. The install script is written only when script is not "".
func writePackage(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	files := maps.Clone(packageFiles)
	maps.Copy(files, otherFiles)
	delete(files, scriptPath)
	for name, content := range files {
		writeFile(t, dir, name, content, testTime)
	}
	if script != "" {
		writeFile(t, dir, scriptPath, script, testTime.Add(time.Hour))
	}
	if err := os.Symlink("agent.py", filepath.Join(dir, "trainmaster_trainer", "link.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "trainmaster_trainer", "folder.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, dir, name, content string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// readArchive returns the entries of a zip by name.
func readArchive(t *testing.T, data []byte) map[string]*zip.File {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]*zip.File{}
	for _, f := range zr.File {
		if out[f.Name] != nil {
			t.Errorf("entry %s twice", f.Name)
		}
		out[f.Name] = f
	}
	return out
}

func readEntry(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantUnavailable(t *testing.T, err error, msgPart string) {
	t.Helper()
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), msgPart) {
		t.Errorf("err = %v, want ErrUnavailable containing %q", err, msgPart)
	}
}

func TestInstallScript(t *testing.T) {
	s := New(writePackage(t, testScript))
	f, err := s.InstallScript()
	if err != nil {
		t.Fatal(err)
	}
	// The BOM is removed, nothing else.
	if want := strings.TrimPrefix(testScript, "\ufeff"); string(f.Data) != want {
		t.Errorf("script = %q, want %q", f.Data, want)
	}
	if !f.ModTime.Equal(testTime.Add(time.Hour)) || f.ETag != "" {
		t.Errorf("modTime = %v, etag = %q", f.ModTime, f.ETag)
	}

	// A script without a BOM is served as it is; only a leading BOM is
	// one.
	for _, script := range []string{"Write-Host 'x'\n", "Write-Host '\ufeff'\n", "\ufeff"} {
		s = New(writePackage(t, script))
		if f, err := s.InstallScript(); err != nil || string(f.Data) != strings.TrimPrefix(script, "\ufeff") {
			t.Errorf("script %q: %q, %v", script, f.Data, err)
		}
	}
	// Only one BOM is removed.
	s = New(writePackage(t, "\ufeff\ufeffx"))
	if f, err := s.InstallScript(); err != nil || string(f.Data) != "\ufeffx" {
		t.Errorf("script with two BOMs: %q, %v", f.Data, err)
	}
}

func TestInstallScriptUnavailable(t *testing.T) {
	_, err := New("").InstallScript()
	wantUnavailable(t, err, "no directory configured")
	_, err = New(filepath.Join(t.TempDir(), "missing")).InstallScript()
	wantUnavailable(t, err, "scripts/install.ps1 is missing")

	dir := writePackage(t, "")
	s := New(dir)
	_, err = s.InstallScript()
	wantUnavailable(t, err, "scripts/install.ps1 is missing")

	// A symbolic link is not followed, a folder is no script.
	script := filepath.Join(dir, "scripts", "install.ps1")
	writeFile(t, dir, "scripts/real.ps1", testScript, testTime)
	if err := os.Symlink("real.ps1", script); err != nil {
		t.Fatal(err)
	}
	_, err = s.InstallScript()
	wantUnavailable(t, err, "scripts/install.ps1 is not a regular file")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(script, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = s.InstallScript()
	wantUnavailable(t, err, "scripts/install.ps1 is not a regular file")

	// The script does not need the rest of the package.
	dir = t.TempDir()
	writeFile(t, dir, scriptPath, testScript, testTime)
	if _, err := New(dir).InstallScript(); err != nil {
		t.Errorf("script alone: %v", err)
	}
}

var etagPattern = regexp.MustCompile(`^"[0-9a-f]{32}"$`)

func TestArchive(t *testing.T) {
	s := New(writePackage(t, testScript))
	a, err := s.Archive()
	if err != nil {
		t.Fatal(err)
	}
	entries := readArchive(t, a.Data)
	// Exactly the files of the package, at the top of the archive, with
	// "/" and as they are (the script keeps its BOM: run from the
	// extracted package with -File, it needs it).
	if got, want := slices.Sorted(maps.Keys(entries)), slices.Sorted(maps.Keys(packageFiles)); !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	for name, f := range entries {
		if got := readEntry(t, f); got != packageFiles[name] {
			t.Errorf("%s = %q, want %q", name, got, packageFiles[name])
		}
		if f.Method != zip.Deflate || f.Mode() != 0o644 {
			t.Errorf("%s: method %d, mode %v", name, f.Method, f.Mode())
		}
		modTime := testTime
		if name == scriptPath {
			modTime = testTime.Add(time.Hour)
		}
		if !f.Modified.Equal(modTime) {
			t.Errorf("%s: modified %v, want %v", name, f.Modified, modTime)
		}
	}
	// The newest file dates the archive.
	if !a.ModTime.Equal(testTime.Add(time.Hour)) || !etagPattern.MatchString(a.ETag) {
		t.Errorf("modTime = %v, etag = %q", a.ModTime, a.ETag)
	}

	// The same files give the same archive, also from another Source
	// (after a restart).
	b, err := New(s.dir).Archive()
	if err != nil {
		t.Fatal(err)
	}
	if b.ETag != a.ETag || !bytes.Equal(b.Data, a.Data) {
		t.Errorf("archive of the same files differs: %q, %q", a.ETag, b.ETag)
	}
}

func TestArchiveCache(t *testing.T) {
	dir := writePackage(t, testScript)
	s := New(dir)
	first, err := s.Archive()
	if err != nil {
		t.Fatal(err)
	}
	archive := func() *File {
		t.Helper()
		a, err := s.Archive()
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := archive(); a != first {
		t.Error("an unchanged directory should give the cached archive")
	}
	// Files outside the package do not matter.
	writeFile(t, dir, "trainmaster_trainer/notes.md", "# changed notes\n", testTime.Add(2*time.Hour))
	writeFile(t, dir, "trainmaster_trainer/__pycache__/new.cpython-312.pyc", "\x00", testTime.Add(2*time.Hour))
	if a := archive(); a != first {
		t.Error("files outside the package should not rebuild the archive")
	}

	// A change of content and modification time (same size).
	writeFile(t, dir, "trainmaster_trainer/agent.py", "def main():\n    return\n", testTime.Add(2*time.Hour))
	second := archive()
	if second == first || second.ETag == first.ETag || !etagPattern.MatchString(second.ETag) ||
		!second.ModTime.Equal(testTime.Add(2*time.Hour)) {
		t.Fatalf("after a change: etag %q (before %q), modTime %v", second.ETag, first.ETag, second.ModTime)
	}
	if got := readEntry(t, readArchive(t, second.Data)["trainmaster_trainer/agent.py"]); got != "def main():\n    return\n" {
		t.Errorf("agent.py = %q", got)
	}
	if a := archive(); a != second {
		t.Error("the rebuilt archive should be cached")
	}

	// Only the modification time.
	path := filepath.Join(dir, "requirements.txt")
	if err := os.Chtimes(path, testTime, testTime.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	third := archive()
	if third.ETag == second.ETag {
		t.Error("a new modification time should change the archive")
	}
	// Only the size (the same modification time).
	writeFile(t, dir, "requirements.txt", "psutil>=5.9\npyyaml\n", testTime.Add(-time.Hour))
	fourth := archive()
	if fourth.ETag == third.ETag {
		t.Error("a new size should change the archive")
	}

	// A new source file is added, a removed one (here the install script)
	// is left out.
	writeFile(t, dir, "trainmaster_trainer/windows.py", "x = 1\n", testTime)
	if err := os.Remove(filepath.Join(dir, "scripts", "install.ps1")); err != nil {
		t.Fatal(err)
	}
	fifth := archive()
	entries := readArchive(t, fifth.Data)
	if fifth.ETag == fourth.ETag || entries["trainmaster_trainer/windows.py"] == nil || entries[scriptPath] != nil {
		t.Errorf("entries = %v", slices.Sorted(maps.Keys(entries)))
	}
	if !fifth.ModTime.Equal(testTime.Add(2 * time.Hour)) {
		t.Errorf("modTime = %v", fifth.ModTime)
	}
}

func TestArchiveConcurrent(t *testing.T) {
	s := New(writePackage(t, testScript))
	var wg sync.WaitGroup
	etags := make([]string, 8)
	for i := range etags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, err := s.Archive(); err == nil {
				etags[i] = a.ETag
			}
		}()
	}
	wg.Wait()
	for _, etag := range etags {
		if etag == "" || etag != etags[0] {
			t.Fatalf("etags = %q", etags)
		}
	}
}

func TestArchiveUnavailable(t *testing.T) {
	_, err := New("").Archive()
	wantUnavailable(t, err, "no directory configured")
	_, err = New(filepath.Join(t.TempDir(), "missing")).Archive()
	wantUnavailable(t, err, "pyproject.toml is missing")

	for _, name := range []string{"pyproject.toml", "requirements.txt", "trainmaster_trainer/__init__.py"} {
		t.Run(name, func(t *testing.T) {
			dir := writePackage(t, testScript)
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			s := New(dir)
			_, err := s.Archive()
			wantUnavailable(t, err, name+" is missing")
			wantUnavailable(t, s.Check(), name+" is missing")
			// A symbolic link is not followed.
			if err := os.Symlink(filepath.Join(dir, "trainmaster_trainer", "agent.py"), path); err != nil {
				t.Fatal(err)
			}
			_, err = s.Archive()
			wantUnavailable(t, err, name+" is not a regular file")
		})
	}

	// Without the module folder.
	dir := writePackage(t, testScript)
	if err := os.RemoveAll(filepath.Join(dir, "trainmaster_trainer")); err != nil {
		t.Fatal(err)
	}
	_, err = New(dir).Archive()
	wantUnavailable(t, err, "trainmaster_trainer/__init__.py is missing")
}

func TestCheck(t *testing.T) {
	dir := writePackage(t, testScript)
	if err := New(dir).Check(); err != nil {
		t.Errorf("complete directory: %v", err)
	}
	wantUnavailable(t, New("").Check(), "no directory configured")

	// The package without the install script can be built, but the
	// installer is not complete.
	dir = writePackage(t, "")
	if _, err := New(dir).Archive(); err != nil {
		t.Fatal(err)
	}
	wantUnavailable(t, New(dir).Check(), "scripts/install.ps1 is missing")

	// The script alone is not enough either.
	dir = t.TempDir()
	writeFile(t, dir, scriptPath, testScript, testTime)
	wantUnavailable(t, New(dir).Check(), "pyproject.toml is missing")
}

// TestRepositoryTrainer builds the package from the repository's trainer/
// directory, which make dev-backend serves and the Docker image bundles:
// it must have what the package needs, and what the package leaves out
// must not be needed (the module reads no other data files).
func TestRepositoryTrainer(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "trainer")
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err != nil {
		t.Skipf("no repository trainer/ directory: %v", err)
	}
	a, err := New(dir).Archive()
	if err != nil {
		t.Fatal(err)
	}
	entries := readArchive(t, a.Data)
	for _, name := range []string{"pyproject.toml", "requirements.txt", "trainmaster_trainer/__init__.py",
		"trainmaster_trainer/__main__.py", "trainmaster_trainer/torch_variants.txt"} {
		if entries[name] == nil {
			t.Errorf("the package lacks %s", name)
		}
	}
	for name := range entries {
		if name != scriptPath && name != pyprojectPath && name != requirementsPath &&
			!(strings.HasPrefix(name, moduleDir+"/") && moduleFile(strings.TrimPrefix(name, moduleDir+"/"))) {
			t.Errorf("unexpected entry %s", name)
		}
	}
	// setuptools installs no other data files of the module.
	pyproject := readEntry(t, entries["pyproject.toml"])
	if !strings.Contains(pyproject, `trainmaster_trainer = ["torch_variants.txt"]`) {
		t.Errorf("package-data of pyproject.toml changed; update moduleFile:\n%s", pyproject)
	}
}
