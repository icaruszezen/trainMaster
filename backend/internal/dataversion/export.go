package dataversion

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// Downloads: YOLO, Pascal VOC and COCO zip archives streamed from the
// version folder while they are sent, so a download needs no disk space and
// starts at once (like dataset downloads: CreateExport prepares a ticket,
// then a plain GET fetches it). The images come from the version folder,
// their boxes from data_version_images (the version's labels/ folder is
// for training in place). Every format gets each image once: the version
// gave every image a unique name (case-insensitively, also without the
// extension and as a label path).

// exportTTL is how long a prepared download can be fetched (repeatedly:
// browsers and download managers may retry).
const exportTTL = time.Hour

// maxExportTopBytes bounds the folder name in the archive (and so the file
// name) well below the usual 255-byte limit.
const maxExportTopBytes = 200

// exportChunk is how many image rows a download reads at a time.
const exportChunk = 1000

var errExportExpired = &Error{Kind: ErrNotFound, Message: "下载链接已失效，请重新下载"}

type exportRequest struct {
	versionID        int64
	format           string
	includeAugmented bool
	top              string // the folder in the archive
	fileName         string
	expires          time.Time
}

// exportTickets holds the prepared downloads in memory (the service runs as
// a single instance). The zero value is ready to use.
type exportTickets struct {
	mu sync.Mutex
	m  map[string]exportRequest
}

func (t *exportTickets) put(token string, r exportRequest, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]exportRequest{}
	}
	for k, old := range t.m {
		if now.After(old.expires) {
			delete(t.m, k)
		}
	}
	t.m[token] = r
}

func (t *exportTickets) get(token string, now time.Time) (exportRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.m[token]
	return r, ok && !now.After(r.expires)
}

// exportFormat validates the format of a download.
func exportFormat(format string) (string, error) {
	switch format {
	case FormatYOLO, FormatVOC, FormatCOCO:
		return format, nil
	}
	return "", newError(ErrInvalid, "下载格式必须是 yolo（YOLO）、voc（Pascal VOC）或 coco（COCO）")
}

// CreateExport prepares the download of a ready version.
func (s *Service) CreateExport(ctx context.Context, id int64, in ExportInput) (ExportTicket, error) {
	format, err := exportFormat(in.Format)
	if err != nil {
		return ExportTicket{}, err
	}
	v, err := queryVersion(ctx, s.db, id, false)
	if err != nil {
		return ExportTicket{}, err
	}
	if v.Status != StatusReady {
		return ExportTicket{}, errNotReady
	}
	now := time.Now()
	top := exportTop(v.ProjectName, v.Version)
	r := exportRequest{versionID: id, format: format, includeAugmented: in.IncludeAugmented, top: top,
		fileName: top + "-" + format + ".zip", expires: now.Add(exportTTL)}
	token := rand.Text()
	s.exports.put(token, r, now)
	return ExportTicket{Token: token, FileName: r.fileName, ExpiresAt: r.expires.UTC()}, nil
}

// exportTop is the folder name in the archive of a version:
// <project name>-数据版本v<n>, the project name made safe as a file name
// (characters Windows does not allow and control characters become "_",
// leading and trailing dots and spaces go) and shortened to
// maxExportTopBytes.
func exportTop(projectName string, version int) string {
	suffix := fmt.Sprintf("-数据版本v%d", version)
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || unicode.IsControl(r) || r == ' ' || r == ' ' {
			return '_'
		}
		return r
	}, projectName)
	name = strings.Trim(name, ". ")
	if over := len(name) + len(suffix) - maxExportTopBytes; over > 0 {
		n := len(name) - over
		for n > 0 && !utf8.RuneStart(name[n]) {
			n--
		}
		name = strings.TrimRight(name[:n], ". ")
	}
	if name == "" {
		name = "项目"
	}
	return name + suffix
}

// Export is an opened download; Write it, then Close it.
type Export struct {
	FileName string

	db               querier
	log              *slog.Logger
	root             *os.Root // the version folder
	version          DataVersion
	format           string
	includeAugmented bool
	top              string
	splits           map[string]int // the images per split (without the augmented ones unless included)

	entries map[uint64]bool // hashes of the archive entry names written so far

	// Counts for the log.
	written, missing, changed int
}

// OpenExport opens a download prepared by CreateExport. The version must
// still be ready and not in the recycle bin.
func (s *Service) OpenExport(ctx context.Context, id int64, token string) (*Export, error) {
	r, ok := s.exports.get(token, time.Now())
	if !ok || r.versionID != id {
		return nil, errExportExpired
	}
	v, err := queryVersion(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if v.Status != StatusReady {
		return nil, errNotReady
	}
	var dirName string
	if err := s.db.QueryRow(ctx, "SELECT dir_name FROM data_versions WHERE id = $1", id).Scan(&dirName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errVersionNotFound
		}
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT split, count(*) FROM data_version_images
		WHERE version_id = $1 AND ($2 OR augmented_from IS NULL) GROUP BY split`, id, r.includeAugmented)
	if err != nil {
		return nil, err
	}
	splits := map[string]int{}
	var split string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&split, &n}, func() error {
		splits[split] = n
		return nil
	}); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.versionDir(dirName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, newError(ErrConflict, "数据版本的文件夹不存在（可能已被移走），无法下载")
		}
		return nil, fmt.Errorf("open data version folder: %w", err)
	}
	return &Export{FileName: r.fileName, db: s.db, root: root, version: v, format: r.format,
		includeAugmented: r.includeAugmented, top: r.top, splits: splits, entries: map[uint64]bool{},
		log: s.log.With("data_version_id", id, "format", r.format, "include_augmented", r.includeAugmented)}, nil
}

// Write sends the zip archive to w. Images whose file is missing in the
// version folder are left out (logged); an error means the archive is
// incomplete.
func (e *Export) Write(ctx context.Context, w io.Writer) error {
	start := time.Now()
	zw := zip.NewWriter(w)
	dataset.RegisterZipCompressor(zw)
	var err error
	switch e.format {
	case FormatYOLO:
		err = e.writeYOLO(ctx, zw)
	case FormatVOC:
		err = e.writeVOC(ctx, zw)
	default:
		err = e.writeCOCO(ctx, zw)
	}
	if err == nil {
		// Purged while it was sent (after a week in the recycle bin): its
		// rows went midway, so the archive may lack images.
		var exists bool
		if err = e.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM data_versions WHERE id = $1)",
			e.version.ID).Scan(&exists); err == nil && !exists {
			err = errors.New("the data version was purged during the download")
		}
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return err
	}
	e.log.Info("data version exported", "images", e.written, "images_missing", e.missing,
		"images_changed", e.changed, "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// Close releases the version folder.
func (e *Export) Close() error {
	if e == nil || e.root == nil {
		return nil
	}
	return e.root.Close()
}

// exportImage is an image row of a download.
type exportImage struct {
	id       int64
	position int
	split    string
	path     string // in the version folder: images/...
	width    int
	height   int
	stored   storedImage
	objects  []dataset.AnnotationObject
}

// eachImage calls fn for the images of the download in position order, of
// one split ("" all), with their objects when withObjects. The rows are
// read a chunk at a time, and fn runs after a chunk is read, so that a slow
// client does not hold a database connection.
func (e *Export) eachImage(ctx context.Context, split string, withObjects bool, fn func(exportImage) error) error {
	objects := "NULL::jsonb"
	if withObjects {
		objects = "i.objects"
	}
	lastPosition, lastID := -1, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := e.db.Query(ctx, `SELECT i.id, i.position, i.split, i.path, i.width, i.height, i.size_bytes,
				i.mod_time, `+objects+`
			FROM data_version_images i
			WHERE i.version_id = $1 AND ($2 = '' OR i.split = $2) AND ($3 OR i.augmented_from IS NULL)
				AND (i.position, i.id) > ($4, $5)
			ORDER BY i.position, i.id LIMIT $6`,
			e.version.ID, split, e.includeAugmented, lastPosition, lastID, exportChunk)
		if err != nil {
			return fmt.Errorf("load images: %w", err)
		}
		chunk, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (exportImage, error) {
			var img exportImage
			err := row.Scan(&img.id, &img.position, &img.split, &img.path, &img.width, &img.height, &img.stored.size,
				&img.stored.modTime, &img.objects)
			img.stored.path = img.path
			return img, err
		})
		if err != nil {
			return fmt.Errorf("load images: %w", err)
		}
		for _, img := range chunk {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(img); err != nil {
				return err
			}
		}
		if len(chunk) < exportChunk {
			return nil
		}
		last := chunk[len(chunk)-1]
		lastPosition, lastID = last.position, last.id
	}
}

// header returns the header of the entry for name (under the top folder),
// or ok false when an earlier entry has the same name, ignoring case
// (extracting the archive would overwrite one file with the other). The
// version's unique names make that an error of the version: it is logged.
func (e *Export) header(name string, method uint16, modTime time.Time) (h *zip.FileHeader, ok bool) {
	name = e.top + "/" + strings.ReplaceAll(name, `\`, "_")
	hash := fnv.New64a()
	hash.Write([]byte(strings.ToLower(name)))
	key := hash.Sum64()
	if e.entries[key] {
		e.log.Warn("data version export: archive name taken by an earlier file; leaving this one out", "name", name)
		return nil, false
	}
	e.entries[key] = true
	h = &zip.FileHeader{Name: name, Method: method, Modified: modTime}
	h.SetMode(0o644)
	return h, true
}

// copyImage adds the file of an image to the archive as name; ok is false
// (and nothing is written) when the file is missing in the version folder
// or the name is taken.
func (e *Export) copyImage(zw *zip.Writer, img exportImage, name string) (ok bool, err error) {
	f, err := e.root.Open(filepath.FromSlash(img.path))
	if err != nil {
		e.log.Warn("data version export: leaving out an image whose file cannot be opened", "path", img.path, "err", err)
		e.missing++
		return false, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		e.log.Warn("data version export: leaving out an image that is not a regular file", "path", img.path, "err", err)
		e.missing++
		return false, nil
	}
	if storedChanged(fi, img.stored) {
		e.log.Warn("data version export: image file changed since the version was built (rewritten in place?)",
			"path", img.path, "size", fi.Size(), "recorded_size", img.stored.size)
		e.changed++
	}
	h, ok := e.header(name, dataset.ZipMethod(name), fi.ModTime())
	if !ok {
		return false, nil
	}
	out, err := zw.CreateHeader(h)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, f); err != nil {
		return false, err
	}
	e.written++
	return true, nil
}

// addFile adds generated content to the archive (deflated).
func (e *Export) addFile(zw *zip.Writer, name string, data []byte) error {
	h, ok := e.header(name, zip.Deflate, e.modTime())
	if !ok {
		return nil
	}
	out, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// modTime is the time of the generated files: when the version was
// finished.
func (e *Export) modTime() time.Time {
	if e.version.FinishedAt != nil {
		return *e.version.FinishedAt
	}
	return e.version.CreatedAt
}

// classNames are the names of the version's classes, in order (the class
// ids).
func (e *Export) classNames() []string {
	names := make([]string, len(e.version.Classes))
	for i, c := range e.version.Classes {
		names[i] = c.Name
	}
	return names
}

// classIDs maps the class names to their ids (0-based).
func (e *Export) classIDs() map[string]int {
	ids := make(map[string]int, len(e.version.Classes))
	for i, c := range e.version.Classes {
		ids[c.Name] = i
	}
	return ids
}

// splitPath is where an image goes in the split folders of the YOLO and
// COCO layouts: <folder>/<split>/<version path without "images/">.
func splitPath(folder, split, versionPath string) string {
	return folder + "/" + split + "/" + strings.TrimPrefix(versionPath, "images/")
}

// clipBox clips a box to the image; ok is false when nothing is left.
func clipBox(o dataset.AnnotationObject, width, height int) (x1, y1, x2, y2 int, ok bool) {
	x1, x2 = clampInt(min(o.Xmin, o.Xmax), 0, width), clampInt(max(o.Xmin, o.Xmax), 0, width)
	y1, y2 = clampInt(min(o.Ymin, o.Ymax), 0, height), clampInt(max(o.Ymin, o.Ymax), 0, height)
	return x1, y1, x2, y2, x2 > x1 && y2 > y1
}

func clampInt(v, lo, hi int) int { return max(lo, min(v, hi)) }

// commentText makes text safe for a one-line comment (YAML, README): line
// breaks and other control characters become spaces.
func commentText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, text)
}

// noteLine is the first line of the version's note.
func (e *Export) noteLine() string {
	line, _, _ := strings.Cut(strings.TrimSpace(e.version.Note), "\n")
	return strings.TrimSpace(commentText(line))
}

// title is the version as users see it: 外观检测 · 数据版本 v4.
func (e *Export) title() string {
	return fmt.Sprintf("%s · 数据版本 v%d", commentText(e.version.ProjectName), e.version.Version)
}
