package reviewer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// lockingOS: the systems lockFile locks on (see lock_*.go).
var lockingOS = runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin"

func TestPackageLock(t *testing.T) {
	if !lockingOS {
		t.Skip("no locking on " + runtime.GOOS)
	}
	dir := t.TempDir()
	newTestPackage().writeFolder(t, dir)
	ctx := context.Background()
	a, err := loadPackage(ctx, dir, loadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A second program opening the folder: it is refused, as it would
	// overwrite the statuses of the first one.
	_, err = loadPackage(ctx, dir, loadOptions{})
	var re *Error
	if !errors.Is(err, ErrConflict) || !errors.As(err, &re) || re.Message != "这个审图包已在另一个本地审图程序窗口中打开" {
		t.Fatalf("second open: %v", err)
	}
	if _, err := a.SetStatus(101, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// Once the first one closed it, the folder opens, with its statuses.
	b, err := loadPackage(ctx, dir, loadOptions{})
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	defer b.Close()
	if img, err := b.GetImage(101); err != nil || img.Status != "approved" {
		t.Errorf("image 101 = %+v, %v", img.Image, err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

// TestPackageLockReadOnlyFolder: a folder the lock file cannot be created
// in opens without the lock.
func TestPackageLockReadOnlyFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a folder the test cannot write to")
	}
	dir := t.TempDir()
	newTestPackage().writeFolder(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	p, err := loadPackage(context.Background(), dir, loadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}

func TestOpenPackageOpenInAnotherProgram(t *testing.T) {
	if !lockingOS {
		t.Skip("no locking on " + runtime.GOOS)
	}
	first, second := newTestServer(t), newTestServer(t)
	dir := t.TempDir()
	zipPath := newTestPackage().writeZip(t, dir, zipName)
	folder := filepath.Join(dir, "审图-7-外观检测")
	first.open(zipPath)
	if code := first.do(http.MethodPut, "/api/review/images/101/status", map[string]string{"status": "needs_sample"}, nil); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}

	// The same zip (or its folder) in another program: refused.
	for _, p := range []string{zipPath, folder} {
		if code := second.do(http.MethodPost, "/api/open", map[string]string{"path": p}, nil); code != http.StatusAccepted {
			t.Fatalf("open %s: %d", p, code)
		}
		s := second.waitOpened()
		if s.Package != nil || s.OpenError == nil || s.OpenError.Message != "这个审图包已在另一个本地审图程序窗口中打开" {
			t.Errorf("second program, %s: package %+v, error %+v", p, s.Package, s.OpenError)
		}
	}

	// In the program that has it open, its zip opens it as it is.
	s := first.open(zipPath)
	if s.OpenError != nil || s.Package.Path != folder || s.Package.Counts.NeedsSample != 1 {
		t.Errorf("same program: package %+v, error %+v", s.Package, s.OpenError)
	}

	// The lock file is not part of the results.
	var res ExportResult
	if code := first.do(http.MethodPost, "/api/export", nil, &res); code != http.StatusOK {
		t.Fatalf("export: %d", code)
	}
	for name := range readZip(t, res.Path) {
		if strings.Contains(name, lockFileName) {
			t.Errorf("results hold %s", name)
		}
	}

	// Closed in the first program, it opens in the second one, with the
	// first one's work.
	if code := first.do(http.MethodPost, "/api/close", nil, nil); code != http.StatusNoContent {
		t.Fatalf("close: %d", code)
	}
	if s := second.open(zipPath); s.Package.Path != folder || !s.Package.Resumed || s.Package.Counts.NeedsSample != 1 {
		t.Errorf("second program after closing: %+v", s.Package)
	}
}
