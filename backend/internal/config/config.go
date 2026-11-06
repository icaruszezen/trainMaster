// Package config loads the service configuration from TM_* environment
// variables. An empty variable is treated the same as an unset one.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the validated service configuration.
type Config struct {
	// HTTPAddr is the listen address, e.g. ":8080".
	HTTPAddr string
	// DataDir is the root directory for file storage (images, models, ...).
	DataDir string
	// WebDir is the built frontend directory. Empty disables static hosting.
	WebDir string
	// DatabaseURL is the PostgreSQL connection string, either TM_DATABASE_URL
	// as given or a URL assembled from the TM_DB_* variables.
	DatabaseURL string
	// AutoMigrate runs pending database migrations on startup.
	AutoMigrate bool
	// LogLevel is the minimum level that gets logged.
	LogLevel slog.Level
	// LogFormat is "text" or "json".
	LogFormat string
	// TrustedProxies are the CIDR prefixes of reverse proxies whose
	// X-Forwarded-For header is believed (TM_TRUSTED_PROXIES). Empty means
	// the header is ignored and the TCP peer is the client.
	TrustedProxies []string
	// NodeToken is the bearer token training nodes must send to register
	// and send heartbeats (TM_NODE_TOKEN). Empty: no token is required.
	NodeToken string
	// NodeHeartbeatInterval is how often training nodes send heartbeats
	// (TM_NODE_HEARTBEAT_INTERVAL); a node is offline after three missed
	// ones plus 5 seconds.
	NodeHeartbeatInterval time.Duration
	// TrainerMode is TM_TRAINER_MODE, the mode make setup chose for the
	// training node on this machine: auto, gpu, cpu or off (docker compose
	// passes it from .env). Empty: not known, e.g. outside docker compose.
	TrainerMode string
	// ReviewerTool is the path of the bundled local reviewer for Windows,
	// trainmaster-reviewer.exe (TM_REVIEWER_TOOL; the Docker image sets
	// /app/tools/trainmaster-reviewer.exe). Empty: none is bundled. Like
	// TM_WEB_DIR it is not checked here: a missing file only makes the
	// download unavailable.
	ReviewerTool string
	// TrainerPackageDir is the source directory of the training node for
	// Windows computers, laid out like the repository's trainer/ directory
	// (TM_TRAINER_PACKAGE_DIR; the Docker image sets /app/tools/trainer).
	// The platform serves its scripts/install.ps1 and a zip of the sources.
	// Empty: none is bundled. Like TM_REVIEWER_TOOL it is not checked here:
	// a missing or incomplete directory only makes the downloads
	// unavailable.
	TrainerPackageDir string
}

// Limits of the training node settings.
const (
	minNodeTokenLength           = 16
	maxNodeTokenLength           = 256
	minNodeHeartbeatInterval     = 2 * time.Second
	maxNodeHeartbeatInterval     = 5 * time.Minute
	defaultNodeHeartbeatInterval = "10s"
)

// DBParams are the parts used to build a connection URL when
// TM_DATABASE_URL is not set.
type DBParams struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// URL returns a postgres:// connection URL with every component escaped, so
// user names and passwords may contain characters such as '@', ':' or '/'.
func (p DBParams) URL() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.User, p.Password),
		Host:     net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path:     "/" + p.Name,
		RawQuery: url.Values{"sslmode": {p.SSLMode}}.Encode(),
	}
	return u.String()
}

var sslModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

// trainerModes are the modes of make setup --mode (TM_TRAINER_MODE).
var trainerModes = []string{"auto", "gpu", "cpu", "off"}

// Load reads the configuration through getenv (normally os.Getenv) and
// validates it. All problems are reported together.
func Load(getenv func(string) string) (*Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	// Credentials are used verbatim: docker compose passes the same value
	// to the postgres container, which does not trim it either.
	verbatim := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	var problems []string
	fail := func(key, format string, args ...any) {
		problems = append(problems, key+": "+fmt.Sprintf(format, args...))
	}

	cfg := &Config{
		HTTPAddr:  get("TM_HTTP_ADDR", ":8080"),
		DataDir:   get("TM_DATA_DIR", "./data"),
		WebDir:    get("TM_WEB_DIR", ""),
		LogFormat: strings.ToLower(get("TM_LOG_FORMAT", "text")),
		// Paths, so trimmed like the other paths.
		ReviewerTool:      get("TM_REVIEWER_TOOL", ""),
		TrainerPackageDir: get("TM_TRAINER_PACKAGE_DIR", ""),
	}

	if err := checkListenAddr(cfg.HTTPAddr); err != nil {
		fail("TM_HTTP_ADDR", "%v", err)
	}

	if dsn := get("TM_DATABASE_URL", ""); dsn != "" {
		cfg.DatabaseURL = dsn
	} else {
		p := DBParams{
			Host:     get("TM_DB_HOST", "localhost"),
			User:     verbatim("TM_DB_USER", "trainmaster"),
			Password: verbatim("TM_DB_PASSWORD", "trainmaster"),
			Name:     verbatim("TM_DB_NAME", "trainmaster"),
			SSLMode:  get("TM_DB_SSLMODE", "disable"),
		}
		port, err := parsePort(get("TM_DB_PORT", "5432"))
		if err != nil {
			fail("TM_DB_PORT", "%v", err)
		}
		p.Port = port
		if !slices.Contains(sslModes, p.SSLMode) {
			fail("TM_DB_SSLMODE", "unknown mode %q (one of %s)", p.SSLMode, strings.Join(sslModes, ", "))
		}
		cfg.DatabaseURL = p.URL()
	}

	autoMigrate := get("TM_DB_AUTO_MIGRATE", "true")
	if b, err := strconv.ParseBool(autoMigrate); err != nil {
		fail("TM_DB_AUTO_MIGRATE", "invalid boolean %q (use true or false)", autoMigrate)
	} else {
		cfg.AutoMigrate = b
	}

	if level, err := parseLevel(get("TM_LOG_LEVEL", "info")); err != nil {
		fail("TM_LOG_LEVEL", "%v", err)
	} else {
		cfg.LogLevel = level
	}

	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		fail("TM_LOG_FORMAT", "unknown format %q (text or json)", cfg.LogFormat)
	}

	if proxies, err := parsePrefixes(get("TM_TRUSTED_PROXIES", "")); err != nil {
		fail("TM_TRUSTED_PROXIES", "%v", err)
	} else {
		cfg.TrustedProxies = proxies
	}

	// The token is compared byte for byte with what nodes send, so it is
	// not trimmed; a value that would need trimming is rejected instead.
	if token := getenv("TM_NODE_TOKEN"); token != "" {
		if err := checkNodeToken(token); err != nil {
			fail("TM_NODE_TOKEN", "%v", err)
		} else {
			cfg.NodeToken = token
		}
	}

	interval := get("TM_NODE_HEARTBEAT_INTERVAL", defaultNodeHeartbeatInterval)
	if d, err := time.ParseDuration(interval); err != nil || d < minNodeHeartbeatInterval || d > maxNodeHeartbeatInterval {
		fail("TM_NODE_HEARTBEAT_INTERVAL", "invalid duration %q (between 2s and 5m, e.g. 10s or 1m)", interval)
	} else {
		cfg.NodeHeartbeatInterval = d
	}

	// TM_TRAINER_MODE only tunes the compute page's hints, so a bad value
	// never stops the service: like setup-compute.sh, which warns and goes
	// on, it counts as auto.
	if mode := get("TM_TRAINER_MODE", ""); mode != "" {
		if !slices.Contains(trainerModes, mode) {
			mode = "auto"
		}
		cfg.TrainerMode = mode
	}

	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// checkNodeToken validates TM_NODE_TOKEN: 16-256 printable ASCII
// characters without spaces. The error never contains the token itself,
// since configuration errors end up in logs.
func checkNodeToken(token string) error {
	if n := len(token); n < minNodeTokenLength || n > maxNodeTokenLength {
		return fmt.Errorf("must be %d to %d characters long (got %d)", minNodeTokenLength, maxNodeTokenLength, n)
	}
	for i := 0; i < len(token); i++ {
		if c := token[i]; c < 0x21 || c > 0x7e {
			return fmt.Errorf("character %d is not allowed (use printable ASCII without spaces, e.g. letters and digits)", i+1)
		}
	}
	return nil
}

func checkListenAddr(addr string) error {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if _, err := parsePort(port); err == nil {
			return nil
		}
	}
	return fmt.Errorf("invalid listen address %q (expected host:port with a numeric port, e.g. :8080)", addr)
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q (must be a number between 1 and 65535)", s)
	}
	return n, nil
}

// parsePrefixes parses a comma-separated list of CIDR prefixes or single
// IP addresses ("172.18.0.1, 10.0.0.0/8") into canonical prefixes.
func parsePrefixes(s string) ([]string, error) {
	var out []string
	for _, field := range strings.Split(s, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		p, err := netip.ParsePrefix(field)
		if err != nil {
			addr, addrErr := netip.ParseAddr(field)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("invalid address or CIDR prefix %q (e.g. 172.18.0.1 or 10.0.0.0/8)", field)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown level %q (debug, info, warn or error)", s)
}
