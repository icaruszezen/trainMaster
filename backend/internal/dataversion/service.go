// Package dataversion implements data versions (数据版本): frozen training
// sets of a project, built from some of the project's datasets with the
// project's class list, split into train / val / test and optionally
// augmented, stored as plain files under exports/data-versions/ with their
// metadata in PostgreSQL, and downloaded as YOLO, Pascal VOC or COCO.
//
// Versions are built in the background, one at a time (build.go). The
// build queue's notifications and the cancel functions live in this
// process, so the platform supports a single instance only (like the
// dataset import runner).
package dataversion

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Options are the dependencies of a Service.
type Options struct {
	DB       *pgxpool.Pool
	Storage  *storage.Storage
	Logger   *slog.Logger
	Datasets *dataset.Service
	Projects *project.Service
	// DiskUsage reports the free space of the storage directory; nil means
	// Storage.Usage. Tests simulate a full disk.
	DiskUsage func() (storage.Usage, error)
}

// Service implements the data version operations used by the HTTP
// handlers. Start runs the background work (builds, recycle bin purge).
type Service struct {
	db       *pgxpool.Pool
	storage  *storage.Storage
	log      *slog.Logger
	datasets *dataset.Service
	projects *project.Service
	usage    func() (storage.Usage, error)

	// base ends with the service: the builder and the janitor stop, and a
	// running build is canceled (and fails as interrupted).
	base  context.Context
	stop  context.CancelFunc
	loops sync.WaitGroup

	background sync.WaitGroup // folder removals started by discard

	builder builder       // build.go
	exports exportTickets // export.go
	thumbs  thumbnails    // images.go

	removeAll func(path string) error // os.RemoveAll; tests make it fail
	// link is os.Link; tests simulate EXDEV, EPERM or a link to another
	// file (the SameFile check) with it.
	link func(oldname, newname string) error
}

// NewService creates a Service. It does not touch the database yet.
func NewService(o Options) *Service {
	s := &Service{
		db:        o.DB,
		storage:   o.Storage,
		log:       o.Logger,
		datasets:  o.Datasets,
		projects:  o.Projects,
		usage:     o.DiskUsage,
		base:      context.Background(),
		removeAll: os.RemoveAll,
		link:      os.Link,
	}
	if s.usage == nil {
		s.usage = o.Storage.Usage
	}
	s.builder.init()
	return s
}

// janitorInterval is how often the recycle bin purge and the cleanup of
// leftover folders run.
const janitorInterval = 30 * time.Minute

// Start recovers the builds a previous run left (they fail as
// interrupted), then runs the builder and the janitor until ctx ends or
// Stop is called.
func (s *Service) Start(ctx context.Context) error {
	if err := s.recoverBuilds(ctx); err != nil {
		return err
	}
	s.base, s.stop = context.WithCancel(ctx)
	s.loops.Add(2)
	go func() {
		defer s.loops.Done()
		s.builderLoop(s.base)
	}()
	go func() {
		defer s.loops.Done()
		s.janitorLoop(s.base)
	}()
	return nil
}

// Stop cancels the background work and waits for it to end.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.loops.Wait()
	s.background.Wait()
}

// rootDir is exports/data-versions: the folders of all versions.
func (s *Service) rootDir() string {
	return filepath.Join(s.storage.Dir(storage.Exports), "data-versions")
}

// versionDir is the folder of a version (dir_name is relative to
// exports/).
func (s *Service) versionDir(dirName string) string {
	return filepath.Join(s.storage.Dir(storage.Exports), filepath.FromSlash(dirName))
}

// versionDirName is the dir_name of version n of a project.
func versionDirName(projectID int64, version int) string {
	return "data-versions/project-" + itoa(projectID) + "/v" + itoa(int64(version))
}
