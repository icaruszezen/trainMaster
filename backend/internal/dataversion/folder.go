package dataversion

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Folder helpers shared by the builder, Delete and the janitor. A version
// folder is never removed in place: it is renamed aside under rootDir
// (.deleting-<id>-<nanos>) first, so that a new build of the same number
// starts from an empty folder at once, and removed in the background; the
// janitor removes what a crash left behind.

// pendingMarker is the file in a version folder while it is being built:
// a folder with it may be cleared (retry, recovery); a folder without it is
// a finished version (possibly restored from a backup) and is never
// cleared by a build.
const pendingMarker = ".pending"

// deletingPrefix names the folders being removed under rootDir.
const deletingPrefix = ".deleting-"

// discard moves the folder dir of version id aside and removes it in the
// background. A missing folder is fine. The removal is best effort
// (logged); leftovers are removed by the janitor.
func (s *Service) discard(id int64, dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(s.rootDir(), 0o755); err != nil {
		return err
	}
	aside := filepath.Join(s.rootDir(), deletingPrefix+strconv.FormatInt(id, 10)+"-"+
		strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.Rename(dir, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		if err := s.removeAll(aside); err != nil {
			s.log.Warn("data version: remove folder", "path", aside, "err", err)
		}
	}()
	return nil
}

// thumbDir is the thumbnail cache of a version.
func (s *Service) thumbDir(id int64) string {
	return filepath.Join(s.storage.Dir(storage.Cache), "data-versions", strconv.FormatInt(id, 10))
}

// discardThumbnails removes the thumbnail cache of a version (best effort,
// logged).
func (s *Service) discardThumbnails(id int64) {
	if err := s.removeAll(s.thumbDir(id)); err != nil {
		s.log.Warn("data version: remove thumbnails", "version_id", id, "err", err)
	}
}

// isDeletingName reports whether name (a folder under rootDir) is a folder
// discard is removing or left behind.
func isDeletingName(name string) bool { return strings.HasPrefix(name, deletingPrefix) }

// WaitBackground waits for the background removals started so far (tests,
// and Stop).
func (s *Service) WaitBackground() { s.background.Wait() }
