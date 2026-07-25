package log

// Logger is the decoupled logging interface for domain packages.
// Implement this to redirect noci's log output to a structured logger
// (slog, zap, etc.) or to silence it entirely.
type Logger interface {
	Info(format string, a ...interface{})
	Action(format string, a ...interface{})
	Warning(format string, a ...interface{})
	Success(format string, a ...interface{})
}

// DefaultLogger delegates to the package-level log functions.
type DefaultLogger struct{}

func (d DefaultLogger) Info(format string, a ...interface{})    { Info(format, a...) }
func (d DefaultLogger) Action(format string, a ...interface{})  { Action(format, a...) }
func (d DefaultLogger) Warning(format string, a ...interface{}) { Warning(format, a...) }
func (d DefaultLogger) Success(format string, a ...interface{}) { Success(format, a...) }

// NopLogger discards all log output.
type NopLogger struct{}

func (n NopLogger) Info(string, ...interface{})    {}
func (n NopLogger) Action(string, ...interface{})  {}
func (n NopLogger) Warning(string, ...interface{}) {}
func (n NopLogger) Success(string, ...interface{}) {}
