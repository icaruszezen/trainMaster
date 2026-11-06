package httpapi

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/icaruszezen/trainmaster/backend/internal/trainerpkg"
)

// The downloads that install the training node on a Windows computer
// (TM_TRAINER_PACKAGE_DIR, see package trainerpkg):
// GET /api/v1/compute/windows/install.ps1, which the install command
// downloads and runs, and GET /api/v1/compute/windows/trainer.zip, which
// the script installs.

// File names and headers of the downloads; the names are ASCII, so no
// filename* is needed.
const (
	windowsScriptName         = "install.ps1"
	windowsScriptDisposition  = `inline; filename="install.ps1"`
	windowsPackageName        = "trainmaster-trainer.zip"
	windowsPackageDisposition = `attachment; filename="trainmaster-trainer.zip"`
)

const (
	msgNoWindowsScript  = "平台没有附带 Windows 训练节点的安装脚本（未配置 TM_TRAINER_PACKAGE_DIR，或目录中没有 scripts/install.ps1），请联系管理员"
	msgNoWindowsPackage = "平台没有附带 Windows 训练节点的安装包（未配置 TM_TRAINER_PACKAGE_DIR，或目录不完整），请联系管理员"
)

// windowsInstallScript serves scripts/install.ps1 as UTF-8 text without a
// BOM, as Invoke-RestMethod reads it.
func (s *server) windowsInstallScript(w http.ResponseWriter, r *http.Request) {
	f, err := s.trainerPackage.InstallScript()
	if err != nil {
		s.trainerPackageError(w, r, err, msgNoWindowsScript)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", windowsScriptDisposition)
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, windowsScriptName, f.ModTime, bytes.NewReader(f.Data))
}

// windowsTrainerPackage serves the zip of the training node, with an ETag
// and Last-Modified for conditional and range requests.
func (s *server) windowsTrainerPackage(w http.ResponseWriter, r *http.Request) {
	f, err := s.trainerPackage.Archive()
	if err != nil {
		s.trainerPackageError(w, r, err, msgNoWindowsPackage)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", windowsPackageDisposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", f.ETag)
	http.ServeContent(w, r, windowsPackageName, f.ModTime, bytes.NewReader(f.Data))
}

// trainerPackageError answers 404 with message when a download is not
// available. Like a missing local reviewer that is no error; a file that
// is there but cannot be read (e.g. for its permissions) is logged.
func (s *server) trainerPackageError(w http.ResponseWriter, r *http.Request, err error, message string) {
	if !errors.Is(err, trainerpkg.ErrUnavailable) {
		s.log.Warn("read the training node package for Windows", "path", r.URL.Path, "err", err,
			"request_id", RequestID(r.Context()))
	}
	notFoundMessage(w, message)
}
