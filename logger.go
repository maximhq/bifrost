// Package bifrost provides the core implementation of the Bifrost system.
package bifrost

import (
	"fmt"
	"os"
	"time"

	"github.com/maximhq/bifrost/interfaces"
)

// DefaultLogger implements the Logger interface with stdout/stderr printing.
// It provides a simple logging implementation that writes to standard output
// and error streams with formatted timestamps and log levels.
// It is used as the default logger if no logger is provided in the BifrostConfig.
type DefaultLogger struct {
	level interfaces.LogLevel // Current logging level
}

// NewDefaultLogger creates a new DefaultLogger instance with the specified log level.
// The log level determines which messages will be output based on their severity.
func NewDefaultLogger(level interfaces.LogLevel) *DefaultLogger {
	return &DefaultLogger{
		level: level,
	}
}

// formatMessage formats the log message with timestamp, level, and optional error information.
// It creates a consistent log format: [BIFROST-TIMESTAMP] LEVEL: message (error: err)
func (logger *DefaultLogger) formatMessage(level interfaces.LogLevel, msg string, err error) string {
	timestamp := time.Now().Format(time.RFC3339)
	baseMsg := fmt.Sprintf("[BIFROST-%s] %s: %s", timestamp, level, msg)
	if err != nil {
		return fmt.Sprintf("%s (error: %v)", baseMsg, err)
	}
	return baseMsg
}

// Debug logs a debug level message to stdout.
// Messages are only output if the logger's level is set to LogLevelDebug.
func (logger *DefaultLogger) Debug(msg string) {
	if logger.level == interfaces.LogLevelDebug {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelDebug, msg, nil))
	}
}

// Info logs an info level message to stdout.
// Messages are output if the logger's level is LogLevelDebug or LogLevelInfo.
func (logger *DefaultLogger) Info(msg string) {
	if logger.level == interfaces.LogLevelDebug || logger.level == interfaces.LogLevelInfo {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelInfo, msg, nil))
	}
}

// Warn logs a warning level message to stdout.
// Messages are output if the logger's level is LogLevelDebug, LogLevelInfo, or LogLevelWarn.
func (logger *DefaultLogger) Warn(msg string) {
	if logger.level == interfaces.LogLevelDebug || logger.level == interfaces.LogLevelInfo || logger.level == interfaces.LogLevelWarn {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelWarn, msg, nil))
	}
}

// Error logs an error level message to stderr.
// Error messages are always output regardless of the logger's level.
func (logger *DefaultLogger) Error(err error) {
	fmt.Fprintln(os.Stderr, logger.formatMessage(interfaces.LogLevelError, "", err))
}

// SetLevel sets the logging level for the logger.
// This determines which messages will be output based on their severity.
func (logger *DefaultLogger) SetLevel(level interfaces.LogLevel) {
	logger.level = level
}
