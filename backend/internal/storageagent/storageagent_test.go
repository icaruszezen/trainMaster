package storageagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

func newTestAgent(t *testing.T, token string, ttl time.Duration) (*Server, *httptest.Server) {
	t.Helper()
	zeroHeadroom := uint64(0)
	srv, err := NewServer(Options{
		DataDir:      t.TempDir(),
		Token:        token,
		TempTTL:      ttl,
		Logger:       slog.New(slog.DiscardHandler),
		FreeHeadroom: &zeroHeadroom,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return srv, ts
}

func doJSONReq(t *testing.T, client *http.Client, method, url, token string, body any, extraHeaders ...string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
		case io.Reader:
			bodyReader = v
		default:
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			bodyReader = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		if _, isBytes := body.([]byte); !isBytes {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		req.Header.Set(extraHeaders[i], extraHeaders[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, url, err)
	}
	return resp
}

func TestStorageAgentAuthHealthzInfoAndPOSIX(t *testing.T) {
	agent, ts := newTestAgent(t, "secret-node-token-123", time.Hour)
	client := NewDirectHTTPClient(false, 10*time.Second)

	// 1. /healthz is unauthenticated.
	resp := doJSONReq(t, client, http.MethodGet, ts.URL+"/healthz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. /api/v1/info rejects missing or wrong token.
	resp = doJSONReq(t, client, http.MethodGet, ts.URL+"/api/v1/info", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSONReq(t, client, http.MethodGet, ts.URL+"/api/v1/info", "wrong-token", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. /api/v1/info succeeds with valid Bearer token.
	resp = doJSONReq(t, client, http.MethodGet, ts.URL+"/api/v1/info", "secret-node-token-123", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info status = %d, want 200", resp.StatusCode)
	}
	var info InfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !info.OK || !info.SupportsArchive || info.DisplayPath != agent.DataDir() {
		t.Fatalf("unexpected info response: %+v", info)
	}

	// 4. Create dataset directory, upload file, HEAD, Range GET, Walk, CopyWithin, Rename, Delete.
	resp = doJSONReq(t, client, http.MethodPost, ts.URL+"/api/v1/spaces/datasets/dirs", "secret-node-token-123",
		map[string]any{"dir": "line-a", "exclusive": true})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mkdir exclusive = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Duplicate exclusive mkdir returns 409.
	resp = doJSONReq(t, client, http.MethodPost, ts.URL+"/api/v1/spaces/datasets/dirs", "secret-node-token-123",
		map[string]any{"dir": "line-a", "exclusive": true})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate mkdir exclusive = %d, want 409", resp.StatusCode)
	}
	_ = resp.Body.Close()

	payload := []byte("0123456789ABCDEF")
	resp = doJSONReq(t, client, http.MethodPut,
		ts.URL+"/api/v1/spaces/datasets/files?dir=line-a&path=sub/img_01.jpg",
		"secret-node-token-123", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put file status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// HEAD metadata check.
	resp = doJSONReq(t, client, http.MethodHead,
		ts.URL+"/api/v1/spaces/datasets/files?dir=line-a&path=sub/img_01.jpg",
		"secret-node-token-123", nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-File-Size") != "16" {
		t.Fatalf("HEAD status=%d size=%q", resp.StatusCode, resp.Header.Get("X-File-Size"))
	}
	_ = resp.Body.Close()

	// Range GET (for TIFF/EXIF ReaderAt).
	resp = doJSONReq(t, client, http.MethodGet,
		ts.URL+"/api/v1/spaces/datasets/files?dir=line-a&path=sub/img_01.jpg",
		"secret-node-token-123", nil, "Range", "bytes=2-5")
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range GET status = %d, want 206", resp.StatusCode)
	}
	rangeBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(rangeBytes) != "2345" {
		t.Fatalf("Range GET body = %q, want %q", string(rangeBytes), "2345")
	}

	// Same-node CopyWithin hardlinks datasets -> exports when FS supports link, else copies atomically.
	probeSrc := filepath.Join(agent.DataDir(), "datasets/line-a/sub/img_01.jpg")
	probeLink := filepath.Join(agent.DataDir(), "datasets/line-a/sub/.link-probe")
	hardlinkOK := os.Link(probeSrc, probeLink) == nil
	_ = os.Remove(probeLink)

	resp = doJSONReq(t, client, http.MethodPost, ts.URL+"/api/v1/spaces/copy-within",
		"secret-node-token-123", CopyWithinItem{
			SrcSpace: "datasets",
			SrcRel:   "line-a/sub/img_01.jpg",
			DstSpace: "exports",
			DstRel:   "data-versions/project-1/v1/images/img_01.jpg",
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("copy-within status = %d", resp.StatusCode)
	}
	var copyRes CopyWithinResult
	if err := json.NewDecoder(resp.Body).Decode(&copyRes); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if copyRes.Size != 16 || (hardlinkOK && !copyRes.Linked) {
		t.Fatalf("unexpected copyRes=%+v (hardlinkOK=%v)", copyRes, hardlinkOK)
	}
	copiedData, err := os.ReadFile(filepath.Join(agent.DataDir(), "exports/data-versions/project-1/v1/images/img_01.jpg"))
	if err != nil || string(copiedData) != string(payload) {
		t.Fatalf("copied file content = %q, err = %v", string(copiedData), err)
	}

	// Path traversal attempt is rejected with 400.
	resp = doJSONReq(t, client, http.MethodGet,
		ts.URL+"/api/v1/spaces/datasets/files?dir=line-a&path=../../etc/passwd",
		"secret-node-token-123", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestStorageAgentArchivePackStreamUnpackPullAndJanitor(t *testing.T) {
	// Poison HTTP_PROXY to prove peer-to-peer pull-unpack bypasses environment proxy!
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	srcAgent, srcTS := newTestAgent(t, "src-tok", 30*time.Second)
	dstAgent, dstTS := newTestAgent(t, "dst-tok", 30*time.Second)
	client := NewDirectHTTPClient(false, 15*time.Second)

	// Seed source dataset on srcAgent.
	srcDS := filepath.Join(srcAgent.DataDir(), "datasets", "camera-batch-1")
	if err := os.MkdirAll(filepath.Join(srcDS, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDS, "images/001.jpg"), []byte("jpeg-payload-001"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDS, "images/001.xml"), []byte("<annotation/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Pack source dataset into tar.zst on srcAgent.
	resp := doJSONReq(t, client, http.MethodPost, srcTS.URL+"/api/v1/archives/pack", "src-tok", PackArchiveRequest{
		Space:  "datasets",
		Dir:    "camera-batch-1",
		Format: "tar.zst",
	})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("pack status = %d: %s", resp.StatusCode, string(body))
	}
	var packed PackArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&packed); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if packed.FilesCount != 2 || packed.ArchiveBytes <= 0 || len(packed.SHA256) != 64 {
		t.Fatalf("unexpected pack response: %+v", packed)
	}

	// 2. Peer-to-peer pull-unpack from srcTS -> dstTS!
	resp = doJSONReq(t, client, http.MethodPost, dstTS.URL+"/api/v1/archives/pull-unpack", "dst-tok", PullUnpackRequest{
		SourceURL:            srcTS.URL + "/api/v1/archives/" + packed.ArchiveID,
		SourceToken:          "src-tok",
		ExpectedSHA256:       packed.SHA256,
		ExpectedArchiveBytes: packed.ArchiveBytes,
		ExpectedRawBytes:     &packed.RawBytes,
		Space:                "datasets",
		Dir:                  "camera-batch-1",
		Format:               "tar.zst",
		Mode:                 "atomic",
		IncludeModTimes:      true,
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("pull-unpack status = %d: %s", resp.StatusCode, string(body))
	}
	var unpacked UnpackArchiveResponse
	if err := json.NewDecoder(resp.Body).Decode(&unpacked); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if unpacked.FilesCount != 2 || unpacked.RawBytes != packed.RawBytes || len(unpacked.ModTimes) != 2 {
		t.Fatalf("unexpected pull-unpack response: %+v", unpacked)
	}

	gotBytes, err := os.ReadFile(filepath.Join(dstAgent.DataDir(), "datasets/camera-batch-1/images/001.jpg"))
	if err != nil || string(gotBytes) != "jpeg-payload-001" {
		t.Fatalf("unpacked file mismatch: err=%v got=%q", err, string(gotBytes))
	}

	// 3. Stream download + stream unpack with bad SHA-256 returns 400, then good SHA-256 succeeds.
	dlResp := doJSONReq(t, client, http.MethodGet, srcTS.URL+"/api/v1/archives/"+packed.ArchiveID, "src-tok", nil)
	if dlResp.StatusCode != http.StatusOK || dlResp.Header.Get("X-Archive-Sha256") != packed.SHA256 {
		t.Fatalf("archive download headers invalid: status=%d sha=%q", dlResp.StatusCode, dlResp.Header.Get("X-Archive-Sha256"))
	}
	archBytes, err := io.ReadAll(dlResp.Body)
	_ = dlResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	badResp := doJSONReq(t, client, http.MethodPost,
		dstTS.URL+"/api/v1/archives/unpack?space=datasets&dir=bad-sha&format=tar.zst&mode=atomic",
		"dst-tok", archBytes, "X-Archive-Sha256", strings.Repeat("f", 64))
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad sha unpack status = %d, want 400", badResp.StatusCode)
	}
	_ = badResp.Body.Close()

	okResp := doJSONReq(t, client, http.MethodPost,
		dstTS.URL+"/api/v1/archives/unpack?space=datasets&dir=streamed-copy&format=tar.zst&mode=atomic",
		"dst-tok", archBytes, "X-Archive-Sha256", packed.SHA256)
	if okResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(okResp.Body)
		_ = okResp.Body.Close()
		t.Fatalf("stream unpack status = %d: %s", okResp.StatusCode, string(body))
	}
	_ = okResp.Body.Close()

	// 4. Explicit DELETE /api/v1/archives/{archiveID} removes staged archive immediately.
	delResp := doJSONReq(t, client, http.MethodDelete, srcTS.URL+"/api/v1/archives/"+packed.ArchiveID, "src-tok", nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete archive status = %d, want 204", delResp.StatusCode)
	}
	_ = delResp.Body.Close()

	afterDel := doJSONReq(t, client, http.MethodGet, srcTS.URL+"/api/v1/archives/"+packed.ArchiveID, "src-tok", nil)
	if afterDel.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", afterDel.StatusCode)
	}
	_ = afterDel.Body.Close()

	// 5. TTL SweepTemp removes expired archives & orphaned .unpack-* directories.
	orphanUnpack := filepath.Join(dstAgent.DataDir(), "datasets/.unpack-orphan123")
	if err := os.MkdirAll(orphanUnpack, 0o755); err != nil {
		t.Fatal(err)
	}
	dstAgent.SweepTemp(time.Now().Add(time.Hour))
	if _, err := os.Stat(orphanUnpack); !os.IsNotExist(err) {
		t.Fatalf("expected SweepTemp to remove orphan .unpack-*, stat err=%v", err)
	}

	// 6. Insufficient disk space returns 507.
	lowSpaceSrv, err := NewServer(Options{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.DiscardHandler),
		DiskUsage: func(string) (storage.Usage, error) {
			return storage.Usage{TotalBytes: 100 << 20, FreeBytes: 512, UsedBytes: (100 << 20) - 512}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lowSpaceSrv.Close()
	lowTS := httptest.NewServer(lowSpaceSrv.Handler())
	defer lowTS.Close()

	noSpaceResp := doJSONReq(t, client, http.MethodPost,
		lowTS.URL+"/api/v1/archives/unpack?space=datasets&dir=full&format=tar.zst&mode=atomic",
		"", archBytes, "X-Archive-Sha256", packed.SHA256)
	if noSpaceResp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("expected 507 Insufficient Storage, got %d", noSpaceResp.StatusCode)
	}
	_ = noSpaceResp.Body.Close()
}

func TestWriteMappedErrMapsSyscallENOSPCTo507(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMappedErr(rec, fmt.Errorf("write recv temp: %w", syscall.ENOSPC))
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("expected 507 Insufficient Storage, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Error.Code != "insufficient_storage" {
		t.Fatalf("expected code insufficient_storage, got %q", payload.Error.Code)
	}

	c := NewDirectHTTPClient(false, 0)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil || tr.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("expected DialContext and 10s TLSHandshakeTimeout on NewDirectHTTPClient transport, got %+v", c.Transport)
	}
}
