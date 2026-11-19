// Package storageagent implements the standalone trainMaster storage node HTTP
// daemon (cmd/trainmaster-storage), providing confined POSIX file/directory
// operations and high-throughput dual-format (.tar.zst / .zip) archive pack,
// stream, pull-unpack, and TTL cleanup over a single /data mount.
package storageagent

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
	"github.com/icaruszezen/trainmaster/backend/internal/storage"
	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
)

// Managed logical spaces under DataDir.
const (
	SpaceDatasets  = "datasets"
	SpaceExports   = "exports"
	TmpArchivesDir = ".tmp-archives"

	DefaultTempTTL = 2 * time.Hour
	keepMarkerFile = ".tm-keep"
)

// Options configures a storageagent Server.
type Options struct {
	DataDir      string
	Token        string
	TempTTL      time.Duration
	Logger       *slog.Logger
	DiskUsage    storagearchive.DiskUsageFunc
	FreeHeadroom *uint64
}

type stagedArchiveMeta struct {
	ID           string    `json:"archive_id"`
	Path         string    `json:"-"`
	Format       string    `json:"format"`
	ArchiveBytes int64     `json:"archive_bytes"`
	SHA256       string    `json:"sha256"`
	FilesCount   int       `json:"files_count"`
	RawBytes     int64     `json:"raw_bytes"`
	CreatedAt    time.Time `json:"-"`
	ExpiresAt    time.Time `json:"expires_at"`
	inUse        int
}

// Server serves the trainMaster storage node HTTP API.
type Server struct {
	dataDir      string
	tmpDir       string
	token        string
	tempTTL      time.Duration
	log          *slog.Logger
	diskUsage    storagearchive.DiskUsageFunc
	freeHeadroom *uint64
	router       chi.Router

	mu          sync.Mutex
	archives    map[string]*stagedArchiveMeta
	activeFiles map[string]bool // basename in .tmp-archives currently being written/unpacked

	stopOnce sync.Once
	stopJan  context.CancelFunc
	janWG    sync.WaitGroup
}

// NewServer initializes DataDir subdirectories (datasets, exports, .tmp-archives),
// sweeps stale leftover temp artifacts, and starts the background TTL janitor.
func NewServer(opts Options) (*Server, error) {
	rawDir := strings.TrimSpace(opts.DataDir)
	if rawDir == "" {
		rawDir = "/data"
	}
	absDir, err := filepath.Abs(rawDir)
	if err != nil {
		return nil, fmt.Errorf("resolve storage data dir %q: %w", rawDir, err)
	}
	for _, sub := range []string{SpaceDatasets, SpaceExports, TmpArchivesDir} {
		if err := os.MkdirAll(filepath.Join(absDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create storage subdir %s: %w", sub, err)
		}
	}

	ttl := opts.TempTTL
	if ttl <= 0 {
		ttl = DefaultTempTTL
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	usageFn := opts.DiskUsage
	if usageFn == nil {
		usageFn = storage.DiskUsage
	}

	s := &Server{
		dataDir:      absDir,
		tmpDir:       filepath.Join(absDir, TmpArchivesDir),
		token:        opts.Token,
		tempTTL:      ttl,
		log:          log,
		diskUsage:    usageFn,
		freeHeadroom: opts.FreeHeadroom,
		archives:     make(map[string]*stagedArchiveMeta),
		activeFiles:  make(map[string]bool),
	}

	// Startup sweep removes stale temp archives and .unpack-* dirs older than TTL.
	s.SweepTemp(time.Now())

	janCtx, cancel := context.WithCancel(context.Background())
	s.stopJan = cancel
	s.janWG.Add(1)
	go func() {
		defer s.janWG.Done()
		s.janitorLoop(janCtx)
	}()

	s.router = s.buildRouter()
	return s, nil
}

// Handler returns the HTTP handler for the storage service.
func (s *Server) Handler() http.Handler {
	return s.router
}

// DataDir returns the resolved absolute root path.
func (s *Server) DataDir() string {
	return s.dataDir
}

// Close stops the background janitor goroutine.
func (s *Server) Close() error {
	s.stopOnce.Do(func() {
		if s.stopJan != nil {
			s.stopJan()
		}
		s.janWG.Wait()
	})
	return nil
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)
	r.Head("/healthz", s.handleHealthz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.requireBearerToken)

		r.Get("/info", s.handleInfo)

		r.Post("/spaces/copy-within", s.handleCopyWithin)
		r.Route("/spaces/{space}", func(r chi.Router) {
			r.Get("/dirs", s.handleGetDirs)
			r.Post("/dirs", s.handleCreateDir)
			r.Patch("/dirs", s.handleRenameDir)
			r.Delete("/dirs", s.handleDeleteDir)

			r.Get("/walk", s.handleWalk)

			r.Head("/files", s.handleHeadFile)
			r.Get("/files", s.handleGetFile)
			r.Put("/files", s.handlePutFile)
			r.Patch("/files", s.handleRenameFile)
			r.Delete("/files", s.handleDeleteFile)
		})

		r.Route("/archives", func(r chi.Router) {
			r.Post("/pack", s.handlePackArchive)
			r.Post("/unpack", s.handleUnpackArchive)
			r.Post("/pull-unpack", s.handlePullUnpackArchive)
			r.Head("/{archiveID}", s.handleGetArchive)
			r.Get("/{archiveID}", s.handleGetArchive)
			r.Delete("/{archiveID}", s.handleDeleteArchive)
		})
	})

	return r
}

func (s *Server) requireBearerToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got, ok := extractBearer(r.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="trainmaster-storage"`)
			writeErr(w, http.StatusUnauthorized, "unauthorized", "存储节点访问令牌无效或未提供")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(header string) (string, bool) {
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(rest)
	if tok == "" {
		return "", false
	}
	return tok, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "trainmaster-storage",
		"version": buildinfo.Version,
	})
}

// InfoResponse is returned by GET /api/v1/info.
type InfoResponse struct {
	OK              bool      `json:"ok"`
	Hostname        string    `json:"hostname"`
	DisplayPath     string    `json:"display_path"`
	SupportsArchive bool      `json:"supports_archive"`
	Usage           UsageBody `json:"usage"`
}

// UsageBody reports disk capacity of the storage container's /data volume.
type UsageBody struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	for _, sp := range []string{SpaceDatasets, SpaceExports, TmpArchivesDir} {
		dir := filepath.Join(s.dataDir, sp)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "storage_unavailable", fmt.Sprintf("无法创建存储目录 %s: %v", dir, err))
			return
		}
		f, err := os.CreateTemp(dir, ".tm-probe-*")
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "storage_unavailable", fmt.Sprintf("存储目录 %s 不可写: %v", dir, err))
			return
		}
		name := f.Name()
		_, werr := f.WriteString("ok")
		cerr := f.Close()
		rerr := os.Remove(name)
		if err := errors.Join(werr, cerr, rerr); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "storage_unavailable", fmt.Sprintf("存储目录 %s 读写校验失败: %v", dir, err))
			return
		}
	}

	hostname, _ := os.Hostname()
	var usage UsageBody
	if u, err := s.diskUsage(s.dataDir); err == nil && u.TotalBytes > 0 {
		used := u.UsedBytes
		if used == 0 && u.TotalBytes > u.FreeBytes {
			used = u.TotalBytes - u.FreeBytes
		}
		usage = UsageBody{
			TotalBytes: u.TotalBytes,
			FreeBytes:  u.FreeBytes,
			UsedBytes:  used,
		}
	}

	writeJSON(w, http.StatusOK, InfoResponse{
		OK:              true,
		Hostname:        hostname,
		DisplayPath:     s.dataDir,
		SupportsArchive: true,
		Usage:           usage,
	})
}

func (s *Server) janitorLoop(ctx context.Context) {
	interval := s.tempTTL / 2
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	if interval > 15*time.Minute {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.SweepTemp(now)
		}
	}
}

// SweepTemp deletes expired staged archives in .tmp-archives and orphaned
// .unpack-* directories older than TempTTL while skipping in-flight files.
func (s *Server) SweepTemp(now time.Time) {
	var toDeletePaths []string
	activeNames := make(map[string]bool)

	s.mu.Lock()
	for name := range s.activeFiles {
		activeNames[name] = true
	}
	for id, meta := range s.archives {
		base := filepath.Base(meta.Path)
		if !now.Before(meta.ExpiresAt) && meta.inUse == 0 {
			toDeletePaths = append(toDeletePaths, meta.Path)
			delete(s.archives, id)
		} else {
			activeNames[base] = true
		}
	}
	s.mu.Unlock()

	for _, p := range toDeletePaths {
		_ = os.Remove(p)
	}

	if entries, err := os.ReadDir(s.tmpDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".tm-probe-") || isNameOrPartActive(name, activeNames) {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			if now.Sub(info.ModTime()) >= s.tempTTL {
				_ = os.RemoveAll(filepath.Join(s.tmpDir, name))
			}
		}
	}

	for _, sp := range []string{SpaceDatasets, SpaceExports} {
		spDir := filepath.Join(s.dataDir, sp)
		entries, err := os.ReadDir(spDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, ".unpack-") || activeNames[name] {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			if now.Sub(info.ModTime()) >= s.tempTTL {
				_ = os.RemoveAll(filepath.Join(spDir, name))
			}
		}
	}
}

func isNameOrPartActive(name string, activeNames map[string]bool) bool {
	if activeNames[name] {
		return true
	}
	if idx := strings.Index(name, ".part-"); idx > 0 {
		return activeNames[name[:idx]]
	}
	return false
}

// NewDirectHTTPClient creates an HTTP client that bypasses environment proxy
// variables (HTTP_PROXY/HTTPS_PROXY) so container-to-container and loopback
// transfers are never hijacked by corporate or sandbox proxies.
func NewDirectHTTPClient(insecureSkipVerify bool, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in per storage node config
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

func validSpace(space string) bool {
	return space == SpaceDatasets || space == SpaceExports
}

func cleanOptionalRel(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	return storagearchive.CleanRelPath(trimmed)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体 JSON 格式无效: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func writeMappedErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storagearchive.ErrTargetExists), errors.Is(err, fs.ErrExist):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, storagearchive.ErrInsufficientStorage), errors.Is(err, syscall.ENOSPC):
		writeErr(w, http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	case errors.Is(err, storagearchive.ErrChecksumMismatch):
		writeErr(w, http.StatusBadRequest, "checksum_mismatch", err.Error())
	case errors.Is(err, storagearchive.ErrUnsafeArchiveEntry):
		writeErr(w, http.StatusBadRequest, "unsafe_archive_entry", err.Error())
	case errors.Is(err, storagearchive.ErrArchiveTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, "archive_too_large", err.Error())
	case errors.Is(err, storagearchive.ErrInvalidArchiveInput), errors.Is(err, storage.ErrInvalidPath):
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
