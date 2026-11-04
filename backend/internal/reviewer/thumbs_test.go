package reviewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThumbnailFolderStaysInCache: the cache folder of a package is named
// by its ID, and an ID from review.json that is not one (a zip made to
// write elsewhere) cannot name a folder outside the cache.
func TestThumbnailFolderStaysInCache(t *testing.T) {
	cache := t.TempDir()
	for _, th := range []*thumbnailer{newThumbnailer(cache), newThumbnailer("")} {
		base := th.dir
		if base == "" {
			base = filepath.Join(os.TempDir(), "trainmaster-reviewer-thumbs")
		}
		id := strings.Repeat("ab", 16)
		if got := th.packageDir(id); got != filepath.Join(base, id) {
			t.Errorf("packageDir(%q) = %q", id, got)
		}
		seen := map[string]bool{}
		for _, id := range []string{
			"", ".", "..", "../../zz-escaped", `..\..\zz-escaped`, "/tmp/x", `C:\x`, "a/b",
			strings.Repeat("AB", 16), strings.Repeat("ab", 17), strings.Repeat("ab", 15) + "/.",
		} {
			got := th.packageDir(id)
			if filepath.Dir(got) != base || len(filepath.Base(got)) != 32 || strings.Trim(filepath.Base(got), "0123456789abcdef") != "" {
				t.Errorf("packageDir(%q) = %q, not a folder of %q", id, got, base)
			}
			if seen[got] {
				t.Errorf("packageDir(%q) = %q twice", id, got)
			}
			seen[got] = true
		}
	}
}
