//go:build !windows

package reviewer

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// defaultPlaces returns the home folder (and its usual sub-folders) and
// the root of the file system. The program is meant for Windows; this is
// for developing and testing it elsewhere.
func defaultPlaces() []Place {
	home, _ := os.UserHomeDir()
	known := map[string]string{}
	if home != "" {
		known[kindDownloads] = filepath.Join(home, "Downloads")
		known[kindDesktop] = filepath.Join(home, "Desktop")
		known[kindDocuments] = filepath.Join(home, "Documents")
	}
	return append(homePlaces(home, known), Place{Name: "/", Path: "/", Kind: kindRoot})
}

func isHidden(fs.FileInfo) bool { return false }

// reveal shows the file in the file manager: selected on macOS, its folder
// elsewhere.
func reveal(path string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", "-R", path)
	} else {
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
