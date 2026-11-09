package dataset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Renaming images that share a name (同名图片重命名). Images in different
// folders of a dataset may have the same name, such as cam1/0001.jpg and
// cam2/0001.jpg. The platform tells them apart by their paths, but tools
// that gather the images of a dataset into one folder, or look annotation
// files up by name alone, mix them up. An import therefore gives an image
// whose name is taken by another image with different content a name of
// its own, together with its annotation file:
//
//   - Names are compared without the extension and ignoring case, across all
//     folders of the dataset: 0001.jpg, 0001.PNG and 0001.bmp share a name,
//     as their annotation files (0001.xml, 0001.txt, 0001.json) would.
//   - The images already in the dataset keep their names. Of the new images,
//     the first in image order (see ImageSortKey) keeps a free name; a later
//     one whose name is taken becomes <stem>_2 (then _3, ...) in its own
//     folder: the first such name that no image or XML file of the dataset
//     or the upload has. Its VOC file is renamed the same way in its own
//     folder (next to the image, or in Annotations/), and the <filename> and
//     <path> recorded in it name the new file.
//   - An image whose content is byte for byte that of the images holding
//     its name keeps its name: it is the same picture stored in two places.
//     Imports only treat an image at the same path as a duplicate (it is
//     skipped or replaced, see ConflictSkip), so the platform keeps both,
//     and lists the image as identical.
//   - A copy of an image that was renamed gets the same numbered name in its
//     own folder: with cam1/0001.jpg (A), cam2/0001.jpg (B) and cam3/0001.jpg
//     (B again), cam2 and cam3 both become 0001_2.jpg, so that one name still
//     means one picture. This applies when every image holding <stem>_<k> has
//     that content and no file in the image's folder (or its annotation's)
//     has the name yet; otherwise the image gets the next free number.
//   - The same upload imported again finds the names its images got the
//     first time: an image whose numbered name exists in its folder in the
//     dataset with the same content goes there, and is skipped or replaced
//     like any image whose path exists.
//
// Uploads (archives and folders) are renamed in their staging folder after
// the annotations were converted to VOC and before they are moved into the
// dataset. A folder added with "添加已有目录" is renamed in place by its
// first scan (Import.RenameImages); later scans never rename: images are
// referenced by path once they are in a dataset (reviews, versions).

// Kinds of entries in an import's rename list.
const (
	RenameRenamed   = "renamed"   // got a new name with its annotation file
	RenameIdentical = "identical" // kept its name: the same content as the image holding it
)

// ImageRename is an image an import renamed, or found identical to the
// image holding its name. The JSON field names are part of the API
// contract with the frontend (frontend/src/features/datasets/api.ts).
type ImageRename struct {
	Kind         string `json:"kind"`          // RenameRenamed or RenameIdentical
	OriginalPath string `json:"original_path"` // in the upload or folder, relative to the dataset folder
	Path         string `json:"path"`          // in the dataset now (OriginalPath when identical)
	SameNameAs   string `json:"same_name_as"`  // the image whose name it has (or had)
	// AnnotationPath is the renamed VOC file, nil when the image has none
	// (and for identical images, whose files keep their names).
	AnnotationPath *string `json:"annotation_path"`
}

// maxFileNameBytes is the longest file name the common file systems take.
const maxFileNameBytes = 255

// maxSuffixDigits is the most digits a number k of <stem>_<k> has.
const maxSuffixDigits = 19

// renamePlan is what renaming does with one new image.
type renamePlan struct {
	From, To       string // the image's path before and after (equal when identical)
	XMLFrom, XMLTo string // its VOC file before and after, "" without one
	SameNameAs     string // the first image that holds the name
	Identical      bool
	seq            int // its place in the rename list (image order)
}

func (p *renamePlan) entry() ImageRename {
	e := ImageRename{Kind: RenameRenamed, OriginalPath: p.From, Path: p.To, SameNameAs: p.SameNameAs}
	if p.Identical {
		e.Kind = RenameIdentical
	} else if p.XMLTo != "" {
		xmlTo := p.XMLTo
		e.AnnotationPath = &xmlTo
	}
	return e
}

// nameRef is an image whose content renaming may compare.
type nameRef struct {
	rel    string // in its folder
	staged bool   // in the upload's staging folder rather than the dataset folder
	size   int64
}

// nameGroup is the images that hold one name (lower-case stem).
type nameGroup struct {
	first string    // the first holder, for the list
	reps  []nameRef // one image per distinct content found so far
	// pending holds images of the dataset not compared yet: they are only
	// compared when a new image wants the name, and only until two
	// different contents are known (a new image cannot equal both).
	pending []nameRef
}

// namePlanner decides the names of the new images of one job.
type namePlanner struct {
	ctx      context.Context
	taken    map[string]bool    // lower stems of every image and XML file, and of the new names
	incoming map[string]bool    // lower stems of the new images and XML files
	paths    map[string]bool    // lower paths without extension of every image and XML file, and of the new names
	existing map[string]nameRef // the images of the dataset by path
	groups   map[string]*nameGroup
	reused   map[string]bool // dataset paths a new image goes to again
	// next is, per lower stem and extension length, the smallest number
	// <stem>_<k> may still be free at: the numbers below it are taken, so
	// that numbering many images of one name stays linear.
	next map[string]int
	// reuse finds the images of the dataset at a numbered name of a new
	// image in its folder; join the numbered names an identical copy may
	// get (see numberedIndex).
	reuse map[reuseKey][]int
	join  map[joinKey]*joinBucket
	// same reports whether two images have the same content (images of
	// different sizes never do); digest returns a hash of an image's
	// content, for comparing an image with many images of its size at once.
	same    func(ctx context.Context, a, b nameRef) (bool, error)
	digest  func(ctx context.Context, r nameRef) (string, error)
	digests map[nameRef]string
}

// reuseKey is a new image's folder and lower stem (dir/stem) with its size.
type reuseKey struct {
	dirStem string
	size    int64
}

// joinKey is a lower stem and a size: the numbered names <stem>_<k>
// created from that stem whose image has that size.
type joinKey struct {
	stem string
	size int64
}

// joinBucket holds the numbers k of one joinKey. They are compared one by
// one while there are few; once there are more they are indexed by the
// digest of their image, so that every image is read at most once (BMP and
// TIFF images of one camera all have the same size).
type joinBucket struct {
	ks       []int
	byDigest map[string][]int // nil until indexed
}

// joinDirect is how many numbers a bucket compares one by one.
const joinDirect = 2

// nameStem is the lower-case name of the file at rel without its
// extension: the name renaming compares.
func nameStem(rel string) string {
	base := path.Base(rel)
	return strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
}

// newNamePlanner prepares the planning of the new images: existing are the
// images of the dataset (path → file), existingXMLs its XML files, staged
// the new images and their sizes, stagedXMLs the new XML files. In
// overwrite mode a new image at the path of an image of the dataset
// replaces it, so it stands for that image from the start.
func newNamePlanner(ctx context.Context, existing map[string]diskFile, existingXMLs []string,
	staged map[string]diskFile, stagedXMLs []string, onConflict string,
	same func(ctx context.Context, a, b nameRef) (bool, error),
	digest func(ctx context.Context, r nameRef) (string, error)) *namePlanner {
	pl := &namePlanner{
		ctx:      ctx,
		taken:    map[string]bool{},
		incoming: map[string]bool{},
		paths:    map[string]bool{},
		existing: make(map[string]nameRef, len(existing)),
		groups:   map[string]*nameGroup{},
		reused:   map[string]bool{},
		next:     map[string]int{},
		reuse:    map[reuseKey][]int{},
		join:     map[joinKey]*joinBucket{},
		same:     same,
		digest:   digest,
		digests:  map[nameRef]string{},
	}
	for rel := range staged {
		pl.incoming[nameStem(rel)] = true
		pl.paths[pathStem(rel)] = true
	}
	for _, rel := range stagedXMLs {
		pl.incoming[nameStem(rel)] = true
		pl.paths[pathStem(rel)] = true
	}
	for _, rel := range existingXMLs {
		pl.taken[nameStem(rel)] = true
		pl.paths[pathStem(rel)] = true
	}
	for rel := range existing {
		pl.paths[pathStem(rel)] = true
	}
	maps.Copy(pl.taken, pl.incoming)

	for _, rel := range sortedByImageOrder(slices.Collect(maps.Keys(existing))) {
		ref := nameRef{rel: rel, size: existing[rel].Size}
		if f, ok := staged[rel]; ok && onConflict == ConflictOverwrite {
			ref = nameRef{rel: rel, staged: true, size: f.Size}
		}
		pl.existing[rel] = ref
		s := nameStem(rel)
		pl.taken[s] = true
		g := pl.groups[s]
		if g == nil {
			g = &nameGroup{first: rel}
			pl.groups[s] = g
		}
		g.pending = append(g.pending, ref)
		if prefix, k, ok := splitNumbered(s); ok {
			key := reuseKey{strings.ToLower(path.Dir(rel)) + "/" + prefix, ref.size}
			pl.reuse[key] = append(pl.reuse[key], k)
			_ = pl.addNumbered(prefix, k, ref) // no bucket is indexed yet: no digest, no error
		}
	}
	return pl
}

// pathStem is the lower-case path of rel without its extension: two files
// with the same path stem pair with the same VOC file.
func pathStem(rel string) string {
	return strings.ToLower(strings.TrimSuffix(rel, path.Ext(rel)))
}

// splitNumbered splits the lower stem s of a numbered name, <prefix>_<k>
// with k >= 2 written without leading zeros, into prefix and k.
func splitNumbered(s string) (prefix string, k int, ok bool) {
	i := strings.LastIndexByte(s, '_')
	if i <= 0 {
		return "", 0, false
	}
	digits := s[i+1:]
	k, err := strconv.Atoi(digits)
	if err != nil || k < 2 || strconv.Itoa(k) != digits {
		return "", 0, false
	}
	return s[:i], k, true
}

// addNumbered records that the numbered name <stem>_<k> (stem in lower
// case) holds the image r.
func (pl *namePlanner) addNumbered(stem string, k int, r nameRef) error {
	key := joinKey{stem, r.size}
	b := pl.join[key]
	if b == nil {
		b = &joinBucket{}
		pl.join[key] = b
	}
	if b.byDigest == nil {
		b.ks = append(b.ks, k)
		return nil
	}
	d, err := pl.digestOf(r)
	if err != nil {
		return err
	}
	b.byDigest[d] = append(b.byDigest[d], k)
	return nil
}

// digestOf returns the (cached) digest of the image r.
func (pl *namePlanner) digestOf(r nameRef) (string, error) {
	if d, ok := pl.digests[r]; ok {
		return d, nil
	}
	d, err := pl.digest(pl.ctx, r)
	if err != nil {
		return "", err
	}
	pl.digests[r] = d
	return d, nil
}

// sortedByImageOrder sorts slash-separated paths in image order.
func sortedByImageOrder(paths []string) []string {
	keys := make(map[string][]byte, len(paths))
	for _, p := range paths {
		keys[p] = ImageSortKey(p)
	}
	slices.SortFunc(paths, func(a, b string) int {
		if c := bytes.Compare(keys[a], keys[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return paths
}

// plan decides the name of the new image rel (size bytes) whose VOC file
// is xmlRel ("" for none). It returns nil when the image keeps its name
// without a conflict, or its path exists in the dataset (the import skips
// or replaces it).
func (pl *namePlanner) plan(rel string, size int64, xmlRel string) (*renamePlan, error) {
	if _, ok := pl.existing[rel]; ok {
		return nil, nil
	}
	ref := nameRef{rel: rel, staged: true, size: size}
	s := nameStem(rel)
	g := pl.groups[s]
	if g == nil {
		pl.groups[s] = &nameGroup{first: rel, reps: []nameRef{ref}}
		return nil, nil
	}
	if err := pl.resolve(g); err != nil {
		return nil, err
	}
	if len(g.reps) == 1 {
		eq, err := pl.same(pl.ctx, g.reps[0], ref)
		if err != nil {
			return nil, err
		}
		if eq {
			return &renamePlan{From: rel, To: rel, SameNameAs: g.first, Identical: true}, nil
		}
	}

	to, k, reuse, err := pl.newName(rel, ref, xmlRel)
	if err != nil {
		return nil, err
	}
	p := &renamePlan{From: rel, To: to, SameNameAs: g.first}
	pl.paths[pathStem(to)] = true
	if xmlRel != "" {
		p.XMLFrom = xmlRel
		p.XMLTo = path.Join(path.Dir(xmlRel), strings.TrimSuffix(path.Base(to), path.Ext(to))+".xml")
		pl.paths[pathStem(p.XMLTo)] = true
	}
	ns := nameStem(to)
	pl.taken[ns] = true
	switch {
	case reuse:
		pl.reused[to] = true
	case pl.groups[ns] == nil: // a free name
		pl.groups[ns] = &nameGroup{first: to, reps: []nameRef{ref}}
		if err := pl.addNumbered(s, k, ref); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// resolve compares the pending images of g until two different contents
// are known or none is left.
func (pl *namePlanner) resolve(g *nameGroup) error {
	for len(g.pending) > 0 && len(g.reps) < 2 {
		next := g.pending[0]
		g.pending = g.pending[1:]
		known := false
		for _, r := range g.reps {
			eq, err := pl.same(pl.ctx, r, next)
			if err != nil {
				return err
			}
			if eq {
				known = true
				break
			}
		}
		if !known {
			g.reps = append(g.reps, next)
		}
	}
	return nil
}

// newName finds the new path of the image rel (content ref, VOC file
// xmlRel or ""), <stem>_<k> in its folder, and returns it with k:
//
//  1. the path of an image of the dataset with the same content (reuse:
//     the image was renamed so by an earlier import of the same files);
//  2. else the smallest k whose name only images with this content hold,
//     when nothing in the image's folder (or its VOC file's) has it yet: a
//     copy of an image renamed from the same name;
//  3. else the smallest k whose name is free.
func (pl *namePlanner) newName(rel string, ref nameRef, xmlRel string) (to string, k int, reuse bool, err error) {
	if err := pl.ctx.Err(); err != nil {
		return "", 0, false, err
	}
	dir, base := path.Split(rel)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	s := strings.ToLower(stem)

	ks := pl.reuse[reuseKey{strings.ToLower(path.Dir(rel)) + "/" + s, ref.size}]
	if len(stem)+len(ext)+len("_")+maxSuffixDigits > maxFileNameBytes {
		// numberedStem may shorten the stem, so the index (by the stem of
		// the numbered names) does not know these names: try every taken
		// number instead (rare: names of about 230 bytes and more).
		ks = nil
		for k := 2; pl.taken[strings.ToLower(numberedStem(stem, ext, k))]; k++ {
			ks = append(ks, k)
		}
	}
	for _, k := range sortedUnique(ks) {
		t := numberedStem(stem, ext, k)
		cand := dir + t + ext
		e, ok := pl.existing[cand]
		if !ok || pl.incoming[strings.ToLower(t)] || pl.reused[cand] {
			continue
		}
		eq, err := pl.same(pl.ctx, e, ref)
		if err != nil {
			return "", 0, false, err
		}
		if eq {
			return cand, k, true, nil
		}
	}

	if k, ok, err := pl.joinable(s, stem, ext, rel, xmlRel, ref); err != nil {
		return "", 0, false, err
	} else if ok {
		return dir + numberedStem(stem, ext, k) + ext, k, false, nil
	}

	key := s + "/" + strconv.Itoa(len(ext))
	k = max(pl.next[key], 2)
	for pl.taken[strings.ToLower(numberedStem(stem, ext, k))] {
		k++
	}
	pl.next[key] = k + 1
	return dir + numberedStem(stem, ext, k) + ext, k, false, nil
}

// joinable returns the smallest k such that the numbered name <stem>_<k>
// (s is stem in lower case) is held by images with the content of ref only
// and no file has it yet where the image would pair with it: in the folder
// of rel, of its VOC file xmlRel, or the Annotations/ folder next to a
// JPEGImages/ (or images/) folder holding rel (see pairAnnotations).
func (pl *namePlanner) joinable(s, stem, ext, rel, xmlRel string, ref nameRef) (int, bool, error) {
	b := pl.join[joinKey{s, ref.size}]
	if b == nil {
		return 0, false, nil
	}
	dir := path.Dir(rel)
	vocDir := ""
	if d := strings.ToLower(path.Base(dir)); d == "jpegimages" || d == "images" {
		vocDir = path.Join(path.Dir(dir), "annotations")
	}
	// fits reports whether the name with number k may be joined: d is the
	// digest of ref, or "" to compare the images themselves.
	fits := func(k int, d string) (bool, error) {
		t := numberedStem(stem, ext, k)
		lt := strings.ToLower(t)
		g := pl.groups[lt]
		if g == nil || pl.incoming[lt] || pl.paths[strings.ToLower(path.Join(dir, t))] ||
			(xmlRel != "" && pl.paths[strings.ToLower(path.Join(path.Dir(xmlRel), t))]) ||
			(vocDir != "" && pl.paths[strings.ToLower(path.Join(vocDir, t))]) {
			return false, nil
		}
		if err := pl.resolve(g); err != nil {
			return false, err
		}
		if len(g.reps) != 1 || g.reps[0].size != ref.size {
			return false, nil
		}
		if d == "" {
			return pl.same(pl.ctx, g.reps[0], ref)
		}
		rd, err := pl.digestOf(g.reps[0])
		return rd == d, err
	}

	if b.byDigest == nil && len(b.ks) <= joinDirect {
		for _, k := range sortedUnique(b.ks) {
			if ok, err := fits(k, ""); err != nil || ok {
				return k, ok, err
			}
		}
		return 0, false, nil
	}
	if b.byDigest == nil {
		// Index the numbers by the digest of the image holding them.
		b.byDigest = map[string][]int{}
		for _, k := range b.ks {
			g := pl.groups[strings.ToLower(numberedStem(stem, ext, k))]
			if g == nil {
				continue
			}
			if err := pl.resolve(g); err != nil {
				return 0, false, err
			}
			if len(g.reps) != 1 || g.reps[0].size != ref.size {
				continue
			}
			d, err := pl.digestOf(g.reps[0])
			if err != nil {
				return 0, false, err
			}
			b.byDigest[d] = append(b.byDigest[d], k)
		}
		b.ks = nil
	}
	d, err := pl.digestOf(ref)
	if err != nil {
		return 0, false, err
	}
	for _, k := range sortedUnique(b.byDigest[d]) {
		if ok, err := fits(k, d); err != nil || ok {
			return k, ok, err
		}
	}
	return 0, false, nil
}

// sortedUnique returns the numbers ks in increasing order, without repeats.
func sortedUnique(ks []int) []int {
	if len(ks) < 2 {
		return ks
	}
	out := slices.Clone(ks)
	slices.Sort(out)
	return slices.Compact(out)
}

// numberedStem returns stem_k, with stem shortened (at a character
// boundary) so that the name with ext stays within maxFileNameBytes.
func numberedStem(stem, ext string, k int) string {
	suffix := "_" + strconv.Itoa(k)
	if room := maxFileNameBytes - len(ext) - len(suffix); len(stem) > room {
		cut := max(room, 0)
		for cut > 0 && !utf8.RuneStart(stem[cut]) {
			cut--
		}
		stem = stem[:cut]
	}
	return stem + suffix
}

// sameContent reports whether the files a and b (of sizes sa and sb) have
// the same bytes. A file that cannot be read counts as different (the image
// is then renamed, which is always safe).
func sameContent(ctx context.Context, a, b string, sa, sb int64) (bool, error) {
	if sa != sb {
		return false, nil
	}
	fa, err := os.Open(a)
	if err != nil {
		return false, nil
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, nil
	}
	defer fb.Close()
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		endA := errA == io.EOF || errors.Is(errA, io.ErrUnexpectedEOF)
		endB := errB == io.EOF || errors.Is(errB, io.ErrUnexpectedEOF)
		switch {
		case endA && endB:
			return true, nil
		case errA != nil && !endA, errB != nil && !endB, endA != endB:
			return false, nil
		}
	}
}

// fileDigest returns the SHA-256 of the file at name. A file that cannot be
// read gets a digest of its own (it equals no other file, see sameContent).
func fileDigest(ctx context.Context, name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "unreadable:" + name, nil
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			return string(h.Sum(nil)), nil
		}
		if err != nil {
			return "unreadable:" + name, nil
		}
	}
}

// renameResult counts what renaming did.
type renameResult struct {
	Renamed, Identical int
	Failed             int // in place only: renames the file system refused
}

// planRenames walks the new images (in image order) and plans their names.
// images are the new images with their sizes, pairs their VOC files.
func planRenames(pl *namePlanner, images map[string]diskFile, pairs map[string]string, tick func(done int)) ([]*renamePlan, error) {
	var plans []*renamePlan
	for i, rel := range sortedByImageOrder(slices.Collect(maps.Keys(images))) {
		p, err := pl.plan(rel, images[rel].Size, pairs[rel])
		if err != nil {
			return nil, err
		}
		if p != nil {
			p.seq = len(plans)
			plans = append(plans, p)
		}
		if tick != nil {
			tick(i + 1)
		}
	}
	return plans, nil
}

// renameStaged renames the new images of an upload staged below staged,
// and their VOC files, whose names other images of the dataset in root or
// of the upload have (see the top of this file). It returns the plans of
// the renamed and identical images, in image order.
func (s *Service) renameStaged(ctx context.Context, root *os.Root, staged, onConflict string,
	p *progressTracker) ([]*renamePlan, error) {
	p.setStage(ctx, stageRenaming, unitFiles, 0)
	in, err := walkFiles(ctx, s.log, staged, nil)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // nothing was staged
	}
	if err != nil {
		return nil, fmt.Errorf("list staged files: %w", err)
	}
	if len(in.Images) == 0 {
		return nil, nil
	}
	var walked int64
	have, err := walkFiles(ctx, s.log, root.Name(), func() {
		walked++
		p.set(ctx, walked, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("walk dataset folder: %w", err)
	}
	src, err := os.OpenRoot(staged)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	file := func(r nameRef) string {
		if r.staged {
			return filepath.Join(staged, filepath.FromSlash(r.rel))
		}
		return filepath.Join(root.Name(), filepath.FromSlash(r.rel))
	}
	same := func(ctx context.Context, a, b nameRef) (bool, error) {
		return sameContent(ctx, file(a), file(b), a.size, b.size)
	}
	digest := func(ctx context.Context, r nameRef) (string, error) { return fileDigest(ctx, file(r)) }
	pl := newNamePlanner(ctx, have.Images, have.xmlPaths(), in.Images, in.xmlPaths(), onConflict, same, digest)
	cache := newAnnotationCache(src)
	pairs := pairAnnotations(in.imagePaths(), in.xmlPaths(), cache.filename)
	total := int64(len(in.Images))
	p.setStage(ctx, stageRenaming, unitFiles, total)
	plans, err := planRenames(pl, in.Images, pairs, func(done int) { p.set(ctx, int64(done), total) })
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !plan.Identical {
			if err := renameImageFiles(src, plan, false); err != nil {
				return nil, fmt.Errorf("rename staged image %s: %w", plan.From, err)
			}
		}
	}
	return plans, nil
}

// renameInPlace renames the images of the dataset folder in root whose
// names are taken (the first scan of a folder added as a dataset; there
// are no other images yet). The renamed and identical images are passed to
// record in batches as they are done, so that a crash loses little of the
// list. It stops at the next image when ctx ends. An image the file system
// does not let it rename (a read-only folder, a full disk) keeps its name
// and is counted as failed; the job goes on.
func (s *Service) renameInPlace(ctx context.Context, root *os.Root, p *progressTracker,
	record func([]*renamePlan)) (renameResult, error) {
	var res renameResult
	p.setStage(ctx, stageRenaming, unitFiles, 0)
	var walked int64
	set, err := walkFiles(ctx, s.log, root.Name(), func() {
		walked++
		p.set(ctx, walked, 0)
	})
	if err != nil {
		return res, fmt.Errorf("walk dataset folder: %w", err)
	}
	file := func(r nameRef) string { return filepath.Join(root.Name(), filepath.FromSlash(r.rel)) }
	same := func(ctx context.Context, a, b nameRef) (bool, error) {
		return sameContent(ctx, file(a), file(b), a.size, b.size)
	}
	digest := func(ctx context.Context, r nameRef) (string, error) { return fileDigest(ctx, file(r)) }
	pl := newNamePlanner(ctx, nil, nil, set.Images, set.xmlPaths(), ConflictSkip, same, digest)
	cache := newAnnotationCache(root)
	pairs := pairAnnotations(set.imagePaths(), set.xmlPaths(), cache.filename)
	total := int64(len(set.Images))
	p.setStage(ctx, stageRenaming, unitFiles, total)
	plans, err := planRenames(pl, set.Images, pairs, func(done int) { p.set(ctx, int64(done), total) })
	if err != nil {
		return res, err
	}
	var batch []*renamePlan
	defer func() { record(batch) }()
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if len(batch) == renameRecordBatch {
			record(batch)
			batch = nil
		}
		if plan.Identical {
			res.Identical++
			batch = append(batch, plan)
			continue
		}
		err := renameImageFiles(root, plan, true)
		var left *leftoverError
		switch {
		case errors.As(err, &left):
			s.log.Warn("renamed an image but cannot remove its old annotation file", "path", left.path, "err", left.err)
		case err != nil:
			s.log.Warn("cannot rename image with a taken name", "path", plan.From, "to", plan.To, "err", err)
			res.Failed++
			continue
		}
		res.Renamed++
		batch = append(batch, plan)
	}
	return res, nil
}

// renameRecordBatch is how many entries of a rename list are stored at a
// time while the images are renamed (in place) or moved (uploads).
const renameRecordBatch = 500

// renameImageFiles renames the image of plan inside root, and its VOC file
// with the <filename> and <path> recorded in it updated. Neither target may
// exist. A failure leaves the image and its file as they were, except when
// only the old VOC file could not be removed after its renamed copy was
// written (a *leftoverError: the rename itself is done). durable writes the
// renamed VOC file like a saved annotation (with the permissions of the old
// one, synced to disk before the old one is removed): the folder holds the
// user's only copy, unlike the staging folder of an upload.
func renameImageFiles(root *os.Root, plan *renamePlan, durable bool) error {
	if existsIn(root, plan.To) {
		return fmt.Errorf("%s: %w", plan.To, fs.ErrExist)
	}
	if plan.XMLFrom == "" {
		return renameIn(root, plan.From, plan.To)
	}
	if existsIn(root, plan.XMLTo) {
		return fmt.Errorf("%s: %w", plan.XMLTo, fs.ErrExist)
	}
	data, err := readSmallFile(root, plan.XMLFrom, maxAnnotationBytes)
	if err == nil {
		data, err = renameVOC(data, path.Base(plan.To))
	}
	if err != nil {
		// Not a VOC file the platform can edit (or one it cannot read):
		// both files only change their names.
		if err := renameIn(root, plan.From, plan.To); err != nil {
			return err
		}
		if err := renameIn(root, plan.XMLFrom, plan.XMLTo); err != nil {
			_ = renameIn(root, plan.To, plan.From)
			return err
		}
		return nil
	}

	// The new file is written first, so that the image is never left with
	// the annotation of its old name.
	if durable {
		tmp, err := writeTempAnnotation(root, plan.XMLFrom, data)
		if err != nil {
			return err
		}
		if err := renameIn(root, tmp, plan.XMLTo); err != nil {
			_ = root.Remove(filepath.FromSlash(tmp))
			return err
		}
	} else {
		f, err := root.OpenFile(filepath.FromSlash(plan.XMLTo), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = root.Remove(filepath.FromSlash(plan.XMLTo))
			return err
		}
	}
	if err := renameIn(root, plan.From, plan.To); err != nil {
		_ = root.Remove(filepath.FromSlash(plan.XMLTo))
		return err
	}
	if err := root.Remove(filepath.FromSlash(plan.XMLFrom)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &leftoverError{path: plan.XMLFrom, err: err}
	}
	return nil
}

// leftoverError reports an old VOC file that could not be removed after
// the image and its renamed copy were in place.
type leftoverError struct {
	path string
	err  error
}

func (e *leftoverError) Error() string { return "remove " + e.path + ": " + e.err.Error() }
func (e *leftoverError) Unwrap() error { return e.err }

// renameIn renames the file at from to to inside root (to must not exist;
// the caller checked).
func renameIn(root *os.Root, from, to string) error {
	return os.Rename(filepath.Join(root.Name(), filepath.FromSlash(from)), filepath.Join(root.Name(), filepath.FromSlash(to)))
}

// existsIn reports whether anything is at rel in root (without following a
// symbolic link there).
func existsIn(root *os.Root, rel string) bool {
	_, err := root.Lstat(filepath.FromSlash(rel))
	return err == nil
}

// readSmallFile reads the file at rel in root, failing when it is larger
// than limit bytes.
func readSmallFile(root *os.Root, rel string, limit int64) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", rel, limit)
	}
	return data, nil
}

// renameVOC rewrites the VOC file data for an image renamed to filename:
// the <filename> element names it, and the file name at the end of the
// <path> element (LabelImg writes the absolute path of the image) is
// replaced. Everything else is kept as patchVOC keeps it; the result is
// UTF-8. It fails for a file that is not a VOC file patchVOC can edit, or
// when the result would not read back with the same boxes.
func renameVOC(data []byte, filename string) ([]byte, error) {
	text, err := vocUTF8(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	doc, err := splitVOC(text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	nl := doc.newline
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + nl)
	for _, p := range doc.prolog {
		b.Write(p)
		b.WriteString(nl)
	}
	b.Write(doc.start)
	hadFilename := false
	for _, n := range doc.nodes {
		raw := n.raw
		switch n.name {
		case "filename":
			if r, ok := replaceElementText(raw, func(string) string { return filename }); ok {
				raw, hadFilename = r, true
			}
		case "path":
			if r, ok := replaceElementText(raw, func(old string) string { return replaceBaseName(old, filename) }); ok {
				raw = r
			}
		}
		b.WriteString(nl + "\t")
		b.Write(raw)
	}
	b.WriteString(nl)
	b.Write(doc.end)
	b.WriteString(nl)

	before, err := parseVOC(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotPatchable, err)
	}
	after, err := parseVOC(bytes.NewReader(b.Bytes()))
	want := before.Filename
	if hadFilename {
		want = filename
	}
	if err != nil || after.Filename != want || after.Unsupported != before.Unsupported ||
		after.Width != before.Width || after.Height != before.Height || !slices.Equal(after.Objects, before.Objects) {
		return nil, fmt.Errorf("%w: the renamed file reads differently", errNotPatchable)
	}
	return b.Bytes(), nil
}

// replaceElementText rewrites an element with text content only: its text
// becomes repl(old text), keeping the start tag. ok is false for an element
// that has child elements (left alone).
func replaceElementText(raw []byte, repl func(string) string) ([]byte, bool) {
	var el struct {
		Text  string `xml:",chardata"`
		Inner []byte `xml:",innerxml"`
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&el); err != nil || bytes.Contains(el.Inner, []byte("<")) && !isOnlyCDATA(el.Inner) {
		return nil, false
	}
	end := bytes.IndexByte(raw, '>')
	if end < 0 {
		return nil, false
	}
	start := raw[:end+1]
	var closeTag []byte
	if bytes.HasSuffix(start, []byte("/>")) { // <filename/>
		open := bytes.TrimRight(start[:len(start)-2], xmlSpace)
		qname := open[1:]
		if i := bytes.IndexAny(qname, xmlSpace); i >= 0 {
			qname = qname[:i]
		}
		start = append(append([]byte{}, open...), '>')
		closeTag = []byte("</" + string(qname) + ">")
	} else {
		i := bytes.LastIndex(raw, []byte("</"))
		if i < 0 {
			return nil, false
		}
		closeTag = raw[i:]
	}
	out := append(append([]byte{}, start...), xmlText(repl(strings.TrimSpace(el.Text)))...)
	return append(out, closeTag...), true
}

// isOnlyCDATA reports whether the inner XML of an element is a single
// CDATA section (text, not child elements).
func isOnlyCDATA(inner []byte) bool {
	t := bytes.TrimSpace(inner)
	return bytes.HasPrefix(t, []byte("<![CDATA[")) && bytes.HasSuffix(t, []byte("]]>")) &&
		bytes.Count(t, []byte("<![CDATA[")) == 1
}

// replaceBaseName replaces the last element of a path written with "/" or
// "\" (Windows) by name; a path without a separator becomes name.
func replaceBaseName(p, name string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[:i+1] + name
	}
	return name
}

// ---- the rename list of an import ----

// copier is the bulk insert of a pool or transaction.
type copier interface {
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

// insertRenames stores entries of the rename list of an import.
func insertRenames(ctx context.Context, q copier, importID int64, plans []*renamePlan) error {
	if len(plans) == 0 {
		return nil
	}
	rows := make([][]any, len(plans))
	for i, p := range plans {
		e := p.entry()
		rows[i] = []any{importID, p.seq, e.Kind, e.OriginalPath, e.Path, e.SameNameAs, e.AnnotationPath}
	}
	_, err := q.CopyFrom(ctx, pgx.Identifier{"dataset_import_renames"},
		[]string{"import_id", "seq", "kind", "original_path", "path", "same_name_as", "annotation_path"},
		pgx.CopyFromRows(rows))
	return err
}

// recordRenames stores entries of the rename list of a job (a batch of the
// images renamed or found identical so far) even when the job was canceled
// or the service is stopping (bounded by finishTimeout), and logs a
// failure: the files are renamed either way. The counts in the job's
// summary are updated with them, so that the list can be found from the
// job even when the process dies before the job ends (the summary is
// otherwise written when it ends).
func (s *Service) recordRenames(importID int64, plans []*renamePlan) {
	if len(plans) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.indexContext()), finishTimeout)
	defer cancel()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := insertRenames(ctx, tx, importID, plans); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE dataset_imports SET summary = summary || (
				SELECT jsonb_build_object(
					$2::text, count(*) FILTER (WHERE kind = $3),
					$4::text, count(*) FILTER (WHERE kind = $5))
				FROM dataset_import_renames WHERE import_id = $1)
			WHERE id = $1 AND status = 'running'`,
			importID, sumImagesRenamed, RenameRenamed, sumImagesIdentical, RenameIdentical)
		return err
	})
	if err != nil {
		s.log.Error("record renamed images", "import_id", importID, "count", len(plans), "err", err)
	}
}

// RenameQuery selects a page of an import's rename list.
type RenameQuery struct {
	Kind   string // RenameRenamed, RenameIdentical or "" for both
	Offset int
	Limit  int
}

// ListImportRenames returns a page of the images an import renamed or
// found identical to the image holding their name (in image order), and
// how many there are.
func (s *Service) ListImportRenames(ctx context.Context, datasetID, importID int64, q RenameQuery) ([]ImageRename, int, error) {
	if _, err := s.GetImport(ctx, datasetID, importID); err != nil {
		return nil, 0, err
	}
	const where = ` FROM dataset_import_renames WHERE import_id = $1 AND ($2 = '' OR kind = $2)`
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+where, importID, q.Kind).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT kind, original_path, path, same_name_as, annotation_path"+where+
		" ORDER BY seq LIMIT $3 OFFSET $4", importID, q.Kind, q.Limit, q.Offset)
	if err != nil {
		return nil, 0, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ImageRename, error) {
		var e ImageRename
		err := row.Scan(&e.Kind, &e.OriginalPath, &e.Path, &e.SameNameAs, &e.AnnotationPath)
		return e, err
	})
	return items, total, err
}
