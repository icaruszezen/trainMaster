package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/compute"
)

const (
	windowsScriptURL  = "/api/v1/compute/windows/install.ps1"
	windowsPackageURL = "/api/v1/compute/windows/trainer.zip"
	// testInstallScript is saved with a UTF-8 BOM, like the script in the
	// repository.
	testInstallScript = "\ufeff# trainMaster 训练节点\r\nparam([string]$ServerUrl)\r\n"
)

// testTrainerTime is the modification time of the files
// writeTrainerPackage writes.
var testTrainerTime = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)

// writeTrainerPackage creates a trainer source directory (as the
// repository's trainer/) with the install script when script is not "".
func writeTrainerPackage(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"pyproject.toml":                         "[project]\nname = \"trainmaster-trainer\"\n",
		"requirements.txt":                       "psutil>=5.9\n",
		"trainmaster_trainer/__init__.py":        "__version__ = \"0.4.0\"\n",
		"trainmaster_trainer/agent.py":           "def main():\n    pass\n",
		"trainmaster_trainer/torch_variants.txt": "cu126 525\n",

		// Not in the package.
		"requirements-dev.txt": "pytest\n",
		"scripts/install.sh":   "#!/bin/sh\n",
		"tests/test_agent.py":  "def test(): pass\n",

		"trainmaster_trainer/__pycache__/agent.cpython-312.pyc": "\x00",
	}
	if script != "" {
		files["scripts/install.ps1"] = script
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, testTrainerTime, testTrainerTime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newTrainerPackageEnv serves the compute API (without a database) with
// the trainer source directory dir and a node token, which the downloads
// must not need.
func newTrainerPackageEnv(t *testing.T, dir string) *computeEnv {
	t.Helper()
	return newComputeHandler(t, compute.Options{}, Options{TrainerPackageDir: dir, NodeToken: "s3cr3t-node-token-0123"})
}

func TestWindowsInstallScript(t *testing.T) {
	// Not configured, or the directory has no script.
	for _, dir := range []string{"", t.TempDir(), writeTrainerPackage(t, "")} {
		e := newTrainerPackageEnv(t, dir)
		for _, method := range []string{"GET", "HEAD"} {
			rec := e.do(method, windowsScriptURL, nil)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %q: status = %d", method, dir, rec.Code)
			}
		}
		wantError(t, e.do("GET", windowsScriptURL, nil), 404, codeNotFound,
			"平台没有附带 Windows 训练节点的安装脚本（未配置 TM_TRAINER_PACKAGE_DIR，或目录中没有 scripts/install.ps1），请联系管理员")
	}

	e := newTrainerPackageEnv(t, writeTrainerPackage(t, testInstallScript))
	rec := e.do("GET", windowsScriptURL, nil)
	want := strings.TrimPrefix(testInstallScript, "\ufeff")
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("script = %d %q, want %q without the BOM", rec.Code, rec.Body.String(), want)
	}
	h := rec.Header()
	for key, value := range map[string]string{
		"Content-Type":           "text/plain; charset=utf-8",
		"Content-Disposition":    `inline; filename="install.ps1"`,
		"X-Content-Type-Options": "nosniff",
		"Content-Length":         strconv.Itoa(len(want)),
		"Last-Modified":          testTrainerTime.Format(http.TimeFormat),
	} {
		if got := h.Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}

	// HEAD, as the GET routes all answer it.
	rec = e.do("HEAD", windowsScriptURL, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != strconv.Itoa(len(want)) ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("HEAD = %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	// Conditional requests.
	rec = e.do("GET", windowsScriptURL, nil, "If-Modified-Since", testTrainerTime.Format(http.TimeFormat))
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-Modified-Since = %d %q", rec.Code, rec.Body.String())
	}
	// The download is read-only.
	wantError(t, e.do("POST", windowsScriptURL, nil), 405, codeMethodNotAllowed, "")
}

func TestWindowsTrainerPackage(t *testing.T) {
	// Not configured, or the directory is not complete.
	incomplete := writeTrainerPackage(t, testInstallScript)
	if err := os.Remove(filepath.Join(incomplete, "trainmaster_trainer", "__init__.py")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"", t.TempDir(), incomplete} {
		e := newTrainerPackageEnv(t, dir)
		wantError(t, e.do("GET", windowsPackageURL, nil), 404, codeNotFound,
			"平台没有附带 Windows 训练节点的安装包（未配置 TM_TRAINER_PACKAGE_DIR，或目录不完整），请联系管理员")
		if rec := e.do("HEAD", windowsPackageURL, nil); rec.Code != http.StatusNotFound {
			t.Errorf("HEAD %q: status = %d", dir, rec.Code)
		}
	}

	dir := writeTrainerPackage(t, testInstallScript)
	e := newTrainerPackageEnv(t, dir)
	rec := e.do("GET", windowsPackageURL, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	h := rec.Header()
	etag := h.Get("ETag")
	if h.Get("Content-Type") != "application/zip" ||
		h.Get("Content-Disposition") != `attachment; filename="trainmaster-trainer.zip"` ||
		h.Get("Last-Modified") != testTrainerTime.Format(http.TimeFormat) ||
		h.Get("Content-Length") != strconv.Itoa(rec.Body.Len()) || !strings.HasPrefix(etag, `"`) || len(etag) < 10 {
		t.Errorf("headers = %v", h)
	}
	entries := readZip(t, rec.Body.Bytes())
	var names []string
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"pyproject.toml", "requirements.txt", "scripts/install.ps1", "trainmaster_trainer/__init__.py",
		"trainmaster_trainer/agent.py", "trainmaster_trainer/torch_variants.txt"}; !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	// The script in the package keeps its BOM, for -File.
	if got := string(entries["scripts/install.ps1"]); got != testInstallScript {
		t.Errorf("install.ps1 in the package = %q", got)
	}

	// HEAD, conditional and range requests.
	rec = e.do("HEAD", windowsPackageURL, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
		t.Errorf("HEAD = %d, %d bytes, %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	if rec = e.do("GET", windowsPackageURL, nil, "If-None-Match", etag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match = %d", rec.Code)
	}
	if rec = e.do("GET", windowsPackageURL, nil, "Range", "bytes=0-1"); rec.Code != http.StatusPartialContent || rec.Body.String() != "PK" {
		t.Errorf("range = %d %q", rec.Code, rec.Body.String())
	}

	// After a change the package is built again, with another ETag.
	path := filepath.Join(dir, "trainmaster_trainer", "agent.py")
	if err := os.WriteFile(path, []byte("def main():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := testTrainerTime.Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	rec = e.do("GET", windowsPackageURL, nil, "If-None-Match", etag)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag || rec.Header().Get("Last-Modified") != later.Format(http.TimeFormat) {
		t.Fatalf("after a change = %d %v", rec.Code, rec.Header())
	}
	if got := string(readZip(t, rec.Body.Bytes())["trainmaster_trainer/agent.py"]); got != "def main():\n    return 1\n" {
		t.Errorf("agent.py = %q", got)
	}
	wantError(t, e.do("DELETE", windowsPackageURL, nil), 405, codeMethodNotAllowed, "")
}

func TestWindowsInstallerAvailable(t *testing.T) {
	available := func(dir string) any {
		t.Helper()
		e := newTrainerPackageEnv(t, dir)
		raw := want[map[string]any](t, e.do("GET", "/api/v1/compute/server", nil), 200)
		reg := object(t, raw["node_registration"], "node_registration")
		wantKeys(t, "node_registration", reg, registrationKeys...)
		return reg["windows_installer_available"]
	}
	if got := available(writeTrainerPackage(t, testInstallScript)); got != true {
		t.Errorf("complete directory: windows_installer_available = %v", got)
	}
	// Both downloads are needed.
	if got := available(writeTrainerPackage(t, "")); got != false {
		t.Errorf("without the script: windows_installer_available = %v", got)
	}
	noPackage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(noPackage, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noPackage, "scripts", "install.ps1"), []byte(testInstallScript), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := available(noPackage); got != false {
		t.Errorf("without the package: windows_installer_available = %v", got)
	}
	if got := available(""); got != false {
		t.Errorf("not configured: windows_installer_available = %v", got)
	}
}
