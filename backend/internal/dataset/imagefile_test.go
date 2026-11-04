package dataset

import (
	"bytes"
	"errors"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func openTempImage(t *testing.T, name string, data []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestWriteThumbnail(t *testing.T) {
	side, ok := ThumbnailSide(ThumbSmall)
	if !ok || side != 320 {
		t.Fatalf("ThumbnailSide(sm) = %d, %v", side, ok)
	}
	if side, ok := ThumbnailSide(ThumbLarge); !ok || side != 1600 {
		t.Errorf("ThumbnailSide(lg) = %d, %v", side, ok)
	}
	if _, ok := ThumbnailSide("xl"); ok {
		t.Error("ThumbnailSide(xl) ok")
	}

	for _, format := range []string{"png", "tiff", "bmp", "jpeg"} {
		f := openTempImage(t, "a."+format, encodeImage(t, format, gradient(640, 400)))
		var reserved, released int
		var b bytes.Buffer
		err := WriteThumbnail(&b, f, side, func(cost int64) (func(), error) {
			if cost <= 0 {
				t.Errorf("%s: cost %d", format, cost)
			}
			reserved++
			return func() { released++ }, nil
		})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		img, err := jpeg.Decode(&b)
		if err != nil {
			t.Fatalf("%s: not a JPEG: %v", format, err)
		}
		if got := img.Bounds().Size(); got.X != 320 || got.Y != 200 || reserved != 1 || released != 1 {
			t.Errorf("%s: %v, reserved %d, released %d", format, got, reserved, released)
		}
		if w, h := ImageDimensions(f); w == nil || *w != 640 || *h != 400 {
			t.Errorf("%s: dimensions %v×%v", format, w, h)
		}
	}

	// Never scaled up; no reserve needed.
	f := openTempImage(t, "small.png", encodeImage(t, "png", gradient(40, 30)))
	var b bytes.Buffer
	if err := WriteThumbnail(&b, f, side, nil); err != nil {
		t.Fatal(err)
	}
	if img, err := jpeg.Decode(&b); err != nil {
		t.Errorf("small: %v", err)
	} else if img.Bounds().Dx() != 40 {
		t.Errorf("small: %v", img.Bounds())
	}

	// Not an image.
	f = openTempImage(t, "x.png", []byte("not an image"))
	if err := WriteThumbnail(&b, f, side, nil); !errors.Is(err, ErrUnsupportedImage) {
		t.Errorf("not an image: %v", err)
	}
	// A refused reservation stops it.
	f = openTempImage(t, "b.png", encodeImage(t, "png", gradient(64, 64)))
	stop := errors.New("stop")
	if err := WriteThumbnail(&b, f, side, func(int64) (func(), error) { return nil, stop }); !errors.Is(err, stop) {
		t.Errorf("refused reservation: %v", err)
	}
}

func TestImageContentType(t *testing.T) {
	for name, want := range map[string]string{
		"a.PNG": "image/png", "a.jpg": "image/jpeg", "a.jpeg": "image/jpeg", "a.tif": "image/tiff", "a.bmp": "image/bmp",
		"a.webp": "image/webp", "a.txt": "application/octet-stream",
	} {
		if got := ImageContentType(name); got != want {
			t.Errorf("ImageContentType(%q) = %s, want %s", name, got, want)
		}
	}
}
