//go:build windows

package reviewer

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// defaultPlaces returns the known folders of the user and the drives.
func defaultPlaces() []Place {
	known := map[string]string{}
	for kind, id := range map[string]*windows.KNOWNFOLDERID{
		kindDownloads: windows.FOLDERID_Downloads,
		kindDesktop:   windows.FOLDERID_Desktop,
		kindDocuments: windows.FOLDERID_Documents,
	} {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil {
			known[kind] = p
		}
	}
	home, _ := os.UserHomeDir()
	places := homePlaces(home, known)
	drives, err := windows.GetLogicalDrives()
	if err != nil {
		return places
	}
	for i := range 26 {
		if drives&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		switch windows.GetDriveType(p) {
		case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_REMOTE, windows.DRIVE_RAMDISK:
			places = append(places, Place{Name: root[:2], Path: root, Kind: kindDrive})
		}
	}
	return places
}

// isHidden reports whether Explorer hides a file by default (hidden or
// system files).
func isHidden(fi fs.FileInfo) bool {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&(windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM) != 0
	}
	return false
}

// reveal opens Explorer with the file selected.
func reveal(path string) error {
	path = filepath.Clean(path)
	cmd := exec.Command("explorer.exe")
	// explorer.exe parses its command line itself: /select,"<path>" (paths
	// on Windows cannot contain quotes).
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	if err := cmd.Start(); err != nil {
		return err
	}
	// explorer.exe exits with 1 even when it worked.
	go func() { _ = cmd.Wait() }()
	return nil
}
