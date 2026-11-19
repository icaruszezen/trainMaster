package storageagent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
)

// PackArchiveRequest is the JSON body of POST /api/v1/archives/pack.
type PackArchiveRequest struct {
	Space  string   `json:"space"`
	Dir    string   `json:"dir"`
	Format string   `json:"format"`
	Paths  []string `json:"paths,omitempty"`
}

// PackArchiveResponse is returned by POST /api/v1/archives/pack.
type PackArchiveResponse struct {
	ArchiveID    string    `json:"archive_id"`
	Format       string    `json:"format"`
	ArchiveBytes int64     `json:"archive_bytes"`
	SHA256       string    `json:"sha256"`
	FilesCount   int       `json:"files_count"`
	RawBytes     int64     `json:"raw_bytes"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// UnpackArchiveResponse is returned by POST /api/v1/archives/unpack and pull-unpack.
type UnpackArchiveResponse struct {
	FilesCount int                  `json:"files_count"`
	RawBytes   int64                `json:"raw_bytes"`
	ModTimes   map[string]time.Time `json:"mod_times,omitempty"`
}

// PullUnpackRequest is the JSON body of POST /api/v1/archives/pull-unpack.
type PullUnpackRequest struct {
	SourceURL            string `json:"source_url"`
	SourceToken          string `json:"source_token,omitempty"`
	InsecureSkipVerify   bool   `json:"insecure_skip_verify,omitempty"`
	ExpectedSHA256       string `json:"expected_sha256"`
	ExpectedArchiveBytes int64  `json:"expected_archive_bytes,omitempty"`
	ExpectedRawBytes     *int64 `json:"expected_raw_bytes,omitempty"`
	Space                string `json:"space"`
	Dir                  string `json:"dir"`
	Format               string `json:"format,omitempty"`
	Mode                 string `json:"mode,omitempty"`
	IncludeModTimes      bool   `json:"include_mod_times,omitempty"`
}

func validArchiveID(id string) bool {
	if len(id) < 4 || len(id) > 120 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) markActiveTemp(name string) {
	s.mu.Lock()
	s.activeFiles[name] = true
	s.mu.Unlock()
}

func (s *Server) unmarkActiveTemp(name string) {
	s.mu.Lock()
	delete(s.activeFiles, name)
	s.mu.Unlock()
}

func (s *Server) handlePackArchive(w http.ResponseWriter, r *http.Request) {
	var req PackArchiveRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	spaceDir, err := s.resolveSpaceDir(req.Space)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	cleanDir, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(req.Dir), "/"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	format, err := storagearchive.NormalizeFormat(req.Format)
	if err != nil {
		writeMappedErr(w, err)
		return
	}

	root, srcAbs, err := openScopedDirRoot(spaceDir, cleanDir)
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusBadRequest, "not_directory", "要打包的目标路径不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	_ = root.Close()

	archiveID := "pack-" + strings.ToLower(rand.Text()[:20])
	fileName := archiveID + storagearchive.FormatExtension(format)
	destPath := filepath.Join(s.tmpDir, fileName)

	s.markActiveTemp(fileName)
	res, err := storagearchive.Pack(r.Context(), storagearchive.PackOptions{
		SourceDir:       srcAbs,
		DestArchivePath: destPath,
		Format:          format,
		Paths:           req.Paths,
		FreeHeadroom:    s.freeHeadroom,
		DiskUsage:       s.diskUsage,
	})
	if err != nil {
		s.unmarkActiveTemp(fileName)
		_ = os.Remove(destPath)
		writeMappedErr(w, err)
		return
	}

	now := time.Now().UTC()
	meta := &stagedArchiveMeta{
		ID:           archiveID,
		Path:         destPath,
		Format:       res.Format,
		ArchiveBytes: res.ArchiveBytes,
		SHA256:       res.SHA256,
		FilesCount:   res.FilesCount,
		RawBytes:     res.RawBytes,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.tempTTL),
	}

	s.mu.Lock()
	delete(s.activeFiles, fileName)
	s.archives[archiveID] = meta
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, PackArchiveResponse{
		ArchiveID:    meta.ID,
		Format:       meta.Format,
		ArchiveBytes: meta.ArchiveBytes,
		SHA256:       meta.SHA256,
		FilesCount:   meta.FilesCount,
		RawBytes:     meta.RawBytes,
		ExpiresAt:    meta.ExpiresAt,
	})
}

func (s *Server) handleGetArchive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "archiveID")
	if !validArchiveID(id) {
		writeErr(w, http.StatusNotFound, "not_found", "归档包不存在")
		return
	}

	s.mu.Lock()
	meta, ok := s.archives[id]
	if ok {
		meta.inUse++
	}
	s.mu.Unlock()

	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "归档包不存在或已过期清理")
		return
	}
	defer func() {
		s.mu.Lock()
		meta.inUse--
		s.mu.Unlock()
	}()

	f, err := os.Open(meta.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeErr(w, http.StatusNotFound, "not_found", "归档包文件已不存在")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer f.Close()

	contentType := "application/zstd"
	if meta.Format == storagearchive.FormatZip {
		contentType = "application/zip"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Archive-Sha256", meta.SHA256)
	h.Set("X-Archive-Files", strconv.Itoa(meta.FilesCount))
	h.Set("X-Archive-Files-Count", strconv.Itoa(meta.FilesCount))
	h.Set("X-Archive-Raw-Bytes", strconv.FormatInt(meta.RawBytes, 10))
	h.Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(w, r, filepath.Base(meta.Path), meta.CreatedAt, f)
}

func (s *Server) handleDeleteArchive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "archiveID")
	if !validArchiveID(id) {
		writeErr(w, http.StatusBadRequest, "invalid_input", "归档 ID 无效")
		return
	}

	var pathToRemove string
	s.mu.Lock()
	if meta, ok := s.archives[id]; ok {
		pathToRemove = meta.Path
		delete(s.archives, id)
	}
	s.mu.Unlock()

	if pathToRemove != "" {
		_ = os.Remove(pathToRemove)
	} else {
		// Defensive idempotent cleanup in case registry was cleared.
		for _, ext := range []string{".tar.zst", ".zip"} {
			_ = os.Remove(filepath.Join(s.tmpDir, id+ext))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnpackArchive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	spaceDir, err := s.resolveSpaceDir(q.Get("space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	targetRel, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(q.Get("dir")), "/"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	format, err := storagearchive.NormalizeFormat(q.Get("format"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	mode, err := storagearchive.NormalizeMode(q.Get("mode"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	expectedSHA := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Archive-Sha256")))
	if !storagearchive.ValidSHA256Hex(expectedSHA) {
		writeErr(w, http.StatusBadRequest, "invalid_sha256", "缺少或无效的请求头 X-Archive-Sha256")
		return
	}

	var declaredRaw *int64
	if rawHdr := strings.TrimSpace(r.Header.Get("X-Archive-Raw-Bytes")); rawHdr != "" {
		val, perr := strconv.ParseInt(rawHdr, 10, 64)
		if perr != nil || val < 0 {
			writeErr(w, http.StatusBadRequest, "invalid_input", "请求头 X-Archive-Raw-Bytes 无效")
			return
		}
		declaredRaw = &val
	}

	if mode == storagearchive.ModeAtomic {
		if _, stErr := os.Lstat(filepath.Join(spaceDir, filepath.FromSlash(targetRel))); stErr == nil {
			writeErr(w, http.StatusConflict, "conflict", fmt.Sprintf("目标目录 %q 已存在", targetRel))
			return
		}
	}

	recvName := "recv-" + strings.ToLower(rand.Text()[:20]) + storagearchive.FormatExtension(format)
	stageName := ".unpack-" + strings.ToLower(rand.Text()[:20])
	recvPath := filepath.Join(s.tmpDir, recvName)
	s.markActiveTemp(recvName)
	s.markActiveTemp(stageName)
	defer func() {
		_ = os.Remove(recvPath)
		s.unmarkActiveTemp(recvName)
		s.unmarkActiveTemp(stageName)
	}()

	tmpFile, err := os.OpenFile(recvPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	written, copyErr := io.Copy(tmpFile, r.Body)
	if copyErr == nil && r.ContentLength >= 0 && written != r.ContentLength {
		copyErr = fmt.Errorf("%w: incomplete upload (%d of %d bytes)", storagearchive.ErrChecksumMismatch, written, r.ContentLength)
	}
	if cerr := tmpFile.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		writeMappedErr(w, copyErr)
		return
	}

	var expectedArchBytes int64
	if r.ContentLength > 0 {
		expectedArchBytes = r.ContentLength
	}
	res, err := storagearchive.Unpack(r.Context(), storagearchive.UnpackOptions{
		ArchivePath:          recvPath,
		Format:               format,
		SpaceDir:             spaceDir,
		TargetRel:            targetRel,
		Mode:                 mode,
		ExpectedSHA256:       expectedSHA,
		ExpectedArchiveBytes: expectedArchBytes,
		DeclaredRawBytes:     declaredRaw,
		FreeHeadroom:         s.freeHeadroom,
		DiskUsage:            s.diskUsage,
		CollectModTimes:      q.Get("include_mod_times") == "true",
		StageName:            stageName,
	})
	if err != nil {
		writeMappedErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, UnpackArchiveResponse{
		FilesCount: res.FilesCount,
		RawBytes:   res.RawBytes,
		ModTimes:   res.ModTimes,
	})
}

func (s *Server) handlePullUnpackArchive(w http.ResponseWriter, r *http.Request) {
	var req PullUnpackRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	spaceDir, err := s.resolveSpaceDir(req.Space)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	targetRel, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(req.Dir), "/"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	format, err := storagearchive.NormalizeFormat(req.Format)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	mode, err := storagearchive.NormalizeMode(req.Mode)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	expectedSHA := strings.ToLower(strings.TrimSpace(req.ExpectedSHA256))
	if !storagearchive.ValidSHA256Hex(expectedSHA) {
		writeErr(w, http.StatusBadRequest, "invalid_sha256", "expected_sha256 必须为 64 位十六进制 SHA-256")
		return
	}
	srcURL := strings.TrimSpace(req.SourceURL)
	parsedURL, err := url.Parse(srcURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		writeErr(w, http.StatusBadRequest, "invalid_source_url", "source_url 必须为有效的 http/https 地址")
		return
	}

	if mode == storagearchive.ModeAtomic {
		if _, stErr := os.Lstat(filepath.Join(spaceDir, filepath.FromSlash(targetRel))); stErr == nil {
			writeErr(w, http.StatusConflict, "conflict", fmt.Sprintf("目标目录 %q 已存在", targetRel))
			return
		}
	}

	recvName := "pull-" + strings.ToLower(rand.Text()[:20]) + storagearchive.FormatExtension(format)
	stageName := ".unpack-" + strings.ToLower(rand.Text()[:20])
	recvPath := filepath.Join(s.tmpDir, recvName)
	s.markActiveTemp(recvName)
	s.markActiveTemp(stageName)
	defer func() {
		_ = os.Remove(recvPath)
		s.unmarkActiveTemp(recvName)
		s.unmarkActiveTemp(stageName)
	}()

	client := NewDirectHTTPClient(req.InsecureSkipVerify, 0)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, srcURL, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_source_url", err.Error())
		return
	}
	if req.SourceToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.SourceToken)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "pull_failed", "从源存储节点拉取归档包失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeErr(w, http.StatusBadGateway, "pull_failed",
			fmt.Sprintf("源存储节点返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet))))
		return
	}

	tmpFile, err := os.OpenFile(recvPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	written, copyErr := io.Copy(tmpFile, resp.Body)
	if copyErr == nil && resp.ContentLength >= 0 && written != resp.ContentLength {
		copyErr = fmt.Errorf("%w: incomplete pull (%d of %d bytes)", storagearchive.ErrChecksumMismatch, written, resp.ContentLength)
	}
	if cerr := tmpFile.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		writeMappedErr(w, copyErr)
		return
	}

	res, err := storagearchive.Unpack(r.Context(), storagearchive.UnpackOptions{
		ArchivePath:          recvPath,
		Format:               format,
		SpaceDir:             spaceDir,
		TargetRel:            targetRel,
		Mode:                 mode,
		ExpectedSHA256:       expectedSHA,
		ExpectedArchiveBytes: req.ExpectedArchiveBytes,
		DeclaredRawBytes:     req.ExpectedRawBytes,
		FreeHeadroom:         s.freeHeadroom,
		DiskUsage:            s.diskUsage,
		CollectModTimes:      req.IncludeModTimes,
		StageName:            stageName,
	})
	if err != nil {
		writeMappedErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, UnpackArchiveResponse{
		FilesCount: res.FilesCount,
		RawBytes:   res.RawBytes,
		ModTimes:   res.ModTimes,
	})
}
