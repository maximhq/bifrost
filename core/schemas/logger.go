// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// LogLevel represents the severity level of a log message.
// Alias to zerolog.Level to ensure seamless interoperability.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// Logger defines the interface for logging operations in the Bifrost system.
// Implementations of this interface should provide methods for logging messages
// at different severity levels.
type Logger interface {
	// Debug logs a debug-level message.
	// This is used for detailed debugging information that is typically only needed
	// during development or troubleshooting.
	Debug(msg string)

	// Info logs an info-level message.
	// This is used for general informational messages about normal operation.
	Info(msg string)

	// Warn logs a warning-level message.
	// This is used for potentially harmful situations that don't prevent normal operation.
	Warn(msg string)

	// Error logs an error-level message.
	// This is used for serious problems that need attention and may prevent normal operation.
	Error(err error)

	// Fatal logs a fatal-level message.
	// This is used for critical situations that require immediate attention and will terminate the program.
	Fatal(msg string, err error)
}
