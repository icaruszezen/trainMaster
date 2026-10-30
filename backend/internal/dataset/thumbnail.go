package dataset

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// Thumbnail sizes: the longer side fits in the given number of pixels.
const (
	ThumbSmall = "sm" // grid tiles
	ThumbLarge = "lg" // image viewer
)

var thumbSides = map[string]int{ThumbSmall: 320, ThumbLarge: 1600}

const (
	thumbQuality = 82
	// maxThumbnailCost refuses images whose decoding and scaling would
	// need more memory (see thumbnailCost). A 4096×30000 line-scan image
	// needs about 0.7 GiB for the large size.
	maxThumbnailCost = 1 << 30
	// thumbMemoryBudget bounds the memory of all generations running at
	// the same time, so it stays bounded even when many large images are
	// requested at once.
	thumbMemoryBudget = 2 << 30
)

var (
	errUnsupportedImage = &Error{Kind: ErrUnsupportedImage, Message: "无法生成该图片的缩略图（格式不受支持或文件已损坏）"}
	errImageTooLarge    = &Error{Kind: ErrUnsupportedImage, Message: "图片尺寸过大或长宽比过于悬殊，生成缩略图所需的内存超出限制"}
)

// thumbnailer generates thumbnails with bounded concurrency and memory,
// generating each one only once when several requests ask for it.
type thumbnailer struct {
	slots  chan struct{}
	memory *semaphore.Weighted // bytes, as estimated by thumbnailCost
	group  singleflight.Group
}

func newThumbnailer() *thumbnailer {
	return &thumbnailer{
		slots:  make(chan struct{}, max(2, runtime.NumCPU())),
		memory: semaphore.NewWeighted(thumbMemoryBudget),
	}
}

func (s *Service) thumbDir(datasetID int64) string {
	return filepath.Join(s.storage.Dir(storage.Cache), "thumbs", strconv.FormatInt(datasetID, 10))
}

// OpenThumbnail returns the JPEG thumbnail of an image, generating and
// caching it on first use. Thumbnails are cached per file version, so a
// changed image gets a new one; older versions are removed.
func (s *Service) OpenThumbnail(ctx context.Context, datasetID, imageID int64, size string) (*ImageFile, error) {
	side, ok := thumbSides[size]
	if !ok {
		return nil, &Error{Kind: ErrBadRequest, Message: "size 参数只能是 sm 或 lg"}
	}
	img, root, err := s.imageRoot(ctx, datasetID, imageID)
	if err != nil {
		return nil, err
	}
	rel := filepath.FromSlash(img.Path)
	fi, err := root.Stat(rel)
	srcDir := root.Name()
	root.Close()
	if err != nil {
		return nil, imageFileError(err)
	}
	if !fi.Mode().IsRegular() {
		return nil, imageFileError(fs.ErrNotExist)
	}
	version := fileVersion(fi.Size(), dbTime(fi.ModTime()))
	prefix := fmt.Sprintf("%d-%s-", imageID, size)
	cachePath := filepath.Join(s.thumbDir(datasetID), prefix+version+".jpg")

	if f, err := openThumb(cachePath, img, version); err == nil {
		return f, nil
	}
	// The generation is shared by concurrent requests and continues when
	// this one gives up, so the result lands in the cache either way.
	ch := s.thumbs.group.DoChan(cachePath, func() (any, error) {
		err := s.thumbs.generate(srcDir, rel, side, cachePath)
		if err == nil {
			removeOtherVersions(filepath.Dir(cachePath), prefix, filepath.Base(cachePath))
		}
		return nil, err
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return openThumb(cachePath, img, version)
}

func openThumb(p string, img Image, version string) (*ImageFile, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	name := strings.TrimSuffix(img.FileName, filepath.Ext(img.FileName)) + ".jpg"
	return &ImageFile{File: f, Name: name, ContentType: "image/jpeg", ModTime: fi.ModTime(), Version: version}, nil
}

// generate renders the thumbnail of dir/rel into dst.
func (t *thumbnailer) generate(dir, rel string, side int, dst string) error {
	t.slots <- struct{}{}
	defer func() { <-t.slots }()

	root, err := os.OpenRoot(dir)
	if err != nil {
		return imageFileError(err)
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return imageFileError(err)
	}
	defer f.Close()

	cfg, format, err := decodeImageConfig(f)
	if err != nil {
		return errUnsupportedImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return errUnsupportedImage
	}
	// Checking the pixel count first keeps the estimate from overflowing.
	if int64(cfg.Width)*int64(cfg.Height) > maxThumbnailCost {
		return errImageTooLarge
	}
	tw, th := fitSize(cfg.Width, cfg.Height, side)
	cost := thumbnailCost(cfg.Width, cfg.Height, tw, th, decodeCost(f, format, cfg))
	if cost > maxThumbnailCost {
		return errImageTooLarge
	}
	if err := t.memory.Acquire(context.Background(), cost); err != nil {
		return err
	}
	src, err := decodeImage(f)
	if err != nil {
		t.memory.Release(cost)
		return errUnsupportedImage
	}
	thumb := fitWithin(src, side)
	t.memory.Release(cost)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create thumbnail folder: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".thumb-*")
	if err != nil {
		return fmt.Errorf("create thumbnail: %w", err)
	}
	err = jpeg.Encode(tmp, thumb, &jpeg.Options{Quality: thumbQuality})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write thumbnail: %w", err)
	}
	return nil
}

// thumbnailCost estimates the memory in bytes needed to decode a w×h image
// (decoded bytes, see decodeCost) and scale it to tw×th. Besides the
// decoded image and the result, the scaler allocates a temporary buffer of
// 32 bytes per destination column and source row, which dominates for very
// tall images (a 1×100,000,000 PNG of a few hundred KB would need 3 GB),
// and filter weights of about 64 bytes per source column and row.
func thumbnailCost(w, h, tw, th int, decoded int64) int64 {
	cost := decoded + 4*int64(tw)*int64(th)
	if tw != w || th != h {
		cost += 32*int64(tw)*int64(h) + 64*(int64(w)+int64(h))
	}
	return cost
}

// decodeCost estimates the memory in bytes needed to decode the image in
// f, of the given format (as image.DecodeConfig names it).
func decodeCost(f io.ReaderAt, format string, cfg image.Config) int64 {
	pixels := int64(cfg.Width) * int64(cfg.Height)
	switch format {
	case "jpeg":
		return jpegDecodeCost(f, cfg)
	case "png":
		// An interlaced image is decoded pass by pass into separate images
		// that are then copied into the result.
		var interlace [1]byte
		if _, err := f.ReadAt(interlace[:], 28); err != nil || interlace[0] != 0 {
			return 2 * pixels * decodedBytesPerPixel(cfg.ColorModel)
		}
	}
	return pixels * decodedBytesPerPixel(cfg.ColorModel)
}

// decodedBytesPerPixel is the memory per pixel of the image the decoders
// return for a color model; 8 (64-bit color) for models not listed.
func decodedBytesPerPixel(m color.Model) int64 {
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3 // at most: chroma subsampling makes it less
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	}
	return 8
}

// jpegDecodeCost estimates the memory in bytes that image/jpeg needs to
// decode the JPEG image in f, from its frame header: one byte per sample
// of each component, in planes padded to whole MCUs; for a progressive
// image also all DCT coefficients, 4 bytes per sample; for an RGB or
// four-component image also the RGBA or CMYK image the planes are
// converted into. An image whose frame header cannot be read counts as
// too large.
func jpegDecodeCost(f io.ReaderAt, cfg image.Config) int64 {
	frame, ok := readJPEGFrame(f)
	if !ok {
		return maxThumbnailCost + 1
	}
	mcus := ceilDiv(int64(cfg.Width), frame.mcuWidth) * ceilDiv(int64(cfg.Height), frame.mcuHeight)
	samples := 64 * mcus * frame.blocks
	cost := samples
	if frame.progressive {
		cost += 4 * samples
	}
	if cfg.ColorModel != color.GrayModel && cfg.ColorModel != color.YCbCrModel {
		cost += 4 * int64(cfg.Width) * int64(cfg.Height)
	}
	return cost
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// jpegFrame is what the frame header of a JPEG image says about the
// memory image/jpeg needs.
type jpegFrame struct {
	progressive         bool
	mcuWidth, mcuHeight int64 // pixels
	blocks              int64 // 8×8 blocks per MCU, over all components
}

// readJPEGFrame reads the frame header of the JPEG image in r. It skips
// what precedes the header the way image/jpeg does, so that both see the
// same one; ok is false when it is not within the first 16 MiB or is not
// one image/jpeg decodes.
func readJPEGFrame(r io.ReaderAt) (frame jpegFrame, ok bool) {
	br := bufio.NewReader(io.NewSectionReader(r, 0, 16<<20))
	var b [2]byte
	if _, err := io.ReadFull(br, b[:]); err != nil || b != [2]byte{0xff, 0xd8} { // SOI
		return frame, false
	}
	for {
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return frame, false
		}
		for b[0] != 0xff { // extraneous bytes before a marker are skipped
			c, err := br.ReadByte()
			if err != nil {
				return frame, false
			}
			b[0], b[1] = b[1], c
		}
		marker := b[1]
		if marker == 0x00 { // so is "\xff\x00"
			continue
		}
		for marker == 0xff { // fill bytes
			c, err := br.ReadByte()
			if err != nil {
				return frame, false
			}
			marker = c
		}
		if marker >= 0xd0 && marker <= 0xd7 { // RSTn, without a length
			continue
		}
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return frame, false
		}
		n := int(binary.BigEndian.Uint16(b[:])) - 2
		if n < 0 {
			return frame, false
		}
		switch {
		case marker == 0xc0 || marker == 0xc1 || marker == 0xc2: // SOF0, SOF1, SOF2
			return parseJPEGFrame(br, n, marker == 0xc2)
		case marker == 0xc4 || marker == 0xdb || marker == 0xdd || // DHT, DQT, DRI
			marker >= 0xe0 && marker <= 0xef || marker == 0xfe: // APPn, COM
			if _, err := br.Discard(n); err != nil {
				return frame, false
			}
		default: // image/jpeg fails on any other marker before the frame header
			return frame, false
		}
	}
}

// parseJPEGFrame parses the n bytes of a frame header in r.
func parseJPEGFrame(r io.Reader, n int, progressive bool) (frame jpegFrame, ok bool) {
	if n != 6+3*1 && n != 6+3*3 && n != 6+3*4 {
		return frame, false
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return frame, false
	}
	components := int(buf[5])
	if 6+3*components != n {
		return frame, false
	}
	frame.progressive = progressive
	for i := range components {
		h, v := int64(buf[7+3*i]>>4), int64(buf[7+3*i]&0x0f)
		if h < 1 || h > 4 || v < 1 || v > 4 {
			return frame, false
		}
		if components == 1 { // image/jpeg ignores the sampling factors
			h, v = 1, 1
		}
		if i == 0 { // the first component has the largest factors
			frame.mcuWidth, frame.mcuHeight = 8*h, 8*v
		}
		frame.blocks += h * v
	}
	return frame, true
}

// fitSize returns the size of an image of w×h pixels scaled down (never
// up) so that both sides fit in side pixels.
func fitSize(w, h, side int) (int, int) {
	scale := math.Min(1, math.Min(float64(side)/float64(w), float64(side)/float64(h)))
	return max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
}

// fitWithin scales img down (never up) so that both sides fit in side
// pixels, flattening transparency onto white since JPEG has no alpha.
// EXIF orientation is deliberately ignored: VOC coordinates refer to the
// stored pixel grid.
func fitWithin(img image.Image, side int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	tw, th := fitSize(w, h, side)

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	if tw == w && th == h {
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	}
	return dst
}

// removeOtherVersions deletes cached thumbnails that start with prefix
// except keep.
func removeOtherVersions(dir, prefix, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); n != keep && strings.HasPrefix(n, prefix) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

// removeThumbnails deletes the cached thumbnails of the given images.
func (s *Service) removeThumbnails(datasetID int64, imageIDs []int64) {
	if len(imageIDs) == 0 {
		return
	}
	dir := s.thumbDir(datasetID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	slices.Sort(imageIDs)
	for _, e := range entries {
		idText, _, ok := strings.Cut(e.Name(), "-")
		id, err := strconv.ParseInt(idText, 10, 64)
		if ok && err == nil {
			if _, found := slices.BinarySearch(imageIDs, id); found {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// removeAllThumbnails deletes the thumbnail cache of a dataset.
func (s *Service) removeAllThumbnails(datasetID int64) {
	if err := os.RemoveAll(s.thumbDir(datasetID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("remove dataset thumbnails", "dataset_id", datasetID, "err", err)
	}
}
