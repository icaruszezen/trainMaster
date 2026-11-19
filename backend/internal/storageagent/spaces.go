package storageagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/storagearchive"
)

var errNotPlainDir = errors.New("path component is not a plain directory")

func (s *Server) resolveSpaceDir(space string) (string, error) {
	if !validSpace(space) {
		return "", fmt.Errorf("%w: unknown space %q", storagearchive.ErrInvalidArchiveInput, space)
	}
	return filepath.Join(s.dataDir, space), nil
}

func ensureSubdirsInRoot(root *os.Root, cleanRel string) error {
	if cleanRel == "" || cleanRel == "." {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(cleanRel, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		osCur := filepath.FromSlash(cur)
		fi, err := root.Lstat(osCur)
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := root.Mkdir(osCur, 0o755); mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
				return mkErr
			}
			fi, err = root.Lstat(osCur)
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return errNotPlainDir
		}
	}
	return nil
}

func openScopedDirRoot(spaceDir, dirRel string) (*os.Root, string, error) {
	cleanDir, err := cleanOptionalRel(dirRel)
	if err != nil {
		return nil, "", err
	}
	if cleanDir == "" {
		r, err := os.OpenRoot(spaceDir)
		return r, spaceDir, err
	}
	spaceRoot, err := os.OpenRoot(spaceDir)
	if err != nil {
		return nil, "", err
	}
	defer spaceRoot.Close()

	// Verify every segment of cleanDir is a real directory (not a symlink or file).
	cur := ""
	for _, seg := range strings.Split(cleanDir, "/") {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		fi, err := spaceRoot.Lstat(filepath.FromSlash(cur))
		if err != nil {
			return nil, "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return nil, "", errNotPlainDir
		}
	}
	absTarget := filepath.Join(spaceDir, filepath.FromSlash(cleanDir))
	r, err := os.OpenRoot(absTarget)
	return r, absTarget, err
}

// TopDirItem describes a top-level directory under a space or prefix.
type TopDirItem struct {
	Name       string    `json:"name"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Server) handleGetDirs(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	op := r.URL.Query().Get("op")
	switch op {
	case "exists":
		cleanDir, err := cleanOptionalRel(r.URL.Query().Get("dir"))
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		spaceRoot, err := os.OpenRoot(spaceDir)
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		defer spaceRoot.Close()

		target := "."
		if cleanDir != "" {
			target = filepath.FromSlash(cleanDir)
		}
		fi, err := spaceRoot.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}
		if err != nil {
			// If an intermediate path component is missing, DirExists is false.
			writeJSON(w, http.StatusOK, map[string]any{"exists": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"exists":      true,
			"modified_at": fi.ModTime().UTC(),
		})

	case "is_plain_dir":
		cleanDir, err := cleanOptionalRel(r.URL.Query().Get("dir"))
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		root, _, err := openScopedDirRoot(spaceDir, cleanDir)
		if err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "目标路径不是普通目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
		_ = root.Close()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "read_names":
		dirRel := r.URL.Query().Get("dir")
		subRel, err := cleanOptionalRel(r.URL.Query().Get("rel"))
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		root, _, err := openScopedDirRoot(spaceDir, dirRel)
		if err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "目标路径不是普通目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
		defer root.Close()

		target := "."
		if subRel != "" {
			target = filepath.FromSlash(subRel)
		}
		f, err := root.Open(target)
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		defer f.Close()
		raw, err := f.Readdirnames(-1)
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		names := make([]string, 0, len(raw))
		for _, name := range raw {
			if name == keepMarkerFile || strings.HasPrefix(name, ".tm-tmp-") {
				continue
			}
			names = append(names, name)
		}
		writeJSON(w, http.StatusOK, map[string]any{"names": names})

	case "", "list_top":
		prefix, err := cleanOptionalRel(r.URL.Query().Get("prefix"))
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		targetAbs := spaceDir
		if prefix != "" {
			root, abs, oerr := openScopedDirRoot(spaceDir, prefix)
			if errors.Is(oerr, fs.ErrNotExist) {
				writeJSON(w, http.StatusOK, map[string]any{"items": []TopDirItem{}})
				return
			}
			if oerr != nil {
				if errors.Is(oerr, errNotPlainDir) {
					writeErr(w, http.StatusConflict, "not_directory", "目标路径不是普通目录")
					return
				}
				writeMappedErr(w, oerr)
				return
			}
			_ = root.Close()
			targetAbs = abs
		}
		entries, err := os.ReadDir(targetAbs)
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]any{"items": []TopDirItem{}})
			return
		}
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		items := make([]TopDirItem, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".unpack-") || strings.HasPrefix(name, ".tm-") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			items = append(items, TopDirItem{Name: name, ModifiedAt: info.ModTime().UTC()})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})

	default:
		writeErr(w, http.StatusBadRequest, "invalid_op", fmt.Sprintf("未知目录操作 %q", op))
	}
}

type createDirReq struct {
	Dir       string `json:"dir"`
	Exclusive bool   `json:"exclusive"`
	EnsureRel string `json:"ensure_rel"`
}

func (s *Server) handleCreateDir(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	var req createDirReq
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.EnsureRel) != "" {
		ensureClean, err := cleanOptionalRel(req.EnsureRel)
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		root, _, err := openScopedDirRoot(spaceDir, req.Dir)
		if err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "目标路径不是普通目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
		defer root.Close()
		if err := ensureSubdirsInRoot(root, ensureClean); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "路径组件不是目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	cleanDir, err := cleanOptionalRel(req.Dir)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	if cleanDir == "" {
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
		return
	}

	spaceRoot, err := os.OpenRoot(spaceDir)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	defer spaceRoot.Close()

	parentRel := path.Dir(cleanDir)
	if parentRel != "." && parentRel != "" {
		if err := ensureSubdirsInRoot(spaceRoot, parentRel); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "父路径包含非目录组件")
				return
			}
			writeMappedErr(w, err)
			return
		}
	}
	osDir := filepath.FromSlash(cleanDir)
	if req.Exclusive {
		if err := spaceRoot.Mkdir(osDir, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				writeErr(w, http.StatusConflict, "already_exists", "目录已存在")
				return
			}
			writeMappedErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
		return
	}
	if err := ensureSubdirsInRoot(spaceRoot, cleanDir); err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "目标路径已被文件或符号链接占用")
			return
		}
		writeMappedErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

type renameDirReq struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) handleRenameDir(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	var req renameDirReq
	if !decodeJSONBody(w, r, &req) {
		return
	}
	fromClean, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(req.From), "/"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	toClean, err := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(req.To), "/"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}

	spaceRoot, err := os.OpenRoot(spaceDir)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	defer spaceRoot.Close()

	if parentFrom := path.Dir(fromClean); parentFrom != "." && parentFrom != "" {
		if _, _, err := openScopedDirRoot(spaceDir, parentFrom); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "源父路径不是目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
	}
	if parentTo := path.Dir(toClean); parentTo != "." && parentTo != "" {
		if err := ensureSubdirsInRoot(spaceRoot, parentTo); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "目标父路径不是目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
	}
	fromAbs := filepath.Join(spaceDir, filepath.FromSlash(fromClean))
	toAbs := filepath.Join(spaceDir, filepath.FromSlash(toClean))
	if err := os.Rename(fromAbs, toAbs); err != nil {
		writeMappedErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteDir(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	rawDir := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("dir")), "/")
	if rawDir == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "缺少要删除的子目录参数 dir")
		return
	}
	cleanDir, err := storagearchive.CleanRelPath(rawDir)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	// Ensure parent chain does not traverse symlinks.
	if parent := path.Dir(cleanDir); parent != "." && parent != "" {
		rRoot, _, oerr := openScopedDirRoot(spaceDir, parent)
		if errors.Is(oerr, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if oerr != nil {
			if errors.Is(oerr, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "父路径不是普通目录")
				return
			}
			writeMappedErr(w, oerr)
			return
		}
		_ = rRoot.Close()
	}

	targetAbs := filepath.Join(spaceDir, filepath.FromSlash(cleanDir))
	emptyOnly := r.URL.Query().Get("empty_only") == "true" || r.URL.Query().Get("recursive") == "false"
	if emptyOnly {
		if err := os.Remove(targetAbs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				writeErr(w, http.StatusConflict, "not_empty", "目录非空，拒绝执行非递归删除")
				return
			}
			writeMappedErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := removeTreeCtx(r.Context(), targetAbs); err != nil {
		writeMappedErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func removeTreeCtx(ctx context.Context, p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := removeTreeCtx(ctx, filepath.Join(p, e.Name())); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// WalkEntry is one entry emitted by GET /api/v1/spaces/{space}/walk.
type WalkEntry struct {
	Rel       string `json:"rel"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	ModTimeNs int64  `json:"mod_time_ns"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
}

func (s *Server) handleWalk(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, absTarget, err := openScopedDirRoot(spaceDir, r.URL.Query().Get("dir"))
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "目标路径不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"items":[`)
	first := true
	enc := json.NewEncoder(w)

	_ = filepath.WalkDir(absTarget, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || p == absTarget {
			return nil
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		relOS, err := filepath.Rel(absTarget, p)
		if err != nil {
			return nil
		}
		rel := filepath.ToSlash(relOS)
		base := path.Base(rel)
		if base == keepMarkerFile || strings.HasPrefix(base, ".tm-tmp-") {
			return nil
		}

		isSymlink := d.Type()&fs.ModeSymlink != 0
		info, ierr := d.Info()
		var item WalkEntry
		if ierr != nil {
			if !isSymlink {
				return nil
			}
			item = WalkEntry{Rel: rel, Mode: uint32(0o777), IsSymlink: true}
		} else {
			item = WalkEntry{
				Rel:       rel,
				Size:      info.Size(),
				Mode:      uint32(info.Mode().Perm()),
				ModTimeNs: info.ModTime().UTC().UnixNano(),
				IsDir:     !isSymlink && d.IsDir(),
				IsSymlink: isSymlink,
			}
		}
		if !first {
			_, _ = io.WriteString(w, ",")
		}
		first = false
		_ = enc.Encode(item)
		return nil
	})
	_, _ = io.WriteString(w, "]}\n")
}

func setFileMetaHeaders(h http.Header, fi fs.FileInfo, isSymlink bool) {
	h.Set("X-File-Name", fi.Name())
	h.Set("X-File-Size", strconv.FormatInt(fi.Size(), 10))
	h.Set("X-File-Mode", strconv.FormatUint(uint64(fi.Mode().Perm()), 10))
	h.Set("X-File-ModTime-Ns", strconv.FormatInt(fi.ModTime().UTC().UnixNano(), 10))
	h.Set("X-File-Is-Dir", strconv.FormatBool(!isSymlink && fi.IsDir()))
	h.Set("X-File-Is-Symlink", strconv.FormatBool(isSymlink))
}

func (s *Server) handleHeadFile(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	relClean, err := storagearchive.CleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, _, err := openScopedDirRoot(spaceDir, r.URL.Query().Get("dir"))
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "父目录不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	osRel := filepath.FromSlash(relClean)
	useStat := r.URL.Query().Get("lstat") == "false"
	var fi fs.FileInfo
	if useStat {
		fi, err = root.Stat(osRel)
	} else {
		fi, err = root.Lstat(osRel)
	}
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	isSym := fi.Mode()&fs.ModeSymlink != 0
	setFileMetaHeaders(w.Header(), fi, isSym)
	if fi.Mode().IsRegular() {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	relClean, err := storagearchive.CleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, _, err := openScopedDirRoot(spaceDir, r.URL.Query().Get("dir"))
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "父目录不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	osRel := filepath.FromSlash(relClean)
	f, err := root.Open(osRel)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	if !fi.Mode().IsRegular() {
		writeErr(w, http.StatusConflict, "not_regular_file", "目标路径不是普通文件")
		return
	}
	setFileMetaHeaders(w.Header(), fi, false)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, path.Base(relClean), fi.ModTime().UTC(), f)
}

// FileInfoResponse is returned by PUT /api/v1/spaces/{space}/files.
type FileInfoResponse struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	ModTimeNs int64  `json:"mod_time_ns"`
}

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	relClean, err := storagearchive.CleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, absDir, err := openScopedDirRoot(spaceDir, r.URL.Query().Get("dir"))
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "父目录不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	parentRel := path.Dir(relClean)
	if parentRel != "." && parentRel != "" {
		if err := ensureSubdirsInRoot(root, parentRel); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "父路径组件不是目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
	}

	perm := fs.FileMode(0o644)
	if rawPerm := r.URL.Query().Get("perm"); rawPerm != "" {
		if parsed, perr := strconv.ParseUint(rawPerm, 8, 32); perr == nil && parsed > 0 {
			perm = fs.FileMode(parsed) & 0o777
		}
	}
	exclusive := r.URL.Query().Get("exclusive") == "true"
	osRel := filepath.FromSlash(relClean)

	if exclusive {
		f, err := root.OpenFile(osRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				writeErr(w, http.StatusConflict, "already_exists", "文件已存在")
				return
			}
			writeMappedErr(w, err)
			return
		}
		n, werr := io.Copy(f, r.Body)
		if werr == nil && r.ContentLength >= 0 && n != r.ContentLength {
			werr = io.ErrUnexpectedEOF
		}
		if werr == nil {
			_ = f.Chmod(perm)
			werr = f.Sync()
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = root.Remove(osRel)
			writeMappedErr(w, werr)
			return
		}
		fi, err := root.Lstat(osRel)
		if err != nil {
			writeMappedErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, FileInfoResponse{
			Name:      fi.Name(),
			Size:      fi.Size(),
			Mode:      uint32(fi.Mode().Perm()),
			ModTimeNs: fi.ModTime().UTC().UnixNano(),
		})
		return
	}

	dirOS := filepath.Dir(osRel)
	tmpOS := filepath.Join(dirOS, ".tm-tmp-"+strings.ToLower(rand.Text()[:12]))
	f, err := root.OpenFile(tmpOS, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	n, werr := io.Copy(f, r.Body)
	if werr == nil && r.ContentLength >= 0 && n != r.ContentLength {
		werr = io.ErrUnexpectedEOF
	}
	if werr == nil {
		_ = f.Chmod(perm)
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(filepath.Join(absDir, tmpOS), filepath.Join(absDir, osRel))
	}
	if werr != nil {
		_ = root.Remove(tmpOS)
		writeMappedErr(w, werr)
		return
	}
	fi, err := root.Lstat(osRel)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, FileInfoResponse{
		Name:      fi.Name(),
		Size:      fi.Size(),
		Mode:      uint32(fi.Mode().Perm()),
		ModTimeNs: fi.ModTime().UTC().UnixNano(),
	})
}

type renameFileReq struct {
	Dir     string `json:"dir"`
	From    string `json:"from"`
	To      string `json:"to"`
	Replace bool   `json:"replace"`
}

func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	var req renameFileReq
	if !decodeJSONBody(w, r, &req) {
		return
	}
	fromClean, err := storagearchive.CleanRelPath(req.From)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	toClean, err := storagearchive.CleanRelPath(req.To)
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, absDir, err := openScopedDirRoot(spaceDir, req.Dir)
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "父目录不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	if parentTo := path.Dir(toClean); parentTo != "." && parentTo != "" {
		if err := ensureSubdirsInRoot(root, parentTo); err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "目标父路径不是普通目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
	}
	fromOS := filepath.FromSlash(fromClean)
	toOS := filepath.FromSlash(toClean)
	if !req.Replace {
		if _, err := root.Lstat(toOS); err == nil {
			writeErr(w, http.StatusConflict, "already_exists", "目标文件已存在")
			return
		}
	}
	if err := os.Rename(filepath.Join(absDir, fromOS), filepath.Join(absDir, toOS)); err != nil {
		writeMappedErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	spaceDir, err := s.resolveSpaceDir(chi.URLParam(r, "space"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	relClean, err := storagearchive.CleanRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeMappedErr(w, err)
		return
	}
	root, _, err := openScopedDirRoot(spaceDir, r.URL.Query().Get("dir"))
	if errors.Is(err, fs.ErrNotExist) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		if errors.Is(err, errNotPlainDir) {
			writeErr(w, http.StatusConflict, "not_directory", "父目录不是普通目录")
			return
		}
		writeMappedErr(w, err)
		return
	}
	defer root.Close()

	if err := root.Remove(filepath.FromSlash(relClean)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		writeMappedErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CopyWithinItem requests hardlinking or copying one file within the same storage container.
type CopyWithinItem struct {
	SrcSpace string `json:"src_space"`
	SrcDir   string `json:"src_dir,omitempty"`
	SrcRel   string `json:"src_rel"`
	DstSpace string `json:"dst_space"`
	DstDir   string `json:"dst_dir,omitempty"`
	DstRel   string `json:"dst_rel"`
}

// CopyWithinResult reports the outcome for one CopyWithinItem.
type CopyWithinResult struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	ModTimeNs int64  `json:"mod_time_ns"`
	Linked    bool   `json:"linked"`
}

type copyWithinBatchReq struct {
	CopyWithinItem
	Items []CopyWithinItem `json:"items"`
}

func (s *Server) handleCopyWithin(w http.ResponseWriter, r *http.Request) {
	var req copyWithinBatchReq
	if !decodeJSONBody(w, r, &req) {
		return
	}
	single := len(req.Items) == 0
	items := req.Items
	if single {
		items = []CopyWithinItem{req.CopyWithinItem}
	}

	results := make([]CopyWithinResult, 0, len(items))
	for _, it := range items {
		res, err := s.copySingleWithin(it)
		if err != nil {
			if errors.Is(err, errNotPlainDir) {
				writeErr(w, http.StatusConflict, "not_directory", "路径组件不是目录")
				return
			}
			writeMappedErr(w, err)
			return
		}
		results = append(results, res)
	}
	if single {
		writeJSON(w, http.StatusOK, results[0])
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (s *Server) copySingleWithin(it CopyWithinItem) (CopyWithinResult, error) {
	srcSpaceDir, err := s.resolveSpaceDir(it.SrcSpace)
	if err != nil {
		return CopyWithinResult{}, err
	}
	dstSpaceDir, err := s.resolveSpaceDir(it.DstSpace)
	if err != nil {
		return CopyWithinResult{}, err
	}
	srcRaw := it.SrcRel
	if strings.TrimSpace(it.SrcDir) != "" {
		dirClean, derr := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(it.SrcDir), "/"))
		if derr != nil {
			return CopyWithinResult{}, derr
		}
		relClean, rerr := storagearchive.CleanRelPath(it.SrcRel)
		if rerr != nil {
			return CopyWithinResult{}, rerr
		}
		srcRaw = dirClean + "/" + relClean
	}
	dstRaw := it.DstRel
	if strings.TrimSpace(it.DstDir) != "" {
		dirClean, derr := storagearchive.CleanRelPath(strings.TrimSuffix(strings.TrimSpace(it.DstDir), "/"))
		if derr != nil {
			return CopyWithinResult{}, derr
		}
		relClean, rerr := storagearchive.CleanRelPath(it.DstRel)
		if rerr != nil {
			return CopyWithinResult{}, rerr
		}
		dstRaw = dirClean + "/" + relClean
	}
	srcClean, err := storagearchive.CleanRelPath(srcRaw)
	if err != nil {
		return CopyWithinResult{}, err
	}
	dstClean, err := storagearchive.CleanRelPath(dstRaw)
	if err != nil {
		return CopyWithinResult{}, err
	}

	srcRoot, err := os.OpenRoot(srcSpaceDir)
	if err != nil {
		return CopyWithinResult{}, err
	}
	defer srcRoot.Close()

	if srcParent := path.Dir(srcClean); srcParent != "." && srcParent != "" {
		pRoot, _, perr := openScopedDirRoot(srcSpaceDir, srcParent)
		if perr != nil {
			return CopyWithinResult{}, perr
		}
		_ = pRoot.Close()
	}
	srcOS := filepath.FromSlash(srcClean)
	srcInfo, err := srcRoot.Lstat(srcOS)
	if err != nil {
		return CopyWithinResult{}, err
	}
	if !srcInfo.Mode().IsRegular() {
		return CopyWithinResult{}, fmt.Errorf("%w: source %q is not a regular file", storagearchive.ErrInvalidArchiveInput, srcClean)
	}

	dstRoot, err := os.OpenRoot(dstSpaceDir)
	if err != nil {
		return CopyWithinResult{}, err
	}
	defer dstRoot.Close()

	if dstParent := path.Dir(dstClean); dstParent != "." && dstParent != "" {
		if err := ensureSubdirsInRoot(dstRoot, dstParent); err != nil {
			return CopyWithinResult{}, err
		}
	}

	srcAbs := filepath.Join(srcSpaceDir, srcOS)
	dstOS := filepath.FromSlash(dstClean)
	dstAbs := filepath.Join(dstSpaceDir, dstOS)

	_ = dstRoot.Remove(dstOS)
	if linkErr := os.Link(srcAbs, dstAbs); linkErr == nil {
		if li, stErr := dstRoot.Lstat(dstOS); stErr == nil && os.SameFile(srcInfo, li) {
			return CopyWithinResult{
				Name:      li.Name(),
				Size:      li.Size(),
				Mode:      uint32(li.Mode().Perm()),
				ModTimeNs: li.ModTime().UTC().UnixNano(),
				Linked:    true,
			}, nil
		}
		_ = dstRoot.Remove(dstOS)
	}

	in, err := srcRoot.Open(srcOS)
	if err != nil {
		return CopyWithinResult{}, err
	}
	defer in.Close()

	tmpOS := filepath.Join(filepath.Dir(dstOS), ".tm-tmp-"+strings.ToLower(rand.Text()[:12]))
	out, err := dstRoot.OpenFile(tmpOS, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return CopyWithinResult{}, err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if cerr := out.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr == nil {
		mt := srcInfo.ModTime().UTC()
		_ = os.Chtimes(filepath.Join(dstSpaceDir, tmpOS), mt, mt)
		copyErr = os.Rename(filepath.Join(dstSpaceDir, tmpOS), dstAbs)
	}
	if copyErr != nil {
		_ = dstRoot.Remove(tmpOS)
		return CopyWithinResult{}, copyErr
	}
	fi, err := dstRoot.Lstat(dstOS)
	if err != nil {
		return CopyWithinResult{}, err
	}
	return CopyWithinResult{
		Name:      fi.Name(),
		Size:      fi.Size(),
		Mode:      uint32(fi.Mode().Perm()),
		ModTimeNs: fi.ModTime().UTC().UnixNano(),
		Linked:    false,
	}, nil
}
