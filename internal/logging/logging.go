package logging

import (
	"io"
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

	logger := slog.New(handler).With(
		slog.String("service", service),
		slog.String("version", version.Version),
		slog.String("commit", version.Commit),
	)

	return &Logger{Logger: logger}
}

// With returns a new logger with additional attributes.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{Logger: l.Logger.With(args...)}
}
