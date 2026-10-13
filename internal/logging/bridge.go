package logging

import (
	"log/slog"
)

// SlogAdapter adapts *slog.Logger to be used as a standard logger.
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter creates a new adapter.
func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	return &SlogAdapter{logger: logger}
}

// Printf writes a formatted log message at info level.
func (a *SlogAdapter) Printf(format string, args ...interface{}) {
	a.logger.Info(format, args...)
}

// Print writes a log message at info level.
func (a *SlogAdapter) Print(args ...interface{}) {
	a.logger.Info("", args...)
}

// Println writes a log message at info level.
func (a *SlogAdapter) Println(args ...interface{}) {
	a.logger.Info("", args...)
}

// Fatal is equivalent to Print followed by os.Exit(1).
func (a *SlogAdapter) Fatal(args ...interface{}) {
	a.logger.Error("", args...)
}

// Fatalf is equivalent to Printf followed by os.Exit(1).
func (a *SlogAdapter) Fatalf(format string, args ...interface{}) {
	a.logger.Error(format, args...)
}

// SetPrefix sets a prefix (not supported by slog).
func (a *SlogAdapter) SetPrefix(prefix string) {
	a.logger = a.logger.With("prefix", prefix)
}
