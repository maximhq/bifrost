// Package bifrost provides the core implementation of the Bifrost system.
package bifrost

import (
	"os"
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var zerologOnce sync.Once

// DefaultLogger implements the Logger interface with stdout/stderr printing.
// It provides a simple logging implementation that writes to standard output
// and error streams with formatted timestamps and log levels.
// It is used as the default logger if no logger is provided in the BifrostConfig.
type DefaultLogger struct {
	stderrLogger zerolog.Logger
	stdoutLogger zerolog.Logger
}

// toZerologLevel converts a Bifrost log level to a Zerolog level.
func toZerologLevel(l schemas.LogLevel) zerolog.Level {
	switch l {
	case schemas.LogLevelDebug:
		return zerolog.DebugLevel
	case schemas.LogLevelInfo:
		return zerolog.InfoLevel
	case schemas.LogLevelWarn:
		return zerolog.WarnLevel
	case schemas.LogLevelError:
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// NewDefaultLogger creates a new DefaultLogger instance with the specified log level.
// The log level determines which messages will be output based on their severity.
func NewDefaultLogger(level schemas.LogLevel) *DefaultLogger {
	zerolog.SetGlobalLevel(toZerologLevel(level))
	zerologOnce.Do(func() {
		zerolog.DisableSampling(true)
		zerolog.TimeFieldFormat = time.RFC3339
		log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	})
	return &DefaultLogger{
		stderrLogger: zerolog.New(os.Stderr).With().Timestamp().Logger(),
		stdoutLogger: zerolog.New(os.Stdout).With().Timestamp().Logger(),
	}
}

// Debug logs a debug level message to stdout.
// Messages are only output if the logger's level is set to LogLevelDebug.
func (logger *DefaultLogger) Debug(msg string, args ...any) {
	logger.stdoutLogger.Debug().Msgf(msg, args...)
}

// Info logs an info level message to stdout.
// Messages are output if the logger's level is LogLevelDebug or LogLevelInfo.
func (logger *DefaultLogger) Info(msg string, args ...any) {
	logger.stdoutLogger.Info().Msgf(msg, args...)
}

// Warn logs a warning level message to stdout.
// Messages are output if the logger's level is LogLevelDebug, LogLevelInfo, or LogLevelWarn.
func (logger *DefaultLogger) Warn(msg string, args ...any) {
	logger.stdoutLogger.Warn().Msgf(msg, args...)
}

// Error logs an error level message to stderr.
// Error messages are always output regardless of the logger's level.
func (logger *DefaultLogger) Error(msg string, args ...any) {
	logger.stderrLogger.Error().Msgf(msg, args...)
}

// Fatal logs a fatal-level message to stderr.
// Fatal messages are always output regardless of the logger's level.
func (logger *DefaultLogger) Fatal(msg string, args ...any) {
	// Check if any of the args is an error and exit with non-zero code if found
	var errToPass error
	for i, arg := range args {
		if err, ok := arg.(error); ok && err != nil {
			errToPass = err
			// remove from args
			args = append(args[:i], args[i+1:]...)
		}
	}
	if errToPass != nil {
		logger.stderrLogger.Fatal().Msgf(msg, errToPass)
	} else {
		logger.stderrLogger.Fatal().Msgf(msg, args...)
	}
}

// SetLevel sets the logging level for the logger.
// This determines which messages will be output based on their severity.
func (logger *DefaultLogger) SetLevel(level schemas.LogLevel) {
	zerolog.SetGlobalLevel(toZerologLevel(level))
}

// SetOutputType sets the output type for the logger.
// This determines the format of the log output.
// If the output type is unknown, it defaults to JSON
func (logger *DefaultLogger) SetOutputType(outputType schemas.LoggerOutputType) {
	switch outputType {
	case schemas.LoggerOutputTypePretty:
		logger.stdoutLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()
		logger.stderrLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
	case schemas.LoggerOutputTypeJSON:
		logger.stdoutLogger = zerolog.New(os.Stdout).With().Timestamp().Logger()
		logger.stderrLogger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	default:
		logger.stderrLogger.Warn().
			Str("outputType", string(outputType)).
			Msg("unknown logger output type; defaulting to JSON")
		logger.stdoutLogger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
}

// NoOpLogger is a no-op implementation of schemas.Logger.
type NoOpLogger struct{}

// NewNoOpLogger creates a new NoOpLogger instance.
func NewNoOpLogger() schemas.Logger {
	return &NoOpLogger{}
}

func (l *NoOpLogger) Debug(string, ...any)                   {}
func (l *NoOpLogger) Info(string, ...any)                    {}
func (l *NoOpLogger) Warn(string, ...any)                    {}
func (l *NoOpLogger) Error(string, ...any)                   {}
func (l *NoOpLogger) Fatal(string, ...any)                   {}
func (l *NoOpLogger) SetLevel(schemas.LogLevel)              {}
func (l *NoOpLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *NoOpLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// zerologEventBuilder wraps a zerolog.Event to implement schemas.LogEventBuilder.
type zerologEventBuilder struct {
	event *zerolog.Event
	msg   string
}

func (b *zerologEventBuilder) Str(key, val string) schemas.LogEventBuilder {
	b.event = b.event.Str(key, val)
	return b
}

func (b *zerologEventBuilder) Int(key string, val int) schemas.LogEventBuilder {
	b.event = b.event.Int(key, val)
	return b
}

func (b *zerologEventBuilder) Int64(key string, val int64) schemas.LogEventBuilder {
	b.event = b.event.Int64(key, val)
	return b
}

func (b *zerologEventBuilder) Send() {
	b.event.Msg(b.msg)
}

// LogHTTPRequest returns a LogEventBuilder for structured HTTP access logging.
// We are exposing the zerolog loggers directly to allow for more flexibility in logging and also to reduce the number of allocations we do in the logger.
func (logger *DefaultLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	l := logger.stdoutLogger
	if level == schemas.LogLevelError {
		l = logger.stderrLogger
	}
	var event *zerolog.Event
	switch level {
	case schemas.LogLevelDebug:
		event = l.Debug()
	case schemas.LogLevelWarn:
		event = l.Warn()
	case schemas.LogLevelError:
		event = l.Error()
	default:
		event = l.Info()
	}
	return &zerologEventBuilder{event: event, msg: msg}
}
