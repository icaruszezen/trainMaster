package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/buildinfo"
)

const (
	statusOK          = "ok"
	statusUnavailable = "unavailable"

	// checkTimeout bounds each dependency check so probes answer quickly.
	checkTimeout = 2 * time.Second
)

// healthz is the liveness probe. It must not depend on anything external.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": statusOK})
}

type check struct {
	Status    string  `json:"status"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Error     string  `json:"error,omitempty"`
}

type readiness struct {
	Status string           `json:"status"`
	Checks map[string]check `json:"checks"`
}

// readyz is the readiness probe: 200 when every dependency works, else 503.
func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	resp := readiness{
		Status: statusOK,
		Checks: map[string]check{
			"database": s.checkDatabase(r.Context()),
			"storage":  s.checkStorage(),
		},
	}
	code := http.StatusOK
	for _, c := range resp.Checks {
		if c.Status != statusOK {
			resp.Status, code = statusUnavailable, http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, resp)
}

func (s *server) checkDatabase(ctx context.Context) check {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	start := time.Now()
	if err := s.db.Ping(ctx); err != nil {
		return check{Status: statusUnavailable, Error: err.Error()}
	}
	return check{Status: statusOK, LatencyMS: milliseconds(time.Since(start))}
}

func (s *server) checkStorage() check {
	if err := s.storage.Check(); err != nil {
		return check{Status: statusUnavailable, Error: err.Error()}
	}
	return check{Status: statusOK}
}

// The field names below are a contract with the frontend
// (frontend/src/features/system/api.ts).
type systemInfo struct {
	Name          string       `json:"name"`
	Version       string       `json:"version"`
	Commit        string       `json:"commit"`
	BuildTime     string       `json:"build_time"`
	GoVersion     string       `json:"go_version"`
	StartedAt     time.Time    `json:"started_at"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	Database      databaseInfo `json:"database"`
	Storage       storageInfo  `json:"storage"`
}

type databaseInfo struct {
	Status        string `json:"status"`
	ServerVersion string `json:"server_version,omitempty"`
	SchemaVersion *int64 `json:"schema_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

type storageInfo struct {
	Root        string    `json:"root"`
	TotalBytes  uint64    `json:"total_bytes"`
	FreeBytes   uint64    `json:"free_bytes"`
	UsedBytes   uint64    `json:"used_bytes"`
	Directories []dirInfo `json:"directories"`
}

type dirInfo struct {
	Key      string `json:"key"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

// systemInfo reports build, database and storage details. It always answers
// 200; a broken dependency shows up in its section instead.
func (s *server) systemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, systemInfo{
		Name:          buildinfo.Name,
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		BuildTime:     buildinfo.BuildTime,
		GoVersion:     buildinfo.GoVersion(),
		StartedAt:     s.startedAt.UTC().Truncate(time.Second),
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Database:      s.databaseInfo(r.Context()),
		Storage:       s.storageInfo(),
	})
}

func (s *server) databaseInfo(ctx context.Context) databaseInfo {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	version, err := s.db.ServerVersion(ctx)
	if err != nil {
		return databaseInfo{Status: statusUnavailable, Error: err.Error()}
	}
	schema, err := s.db.SchemaVersion(ctx)
	if err != nil {
		return databaseInfo{Status: statusUnavailable, ServerVersion: version, Error: err.Error()}
	}
	return databaseInfo{Status: statusOK, ServerVersion: version, SchemaVersion: &schema}
}

func (s *server) storageInfo() storageInfo {
	info := storageInfo{Root: s.storage.Root()}
	// On platforms without disk usage support the sizes stay 0.
	if u, err := s.storage.Usage(); err == nil {
		info.TotalBytes, info.FreeBytes, info.UsedBytes = u.TotalBytes, u.FreeBytes, u.UsedBytes
	}
	for _, d := range s.storage.Directories() {
		info.Directories = append(info.Directories, dirInfo{Key: string(d.Key), Path: d.Path, Writable: d.Writable})
	}
	return info
}
