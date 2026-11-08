package dataversion

import (
	"errors"
	"fmt"
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
	// The removal stops with the service (a large folder must not hold up
	// a shutdown); the janitor removes the rest after the next start.
	ctx := s.base
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		if err := removeTreeCtx(ctx, aside); err != nil && ctx.Err() == nil {
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

// hasPending reports whether the folder dir has the .pending marker.
func hasPending(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, pendingMarker))
	return err == nil && fi.Mode().IsRegular()
}

// writePending creates the .pending marker of version id in dir.
func writePending(dir string, id int64) error {
	f, err := os.OpenFile(filepath.Join(dir, pendingMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("write %s: %w", pendingMarker, err)
	}
	_, err = fmt.Fprintf(f, "{\"version_id\": %d}\n", id)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", pendingMarker, err)
	}
	return nil
}

// discardOwned discards the folder dir of version id if it has the
// .pending marker: a folder without it may be a finished version that is
// not this one (restored from a backup) and is left alone. Errors are
// logged.
func (s *Service) discardOwned(id int64, dir string) {
	if !hasPending(dir) {
		return
	}
	if err := s.discard(id, dir); err != nil {
		s.log.Warn("data version: clear folder", "version_id", id, "err", err)
	}
}
