package dataversion

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The split (划分) of the originals of a version into train / val / test.
// It is a pure function of the images (dataset, path, file size) and the
// configuration, used by the preview (on the images the index says would
// be taken) and the build (on the images it takes).
//
//  1. Folders (use_folders), per source: an image whose split folder (the
//     folder right after its last images / JPEGImages folder, else its
//     first folder) is named train, val, valid, validation or test (with
//     an optional number: train2017) takes that split, but only when the
//     source has images in both a train and a val folder; test folders go
//     to the ratio split when test is 0 %.
//  2. The other images are grouped against leakage: images with the same
//     group key (dataset, folder and normalized name, or the folder alone
//     with group_by folder) or the same duplicate key (normalized name and
//     file size: the same file in two datasets or folders) share a split.
//  3. Each group is hashed with the seed into a bucket 0..99: val first,
//     then test, then train. The val sets of two versions with the same seed
//     are therefore nested, and an image keeps its split across versions
//     with the same ratios and seed.
//  4. A small set is fixed up deterministically so that every split with a
//     non-zero ratio gets a group when another split has two or more.

// splitItem is an original the split assigns.
type splitItem struct {
	Source    int    // index of its source in the version
	DatasetID int64  //
	RelPath   string // slash-separated, in the dataset folder
	Size      int64  // the file size in bytes
}

// splitAssignment is the split of an item and how it got it.
type splitAssignment struct {
	Split string // SplitTrain, SplitVal or SplitTest
	By    string // SplitByRatio or SplitByFolder
}

// splitFolderPattern matches the folder names that name a split.
var splitFolderPattern = regexp.MustCompile(`(?i)^(train|val|valid|validation|test)\d*$`)

// folderSplit returns the split the folders of rel name, "" for none. The
// candidate folder is the one right after the last images or JPEGImages
// folder (case-insensitive); when there is no such folder (no images
// folder, or the image is directly in it, as in train/images/a.jpg), the
// first folder.
func folderSplit(rel string) string {
	dirs := strings.Split(rel, "/")
	dirs = dirs[:len(dirs)-1]
	if len(dirs) == 0 {
		return ""
	}
	candidate := dirs[0]
	for i := len(dirs) - 1; i >= 0; i-- {
		if strings.EqualFold(dirs[i], "images") || strings.EqualFold(dirs[i], "JPEGImages") {
			if i+1 < len(dirs) {
				candidate = dirs[i+1]
			}
			break
		}
	}
	m := splitFolderPattern.FindStringSubmatch(candidate)
	if m == nil {
		return ""
	}
	switch strings.ToLower(m[1]) {
	case "train":
		return SplitTrain
	case "test":
		return SplitTest
	default:
		return SplitVal
	}
}

// stemSuffixes are the suffixes normalizeStem strips (lower case): copies
// made by Windows and other tools, augmentation and version markers.
var stemSuffixes = []*regexp.Regexp{
	regexp.MustCompile(`\s*-\s*(副本|copy)(\s*\(\d+\))?$`),
	regexp.MustCompile(`\s*\(\d+\)$`),
	regexp.MustCompile(`[ _-](aug|copy|dup|flip|flipped|mirror|rot|rotate|noise|blur|crop|resize|color|bright|contrast|gamma|hsv|clahe|persp|affine|shift|trans|scale|scaled|cutout|mosaic|mixup|elastic|distort|jitter|sharpen|shear|zoom|pad|padded)\d*$`),
	regexp.MustCompile(`[ _-](v|ver|version)\d+$`),
}

// nonWordRuns matches runs of characters that are neither letters nor
// digits in any script (RE2's \w is ASCII only: it would turn 划痕001 and
// 脏污001 into the same name).
var nonWordRuns = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// normalizeStem is the name of a file without its extension as the
// leakage groups compare it: lower case, without copy, augmentation and
// version suffixes, runs of other characters replaced by "_".
func normalizeStem(stem string) string {
	lower := strings.ToLower(stem)
	s := lower
	for {
		prev := s
		for _, re := range stemSuffixes {
			s = re.ReplaceAllString(s, "")
		}
		if s == prev {
			break
		}
	}
	s = strings.Trim(nonWordRuns.ReplaceAllString(s, "_"), "_")
	if s == "" {
		return lower
	}
	return s
}

// fileStem is the base name of rel without its extension.
func fileStem(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(base, path.Ext(base))
}

// splitBucket hashes the group key of a ratio group with the seed into a
// bucket 0..99 (integer math only).
func splitBucket(seed int64, key string) int {
	h := sha256.Sum256([]byte(strconv.FormatInt(seed, 10) + "\x00" + key))
	return bucketOf(binary.BigEndian.Uint64(h[:8]))
}

// bucketOf maps the first 8 bytes of a hash to 0..99: the top 53 bits
// times 100, divided by 2^53.
func bucketOf(h uint64) int { return int(((h >> 11) * 100) >> 53) }

// splitOfBucket is the split of a bucket: val first, then test, then
// train.
func splitOfBucket(cfg SplitConfig, bucket int) string {
	switch {
	case bucket < cfg.Val:
		return SplitVal
	case bucket < cfg.Val+cfg.Test:
		return SplitTest
	default:
		return SplitTrain
	}
}

// splitInterval is the bucket interval [lo, hi) of a split.
func splitInterval(cfg SplitConfig, split string) (lo, hi int) {
	switch split {
	case SplitVal:
		return 0, cfg.Val
	case SplitTest:
		return cfg.Val, cfg.Val + cfg.Test
	default:
		return cfg.Val + cfg.Test, 100
	}
}

func splitPercent(cfg SplitConfig, split string) int {
	switch split {
	case SplitVal:
		return cfg.Val
	case SplitTest:
		return cfg.Test
	default:
		return cfg.Train
	}
}

// unionFind joins the ratio images into groups.
type unionFind []int

func (u unionFind) find(i int) int {
	for u[i] != i {
		u[i] = u[u[i]]
		i = u[i]
	}
	return i
}

func (u unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u[max(ra, rb)] = min(ra, rb)
	}
}

// splitGroup is a leakage group of ratio images.
type splitGroup struct {
	key     string // the smallest group key of its members
	bucket  int
	split   string
	members []int // item indexes
}

// splitImages assigns a split to every item (see the top of the file).
// cfg must be valid (normalizeInput).
func splitImages(items []splitItem, cfg SplitConfig) []splitAssignment {
	out := make([]splitAssignment, len(items))

	// 1. Folders, decided per source.
	folder := make([]string, len(items))
	if cfg.UseFolders {
		type seen struct{ train, val bool }
		sources := map[int]*seen{}
		for i, it := range items {
			folder[i] = folderSplit(it.RelPath)
			s := sources[it.Source]
			if s == nil {
				s = &seen{}
				sources[it.Source] = s
			}
			s.train = s.train || folder[i] == SplitTrain
			s.val = s.val || folder[i] == SplitVal
		}
		for i, it := range items {
			if s := sources[it.Source]; !s.train || !s.val || (folder[i] == SplitTest && cfg.Test == 0) {
				folder[i] = ""
			}
		}
	}

	// 2. Leakage groups of the ratio images.
	var ratio []int
	for i := range items {
		if folder[i] != "" {
			out[i] = splitAssignment{Split: folder[i], By: SplitByFolder}
		} else {
			ratio = append(ratio, i)
		}
	}
	uf := make(unionFind, len(ratio))
	groupKeys := make([]string, len(ratio))
	first := map[string]int{}
	join := func(k int, key string) {
		if j, ok := first[key]; ok {
			uf.union(j, k)
		} else {
			first[key] = k
		}
	}
	for k, i := range ratio {
		uf[k] = k
		it := items[i]
		stem := normalizeStem(fileStem(it.RelPath))
		dir := strings.ToLower(path.Dir(it.RelPath))
		groupKey := strconv.FormatInt(it.DatasetID, 10) + "/" + dir
		if cfg.GroupBy != GroupByFolder {
			groupKey += "/" + stem
		}
		groupKeys[k] = groupKey
		join(k, "g\x00"+groupKey)
		join(k, "d\x00"+stem+"|"+strconv.FormatInt(it.Size, 10))
	}
	byRoot := map[int]*splitGroup{}
	var groups []*splitGroup
	for k := range ratio {
		r := uf.find(k)
		g := byRoot[r]
		if g == nil {
			g = &splitGroup{key: groupKeys[k]}
			byRoot[r] = g
			groups = append(groups, g)
		}
		g.key = min(g.key, groupKeys[k])
		g.members = append(g.members, ratio[k])
	}

	// 3. Hash each group into a bucket.
	count := map[string]int{}
	for _, g := range groups {
		g.bucket = splitBucket(cfg.Seed, g.key)
		g.split = splitOfBucket(cfg, g.bucket)
		count[g.split]++
	}

	// 4. Fix up small sets: an empty split with a non-zero ratio takes the
	// group closest to its interval from the split with the most groups.
	for {
		moved := false
		for _, empty := range []string{SplitVal, SplitTrain, SplitTest} {
			if splitPercent(cfg, empty) == 0 || count[empty] > 0 {
				continue
			}
			donor := ""
			for _, s := range []string{SplitTrain, SplitVal, SplitTest} {
				if s != empty && count[s] >= 2 && (donor == "" || count[s] > count[donor]) {
					donor = s
				}
			}
			if donor == "" {
				continue
			}
			lo, hi := splitInterval(cfg, empty)
			distance := func(b int) int {
				switch {
				case b < lo:
					return lo - b
				case b >= hi:
					return b - hi + 1
				}
				return 0
			}
			var best *splitGroup
			for _, g := range groups {
				if g.split != donor {
					continue
				}
				if best == nil || cmp.Or(cmp.Compare(distance(g.bucket), distance(best.bucket)),
					strings.Compare(g.key, best.key)) < 0 {
					best = g
				}
			}
			best.split = empty
			count[donor]--
			count[empty]++
			moved = true
			break
		}
		if !moved {
			break
		}
	}

	for _, g := range groups {
		for _, i := range g.members {
			out[i] = splitAssignment{Split: g.split, By: SplitByRatio}
		}
	}
	return out
}

// splitCounts counts the assignments per split.
func splitCounts(as []splitAssignment) SplitCounts {
	var c SplitCounts
	for _, a := range as {
		c.add(a.Split, 1)
	}
	return c
}

// add adds n to the count of split.
func (c *SplitCounts) add(split string, n int) {
	switch split {
	case SplitTrain:
		c.Train += n
	case SplitVal:
		c.Val += n
	case SplitTest:
		c.Test += n
	}
}

// splitRefusal is the error for a split without train or val images (nil
// when it has both).
func splitRefusal(c SplitCounts) error {
	switch {
	case c.Train+c.Val+c.Test == 0:
		return errNoImages
	case c.Val == 0:
		return errNoValImages
	case c.Train == 0:
		return errNoTrainImages
	}
	return nil
}
