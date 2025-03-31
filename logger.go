package bifrost

import (
	"fmt"
	"os"
	"time"

	"github.com/maximhq/bifrost/interfaces"
)

// DefaultLogger implements the Logger interface with stdout printing
type DefaultLogger struct {
	level interfaces.LogLevel
}

// NewDefaultLogger creates a new DefaultLogger instance
func NewDefaultLogger(level interfaces.LogLevel) *DefaultLogger {
	return &DefaultLogger{
		level: level,
	}
}

// formatMessage formats the log message with timestamp and level
func (logger *DefaultLogger) formatMessage(level interfaces.LogLevel, msg string, err error) string {
	timestamp := time.Now().Format(time.RFC3339)
	baseMsg := fmt.Sprintf("[BIFROST-%s] %s: %s", timestamp, level, msg)
	if err != nil {
		return fmt.Sprintf("%s (error: %v)", baseMsg, err)
	}
	return baseMsg
}

// Debug logs a debug level message
func (logger *DefaultLogger) Debug(msg string) {
	if logger.level == interfaces.LogLevelDebug {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelDebug, msg, nil))
	}
}

// Info logs an info level message
func (logger *DefaultLogger) Info(msg string) {
	if logger.level == interfaces.LogLevelDebug || logger.level == interfaces.LogLevelInfo {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelInfo, msg, nil))
	}
}

// Warn logs a warning level message
func (logger *DefaultLogger) Warn(msg string) {
	if logger.level == interfaces.LogLevelDebug || logger.level == interfaces.LogLevelInfo || logger.level == interfaces.LogLevelWarn {
		fmt.Fprintln(os.Stdout, logger.formatMessage(interfaces.LogLevelWarn, msg, nil))
	}
}

// Error logs an error level message
func (logger *DefaultLogger) Error(err error) {
	fmt.Fprintln(os.Stderr, logger.formatMessage(interfaces.LogLevelError, "", err))
}

// SetLevel sets the logging level
func (logger *DefaultLogger) SetLevel(level interfaces.LogLevel) {
	logger.level = level
}
