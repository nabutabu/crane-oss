package logging

import (
	"fmt"
	"log/slog"
)

// SlogAdapter adapts *slog.Logger to the subset of the standard library
// log.Logger API used by older code. It never exits the process: callers are
// expected to handle failures rather than rely on Fatal.
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter creates a new adapter.
func NewSlogAdapter(logger *slog.Logger) *SlogAdapter {
	return &SlogAdapter{logger: logger}
}

// Printf writes a formatted log message at info level.
func (a *SlogAdapter) Printf(format string, args ...interface{}) {
	a.logger.Info(fmt.Sprintf(format, args...))
}

// Print writes a log message at info level.
func (a *SlogAdapter) Print(args ...interface{}) {
	a.logger.Info(fmt.Sprint(args...))
}

// Println writes a log message at info level.
func (a *SlogAdapter) Println(args ...interface{}) {
	a.logger.Info(fmt.Sprint(args...))
}

// Fatal logs at error level. It intentionally does not call os.Exit so library
// code cannot terminate the process; only main may exit.
func (a *SlogAdapter) Fatal(args ...interface{}) {
	a.logger.Error(fmt.Sprint(args...))
}

// Fatalf logs at error level. It intentionally does not call os.Exit.
func (a *SlogAdapter) Fatalf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}

// SetPrefix records a prefix as a structured attribute.
func (a *SlogAdapter) SetPrefix(prefix string) {
	a.logger = a.logger.With("prefix", prefix)
}
