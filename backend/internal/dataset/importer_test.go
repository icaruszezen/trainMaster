package dataset

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/storage"
)

// countingWriter counts the bytes written to it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func TestExtractGuard(t *testing.T) {
	plenty := func() (uint64, error) { return 1 << 40, nil }
	data := func(n int) io.Reader { return strings.NewReader(strings.Repeat("x", n)) }

	// The job's own budget.
	g := &extractGuard{budget: 1000, recheck: 100, freeBytes: plenty}
	if err := g.copy(io.Discard, data(600)); err != nil {
		t.Fatal(err)
	}
	if err := g.copy(io.Discard, data(400)); err != nil {
		t.Fatalf("exactly the budget: %v", err)
	}
	if err := g.copy(io.Discard, data(1)); err != errExtractTooLarge {
		t.Errorf("over the budget: err = %v", err)
	}

	// Other jobs and uploads use up the disk meanwhile: the extraction stops
	// at the next check although its own budget is far away.
	free := uint64(1 << 40)
	out := &countingWriter{}
	g = &extractGuard{budget: 1 << 40, recheck: 100, freeBytes: func() (uint64, error) { return free, nil }}
	if err := g.copy(out, data(150)); err != nil {
		t.Fatal(err)
	}
	free = reserveBytes - 1
	if err := g.copy(out, data(1000)); err != errExtractTooLarge || out.n != 200 {
		t.Errorf("disk filled by others: err = %v after %d bytes, want errExtractTooLarge after 200", err, out.n)
	}

	// Without disk usage support only the budget applies.
	g = &extractGuard{budget: 1 << 40, recheck: 100, freeBytes: func() (uint64, error) { return 0, storage.ErrUsageUnsupported }}
	if err := g.copy(io.Discard, data(1000)); err != nil {
		t.Errorf("usage unsupported: %v", err)
	}

	// Errors of the source and the destination pass through.
	readErr := errors.New("corrupt")
	g = &extractGuard{budget: 1 << 40, recheck: 100, freeBytes: plenty}
	if err := g.copy(io.Discard, io.MultiReader(data(10), iotestErr{readErr})); err != readErr {
		t.Errorf("read error: %v", err)
	}
}

type iotestErr struct{ err error }

func (r iotestErr) Read([]byte) (int, error) { return 0, r.err }

func TestDiskFull(t *testing.T) {
	if err := diskFull(&fs.PathError{Op: "write", Path: "a.png", Err: syscall.ENOSPC}); err != errDiskFull {
		t.Errorf("ENOSPC: %v", err)
	}
	other := errors.New("other")
	if err := diskFull(other); err != other {
		t.Errorf("other error: %v", err)
	}
	if err := diskFull(nil); err != nil {
		t.Errorf("nil: %v", err)
	}
}
