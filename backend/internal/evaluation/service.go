// Package evaluation implements model evaluation tasks (模型评估):
// creating single or batch multi-model evaluations against ready data versions
// or datasets, dispatching model_evaluation tasks to compute nodes via
// model.TaskSource, computing interactive detection metrics (mAP, P/R/F1,
// curves, confusion matrix, industrial OK/NG overkill/escape rates), browsing
// and comparing error samples, and exporting offline HTML/CSV/JSON reports.
package evaluation

import (
	"compress/gzip"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
	"github.com/icaruszezen/trainmaster/backend/internal/dataversion"
	"github.com/icaruszezen/trainmaster/backend/internal/model"
	"github.com/icaruszezen/trainmaster/backend/internal/project"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagenode"
)

const (
	leaseSweepInterval = 15 * time.Second
	maxAttempts        = 3
	maxClaimSkips      = 16
	maxCachedEvals     = 16
	maxResultsBytes    = 256 << 20 // 256 MiB decompressed results payload guard
)

// ServiceOptions configures evaluation.Service.
type ServiceOptions struct {
	DB           *pgxpool.Pool
	Storage      *storage.Storage
	StorageNodes *storagenode.Service
	Logger       *slog.Logger
	Projects     *project.Service
	Datasets     *dataset.Service
	DataVersions *dataversion.Service
	Models       *model.Service
}

// Service manages evaluation lifecycle, compute-node queue, real-time analysis,
// multi-model comparison, and offline report generation.
type Service struct {
	db           *pgxpool.Pool
	storage      *storage.Storage
	storageNodes *storagenode.Service
	log          *slog.Logger
	projects     *project.Service
	datasets     *dataset.Service
	dataVersions *dataversion.Service
	models       *model.Service

	base  context.Context
	stop  context.CancelFunc
	loops sync.WaitGroup

	cache *loadedEvalCache
}

// NewService creates an evaluation.Service and registers it as a TaskSource
// with o.Models (if non-nil) so compute nodes can claim model_evaluation tasks.
func NewService(o ServiceOptions) *Service {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		db:           o.DB,
		storage:      o.Storage,
		storageNodes: o.StorageNodes,
		log:          log,
		projects:     o.Projects,
		datasets:     o.Datasets,
		dataVersions: o.DataVersions,
		models:       o.Models,
		base:         context.Background(),
		cache:        newLoadedEvalCache(maxCachedEvals),
	}
	if o.Models != nil {
		o.Models.AddTaskSource(s)
	}
	return s
}

// Start runs background lease expiry sweeps until ctx ends or Stop is called.
func (s *Service) Start(ctx context.Context) error {
	s.base, s.stop = context.WithCancel(ctx)
	if err := s.expireLeases(s.base); err != nil {
		s.log.Warn("initial evaluation lease sweep failed", "err", err)
	}
	s.loops.Add(1)
	go s.backgroundLoop(s.base)
	return nil
}

// Stop stops background loops and waits for them to exit.
func (s *Service) Stop() {
	if s.stop != nil {
		s.stop()
	}
	s.loops.Wait()
}

func (s *Service) backgroundLoop(ctx context.Context) {
	defer s.loops.Done()
	t := time.NewTicker(leaseSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.expireLeases(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("evaluation lease sweep failed", "err", err)
			}
		}
	}
}

// ---- Storage paths under runs/evaluations/eval-<id>/ ----

func (s *Service) evalDir(id int64) string {
	return filepath.Join(s.storage.Dir(storage.Runs), "evaluations", fmt.Sprintf("eval-%d", id))
}

func (s *Service) curvesPath(id int64) string {
	return filepath.Join(s.evalDir(id), "curves.json")
}

func (s *Service) predictionsPath(id int64) string {
	return filepath.Join(s.evalDir(id), "predictions.json.gz")
}

func (s *Service) removeEvalDir(id int64) {
	if s.storage == nil {
		return
	}
	if err := os.RemoveAll(s.evalDir(id)); err != nil {
		s.log.Warn("remove evaluation run dir failed", "evaluation_id", id, "err", err)
	}
}

func writeFileAtomic(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func writeGzipJSONAtomic(dst string, v any) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	gz := gzip.NewWriter(tmp)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(v); err != nil {
		_ = gz.Close()
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := gz.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func readJSONFile(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, maxResultsBytes)).Decode(dst)
}

func readGzipJSONFile(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	return json.NewDecoder(io.LimitReader(gz, maxResultsBytes)).Decode(dst)
}

// ---- LRU cache for immutable finished evaluation data ----

type loadedEval struct {
	evalID       int64
	classes      []ClassInfo
	storeMinConf float64
	curves       CurvesData
	samples      []*evalSampleData
	sampleByID   map[int64]*evalSampleData
	sampleByKey  map[string]*evalSampleData
}

type cacheEntry struct {
	id  int64
	val *loadedEval
}

type loadedEvalCache struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[int64]*list.Element
}

func newLoadedEvalCache(capacity int) *loadedEvalCache {
	return &loadedEvalCache{
		cap:   capacity,
		ll:    list.New(),
		items: make(map[int64]*list.Element, capacity),
	}
}

func (c *loadedEvalCache) get(id int64) (*loadedEval, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry).val, true
	}
	return nil, false
}

func (c *loadedEvalCache) put(id int64, val *loadedEval) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*cacheEntry).val = val
		return
	}
	el := c.ll.PushFront(&cacheEntry{id: id, val: val})
	c.items[id] = el
	for c.ll.Len() > c.cap {
		last := c.ll.Back()
		if last == nil {
			break
		}
		ent := last.Value.(*cacheEntry)
		delete(c.items, ent.id)
		c.ll.Remove(last)
	}
}

func (c *loadedEvalCache) delete(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[id]; ok {
		delete(c.items, id)
		c.ll.Remove(el)
	}
}

// workersOnline returns the number of compute nodes that can claim evaluations.
func (s *Service) workersOnline() int {
	if s.models == nil {
		return 0
	}
	return s.models.WorkersOnline(TaskKind)
}

func (s *Service) notifyTasks() {
	if s.models != nil {
		s.models.NotifyTasks()
	}
}

func (s *Service) touchWorker(nodeID int64) {
	if s.models != nil {
		s.models.TouchWorker(nodeID, TaskKind)
	}
}

// isNotExist reports whether err is os.ErrNotExist.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
