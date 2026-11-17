package dataversion

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

// Folder helpers shared by the builder, Delete and the janitor. On
// fast-rename volumes (local / SMB), a version folder is renamed aside under
// exports/data-versions (.deleting-<id>-<nanos>) first, so that a new build
// of the same number starts from an empty folder at once, and removed in the
// background; the janitor removes what a crash left behind. On object stores
// (S3), RemoveDirAll clears the prefix synchronously in batch DeleteObjects.

// pendingMarker is the file in a version folder while it is being built:
// a folder with it may be cleared (retry, recovery); a folder without it is
// a finished version (possibly restored from a backup) and is never
// cleared by a build.
const pendingMarker = ".pending"

// deletingPrefix names the folders being removed under rootDir.
const deletingPrefix = ".deleting-"

// discard moves the folder dirName of version id on nodeID aside and removes
// it in the background (or synchronously on object stores). A missing folder
// is fine.
func (s *Service) discard(ctx context.Context, id, nodeID int64, dirName string) error {
	_, vol, err := s.volumeFor(ctx, nodeID)
	if err != nil {
		return err
	}
	return s.discardOnVolume(ctx, id, vol, dirName)
}

func (s *Service) discardOnVolume(ctx context.Context, id int64, vol storagenode.Volume, dirName string) error {
	exists, _, err := vol.DirExists(ctx, storagenode.SpaceExports, dirName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !vol.SupportsFastDirRename() {
		return vol.RemoveDirAll(ctx, storagenode.SpaceExports, dirName)
	}
	if err := vol.MkdirDir(ctx, storagenode.SpaceExports, "data-versions", false); err != nil {
		return err
	}
	asideRel := "data-versions/" + deletingPrefix + strconv.FormatInt(id, 10) + "-" +
		strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := vol.RenameDir(ctx, storagenode.SpaceExports, dirName, asideRel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	// The removal stops with the service (a large folder must not hold up
	// a shutdown); the janitor removes the rest after the next start.
	bctx := s.base
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		if lv, ok := vol.(*storagenode.LocalVolume); ok {
			asidePath := filepath.Join(lv.Root(), storagenode.SpaceExports, filepath.FromSlash(asideRel))
			if err := removeTreeCtx(bctx, asidePath); err != nil && bctx.Err() == nil {
				s.log.Warn("data version: remove folder", "path", asidePath, "err", err)
			}
			return
		}
		if err := vol.RemoveDirAll(bctx, storagenode.SpaceExports, asideRel); err != nil && bctx.Err() == nil {
			s.log.Warn("data version: remove folder", "path", vol.DisplayPath(storagenode.SpaceExports, asideRel), "err", err)
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

// dirHasPending reports whether verDir has the .pending marker.
func dirHasPending(verDir storagenode.Dir) bool {
	fi, err := verDir.Lstat(pendingMarker)
	return err == nil && fi.Mode().IsRegular()
}

// dirWritePending creates the .pending marker of version id in verDir.
func dirWritePending(ctx context.Context, verDir storagenode.Dir, id int64) error {
	data := fmt.Appendf(nil, "{\"version_id\": %d}\n", id)
	if err := verDir.CreateExclusive(ctx, pendingMarker, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", pendingMarker, err)
	}
	return nil
}

func hasPending(dir string) bool {
	d, err := storagenode.OpenLocalDir(dir)
	if err != nil {
		return false
	}
	defer d.Close()
	return dirHasPending(d)
}

func writePending(dir string, id int64) error {
	d, err := storagenode.OpenLocalDir(dir)
	if err != nil {
		return fmt.Errorf("write %s: %w", pendingMarker, err)
	}
	defer d.Close()
	return dirWritePending(context.Background(), d, id)
}

// discardOwned discards the folder dirName of version id on nodeID if it has
// the .pending marker: a folder without it may be a finished version that is
// not this one (restored from a backup) and is left alone. Errors are logged.
func (s *Service) discardOwned(ctx context.Context, id, nodeID int64, dirName string) {
	verDir, err := s.openVersionDir(ctx, nodeID, dirName)
	if err != nil {
		return
	}
	pending := dirHasPending(verDir)
	_ = verDir.Close()
	if !pending {
		return
	}
	if err := s.discard(ctx, id, nodeID, dirName); err != nil {
		s.log.Warn("data version: clear folder", "version_id", id, "err", err)
	}
}
