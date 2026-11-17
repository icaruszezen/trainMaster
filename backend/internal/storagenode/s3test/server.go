// Package s3test provides an in-memory S3-compatible HTTP server for testing
// S3Volume, dataset imports/scans, data version builds, and online migrations.
package s3test

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type storedObject struct {
	data    []byte
	etag    string
	modTime time.Time
}

type multipartState struct {
	bucket string
	key    string
	parts  map[int][]byte
}

// Server is an in-memory S3 REST server supporting path-style requests,
// ListObjectsV2 (with delimiter and continuation-token), HEAD/GET (with HTTP
// Range), PUT, CopyObject, Multipart Upload, and batch DeleteObjects.
type Server struct {
	*httptest.Server

	AccessKey string
	SecretKey string

	mu      sync.RWMutex
	buckets map[string]map[string]storedObject
	uploads map[string]*multipartState
	nextID  int64
}

// NewServer starts an httptest.Server pre-populated with the given buckets.
func NewServer(buckets ...string) *Server {
	s := &Server{
		AccessKey: "minioadmin",
		SecretKey: "minioadmin-secret-123",
		buckets:   make(map[string]map[string]storedObject),
		uploads:   make(map[string]*multipartState),
	}
	for _, b := range buckets {
		s.buckets[b] = make(map[string]storedObject)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	return s
}

// ObjectCount returns the number of stored objects under bucket with optional prefix.
func (s *Server) ObjectCount(bucket, prefix string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.buckets[bucket]
	n := 0
	for k := range b {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// PutRaw seeds an object directly into bucket.
func (s *Server) PutRaw(bucket, key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets[bucket] == nil {
		s.buckets[bucket] = make(map[string]storedObject)
	}
	sum := md5.Sum(data)
	s.buckets[bucket][key] = storedObject{
		data:    bytes.Clone(data),
		etag:    `"` + hex.EncodeToString(sum[:]) + `"`,
		modTime: time.Now().UTC().Truncate(time.Second),
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	// Verify AWS SigV4 Authorization header presence and credential access key.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/") {
		writeS3Error(w, http.StatusForbidden, "AccessDenied", "Invalid Access Key Id")
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, hasKey := strings.Cut(trimmed, "/")
	if bucket == "" {
		writeS3Error(w, http.StatusBadRequest, "InvalidBucketName", "Missing bucket")
		return
	}

	s.mu.RLock()
	_, bucketOK := s.buckets[bucket]
	s.mu.RUnlock()
	if !bucketOK {
		writeS3Error(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}

	q := r.URL.Query()
	if !hasKey || key == "" {
		if r.Method == http.MethodGet && q.Get("list-type") == "2" {
			s.handleListV2(w, bucket, q)
			return
		}
		if r.Method == http.MethodPost && q.Has("delete") {
			s.handleBatchDelete(w, r, bucket)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
		return
	}

	switch r.Method {
	case http.MethodHead:
		s.handleGetOrHead(w, r, bucket, key, true)
	case http.MethodGet:
		s.handleGetOrHead(w, r, bucket, key, false)
	case http.MethodPut:
		if uploadID := q.Get("uploadId"); uploadID != "" {
			s.handleUploadPart(w, r, uploadID, q.Get("partNumber"))
			return
		}
		if copySrc := r.Header.Get("X-Amz-Copy-Source"); copySrc != "" {
			s.handleCopyObject(w, r, bucket, key, copySrc)
			return
		}
		s.handlePutObject(w, r, bucket, key)
	case http.MethodPost:
		if q.Has("uploads") {
			s.handleInitMultipart(w, bucket, key)
			return
		}
		if uploadID := q.Get("uploadId"); uploadID != "" {
			s.handleCompleteMultipart(w, r, uploadID)
			return
		}
		writeS3Error(w, http.StatusBadRequest, "InvalidRequest", "Unsupported POST")
	case http.MethodDelete:
		if uploadID := q.Get("uploadId"); uploadID != "" {
			s.mu.Lock()
			delete(s.uploads, uploadID)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.mu.Lock()
		delete(s.buckets[bucket], key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func (s *Server) handleListV2(w http.ResponseWriter, bucket string, q url.Values) {
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	startIdx, _ := strconv.Atoi(q.Get("continuation-token"))
	maxKeys := 1000
	if mk, err := strconv.Atoi(q.Get("max-keys")); err == nil && mk > 0 {
		maxKeys = mk
	}

	type entry struct {
		isPrefix bool
		key      string
		obj      storedObject
	}

	s.mu.RLock()
	b := s.buckets[bucket]
	keys := make([]string, 0, len(b))
	for k := range b {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)

	seenPrefixes := make(map[string]bool)
	var all []entry
	for _, k := range keys {
		if delimiter != "" {
			rest := strings.TrimPrefix(k, prefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				cp := prefix + rest[:idx+len(delimiter)]
				if !seenPrefixes[cp] {
					seenPrefixes[cp] = true
					all = append(all, entry{isPrefix: true, key: cp})
				}
				continue
			}
		}
		all = append(all, entry{key: k, obj: b[k]})
	}
	s.mu.RUnlock()

	if startIdx > len(all) {
		startIdx = len(all)
	}
	endIdx := startIdx + maxKeys
	truncated := false
	nextToken := ""
	if endIdx < len(all) {
		truncated = true
		nextToken = strconv.Itoa(endIdx)
	} else {
		endIdx = len(all)
	}
	page := all[startIdx:endIdx]

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	fmt.Fprintf(&buf, "<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>%d</MaxKeys><IsTruncated>%t</IsTruncated>",
		xmlEsc(bucket), xmlEsc(prefix), len(page), maxKeys, truncated)
	if nextToken != "" {
		fmt.Fprintf(&buf, "<NextContinuationToken>%s</NextContinuationToken>", xmlEsc(nextToken))
	}
	for _, e := range page {
		if e.isPrefix {
			fmt.Fprintf(&buf, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", xmlEsc(e.key))
		} else {
			fmt.Fprintf(&buf, "<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>%s</ETag><Size>%d</Size></Contents>",
				xmlEsc(e.key), e.obj.modTime.Format(time.RFC3339), xmlEsc(e.obj.etag), len(e.obj.data))
		}
	}
	buf.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleGetOrHead(w http.ResponseWriter, r *http.Request, bucket, key string, headOnly bool) {
	s.mu.RLock()
	obj, ok := s.buckets[bucket][key]
	s.mu.RUnlock()
	if !ok {
		writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}

	w.Header().Set("ETag", obj.etag)
	w.Header().Set("Last-Modified", obj.modTime.UTC().Format(http.TimeFormat))

	rangeHdr := r.Header.Get("Range")
	if !headOnly && strings.HasPrefix(rangeHdr, "bytes=") && len(obj.data) > 0 {
		spec := strings.TrimPrefix(rangeHdr, "bytes=")
		startStr, endStr, _ := strings.Cut(spec, "-")
		start, err := strconv.Atoi(startStr)
		if err != nil || start < 0 || start >= len(obj.data) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(obj.data)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		end := len(obj.data) - 1
		if endStr != "" {
			if parsedEnd, err := strconv.Atoi(endStr); err == nil && parsedEnd < end {
				end = parsedEnd
			}
		}
		if end < start {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		slice := obj.data[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(obj.data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
	w.WriteHeader(http.StatusOK)
	if !headOnly {
		_, _ = w.Write(obj.data)
	}
}

func (s *Server) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	s.mu.Lock()
	if r.Header.Get("If-None-Match") == "*" {
		if _, exists := s.buckets[bucket][key]; exists {
			s.mu.Unlock()
			writeS3Error(w, http.StatusPreconditionFailed, "PreconditionFailed", "Key already exists")
			return
		}
	}
	sum := md5.Sum(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	s.buckets[bucket][key] = storedObject{
		data:    body,
		etag:    etag,
		modTime: time.Now().UTC().Truncate(time.Second),
	}
	s.mu.Unlock()
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleCopyObject(w http.ResponseWriter, _ *http.Request, dstBucket, dstKey, rawSource string) {
	unesc, err := url.PathUnescape(strings.TrimPrefix(rawSource, "/"))
	if err != nil {
		writeS3Error(w, http.StatusBadRequest, "InvalidArgument", err.Error())
		return
	}
	srcBucket, srcKey, ok := strings.Cut(unesc, "/")
	if !ok {
		writeS3Error(w, http.StatusBadRequest, "InvalidArgument", "Invalid copy source")
		return
	}
	s.mu.Lock()
	srcObj, exists := s.buckets[srcBucket][srcKey]
	if !exists {
		s.mu.Unlock()
		writeS3Error(w, http.StatusNotFound, "NoSuchKey", "Source key does not exist")
		return
	}
	cp := storedObject{
		data:    bytes.Clone(srcObj.data),
		etag:    srcObj.etag,
		modTime: time.Now().UTC().Truncate(time.Second),
	}
	s.buckets[dstBucket][dstKey] = cp
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><LastModified>%s</LastModified><ETag>%s</ETag></CopyObjectResult>`,
		cp.modTime.Format(time.RFC3339), xmlEsc(cp.etag))
}

func (s *Server) handleInitMultipart(w http.ResponseWriter, bucket, key string) {
	s.mu.Lock()
	s.nextID++
	uploadID := fmt.Sprintf("up-%d", s.nextID)
	s.uploads[uploadID] = &multipartState{
		bucket: bucket,
		key:    key,
		parts:  make(map[int][]byte),
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`,
		xmlEsc(bucket), xmlEsc(key), uploadID)
}

func (s *Server) handleUploadPart(w http.ResponseWriter, r *http.Request, uploadID, partNumStr string) {
	partNum, err := strconv.Atoi(partNumStr)
	if err != nil || partNum < 1 {
		writeS3Error(w, http.StatusBadRequest, "InvalidPart", "Bad part number")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeS3Error(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	sum := md5.Sum(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok {
		up.parts[partNum] = body
	}
	s.mu.Unlock()
	if !ok {
		writeS3Error(w, http.StatusNotFound, "NoSuchUpload", "Upload not found")
		return
	}
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleCompleteMultipart(w http.ResponseWriter, r *http.Request, uploadID string) {
	type partSpec struct {
		PartNumber int `xml:"PartNumber"`
	}
	type reqXML struct {
		Parts []partSpec `xml:"Part"`
	}
	var req reqXML
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeS3Error(w, http.StatusBadRequest, "MalformedXML", err.Error())
		return
	}

	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if !ok {
		s.mu.Unlock()
		writeS3Error(w, http.StatusNotFound, "NoSuchUpload", "Upload not found")
		return
	}
	var combined bytes.Buffer
	for _, p := range req.Parts {
		chunk, exists := up.parts[p.PartNumber]
		if !exists {
			s.mu.Unlock()
			writeS3Error(w, http.StatusBadRequest, "InvalidPart", "Missing part")
			return
		}
		combined.Write(chunk)
	}
	data := combined.Bytes()
	sum := md5.Sum(data)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	s.buckets[up.bucket][up.key] = storedObject{
		data:    data,
		etag:    etag,
		modTime: time.Now().UTC().Truncate(time.Second),
	}
	delete(s.uploads, uploadID)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult><ETag>%s</ETag></CompleteMultipartUploadResult>`,
		xmlEsc(etag))
}

func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request, bucket string) {
	type objSpec struct {
		Key string `xml:"Key"`
	}
	type reqXML struct {
		Objects []objSpec `xml:"Object"`
	}
	var req reqXML
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		writeS3Error(w, http.StatusBadRequest, "MalformedXML", err.Error())
		return
	}
	s.mu.Lock()
	for _, o := range req.Objects {
		delete(s.buckets[bucket], o.Key)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><DeleteResult></DeleteResult>`))
}

func writeS3Error(w http.ResponseWriter, code int, s3Code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`,
		xmlEsc(s3Code), xmlEsc(msg))
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
