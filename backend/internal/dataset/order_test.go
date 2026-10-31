package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// sortedPaths returns paths in image order.
func sortedPaths(paths []string) []string {
	out := slices.Clone(paths)
	slices.SortStableFunc(out, func(a, b string) int { return bytes.Compare(ImageSortKey(a), ImageSortKey(b)) })
	return out
}

// wantOrder checks that every shuffle of want sorts back to want.
func wantOrder(t *testing.T, want ...string) {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 5 {
		in := slices.Clone(want)
		if i == 0 {
			slices.Reverse(in)
		} else {
			r.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
		}
		if got := sortedPaths(in); !slices.Equal(got, want) {
			t.Errorf("order:\n got %q\nwant %q", got, want)
			return
		}
	}
}

// Orders of Windows Explorer (Windows 10/11, zh-CN), from observations on
// Windows and from CompareStringEx(NORM_IGNORECASE | SORT_DIGITSASNUMBERS)
// with Windows' sort table (Wine's port of it).
func TestImageSortKeyWindowsOrder(t *testing.T) {
	for _, order := range []string{
		// Observed in Explorer.
		"Boiago < Boi-Ago < Boingo < Boi-Ngo",
		"1._ < 1.0 < 1.1 < 1.txt",
		"a_.txt < a1.txt < a2.txt",
		"SSD-00-00 < SSD-01-01 < SSD1-00-00 < SSD-02-02",
		"My file revised.txt < My file.txt",
		"! < # < $ < % < & < ( < ) < , < ; < @ < [ < ] < ^ < _ < ` < { < } < ~ < + < =",
		// Microsoft's word sort example.
		"billet < bills < bill's < cannot < cant < can't < con < coop < co-op",
		// From the sort table.
		"_z.jpg < a (1).jpg < a b.jpg < a(1).jpg < a.jpg < a_b.jpg < a1.jpg < ab.jpg < a-b.jpg < z.jpg < 'z.jpg < -z.jpg",
		"file - Copy.jpg < file (2).jpg < file 1.jpg < file(1).jpg < file.jpeg < file.jpg < file.png < file_1.jpg < file1.jpg < file-3.jpg",
		"a b < a　b < a(1) < a（1） < a,b < a，b < a、b < a.b < a。b < a[1] < a_b < a＿b < a《b》 < a【1】 < a<b> < a1 < ab < a-b < a－b < a一",
		"a~ < a“b” < a「b」 < a【b】 < a+ < a× < a… < a1 < a① < a²",
		"img001.jpg < img01.jpg < img1.jpg < img2.jpg < img10.jpg",
		"a01z < a1b",
		"00 < 0 < 1 < 9 < 10 < 99 < 100",
		"１2 < 3 < 12", // full-width and ASCII digits make two numbers: 1, 2
		// Other number characters, by value.
		"① < ② < ③ < ⑨ < ⑩ < ⑪ < ⑫ < ⑳",
		"工位①_001.jpg < 工位②_001.jpg < 工位⑨_001.jpg < 工位⑩_001.jpg < 工位⑪_001.jpg < 工位⑫_001.jpg",
		"a9 < a10 < a⅛ < a½ < a① < a⑵ < a⒊ < a④ < a⑩ < a⑳ < aⅠ < aⅣ < aⅤ < aⅨ < aⅩ < a∞ < ab",
		"Ⅰ类缺陷_01.jpg < Ⅱ类缺陷_01.jpg < Ⅲ类缺陷_01.jpg < Ⅳ类缺陷_01.jpg",
		"1级 < 9级 < Ⅰ级 < Ⅱ级 < Ⅲ级 < h级 < ia级 < z级",
		"a10 < a߁߀ < a߃ < ab", // N'Ko digits are not compared as numbers
		// Spaces, and the symbols of GB 2312.
		"a b < a\u00a0b < a\u2009b < a\u200bb < a!b",
		"a z.jpg < a\u200bz.jpg < ab.jpg",
		"a~ < a¨ < a‖ < a′ < a¢ < a》 < a【 < a+ < a∈ < a≡ < a⊥ < a⌒ < a■ < a○ < a↑ < a§ < a・ < a♂ < a† < a‰ < a※ < a1",
		"a《b < a¢z", // the same weight
		"a≠b < a=z",
		// Characters without weight.
		"aa.jpg < a\ufffdb.jpg < b.jpg",
		"测试\ufffd.jpg < 测试_1.jpg < 测试1.jpg",
		"a\u200db.jpg < ab1.jpg < abc.jpg",
		"a\u00adb.jpg < ab1.jpg < abc.jpg",
	} {
		wantOrder(t, strings.Split(order, " < ")...)
	}
}

func TestImageSortKeyOrder(t *testing.T) {
	// Digit runs are numbers; case and width are ignored.
	wantOrder(t, "IMG_1.jpg", "img_2.JPG", "Img_10.jpg", "img_11.jpg", "img_100.jpg", "ｉｍｇ_１０１.jpg")
	wantOrder(t, "2023-01-05 08.30.11.jpg", "2023-01-05 8.30.12.jpg", "2023-01-05 10.00.00.jpg", "2023-1-6.jpg")
	// Windows compares at most 19 digits as one number.
	wantOrder(t, "1234567890123456789.jpg", "12345678901234567890.jpg", "1234567890123456790.jpg", "9999999999999999999.jpg")
	// Symbols, then digits, then letters, then Chinese characters (pinyin).
	wantOrder(t, " a.jpg", "(1).jpg", "_a.jpg", "0.jpg", "9.jpg", "A.jpg", "z.jpg", "α.jpg", "安.jpg", "划痕.jpg", "压伤.jpg",
		"异物.jpg", "正常.jpg")
	// Like a folder tree: folders first at every level.
	wantOrder(t, "NG/2/a.jpg", "NG/10/a.jpg", "NG/a.jpg", "NG/b.jpg", "OK/a.jpg", "ok2/a.jpg", "a.jpg", "b.jpg", "ng.jpg")
	wantOrder(t, "a/z.jpg", "a b/a.jpg", "a.jpg")
	// Names equal but for case, width or Unicode form: by code point.
	wantOrder(t, "A.jpg", "a.jpg", "ａ.jpg")
	wantOrder(t, "é.jpg", "é.jpg", "f.jpg")
	wantOrder(t, "a①.jpg", "a⑴.jpg", "a⒈.jpg", "aⅠ.jpg", "aⅰ.jpg")
	// The Chinese numbers in parentheses and circles follow the other
	// numerals (Windows puts them after the Chinese characters).
	wantOrder(t, "a∞.jpg", "a㈠.jpg", "a㈡.jpg", "a㈩.jpg", "a㊀.jpg", "a㊉.jpg", "ab.jpg")
}

func TestImageSortKeyDistinct(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	const alphabet = "aAbB01９ _-'.()（）【】、。中文é́/①⑴Ⅰⅰ½㈠\u200b\ufffd"
	chars := []rune(alphabet)
	seen := map[string]string{}
	for range 20000 {
		var b strings.Builder
		for range 1 + r.IntN(8) {
			b.WriteRune(chars[r.IntN(len(chars))])
		}
		p := b.String()
		if strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
			continue
		}
		k := string(ImageSortKey(p))
		if other, ok := seen[k]; ok && other != p {
			t.Fatalf("%q and %q have the same key", p, other)
		}
		seen[k] = p
	}
}

func TestImageSortKeyLength(t *testing.T) {
	long := strings.Repeat("很长的文件夹名称/", 100) + "a.jpg"
	if k := ImageSortKey(long); len(k) != maxSortKeyLen {
		t.Errorf("key of a %d-byte path: %d bytes", len(long), len(k))
	}
	// Within the limit, the beginning still orders the paths.
	if got := sortedPaths([]string{"b/" + long, "a/" + long}); got[0] != "a/"+long {
		t.Errorf("long paths out of order")
	}
}

func TestImageSortKeyConcurrent(t *testing.T) {
	want := hex.EncodeToString(ImageSortKey("NG/划痕 (12).jpg"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if got := hex.EncodeToString(ImageSortKey("NG/划痕 (12).jpg")); got != want {
					t.Errorf("key = %s, want %s", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestImageSortKeyStable fails when the keys change: increase
// imageOrderRevision, so the stored keys are recomputed, and update the
// hash here. When an upgrade of Go or golang.org/x/text changes the keys,
// imageOrderVersion may change by itself; increase imageOrderRevision
// anyway.
func TestImageSortKeyStable(t *testing.T) {
	paths := []string{
		"IMG_0001.JPG", "img_2.jpg", "NG/划痕/0001.png", "OK/正常 (3).jpg", "a-b_c d.bmp", "ｆｕｌｌ１２.jpg", "é́x.tif",
		"【标注】《说明》、。.png", "2023-01-05 08.30.11.jpg", "12345678901234567890123.jpg", "a\u200bb²①…×→☆.jpg",
		"Ⅲ类/⑩½㈠ ・‰≠\ufffd.jpg",
	}
	h := sha256.New()
	for _, p := range paths {
		h.Write(ImageSortKey(p))
		h.Write([]byte{0xFF})
	}
	const want = "0a413a44c8be38a282cfda564a5a2bfe1e3586e350793294f91801f7b2bd35a4"
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		t.Errorf("sort keys changed (hash %s, want %s): increase imageOrderRevision", got, want)
	}
}

// TestIntegrationImageOrder checks that the list (with any page size), the
// covers and downloads all follow the image order, and that the stored
// keys are refreshed at start-up.
func TestIntegrationImageOrder(t *testing.T) {
	e := newTestEnv(t, true)
	ctx := context.Background()
	want := []string{
		"NG/2/a.png", "NG/10/a.png", "NG/img2.png", "NG/img10.png", "OK/x.png",
		"a (1).png", "a.png", "a_1.png", "img01.png", "IMG1.png", "img2.png", "img10.png", "划痕.png", "正常.png",
	}
	d, err := e.svc.CreateDataset(ctx, CreateDatasetInput{Name: "排序"})
	if err != nil {
		t.Fatal(err)
	}
	img := pngFile(t, 8, 8)
	files := map[string][]byte{}
	for _, p := range want {
		files[p] = img
	}
	writeFiles(t, d.Path, files)
	scan, err := e.svc.StartScan(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.waitJob(t, d.ID, scan.ID); job.Status != StatusSucceeded {
		t.Fatalf("scan = %+v", job)
	}

	list := func(q ImageQuery) []string {
		t.Helper()
		items, _, err := e.svc.ListImages(ctx, d.ID, q)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, img := range items {
			paths = append(paths, img.Path)
		}
		return paths
	}
	wantList := func(what string) {
		t.Helper()
		if got := list(ImageQuery{Limit: 100}); !slices.Equal(got, want) {
			t.Errorf("%s: images\n got %q\nwant %q", what, got, want)
		}
	}
	wantList("after scan")
	for _, size := range []int{1, 3} {
		var paged []string
		for offset := 0; offset < len(want); offset += size {
			paged = append(paged, list(ImageQuery{Offset: offset, Limit: size})...)
		}
		if !slices.Equal(paged, want) {
			t.Errorf("pages of %d: %q", size, paged)
		}
	}
	if got := list(ImageQuery{Limit: 100, Search: "IMG"}); !slices.Equal(got, []string{
		"NG/img2.png", "NG/img10.png", "img01.png", "IMG1.png", "img2.png", "img10.png",
	}) {
		t.Errorf("search: %q", got)
	}

	// Covers are the first images.
	all, _, err := e.svc.ListImages(ctx, d.ID, ImageQuery{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetDataset(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range got.Covers {
		if c.ID != all[i].ID {
			t.Errorf("cover %d = image %d, want %d (%s)", i, c.ID, all[i].ID, all[i].Path)
		}
	}

	// Downloads list the images in the same order.
	ticket, err := e.svc.CreateExport(ctx, d.ID, ExportInput{Format: FormatVOC})
	if err != nil {
		t.Fatal(err)
	}
	ex, err := e.svc.OpenExport(ctx, d.ID, ticket.Token)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = ex.Write(ctx, &buf)
	ex.Close()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, f := range zr.File {
		entries = append(entries, strings.TrimPrefix(f.Name, d.DirName+"/"))
	}
	if !slices.Equal(entries, want) {
		t.Errorf("archive entries\n got %q\nwant %q", entries, want)
	}

	// Start-up gives rows without a key their key, and recomputes all keys
	// when they were made by another version of the order.
	var version string
	if err := e.db.QueryRow(ctx, "SELECT value->>'version' FROM app_settings WHERE key = $1",
		imageOrderSetting).Scan(&version); err != nil || version != imageOrderVersion {
		t.Errorf("stored order version = %q, %v; want %q", version, err, imageOrderVersion)
	}
	if _, err := e.db.Exec(ctx, "UPDATE dataset_images SET sort_key = NULL WHERE rel_path LIKE 'NG/%'"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.refreshSortKeys(ctx); err != nil {
		t.Fatal(err)
	}
	wantList("after filling missing keys")
	if _, err := e.db.Exec(ctx, `UPDATE dataset_images SET sort_key = '\x00';
		UPDATE app_settings SET value = '{"version": "0/old"}' WHERE key = 'dataset.image_order'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.refreshSortKeys(ctx); err != nil {
		t.Fatal(err)
	}
	wantList("after a new order version")
	rows, err := e.db.Query(ctx, "SELECT rel_path, sort_key FROM dataset_images WHERE dataset_id = $1", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rel string
	var key []byte
	if _, err := pgx.ForEachRow(rows, []any{&rel, &key}, func() error {
		if !bytes.Equal(key, ImageSortKey(rel)) {
			t.Errorf("%s: stored key %x, want %x", rel, key, ImageSortKey(rel))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(ctx, "SELECT value->>'version' FROM app_settings WHERE key = $1",
		imageOrderSetting).Scan(&version); err != nil || version != imageOrderVersion {
		t.Errorf("stored order version = %q, %v; want %q", version, err, imageOrderVersion)
	}
}
