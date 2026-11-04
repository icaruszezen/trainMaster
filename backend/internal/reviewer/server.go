package reviewer

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Options configure a Server.
type Options struct {
	// Web is the frontend build: dist/reviewer.html and its assets, or
	// placeholder.html when the frontend was not built (see web.go).
	Web fs.FS
	// Token is the session token the page must present (see security.go).
	Token string
	// Hosts are the Host headers accepted ("127.0.0.1:<port>", ...); none
	// accepts any (tests).
	Hosts []string
	// CookieName names the session cookie; cookies are shared by all
	// ports of a host, so it should include the port.
	CookieName string
	Version    string
	ConfigDir  string // where recent.json is kept; "" keeps it in memory
	CacheDir   string // where thumbnails are cached; "" uses a temporary folder
	Logger     *slog.Logger
	Now        func() time.Time
	// Window tells the page it is shown in the program's own window
	// (quitting closes it) rather than in a browser; see SetWindow.
	Window bool
	// Quit is called (in a goroutine of its own) after POST /api/quit
	// answered: the program closes its window and stops.
	Quit func()
	// Closing is called on POST /api/closing: the page took over closing
	// the window (it saves the last edit, then quits).
	Closing func()
	// Reveal shows a file in the file manager; nil uses the system's.
	Reveal func(path string) error
	// Places replaces the places of the file browser (tests).
	Places func() []Place
}

// Server is the local reviewer's HTTP server. It holds at most one open
// package.
type Server struct {
	opts    Options
	log     *slog.Logger
	now     func() time.Time
	handler http.Handler
	recent  *recentStore
	thumbs  *thumbnailer
	ctx     context.Context // canceled by Shutdown
	cancel  context.CancelFunc

	window atomic.Bool

	mu      sync.Mutex
	pkg     *Package
	opening *openJob
	openErr *OpenFailure
}

// New returns a server.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Reveal == nil {
		o.Reveal = reveal
	}
	if o.Places == nil {
		o.Places = defaultPlaces
	}
	if o.CookieName == "" {
		o.CookieName = "tm_reviewer"
	}
	s := &Server{opts: o, log: o.Logger, now: o.Now, recent: newRecentStore(o.ConfigDir), thumbs: newThumbnailer(o.CacheDir)}
	s.window.Store(o.Window)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.handler = s.protect(s.routes())
	go s.thumbs.cleanup()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Shutdown stops an opening in progress and closes the package (noting
// its counts in the recent list).
func (s *Server) Shutdown() {
	s.cancel()
	s.mu.Lock()
	job := s.opening
	s.mu.Unlock()
	if job != nil {
		<-job.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// current returns the open package.
func (s *Server) current() (*Package, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pkg == nil {
		return nil, errNoPackage
	}
	return s.pkg, nil
}

// ---- session ----

// Session is what the start page shows (GET /api/session).
type Session struct {
	Version   string          `json:"version"`
	OS        string          `json:"os"`     // windows, linux, darwin
	Window    bool            `json:"window"` // in the program's own window
	Package   *PackageInfo    `json:"package"`
	Opening   *Opening        `json:"opening"`
	OpenError *OpenFailure    `json:"open_error"`
	Recent    []RecentPackage `json:"recent"`
}

// Opening is a package being opened.
type Opening struct {
	Path  string `json:"path"`
	Stage string `json:"stage"` // checking, extracting (Done / Total bytes) or loading (images)
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// Opening stages.
const (
	stageChecking   = "checking"
	stageExtracting = "extracting"
	stageLoading    = "loading"
)

// OpenFailure is why the last opening failed.
type OpenFailure struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Session returns the session.
func (s *Server) Session() Session {
	s.mu.Lock()
	pkg, job, failure := s.pkg, s.opening, s.openErr
	s.mu.Unlock()
	out := Session{Version: s.opts.Version, OS: runtime.GOOS, Window: s.window.Load(), OpenError: failure,
		Recent: s.recent.List()}
	if pkg != nil {
		info := pkg.Info()
		out.Package = &info
	}
	if job != nil {
		o := job.snapshot()
		out.Opening = &o
	}
	return out
}

// ---- opening ----

// openJob is a package being opened in the background.
type openJob struct {
	path   string
	cancel context.CancelFunc
	done   chan struct{}
	stage  atomic.Value // string
	count  atomic.Int64
	total  atomic.Int64
}

func (j *openJob) setStage(stage string) {
	j.count.Store(0)
	j.total.Store(0)
	j.stage.Store(stage)
}

func (j *openJob) progress(done, total int64) {
	j.total.Store(total)
	j.count.Store(done)
}

func (j *openJob) snapshot() Opening {
	stage, _ := j.stage.Load().(string)
	return Opening{Path: j.path, Stage: stage, Done: j.count.Load(), Total: j.total.Load()}
}

var errOpenBusy = &Error{Kind: ErrConflict, Message: "正在打开另一个审图包，请等它打开后再试（或先取消）"}

// Open starts opening the package at path (a zip or a folder) in the
// background; GET /api/session tells how it goes. The path is checked
// first: a path that cannot be a package fails at once. The package open
// now stays open until the new one is ready.
func (s *Server) Open(path string) (Opening, error) {
	target, err := resolveOpenPath(path)
	if err != nil {
		return Opening{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opening != nil {
		return Opening{}, errOpenBusy
	}
	if target.dir != "" && s.pkg != nil && samePath(target.dir, s.pkg.dir) {
		return Opening{Path: path, Stage: stageLoading}, nil // open already
	}
	ctx, cancel := context.WithCancel(s.ctx)
	job := &openJob{path: path, cancel: cancel, done: make(chan struct{})}
	job.setStage(stageChecking)
	s.opening, s.openErr = job, nil
	go s.runOpen(ctx, job, target)
	return job.snapshot(), nil
}

// NoteOpenError records that opening path failed, for the start page
// (a path given on the command line that cannot be opened).
func (s *Server) NoteOpenError(path string, err error) {
	msg := "无法打开审图包：" + err.Error()
	var re *Error
	if errors.As(err, &re) {
		msg = re.Message
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openErr = &OpenFailure{Path: path, Message: msg}
}

// SetWindow tells whether the page is shown in the program's own window
// (it may fall back to a browser after the server started).
func (s *Server) SetWindow(window bool) { s.window.Store(window) }

// CancelOpen stops the package being opened (a partly extracted folder is
// removed).
func (s *Server) CancelOpen() {
	s.mu.Lock()
	job := s.opening
	s.mu.Unlock()
	if job != nil {
		job.cancel()
		<-job.done
	}
}

func (s *Server) runOpen(ctx context.Context, job *openJob, target openTarget) {
	defer close(job.done)
	defer job.cancel()
	started := s.now()
	dir, resumed := target.dir, false
	var err error
	if target.zipPath != "" {
		job.setStage(stageExtracting)
		dir, resumed, err = extractPackage(ctx, target.zipPath, job.progress)
	}
	var pkg *Package
	if err == nil {
		job.setStage(stageLoading)
		pkg, err = loadPackage(ctx, dir, loadOptions{zipPath: target.zipPath, resumed: resumed, now: s.now,
			progress: job.progress})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.opening = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.log.Info("opening canceled", "path", job.path)
			return
		}
		var re *Error
		msg := "无法打开审图包：" + err.Error()
		if errors.As(err, &re) {
			msg = re.Message
		}
		s.openErr = &OpenFailure{Path: job.path, Message: msg}
		s.log.Warn("open package", "path", job.path, "err", err)
		return
	}
	s.closeLocked()
	s.pkg = pkg
	info := pkg.Info()
	s.thumbs.touch(info.PackageID)
	s.putRecent(info, true)
	s.log.Info("package opened", "path", info.Path, "zip", info.ZipPath, "review_id", info.Review.ID,
		"images", info.Counts.Total, "resumed", info.Resumed, "elapsed", s.now().Sub(started).Round(time.Millisecond))
}

// Close closes the open package.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *Server) closeLocked() {
	if s.pkg == nil {
		return
	}
	info := s.pkg.Info()
	s.putRecent(info, false)
	if err := s.pkg.Close(); err != nil {
		s.log.Warn("close package", "path", info.Path, "err", err)
	}
	s.pkg = nil
}

func (s *Server) putRecent(info PackageInfo, front bool) {
	entry := RecentPackage{Path: info.Path, ZipPath: info.ZipPath, PackageID: info.PackageID, ReviewID: info.Review.ID,
		ReviewName: info.Review.Name, Counts: info.Counts, OpenedAt: s.now().UTC()}
	if err := s.recent.Put(entry, front); err != nil {
		s.log.Warn("save recent packages", "err", err)
	}
}
