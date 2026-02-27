// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// LogLevel represents the severity level of a log message.
// Internally it maps to zerolog.Level for interoperability.
type LogLevel string

// LogLevel constants for different severity levels.
const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LoggerOutputType represents the output type of a logger.
type LoggerOutputType string

// LoggerOutputType constants for different output types.
const (
	LoggerOutputTypeJSON   LoggerOutputType = "json"
	LoggerOutputTypePretty LoggerOutputType = "pretty"
)

// Logger defines the interface for logging operations in the Bifrost system.
// Implementations of this interface should provide methods for logging messages
// at different severity levels.
type Logger interface {
	// Debug logs a debug-level message.
	// This is used for detailed debugging information that is typically only needed
	// during development or troubleshooting.
	Debug(msg string, args ...any)

	// Info logs an info-level message.
	// This is used for general informational messages about normal operation.
	Info(msg string, args ...any)

	// Warn logs a warning-level message.
	// This is used for potentially harmful situations that don't prevent normal operation.
	Warn(msg string, args ...any)

	// Error logs an error-level message.
	// This is used for serious problems that need attention and may prevent normal operation.
	Error(msg string, args ...any)

	// Fatal logs a fatal-level message.
	// This is used for critical situations that require immediate attention and will terminate the program.
	Fatal(msg string, args ...any)

	// SetLevel sets the log level for the logger.
	SetLevel(level LogLevel)

	// SetOutputType sets the output type for the logger.
	SetOutputType(outputType LoggerOutputType)

	// LogHTTPRequest returns a LogEventBuilder for structured HTTP access logging.
	// The level parameter controls the log severity, msg is sent when Send() is called.
	// Use the fluent builder to attach typed fields before calling Send().
	LogHTTPRequest(level LogLevel, msg string) LogEventBuilder
}

// LogEventBuilder provides a fluent interface for building structured log entries.
type LogEventBuilder interface {
	Str(key, val string) LogEventBuilder
	Int(key string, val int) LogEventBuilder
	Int64(key string, val int64) LogEventBuilder
	Send()
}

// noopLogEventBuilder is a no-op builder for loggers that don't need structured logging.
type noopLogEventBuilder struct{}

func (noopLogEventBuilder) Str(string, string) LogEventBuilder  { return noopLogEventBuilder{} }
func (noopLogEventBuilder) Int(string, int) LogEventBuilder     { return noopLogEventBuilder{} }
func (noopLogEventBuilder) Int64(string, int64) LogEventBuilder { return noopLogEventBuilder{} }
func (noopLogEventBuilder) Send()                               {}

// NoopLogEvent is a shared singleton no-op LogEventBuilder.
var NoopLogEvent LogEventBuilder = noopLogEventBuilder{}
