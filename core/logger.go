// Package bifrost provides the core implementation of the Bifrost system.
package bifrost

import (
	"errors"
	"os"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// DefaultLogger implements the Logger interface with stdout/stderr printing.
// It provides a simple logging implementation that writes to standard output
// and error streams with formatted timestamps and log levels.
// It is used as the default logger if no logger is provided in the BifrostConfig.
type DefaultLogger struct {
	stderrLogger zerolog.Logger
	stdoutLogger zerolog.Logger
}

type LoggerOutputType string

const (
	LoggerOutputTypeJSON   LoggerOutputType = "json"
	LoggerOutputTypePretty LoggerOutputType = "pretty"
)

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
	zerolog.DisableSampling(true)
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	return &DefaultLogger{
		stderrLogger: zerolog.New(os.Stderr).With().Timestamp().Logger(),
		stdoutLogger: zerolog.New(os.Stdout).With().Timestamp().Logger(),
	}
}

// Debug logs a debug level message to stdout.
// Messages are only output if the logger's level is set to LogLevelDebug.
func (logger *DefaultLogger) Debug(msg string) {
	logger.stdoutLogger.Debug().Msg(msg)
}

// Info logs an info level message to stdout.
// Messages are output if the logger's level is LogLevelDebug or LogLevelInfo.
func (logger *DefaultLogger) Info(msg string) {
	logger.stdoutLogger.Info().Msg(msg)
}

// Warn logs a warning level message to stdout.
// Messages are output if the logger's level is LogLevelDebug, LogLevelInfo, or LogLevelWarn.
func (logger *DefaultLogger) Warn(msg string) {
	logger.stdoutLogger.Warn().Msg(msg)
}

// Error logs an error level message to stderr.
// Error messages are always output regardless of the logger's level.
func (logger *DefaultLogger) Error(err error) {
	if err == nil {
		logger.stderrLogger.Error().Msg("nil error")
		return
	}
	logger.stderrLogger.Error().Msg(err.Error())
}

// Fatal logs a fatal-level message to stderr.
// Fatal messages are always output regardless of the logger's level.
func (logger *DefaultLogger) Fatal(msg string, err error) {
	if err == nil {
		logger.stderrLogger.Fatal().Err(errors.New("nil error")).Msg(msg)
		return
	}
	logger.stderrLogger.Fatal().Err(err).Msg(msg)
}

// SetLevel sets the logging level for the logger.
// This determines which messages will be output based on their severity.
func (logger *DefaultLogger) SetLevel(level schemas.LogLevel) {
	zerolog.SetGlobalLevel(toZerologLevel(level))
}

// SetOutputType sets the output type for the logger.
// This determines the format of the log output.
// If the output type is unknown, it defaults to JSON
func (logger *DefaultLogger) SetOutputType(outputType LoggerOutputType) {
	switch outputType {
	case LoggerOutputTypePretty:
		logger.stdoutLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()
		logger.stderrLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
	case LoggerOutputTypeJSON:
		logger.stdoutLogger = zerolog.New(os.Stdout).With().Timestamp().Logger()
		logger.stderrLogger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	default:
		logger.stderrLogger.Warn().
			Str("outputType", string(outputType)).
			Msg("unknown logger output type; defaulting to JSON")
		logger.stdoutLogger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
}
