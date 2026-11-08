package dataversion

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/icaruszezen/trainmaster/backend/internal/augment"
	"github.com/icaruszezen/trainmaster/backend/internal/dataset"
)

// The augmenting stage: offline augmented copies of the train originals
// (package augment does the image work), written next to their original
// as <stem>-aug<k>.jpg|.png with their label files. Copies are made by
// parallel workers, a chunk of originals at a time, and inserted in
// position order (right after their original).

const (
	// augmentChunk is how many originals the workers augment before their
	// copies are inserted.
	augmentChunk = 64
	// maxAugmentMemory: an image whose copy would need more memory is not
	// augmented (augment_skipped).
	maxAugmentMemory = 1 << 30
)

// augmentWorkers is the number of parallel augmenting workers: half the
// CPUs (the server shares them with the web UI).
func augmentWorkers() int { return max(1, runtime.NumCPU()/2) }

// augmentResult is what the workers made of one original.
type augmentResult struct {
	copies  []*buildCopy
	skipped bool // no copy could be made
	err     error
}

// augmentImages makes the augmented copies of the train originals.
func (b *build) augmentImages() error {
	aug := b.job.Config.Augmentation
	if aug == nil || aug.Copies < 1 {
		return nil
	}
	methods := make([]augment.Method, len(aug.Methods))
	for i, m := range aug.Methods {
		methods[i] = augment.Method(m)
	}
	var originals []*buildImage
	var bytes int64
	for _, img := range b.images {
		if img.inserted && img.split == SplitTrain && (len(img.objects) > 0 || !aug.OnlyWithObjects) {
			originals = append(originals, img)
			bytes += img.size
		}
	}
	if len(originals) == 0 {
		return nil
	}
	if err := b.setStage(StageAugmenting, len(originals)*aug.Copies); err != nil {
		return err
	}
	// The whole stage, estimated from the originals' sizes.
	b.space.force()
	if err := b.space.before(bytes * int64(aug.Copies)); err != nil {
		return err
	}
	avg := bytes / int64(len(originals))

	ids := b.classIDs()
	workers := b.s.augmentWorkers
	if workers < 1 {
		workers = augmentWorkers()
	}
	for start := 0; start < len(originals); start += augmentChunk {
		chunk := originals[start:min(start+augmentChunk, len(originals))]
		if err := b.space.before(avg * int64(len(chunk)*aug.Copies)); err != nil {
			return err
		}
		// Names first, in position order, so that they are deterministic.
		stems := make([][]string, len(chunk))
		for i, img := range chunk {
			stems[i] = make([]string, aug.Copies)
			for k := range aug.Copies {
				stems[i][k] = b.namer.copyStem(img.path, k+1)
			}
		}
		results := make([]augmentResult, len(chunk))
		var wg sync.WaitGroup
		next := make(chan int)
		for range min(workers, len(chunk)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					results[i] = b.augmentOne(chunk[i], methods, stems[i])
				}
			}()
		}
		for i := range chunk {
			next <- i
		}
		close(next)
		wg.Wait()

		var rows []imageRow
		var made []*buildCopy
		for _, r := range results {
			if r.err != nil {
				return r.err
			}
			if r.skipped {
				b.stats.Skipped.AugmentSkipped++
			}
			for _, c := range r.copies {
				if err := b.writeLabels(c.path, c.objects, c.width, c.height, ids); err != nil {
					return err
				}
				rows = append(rows, b.copyRow(c))
				made = append(made, c)
				b.space.wrote(c.size)
			}
			if err := b.tick(aug.Copies); err != nil {
				return err
			}
		}
		if len(rows) > 0 {
			if _, err := b.insertRows(rows); err != nil {
				return err
			}
		}
		for _, c := range made {
			b.copies = append(b.copies, c)
			b.written += c.size
			b.stats.Storage.GeneratedBytes += c.size
		}
	}
	return nil
}

// augmentOne makes the copies of one original (stems: their paths without
// extension). An image that cannot be decoded, or would need too much
// memory, gets no copies (skipped).
func (b *build) augmentOne(img *buildImage, methods []augment.Method, stems []string) augmentResult {
	f, err := os.Open(filepath.Join(b.dir, filepath.FromSlash(img.path)))
	if err != nil {
		return augmentResult{err: fmt.Errorf("open image to augment: %w", err)}
	}
	defer f.Close()
	cfg, format, err := dataset.DecodeImageConfig(f)
	if err != nil {
		b.log.Info("data version: cannot augment an image", "path", img.path, "err", err)
		return augmentResult{skipped: true}
	}
	cost := augment.DecodedBytes(cfg) * 3
	if _, paletted := cfg.ColorModel.(color.Palette); paletted {
		cost = augment.DecodedBytes(cfg) + 8*int64(cfg.Width)*int64(cfg.Height)
	}
	if cost > maxAugmentMemory {
		b.log.Info("data version: image too large to augment", "path", img.path, "width", cfg.Width, "height", cfg.Height)
		return augmentResult{skipped: true}
	}
	release, err := dataset.ReserveImageMemory(b.ctx, cost)
	if err != nil {
		return augmentResult{err: err}
	}
	defer release()
	if _, err := f.Seek(0, 0); err != nil {
		return augmentResult{err: err}
	}
	src, err := dataset.DecodeImage(f)
	if err != nil {
		b.log.Info("data version: cannot augment an image", "path", img.path, "err", err)
		return augmentResult{skipped: true}
	}
	outFormat, ext := augment.OutputFormat(format, format == "webp" && augment.IsLosslessWebP(f))
	boxes := make([]augment.Box, len(img.objects))
	minSide := 0
	for i, o := range img.objects {
		boxes[i] = augment.Box{Label: o.Name, Xmin: o.Xmin, Ymin: o.Ymin, Xmax: o.Xmax, Ymax: o.Ymax,
			Difficult: o.Difficult, Truncated: o.Truncated}
		side := min(o.Xmax-o.Xmin, o.Ymax-o.Ymin)
		if i == 0 || side < minSide {
			minSide = side
		}
	}
	var out augmentResult
	for k, stem := range stems {
		if err := b.ctx.Err(); err != nil {
			return augmentResult{err: err}
		}
		plan := augment.NewPlan(b.job.Config.Split.Seed, img.path, k+1, methods, minSide)
		if len(plan.Steps) == 0 {
			continue // cannot differ from the original and the other copies
		}
		result, moved := augment.Apply(src, boxes, plan)
		c := &buildCopy{orig: img, k: k + 1, path: stem + ext, methods: plan.Methods(), position: img.position + k + 1,
			objects: []dataset.AnnotationObject{}}
		c.width, c.height = result.Bounds().Dx(), result.Bounds().Dy()
		for _, m := range moved {
			c.objects = append(c.objects, dataset.AnnotationObject{Name: m.Label, Xmin: m.Xmin, Ymin: m.Ymin,
				Xmax: m.Xmax, Ymax: m.Ymax, Difficult: m.Difficult, Truncated: m.Truncated})
		}
		if c.size, c.modTime, err = b.writeImage(c.path, result, outFormat); err != nil {
			return augmentResult{err: err}
		}
		out.copies = append(out.copies, c)
	}
	out.skipped = len(out.copies) == 0
	return out
}

// writeImage encodes img in format at the version path p and returns the
// file's size and modification time.
func (b *build) writeImage(p string, img image.Image, format string) (int64, time.Time, error) {
	name := filepath.Join(b.dir, filepath.FromSlash(p))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("write augmented image: %w", err)
	}
	err = augment.Encode(f, img, format)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return 0, time.Time{}, fmt.Errorf("write augmented image: %w", err)
	}
	fi, err := os.Lstat(name)
	if err != nil {
		return 0, time.Time{}, err
	}
	return fi.Size(), dbTime(fi.ModTime()), nil
}

// copyRow is the row of an augmented copy: a train image with its
// original's provenance.
func (b *build) copyRow(c *buildCopy) imageRow {
	o := c.orig
	from := o.id
	methods := c.methods
	if methods == nil {
		methods = []string{}
	}
	return imageRow{Position: c.position, Split: SplitTrain, SplitBy: o.splitBy, Path: c.path, SizeBytes: c.size,
		ModTime: c.modTime, Width: c.width, Height: c.height, Objects: c.objects, ObjectCount: len(c.objects),
		Labels: objectLabels(c.objects), DatasetID: b.job.Sources[o.source].DatasetID, SourcePath: o.rel,
		SourceSize: o.sourceSize, SourceModTime: o.sourceMod, AnnotationSource: o.annotation, AugmentedFrom: &from,
		Augment: methods, Stored: StoredGenerated}
}
