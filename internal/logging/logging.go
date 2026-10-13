package logging

import (
	"io"
	stdlog "log"
	"log/slog"
	"os"
	"strings"

	"github.com/nabutabu/crane-oss/internal/version"
)

const (
	EnvLogLevel  = "LOG_LEVEL"
	EnvLogFormat = "LOG_FORMAT"
)

// Logger holds a structured logger.
type Logger struct {
	*slog.Logger
}

// New creates a new logger for the given service.
func New(service string) *Logger {
	levelStr := strings.ToLower(strings.TrimSpace(os.Getenv(EnvLogLevel)))
	if levelStr == "" {
		levelStr = "info"
	}

	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	formatStr := strings.ToLower(strings.TrimSpace(os.Getenv(EnvLogFormat)))
	if formatStr == "" {
		formatStr = "text"
		if os.Getenv("GO_ENV") == "production" || os.Getenv("ENVIRONMENT") == "production" {
			formatStr = "json"
		}
	}

	var w io.Writer = os.Stdout
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: level,
	}

	if formatStr == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	// Redaction is applied at the handler level so every attribute, group,
	// attached field, and message is sanitized before it is written.
	logger := slog.New(NewRedactHandler(handler)).With(
		slog.String("service", service),
		slog.String("version", version.Version),
		slog.String("commit", version.Commit),
	)

	return &Logger{Logger: logger}
}

// SetDefault installs l as the process-wide slog logger and routes the
// standard library "log" package through its handler so legacy log calls are
// emitted as structured, redaction-safe records.
func (l *Logger) SetDefault() {
	slog.SetDefault(l.Logger)
	stdlog.SetOutput(slog.NewLogLogger(l.Logger.Handler(), slog.LevelInfo).Writer())
	stdlog.SetFlags(0)
	stdlog.SetPrefix("")
}

// With returns a new logger with additional attributes.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{Logger: l.Logger.With(args...)}
}
